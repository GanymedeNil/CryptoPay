package store_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cryptopay/internal/catalog"
	"cryptopay/internal/domain"
	"cryptopay/internal/store"
	"cryptopay/internal/store/storetest"
)

func TestSettingsDefaultsAndRoundTrip(t *testing.T) {
	t.Parallel()
	d := storetest.NewDB(t)
	s := d.Store

	got, err := s.GetSettings(ctx)
	require.NoError(t, err)
	assert.Equal(t, domain.DefaultSettings(), got, "no row → defaults")

	want := domain.Settings{
		InvoiceTTLSeconds: 900, GracePeriodSeconds: 3600, UnderpayToleranceBps: 0, StablecoinPeg: false,
		RateMarkupBps: 150, WebhookMaxAttempts: 3, WebhookTimeoutSeconds: 5,
		CheckoutBaseURL: "https://pay.example.com", WalletConnectID: "wc-123",
	}
	put, err := s.PutSettings(ctx, want)
	require.NoError(t, err)
	assert.Equal(t, want, put)
	got, err = s.GetSettings(ctx)
	require.NoError(t, err)
	assert.Equal(t, want, got, "zero values (tolerance 0, peg false) are kept, not defaulted")

	// Fields missing from the stored JSON fall back to defaults.
	_, err = d.Pool.Exec(ctx, `UPDATE settings SET value = '{"invoice_ttl_seconds": 120}'`)
	require.NoError(t, err)
	got, err = s.GetSettings(ctx)
	require.NoError(t, err)
	exp := domain.DefaultSettings()
	exp.InvoiceTTLSeconds = 120
	assert.Equal(t, exp, got)
}

func TestEnsureSettings(t *testing.T) {
	t.Parallel()
	d := storetest.NewDB(t)
	s := d.Store
	require.NoError(t, s.EnsureSettings(ctx))
	got, err := s.GetSettings(ctx)
	require.NoError(t, err)
	assert.Equal(t, domain.DefaultSettings(), got)

	// Existing values win; missing keys are filled in.
	_, err = d.Pool.Exec(ctx, `UPDATE settings SET value = '{"invoice_ttl_seconds": 120, "checkout_base_url": "https://x"}'`)
	require.NoError(t, err)
	require.NoError(t, s.EnsureSettings(ctx))
	var raw string
	require.NoError(t, d.Pool.QueryRow(ctx, `SELECT value::text FROM settings`).Scan(&raw))
	assert.Contains(t, raw, `"webhook_max_attempts": 10`, "missing key persisted")
	got, err = s.GetSettings(ctx)
	require.NoError(t, err)
	assert.Equal(t, 120, got.InvoiceTTLSeconds)
	assert.Equal(t, "https://x", got.CheckoutBaseURL)

	// Idempotent: no update when nothing is missing.
	var before, after time.Time
	require.NoError(t, d.Pool.QueryRow(ctx, `SELECT updated_at FROM settings`).Scan(&before))
	require.NoError(t, s.EnsureSettings(ctx))
	require.NoError(t, d.Pool.QueryRow(ctx, `SELECT updated_at FROM settings`).Scan(&after))
	assert.Equal(t, before, after)
}

func TestBootstrapCatalog(t *testing.T) {
	t.Parallel()
	d := storetest.NewDB(t)
	s := d.Store

	res, err := s.Bootstrap(ctx)
	require.NoError(t, err)
	assert.Equal(t, len(catalog.Chains()), res.ChainsInserted)
	assert.Equal(t, len(catalog.Assets()), res.AssetsInserted)

	chains, err := s.ListChains(ctx)
	require.NoError(t, err)
	require.Len(t, chains, len(catalog.Chains()))
	for _, c := range chains {
		assert.Empty(t, c.RPCURLs, "catalog defaults are not stored in the database (%s)", c.ID)
	}
	btc, err := s.GetChain(ctx, "bitcoin")
	require.NoError(t, err)
	assert.Equal(t, "bc", btc.Params.Bech32HRP)
	assert.Equal(t, byte(0x05), btc.Params.P2SHVersion)
	assert.Equal(t, 30*time.Second, btc.PollInterval)
	assert.True(t, btc.Enabled)
	sep, err := s.GetChain(ctx, "sepolia")
	require.NoError(t, err)
	assert.False(t, sep.Enabled)
	assert.True(t, sep.IsTestnet)
	assert.Equal(t, "11155111", sep.NetworkID)

	var empty int
	require.NoError(t, d.Pool.QueryRow(ctx, `SELECT count(*) FROM chains WHERE rpc_urls_enc <> ''`).Scan(&empty))
	assert.Zero(t, empty)

	// Admin edits survive a re-bootstrap; nothing is inserted twice.
	_, err = s.UpdateChain(ctx, "ethereum", store.ChainUpdate{Confirmations: storetest.Ptr(30)})
	require.NoError(t, err)
	_, err = s.UpdateAsset(ctx, "usdt-tron", store.AssetUpdate{Enabled: storetest.Ptr(false), Name: storetest.Ptr("Custom")})
	require.NoError(t, err)
	_, err = s.PutSettings(ctx, domain.Settings{InvoiceTTLSeconds: 1})
	require.NoError(t, err)

	res, err = s.Bootstrap(ctx)
	require.NoError(t, err)
	assert.Zero(t, res.ChainsInserted)
	assert.Zero(t, res.AssetsInserted)
	eth, err := s.GetChain(ctx, "ethereum")
	require.NoError(t, err)
	assert.Equal(t, 30, eth.Confirmations)
	a, err := s.GetAsset(ctx, "usdt-tron")
	require.NoError(t, err)
	assert.False(t, a.Enabled)
	assert.Equal(t, "Custom", a.Name)
	st, err := s.GetSettings(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, st.InvoiceTTLSeconds)

	// Catalog asset fields round-trip exactly.
	for _, want := range catalog.Assets() {
		if want.ID == "usdt-tron" {
			continue
		}
		got, err := s.GetAsset(ctx, want.ID)
		require.NoError(t, err)
		want.CreatedAt, want.UpdatedAt = got.CreatedAt, got.UpdatedAt
		assert.Equal(t, want, got)
	}
}

func TestEnsureCatalogPartial(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	chains := catalog.Chains()[:2]
	res, err := s.EnsureCatalog(ctx, chains, nil)
	require.NoError(t, err)
	assert.Equal(t, 2, res.ChainsInserted)
	// An asset of an unknown chain is a foreign key violation.
	_, err = s.EnsureCatalog(ctx, nil, []domain.Asset{{ID: "x-nowhere", ChainID: "nowhere", Symbol: "X", Name: "X",
		Decimals: 6, QuotePrecision: 2, RateKey: "x"}})
	require.ErrorIs(t, err, store.ErrForeignKey)
}
