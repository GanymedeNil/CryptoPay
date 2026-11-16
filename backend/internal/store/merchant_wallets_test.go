package store_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cryptopay/internal/domain"
	"cryptopay/internal/store"
	"cryptopay/internal/store/storetest"
)

func TestMerchantWalletBindings(t *testing.T) {
	t.Parallel()
	s := storetest.NewSeeded(t)
	m := storetest.Merchant(t, s, func(m *domain.Merchant) { m.Name = "B shop" })
	other := storetest.Merchant(t, s, func(m *domain.Merchant) { m.Name = "A shop" })
	evm := storetest.Wallet(t, s, domain.FamilyEVM, domain.WalletXpub)
	evm2 := storetest.Wallet(t, s, domain.FamilyEVM, domain.WalletXpub)
	tron := storetest.Wallet(t, s, domain.FamilyTron, domain.WalletStatic)

	list, err := s.ListMerchantWallets(ctx, m.ID)
	require.NoError(t, err)
	assert.NotNil(t, list)
	assert.Empty(t, list)
	_, err = s.GetMerchantWallet(ctx, m.ID, "ethereum")
	require.ErrorIs(t, err, store.ErrNotFound)

	b, err := s.SetMerchantWallet(ctx, m.ID, "sepolia", evm.ID)
	require.NoError(t, err)
	assert.Equal(t, store.MerchantWallet{MerchantID: m.ID, ChainID: "sepolia", WalletID: evm.ID, WalletName: evm.Name,
		WalletKind: domain.WalletXpub, CreatedAt: b.CreatedAt, UpdatedAt: b.UpdatedAt}, b)
	assert.False(t, b.CreatedAt.IsZero())
	_, err = s.SetMerchantWallet(ctx, m.ID, "tron", tron.ID)
	require.NoError(t, err)
	_, err = s.SetMerchantWallet(ctx, m.ID, "ethereum", evm.ID)
	require.NoError(t, err)
	// Upsert: a second set replaces the wallet of that chain.
	b, err = s.SetMerchantWallet(ctx, m.ID, "ethereum", evm2.ID)
	require.NoError(t, err)
	assert.Equal(t, evm2.ID, b.WalletID)
	got, err := s.GetMerchantWallet(ctx, m.ID, "ethereum")
	require.NoError(t, err)
	assert.Equal(t, b, got)
	_, err = s.SetMerchantWallet(ctx, other.ID, "ethereum", evm.ID)
	require.NoError(t, err, "several merchants may share a wallet")

	list, err = s.ListMerchantWallets(ctx, m.ID)
	require.NoError(t, err)
	var chains []string
	for _, x := range list {
		chains = append(chains, x.ChainID)
	}
	assert.Equal(t, []string{"ethereum", "tron", "sepolia"}, chains, "mainnets first, then by name")

	// Validation: family, unknown rows.
	_, err = s.SetMerchantWallet(ctx, m.ID, "base", tron.ID)
	require.ErrorIs(t, err, store.ErrInvalid, "a Tron wallet on an EVM chain")
	_, err = s.SetMerchantWallet(ctx, m.ID, "base", uuid.New())
	require.ErrorIs(t, err, store.ErrForeignKey)
	_, err = s.SetMerchantWallet(ctx, uuid.New(), "base", evm.ID)
	require.ErrorIs(t, err, store.ErrForeignKey)
	_, err = s.SetMerchantWallet(ctx, m.ID, "nope", evm.ID)
	require.ErrorIs(t, err, store.ErrForeignKey)

	// The chain counts its bindings; the wallet lists them.
	c, err := s.GetChain(ctx, "ethereum")
	require.NoError(t, err)
	assert.Equal(t, 2, c.MerchantWallets)
	all, err := s.ListChains(ctx)
	require.NoError(t, err)
	for _, x := range all {
		switch x.ID {
		case "ethereum":
			assert.Equal(t, 2, x.MerchantWallets)
		case "tron", "sepolia":
			assert.Equal(t, 1, x.MerchantWallets, x.ID)
		default:
			assert.Zero(t, x.MerchantWallets, x.ID)
		}
	}
	upd, err := s.UpdateChain(ctx, "ethereum", store.ChainUpdate{Confirmations: storetest.Ptr(15)})
	require.NoError(t, err)
	assert.Equal(t, 2, upd.MerchantWallets, "also after an update")
	v, err := s.GetWalletView(ctx, evm.ID)
	require.NoError(t, err)
	assert.Equal(t, []store.WalletBinding{
		{MerchantID: other.ID, MerchantName: "A shop", ChainID: "ethereum"},
		{MerchantID: m.ID, MerchantName: "B shop", ChainID: "sepolia"},
	}, v.MerchantBindings)
	assert.Equal(t, []string{}, v.ChainIDs, "bindings are not default assignments")
	ws, err := s.ListWallets(ctx)
	require.NoError(t, err)
	for _, w := range ws {
		assert.NotNil(t, w.MerchantBindings)
		if w.ID == tron.ID {
			assert.Equal(t, []store.WalletBinding{{MerchantID: m.ID, MerchantName: "B shop", ChainID: "tron"}}, w.MerchantBindings)
		}
	}

	// Delete is idempotent and reports whether something was removed.
	removed, err := s.DeleteMerchantWallet(ctx, m.ID, "tron")
	require.NoError(t, err)
	assert.True(t, removed)
	removed, err = s.DeleteMerchantWallet(ctx, m.ID, "tron")
	require.NoError(t, err)
	assert.False(t, removed)
	_, err = s.GetMerchantWallet(ctx, m.ID, "tron")
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestResolveWallet(t *testing.T) {
	t.Parallel()
	s := storetest.NewSeeded(t)
	m := storetest.Merchant(t, s)
	other := storetest.Merchant(t, s)
	def := storetest.Wallet(t, s, domain.FamilyEVM, domain.WalletXpub)
	own := storetest.Wallet(t, s, domain.FamilyEVM, domain.WalletXpub)
	storetest.AssignWallet(t, s, "ethereum", def.ID)

	// None: no default wallet, no binding.
	_, err := s.ResolveWallet(ctx, m.ID, "base")
	require.ErrorIs(t, err, store.ErrNotFound)
	_, err = s.ResolveWallet(ctx, m.ID, "nope")
	require.ErrorIs(t, err, store.ErrNotFound)

	// Default.
	r, err := s.ResolveWallet(ctx, m.ID, "ethereum")
	require.NoError(t, err)
	assert.Equal(t, store.ResolvedWallet{Wallet: def, Source: store.WalletSourceDefault}, r)

	// The merchant's binding wins over the default — for that merchant only.
	storetest.BindWallet(t, s, m.ID, "ethereum", own.ID)
	r, err = s.ResolveWallet(ctx, m.ID, "ethereum")
	require.NoError(t, err)
	assert.Equal(t, own.ID, r.ID)
	assert.Equal(t, own.Xpub, r.Xpub)
	assert.Equal(t, store.WalletSourceMerchant, r.Source)
	r, err = s.ResolveWallet(ctx, other.ID, "ethereum")
	require.NoError(t, err)
	assert.Equal(t, def.ID, r.ID)
	r, err = s.ResolveWallet(ctx, uuid.Nil, "ethereum")
	require.NoError(t, err)
	assert.Equal(t, def.ID, r.ID, "no merchant: the default")

	// A binding on a chain without a default wallet.
	storetest.BindWallet(t, s, m.ID, "base", own.ID)
	r, err = s.ResolveWallet(ctx, m.ID, "base")
	require.NoError(t, err)
	assert.Equal(t, own.ID, r.ID)
	_, err = s.ResolveWallet(ctx, other.ID, "base")
	require.ErrorIs(t, err, store.ErrNotFound)

	// Removing the binding falls back to the default.
	_, err = s.DeleteMerchantWallet(ctx, m.ID, "ethereum")
	require.NoError(t, err)
	r, err = s.ResolveWallet(ctx, m.ID, "ethereum")
	require.NoError(t, err)
	assert.Equal(t, def.ID, r.ID)
	assert.Equal(t, store.WalletSourceDefault, r.Source)
}

func TestMerchantPayableAssets(t *testing.T) {
	t.Parallel()
	s := storetest.NewSeeded(t)
	withOwn := storetest.Merchant(t, s)
	plain := storetest.Merchant(t, s)
	tw := storetest.Wallet(t, s, domain.FamilyTron, domain.WalletXpub)
	storetest.AssignWallet(t, s, "tron", tw.ID)
	sw := storetest.Wallet(t, s, domain.FamilySolana, domain.WalletStatic)
	storetest.BindWallet(t, s, withOwn.ID, "solana", sw.ID) // solana has no default wallet

	p, err := s.ListPayableAssets(ctx, withOwn.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"usdt-tron", "usdt-solana", "usdc-solana", "trx-tron", "sol-solana"}, assetIDs(p))
	p, err = s.ListPayableAssets(ctx, plain.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"usdt-tron", "trx-tron"}, assetIDs(p), "only chains with a default wallet")

	_, err = s.GetPayableAsset(ctx, withOwn.ID, "usdc-solana")
	require.NoError(t, err)
	_, err = s.GetPayableAsset(ctx, plain.ID, "usdc-solana")
	require.ErrorIs(t, err, store.ErrNotFound)

	// Disabled asset / chain: not payable even with the merchant's own wallet.
	_, err = s.UpdateAsset(ctx, "sol-solana", store.AssetUpdate{Enabled: storetest.Ptr(false)})
	require.NoError(t, err)
	p, err = s.ListPayableAssets(ctx, withOwn.ID)
	require.NoError(t, err)
	assert.NotContains(t, assetIDs(p), "sol-solana")
	_, err = s.UpdateChain(ctx, "solana", store.ChainUpdate{Enabled: storetest.Ptr(false)})
	require.NoError(t, err)
	p, err = s.ListPayableAssets(ctx, withOwn.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"usdt-tron", "trx-tron"}, assetIDs(p))
	_, err = s.GetPayableAsset(ctx, withOwn.ID, "usdc-solana")
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestWalletKeyMaterialIsUnique(t *testing.T) {
	t.Parallel()
	s := storetest.NewSeeded(t)
	x := storetest.Wallet(t, s, domain.FamilyEVM, domain.WalletXpub)
	for _, fam := range []domain.Family{domain.FamilyEVM, domain.FamilyTron} {
		_, err := s.CreateWallet(ctx, domain.Wallet{Name: "dup", Family: fam, Kind: domain.WalletXpub, Xpub: x.Xpub})
		require.ErrorIs(t, err, store.ErrConflict, fam)
		assert.True(t, store.IsConstraint(err, store.ConstraintWalletXpub), "%v", err)
	}

	st := storetest.Wallet(t, s, domain.FamilyTron, domain.WalletStatic)
	_, err := s.CreateWallet(ctx, domain.Wallet{Name: "dup", Family: domain.FamilyTron, Kind: domain.WalletStatic, Address: st.Address})
	require.ErrorIs(t, err, store.ErrConflict)
	assert.True(t, store.IsConstraint(err, store.ConstraintWalletStaticAddress), "%v", err)
	// The index is per family, and only covers static wallets.
	_, err = s.CreateWallet(ctx, domain.Wallet{Name: "other family", Family: domain.FamilyEVM, Kind: domain.WalletStatic, Address: st.Address})
	require.NoError(t, err)
	_, err = s.CreateWallet(ctx, domain.Wallet{Name: "second tron", Family: domain.FamilyTron, Kind: domain.WalletStatic,
		Address: storetest.StaticAddress(domain.FamilyTron, 1)})
	require.NoError(t, err)
}

func TestWalletInUseByMerchantBinding(t *testing.T) {
	t.Parallel()
	s := storetest.NewSeeded(t)
	m := storetest.Merchant(t, s)
	w := storetest.Wallet(t, s, domain.FamilyTron, domain.WalletXpub)
	storetest.BindWallet(t, s, m.ID, "tron", w.ID)

	used, err := s.WalletInUse(ctx, w.ID)
	require.NoError(t, err)
	assert.True(t, used)
	require.ErrorIs(t, s.DeleteWallet(ctx, w.ID), store.ErrInUse)
	_, err = s.GetWallet(ctx, w.ID)
	require.NoError(t, err, "still there")

	_, err = s.DeleteMerchantWallet(ctx, m.ID, "tron")
	require.NoError(t, err)
	used, err = s.WalletInUse(ctx, w.ID)
	require.NoError(t, err)
	assert.False(t, used)
	require.NoError(t, s.DeleteWallet(ctx, w.ID))
}

func TestDeleteMerchantCascadesWalletBindings(t *testing.T) {
	t.Parallel()
	s := storetest.NewSeeded(t)
	m := storetest.Merchant(t, s)
	w := storetest.Wallet(t, s, domain.FamilyEVM, domain.WalletXpub)
	storetest.BindWallet(t, s, m.ID, "ethereum", w.ID)
	storetest.BindWallet(t, s, m.ID, "base", w.ID)

	require.NoError(t, s.DeleteMerchant(ctx, m.ID))
	list, err := s.ListMerchantWallets(ctx, m.ID)
	require.NoError(t, err)
	assert.Empty(t, list)
	c, err := s.GetChain(ctx, "ethereum")
	require.NoError(t, err)
	assert.Zero(t, c.MerchantWallets)
	v, err := s.GetWalletView(ctx, w.ID)
	require.NoError(t, err)
	assert.Empty(t, v.MerchantBindings)
	require.NoError(t, s.DeleteWallet(ctx, w.ID), "no longer in use")
}
