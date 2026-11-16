package api

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cryptopay/internal/auth"
	"cryptopay/internal/config"
	"cryptopay/internal/invoice"
	"cryptopay/internal/store"
)

// Tests in this file need no database.

func TestMaskURL(t *testing.T) {
	cases := map[string]string{
		"https://eth-mainnet.g.alchemy.com/v2/abcdef":        "https://eth-mainnet.g.alchemy.com/…",
		"https://api.trongrid.io?apikey=xyz":                 "https://api.trongrid.io/…",
		"https://api.trongrid.io/?apikey=xyz":                "https://api.trongrid.io/…",
		"https://rpc.example.org":                            "https://rpc.example.org",
		"https://rpc.example.org/":                           "https://rpc.example.org",
		"https://user:secret@btc.example:8332":               "https://btc.example:8332",
		"https://mainnet.helius-rpc.com/?api-key=k":          "https://mainnet.helius-rpc.com/…",
		"https://x.quiknode.pro/token123/":                   "https://x.quiknode.pro/…",
		"http://127.0.0.1:8545#frag":                         "http://127.0.0.1:8545/…",
		"not a url":                                          "…",
		"https://sepolia.infura.io/v3/0123456789abcdef01234": "https://sepolia.infura.io/…",
	}
	for in, want := range cases {
		assert.Equal(t, want, maskURL(in), in)
	}
}

func TestClientIP(t *testing.T) {
	mk := func(cfg config.Config) *API {
		return &API{cfg: cfg}
	}
	req := func(remote string, hdr map[string]string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = remote
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		return r
	}
	untrusted := mk(config.Config{})
	assert.Equal(t, "203.0.113.5", untrusted.clientIP(req("203.0.113.5:1234", map[string]string{"X-Forwarded-For": "1.1.1.1"})))
	assert.Equal(t, "::1", untrusted.clientIP(req("[::1]:1234", nil)))
	assert.Equal(t, "10.0.0.1", untrusted.clientIP(req("[::ffff:10.0.0.1]:1234", nil)))

	all := mk(config.Config{TrustProxyHeaders: true})
	assert.Equal(t, "1.1.1.1", all.clientIP(req("10.0.0.2:1", map[string]string{"X-Forwarded-For": "1.1.1.1, 10.0.0.3"})))
	assert.Equal(t, "2.2.2.2", all.clientIP(req("10.0.0.2:1", map[string]string{"X-Real-IP": "2.2.2.2"})))
	assert.Equal(t, "10.0.0.2", all.clientIP(req("10.0.0.2:1", map[string]string{"X-Forwarded-For": "garbage"})))

	listed := mk(config.Config{TrustProxyHeaders: true, TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}})
	// The right-most untrusted hop wins: a client cannot spoof by prepending.
	assert.Equal(t, "8.8.8.8", listed.clientIP(req("10.0.0.2:1", map[string]string{"X-Forwarded-For": "6.6.6.6, 8.8.8.8, 10.1.1.1"})))
	// A peer outside the list cannot set the header at all.
	assert.Equal(t, "9.9.9.9", listed.clientIP(req("9.9.9.9:1", map[string]string{"X-Forwarded-For": "6.6.6.6"})))
}

func TestKeyedLimiter(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	l := newKeyedLimiter(Limit{Rate: 1, Burst: 2}, clock)
	ok, _ := l.allow("a")
	assert.True(t, ok)
	ok, _ = l.allow("a")
	assert.True(t, ok)
	ok, wait := l.allow("a")
	assert.False(t, ok)
	assert.Equal(t, time.Second, wait)
	ok, _ = l.allow("b")
	assert.True(t, ok, "keys are independent")
	now = now.Add(time.Second)
	ok, _ = l.allow("a")
	assert.True(t, ok, "refilled")

	// Idle buckets are evicted.
	now = now.Add(limiterIdleTTL + 2*time.Minute)
	l.allow("c")
	assert.Equal(t, 1, l.size())
}

func TestFailureThrottle(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	f := newFailureThrottle(2, time.Minute, func() time.Time { return now })
	blocked, _ := f.blocked("k")
	assert.False(t, blocked)
	f.fail("k")
	f.fail("k")
	blocked, wait := f.blocked("k")
	assert.True(t, blocked)
	assert.Equal(t, time.Minute, wait)
	f.reset("k")
	blocked, _ = f.blocked("k")
	assert.False(t, blocked)
	f.fail("k")
	f.fail("k")
	now = now.Add(time.Minute)
	blocked, _ = f.blocked("k")
	assert.False(t, blocked, "window over")
}

func TestDecodeJSON(t *testing.T) {
	type body struct {
		Name  string   `json:"name"`
		Count *int     `json:"count"`
		Tags  []string `json:"tags"`
	}
	decode := func(ct, s string) (body, error) {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(s))
		if ct != "" {
			r.Header.Set("Content-Type", ct)
		}
		var b body
		err := decodeJSON(r, &b)
		return b, err
	}
	b, err := decode("application/json; charset=utf-8", `{"name":"x","count":2}`)
	require.NoError(t, err)
	assert.Equal(t, "x", b.Name)
	_, err = decode("", "")
	require.NoError(t, err, "empty body is {}")
	_, err = decode("", "  \n ")
	require.NoError(t, err)

	fieldOf := func(err error) map[string]string {
		var e *Error
		require.True(t, errors.As(err, &e), "%v", err)
		assert.Equal(t, http.StatusBadRequest, e.Status)
		return e.Fields
	}
	_, err = decode("application/json", `{"name":1}`)
	assert.Equal(t, map[string]string{"name": "must be a string"}, fieldOf(err))
	_, err = decode("application/json", `{"count":"2"}`)
	assert.Equal(t, map[string]string{"count": "must be an integer"}, fieldOf(err))
	_, err = decode("application/json", `{"tags":"x"}`)
	assert.Equal(t, map[string]string{"tags": "must be an array"}, fieldOf(err))
	_, err = decode("application/json", `{"nam":"x"}`)
	assert.Equal(t, map[string]string{"nam": "unknown field"}, fieldOf(err))
	for _, s := range []string{`{"name":`, `{} {}`, `[]`, `"x"`, `{"name":"x"}x`} {
		_, err = decode("application/json", s)
		assert.Empty(t, fieldOf(err), s)
	}
	_, err = decode("text/plain", `{}`)
	fieldOf(err)
	_, err = decode("application/vnd.api+json", `{}`)
	require.NoError(t, err)

	// optional distinguishes absent / null / value.
	var o struct {
		W optional[string] `json:"w"`
	}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"w":null}`))
	r.Header.Set("Content-Type", "application/json")
	require.NoError(t, decodeJSON(r, &o))
	assert.True(t, o.W.Set && o.W.Null)
	o.W = optional[string]{}
	r = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`))
	r.Header.Set("Content-Type", "application/json")
	require.NoError(t, decodeJSON(r, &o))
	assert.False(t, o.W.Set)
}

func TestToError(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{&invoice.ValidationError{Fields: map[string]string{"amount": "x"}}, 400, CodeInvalidRequest},
		{invoice.ErrNotFound, 404, CodeNotFound},
		{invoice.ErrOrderConflict, 409, CodeConflict},
		{invoice.ErrNotOpen, 409, CodeConflict},
		{invoice.ErrFundsDetected, 409, CodeConflict},
		{invoice.ErrConflict, 409, CodeConflict},
		{invoice.ErrAssetUnavailable, 422, CodeUnprocessable},
		{invoice.ErrRateUnavailable, 422, CodeUnprocessable},
		{invoice.ErrNoMatchingQuote, 422, CodeUnprocessable},
		{invoice.ErrNoUniqueAmount, 503, CodeUnavailable},
		{store.ErrNotFound, 404, CodeNotFound},
		{store.ErrInUse, 409, CodeConflict},
		{errors.New("boom"), 500, CodeInternal},
	}
	for _, tc := range cases {
		e := toError(tc.err)
		assert.Equal(t, tc.status, e.Status, "%v", tc.err)
		assert.Equal(t, tc.code, e.Code, "%v", tc.err)
		assert.NotContains(t, e.Message, "invoice:")
	}
	assert.Equal(t, map[string]string{"amount": "x"}, toError(&invoice.ValidationError{Fields: map[string]string{"amount": "x"}}).Fields)
	assert.Equal(t, "internal server error", toError(errors.New("pq: secret detail")).Message, "internals are not leaked")
}

func TestRecovererAndAccessLog(t *testing.T) {
	var buf bytes.Buffer
	a := &API{log: slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})), now: time.Now, timeout: time.Second}
	h := a.requestInfo(a.accessLog(a.recoverer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("kaboom")
	}))))
	r := httptest.NewRequest(http.MethodPost, "/api/checkout/11111111-1111-4111-8111-111111111111?token=secret", strings.NewReader(`{"password":"pw"}`))
	r.Header.Set("Authorization", "Bearer sk_live_secret")
	r.AddCookie(&http.Cookie{Name: SessionCookie, Value: "cookie-secret"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.JSONEq(t, `{"error":{"code":"internal","message":"internal server error"}}`, rec.Body.String())
	logs := buf.String()
	assert.Contains(t, logs, "kaboom")
	assert.Contains(t, logs, `"status":500`)
	for _, secret := range []string{"sk_live_secret", "cookie-secret", "token=secret", `\"pw\"`, "11111111-1111-4111-8111-111111111111"} {
		assert.NotContains(t, logs, secret)
	}
}

func TestSmallValidators(t *testing.T) {
	for in, want := range map[string]string{
		"https://pay.example.com":       "https://pay.example.com",
		"https://pay.example.com/":      "https://pay.example.com",
		"http://localhost:3000":         "http://localhost:3000",
		" https://Pay.Example.com:8443": "https://Pay.Example.com:8443",
	} {
		got, ok := httpOrigin(in)
		assert.True(t, ok, in)
		assert.Equal(t, want, got)
	}
	for _, in := range []string{"", "pay.example.com", "https://pay.example.com/pay", "https://a@b.example", "ftp://x.example", "https://x.example?q", "https://x.example#f"} {
		_, ok := httpOrigin(in)
		assert.False(t, ok, in)
	}
	for _, in := range []string{"https://rpc.example/v2/k", "http://10.0.0.1:8545"} {
		_, msg := checkRPCURL(in)
		assert.Empty(t, msg, in)
	}
	for _, in := range []string{"", "wss://rpc.example", "rpc.example", "https://", "https://a b.example"} {
		_, msg := checkRPCURL(in)
		assert.NotEmpty(t, msg, in)
	}
	assert.Equal(t, "must be at least 8 characters", passwordMessage(auth.ValidatePassword("short")))
	assert.Equal(t, "must be at most 72 bytes", passwordMessage(auth.ValidatePassword(strings.Repeat("x", 80))))
}
