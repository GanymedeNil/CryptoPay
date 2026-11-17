package store_test

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cryptopay/internal/domain"
	"cryptopay/internal/store"
	"cryptopay/internal/store/storetest"
)

func TestTOTPStateLifecycle(t *testing.T) {
	t.Parallel()
	d := storetest.NewDB(t)
	s := d.Store
	u := storetest.AdminUser(t, s, domain.RoleAdmin)

	st, err := s.GetAdminTOTP(ctx, u.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.AdminTOTP{}, st)

	require.NoError(t, s.SetPendingTOTPSecret(ctx, u.ID, "JBSWY3DPEHPK3PXP"))
	var enc string
	require.NoError(t, d.Pool.QueryRow(ctx, `SELECT totp_secret_enc FROM admin_users WHERE id = $1`, u.ID).Scan(&enc))
	assert.True(t, strings.HasPrefix(enc, "v1:"), "encrypted at rest: %s", enc)
	assert.NotContains(t, enc, "JBSWY3DPEHPK3PXP")
	st, err = s.GetAdminTOTP(ctx, u.ID)
	require.NoError(t, err)
	assert.Equal(t, "JBSWY3DPEHPK3PXP", st.Secret)
	assert.Nil(t, st.EnabledAt, "pending")
	got, err := s.GetAdminUser(ctx, u.ID)
	require.NoError(t, err)
	assert.False(t, got.TwoFactorEnabled())

	// GetAdminTOTPForUpdate needs a transaction.
	_, err = s.GetAdminTOTPForUpdate(ctx, u.ID)
	require.ErrorIs(t, err, store.ErrTxRequired)

	enabled, err := s.EnableTOTP(ctx, u.ID, 100)
	require.NoError(t, err)
	assert.True(t, enabled.TwoFactorEnabled())
	_, err = s.EnableTOTP(ctx, u.ID, 101)
	require.ErrorIs(t, err, store.ErrConflict, "already enabled")
	require.ErrorIs(t, s.SetPendingTOTPSecret(ctx, u.ID, "OTHER"), store.ErrConflict, "cannot replace an enabled secret")

	// Replay protection: only newer steps advance.
	require.ErrorIs(t, s.AdvanceTOTPStep(ctx, u.ID, 100), store.ErrConflict)
	require.ErrorIs(t, s.AdvanceTOTPStep(ctx, u.ID, 99), store.ErrConflict)
	require.NoError(t, s.AdvanceTOTPStep(ctx, u.ID, 101))
	require.ErrorIs(t, s.AdvanceTOTPStep(ctx, uuid.New(), 1), store.ErrNotFound)

	// Concurrent use of one code: exactly one wins.
	var wins atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if s.AdvanceTOTPStep(ctx, u.ID, 102) == nil {
				wins.Add(1)
			}
		})
	}
	wg.Wait()
	assert.EqualValues(t, 1, wins.Load())

	// Clearing removes everything (and reports it).
	other := storetest.AdminUser(t, s, domain.RoleAdmin)
	require.NoError(t, s.WithTx(ctx, func(tx *store.Store) error {
		require.NoError(t, tx.ReplaceRecoveryCodes(ctx, u.ID, []string{"h1", "h2"}))
		return tx.ReplaceRecoveryCodes(ctx, other.ID, []string{"o1"})
	}))
	had, err := s.ClearTOTP(ctx, u.ID)
	require.NoError(t, err)
	assert.True(t, had)
	st, err = s.GetAdminTOTP(ctx, u.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.AdminTOTP{}, st)
	n, err := s.CountRecoveryCodes(ctx, u.ID)
	require.NoError(t, err)
	assert.Zero(t, n)
	n, err = s.CountRecoveryCodes(ctx, other.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "other users untouched")
	had, err = s.ClearTOTP(ctx, u.ID)
	require.NoError(t, err)
	assert.False(t, had)
	_, err = s.ClearTOTP(ctx, uuid.New())
	require.ErrorIs(t, err, store.ErrNotFound)
	// Enabling needs a pending secret.
	_, err = s.EnableTOTP(ctx, u.ID, 5)
	require.ErrorIs(t, err, store.ErrConflict)
}

func TestRecoveryCodes(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	u := storetest.AdminUser(t, s, domain.RoleViewer)
	v := storetest.AdminUser(t, s, domain.RoleViewer)
	require.ErrorIs(t, s.ReplaceRecoveryCodes(ctx, u.ID, []string{"a"}), store.ErrTxRequired)
	require.NoError(t, s.WithTx(ctx, func(tx *store.Store) error {
		return tx.ReplaceRecoveryCodes(ctx, u.ID, []string{"a", "b", "c"})
	}))
	left, err := s.UseRecoveryCode(ctx, u.ID, "b")
	require.NoError(t, err)
	assert.Equal(t, 2, left)
	_, err = s.UseRecoveryCode(ctx, u.ID, "b")
	require.ErrorIs(t, err, store.ErrNotFound, "single use")
	_, err = s.UseRecoveryCode(ctx, v.ID, "a")
	require.ErrorIs(t, err, store.ErrNotFound, "another user's code")
	// Replacing invalidates the old set.
	require.NoError(t, s.WithTx(ctx, func(tx *store.Store) error {
		return tx.ReplaceRecoveryCodes(ctx, u.ID, []string{"d", "e"})
	}))
	_, err = s.UseRecoveryCode(ctx, u.ID, "a")
	require.ErrorIs(t, err, store.ErrNotFound)
	left, err = s.UseRecoveryCode(ctx, u.ID, "e")
	require.NoError(t, err)
	assert.Equal(t, 1, left)
}

func TestLoginChallenges(t *testing.T) {
	t.Parallel()
	clock := storetest.FixedClock(time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC))
	s := storetest.New(t, store.WithClock(clock.Now))
	u := storetest.AdminUser(t, s, domain.RoleAdmin)
	now := clock.Now()
	c, err := s.CreateLoginChallenge(ctx, domain.LoginChallenge{UserID: u.ID, TokenHash: "th1", IP: "192.0.2.1", UserAgent: "ua",
		ExpiresAt: now.Add(5 * time.Minute)})
	require.NoError(t, err)
	assert.Zero(t, c.Attempts)
	_, err = s.CreateLoginChallenge(ctx, domain.LoginChallenge{UserID: u.ID, TokenHash: "th1", ExpiresAt: now.Add(time.Minute)})
	require.ErrorIs(t, err, store.ErrConflict)
	_, err = s.CreateLoginChallenge(ctx, domain.LoginChallenge{UserID: u.ID})
	require.ErrorIs(t, err, store.ErrInvalid)

	got, err := s.GetLoginChallenge(ctx, "th1", now)
	require.NoError(t, err)
	assert.Equal(t, c.ID, got.ID)
	_, err = s.GetLoginChallenge(ctx, "th1", now.Add(5*time.Minute))
	require.ErrorIs(t, err, store.ErrNotFound, "expired")
	_, err = s.GetLoginChallengeForUpdate(ctx, "th1", now)
	require.ErrorIs(t, err, store.ErrTxRequired)

	for want := 1; want <= 3; want++ {
		n, err := s.CountChallengeFailure(ctx, c.ID)
		require.NoError(t, err)
		assert.Equal(t, want, n)
	}
	require.NoError(t, s.DeleteLoginChallenge(ctx, c.ID))
	_, err = s.GetLoginChallenge(ctx, "th1", now)
	require.ErrorIs(t, err, store.ErrNotFound)

	for i, exp := range []time.Duration{time.Minute, 10 * time.Minute} {
		_, err := s.CreateLoginChallenge(ctx, domain.LoginChallenge{UserID: u.ID, TokenHash: "x" + string(rune('0'+i)), ExpiresAt: now.Add(exp)})
		require.NoError(t, err)
	}
	gone, err := s.DeleteExpiredLoginChallenges(ctx, now.Add(5*time.Minute))
	require.NoError(t, err)
	assert.EqualValues(t, 1, gone)
	gone, err = s.DeleteUserLoginChallenges(ctx, u.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 1, gone)
}

func TestLoginHistoryAndReset(t *testing.T) {
	t.Parallel()
	clock := storetest.FixedClock(time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC))
	s := storetest.New(t, store.WithClock(clock.Now))
	u := storetest.AdminUser(t, s, domain.RoleAdmin)
	v := storetest.AdminUser(t, s, domain.RoleAdmin)

	before, seen, err := s.LoginHistory(ctx, u.ID, "192.0.2.1")
	require.NoError(t, err)
	assert.False(t, before)
	assert.False(t, seen)

	storetest.Session(t, s, u.ID, func(x *domain.AdminSession) { x.IP = "192.0.2.1" })
	clock.Advance(time.Minute)
	storetest.Session(t, s, u.ID, func(x *domain.AdminSession) { x.ExpiresAt = clock.Now().Add(-time.Second) })
	storetest.Session(t, s, v.ID)

	before, seen, err = s.LoginHistory(ctx, u.ID, "192.0.2.1")
	require.NoError(t, err)
	assert.True(t, before)
	assert.True(t, seen)
	// Seen through the audit log too (sessions may have been deleted).
	_, err = s.InsertAuditLog(ctx, domain.AuditLog{Action: "auth.login", TargetType: "admin_user", TargetID: u.ID.String(), IP: "198.51.100.9"})
	require.NoError(t, err)
	_, seen, err = s.LoginHistory(ctx, u.ID, "198.51.100.9")
	require.NoError(t, err)
	assert.True(t, seen)
	_, seen, err = s.LoginHistory(ctx, u.ID, "203.0.113.1")
	require.NoError(t, err)
	assert.False(t, seen)
	// Other actions do not count.
	w := storetest.AdminUser(t, s, domain.RoleAdmin)
	_, err = s.InsertAuditLog(ctx, domain.AuditLog{Action: "user.update", TargetType: "admin_user", TargetID: w.ID.String(), IP: "203.0.113.1"})
	require.NoError(t, err)
	before, _, err = s.LoginHistory(ctx, w.ID, "203.0.113.1")
	require.NoError(t, err)
	assert.False(t, before)

	// Owner reset: secret, codes, sessions and challenges.
	require.NoError(t, s.SetPendingTOTPSecret(ctx, u.ID, "JBSWY3DPEHPK3PXP"))
	_, err = s.EnableTOTP(ctx, u.ID, 1)
	require.NoError(t, err)
	_, err = s.CreateLoginChallenge(ctx, domain.LoginChallenge{UserID: u.ID, TokenHash: "c1", ExpiresAt: clock.Now().Add(time.Minute)})
	require.NoError(t, err)
	_, _, err = s.ResetAdminTwoFactor(ctx, u.ID)
	require.ErrorIs(t, err, store.ErrTxRequired)
	var reset bool
	var n int64
	require.NoError(t, s.WithTx(ctx, func(tx *store.Store) error {
		var err error
		reset, n, err = tx.ResetAdminTwoFactor(ctx, u.ID)
		return err
	}))
	assert.True(t, reset)
	assert.EqualValues(t, 2, n, "the live and the expired session")
	_, err = s.GetLoginChallenge(ctx, "c1", clock.Now())
	require.ErrorIs(t, err, store.ErrNotFound)
	require.NoError(t, s.WithTx(ctx, func(tx *store.Store) error {
		var err error
		reset, n, err = tx.ResetAdminTwoFactor(ctx, v.ID)
		return err
	}))
	assert.False(t, reset, "nothing to reset")
	assert.Zero(t, n)
	var left int
	require.NoError(t, s.Pool().QueryRow(ctx, `SELECT count(*) FROM admin_sessions WHERE user_id = $1`, v.ID).Scan(&left))
	assert.Equal(t, 1, left, "a user without 2FA keeps the sessions")
}
