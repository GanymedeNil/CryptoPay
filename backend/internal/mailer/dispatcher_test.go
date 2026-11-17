package mailer_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cryptopay/internal/domain"
	"cryptopay/internal/mailer"
	"cryptopay/internal/store"
	"cryptopay/internal/store/storetest"
)

var ctx = context.Background()

func enqueue(t *testing.T, s *store.Store, to string) domain.EmailMessage {
	t.Helper()
	e, err := s.EnqueueEmail(ctx, domain.EmailMessage{Kind: "security.new_login", To: to, Subject: "[CryptoPay] hi", BodyText: "body", BodyHTML: "<p>body</p>"})
	require.NoError(t, err)
	return e
}

func TestBackoff(t *testing.T) {
	want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute, 32 * time.Minute,
		64 * time.Minute, 2 * time.Hour, 2 * time.Hour}
	for i, w := range want {
		assert.Equal(t, w, mailer.Backoff(i+1), "attempt %d", i+1)
	}
	assert.Equal(t, 2*time.Hour, mailer.Backoff(100))
	assert.Equal(t, time.Minute, mailer.Backoff(0))
}

func TestDispatcherSendsAndRetries(t *testing.T) {
	t.Parallel()
	clock := storetest.FixedClock(time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC))
	s := storetest.New(t, store.WithClock(clock.Now))
	rec := &mailer.Recorder{}
	d := mailer.NewDispatcher(s, rec, nil, clock.Now, mailer.WithConcurrency(2))

	ok1 := enqueue(t, s, "a@example.com")
	bad := enqueue(t, s, "b@example.com")
	rec.Fail = func(m mailer.Message) error {
		if m.To == "b@example.com" {
			return errors.New("421 4.7.0 try again later")
		}
		return nil
	}
	sent, failed, err := d.SendDue(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, sent)
	assert.Equal(t, 1, failed)
	require.Len(t, rec.Sent(), 1)
	m := rec.Sent()[0]
	assert.Equal(t, mailer.Message{To: "a@example.com", Subject: "[CryptoPay] hi", Text: "body", HTML: "<p>body</p>"}, m)
	got, err := s.GetEmail(ctx, ok1.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.EmailSent, got.Status)
	assert.Equal(t, clock.Now(), *got.SentAt)

	// Backoff: min(2^attempt × 30 s, 2 h); retried only when due.
	got, err = s.GetEmail(ctx, bad.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.EmailPending, got.Status)
	assert.Equal(t, 1, got.Attempts)
	assert.Equal(t, clock.Now().Add(time.Minute), got.NextAttemptAt)
	assert.Equal(t, "421 4.7.0 try again later", got.LastError)
	sent, failed, err = d.SendDue(ctx)
	require.NoError(t, err)
	assert.Zero(t, sent+failed, "not due yet")

	// Exhaust the attempts: failed after MaxAttempts.
	for i := 2; i <= mailer.MaxAttempts; i++ {
		clock.Advance(mailer.Backoff(i - 1))
		_, failed, err = d.SendDue(ctx)
		require.NoError(t, err)
		require.Equal(t, 1, failed, "attempt %d", i)
	}
	got, err = s.GetEmail(ctx, bad.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.EmailFailed, got.Status)
	assert.Equal(t, mailer.MaxAttempts, got.Attempts)
	clock.Advance(24 * time.Hour)
	sent, failed, err = d.SendDue(ctx)
	require.NoError(t, err)
	assert.Zero(t, sent+failed, "a failed message is not retried by itself")

	// A permanent error fails at once.
	perm := enqueue(t, s, "c@example.com")
	rec.Fail = func(mailer.Message) error { return fmt.Errorf("%w: invalid recipient", mailer.ErrPermanent) }
	_, failed, err = d.SendDue(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, failed)
	got, err = s.GetEmail(ctx, perm.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.EmailFailed, got.Status)
	assert.Equal(t, 1, got.Attempts)
}

type panicky struct{}

func (panicky) Send(context.Context, mailer.Message) error { panic("smtp library bug") }

func TestDispatcherSurvivesPanics(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	e := enqueue(t, s, "a@example.com")
	d := mailer.NewDispatcher(s, panicky{}, nil, nil)
	_, failed, err := d.SendDue(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, failed)
	got, err := s.GetEmail(ctx, e.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, got.Attempts)
	assert.Equal(t, "internal error while sending", got.LastError)
}

// counting counts deliveries per recipient (slowly, to overlap workers).
type counting struct {
	mu sync.Mutex
	n  map[string]int
}

func (c *counting) Send(ctx context.Context, m mailer.Message) error {
	time.Sleep(2 * time.Millisecond)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n[m.To]++
	return nil
}

// TestDispatchersNeverDoubleSend: several dispatchers (instances) on one
// outbox send every message exactly once.
func TestDispatchersNeverDoubleSend(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	const n = 40
	for i := range n {
		enqueue(t, s, fmt.Sprintf("u%d@example.com", i))
	}
	c := &counting{n: map[string]int{}}
	var total atomic.Int64
	var wg sync.WaitGroup
	for range 4 {
		d := mailer.NewDispatcher(s, c, nil, nil, mailer.WithConcurrency(3), mailer.WithBatchSize(5))
		wg.Go(func() {
			sent, _, err := d.SendDue(ctx)
			assert.NoError(t, err)
			total.Add(int64(sent))
		})
	}
	wg.Wait()
	assert.EqualValues(t, n, total.Load())
	assert.Len(t, c.n, n)
	for to, k := range c.n {
		assert.Equal(t, 1, k, to)
	}
	p, err := s.ListEmails(ctx, store.EmailFilter{Status: domain.EmailSent, PageSize: 100})
	require.NoError(t, err)
	assert.Equal(t, n, p.Total)
}

func TestDispatcherRunStops(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	rec := &mailer.Recorder{}
	d := mailer.NewDispatcher(s, rec, nil, nil, mailer.WithPollInterval(20*time.Millisecond))
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- d.Run(runCtx) }()
	enqueue(t, s, "late@example.com")
	require.Eventually(t, func() bool { return len(rec.Sent()) == 1 }, 5*time.Second, 10*time.Millisecond)
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
}
