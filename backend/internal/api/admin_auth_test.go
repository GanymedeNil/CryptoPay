package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cryptopay/internal/config"
	"cryptopay/internal/domain"
	"cryptopay/internal/store/storetest"
)

func sessionFrom(t *testing.T, r *response) *http.Cookie {
	t.Helper()
	for _, c := range (&http.Response{Header: r.Header}).Cookies() {
		if c.Name == SessionCookie {
			return c
		}
	}
	return nil
}

func TestAdminLogin(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	u := storetest.AdminUser(t, h.Store, domain.RoleAdmin)
	login := func(email, password string, opts ...reqOpt) *response {
		return h.do(http.MethodPost, "/api/admin/auth/login", map[string]any{"email": email, "password": password},
			append([]reqOpt{csrfHeader()}, opts...)...)
	}

	// CSRF header is required, also for login.
	h.do(http.MethodPost, "/api/admin/auth/login", map[string]any{"email": u.Email, "password": storetest.Password}).
		apiError(http.StatusForbidden, CodeForbidden)

	r := login(strings.ToUpper(u.Email), storetest.Password).status(http.StatusOK)
	me := r.object()
	assert.Equal(t, u.ID.String(), me["id"])
	assert.Equal(t, "admin", me["role"])
	assert.NotNil(t, me["last_login_at"])
	assert.NotContains(t, string(r.Body), "password")
	c := sessionFrom(t, r)
	require.NotNil(t, c)
	assert.True(t, c.HttpOnly)
	assert.False(t, c.Secure, "development")
	assert.Equal(t, http.SameSiteLaxMode, c.SameSite)
	assert.Equal(t, "/", c.Path)
	assert.Equal(t, int(SessionTTL/time.Second), c.MaxAge)

	// The cookie authenticates; only its hash is stored.
	h.do(http.MethodGet, "/api/admin/auth/me", nil, sessionCookie(c.Value)).status(http.StatusOK)
	var stored string
	require.NoError(t, h.Store.Pool().QueryRow(h.Ctx, `SELECT token_hash FROM admin_sessions WHERE user_id = $1`, u.ID).Scan(&stored))
	assert.NotEqual(t, c.Value, stored)
	assert.Len(t, stored, 64)

	login(u.Email, "wrong-password").apiError(http.StatusUnauthorized, CodeUnauthorized)
	login("nobody@example.com", storetest.Password).apiError(http.StatusUnauthorized, CodeUnauthorized)
	inactive := storetest.AdminUser(t, h.Store, domain.RoleOwner, func(x *domain.AdminUser) { x.Active = false })
	login(inactive.Email, storetest.Password).apiError(http.StatusUnauthorized, CodeUnauthorized)
	e := h.do(http.MethodPost, "/api/admin/auth/login", map[string]any{}, csrfHeader()).apiError(http.StatusBadRequest, CodeInvalidRequest)
	assert.Contains(t, e.Fields, "email")
	assert.Contains(t, e.Fields, "password")

	logs := h.auditLogs("auth.login")
	require.Len(t, logs, 1)
	assert.Equal(t, u.Email, logs[0].ActorEmail)
	assert.Equal(t, u.ID.String(), logs[0].TargetID)
}

func TestAdminLoginSecureCookieInProduction(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withConfig(func(c *config.Config) { c.Env = config.EnvProduction }))
	u := storetest.AdminUser(t, h.Store, domain.RoleOwner)
	r := h.do(http.MethodPost, "/api/admin/auth/login", map[string]any{"email": u.Email, "password": storetest.Password}, csrfHeader())
	r.status(http.StatusOK)
	c := sessionFrom(t, r)
	require.NotNil(t, c)
	assert.True(t, c.Secure)
	assert.NotEmpty(t, r.Header.Get("Strict-Transport-Security"))
}

func TestAdminLoginThrottle(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withLimits(Limits{LoginFailures: 3, LoginWindow: 10 * time.Minute,
		LoginIP: Limit{Rate: 1000, Burst: 1000}}))
	u := storetest.AdminUser(t, h.Store, domain.RoleOwner)
	login := func(email, password, ip string) *response {
		return h.do(http.MethodPost, "/api/admin/auth/login", map[string]any{"email": email, "password": password},
			csrfHeader(), fromIP(ip))
	}
	for range 3 {
		login(u.Email, "bad-password", "198.51.100.7").apiError(http.StatusUnauthorized, CodeUnauthorized)
	}
	// Blocked even with the right password.
	r := login(u.Email, storetest.Password, "198.51.100.7")
	r.apiError(http.StatusTooManyRequests, CodeRateLimited)
	assert.NotEmpty(t, r.Header.Get("Retry-After"))
	// Per (IP, e-mail): another IP is not blocked.
	login(u.Email, storetest.Password, "198.51.100.8").status(http.StatusOK)
	// After the window the pair may try again.
	h.Clock.Advance(11 * time.Minute)
	login(u.Email, storetest.Password, "198.51.100.7").status(http.StatusOK)

	// The per-IP limiter caps attempts across e-mails.
	h2 := newHarness(t, withLimits(Limits{LoginIP: Limit{Rate: 1, Burst: 2}}))
	for i := range 2 {
		h2.do(http.MethodPost, "/api/admin/auth/login", map[string]any{"email": uuid.NewString() + "@x.example", "password": "whatever-" + string(rune('a'+i))},
			csrfHeader()).apiError(http.StatusUnauthorized, CodeUnauthorized)
	}
	h2.do(http.MethodPost, "/api/admin/auth/login", map[string]any{"email": "z@x.example", "password": "whatever"}, csrfHeader()).
		apiError(http.StatusTooManyRequests, CodeRateLimited)
}

func TestAdminSessionRequired(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.do(http.MethodGet, "/api/admin/auth/me", nil).apiError(http.StatusUnauthorized, CodeUnauthorized)
	h.do(http.MethodGet, "/api/admin/invoices", nil, sessionCookie("forged")).apiError(http.StatusUnauthorized, CodeUnauthorized)

	u := storetest.AdminUser(t, h.Store, domain.RoleOwner)
	_, used := storetest.Session(t, h.Store, u.ID)
	_, idle := storetest.Session(t, h.Store, u.ID)
	h.do(http.MethodGet, "/api/admin/auth/me", nil, sessionCookie(used)).status(http.StatusOK) // slides to 7 days
	h.Clock.Advance(25 * time.Hour)                                                            // storetest sessions last 24h
	h.do(http.MethodGet, "/api/admin/auth/me", nil, sessionCookie(used)).status(http.StatusOK)
	r := h.do(http.MethodGet, "/api/admin/auth/me", nil, sessionCookie(idle))
	r.apiError(http.StatusUnauthorized, CodeUnauthorized)
	if c := sessionFrom(t, r); assert.NotNil(t, c) {
		assert.Less(t, c.MaxAge, 0, "stale cookie cleared")
	}
}

func TestAdminSessionSlides(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.newAdmin(domain.RoleViewer) // storetest session: 24h left < SessionTTL/2
	r := c.get("/auth/me").status(http.StatusOK)
	ck := sessionFrom(t, r)
	require.NotNil(t, ck, "cookie refreshed")
	assert.Equal(t, c.Token, ck.Value)
	h.Clock.Advance(3 * 24 * time.Hour)
	c.get("/auth/me").status(http.StatusOK)
}

func TestAdminLogout(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.newAdmin(domain.RoleViewer)
	r := c.post("/auth/logout", nil).status(http.StatusNoContent)
	ck := sessionFrom(t, r)
	require.NotNil(t, ck)
	assert.Less(t, ck.MaxAge, 0)
	c.get("/auth/me").apiError(http.StatusUnauthorized, CodeUnauthorized)
	// Without a session it still answers 204.
	h.do(http.MethodPost, "/api/admin/auth/logout", nil, csrfHeader()).status(http.StatusNoContent)
	require.Len(t, h.auditLogs("auth.logout"), 1)
}

func TestAdminCSRF(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.newAdmin(domain.RoleOwner)
	cases := []struct{ method, path string }{
		{http.MethodPost, "/merchants"},
		{http.MethodPut, "/settings"},
		{http.MethodPatch, "/chains/ethereum"},
		{http.MethodDelete, "/users/" + uuid.NewString()},
		{http.MethodPost, "/auth/logout"},
		{http.MethodPost, "/auth/password"},
	}
	for _, tc := range cases {
		r := h.do(tc.method, "/api/admin"+tc.path, map[string]any{}, sessionCookie(c.Token))
		r.apiError(http.StatusForbidden, CodeForbidden)
		r = h.do(tc.method, "/api/admin"+tc.path, map[string]any{}, sessionCookie(c.Token), header(CSRFHeader, "XMLHttpRequest"))
		r.apiError(http.StatusForbidden, CodeForbidden)
	}
	// GETs need no header.
	h.do(http.MethodGet, "/api/admin/merchants", nil, sessionCookie(c.Token)).status(http.StatusOK)
	// The session survived (logout without CSRF was refused).
	c.get("/auth/me").status(http.StatusOK)
}

func TestAdminRoleMatrix(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	clients := map[domain.Role]*adminClient{
		domain.RoleOwner:  h.newAdmin(domain.RoleOwner),
		domain.RoleAdmin:  h.newAdmin(domain.RoleAdmin),
		domain.RoleViewer: h.newAdmin(domain.RoleViewer),
	}
	settings := domain.DefaultSettings()
	type want struct{ owner, admin, viewer int }
	cases := []struct {
		name   string
		method string
		path   func() string
		body   func() any
		want   want
	}{
		{"read overview", http.MethodGet, fixed("/overview"), nil, want{200, 200, 200}},
		{"read merchant overviews", http.MethodGet, fixed("/overview/merchants"), nil, want{200, 200, 200}},
		{"read invoices", http.MethodGet, fixed("/invoices"), nil, want{200, 200, 200}},
		{"read chains", http.MethodGet, fixed("/chains"), nil, want{200, 200, 200}},
		{"read settings", http.MethodGet, fixed("/settings"), nil, want{200, 200, 200}},
		{"read audit", http.MethodGet, fixed("/audit-logs"), nil, want{200, 200, 200}},
		{"write settings", http.MethodPut, fixed("/settings"), func() any { return settings }, want{200, 200, 403}},
		{"create merchant", http.MethodPost, fixed("/merchants"), func() any { return map[string]any{"name": "M"} }, want{201, 201, 403}},
		{"patch chain", http.MethodPatch, fixed("/chains/sepolia"), func() any { return map[string]any{"confirmations": 4} }, want{200, 200, 403}},
		{"test rpc", http.MethodPost, fixed("/chains/sepolia/test-rpc"), func() any { return map[string]any{} }, want{200, 200, 403}},
		{"preview wallet", http.MethodPost, fixed("/wallets/preview"), func() any {
			return map[string]any{"family": "tron", "kind": "static", "address": "TJRabPrwbZy45sbavfcjinPJC18kjpRTv8", "chain_id": "tron"}
		}, want{200, 200, 403}},
		{"list users", http.MethodGet, fixed("/users"), nil, want{200, 403, 403}},
		{"create user", http.MethodPost, fixed("/users"), func() any {
			return map[string]any{"email": uuid.NewString() + "@example.com", "password": "long-enough-pw", "role": "viewer"}
		}, want{201, 403, 403}},
		{"own password", http.MethodPost, fixed("/auth/password"), func() any {
			return map[string]any{"current_password": storetest.Password, "new_password": storetest.Password}
		}, want{204, 204, 204}},
		{"me", http.MethodGet, fixed("/auth/me"), nil, want{200, 200, 200}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for role, wantCode := range map[domain.Role]int{
				domain.RoleOwner: tc.want.owner, domain.RoleAdmin: tc.want.admin, domain.RoleViewer: tc.want.viewer,
			} {
				var body any
				if tc.body != nil {
					body = tc.body()
				}
				r := clients[role].do(tc.method, tc.path(), body)
				if wantCode == http.StatusForbidden {
					r.apiError(http.StatusForbidden, CodeForbidden)
				} else {
					require.Equal(t, wantCode, r.Code, "%s as %s: %s", tc.name, role, r.Body)
				}
			}
		})
	}
	// Viewers may log out.
	clients[domain.RoleViewer].post("/auth/logout", nil).status(http.StatusNoContent)
}

func fixed(p string) func() string { return func() string { return p } }

func TestAdminChangePassword(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	a := h.newAdmin(domain.RoleViewer)
	_, other := storetest.Session(t, h.Store, a.User.ID)

	e := a.post("/auth/password", map[string]any{"current_password": "wrong", "new_password": "new-password-1"}).
		apiError(http.StatusBadRequest, CodeInvalidRequest)
	assert.Contains(t, e.Fields, "current_password")
	e = a.post("/auth/password", map[string]any{"current_password": storetest.Password, "new_password": "short"}).
		apiError(http.StatusBadRequest, CodeInvalidRequest)
	assert.Contains(t, e.Fields["new_password"], "at least 8")

	a.post("/auth/password", map[string]any{"current_password": storetest.Password, "new_password": "new-password-1"}).
		status(http.StatusNoContent)
	a.get("/auth/me").status(http.StatusOK) // this session stays
	h.do(http.MethodGet, "/api/admin/auth/me", nil, sessionCookie(other)).apiError(http.StatusUnauthorized, CodeUnauthorized)
	h.do(http.MethodPost, "/api/admin/auth/login", map[string]any{"email": a.User.Email, "password": "new-password-1"}, csrfHeader()).
		status(http.StatusOK)

	assert.NotContains(t, h.allAuditText(), "new-password-1")
	require.Len(t, h.auditLogs("auth.password_change"), 1)
}

func TestAdminUsersAndLastOwner(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	owner := h.newAdmin(domain.RoleOwner)

	e := owner.post("/users", map[string]any{"email": "bad", "password": "x", "role": "god"}).apiError(http.StatusBadRequest, CodeInvalidRequest)
	for _, f := range []string{"email", "password", "role"} {
		assert.Contains(t, e.Fields, f)
	}
	created := owner.post("/users", map[string]any{"email": "ops@example.com", "password": "secret-pass-1", "role": "admin"}).
		status(http.StatusCreated).object()
	assert.Equal(t, "admin", created["role"])
	assert.Equal(t, true, created["active"])
	e = owner.post("/users", map[string]any{"email": "OPS@example.com", "password": "secret-pass-1", "role": "admin"}).
		apiError(http.StatusConflict, CodeConflict)
	assert.Contains(t, e.Fields, "email")
	id := created["id"].(string)

	// The new user can log in; deactivating revokes their sessions.
	r := h.do(http.MethodPost, "/api/admin/auth/login", map[string]any{"email": "ops@example.com", "password": "secret-pass-1"}, csrfHeader())
	tok := sessionFrom(t, r.status(http.StatusOK)).Value
	owner.patch("/users/"+id, map[string]any{"active": false}).status(http.StatusOK)
	h.do(http.MethodGet, "/api/admin/auth/me", nil, sessionCookie(tok)).apiError(http.StatusUnauthorized, CodeUnauthorized)
	out := owner.patch("/users/"+id, map[string]any{"active": true, "role": "viewer", "password": "another-pass-2"}).status(http.StatusOK).object()
	assert.Equal(t, "viewer", out["role"])

	// Last owner protections.
	owner.patch("/users/"+owner.User.ID.String(), map[string]any{"role": "admin"}).apiError(http.StatusConflict, CodeConflict)
	owner.patch("/users/"+owner.User.ID.String(), map[string]any{"active": false}).apiError(http.StatusConflict, CodeConflict)
	owner.del("/users/"+owner.User.ID.String()).apiError(http.StatusConflict, CodeConflict) // yourself

	second := h.newAdmin(domain.RoleOwner)
	// With two owners one may be demoted ...
	second.patch("/users/"+owner.User.ID.String(), map[string]any{"role": "admin"}).status(http.StatusOK)
	// ... but not the last one, by anybody.
	third := h.newAdmin(domain.RoleOwner)
	third.patch("/users/"+second.User.ID.String(), map[string]any{"role": "viewer"}).status(http.StatusOK)
	third.del("/users/"+third.User.ID.String()).apiError(http.StatusConflict, CodeConflict)
	n, err := h.Store.CountActiveOwners(h.Ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	// A demoted owner lost user management.
	owner.get("/users").apiError(http.StatusForbidden, CodeForbidden)

	third.del("/users/" + id).status(http.StatusNoContent)
	third.del("/users/"+id).apiError(http.StatusNotFound, CodeNotFound)
	third.patch("/users/"+uuid.NewString(), map[string]any{"role": "admin"}).apiError(http.StatusNotFound, CodeNotFound)

	var list itemsJSON[map[string]any]
	third.get("/users").status(http.StatusOK).decode(&list)
	assert.Len(t, list.Items, 3)

	audit := h.allAuditText()
	for _, secret := range []string{"secret-pass-1", "another-pass-2", "password_hash", "$2a$"} {
		assert.NotContains(t, audit, secret)
	}
	assert.NotEmpty(t, h.auditLogs("user.create"))
	assert.NotEmpty(t, h.auditLogs("user.update"))
	assert.NotEmpty(t, h.auditLogs("user.delete"))
}
