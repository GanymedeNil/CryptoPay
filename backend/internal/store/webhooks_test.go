package store_test

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cryptopay/internal/domain"
	"cryptopay/internal/store"
	"cryptopay/internal/store/storetest"
)

func TestEnqueueAndGetWebhook(t *testing.T) {
	t.Parallel()
	clk := storetest.FixedClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	s := storetest.New(t, store.WithClock(clk.Now))
	m := storetest.Merchant(t, s)
	inv := storetest.Invoice(t, s, m.ID)

	e := storetest.WebhookEvent(t, s, inv)
	assert.Equal(t, domain.WebhookPending, e.Status)
	assert.Zero(t, e.Attempts)
	assert.Equal(t, clk.Now(), e.NextAttemptAt)
	assert.Equal(t, clk.Now(), e.CreatedAt)
	assert.Nil(t, e.DeliveredAt)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(e.Payload, &payload))
	assert.Equal(t, e.ID.String(), payload["id"])

	got, err := s.GetWebhookEvent(ctx, e.ID)
	require.NoError(t, err)
	assert.Equal(t, e, got)
	_, err = s.GetWebhookEvent(ctx, uuid.New())
	require.ErrorIs(t, err, store.ErrNotFound)

	for _, bad := range []domain.WebhookEvent{
		{MerchantID: m.ID, InvoiceID: inv.ID, EventType: "x", URL: "u"},                                 // no payload
		{MerchantID: m.ID, InvoiceID: inv.ID, EventType: "x", URL: "u", Payload: json.RawMessage(`{x`)}, // bad JSON
		{MerchantID: m.ID, InvoiceID: inv.ID, URL: "u", Payload: json.RawMessage(`{}`)},                 // no type
		{MerchantID: m.ID, InvoiceID: inv.ID, EventType: "x", URL: "u", Payload: json.RawMessage(`{}`), Status: "x"},
	} {
		_, err := s.EnqueueWebhook(ctx, bad)
		require.ErrorIs(t, err, store.ErrInvalid)
	}
	_, err = s.EnqueueWebhook(ctx, domain.WebhookEvent{MerchantID: m.ID, InvoiceID: uuid.New(), EventType: "x", URL: "u", Payload: json.RawMessage(`{}`)})
	require.ErrorIs(t, err, store.ErrForeignKey)
}

func TestClaimDueWebhooks(t *testing.T) {
	t.Parallel()
	clk := storetest.FixedClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	s := storetest.New(t, store.WithClock(clk.Now))
	m := storetest.Merchant(t, s)
	inv := storetest.Invoice(t, s, m.ID)
	now := clk.Now()

	e1 := storetest.WebhookEvent(t, s, inv)
	clk.Advance(time.Second)
	e2 := storetest.WebhookEvent(t, s, inv)
	clk.Advance(time.Second)
	future := storetest.WebhookEvent(t, s, inv, func(e *domain.WebhookEvent) { e.NextAttemptAt = now.Add(time.Hour) })
	storetest.WebhookEvent(t, s, inv, func(e *domain.WebhookEvent) { e.Status = domain.WebhookDelivered })
	storetest.WebhookEvent(t, s, inv, func(e *domain.WebhookEvent) { e.Status = domain.WebhookFailed })

	at := now.Add(5 * time.Second)
	claimed, err := s.ClaimDueWebhooks(ctx, at, 10, 30*time.Second)
	require.NoError(t, err)
	require.Len(t, claimed, 2)
	assert.Equal(t, e1.ID, claimed[0].ID, "oldest first")
	assert.Equal(t, e2.ID, claimed[1].ID)
	for _, c := range claimed {
		assert.Equal(t, at.Add(30*time.Second), c.NextAttemptAt, "lease")
		assert.Equal(t, domain.WebhookPending, c.Status)
	}
	// Leased events are not claimable again until the lease expires.
	again, err := s.ClaimDueWebhooks(ctx, at.Add(29*time.Second), 10, 30*time.Second)
	require.NoError(t, err)
	assert.Empty(t, again)
	// Crashed worker: after the lease the events come back.
	again, err = s.ClaimDueWebhooks(ctx, at.Add(30*time.Second), 10, 30*time.Second)
	require.NoError(t, err)
	assert.Len(t, again, 2)

	// Limit and future events.
	later, err := s.ClaimDueWebhooks(ctx, now.Add(2*time.Hour), 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, later, 1)
	later, err = s.ClaimDueWebhooks(ctx, now.Add(2*time.Hour), 10, time.Minute)
	require.NoError(t, err)
	require.Len(t, later, 2)
	_ = future

	none, err := s.ClaimDueWebhooks(ctx, now, 0, time.Minute)
	require.NoError(t, err)
	assert.Empty(t, none)
	_, err = s.ClaimDueWebhooks(ctx, now, 1, 0)
	require.ErrorIs(t, err, store.ErrInvalid)
}

func TestClaimDueWebhooksConcurrent(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	m := storetest.Merchant(t, s)
	inv := storetest.Invoice(t, s, m.ID)
	const total = 120
	for range total {
		storetest.WebhookEvent(t, s, inv)
	}
	now := time.Now().Add(time.Second)

	var mu sync.Mutex
	claimedBy := map[uuid.UUID]int{}
	var wg sync.WaitGroup
	for w := range 6 {
		wg.Go(func() {
			for {
				batch, err := s.ClaimDueWebhooks(ctx, now, 7, time.Hour)
				if !assert.NoError(t, err) || len(batch) == 0 {
					return
				}
				mu.Lock()
				for _, e := range batch {
					claimedBy[e.ID]++
				}
				mu.Unlock()
				_ = w
			}
		})
	}
	wg.Wait()
	assert.Len(t, claimedBy, total, "every event claimed")
	for id, n := range claimedBy {
		assert.Equal(t, 1, n, "event %s claimed %d times", id, n)
	}
}

func TestMarkWebhook(t *testing.T) {
	t.Parallel()
	clk := storetest.FixedClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	s := storetest.New(t, store.WithClock(clk.Now))
	m := storetest.Merchant(t, s)
	inv := storetest.Invoice(t, s, m.ID)
	now := clk.Now()

	// Delivered.
	e := storetest.WebhookEvent(t, s, inv)
	d, err := s.MarkWebhookDelivered(ctx, e.ID, store.WebhookAttempt{StatusCode: 200, Response: "ok"})
	require.NoError(t, err)
	assert.Equal(t, domain.WebhookDelivered, d.Status)
	assert.Equal(t, 1, d.Attempts)
	require.NotNil(t, d.DeliveredAt)
	assert.Equal(t, now, *d.DeliveredAt)
	assert.Equal(t, 200, d.LastStatusCode)
	assert.Equal(t, "ok", d.LastResponse)
	_, err = s.MarkWebhookDelivered(ctx, e.ID, store.WebhookAttempt{StatusCode: 200})
	require.ErrorIs(t, err, store.ErrConflict, "already delivered")
	_, err = s.MarkWebhookFailed(ctx, e.ID, store.WebhookAttempt{StatusCode: 500}, now, 3)
	require.ErrorIs(t, err, store.ErrConflict, "a late failure must not resurrect a delivered event")
	_, err = s.MarkWebhookDelivered(ctx, uuid.New(), store.WebhookAttempt{})
	require.ErrorIs(t, err, store.ErrNotFound)

	// Failed until exhausted (max 3).
	f := storetest.WebhookEvent(t, s, inv)
	long := strings.Repeat("é", store.MaxWebhookResponseBytes) + "\x00tail"
	r1, err := s.MarkWebhookFailed(ctx, f.ID, store.WebhookAttempt{StatusCode: 500, Error: "HTTP 500", Response: long}, now.Add(15*time.Second), 3)
	require.NoError(t, err)
	assert.Equal(t, domain.WebhookPending, r1.Status)
	assert.Equal(t, 1, r1.Attempts)
	assert.Equal(t, now.Add(15*time.Second), r1.NextAttemptAt)
	assert.Equal(t, "HTTP 500", r1.LastError)
	assert.LessOrEqual(t, len(r1.LastResponse), store.MaxWebhookResponseBytes)
	assert.NotContains(t, r1.LastResponse, "\x00")
	r2, err := s.MarkWebhookFailed(ctx, f.ID, store.WebhookAttempt{Error: "dial tcp: timeout"}, now.Add(30*time.Second), 3)
	require.NoError(t, err)
	assert.Equal(t, domain.WebhookPending, r2.Status)
	assert.Equal(t, 0, r2.LastStatusCode)
	r3, err := s.MarkWebhookFailed(ctx, f.ID, store.WebhookAttempt{StatusCode: 404}, now.Add(time.Minute), 3)
	require.NoError(t, err)
	assert.Equal(t, domain.WebhookFailed, r3.Status)
	assert.Equal(t, 3, r3.Attempts)
	_, err = s.MarkWebhookFailed(ctx, f.ID, store.WebhookAttempt{}, now, 3)
	require.ErrorIs(t, err, store.ErrConflict)

	// Failed events are not claimed.
	c, err := s.ClaimDueWebhooks(ctx, now.Add(time.Hour), 10, time.Minute)
	require.NoError(t, err)
	assert.Empty(t, c)

	// Manual retry: one more attempt, then failed again.
	clk.Advance(time.Hour)
	rq, err := s.RequeueWebhook(ctx, f.ID, clk.Now())
	require.NoError(t, err)
	assert.Equal(t, domain.WebhookPending, rq.Status)
	assert.Equal(t, clk.Now(), rq.NextAttemptAt)
	assert.Equal(t, 3, rq.Attempts)
	c, err = s.ClaimDueWebhooks(ctx, clk.Now(), 10, time.Minute)
	require.NoError(t, err)
	require.Len(t, c, 1)
	r4, err := s.MarkWebhookFailed(ctx, f.ID, store.WebhookAttempt{StatusCode: 500}, clk.Now(), 3)
	require.NoError(t, err)
	assert.Equal(t, domain.WebhookFailed, r4.Status)
	assert.Equal(t, 4, r4.Attempts)

	// Requeue of a delivered event clears delivered_at.
	rd, err := s.RequeueWebhook(ctx, e.ID, clk.Now())
	require.NoError(t, err)
	assert.Equal(t, domain.WebhookPending, rd.Status)
	assert.Nil(t, rd.DeliveredAt)
	_, err = s.RequeueWebhook(ctx, uuid.New(), clk.Now())
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestListWebhookEvents(t *testing.T) {
	t.Parallel()
	clk := storetest.FixedClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	s := storetest.New(t, store.WithClock(clk.Now))
	m1 := storetest.Merchant(t, s)
	m2 := storetest.Merchant(t, s)
	i1 := storetest.Invoice(t, s, m1.ID)
	i2 := storetest.Invoice(t, s, m2.ID)

	a := storetest.WebhookEvent(t, s, i1)
	clk.Advance(time.Second)
	b := storetest.WebhookEvent(t, s, i1, func(e *domain.WebhookEvent) { e.EventType = domain.EventExpired; e.Status = domain.WebhookFailed })
	clk.Advance(time.Second)
	c := storetest.WebhookEvent(t, s, i2, func(e *domain.WebhookEvent) { e.Status = domain.WebhookDelivered })

	ids := func(f store.WebhookFilter) []uuid.UUID {
		t.Helper()
		p, err := s.ListWebhookEvents(ctx, f)
		require.NoError(t, err)
		out := make([]uuid.UUID, len(p.Items))
		for i, e := range p.Items {
			out[i] = e.ID
		}
		return out
	}
	assert.Equal(t, []uuid.UUID{c.ID, b.ID, a.ID}, ids(store.WebhookFilter{}))
	assert.Equal(t, []uuid.UUID{b.ID}, ids(store.WebhookFilter{Status: domain.WebhookFailed}))
	assert.Equal(t, []uuid.UUID{b.ID, a.ID}, ids(store.WebhookFilter{MerchantID: &m1.ID}))
	assert.Equal(t, []uuid.UUID{c.ID}, ids(store.WebhookFilter{InvoiceID: &i2.ID}))
	assert.Equal(t, []uuid.UUID{b.ID}, ids(store.WebhookFilter{EventType: domain.EventExpired}))
	assert.Equal(t, []uuid.UUID{b.ID}, ids(store.WebhookFilter{Page: 2, PageSize: 1}))
	_, err := s.ListWebhookEvents(ctx, store.WebhookFilter{Status: "x"})
	require.ErrorIs(t, err, store.ErrInvalid)

	byInv, err := s.ListWebhookEventsByInvoice(ctx, i1.ID)
	require.NoError(t, err)
	require.Len(t, byInv, 2)
	assert.Equal(t, b.ID, byInv[0].ID)
}

// The outbox pattern: invoice change and event commit or roll back together.
func TestWebhookOutboxIsTransactional(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	m := storetest.Merchant(t, s)
	inv := storetest.Invoice(t, s, m.ID)
	err := s.WithTx(ctx, func(tx *store.Store) error {
		cur, err := tx.GetInvoiceForUpdate(ctx, inv.ID)
		require.NoError(t, err)
		st := store.StateOf(cur)
		st.Status = domain.InvoiceCancelled
		_, err = tx.UpdateInvoiceState(ctx, inv.ID, st)
		require.NoError(t, err)
		_, err = tx.EnqueueWebhook(ctx, domain.WebhookEvent{MerchantID: m.ID, InvoiceID: inv.ID,
			EventType: domain.EventCancelled, URL: m.WebhookURL, Payload: json.RawMessage(`{}`)})
		require.NoError(t, err)
		return assert.AnError
	})
	require.ErrorIs(t, err, assert.AnError)
	got, _ := s.GetInvoice(ctx, inv.ID)
	assert.Equal(t, domain.InvoicePending, got.Status)
	evs, err := s.ListWebhookEventsByInvoice(ctx, inv.ID)
	require.NoError(t, err)
	assert.Empty(t, evs)
}
