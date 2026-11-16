package invoice_test

import (
	"encoding/json"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cryptopay/internal/domain"
	"cryptopay/internal/invoice"
	"cryptopay/internal/invoice/invoicetest"
	"cryptopay/internal/store/storetest"
)

func expire(t *testing.T, e *invoicetest.Env) invoice.ExpireResult {
	t.Helper()
	res, err := e.Invoices.ExpireDue(e.Ctx)
	require.NoError(t, err)
	e.Step()
	return res
}

// ---- expiry ----

func TestExpireNoFunds(t *testing.T) {
	t.Parallel()
	e := invoicetest.New(t)
	bare := e.Create("10", "USD") // no asset selected
	v, q := e.Quoted("10", "USD", "usdt-tron")
	later := e.Create("10", "USD", func(p *invoice.CreateParams) { p.ExpiresIn = ptr(3600) })

	// One microsecond before expiry nothing happens.
	require.True(t, bare.ExpiresAt.Before(v.ExpiresAt))
	e.Clock.Set(bare.ExpiresAt.Add(-time.Microsecond))
	res := expire(t, e)
	assert.Zero(t, res.Changed)
	assert.Equal(t, domain.InvoicePending, e.Invoice(bare.ID).Status)

	e.Clock.Set(v.ExpiresAt)
	res = expire(t, e)
	assert.Equal(t, 2, res.Changed)
	assert.Equal(t, 2, res.Examined)
	for _, id := range []uuid.UUID{bare.ID, v.ID} {
		assert.Equal(t, domain.InvoiceExpired, e.Invoice(id).Status)
		checkEvents(t, e, id, domain.EventExpired)
	}
	assert.Equal(t, domain.InvoicePending, e.Invoice(later.ID).Status)
	// The quote stays watched for the grace period.
	assert.Equal(t, domain.QuoteActive, e.Quote(q.ID).Status)
	watched, err := e.Store.ListWatched(e.Ctx, "tron", e.Clock.Now())
	require.NoError(t, err)
	require.Len(t, watched, 1)

	// Running again (or in a second instance) changes nothing.
	res = expire(t, e)
	assert.Zero(t, res.Changed)
	assert.Zero(t, res.Examined)
	checkEvents(t, e, v.ID, domain.EventExpired)

	// The customer cannot pick an asset any more.
	_, err = e.Invoices.SelectAsset(e.Ctx, v.ID, "usdt-tron")
	assert.ErrorIs(t, err, invoice.ErrNotOpen)
}

func TestExpireConfirmedPartialIsUnderpaid(t *testing.T) {
	t.Parallel()
	e := invoicetest.New(t)
	v, q := e.Quoted("10", "USD", "usdt-ethereum")
	e.Credit(q, 4_000_000, domain.TransferConfirmed)
	e.Clock.Set(v.ExpiresAt)
	res := expire(t, e)
	assert.Equal(t, 1, res.Changed)
	inv := e.Invoice(v.ID)
	assert.Equal(t, domain.InvoiceUnderpaid, inv.Status)
	assert.Nil(t, inv.PaidAt)
	envs := checkEvents(t, e, v.ID, domain.EventPaymentDetected, domain.EventUnderpaid)
	assert.Equal(t, "4", envs[1].Data.Quote.AmountConfirmed)
	assert.Equal(t, "6", envs[1].Data.Quote.AmountRemaining)
}

func TestExpireWaitsForUnconfirmedFunds(t *testing.T) {
	t.Parallel()
	e := invoicetest.New(t)
	// Partial, unconfirmed at expiry: stays partial until the funds confirm …
	v1, q1 := e.Quoted("10", "USD", "usdt-ethereum")
	t1 := e.Credit(q1, 4_000_000, domain.TransferPending)
	// … or are orphaned.
	v2, q2 := e.Quoted("10", "USD", "usdt-ethereum")
	t2 := e.Credit(q2, 4_000_000, domain.TransferPending)
	// Full amount unconfirmed at expiry: confirming is not touched by the job.
	v3, q3 := e.Quoted("10", "USD", "usdt-ethereum")
	t3 := e.Credit(q3, 10_000_000, domain.TransferPending)

	e.Clock.Set(v3.ExpiresAt.Add(time.Minute))
	res := expire(t, e)
	assert.Zero(t, res.Changed)
	assert.Equal(t, domain.InvoicePartial, e.Invoice(v1.ID).Status)
	assert.Equal(t, domain.InvoicePartial, e.Invoice(v2.ID).Status)
	assert.Equal(t, domain.InvoiceConfirming, e.Invoice(v3.ID).Status)

	e.SetTransfer(t1, domain.TransferConfirmed)
	assert.Equal(t, domain.InvoiceUnderpaid, e.Invoice(v1.ID).Status)
	checkEvents(t, e, v1.ID, domain.EventPaymentDetected, domain.EventUnderpaid)

	e.SetTransfer(t2, domain.TransferOrphaned)
	assert.Equal(t, domain.InvoiceExpired, e.Invoice(v2.ID).Status)
	checkEvents(t, e, v2.ID, domain.EventPaymentDetected, domain.EventExpired)

	// Pending funds at expiry, then confirmed: paid, late.
	payTime := e.Clock.Now()
	e.SetTransfer(t3, domain.TransferConfirmed)
	inv := e.Invoice(v3.ID)
	assert.Equal(t, domain.InvoicePaid, inv.Status)
	assert.True(t, inv.PaidLate)
	assert.True(t, inv.PaidAt.Equal(payTime))
	envs := checkEvents(t, e, v3.ID, domain.EventConfirming, domain.EventPaid)
	assert.True(t, envs[1].Data.PaidLate)

	assert.Zero(t, expire(t, e).Changed)
}

func TestLatePaymentWithinGrace(t *testing.T) {
	t.Parallel()
	e := invoicetest.New(t)
	v, q := e.Quoted("10", "USD", "usdt-tron")
	e.Clock.Set(v.ExpiresAt.Add(time.Second))
	expire(t, e)
	require.Equal(t, domain.InvoiceExpired, e.Invoice(v.ID).Status)

	// Twelve hours later (grace is 24 h) the full amount arrives.
	e.Clock.Set(v.ExpiresAt.Add(12 * time.Hour))
	tr := e.Credit(q, q.AmountDue.Int64(), domain.TransferPending)
	assert.Equal(t, domain.InvoiceConfirming, e.Invoice(v.ID).Status)
	e.SetTransfer(tr, domain.TransferConfirmed)
	inv := e.Invoice(v.ID)
	assert.Equal(t, domain.InvoicePaid, inv.Status)
	assert.True(t, inv.PaidLate)
	checkEvents(t, e, v.ID, domain.EventExpired, domain.EventConfirming, domain.EventPaid)

	// A late partial payment on an expired invoice: partial while
	// unconfirmed, underpaid once confirmed.
	v2, q2 := e.Quoted("10", "USD", "usdt-tron")
	e.Clock.Set(v2.ExpiresAt)
	expire(t, e)
	tr2 := e.Credit(q2, 1_000_000, domain.TransferPending)
	assert.Equal(t, domain.InvoicePartial, e.Invoice(v2.ID).Status)
	e.SetTransfer(tr2, domain.TransferConfirmed)
	assert.Equal(t, domain.InvoiceUnderpaid, e.Invoice(v2.ID).Status)
	checkEvents(t, e, v2.ID, domain.EventExpired, domain.EventPaymentDetected, domain.EventUnderpaid)
}

func TestUnderpaidToppedUpInGrace(t *testing.T) {
	t.Parallel()
	e := invoicetest.New(t)
	v, q := e.Quoted("10", "USD", "usdt-ethereum")
	e.Credit(q, 7_000_000, domain.TransferConfirmed)
	e.Clock.Set(v.ExpiresAt)
	expire(t, e)
	require.Equal(t, domain.InvoiceUnderpaid, e.Invoice(v.ID).Status)
	e.Clock.Advance(time.Hour)
	e.Credit(e.Quote(q.ID), 3_000_000, domain.TransferConfirmed)
	inv := e.Invoice(v.ID)
	assert.Equal(t, domain.InvoicePaid, inv.Status)
	assert.True(t, inv.PaidLate)
	checkEvents(t, e, v.ID, domain.EventPaymentDetected, domain.EventUnderpaid, domain.EventPaid)
}

func TestWatchUntilClosesQuotes(t *testing.T) {
	t.Parallel()
	e := invoicetest.New(t)
	v, q := e.Quoted("10", "USD", "usdt-tron")
	b := e.Select(v.ID, "trx-tron") // q superseded, b active
	require.Equal(t, v.ExpiresAt.Add(24*time.Hour), q.WatchUntil)

	e.Clock.Set(q.WatchUntil.Add(-time.Second))
	res := expire(t, e)
	assert.Equal(t, 1, res.Changed) // expired
	assert.Zero(t, res.QuotesClosed)

	e.Clock.Set(q.WatchUntil)
	res = expire(t, e)
	assert.Equal(t, int64(2), res.QuotesClosed)
	assert.Equal(t, domain.QuoteClosed, e.Quote(q.ID).Status)
	assert.Equal(t, domain.QuoteClosed, e.Quote(b.ID).Status)
	watched, err := e.Store.ListWatched(e.Ctx, "tron", e.Clock.Now())
	require.NoError(t, err)
	assert.Empty(t, watched)
	assert.Equal(t, domain.InvoiceExpired, e.Invoice(v.ID).Status)
	checkEvents(t, e, v.ID, domain.EventExpired)

	// The static amount is free again for a new invoice.
	_, again := e.Quoted("10", "USD", "usdt-tron")
	assert.Equal(t, q.AmountDue.String(), again.AmountDue.String())
	assert.Zero(t, expire(t, e).QuotesClosed)
}

func TestExpireDueConcurrentInstances(t *testing.T) {
	t.Parallel()
	e := invoicetest.New(t)
	const n = 30
	ids := make([]uuid.UUID, 0, n)
	for i := range n {
		v := e.Create("10", "USD")
		if i%3 == 0 {
			q := e.Select(v.ID, "usdt-tron")
			e.Credit(q, 1_000_000, domain.TransferConfirmed) // → underpaid
		}
		ids = append(ids, v.ID)
	}
	e.Clock.Advance(time.Hour)
	// Several "instances" (services sharing the database) with small batches.
	var wg sync.WaitGroup
	var mu sync.Mutex
	total := 0
	for range 4 {
		svc := invoice.New(e.Store, e.Rates, e.Log, e.Clock.Now)
		svc.SetExpireBatch(4)
		wg.Go(func() {
			res, err := svc.ExpireDue(e.Ctx)
			assert.NoError(t, err)
			mu.Lock()
			total += res.Changed
			mu.Unlock()
		})
	}
	wg.Wait()
	assert.Equal(t, n, total, "every invoice changed exactly once across instances")
	for i, id := range ids {
		if i%3 == 0 {
			assert.Equal(t, domain.InvoiceUnderpaid, e.Invoice(id).Status)
			checkEvents(t, e, id, domain.EventPaymentDetected, domain.EventUnderpaid)
		} else {
			assert.Equal(t, domain.InvoiceExpired, e.Invoice(id).Status)
			checkEvents(t, e, id, domain.EventExpired)
		}
	}
}

func TestExpireDuePagesPastInvoicesThatStay(t *testing.T) {
	t.Parallel()
	e := invoicetest.New(t)
	e.Invoices.SetExpireBatch(2)
	// Five partial invoices with unconfirmed funds expire first; they must
	// not hide the two confirmed-partial ones expiring after them.
	var stuck, due []uuid.UUID
	for range 5 {
		v, q := e.Quoted("10", "USD", "usdt-ethereum")
		e.Credit(q, 1_000_000, domain.TransferPending)
		stuck = append(stuck, v.ID)
	}
	for range 2 {
		v, q := e.Quoted("10", "USD", "usdt-ethereum")
		e.Credit(q, 1_000_000, domain.TransferConfirmed)
		due = append(due, v.ID)
	}
	var pending []uuid.UUID
	for range 5 {
		pending = append(pending, e.Create("10", "USD").ID)
	}
	e.Clock.Advance(time.Hour)
	res := expire(t, e)
	assert.Equal(t, 7, res.Changed)
	assert.Equal(t, 12, res.Examined)
	for _, id := range stuck {
		assert.Equal(t, domain.InvoicePartial, e.Invoice(id).Status)
	}
	for _, id := range due {
		assert.Equal(t, domain.InvoiceUnderpaid, e.Invoice(id).Status)
	}
	for _, id := range pending {
		assert.Equal(t, domain.InvoiceExpired, e.Invoice(id).Status)
	}
}

// ---- cancel ----

func TestCancel(t *testing.T) {
	t.Parallel()
	e := invoicetest.New(t)
	v, q := e.Quoted("10", "USD", "usdt-tron")
	out, err := e.Invoices.Cancel(e.Ctx, v.ID, "merchant")
	require.NoError(t, err)
	e.Step()
	assert.Equal(t, domain.InvoiceCancelled, out.Status)
	assert.Equal(t, e.Merchant.Name, out.MerchantName)
	require.NotNil(t, out.Quote)
	checkEvents(t, e, v.ID, domain.EventCancelled)

	_, err = e.Invoices.Cancel(e.Ctx, v.ID, "merchant")
	assert.ErrorIs(t, err, invoice.ErrConflict, "already cancelled")
	_, err = e.Invoices.Cancel(e.Ctx, uuid.New(), "merchant")
	assert.ErrorIs(t, err, invoice.ErrNotFound)
	_, err = e.Invoices.SelectAsset(e.Ctx, v.ID, "usdt-tron")
	assert.ErrorIs(t, err, invoice.ErrNotOpen)

	// Funds arriving for a cancelled invoice are recorded, the status stays.
	tr := e.Credit(q, q.AmountDue.Int64(), domain.TransferPending)
	e.SetTransfer(tr, domain.TransferConfirmed)
	assert.Equal(t, domain.InvoiceCancelled, e.Invoice(v.ID).Status)
	assert.Equal(t, q.AmountDue.String(), e.Quote(q.ID).AmountConfirmed.String())
	d, err := e.Invoices.Get(e.Ctx, v.ID)
	require.NoError(t, err)
	assert.Len(t, d.Transfers, 1)
	checkEvents(t, e, v.ID, domain.EventCancelled)

	// Only pending invoices can be cancelled.
	for _, amount := range []int64{1_000_000, 10_000_000} {
		p, pq := e.Quoted("10", "USD", "usdt-ethereum")
		e.Credit(pq, amount, domain.TransferPending)
		_, err = e.Invoices.Cancel(e.Ctx, p.ID, "merchant")
		assert.ErrorIs(t, err, invoice.ErrConflict)
	}
	x := e.Create("10", "USD")
	e.Clock.Advance(time.Hour)
	expire(t, e)
	_, err = e.Invoices.Cancel(e.Ctx, x.ID, "merchant")
	assert.ErrorIs(t, err, invoice.ErrConflict, "expired")
}

// ---- mark paid ----

func TestMarkPaid(t *testing.T) {
	t.Parallel()
	e := invoicetest.New(t)

	// Underpaid invoice settled out of band, after expiry.
	v, q := e.Quoted("10", "USD", "usdt-ethereum")
	other := e.Select(v.ID, "usdt-polygon") // q superseded
	e.Credit(q, 9_000_000, domain.TransferConfirmed)
	e.Clock.Set(v.ExpiresAt)
	expire(t, e)
	require.Equal(t, domain.InvoiceUnderpaid, e.Invoice(v.ID).Status)
	now := e.Clock.Now()
	out, err := e.Invoices.MarkPaid(e.Ctx, v.ID, "customer paid the rest by bank transfer", "admin@example.com")
	require.NoError(t, err)
	e.Step()
	assert.Equal(t, domain.InvoicePaid, out.Status)
	assert.True(t, out.PaidLate)
	assert.True(t, out.PaidAt.Equal(now))
	require.NotNil(t, out.PaidQuoteID)
	assert.Equal(t, q.ID, *out.PaidQuoteID, "the quote that received the funds")
	require.NotNil(t, out.Quote)
	assert.Equal(t, q.ID, out.Quote.ID)
	assert.Equal(t, domain.QuotePaid, e.Quote(q.ID).Status)
	assert.Equal(t, domain.QuoteClosed, e.Quote(other.ID).Status)
	checkEvents(t, e, v.ID, domain.EventPaymentDetected, domain.EventUnderpaid, domain.EventPaid)

	_, err = e.Invoices.MarkPaid(e.Ctx, v.ID, "", "admin")
	assert.ErrorIs(t, err, invoice.ErrConflict, "already paid")
	_, err = e.Invoices.MarkPaid(e.Ctx, uuid.New(), "", "admin")
	assert.ErrorIs(t, err, invoice.ErrNotFound)

	// A pending invoice without quotes, before expiry.
	bare := e.Create("10", "USD")
	out, err = e.Invoices.MarkPaid(e.Ctx, bare.ID, "", "admin")
	require.NoError(t, err)
	assert.Equal(t, domain.InvoicePaid, out.Status)
	assert.False(t, out.PaidLate)
	assert.Nil(t, out.PaidQuoteID)
	assert.Nil(t, out.Quote)
	checkEvents(t, e, bare.ID, domain.EventPaid)

	// Confirming: marked paid, the later confirmation changes nothing.
	c, cq := e.Quoted("10", "USD", "usdt-ethereum")
	tr := e.Credit(cq, 10_000_000, domain.TransferPending)
	_, err = e.Invoices.MarkPaid(e.Ctx, c.ID, "", "admin")
	require.NoError(t, err)
	e.Step()
	e.SetTransfer(tr, domain.TransferConfirmed)
	assert.Equal(t, domain.InvoicePaid, e.Invoice(c.ID).Status)
	checkEvents(t, e, c.ID, domain.EventConfirming, domain.EventPaid)

	// Cancelled without funds: refused. Cancelled but the customer paid
	// anyway: the admin can resolve it as paid.
	x, xq := e.Quoted("10", "USD", "usdt-tron")
	_, err = e.Invoices.Cancel(e.Ctx, x.ID, "merchant")
	require.NoError(t, err)
	e.Step()
	_, err = e.Invoices.MarkPaid(e.Ctx, x.ID, "", "admin")
	assert.ErrorIs(t, err, invoice.ErrConflict)
	e.Credit(xq, xq.AmountDue.Int64(), domain.TransferConfirmed)
	require.Equal(t, domain.InvoiceCancelled, e.Invoice(x.ID).Status)
	out, err = e.Invoices.MarkPaid(e.Ctx, x.ID, "paid after cancelling", "admin")
	require.NoError(t, err)
	assert.Equal(t, domain.InvoicePaid, out.Status)
	assert.Equal(t, xq.ID, *out.PaidQuoteID)
	checkEvents(t, e, x.ID, domain.EventCancelled, domain.EventPaid)
}

// ---- manual transfer assignment ----

func TestAssignTransfer(t *testing.T) {
	t.Parallel()
	e := invoicetest.New(t)
	usdtTron, err := e.Store.GetAsset(e.Ctx, "usdt-tron")
	require.NoError(t, err)
	v, q := e.Quoted("10", "USD", "usdt-tron")
	// The customer sent a wrong amount to the static address: the scanner
	// stored it unmatched.
	unmatched := func(amount int64, mods ...func(*domain.Transfer)) domain.Transfer {
		return storetest.Transfer(t, e.Store, append([]func(*domain.Transfer){func(x *domain.Transfer) {
			x.ChainID, x.AssetID, x.TokenContract = "tron", "usdt-tron", usdtTron.ContractAddress
			x.ToAddress, x.Amount = e.TronWallet.Address, big.NewInt(amount)
		}}, mods...)...)
	}
	tr := unmatched(9_990_000, func(x *domain.Transfer) { x.Status = domain.TransferConfirmed })

	out, err := e.Invoices.AssignTransfer(e.Ctx, tr.ID, v.ID)
	require.NoError(t, err)
	e.Step()
	require.NotNil(t, out.QuoteID)
	assert.Equal(t, q.ID, *out.QuoteID)
	assert.Equal(t, v.ID, *out.InvoiceID)
	assert.Equal(t, "9990000", e.Quote(q.ID).AmountConfirmed.String())
	assert.Equal(t, domain.InvoicePaid, e.Invoice(v.ID).Status, "within tolerance")
	checkEvents(t, e, v.ID, domain.EventPaid)

	_, err = e.Invoices.AssignTransfer(e.Ctx, tr.ID, v.ID)
	assert.ErrorIs(t, err, invoice.ErrConflict, "already assigned")

	// A pending transfer counts as received until confirmed.
	v2, q2 := e.Quoted("10", "USD", "usdt-tron")
	p := unmatched(4_000_000)
	_, err = e.Invoices.AssignTransfer(e.Ctx, p.ID, v2.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.InvoicePartial, e.Invoice(v2.ID).Status)
	assert.Equal(t, "4000000", e.Quote(q2.ID).AmountReceived.String())

	// No quote for the transfer's asset / chain, unknown token, orphaned.
	trx := unmatched(1, func(x *domain.Transfer) { x.AssetID, x.TokenContract = "trx-tron", "" })
	_, err = e.Invoices.AssignTransfer(e.Ctx, trx.ID, v2.ID)
	assert.ErrorIs(t, err, invoice.ErrNoMatchingQuote)
	unknown := unmatched(1, func(x *domain.Transfer) { x.AssetID, x.TokenContract = "", "TUnknownToken" })
	_, err = e.Invoices.AssignTransfer(e.Ctx, unknown.ID, v2.ID)
	assert.ErrorIs(t, err, invoice.ErrNoMatchingQuote)
	gone := unmatched(1, func(x *domain.Transfer) { x.Status = domain.TransferOrphaned })
	_, err = e.Invoices.AssignTransfer(e.Ctx, gone.ID, v2.ID)
	assert.ErrorIs(t, err, invoice.ErrConflict)
	_, err = e.Invoices.AssignTransfer(e.Ctx, uuid.New(), v2.ID)
	assert.ErrorIs(t, err, invoice.ErrNotFound)
	_, err = e.Invoices.AssignTransfer(e.Ctx, trx.ID, uuid.New())
	assert.ErrorIs(t, err, invoice.ErrNotFound)
	// The failed attempts left nothing behind.
	got, err := e.Store.GetTransfer(e.Ctx, trx.ID)
	require.NoError(t, err)
	assert.Nil(t, got.InvoiceID)

	// An expired invoice past its grace period: the admin's call, it pays.
	v3, q3 := e.Quoted("10", "USD", "usdt-tron")
	e.Clock.Set(q3.WatchUntil.Add(time.Hour))
	expire(t, e)
	require.Equal(t, domain.QuoteClosed, e.Quote(q3.ID).Status)
	late := unmatched(10_000_000, func(x *domain.Transfer) { x.Status = domain.TransferConfirmed })
	_, err = e.Invoices.AssignTransfer(e.Ctx, late.ID, v3.ID)
	require.NoError(t, err)
	inv := e.Invoice(v3.ID)
	assert.Equal(t, domain.InvoicePaid, inv.Status)
	assert.True(t, inv.PaidLate)
	checkEvents(t, e, v3.ID, domain.EventExpired, domain.EventPaid)
}

// ---- checkout read helper ----

func TestCheckout(t *testing.T) {
	t.Parallel()
	e := invoicetest.New(t)
	e.Settings(func(s *domain.Settings) { s.WalletConnectID = "wc-1" })
	v, q := e.Quoted("10", "USD", "usdt-tron")
	e.Credit(q, 4_000_000, domain.TransferPending)

	c, err := e.Invoices.Checkout(e.Ctx, v.ID)
	require.NoError(t, err)
	assert.Equal(t, e.Clock.Now(), c.Now)
	assert.Equal(t, domain.InvoicePartial, c.Status)
	require.Len(t, c.Transfers, 1)
	payable, err := e.Store.ListPayableAssets(e.Ctx)
	require.NoError(t, err)
	assert.Len(t, c.Estimates, len(payable))

	raw, err := json.Marshal(c.View())
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	assert.Equal(t, "wc-1", m["walletconnect_project_id"])
	assert.Equal(t, "partial", m["invoice"].(map[string]any)["status"])
	assert.Equal(t, e.Merchant.Name, m["invoice"].(map[string]any)["merchant_name"])
	assert.Equal(t, "4", m["quote"].(map[string]any)["amount_received"])
	assert.Len(t, m["assets"], len(payable))
	assert.Len(t, m["transfers"], 1)

	_, err = e.Invoices.Checkout(e.Ctx, uuid.New())
	assert.ErrorIs(t, err, invoice.ErrNotFound)
}
