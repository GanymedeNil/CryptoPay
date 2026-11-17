package notify_test

import (
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cryptopay/internal/domain"
	"cryptopay/internal/invoice/invoicetest"
	"cryptopay/internal/notify"
	"cryptopay/internal/store"
	"cryptopay/internal/store/storetest"
)

type env struct {
	*invoicetest.Env
	n *notify.Service
}

func newEnv(t *testing.T, configured bool) *env {
	e := invoicetest.New(t)
	return &env{Env: e, n: notify.New(e.Store, configured, nil, e.Clock.Now)}
}

func (e *env) outbox(kind string) []domain.EmailMessage {
	e.T.Helper()
	p, err := e.Store.ListEmails(e.Ctx, store.EmailFilter{Kind: kind, PageSize: store.MaxPageSize})
	require.NoError(e.T, err)
	return p.Items
}

func (e *env) alertsOn(recipients ...string) {
	e.Settings(func(s *domain.Settings) {
		s.AlertEmails = true
		s.AlertRecipients = recipients
	})
}

func TestAlertsOffByDefaultAndWhenUnconfigured(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	storetest.AdminUser(t, e.Store, domain.RoleOwner)
	ev := domain.WebhookEvent{ID: uuid.New(), MerchantID: e.Merchant.ID, InvoiceID: uuid.New(), EventType: domain.EventPaid, Attempts: 10}
	e.n.WebhookFailed(e.Ctx, ev)
	e.n.ScannerFailing(e.Ctx, "ethereum", e.Clock.Now().Add(-time.Hour), "boom")
	assert.Empty(t, e.outbox(""), "alert_emails defaults to off")

	off := newEnv(t, false)
	off.alertsOn("ops@example.com")
	off.n.WebhookFailed(off.Ctx, ev)
	off.n.ScannerFailing(off.Ctx, "ethereum", off.Clock.Now(), "boom")
	assert.Empty(t, off.outbox(""), "e-mail not configured")
	assert.False(t, off.n.Enabled())
	var nilSvc *notify.Service
	assert.False(t, nilSvc.Enabled())
}

func TestWebhookFailedAlert(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	o1 := storetest.AdminUser(t, e.Store, domain.RoleOwner)
	o2 := storetest.AdminUser(t, e.Store, domain.RoleOwner)
	storetest.AdminUser(t, e.Store, domain.RoleOwner, func(u *domain.AdminUser) { u.Active = false })
	storetest.AdminUser(t, e.Store, domain.RoleAdmin)
	e.alertsOn() // [] = every active owner
	ev := domain.WebhookEvent{ID: uuid.New(), MerchantID: e.Merchant.ID, InvoiceID: uuid.New(), EventType: domain.EventPaid,
		URL: "https://merchant.example/hooks/SECRET-TOKEN?key=abc", Attempts: 10, LastError: "HTTP 500", Status: domain.WebhookFailed}
	e.n.WebhookFailed(e.Ctx, ev)
	mails := e.outbox(notify.KindWebhookFailed)
	require.Len(t, mails, 2)
	to := []string{mails[0].To, mails[1].To}
	assert.ElementsMatch(t, []string{o1.Email, o2.Email}, to)
	m := mails[0]
	assert.Equal(t, e.Merchant.ID.String(), m.DedupeKey)
	assert.True(t, strings.HasPrefix(m.Subject, "[CryptoPay] "))
	assert.Contains(t, m.Subject, e.Merchant.Name)
	for _, s := range []string{e.Merchant.Name, domain.EventPaid, ev.ID.String(), ev.InvoiceID.String(), "HTTP 500", "https://merchant.example/…",
		"/admin/webhooks?status=failed&merchant_id=" + e.Merchant.ID.String()} {
		assert.Contains(t, m.BodyText, s)
	}
	assert.NotContains(t, m.BodyText+m.BodyHTML, "SECRET-TOKEN", "the webhook URL path/query is masked")
	assert.NotContains(t, m.BodyText+m.BodyHTML, e.Merchant.WebhookSecret)

	// Throttled per merchant per hour.
	e.Clock.Advance(30 * time.Minute)
	e.n.WebhookFailed(e.Ctx, ev)
	assert.Len(t, e.outbox(notify.KindWebhookFailed), 2)
	other := storetest.Merchant(t, e.Store)
	e.n.WebhookFailed(e.Ctx, domain.WebhookEvent{ID: uuid.New(), MerchantID: other.ID, EventType: domain.EventExpired, Attempts: 3})
	assert.Len(t, e.outbox(notify.KindWebhookFailed), 4, "another merchant")
	e.Clock.Advance(31 * time.Minute)
	e.n.WebhookFailed(e.Ctx, ev)
	assert.Len(t, e.outbox(notify.KindWebhookFailed), 6, "an hour later")
}

func TestTransferUnmatchedAlert(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	e.alertsOn("ops@example.com", "oncall@example.com")
	tr := domain.Transfer{ID: uuid.New(), ChainID: "ethereum", AssetID: "usdt-ethereum", TxHash: "0xabc123",
		ToAddress: "0x52908400098527886E0F7030069857D2E4169EE7", Amount: big.NewInt(12_500_000), DetectedAt: e.Clock.Now()}
	e.n.TransferUnmatched(e.Ctx, tr)
	mails := e.outbox(notify.KindTransferUnmatched)
	require.Len(t, mails, 2)
	assert.ElementsMatch(t, []string{"ops@example.com", "oncall@example.com"}, []string{mails[0].To, mails[1].To})
	m := mails[0]
	assert.Equal(t, "ethereum", m.DedupeKey)
	for _, s := range []string{"Ethereum", "12.5 USDT", "0xabc123", "https://etherscan.io/tx/0xabc123", tr.ToAddress} {
		assert.Contains(t, m.BodyText, s)
	}
	// Per chain per hour.
	e.n.TransferUnmatched(e.Ctx, tr)
	assert.Len(t, e.outbox(notify.KindTransferUnmatched), 2)
	e.n.TransferUnmatched(e.Ctx, domain.Transfer{ChainID: "tron", TxHash: "t1", Amount: big.NewInt(5), DetectedAt: e.Clock.Now()})
	tron := e.outbox(notify.KindTransferUnmatched)
	require.Len(t, tron, 4)
	assert.Contains(t, tron[0].BodyText+tron[1].BodyText, "5", "unknown token: base units")
}

func TestInvoiceUnderpaidAlert(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	e.alertsOn("ops@example.com")
	v, q := e.Quoted("10", "USD", "usdt-ethereum")
	e.Credit(q, 4_000_000, domain.TransferConfirmed)
	inv := e.Invoice(v.ID)
	e.n.InvoiceUnderpaid(e.Ctx, inv)
	mails := e.outbox(notify.KindInvoiceUnderpaid)
	require.Len(t, mails, 1)
	m := mails[0]
	assert.Equal(t, inv.ID.String(), m.DedupeKey)
	for _, s := range []string{inv.ID.String(), e.Merchant.Name, "10 USD", "4 / 10 USDT", "/admin/invoices/" + inv.ID.String()} {
		assert.Contains(t, m.BodyText, s)
	}
	// Once per invoice, ever.
	e.Clock.Advance(48 * time.Hour)
	e.n.InvoiceUnderpaid(e.Ctx, inv)
	assert.Len(t, e.outbox(notify.KindInvoiceUnderpaid), 1)
}

func TestScannerFailingAlertAndLocale(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	e.alertsOn("ops@example.com")
	e.Settings(func(s *domain.Settings) { s.EmailLocale = domain.LocaleEN })
	since := e.Clock.Now().Add(-12 * time.Minute)
	e.n.ScannerFailing(e.Ctx, "tron", since, "head: dial tcp: connection refused (https://api.trongrid.io/…)")
	mails := e.outbox(notify.KindScannerFailing)
	require.Len(t, mails, 1)
	m := mails[0]
	assert.Equal(t, "[CryptoPay] Chain scanner failing: Tron", m.Subject)
	assert.Contains(t, m.BodyText, "12 minutes")
	assert.Contains(t, m.BodyText, "connection refused")
	assert.Contains(t, m.BodyText, "UTC")
	// Per chain per 6 hours.
	e.Clock.Advance(5 * time.Hour)
	e.n.ScannerFailing(e.Ctx, "tron", since, "x")
	assert.Len(t, e.outbox(notify.KindScannerFailing), 1)
	e.Clock.Advance(61 * time.Minute)
	e.n.ScannerFailing(e.Ctx, "tron", since, "x")
	assert.Len(t, e.outbox(notify.KindScannerFailing), 2)
}

// TestSecurityContent renders every security kind in both languages.
func TestSecurityContent(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	u := storetest.AdminUser(t, e.Store, domain.RoleAdmin)
	kinds := []string{notify.KindNewLogin, notify.KindLoginBlocked, notify.KindPasswordChanged, notify.Kind2FAEnabled,
		notify.Kind2FADisabled, notify.Kind2FAReset, notify.KindRecoveryCodes, notify.KindRecoveryCodeUsed,
		notify.KindAccountCreated, notify.KindRoleChanged, notify.KindAccountDeactivated, notify.KindAccountReactivated}
	for _, loc := range []string{domain.LocaleZH, domain.LocaleEN} {
		e.Settings(func(s *domain.Settings) { s.EmailLocale = loc })
		for _, k := range kinds {
			e.Step()
			err := e.n.Security(e.Ctx, e.Store, u, notify.Event{Kind: k, IP: "203.0.113.5", UserAgent: "Agent/1",
				Actor: "boss@example.com", Role: domain.RoleViewer, Remaining: 3})
			require.NoError(t, err, k)
		}
	}
	for _, k := range kinds {
		mails := e.outbox(k)
		want := 2
		if k == notify.KindLoginBlocked {
			want = 1 // throttled: one per account per hour
		}
		require.Len(t, mails, want, k)
		for _, m := range mails {
			assert.Equal(t, u.Email, m.To, k)
			assert.True(t, strings.HasPrefix(m.Subject, "[CryptoPay] "), k)
			assert.Contains(t, m.BodyText, u.Email, k)
			assert.NotEmpty(t, m.BodyHTML, k)
			assert.True(t, strings.Contains(m.BodyText, "UTC"), k)
		}
	}
	en := e.outbox(notify.KindRecoveryCodeUsed)[0]
	assert.Contains(t, en.BodyText, "3 unused codes are left")
	assert.Contains(t, e.outbox(notify.KindNewLogin)[0].BodyText, "203.0.113.5")
	assert.Contains(t, e.outbox(notify.KindNewLogin)[1].BodyText, "IP 地址")
	require.Error(t, e.n.Security(e.Ctx, e.Store, u, notify.Event{Kind: "security.unknown"}))

	// HTML escapes values.
	u.Email = "x<script>@example.com"
	require.NoError(t, e.n.Security(e.Ctx, e.Store, u, notify.Event{Kind: notify.KindPasswordChanged, UserAgent: "<b>"}))
	m := e.outbox(notify.KindPasswordChanged)[0]
	assert.NotContains(t, m.BodyHTML, "<script>")
	assert.Contains(t, m.BodyHTML, "&lt;script&gt;")

	// Inactive users get only the deactivation notice; toggles are honoured.
	u.Active = false
	before := len(e.outbox(""))
	require.NoError(t, e.n.Security(e.Ctx, e.Store, u, notify.Event{Kind: notify.KindRoleChanged}))
	assert.Len(t, e.outbox(""), before)
	require.NoError(t, e.n.Security(e.Ctx, e.Store, u, notify.Event{Kind: notify.KindAccountDeactivated}))
	assert.Len(t, e.outbox(""), before+1)
	u.Active = true
	e.Settings(func(s *domain.Settings) { s.SecurityEmails = false })
	require.NoError(t, e.n.Security(e.Ctx, e.Store, u, notify.Event{Kind: notify.KindRoleChanged}))
	assert.Len(t, e.outbox(""), before+1)
}

func TestFormatTimeAndTestMessage(t *testing.T) {
	at := time.Date(2026, 11, 17, 8, 30, 5, 0, time.FixedZone("CST", 8*3600))
	assert.Equal(t, "2026年11月17日 00:30:05（UTC）", notify.FormatTime(domain.LocaleZH, at))
	assert.Equal(t, "Tue, 17 Nov 2026 00:30:05 UTC", notify.FormatTime(domain.LocaleEN, at))
	m := notify.TestMessage(domain.LocaleEN, "ops@example.com", "boss@example.com", "smtp.example.com", at)
	assert.Equal(t, "ops@example.com", m.To)
	assert.Equal(t, "[CryptoPay] Test e-mail", m.Subject)
	assert.Contains(t, m.Text, "smtp.example.com")
	assert.Contains(t, m.HTML, "boss@example.com")
	assert.Equal(t, "[CryptoPay] 测试邮件", notify.TestMessage(domain.LocaleZH, "a@b", "c", "h", at).Subject)
}
