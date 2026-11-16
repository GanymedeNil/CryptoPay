package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"cryptopay/internal/auth"
	"cryptopay/internal/domain"
	"cryptopay/internal/store"
)

const (
	// SessionCookie is the admin session cookie (docs/api.md).
	SessionCookie = "cp_session"
	// SessionTTL is the lifetime of an admin session; it slides while the
	// session is used.
	SessionTTL = 7 * 24 * time.Hour
	// CSRFHeader / CSRFValue must be sent with every non-GET admin request.
	CSRFHeader = "X-Requested-With"
	CSRFValue  = "cryptopay"
)

// adminCtx is the authenticated admin of a request.
type adminCtx struct {
	Session domain.AdminSession
	User    domain.AdminUser
}

func adminFrom(ctx context.Context) (adminCtx, bool) {
	a, ok := ctx.Value(keyAdmin).(adminCtx)
	return a, ok
}

func (a *API) adminRoutes(r chi.Router) {
	r.Use(a.csrf)
	r.With(a.limitByIP(a.loginLimit)).Post("/auth/login", a.login)
	r.Post("/auth/logout", a.logout)

	r.Group(func(r chi.Router) {
		r.Use(a.adminAuth)
		// Any role, including viewers.
		r.Get("/auth/me", a.me)
		r.Post("/auth/password", a.changePassword)

		r.Group(func(r chi.Router) {
			// Viewers are read-only.
			r.Use(a.requireWriteForMutations)

			r.Get("/overview", a.adminOverview)

			r.Get("/invoices", a.adminListInvoices)
			r.Post("/invoices", a.adminCreateInvoice)
			r.Get("/invoices/{id}", a.adminGetInvoice)
			r.Post("/invoices/{id}/cancel", a.adminCancelInvoice)
			r.Post("/invoices/{id}/mark-paid", a.adminMarkPaid)

			r.Get("/transfers", a.adminListTransfers)
			r.Post("/transfers/{id}/assign", a.adminAssignTransfer)

			r.Get("/merchants", a.adminListMerchants)
			r.Post("/merchants", a.adminCreateMerchant)
			r.Get("/merchants/{id}", a.adminGetMerchant)
			r.Patch("/merchants/{id}", a.adminUpdateMerchant)
			r.Post("/merchants/{id}/api-keys", a.adminCreateAPIKey)
			r.Delete("/merchants/{id}/api-keys/{key_id}", a.adminRevokeAPIKey)
			r.Post("/merchants/{id}/webhook-secret", a.adminRotateWebhookSecret)
			r.Put("/merchants/{id}/wallets/{chain_id}", a.adminSetMerchantWallet)
			r.Delete("/merchants/{id}/wallets/{chain_id}", a.adminUnsetMerchantWallet)

			r.Get("/chains", a.adminListChains)
			r.Patch("/chains/{id}", a.adminUpdateChain)
			r.Post("/chains/{id}/test-rpc", a.adminTestRPC)

			r.Get("/assets", a.adminListAssets)
			r.Post("/assets", a.adminCreateAsset)
			r.Patch("/assets/{id}", a.adminUpdateAsset)

			r.Get("/wallets", a.adminListWallets)
			r.Post("/wallets/preview", a.adminPreviewWallet)
			r.Post("/wallets", a.adminCreateWallet)
			r.Patch("/wallets/{id}", a.adminUpdateWallet)
			r.Delete("/wallets/{id}", a.adminDeleteWallet)

			r.Get("/webhook-events", a.adminListWebhookEvents)
			r.Post("/webhook-events/{id}/retry", a.adminRetryWebhookEvent)

			r.Get("/settings", a.adminGetSettings)
			r.Put("/settings", a.adminPutSettings)

			r.Get("/audit-logs", a.adminListAuditLogs)

			r.Group(func(r chi.Router) {
				// User management is owner-only, reads included.
				r.Use(a.requireOwner)
				r.Get("/users", a.adminListUsers)
				r.Post("/users", a.adminCreateUser)
				r.Patch("/users/{id}", a.adminUpdateUser)
				r.Delete("/users/{id}", a.adminDeleteUser)
			})
		})
	})
}

func isSafeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// csrf requires `X-Requested-With: cryptopay` on every non-GET admin
// request. Browsers cannot add custom headers to cross-site form posts, and
// a cross-site fetch with one needs a CORS preflight the admin API never
// grants.
func (a *API) csrf(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isSafeMethod(r.Method) && r.Header.Get(CSRFHeader) != CSRFValue {
			a.fail(w, r, errForbidden("missing or invalid "+CSRFHeader+" header"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// adminAuth resolves the session cookie (401 when missing, expired or the
// user is inactive). Sessions slide: one used in the second half of its
// lifetime is extended to a full SessionTTL.
func (a *API) adminAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		c, err := r.Cookie(SessionCookie)
		if err != nil || c.Value == "" || len(c.Value) > 256 {
			a.fail(w, r, errUnauthorized("not signed in"))
			return
		}
		now := a.now()
		sess, user, err := a.store.GetSessionByTokenHash(ctx, auth.HashToken(c.Value), now)
		if errors.Is(err, store.ErrNotFound) {
			a.clearSessionCookie(w)
			a.fail(w, r, errUnauthorized("the session has expired, sign in again"))
			return
		}
		if err != nil {
			a.fail(w, r, err)
			return
		}
		if sess.ExpiresAt.Sub(now) < SessionTTL/2 {
			exp := now.Add(SessionTTL)
			if err := a.store.ExtendSession(ctx, sess.ID, exp); err == nil {
				sess.ExpiresAt = exp
				a.setSessionCookie(w, c.Value, exp)
			} else {
				a.log.WarnContext(ctx, "extend session failed", "request_id", requestIDFrom(ctx), "error", err)
			}
		}
		infoFrom(ctx).AdminID = user.ID.String()
		next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, keyAdmin, adminCtx{Session: sess, User: user})))
	})
}

// requireWriteForMutations rejects non-GET requests of viewers (403).
func (a *API) requireWriteForMutations(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ad, _ := adminFrom(r.Context())
		if !isSafeMethod(r.Method) && !ad.User.Role.CanWrite() {
			a.fail(w, r, errForbidden("your role ("+string(ad.User.Role)+") is read-only"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireOwner restricts user management to owners (403).
func (a *API) requireOwner(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ad, _ := adminFrom(r.Context())
		if !ad.User.Role.CanManage() {
			a.fail(w, r, errForbidden("only owners can manage users"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) setSessionCookie(w http.ResponseWriter, token string, expires time.Time) {
	maxAge := int(expires.Sub(a.now()) / time.Second)
	if maxAge < 1 {
		maxAge = 1
	}
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: token, Path: "/", MaxAge: maxAge, Expires: expires.UTC(),
		HttpOnly: true, Secure: a.cfg.SecureCookies(), SameSite: http.SameSiteLaxMode,
	})
}

func (a *API) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: "", Path: "/", MaxAge: -1, Expires: time.Unix(0, 0),
		HttpOnly: true, Secure: a.cfg.SecureCookies(), SameSite: http.SameSiteLaxMode,
	})
}

// ---- audit ----

// auditEntry is one admin action for the audit log. Detail must never
// carry secrets, keys, passwords or full RPC URLs.
type auditEntry struct {
	Action     string
	TargetType string
	TargetID   string
	Detail     map[string]any
}

// audit inserts the entry with tx (the store or a transaction) on behalf of
// the request's admin (or the given user for login, where no session exists
// yet).
func (a *API) audit(ctx context.Context, tx *store.Store, actor *domain.AdminUser, e auditEntry) error {
	if actor == nil {
		if ad, ok := adminFrom(ctx); ok {
			actor = &ad.User
		}
	}
	l := domain.AuditLog{Action: e.Action, TargetType: e.TargetType, TargetID: e.TargetID, IP: clientIPFrom(ctx)}
	if actor != nil {
		id := actor.ID
		l.ActorID, l.ActorEmail = &id, actor.Email
	}
	if e.Detail != nil {
		raw, err := json.Marshal(e.Detail)
		if err != nil {
			return err
		}
		l.Detail = raw
	}
	_, err := tx.InsertAuditLog(ctx, l)
	return err
}

// auditAfter records an action that was already committed by a service
// (which runs its own transaction). A failure is logged, not returned: the
// action itself succeeded.
func (a *API) auditAfter(ctx context.Context, e auditEntry) {
	// Do not lose the entry because the client hung up.
	ctx2, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := a.audit(ctx2, a.store, nil, e); err != nil {
		a.log.ErrorContext(ctx, "write audit log failed", "request_id", requestIDFrom(ctx), "action", e.Action, "error", err)
	}
}

// inTx runs fn in a transaction (store.WithTx).
func (a *API) inTx(ctx context.Context, fn func(tx *store.Store) error) error {
	return a.store.WithTx(ctx, fn)
}
