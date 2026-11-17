package store_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cryptopay/internal/domain"
	"cryptopay/internal/store"
	"cryptopay/internal/store/storetest"
)

func TestLoginHistory(t *testing.T) {
	t.Parallel()
	clock := storetest.FixedClock(time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC))
	s := storetest.New(t, store.WithClock(clock.Now))
	u := storetest.AdminUser(t, s, domain.RoleAdmin)

	before, seen, err := s.LoginHistory(ctx, u.ID, "192.0.2.1")
	require.NoError(t, err)
	assert.False(t, before)
	assert.False(t, seen)

	storetest.Session(t, s, u.ID, func(x *domain.AdminSession) { x.IP = "192.0.2.1" })
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
}
