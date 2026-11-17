// Package alert is the narrow seam between the money path (invoice
// service, scanner, webhook dispatcher) and operational e-mail alerts.
//
// The money path calls a Notifier after the database transaction that made
// the change has committed, through Notify, which is nil-safe and recovers
// panics. A Notifier must return immediately; production code passes a
// Queue, which hands each alert to one background goroutine through a
// bounded buffer and drops alerts when the buffer is full. So an alert can
// never fail, roll back or slow down payment processing — at worst it is
// lost (and logged).
package alert

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"cryptopay/internal/domain"
)

// Notifier receives operational events. Every method is called after the
// change it reports was committed, must not block and must not keep ctx
// (which may end with the caller).
type Notifier interface {
	// WebhookFailed: a webhook event reached its final failed state.
	WebhookFailed(ctx context.Context, e domain.WebhookEvent)
	// TransferUnmatched: a transfer was stored without a matching quote.
	TransferUnmatched(ctx context.Context, t domain.Transfer)
	// InvoiceUnderpaid: an invoice entered status underpaid.
	InvoiceUnderpaid(ctx context.Context, inv domain.Invoice)
	// ScannerFailing: a chain's scanner has been failing since `since`.
	ScannerFailing(ctx context.Context, chainID string, since time.Time, lastError string)
}

// Notify calls fn with n unless n is nil, recovering (and logging) a panic.
// log may be nil.
func Notify(n Notifier, log *slog.Logger, fn func(Notifier)) {
	if n == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil && log != nil {
			log.Error("alert: notifier panicked", "panic", fmt.Sprint(r))
		}
	}()
	fn(n)
}

// DefaultQueueSize is the buffer of NewQueue when size ≤ 0.
const DefaultQueueSize = 256

// DefaultHandleTimeout bounds the handling of one queued alert.
const DefaultHandleTimeout = 30 * time.Second

// Queue is an asynchronous Notifier: calls only put the alert into a bounded
// buffer (never blocking; when it is full the alert is dropped and counted)
// and Run hands them to the wrapped Notifier one at a time, so alert work
// (database queries, rendering) uses at most one connection and never
// competes with the caller.
type Queue struct {
	next    Notifier
	log     *slog.Logger
	ch      chan func(context.Context)
	timeout time.Duration
	dropped atomic.Int64
}

var _ Notifier = (*Queue)(nil)

// NewQueue wraps next. size ≤ 0 selects DefaultQueueSize; log may be nil.
func NewQueue(next Notifier, size int, log *slog.Logger) *Queue {
	if size <= 0 {
		size = DefaultQueueSize
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Queue{next: next, log: log, ch: make(chan func(context.Context), size), timeout: DefaultHandleTimeout}
}

// Dropped returns how many alerts were dropped because the buffer was full.
func (q *Queue) Dropped() int64 { return q.dropped.Load() }

func (q *Queue) put(name string, f func(context.Context)) {
	select {
	case q.ch <- f:
	default:
		if n := q.dropped.Add(1); n == 1 || n%100 == 0 {
			q.log.Warn("alert queue full, alert dropped", "alert", name, "dropped_total", n)
		}
	}
}

// Run handles queued alerts until ctx is done (alerts still queued then
// are dropped: they are best effort).
func (q *Queue) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case f := <-q.ch:
			q.handle(ctx, f)
		}
	}
}

func (q *Queue) handle(ctx context.Context, f func(context.Context)) {
	hctx, cancel := context.WithTimeout(ctx, q.timeout)
	defer cancel()
	defer func() {
		if r := recover(); r != nil {
			q.log.Error("alert: handler panicked", "panic", fmt.Sprint(r))
		}
	}()
	f(hctx)
}

func (q *Queue) WebhookFailed(_ context.Context, e domain.WebhookEvent) {
	q.put("webhook_failed", func(ctx context.Context) { q.next.WebhookFailed(ctx, e) })
}

func (q *Queue) TransferUnmatched(_ context.Context, t domain.Transfer) {
	q.put("transfer_unmatched", func(ctx context.Context) { q.next.TransferUnmatched(ctx, t) })
}

func (q *Queue) InvoiceUnderpaid(_ context.Context, inv domain.Invoice) {
	q.put("invoice_underpaid", func(ctx context.Context) { q.next.InvoiceUnderpaid(ctx, inv) })
}

func (q *Queue) ScannerFailing(_ context.Context, chainID string, since time.Time, lastError string) {
	q.put("scanner_failing", func(ctx context.Context) { q.next.ScannerFailing(ctx, chainID, since, lastError) })
}

// Recorder is a Notifier for tests that records every call.
type Recorder struct {
	calls chan Call
}

// Call is one recorded notification.
type Call struct {
	Kind      string // "webhook_failed", "transfer_unmatched", "invoice_underpaid", "scanner_failing"
	Webhook   domain.WebhookEvent
	Transfer  domain.Transfer
	Invoice   domain.Invoice
	ChainID   string
	Since     time.Time
	LastError string
}

// NewRecorder returns a recorder buffering up to 1000 calls.
func NewRecorder() *Recorder { return &Recorder{calls: make(chan Call, 1000)} }

// Calls returns (and forgets) the calls recorded so far.
func (r *Recorder) Calls() []Call {
	var out []Call
	for {
		select {
		case c := <-r.calls:
			out = append(out, c)
		default:
			return out
		}
	}
}

func (r *Recorder) WebhookFailed(_ context.Context, e domain.WebhookEvent) {
	r.calls <- Call{Kind: "webhook_failed", Webhook: e}
}

func (r *Recorder) TransferUnmatched(_ context.Context, t domain.Transfer) {
	r.calls <- Call{Kind: "transfer_unmatched", Transfer: t}
}

func (r *Recorder) InvoiceUnderpaid(_ context.Context, inv domain.Invoice) {
	r.calls <- Call{Kind: "invoice_underpaid", Invoice: inv}
}

func (r *Recorder) ScannerFailing(_ context.Context, chainID string, since time.Time, lastError string) {
	r.calls <- Call{Kind: "scanner_failing", ChainID: chainID, Since: since, LastError: lastError}
}
