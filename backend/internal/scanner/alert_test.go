package scanner_test

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cryptopay/internal/alert"
	"cryptopay/internal/domain"
	"cryptopay/internal/scanner"
)

// withNotifier replaces the harness manager by one reporting to n (and
// makes the invoice service report to n too).
func (h *harness) withNotifier(n alert.Notifier) {
	cfg := testConfig
	cfg.Notifier = n
	h.Invoices.SetNotifier(n)
	h.M = scanner.New(h.Store, h.Provider, h.Invoices, h.Log, h.Clock.Now, cfg)
}

func TestUnmatchedTransferAlert(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	rec := alert.NewRecorder()
	h.withNotifier(rec)
	f := h.Fakes["ethereum"]
	_, qa := h.Quoted("10", "USD", "usdt-ethereum")
	h.Quoted("10", "USD", "usdc-ethereum")
	h.tick("ethereum")

	f.Pay(1001, "0xmatched", qa.Address, h.contract("usdt-ethereum"), big.NewInt(1_000_000))
	f.Pay(1001, "0xunmatched", qa.Address, h.contract("usdc-ethereum"), big.NewInt(10_000_000))
	f.SetHead(1001)
	h.tick("ethereum")
	calls := rec.Calls()
	require.Len(t, calls, 1, "only the unmatched transfer")
	assert.Equal(t, "transfer_unmatched", calls[0].Kind)
	assert.Equal(t, "0xunmatched", calls[0].Transfer.TxHash)
	assert.Equal(t, "usdc-ethereum", calls[0].Transfer.AssetID)
	assert.Nil(t, calls[0].Transfer.InvoiceID)
	// Stored (committed) before it was reported.
	h.transfer("ethereum", "0xunmatched", 0)

	// Re-scanning the same range reports nothing again.
	h.setCursor("ethereum", 1000)
	h.tick("ethereum")
	assert.Empty(t, rec.Calls())
}

func TestUnderpaidAlertFromScanner(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	rec := alert.NewRecorder()
	h.withNotifier(rec)
	f := h.Fakes["ethereum"]
	v, q := h.Quoted("10", "USD", "usdt-ethereum")
	h.tick("ethereum")
	f.Pay(1001, "0xshort", q.Address, h.contract("usdt-ethereum"), big.NewInt(4_000_000))
	f.SetHead(1001)
	h.tick("ethereum")
	h.Clock.Advance(31 * time.Minute) // expired, still partial (unconfirmed)
	_, err := h.Invoices.ExpireDue(h.Ctx)
	require.NoError(t, err)
	assert.Equal(t, domain.InvoicePartial, h.status(v.ID))
	assert.Empty(t, rec.Calls())
	f.SetHead(1001 + confirmations)
	h.tick("ethereum")
	assert.Equal(t, domain.InvoiceUnderpaid, h.status(v.ID))
	calls := rec.Calls()
	require.Len(t, calls, 1)
	assert.Equal(t, "invoice_underpaid", calls[0].Kind)
	assert.Equal(t, v.ID, calls[0].Invoice.ID)
}

type panicNotifier struct{ alert.Notifier }

func (panicNotifier) TransferUnmatched(context.Context, domain.Transfer)        { panic("alert bug") }
func (panicNotifier) InvoiceUnderpaid(context.Context, domain.Invoice)          { panic("alert bug") }
func (panicNotifier) ScannerFailing(context.Context, string, time.Time, string) { panic("alert bug") }
func (panicNotifier) WebhookFailed(context.Context, domain.WebhookEvent)        { panic("alert bug") }

type hangNotifier struct{ release chan struct{} }

func (h hangNotifier) TransferUnmatched(context.Context, domain.Transfer)        { <-h.release }
func (h hangNotifier) InvoiceUnderpaid(context.Context, domain.Invoice)          { <-h.release }
func (h hangNotifier) ScannerFailing(context.Context, string, time.Time, string) { <-h.release }
func (h hangNotifier) WebhookFailed(context.Context, domain.WebhookEvent)        { <-h.release }

// TestAlertsCannotFailOrSlowScanning: panicking or hanging notifiers
// change nothing about crediting.
func TestAlertsCannotFailOrSlowScanning(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.withNotifier(panicNotifier{})
	f := h.Fakes["ethereum"]
	v, q := h.Quoted("10", "USD", "usdt-ethereum")
	h.Quoted("10", "USD", "usdc-ethereum")
	h.tick("ethereum")
	f.Pay(1001, "0xstray", q.Address, h.contract("usdc-ethereum"), big.NewInt(1))
	f.Pay(1001, "0xpay", q.Address, h.contract("usdt-ethereum"), big.NewInt(10_000_000))
	f.SetHead(1001 + confirmations)
	res := h.tick("ethereum")
	assert.Equal(t, 2, res.NewTransfers)
	assert.Equal(t, domain.InvoicePaid, h.status(v.ID))

	// A hung notifier behind the production queue: nothing waits for it.
	hang := hangNotifier{release: make(chan struct{})}
	defer close(hang.release)
	q2 := alert.NewQueue(hang, 2, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go q2.Run(ctx)
	h.withNotifier(q2)
	w, wq := h.Quoted("10", "USD", "usdt-ethereum")
	h.tick("ethereum")
	head := uint64(1001 + confirmations + 1)
	for i := range 10 {
		f.Pay(head, fmt.Sprintf("0xstray%d", i), wq.Address, h.contract("usdc-ethereum"), big.NewInt(int64(i+1)))
	}
	f.Pay(head, "0xpay2", wq.Address, h.contract("usdt-ethereum"), big.NewInt(10_000_000))
	f.SetHead(head + confirmations)
	// The notifier never returns: if the tick waited for it, it would hang
	// (the bound is generous for -race under load).
	start := time.Now()
	res = h.tick("ethereum")
	assert.Less(t, time.Since(start), 30*time.Second)
	assert.Equal(t, 11, res.NewTransfers)
	assert.Equal(t, domain.InvoicePaid, h.status(w.ID))
	assert.Positive(t, q2.Dropped())
}

// TestScannerFailingAlert: a chain failing for FailingAlertAfter is
// reported once (with a sanitized error) and again only after another
// FailingAlertAfter of failures; a success resets the streak.
func TestScannerFailingAlert(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	rec := alert.NewRecorder()
	h.withNotifier(rec)
	tron := h.Fakes["tron"]
	tron.SetHeadError(fmt.Errorf("dial tcp: connection refused: https://api.trongrid.io/v1?apikey=SECRETKEY: %w", errors.New("x")))
	start := h.Clock.Now()

	ctx, cancel := context.WithCancel(h.Ctx)
	done := make(chan struct{})
	go func() { _ = h.M.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	eventually(t, "tron failing", func() bool { return tron.HeadCalls() >= 3 })
	assert.Empty(t, rec.Calls(), "not before 10 minutes")
	h.Clock.Advance(11 * time.Minute)
	var calls []alert.Call
	eventually(t, "alert", func() bool { calls = append(calls, rec.Calls()...); return len(calls) > 0 })
	require.Len(t, calls, 1)
	assert.Equal(t, "scanner_failing", calls[0].Kind)
	assert.Equal(t, "tron", calls[0].ChainID)
	assert.Equal(t, start, calls[0].Since)
	assert.Contains(t, calls[0].LastError, "connection refused")
	assert.NotContains(t, calls[0].LastError, "SECRETKEY")

	// Still failing: no repeat within the next 10 minutes …
	n := tron.HeadCalls()
	eventually(t, "more failures", func() bool { return tron.HeadCalls() >= n+3 })
	assert.Empty(t, rec.Calls())
	// … but again after another 10 minutes (the notifier throttles to 6 h).
	h.Clock.Advance(11 * time.Minute)
	eventually(t, "second alert", func() bool { calls = rec.Calls(); return len(calls) > 0 })
	assert.Equal(t, start, calls[0].Since, "same streak")

	// Recovery resets the streak and sends nothing.
	tron.SetHeadError(nil)
	n = tron.HeadCalls()
	eventually(t, "recovered", func() bool { return tron.HeadCalls() >= n+2 && h.chain("tron").LastError == "" })
	tron.SetHeadError(errors.New("down again"))
	n = tron.HeadCalls()
	eventually(t, "failing again", func() bool { return tron.HeadCalls() >= n+3 })
	assert.Empty(t, rec.Calls(), "a new streak starts from zero")
}
