package rates

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeClock is a manually advanced clock.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock { return &fakeClock{t: time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// fakeGecko is an httptest CoinGecko /simple/price endpoint.
type fakeGecko struct {
	srv      *httptest.Server
	requests atomic.Int64

	mu      sync.Mutex
	status  int
	body    string // raw response body; overrides prices when non-empty
	prices  map[string]map[string]string
	queries []map[string]string // ids, vs_currencies, precision of each request
	headers []http.Header
	gate    chan struct{} // when non-nil the handler blocks until it is closed
	arrived chan struct{} // receives one value per request (non-blocking)
}

func newFakeGecko(t *testing.T) *fakeGecko {
	f := &fakeGecko{status: http.StatusOK, prices: map[string]map[string]string{}, arrived: make(chan struct{}, 100)}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGecko) serve(w http.ResponseWriter, r *http.Request) {
	f.requests.Add(1)
	select {
	case f.arrived <- struct{}{}:
	default:
	}
	f.mu.Lock()
	gate := f.gate
	f.mu.Unlock()
	if gate != nil {
		<-gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path != "/simple/price" {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	f.queries = append(f.queries, map[string]string{
		"ids": q.Get("ids"), "vs_currencies": q.Get("vs_currencies"), "precision": q.Get("precision"),
	})
	f.headers = append(f.headers, r.Header.Clone())
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(f.status)
	if f.body != "" || f.status != http.StatusOK {
		_, _ = w.Write([]byte(f.body))
		return
	}
	// Build the JSON by hand so numbers keep their exact text.
	var sb strings.Builder
	sb.WriteString("{")
	first := true
	for _, id := range strings.Split(q.Get("ids"), ",") {
		m, ok := f.prices[id]
		if !ok {
			continue
		}
		if !first {
			sb.WriteString(",")
		}
		first = false
		sb.WriteString(`"` + id + `":{`)
		firstCur := true
		for _, cur := range strings.Split(q.Get("vs_currencies"), ",") {
			v, ok := m[cur]
			if !ok {
				continue
			}
			if !firstCur {
				sb.WriteString(",")
			}
			firstCur = false
			sb.WriteString(`"` + cur + `":` + v)
		}
		sb.WriteString("}")
	}
	sb.WriteString("}")
	_, _ = w.Write([]byte(sb.String()))
}

func (f *fakeGecko) setPrice(id, cur, raw string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.prices[id] == nil {
		f.prices[id] = map[string]string{}
	}
	f.prices[id][cur] = raw
}

func (f *fakeGecko) setResponse(status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status, f.body = status, body
}

func (f *fakeGecko) setGate(g chan struct{}) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gate = g
}

func (f *fakeGecko) lastQuery() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.queries) == 0 {
		return nil
	}
	return f.queries[len(f.queries)-1]
}

func (f *fakeGecko) lastHeader() http.Header {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.headers) == 0 {
		return nil
	}
	return f.headers[len(f.headers)-1]
}

func newTestProvider(f *fakeGecko, clk *fakeClock, mod func(*CoinGeckoConfig)) *CoinGecko {
	cfg := CoinGeckoConfig{BaseURL: f.srv.URL, Now: clk.Now}
	if mod != nil {
		mod(&cfg)
	}
	return NewCoinGecko(cfg)
}

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestCoinGeckoCachesWithinTTL(t *testing.T) {
	f := newFakeGecko(t)
	f.setPrice("bitcoin", "usd", "67187.33912345678901")
	clk := newClock()
	p := newTestProvider(f, clk, nil)
	ctx := context.Background()

	r, err := p.Rate(ctx, "bitcoin", "usd")
	require.NoError(t, err)
	assert.Equal(t, "67187.33912345678901", r.String())

	clk.Advance(59 * time.Second)
	r, err = p.Rate(ctx, "bitcoin", "USD")
	require.NoError(t, err)
	assert.True(t, r.Equal(dec("67187.33912345678901")))
	assert.EqualValues(t, 1, f.requests.Load())
	assert.Equal(t, "full", f.lastQuery()["precision"])
}

func TestCoinGeckoRefreshesAfterTTL(t *testing.T) {
	f := newFakeGecko(t)
	f.setPrice("bitcoin", "usd", "100")
	clk := newClock()
	p := newTestProvider(f, clk, func(c *CoinGeckoConfig) { c.TTL = 30 * time.Second })
	ctx := context.Background()

	_, err := p.Rate(ctx, "bitcoin", "usd")
	require.NoError(t, err)
	f.setPrice("bitcoin", "usd", "101.5")
	clk.Advance(29 * time.Second)
	r, _ := p.Rate(ctx, "bitcoin", "usd")
	assert.Equal(t, "100", r.String())
	clk.Advance(time.Second)
	r, err = p.Rate(ctx, "bitcoin", "usd")
	require.NoError(t, err)
	assert.Equal(t, "101.5", r.String())
	assert.EqualValues(t, 2, f.requests.Load())
}

func TestCoinGeckoBatchesAllKnownKeysAndCurrencies(t *testing.T) {
	f := newFakeGecko(t)
	f.setPrice("bitcoin", "usd", "60000")
	f.setPrice("bitcoin", "eur", "55000")
	f.setPrice("tether", "usd", "1.0001")
	f.setPrice("tether", "eur", "0.92")
	f.setPrice("ethereum", "usd", "3000")
	f.setPrice("ethereum", "eur", "2750")
	f.setPrice("ethereum", "cny", "21000")
	clk := newClock()
	p := newTestProvider(f, clk, func(c *CoinGeckoConfig) {
		c.RateKeys = []string{"tether", "Bitcoin", "bad,key", ""}
		c.Currencies = []string{"EUR", "xxx"}
	})
	ctx := context.Background()

	r, err := p.Rate(ctx, "bitcoin", "usd")
	require.NoError(t, err)
	assert.Equal(t, "60000", r.String())
	assert.Equal(t, map[string]string{"ids": "bitcoin,tether", "vs_currencies": "eur,usd", "precision": "full"}, f.lastQuery())

	// Everything in the batch is now cached.
	r, err = p.Rate(ctx, "tether", "EUR")
	require.NoError(t, err)
	assert.Equal(t, "0.92", r.String())
	r, err = p.Rate(ctx, "tether", "usd")
	require.NoError(t, err)
	assert.Equal(t, "1.0001", r.String())
	assert.EqualValues(t, 1, f.requests.Load())

	// A new key triggers one refresh that includes everything known so far.
	r, err = p.Rate(ctx, "ethereum", "usd")
	require.NoError(t, err)
	assert.Equal(t, "3000", r.String())
	assert.EqualValues(t, 2, f.requests.Load())
	assert.Equal(t, "bitcoin,ethereum,tether", f.lastQuery()["ids"])
	assert.Equal(t, "eur,usd", f.lastQuery()["vs_currencies"])

	// A new currency is learned too.
	r, err = p.Rate(ctx, "ethereum", "cny")
	require.NoError(t, err)
	assert.Equal(t, "21000", r.String())
	assert.EqualValues(t, 3, f.requests.Load())
	assert.Equal(t, "cny,eur,usd", f.lastQuery()["vs_currencies"])
}

func TestCoinGeckoExactDecimals(t *testing.T) {
	f := newFakeGecko(t)
	f.setPrice("bitcoin", "usd", "67187.33912345678901")
	f.setPrice("shib", "usd", "0.000001234567890123")
	f.setPrice("tiny", "usd", "1.5e-7")
	f.setPrice("big", "vnd", "1234567890123456789.123456789")
	clk := newClock()
	p := newTestProvider(f, clk, func(c *CoinGeckoConfig) {
		c.RateKeys = []string{"bitcoin", "shib", "tiny", "big"}
		c.Currencies = []string{"vnd"}
	})
	ctx := context.Background()
	for _, tc := range []struct{ key, cur, want string }{
		{"bitcoin", "usd", "67187.33912345678901"},
		{"shib", "usd", "0.000001234567890123"},
		{"tiny", "usd", "0.00000015"},
		{"big", "vnd", "1234567890123456789.123456789"},
	} {
		r, err := p.Rate(ctx, tc.key, tc.cur)
		require.NoError(t, err, tc.key)
		assert.True(t, r.Equal(dec(tc.want)), "%s: got %s want %s", tc.key, r, tc.want)
	}
	assert.EqualValues(t, 1, f.requests.Load())
}

func TestCoinGeckoStaleFallback(t *testing.T) {
	f := newFakeGecko(t)
	f.setPrice("bitcoin", "usd", "60000")
	clk := newClock()
	p := newTestProvider(f, clk, nil) // TTL 60s, MaxStale 10m, backoff 10s
	ctx := context.Background()

	_, err := p.Rate(ctx, "bitcoin", "usd")
	require.NoError(t, err)

	f.setResponse(http.StatusInternalServerError, `{"error":"boom"}`)
	clk.Advance(2 * time.Minute)
	r, err := p.Rate(ctx, "bitcoin", "usd")
	require.NoError(t, err, "stale value within MaxStale")
	assert.Equal(t, "60000", r.String())
	assert.EqualValues(t, 2, f.requests.Load())

	// Within the failure backoff upstream is not hit again.
	clk.Advance(5 * time.Second)
	r, err = p.Rate(ctx, "bitcoin", "usd")
	require.NoError(t, err)
	assert.Equal(t, "60000", r.String())
	assert.EqualValues(t, 2, f.requests.Load())

	// Exactly MaxStale old is still served.
	clk.Advance(10*time.Minute - 2*time.Minute - 5*time.Second)
	_, err = p.Rate(ctx, "bitcoin", "usd")
	require.NoError(t, err)
	assert.EqualValues(t, 3, f.requests.Load())

	// Beyond MaxStale: unavailable, with the upstream cause in the message.
	clk.Advance(11 * time.Second)
	_, err = p.Rate(ctx, "bitcoin", "usd")
	require.ErrorIs(t, err, ErrUnavailable)
	assert.Contains(t, err.Error(), "500")
	assert.EqualValues(t, 4, f.requests.Load())

	// Upstream recovers: fresh value again.
	f.setResponse(http.StatusOK, "")
	f.setPrice("bitcoin", "usd", "61000")
	clk.Advance(DefaultFailureBackoff)
	r, err = p.Rate(ctx, "bitcoin", "usd")
	require.NoError(t, err)
	assert.Equal(t, "61000", r.String())
}

func TestCoinGeckoStaleDisabled(t *testing.T) {
	f := newFakeGecko(t)
	f.setPrice("bitcoin", "usd", "60000")
	clk := newClock()
	p := newTestProvider(f, clk, func(c *CoinGeckoConfig) { c.MaxStale = -1 })
	_, err := p.Rate(context.Background(), "bitcoin", "usd")
	require.NoError(t, err)
	f.setResponse(http.StatusBadGateway, "")
	clk.Advance(61 * time.Second)
	_, err = p.Rate(context.Background(), "bitcoin", "usd")
	require.ErrorIs(t, err, ErrUnavailable)
}

func TestCoinGeckoNeverFetchedIsUnavailable(t *testing.T) {
	f := newFakeGecko(t)
	f.setResponse(http.StatusServiceUnavailable, "down")
	p := newTestProvider(f, newClock(), nil)
	_, err := p.Rate(context.Background(), "bitcoin", "usd")
	require.ErrorIs(t, err, ErrUnavailable)
	assert.Contains(t, err.Error(), "503")
}

func TestCoinGeckoErrorMapping(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"rate limited", http.StatusTooManyRequests, `{"status":{"error_code":429,"error_message":"You've exceeded the Rate Limit"}}`, "429"},
		{"server error", http.StatusInternalServerError, "oops", "500"},
		{"bad gateway", http.StatusBadGateway, "", "502"},
		{"unauthorized", http.StatusUnauthorized, `{"error":"invalid api key"}`, "401"},
		{"malformed json", http.StatusOK, `{"bitcoin":`, "malformed"},
		{"not an object", http.StatusOK, `[1,2]`, "malformed"},
		{"null", http.StatusOK, `null`, "malformed"},
		{"error object", http.StatusOK, `{"error":"coin not found"}`, "error response"},
		{"status object", http.StatusOK, `{"status":{"error_code":429}}`, "error response"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeGecko(t)
			f.setResponse(tc.status, tc.body)
			p := newTestProvider(f, newClock(), nil)
			_, err := p.Rate(context.Background(), "bitcoin", "usd")
			require.ErrorIs(t, err, ErrUnavailable)
			assert.False(t, errors.Is(err, ErrUnknownAsset))
			assert.Contains(t, err.Error(), tc.want)
		})
	}

	t.Run("transport error", func(t *testing.T) {
		f := newFakeGecko(t)
		url := f.srv.URL
		f.srv.Close()
		p := NewCoinGecko(CoinGeckoConfig{BaseURL: url, Now: newClock().Now})
		_, err := p.Rate(context.Background(), "bitcoin", "usd")
		require.ErrorIs(t, err, ErrUnavailable)
	})

	t.Run("request timeout", func(t *testing.T) {
		f := newFakeGecko(t)
		gate := make(chan struct{})
		f.setGate(gate)
		defer close(gate)
		p := newTestProvider(f, newClock(), func(c *CoinGeckoConfig) { c.RequestTimeout = 50 * time.Millisecond })
		_, err := p.Rate(context.Background(), "bitcoin", "usd")
		require.ErrorIs(t, err, ErrUnavailable)
	})
}

func TestCoinGeckoUnknownAsset(t *testing.T) {
	f := newFakeGecko(t)
	f.setPrice("bitcoin", "usd", "60000")
	f.setPrice("dead", "usd", "0")
	f.setPrice("neg", "usd", "-1")
	f.setPrice("nul", "usd", "null")
	f.setPrice("str", "usd", `"12.5"`)
	clk := newClock()
	p := newTestProvider(f, clk, func(c *CoinGeckoConfig) { c.RateKeys = []string{"bitcoin", "dead", "neg", "nul", "str"} })
	ctx := context.Background()

	for _, key := range []string{"no-such-coin", "dead", "neg", "nul", "str"} {
		_, err := p.Rate(ctx, key, "usd")
		require.ErrorIs(t, err, ErrUnknownAsset, key)
		assert.False(t, errors.Is(err, ErrUnavailable), key)
	}
	// "no-such-coin" was learned and fetched once; the rest came from that
	// same batch or the negative cache.
	assert.EqualValues(t, 1, f.requests.Load())

	// Known asset in the same batch is fine.
	r, err := p.Rate(ctx, "bitcoin", "usd")
	require.NoError(t, err)
	assert.Equal(t, "60000", r.String())

	// Negative entries expire with the TTL and are re-asked.
	clk.Advance(DefaultTTL)
	f.setPrice("dead", "usd", "0.5")
	r, err = p.Rate(ctx, "dead", "usd")
	require.NoError(t, err)
	assert.Equal(t, "0.5", r.String())
	assert.EqualValues(t, 2, f.requests.Load())
}

func TestCoinGeckoInputValidation(t *testing.T) {
	f := newFakeGecko(t)
	f.setPrice("bitcoin", "usd", "60000")
	p := newTestProvider(f, newClock(), nil)
	ctx := context.Background()

	for _, cur := range []string{"", "xxx", "usdt", "btc", "us d"} {
		_, err := p.Rate(ctx, "bitcoin", cur)
		require.ErrorIs(t, err, ErrUnsupportedCurrency, cur)
	}
	for _, key := range []string{"", "  ", "a,b", "bit coin", "a&ids=b", strings.Repeat("a", 129)} {
		_, err := p.Rate(ctx, key, "usd")
		require.ErrorIs(t, err, ErrUnknownAsset, key)
	}
	assert.EqualValues(t, 0, f.requests.Load())

	for _, cur := range []string{"USD", " usd ", "Usd"} {
		r, err := p.Rate(ctx, " Bitcoin ", cur)
		require.NoError(t, err, cur)
		assert.Equal(t, "60000", r.String())
	}
	assert.EqualValues(t, 1, f.requests.Load())
}

func TestCoinGeckoAPIKeyHeaders(t *testing.T) {
	t.Run("anonymous", func(t *testing.T) {
		f := newFakeGecko(t)
		f.setPrice("bitcoin", "usd", "1")
		p := newTestProvider(f, newClock(), nil)
		_, err := p.Rate(context.Background(), "bitcoin", "usd")
		require.NoError(t, err)
		h := f.lastHeader()
		assert.Empty(t, h.Get(DemoAPIKeyHeader))
		assert.Empty(t, h.Get(ProAPIKeyHeader))
		assert.Equal(t, "application/json", h.Get("Accept"))
	})
	t.Run("demo", func(t *testing.T) {
		f := newFakeGecko(t)
		f.setPrice("bitcoin", "usd", "1")
		p := newTestProvider(f, newClock(), func(c *CoinGeckoConfig) { c.APIKey = "CG-demo" })
		_, err := p.Rate(context.Background(), "bitcoin", "usd")
		require.NoError(t, err)
		assert.Equal(t, "CG-demo", f.lastHeader().Get("x-cg-demo-api-key"))
		assert.Empty(t, f.lastHeader().Get("x-cg-pro-api-key"))
	})
	t.Run("pro", func(t *testing.T) {
		f := newFakeGecko(t)
		f.setPrice("bitcoin", "usd", "1")
		p := newTestProvider(f, newClock(), func(c *CoinGeckoConfig) { c.APIKey = "CG-pro"; c.Pro = true })
		_, err := p.Rate(context.Background(), "bitcoin", "usd")
		require.NoError(t, err)
		assert.Equal(t, "CG-pro", f.lastHeader().Get("x-cg-pro-api-key"))
		assert.Empty(t, f.lastHeader().Get("x-cg-demo-api-key"))
	})
	t.Run("base url defaults", func(t *testing.T) {
		assert.Equal(t, DefaultBaseURL, NewCoinGecko(CoinGeckoConfig{}).baseURL)
		assert.Equal(t, DefaultProBaseURL, NewCoinGecko(CoinGeckoConfig{Pro: true}).baseURL)
		assert.Equal(t, "https://example.test/api/v3", NewCoinGecko(CoinGeckoConfig{Pro: true, BaseURL: "https://example.test/api/v3/"}).baseURL)
	})
}

func TestCoinGeckoConcurrentColdCacheSingleRequest(t *testing.T) {
	f := newFakeGecko(t)
	f.setPrice("bitcoin", "usd", "60000")
	f.setPrice("bitcoin", "eur", "55000")
	gate := make(chan struct{})
	f.setGate(gate)
	p := newTestProvider(f, newClock(), func(c *CoinGeckoConfig) { c.Currencies = []string{"eur"} })

	const n = 50
	var wg sync.WaitGroup
	errs := make(chan error, n)
	start := make(chan struct{})
	for i := range n {
		wg.Go(func() {
			<-start
			cur := "usd"
			want := "60000"
			if i%2 == 1 {
				cur, want = "EUR", "55000"
			}
			r, err := p.Rate(context.Background(), "bitcoin", cur)
			if err == nil && r.String() != want {
				err = errors.New("wrong rate " + r.String())
			}
			errs <- err
		})
	}
	close(start)
	<-f.arrived                       // the single refresh reached the server
	time.Sleep(20 * time.Millisecond) // let the other goroutines join it
	close(gate)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	assert.EqualValues(t, 1, f.requests.Load())
}

func TestCoinGeckoWaiterForUncoveredKeyTriggersSecondRefresh(t *testing.T) {
	f := newFakeGecko(t)
	f.setPrice("bitcoin", "usd", "60000")
	f.setPrice("ethereum", "usd", "3000")
	gate := make(chan struct{})
	f.setGate(gate)
	p := newTestProvider(f, newClock(), nil)

	btc := make(chan error, 1)
	go func() {
		_, err := p.Rate(context.Background(), "bitcoin", "usd")
		btc <- err
	}()
	<-f.arrived // first refresh (ids=bitcoin) is in flight

	eth := make(chan decimal.Decimal, 1)
	ethErr := make(chan error, 1)
	go func() {
		r, err := p.Rate(context.Background(), "ethereum", "usd")
		eth <- r
		ethErr <- err
	}()
	time.Sleep(20 * time.Millisecond)
	f.setGate(nil)
	close(gate)

	require.NoError(t, <-btc)
	require.NoError(t, <-ethErr)
	assert.Equal(t, "3000", (<-eth).String())
	assert.EqualValues(t, 2, f.requests.Load())
	assert.Equal(t, "bitcoin,ethereum", f.lastQuery()["ids"])
}

func TestCoinGeckoContextCancellation(t *testing.T) {
	f := newFakeGecko(t)
	f.setPrice("bitcoin", "usd", "60000")
	gate := make(chan struct{})
	f.setGate(gate)
	p := newTestProvider(f, newClock(), nil)

	// Already cancelled: no request at all.
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := p.Rate(cancelled, "bitcoin", "usd")
	require.ErrorIs(t, err, context.Canceled)
	assert.EqualValues(t, 0, f.requests.Load())

	// Cancelled while waiting: returns promptly, the refresh keeps going.
	ctx, cancel2 := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel2()
	begin := time.Now()
	_, err = p.Rate(ctx, "bitcoin", "usd")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(begin), 2*time.Second)

	f.setGate(nil)
	close(gate)
	// The detached refresh completes and fills the cache for later callers.
	require.Eventually(t, func() bool {
		r, err := p.Rate(context.Background(), "bitcoin", "usd")
		return err == nil && r.String() == "60000"
	}, 2*time.Second, 10*time.Millisecond)
	assert.EqualValues(t, 1, f.requests.Load())
}

func TestCoinGeckoPrefetch(t *testing.T) {
	f := newFakeGecko(t)
	f.setPrice("bitcoin", "usd", "60000")
	f.setPrice("tether", "usd", "1")
	clk := newClock()
	ctx := context.Background()

	empty := newTestProvider(f, clk, nil)
	require.NoError(t, empty.Prefetch(ctx))
	assert.EqualValues(t, 0, f.requests.Load())

	p := newTestProvider(f, clk, func(c *CoinGeckoConfig) { c.RateKeys = []string{"bitcoin", "tether"} })
	require.NoError(t, p.Prefetch(ctx))
	assert.EqualValues(t, 1, f.requests.Load())
	r, err := p.Rate(ctx, "tether", "usd")
	require.NoError(t, err)
	assert.Equal(t, "1", r.String())
	assert.EqualValues(t, 1, f.requests.Load())

	// Prefetch ignores the TTL.
	require.NoError(t, p.Prefetch(ctx))
	assert.EqualValues(t, 2, f.requests.Load())

	// Failing upstream: ErrUnavailable, and Prefetch ignores the backoff.
	f.setResponse(http.StatusInternalServerError, "")
	require.ErrorIs(t, p.Prefetch(ctx), ErrUnavailable)
	require.ErrorIs(t, p.Prefetch(ctx), ErrUnavailable)
	assert.EqualValues(t, 4, f.requests.Load())
	// Cached values are still served (stale) to Rate.
	r, err = p.Rate(ctx, "bitcoin", "usd")
	require.NoError(t, err)
	assert.Equal(t, "60000", r.String())
}

func TestCoinGeckoRealHTTPClientDefaults(t *testing.T) {
	p := NewCoinGecko(CoinGeckoConfig{})
	assert.Equal(t, DefaultTTL, p.ttl)
	assert.Equal(t, DefaultMaxStale, p.maxStale)
	assert.Equal(t, DefaultFailureBackoff, p.backoff)
	assert.Equal(t, DefaultRequestTimeout, p.timeout)
	assert.NotNil(t, p.client)
	assert.Equal(t, DefaultRequestTimeout, p.client.Timeout)
	assert.Contains(t, p.currencies, "usd")
}
