package alert

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cryptopay/internal/domain"
)

// blocking is a Notifier whose calls block until release is closed.
type blocking struct {
	release chan struct{}
	mu      sync.Mutex
	n       int
}

func (b *blocking) wait() {
	<-b.release
	b.mu.Lock()
	b.n++
	b.mu.Unlock()
}
func (b *blocking) WebhookFailed(context.Context, domain.WebhookEvent)        { b.wait() }
func (b *blocking) TransferUnmatched(context.Context, domain.Transfer)        { b.wait() }
func (b *blocking) InvoiceUnderpaid(context.Context, domain.Invoice)          { b.wait() }
func (b *blocking) ScannerFailing(context.Context, string, time.Time, string) { b.wait() }
func (b *blocking) count() int                                                { b.mu.Lock(); defer b.mu.Unlock(); return b.n }

type panicking struct{}

func (panicking) WebhookFailed(context.Context, domain.WebhookEvent)        { panic("boom") }
func (panicking) TransferUnmatched(context.Context, domain.Transfer)        { panic("boom") }
func (panicking) InvoiceUnderpaid(context.Context, domain.Invoice)          { panic("boom") }
func (panicking) ScannerFailing(context.Context, string, time.Time, string) { panic("boom") }

func TestNotifyNilAndPanic(t *testing.T) {
	Notify(nil, nil, func(Notifier) { t.Fatal("not called for a nil notifier") })
	assert.NotPanics(t, func() {
		Notify(panicking{}, nil, func(n Notifier) { n.InvoiceUnderpaid(context.Background(), domain.Invoice{}) })
	})
}

// TestQueueNeverBlocks: with a handler that hangs, callers still return at
// once; the overflow is dropped and counted.
func TestQueueNeverBlocks(t *testing.T) {
	b := &blocking{release: make(chan struct{})}
	q := NewQueue(b, 4, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { q.Run(ctx); close(done) }()

	start := time.Now()
	for range 100 {
		q.InvoiceUnderpaid(context.Background(), domain.Invoice{ID: uuid.New()})
		q.TransferUnmatched(context.Background(), domain.Transfer{})
		q.WebhookFailed(context.Background(), domain.WebhookEvent{})
		q.ScannerFailing(context.Background(), "ethereum", time.Now(), "x")
	}
	assert.Less(t, time.Since(start), time.Second, "400 calls against a hung handler return immediately")
	assert.GreaterOrEqual(t, q.Dropped(), int64(400-4-1), "everything beyond the buffer (and the one in flight) is dropped")

	close(b.release)
	require.Eventually(t, func() bool { return b.count() >= 4 }, 5*time.Second, 10*time.Millisecond, "queued alerts are handled")
	cancel()
	<-done
}

// TestQueueSurvivesPanics: a panicking handler does not stop the queue.
func TestQueueSurvivesPanics(t *testing.T) {
	rec := NewRecorder()
	mux := &switching{first: panicking{}, then: rec}
	q := NewQueue(mux, 8, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go q.Run(ctx)
	q.ScannerFailing(context.Background(), "tron", time.Unix(1, 0), "boom")
	q.ScannerFailing(context.Background(), "tron", time.Unix(2, 0), "second")
	require.Eventually(t, func() bool {
		calls := rec.Calls()
		if len(calls) == 0 {
			return false
		}
		assert.Equal(t, "second", calls[0].LastError)
		return true
	}, 5*time.Second, 10*time.Millisecond)
}

// switching panics on the first call and delegates afterwards.
type switching struct {
	mu    sync.Mutex
	used  bool
	first Notifier
	then  Notifier
}

func (s *switching) pick() Notifier {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.used {
		s.used = true
		return s.first
	}
	return s.then
}
func (s *switching) WebhookFailed(ctx context.Context, e domain.WebhookEvent) {
	s.pick().WebhookFailed(ctx, e)
}
func (s *switching) TransferUnmatched(ctx context.Context, t domain.Transfer) {
	s.pick().TransferUnmatched(ctx, t)
}
func (s *switching) InvoiceUnderpaid(ctx context.Context, i domain.Invoice) {
	s.pick().InvoiceUnderpaid(ctx, i)
}
func (s *switching) ScannerFailing(ctx context.Context, c string, t time.Time, e string) {
	s.pick().ScannerFailing(ctx, c, t, e)
}
