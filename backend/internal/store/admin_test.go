package store_test

import (
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cryptopay/internal/auth"
	"cryptopay/internal/domain"
	"cryptopay/internal/store"
	"cryptopay/internal/store/storetest"
)

func TestAdminUsersCRUD(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)

	u, err := s.CreateAdminUser(ctx, domain.AdminUser{Email: "  Alice@Example.COM ", PasswordHash: "h1", Role: domain.RoleOwner, Active: true})
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, u.ID)
	assert.Equal(t, "Alice@Example.COM", u.Email)
	assert.Nil(t, u.LastLoginAt)

	got, err := s.GetAdminUserByEmail(ctx, "alice@example.com")
	require.NoError(t, err)
	assert.Equal(t, u, got)
	got, err = s.GetAdminUser(ctx, u.ID)
	require.NoError(t, err)
	assert.Equal(t, u, got)
	_, err = s.GetAdminUserByEmail(ctx, "bob@example.com")
	require.ErrorIs(t, err, store.ErrNotFound)

	// Case-insensitive uniqueness.
	_, err = s.CreateAdminUser(ctx, domain.AdminUser{Email: "ALICE@example.com", PasswordHash: "h", Role: domain.RoleAdmin})
	require.ErrorIs(t, err, store.ErrConflict)
	assert.True(t, store.IsConstraint(err, store.ConstraintAdminUserEmail), err)

	// Validation.
	_, err = s.CreateAdminUser(ctx, domain.AdminUser{Email: "x@y", PasswordHash: "h", Role: "god"})
	require.ErrorIs(t, err, store.ErrInvalid)
	_, err = s.CreateAdminUser(ctx, domain.AdminUser{Email: "", PasswordHash: "h", Role: domain.RoleAdmin})
	require.ErrorIs(t, err, store.ErrInvalid)

	b := storetest.AdminUser(t, s, domain.RoleViewer)
	list, err := s.ListAdminUsers(ctx)
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Equal(t, u.ID, list[0].ID)
	n, err := s.CountAdminUsers(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, n)

	// Update.
	role := domain.RoleAdmin
	upd, err := s.UpdateAdminUser(ctx, b.ID, store.AdminUserUpdate{Role: &role, Active: storetest.Ptr(false), PasswordHash: storetest.Ptr("h2")})
	require.NoError(t, err)
	assert.Equal(t, domain.RoleAdmin, upd.Role)
	assert.False(t, upd.Active)
	assert.Equal(t, "h2", upd.PasswordHash)
	assert.Equal(t, b.Email, upd.Email, "unchanged")
	_, err = s.UpdateAdminUser(ctx, b.ID, store.AdminUserUpdate{Email: storetest.Ptr("alice@EXAMPLE.com")})
	require.ErrorIs(t, err, store.ErrConflict)
	_, err = s.UpdateAdminUser(ctx, b.ID, store.AdminUserUpdate{Role: storetest.Ptr(domain.Role("x"))})
	require.ErrorIs(t, err, store.ErrInvalid)
	_, err = s.UpdateAdminUser(ctx, uuid.New(), store.AdminUserUpdate{Active: storetest.Ptr(true)})
	require.ErrorIs(t, err, store.ErrNotFound)

	// Touch login.
	require.NoError(t, s.TouchAdminLogin(ctx, u.ID))
	got, err = s.GetAdminUser(ctx, u.ID)
	require.NoError(t, err)
	require.NotNil(t, got.LastLoginAt)
	require.ErrorIs(t, s.TouchAdminLogin(ctx, uuid.New()), store.ErrNotFound)

	// Delete cascades sessions.
	sess, _ := storetest.Session(t, s, b.ID)
	require.NoError(t, s.DeleteAdminUser(ctx, b.ID))
	require.ErrorIs(t, s.DeleteAdminUser(ctx, b.ID), store.ErrNotFound)
	_, _, err = s.GetSessionByTokenHash(ctx, sess.TokenHash, time.Now())
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestCountActiveOwners(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	n, err := s.CountActiveOwners(ctx)
	require.NoError(t, err)
	assert.Zero(t, n)
	storetest.AdminUser(t, s, domain.RoleOwner)
	storetest.AdminUser(t, s, domain.RoleOwner, func(u *domain.AdminUser) { u.Active = false })
	storetest.AdminUser(t, s, domain.RoleAdmin)
	n, err = s.CountActiveOwners(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
}

// Two owners try to demote each other concurrently: with CountActiveOwners
// inside the transaction exactly one demotion succeeds.
func TestLastOwnerGuardIsRaceFree(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	a := storetest.AdminUser(t, s, domain.RoleOwner)
	b := storetest.AdminUser(t, s, domain.RoleOwner)

	errLast := assert.AnError
	demote := func(id uuid.UUID) error {
		return s.WithTx(ctx, func(tx *store.Store) error {
			n, err := tx.CountActiveOwners(ctx)
			if err != nil {
				return err
			}
			if n <= 1 {
				return errLast
			}
			time.Sleep(50 * time.Millisecond) // widen the race window
			_, err = tx.UpdateAdminUser(ctx, id, store.AdminUserUpdate{Role: storetest.Ptr(domain.RoleAdmin)})
			return err
		})
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, id := range []uuid.UUID{a.ID, b.ID} {
		wg.Go(func() { errs[i] = demote(id) })
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
		} else {
			require.ErrorIs(t, err, errLast)
		}
	}
	assert.Equal(t, 1, ok)
	n, err := s.CountActiveOwners(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
}

func TestEnsureOwner(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	hash := storetest.PasswordHash()
	var wg sync.WaitGroup
	created := make([]bool, 4)
	for i := range created {
		wg.Go(func() {
			var err error
			created[i], err = s.EnsureOwner(ctx, "root@example.com", hash)
			assert.NoError(t, err)
		})
	}
	wg.Wait()
	n := 0
	for _, c := range created {
		if c {
			n++
		}
	}
	assert.Equal(t, 1, n)
	u, err := s.GetAdminUserByEmail(ctx, "ROOT@example.com")
	require.NoError(t, err)
	assert.Equal(t, domain.RoleOwner, u.Role)
	assert.True(t, u.Active)
	require.NoError(t, auth.VerifyPassword(u.PasswordHash, storetest.Password))

	again, err := s.EnsureOwner(ctx, "other@example.com", hash)
	require.NoError(t, err)
	assert.False(t, again, "users exist → nothing created")
	_, err = s.EnsureOwner(ctx, "", hash)
	require.ErrorIs(t, err, store.ErrInvalid)
}

func TestSessions(t *testing.T) {
	t.Parallel()
	clk := storetest.FixedClock(time.Date(2031, 5, 1, 12, 0, 0, 0, time.UTC))
	s := storetest.New(t, store.WithClock(clk.Now))
	u := storetest.AdminUser(t, s, domain.RoleAdmin)
	now := clk.Now()

	plain, hash := auth.NewSessionToken()
	x, err := s.CreateSession(ctx, domain.AdminSession{UserID: u.ID, TokenHash: hash, IP: "10.0.0.1", UserAgent: "UA",
		ExpiresAt: now.Add(time.Hour)})
	require.NoError(t, err)
	assert.Equal(t, now, x.CreatedAt)
	assert.Equal(t, auth.HashToken(plain), x.TokenHash)

	gs, gu, err := s.GetSessionByTokenHash(ctx, hash, now)
	require.NoError(t, err)
	assert.Equal(t, x, gs)
	assert.Equal(t, u.ID, gu.ID)
	assert.Equal(t, u.Email, gu.Email)

	// Expired at exactly expires_at.
	_, _, err = s.GetSessionByTokenHash(ctx, hash, now.Add(time.Hour))
	require.ErrorIs(t, err, store.ErrNotFound)
	// Extend.
	require.NoError(t, s.ExtendSession(ctx, x.ID, now.Add(2*time.Hour)))
	_, _, err = s.GetSessionByTokenHash(ctx, hash, now.Add(time.Hour))
	require.NoError(t, err)
	require.ErrorIs(t, s.ExtendSession(ctx, uuid.New(), now), store.ErrNotFound)

	// Inactive user → not found.
	_, err = s.UpdateAdminUser(ctx, u.ID, store.AdminUserUpdate{Active: storetest.Ptr(false)})
	require.NoError(t, err)
	_, _, err = s.GetSessionByTokenHash(ctx, hash, now)
	require.ErrorIs(t, err, store.ErrNotFound)
	_, err = s.UpdateAdminUser(ctx, u.ID, store.AdminUserUpdate{Active: storetest.Ptr(true)})
	require.NoError(t, err)

	// Duplicate token hash.
	_, err = s.CreateSession(ctx, domain.AdminSession{UserID: u.ID, TokenHash: hash, ExpiresAt: now.Add(time.Hour)})
	require.ErrorIs(t, err, store.ErrConflict)
	assert.True(t, store.IsConstraint(err, store.ConstraintSessionTokenHash), err)
	// Unknown user.
	_, err = s.CreateSession(ctx, domain.AdminSession{UserID: uuid.New(), TokenHash: "zz", ExpiresAt: now.Add(time.Hour)})
	require.ErrorIs(t, err, store.ErrForeignKey)

	// Delete by hash (logout); idempotent.
	require.NoError(t, s.DeleteSessionByTokenHash(ctx, hash))
	require.NoError(t, s.DeleteSessionByTokenHash(ctx, hash))
	_, _, err = s.GetSessionByTokenHash(ctx, hash, now)
	require.ErrorIs(t, err, store.ErrNotFound)

	// Delete expired.
	old, _ := storetest.Session(t, s, u.ID, func(x *domain.AdminSession) { x.ExpiresAt = now.Add(-time.Minute) })
	fresh, _ := storetest.Session(t, s, u.ID)
	n, err := s.DeleteExpiredSessions(ctx, now)
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
	_, _, err = s.GetSessionByTokenHash(ctx, old.TokenHash, now.Add(-time.Hour))
	require.ErrorIs(t, err, store.ErrNotFound)

	// Delete all sessions of a user except the current one.
	other, _ := storetest.Session(t, s, u.ID)
	n, err = s.DeleteUserSessions(ctx, u.ID, fresh.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
	_, _, err = s.GetSessionByTokenHash(ctx, other.TokenHash, now)
	require.ErrorIs(t, err, store.ErrNotFound)
	_, _, err = s.GetSessionByTokenHash(ctx, fresh.TokenHash, now)
	require.NoError(t, err)
	n, err = s.DeleteUserSessions(ctx, u.ID, uuid.Nil)
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)

	require.NoError(t, s.DeleteSession(ctx, uuid.New()))
}
