package store_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cryptopay/internal/catalog"
	"cryptopay/internal/domain"
	"cryptopay/internal/store"
	"cryptopay/internal/store/storetest"
)

func assetIDs(as []domain.Asset) []string {
	out := make([]string, len(as))
	for i, a := range as {
		out[i] = a.ID
	}
	return out
}

func TestListAssets(t *testing.T) {
	t.Parallel()
	s := storetest.NewSeeded(t)
	all, err := s.ListAssets(ctx, store.AssetFilter{})
	require.NoError(t, err)
	assert.Len(t, all, len(catalog.Assets()))
	for i := 1; i < len(all); i++ {
		assert.LessOrEqual(t, all[i-1].SortOrder, all[i].SortOrder)
	}
	tron, err := s.ListAssets(ctx, store.AssetFilter{ChainID: "tron"})
	require.NoError(t, err)
	assert.Equal(t, []string{"usdt-tron", "trx-tron"}, assetIDs(tron))

	_, err = s.UpdateAsset(ctx, "trx-tron", store.AssetUpdate{Enabled: storetest.Ptr(false)})
	require.NoError(t, err)
	en, err := s.ListAssets(ctx, store.AssetFilter{ChainID: "tron", EnabledOnly: true})
	require.NoError(t, err)
	assert.Equal(t, []string{"usdt-tron"}, assetIDs(en))

	none, err := s.ListAssets(ctx, store.AssetFilter{ChainID: "nope"})
	require.NoError(t, err)
	assert.NotNil(t, none)
	assert.Empty(t, none)

	a, err := s.GetAsset(ctx, "usdc-base")
	require.NoError(t, err)
	assert.Equal(t, "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913", a.ContractAddress)
	assert.Equal(t, 6, a.Decimals)
	assert.True(t, a.IsStablecoin)
	_, err = s.GetAsset(ctx, "nope")
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestPayableAssets(t *testing.T) {
	t.Parallel()
	s := storetest.NewSeeded(t)
	m := storetest.Merchant(t, s)
	p, err := s.ListPayableAssets(ctx, m.ID)
	require.NoError(t, err)
	assert.Empty(t, p, "no wallets assigned yet")
	assert.NotNil(t, p)

	tw := storetest.Wallet(t, s, domain.FamilyTron, domain.WalletXpub)
	storetest.AssignWallet(t, s, "tron", tw.ID)
	sw := storetest.Wallet(t, s, domain.FamilySolana, domain.WalletStatic)
	storetest.AssignWallet(t, s, "solana", sw.ID)
	// A disabled chain with a wallet is not payable.
	ew := storetest.Wallet(t, s, domain.FamilyEVM, domain.WalletXpub)
	_, err = s.UpdateChain(ctx, "sepolia", store.ChainUpdate{SetWallet: true, WalletID: &ew.ID})
	require.NoError(t, err)
	// A disabled asset is not payable.
	_, err = s.UpdateAsset(ctx, "sol-solana", store.AssetUpdate{Enabled: storetest.Ptr(false)})
	require.NoError(t, err)

	p, err = s.ListPayableAssets(ctx, m.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"usdt-tron", "usdt-solana", "usdc-solana", "pyusd-solana", "trx-tron"}, assetIDs(p))

	_, err = s.GetPayableAsset(ctx, m.ID, "usdt-tron")
	require.NoError(t, err)
	_, err = s.GetPayableAsset(ctx, m.ID, "sol-solana")
	require.ErrorIs(t, err, store.ErrNotFound)
	_, err = s.GetPayableAsset(ctx, m.ID, "eth-sepolia")
	require.ErrorIs(t, err, store.ErrNotFound)

	cat, err := s.LoadCatalog(ctx)
	require.NoError(t, err)
	c, ok := cat.Chain("solana")
	require.True(t, ok)
	assert.Equal(t, sw.ID, *c.WalletID)
	a, ok := cat.Asset("usdt-tron")
	require.True(t, ok)
	assert.Equal(t, "tron", a.ChainID)
	_, ok = cat.Asset("nope")
	assert.False(t, ok)
	_, ok = cat.Chain("nope")
	assert.False(t, ok)
	byC, ok := cat.AssetByContract("tron", "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t")
	require.True(t, ok)
	assert.Equal(t, "usdt-tron", byC.ID)
	native, ok := cat.AssetByContract("tron", "")
	require.True(t, ok)
	assert.Equal(t, "trx-tron", native.ID)
}

func TestCreateAndUpdateAsset(t *testing.T) {
	t.Parallel()
	s := storetest.NewSeeded(t)

	a, err := s.CreateAsset(ctx, domain.Asset{ChainID: "polygon", Symbol: "DAI", Name: "Dai",
		ContractAddress: "0x8f3Cf7ad23Cd3CaDbD9735AFf958023239c6A063", Decimals: 18, QuotePrecision: 2,
		RateKey: "dai", IsStablecoin: true, Enabled: true, SortOrder: 50})
	require.NoError(t, err)
	assert.Equal(t, "dai-polygon", a.ID)
	assert.Equal(t, store.AssetID("DAI", "polygon"), a.ID)
	got, err := s.GetAsset(ctx, a.ID)
	require.NoError(t, err)
	assert.Equal(t, a, got)

	// Conflicts.
	_, err = s.CreateAsset(ctx, domain.Asset{ChainID: "polygon", Symbol: "dai", ContractAddress: "0xother",
		Decimals: 18, QuotePrecision: 2, RateKey: "dai"})
	require.ErrorIs(t, err, store.ErrConflict)
	assert.True(t, store.IsConstraint(err, store.ConstraintAssetPK), err)
	_, err = s.CreateAsset(ctx, domain.Asset{ID: "dai2-polygon", ChainID: "polygon", Symbol: "DAI2",
		ContractAddress: "0x8f3Cf7ad23Cd3CaDbD9735AFf958023239c6A063", Decimals: 18, QuotePrecision: 2, RateKey: "dai"})
	require.ErrorIs(t, err, store.ErrConflict)
	assert.True(t, store.IsConstraint(err, store.ConstraintAssetChainContract), err)
	// Unknown chain, precision > decimals, missing fields.
	_, err = s.CreateAsset(ctx, domain.Asset{ChainID: "nope", Symbol: "X", Decimals: 6, QuotePrecision: 2, RateKey: "x"})
	require.ErrorIs(t, err, store.ErrForeignKey)
	_, err = s.CreateAsset(ctx, domain.Asset{ChainID: "polygon", Symbol: "Y", ContractAddress: "0xy", Decimals: 2, QuotePrecision: 6, RateKey: "y"})
	require.ErrorIs(t, err, store.ErrInvalid)
	_, err = s.CreateAsset(ctx, domain.Asset{ChainID: "polygon", Symbol: "", RateKey: "y"})
	require.ErrorIs(t, err, store.ErrInvalid)

	// Conflict inside a transaction keeps it usable.
	err = s.WithTx(ctx, func(tx *store.Store) error {
		_, err := tx.CreateAsset(ctx, domain.Asset{ChainID: "polygon", Symbol: "DAI", ContractAddress: "0xz",
			Decimals: 18, QuotePrecision: 2, RateKey: "dai"})
		require.ErrorIs(t, err, store.ErrConflict)
		_, err = tx.GetAsset(ctx, "dai-polygon")
		return err
	})
	require.NoError(t, err)

	// Update.
	up, err := s.UpdateAsset(ctx, a.ID, store.AssetUpdate{QuotePrecision: storetest.Ptr(4), SortOrder: storetest.Ptr(1),
		RateKey: storetest.Ptr(" makerdao-dai "), Name: storetest.Ptr("Dai Stablecoin")})
	require.NoError(t, err)
	assert.Equal(t, 4, up.QuotePrecision)
	assert.Equal(t, 1, up.SortOrder)
	assert.Equal(t, "makerdao-dai", up.RateKey)
	assert.Equal(t, "Dai Stablecoin", up.Name)
	assert.True(t, up.Enabled)
	_, err = s.UpdateAsset(ctx, a.ID, store.AssetUpdate{QuotePrecision: storetest.Ptr(19)})
	require.ErrorIs(t, err, store.ErrInvalid)
	_, err = s.UpdateAsset(ctx, a.ID, store.AssetUpdate{RateKey: storetest.Ptr("")})
	require.ErrorIs(t, err, store.ErrInvalid)
	_, err = s.UpdateAsset(ctx, "nope", store.AssetUpdate{})
	require.ErrorIs(t, err, store.ErrNotFound)
}
