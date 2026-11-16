package invoice

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"time"

	"github.com/google/uuid"

	"cryptopay/internal/domain"
	"cryptopay/internal/store"
	"cryptopay/internal/view"
	"cryptopay/internal/webhook"
)

// Evaluation is the outcome of Reevaluate.
type Evaluation struct {
	Invoice       domain.Invoice       // the invoice after the evaluation
	Previous      domain.InvoiceStatus // its status before
	StatusChanged bool                 // Invoice.Status != Previous
	// Event is the webhook event type enqueued for the status change; ""
	// when the status did not change, the new status has no event (pending)
	// or the merchant has no webhook URL.
	Event string
}

// Reevaluate is the crediting core: it recomputes the amounts of every
// quote of the invoice from its non-orphaned transfers and derives the
// invoice status from them (see the package documentation for the rules),
// persisting the new state and enqueueing the webhook event of a status
// change — all inside the caller's transaction tx, which must come from
// store.WithTx (store.ErrTxRequired otherwise).
//
// It takes the invoice row lock first; callers that also lock transfers
// must do so after calling GetInvoiceForUpdate themselves (lock order
// invoice → quotes → transfers). It is idempotent: calling it again without
// new facts changes nothing and enqueues nothing. Unknown invoice →
// ErrNotFound.
func (s *Service) Reevaluate(ctx context.Context, tx *store.Store, invoiceID uuid.UUID) (Evaluation, error) {
	inv, err := tx.GetInvoiceForUpdate(ctx, invoiceID)
	if err != nil {
		return Evaluation{}, mapNotFound(err)
	}
	quotes, err := tx.ListQuotesByInvoice(ctx, invoiceID)
	if err != nil {
		return Evaluation{}, err
	}
	for i := range quotes {
		q := &quotes[i]
		received, confirmed, err := tx.QuoteTransferSums(ctx, q.ID)
		if err != nil {
			return Evaluation{}, err
		}
		if cmp(q.AmountReceived, received) == 0 && cmp(q.AmountConfirmed, confirmed) == 0 {
			continue
		}
		// Recompute in SQL under the quote row lock taken by the UPDATE.
		if *q, err = tx.RecomputeQuoteAmounts(ctx, q.ID); err != nil {
			return Evaluation{}, err
		}
	}
	st, err := tx.GetSettings(ctx)
	if err != nil {
		return Evaluation{}, err
	}
	now := s.clock()
	next, paidQuote := derive(inv, quotes, now, st.UnderpayToleranceBps)
	return s.apply(ctx, tx, inv, next, paidQuote)
}

// apply persists a state transition of the locked invoice inv and enqueues
// the webhook event if the status changed. paidQuote is the quote that
// carries the payment when next.Status is paid (may be nil for a manual
// resolution without quotes).
func (s *Service) apply(ctx context.Context, tx *store.Store, inv domain.Invoice, next store.InvoiceState, paidQuote *domain.Quote) (Evaluation, error) {
	ev := Evaluation{Invoice: inv, Previous: inv.Status}
	if sameState(store.StateOf(inv), next) {
		return ev, nil
	}
	updated, err := tx.UpdateInvoiceState(ctx, inv.ID, next)
	if err != nil {
		return Evaluation{}, err
	}
	ev.Invoice = updated
	ev.StatusChanged = updated.Status != inv.Status
	if !ev.StatusChanged {
		return ev, nil
	}
	if updated.Status == domain.InvoicePaid {
		keep := uuid.Nil
		if paidQuote != nil {
			keep = paidQuote.ID
			if _, err := tx.SetQuoteStatus(ctx, paidQuote.ID, domain.QuotePaid); err != nil {
				return Evaluation{}, err
			}
		}
		// The other quotes are no longer needed: stop watching them (this
		// also frees their static amounts).
		if _, err := tx.CloseLiveQuotes(ctx, inv.ID, keep); err != nil {
			return Evaluation{}, err
		}
	}
	if ev.Event, err = s.enqueueEvent(ctx, tx, updated); err != nil {
		return Evaluation{}, err
	}
	s.log.InfoContext(ctx, "invoice status changed", "invoice_id", inv.ID, "from", string(inv.Status),
		"to", string(updated.Status), "overpaid", updated.Overpaid, "paid_late", updated.PaidLate, "event", ev.Event)
	return ev, nil
}

// derive computes the lifecycle state of inv from its quotes' amounts at
// time now. It returns the quote carrying the payment when the result is a
// new paid state.
func derive(inv domain.Invoice, quotes []domain.Quote, now time.Time, toleranceBps int) (store.InvoiceState, *domain.Quote) {
	cur := store.StateOf(inv)
	switch inv.Status {
	case domain.InvoiceCancelled:
		return cur, nil // terminal; funds stay recorded on the quotes
	case domain.InvoicePaid:
		// Terminal. Only the informational overpaid flag still follows the
		// paid quote (an extra transfer that was pending when the invoice
		// was paid may confirm — or be orphaned — later). A manual
		// resolution keeps the flag it was given while nothing was received.
		if inv.PaidQuoteID != nil {
			for i := range quotes {
				q := &quotes[i]
				if q.ID == *inv.PaidQuoteID && sign(q.AmountReceived) > 0 {
					cur.Overpaid = cmp(q.AmountReceived, q.AmountDue) > 0
				}
			}
		}
		return cur, nil
	}

	// Prefer the quote the customer is looking at, then older ones.
	order := make([]*domain.Quote, len(quotes))
	for i := range quotes {
		order[i] = &quotes[i]
	}
	sort.SliceStable(order, func(i, j int) bool { return quoteRank(order[i].Status) < quoteRank(order[j].Status) })

	var paid, full, funded *domain.Quote
	unconfirmed := false
	for _, q := range order {
		if paid == nil && meets(q.AmountConfirmed, q.AmountDue, toleranceBps) {
			paid = q
		}
		if full == nil && meets(q.AmountReceived, q.AmountDue, toleranceBps) {
			full = q
		}
		if funded == nil && sign(q.AmountReceived) > 0 {
			funded = q
		}
		if cmp(q.AmountReceived, q.AmountConfirmed) > 0 {
			unconfirmed = true
		}
	}
	expired := !now.Before(inv.ExpiresAt)
	next := store.InvoiceState{}
	switch {
	case paid != nil:
		at := now
		id := paid.ID
		next = store.InvoiceState{Status: domain.InvoicePaid, PaidAt: &at, PaidQuoteID: &id, PaidLate: expired,
			Overpaid: cmp(paid.AmountReceived, paid.AmountDue) > 0}
		return next, paid
	case full != nil:
		next.Status = domain.InvoiceConfirming
		next.Overpaid = cmp(full.AmountReceived, full.AmountDue) > 0
	case funded != nil:
		// After expiry a short payment is final only once nothing is left
		// unconfirmed (pending funds may still confirm or be orphaned).
		if expired && !unconfirmed {
			next.Status = domain.InvoiceUnderpaid
		} else {
			next.Status = domain.InvoicePartial
		}
	case expired:
		next.Status = domain.InvoiceExpired
	default:
		next.Status = domain.InvoicePending
	}
	return next, nil
}

func quoteRank(st domain.QuoteStatus) int {
	switch st {
	case domain.QuoteActive:
		return 0
	case domain.QuoteSuperseded:
		return 1
	case domain.QuotePaid:
		return 2
	}
	return 3
}

var bpsDenominator = big.NewInt(10000)

// meets reports whether amount covers due within the underpayment
// tolerance: amount ≥ due × (1 − toleranceBps/10000), computed exactly as
// amount × 10000 ≥ due × (10000 − toleranceBps). Nothing is never enough.
func meets(amount, due *big.Int, toleranceBps int) bool {
	if sign(amount) <= 0 || sign(due) <= 0 {
		return false
	}
	tol := min(max(toleranceBps, 0), 10000)
	lhs := new(big.Int).Mul(amount, bpsDenominator)
	rhs := new(big.Int).Mul(due, big.NewInt(int64(10000-tol)))
	return lhs.Cmp(rhs) >= 0
}

func sign(v *big.Int) int {
	if v == nil {
		return 0
	}
	return v.Sign()
}

func cmp(a, b *big.Int) int {
	if a == nil {
		a = new(big.Int)
	}
	if b == nil {
		b = new(big.Int)
	}
	return a.Cmp(b)
}

func sameState(a, b store.InvoiceState) bool {
	if a.Status != b.Status || a.Overpaid != b.Overpaid || a.PaidLate != b.PaidLate {
		return false
	}
	if (a.PaidQuoteID == nil) != (b.PaidQuoteID == nil) || (a.PaidQuoteID != nil && *a.PaidQuoteID != *b.PaidQuoteID) {
		return false
	}
	if (a.PaidAt == nil) != (b.PaidAt == nil) || (a.PaidAt != nil && !a.PaidAt.Equal(*b.PaidAt)) {
		return false
	}
	return true
}

func gracePeriod(st domain.Settings) time.Duration {
	return time.Duration(max(st.GracePeriodSeconds, 0)) * time.Second
}

// EventForStatus returns the webhook event type announcing that an invoice
// entered status ("" for pending, which has none).
func EventForStatus(status domain.InvoiceStatus) string {
	switch status {
	case domain.InvoicePartial:
		return domain.EventPaymentDetected
	case domain.InvoiceConfirming:
		return domain.EventConfirming
	case domain.InvoicePaid:
		return domain.EventPaid
	case domain.InvoiceUnderpaid:
		return domain.EventUnderpaid
	case domain.InvoiceExpired:
		return domain.EventExpired
	case domain.InvoiceCancelled:
		return domain.EventCancelled
	}
	return ""
}

// enqueueEvent writes the outbox event for inv's (new) status inside tx and
// returns its type, or "" when nothing was enqueued. The stored payload is
// the complete envelope {id, type, created_at, data: Invoice} with id = the
// event id; the URL is the merchant's webhook URL right now.
func (s *Service) enqueueEvent(ctx context.Context, tx *store.Store, inv domain.Invoice) (string, error) {
	eventType := EventForStatus(inv.Status)
	if eventType == "" {
		// Only "back to pending" (a reorg dropped every unconfirmed transfer).
		s.log.InfoContext(ctx, "invoice status change without webhook event", "invoice_id", inv.ID, "status", string(inv.Status))
		return "", nil
	}
	merchant, err := tx.GetMerchant(ctx, inv.MerchantID)
	if err != nil {
		return "", err
	}
	if merchant.WebhookURL == "" {
		return "", nil
	}
	cat, err := tx.LoadCatalog(ctx)
	if err != nil {
		return "", err
	}
	st, err := tx.GetSettings(ctx)
	if err != nil {
		return "", err
	}
	var quote *domain.Quote
	q, err := tx.GetCurrentQuote(ctx, inv.ID)
	switch {
	case err == nil:
		quote = &q
	case !errors.Is(err, store.ErrNotFound):
		return "", err
	}
	// Events of one invoice get strictly increasing created_at (and thus
	// next_attempt_at), so they sort — and are dispatched — in the order the
	// status changed even when two changes fall into the same microsecond
	// (e.g. a transfer detected and confirmed in one scanner tick). The
	// invoice row lock makes this read-then-write safe.
	createdAt := tx.Now()
	prev, err := tx.ListWebhookEventsByInvoice(ctx, inv.ID) // newest first
	if err != nil {
		return "", err
	}
	if len(prev) > 0 && !createdAt.After(prev[0].CreatedAt) {
		createdAt = prev[0].CreatedAt.Add(time.Microsecond)
	}
	id := uuid.New()
	payload, err := webhook.BuildEnvelope(id, eventType, createdAt, view.NewInvoice(inv, quote, cat, st))
	if err != nil {
		return "", fmt.Errorf("invoice: encode webhook payload: %w", err)
	}
	_, err = tx.EnqueueWebhook(ctx, domain.WebhookEvent{
		ID:            id,
		MerchantID:    inv.MerchantID,
		InvoiceID:     inv.ID,
		EventType:     eventType,
		Payload:       payload,
		URL:           merchant.WebhookURL,
		NextAttemptAt: createdAt,
		CreatedAt:     createdAt,
	})
	if err != nil {
		return "", err
	}
	return eventType, nil
}
