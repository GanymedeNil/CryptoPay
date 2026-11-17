package invoice_test

import (
	"context"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cryptopay/internal/alert"
	"cryptopay/internal/domain"
	"cryptopay/internal/invoice/invoicetest"
	"cryptopay/internal/store"
	"cryptopay/internal/store/storetest"
)

// observer records underpaid alerts together with the invoice status it
// can read from the database at that moment (a separate connection only
// sees committed data).
type observer struct {
	alert.Notifier
	st *store.Store

	mu   sync.Mutex
	seen []domain.InvoiceStatus
	ids  []string
}

func (o *observer) InvoiceUnderpaid(ctx context.Context, inv domain.Invoice) {
	got, err := o.st.GetInvoice(ctx, inv.ID)
	o.mu.Lock()
	defer o.mu.Unlock()
	if err == nil {
		o.seen = append(o.seen, got.Status)
	}
	o.ids = append(o.ids, inv.ID.String())
}

func (o *observer) calls() ([]domain.InvoiceStatus, []string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]domain.InvoiceStatus(nil), o.seen...), append([]string(nil), o.ids...)
}

func TestUnderpaidAlertAfterCommit(t *testing.T) {
	t.Parallel()
	e := invoicetest.New(t)
	obs := &observer{st: e.Store}
	e.Invoices.SetNotifier(obs)

	// Expiry turns a short, fully confirmed payment into underpaid.
	v, q := e.Quoted("10", "USD", "usdt-ethereum")
	e.Credit(q, 4_000_000, domain.TransferConfirmed)
	other, _ := e.Quoted("10", "USD", "usdt-tron") // expires without funds: no alert
	e.Clock.Advance(31 * time.Minute)
	res, err := e.Invoices.ExpireDue(e.Ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, res.Changed)
	seen, ids := obs.calls()
	require.Equal(t, []string{v.ID.String()}, ids, "only the underpaid invoice")
	assert.Equal(t, []domain.InvoiceStatus{domain.InvoiceUnderpaid}, seen, "reported after the commit")
	assert.Equal(t, domain.InvoiceExpired, e.Invoice(other.ID).Status)

	// Re-running changes nothing and reports nothing.
	_, err = e.Invoices.ExpireDue(e.Ctx)
	require.NoError(t, err)
	_, ids = obs.calls()
	assert.Len(t, ids, 1)

	// Assigning a short confirmed transfer to an expired invoice.
	w, wq := e.Quoted("10", "USD", "usdt-ethereum")
	e.Clock.Advance(31 * time.Minute)
	_, err = e.Invoices.ExpireDue(e.Ctx)
	require.NoError(t, err)
	tr := storetest.Transfer(t, e.Store, func(x *domain.Transfer) {
		x.ChainID, x.AssetID, x.ToAddress, x.Amount, x.Status = "ethereum", "usdt-ethereum", wq.Address, big.NewInt(1_000_000), domain.TransferConfirmed
		x.TokenContract = "0xdAC17F958D2ee523a2206206994597C13D831ec7"
	})
	_, err = e.Invoices.AssignTransfer(e.Ctx, tr.ID, w.ID)
	require.NoError(t, err)
	seen, ids = obs.calls()
	require.Len(t, ids, 2)
	assert.Equal(t, w.ID.String(), ids[1])
	assert.Equal(t, domain.InvoiceUnderpaid, seen[1])
}

type panicking struct{ alert.Notifier }

func (panicking) InvoiceUnderpaid(context.Context, domain.Invoice) { panic("alert bug") }

type blocking struct {
	alert.Notifier
	release chan struct{}
}

func (b blocking) InvoiceUnderpaid(context.Context, domain.Invoice) { <-b.release }

// TestAlertsCannotFailOrSlowExpiry: a panicking notifier, or a hung one
// behind an alert.Queue, leaves the money path untouched.
func TestAlertsCannotFailOrSlowExpiry(t *testing.T) {
	t.Parallel()
	e := invoicetest.New(t)
	e.Invoices.SetNotifier(panicking{})
	v, q := e.Quoted("10", "USD", "usdt-ethereum")
	e.Credit(q, 4_000_000, domain.TransferConfirmed)
	e.Clock.Advance(31 * time.Minute)
	_, err := e.Invoices.ExpireDue(e.Ctx)
	require.NoError(t, err)
	assert.Equal(t, domain.InvoiceUnderpaid, e.Invoice(v.ID).Status)
	assert.Equal(t, []string{domain.EventPaymentDetected, domain.EventUnderpaid}, e.EventTypes(v.ID))

	b := blocking{release: make(chan struct{})}
	defer close(b.release)
	q2 := alert.NewQueue(b, 1, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go q2.Run(ctx)
	e.Invoices.SetNotifier(q2)
	var short []string
	for range 5 {
		w, wq := e.Quoted("10", "USD", "usdt-ethereum")
		e.Credit(wq, 1_000_000, domain.TransferConfirmed)
		short = append(short, w.ID.String())
	}
	e.Clock.Advance(31 * time.Minute)
	start := time.Now()
	res, err := e.Invoices.ExpireDue(e.Ctx)
	require.NoError(t, err)
	assert.Less(t, time.Since(start), 30*time.Second, "the notifier never returns: waiting for it would hang")
	assert.Equal(t, 5, res.Changed)
	for _, id := range short {
		inv, err := e.Store.GetInvoice(e.Ctx, mustParse(id))
		require.NoError(t, err)
		assert.Equal(t, domain.InvoiceUnderpaid, inv.Status)
	}
	assert.Positive(t, q2.Dropped(), "overflow dropped instead of blocking")
}

func mustParse(s string) uuid.UUID { return uuid.MustParse(s) }
