package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"cryptopay/internal/auth"
	"cryptopay/internal/config"
	"cryptopay/internal/domain"
	"cryptopay/internal/invoice"
	"cryptopay/internal/invoice/invoicetest"
	"cryptopay/internal/mailer"
	"cryptopay/internal/notify"
	"cryptopay/internal/store"
	"cryptopay/internal/store/storetest"
)

// invoiceParams are the parameters of Env.Create mods.
type invoiceParams = invoice.CreateParams

// fakeRegistry records Invalidate calls and answers Probe from a script.
type fakeRegistry struct {
	mu          sync.Mutex
	invalidated []string
	probes      []string // override URLs probed ("" = configured)
	head        uint64
	err         error
}

func (f *fakeRegistry) Probe(_ context.Context, c domain.Chain, override string) (uint64, time.Duration, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.probes = append(f.probes, override)
	if f.err != nil {
		return 0, 12 * time.Millisecond, f.err
	}
	return f.head, 42 * time.Millisecond, nil
}

func (f *fakeRegistry) Invalidate(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.invalidated = append(f.invalidated, id)
}

func (f *fakeRegistry) Invalidated() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.invalidated...)
}

// harness is an invoicetest.Env (seeded store, fixed clock, static rates,
// wallets on ethereum/polygon/bitcoin/tron/solana, one merchant) behind the
// real router.
type harness struct {
	*invoicetest.Env
	t          *testing.T
	api        *API
	handler    http.Handler
	reg        *fakeRegistry
	kicks      atomic.Int32
	emailKicks atomic.Int32
	changed    chan string
	MerchKey   string // plaintext API key of Env.Merchant
	// Mail records the test e-mails sent through Deps.Mailer (withEmail).
	Mail *mailer.Recorder
}

type harnessOpt func(*Deps)

func withLimits(l Limits) harnessOpt               { return func(d *Deps) { d.Limits = l } }
func withConfig(f func(*config.Config)) harnessOpt { return func(d *Deps) { f(&d.Config) } }

// withEmail configures e-mail: SMTP settings, the notifier (enqueues into
// the outbox) and a recording sender for the test e-mail.
func withEmail() harnessOpt {
	return func(d *Deps) {
		d.Config.SMTP = config.SMTPConfig{Host: "smtp.test.example", Port: 587, Username: "mailer", Password: "smtp-secret-pw",
			From: "CryptoPay <no-reply@cryptopay.test>", TLS: config.SMTPStartTLS}
		d.Notifier = notify.New(d.Store, true, d.Logger, d.Now)
		d.Mailer = &mailer.Recorder{}
	}
}

// generousLimits keep rate limiting out of the way of functional tests (the
// harness clock is fixed, so buckets never refill).
var generousLimits = Limits{
	MerchantKey:   Limit{Rate: 1000, Burst: 10_000},
	CheckoutGet:   Limit{Rate: 1000, Burst: 10_000},
	CheckoutQuote: Limit{Rate: 1000, Burst: 10_000},
	LoginIP:       Limit{Rate: 1000, Burst: 10_000},
	LoginFailures: 5,
	LoginWindow:   15 * time.Minute,
	EmailTest:     Limit{Rate: 1000, Burst: 10_000},
}

func newHarness(t *testing.T, opts ...harnessOpt) *harness {
	t.Helper()
	env := invoicetest.New(t)
	h := &harness{Env: env, t: t, reg: &fakeRegistry{head: 123456}, changed: make(chan string, 100)}
	d := Deps{
		Store: env.Store, Invoices: env.Invoices, Registry: h.reg, Rates: env.Rates,
		Config: config.Config{Env: config.EnvDevelopment},
		Logger: env.Log, Now: env.Clock.Now, Hasher: auth.Hasher{Cost: bcrypt.MinCost},
		Limits: generousLimits,
		Hooks: Hooks{
			WebhookKick:  func() { h.kicks.Add(1) },
			ChainChanged: func(id string) { h.changed <- id },
			EmailKick:    func() { h.emailKicks.Add(1) },
		},
	}
	for _, o := range opts {
		o(&d)
	}
	if rec, ok := d.Mailer.(*mailer.Recorder); ok {
		h.Mail = rec
	}
	h.api = newAPI(d)
	h.handler = h.api.Routes()
	_, h.MerchKey = storetest.APIKey(t, env.Store, env.Merchant.ID)
	return h
}

// response is a recorded HTTP response.
type response struct {
	t      *testing.T
	Code   int
	Header http.Header
	Body   []byte
}

func (r *response) decode(v any) {
	r.t.Helper()
	require.NoError(r.t, json.Unmarshal(r.Body, v), "body: %s", r.Body)
}

// object decodes the body as a JSON object.
func (r *response) object() map[string]any {
	r.t.Helper()
	var m map[string]any
	r.decode(&m)
	return m
}

// status asserts the status code.
func (r *response) status(code int) *response {
	r.t.Helper()
	require.Equal(r.t, code, r.Code, "body: %s", r.Body)
	return r
}

// apiError asserts the error envelope (exactly {error:{code,message[,fields]}})
// with the given status and code and returns it.
func (r *response) apiError(status int, code string) envelopeBody {
	r.t.Helper()
	require.Equal(r.t, status, r.Code, "body: %s", r.Body)
	require.Contains(r.t, r.Header.Get("Content-Type"), "application/json")
	var raw map[string]map[string]any
	r.decode(&raw)
	require.Len(r.t, raw, 1, "envelope has exactly one key: %s", r.Body)
	inner, ok := raw["error"]
	require.True(r.t, ok, "envelope key is error: %s", r.Body)
	for k := range inner {
		require.Contains(r.t, []string{"code", "message", "fields"}, k, "unexpected key in error: %s", r.Body)
	}
	var env envelope
	r.decode(&env)
	require.Equal(r.t, code, env.Error.Code, "body: %s", r.Body)
	require.NotEmpty(r.t, env.Error.Message)
	return env.Error
}

// reqOpt adjusts a request.
type reqOpt func(*http.Request)

func bearer(key string) reqOpt {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+key) }
}
func sessionCookie(token string) reqOpt {
	return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: SessionCookie, Value: token}) }
}
func csrfHeader() reqOpt { return func(r *http.Request) { r.Header.Set(CSRFHeader, CSRFValue) } }
func fromIP(ip string) reqOpt {
	return func(r *http.Request) { r.RemoteAddr = ip + ":40000" }
}
func header(k, v string) reqOpt { return func(r *http.Request) { r.Header.Set(k, v) } }

// rawBody marks a string body as already encoded.
type rawBody struct {
	contentType string
	data        string
}

func (h *harness) do(method, path string, body any, opts ...reqOpt) *response {
	h.t.Helper()
	var rd *bytes.Reader
	ct := ""
	switch b := body.(type) {
	case nil:
		rd = bytes.NewReader(nil)
	case rawBody:
		rd, ct = bytes.NewReader([]byte(b.data)), b.contentType
	default:
		raw, err := json.Marshal(b)
		require.NoError(h.t, err)
		rd, ct = bytes.NewReader(raw), "application/json"
	}
	req := httptest.NewRequest(method, path, rd)
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	for _, o := range opts {
		o(req)
	}
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return &response{t: h.t, Code: rec.Code, Header: rec.Result().Header, Body: rec.Body.Bytes()}
}

// merchant calls the merchant API with the harness merchant's key.
func (h *harness) merchant(method, path string, body any, opts ...reqOpt) *response {
	h.t.Helper()
	return h.do(method, path, body, append([]reqOpt{bearer(h.MerchKey)}, opts...)...)
}

// adminClient calls the admin API as one user (session cookie + CSRF header).
type adminClient struct {
	h     *harness
	User  domain.AdminUser
	Token string
	Sess  domain.AdminSession
}

func (h *harness) newAdmin(role domain.Role) *adminClient {
	h.t.Helper()
	u := storetest.AdminUser(h.t, h.Store, role)
	sess, tok := storetest.Session(h.t, h.Store, u.ID)
	return &adminClient{h: h, User: u, Token: tok, Sess: sess}
}

func (c *adminClient) do(method, path string, body any, opts ...reqOpt) *response {
	c.h.t.Helper()
	all := append([]reqOpt{sessionCookie(c.Token), csrfHeader()}, opts...)
	return c.h.do(method, "/api/admin"+path, body, all...)
}

func (c *adminClient) get(path string) *response {
	c.h.t.Helper()
	return c.do(http.MethodGet, path, nil)
}
func (c *adminClient) post(path string, body any) *response {
	c.h.t.Helper()
	return c.do(http.MethodPost, path, body)
}
func (c *adminClient) patch(path string, body any) *response {
	c.h.t.Helper()
	return c.do(http.MethodPatch, path, body)
}
func (c *adminClient) put(path string, body any) *response {
	c.h.t.Helper()
	return c.do(http.MethodPut, path, body)
}
func (c *adminClient) del(path string) *response {
	c.h.t.Helper()
	return c.do(http.MethodDelete, path, nil)
}

// auditLogs returns the audit entries with the given action, newest first.
func (h *harness) auditLogs(action string) []domain.AuditLog {
	h.t.Helper()
	p, err := h.Store.ListAuditLogs(h.Ctx, store.AuditFilter{Action: action, PageSize: store.MaxPageSize})
	require.NoError(h.t, err)
	return p.Items
}

// allAuditText concatenates every audit entry (for secret scans).
func (h *harness) allAuditText() string {
	h.t.Helper()
	var b strings.Builder
	for page := 1; ; page++ {
		p, err := h.Store.ListAuditLogs(h.Ctx, store.AuditFilter{Page: page, PageSize: store.MaxPageSize})
		require.NoError(h.t, err)
		for _, l := range p.Items {
			raw, _ := json.Marshal(l)
			b.Write(raw)
			b.WriteByte('\n')
		}
		if page*store.MaxPageSize >= p.Total {
			break
		}
	}
	return b.String()
}

// otherMerchant creates a second merchant with its own API key.
func (h *harness) otherMerchant() (domain.Merchant, string) {
	h.t.Helper()
	m := storetest.Merchant(h.t, h.Store)
	_, key := storetest.APIKey(h.t, h.Store, m.ID)
	return m, key
}

func mustUUID(t *testing.T, s any) uuid.UUID {
	t.Helper()
	str, ok := s.(string)
	require.True(t, ok, "not a string: %v", s)
	id, err := uuid.Parse(str)
	require.NoError(t, err)
	return id
}

var errProbe = errors.New("dial tcp: lookup https://rpc.example/v2/SECRETKEY: no such host")

// ---- sign-in and e-mail helpers ----

// login posts e-mail and password (CSRF header set).
func (h *harness) login(email, password string, opts ...reqOpt) *response {
	h.t.Helper()
	return h.do(http.MethodPost, "/api/admin/auth/login", map[string]any{"email": email, "password": password},
		append([]reqOpt{csrfHeader()}, opts...)...)
}

// emails returns the outbox messages of a kind, newest first.
func (h *harness) emails(kind string) []domain.EmailMessage {
	h.t.Helper()
	p, err := h.Store.ListEmails(h.Ctx, store.EmailFilter{Kind: kind, PageSize: store.MaxPageSize})
	require.NoError(h.t, err)
	return p.Items
}

// allEmailText concatenates every outbox message (for secret scans).
func (h *harness) allEmailText() string {
	h.t.Helper()
	p, err := h.Store.ListEmails(h.Ctx, store.EmailFilter{PageSize: store.MaxPageSize})
	require.NoError(h.t, err)
	var b strings.Builder
	for _, e := range p.Items {
		b.WriteString(e.Subject + "\n" + e.BodyText + "\n" + e.BodyHTML + "\n")
	}
	return b.String()
}
