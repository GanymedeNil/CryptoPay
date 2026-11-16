package invoice_test

import (
	"math/big"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cryptopay/internal/domain"
	"cryptopay/internal/invoice"
	"cryptopay/internal/invoice/invoicetest"
	"cryptopay/internal/store"
	"cryptopay/internal/store/storetest"
	"cryptopay/internal/view"
)

// statusAnnounced is the inverse of invoice.EventForStatus.
var statusAnnounced = map[string]domain.InvoiceStatus{
	domain.EventPaymentDetected: domain.InvoicePartial,
	domain.EventConfirming:      domain.InvoiceConfirming,
	domain.EventPaid:            domain.InvoicePaid,
	domain.EventUnderpaid:       domain.InvoiceUnderpaid,
	domain.EventExpired:         domain.InvoiceExpired,
	domain.EventCancelled:       domain.InvoiceCancelled,
}

// checkEvents asserts the invoice's outbox events (oldest first) have
// exactly the given types, and that every payload is a complete envelope
// (id = event id, type, created_at) whose data is the invoice in the status
// the event announces. It returns the decoded envelopes.
func checkEvents(t *testing.T, e *invoicetest.Env, invoiceID uuid.UUID, want ...string) []view.WebhookEnvelope {
	t.Helper()
	evs := e.Events(invoiceID)
	got := []string{}
	for _, ev := range evs {
		got = append(got, ev.EventType)
	}
	if want == nil {
		want = []string{}
	}
	require.Equal(t, want, got, "webhook events")
	out := make([]view.WebhookEnvelope, 0, len(evs))
	for _, ev := range evs {
		env := e.Envelope(ev)
		assert.Equal(t, ev.ID.String(), env.ID, "envelope id = event id")
		assert.Equal(t, ev.EventType, env.Type)
		assert.False(t, env.CreatedAt.IsZero())
		assert.Equal(t, invoiceID.String(), env.Data.ID)
		assert.Equal(t, string(statusAnnounced[ev.EventType]), env.Data.Status, "data carries the new status (%s)", ev.EventType)
		assert.Equal(t, ev.MerchantID.String(), env.Data.MerchantID)
		assert.Equal(t, domain.WebhookPending, ev.Status)
		assert.Contains(t, env.Data.CheckoutURL, "/pay/"+invoiceID.String())
		out = append(out, env)
	}
	return out
}

func TestPayInFullConfirmingThenPaid(t *testing.T) {
	t.Parallel()
	e := invoicetest.New(t)
	v, q := e.Quoted("10", "USD", "usdt-ethereum")
	require.Equal(t, domain.InvoicePending, e.Invoice(v.ID).Status)
	checkEvents(t, e, v.ID)

	tr := e.Credit(q, 10_000_000, domain.TransferPending)
	inv := e.Invoice(v.ID)
	assert.Equal(t, domain.InvoiceConfirming, inv.Status)
	assert.False(t, inv.Overpaid)
	assert.Nil(t, inv.PaidAt)
	q = e.Quote(q.ID)
	assert.Equal(t, "10000000", q.AmountReceived.String())
	assert.Equal(t, "0", q.AmountConfirmed.String())
	assert.Equal(t, domain.QuoteActive, q.Status)
	envs := checkEvents(t, e, v.ID, domain.EventConfirming)
	require.NotNil(t, envs[0].Data.Quote)
	assert.Equal(t, "10", envs[0].Data.Quote.AmountReceived)
	assert.Equal(t, "0", envs[0].Data.Quote.AmountConfirmed)
	assert.Equal(t, "0", envs[0].Data.Quote.AmountRemaining)

	payTime := e.Clock.Now()
	e.SetTransfer(tr, domain.TransferConfirmed)
	inv = e.Invoice(v.ID)
	assert.Equal(t, domain.InvoicePaid, inv.Status)
	require.NotNil(t, inv.PaidAt)
	assert.True(t, inv.PaidAt.Equal(payTime))
	require.NotNil(t, inv.PaidQuoteID)
	assert.Equal(t, q.ID, *inv.PaidQuoteID)
	assert.False(t, inv.PaidLate)
	assert.False(t, inv.Overpaid)
	q = e.Quote(q.ID)
	assert.Equal(t, domain.QuotePaid, q.Status)
	assert.Equal(t, "10000000", q.AmountConfirmed.String())
	envs = checkEvents(t, e, v.ID, domain.EventConfirming, domain.EventPaid)
	paid := envs[1].Data
	require.NotNil(t, paid.PaidAt)
	require.NotNil(t, paid.Quote)
	assert.Equal(t, "paid", paid.Quote.Status)
	assert.Equal(t, "10", paid.Quote.AmountConfirmed)
	assert.Equal(t, q.ID.String(), paid.Quote.ID)

	// Re-evaluating without new facts changes and emits nothing.
	for range 3 {
		ev := e.Reevaluate(v.ID)
		assert.False(t, ev.StatusChanged)
		assert.Empty(t, ev.Event)
		assert.Equal(t, domain.InvoicePaid, ev.Previous)
	}
	checkEvents(t, e, v.ID, domain.EventConfirming, domain.EventPaid)
	// Paid is terminal and nothing of the invoice is watched any more.
	watched, err := e.Store.ListWatched(e.Ctx, "ethereum", e.Clock.Now())
	require.NoError(t, err)
	assert.Empty(t, watched)
}

func TestFirstTransferAlreadyConfirmedPaysDirectly(t *testing.T) {
	t.Parallel()
	e := invoicetest.New(t)
	v, q := e.Quoted("10", "USD", "usdt-tron")
	e.Credit(q, q.AmountDue.Int64(), domain.TransferConfirmed)
	assert.Equal(t, domain.InvoicePaid, e.Invoice(v.ID).Status)
	checkEvents(t, e, v.ID, domain.EventPaid)
}

func TestPartialThenTopUp(t *testing.T) {
	t.Parallel()
	e := invoicetest.New(t)
	v, q := e.Quoted("10", "USD", "usdt-ethereum")

	t1 := e.Credit(q, 4_000_000, domain.TransferPending)
	assert.Equal(t, domain.InvoicePartial, e.Invoice(v.ID).Status)
	envs := checkEvents(t, e, v.ID, domain.EventPaymentDetected)
	assert.Equal(t, "6", envs[0].Data.Quote.AmountRemaining)

	// Confirming the partial amount is not a status change.
	e.SetTransfer(t1, domain.TransferConfirmed)
	assert.Equal(t, domain.InvoicePartial, e.Invoice(v.ID).Status)
	assert.Equal(t, "4000000", e.Quote(q.ID).AmountConfirmed.String())
	checkEvents(t, e, v.ID, domain.EventPaymentDetected)

	// The customer may still re-select the asset (same quote, unchanged).
	same, err := e.Invoices.SelectAsset(e.Ctx, v.ID, "usdt-ethereum")
	require.NoError(t, err)
	assert.Equal(t, q.ID, same.ID)

	t2 := e.Credit(q, 6_000_000, domain.TransferPending)
	assert.Equal(t, domain.InvoiceConfirming, e.Invoice(v.ID).Status)
	q = e.Quote(q.ID)
	assert.Equal(t, "10000000", q.AmountReceived.String())
	assert.Equal(t, "4000000", q.AmountConfirmed.String())

	e.SetTransfer(t2, domain.TransferConfirmed)
	inv := e.Invoice(v.ID)
	assert.Equal(t, domain.InvoicePaid, inv.Status)
	assert.False(t, inv.Overpaid)
	checkEvents(t, e, v.ID, domain.EventPaymentDetected, domain.EventConfirming, domain.EventPaid)
}

func TestTwoPendingTransfersPayTogether(t *testing.T) {
	t.Parallel()
	e := invoicetest.New(t)
	v, q := e.Quoted("0.01", "BTC", "btc-bitcoin") // 1_000_000 sat
	require.Equal(t, "1000000", q.AmountDue.String())
	a := e.Credit(q, 500_000, domain.TransferPending)
	assert.Equal(t, domain.InvoicePartial, e.Invoice(v.ID).Status)
	b := e.Credit(q, 500_000, domain.TransferPending)
	assert.Equal(t, domain.InvoiceConfirming, e.Invoice(v.ID).Status)

	e.SetTransfer(a, domain.TransferConfirmed)
	assert.Equal(t, domain.InvoiceConfirming, e.Invoice(v.ID).Status, "half confirmed is not paid")
	e.SetTransfer(b, domain.TransferConfirmed)
	assert.Equal(t, domain.InvoicePaid, e.Invoice(v.ID).Status)
	checkEvents(t, e, v.ID, domain.EventPaymentDetected, domain.EventConfirming, domain.EventPaid)
}

func TestOverpaid(t *testing.T) {
	t.Parallel()
	e := invoicetest.New(t)
	v, q := e.Quoted("10", "USD", "usdt-ethereum")
	tr := e.Credit(q, 12_500_000, domain.TransferPending)
	inv := e.Invoice(v.ID)
	assert.Equal(t, domain.InvoiceConfirming, inv.Status)
	assert.True(t, inv.Overpaid)
	envs := checkEvents(t, e, v.ID, domain.EventConfirming)
	assert.True(t, envs[0].Data.Overpaid)
	assert.Equal(t, "0", envs[0].Data.Quote.AmountRemaining)

	e.SetTransfer(tr, domain.TransferConfirmed)
	inv = e.Invoice(v.ID)
	assert.Equal(t, domain.InvoicePaid, inv.Status)
	assert.True(t, inv.Overpaid)
	envs = checkEvents(t, e, v.ID, domain.EventConfirming, domain.EventPaid)
	assert.True(t, envs[1].Data.Overpaid)

	// Exactly the amount due is not overpaid; an extra transfer seen after
	// the payment raises the flag (no new event) and a reorg clears it again.
	v2, q2 := e.Quoted("10", "USD", "usdt-ethereum")
	e.Credit(q2, 10_000_000, domain.TransferConfirmed)
	require.Equal(t, domain.InvoicePaid, e.Invoice(v2.ID).Status)
	assert.False(t, e.Invoice(v2.ID).Overpaid)
	extra := e.Credit(e.Quote(q2.ID), 1, domain.TransferPending)
	assert.True(t, e.Invoice(v2.ID).Overpaid)
	assert.Equal(t, domain.InvoicePaid, e.Invoice(v2.ID).Status)
	e.SetTransfer(extra, domain.TransferOrphaned)
	assert.False(t, e.Invoice(v2.ID).Overpaid)
	checkEvents(t, e, v2.ID, domain.EventPaid)
}

func TestToleranceBoundary(t *testing.T) {
	t.Parallel()
	e := invoicetest.New(t)
	// Default tolerance: 50 bps of 10 USDT = 0.05 → threshold 9.95 USDT.
	cases := []struct {
		amount int64
		status domain.TransferStatus
		want   domain.InvoiceStatus
	}{
		{9_950_000, domain.TransferConfirmed, domain.InvoicePaid},     // exactly at the threshold
		{9_949_999, domain.TransferConfirmed, domain.InvoicePartial},  // one base unit below
		{9_950_000, domain.TransferPending, domain.InvoiceConfirming}, // the threshold applies to "confirming" too
		{9_949_999, domain.TransferPending, domain.InvoicePartial},    //
		{10_000_000, domain.TransferConfirmed, domain.InvoicePaid},    // exact
		{10_000_001, domain.TransferConfirmed, domain.InvoicePaid},    // over
		{1, domain.TransferPending, domain.InvoicePartial},            // dust
	}
	for _, tc := range cases {
		v, q := e.Quoted("10", "USD", "usdt-polygon")
		e.Credit(q, tc.amount, tc.status)
		inv := e.Invoice(v.ID)
		assert.Equal(t, tc.want, inv.Status, "%d %s", tc.amount, tc.status)
		assert.Equal(t, tc.amount > 10_000_000, inv.Overpaid, "%d", tc.amount)
	}

	// Zero tolerance: only the full amount counts.
	e.Settings(func(s *domain.Settings) { s.UnderpayToleranceBps = 0 })
	v, q := e.Quoted("10", "USD", "usdt-polygon")
	e.Credit(q, 9_999_999, domain.TransferConfirmed)
	assert.Equal(t, domain.InvoicePartial, e.Invoice(v.ID).Status)
	e.Credit(e.Quote(q.ID), 1, domain.TransferConfirmed)
	assert.Equal(t, domain.InvoicePaid, e.Invoice(v.ID).Status)
	checkEvents(t, e, v.ID, domain.EventPaymentDetected, domain.EventPaid)

	// A large tolerance (10 %) on an 8-decimals coin, computed exactly.
	e.Settings(func(s *domain.Settings) { s.UnderpayToleranceBps = 1000 })
	v, q = e.Quoted("100", "USD", "btc-bitcoin") // 0.002 BTC = 200000 sat
	e.Credit(q, 180_000, domain.TransferConfirmed)
	assert.Equal(t, domain.InvoicePaid, e.Invoice(v.ID).Status)
	v, q = e.Quoted("100", "USD", "btc-bitcoin")
	e.Credit(q, 179_999, domain.TransferConfirmed)
	assert.Equal(t, domain.InvoicePartial, e.Invoice(v.ID).Status)
}

func TestReorgMovesInvoiceBack(t *testing.T) {
	t.Parallel()
	e := invoicetest.New(t)

	// The only transfer is orphaned: confirming → pending. docs/api.md has no
	// event for "pending", so nothing is emitted; the re-included transfer
	// later emits invoice.confirming again.
	v, q := e.Quoted("10", "USD", "usdt-ethereum")
	tr := e.Credit(q, 10_000_000, domain.TransferPending)
	require.Equal(t, domain.InvoiceConfirming, e.Invoice(v.ID).Status)
	ev := func() invoice.Evaluation {
		_, err := e.Store.SetTransferStatus(e.Ctx, tr.ID, domain.TransferOrphaned, 0)
		require.NoError(t, err)
		return e.Reevaluate(v.ID)
	}()
	assert.True(t, ev.StatusChanged)
	assert.Equal(t, domain.InvoiceConfirming, ev.Previous)
	assert.Equal(t, domain.InvoicePending, ev.Invoice.Status)
	assert.Empty(t, ev.Event)
	assert.Equal(t, "0", e.Quote(q.ID).AmountReceived.String())
	checkEvents(t, e, v.ID, domain.EventConfirming)
	_, err := e.Store.ReviveTransfer(e.Ctx, tr.ID, 200, "0xnew", e.Clock.Now())
	require.NoError(t, err)
	e.Reevaluate(v.ID)
	assert.Equal(t, domain.InvoiceConfirming, e.Invoice(v.ID).Status)
	assert.Equal(t, "10000000", e.Quote(q.ID).AmountReceived.String(), "credited once")
	checkEvents(t, e, v.ID, domain.EventConfirming, domain.EventConfirming)

	// One of two transfers is orphaned: confirming → partial.
	v2, q2 := e.Quoted("10", "USD", "usdt-ethereum")
	a := e.Credit(q2, 6_000_000, domain.TransferPending)
	e.Credit(e.Quote(q2.ID), 6_000_000, domain.TransferPending)
	require.Equal(t, domain.InvoiceConfirming, e.Invoice(v2.ID).Status)
	require.True(t, e.Invoice(v2.ID).Overpaid)
	e.SetTransfer(a, domain.TransferOrphaned)
	inv := e.Invoice(v2.ID)
	assert.Equal(t, domain.InvoicePartial, inv.Status)
	assert.False(t, inv.Overpaid)
	envs := checkEvents(t, e, v2.ID, domain.EventPaymentDetected, domain.EventConfirming, domain.EventPaymentDetected)
	assert.Equal(t, "6", envs[2].Data.Quote.AmountReceived, "the event carries the reduced amount")
}

func TestFundsOnSupersededQuote(t *testing.T) {
	t.Parallel()
	e := invoicetest.New(t)
	v, a := e.Quoted("10", "USD", "usdt-ethereum")
	b := e.Select(v.ID, "usdt-polygon")
	// The customer paid the first quote after all.
	e.Credit(a, 10_000_000, domain.TransferConfirmed)
	inv := e.Invoice(v.ID)
	assert.Equal(t, domain.InvoicePaid, inv.Status)
	require.NotNil(t, inv.PaidQuoteID)
	assert.Equal(t, a.ID, *inv.PaidQuoteID)
	assert.Equal(t, domain.QuotePaid, e.Quote(a.ID).Status)
	assert.Equal(t, domain.QuoteClosed, e.Quote(b.ID).Status, "other quotes stop being watched")
	envs := checkEvents(t, e, v.ID, domain.EventPaid)
	assert.Equal(t, a.ID.String(), envs[0].Data.Quote.ID, "the payload shows the paid quote")

	// Funds on two quotes of different assets are never added up.
	v2, c := e.Quoted("10", "USD", "usdt-ethereum")
	d := e.Select(v2.ID, "usdc-ethereum")
	e.Credit(c, 6_000_000, domain.TransferConfirmed)
	e.Credit(d, 6_000_000, domain.TransferConfirmed)
	assert.Equal(t, domain.InvoicePartial, e.Invoice(v2.ID).Status)
}

func TestMerchantWebhookURL(t *testing.T) {
	t.Parallel()
	e := invoicetest.New(t)
	silent := storetest.Merchant(t, e.Store, func(m *domain.Merchant) { m.WebhookURL = "" })
	v, _, err := e.Invoices.Create(e.Ctx, silent, invoice.CreateParams{Amount: "10", Currency: "USD", AssetID: "usdt-tron"})
	require.NoError(t, err)
	tr := e.Credit(*v.Quote, v.Quote.AmountDue.Int64(), domain.TransferPending)
	assert.Equal(t, domain.InvoiceConfirming, e.Invoice(v.ID).Status, "the state machine runs without a webhook URL")
	checkEvents(t, e, v.ID)

	// The URL in force when the status changes is the one stored on the event.
	_, err = e.Store.UpdateMerchant(e.Ctx, silent.ID, store.MerchantUpdate{WebhookURL: storetest.Ptr("https://hooks.example/a")})
	require.NoError(t, err)
	e.SetTransfer(tr, domain.TransferConfirmed)
	evs := e.Events(v.ID)
	require.Len(t, evs, 1)
	assert.Equal(t, "https://hooks.example/a", evs[0].URL)
	assert.Equal(t, silent.ID, evs[0].MerchantID)
	checkEvents(t, e, v.ID, domain.EventPaid)

	// The default merchant's URL.
	v2 := e.Create("10", "USD")
	_, err = e.Invoices.Cancel(e.Ctx, v2.ID, "test")
	require.NoError(t, err)
	evs = e.Events(v2.ID)
	require.Len(t, evs, 1)
	assert.Equal(t, e.Merchant.WebhookURL, evs[0].URL)
}

func TestConcurrentReevaluateEmitsOnce(t *testing.T) {
	t.Parallel()
	e := invoicetest.New(t)
	v, q := e.Quoted("10", "USD", "usdt-ethereum")
	storetest.TransferFor(t, e.Store, q, 10_000_000, func(x *domain.Transfer) { x.Status = domain.TransferConfirmed })
	var wg sync.WaitGroup
	changed := make(chan bool, 16)
	for range 16 {
		wg.Go(func() {
			err := e.Store.WithTx(e.Ctx, func(tx *store.Store) error {
				ev, err := e.Invoices.Reevaluate(e.Ctx, tx, v.ID)
				if err == nil {
					changed <- ev.StatusChanged
				}
				return err
			})
			assert.NoError(t, err)
		})
	}
	wg.Wait()
	close(changed)
	n := 0
	for c := range changed {
		if c {
			n++
		}
	}
	assert.Equal(t, 1, n, "exactly one transaction saw the change")
	checkEvents(t, e, v.ID, domain.EventPaid)
}

func TestEventsOrderedWithinOneInstant(t *testing.T) {
	t.Parallel()
	e := invoicetest.New(t)
	v, q := e.Quoted("10", "USD", "usdt-ethereum")
	reevaluate := func() {
		require.NoError(t, e.Store.WithTx(e.Ctx, func(tx *store.Store) error {
			_, err := e.Invoices.Reevaluate(e.Ctx, tx, v.ID)
			return err
		}))
	}
	// Three status changes without the clock moving (as in one scanner tick).
	a := storetest.TransferFor(t, e.Store, q, 4_000_000)
	reevaluate() // partial
	b := storetest.TransferFor(t, e.Store, q, 6_000_000)
	reevaluate() // confirming
	for _, tr := range []domain.Transfer{a, b} {
		_, err := e.Store.SetTransferStatus(e.Ctx, tr.ID, domain.TransferConfirmed, 3)
		require.NoError(t, err)
	}
	reevaluate() // paid
	envs := checkEvents(t, e, v.ID, domain.EventPaymentDetected, domain.EventConfirming, domain.EventPaid)
	evs := e.Events(v.ID)
	for i := 1; i < len(evs); i++ {
		assert.True(t, evs[i].CreatedAt.After(evs[i-1].CreatedAt), "strictly increasing created_at")
		assert.True(t, evs[i].NextAttemptAt.After(evs[i-1].NextAttemptAt), "dispatched in order")
		assert.True(t, envs[i].CreatedAt.After(envs[i-1].CreatedAt), "payload created_at follows")
	}
}

func TestReevaluateErrors(t *testing.T) {
	t.Parallel()
	e := invoicetest.New(t)
	_, err := e.Invoices.Reevaluate(e.Ctx, e.Store, uuid.New())
	assert.ErrorIs(t, err, store.ErrTxRequired, "outside a transaction")
	err = e.Store.WithTx(e.Ctx, func(tx *store.Store) error {
		_, err := e.Invoices.Reevaluate(e.Ctx, tx, uuid.New())
		return err
	})
	assert.ErrorIs(t, err, invoice.ErrNotFound)
}

func TestEventForStatus(t *testing.T) {
	t.Parallel()
	for ev, st := range statusAnnounced {
		assert.Equal(t, ev, invoice.EventForStatus(st))
	}
	assert.Empty(t, invoice.EventForStatus(domain.InvoicePending))
	assert.Empty(t, invoice.EventForStatus("bogus"))
}

// quoteAmounts is a compact view of a quote's amounts for assertions.
func quoteAmounts(q domain.Quote) [3]string {
	s := func(v *big.Int) string {
		if v == nil {
			return "<nil>"
		}
		return v.String()
	}
	return [3]string{s(q.AmountDue), s(q.AmountReceived), s(q.AmountConfirmed)}
}

func TestRecomputeIgnoresOrphanedAndOtherQuotes(t *testing.T) {
	t.Parallel()
	e := invoicetest.New(t)
	v, q := e.Quoted("10", "USD", "usdt-ethereum")
	a := e.Credit(q, 1_000_000, domain.TransferPending)
	e.Credit(e.Quote(q.ID), 2_000_000, domain.TransferConfirmed)
	e.SetTransfer(a, domain.TransferOrphaned)
	assert.Equal(t, [3]string{"10000000", "2000000", "2000000"}, quoteAmounts(e.Quote(q.ID)))
	// A transfer of another invoice does not leak in.
	_, q2 := e.Quoted("10", "USD", "usdt-ethereum")
	e.Credit(q2, 3_000_000, domain.TransferConfirmed)
	assert.Equal(t, [3]string{"10000000", "2000000", "2000000"}, quoteAmounts(e.Quote(q.ID)))
	assert.Equal(t, domain.InvoicePartial, e.Invoice(v.ID).Status)
}
