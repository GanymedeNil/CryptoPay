package storetest_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cryptopay/internal/auth"
	"cryptopay/internal/db"
	"cryptopay/internal/domain"
	"cryptopay/internal/store"
	"cryptopay/internal/store/storetest"
)

func TestSchemaLifecycle(t *testing.T) {
	ctx := context.Background()
	var schema string
	t.Run("inner", func(t *testing.T) {
		d := storetest.NewDB(t)
		schema = d.Schema
		var cur string
		require.NoError(t, d.Pool.QueryRow(ctx, "SELECT current_schema()").Scan(&cur))
		assert.Equal(t, d.Schema, cur)

		// The URL points at the same schema for code that opens its own pool.
		pool, err := db.Open(ctx, d.URL)
		require.NoError(t, err)
		defer pool.Close()
		require.NoError(t, pool.QueryRow(ctx, "SELECT current_schema()").Scan(&cur))
		assert.Equal(t, d.Schema, cur)
		v, err := db.Version(ctx, pool)
		require.NoError(t, err)
		assert.Positive(t, v)
	})
	// After the subtest's cleanup the schema is gone.
	base := storetest.BaseURL(t)
	pool, err := db.Open(ctx, base)
	require.NoError(t, err)
	defer pool.Close()
	var exists bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = $1)`, schema).Scan(&exists))
	assert.False(t, exists)
}

func TestEmptySchemaHasNoTables(t *testing.T) {
	t.Parallel()
	d := storetest.EmptySchema(t)
	assert.Nil(t, d.Store)
	var n int
	require.NoError(t, d.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM pg_tables WHERE schemaname = $1`, d.Schema).Scan(&n))
	assert.Zero(t, n)
}

func TestFixtures(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := storetest.NewSeeded(t)

	m := storetest.Merchant(t, s)
	assert.NotEmpty(t, m.WebhookSecret)
	k, plain := storetest.APIKey(t, s, m.ID)
	gk, gm, err := s.LookupAPIKey(ctx, auth.HashToken(plain))
	require.NoError(t, err)
	assert.Equal(t, k.ID, gk.ID)
	assert.Equal(t, m.ID, gm.ID)

	u := storetest.AdminUser(t, s, domain.RoleOwner)
	require.NoError(t, auth.VerifyPassword(u.PasswordHash, storetest.Password))
	sess, token := storetest.Session(t, s, u.ID)
	gs, _, err := s.GetSessionByTokenHash(ctx, auth.HashToken(token), s.Now())
	require.NoError(t, err)
	assert.Equal(t, sess.ID, gs.ID)

	for fam := range storetest.StaticAddresses {
		storetest.Wallet(t, s, fam, domain.WalletStatic)
	}
	xw := storetest.Wallet(t, s, domain.FamilyEVM, domain.WalletXpub)
	c := storetest.AssignWallet(t, s, "sepolia", xw.ID)
	assert.True(t, c.Enabled)

	inv := storetest.Invoice(t, s, m.ID)
	q1 := storetest.Quote(t, s, inv, "eth-sepolia", xw)
	require.NotNil(t, q1.AddressIndex)
	assert.EqualValues(t, 0, *q1.AddressIndex)
	inv2 := storetest.Invoice(t, s, m.ID)
	q2 := storetest.Quote(t, s, inv2, "eth-sepolia", xw)
	assert.EqualValues(t, 1, *q2.AddressIndex)
	assert.NotEqual(t, q1.Address, q2.Address)

	tr := storetest.TransferFor(t, s, q1, 42)
	assert.Equal(t, q1.ID, *tr.QuoteID)
	assert.Equal(t, inv.ID, *tr.InvoiceID)
	assert.Equal(t, "", tr.TokenContract)
	storetest.Transfer(t, s)

	e := storetest.WebhookEvent(t, s, inv)
	assert.Equal(t, domain.EventPaid, e.EventType)

	clk := storetest.FixedClock(s.Now())
	before := clk.Now()
	clk.Advance(5)
	assert.True(t, clk.Now().After(before))

	cat, err := s.LoadCatalog(ctx)
	require.NoError(t, err)
	assert.NotEmpty(t, cat.Assets)
	_ = store.AssetID
}
