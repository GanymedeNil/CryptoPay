package store_test

import (
	"fmt"
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

func mail(kind, key, to string) domain.EmailMessage {
	return domain.EmailMessage{Kind: kind, DedupeKey: key, To: to, Subject: "[CryptoPay] s", BodyText: "text", BodyHTML: "<p>html</p>"}
}

func TestEmailOutboxLifecycle(t *testing.T) {
	t.Parallel()
	clock := storetest.FixedClock(time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC))
	s := storetest.New(t, store.WithClock(clock.Now))

	e, err := s.EnqueueEmail(ctx, mail("security.new_login", "", "a@example.com"))
	require.NoError(t, err)
	assert.Equal(t, domain.EmailPending, e.Status)
	assert.Equal(t, clock.Now(), e.NextAttemptAt)
	assert.Equal(t, "<p>html</p>", e.BodyHTML)
	for _, bad := range []domain.EmailMessage{
		{Kind: "k", To: "a@b", Subject: "s"},                            // no body
		{Kind: "k", To: "a@b\r\nBcc: x@y", Subject: "s", BodyText: "b"}, // header injection
		{Kind: "k", To: "a@b", Subject: "s\nX", BodyText: "b"},
		{Kind: "k", To: "a@b", Subject: "s", BodyText: "b", Status: "bounced"},
	} {
		_, err := s.EnqueueEmail(ctx, bad)
		require.ErrorIs(t, err, store.ErrInvalid, "%+v", bad)
	}

	got, err := s.GetEmail(ctx, e.ID)
	require.NoError(t, err)
	assert.Equal(t, e, got)
	_, err = s.GetEmail(ctx, uuid.New())
	require.ErrorIs(t, err, store.ErrNotFound)

	// Claim → lease → nothing due until the lease ends.
	claimed, err := s.ClaimDueEmails(ctx, clock.Now(), 10, time.Minute)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	again, err := s.ClaimDueEmails(ctx, clock.Now(), 10, time.Minute)
	require.NoError(t, err)
	assert.Empty(t, again, "leased")
	again, err = s.ClaimDueEmails(ctx, clock.Now().Add(2*time.Minute), 10, time.Minute)
	require.NoError(t, err)
	assert.Len(t, again, 1, "a crashed worker's message is retried after the lease")

	// Failures with backoff, then failed after max attempts.
	f, err := s.MarkEmailFailed(ctx, e.ID, "421 try later", clock.Now().Add(time.Minute), 2)
	require.NoError(t, err)
	assert.Equal(t, domain.EmailPending, f.Status)
	assert.Equal(t, 1, f.Attempts)
	assert.Equal(t, "421 try later", f.LastError)
	f, err = s.MarkEmailFailed(ctx, e.ID, "421 try later", clock.Now().Add(time.Minute), 2)
	require.NoError(t, err)
	assert.Equal(t, domain.EmailFailed, f.Status)
	_, err = s.MarkEmailFailed(ctx, e.ID, "x", clock.Now(), 2)
	require.ErrorIs(t, err, store.ErrConflict, "only pending messages")
	_, err = s.MarkEmailSent(ctx, e.ID, time.Time{})
	require.ErrorIs(t, err, store.ErrConflict)

	// Requeue a failed one: attempts kept, due now.
	r, err := s.RequeueEmail(ctx, e.ID, clock.Now())
	require.NoError(t, err)
	assert.Equal(t, domain.EmailPending, r.Status)
	assert.Equal(t, 2, r.Attempts)
	sent, err := s.MarkEmailSent(ctx, e.ID, time.Time{})
	require.NoError(t, err)
	assert.Equal(t, domain.EmailSent, sent.Status)
	assert.Equal(t, 3, sent.Attempts)
	assert.Empty(t, sent.LastError)
	require.NotNil(t, sent.SentAt)
	_, err = s.RequeueEmail(ctx, e.ID, clock.Now())
	require.ErrorIs(t, err, store.ErrConflict, "sent messages are not re-sent")
	_, err = s.RequeueEmail(ctx, uuid.New(), clock.Now())
	require.ErrorIs(t, err, store.ErrNotFound)
}

// TestClaimDueEmailsConcurrent: concurrent claimers never get the same
// message.
func TestClaimDueEmailsConcurrent(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	const n = 60
	for i := range n {
		_, err := s.EnqueueEmail(ctx, mail("alert.x", "", fmt.Sprintf("u%d@example.com", i)))
		require.NoError(t, err)
	}
	var mu sync.Mutex
	seen := map[uuid.UUID]int{}
	var wg sync.WaitGroup
	for range 6 {
		wg.Go(func() {
			for {
				batch, err := s.ClaimDueEmails(ctx, time.Now(), 4, time.Hour)
				if !assert.NoError(t, err) || len(batch) == 0 {
					return
				}
				mu.Lock()
				for _, e := range batch {
					seen[e.ID]++
				}
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	assert.Len(t, seen, n)
	for id, c := range seen {
		assert.Equal(t, 1, c, "claimed twice: %s", id)
	}
}

func TestEmailDedupe(t *testing.T) {
	t.Parallel()
	clock := storetest.FixedClock(time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC))
	s := storetest.New(t, store.WithClock(clock.Now))

	dup, err := s.EmailCreatedSince(ctx, "alert.transfer_unmatched", "ethereum", clock.Now().Add(-time.Hour))
	require.NoError(t, err)
	assert.False(t, dup)

	ok, err := s.EnqueueEmailsOnce(ctx, clock.Now().Add(-time.Hour), []domain.EmailMessage{
		mail("alert.transfer_unmatched", "ethereum", "a@example.com"), mail("alert.transfer_unmatched", "ethereum", "b@example.com")})
	require.NoError(t, err)
	assert.True(t, ok)
	clock.Advance(30 * time.Minute)
	ok, err = s.EnqueueEmailsOnce(ctx, clock.Now().Add(-time.Hour), []domain.EmailMessage{mail("alert.transfer_unmatched", "ethereum", "a@example.com")})
	require.NoError(t, err)
	assert.False(t, ok, "within the hour")
	ok, err = s.EnqueueEmailsOnce(ctx, clock.Now().Add(-time.Hour), []domain.EmailMessage{mail("alert.transfer_unmatched", "tron", "a@example.com")})
	require.NoError(t, err)
	assert.True(t, ok, "another key")
	clock.Advance(31 * time.Minute)
	ok, err = s.EnqueueEmailsOnce(ctx, clock.Now().Add(-time.Hour), []domain.EmailMessage{mail("alert.transfer_unmatched", "ethereum", "a@example.com")})
	require.NoError(t, err)
	assert.True(t, ok, "an hour later")
	// "Ever" (zero since).
	ok, err = s.EnqueueEmailsOnce(ctx, time.Time{}, []domain.EmailMessage{mail("alert.invoice_underpaid", "inv-1", "a@example.com")})
	require.NoError(t, err)
	assert.True(t, ok)
	clock.Advance(365 * 24 * time.Hour)
	ok, err = s.EnqueueEmailsOnce(ctx, time.Time{}, []domain.EmailMessage{mail("alert.invoice_underpaid", "inv-1", "a@example.com")})
	require.NoError(t, err)
	assert.False(t, ok)
	_, err = s.EnqueueEmailsOnce(ctx, time.Time{}, []domain.EmailMessage{mail("a", "1", "x@y"), mail("b", "1", "x@y")})
	require.ErrorIs(t, err, store.ErrInvalid, "one kind and key per call")

	p, err := s.ListEmails(ctx, store.EmailFilter{Kind: "alert.transfer_unmatched"})
	require.NoError(t, err)
	assert.Equal(t, 4, p.Total)

	// Concurrent callers (other instances) enqueue one set only.
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for range 8 {
		wg.Go(func() {
			ok, err := s.EnqueueEmailsOnce(ctx, clock.Now().Add(-6*time.Hour), []domain.EmailMessage{mail("alert.scanner_failing", "tron", "a@example.com")})
			assert.NoError(t, err)
			if ok {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	assert.Equal(t, 1, wins)
}

func TestListEmailsAndOwners(t *testing.T) {
	t.Parallel()
	clock := storetest.FixedClock(time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC))
	s := storetest.New(t, store.WithClock(clock.Now))
	for i, kind := range []string{"security.new_login", "alert.webhook_failed", "security.new_login"} {
		e, err := s.EnqueueEmail(ctx, mail(kind, "", fmt.Sprintf("u%d@example.com", i)))
		require.NoError(t, err)
		if i == 1 {
			_, err = s.MarkEmailFailed(ctx, e.ID, "boom", clock.Now(), 1)
			require.NoError(t, err)
		}
		clock.Advance(time.Second)
	}
	p, err := s.ListEmails(ctx, store.EmailFilter{})
	require.NoError(t, err)
	require.Equal(t, 3, p.Total)
	assert.Equal(t, "u2@example.com", p.Items[0].To, "newest first")
	p, err = s.ListEmails(ctx, store.EmailFilter{Kind: "security.new_login", PageSize: 1, Page: 2})
	require.NoError(t, err)
	assert.Equal(t, 2, p.Total)
	require.Len(t, p.Items, 1)
	assert.Equal(t, "u0@example.com", p.Items[0].To)
	p, err = s.ListEmails(ctx, store.EmailFilter{Status: domain.EmailFailed})
	require.NoError(t, err)
	assert.Equal(t, 1, p.Total)
	_, err = s.ListEmails(ctx, store.EmailFilter{Status: "nope"})
	require.ErrorIs(t, err, store.ErrInvalid)

	storetest.AdminUser(t, s, domain.RoleOwner, func(u *domain.AdminUser) { u.Email = "owner1@example.com" })
	clock.Advance(time.Second)
	storetest.AdminUser(t, s, domain.RoleOwner, func(u *domain.AdminUser) { u.Email = "owner2@example.com" })
	storetest.AdminUser(t, s, domain.RoleOwner, func(u *domain.AdminUser) { u.Email = "gone@example.com"; u.Active = false })
	storetest.AdminUser(t, s, domain.RoleAdmin)
	owners, err := s.ListActiveOwnerEmails(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"owner1@example.com", "owner2@example.com"}, owners)
}
