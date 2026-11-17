package mailer

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"io"
	"math/big"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cryptopay/internal/config"
)

// ---- a minimal in-process SMTP server ----

type received struct {
	From   string
	To     []string
	Data   string
	TLS    bool
	AuthAs string
}

type fakeSMTP struct {
	t        *testing.T
	ln       net.Listener
	cert     *tls.Config // server TLS config (STARTTLS / implicit)
	startTLS bool        // advertise STARTTLS
	auth     bool        // advertise AUTH PLAIN
	user     string
	pass     string
	authResp string // answer to AUTH (default: 235 on the right credentials, else 535)
	silent   bool   // accept connections but never greet

	mu   sync.Mutex
	msgs []received
	wg   sync.WaitGroup
}

func newFakeSMTP(t *testing.T, implicitTLS bool, opts func(*fakeSMTP)) *fakeSMTP {
	t.Helper()
	f := &fakeSMTP{t: t, cert: serverTLS(t)}
	if opts != nil {
		opts(f)
	}
	var err error
	if implicitTLS {
		f.ln, err = tls.Listen("tcp", "127.0.0.1:0", f.cert)
	} else {
		f.ln, err = net.Listen("tcp", "127.0.0.1:0")
	}
	require.NoError(t, err)
	f.wg.Go(f.serve)
	t.Cleanup(func() {
		f.ln.Close()
		f.wg.Wait()
	})
	return f
}

func (f *fakeSMTP) port() int { return f.ln.Addr().(*net.TCPAddr).Port }

func (f *fakeSMTP) received() []received {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]received(nil), f.msgs...)
}

func (f *fakeSMTP) serve() {
	for {
		c, err := f.ln.Accept()
		if err != nil {
			return
		}
		f.wg.Go(func() { f.handle(c) })
	}
}

func (f *fakeSMTP) handle(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if f.silent {
		_, _ = io.Copy(io.Discard, conn)
		return
	}
	_, isTLS := conn.(*tls.Conn)
	r := bufio.NewReader(conn)
	w := func(s string) { _, _ = io.WriteString(conn, s+"\r\n") }
	w("220 fake.smtp ESMTP ready")
	var cur received
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		verb := strings.ToUpper(strings.SplitN(line, " ", 2)[0])
		switch verb {
		case "EHLO", "HELO":
			w("250-fake.smtp")
			if f.startTLS && !isTLS {
				w("250-STARTTLS")
			}
			if f.auth {
				w("250-AUTH PLAIN")
			}
			w("250 8BITMIME")
		case "STARTTLS":
			w("220 go ahead")
			tc := tls.Server(conn, f.cert)
			if err := tc.Handshake(); err != nil {
				return
			}
			conn, isTLS, r = tc, true, bufio.NewReader(tc)
			w = func(s string) { _, _ = io.WriteString(conn, s+"\r\n") }
		case "AUTH":
			parts := strings.Fields(line)
			if f.authResp != "" {
				w(f.authResp)
				continue
			}
			if len(parts) == 3 && parts[1] == "PLAIN" {
				raw, _ := base64.StdEncoding.DecodeString(parts[2])
				fields := strings.Split(string(raw), "\x00")
				if len(fields) == 3 && fields[1] == f.user && fields[2] == f.pass {
					cur.AuthAs = fields[1]
					w("235 2.7.0 authenticated")
					continue
				}
			}
			w("535 5.7.8 authentication failed")
		case "MAIL":
			cur.From = angle(line)
			w("250 ok")
		case "RCPT":
			to := angle(line)
			if strings.HasPrefix(to, "reject") {
				w("550 5.1.1 no such user")
				continue
			}
			cur.To = append(cur.To, to)
			w("250 ok")
		case "DATA":
			w("354 end with <CRLF>.<CRLF>")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(strings.TrimPrefix(l, ".")) // dot unstuffing
			}
			cur.Data, cur.TLS = b.String(), isTLS
			f.mu.Lock()
			f.msgs = append(f.msgs, cur)
			f.mu.Unlock()
			cur = received{AuthAs: cur.AuthAs}
			w("250 2.0.0 queued")
		case "RSET", "NOOP":
			w("250 ok")
		case "QUIT":
			w("221 bye")
			return
		default:
			w("502 unknown command")
		}
	}
}

// angle returns the address between < and > of a MAIL / RCPT line.
func angle(line string) string {
	i, j := strings.IndexByte(line, '<'), strings.IndexByte(line, '>')
	if i < 0 || j < i {
		return ""
	}
	return line[i+1 : j]
}

var (
	certOnce sync.Once
	certPEM  *x509.Certificate
	certPair tls.Certificate
)

// serverTLS returns a self-signed certificate for 127.0.0.1 / localhost.
func serverTLS(t *testing.T) *tls.Config {
	t.Helper()
	certOnce.Do(func() {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		require.NoError(t, err)
		tpl := &x509.Certificate{
			SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fake smtp"},
			NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
			IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"localhost"},
			KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}
		der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
		require.NoError(t, err)
		certPEM, err = x509.ParseCertificate(der)
		require.NoError(t, err)
		certPair = tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	})
	return &tls.Config{Certificates: []tls.Certificate{certPair}, MinVersion: tls.VersionTLS12}
}

// trustTest makes the client trust the test certificate.
func trustTest() SMTPOption {
	pool := x509.NewCertPool()
	pool.AddCert(certPEM)
	return WithTLSConfig(&tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12})
}

func smtpCfg(f *fakeSMTP, mode string) config.SMTPConfig {
	return config.SMTPConfig{Host: "127.0.0.1", Port: f.port(), From: "CryptoPay <no-reply@cryptopay.test>", TLS: mode}
}

var testMsg = Message{To: "ops@example.com", Subject: "[CryptoPay] 新的登录 – new sign-in", Text: "Hello\n.dot line\nünïcødé 中文", HTML: "<p>Hello</p>"}

// parsed is a delivered message decoded back.
type parsed struct {
	Header *mail.Message
	Text   string
	HTML   string
}

func parse(t *testing.T, raw string) parsed {
	t.Helper()
	m, err := mail.ReadMessage(strings.NewReader(raw))
	require.NoError(t, err)
	out := parsed{Header: m}
	mt, params, err := mime.ParseMediaType(m.Header.Get("Content-Type"))
	require.NoError(t, err)
	if mt == "text/plain" {
		b, err := io.ReadAll(quotedprintable.NewReader(m.Body))
		require.NoError(t, err)
		out.Text = string(b)
		return out
	}
	require.Equal(t, "multipart/alternative", mt)
	mr := multipart.NewReader(m.Body, params["boundary"])
	for {
		p, err := mr.NextRawPart()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		assert.Equal(t, "quoted-printable", p.Header.Get("Content-Transfer-Encoding"))
		b, err := io.ReadAll(quotedprintable.NewReader(p))
		require.NoError(t, err)
		switch p.Header.Get("Content-Type") {
		case "text/plain; charset=utf-8":
			out.Text = string(b)
		case "text/html; charset=utf-8":
			out.HTML = string(b)
		default:
			t.Fatalf("unexpected part %s", p.Header.Get("Content-Type"))
		}
	}
	return out
}

// ---- tests ----

func TestBuildMessage(t *testing.T) {
	from, _ := mail.ParseAddress("CryptoPay <no-reply@cryptopay.test>")
	at := time.Date(2026, 11, 17, 8, 30, 0, 0, time.UTC)
	raw, err := Build(from, testMsg, at)
	require.NoError(t, err)
	s := string(raw)
	for _, line := range strings.Split(strings.TrimSuffix(s, "\r\n"), "\r\n") {
		assert.LessOrEqual(t, len(line), 998)
	}
	assert.NotContains(t, strings.ReplaceAll(s, "\r\n", ""), "\n", "every line ends in CRLF")
	p := parse(t, s)
	h := p.Header.Header
	assert.Equal(t, `"CryptoPay" <no-reply@cryptopay.test>`, h.Get("From"))
	assert.Equal(t, "<ops@example.com>", h.Get("To"))
	subj, err := new(mime.WordDecoder).DecodeHeader(h.Get("Subject"))
	require.NoError(t, err)
	assert.Equal(t, testMsg.Subject, subj)
	assert.True(t, strings.HasPrefix(h.Get("Subject"), "=?utf-8?b?"), "non-ASCII subject is encoded")
	assert.Equal(t, "Tue, 17 Nov 2026 08:30:00 +0000", h.Get("Date"))
	assert.Regexp(t, `^<[0-9a-f]{32}@cryptopay\.test>$`, h.Get("Message-ID"))
	assert.Equal(t, "1.0", h.Get("MIME-Version"))
	assert.Equal(t, "auto-generated", h.Get("Auto-Submitted"))
	assert.Equal(t, strings.ReplaceAll(testMsg.Text, "\n", "\r\n"), p.Text)
	assert.Equal(t, testMsg.HTML, p.HTML)

	// Two messages never share a Message-ID.
	raw2, err := Build(from, testMsg, at)
	require.NoError(t, err)
	assert.NotEqual(t, h.Get("Message-ID"), parse(t, string(raw2)).Header.Header.Get("Message-ID"))

	// Text only.
	raw, err = Build(from, Message{To: "a@b.example", Subject: "plain", Text: "only text"}, at)
	require.NoError(t, err)
	p = parse(t, string(raw))
	assert.Equal(t, "plain", p.Header.Header.Get("Subject"))
	assert.Equal(t, "only text", p.Text)

	// Refused (permanent).
	for _, m := range []Message{
		{To: "nobody", Subject: "s", Text: "t"},
		{To: "a@b.example\r\nBcc: c@d.example", Subject: "s", Text: "t"},
		{To: "a@b.example", Subject: "s\r\nBcc: c@d.example", Text: "t"},
		{To: "a@b.example", Subject: "s"},
	} {
		_, err := Build(from, m, at)
		require.ErrorIs(t, err, ErrPermanent, "%+v", m)
	}
}

func TestSMTPPlainWithAuth(t *testing.T) {
	f := newFakeSMTP(t, false, func(f *fakeSMTP) { f.auth, f.user, f.pass = true, "mailer", "s3cret" })
	cfg := smtpCfg(f, config.SMTPNoTLS)
	cfg.Username, cfg.Password = "mailer", "s3cret"
	s, err := NewSMTP(cfg)
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1", s.Host())
	require.NoError(t, s.Send(context.Background(), testMsg))
	got := f.received()
	require.Len(t, got, 1)
	assert.Equal(t, "no-reply@cryptopay.test", got[0].From)
	assert.Equal(t, []string{"ops@example.com"}, got[0].To)
	assert.Equal(t, "mailer", got[0].AuthAs)
	assert.False(t, got[0].TLS)
	p := parse(t, got[0].Data)
	assert.Contains(t, p.Text, ".dot line", "dot-stuffing round trips")

	// Wrong password: error, sanitized.
	cfg.Password = "wrong-pass-123"
	s, err = NewSMTP(cfg)
	require.NoError(t, err)
	err = s.Send(context.Background(), testMsg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "535")
	assert.NotContains(t, err.Error(), "wrong-pass-123")
}

func TestSMTPStartTLS(t *testing.T) {
	f := newFakeSMTP(t, false, func(f *fakeSMTP) { f.startTLS, f.auth, f.user, f.pass = true, true, "mailer", "s3cret" })
	cfg := smtpCfg(f, config.SMTPStartTLS)
	cfg.Username, cfg.Password = "mailer", "s3cret"
	s, err := NewSMTP(cfg, trustTest())
	require.NoError(t, err)
	require.NoError(t, s.Send(context.Background(), testMsg))
	got := f.received()
	require.Len(t, got, 1)
	assert.True(t, got[0].TLS, "upgraded before sending")
	assert.Equal(t, "mailer", got[0].AuthAs)

	// The certificate must verify: an untrusted one is refused.
	s, err = NewSMTP(cfg)
	require.NoError(t, err)
	err = s.Send(context.Background(), testMsg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "STARTTLS")
	assert.Len(t, f.received(), 1, "nothing sent")
}

// TestSMTPStartTLSRequired: a server without STARTTLS never gets the
// message (or the credentials) in clear text.
func TestSMTPStartTLSRequired(t *testing.T) {
	f := newFakeSMTP(t, false, func(f *fakeSMTP) { f.auth, f.user, f.pass = true, "mailer", "s3cret" })
	cfg := smtpCfg(f, config.SMTPStartTLS)
	cfg.Username, cfg.Password = "mailer", "s3cret"
	s, err := NewSMTP(cfg, trustTest())
	require.NoError(t, err)
	err = s.Send(context.Background(), testMsg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not offer STARTTLS")
	assert.Empty(t, f.received())
}

func TestSMTPImplicitTLS(t *testing.T) {
	f := newFakeSMTP(t, true, nil)
	s, err := NewSMTP(smtpCfg(f, config.SMTPTLS), trustTest())
	require.NoError(t, err)
	require.NoError(t, s.Send(context.Background(), testMsg))
	got := f.received()
	require.Len(t, got, 1)
	assert.True(t, got[0].TLS)
}

func TestSMTPErrors(t *testing.T) {
	f := newFakeSMTP(t, false, nil)
	s, err := NewSMTP(smtpCfg(f, config.SMTPNoTLS))
	require.NoError(t, err)
	// Rejected recipient.
	err = s.Send(context.Background(), Message{To: "reject@example.com", Subject: "s", Text: "t"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "550")
	// Invalid recipient: permanent, nothing dialled.
	err = s.Send(context.Background(), Message{To: "not-an-address", Subject: "s", Text: "t"})
	require.ErrorIs(t, err, ErrPermanent)
	// AUTH required but not offered.
	cfg := smtpCfg(f, config.SMTPNoTLS)
	cfg.Username, cfg.Password = "u", "p"
	s, err = NewSMTP(cfg)
	require.NoError(t, err)
	err = s.Send(context.Background(), testMsg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authentication")
	// Nothing listening.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	s, err = NewSMTP(config.SMTPConfig{Host: "127.0.0.1", Port: port, From: "a@b.example", TLS: config.SMTPNoTLS})
	require.NoError(t, err)
	require.Error(t, s.Send(context.Background(), testMsg))
	assert.Empty(t, f.received())

	// Bad configuration.
	_, err = NewSMTP(config.SMTPConfig{Host: "h", From: "not an address"})
	require.Error(t, err)
	_, err = NewSMTP(config.SMTPConfig{From: "a@b.example"})
	require.Error(t, err)
	_, err = NewSMTP(config.SMTPConfig{Host: "h", From: "a@b.example", TLS: "ssl"})
	require.Error(t, err)
}

func TestSMTPTimeoutAndCancel(t *testing.T) {
	f := newFakeSMTP(t, false, func(f *fakeSMTP) { f.silent = true })
	s, err := NewSMTP(smtpCfg(f, config.SMTPNoTLS), WithTimeout(300*time.Millisecond))
	require.NoError(t, err)
	start := time.Now()
	require.Error(t, s.Send(context.Background(), testMsg))
	assert.Less(t, time.Since(start), 3*time.Second, "bounded by the timeout")

	s, err = NewSMTP(smtpCfg(f, config.SMTPNoTLS))
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start = time.Now()
	require.Error(t, s.Send(ctx, testMsg))
	assert.Less(t, time.Since(start), 3*time.Second, "bounded by the context")
}

func TestSanitizeError(t *testing.T) {
	assert.Equal(t, "auth failed for [redacted] at host", SanitizeError("auth failed\r\nfor   hunter2\tat host", "hunter2", ""))
	long := strings.Repeat("é", 400)
	out := SanitizeError(long)
	assert.LessOrEqual(t, len(out), MaxErrorLen)
	assert.True(t, strings.HasPrefix(long, out))
	assert.Equal(t, "x", SanitizeError("x", ""))
}
