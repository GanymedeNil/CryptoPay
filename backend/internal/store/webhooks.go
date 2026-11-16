package store

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"cryptopay/internal/domain"
)

const webhookCols = `w.id, w.merchant_id, w.invoice_id, w.event_type, w.payload::text, w.url, w.status, w.attempts,
	w.next_attempt_at, w.last_status_code, w.last_error, w.last_response, w.delivered_at, w.created_at`

// Limits applied to the diagnostic fields of a delivery attempt.
const (
	MaxWebhookResponseBytes = 4096
	MaxWebhookErrorBytes    = 1024
)

func scanWebhook(row pgx.Row) (domain.WebhookEvent, error) {
	var e domain.WebhookEvent
	var status string
	err := row.Scan(&e.ID, &e.MerchantID, &e.InvoiceID, &e.EventType, jsonScan{&e.Payload}, &e.URL, &status,
		&e.Attempts, &e.NextAttemptAt, &e.LastStatusCode, &e.LastError, &e.LastResponse, &e.DeliveredAt, &e.CreatedAt)
	e.Status = domain.WebhookStatus(status)
	return e, err
}

func collectWebhooks(rows pgx.Rows, err error, op string) ([]domain.WebhookEvent, error) {
	if err != nil {
		return nil, wrap(op, err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.WebhookEvent, error) { return scanWebhook(r) })
	return nonNil(out), wrap(op, err)
}

func validWebhookStatus(st domain.WebhookStatus) bool {
	return st == domain.WebhookPending || st == domain.WebhookDelivered || st == domain.WebhookFailed
}

// EnqueueWebhook adds an event to the outbox (call it in the same
// transaction as the invoice change it reports). Zero ID → generated; set
// ID yourself (uuid.New()) when Payload must contain the event id. Status
// "" → pending; zero NextAttemptAt / CreatedAt → store clock. Payload must
// be valid JSON (ErrInvalid otherwise); it is stored as jsonb, so key order
// and whitespace are normalised — sign the bytes you actually send.
func (s *Store) EnqueueWebhook(ctx context.Context, e domain.WebhookEvent) (domain.WebhookEvent, error) {
	const op = "enqueue webhook"
	if e.Status == "" {
		e.Status = domain.WebhookPending
	}
	if !validWebhookStatus(e.Status) {
		return domain.WebhookEvent{}, invalidf(op, "invalid status %q", e.Status)
	}
	if e.EventType == "" || e.URL == "" {
		return domain.WebhookEvent{}, invalidf(op, "event type and url are required")
	}
	if len(e.Payload) == 0 || !json.Valid(e.Payload) {
		return domain.WebhookEvent{}, invalidf(op, "payload must be valid JSON")
	}
	now := s.Now()
	out, err := scanWebhook(s.db.QueryRow(ctx, `
		INSERT INTO webhook_events AS w (id, merchant_id, invoice_id, event_type, payload, url, status, attempts,
			next_attempt_at, last_status_code, last_error, last_response, delivered_at, created_at)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		RETURNING `+webhookCols,
		uuidOrNew(e.ID), e.MerchantID, e.InvoiceID, e.EventType, string(e.Payload), e.URL, string(e.Status),
		e.Attempts, timeOr(e.NextAttemptAt, now), e.LastStatusCode, cleanText(e.LastError, MaxWebhookErrorBytes),
		cleanText(e.LastResponse, MaxWebhookResponseBytes), utcPtr(e.DeliveredAt), timeOr(e.CreatedAt, now)))
	return out, wrap(op, err)
}

// GetWebhookEvent returns an outbox event by id.
func (s *Store) GetWebhookEvent(ctx context.Context, id uuid.UUID) (domain.WebhookEvent, error) {
	e, err := scanWebhook(s.db.QueryRow(ctx, `SELECT `+webhookCols+` FROM webhook_events w WHERE w.id = $1`, id))
	return e, wrap("get webhook event", err)
}

// ClaimDueWebhooks atomically claims up to limit pending events with
// next_attempt_at <= now (oldest first) for delivery. Claiming pushes
// next_attempt_at to now+lease, so concurrent dispatchers never claim the
// same event (FOR UPDATE SKIP LOCKED) and an event claimed by a worker that
// crashes is retried after the lease. The worker must finish each event with
// MarkWebhookDelivered or MarkWebhookFailed. Runs as one statement; no
// transaction needed.
func (s *Store) ClaimDueWebhooks(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]domain.WebhookEvent, error) {
	const op = "claim due webhooks"
	if limit <= 0 {
		return []domain.WebhookEvent{}, nil
	}
	if lease <= 0 {
		return nil, invalidf(op, "lease must be positive")
	}
	now = now.UTC()
	rows, err := s.db.Query(ctx, `
		WITH due AS (
			SELECT id FROM webhook_events
			WHERE status = 'pending' AND next_attempt_at <= $1
			ORDER BY next_attempt_at, id
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		)
		UPDATE webhook_events w SET next_attempt_at = $3
		FROM due WHERE w.id = due.id
		RETURNING `+webhookCols, now, limit, now.Add(lease))
	out, err := collectWebhooks(rows, err, op)
	if err != nil {
		return nil, err
	}
	// RETURNING order is unspecified; return oldest events first.
	slices.SortFunc(out, func(a, b domain.WebhookEvent) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID.String(), b.ID.String())
	})
	return out, nil
}

// WebhookAttempt is the outcome of one delivery attempt.
type WebhookAttempt struct {
	StatusCode int       // HTTP status, 0 if no response
	Error      string    // transport / non-2xx description ("" on success)
	Response   string    // response body (truncated to MaxWebhookResponseBytes)
	At         time.Time // when the attempt finished; zero = store clock
}

// MarkWebhookDelivered records a successful attempt: attempts+1, status
// delivered, delivered_at, last_* fields. Only pending events can be
// marked (ErrConflict otherwise, e.g. if another worker already did).
func (s *Store) MarkWebhookDelivered(ctx context.Context, id uuid.UUID, a WebhookAttempt) (domain.WebhookEvent, error) {
	const op = "mark webhook delivered"
	e, err := scanWebhook(s.db.QueryRow(ctx, `
		UPDATE webhook_events w SET status = 'delivered', attempts = w.attempts + 1, delivered_at = $2,
			last_status_code = $3, last_error = $4, last_response = $5
		WHERE w.id = $1 AND w.status = 'pending'
		RETURNING `+webhookCols,
		id, timeOr(a.At, s.Now()), a.StatusCode, cleanText(a.Error, MaxWebhookErrorBytes),
		cleanText(a.Response, MaxWebhookResponseBytes)))
	if isNoRows(err) {
		return domain.WebhookEvent{}, s.errNoRow(ctx, op, "webhook_events", id)
	}
	return e, wrap(op, err)
}

// MarkWebhookFailed records a failed attempt: attempts+1, last_* fields and
// next_attempt_at = nextAttemptAt; when attempts reaches maxAttempts the
// event becomes failed (retries exhausted). Only pending events can be
// marked (ErrConflict otherwise).
func (s *Store) MarkWebhookFailed(ctx context.Context, id uuid.UUID, a WebhookAttempt, nextAttemptAt time.Time, maxAttempts int) (domain.WebhookEvent, error) {
	const op = "mark webhook failed"
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	e, err := scanWebhook(s.db.QueryRow(ctx, `
		UPDATE webhook_events w SET attempts = w.attempts + 1,
			status = CASE WHEN w.attempts + 1 >= $2 THEN 'failed' ELSE 'pending' END,
			next_attempt_at = $3, last_status_code = $4, last_error = $5, last_response = $6
		WHERE w.id = $1 AND w.status = 'pending'
		RETURNING `+webhookCols,
		id, maxAttempts, nextAttemptAt.UTC(), a.StatusCode, cleanText(a.Error, MaxWebhookErrorBytes),
		cleanText(a.Response, MaxWebhookResponseBytes)))
	if isNoRows(err) {
		return domain.WebhookEvent{}, s.errNoRow(ctx, op, "webhook_events", id)
	}
	return e, wrap(op, err)
}

// RequeueWebhook schedules an event (pending, failed or delivered) for
// immediate delivery at now: status pending, next_attempt_at = now,
// delivered_at cleared. The attempt counter is kept, so an exhausted event
// gets exactly one more try before failing again.
func (s *Store) RequeueWebhook(ctx context.Context, id uuid.UUID, now time.Time) (domain.WebhookEvent, error) {
	e, err := scanWebhook(s.db.QueryRow(ctx, `
		UPDATE webhook_events w SET status = 'pending', next_attempt_at = $2, delivered_at = NULL
		WHERE w.id = $1
		RETURNING `+webhookCols, id, now.UTC()))
	return e, wrap("requeue webhook", err)
}

// WebhookFilter selects events for ListWebhookEvents.
type WebhookFilter struct {
	Status     domain.WebhookStatus // "" = any
	MerchantID *uuid.UUID
	InvoiceID  *uuid.UUID
	EventType  string // "" = any
	Page       int    // 1-based
	PageSize   int
}

// ListWebhookEvents returns a page of outbox events, newest first.
func (s *Store) ListWebhookEvents(ctx context.Context, f WebhookFilter) (Page[domain.WebhookEvent], error) {
	const op = "list webhook events"
	page, size, offset := normPage(f.Page, f.PageSize)
	out := Page[domain.WebhookEvent]{Items: []domain.WebhookEvent{}, Page: page, PageSize: size}
	var c conds
	if f.Status != "" {
		if !validWebhookStatus(f.Status) {
			return out, invalidf(op, "invalid status %q", f.Status)
		}
		c.add("w.status = " + c.arg(string(f.Status)))
	}
	if f.MerchantID != nil {
		c.add("w.merchant_id = " + c.arg(*f.MerchantID))
	}
	if f.InvoiceID != nil {
		c.add("w.invoice_id = " + c.arg(*f.InvoiceID))
	}
	if f.EventType != "" {
		c.add("w.event_type = " + c.arg(f.EventType))
	}
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM webhook_events w`+c.where(), c.args...).Scan(&out.Total); err != nil {
		return out, wrap(op, err)
	}
	if out.Total == 0 || offset >= out.Total {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `SELECT `+webhookCols+` FROM webhook_events w`+c.where()+
		fmt.Sprintf(` ORDER BY w.created_at DESC, w.id DESC LIMIT %d OFFSET %d`, size, offset), c.args...)
	items, err := collectWebhooks(rows, err, op)
	if err != nil {
		return out, err
	}
	out.Items = items
	return out, nil
}

// ListWebhookEventsByInvoice returns all events of an invoice, newest first.
func (s *Store) ListWebhookEventsByInvoice(ctx context.Context, invoiceID uuid.UUID) ([]domain.WebhookEvent, error) {
	rows, err := s.db.Query(ctx, `SELECT `+webhookCols+` FROM webhook_events w WHERE w.invoice_id = $1
		ORDER BY w.created_at DESC, w.id DESC`, invoiceID)
	return collectWebhooks(rows, err, "list webhook events by invoice")
}
