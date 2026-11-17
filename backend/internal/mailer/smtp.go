package mailer

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"cryptopay/internal/config"
)

// DefaultSMTPTimeout bounds one delivery (connect, TLS, auth and data).
const DefaultSMTPTimeout = 30 * time.Second

// SMTP sends messages through an SMTP server. Create it with NewSMTP.
type SMTP struct {
	host      string
	addr      string
	mode      string
	username  string
	password  string
	from      *mail.Address
	timeout   time.Duration
	tlsConfig *tls.Config
	localName string
	now       func() time.Time
}

// SMTPOption configures an SMTP sender.
type SMTPOption func(*SMTP)

// WithTimeout bounds one delivery (default DefaultSMTPTimeout).
func WithTimeout(d time.Duration) SMTPOption {
	return func(s *SMTP) {
		if d > 0 {
			s.timeout = d
		}
	}
}

// WithTLSConfig replaces the TLS configuration (tests use it to trust a
// self-signed certificate). ServerName defaults to the SMTP host.
func WithTLSConfig(c *tls.Config) SMTPOption { return func(s *SMTP) { s.tlsConfig = c } }

// WithClock sets the clock of the Date header (default time.Now).
func WithClock(now func() time.Time) SMTPOption {
	return func(s *SMTP) {
		if now != nil {
			s.now = now
		}
	}
}

// NewSMTP returns a sender for cfg (which config.LoadFrom validated).
func NewSMTP(cfg config.SMTPConfig, opts ...SMTPOption) (*SMTP, error) {
	if cfg.Host == "" || cfg.From == "" {
		return nil, errors.New("mailer: SMTP host and sender are required")
	}
	from, err := mail.ParseAddress(cfg.From)
	if err != nil {
		return nil, fmt.Errorf("mailer: invalid sender %q: %w", cfg.From, err)
	}
	port := cfg.Port
	if port == 0 {
		port = config.DefaultSMTPPort
	}
	mode := cfg.TLS
	if mode == "" {
		mode = config.SMTPStartTLS
	}
	switch mode {
	case config.SMTPStartTLS, config.SMTPTLS, config.SMTPNoTLS:
	default:
		return nil, fmt.Errorf("mailer: unknown TLS mode %q", mode)
	}
	s := &SMTP{
		host: cfg.Host, addr: net.JoinHostPort(cfg.Host, strconv.Itoa(port)), mode: mode,
		username: cfg.Username, password: cfg.Password, from: from,
		timeout: DefaultSMTPTimeout, localName: "localhost", now: time.Now,
	}
	for _, o := range opts {
		o(s)
	}
	return s, nil
}

// From returns the sender address as configured.
func (s *SMTP) From() string { return s.from.String() }

// Host returns the SMTP host (no port, no credentials).
func (s *SMTP) Host() string { return s.host }

func (s *SMTP) tlsCfg() *tls.Config {
	c := &tls.Config{MinVersion: tls.VersionTLS12}
	if s.tlsConfig != nil {
		c = s.tlsConfig.Clone()
	}
	if c.ServerName == "" {
		c.ServerName = s.host
	}
	return c
}

// Send delivers m. With mode starttls the server must offer STARTTLS: the
// message is never sent in clear text instead. Credentials are only sent
// over TLS (or to a loopback server). Errors never contain the password.
func (s *SMTP) Send(ctx context.Context, m Message) (err error) {
	to, err := ParseRecipient(m.To)
	if err != nil {
		return err
	}
	raw, err := Build(s.from, m, s.now())
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			err = errors.New(s.sanitize(err.Error()))
		}
	}()

	deadline := time.Now().Add(s.timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	dialer := &net.Dialer{Deadline: deadline}
	var conn net.Conn
	if s.mode == config.SMTPTLS {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: s.tlsCfg()}).DialContext(ctx, "tcp", s.addr)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", s.addr)
	}
	if err != nil {
		return fmt.Errorf("connect to %s: %w", s.addr, err)
	}
	if err := conn.SetDeadline(deadline); err != nil {
		conn.Close()
		return err
	}
	// Abort a blocked exchange when ctx is cancelled.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	c, err := smtp.NewClient(conn, s.host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("smtp greeting: %w", err)
	}
	defer c.Close()
	if err := c.Hello(s.localName); err != nil {
		return fmt.Errorf("smtp EHLO: %w", err)
	}
	if s.mode == config.SMTPStartTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("the SMTP server does not offer STARTTLS; refusing to send in clear text (set SMTP_TLS=tls or none if intended)")
		}
		if err := c.StartTLS(s.tlsCfg()); err != nil {
			return fmt.Errorf("smtp STARTTLS: %w", err)
		}
	}
	if s.username != "" {
		if ok, _ := c.Extension("AUTH"); !ok {
			return errors.New("the SMTP server does not offer authentication (AUTH)")
		}
		if err := c.Auth(smtp.PlainAuth("", s.username, s.password, s.host)); err != nil {
			return fmt.Errorf("smtp AUTH: %w", err)
		}
	}
	if err := c.Mail(s.from.Address); err != nil {
		return fmt.Errorf("smtp MAIL FROM: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("smtp RCPT TO: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp DATA: %w", err)
	}
	if _, err := w.Write(raw); err != nil {
		return fmt.Errorf("smtp DATA: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp DATA: %w", err)
	}
	if err := c.Quit(); err != nil && ctx.Err() == nil {
		// The message was accepted at the end of DATA; a failed QUIT does
		// not undo that.
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}

// MaxErrorLen bounds error texts kept in the outbox / shown in the console.
const MaxErrorLen = 500

// sanitize removes credentials from an error text and bounds its length.
func (s *SMTP) sanitize(msg string) string {
	return SanitizeError(msg, s.password)
}

// SanitizeError replaces every non-empty secret in msg, collapses line
// breaks and truncates the result to MaxErrorLen bytes (on a rune boundary).
func SanitizeError(msg string, secrets ...string) string {
	for _, sec := range secrets {
		if sec != "" {
			msg = strings.ReplaceAll(msg, sec, "[redacted]")
		}
	}
	msg = strings.Join(strings.Fields(strings.ToValidUTF8(msg, "�")), " ")
	if len(msg) > MaxErrorLen {
		msg = strings.ToValidUTF8(msg[:MaxErrorLen], "") // drop a cut rune
	}
	return msg
}
