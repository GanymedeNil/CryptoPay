package store

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"cryptopay/internal/domain"
)

const emailCols = `e.id, e.kind, e.dedupe_key, e.to_address, e.subject, e.body_text, e.body_html, e.status, e.attempts,
	e.next_attempt_at, e.last_error, e.sent_at, e.created_at`

// Limits of the outbox text columns.
const (
	MaxEmailErrorBytes   = 1024
	MaxEmailSubjectBytes = 998 // RFC 5322 line limit
	MaxEmailBodyBytes    = 256 << 10
)

func scanEmail(row pgx.Row) (domain.EmailMessage, error) {
	var e domain.EmailMessage
	var status string
	err := row.Scan(&e.ID, &e.Kind, &e.DedupeKey, &e.To, &e.Subject, &e.BodyText, &e.BodyHTML, &status, &e.Attempts,
		&e.NextAttemptAt, &e.LastError, &e.SentAt, &e.CreatedAt)
	e.Status = domain.EmailStatus(status)
	return e, err
}

func collectEmails(rows pgx.Rows, err error, op string) ([]domain.EmailMessage, error) {
	if err != nil {
		return nil, wrap(op, err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.EmailMessage, error) { return scanEmail(r) })
	return nonNil(out), wrap(op, err)
}

func validEmailStatus(st domain.EmailStatus) bool {
	return st == domain.EmailPending || st == domain.EmailSent || st == domain.EmailFailed
}

// EnqueueEmail adds a message to the outbox. Call it on the transaction of
// the change it reports (when there is one), so a rolled back change sends
// nothing. Zero ID → generated; Status "" → pending; zero NextAttemptAt /
// CreatedAt → store clock.
func (s *Store) EnqueueEmail(ctx context.Context, e domain.EmailMessage) (domain.EmailMessage, error) {
	const op = "enqueue email"
	if e.Status == "" {
		e.Status = domain.EmailPending
	}
	if !validEmailStatus(e.Status) {
		return domain.EmailMessage{}, invalidf(op, "invalid status %q", e.Status)
	}
	if e.Kind == "" || strings.TrimSpace(e.To) == "" || e.Subject == "" || e.BodyText == "" {
		return domain.EmailMessage{}, invalidf(op, "kind, recipient, subject and text body are required")
	}
	if strings.ContainsAny(e.To, "\r\n") || strings.ContainsAny(e.Subject, "\r\n") {
		return domain.EmailMessage{}, invalidf(op, "recipient and subject must be a single line")
	}
	now := s.Now()
	out, err := scanEmail(s.db.QueryRow(ctx, `
		INSERT INTO email_outbox AS e (id, kind, dedupe_key, to_address, subject, body_text, body_html, status, attempts,
			next_attempt_at, last_error, sent_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		RETURNING `+emailCols,
		uuidOrNew(e.ID), e.Kind, cleanText(e.DedupeKey, 200), strings.TrimSpace(e.To), cleanText(e.Subject, MaxEmailSubjectBytes),
		cleanText(e.BodyText, MaxEmailBodyBytes), cleanText(e.BodyHTML, MaxEmailBodyBytes), string(e.Status), e.Attempts,
		timeOr(e.NextAttemptAt, now), cleanText(e.LastError, MaxEmailErrorBytes), utcPtr(e.SentAt), timeOr(e.CreatedAt, now)))
	return out, wrap(op, err)
}

// EmailCreatedSince reports whether a message of (kind, dedupeKey) was
// created after since — the throttling helper ("at most one alert per chain
// per hour"). A zero since asks "ever".
func (s *Store) EmailCreatedSince(ctx context.Context, kind, dedupeKey string, since time.Time) (bool, error) {
	var found bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM email_outbox
		WHERE kind = $1 AND dedupe_key = $2 AND created_at > $3)`, kind, dedupeKey, since.UTC()).Scan(&found)
	return found, wrap("email created since", err)
}

// EnqueueEmailsOnce enqueues msgs (one per recipient, all of the same kind
// and dedupe key) unless a message of that kind and key was created after
// since. The check and the inserts run in one transaction under an advisory
// lock on (kind, key), so concurrent callers — also on other gateway
// instances — enqueue at most one set. It reports whether it enqueued.
func (s *Store) EnqueueEmailsOnce(ctx context.Context, since time.Time, msgs []domain.EmailMessage) (bool, error) {
	const op = "enqueue emails once"
	if len(msgs) == 0 {
		return false, nil
	}
	kind, key := msgs[0].Kind, msgs[0].DedupeKey
	for _, m := range msgs {
		if m.Kind != kind || m.DedupeKey != key {
			return false, invalidf(op, "messages must share kind and dedupe key")
		}
	}
	enqueued := false
	err := s.WithTx(ctx, func(tx *Store) error {
		enqueued = false
		if _, err := tx.db.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('cryptopay:email:' || $1 || ':' || $2, 0))`,
			kind, key); err != nil {
			return wrap(op, err)
		}
		dup, err := tx.EmailCreatedSince(ctx, kind, key, since)
		if err != nil || dup {
			return err
		}
		for _, m := range msgs {
			if _, err := tx.EnqueueEmail(ctx, m); err != nil {
				return err
			}
		}
		enqueued = true
		return nil
	})
	return enqueued, err
}

// GetEmail returns an outbox message by id.
func (s *Store) GetEmail(ctx context.Context, id uuid.UUID) (domain.EmailMessage, error) {
	e, err := scanEmail(s.db.QueryRow(ctx, `SELECT `+emailCols+` FROM email_outbox e WHERE e.id = $1`, id))
	return e, wrap("get email", err)
}

// ClaimDueEmails atomically claims up to limit pending messages with
// next_attempt_at <= now (oldest first). Claiming pushes next_attempt_at to
// now+lease, so concurrent workers never claim the same message (FOR UPDATE
// SKIP LOCKED) and a message claimed by a worker that crashed is retried
// after the lease. Finish each with MarkEmailSent or MarkEmailFailed.
func (s *Store) ClaimDueEmails(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]domain.EmailMessage, error) {
	const op = "claim due emails"
	if limit <= 0 {
		return []domain.EmailMessage{}, nil
	}
	if lease <= 0 {
		return nil, invalidf(op, "lease must be positive")
	}
	now = now.UTC()
	rows, err := s.db.Query(ctx, `
		WITH due AS (
			SELECT id FROM email_outbox
			WHERE status = 'pending' AND next_attempt_at <= $1
			ORDER BY next_attempt_at, id
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		)
		UPDATE email_outbox e SET next_attempt_at = $3
		FROM due WHERE e.id = due.id
		RETURNING `+emailCols, now, limit, now.Add(lease))
	out, err := collectEmails(rows, err, op)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(out, func(a, b domain.EmailMessage) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID.String(), b.ID.String())
	})
	return out, nil
}

// MarkEmailSent records a successful delivery: attempts+1, status sent,
// sent_at = at (zero = store clock). Only pending messages can be marked
// (ErrConflict otherwise).
func (s *Store) MarkEmailSent(ctx context.Context, id uuid.UUID, at time.Time) (domain.EmailMessage, error) {
	const op = "mark email sent"
	e, err := scanEmail(s.db.QueryRow(ctx, `
		UPDATE email_outbox e SET status = 'sent', attempts = e.attempts + 1, sent_at = $2, last_error = ''
		WHERE e.id = $1 AND e.status = 'pending'
		RETURNING `+emailCols, id, timeOr(at, s.Now())))
	if isNoRows(err) {
		return domain.EmailMessage{}, s.errNoRow(ctx, op, "email_outbox", id)
	}
	return e, wrap(op, err)
}

// MarkEmailFailed records a failed attempt: attempts+1, last_error and
// next_attempt_at; when attempts reaches maxAttempts the message becomes
// failed. Only pending messages can be marked (ErrConflict otherwise).
func (s *Store) MarkEmailFailed(ctx context.Context, id uuid.UUID, errText string, nextAttemptAt time.Time, maxAttempts int) (domain.EmailMessage, error) {
	const op = "mark email failed"
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	e, err := scanEmail(s.db.QueryRow(ctx, `
		UPDATE email_outbox e SET attempts = e.attempts + 1,
			status = CASE WHEN e.attempts + 1 >= $2 THEN 'failed' ELSE 'pending' END,
			next_attempt_at = $3, last_error = $4
		WHERE e.id = $1 AND e.status = 'pending'
		RETURNING `+emailCols, id, maxAttempts, nextAttemptAt.UTC(), cleanText(errText, MaxEmailErrorBytes)))
	if isNoRows(err) {
		return domain.EmailMessage{}, s.errNoRow(ctx, op, "email_outbox", id)
	}
	return e, wrap(op, err)
}

// RequeueEmail schedules a pending or failed message for immediate delivery
// (status pending, next_attempt_at = now). The attempt counter is kept, so a
// failed message gets one more try. A sent message → ErrConflict.
func (s *Store) RequeueEmail(ctx context.Context, id uuid.UUID, now time.Time) (domain.EmailMessage, error) {
	const op = "requeue email"
	e, err := scanEmail(s.db.QueryRow(ctx, `
		UPDATE email_outbox e SET status = 'pending', next_attempt_at = $2
		WHERE e.id = $1 AND e.status <> 'sent'
		RETURNING `+emailCols, id, now.UTC()))
	if isNoRows(err) {
		return domain.EmailMessage{}, s.errNoRow(ctx, op, "email_outbox", id)
	}
	return e, wrap(op, err)
}

// EmailFilter selects messages for ListEmails.
type EmailFilter struct {
	Status   domain.EmailStatus // "" = any
	Kind     string             // "" = any
	Page     int                // 1-based
	PageSize int
}

// ListEmails returns a page of outbox messages, newest first.
func (s *Store) ListEmails(ctx context.Context, f EmailFilter) (Page[domain.EmailMessage], error) {
	const op = "list emails"
	page, size, offset := normPage(f.Page, f.PageSize)
	out := Page[domain.EmailMessage]{Items: []domain.EmailMessage{}, Page: page, PageSize: size}
	var c conds
	if f.Status != "" {
		if !validEmailStatus(f.Status) {
			return out, invalidf(op, "invalid status %q", f.Status)
		}
		c.add("e.status = " + c.arg(string(f.Status)))
	}
	if f.Kind != "" {
		c.add("e.kind = " + c.arg(f.Kind))
	}
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM email_outbox e`+c.where(), c.args...).Scan(&out.Total); err != nil {
		return out, wrap(op, err)
	}
	if out.Total == 0 || offset >= out.Total {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `SELECT `+emailCols+` FROM email_outbox e`+c.where()+
		fmt.Sprintf(` ORDER BY e.created_at DESC, e.id DESC LIMIT %d OFFSET %d`, size, offset), c.args...)
	items, err := collectEmails(rows, err, op)
	if err != nil {
		return out, err
	}
	out.Items = items
	return out, nil
}

// ListActiveOwnerEmails returns the e-mail addresses of all active owners
// (default alert recipients), oldest account first.
func (s *Store) ListActiveOwnerEmails(ctx context.Context) ([]string, error) {
	const op = "list active owner emails"
	rows, err := s.db.Query(ctx, `SELECT email FROM admin_users WHERE role = 'owner' AND active ORDER BY created_at, id`)
	if err != nil {
		return nil, wrap(op, err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	return nonNil(out), wrap(op, err)
}
