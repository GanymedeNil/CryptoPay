package notify

import (
	"fmt"
	"html"
	"strings"
	"time"

	"cryptopay/internal/domain"
)

// SubjectPrefix starts every subject.
const SubjectPrefix = "[CryptoPay] "

// content is the locale-specific content of one mail, rendered to a plain
// text and a simple HTML body by render.
type content struct {
	Subject   string // without SubjectPrefix
	Lead      string // what happened
	Facts     []fact // when, from where, ...
	Advice    string // what to do (if it was not you)
	Link      string // optional console link
	LinkLabel string
}

type fact struct{ Label, Value string }

// rendered is a ready mail.
type rendered struct {
	Subject, Text, HTML string
}

func render(loc string, c content) rendered {
	l := lang(loc)
	var t strings.Builder
	t.WriteString(c.Lead + "\n\n")
	width := 0
	for _, f := range c.Facts {
		width = max(width, len([]rune(f.Label)))
	}
	for _, f := range c.Facts {
		if f.Value == "" {
			continue
		}
		t.WriteString(f.Label + l.colon + strings.Repeat(" ", width-len([]rune(f.Label))) + " " + f.Value + "\n")
	}
	if len(c.Facts) > 0 {
		t.WriteString("\n")
	}
	if c.Advice != "" {
		t.WriteString(c.Advice + "\n\n")
	}
	if c.Link != "" {
		t.WriteString(c.LinkLabel + l.colon + " " + c.Link + "\n\n")
	}
	t.WriteString("-- \n" + l.footer + "\n")

	esc := html.EscapeString
	var h strings.Builder
	h.WriteString(`<!doctype html><html><head><meta charset="utf-8"><title>` + esc(SubjectPrefix+c.Subject) + `</title></head>`)
	h.WriteString(`<body style="margin:0;padding:24px;background:#f6f6f4;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Helvetica,Arial,'PingFang SC','Microsoft YaHei',sans-serif;color:#1f2328;">`)
	h.WriteString(`<div style="max-width:560px;margin:0 auto;background:#ffffff;border:1px solid #e3e3df;border-radius:8px;padding:24px;">`)
	h.WriteString(`<p style="margin:0 0 4px;font-size:12px;letter-spacing:.08em;text-transform:uppercase;color:#6b6f76;">CryptoPay</p>`)
	h.WriteString(`<h1 style="margin:0 0 16px;font-size:18px;line-height:1.4;">` + esc(c.Subject) + `</h1>`)
	h.WriteString(`<p style="margin:0 0 16px;font-size:14px;line-height:1.6;">` + esc(c.Lead) + `</p>`)
	if len(c.Facts) > 0 {
		h.WriteString(`<table role="presentation" style="border-collapse:collapse;margin:0 0 16px;font-size:13px;line-height:1.5;">`)
		for _, f := range c.Facts {
			if f.Value == "" {
				continue
			}
			h.WriteString(`<tr><td style="padding:2px 16px 2px 0;color:#6b6f76;vertical-align:top;white-space:nowrap;">` + esc(f.Label) +
				`</td><td style="padding:2px 0;font-family:ui-monospace,SFMono-Regular,Menlo,monospace;word-break:break-all;">` + esc(f.Value) + `</td></tr>`)
		}
		h.WriteString(`</table>`)
	}
	if c.Advice != "" {
		h.WriteString(`<p style="margin:0 0 16px;font-size:14px;line-height:1.6;">` + esc(c.Advice) + `</p>`)
	}
	if c.Link != "" {
		h.WriteString(`<p style="margin:0 0 16px;font-size:14px;"><a href="` + esc(c.Link) + `" style="color:#0b6e4f;">` + esc(c.LinkLabel) + `</a></p>`)
	}
	h.WriteString(`<p style="margin:16px 0 0;padding-top:12px;border-top:1px solid #e3e3df;font-size:12px;color:#6b6f76;">` + esc(l.footer) + `</p>`)
	h.WriteString(`</div></body></html>`)
	return rendered{Subject: SubjectPrefix + c.Subject, Text: t.String(), HTML: h.String()}
}

// strs are the locale-dependent fixed strings.
type strs struct {
	colon, footer                          string
	time, ip, browser, by, role, account   string
	merchant, event, eventID, invoice      string
	orderID, url, attempts, lastError      string
	chain, amount, to, tx, explorer        string
	price, received, since, error_, host   string
	open, security, unknown, cli, unknownT string
}

var zh = strs{
	colon: "：", footer: "这是 CryptoPay 网关自动发送的邮件，请勿直接回复。",
	time: "时间", ip: "IP 地址", browser: "浏览器", by: "操作人", role: "角色", account: "账户",
	merchant: "商户", event: "事件", eventID: "事件 ID", invoice: "账单",
	orderID: "订单号", url: "地址", attempts: "尝试次数", lastError: "最后错误",
	chain: "链", amount: "金额", to: "收款地址", tx: "交易", explorer: "区块浏览器",
	price: "应付", received: "已确认到账", since: "开始时间", error_: "错误", host: "SMTP 服务器",
	open: "在管理后台中查看", security: "打开“账户安全”", unknown: "未知", cli: "服务器命令行（gateway reset-2fa）",
	unknownT: "（未知代币，基础单位）",
}

var en = strs{
	colon: ":", footer: "This message was sent automatically by your CryptoPay gateway. Please do not reply.",
	time: "Time", ip: "IP address", browser: "Browser", by: "By", role: "Role", account: "Account",
	merchant: "Merchant", event: "Event", eventID: "Event ID", invoice: "Invoice",
	orderID: "Order ID", url: "Endpoint", attempts: "Attempts", lastError: "Last error",
	chain: "Chain", amount: "Amount", to: "To address", tx: "Transaction", explorer: "Explorer",
	price: "Amount due", received: "Confirmed", since: "Since", error_: "Error", host: "SMTP server",
	open: "Open in the admin console", security: "Open Account security", unknown: "unknown", cli: "the server command line (gateway reset-2fa)",
	unknownT: " (unknown token, base units)",
}

func lang(loc string) strs {
	if loc == domain.LocaleEN {
		return en
	}
	return zh
}

// FormatTime renders t in UTC in the locale's style.
func FormatTime(loc string, t time.Time) string {
	t = t.UTC()
	if loc == domain.LocaleEN {
		return t.Format("Mon, 2 Jan 2006 15:04:05") + " UTC"
	}
	return t.Format("2006年1月2日 15:04:05") + "（UTC）"
}

func roleName(loc string, r domain.Role) string {
	names := map[domain.Role][2]string{
		domain.RoleOwner:  {"所有者（owner）", "owner"},
		domain.RoleAdmin:  {"管理员（admin）", "admin"},
		domain.RoleViewer: {"只读（viewer）", "viewer"},
	}
	n, ok := names[r]
	if !ok {
		return string(r)
	}
	if loc == domain.LocaleEN {
		return n[1]
	}
	return n[0]
}

func pick(loc, zhText, enText string) string {
	if loc == domain.LocaleEN {
		return enText
	}
	return zhText
}

// shortUA bounds a user agent for display.
func shortUA(ua string) string {
	ua = strings.Join(strings.Fields(ua), " ")
	if r := []rune(ua); len(r) > 160 {
		return string(r[:160]) + "…"
	}
	return ua
}

func orUnknown(loc, s string) string {
	if strings.TrimSpace(s) == "" {
		return lang(loc).unknown
	}
	return s
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf(one, n)
	}
	return fmt.Sprintf(many, n)
}
