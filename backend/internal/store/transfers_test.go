package store_test

import (
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cryptopay/internal/domain"
	"cryptopay/internal/store"
	"cryptopay/internal/store/storetest"
)

func TestInsertTransferIfNewIdempotent(t *testing.T) {
	t.Parallel()
	clk := storetest.FixedClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	s := storetest.NewSeeded(t, store.WithClock(clk.Now))

	amount, _ := new(big.Int).SetString("115792089237316195423570985008687907853269984665640564039457584007913129639935", 10) // 2^256-1
	blockTime := time.Date(2029, 12, 31, 23, 59, 59, 0, time.UTC)
	in := domain.Transfer{
		ChainID: "ethereum", TxHash: "0xdeadbeef", Index: -1, BlockNumber: 19_000_000, BlockHash: "0xblock",
		BlockTime: blockTime, FromAddress: "0xFrom", ToAddress: "0xTo", Amount: amount,
	}
	x, inserted, err := s.InsertTransferIfNew(ctx, in)
	require.NoError(t, err)
	assert.True(t, inserted)
	assert.Equal(t, domain.TransferPending, x.Status)
	assert.Equal(t, 0, amount.Cmp(x.Amount), x.Amount.String())
	assert.Equal(t, -1, x.Index)
	assert.EqualValues(t, 19_000_000, x.BlockNumber)
	assert.Equal(t, blockTime, x.BlockTime)
	assert.Equal(t, clk.Now(), x.DetectedAt)
	assert.Equal(t, "", x.AssetID, "unknown token ↔ NULL")
	assert.Nil(t, x.QuoteID)
	assert.Nil(t, x.ConfirmedAt)

	// Re-scan: no-op, returns the stored row unchanged.
	clk.Advance(time.Hour)
	again := in
	again.Amount = big.NewInt(1)
	again.BlockHash = "0xother"
	y, inserted, err := s.InsertTransferIfNew(ctx, again)
	require.NoError(t, err)
	assert.False(t, inserted)
	assert.Equal(t, x, y)

	// Different index in the same tx is a different transfer.
	in2 := in
	in2.Index = 3
	_, inserted, err = s.InsertTransferIfNew(ctx, in2)
	require.NoError(t, err)
	assert.True(t, inserted)

	got, err := s.GetTransferByTx(ctx, "ethereum", "0xdeadbeef", -1)
	require.NoError(t, err)
	assert.Equal(t, x.ID, got.ID)
	got, err = s.GetTransfer(ctx, x.ID)
	require.NoError(t, err)
	assert.Equal(t, x, got)
	_, err = s.GetTransfer(ctx, uuid.New())
	require.ErrorIs(t, err, store.ErrNotFound)

	// Validation.
	_, _, err = s.InsertTransferIfNew(ctx, domain.Transfer{ChainID: "ethereum", TxHash: "h", ToAddress: "a"})
	require.ErrorIs(t, err, store.ErrInvalid, "nil amount")
	_, _, err = s.InsertTransferIfNew(ctx, domain.Transfer{ChainID: "nope", TxHash: "h", ToAddress: "a", Amount: big.NewInt(1)})
	require.ErrorIs(t, err, store.ErrForeignKey)
	_, _, err = s.InsertTransferIfNew(ctx, domain.Transfer{ChainID: "tron", TxHash: "h", ToAddress: "a", Amount: big.NewInt(1), BlockNumber: 1 << 63})
	require.ErrorIs(t, err, store.ErrInvalid)
}

func TestInsertTransferConcurrent(t *testing.T) {
	t.Parallel()
	s := storetest.NewSeeded(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	inserted := 0
	ids := map[uuid.UUID]bool{}
	for range 12 {
		wg.Go(func() {
			x, ins, err := s.InsertTransferIfNew(ctx, domain.Transfer{ChainID: "tron", TxHash: "race", Index: 0,
				BlockNumber: 5, ToAddress: "T", Amount: big.NewInt(100)})
			if !assert.NoError(t, err) {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			ids[x.ID] = true
			if ins {
				inserted++
			}
		})
	}
	wg.Wait()
	assert.Equal(t, 1, inserted)
	assert.Len(t, ids, 1, "everyone sees the same row")
}

func TestTransferLifecycleAndSums(t *testing.T) {
	t.Parallel()
	clk := storetest.FixedClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	s := storetest.NewSeeded(t, store.WithClock(clk.Now))
	m := storetest.Merchant(t, s)
	w := storetest.Wallet(t, s, domain.FamilyEVM, domain.WalletXpub)
	inv := storetest.Invoice(t, s, m.ID)
	q := storetest.Quote(t, s, inv, "eth-ethereum", w)

	oneEth, _ := new(big.Int).SetString("1000000000000000000", 10)
	t1 := storetest.TransferFor(t, s, q, 0, func(x *domain.Transfer) { x.Amount = oneEth; x.BlockNumber = 10 })
	clk.Advance(time.Second)
	t2 := storetest.TransferFor(t, s, q, 250, func(x *domain.Transfer) { x.BlockNumber = 11 })
	clk.Advance(time.Second)
	t3 := storetest.TransferFor(t, s, q, 7, func(x *domain.Transfer) { x.BlockNumber = 12 })

	rec, conf, err := s.QuoteTransferSums(ctx, q.ID)
	require.NoError(t, err)
	assert.Equal(t, "1000000000000000257", rec.String())
	assert.Equal(t, "0", conf.String())

	// Confirm t1, orphan t3.
	require.NoError(t, s.UpdateTransferConfirmations(ctx, t1.ID, 5))
	c1, err := s.SetTransferStatus(ctx, t1.ID, domain.TransferConfirmed, 12)
	require.NoError(t, err)
	assert.Equal(t, domain.TransferConfirmed, c1.Status)
	assert.Equal(t, 12, c1.Confirmations)
	require.NotNil(t, c1.ConfirmedAt)
	assert.Equal(t, clk.Now(), *c1.ConfirmedAt)
	clk.Advance(time.Minute)
	c1b, err := s.SetTransferStatus(ctx, t1.ID, domain.TransferConfirmed, 13)
	require.NoError(t, err)
	assert.Equal(t, *c1.ConfirmedAt, *c1b.ConfirmedAt, "confirmed_at kept")
	o3, err := s.SetTransferStatus(ctx, t3.ID, domain.TransferOrphaned, 0)
	require.NoError(t, err)
	assert.Nil(t, o3.ConfirmedAt)

	rec, conf, err = s.QuoteTransferSums(ctx, q.ID)
	require.NoError(t, err)
	assert.Equal(t, "1000000000000000250", rec.String(), "orphaned excluded")
	assert.Equal(t, "1000000000000000000", conf.String())

	rq, err := s.RecomputeQuoteAmounts(ctx, q.ID)
	require.NoError(t, err)
	assert.Equal(t, "1000000000000000250", rq.AmountReceived.String())
	assert.Equal(t, "1000000000000000000", rq.AmountConfirmed.String())
	assert.Equal(t, clk.Now(), rq.UpdatedAt)

	// Quote without transfers recomputes to zero.
	q2 := storetest.Quote(t, s, inv, "usdt-ethereum", w, func(x *domain.Quote) { x.AmountReceived = big.NewInt(99) })
	rq2, err := s.RecomputeQuoteAmounts(ctx, q2.ID)
	require.NoError(t, err)
	assert.Zero(t, rq2.AmountReceived.Sign())
	_, err = s.RecomputeQuoteAmounts(ctx, uuid.New())
	require.ErrorIs(t, err, store.ErrNotFound)

	// Lists.
	byInv, err := s.ListTransfersByInvoice(ctx, inv.ID)
	require.NoError(t, err)
	require.Len(t, byInv, 3)
	assert.Equal(t, []uuid.UUID{t3.ID, t2.ID, t1.ID}, []uuid.UUID{byInv[0].ID, byInv[1].ID, byInv[2].ID}, "newest first")
	byQ, err := s.ListTransfersByQuote(ctx, q.ID)
	require.NoError(t, err)
	assert.Len(t, byQ, 3)
	pend, err := s.ListPendingTransfers(ctx, "ethereum")
	require.NoError(t, err)
	require.Len(t, pend, 1)
	assert.Equal(t, t2.ID, pend[0].ID)

	// Revive the orphaned transfer seen again in another block.
	_, err = s.ReviveTransfer(ctx, t2.ID, 20, "0xnew", clk.Now())
	require.ErrorIs(t, err, store.ErrConflict, "not orphaned")
	_, err = s.ReviveTransfer(ctx, uuid.New(), 20, "0xnew", clk.Now())
	require.ErrorIs(t, err, store.ErrNotFound)
	rv, err := s.ReviveTransfer(ctx, t3.ID, 20, "0xnew", clk.Now())
	require.NoError(t, err)
	assert.Equal(t, domain.TransferPending, rv.Status)
	assert.EqualValues(t, 20, rv.BlockNumber)
	assert.Equal(t, "0xnew", rv.BlockHash)
	assert.Zero(t, rv.Confirmations)
	pend, err = s.ListPendingTransfers(ctx, "ethereum")
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{t2.ID, t3.ID}, []uuid.UUID{pend[0].ID, pend[1].ID}, "by block")

	_, err = s.SetTransferStatus(ctx, t1.ID, "lost", 0)
	require.ErrorIs(t, err, store.ErrInvalid)
	require.ErrorIs(t, s.UpdateTransferConfirmations(ctx, uuid.New(), 1), store.ErrNotFound)

	err = s.WithTx(ctx, func(tx *store.Store) error {
		x, err := tx.GetTransferForUpdate(ctx, t1.ID)
		assert.Equal(t, t1.ID, x.ID)
		return err
	})
	require.NoError(t, err)
}

func TestAssignTransfer(t *testing.T) {
	t.Parallel()
	s := storetest.NewSeeded(t)
	m := storetest.Merchant(t, s)
	w := storetest.Wallet(t, s, domain.FamilySolana, domain.WalletStatic)
	inv := storetest.Invoice(t, s, m.ID)
	q := storetest.Quote(t, s, inv, "usdc-solana", w)

	un := storetest.Transfer(t, s, func(x *domain.Transfer) {
		x.ChainID, x.AssetID, x.ToAddress, x.Amount = "solana", "usdc-solana", w.Address, big.NewInt(9_999_999)
	})
	a, err := s.AssignTransfer(ctx, un.ID, q.ID, inv.ID)
	require.NoError(t, err)
	require.NotNil(t, a.QuoteID)
	assert.Equal(t, q.ID, *a.QuoteID)
	assert.Equal(t, inv.ID, *a.InvoiceID)
	_, err = s.AssignTransfer(ctx, un.ID, q.ID, inv.ID)
	require.ErrorIs(t, err, store.ErrConflict, "already assigned")
	_, err = s.AssignTransfer(ctx, uuid.New(), q.ID, inv.ID)
	require.ErrorIs(t, err, store.ErrNotFound)
	un2 := storetest.Transfer(t, s)
	_, err = s.AssignTransfer(ctx, un2.ID, uuid.New(), inv.ID)
	require.ErrorIs(t, err, store.ErrForeignKey)
}

func TestListTransfers(t *testing.T) {
	t.Parallel()
	clk := storetest.FixedClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	s := storetest.NewSeeded(t, store.WithClock(clk.Now))
	m := storetest.Merchant(t, s)
	w := storetest.Wallet(t, s, domain.FamilyEVM, domain.WalletStatic)
	inv := storetest.Invoice(t, s, m.ID)
	q := storetest.Quote(t, s, inv, "usdc-base", w)

	matched := storetest.TransferFor(t, s, q, 10, func(x *domain.Transfer) {
		x.TxHash = "0xAAAA"
		x.FromAddress = "0xCustomerWallet"
	})
	clk.Advance(time.Second)
	unmatched := storetest.Transfer(t, s, func(x *domain.Transfer) { x.ChainID = "base"; x.ToAddress = w.Address; x.TxHash = "0xBBBB" })
	clk.Advance(time.Second)
	tronT := storetest.Transfer(t, s, func(x *domain.Transfer) { x.Status = domain.TransferConfirmed; x.FromAddress = "" })

	ids := func(f store.TransferFilter) []uuid.UUID {
		t.Helper()
		p, err := s.ListTransfers(ctx, f)
		require.NoError(t, err)
		out := make([]uuid.UUID, len(p.Items))
		for i, x := range p.Items {
			out[i] = x.ID
		}
		assert.Equal(t, len(out), min(p.Total, p.PageSize))
		return out
	}
	yes, no := true, false
	assert.Equal(t, []uuid.UUID{tronT.ID, unmatched.ID, matched.ID}, ids(store.TransferFilter{}))
	assert.Equal(t, []uuid.UUID{unmatched.ID, matched.ID}, ids(store.TransferFilter{ChainID: "base"}))
	assert.Equal(t, []uuid.UUID{tronT.ID}, ids(store.TransferFilter{Status: domain.TransferConfirmed}))
	assert.Equal(t, []uuid.UUID{matched.ID}, ids(store.TransferFilter{Matched: &yes}))
	assert.Equal(t, []uuid.UUID{tronT.ID, unmatched.ID}, ids(store.TransferFilter{Matched: &no}))
	assert.Equal(t, []uuid.UUID{matched.ID}, ids(store.TransferFilter{InvoiceID: &inv.ID}))
	assert.Equal(t, []uuid.UUID{matched.ID}, ids(store.TransferFilter{Q: "0xaaaa"}))
	assert.Equal(t, []uuid.UUID{matched.ID}, ids(store.TransferFilter{Q: "0xcustomerwallet"}))
	assert.Equal(t, []uuid.UUID{unmatched.ID, matched.ID}, ids(store.TransferFilter{Q: w.Address}))
	assert.Len(t, ids(store.TransferFilter{Q: "   "}), 3, "blank q is ignored")
	assert.Equal(t, []uuid.UUID{matched.ID}, ids(store.TransferFilter{Page: 2, PageSize: 1, ChainID: "base"}))
	_, err := s.ListTransfers(ctx, store.TransferFilter{Status: "x"})
	require.ErrorIs(t, err, store.ErrInvalid)
}
