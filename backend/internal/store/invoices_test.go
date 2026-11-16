package store_test

import (
	"encoding/json"
	"strings"
	"sync"
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

func TestCreateAndGetInvoice(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	m := storetest.Merchant(t, s)

	// numeric(38,18) round-trips exactly: 20 integer digits + 18 decimals.
	price := decimal.RequireFromString("12345678901234567890.123456789012345678")
	usd := decimal.RequireFromString("0.000000000000000001")
	exp := time.Date(2032, 2, 3, 4, 5, 6, 789000, time.UTC)
	in := domain.Invoice{
		MerchantID: m.ID, MerchantOrderID: "ORD-1", Title: "Title ✓", Description: "Desc",
		PriceAmount: price, PriceCurrency: "EUR", AmountUSD: &usd,
		SuccessURL: "https://s", CancelURL: "https://c", CustomerEmail: "c@example.com",
		Metadata:  json.RawMessage(`{"cart": [1, 2], "note": "x"}`),
		ExpiresAt: exp,
	}
	inv, err := s.CreateInvoice(ctx, in)
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, inv.ID)
	assert.Equal(t, domain.InvoicePending, inv.Status)
	assert.True(t, price.Equal(inv.PriceAmount), inv.PriceAmount.String())
	assert.Equal(t, "12345678901234567890.123456789012345678", inv.PriceAmount.String())
	require.NotNil(t, inv.AmountUSD)
	assert.True(t, usd.Equal(*inv.AmountUSD))
	assert.Equal(t, exp, inv.ExpiresAt)
	assert.JSONEq(t, `{"cart":[1,2],"note":"x"}`, string(inv.Metadata))
	assert.Nil(t, inv.PaidAt)
	assert.Nil(t, inv.PaidQuoteID)

	got, err := s.GetInvoice(ctx, inv.ID)
	require.NoError(t, err)
	assert.Equal(t, inv, got)
	got, err = s.GetInvoiceByOrderID(ctx, m.ID, "ORD-1")
	require.NoError(t, err)
	assert.Equal(t, inv.ID, got.ID)
	_, err = s.GetInvoiceByOrderID(ctx, m.ID, "")
	require.ErrorIs(t, err, store.ErrNotFound)
	_, err = s.GetInvoiceByOrderID(ctx, uuid.New(), "ORD-1")
	require.ErrorIs(t, err, store.ErrNotFound)
	_, err = s.GetInvoice(ctx, uuid.New())
	require.ErrorIs(t, err, store.ErrNotFound)

	// Defaults: nil metadata → {}, nil AmountUSD stays NULL.
	inv2, err := s.CreateInvoice(ctx, domain.Invoice{MerchantID: m.ID, PriceAmount: decimal.NewFromInt(1),
		PriceCurrency: "USDT", ExpiresAt: exp})
	require.NoError(t, err)
	assert.JSONEq(t, `{}`, string(inv2.Metadata))
	assert.Nil(t, inv2.AmountUSD)

	// Empty order ids do not collide.
	_, err = s.CreateInvoice(ctx, domain.Invoice{MerchantID: m.ID, PriceAmount: decimal.NewFromInt(1), PriceCurrency: "USD", ExpiresAt: exp})
	require.NoError(t, err)

	// Validation / constraints.
	_, err = s.CreateInvoice(ctx, domain.Invoice{MerchantID: m.ID, PriceAmount: decimal.Zero, PriceCurrency: "USD", ExpiresAt: exp})
	require.ErrorIs(t, err, store.ErrInvalid)
	_, err = s.CreateInvoice(ctx, domain.Invoice{MerchantID: m.ID, PriceAmount: decimal.NewFromInt(1), PriceCurrency: "USD", ExpiresAt: exp,
		Metadata: json.RawMessage(`{broken`)})
	require.ErrorIs(t, err, store.ErrInvalid)
	_, err = s.CreateInvoice(ctx, domain.Invoice{MerchantID: uuid.New(), PriceAmount: decimal.NewFromInt(1), PriceCurrency: "USD", ExpiresAt: exp})
	require.ErrorIs(t, err, store.ErrForeignKey)
	_, err = s.CreateInvoice(ctx, domain.Invoice{MerchantID: m.ID, PriceAmount: decimal.NewFromInt(1), PriceCurrency: "USD"})
	require.ErrorIs(t, err, store.ErrInvalid)
	_, err = s.CreateInvoice(ctx, domain.Invoice{MerchantID: m.ID, PriceAmount: decimal.RequireFromString("1e21"), PriceCurrency: "USD", ExpiresAt: exp})
	require.ErrorIs(t, err, store.ErrInvalid, "numeric overflow")
}

func TestInvoiceOrderIDIdempotency(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	m := storetest.Merchant(t, s)
	other := storetest.Merchant(t, s)
	first := storetest.Invoice(t, s, m.ID, func(i *domain.Invoice) { i.MerchantOrderID = "A-1" })

	_, err := s.CreateInvoice(ctx, domain.Invoice{MerchantID: m.ID, MerchantOrderID: "A-1", PriceAmount: decimal.NewFromInt(5),
		PriceCurrency: "USD", ExpiresAt: time.Now().Add(time.Hour)})
	require.ErrorIs(t, err, store.ErrConflict)
	assert.Equal(t, store.ConstraintInvoiceMerchantOrder, store.ConstraintName(err))

	// Same order id for another merchant is fine.
	storetest.Invoice(t, s, other.ID, func(i *domain.Invoice) { i.MerchantOrderID = "A-1" })

	// Inside a transaction the conflict does not abort it: the caller can
	// load the existing invoice.
	err = s.WithTx(ctx, func(tx *store.Store) error {
		_, err := tx.CreateInvoice(ctx, domain.Invoice{MerchantID: m.ID, MerchantOrderID: "A-1",
			PriceAmount: decimal.NewFromInt(10), PriceCurrency: "USD", ExpiresAt: time.Now().Add(time.Hour)})
		require.ErrorIs(t, err, store.ErrConflict)
		existing, err := tx.GetInvoiceByOrderID(ctx, m.ID, "A-1")
		require.NoError(t, err)
		assert.Equal(t, first.ID, existing.ID)
		return nil
	})
	require.NoError(t, err)

	// Concurrent creation with the same order id: exactly one wins.
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins, conflicts := 0, 0
	for range 8 {
		wg.Go(func() {
			_, err := s.CreateInvoice(ctx, domain.Invoice{MerchantID: m.ID, MerchantOrderID: "RACE",
				PriceAmount: decimal.NewFromInt(1), PriceCurrency: "USD", ExpiresAt: time.Now().Add(time.Hour)})
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				wins++
			} else if assert.ErrorIs(t, err, store.ErrConflict) {
				conflicts++
			}
		})
	}
	wg.Wait()
	assert.Equal(t, 1, wins)
	assert.Equal(t, 7, conflicts)
}

func TestUpdateInvoiceStateAndLock(t *testing.T) {
	t.Parallel()
	clk := storetest.FixedClock(time.Date(2030, 3, 1, 10, 0, 0, 0, time.UTC))
	s := storetest.NewSeeded(t, store.WithClock(clk.Now))
	m := storetest.Merchant(t, s)
	w := storetest.Wallet(t, s, domain.FamilyTron, domain.WalletXpub)
	inv := storetest.Invoice(t, s, m.ID)
	q := storetest.Quote(t, s, inv, "usdt-tron", w)

	clk.Advance(time.Minute)
	paidAt := clk.Now()
	err := s.WithTx(ctx, func(tx *store.Store) error {
		cur, err := tx.GetInvoiceForUpdate(ctx, inv.ID)
		require.NoError(t, err)
		st := store.StateOf(cur)
		st.Status, st.Overpaid, st.PaidLate, st.PaidQuoteID, st.PaidAt = domain.InvoicePaid, true, true, &q.ID, &paidAt
		up, err := tx.UpdateInvoiceState(ctx, inv.ID, st)
		require.NoError(t, err)
		assert.Equal(t, domain.InvoicePaid, up.Status)
		assert.True(t, up.Overpaid)
		assert.True(t, up.PaidLate)
		assert.Equal(t, q.ID, *up.PaidQuoteID)
		assert.Equal(t, paidAt, *up.PaidAt)
		assert.Equal(t, paidAt, up.UpdatedAt)
		assert.Equal(t, inv.CreatedAt, up.CreatedAt)
		return nil
	})
	require.NoError(t, err)

	_, err = s.UpdateInvoiceState(ctx, inv.ID, store.InvoiceState{Status: "bogus"})
	require.ErrorIs(t, err, store.ErrInvalid)
	_, err = s.UpdateInvoiceState(ctx, uuid.New(), store.InvoiceState{Status: domain.InvoicePending})
	require.ErrorIs(t, err, store.ErrNotFound)
	err = s.WithTx(ctx, func(tx *store.Store) error {
		_, err := tx.GetInvoiceForUpdate(ctx, uuid.New())
		return err
	})
	require.ErrorIs(t, err, store.ErrNotFound)

	// GetInvoiceForUpdate really locks: a second locker waits for commit.
	inv2 := storetest.Invoice(t, s, m.ID)
	locked := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_ = s.WithTx(ctx, func(tx *store.Store) error {
			_, err := tx.GetInvoiceForUpdate(ctx, inv2.ID)
			require.NoError(t, err)
			close(locked)
			<-release
			_, err = tx.UpdateInvoiceState(ctx, inv2.ID, store.InvoiceState{Status: domain.InvoiceCancelled})
			return err
		})
	}()
	<-locked
	got := make(chan domain.InvoiceStatus, 1)
	go func() {
		_ = s.WithTx(ctx, func(tx *store.Store) error {
			cur, err := tx.GetInvoiceForUpdate(ctx, inv2.ID)
			require.NoError(t, err)
			got <- cur.Status
			return nil
		})
	}()
	select {
	case <-got:
		t.Fatal("second GetInvoiceForUpdate did not wait for the lock")
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	select {
	case st := <-got:
		assert.Equal(t, domain.InvoiceCancelled, st, "sees the committed state")
	case <-time.After(5 * time.Second):
		t.Fatal("timeout")
	}
}

func TestInvoiceViewAndCurrentQuote(t *testing.T) {
	t.Parallel()
	clk := storetest.FixedClock(time.Date(2030, 3, 1, 10, 0, 0, 0, time.UTC))
	s := storetest.NewSeeded(t, store.WithClock(clk.Now))
	m := storetest.Merchant(t, s, func(m *domain.Merchant) { m.Name = "Acme" })
	tw := storetest.Wallet(t, s, domain.FamilyTron, domain.WalletXpub)
	inv := storetest.Invoice(t, s, m.ID)

	v, err := s.GetInvoiceView(ctx, inv.ID)
	require.NoError(t, err)
	assert.Equal(t, "Acme", v.MerchantName)
	assert.Nil(t, v.Quote)
	assert.Equal(t, inv, v.Invoice)
	_, err = s.GetCurrentQuote(ctx, inv.ID)
	require.ErrorIs(t, err, store.ErrNotFound)
	_, err = s.GetInvoiceView(ctx, uuid.New())
	require.ErrorIs(t, err, store.ErrNotFound)

	q1 := storetest.Quote(t, s, inv, "usdt-tron", tw)
	clk.Advance(time.Second)
	_, err = s.SupersedeActiveQuotes(ctx, inv.ID)
	require.NoError(t, err)
	q2 := storetest.Quote(t, s, inv, "trx-tron", tw)
	v, err = s.GetInvoiceView(ctx, inv.ID)
	require.NoError(t, err)
	require.NotNil(t, v.Quote)
	assert.Equal(t, q2.ID, v.Quote.ID, "newest active quote")

	// The paid quote wins even if superseded / older.
	_, err = s.UpdateInvoiceState(ctx, inv.ID, store.InvoiceState{Status: domain.InvoicePaid, PaidQuoteID: &q1.ID, PaidAt: storetest.Ptr(clk.Now())})
	require.NoError(t, err)
	cq, err := s.GetCurrentQuote(ctx, inv.ID)
	require.NoError(t, err)
	assert.Equal(t, q1.ID, cq.ID)

	// No active quote, no paid quote → newest of any status.
	inv2 := storetest.Invoice(t, s, m.ID)
	a := storetest.Quote(t, s, inv2, "usdt-tron", tw)
	clk.Advance(time.Second)
	b := storetest.Quote(t, s, inv2, "trx-tron", tw, func(q *domain.Quote) { q.Status = domain.QuoteSuperseded })
	_, err = s.SetQuoteStatus(ctx, a.ID, domain.QuoteClosed)
	require.NoError(t, err)
	cq, err = s.GetCurrentQuote(ctx, inv2.ID)
	require.NoError(t, err)
	assert.Equal(t, b.ID, cq.ID)

	cur, err := s.CurrentQuotes(ctx, []uuid.UUID{inv.ID, inv2.ID, uuid.New()})
	require.NoError(t, err)
	require.Len(t, cur, 2)
	assert.Equal(t, q1.ID, cur[inv.ID].ID)
	assert.Equal(t, b.ID, cur[inv2.ID].ID)
	empty, err := s.CurrentQuotes(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, empty)
}

func TestListInvoices(t *testing.T) {
	t.Parallel()
	clk := storetest.FixedClock(time.Date(2030, 4, 1, 0, 0, 0, 0, time.UTC))
	s := storetest.NewSeeded(t, store.WithClock(clk.Now))
	m1 := storetest.Merchant(t, s, func(m *domain.Merchant) { m.Name = "M1" })
	m2 := storetest.Merchant(t, s, func(m *domain.Merchant) { m.Name = "M2" })
	ew := storetest.Wallet(t, s, domain.FamilyEVM, domain.WalletStatic)

	var invs []domain.Invoice
	statuses := []domain.InvoiceStatus{domain.InvoicePending, domain.InvoicePaid, domain.InvoiceExpired, domain.InvoicePending, domain.InvoiceCancelled}
	for i, st := range statuses {
		mid := m1.ID
		if i%2 == 1 {
			mid = m2.ID
		}
		invs = append(invs, storetest.Invoice(t, s, mid, func(inv *domain.Invoice) {
			inv.Status = st
			inv.MerchantOrderID = "order-" + string(rune('a'+i))
		}))
		clk.Advance(time.Hour)
	}
	// invs[0]: quote on the static EVM address (mixed-case canonical) + a transfer.
	q := storetest.Quote(t, s, invs[0], "usdc-base", ew)
	tr := storetest.TransferFor(t, s, q, 5, func(x *domain.Transfer) { x.TxHash = "0xABCDEF0123" })

	list := func(f store.InvoiceFilter) store.Page[store.InvoiceView] {
		t.Helper()
		p, err := s.ListInvoices(ctx, f)
		require.NoError(t, err)
		return p
	}
	ids := func(p store.Page[store.InvoiceView]) []uuid.UUID {
		out := make([]uuid.UUID, len(p.Items))
		for i, v := range p.Items {
			out[i] = v.ID
		}
		return out
	}

	all := list(store.InvoiceFilter{})
	assert.Equal(t, 5, all.Total)
	assert.Equal(t, []uuid.UUID{invs[4].ID, invs[3].ID, invs[2].ID, invs[1].ID, invs[0].ID}, ids(all))
	assert.Equal(t, "M1", all.Items[0].MerchantName)
	assert.Equal(t, "M2", all.Items[1].MerchantName)
	require.NotNil(t, all.Items[4].Quote)
	assert.Equal(t, q.ID, all.Items[4].Quote.ID)
	assert.Nil(t, all.Items[0].Quote)

	assert.Equal(t, []uuid.UUID{invs[3].ID, invs[1].ID}, ids(list(store.InvoiceFilter{MerchantID: &m2.ID})))
	assert.Equal(t, []uuid.UUID{invs[3].ID, invs[0].ID}, ids(list(store.InvoiceFilter{Statuses: []domain.InvoiceStatus{domain.InvoicePending}})))
	assert.Equal(t, 3, list(store.InvoiceFilter{Statuses: []domain.InvoiceStatus{domain.InvoicePending, domain.InvoicePaid}}).Total)
	assert.Equal(t, []uuid.UUID{invs[2].ID}, ids(list(store.InvoiceFilter{OrderID: "order-c"})))
	assert.Empty(t, ids(list(store.InvoiceFilter{OrderID: "order-c", MerchantID: &m2.ID})))

	// q: invoice id, order id, quote address (any case), tx hash (any case).
	assert.Equal(t, []uuid.UUID{invs[2].ID}, ids(list(store.InvoiceFilter{Q: invs[2].ID.String()})))
	assert.Equal(t, []uuid.UUID{invs[1].ID}, ids(list(store.InvoiceFilter{Q: " order-b "})))
	assert.Equal(t, []uuid.UUID{invs[0].ID}, ids(list(store.InvoiceFilter{Q: strings.ToLower(ew.Address)})))
	assert.Equal(t, []uuid.UUID{invs[0].ID}, ids(list(store.InvoiceFilter{Q: "0xabcdef0123"})))
	assert.Equal(t, []uuid.UUID{invs[0].ID}, ids(list(store.InvoiceFilter{Q: tr.TxHash, MerchantID: &m1.ID})))
	assert.Empty(t, ids(list(store.InvoiceFilter{Q: tr.TxHash, MerchantID: &m2.ID})))
	assert.Empty(t, ids(list(store.InvoiceFilter{Q: "nothing-matches' OR 1=1 --"})))

	// Time range [From, To).
	from := invs[1].CreatedAt
	to := invs[3].CreatedAt
	assert.Equal(t, []uuid.UUID{invs[2].ID, invs[1].ID}, ids(list(store.InvoiceFilter{From: from, To: to})))

	// Paging.
	p := list(store.InvoiceFilter{Page: 2, PageSize: 2})
	assert.Equal(t, 5, p.Total)
	assert.Equal(t, []uuid.UUID{invs[2].ID, invs[1].ID}, ids(p))
	p = list(store.InvoiceFilter{Page: 9, PageSize: 2})
	assert.Equal(t, 5, p.Total)
	assert.Empty(t, p.Items)
	assert.NotNil(t, p.Items)

	_, err := s.ListInvoices(ctx, store.InvoiceFilter{Statuses: []domain.InvoiceStatus{"nope"}})
	require.ErrorIs(t, err, store.ErrInvalid)
}

func TestListInvoicesToExpire(t *testing.T) {
	t.Parallel()
	clk := storetest.FixedClock(time.Date(2030, 5, 1, 12, 0, 0, 0, time.UTC))
	s := storetest.New(t, store.WithClock(clk.Now))
	m := storetest.Merchant(t, s)
	now := clk.Now()
	mk := func(st domain.InvoiceStatus, exp time.Duration) domain.Invoice {
		return storetest.Invoice(t, s, m.ID, func(i *domain.Invoice) { i.Status = st; i.ExpiresAt = now.Add(exp) })
	}
	p1 := mk(domain.InvoicePending, -2*time.Minute)
	p2 := mk(domain.InvoicePartial, -time.Minute)
	mk(domain.InvoicePending, time.Minute)        // not yet
	c := mk(domain.InvoiceConfirming, -time.Hour) // not by default
	mk(domain.InvoicePaid, -time.Hour)
	mk(domain.InvoiceExpired, -time.Hour)
	p3 := mk(domain.InvoicePending, 0) // expires exactly now

	due, err := s.ListInvoicesToExpire(ctx, now, 10)
	require.NoError(t, err)
	var got []uuid.UUID
	for _, i := range due {
		got = append(got, i.ID)
	}
	assert.Equal(t, []uuid.UUID{p1.ID, p2.ID, p3.ID}, got)

	due, err = s.ListInvoicesToExpire(ctx, now, 2)
	require.NoError(t, err)
	assert.Len(t, due, 2)

	due, err = s.ListInvoicesToExpire(ctx, now, 10, domain.InvoiceConfirming)
	require.NoError(t, err)
	require.Len(t, due, 1)
	assert.Equal(t, c.ID, due[0].ID)

	_, err = s.ListInvoicesToExpire(ctx, now, 10, "nope")
	require.ErrorIs(t, err, store.ErrInvalid)

	// Concurrent workers inside transactions get disjoint batches.
	locked := make(chan []domain.Invoice)
	release := make(chan struct{})
	go func() {
		_ = s.WithTx(ctx, func(tx *store.Store) error {
			b, err := tx.ListInvoicesToExpire(ctx, now, 2)
			require.NoError(t, err)
			locked <- b
			<-release
			return nil
		})
	}()
	first := <-locked
	var second []domain.Invoice
	err = s.WithTx(ctx, func(tx *store.Store) error {
		var err error
		second, err = tx.ListInvoicesToExpire(ctx, now, 10)
		return err
	})
	close(release)
	require.NoError(t, err)
	require.Len(t, first, 2)
	require.Len(t, second, 1)
	assert.Equal(t, p3.ID, second[0].ID)
}

func TestListInvoicesToExpireAfter(t *testing.T) {
	t.Parallel()
	clk := storetest.FixedClock(time.Date(2030, 5, 1, 12, 0, 0, 0, time.UTC))
	s := storetest.New(t, store.WithClock(clk.Now))
	m := storetest.Merchant(t, s)
	now := clk.Now()
	mk := func(st domain.InvoiceStatus, exp time.Duration) domain.Invoice {
		return storetest.Invoice(t, s, m.ID, func(i *domain.Invoice) { i.Status = st; i.ExpiresAt = now.Add(exp) })
	}
	// Two invoices share an expiry: the id breaks the tie.
	a := mk(domain.InvoicePartial, -3*time.Minute)
	b1 := mk(domain.InvoicePartial, -2*time.Minute)
	b2 := mk(domain.InvoicePartial, -2*time.Minute)
	c := mk(domain.InvoicePending, -time.Minute)
	mk(domain.InvoicePartial, time.Minute) // not yet due
	if b2.ID.String() < b1.ID.String() {
		b1, b2 = b2, b1
	}
	ids := func(invs []domain.Invoice) []uuid.UUID {
		out := []uuid.UUID{}
		for _, i := range invs {
			out = append(out, i.ID)
		}
		return out
	}

	// Zero cursor = from the beginning (same as ListInvoicesToExpire).
	all, err := s.ListInvoicesToExpireAfter(ctx, now, time.Time{}, uuid.Nil, 10)
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{a.ID, b1.ID, b2.ID, c.ID}, ids(all))

	// Paging with the last row of each page visits every row exactly once.
	var seen []uuid.UUID
	var cur domain.Invoice
	for range 10 {
		page, err := s.ListInvoicesToExpireAfter(ctx, now, cur.ExpiresAt, cur.ID, 1)
		require.NoError(t, err)
		if len(page) == 0 {
			break
		}
		seen = append(seen, page[0].ID)
		cur = page[0]
	}
	assert.Equal(t, []uuid.UUID{a.ID, b1.ID, b2.ID, c.ID}, seen)

	// Status filter and validation.
	page, err := s.ListInvoicesToExpireAfter(ctx, now, a.ExpiresAt, a.ID, 10, domain.InvoicePartial)
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{b1.ID, b2.ID}, ids(page))
	_, err = s.ListInvoicesToExpireAfter(ctx, now, a.ExpiresAt, a.ID, 10, "nope")
	require.ErrorIs(t, err, store.ErrInvalid)
}
