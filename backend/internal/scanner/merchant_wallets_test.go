package scanner_test

import (
	"math/big"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cryptopay/internal/domain"
	"cryptopay/internal/hdwallet/hdwallettest"
	"cryptopay/internal/invoice"
	"cryptopay/internal/invoice/invoicetest"
	"cryptopay/internal/scanner"
	"cryptopay/internal/scanner/scannertest"
	"cryptopay/internal/store"
	"cryptopay/internal/store/storetest"
)

// merchantWithWallet creates a merchant whose own wallet on chainID is the
// EVM account wallet at path of the test mnemonic.
func (h *harness) merchantWithWallet(chainID, path string) (domain.Merchant, domain.Wallet) {
	h.t.Helper()
	m := storetest.Merchant(h.t, h.Store)
	w := storetest.Wallet(h.t, h.Store, domain.FamilyEVM, domain.WalletXpub, func(w *domain.Wallet) {
		w.Xpub = hdwallettest.AccountXpub(invoicetest.Mnemonic, path)
	})
	storetest.BindWallet(h.t, h.Store, m.ID, chainID, w.ID)
	return m, w
}

func (h *harness) invoiceFor(m domain.Merchant, assetID string) (store.InvoiceView, domain.Quote) {
	h.t.Helper()
	v, _, err := h.Invoices.Create(h.Ctx, m, invoice.CreateParams{Amount: "10", Currency: "USD", AssetID: assetID})
	require.NoError(h.t, err)
	require.NotNil(h.t, v.Quote)
	h.Step()
	return v, *v.Quote
}

// TestChainWithOnlyMerchantWalletsIsScanned: a chain without a default
// wallet is scanned as soon as one merchant has its own wallet on it.
func TestChainWithOnlyMerchantWalletsIsScanned(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	base := scannertest.NewFakeChain(500)
	h.Provider.Set("base", base)
	res, err := h.M.TickOnce(h.Ctx, "base")
	require.NoError(t, err)
	assert.Equal(t, scanner.SkipInactive, res.Skipped, "enabled, but no wallet of any kind")

	shop, _ := h.merchantWithWallet("base", "m/44'/60'/1'")
	require.Nil(t, h.chain("base").WalletID, "still no default wallet")
	h.tick("base") // first scan starts at the head
	v, q := h.invoiceFor(shop, "usdc-base")
	base.Pay(501, "0xbase-pay", q.Address, h.contract("usdc-base"), q.AmountDue)
	base.SetHead(501)
	res = h.tick("base")
	assert.Equal(t, 1, res.NewTransfers)
	assert.Equal(t, domain.InvoiceConfirming, h.status(v.ID))

	// The last binding gone (and no default wallet): the chain is idle again.
	_, err = h.Store.DeleteMerchantWallet(h.Ctx, shop.ID, "base")
	require.NoError(t, err)
	res, err = h.M.TickOnce(h.Ctx, "base")
	require.NoError(t, err)
	assert.Equal(t, scanner.SkipInactive, res.Skipped)
}

// TestMerchantWalletsCreditedIndependently: two merchants with their own
// xpub wallets on one chain, same price: each payment credits exactly the
// invoice of the merchant whose address received it.
func TestMerchantWalletsCreditedIndependently(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	f := h.Fakes["ethereum"]
	usdt := h.contract("usdt-ethereum")
	shopA, wa := h.merchantWithWallet("ethereum", "m/44'/60'/1'")
	shopB, wb := h.merchantWithWallet("ethereum", "m/44'/60'/2'")
	va, qa := h.invoiceFor(shopA, "usdt-ethereum")
	vb, qb := h.invoiceFor(shopB, "usdt-ethereum")
	require.Equal(t, wa.ID, qa.WalletID)
	require.Equal(t, wb.ID, qb.WalletID)
	require.NotEqual(t, qa.Address, qb.Address)
	require.Equal(t, qa.AmountDue.String(), qb.AmountDue.String(), "same price")
	h.tick("ethereum")

	// Only shop A's customer pays.
	f.Pay(1001, "0xpay-a", qa.Address, usdt, qa.AmountDue)
	f.SetHead(1001)
	assert.Equal(t, 1, h.tick("ethereum").NewTransfers)
	assert.Equal(t, domain.InvoiceConfirming, h.status(va.ID))
	assert.Equal(t, domain.InvoicePending, h.status(vb.ID))
	assert.Equal(t, va.ID, *h.transfer("ethereum", "0xpay-a", 0).InvoiceID)

	// Then shop B's, a block later; both confirm.
	f.Pay(1002, "0xpay-b", qb.Address, usdt, new(big.Int).Set(qb.AmountDue))
	f.SetHead(1002)
	assert.Equal(t, 1, h.tick("ethereum").NewTransfers)
	assert.Equal(t, vb.ID, *h.transfer("ethereum", "0xpay-b", 0).InvoiceID)
	f.SetHead(1004)
	assert.Equal(t, 2, h.tick("ethereum").Confirmed)
	assert.Equal(t, domain.InvoicePaid, h.status(va.ID))
	assert.Equal(t, domain.InvoicePaid, h.status(vb.ID))
	assert.Equal(t, "10000000", h.Quote(qa.ID).AmountConfirmed.String())
	assert.Equal(t, "10000000", h.Quote(qb.ID).AmountConfirmed.String())

	// Webhooks go to the merchant the invoice belongs to.
	for _, tc := range []struct {
		inv store.InvoiceView
		m   domain.Merchant
	}{{va, shopA}, {vb, shopB}} {
		evs := h.Events(tc.inv.ID)
		require.NotEmpty(t, evs)
		for _, ev := range evs {
			assert.Equal(t, tc.m.ID, ev.MerchantID)
		}
		assert.Equal(t, []string{domain.EventConfirming, domain.EventPaid}, h.EventTypes(tc.inv.ID))
	}
}
