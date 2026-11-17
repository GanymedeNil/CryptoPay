package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cryptopay/internal/auth"
	"cryptopay/internal/domain"
	"cryptopay/internal/notify"
	"cryptopay/internal/store"
	"cryptopay/internal/store/storetest"
)

// logBuffer captures the API's log output (goroutine-safe).
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// withLog sends the API's logs (debug level) to buf. Put it before withEmail.
func withLog(buf *logBuffer) harnessOpt {
	return func(d *Deps) {
		d.Logger = slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
}

// detailOf decodes an audit entry's detail.
func detailOf(t *testing.T, l domain.AuditLog) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(l.Detail, &m))
	return m
}

func challengeOf(t *testing.T, r *response) string {
	t.Helper()
	require.Equal(t, http.StatusAccepted, r.Code, "body: %s", r.Body)
	m := r.object()
	require.Equal(t, true, m["two_factor_required"])
	tok, _ := m["challenge_token"].(string)
	require.NotEmpty(t, tok)
	return tok
}

func TestTwoFactorLoginFlow(t *testing.T) {
	t.Parallel()
	logs := &logBuffer{}
	h := newHarness(t, withLog(logs), withEmail())
	a := h.newAdmin(domain.RoleAdmin)
	secret, codes := a.enable2FA()
	require.Len(t, codes, 10)

	// The secret is encrypted at rest.
	var enc string
	require.NoError(t, h.Store.Pool().QueryRow(h.Ctx, `SELECT totp_secret_enc FROM admin_users WHERE id = $1`, a.User.ID).Scan(&enc))
	assert.True(t, strings.HasPrefix(enc, "v1:"), "AES-GCM ciphertext")
	assert.NotContains(t, enc, secret)
	plain, err := storetest.Box().Decrypt(enc)
	require.NoError(t, err)
	assert.Equal(t, secret, plain)
	// Recovery codes only as hashes.
	var hashes []string
	rows, err := h.Store.Pool().Query(h.Ctx, `SELECT code_hash FROM admin_recovery_codes WHERE user_id = $1`, a.User.ID)
	require.NoError(t, err)
	for rows.Next() {
		var x string
		require.NoError(t, rows.Scan(&x))
		hashes = append(hashes, x)
	}
	require.Len(t, hashes, 10)
	for _, c := range codes {
		for _, x := range hashes {
			assert.NotContains(t, x, strings.ReplaceAll(c, "-", ""))
		}
	}

	// Step one: 202, a challenge, NO cookie.
	r := h.login(a.User.Email, storetest.Password)
	tok := challengeOf(t, r)
	assert.Nil(t, sessionFrom(t, r), "no session cookie before the second step")
	exp, err := time.Parse(time.RFC3339, r.object()["expires_at"].(string))
	require.NoError(t, err)
	assert.Equal(t, h.Clock.Now().Add(ChallengeTTL).Truncate(time.Second), exp.Truncate(time.Second))
	var stored string
	require.NoError(t, h.Store.Pool().QueryRow(h.Ctx, `SELECT token_hash FROM admin_login_challenges WHERE user_id = $1`, a.User.ID).Scan(&stored))
	assert.Equal(t, auth.HashToken(tok), stored, "only the hash is stored")

	// Body validation.
	e := h.login2FA(map[string]any{"challenge_token": tok}).apiError(http.StatusBadRequest, CodeInvalidRequest)
	assert.Contains(t, e.Fields, "code")
	h.login2FA(map[string]any{"challenge_token": tok, "code": "123456", "recovery_code": codes[0]}).apiError(http.StatusBadRequest, CodeInvalidRequest)
	h.login2FA(map[string]any{"code": "123456"}).apiError(http.StatusBadRequest, CodeInvalidRequest)
	// CSRF header is required.
	h.do(http.MethodPost, "/api/admin/auth/login/2fa", map[string]any{"challenge_token": tok, "code": h.totp(secret)}).
		apiError(http.StatusForbidden, CodeForbidden)

	// Step two: wrong code → generic 401; right code → 200 + cookie.
	wrong := h.login2FA(map[string]any{"challenge_token": tok, "code": "000000"}).apiError(http.StatusUnauthorized, CodeUnauthorized)
	unknown := h.login2FA(map[string]any{"challenge_token": "unknown-token", "code": h.totp(secret)}).apiError(http.StatusUnauthorized, CodeUnauthorized)
	assert.Equal(t, wrong.Message, unknown.Message, "one generic message")
	ok := h.login2FA(map[string]any{"challenge_token": tok, "code": h.totp(secret)}).status(http.StatusOK)
	me := ok.object()
	assert.Equal(t, a.User.ID.String(), me["id"])
	assert.Equal(t, true, me["two_factor_enabled"])
	ck := sessionFrom(t, ok)
	require.NotNil(t, ck)
	h.do(http.MethodGet, "/api/admin/auth/me", nil, sessionCookie(ck.Value)).status(http.StatusOK)
	// The challenge is consumed.
	h.login2FA(map[string]any{"challenge_token": tok, "code": h.totp(secret)}).apiError(http.StatusUnauthorized, CodeUnauthorized)

	// Replay: the same code (same time step) cannot sign in again.
	tok2 := challengeOf(t, h.login(a.User.Email, storetest.Password))
	h.login2FA(map[string]any{"challenge_token": tok2, "code": h.totp(secret)}).apiError(http.StatusUnauthorized, CodeUnauthorized)
	h.nextStep()
	h.login2FA(map[string]any{"challenge_token": tok2, "code": h.totp(secret)}).status(http.StatusOK)

	logins := h.auditLogs("auth.login")
	require.NotEmpty(t, logins)
	assert.Equal(t, "totp", detailOf(t, logins[0])["method"])

	// Wrong passwords never yield a challenge.
	h.login(a.User.Email, "wrong-password").apiError(http.StatusUnauthorized, CodeUnauthorized)

	// Nothing secret in audit, logs or outbox.
	audit, logText, mails := h.allAuditText(), logs.String(), h.allEmailText()
	for _, s := range append([]string{secret, tok, tok2, storetest.Password}, codes...) {
		assert.NotContains(t, audit, s)
		assert.NotContains(t, logText, s)
		assert.NotContains(t, mails, s)
	}
	// Nor in later API responses.
	for _, path := range []string{"/auth/me", "/auth/sessions", "/audit-logs?page_size=100"} {
		body := string(a.get(path).status(http.StatusOK).Body)
		assert.NotContains(t, body, secret, path)
		assert.NotContains(t, body, enc, path)
	}
	owner := h.newAdmin(domain.RoleOwner)
	assert.NotContains(t, string(owner.get("/users").status(http.StatusOK).Body), secret)
}

func TestTwoFactorChallengeExhaustedAndExpired(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	a := h.newAdmin(domain.RoleViewer)
	secret, _ := a.enable2FA()

	// 5 wrong codes delete the challenge: then even the right code fails.
	tok := challengeOf(t, h.login(a.User.Email, storetest.Password))
	for i := range MaxChallengeAttempts {
		h.login2FA(map[string]any{"challenge_token": tok, "code": "00000" + string(rune('0'+i))}).apiError(http.StatusUnauthorized, CodeUnauthorized)
	}
	var n int
	require.NoError(t, h.Store.Pool().QueryRow(h.Ctx, `SELECT count(*) FROM admin_login_challenges WHERE user_id = $1`, a.User.ID).Scan(&n))
	assert.Zero(t, n, "challenge invalidated")
	h.login2FA(map[string]any{"challenge_token": tok, "code": h.totp(secret)}).apiError(http.StatusUnauthorized, CodeUnauthorized)

	// Starting over works.
	tok = challengeOf(t, h.login(a.User.Email, storetest.Password))
	h.login2FA(map[string]any{"challenge_token": tok, "code": h.totp(secret)}).status(http.StatusOK)
	h.nextStep()

	// An expired challenge is refused.
	tok = challengeOf(t, h.login(a.User.Email, storetest.Password))
	h.Clock.Advance(ChallengeTTL + time.Second)
	h.login2FA(map[string]any{"challenge_token": tok, "code": h.totp(secret)}).apiError(http.StatusUnauthorized, CodeUnauthorized)

	// A user deactivated between the steps cannot finish.
	h.nextStep()
	tok = challengeOf(t, h.login(a.User.Email, storetest.Password))
	_, err := h.Store.UpdateAdminUser(h.Ctx, a.User.ID, store.AdminUserUpdate{Active: storetest.Ptr(false)})
	require.NoError(t, err)
	h.login2FA(map[string]any{"challenge_token": tok, "code": h.totp(secret)}).apiError(http.StatusUnauthorized, CodeUnauthorized)
}

func TestTwoFactorRecoveryCodeLogin(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withEmail())
	a := h.newAdmin(domain.RoleAdmin)
	_, codes := a.enable2FA()

	tok := challengeOf(t, h.login(a.User.Email, storetest.Password))
	// Typed sloppily: upper case, no dash.
	typed := strings.ToUpper(strings.ReplaceAll(codes[3], "-", ""))
	r := h.login2FA(map[string]any{"challenge_token": tok, "recovery_code": typed}).status(http.StatusOK)
	require.NotNil(t, sessionFrom(t, r))
	logins := h.auditLogs("auth.login")
	assert.Equal(t, "recovery_code", detailOf(t, logins[0])["method"])

	// Single use.
	tok = challengeOf(t, h.login(a.User.Email, storetest.Password))
	h.login2FA(map[string]any{"challenge_token": tok, "recovery_code": codes[3]}).apiError(http.StatusUnauthorized, CodeUnauthorized)
	h.login2FA(map[string]any{"challenge_token": tok, "recovery_code": codes[4]}).status(http.StatusOK)
	left, err := h.Store.CountRecoveryCodes(h.Ctx, a.User.ID)
	require.NoError(t, err)
	assert.Equal(t, 8, left)

	// Another user's code does not work for this account.
	b := h.newAdmin(domain.RoleAdmin)
	_, bCodes := b.enable2FA()
	tok = challengeOf(t, h.login(a.User.Email, storetest.Password))
	h.login2FA(map[string]any{"challenge_token": tok, "recovery_code": bCodes[0]}).apiError(http.StatusUnauthorized, CodeUnauthorized)

	// Each use is mailed with the number left; no code is in any mail.
	used := h.emails(notify.KindRecoveryCodeUsed)
	require.Len(t, used, 2)
	assert.Equal(t, a.User.Email, used[0].To)
	// Same created_at (fixed clock): compare without relying on the order.
	bodies := used[0].BodyText + "\n" + used[1].BodyText
	assert.Contains(t, bodies, "还剩 9 个")
	assert.Contains(t, bodies, "还剩 8 个")
	mails := h.allEmailText()
	for _, c := range append(codes, bCodes...) {
		assert.NotContains(t, mails, c)
		assert.NotContains(t, mails, strings.ReplaceAll(c, "-", ""))
	}
}

func TestTwoFactorThrottle(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withEmail(), withLimits(Limits{LoginIP: Limit{Rate: 1000, Burst: 1000}, TwoFactorFailures: 3,
		LoginFailures: 5, LoginWindow: 10 * time.Minute}))
	a := h.newAdmin(domain.RoleAdmin)
	secret, _ := a.enable2FA()

	// Wrong codes count per user across challenges and IPs.
	for i := range 3 {
		tok := challengeOf(t, h.login(a.User.Email, storetest.Password, fromIP("198.51.100."+string(rune('1'+i)))))
		h.login2FA(map[string]any{"challenge_token": tok, "code": "999999"}, fromIP("198.51.100."+string(rune('1'+i)))).
			apiError(http.StatusUnauthorized, CodeUnauthorized)
	}
	tok := challengeOf(t, h.login(a.User.Email, storetest.Password, fromIP("203.0.113.9")))
	r := h.login2FA(map[string]any{"challenge_token": tok, "code": h.totp(secret)}, fromIP("203.0.113.9"))
	r.apiError(http.StatusTooManyRequests, CodeRateLimited)
	assert.NotEmpty(t, r.Header.Get("Retry-After"))
	// The account owner is told once.
	blocked := h.emails(notify.KindLoginBlocked)
	require.Len(t, blocked, 1)
	assert.Equal(t, a.User.Email, blocked[0].To)
	// The management endpoints are throttled too.
	a.post("/auth/2fa/recovery-codes", map[string]any{"current_password": storetest.Password, "code": h.totp(secret)}).
		apiError(http.StatusTooManyRequests, CodeRateLimited)

	// After the window the code works (the challenge was not consumed).
	h.Clock.Advance(4 * time.Minute)
	tok = challengeOf(t, h.login(a.User.Email, storetest.Password, fromIP("203.0.113.9")))
	h.Clock.Advance(7 * time.Minute)
	h.login2FA(map[string]any{"challenge_token": tok, "code": h.totp(secret)}, fromIP("203.0.113.9")).apiError(http.StatusUnauthorized, CodeUnauthorized)
	tok = challengeOf(t, h.login(a.User.Email, storetest.Password, fromIP("203.0.113.9")))
	h.login2FA(map[string]any{"challenge_token": tok, "code": h.totp(secret)}, fromIP("203.0.113.9")).status(http.StatusOK)
}

func TestPasswordThrottleMailsOncePerHour(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withEmail(), withLimits(Limits{LoginIP: Limit{Rate: 1000, Burst: 1000}, LoginFailures: 2, LoginWindow: 10 * time.Minute}))
	u := storetest.AdminUser(t, h.Store, domain.RoleAdmin)
	for _, ip := range []string{"198.51.100.1", "198.51.100.2"} {
		for range 2 {
			h.login(u.Email, "wrong-password", fromIP(ip)).apiError(http.StatusUnauthorized, CodeUnauthorized)
		}
		h.login(u.Email, storetest.Password, fromIP(ip)).apiError(http.StatusTooManyRequests, CodeRateLimited)
	}
	mails := h.emails(notify.KindLoginBlocked)
	require.Len(t, mails, 1, "at most one per account per hour")
	assert.Equal(t, u.Email, mails[0].To)
	assert.Contains(t, mails[0].BodyText, "198.51.100.1")
	assert.NotContains(t, mails[0].BodyText, "wrong-password")

	// An unknown e-mail never mails anybody.
	for range 3 {
		h.login("nobody@example.com", "x-password", fromIP("198.51.100.3"))
	}
	assert.Len(t, h.emails(notify.KindLoginBlocked), 1)

	// An hour later it may mail again.
	h.Clock.Advance(61 * time.Minute)
	for range 2 {
		h.login(u.Email, "wrong-password", fromIP("198.51.100.4"))
	}
	assert.Len(t, h.emails(notify.KindLoginBlocked), 2)
}

func TestTwoFactorManagement(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withEmail())
	a := h.newAdmin(domain.RoleViewer)
	_, other := storetest.Session(t, h.Store, a.User.ID)

	// Setup needs the password.
	e := a.post("/auth/2fa/setup", map[string]any{}).apiError(http.StatusBadRequest, CodeInvalidRequest)
	assert.Contains(t, e.Fields, "current_password")
	e = a.post("/auth/2fa/setup", map[string]any{"current_password": "wrong"}).apiError(http.StatusBadRequest, CodeInvalidRequest)
	assert.Equal(t, "is incorrect", e.Fields["current_password"])
	// Enable before setup → 409.
	a.post("/auth/2fa/enable", map[string]any{"code": "123456"}).apiError(http.StatusConflict, CodeConflict)

	setup := a.post("/auth/2fa/setup", map[string]any{"current_password": storetest.Password}).status(http.StatusOK).object()
	first := setup["secret"].(string)
	assert.Regexp(t, `^[A-Z2-7]{32}$`, first)
	assert.Equal(t, auth.OTPAuthURI(a.User.Email, first), setup["otpauth_uri"])
	assert.True(t, strings.HasPrefix(setup["otpauth_uri"].(string), "otpauth://totp/CryptoPay:"))
	// Calling again replaces the pending secret; the first one no longer enables.
	setup = a.post("/auth/2fa/setup", map[string]any{"current_password": storetest.Password}).status(http.StatusOK).object()
	secret := setup["secret"].(string)
	assert.NotEqual(t, first, secret)
	e = a.post("/auth/2fa/enable", map[string]any{"code": h.totp(first)}).apiError(http.StatusBadRequest, CodeInvalidRequest)
	assert.Contains(t, e.Fields, "code")
	// Still pending: me says disabled, the other session works.
	assert.Equal(t, false, a.get("/auth/me").object()["two_factor_enabled"])

	var out struct {
		RecoveryCodes []string `json:"recovery_codes"`
	}
	a.post("/auth/2fa/enable", map[string]any{"code": h.totp(secret)}).status(http.StatusOK).decode(&out)
	require.Len(t, out.RecoveryCodes, 10)
	assert.Equal(t, true, a.get("/auth/me").object()["two_factor_enabled"])
	// Other sessions were signed out, this one stays.
	h.do(http.MethodGet, "/api/admin/auth/me", nil, sessionCookie(other)).apiError(http.StatusUnauthorized, CodeUnauthorized)
	enabled := h.auditLogs("auth.2fa_enable")
	require.Len(t, enabled, 1)
	assert.EqualValues(t, 1, detailOf(t, enabled[0])["other_sessions_revoked"])
	// Enable / setup again → 409.
	h.nextStep()
	a.post("/auth/2fa/enable", map[string]any{"code": h.totp(secret)}).apiError(http.StatusConflict, CodeConflict)
	a.post("/auth/2fa/setup", map[string]any{"current_password": storetest.Password}).apiError(http.StatusConflict, CodeConflict)

	// Regenerate: wrong password / wrong code → field errors; a recovery
	// code is not accepted here.
	e = a.post("/auth/2fa/recovery-codes", map[string]any{"current_password": "nope", "code": h.totp(secret)}).
		apiError(http.StatusBadRequest, CodeInvalidRequest)
	assert.Contains(t, e.Fields, "current_password")
	e = a.post("/auth/2fa/recovery-codes", map[string]any{"current_password": storetest.Password, "code": "111111"}).
		apiError(http.StatusBadRequest, CodeInvalidRequest)
	assert.Contains(t, e.Fields, "code")
	a.post("/auth/2fa/recovery-codes", map[string]any{"current_password": storetest.Password, "code": out.RecoveryCodes[0]}).
		apiError(http.StatusBadRequest, CodeInvalidRequest)
	var regen struct {
		RecoveryCodes []string `json:"recovery_codes"`
	}
	a.post("/auth/2fa/recovery-codes", map[string]any{"current_password": storetest.Password, "code": h.totp(secret)}).
		status(http.StatusOK).decode(&regen)
	require.Len(t, regen.RecoveryCodes, 10)
	// Replay of the same code is refused.
	a.post("/auth/2fa/recovery-codes", map[string]any{"current_password": storetest.Password, "code": h.totp(secret)}).
		apiError(http.StatusBadRequest, CodeInvalidRequest)
	// The old set no longer works, the new one does (to disable).
	h.nextStep()
	e = a.post("/auth/2fa/disable", map[string]any{"current_password": storetest.Password, "code": out.RecoveryCodes[1]}).
		apiError(http.StatusBadRequest, CodeInvalidRequest)
	assert.Contains(t, e.Fields, "code")
	e = a.post("/auth/2fa/disable", map[string]any{"current_password": "nope", "code": regen.RecoveryCodes[1]}).
		apiError(http.StatusBadRequest, CodeInvalidRequest)
	assert.Contains(t, e.Fields, "current_password")
	a.post("/auth/2fa/disable", map[string]any{"current_password": storetest.Password, "code": regen.RecoveryCodes[1]}).
		status(http.StatusNoContent)
	assert.Equal(t, false, a.get("/auth/me").object()["two_factor_enabled"])
	n, err := h.Store.CountRecoveryCodes(h.Ctx, a.User.ID)
	require.NoError(t, err)
	assert.Zero(t, n, "recovery codes removed")
	tot, err := h.Store.GetAdminTOTP(h.Ctx, a.User.ID)
	require.NoError(t, err)
	assert.Empty(t, tot.Secret)
	a.post("/auth/2fa/disable", map[string]any{"current_password": storetest.Password, "code": "123456"}).apiError(http.StatusConflict, CodeConflict)
	a.post("/auth/2fa/recovery-codes", map[string]any{"current_password": storetest.Password, "code": "123456"}).apiError(http.StatusConflict, CodeConflict)
	// Password-only login again.
	h.login(a.User.Email, storetest.Password).status(http.StatusOK)

	for _, action := range []string{"auth.2fa_setup", "auth.2fa_enable", "auth.2fa_recovery_codes", "auth.2fa_disable"} {
		assert.NotEmpty(t, h.auditLogs(action), action)
	}
	audit, mails := h.allAuditText(), h.allEmailText()
	for _, s := range append(append([]string{first, secret}, out.RecoveryCodes...), regen.RecoveryCodes...) {
		assert.NotContains(t, audit, s)
		assert.NotContains(t, mails, s)
	}
	for _, kind := range []string{notify.Kind2FAEnabled, notify.KindRecoveryCodes, notify.Kind2FADisabled} {
		m := h.emails(kind)
		require.Len(t, m, 1, kind)
		assert.Equal(t, a.User.Email, m[0].To)
		assert.True(t, strings.HasPrefix(m[0].Subject, "[CryptoPay] "))
	}
}

func TestTwoFactorEnforcement(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	owner := h.newAdmin(domain.RoleOwner)
	settings := owner.get("/settings").status(http.StatusOK).object()

	// Turning require_2fa on without own 2FA → 409.
	settings["require_2fa"] = true
	e := owner.put("/settings", settings).apiError(http.StatusConflict, CodeConflict)
	assert.Contains(t, e.Message, "two-factor")
	owner.enable2FA()
	owner.put("/settings", settings).status(http.StatusOK)

	// A user without 2FA: only the setup endpoints are reachable.
	user := h.newAdmin(domain.RoleAdmin)
	me := user.get("/auth/me").status(http.StatusOK).object()
	assert.Equal(t, true, me["two_factor_setup_required"])
	blocked := []struct{ method, path string }{
		{http.MethodGet, "/overview"}, {http.MethodGet, "/invoices"}, {http.MethodGet, "/settings"},
		{http.MethodGet, "/auth/sessions"}, {http.MethodPost, "/auth/sessions/revoke-others"},
		{http.MethodPost, "/auth/2fa/disable"}, {http.MethodPost, "/auth/2fa/recovery-codes"},
		{http.MethodGet, "/email/status"}, {http.MethodGet, "/chains"}, {http.MethodPost, "/merchants"},
	}
	for _, b := range blocked {
		r := user.do(b.method, b.path, map[string]any{})
		r.apiError(http.StatusForbidden, CodeTwoFactorSetupRequired)
	}
	user.post("/auth/password", map[string]any{"current_password": storetest.Password, "new_password": storetest.Password}).
		status(http.StatusNoContent)
	user.enable2FA()
	me = user.get("/auth/me").status(http.StatusOK).object()
	assert.Equal(t, false, me["two_factor_setup_required"])
	user.get("/invoices").status(http.StatusOK)

	// Logout is always possible.
	other := h.newAdmin(domain.RoleViewer)
	other.post("/auth/logout", nil).status(http.StatusNoContent)

	// While required, 2FA cannot be turned off.
	h.nextStep()
	user.post("/auth/2fa/disable", map[string]any{"current_password": storetest.Password, "code": "000000"}).
		apiError(http.StatusConflict, CodeConflict)

	// Users with 2FA see two_factor_setup_required false; turning it off again works.
	settings["require_2fa"] = false
	owner.put("/settings", settings).status(http.StatusOK)
	viewer := h.newAdmin(domain.RoleViewer)
	assert.Equal(t, false, viewer.get("/auth/me").object()["two_factor_setup_required"])
	viewer.get("/invoices").status(http.StatusOK)
	changes := h.auditLogs("settings.update")
	require.NotEmpty(t, changes)
	assert.Contains(t, string(changes[0].Detail), "require_2fa")
}

func TestOwnerResetTwoFactor(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withEmail())
	owner := h.newAdmin(domain.RoleOwner)
	target := h.newAdmin(domain.RoleAdmin)
	secret, _ := target.enable2FA()
	_, extra := storetest.Session(t, h.Store, target.User.ID)
	pending := challengeOf(t, h.login(target.User.Email, storetest.Password))

	// Owner only; not yourself; unknown user; only with 2FA.
	admin := h.newAdmin(domain.RoleAdmin)
	admin.del("/users/"+target.User.ID.String()+"/2fa").apiError(http.StatusForbidden, CodeForbidden)
	owner.del("/users/"+owner.User.ID.String()+"/2fa").apiError(http.StatusConflict, CodeConflict)
	owner.del("/users/"+uuid.NewString()+"/2fa").apiError(http.StatusNotFound, CodeNotFound)
	owner.del("/users/"+admin.User.ID.String()+"/2fa").apiError(http.StatusConflict, CodeConflict)

	owner.del("/users/" + target.User.ID.String() + "/2fa").status(http.StatusNoContent)
	// Signed out everywhere, challenge gone, 2FA off.
	target.get("/auth/me").apiError(http.StatusUnauthorized, CodeUnauthorized)
	h.do(http.MethodGet, "/api/admin/auth/me", nil, sessionCookie(extra)).apiError(http.StatusUnauthorized, CodeUnauthorized)
	h.login2FA(map[string]any{"challenge_token": pending, "code": h.totp(secret)}).apiError(http.StatusUnauthorized, CodeUnauthorized)
	h.login(target.User.Email, storetest.Password).status(http.StatusOK)
	u, err := h.Store.GetAdminUser(h.Ctx, target.User.ID)
	require.NoError(t, err)
	assert.False(t, u.TwoFactorEnabled())

	logs := h.auditLogs("user.2fa_reset")
	require.Len(t, logs, 1)
	assert.Equal(t, owner.User.Email, logs[0].ActorEmail)
	assert.EqualValues(t, 2, detailOf(t, logs[0])["sessions_revoked"])
	mails := h.emails(notify.Kind2FAReset)
	require.Len(t, mails, 1)
	assert.Equal(t, target.User.Email, mails[0].To)
	assert.Contains(t, mails[0].BodyText, owner.User.Email)

	// The users list shows the badge field.
	var list itemsJSON[map[string]any]
	owner.get("/users").status(http.StatusOK).decode(&list)
	for _, x := range list.Items {
		if x["id"] == owner.User.ID.String() {
			assert.Equal(t, false, x["two_factor_enabled"])
		}
	}
}

func TestDeactivationClearsChallenges(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	owner := h.newAdmin(domain.RoleOwner)
	u := h.newAdmin(domain.RoleAdmin)
	u.enable2FA()
	challengeOf(t, h.login(u.User.Email, storetest.Password))
	owner.patch("/users/"+u.User.ID.String(), map[string]any{"active": false}).status(http.StatusOK)
	var n int
	require.NoError(t, h.Store.Pool().QueryRow(h.Ctx, `SELECT count(*) FROM admin_login_challenges WHERE user_id = $1`, u.User.ID).Scan(&n))
	assert.Zero(t, n)
	// Password change by an owner clears them too.
	owner.patch("/users/"+u.User.ID.String(), map[string]any{"active": true}).status(http.StatusOK)
	challengeOf(t, h.login(u.User.Email, storetest.Password))
	owner.patch("/users/"+u.User.ID.String(), map[string]any{"password": "brand-new-pass-1"}).status(http.StatusOK)
	require.NoError(t, h.Store.Pool().QueryRow(h.Ctx, `SELECT count(*) FROM admin_login_challenges WHERE user_id = $1`, u.User.ID).Scan(&n))
	assert.Zero(t, n)
	// Deleting the user removes everything (cascade).
	challengeOf(t, h.login(u.User.Email, "brand-new-pass-1"))
	owner.del("/users/" + u.User.ID.String()).status(http.StatusNoContent)
	require.NoError(t, h.Store.Pool().QueryRow(h.Ctx, `SELECT count(*) FROM admin_login_challenges`).Scan(&n))
	assert.Zero(t, n)
}

func TestSessionManagement(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	a := h.newAdmin(domain.RoleViewer)
	h.Clock.Advance(time.Minute)
	s2, tok2 := storetest.Session(t, h.Store, a.User.ID, func(x *domain.AdminSession) { x.IP, x.UserAgent = "198.51.100.20", "Firefox" })
	h.Clock.Advance(time.Minute)
	_, tok3 := storetest.Session(t, h.Store, a.User.ID)
	expired, _ := storetest.Session(t, h.Store, a.User.ID, func(x *domain.AdminSession) { x.ExpiresAt = h.Clock.Now().Add(-time.Second) })
	b := h.newAdmin(domain.RoleViewer)

	var list struct {
		Items []map[string]any `json:"items"`
	}
	a.get("/auth/sessions").status(http.StatusOK).decode(&list)
	require.Len(t, list.Items, 3, "own unexpired sessions only")
	ids := []string{}
	current := 0
	for _, s := range list.Items {
		ids = append(ids, s["id"].(string))
		if s["current"] == true {
			current++
			assert.Equal(t, a.Sess.ID.String(), s["id"])
		}
	}
	assert.Equal(t, 1, current)
	assert.NotContains(t, ids, expired.ID.String())
	assert.NotContains(t, ids, b.Sess.ID.String())
	assert.Equal(t, []string{ids[0], ids[1], ids[2]}, ids)
	assert.Equal(t, "198.51.100.20", list.Items[1]["ip"], "newest first")
	assert.Equal(t, "Firefox", list.Items[1]["user_agent"])

	// Isolation: another user's session → 404, nothing deleted.
	a.del("/auth/sessions/"+b.Sess.ID.String()).apiError(http.StatusNotFound, CodeNotFound)
	b.get("/auth/me").status(http.StatusOK)
	a.del("/auth/sessions/"+uuid.NewString()).apiError(http.StatusNotFound, CodeNotFound)
	a.del("/auth/sessions/not-a-uuid").apiError(http.StatusNotFound, CodeNotFound)

	a.del("/auth/sessions/" + s2.ID.String()).status(http.StatusNoContent)
	h.do(http.MethodGet, "/api/admin/auth/me", nil, sessionCookie(tok2)).apiError(http.StatusUnauthorized, CodeUnauthorized)
	h.do(http.MethodGet, "/api/admin/auth/me", nil, sessionCookie(tok3)).status(http.StatusOK)

	var revoked struct {
		Revoked int `json:"revoked"`
	}
	a.post("/auth/sessions/revoke-others", nil).status(http.StatusOK).decode(&revoked)
	assert.Equal(t, 2, revoked.Revoked, "the third session and the expired one")
	h.do(http.MethodGet, "/api/admin/auth/me", nil, sessionCookie(tok3)).apiError(http.StatusUnauthorized, CodeUnauthorized)
	a.get("/auth/me").status(http.StatusOK)
	b.get("/auth/me").status(http.StatusOK)

	// Deleting the current session signs out (cookie cleared).
	r := a.del("/auth/sessions/" + a.Sess.ID.String()).status(http.StatusNoContent)
	ck := sessionFrom(t, r)
	require.NotNil(t, ck)
	assert.Less(t, ck.MaxAge, 0)
	a.get("/auth/me").apiError(http.StatusUnauthorized, CodeUnauthorized)

	assert.Len(t, h.auditLogs("auth.session_revoke"), 2)
	assert.Len(t, h.auditLogs("auth.sessions_revoke_others"), 1)
	// CSRF applies.
	h.do(http.MethodPost, "/api/admin/auth/sessions/revoke-others", nil, sessionCookie(b.Token)).apiError(http.StatusForbidden, CodeForbidden)
}
