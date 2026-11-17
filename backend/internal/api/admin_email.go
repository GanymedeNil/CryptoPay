package api

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"cryptopay/internal/domain"
	"cryptopay/internal/mailer"
	"cryptopay/internal/notify"
	"cryptopay/internal/store"
)

// ---- e-mail (docs/api.md "E-mail") ----

// testEmailTimeout bounds the synchronous test send (below the request timeout).
const testEmailTimeout = 20 * time.Second

var errEmailNotConfigured = errConflict("e-mail is not configured: set SMTP_HOST and SMTP_FROM (see README) and restart the gateway")

type emailStatusJSON struct {
	Configured bool   `json:"configured"`
	From       string `json:"from"`
	Host       string `json:"host"`
}

func (a *API) emailConfigured() bool { return a.cfg.EmailConfigured() && a.mailer != nil }

func (a *API) emailStatus(w http.ResponseWriter, r *http.Request) {
	out := emailStatusJSON{}
	if a.cfg.EmailConfigured() {
		out = emailStatusJSON{Configured: a.emailConfigured(), From: a.cfg.SMTP.From, Host: a.cfg.SMTP.Host}
	}
	writeJSON(w, http.StatusOK, out)
}

type emailTestResultJSON struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

// emailTest sends a test message synchronously and reports the outcome.
func (a *API) emailTest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body struct {
		To *string `json:"to"`
	}
	if err := decodeJSON(r, &body); err != nil {
		a.fail(w, r, err)
		return
	}
	ad, _ := adminFrom(ctx)
	to := ad.User.Email
	if body.To != nil && strings.TrimSpace(*body.To) != "" {
		f := fields{}
		to = checkEmail(f, *body.To)
		if f["email"] != "" {
			a.fail(w, r, errField("to", f["email"]))
			return
		}
	}
	if !a.emailConfigured() {
		a.fail(w, r, errEmailNotConfigured)
		return
	}
	if ok, wait := a.emailTestLimit.allow(ad.User.ID.String()); !ok {
		a.rateLimited(w, r, wait)
		return
	}
	st, err := a.store.GetSettings(ctx)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	sctx, cancel := context.WithTimeout(ctx, testEmailTimeout)
	defer cancel()
	serr := a.mailer.Send(sctx, notify.TestMessage(st.EmailLocale, to, ad.User.Email, a.cfg.SMTP.Host, a.now()))
	out := emailTestResultJSON{OK: serr == nil}
	if serr != nil {
		out.Error = mailer.SanitizeError(serr.Error(), a.cfg.SMTP.Password)
		if out.Error == "" {
			out.Error = "sending failed"
		}
		a.log.WarnContext(ctx, "test e-mail failed", "request_id", requestIDFrom(ctx), "error", out.Error)
	}
	a.auditAfter(ctx, auditEntry{Action: "email.test", TargetType: "email", TargetID: to,
		Detail: map[string]any{"to": to, "ok": out.OK}})
	writeJSON(w, http.StatusOK, out)
}

type emailJSON struct {
	ID        string     `json:"id"`
	Kind      string     `json:"kind"`
	To        string     `json:"to"`
	Subject   string     `json:"subject"`
	Status    string     `json:"status"`
	Attempts  int        `json:"attempts"`
	LastError string     `json:"last_error"`
	CreatedAt time.Time  `json:"created_at"`
	SentAt    *time.Time `json:"sent_at"`
}

func newEmailJSON(e domain.EmailMessage) emailJSON {
	return emailJSON{ID: e.ID.String(), Kind: e.Kind, To: e.To, Subject: e.Subject, Status: string(e.Status),
		Attempts: e.Attempts, LastError: e.LastError, CreatedAt: e.CreatedAt.UTC(), SentAt: utcPtr(e.SentAt)}
}

type emailDetailJSON struct {
	emailJSON
	BodyText string `json:"body_text"`
}

var emailKindRE = regexp.MustCompile(`^[a-z0-9_.]{1,64}$`)

func (a *API) emailOutbox(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r)
	f := store.EmailFilter{Status: domain.EmailStatus(q.oneOf("status", "pending", "sent", "failed"))}
	if k := q.str("kind"); k != "" {
		if !emailKindRE.MatchString(k) {
			q.fields.add("kind", "is not a valid e-mail kind")
		}
		f.Kind = k
	}
	f.Page, f.PageSize = q.page()
	if err := q.err(); err != nil {
		a.fail(w, r, err)
		return
	}
	p, err := a.store.ListEmails(r.Context(), f)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, mapPage(p, newEmailJSON))
}

func (a *API) emailGet(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	e, err := a.store.GetEmail(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			err = errNotFound("e-mail not found")
		}
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, emailDetailJSON{emailJSON: newEmailJSON(e), BodyText: e.BodyText})
}

// emailRetry re-queues a pending or failed message for immediate delivery.
func (a *API) emailRetry(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := pathUUID(r, "id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if err := decodeJSON(r, &struct{}{}); err != nil {
		a.fail(w, r, err)
		return
	}
	var e domain.EmailMessage
	err = a.inTx(ctx, func(tx *store.Store) error {
		var err error
		if e, err = tx.RequeueEmail(ctx, id, a.now()); err != nil {
			switch {
			case errors.Is(err, store.ErrNotFound):
				return errNotFound("e-mail not found")
			case errors.Is(err, store.ErrConflict):
				return errConflict("this e-mail was already sent")
			}
			return err
		}
		return a.audit(ctx, tx, nil, auditEntry{Action: "email.retry", TargetType: "email", TargetID: id.String(),
			Detail: map[string]any{"kind": e.Kind, "to": e.To, "attempts": e.Attempts}})
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.kickEmail()
	writeJSON(w, http.StatusOK, newEmailJSON(e))
}
