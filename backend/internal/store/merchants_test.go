package store_test

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cryptopay/internal/auth"
	"cryptopay/internal/domain"
	"cryptopay/internal/secret"
	"cryptopay/internal/store"
	"cryptopay/internal/store/storetest"
)

func TestMerchantCRUDAndEncryption(t *testing.T) {
	t.Parallel()
	d := storetest.NewDB(t)
	s := d.Store

	whsec := auth.NewWebhookSecret()
	m, err := s.CreateMerchant(ctx, domain.Merchant{Name: " Shop ", WebhookURL: "https://shop.example/hook", WebhookSecret: whsec})
	require.NoError(t, err)
	assert.Equal(t, "Shop", m.Name)
	assert.Equal(t, domain.MerchantActive, m.Status)
	assert.Equal(t, whsec, m.WebhookSecret)

	// The column holds ciphertext, never the secret.
	var enc string
	require.NoError(t, d.Pool.QueryRow(ctx, `SELECT webhook_secret_enc FROM merchants WHERE id = $1`, m.ID).Scan(&enc))
	assert.True(t, secret.IsEncrypted(enc), enc)
	assert.NotContains(t, enc, whsec)
	assert.NotContains(t, enc, strings.TrimPrefix(whsec, "whsec_"))
	plain, err := storetest.Box().Decrypt(enc)
	require.NoError(t, err)
	assert.Equal(t, whsec, plain)

	got, err := s.GetMerchant(ctx, m.ID)
	require.NoError(t, err)
	assert.Equal(t, m, got)

	// A merchant without secret stores "".
	m2, err := s.CreateMerchant(ctx, domain.Merchant{Name: "No hook"})
	require.NoError(t, err)
	assert.Empty(t, m2.WebhookSecret)
	require.NoError(t, d.Pool.QueryRow(ctx, `SELECT webhook_secret_enc FROM merchants WHERE id = $1`, m2.ID).Scan(&enc))
	assert.Empty(t, enc)

	_, err = s.CreateMerchant(ctx, domain.Merchant{Name: " "})
	require.ErrorIs(t, err, store.ErrInvalid)
	_, err = s.CreateMerchant(ctx, domain.Merchant{Name: "x", Status: "weird"})
	require.ErrorIs(t, err, store.ErrInvalid)
	_, err = s.GetMerchant(ctx, uuid.New())
	require.ErrorIs(t, err, store.ErrNotFound)

	// Update.
	disabled := domain.MerchantDisabled
	up, err := s.UpdateMerchant(ctx, m.ID, store.MerchantUpdate{Status: &disabled, WebhookURL: storetest.Ptr("")})
	require.NoError(t, err)
	assert.Equal(t, domain.MerchantDisabled, up.Status)
	assert.Equal(t, "", up.WebhookURL)
	assert.Equal(t, "Shop", up.Name)
	assert.Equal(t, whsec, up.WebhookSecret)
	_, err = s.UpdateMerchant(ctx, m.ID, store.MerchantUpdate{Name: storetest.Ptr("")})
	require.ErrorIs(t, err, store.ErrInvalid)
	_, err = s.UpdateMerchant(ctx, uuid.New(), store.MerchantUpdate{})
	require.ErrorIs(t, err, store.ErrNotFound)

	// Rotate.
	newSec := auth.NewWebhookSecret()
	rot, err := s.RotateWebhookSecret(ctx, m.ID, newSec)
	require.NoError(t, err)
	assert.Equal(t, newSec, rot.WebhookSecret)
	got, err = s.GetMerchant(ctx, m.ID)
	require.NoError(t, err)
	assert.Equal(t, newSec, got.WebhookSecret)
	_, err = s.RotateWebhookSecret(ctx, m.ID, "")
	require.ErrorIs(t, err, store.ErrInvalid)

	// Wrong key cannot read it.
	other := store.New(d.Pool, secret.MustNew(strings.Repeat("k", 40)))
	_, err = other.GetMerchant(ctx, m.ID)
	require.ErrorIs(t, err, secret.ErrDecrypt)
}

func TestDeleteMerchant(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	m := storetest.Merchant(t, s)
	storetest.APIKey(t, s, m.ID)
	storetest.Invoice(t, s, m.ID)
	err := s.DeleteMerchant(ctx, m.ID)
	require.ErrorIs(t, err, store.ErrInUse)
	require.ErrorIs(t, err, store.ErrForeignKey)

	m2 := storetest.Merchant(t, s)
	k, _ := storetest.APIKey(t, s, m2.ID)
	require.NoError(t, s.DeleteMerchant(ctx, m2.ID))
	_, err = s.GetAPIKey(ctx, k.ID)
	require.ErrorIs(t, err, store.ErrNotFound, "keys cascade")
	require.ErrorIs(t, s.DeleteMerchant(ctx, m2.ID), store.ErrNotFound)
}

func TestListMerchants(t *testing.T) {
	t.Parallel()
	clk := storetest.FixedClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	s := storetest.New(t, store.WithClock(clk.Now))
	var ms []domain.Merchant
	for _, name := range []string{"Alpha Shop", "beta store", "Gamma_shop", "delta"} {
		ms = append(ms, storetest.Merchant(t, s, func(m *domain.Merchant) { m.Name = name }))
		clk.Advance(time.Minute)
	}
	_, err := s.UpdateMerchant(ctx, ms[3].ID, store.MerchantUpdate{Status: storetest.Ptr(domain.MerchantDisabled)})
	require.NoError(t, err)

	all, err := s.ListMerchants(ctx, store.MerchantFilter{})
	require.NoError(t, err)
	assert.Equal(t, 4, all.Total)
	assert.Equal(t, 1, all.Page)
	assert.Equal(t, store.DefaultPageSize, all.PageSize)
	require.Len(t, all.Items, 4)
	assert.Equal(t, ms[3].ID, all.Items[0].ID, "newest first")

	shop, err := s.ListMerchants(ctx, store.MerchantFilter{Q: "SHOP"})
	require.NoError(t, err)
	assert.Equal(t, 2, shop.Total)

	// LIKE wildcards in q are literal.
	under, err := s.ListMerchants(ctx, store.MerchantFilter{Q: "a_s"})
	require.NoError(t, err)
	assert.Equal(t, 1, under.Total)
	assert.Equal(t, "Gamma_shop", under.Items[0].Name)
	pct, err := s.ListMerchants(ctx, store.MerchantFilter{Q: "%"})
	require.NoError(t, err)
	assert.Zero(t, pct.Total)
	assert.NotNil(t, pct.Items)

	byID, err := s.ListMerchants(ctx, store.MerchantFilter{Q: ms[1].ID.String()})
	require.NoError(t, err)
	require.Equal(t, 1, byID.Total)
	assert.Equal(t, ms[1].ID, byID.Items[0].ID)

	dis, err := s.ListMerchants(ctx, store.MerchantFilter{Status: domain.MerchantDisabled})
	require.NoError(t, err)
	assert.Equal(t, 1, dis.Total)

	p2, err := s.ListMerchants(ctx, store.MerchantFilter{Page: 2, PageSize: 3})
	require.NoError(t, err)
	assert.Equal(t, 4, p2.Total)
	require.Len(t, p2.Items, 1)
	assert.Equal(t, ms[0].ID, p2.Items[0].ID)

	big, err := s.ListMerchants(ctx, store.MerchantFilter{PageSize: 1000, Page: -3})
	require.NoError(t, err)
	assert.Equal(t, store.MaxPageSize, big.PageSize)
	assert.Equal(t, 1, big.Page)
}

func TestAPIKeys(t *testing.T) {
	t.Parallel()
	clk := storetest.FixedClock(time.Date(2030, 6, 1, 0, 0, 0, 0, time.UTC))
	s := storetest.New(t, store.WithClock(clk.Now))
	m := storetest.Merchant(t, s)
	other := storetest.Merchant(t, s)

	plain, prefix, hash := auth.NewAPIKey()
	k, err := s.CreateAPIKey(ctx, domain.APIKey{MerchantID: m.ID, Name: "prod", Prefix: prefix, KeyHash: hash})
	require.NoError(t, err)
	assert.Equal(t, prefix, k.Prefix)
	assert.Nil(t, k.RevokedAt)
	assert.Nil(t, k.LastUsedAt)

	// Only the hash is stored.
	var n int
	require.NoError(t, s.Pool().QueryRow(ctx, `SELECT count(*) FROM api_keys WHERE key_hash = $1 OR prefix = $2`, plain, plain).Scan(&n))
	assert.Zero(t, n)

	_, err = s.CreateAPIKey(ctx, domain.APIKey{MerchantID: m.ID, Prefix: prefix, KeyHash: hash})
	require.ErrorIs(t, err, store.ErrConflict)
	assert.True(t, store.IsConstraint(err, store.ConstraintAPIKeyHash))
	_, err = s.CreateAPIKey(ctx, domain.APIKey{MerchantID: uuid.New(), Prefix: "p", KeyHash: "h"})
	require.ErrorIs(t, err, store.ErrForeignKey)
	_, err = s.CreateAPIKey(ctx, domain.APIKey{MerchantID: m.ID})
	require.ErrorIs(t, err, store.ErrInvalid)

	clk.Advance(time.Second)
	k2, _ := storetest.APIKey(t, s, m.ID)
	keys, err := s.ListAPIKeys(ctx, m.ID)
	require.NoError(t, err)
	require.Len(t, keys, 2)
	assert.Equal(t, k2.ID, keys[0].ID, "newest first")
	none, err := s.ListAPIKeys(ctx, other.ID)
	require.NoError(t, err)
	assert.NotNil(t, none)
	assert.Empty(t, none)

	// Lookup.
	gk, gm, err := s.LookupAPIKey(ctx, auth.HashToken(plain))
	require.NoError(t, err)
	assert.Equal(t, k.ID, gk.ID)
	assert.Equal(t, m.ID, gm.ID)
	assert.Equal(t, m.WebhookSecret, gm.WebhookSecret)
	_, _, err = s.LookupAPIKey(ctx, auth.HashToken("sk_live_unknown"))
	require.ErrorIs(t, err, store.ErrNotFound)

	// Touch is throttled.
	require.NoError(t, s.TouchAPIKey(ctx, k.ID))
	got, err := s.GetAPIKey(ctx, k.ID)
	require.NoError(t, err)
	require.NotNil(t, got.LastUsedAt)
	first := *got.LastUsedAt
	clk.Advance(30 * time.Second)
	require.NoError(t, s.TouchAPIKey(ctx, k.ID))
	got, _ = s.GetAPIKey(ctx, k.ID)
	assert.Equal(t, first, *got.LastUsedAt, "within the interval: not written")
	clk.Advance(store.APIKeyTouchInterval)
	require.NoError(t, s.TouchAPIKey(ctx, k.ID))
	got, _ = s.GetAPIKey(ctx, k.ID)
	assert.True(t, got.LastUsedAt.After(first))

	// Revoke: wrong merchant → not found; idempotent; lookup fails afterwards.
	_, err = s.RevokeAPIKey(ctx, other.ID, k.ID)
	require.ErrorIs(t, err, store.ErrNotFound)
	rk, err := s.RevokeAPIKey(ctx, m.ID, k.ID)
	require.NoError(t, err)
	require.NotNil(t, rk.RevokedAt)
	clk.Advance(time.Hour)
	rk2, err := s.RevokeAPIKey(ctx, m.ID, k.ID)
	require.NoError(t, err)
	assert.Equal(t, *rk.RevokedAt, *rk2.RevokedAt)
	_, _, err = s.LookupAPIKey(ctx, hash)
	require.ErrorIs(t, err, store.ErrNotFound)

	// Disabled merchant is still returned (caller rejects it).
	_, plain3 := storetest.APIKey(t, s, other.ID)
	_, err = s.UpdateMerchant(ctx, other.ID, store.MerchantUpdate{Status: storetest.Ptr(domain.MerchantDisabled)})
	require.NoError(t, err)
	_, gm, err = s.LookupAPIKey(ctx, auth.HashToken(plain3))
	require.NoError(t, err)
	assert.Equal(t, domain.MerchantDisabled, gm.Status)
}
