package mailer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"cryptopay/internal/domain"
	"cryptopay/internal/store"
)

// Dispatcher defaults.
const (
	DefaultPollInterval = 5 * time.Second
	DefaultConcurrency  = 2
	// MaxAttempts is how often a message is tried before it is failed.
	MaxAttempts = 8

	baseBackoff = 30 * time.Second
	maxBackoff  = 2 * time.Hour
)

// Dispatcher delivers the e-mail outbox. Several dispatchers (in one or
// several processes) may run against one database: messages are claimed
// with FOR UPDATE SKIP LOCKED and a lease, so each attempt happens once.
type Dispatcher struct {
	store       *store.Store
	sender      Sender
	log         *slog.Logger
	now         func() time.Time
	poll        time.Duration
	concurrency int
	batch       int
	timeout     time.Duration
	maxAttempts int
}

// DispatcherOption configures a Dispatcher.
type DispatcherOption func(*Dispatcher)

// WithPollInterval sets how often Run looks for due messages (default 5 s).
func WithPollInterval(d time.Duration) DispatcherOption {
	return func(x *Dispatcher) {
		if d > 0 {
			x.poll = d
		}
	}
}

// WithConcurrency bounds parallel deliveries (default 2).
func WithConcurrency(n int) DispatcherOption {
	return func(x *Dispatcher) {
		if n > 0 {
			x.concurrency = n
		}
	}
}

// WithBatchSize sets how many messages one claim takes (default = concurrency).
func WithBatchSize(n int) DispatcherOption {
	return func(x *Dispatcher) {
		if n > 0 {
			x.batch = n
		}
	}
}

// WithSendTimeout bounds one delivery (default DefaultSMTPTimeout).
func WithSendTimeout(d time.Duration) DispatcherOption {
	return func(x *Dispatcher) {
		if d > 0 {
			x.timeout = d
		}
	}
}

// WithMaxAttempts overrides MaxAttempts (tests).
func WithMaxAttempts(n int) DispatcherOption {
	return func(x *Dispatcher) {
		if n > 0 {
			x.maxAttempts = n
		}
	}
}

// NewDispatcher returns a dispatcher sending with sender. log and now may be nil.
func NewDispatcher(st *store.Store, sender Sender, log *slog.Logger, now func() time.Time, opts ...DispatcherOption) *Dispatcher {
	if st == nil || sender == nil {
		panic("mailer: store and sender are required")
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if now == nil {
		now = time.Now
	}
	d := &Dispatcher{store: st, sender: sender, log: log, now: now, poll: DefaultPollInterval,
		concurrency: DefaultConcurrency, timeout: DefaultSMTPTimeout, maxAttempts: MaxAttempts}
	for _, o := range opts {
		o(d)
	}
	if d.batch == 0 {
		d.batch = d.concurrency
	}
	return d
}

// Run delivers due messages until ctx is done, then returns nil. Errors of
// one pass are logged and do not stop the loop.
func (d *Dispatcher) Run(ctx context.Context) error {
	t := time.NewTicker(d.poll)
	defer t.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		sent, failed, err := d.SendDue(ctx)
		switch {
		case ctx.Err() != nil:
			return nil
		case err != nil:
			d.log.Error("e-mail pass failed", "error", err)
		case sent+failed > 0:
			d.log.Debug("e-mail pass", "sent", sent, "failed", failed)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// SendDue claims and sends every message due now (repeating while batches
// come back full) and returns how many attempts succeeded and failed.
func (d *Dispatcher) SendDue(ctx context.Context) (sent, failed int, err error) {
	waves := (d.batch + d.concurrency - 1) / d.concurrency
	lease := d.timeout*time.Duration(waves+1) + 30*time.Second
	var nOK, nFail atomic.Int64
	for {
		msgs, cerr := d.store.ClaimDueEmails(ctx, d.now(), d.batch, lease)
		if cerr != nil {
			return int(nOK.Load()), int(nFail.Load()), fmt.Errorf("mailer: claim: %w", cerr)
		}
		var wg sync.WaitGroup
		sem := make(chan struct{}, d.concurrency)
		for _, m := range msgs {
			sem <- struct{}{}
			wg.Go(func() {
				defer func() { <-sem }()
				switch d.deliverSafe(ctx, m) {
				case outcomeSent:
					nOK.Add(1)
				case outcomeFailed:
					nFail.Add(1)
				}
			})
		}
		wg.Wait()
		if len(msgs) < d.batch || ctx.Err() != nil {
			break
		}
	}
	return int(nOK.Load()), int(nFail.Load()), ctx.Err()
}

type outcome int

const (
	outcomeSkipped outcome = iota
	outcomeSent
	outcomeFailed
)

func (d *Dispatcher) deliverSafe(ctx context.Context, m domain.EmailMessage) (out outcome) {
	defer func() {
		if r := recover(); r != nil {
			d.log.Error("e-mail delivery panicked", "email", m.ID, "panic", fmt.Sprint(r))
			out = d.fail(ctx, m, "internal error while sending", false)
		}
	}()
	sctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	err := d.sender.Send(sctx, Message{To: m.To, Subject: m.Subject, Text: m.BodyText, HTML: m.BodyHTML})
	if err == nil {
		return d.mark(ctx, m, func(c context.Context) error {
			_, err := d.store.MarkEmailSent(c, m.ID, d.now())
			return err
		}, outcomeSent)
	}
	if ctx.Err() != nil {
		return outcomeSkipped // shutting down: the lease expires and it is retried
	}
	return d.fail(ctx, m, SanitizeError(err.Error()), errors.Is(err, ErrPermanent))
}

// fail records a failed attempt; permanent gives up at once.
func (d *Dispatcher) fail(ctx context.Context, m domain.EmailMessage, errText string, permanent bool) outcome {
	attempt := m.Attempts + 1
	max := d.maxAttempts
	if permanent {
		max = attempt
	}
	next := d.now().Add(Backoff(attempt))
	d.log.Warn("e-mail attempt failed", "email", m.ID, "kind", m.Kind, "attempt", attempt, "error", errText,
		"final", attempt >= max)
	return d.mark(ctx, m, func(c context.Context) error {
		_, err := d.store.MarkEmailFailed(c, m.ID, errText, next, max)
		return err
	}, outcomeFailed)
}

func (d *Dispatcher) mark(ctx context.Context, m domain.EmailMessage, f func(context.Context) error, ok outcome) outcome {
	// Detach from ctx so a shutdown does not lose an attempt that completed.
	c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := f(c); err != nil {
		if errors.Is(err, store.ErrConflict) {
			d.log.Warn("mailer: message already finalised elsewhere", "email", m.ID)
		} else {
			d.log.Error("mailer: record outcome", "email", m.ID, "error", err)
		}
		return outcomeSkipped
	}
	return ok
}

// Backoff is the delay after the attempt-th failed attempt (attempt ≥ 1):
// min(2^attempt × 30 s, 2 h).
func Backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt >= 16 {
		return maxBackoff
	}
	return min(baseBackoff<<uint(attempt), maxBackoff)
}
