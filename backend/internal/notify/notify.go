// Package notify decides which e-mails the gateway sends, to whom and when,
// renders them (Chinese or English, Settings.EmailLocale) and puts them into
// the e-mail outbox, which mailer.Dispatcher delivers.
//
//   - Security notifications go to the affected admin's own address when
//     Settings.SecurityEmails is on (Security). They are enqueued on the
//     transaction of the change they report.
//   - Operational alerts go to Settings.AlertRecipients (or every active
//     owner) when Settings.AlertEmails is on. Service implements
//     alert.Notifier for them; each kind is throttled with the outbox's
//     dedupe key so a storm cannot flood a mailbox.
//
// Nothing is enqueued while e-mail is not configured (SMTP_HOST/SMTP_FROM).
// No mail ever contains a password, secret, code or token.
package notify

import (
	"context"
	"fmt"
	"log/slog"
	"math/big"
	"net/url"
	"strings"
	"time"

	"cryptopay/internal/alert"
	"cryptopay/internal/domain"
	"cryptopay/internal/mailer"
	"cryptopay/internal/money"
	"cryptopay/internal/store"
	"cryptopay/internal/view"
)

// E-mail kinds (email_outbox.kind, docs/api.md "E-mail").
const (
	KindNewLogin           = "security.new_login"
	KindLoginBlocked       = "security.login_blocked"
	KindPasswordChanged    = "security.password_changed"
	Kind2FAEnabled         = "security.2fa_enabled"
	Kind2FADisabled        = "security.2fa_disabled"
	Kind2FAReset           = "security.2fa_reset"
	KindRecoveryCodes      = "security.recovery_codes"
	KindRecoveryCodeUsed   = "security.recovery_code_used"
	KindAccountCreated     = "security.account_created"
	KindRoleChanged        = "security.role_changed"
	KindAccountDeactivated = "security.account_deactivated"
	KindAccountReactivated = "security.account_reactivated"

	KindWebhookFailed     = "alert.webhook_failed"
	KindTransferUnmatched = "alert.transfer_unmatched"
	KindInvoiceUnderpaid  = "alert.invoice_underpaid"
	KindScannerFailing    = "alert.scanner_failing"
)

// Throttles (docs/api.md "E-mail").
const (
	LoginBlockedEvery      = time.Hour
	WebhookFailedEvery     = time.Hour
	TransferUnmatchedEvery = time.Hour
	ScannerFailingEvery    = 6 * time.Hour
	// ScannerFailingAfter is how long a chain must fail before it is reported.
	ScannerFailingAfter = 10 * time.Minute
)

// Service enqueues notification e-mails. Create it with New.
type Service struct {
	store   *store.Store
	enabled bool
	log     *slog.Logger
	now     func() time.Time
}

var _ alert.Notifier = (*Service)(nil)

// New returns a Service. enabled is whether e-mail is configured
// (config.Config.EmailConfigured); when false every method is a no-op.
func New(st *store.Store, enabled bool, log *slog.Logger, now func() time.Time) *Service {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if now == nil {
		now = time.Now
	}
	return &Service{store: st, enabled: enabled, log: log, now: now}
}

// Enabled reports whether e-mail is configured.
func (s *Service) Enabled() bool { return s != nil && s.enabled }

// ---- security notifications ----

// Event is a security event on one admin's account.
type Event struct {
	Kind      string    // Kind* security constant
	At        time.Time // zero = now
	IP        string    // where the request came from, if relevant
	UserAgent string
	// Actor is the e-mail of the owner who did it to the user; "" when the
	// user did it themselves. ActorCLI marks `gateway reset-2fa`.
	Actor    string
	ActorCLI bool
	Role     domain.Role // new role (account_created, role_changed)
	// Remaining is the number of unused recovery codes (recovery_code_used).
	Remaining int
}

// Security enqueues the notification of ev for user on tx (the store, or the
// transaction of the change so a rolled back change mails nothing). It does
// nothing when e-mail is off, security e-mails are disabled in the settings,
// or the account is inactive (except for the deactivation notice itself).
// security.login_blocked is throttled to one per account per hour.
func (s *Service) Security(ctx context.Context, tx *store.Store, user domain.AdminUser, ev Event) error {
	if !s.Enabled() || strings.TrimSpace(user.Email) == "" {
		return nil
	}
	if !user.Active && ev.Kind != KindAccountDeactivated {
		return nil
	}
	st, err := tx.GetSettings(ctx)
	if err != nil {
		return err
	}
	if !st.SecurityEmails {
		return nil
	}
	if ev.At.IsZero() {
		ev.At = s.now()
	}
	c, err := securityContent(st.EmailLocale, st.CheckoutBaseURL, user, ev)
	if err != nil {
		return err
	}
	r := render(st.EmailLocale, c)
	msg := domain.EmailMessage{Kind: ev.Kind, To: user.Email, Subject: r.Subject, BodyText: r.Text, BodyHTML: r.HTML}
	if ev.Kind == KindLoginBlocked {
		msg.DedupeKey = user.ID.String()
		_, err := tx.EnqueueEmailsOnce(ctx, s.now().Add(-LoginBlockedEvery), []domain.EmailMessage{msg})
		return err
	}
	_, err = tx.EnqueueEmail(ctx, msg)
	return err
}

func securityContent(loc, base string, u domain.AdminUser, ev Event) (content, error) {
	l := lang(loc)
	when := fact{l.time, FormatTime(loc, ev.At)}
	where := []fact{when, {l.ip, ev.IP}, {l.browser, shortUA(ev.UserAgent)}}
	actor := ev.Actor
	if ev.ActorCLI {
		actor = l.cli
	}
	byFact := fact{l.by, actor}
	link := consoleLink(base, "/admin/security")
	c := content{Link: link, LinkLabel: l.security}
	notYou := pick(loc, "如果这不是您本人或您不知情，请立即联系所有者。", "If you did not do this or did not expect it, contact an owner right away.")
	switch ev.Kind {
	case KindNewLogin:
		c.Subject = pick(loc, "来自新 IP 地址的登录", "New sign-in from an unfamiliar IP address")
		c.Lead = pick(loc, fmt.Sprintf("您的 CryptoPay 管理员账户（%s）刚刚从一个以前没有使用过的 IP 地址登录。", u.Email),
			fmt.Sprintf("Your CryptoPay admin account (%s) was just signed in from an IP address it has not used before.", u.Email))
		c.Facts = where
		c.Advice = pick(loc, "如果这是您本人，无需任何操作。如果不是，请立即修改密码、启用两步验证，并在“账户安全”页面中退出其他会话；如果已无法登录，请联系所有者。",
			"If this was you, there is nothing to do. If it was not, change your password right away, turn on two-factor authentication and sign out the other sessions on the Account security page; if you cannot sign in any more, contact an owner.")
	case KindLoginBlocked:
		c.Subject = pick(loc, "登录已被暂时阻止", "Sign-in temporarily blocked")
		c.Lead = pick(loc, fmt.Sprintf("由于多次登录失败，来自下方 IP 地址对您的账户（%s）的登录已被暂时阻止。", u.Email),
			fmt.Sprintf("Sign-in to your account (%s) from the IP address below was temporarily blocked after repeated failed attempts.", u.Email))
		c.Facts = where
		c.Advice = pick(loc, "如果这是您本人，请稍后再试。如果不是，说明有人正在尝试您的密码：请改用一个强密码并启用两步验证。本通知每小时最多发送一次。",
			"If this was you, wait a while and try again. If it was not, someone is trying to guess your password: change it to a strong one and turn on two-factor authentication. This notice is sent at most once per hour.")
	case KindPasswordChanged:
		c.Subject = pick(loc, "您的密码已修改", "Your password was changed")
		if actor == "" {
			c.Lead = pick(loc, fmt.Sprintf("您的账户（%s）的密码已修改，其他设备上的会话已退出。", u.Email),
				fmt.Sprintf("The password of your account (%s) was changed; sessions on other devices were signed out.", u.Email))
		} else {
			c.Lead = pick(loc, fmt.Sprintf("所有者 %s 修改了您的账户（%s）的密码，所有会话均已退出。", actor, u.Email),
				fmt.Sprintf("An owner (%s) changed the password of your account (%s) and signed out all of its sessions.", actor, u.Email))
		}
		c.Facts = []fact{when, {l.ip, ev.IP}, byFact}
		c.Advice = notYou
	case Kind2FAEnabled:
		c.Subject = pick(loc, "两步验证已启用", "Two-factor authentication turned on")
		c.Lead = pick(loc, fmt.Sprintf("您的账户（%s）已启用两步验证。今后登录时，除密码外还需要输入验证器应用中的验证码；其他设备上的会话已退出。", u.Email),
			fmt.Sprintf("Two-factor authentication was turned on for your account (%s). Signing in now requires a code from your authenticator app in addition to the password; sessions on other devices were signed out.", u.Email))
		c.Facts = where[:2]
		c.Advice = pick(loc, "请把恢复码保存在安全的地方。如果这不是您本人，请立即联系所有者。",
			"Keep your recovery codes in a safe place. If this was not you, contact an owner right away.")
	case Kind2FADisabled:
		c.Subject = pick(loc, "两步验证已关闭", "Two-factor authentication turned off")
		c.Lead = pick(loc, fmt.Sprintf("您的账户（%s）已关闭两步验证，现在仅凭密码即可登录。", u.Email),
			fmt.Sprintf("Two-factor authentication was turned off for your account (%s); the password alone now signs in.", u.Email))
		c.Facts = where[:2]
		c.Advice = pick(loc, "如果这不是您本人，请立即修改密码、重新启用两步验证并联系所有者。",
			"If this was not you, change your password, turn two-factor authentication back on and contact an owner right away.")
	case Kind2FAReset:
		c.Subject = pick(loc, "两步验证已被重置", "Two-factor authentication was reset")
		c.Lead = pick(loc, fmt.Sprintf("%s 重置了您的账户（%s）的两步验证：验证器密钥和恢复码已删除，所有会话均已退出。", orUnknown(loc, actor), u.Email),
			fmt.Sprintf("Two-factor authentication of your account (%s) was reset by %s: the authenticator key and the recovery codes were removed and all sessions were signed out.", u.Email, orUnknown(loc, actor)))
		c.Facts = []fact{when, byFact}
		c.Advice = pick(loc, "请在下次登录后重新设置两步验证。如果您没有请求重置，请立即联系所有者。",
			"Set up two-factor authentication again after your next sign-in. If you did not ask for this, contact an owner right away.")
	case KindRecoveryCodes:
		c.Subject = pick(loc, "已生成新的恢复码", "New recovery codes generated")
		c.Lead = pick(loc, fmt.Sprintf("您的账户（%s）生成了一组新的恢复码，之前的恢复码已全部失效。", u.Email),
			fmt.Sprintf("A new set of recovery codes was generated for your account (%s); the previous codes no longer work.", u.Email))
		c.Facts = where[:2]
		c.Advice = pick(loc, "如果这不是您本人，请立即修改密码并联系所有者。", "If this was not you, change your password and contact an owner right away.")
	case KindRecoveryCodeUsed:
		c.Subject = pick(loc, "使用恢复码登录", "Signed in with a recovery code")
		c.Lead = pick(loc, fmt.Sprintf("您的账户（%s）刚刚使用一个恢复码登录。该恢复码已失效，还剩 %d 个可用。", u.Email, ev.Remaining),
			fmt.Sprintf("Your account (%s) was just signed in with a recovery code. That code no longer works; %s left.", u.Email,
				plural(ev.Remaining, "%d unused code is", "%d unused codes are")))
		c.Facts = where
		c.Advice = pick(loc, "如果您的验证器设备丢失，请在“账户安全”页面重新生成恢复码或重新设置两步验证。如果这不是您本人，请立即修改密码并联系所有者。",
			"If you lost your authenticator device, generate new recovery codes or set up two-factor authentication again on the Account security page. If this was not you, change your password and contact an owner right away.")
	case KindAccountCreated:
		c.Subject = pick(loc, "已为您创建管理员账户", "An admin account was created for you")
		c.Lead = pick(loc, fmt.Sprintf("%s 为您创建了 CryptoPay 管理员账户（%s），角色：%s。", orUnknown(loc, actor), u.Email, roleName(loc, ev.Role)),
			fmt.Sprintf("%s created a CryptoPay admin account for you (%s) with the role %s.", orUnknown(loc, actor), u.Email, roleName(loc, ev.Role)))
		c.Facts = []fact{when, {l.role, roleName(loc, ev.Role)}, byFact}
		c.Advice = pick(loc, "初始密码请向创建者索取，邮件中不会包含任何密码。首次登录后请修改密码并启用两步验证。如果您不认识此操作，请忽略本邮件并联系管理员。",
			"Ask the person who created it for the initial password; it is never sent by e-mail. After your first sign-in, change the password and turn on two-factor authentication. If you do not recognise this, ignore this message and contact the administrator.")
		c.Link, c.LinkLabel = consoleLink(base, "/admin/login"), l.open
	case KindRoleChanged:
		c.Subject = pick(loc, "您的角色已变更", "Your role was changed")
		c.Lead = pick(loc, fmt.Sprintf("所有者 %s 将您的账户（%s）的角色改为 %s。", orUnknown(loc, actor), u.Email, roleName(loc, ev.Role)),
			fmt.Sprintf("An owner (%s) changed the role of your account (%s) to %s.", orUnknown(loc, actor), u.Email, roleName(loc, ev.Role)))
		c.Facts = []fact{when, {l.role, roleName(loc, ev.Role)}, byFact}
		c.Advice = notYou
	case KindAccountDeactivated:
		c.Subject = pick(loc, "您的账户已停用", "Your account was deactivated")
		c.Lead = pick(loc, fmt.Sprintf("所有者 %s 停用了您的账户（%s），所有会话均已退出。", orUnknown(loc, actor), u.Email),
			fmt.Sprintf("An owner (%s) deactivated your account (%s) and signed out all of its sessions.", orUnknown(loc, actor), u.Email))
		c.Facts = []fact{when, byFact}
		c.Advice = pick(loc, "如有疑问，请联系所有者。", "If you have questions, contact an owner.")
		c.Link = ""
	case KindAccountReactivated:
		c.Subject = pick(loc, "您的账户已重新启用", "Your account was reactivated")
		c.Lead = pick(loc, fmt.Sprintf("所有者 %s 重新启用了您的账户（%s）。", orUnknown(loc, actor), u.Email),
			fmt.Sprintf("An owner (%s) reactivated your account (%s).", orUnknown(loc, actor), u.Email))
		c.Facts = []fact{when, byFact}
		c.Advice = notYou
		c.Link, c.LinkLabel = consoleLink(base, "/admin/login"), l.open
	default:
		return content{}, fmt.Errorf("notify: unknown security event %q", ev.Kind)
	}
	return c, nil
}

// consoleLink joins the console origin and a path ("" without an origin).
func consoleLink(base, path string) string {
	base = strings.TrimRight(base, "/")
	if base == "" {
		return ""
	}
	return base + path
}

// ---- test e-mail ----

// TestMessage is the content of the "send test e-mail" button.
func TestMessage(loc, to, actor, host string, at time.Time) mailer.Message {
	l := lang(loc)
	c := content{
		Subject: pick(loc, "测试邮件", "Test e-mail"),
		Lead: pick(loc, "这是一封来自 CryptoPay 网关的测试邮件。如果您收到了它，说明邮件发送配置正确。",
			"This is a test message from your CryptoPay gateway. If you received it, e-mail delivery is configured correctly."),
		Facts: []fact{{l.time, FormatTime(loc, at)}, {l.by, actor}, {l.host, host}},
	}
	r := render(loc, c)
	return mailer.Message{To: to, Subject: r.Subject, Text: r.Text, HTML: r.HTML}
}

// ---- operational alerts (alert.Notifier) ----

// alertSettings returns the settings when alert e-mails are wanted.
func (s *Service) alertSettings(ctx context.Context) (domain.Settings, bool) {
	if !s.Enabled() {
		return domain.Settings{}, false
	}
	st, err := s.store.GetSettings(ctx)
	if err != nil {
		s.log.ErrorContext(ctx, "notify: load settings", "error", err)
		return domain.Settings{}, false
	}
	return st, st.AlertEmails
}

func (s *Service) recipients(ctx context.Context, st domain.Settings) ([]string, error) {
	if len(st.AlertRecipients) > 0 {
		return st.AlertRecipients, nil
	}
	return s.store.ListActiveOwnerEmails(ctx)
}

// sendAlert enqueues c to every recipient unless (kind, key) was mailed
// after now−every (every = 0: ever).
func (s *Service) sendAlert(ctx context.Context, st domain.Settings, kind, key string, every time.Duration, build func() (content, error)) {
	since := time.Time{}
	if every > 0 {
		since = s.now().Add(-every)
	}
	// Cheap pre-check before rendering; EnqueueEmailsOnce decides for real.
	if dup, err := s.store.EmailCreatedSince(ctx, kind, key, since); err != nil {
		s.log.ErrorContext(ctx, "notify: throttle check", "kind", kind, "error", err)
		return
	} else if dup {
		return
	}
	to, err := s.recipients(ctx, st)
	if err != nil {
		s.log.ErrorContext(ctx, "notify: recipients", "kind", kind, "error", err)
		return
	}
	if len(to) == 0 {
		s.log.WarnContext(ctx, "notify: no alert recipients (no active owner)", "kind", kind)
		return
	}
	c, err := build()
	if err != nil {
		s.log.ErrorContext(ctx, "notify: build alert", "kind", kind, "error", err)
		return
	}
	r := render(st.EmailLocale, c)
	msgs := make([]domain.EmailMessage, 0, len(to))
	for _, addr := range to {
		msgs = append(msgs, domain.EmailMessage{Kind: kind, DedupeKey: key, To: addr, Subject: r.Subject, BodyText: r.Text, BodyHTML: r.HTML})
	}
	if _, err := s.store.EnqueueEmailsOnce(ctx, since, msgs); err != nil {
		s.log.ErrorContext(ctx, "notify: enqueue alert", "kind", kind, "error", err)
	}
}

// WebhookFailed mails that a webhook event exhausted its retries (one per
// merchant per hour).
func (s *Service) WebhookFailed(ctx context.Context, e domain.WebhookEvent) {
	st, ok := s.alertSettings(ctx)
	if !ok {
		return
	}
	s.sendAlert(ctx, st, KindWebhookFailed, e.MerchantID.String(), WebhookFailedEvery, func() (content, error) {
		loc, l := st.EmailLocale, lang(st.EmailLocale)
		name := e.MerchantID.String()
		if m, err := s.store.GetMerchant(ctx, e.MerchantID); err == nil {
			name = m.Name
		}
		return content{
			Subject: pick(loc, "Webhook 投递失败："+name, "Webhook delivery failed: "+name),
			Lead: pick(loc, fmt.Sprintf("商户 %s 的一个 Webhook 事件在尝试 %d 次后仍未投递成功，已标记为失败，不会再自动重试。", name, e.Attempts),
				fmt.Sprintf("A webhook event of merchant %s could not be delivered after %d attempts and was marked as failed; it will not be retried automatically.", name, e.Attempts)),
			Facts: []fact{
				{l.time, FormatTime(loc, s.now())}, {l.merchant, name}, {l.event, e.EventType}, {l.eventID, e.ID.String()},
				{l.invoice, e.InvoiceID.String()}, {l.url, maskURL(e.URL)}, {l.attempts, fmt.Sprint(e.Attempts)},
				{l.lastError, e.LastError},
			},
			Advice: pick(loc, "请检查该商户的 Webhook 地址是否可以访问；修复后可在管理后台的 Webhooks 页面中重试该事件。本提醒每个商户每小时最多发送一次。",
				"Check that the merchant's webhook endpoint is reachable; once it is fixed, retry the event on the Webhooks page of the admin console. This alert is sent at most once per merchant per hour."),
			Link:      consoleLink(st.CheckoutBaseURL, "/admin/webhooks?status=failed&merchant_id="+e.MerchantID.String()),
			LinkLabel: l.open,
		}, nil
	})
}

// TransferUnmatched mails that a transfer could not be matched (one per
// chain per hour).
func (s *Service) TransferUnmatched(ctx context.Context, t domain.Transfer) {
	st, ok := s.alertSettings(ctx)
	if !ok {
		return
	}
	s.sendAlert(ctx, st, KindTransferUnmatched, t.ChainID, TransferUnmatchedEvery, func() (content, error) {
		loc, l := st.EmailLocale, lang(st.EmailLocale)
		chainName, explorer := t.ChainID, ""
		if c, err := s.store.GetChain(ctx, t.ChainID); err == nil {
			chainName, explorer = c.Name, view.ExplorerTxURL(c, t.TxHash)
		}
		return content{
			Subject: pick(loc, "收到无法匹配的转账（"+chainName+"）", "Unmatched transfer on "+chainName),
			Lead: pick(loc, fmt.Sprintf("在 %s 上收到一笔无法自动匹配到任何账单的转账，需要人工处理。", chainName),
				fmt.Sprintf("A transfer on %s could not be matched to any invoice and needs manual review.", chainName)),
			Facts: []fact{
				{l.time, FormatTime(loc, t.DetectedAt)}, {l.chain, chainName}, {l.amount, s.transferAmount(ctx, loc, t)},
				{l.to, t.ToAddress}, {l.tx, t.TxHash}, {l.explorer, explorer},
			},
			Advice: pick(loc, "可在管理后台的“转账”页面中把它分配给对应的账单。本提醒每条链每小时最多发送一次，期间的其他未匹配转账请在后台查看。",
				"Assign it to the right invoice on the Transfers page of the admin console. This alert is sent at most once per chain per hour; look for further unmatched transfers in the console."),
			Link:      consoleLink(st.CheckoutBaseURL, "/admin/transfers?matched=false&chain_id="+url.QueryEscape(t.ChainID)),
			LinkLabel: l.open,
		}, nil
	})
}

func (s *Service) transferAmount(ctx context.Context, loc string, t domain.Transfer) string {
	amount := t.Amount
	if amount == nil {
		amount = new(big.Int)
	}
	if t.AssetID != "" {
		if a, err := s.store.GetAsset(ctx, t.AssetID); err == nil {
			return money.Format(amount, a.Decimals) + " " + a.Symbol
		}
	}
	return amount.String() + lang(loc).unknownT
}

// InvoiceUnderpaid mails that an invoice became underpaid (once per invoice).
func (s *Service) InvoiceUnderpaid(ctx context.Context, inv domain.Invoice) {
	st, ok := s.alertSettings(ctx)
	if !ok {
		return
	}
	s.sendAlert(ctx, st, KindInvoiceUnderpaid, inv.ID.String(), 0, func() (content, error) {
		loc, l := st.EmailLocale, lang(st.EmailLocale)
		v, err := s.store.GetInvoiceView(ctx, inv.ID)
		if err != nil {
			return content{}, err
		}
		received := ""
		if v.Quote != nil {
			if a, err := s.store.GetAsset(ctx, v.Quote.AssetID); err == nil {
				received = money.Format(nz(v.Quote.AmountConfirmed), a.Decimals) + " / " + money.Format(nz(v.Quote.AmountDue), a.Decimals) + " " + a.Symbol
			}
		}
		return content{
			Subject: pick(loc, "账单少付："+v.MerchantName, "Invoice underpaid: "+v.MerchantName),
			Lead: pick(loc, fmt.Sprintf("商户 %s 的账单 %s 已过期，收到的金额少于应付金额，状态为“少付”（underpaid）。", v.MerchantName, inv.ID),
				fmt.Sprintf("Invoice %s of merchant %s expired with less than the amount due received; it is now underpaid.", inv.ID, v.MerchantName)),
			Facts: []fact{
				{l.time, FormatTime(loc, s.now())}, {l.invoice, inv.ID.String()}, {l.merchant, v.MerchantName},
				{l.orderID, v.MerchantOrderID}, {l.price, v.PriceAmount.String() + " " + v.PriceCurrency}, {l.received, received},
			},
			Advice: pick(loc, "请与客户联系补足差额，或在管理后台中将该账单手动标记为已付款。宽限期内补付的款项仍会被自动入账。",
				"Contact the customer about the difference, or mark the invoice as paid manually in the admin console. Payments that arrive within the grace period are still credited automatically."),
			Link:      consoleLink(st.CheckoutBaseURL, "/admin/invoices/"+inv.ID.String()),
			LinkLabel: l.open,
		}, nil
	})
}

// ScannerFailing mails that a chain's scanner keeps failing (one per chain
// per 6 hours).
func (s *Service) ScannerFailing(ctx context.Context, chainID string, since time.Time, lastError string) {
	st, ok := s.alertSettings(ctx)
	if !ok {
		return
	}
	s.sendAlert(ctx, st, KindScannerFailing, chainID, ScannerFailingEvery, func() (content, error) {
		loc, l := st.EmailLocale, lang(st.EmailLocale)
		name := chainID
		if c, err := s.store.GetChain(ctx, chainID); err == nil {
			name = c.Name
		}
		minutes := int(s.now().Sub(since) / time.Minute)
		return content{
			Subject: pick(loc, "链扫描持续失败："+name, "Chain scanner failing: "+name),
			Lead: pick(loc, fmt.Sprintf("%s 的扫描器已连续失败 %d 分钟。在此期间到账的付款要等扫描恢复后才会被检测到（不会跳过任何区块）。", name, minutes),
				fmt.Sprintf("The scanner of %s has been failing for %d minutes. Payments arriving meanwhile are detected once it recovers (no block is skipped).", name, minutes)),
			Facts: []fact{{l.chain, name}, {l.since, FormatTime(loc, since)}, {l.error_, lastError}},
			Advice: pick(loc, "请检查该链的 RPC 节点配置，可在管理后台的“链”页面中测试 RPC。恢复后扫描会自动从中断处继续。本提醒每条链每 6 小时最多发送一次。",
				"Check the chain's RPC endpoints; the Chains page of the admin console can test them. Scanning resumes where it stopped once the endpoint works again. This alert is sent at most once per chain per 6 hours."),
			Link:      consoleLink(st.CheckoutBaseURL, "/admin/chains"),
			LinkLabel: l.open,
		}, nil
	})
}

func nz(v *big.Int) *big.Int {
	if v == nil {
		return new(big.Int)
	}
	return v
}

// maskURL keeps scheme and host of a webhook URL (paths and queries may
// carry tokens).
func maskURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	out := u.Scheme + "://" + u.Host
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
		out += "/…"
	}
	return out
}
