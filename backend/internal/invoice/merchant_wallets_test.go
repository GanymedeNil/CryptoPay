package invoice_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cryptopay/internal/domain"
	"cryptopay/internal/hdwallet"
	"cryptopay/internal/hdwallet/hdwallettest"
	"cryptopay/internal/invoice"
	"cryptopay/internal/invoice/invoicetest"
	"cryptopay/internal/store"
	"cryptopay/internal/store/storetest"
)

// evmAccountWallet inserts an EVM xpub wallet for an account path of the
// test mnemonic (the environment's default EVM wallet is m/44'/60'/0').
func evmAccountWallet(e *invoicetest.Env, path string) domain.Wallet {
	return storetest.Wallet(e.T, e.Store, domain.FamilyEVM, domain.WalletXpub, func(w *domain.Wallet) {
		w.Name = "EVM " + path
		w.Xpub = hdwallettest.AccountXpub(invoicetest.Mnemonic, path)
	})
}

// createFor creates a 10 USD invoice for merchant m with assetID pre-selected.
func createFor(t *testing.T, e *invoicetest.Env, m domain.Merchant, assetID string) store.InvoiceView {
	t.Helper()
	v, created, err := e.Invoices.Create(e.Ctx, m, invoice.CreateParams{Amount: "10", Currency: "USD", AssetID: assetID})
	require.NoError(t, err)
	require.True(t, created)
	require.NotNil(t, v.Quote)
	e.Step()
	return v
}

func TestMerchantXpubWalletsOnOneChain(t *testing.T) {
	t.Parallel()
	e := invoicetest.New(t)
	shopA := storetest.Merchant(t, e.Store)
	shopB := storetest.Merchant(t, e.Store)
	wa := evmAccountWallet(e, "m/44'/60'/1'")
	wb := evmAccountWallet(e, "m/44'/60'/2'")
	storetest.BindWallet(t, e.Store, shopA.ID, "ethereum", wa.ID)
	storetest.BindWallet(t, e.Store, shopB.ID, "ethereum", wb.ID)
	eth, err := e.Store.GetChain(e.Ctx, "ethereum")
	require.NoError(t, err)

	// Same price, same chain and asset: every merchant gets the first
	// address of its own wallet; the merchant without a binding gets the
	// chain's default wallet.
	seen := map[string]bool{}
	for _, tc := range []struct {
		m domain.Merchant
		w domain.Wallet
	}{{shopA, wa}, {shopB, wb}, {e.Merchant, e.EVMWallet}} {
		q := createFor(t, e, tc.m, "usdt-ethereum").Quote
		assert.Equal(t, tc.w.ID, q.WalletID, tc.w.Name)
		require.NotNil(t, q.AddressIndex)
		assert.Zero(t, *q.AddressIndex, tc.w.Name)
		want, err := hdwallet.Derive(eth, tc.w.Xpub, 0)
		require.NoError(t, err)
		assert.Equal(t, want, q.Address, tc.w.Name)
		assert.Equal(t, "10000000", q.AmountDue.String())
		assert.False(t, seen[q.Address], "addresses are distinct")
		seen[q.Address] = true
		w, err := e.Store.GetWallet(e.Ctx, tc.w.ID)
		require.NoError(t, err)
		assert.EqualValues(t, 1, w.NextIndex, "%s handed out one address", tc.w.Name)
	}

	// A chain without a binding uses the default wallet, for shop A too.
	v := createFor(t, e, shopA, "usdt-polygon")
	assert.Equal(t, e.EVMWallet.ID, v.Quote.WalletID)
}

func hdwalletAddr(t *testing.T, c domain.Chain, xpub string) string {
	t.Helper()
	a, err := hdwallet.Derive(c, xpub, 0)
	require.NoError(t, err)
	return a
}

func TestMerchantWithoutAnyWalletOnAChain(t *testing.T) {
	t.Parallel()
	e := invoicetest.New(t)
	shop := storetest.Merchant(t, e.Store)
	w := evmAccountWallet(e, "m/44'/60'/1'")
	storetest.BindWallet(t, e.Store, shop.ID, "base", w.ID) // base has no default wallet

	// The bound merchant can be paid on base.
	v := createFor(t, e, shop, "usdc-base")
	assert.Equal(t, w.ID, v.Quote.WalletID)
	est, err := e.Invoices.Estimates(e.Ctx, v.Invoice)
	require.NoError(t, err)
	assert.Contains(t, estimateIDs(est), "usdc-base")

	// The other merchant cannot: no own wallet, no default wallet.
	_, _, err = e.Invoices.Create(e.Ctx, e.Merchant, invoice.CreateParams{Amount: "10", Currency: "USD", AssetID: "usdc-base"})
	require.ErrorIs(t, err, invoice.ErrAssetUnavailable)
	d := e.Create("10", "USD")
	_, err = e.Invoices.SelectAsset(e.Ctx, d.ID, "usdc-base")
	require.ErrorIs(t, err, invoice.ErrAssetUnavailable)
	est, err = e.Invoices.Estimates(e.Ctx, d.Invoice)
	require.NoError(t, err)
	assert.NotContains(t, estimateIDs(est), "usdc-base")
	assert.NotContains(t, estimateIDs(est), "eth-base")
	assert.Contains(t, estimateIDs(est), "usdt-ethereum")
	c, err := e.Invoices.Checkout(e.Ctx, d.ID)
	require.NoError(t, err)
	assert.NotContains(t, estimateIDs(c.Estimates), "usdc-base", "checkout asset list is the merchant's")
	c, err = e.Invoices.Checkout(e.Ctx, v.ID)
	require.NoError(t, err)
	assert.Contains(t, estimateIDs(c.Estimates), "usdc-base")
}

func estimateIDs(est []invoice.Estimate) []string {
	out := make([]string, 0, len(est))
	for _, x := range est {
		out = append(out, x.Asset.ID)
	}
	return out
}

func TestMerchantStaticWallets(t *testing.T) {
	t.Parallel()
	e := invoicetest.New(t)
	own := storetest.Wallet(t, e.Store, domain.FamilyTron, domain.WalletStatic, func(w *domain.Wallet) {
		w.Address = storetest.StaticAddress(domain.FamilyTron, 1)
	})
	shopA := storetest.Merchant(t, e.Store)
	shopC := storetest.Merchant(t, e.Store)
	storetest.BindWallet(t, e.Store, shopA.ID, "tron", own.ID)
	storetest.BindWallet(t, e.Store, shopC.ID, "tron", own.ID) // shares shop A's address

	// Different static addresses: the plain amount for each merchant.
	qd := createFor(t, e, e.Merchant, "usdt-tron").Quote
	qa := createFor(t, e, shopA, "usdt-tron").Quote
	assert.Equal(t, e.TronWallet.Address, qd.Address)
	assert.Equal(t, own.Address, qa.Address)
	assert.Equal(t, "10000000", qd.AmountDue.String())
	assert.Equal(t, "10000000", qa.AmountDue.String(), "another address: no bump needed")

	// The same static address: amounts are still told apart.
	qc := createFor(t, e, shopC, "usdt-tron").Quote
	assert.Equal(t, own.Address, qc.Address)
	assert.Equal(t, "10000100", qc.AmountDue.String())
	qd2 := createFor(t, e, e.Merchant, "usdt-tron").Quote
	assert.Equal(t, e.TronWallet.Address, qd2.Address)
	assert.Equal(t, "10000100", qd2.AmountDue.String())
}

func TestChangingMerchantWalletKeepsExistingQuotes(t *testing.T) {
	t.Parallel()
	e := invoicetest.New(t)
	shop := storetest.Merchant(t, e.Store)
	w1 := evmAccountWallet(e, "m/44'/60'/1'")
	w2 := evmAccountWallet(e, "m/44'/60'/2'")
	eth, err := e.Store.GetChain(e.Ctx, "ethereum")
	require.NoError(t, err)
	storetest.BindWallet(t, e.Store, shop.ID, "ethereum", w1.ID)
	v := createFor(t, e, shop, "usdt-ethereum")
	q := *v.Quote
	require.Equal(t, w1.ID, q.WalletID)

	// The admin switches the merchant to another wallet.
	storetest.BindWallet(t, e.Store, shop.ID, "ethereum", w2.ID)
	assert.Equal(t, q, e.Quote(q.ID), "the existing quote is untouched")
	same := e.Select(v.ID, "usdt-ethereum")
	assert.Equal(t, q.ID, same.ID, "selecting the same asset again keeps the quote")
	assert.Equal(t, q.Address, same.Address)

	// A new quote of the invoice (another asset) uses the new wallet ...
	qe := e.Select(v.ID, "eth-ethereum")
	assert.Equal(t, w2.ID, qe.WalletID)
	assert.Equal(t, hdwalletAddr(t, eth, w2.Xpub), qe.Address)
	// ... and switching back re-activates the original quote and address.
	back := e.Select(v.ID, "usdt-ethereum")
	assert.Equal(t, q.ID, back.ID)
	assert.Equal(t, q.Address, back.Address)
	assert.Equal(t, w1.ID, back.WalletID)

	// New invoices get the new wallet.
	v2 := createFor(t, e, shop, "usdt-ethereum")
	assert.Equal(t, w2.ID, v2.Quote.WalletID)
	assert.EqualValues(t, 1, *v2.Quote.AddressIndex)

	// Removing the binding: existing quotes stay, new ones use the default.
	_, err = e.Store.DeleteMerchantWallet(e.Ctx, shop.ID, "ethereum")
	require.NoError(t, err)
	assert.Equal(t, w2.ID, e.Quote(v2.Quote.ID).WalletID)
	assert.Equal(t, v2.Quote.Address, e.Quote(v2.Quote.ID).Address)
	v3 := createFor(t, e, shop, "usdt-ethereum")
	assert.Equal(t, e.EVMWallet.ID, v3.Quote.WalletID)
}
