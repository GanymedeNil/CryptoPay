// Package api is the HTTP layer of the gateway (docs/api.md): the merchant
// API (/api/v1, API key), the public checkout API (/api/checkout) and the
// admin API (/api/admin, cookie session), plus /healthz and /readyz.
//
// Every response is JSON. Errors use the envelope of docs/api.md
// `{ "error": { "code", "message", "fields"? } }` — also for unknown routes
// (404), wrong methods (405), panics (500) and rate limiting (429).
//
// Request bodies are limited to 1 MiB, must be JSON objects
// (Content-Type: application/json) and are decoded strictly: unknown fields
// are rejected with 400 invalid_request.
//
// Security notes:
//   - The access log records method, route, status, size, duration, client
//     IP and request id. It never records headers (Authorization, cookies),
//     query strings or bodies; checkout paths are logged as their route
//     pattern because the invoice id in them is a bearer secret.
//   - Admin sessions are random tokens stored as SHA-256 hashes; the cookie
//     is HttpOnly, SameSite=Lax and Secure in production. Every non-GET admin
//     request must carry `X-Requested-With: cryptopay` (CSRF guard).
//   - Every admin mutation writes an audit log entry whose detail never
//     contains secrets, API keys, passwords or full RPC URLs.
//   - Security-relevant account changes are e-mailed to the affected admin
//     (package notify).
package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"cryptopay/internal/auth"
	"cryptopay/internal/config"
	"cryptopay/internal/domain"
	"cryptopay/internal/invoice"
	"cryptopay/internal/mailer"
	"cryptopay/internal/notify"
	"cryptopay/internal/rates"
	"cryptopay/internal/store"
)

// MaxBodyBytes is the maximum accepted request body size.
const MaxBodyBytes = 1 << 20

// DefaultRequestTimeout bounds the handling of one request.
const DefaultRequestTimeout = 30 * time.Second

// ChainRegistry is what the admin API needs from the chain client registry
// (*registry.Registry satisfies it): probing an RPC endpoint for the "test
// RPC" button and dropping a cached client after a configuration change.
type ChainRegistry interface {
	Probe(ctx context.Context, c domain.Chain, overrideURL string) (head uint64, latency time.Duration, err error)
	Invalidate(chainID string)
}

// Hooks let the HTTP layer nudge background workers. All are optional and
// must not block.
type Hooks struct {
	// WebhookKick is called after an admin re-queued a webhook event, so the
	// dispatcher can deliver it now instead of at its next poll.
	WebhookKick func()
	// ChainChanged is called after an admin changed a chain's configuration
	// (enabled, RPC endpoints, wallet, ...), e.g. to scan it right away.
	ChainChanged func(chainID string)
	// EmailKick is called after an e-mail was queued or re-queued, so the
	// mail dispatcher can send it now instead of at its next poll.
	EmailKick func()
}

// Deps are the dependencies of the HTTP layer.
type Deps struct {
	Store    *store.Store
	Invoices *invoice.Service
	Registry ChainRegistry
	Rates    rates.Provider
	Config   config.Config
	Logger   *slog.Logger     // nil = discard
	Now      func() time.Time // nil = time.Now; use the clock of the store / invoice service
	// Hasher hashes admin passwords; the zero value uses auth.DefaultBcryptCost.
	Hasher auth.Hasher
	// Limits configures rate limiting; zero fields select the defaults.
	Limits Limits
	Hooks  Hooks
	// RequestTimeout bounds one request (default DefaultRequestTimeout).
	RequestTimeout time.Duration
	// Notifier enqueues security e-mails (nil = none; it is a no-op itself
	// when e-mail is not configured).
	Notifier *notify.Service
	// Mailer sends the "test e-mail" synchronously (nil = e-mail not
	// configured).
	Mailer mailer.Sender
}

// API holds the handlers. Create it with New.
type API struct {
	store    *store.Store
	invoices *invoice.Service
	registry ChainRegistry
	rates    rates.Provider
	cfg      config.Config
	log      *slog.Logger
	now      func() time.Time
	hasher   auth.Hasher
	hooks    Hooks
	timeout  time.Duration
	notifier *notify.Service
	mailer   mailer.Sender

	limits         Limits
	merchantLimit  *keyedLimiter // per API key
	checkoutLimit  *keyedLimiter // per IP, GET /api/checkout/{id}
	quoteLimit     *keyedLimiter // per IP, POST /api/checkout/{id}/quote
	loginLimit     *keyedLimiter // per IP, POST /api/admin/auth/login
	loginFailures  *failureThrottle
	emailTestLimit *keyedLimiter // per user, POST /email/test
}

// New returns the gateway's HTTP handler.
func New(d Deps) http.Handler {
	return newAPI(d).Routes()
}

func newAPI(d Deps) *API {
	if d.Store == nil || d.Invoices == nil {
		panic("api: Store and Invoices are required")
	}
	if d.Logger == nil {
		d.Logger = slog.New(slog.DiscardHandler)
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.RequestTimeout <= 0 {
		d.RequestTimeout = DefaultRequestTimeout
	}
	lim := d.Limits.withDefaults()
	a := &API{
		store: d.Store, invoices: d.Invoices, registry: d.Registry, rates: d.Rates, cfg: d.Config,
		log: d.Logger, now: d.Now, hasher: d.Hasher, hooks: d.Hooks, timeout: d.RequestTimeout,
		limits: lim, notifier: d.Notifier, mailer: d.Mailer,
	}
	a.merchantLimit = newKeyedLimiter(lim.MerchantKey, a.now)
	a.checkoutLimit = newKeyedLimiter(lim.CheckoutGet, a.now)
	a.quoteLimit = newKeyedLimiter(lim.CheckoutQuote, a.now)
	a.loginLimit = newKeyedLimiter(lim.LoginIP, a.now)
	a.loginFailures = newFailureThrottle(lim.LoginFailures, lim.LoginWindow, a.now)
	a.emailTestLimit = newKeyedLimiter(lim.EmailTest, a.now)
	return a
}

// Routes builds the router.
func (a *API) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(a.requestInfo, a.accessLog, a.recoverer, a.securityHeaders, a.withTimeout, a.limitBody)
	// Set before any sub-router is mounted so they inherit the JSON handlers.
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		a.fail(w, r, errNotFound("route not found"))
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		a.fail(w, r, &Error{Status: http.StatusMethodNotAllowed, Code: CodeInvalidRequest,
			Message: "method " + r.Method + " is not allowed on this route"})
	})

	r.Get("/healthz", a.healthz)
	r.Get("/readyz", a.readyz)

	r.Route("/api/v1", a.merchantRoutes)
	r.Route("/api/checkout", a.checkoutRoutes)
	r.Route("/api/admin", a.adminRoutes)
	return r
}

func (a *API) healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *API) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := a.store.Ping(ctx); err != nil {
		a.log.WarnContext(ctx, "readiness check failed", "error", err)
		a.fail(w, r, &Error{Status: http.StatusServiceUnavailable, Code: CodeUnavailable, Message: "database unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
