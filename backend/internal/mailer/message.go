// Package mailer sends e-mail: a Sender interface with an SMTP
// implementation (net/smtp, STARTTLS / implicit TLS, timeouts, proper MIME)
// and a recording fake for tests, plus the Dispatcher that delivers the
// e-mail outbox (store.ClaimDueEmails) with retries, like package webhook
// does for webhook events.
//
// What is mailed, to whom and when is decided by package notify; this
// package only moves bytes.
package mailer

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"strings"
	"time"
)

// Message is one e-mail to one recipient. Text is required; HTML is an
// optional alternative rendering of the same content.
type Message struct {
	To      string
	Subject string
	Text    string
	HTML    string
}

// Sender delivers a message. Implementations must honour ctx and be safe for
// concurrent use.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

// ErrPermanent marks errors that retrying cannot fix (an invalid recipient
// or message); the dispatcher gives up on such a message at once.
var ErrPermanent = errors.New("mailer: permanent failure")

// ParseRecipient validates a single recipient address and returns its bare
// form (user@host).
func ParseRecipient(to string) (string, error) {
	to = strings.TrimSpace(to)
	if to == "" || strings.ContainsAny(to, "\r\n") || len(to) > 254 {
		return "", fmt.Errorf("%w: invalid recipient %q", ErrPermanent, to)
	}
	a, err := mail.ParseAddress(to)
	if err != nil || a.Address == "" {
		return "", fmt.Errorf("%w: invalid recipient %q", ErrPermanent, to)
	}
	return a.Address, nil
}

// Build renders m as an RFC 5322 message from from: UTF-8 headers encoded
// per RFC 2047, Date, Message-ID, Auto-Submitted, and a body that is
// text/plain or — with HTML — multipart/alternative, both quoted-printable.
// Lines end in CRLF.
func Build(from *mail.Address, m Message, now time.Time) ([]byte, error) {
	if from == nil || from.Address == "" {
		return nil, fmt.Errorf("%w: missing sender", ErrPermanent)
	}
	to, err := ParseRecipient(m.To)
	if err != nil {
		return nil, err
	}
	if strings.ContainsAny(m.Subject, "\r\n") {
		return nil, fmt.Errorf("%w: the subject must be a single line", ErrPermanent)
	}
	if m.Text == "" {
		return nil, fmt.Errorf("%w: empty message", ErrPermanent)
	}
	var b bytes.Buffer
	h := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	h("From", from.String())
	h("To", (&mail.Address{Address: to}).String())
	h("Subject", mime.BEncoding.Encode("utf-8", m.Subject))
	h("Date", now.UTC().Format(time.RFC1123Z))
	h("Message-ID", messageID(from.Address))
	h("MIME-Version", "1.0")
	h("Auto-Submitted", "auto-generated")

	if m.HTML == "" {
		h("Content-Type", "text/plain; charset=utf-8")
		h("Content-Transfer-Encoding", "quoted-printable")
		b.WriteString("\r\n")
		if err := writeQP(&b, m.Text); err != nil {
			return nil, err
		}
		return b.Bytes(), nil
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	h("Content-Type", mime.FormatMediaType("multipart/alternative", map[string]string{"boundary": mw.Boundary()}))
	b.WriteString("\r\n")
	for _, part := range []struct{ ctype, content string }{
		{"text/plain; charset=utf-8", m.Text},
		{"text/html; charset=utf-8", m.HTML},
	} {
		pw, err := mw.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {part.ctype},
			"Content-Transfer-Encoding": {"quoted-printable"},
		})
		if err != nil {
			return nil, err
		}
		if err := writeQP(pw, part.content); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	b.Write(body.Bytes())
	return b.Bytes(), nil
}

func writeQP(w interface{ Write([]byte) (int, error) }, s string) error {
	qp := quotedprintable.NewWriter(w)
	if _, err := qp.Write([]byte(strings.ReplaceAll(s, "\r\n", "\n"))); err != nil {
		return err
	}
	return qp.Close()
}

// messageID returns a unique Message-ID in the sender's domain.
func messageID(from string) string {
	domain := "cryptopay.local"
	if i := strings.LastIndexByte(from, '@'); i >= 0 && i < len(from)-1 {
		domain = from[i+1:]
	}
	var r [16]byte
	_, _ = rand.Read(r[:])
	return "<" + hex.EncodeToString(r[:]) + "@" + domain + ">"
}
