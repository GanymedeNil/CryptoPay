package store_test

import (
	"math/big"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cryptopay/internal/domain"
	"cryptopay/internal/store"
	"cryptopay/internal/store/storetest"
)

// pow2 returns 2^n.
func pow2(n uint) *big.Int { return new(big.Int).Lsh(big.NewInt(1), n) }

func TestCreateQuoteRoundTrip(t *testing.T) {
	t.Parallel()
	s := storetest.NewSeeded(t)
	m := storetest.Merchant(t, s)
	w := storetest.Wallet(t, s, domain.FamilyEVM, domain.WalletXpub)
	inv := storetest.Invoice(t, s, m.ID)

	due := pow2(255)                                       // 2^255
	received := new(big.Int).Sub(pow2(256), big.NewInt(1)) // 2^256-1 (78 digits)
	confirmed, _ := new(big.Int).SetString("123456789012345678901234567890123456789012345678901234567890", 10)
	rate := decimal.RequireFromString("67187.123456789012345678")
	idx := uint32(4_000_000_000)
	q, err := s.CreateQuote(ctx, domain.Quote{
		InvoiceID: inv.ID, AssetID: "eth-ethereum", ChainID: "ethereum", WalletID: w.ID, WalletKind: domain.WalletXpub,
		Address: "0xAbC", AddressIndex: &idx, Rate: rate, AmountDue: due, AmountReceived: received,
		AmountConfirmed: confirmed, WatchUntil: inv.ExpiresAt.Add(time.Hour),
	})
	require.NoError(t, err)
	assert.Equal(t, domain.QuoteActive, q.Status)
	assert.Equal(t, 0, due.Cmp(q.AmountDue), q.AmountDue.String())
	assert.Equal(t, 0, received.Cmp(q.AmountReceived), q.AmountReceived.String())
	assert.Equal(t, 0, confirmed.Cmp(q.AmountConfirmed))
	assert.Equal(t, "67187.123456789012345678", q.Rate.String())
	require.NotNil(t, q.AddressIndex)
	assert.Equal(t, idx, *q.AddressIndex)

	got, err := s.GetQuote(ctx, q.ID)
	require.NoError(t, err)
	assert.Equal(t, q, got)

	// Defaults for nil amounts; 18-decimal base units.
	weiDue, _ := new(big.Int).SetString("1500000000000000001", 10) // 1.500000000000000001 ETH
	q2, err := s.CreateQuote(ctx, domain.Quote{InvoiceID: inv.ID, AssetID: "eth-base", ChainID: "base", WalletID: w.ID,
		WalletKind: domain.WalletXpub, Address: "0xAbC", AddressIndex: &idx, Rate: rate, AmountDue: weiDue,
		WatchUntil: inv.ExpiresAt})
	require.NoError(t, err)
	assert.Equal(t, "1500000000000000001", q2.AmountDue.String())
	assert.Equal(t, 0, q2.AmountReceived.Sign())
	assert.Equal(t, 0, q2.AmountConfirmed.Sign())

	// Validation and FK errors.
	base := domain.Quote{InvoiceID: inv.ID, AssetID: "usdt-tron", ChainID: "tron", WalletID: w.ID,
		WalletKind: domain.WalletXpub, Address: "T1", Rate: rate, AmountDue: big.NewInt(1), WatchUntil: inv.ExpiresAt}
	bad := base
	bad.AmountDue = big.NewInt(0)
	_, err = s.CreateQuote(ctx, bad)
	require.ErrorIs(t, err, store.ErrInvalid)
	bad = base
	bad.WalletKind = "x"
	_, err = s.CreateQuote(ctx, bad)
	require.ErrorIs(t, err, store.ErrInvalid)
	bad = base
	bad.Status = "x"
	_, err = s.CreateQuote(ctx, bad)
	require.ErrorIs(t, err, store.ErrInvalid)
	bad = base
	bad.AssetID = "nope"
	_, err = s.CreateQuote(ctx, bad)
	require.ErrorIs(t, err, store.ErrForeignKey)
	bad = base
	bad.AmountDue = new(big.Int).Exp(big.NewInt(10), big.NewInt(78), nil) // 79 digits
	_, err = s.CreateQuote(ctx, bad)
	require.ErrorIs(t, err, store.ErrInvalid)
	_, err = s.GetQuote(ctx, uuid.New())
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestQuoteConstraints(t *testing.T) {
	t.Parallel()
	clk := storetest.FixedClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	s := storetest.NewSeeded(t, store.WithClock(clk.Now))
	m := storetest.Merchant(t, s)
	sw := storetest.Wallet(t, s, domain.FamilySolana, domain.WalletStatic)
	inv1 := storetest.Invoice(t, s, m.ID)
	inv2 := storetest.Invoice(t, s, m.ID)

	q1 := storetest.Quote(t, s, inv1, "usdt-solana", sw, func(q *domain.Quote) { q.AmountDue = big.NewInt(10_000_000) })

	// Same static address/asset/amount for another invoice → collision.
	newQ := func(inv domain.Invoice, amount int64) domain.Quote {
		return domain.Quote{InvoiceID: inv.ID, AssetID: "usdt-solana", ChainID: "solana", WalletID: sw.ID,
			WalletKind: domain.WalletStatic, Address: sw.Address, Rate: decimal.NewFromInt(1),
			AmountDue: big.NewInt(amount), WatchUntil: inv.ExpiresAt.Add(time.Hour)}
	}
	_, err := s.CreateQuote(ctx, newQ(inv2, 10_000_000))
	require.ErrorIs(t, err, store.ErrConflict)
	assert.Equal(t, store.ConstraintQuoteStaticAmount, store.ConstraintName(err))

	// Second live quote for the same invoice and asset.
	_, err = s.CreateQuote(ctx, newQ(inv1, 10_000_001))
	require.ErrorIs(t, err, store.ErrConflict)
	assert.Equal(t, store.ConstraintQuoteInvoiceAssetLive, store.ConstraintName(err))

	// Bump-and-retry loop inside one transaction (savepoints keep it alive).
	var created domain.Quote
	err = s.WithTx(ctx, func(tx *store.Store) error {
		_, err := tx.GetInvoiceForUpdate(ctx, inv2.ID)
		require.NoError(t, err)
		amount := int64(10_000_000)
		for {
			q, err := tx.CreateQuote(ctx, newQ(inv2, amount))
			if store.IsConstraint(err, store.ConstraintQuoteStaticAmount) {
				amount++
				continue
			}
			created = q
			return err
		}
	})
	require.NoError(t, err)
	assert.EqualValues(t, 10_000_001, created.AmountDue.Int64())

	inUse, err := s.StaticAmountInUse(ctx, "solana", "usdt-solana", sw.Address, big.NewInt(10_000_000))
	require.NoError(t, err)
	assert.True(t, inUse)

	// Closing a quote frees its amount.
	_, err = s.SetQuoteStatus(ctx, q1.ID, domain.QuoteClosed)
	require.NoError(t, err)
	inUse, err = s.StaticAmountInUse(ctx, "solana", "usdt-solana", sw.Address, big.NewInt(10_000_000))
	require.NoError(t, err)
	assert.False(t, inUse)
	inv3 := storetest.Invoice(t, s, m.ID)
	_, err = s.CreateQuote(ctx, newQ(inv3, 10_000_000))
	require.NoError(t, err)

	// xpub wallets do not have the amount constraint.
	xw := storetest.Wallet(t, s, domain.FamilyTron, domain.WalletXpub)
	storetest.Quote(t, s, inv1, "usdt-tron", xw, func(q *domain.Quote) { q.Address = "TSame"; q.AmountDue = big.NewInt(5) })
	storetest.Quote(t, s, inv2, "usdt-tron", xw, func(q *domain.Quote) { q.Address = "TSame"; q.AmountDue = big.NewInt(5) })
}

func TestQuoteQueries(t *testing.T) {
	t.Parallel()
	clk := storetest.FixedClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	s := storetest.NewSeeded(t, store.WithClock(clk.Now))
	m := storetest.Merchant(t, s)
	ew := storetest.Wallet(t, s, domain.FamilyEVM, domain.WalletXpub)
	tw := storetest.Wallet(t, s, domain.FamilyTron, domain.WalletXpub)
	inv := storetest.Invoice(t, s, m.ID)

	_, _, err := s.FindInvoiceAddress(ctx, inv.ID, ew.ID)
	require.ErrorIs(t, err, store.ErrNotFound)

	qEth := storetest.Quote(t, s, inv, "usdt-ethereum", ew)
	clk.Advance(time.Second)
	addr, idx, err := s.FindInvoiceAddress(ctx, inv.ID, ew.ID)
	require.NoError(t, err)
	assert.Equal(t, qEth.Address, addr)
	require.NotNil(t, idx)
	assert.Equal(t, *qEth.AddressIndex, *idx)

	// Customer switches to Polygon: same wallet → same address/index.
	n, err := s.SupersedeActiveQuotes(ctx, inv.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
	qPol := storetest.Quote(t, s, inv, "usdt-polygon", ew, func(q *domain.Quote) { q.Address = addr; q.AddressIndex = idx })
	clk.Advance(time.Second)
	_, _ = s.SupersedeActiveQuotes(ctx, inv.ID)
	qTrx := storetest.Quote(t, s, inv, "trx-tron", tw)

	list, err := s.ListQuotesByInvoice(ctx, inv.ID)
	require.NoError(t, err)
	require.Len(t, list, 3)
	assert.Equal(t, []uuid.UUID{qEth.ID, qPol.ID, qTrx.ID}, []uuid.UUID{list[0].ID, list[1].ID, list[2].ID})
	assert.Equal(t, domain.QuoteSuperseded, list[0].Status)
	assert.Equal(t, domain.QuoteActive, list[2].Status)

	act, err := s.GetActiveQuote(ctx, inv.ID)
	require.NoError(t, err)
	assert.Equal(t, qTrx.ID, act.ID)
	live, err := s.GetLiveQuote(ctx, inv.ID, "usdt-polygon")
	require.NoError(t, err)
	assert.Equal(t, qPol.ID, live.ID)
	_, err = s.GetLiveQuote(ctx, inv.ID, "btc-bitcoin")
	require.ErrorIs(t, err, store.ErrNotFound)

	// Static wallet: FindInvoiceAddress returns a nil index.
	sw := storetest.Wallet(t, s, domain.FamilyUTXO, domain.WalletStatic)
	storetest.Quote(t, s, inv, "btc-bitcoin", sw)
	addr, idx, err = s.FindInvoiceAddress(ctx, inv.ID, sw.ID)
	require.NoError(t, err)
	assert.Equal(t, sw.Address, addr)
	assert.Nil(t, idx)

	// Close all live quotes except one.
	n, err = s.CloseLiveQuotes(ctx, inv.ID, qTrx.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 3, n)
	list, _ = s.ListQuotesByInvoice(ctx, inv.ID)
	for _, q := range list {
		if q.ID == qTrx.ID {
			assert.Equal(t, domain.QuoteActive, q.Status)
		} else {
			assert.Equal(t, domain.QuoteClosed, q.Status)
		}
	}

	_, err = s.SetQuoteStatus(ctx, qTrx.ID, "bogus")
	require.ErrorIs(t, err, store.ErrInvalid)
	_, err = s.SetQuoteStatus(ctx, uuid.New(), domain.QuotePaid)
	require.ErrorIs(t, err, store.ErrNotFound)

	// Amounts.
	up, err := s.UpdateQuoteAmounts(ctx, qTrx.ID, pow2(200), big.NewInt(7))
	require.NoError(t, err)
	assert.Equal(t, 0, pow2(200).Cmp(up.AmountReceived))
	assert.EqualValues(t, 7, up.AmountConfirmed.Int64())
	_, err = s.UpdateQuoteAmounts(ctx, qTrx.ID, big.NewInt(-1), nil)
	require.ErrorIs(t, err, store.ErrInvalid)

	ext, err := s.ExtendQuoteWatch(ctx, qTrx.ID, clk.Now().Add(48*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, clk.Now().Add(48*time.Hour), ext.WatchUntil)

	// GetQuoteForUpdate inside a transaction.
	err = s.WithTx(ctx, func(tx *store.Store) error {
		q, err := tx.GetQuoteForUpdate(ctx, qTrx.ID)
		assert.Equal(t, qTrx.ID, q.ID)
		return err
	})
	require.NoError(t, err)
}

func TestWatchedQuotesAndExpiry(t *testing.T) {
	t.Parallel()
	clk := storetest.FixedClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	s := storetest.NewSeeded(t, store.WithClock(clk.Now))
	m := storetest.Merchant(t, s)
	tw := storetest.Wallet(t, s, domain.FamilyTron, domain.WalletXpub)
	now := clk.Now()

	inv1 := storetest.Invoice(t, s, m.ID)
	inv2 := storetest.Invoice(t, s, m.ID)
	inv3 := storetest.Invoice(t, s, m.ID)
	a := storetest.Quote(t, s, inv1, "usdt-tron", tw, func(q *domain.Quote) { q.WatchUntil = now.Add(time.Hour) })
	clk.Advance(time.Millisecond) // deterministic created_at order
	b := storetest.Quote(t, s, inv1, "trx-tron", tw, func(q *domain.Quote) {
		q.Status = domain.QuoteSuperseded
		q.WatchUntil = now.Add(2 * time.Hour)
	})
	c := storetest.Quote(t, s, inv2, "usdt-tron", tw, func(q *domain.Quote) { q.WatchUntil = now.Add(-time.Minute) }) // window over
	storetest.Quote(t, s, inv3, "usdt-tron", tw, func(q *domain.Quote) { q.Status = domain.QuotePaid; q.WatchUntil = now.Add(time.Hour) })
	other := storetest.Quote(t, s, inv3, "usdt-ethereum", storetest.Wallet(t, s, domain.FamilyEVM, domain.WalletXpub))

	w, err := s.ListWatched(ctx, "tron", now)
	require.NoError(t, err)
	require.Len(t, w, 2)
	assert.Equal(t, a.ID, w[0].ID)
	assert.Equal(t, "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t", w[0].TokenContract)
	assert.Equal(t, 6, w[0].Decimals)
	assert.Equal(t, b.ID, w[1].ID)
	assert.Equal(t, "", w[1].TokenContract, "native TRX")
	assert.Equal(t, domain.QuoteSuperseded, w[1].Status)

	byAddr, err := s.ListWatchedByAddress(ctx, "tron", a.Address, now)
	require.NoError(t, err)
	require.Len(t, byAddr, 1)
	assert.Equal(t, a.ID, byAddr[0].ID)
	none, err := s.ListWatchedByAddress(ctx, "tron", "Tnobody", now)
	require.NoError(t, err)
	assert.Empty(t, none)
	assert.NotNil(t, none)

	eth, err := s.ListWatched(ctx, "ethereum", now)
	require.NoError(t, err)
	require.Len(t, eth, 1)
	assert.Equal(t, other.ID, eth[0].ID)

	// Close expired: only c (and later a) are closed.
	n, err := s.CloseExpiredQuotes(ctx, now)
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
	got, _ := s.GetQuote(ctx, c.ID)
	assert.Equal(t, domain.QuoteClosed, got.Status)

	n, err = s.CloseExpiredQuotes(ctx, now.Add(time.Hour))
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
	w, err = s.ListWatched(ctx, "tron", now.Add(time.Hour))
	require.NoError(t, err)
	require.Len(t, w, 1)
	assert.Equal(t, b.ID, w[0].ID)
}
