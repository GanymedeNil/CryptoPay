package api

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cryptopay/internal/domain"
	"cryptopay/internal/notify"
	"cryptopay/internal/store"
	"cryptopay/internal/store/storetest"
)

func TestEmailStatusAndTest(t *testing.T) {
	t.Parallel()
	logs := &logBuffer{}
	h := newHarness(t, withLog(logs), withEmail())
	owner := h.newAdmin(domain.RoleOwner)
	viewer := h.newAdmin(domain.RoleViewer)

	st := viewer.get("/email/status").status(http.StatusOK).object()
	assert.Equal(t, map[string]any{"configured": true, "from": "CryptoPay <no-reply@cryptopay.test>", "host": "smtp.test.example"}, st)
	body := string(viewer.get("/email/status").Body)
	assert.NotContains(t, body, "smtp-secret-pw")
	assert.NotContains(t, body, "mailer")

	// Default recipient: the caller.
	res := owner.post("/email/test", map[string]any{}).status(http.StatusOK).object()
	assert.Equal(t, map[string]any{"ok": true, "error": ""}, res)
	sent := h.Mail.Sent()
	require.Len(t, sent, 1)
	assert.Equal(t, owner.User.Email, sent[0].To)
	assert.True(t, strings.HasPrefix(sent[0].Subject, "[CryptoPay] "))
	assert.Contains(t, sent[0].Text, "smtp.test.example")
	assert.NotEmpty(t, sent[0].HTML)

	owner.post("/email/test", map[string]any{"to": "ops@example.com"}).status(http.StatusOK)
	assert.Equal(t, "ops@example.com", h.Mail.Sent()[1].To)
	e := owner.post("/email/test", map[string]any{"to": "not an address"}).apiError(http.StatusBadRequest, CodeInvalidRequest)
	assert.Contains(t, e.Fields, "to")
	owner.post("/email/test", map[string]any{"to": "a@b.example\r\nBcc: x@y.example"}).apiError(http.StatusBadRequest, CodeInvalidRequest)

	// A failure is reported, sanitized: the SMTP password never shows.
	h.Mail.SetErr(errors.New("smtp AUTH: 535 authentication failed for mailer/smtp-secret-pw\r\nmore"))
	res = owner.post("/email/test", map[string]any{}).status(http.StatusOK).object()
	assert.Equal(t, false, res["ok"])
	assert.Contains(t, res["error"], "535 authentication failed")
	assert.NotContains(t, res["error"], "smtp-secret-pw")
	assert.NotContains(t, res["error"], "\n")
	assert.NotContains(t, logs.String(), "smtp-secret-pw")
	h.Mail.SetErr(nil)

	// Viewers cannot send.
	viewer.post("/email/test", map[string]any{}).apiError(http.StatusForbidden, CodeForbidden)
	logsOf := h.auditLogs("email.test")
	require.Len(t, logsOf, 3)
	assert.Equal(t, false, detailOf(t, logsOf[0])["ok"])
}

func TestEmailNotConfigured(t *testing.T) {
	t.Parallel()
	h := newHarness(t) // no SMTP
	owner := h.newAdmin(domain.RoleOwner)
	assert.Equal(t, map[string]any{"configured": false, "from": "", "host": ""}, owner.get("/email/status").status(http.StatusOK).object())
	owner.post("/email/test", map[string]any{}).apiError(http.StatusConflict, CodeConflict)

	// Nothing is ever queued: security events with notifications on.
	u := owner.post("/users", map[string]any{"email": "new@example.com", "password": "long-enough-1", "role": "viewer"}).
		status(http.StatusCreated).object()
	owner.patch("/users/"+u["id"].(string), map[string]any{"role": "admin"}).status(http.StatusOK)
	owner.enable2FA()
	owner.post("/auth/password", map[string]any{"current_password": storetest.Password, "new_password": "another-pass-1"}).
		status(http.StatusNoContent)
	p, err := h.Store.ListEmails(h.Ctx, storeEmailFilterAll())
	require.NoError(t, err)
	assert.Zero(t, p.Total)
	assert.Zero(t, h.emailKicks.Load())
}

func TestEmailOutboxAPI(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withEmail())
	owner := h.newAdmin(domain.RoleOwner)
	admin := h.newAdmin(domain.RoleAdmin)
	viewer := h.newAdmin(domain.RoleViewer)
	admin.enable2FA()                                                                                     // security.2fa_enabled
	owner.patch("/users/"+admin.User.ID.String(), map[string]any{"role": "viewer"}).status(http.StatusOK) // security.role_changed
	h.Step()

	var page struct {
		Items []map[string]any `json:"items"`
		Total int              `json:"total"`
	}
	owner.get("/email/outbox").status(http.StatusOK).decode(&page)
	require.Equal(t, 2, page.Total)
	assert.Equal(t, notify.KindRoleChanged, page.Items[0]["kind"], "newest first")
	for _, it := range page.Items {
		assert.NotContains(t, it, "body_text", "no bodies in the list")
		assert.Equal(t, admin.User.Email, it["to"])
		assert.Equal(t, "pending", it["status"])
		assert.Nil(t, it["sent_at"])
	}
	owner.get("/email/outbox?kind=" + notify.Kind2FAEnabled).status(http.StatusOK).decode(&page)
	require.Equal(t, 1, page.Total)
	id := page.Items[0]["id"].(string)
	owner.get("/email/outbox?status=failed").status(http.StatusOK).decode(&page)
	assert.Zero(t, page.Total)
	owner.get("/email/outbox?status=bounced").apiError(http.StatusBadRequest, CodeInvalidRequest)
	owner.get("/email/outbox?kind=DROP%20TABLE").apiError(http.StatusBadRequest, CodeInvalidRequest)

	detail := admin.get("/email/outbox/" + id)
	detail.apiError(http.StatusForbidden, CodeForbidden) // admin was demoted to viewer above
	d := owner.get("/email/outbox/" + id).status(http.StatusOK).object()
	assert.Contains(t, d["body_text"], admin.User.Email)
	owner.get("/email/outbox/"+uuid.NewString()).apiError(http.StatusNotFound, CodeNotFound)
	viewer.get("/email/outbox").apiError(http.StatusForbidden, CodeForbidden)

	// Retry: failed and pending are re-queued, sent is refused.
	_, err := h.Store.MarkEmailFailed(h.Ctx, mustUUID(t, id), "550 no such user", h.Clock.Now(), 1)
	require.NoError(t, err)
	kicks := h.emailKicks.Load()
	out := owner.post("/email/outbox/"+id+"/retry", nil).status(http.StatusOK).object()
	assert.Equal(t, "pending", out["status"])
	assert.EqualValues(t, 1, out["attempts"])
	assert.Greater(t, h.emailKicks.Load(), kicks)
	_, err = h.Store.MarkEmailSent(h.Ctx, mustUUID(t, id), h.Clock.Now())
	require.NoError(t, err)
	owner.post("/email/outbox/"+id+"/retry", nil).apiError(http.StatusConflict, CodeConflict)
	owner.post("/email/outbox/"+uuid.NewString()+"/retry", nil).apiError(http.StatusNotFound, CodeNotFound)
	viewer.post("/email/outbox/"+id+"/retry", nil).apiError(http.StatusForbidden, CodeForbidden)
	require.Len(t, h.auditLogs("email.retry"), 1)
}

// TestSecurityNotifications checks every account e-mail: recipient, trigger
// and that none carries a secret.
func TestSecurityNotifications(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withEmail())
	owner := h.newAdmin(domain.RoleOwner)

	// Account created (to the new user, without the password).
	u := owner.post("/users", map[string]any{"email": "Alice@Example.com", "password": "initial-pass-1", "role": "admin"}).
		status(http.StatusCreated).object()
	id := u["id"].(string)
	created := h.emails(notify.KindAccountCreated)
	require.Len(t, created, 1)
	assert.Equal(t, "Alice@Example.com", created[0].To)
	assert.Contains(t, created[0].BodyText, owner.User.Email)
	assert.NotContains(t, created[0].BodyText+created[0].BodyHTML, "initial-pass-1")

	// First sign-in: no "new sign-in" mail; same IP again: none; new IP: one.
	h.login("alice@example.com", "initial-pass-1", fromIP("198.51.100.10")).status(http.StatusOK)
	assert.Empty(t, h.emails(notify.KindNewLogin), "the first sign-in of an account does not mail")
	h.login("alice@example.com", "initial-pass-1", fromIP("198.51.100.10")).status(http.StatusOK)
	assert.Empty(t, h.emails(notify.KindNewLogin))
	h.login("alice@example.com", "initial-pass-1", fromIP("203.0.113.77"), header("User-Agent", "TestBrowser/1.0")).status(http.StatusOK)
	newLogin := h.emails(notify.KindNewLogin)
	require.Len(t, newLogin, 1)
	assert.Equal(t, "Alice@Example.com", newLogin[0].To)
	assert.Contains(t, newLogin[0].BodyText, "203.0.113.77")
	assert.Contains(t, newLogin[0].BodyText, "TestBrowser/1.0")
	assert.Contains(t, newLogin[0].BodyText, "UTC")
	h.login("alice@example.com", "initial-pass-1", fromIP("203.0.113.77")).status(http.StatusOK)
	assert.Len(t, h.emails(notify.KindNewLogin), 1, "seen now")

	// Role change, deactivation, reactivation, password reset by an owner.
	owner.patch("/users/"+id, map[string]any{"role": "viewer"}).status(http.StatusOK)
	owner.patch("/users/"+id, map[string]any{"role": "viewer"}).status(http.StatusOK) // unchanged: no mail
	owner.patch("/users/"+id, map[string]any{"active": false}).status(http.StatusOK)
	owner.patch("/users/"+id, map[string]any{"active": true, "password": "reset-by-owner-1"}).status(http.StatusOK)
	for kind, n := range map[string]int{notify.KindRoleChanged: 1, notify.KindAccountDeactivated: 1,
		notify.KindAccountReactivated: 1, notify.KindPasswordChanged: 1} {
		m := h.emails(kind)
		require.Len(t, m, n, kind)
		assert.Equal(t, "Alice@Example.com", m[0].To, kind)
		assert.Contains(t, m[0].BodyText, owner.User.Email, kind)
	}

	// Own password change (to oneself).
	h.Step()
	owner.post("/auth/password", map[string]any{"current_password": storetest.Password, "new_password": "owner-new-pass-1"}).
		status(http.StatusNoContent)
	pw := h.emails(notify.KindPasswordChanged)
	require.Len(t, pw, 2)
	assert.Equal(t, owner.User.Email, pw[0].To)

	// Owners editing themselves are not mailed about role/active changes.
	before := len(h.emails(""))
	second := h.newAdmin(domain.RoleOwner)
	second.patch("/users/"+second.User.ID.String(), map[string]any{"role": "owner"}).status(http.StatusOK)
	assert.Len(t, h.emails(""), before)

	// Nothing secret anywhere.
	all := h.allEmailText()
	for _, s := range []string{"initial-pass-1", "reset-by-owner-1", "owner-new-pass-1", storetest.Password, "$2a$", "smtp-secret-pw"} {
		assert.NotContains(t, all, s)
	}
	for _, m := range h.emails("") {
		assert.True(t, strings.HasPrefix(m.Subject, "[CryptoPay] "), m.Subject)
		assert.Contains(t, m.BodyHTML, "<html>")
	}

	// security_emails off: nothing is queued any more.
	st := owner.get("/settings").status(http.StatusOK).object()
	st["security_emails"] = false
	owner.put("/settings", st).status(http.StatusOK)
	before = len(h.emails(""))
	owner.patch("/users/"+id, map[string]any{"role": "admin"}).status(http.StatusOK)
	h.login("alice@example.com", "reset-by-owner-1", fromIP("192.0.2.200")).status(http.StatusOK)
	assert.Len(t, h.emails(""), before)

	// English when email_locale is en.
	st["security_emails"], st["email_locale"] = true, "en"
	owner.put("/settings", st).status(http.StatusOK)
	h.Step()
	owner.patch("/users/"+id, map[string]any{"role": "viewer"}).status(http.StatusOK)
	m := h.emails(notify.KindRoleChanged)[0]
	assert.Equal(t, "[CryptoPay] Your role was changed", m.Subject)
	assert.Contains(t, m.BodyText, "viewer")
}

func TestSettingsSecurityAndEmailFields(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	owner := h.newAdmin(domain.RoleOwner)
	st := owner.get("/settings").status(http.StatusOK).object()
	assert.Equal(t, false, st["require_2fa"])
	assert.Equal(t, true, st["security_emails"])
	assert.Equal(t, false, st["alert_emails"])
	assert.Equal(t, []any{}, st["alert_recipients"])
	assert.Equal(t, "zh", st["email_locale"])

	put := func(mod func(map[string]any)) *response {
		body := map[string]any{}
		for k, v := range st {
			body[k] = v
		}
		mod(body)
		return owner.put("/settings", body)
	}
	cases := []struct {
		name  string
		mod   func(map[string]any)
		field string
	}{
		{"missing require_2fa", func(m map[string]any) { delete(m, "require_2fa") }, "require_2fa"},
		{"missing security_emails", func(m map[string]any) { delete(m, "security_emails") }, "security_emails"},
		{"missing alert_emails", func(m map[string]any) { delete(m, "alert_emails") }, "alert_emails"},
		{"missing recipients", func(m map[string]any) { delete(m, "alert_recipients") }, "alert_recipients"},
		{"missing locale", func(m map[string]any) { delete(m, "email_locale") }, "email_locale"},
		{"bad locale", func(m map[string]any) { m["email_locale"] = "de" }, "email_locale"},
		{"bad recipient", func(m map[string]any) { m["alert_recipients"] = []string{"ops@example.com", "nope"} }, "alert_recipients"},
		{"display name", func(m map[string]any) { m["alert_recipients"] = []string{"Ops <ops@example.com>"} }, "alert_recipients"},
		{"duplicate", func(m map[string]any) { m["alert_recipients"] = []string{"ops@example.com", "OPS@example.com"} }, "alert_recipients"},
		{"too many", func(m map[string]any) {
			var many []string
			for i := range 11 {
				many = append(many, "ops"+string(rune('a'+i))+"@example.com")
			}
			m["alert_recipients"] = many
		}, "alert_recipients"},
	}
	for _, tc := range cases {
		e := put(tc.mod).apiError(http.StatusBadRequest, CodeInvalidRequest)
		assert.Contains(t, e.Fields, tc.field, tc.name)
	}
	out := put(func(m map[string]any) {
		m["alert_emails"], m["security_emails"], m["email_locale"] = true, false, "en"
		m["alert_recipients"] = []string{" ops@example.com ", "oncall@example.com"}
	}).status(http.StatusOK).object()
	assert.Equal(t, []any{"ops@example.com", "oncall@example.com"}, out["alert_recipients"])
	assert.Equal(t, "en", out["email_locale"])
	logs := h.auditLogs("settings.update")
	require.NotEmpty(t, logs)
	changes := detailOf(t, logs[0])["changes"].(map[string]any)
	assert.Contains(t, changes, "alert_recipients")
	assert.Contains(t, changes, "email_locale")
	assert.NotContains(t, changes, "require_2fa")
}

func storeEmailFilterAll() store.EmailFilter { return store.EmailFilter{PageSize: store.MaxPageSize} }
