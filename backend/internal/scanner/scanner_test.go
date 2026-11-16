package scanner_test

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cryptopay/internal/chain"
	"cryptopay/internal/chain/registry"
	"cryptopay/internal/domain"
	"cryptopay/internal/invoice"
	"cryptopay/internal/invoice/invoicetest"
	"cryptopay/internal/rates"
	"cryptopay/internal/scanner"
	"cryptopay/internal/scanner/scannertest"
	"cryptopay/internal/store"
	"cryptopay/internal/store/storetest"
)

func ptr[T any](v T) *T { return &v }

// The production client provider is the adapter registry.
var _ scanner.ClientProvider = (*registry.Registry)(nil)

// testConfig keeps every interval short so loop tests finish quickly.
var testConfig = scanner.Config{
	ReloadInterval:    50 * time.Millisecond,
	ExpireInterval:    50 * time.Millisecond,
	MaxBatchesPerTick: 5,
	BackoffBase:       10 * time.Millisecond,
	BackoffMax:        50 * time.Millisecond,
	InvalidateAfter:   3,
	TickTimeout:       30 * time.Second,
}

const (
	startHead     = 1000
	confirmations = 3
	scanBatch     = 10
)

// harness is an invoicetest.Env (wallets on ethereum, polygon, bitcoin,
// tron, solana) with a fake chain per chain and a Manager.
type harness struct {
	*invoicetest.Env
	t        *testing.T
	Fakes    map[string]*scannertest.FakeChain
	Provider *scannertest.Provider
	M        *scanner.Manager
}

var scannedChains = []string{"ethereum", "polygon", "bitcoin", "tron", "solana"}

func newHarness(t *testing.T) *harness {
	t.Helper()
	e := invoicetest.New(t)
	h := &harness{Env: e, t: t, Fakes: map[string]*scannertest.FakeChain{}}
	clients := map[string]chain.Client{}
	for _, id := range scannedChains {
		f := scannertest.NewFakeChain(startHead)
		h.Fakes[id] = f
		clients[id] = f
		_, err := e.Store.UpdateChain(e.Ctx, id, store.ChainUpdate{Confirmations: ptr(confirmations),
			ScanBatch: ptr(scanBatch), PollInterval: ptr(500 * time.Millisecond)})
		require.NoError(t, err)
	}
	h.Provider = scannertest.NewProvider(clients)
	h.M = scanner.New(e.Store, h.Provider, e.Invoices, e.Log, e.Clock.Now, testConfig)
	return h
}

func (h *harness) tick(chainID string) scanner.TickResult {
	h.t.Helper()
	res, err := h.M.TickOnce(h.Ctx, chainID)
	require.NoError(h.t, err)
	require.Empty(h.t, res.Skipped)
	return res
}

func (h *harness) chain(id string) domain.Chain {
	h.t.Helper()
	c, err := h.Store.GetChain(h.Ctx, id)
	require.NoError(h.t, err)
	return c
}

func (h *harness) setCursor(id string, cursor uint64) {
	h.t.Helper()
	c := h.chain(id)
	require.NoError(h.t, h.Store.UpdateScanState(h.Ctx, id, store.ScanState{Cursor: cursor, Head: c.LastHead}))
}

func (h *harness) contract(assetID string) string {
	h.t.Helper()
	a, err := h.Store.GetAsset(h.Ctx, assetID)
	require.NoError(h.t, err)
	return a.ContractAddress
}

func (h *harness) transfer(chainID, txHash string, idx int) domain.Transfer {
	h.t.Helper()
	tr, err := h.Store.GetTransferByTx(h.Ctx, chainID, txHash, idx)
	require.NoError(h.t, err)
	return tr
}

func (h *harness) allTransfers(chainID string) []domain.Transfer {
	h.t.Helper()
	p, err := h.Store.ListTransfers(h.Ctx, store.TransferFilter{ChainID: chainID, PageSize: 100})
	require.NoError(h.t, err)
	return p.Items
}

func (h *harness) status(id uuid.UUID) domain.InvoiceStatus { return h.Invoice(id).Status }

// eventually polls cond until it holds or the deadline passes.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ranges renders scan requests as "from-to".
func ranges(reqs []chain.ScanRequest) []string {
	out := []string{}
	for _, r := range reqs {
		out = append(out, fmt.Sprintf("%d-%d", r.FromBlock, r.ToBlock))
	}
	return out
}

// ---- cursor handling ----

func TestFirstScanStartsAtHead(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	f := h.Fakes["tron"]
	_, q := h.Quoted("10", "USD", "usdt-tron")
	f.Pay(990, "old-tx", q.Address, h.contract("usdt-tron"), q.AmountDue) // before the first scan

	res := h.tick("tron")
	assert.Equal(t, uint64(startHead), res.Cursor)
	assert.Equal(t, uint64(startHead), res.Head)
	assert.Zero(t, res.Batches)
	assert.False(t, res.Behind)
	assert.Empty(t, f.ScanCalls(), "no backfill")
	c := h.chain("tron")
	assert.Equal(t, uint64(startHead), c.ScanCursor)
	assert.Equal(t, uint64(startHead), c.LastHead)
	require.NotNil(t, c.LastScanAt)
	assert.True(t, c.LastScanAt.Equal(h.Clock.Now()))
	assert.Empty(t, c.LastError)
	assert.Empty(t, h.allTransfers("tron"))

	// From now on new blocks are scanned.
	f.Pay(1003, "new-tx", q.Address, h.contract("usdt-tron"), q.AmountDue)
	f.SetHead(1004)
	res = h.tick("tron")
	assert.Equal(t, []string{"1001-1004"}, ranges(f.ScanCalls()))
	assert.Equal(t, 1, res.NewTransfers)
	assert.Equal(t, uint64(1004), h.chain("tron").ScanCursor)
}

func TestBatchCatchUp(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	f := h.Fakes["tron"]
	v, q := h.Quoted("10", "USD", "usdt-tron")
	h.Quoted("10", "USD", "usdt-tron") // keeps the address watched after v is paid
	h.setCursor("tron", 100)
	f.SetHead(205)
	f.Pay(175, "tx-175", q.Address, h.contract("usdt-tron"), q.AmountDue)

	res := h.tick("tron")
	assert.Equal(t, 5, res.Batches, "bounded batches per tick")
	assert.Equal(t, uint64(150), res.Cursor)
	assert.True(t, res.Behind)
	assert.Equal(t, uint64(150), h.chain("tron").ScanCursor, "persisted after every batch")
	assert.Equal(t, domain.InvoicePending, h.status(v.ID))

	res = h.tick("tron")
	assert.Equal(t, uint64(200), res.Cursor)
	assert.Equal(t, 1, res.NewTransfers)
	// Found 31 blocks deep: verified and confirmed in the same tick.
	assert.Equal(t, 1, res.Confirmed)
	assert.Equal(t, domain.InvoicePaid, h.status(v.ID))
	assert.Equal(t, []string{domain.EventConfirming, domain.EventPaid}, h.EventTypes(v.ID))
	res = h.tick("tron")
	assert.Equal(t, uint64(205), res.Cursor)
	assert.False(t, res.Behind)

	assert.Equal(t, []string{"101-110", "111-120", "121-130", "131-140", "141-150", "151-160", "161-170",
		"171-180", "181-190", "191-200", "201-205"}, ranges(f.ScanCalls()), "contiguous, never skipping a block")
}

func TestNoWatchersFastForward(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	f := h.Fakes["ethereum"]
	h.setCursor("ethereum", 100)
	res := h.tick("ethereum")
	assert.True(t, res.FastForwarded)
	assert.Equal(t, uint64(startHead), res.Cursor)
	assert.Zero(t, res.Batches)
	assert.Empty(t, f.ScanCalls())
	assert.Equal(t, uint64(startHead), h.chain("ethereum").ScanCursor)
}

func TestScanRequestContents(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	f := h.Fakes["ethereum"]
	usdt, usdc := h.contract("usdt-ethereum"), h.contract("usdc-ethereum")

	// A quote whose watch window has passed is not watched.
	h.Settings(func(s *domain.Settings) { s.GracePeriodSeconds = 0 })
	old := h.Create("10", "USD", func(p *invoice.CreateParams) { p.ExpiresIn = ptr(60) })
	oldQ := h.Select(old.ID, "usdt-ethereum")
	h.Settings(func(s *domain.Settings) { s.GracePeriodSeconds = 86400 })
	h.Clock.Advance(2 * time.Minute)

	// Invoice A: USDT then ETH (two live quotes, one address); B: USDC.
	a, qa := h.Quoted("10", "USD", "usdt-ethereum")
	qa2 := h.Select(a.ID, "eth-ethereum")
	_, qb := h.Quoted("10", "USD", "usdc-ethereum")
	h.Quoted("10", "USD", "usdt-polygon") // another chain
	require.Equal(t, qa.Address, qa2.Address)
	require.NotEqual(t, oldQ.Address, qa.Address)

	h.setCursor("ethereum", 990)
	h.tick("ethereum")
	calls := f.ScanCalls()
	require.Len(t, calls, 1)
	req := calls[0]
	assert.Equal(t, uint64(991), req.FromBlock)
	assert.Equal(t, uint64(1000), req.ToBlock)
	assert.Equal(t, slices.Sorted(slices.Values([]string{qa.Address, qb.Address})), req.Addresses, "de-duplicated, sorted")
	assert.Equal(t, slices.Sorted(slices.Values([]string{usdt, usdc})), req.Tokens)
	assert.True(t, req.Native, "ETH is watched")

	// Without a native quote, Native is off.
	f.ResetCalls()
	_, err := h.Store.SetQuoteStatus(h.Ctx, qa2.ID, domain.QuoteClosed)
	require.NoError(t, err)
	f.Advance(1)
	h.tick("ethereum")
	require.Len(t, f.ScanCalls(), 1)
	assert.False(t, f.ScanCalls()[0].Native)
}

// ---- end-to-end crediting ----

func TestXpubPaymentLifecycle(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	f := h.Fakes["ethereum"]
	v, q := h.Quoted("10", "USD", "usdt-ethereum")
	h.tick("ethereum") // cursor = head

	f.Pay(1001, "0xpay", q.Address, h.contract("usdt-ethereum"), big.NewInt(10_000_000))
	f.SetHead(1001)
	res := h.tick("ethereum")
	assert.Equal(t, 1, res.NewTransfers)
	tr := h.transfer("ethereum", "0xpay", 0)
	assert.Equal(t, domain.TransferPending, tr.Status)
	require.NotNil(t, tr.QuoteID)
	assert.Equal(t, q.ID, *tr.QuoteID)
	assert.Equal(t, v.ID, *tr.InvoiceID)
	assert.Equal(t, "usdt-ethereum", tr.AssetID)
	assert.Equal(t, scannertest.BlockHash(1001), tr.BlockHash)
	assert.Equal(t, 1, tr.Confirmations)
	assert.True(t, tr.DetectedAt.Equal(h.Clock.Now()))
	assert.Equal(t, domain.InvoiceConfirming, h.status(v.ID))

	// Confirmation progress is persisted; nothing is verified early.
	f.SetHead(1002)
	h.tick("ethereum")
	assert.Equal(t, 2, h.transfer("ethereum", "0xpay", 0).Confirmations)
	assert.Empty(t, f.VerifyCalls())
	assert.Equal(t, domain.InvoiceConfirming, h.status(v.ID))

	f.SetHead(1003)
	res = h.tick("ethereum")
	assert.Equal(t, 1, res.Confirmed)
	require.Len(t, f.VerifyCalls(), 1)
	assert.Equal(t, "0xpay", f.VerifyCalls()[0].TxHash)
	tr = h.transfer("ethereum", "0xpay", 0)
	assert.Equal(t, domain.TransferConfirmed, tr.Status)
	assert.Equal(t, confirmations, tr.Confirmations)
	require.NotNil(t, tr.ConfirmedAt)
	inv := h.Invoice(v.ID)
	assert.Equal(t, domain.InvoicePaid, inv.Status)
	assert.False(t, inv.PaidLate)
	assert.Equal(t, []string{domain.EventConfirming, domain.EventPaid}, h.EventTypes(v.ID))

	// Re-scanning the same range changes nothing and emits nothing.
	h.setCursor("ethereum", 995)
	f.ResetCalls()
	res = h.tick("ethereum")
	assert.Zero(t, res.NewTransfers)
	assert.Zero(t, res.Revived)
	assert.Empty(t, f.VerifyCalls(), "confirmed transfers are final")
	assert.Len(t, h.allTransfers("ethereum"), 1)
	assert.Equal(t, "10000000", h.Quote(q.ID).AmountReceived.String())
	assert.Equal(t, []string{domain.EventConfirming, domain.EventPaid}, h.EventTypes(v.ID))
}

func TestStaticWalletExactAmountMatching(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	f := h.Fakes["tron"]
	usdt := h.contract("usdt-tron")
	v1, q1 := h.Quoted("10", "USD", "usdt-tron")
	v2, q2 := h.Quoted("10", "USD", "usdt-tron")
	require.Equal(t, "10000000", q1.AmountDue.String())
	require.Equal(t, "10000100", q2.AmountDue.String())
	h.tick("tron")

	f.Pay(1001, "tx-exact", h.TronWallet.Address, usdt, q2.AmountDue)          // exactly the second invoice's amount
	f.Pay(1001, "tx-wrong", h.TronWallet.Address, usdt, big.NewInt(9_990_000)) // matches nobody
	f.SetHead(1001)
	res := h.tick("tron")
	assert.Equal(t, 2, res.NewTransfers)
	exact := h.transfer("tron", "tx-exact", 0)
	require.NotNil(t, exact.QuoteID)
	assert.Equal(t, q2.ID, *exact.QuoteID)
	assert.Equal(t, domain.InvoiceConfirming, h.status(v2.ID))
	assert.Equal(t, domain.InvoicePending, h.status(v1.ID), "a different amount is not this invoice's")
	wrong := h.transfer("tron", "tx-wrong", 0)
	assert.Nil(t, wrong.QuoteID)
	assert.Nil(t, wrong.InvoiceID)
	assert.Equal(t, "usdt-tron", wrong.AssetID, "the asset is known, only the match is missing")

	// The admin assigns the unmatched transfer; the scanner later confirms it.
	_, err := h.Invoices.AssignTransfer(h.Ctx, wrong.ID, v1.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.InvoiceConfirming, h.status(v1.ID))
	f.SetHead(1003)
	res = h.tick("tron")
	assert.Equal(t, 2, res.Confirmed)
	assert.Equal(t, domain.InvoicePaid, h.status(v1.ID))
	assert.Equal(t, domain.InvoicePaid, h.status(v2.ID))
	assert.Equal(t, []string{domain.EventConfirming, domain.EventPaid}, h.EventTypes(v1.ID))
}

func TestUnmatchedTransfers(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	f := h.Fakes["ethereum"]
	a, qa := h.Quoted("10", "USD", "usdt-ethereum")
	h.Quoted("10", "USD", "usdc-ethereum") // makes USDC part of the request
	h.tick("ethereum")

	// USDC sent to an address that expects USDT: known asset, no quote.
	f.Pay(1001, "0xwrongasset", qa.Address, h.contract("usdc-ethereum"), big.NewInt(10_000_000))
	// An unknown token returned by the adapter (Solana-style adapters may do
	// that), a zero-value spam transfer, and the same transfer twice.
	unknown := chain.Transfer{TxHash: "0xunknown", BlockNumber: 1001, BlockHash: scannertest.BlockHash(1001),
		To: qa.Address, Token: "0x000000000000000000000000000000000000dEaD", Amount: big.NewInt(5)}
	spam := chain.Transfer{TxHash: "0xspam", BlockNumber: 1001, BlockHash: scannertest.BlockHash(1001),
		To: qa.Address, Token: h.contract("usdt-ethereum"), Amount: big.NewInt(0)}
	dup := chain.Transfer{TxHash: "0xdup", Index: 4, BlockNumber: 1001, BlockHash: scannertest.BlockHash(1001),
		To: qa.Address, Token: h.contract("usdt-ethereum"), Amount: big.NewInt(1_000_000)}
	h.Provider.Set("ethereum", &extraClient{FakeChain: f, extra: []chain.Transfer{unknown, spam, dup, dup}})
	f.SetHead(1001)
	res := h.tick("ethereum")
	assert.Equal(t, 3, res.NewTransfers)

	wrong := h.transfer("ethereum", "0xwrongasset", 0)
	assert.Nil(t, wrong.InvoiceID)
	assert.Equal(t, "usdc-ethereum", wrong.AssetID)
	unk := h.transfer("ethereum", "0xunknown", 0)
	assert.Nil(t, unk.InvoiceID)
	assert.Empty(t, unk.AssetID, "unknown token: no asset")
	assert.Equal(t, unknown.Token, unk.TokenContract)
	_, err := h.Store.GetTransferByTx(h.Ctx, "ethereum", "0xspam", 0)
	assert.ErrorIs(t, err, store.ErrNotFound, "zero-value transfers are ignored")
	d := h.transfer("ethereum", "0xdup", 4)
	require.NotNil(t, d.QuoteID)
	assert.Equal(t, qa.ID, *d.QuoteID)
	assert.Equal(t, "1000000", h.Quote(qa.ID).AmountReceived.String(), "the duplicate was credited once")
	assert.Equal(t, domain.InvoicePartial, h.status(a.ID))
	assert.Equal(t, []string{domain.EventPaymentDetected}, h.EventTypes(a.ID))
}

// extraClient returns additional transfers (inside the requested range)
// on top of the fake chain's own.
type extraClient struct {
	*scannertest.FakeChain
	extra []chain.Transfer
}

func (c *extraClient) ScanTransfers(ctx context.Context, req chain.ScanRequest) ([]chain.Transfer, error) {
	out, err := c.FakeChain.ScanTransfers(ctx, req)
	if err != nil {
		return nil, err
	}
	for _, t := range c.extra {
		if t.BlockNumber >= req.FromBlock && t.BlockNumber <= req.ToBlock {
			out = append(out, t)
		}
	}
	return out, nil
}

func (c *extraClient) VerifyTransfer(ctx context.Context, t chain.Transfer) (bool, error) {
	for _, x := range c.extra {
		if x.TxHash == t.TxHash && x.Index == t.Index {
			return true, nil
		}
	}
	return c.FakeChain.VerifyTransfer(ctx, t)
}

func TestPaymentAfterWatchUntilIgnored(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	eth, tron := h.Fakes["ethereum"], h.Fakes["tron"]
	v, q := h.Quoted("10", "USD", "usdt-ethereum")
	_, sq := h.Quoted("10", "USD", "usdt-tron")
	h.tick("ethereum")
	h.tick("tron")

	require.True(t, sq.WatchUntil.After(q.WatchUntil))
	h.Clock.Set(sq.WatchUntil)
	_, live := h.Quoted("10", "USD", "usdt-tron") // keeps the static address watched
	require.NotEqual(t, sq.AmountDue.String(), live.AmountDue.String())

	eth.Pay(1001, "0xlate", q.Address, h.contract("usdt-ethereum"), q.AmountDue)
	eth.SetHead(1001)
	res := h.tick("ethereum")
	assert.True(t, res.FastForwarded, "nobody watches the xpub address any more")
	assert.Empty(t, eth.ScanCalls())
	assert.Empty(t, h.allTransfers("ethereum"))
	assert.Equal(t, domain.InvoicePending, h.status(v.ID))

	// On the shared static address the late transfer is seen, but its amount
	// matches no live quote: stored unmatched.
	tron.Pay(1001, "late-static", h.TronWallet.Address, h.contract("usdt-tron"), sq.AmountDue)
	tron.SetHead(1001)
	h.tick("tron")
	late := h.transfer("tron", "late-static", 0)
	assert.Nil(t, late.QuoteID)
	assert.Equal(t, "0", h.Quote(sq.ID).AmountReceived.String())
}

func TestCancelledInvoiceStillRecordsFunds(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	f := h.Fakes["tron"]
	v, q := h.Quoted("10", "USD", "usdt-tron")
	_, err := h.Invoices.Cancel(h.Ctx, v.ID, "merchant")
	require.NoError(t, err)
	h.tick("tron")
	f.Pay(1001, "after-cancel", q.Address, h.contract("usdt-tron"), q.AmountDue)
	f.SetHead(1003)
	h.tick("tron")
	tr := h.transfer("tron", "after-cancel", 0)
	assert.Equal(t, v.ID, *tr.InvoiceID)
	assert.Equal(t, domain.TransferConfirmed, tr.Status)
	assert.Equal(t, q.AmountDue.String(), h.Quote(q.ID).AmountConfirmed.String())
	assert.Equal(t, domain.InvoiceCancelled, h.status(v.ID))
	assert.Equal(t, []string{domain.EventCancelled}, h.EventTypes(v.ID))
}

// ---- reorgs ----

func TestReorgOrphansAndReincludes(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	f := h.Fakes["tron"]
	v, q := h.Quoted("10", "USD", "usdt-tron")
	h.tick("tron")
	f.Pay(1001, "tx-r", q.Address, h.contract("usdt-tron"), q.AmountDue)
	f.SetHead(1002)
	h.tick("tron")
	require.Equal(t, domain.InvoiceConfirming, h.status(v.ID))

	// Reorg: the block at 1001 is replaced; the tx is re-included at the
	// same height in the new block — a height the cursor already passed.
	f.Move("tx-r", 1001, "")
	f.SetHead(1003)
	res := h.tick("tron")
	assert.Equal(t, 1, res.Orphaned)
	assert.True(t, res.Rewound)
	assert.True(t, res.Behind)
	assert.Equal(t, uint64(1000), res.Cursor, "rewound to the block before")
	assert.Equal(t, uint64(1000), h.chain("tron").ScanCursor)
	assert.Equal(t, domain.TransferOrphaned, h.transfer("tron", "tx-r", 0).Status)
	assert.Equal(t, domain.InvoicePending, h.status(v.ID), "the funds are gone: back to pending")
	assert.Equal(t, "0", h.Quote(q.ID).AmountReceived.String())

	// The next tick scans the range again and finds the re-included tx
	// (already deep enough, so it is verified in the same tick).
	res = h.tick("tron")
	assert.Equal(t, 1, res.Revived)
	assert.Zero(t, res.NewTransfers)
	assert.Equal(t, 1, res.Confirmed)
	tr := h.transfer("tron", "tx-r", 0)
	assert.Equal(t, domain.TransferConfirmed, tr.Status)
	assert.Equal(t, "0xreorg1001", tr.BlockHash)
	assert.Equal(t, domain.InvoicePaid, h.status(v.ID))
	assert.Len(t, h.allTransfers("tron"), 1, "one row")
	assert.Equal(t, q.AmountDue.String(), h.Quote(q.ID).AmountConfirmed.String(), "credited exactly once")
	assert.Equal(t, []string{domain.EventConfirming, domain.EventConfirming, domain.EventPaid}, h.EventTypes(v.ID))
}

func TestReorgMovesTxToLaterBlock(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	f := h.Fakes["bitcoin"]
	v, q := h.Quoted("100", "USD", "btc-bitcoin")
	h.tick("bitcoin")
	f.Pay(1001, "btc-tx", q.Address, "", q.AmountDue)
	f.SetHead(1001)
	h.tick("bitcoin")
	require.Equal(t, domain.InvoiceConfirming, h.status(v.ID))

	// The tx drops out of 1001 and is mined again at 1003 before reaching
	// depth: the scan picks up the new block data, no orphan/revive dance.
	f.Move("btc-tx", 1003, "")
	f.SetHead(1003)
	res := h.tick("bitcoin")
	assert.Zero(t, res.Orphaned)
	tr := h.transfer("bitcoin", "btc-tx", 0)
	assert.Equal(t, uint64(1003), tr.BlockNumber)
	assert.Equal(t, domain.TransferPending, tr.Status)
	assert.Equal(t, 1, tr.Confirmations)
	f.SetHead(1005)
	h.tick("bitcoin")
	assert.Equal(t, domain.InvoicePaid, h.status(v.ID))
	assert.Equal(t, []string{domain.EventConfirming, domain.EventPaid}, h.EventTypes(v.ID))
}

func TestReorgDropsTxForGood(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	f := h.Fakes["tron"]
	v, q := h.Quoted("10", "USD", "usdt-tron")
	h.tick("tron")
	f.Pay(1001, "tx-gone", q.Address, h.contract("usdt-tron"), q.AmountDue)
	f.SetHead(1001)
	h.tick("tron")
	require.True(t, f.Remove("tx-gone"))
	f.SetHead(1003)
	res := h.tick("tron")
	assert.Equal(t, 1, res.Orphaned)
	res = h.tick("tron") // rescans 1001-1003: nothing
	assert.Zero(t, res.Revived)
	assert.Equal(t, uint64(1003), res.Cursor)
	assert.Equal(t, domain.InvoicePending, h.status(v.ID))
	assert.Equal(t, domain.TransferOrphaned, h.transfer("tron", "tx-gone", 0).Status)
	assert.Equal(t, []string{domain.EventConfirming}, h.EventTypes(v.ID))
}

// ---- other families: no BlockHash (Solana), no sender (UTXO) ----

// strippedClient drops BlockHash and From like the Solana / UTXO adapters
// and verifies by (tx, index, block) only.
type strippedClient struct{ *scannertest.FakeChain }

func (c strippedClient) ScanTransfers(ctx context.Context, req chain.ScanRequest) ([]chain.Transfer, error) {
	out, err := c.FakeChain.ScanTransfers(ctx, req)
	for i := range out {
		out[i].BlockHash, out[i].From = "", ""
	}
	return out, err
}

func (c strippedClient) VerifyTransfer(ctx context.Context, t chain.Transfer) (bool, error) {
	if t.BlockHash != "" || t.From != "" {
		return false, errors.New("unexpected block hash / sender")
	}
	t.BlockHash = scannertest.BlockHash(t.BlockNumber)
	return c.FakeChain.VerifyTransfer(ctx, t)
}

func TestTransfersWithoutBlockHashOrSender(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	sol, btc := h.Fakes["solana"], h.Fakes["bitcoin"]
	h.Provider.Set("solana", strippedClient{sol})
	h.Provider.Set("bitcoin", strippedClient{btc})

	// Solana: static wallet, native SOL, exact amount.
	sv, sq := h.Quoted("15", "USD", "sol-solana")
	h.tick("solana")
	sol.Add(chain.Transfer{TxHash: "solsig", Index: 3, BlockNumber: 1001, To: h.SolanaWallet.Address, Amount: sq.AmountDue})
	sol.SetHead(1003)
	h.tick("solana")
	tr := h.transfer("solana", "solsig", 3)
	assert.Empty(t, tr.BlockHash)
	assert.Empty(t, tr.FromAddress)
	assert.Equal(t, domain.TransferConfirmed, tr.Status)
	assert.Equal(t, domain.InvoicePaid, h.status(sv.ID))

	// UTXO: one tx paying the invoice address in two outputs.
	bv, bq := h.Quoted("100", "USD", "btc-bitcoin")
	h.tick("bitcoin")
	half := new(big.Int).Div(bq.AmountDue, big.NewInt(2))
	rest := new(big.Int).Sub(bq.AmountDue, half)
	btc.Add(chain.Transfer{TxHash: "btctx", Index: 0, BlockNumber: 1001, To: bq.Address, Amount: half})
	btc.Add(chain.Transfer{TxHash: "btctx", Index: 1, BlockNumber: 1001, To: bq.Address, Amount: rest})
	btc.SetHead(1001)
	res := h.tick("bitcoin")
	assert.Equal(t, 2, res.NewTransfers)
	assert.Equal(t, domain.InvoiceConfirming, h.status(bv.ID))
	btc.SetHead(1003)
	res = h.tick("bitcoin")
	assert.Equal(t, 2, res.Confirmed)
	assert.Equal(t, domain.InvoicePaid, h.status(bv.ID))
	assert.Equal(t, bq.AmountDue.String(), h.Quote(bq.ID).AmountConfirmed.String())
}

// ---- errors ----

func TestRangeTooLargeHalvesTheBatch(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	f := h.Fakes["tron"]
	h.Quoted("10", "USD", "usdt-tron")
	_, err := h.Store.UpdateChain(h.Ctx, "tron", store.ChainUpdate{ScanBatch: ptr(40)})
	require.NoError(t, err)
	h.setCursor("tron", 900)
	f.SetMaxRange(7)

	for range 50 {
		if h.tick("tron").Cursor == startHead {
			break
		}
	}
	assert.Equal(t, uint64(startHead), h.chain("tron").ScanCursor)
	limit := h.M.BatchLimit("tron")
	assert.Positive(t, limit)
	assert.Less(t, limit, 40)

	// Successful requests cover 901..1000 contiguously, each within the limit.
	next := uint64(901)
	for _, r := range f.ScanCalls() {
		if r.ToBlock-r.FromBlock+1 > 7 {
			continue // rejected with ErrRangeTooLarge
		}
		require.Equal(t, next, r.FromBlock, "no gap, no overlap")
		next = r.ToBlock + 1
	}
	assert.Equal(t, uint64(startHead+1), next)

	// The provider relents: the batch grows back slowly to the configured size.
	f.SetMaxRange(0)
	f.Advance(40)
	h.tick("tron")
	assert.Positive(t, h.M.BatchLimit("tron"), "recovery is gradual")
	for range 100 {
		f.Advance(40)
		h.tick("tron")
		if h.M.BatchLimit("tron") == 0 {
			break
		}
	}
	assert.Zero(t, h.M.BatchLimit("tron"))

	// A single block that is still "too large" is a real error.
	_, err = h.Store.UpdateChain(h.Ctx, "tron", store.ChainUpdate{ScanBatch: ptr(1)})
	require.NoError(t, err)
	cursor := h.chain("tron").ScanCursor
	f.Advance(1)
	f.FailScan(fmt.Errorf("provider: %w", chain.ErrRangeTooLarge))
	_, err = h.M.TickOnce(h.Ctx, "tron")
	assert.ErrorIs(t, err, chain.ErrRangeTooLarge)
	assert.Equal(t, cursor, h.chain("tron").ScanCursor)
}

func TestRateLimitedKeepsCursorAndRecordsSanitizedError(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	f := h.Fakes["ethereum"]
	h.Quoted("10", "USD", "usdt-ethereum")
	h.setCursor("ethereum", 990)
	f.FailScan(fmt.Errorf(`POST "https://eth.example.com/v2/SECRETKEY?apikey=SECRET2": status 429: %w`, chain.ErrRateLimited))

	res, err := h.M.TickOnce(h.Ctx, "ethereum")
	require.Error(t, err)
	assert.ErrorIs(t, err, chain.ErrRateLimited)
	assert.Equal(t, uint64(990), res.Cursor)
	c := h.chain("ethereum")
	assert.Equal(t, uint64(990), c.ScanCursor, "the range is retried, never skipped")
	assert.Equal(t, uint64(startHead), c.LastHead)
	assert.Contains(t, c.LastError, "rate limited")
	assert.Contains(t, c.LastError, "eth.example.com")
	assert.NotContains(t, c.LastError, "SECRET")

	res = h.tick("ethereum")
	assert.Equal(t, uint64(startHead), res.Cursor)
	assert.Empty(t, h.chain("ethereum").LastError, "cleared by the next successful tick")
	assert.Equal(t, []string{"991-1000", "991-1000"}, ranges(f.ScanCalls()))

	// Head failures likewise leave the state alone.
	f.FailHead(errors.New("dial tcp: connection refused"))
	_, err = h.M.TickOnce(h.Ctx, "ethereum")
	require.Error(t, err)
	assert.Equal(t, uint64(startHead), h.chain("ethereum").ScanCursor)
	assert.Contains(t, h.chain("ethereum").LastError, "connection refused")
}

func TestBackoff(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	m := scanner.New(st, scannertest.NewProvider(nil), invoice.New(st, rates.NewStatic(nil), nil, nil), nil, nil,
		scanner.Config{BackoffBase: 100 * time.Millisecond, BackoffMax: 800 * time.Millisecond})
	within := func(d, centre time.Duration) bool { return d >= centre*3/4 && d <= centre*5/4 }
	for range 50 {
		assert.True(t, within(m.Backoff(1), 100*time.Millisecond))
		assert.True(t, within(m.Backoff(2), 200*time.Millisecond))
		assert.True(t, within(m.Backoff(3), 400*time.Millisecond))
		assert.True(t, within(m.Backoff(4), 800*time.Millisecond))
		assert.True(t, within(m.Backoff(60), 800*time.Millisecond), "capped")
	}
	seen := map[time.Duration]bool{}
	for range 50 {
		seen[m.Backoff(3)] = true
	}
	assert.Greater(t, len(seen), 1, "jittered")
}

func TestVerifyErrorKeepsTransferPending(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	f := h.Fakes["tron"]
	v, q := h.Quoted("10", "USD", "usdt-tron")
	h.tick("tron")
	f.Pay(1001, "tx-v", q.Address, h.contract("usdt-tron"), q.AmountDue)
	f.SetHead(1003)
	f.FailVerify(errors.New("node: header not found"))
	res, err := h.M.TickOnce(h.Ctx, "tron")
	require.NoError(t, err, "a node that cannot answer does not fail the tick")
	assert.Equal(t, 1, res.VerifyErrors)
	assert.Zero(t, res.Confirmed)
	assert.Zero(t, res.Orphaned)
	tr := h.transfer("tron", "tx-v", 0)
	assert.Equal(t, domain.TransferPending, tr.Status)
	assert.Equal(t, 3, tr.Confirmations)
	assert.Equal(t, domain.InvoiceConfirming, h.status(v.ID))
	assert.Contains(t, h.chain("tron").LastError, "header not found")
	assert.Equal(t, uint64(1003), h.chain("tron").ScanCursor)

	res = h.tick("tron")
	assert.Equal(t, 1, res.Confirmed)
	assert.Equal(t, domain.InvoicePaid, h.status(v.ID))
	assert.Empty(t, h.chain("tron").LastError)

	// A rate-limited verification makes the tick fail (back off).
	v2, q2 := h.Quoted("10", "USD", "usdt-tron")
	f.Pay(1004, "tx-v2", q2.Address, h.contract("usdt-tron"), q2.AmountDue)
	f.SetHead(1006)
	f.FailVerify(fmt.Errorf("verify: %w", chain.ErrRateLimited))
	_, err = h.M.TickOnce(h.Ctx, "tron")
	assert.ErrorIs(t, err, chain.ErrRateLimited)
	assert.Equal(t, domain.InvoiceConfirming, h.status(v2.ID))
	assert.Equal(t, uint64(1006), h.chain("tron").ScanCursor, "the scan itself was kept")
	h.tick("tron")
	assert.Equal(t, domain.InvoicePaid, h.status(v2.ID))
}

func TestConfirmationsProceedWhileScanningFails(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	f := h.Fakes["tron"]
	v, q := h.Quoted("10", "USD", "usdt-tron")
	h.tick("tron")
	f.Pay(1001, "tx-c", q.Address, h.contract("usdt-tron"), q.AmountDue)
	f.SetHead(1001)
	h.tick("tron")
	f.SetHead(1010)
	f.SetScanError(errors.New("eth_getLogs: query timeout"))
	_, err := h.M.TickOnce(h.Ctx, "tron")
	require.Error(t, err)
	assert.Equal(t, uint64(1001), h.chain("tron").ScanCursor)
	assert.Equal(t, domain.InvoicePaid, h.status(v.ID), "the detected payment was confirmed anyway")
}

func TestInactiveChainsAreSkipped(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	base := scannertest.NewFakeChain(5)
	h.Provider.Set("base", base)
	res, err := h.M.TickOnce(h.Ctx, "base") // enabled, no wallet
	require.NoError(t, err)
	assert.Equal(t, scanner.SkipInactive, res.Skipped)
	_, err = h.Store.UpdateChain(h.Ctx, "tron", store.ChainUpdate{Enabled: ptr(false)})
	require.NoError(t, err)
	res, err = h.M.TickOnce(h.Ctx, "tron")
	require.NoError(t, err)
	assert.Equal(t, scanner.SkipInactive, res.Skipped)
	assert.Zero(t, base.HeadCalls())
	assert.Zero(t, h.Fakes["tron"].HeadCalls())
	_, err = h.M.TickOnce(h.Ctx, "nope")
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestPanicIsRecovered(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	f := h.Fakes["tron"]
	h.tick("tron")
	f.PanicOnHead("boom")
	_, err := h.M.TickOnce(h.Ctx, "tron")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "panic")
	c := h.chain("tron")
	assert.Contains(t, c.LastError, "panic")
	assert.Equal(t, uint64(startHead), c.ScanCursor)
	// The lock and the tick slot were released: the next tick works.
	f.PanicOnHead(nil)
	f.Advance(2)
	assert.Equal(t, uint64(startHead+2), h.tick("tron").Cursor)
	assert.Empty(t, h.chain("tron").LastError)
}

// ---- several instances ----

// gateClient blocks Head until released, so a test can hold a tick (and
// with it the chain's advisory lock) open.
type gateClient struct {
	*scannertest.FakeChain
	entered chan struct{}
	release chan struct{}
}

func (g *gateClient) Head(ctx context.Context) (uint64, error) {
	g.entered <- struct{}{}
	select {
	case <-g.release:
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	return g.FakeChain.Head(ctx)
}

func TestAdvisoryLockOneInstancePerChain(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	f := h.Fakes["tron"]
	gate := &gateClient{FakeChain: f, entered: make(chan struct{}, 1), release: make(chan struct{})}
	a := scanner.New(h.Store, scannertest.NewProvider(map[string]chain.Client{"tron": gate}), h.Invoices, h.Log, h.Clock.Now, testConfig)
	b := scanner.New(h.Store, scannertest.NewProvider(map[string]chain.Client{"tron": f}), h.Invoices, h.Log, h.Clock.Now, testConfig)

	type result struct {
		res scanner.TickResult
		err error
	}
	done := make(chan result, 1)
	go func() {
		res, err := a.TickOnce(h.Ctx, "tron")
		done <- result{res, err}
	}()
	<-gate.entered // instance A holds the lock
	res, err := b.TickOnce(h.Ctx, "tron")
	require.NoError(t, err)
	assert.Equal(t, scanner.SkipLocked, res.Skipped)
	assert.Zero(t, f.HeadCalls(), "B did not touch the chain")
	// Another schema (another deployment) is not blocked: different key.
	other := newHarness(t)
	other.tick("tron")
	close(gate.release)
	r := <-done
	require.NoError(t, r.err)
	assert.Empty(t, r.res.Skipped)
	// Lock released: B can scan now.
	res, err = b.TickOnce(h.Ctx, "tron")
	require.NoError(t, err)
	assert.Empty(t, res.Skipped)
}

// trackingClient measures how many calls run at the same time.
type trackingClient struct {
	*scannertest.FakeChain
	inFlight, maxInFlight atomic.Int32
}

func (c *trackingClient) Head(ctx context.Context) (uint64, error) {
	n := c.inFlight.Add(1)
	defer c.inFlight.Add(-1)
	for {
		m := c.maxInFlight.Load()
		if n <= m || c.maxInFlight.CompareAndSwap(m, n) {
			break
		}
	}
	time.Sleep(3 * time.Millisecond)
	return c.FakeChain.Head(ctx)
}

func TestConcurrentInstancesNeverScanTogether(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	f := h.Fakes["tron"]
	v, q := h.Quoted("10", "USD", "usdt-tron")
	tc := &trackingClient{FakeChain: f}
	h.tick("tron")
	f.Pay(1001, "tx-cc", q.Address, h.contract("usdt-tron"), q.AmountDue)
	f.SetHead(1001)

	var ran, skipped atomic.Int32
	var wg sync.WaitGroup
	for range 3 {
		m := scanner.New(h.Store, scannertest.NewProvider(map[string]chain.Client{"tron": tc}), h.Invoices, h.Log, h.Clock.Now, testConfig)
		wg.Go(func() {
			for range 15 {
				res, err := m.TickOnce(h.Ctx, "tron")
				if !assert.NoError(t, err) {
					return
				}
				if res.Skipped == scanner.SkipLocked {
					skipped.Add(1)
				} else {
					ran.Add(1)
				}
				f.Advance(1)
			}
		})
	}
	wg.Wait()
	assert.Equal(t, int32(1), tc.maxInFlight.Load(), "never two instances on one chain at once")
	assert.Positive(t, ran.Load())
	assert.Equal(t, int32(45), ran.Load()+skipped.Load())
	assert.Len(t, h.allTransfers("tron"), 1)
	assert.Equal(t, domain.InvoicePaid, h.status(v.ID))
	assert.Equal(t, []string{domain.EventConfirming, domain.EventPaid}, h.EventTypes(v.ID))
}

// ---- the Run loop ----

func TestRunFollowsConfigAndStopsCleanly(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	tron := h.Fakes["tron"]
	ctx, cancel := context.WithCancel(h.Ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.M.Run(ctx) }()

	for _, id := range scannedChains {
		eventually(t, id+" scanned", func() bool { return h.Fakes[id].HeadCalls() > 0 })
	}
	eventually(t, "cursor initialised", func() bool { return h.chain("tron").ScanCursor == startHead })

	// The admin disables tron: its loop stops (and its client is dropped).
	_, err := h.Store.UpdateChain(h.Ctx, "tron", store.ChainUpdate{Enabled: ptr(false)})
	require.NoError(t, err)
	eventually(t, "tron invalidated", func() bool { return slices.Contains(h.Provider.Invalidated(), "tron") })
	time.Sleep(100 * time.Millisecond)
	calls := tron.HeadCalls()
	time.Sleep(800 * time.Millisecond) // longer than the poll interval
	assert.Equal(t, calls, tron.HeadCalls(), "a disabled chain is not scanned")

	// Re-enabled, and a new chain gets a wallet: both are picked up.
	_, err = h.Store.UpdateChain(h.Ctx, "tron", store.ChainUpdate{Enabled: ptr(true)})
	require.NoError(t, err)
	base := scannertest.NewFakeChain(77)
	h.Provider.Set("base", base)
	storetest.AssignWallet(t, h.Store, "base", h.EVMWallet.ID)
	eventually(t, "tron resumed", func() bool { return tron.HeadCalls() > calls })
	eventually(t, "base started", func() bool { return h.chain("base").ScanCursor == 77 })

	// The expiry ticker runs too.
	v := h.Create("10", "USD")
	h.Clock.Set(v.ExpiresAt)
	eventually(t, "invoice expired", func() bool { return h.status(v.ID) == domain.InvoiceExpired })

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
	assert.Empty(t, h.chain("tron").LastError, "shutdown is not recorded as a scan error")
}

func TestRunSurvivesPanics(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	f := h.Fakes["tron"]
	f.PanicOnHead("kaboom")
	ctx, cancel := context.WithCancel(h.Ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.M.Run(ctx) }()
	// Repeated panics (more than the tick slots) are retried with backoff.
	eventually(t, "retries after panics", func() bool { return f.HeadCalls() >= 5 })
	assert.Contains(t, h.chain("tron").LastError, "panic")
	assert.Contains(t, h.Provider.Invalidated(), "tron", "client dropped after repeated failures")
	f.PanicOnHead(nil)
	eventually(t, "recovered", func() bool { return h.chain("tron").ScanCursor == startHead })
	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

// ---- helpers ----

func TestSanitizeError(t *testing.T) {
	t.Parallel()
	assert.Empty(t, scanner.SanitizeError(nil))
	got := scanner.SanitizeError(errors.New(`Post "https://user:pw@rpc.example.org:8545/v3/abc123?key=zzz": EOF; also wss://ws.example.net/key/xyz and ftp://`))
	assert.Equal(t, `Post "https://rpc.example.org/…": EOF; also wss://ws.example.net/… and ftp://`, got)
	long := scanner.SanitizeError(errors.New(strings.Repeat("é", 400)))
	assert.LessOrEqual(t, len(long), scanner.MaxErrorLen)
	assert.True(t, strings.HasPrefix(strings.Repeat("é", 400), long), "cut on a rune boundary")
}
