// Package invoice implements the money path of the gateway: creating
// invoices, quoting them in a crypto asset, and the payment state machine
// that credits on-chain transfers and moves an invoice through its
// lifecycle (docs/architecture.md, "Invoice lifecycle").
//
// # State machine
//
// The status of an invoice is a pure function of (a) the funds recorded for
// its quotes, (b) the current time relative to expires_at and (c) the two
// terminal decisions paid and cancelled. Reevaluate recomputes it under the
// invoice row lock; it is the only place a status is derived, and it is used
// by the scanner (after inserting / confirming / orphaning a transfer), by
// AssignTransfer and by ExpireDue:
//
//	funds on a quote (pending+confirmed = "received")      before expires_at   at/after expires_at
//	-----------------------------------------------------  ------------------  --------------------------
//	confirmed ≥ due × (1 − tolerance)                       paid                paid (paid_late = true)
//	received  ≥ due × (1 − tolerance), not yet confirmed    confirming          confirming (not finalised)
//	0 < received < threshold, some of it unconfirmed        partial             partial (not finalised)
//	0 < received < threshold, all of it confirmed           partial             underpaid
//	nothing                                                 pending             expired
//
// "Received" counts pending and confirmed transfers, "confirmed" only
// confirmed ones; orphaned transfers never count. The threshold is
// due × (1 − underpay_tolerance_bps/10000); quotes of different assets are
// never added up — each quote is judged on its own (the active quote first).
//
// paid is terminal (paid_at, paid_quote_id are set, the quote becomes
// "paid", the other live quotes are closed). paid_late is set when the
// confirmation that pays the invoice happens at or after expires_at — also
// when the full amount was already seen (confirming) before expiry.
// cancelled is terminal too: funds arriving for a cancelled invoice are
// still recorded on its quotes (they stay watched until watch_until) but the
// status stays cancelled; the admin may then resolve it with MarkPaid.
// expired and underpaid are not final while the quotes are watched (grace
// period): a late payment moves the invoice on (partial / confirming /
// paid with paid_late = true), and a reorg that drops unconfirmed funds
// moves it back. overpaid is set when the quote that carries the payment
// received more than its amount due.
//
// # Webhook events
//
// Every status change enqueues exactly one outbox event in the same
// transaction; the event type is determined by the new status alone
// (EventForStatus):
//
//	partial → invoice.payment_detected     underpaid → invoice.underpaid
//	confirming → invoice.confirming        expired   → invoice.expired
//	paid → invoice.paid                    cancelled → invoice.cancelled
//
// So an invoice whose first transfer already covers the amount emits only
// invoice.confirming (no separate payment_detected), and a reorg that takes
// a confirming invoice back to partial emits invoice.payment_detected again
// with the reduced amounts. The single exception is a change back to
// pending, which only a reorg removing every unconfirmed transfer can cause:
// docs/api.md has no event type for it, so nothing is emitted and the
// merchant hears about the invoice again with its next status change (a new
// payment, or invoice.expired). Changes that do not alter the status (e.g.
// the overpaid flag, a second partial transfer) emit nothing. Because the
// decision is taken under the invoice row lock from the stored status,
// re-running Reevaluate / ExpireDue or re-scanning blocks never emits an
// event twice. No event is enqueued when the merchant has no webhook_url at
// that moment; the stored payload is webhook.BuildEnvelope's
// {id, type, created_at, data: view.Invoice} with id = the event id.
//
// # Locking
//
// All state changes of an invoice happen in a transaction holding the
// invoice row lock (store.GetInvoiceForUpdate), always taken first; quote and
// transfer rows of the invoice are only written while it is held. The only
// writers without it touch a single table (the scanner on unmatched
// transfers and confirmation counters, ExpireDue closing quotes past
// watch_until), so no lock cycle can form; a deadlock with the latter is
// retried by store.WithTx.
//
// # Alerts
//
// An invoice that becomes underpaid is reported to the alert.Notifier given
// to SetNotifier — only after the transaction that changed it committed
// (Committed), so an alert can neither roll back nor delay the change, and
// a rolled back change reports nothing.
package invoice

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"cryptopay/internal/alert"
	"cryptopay/internal/domain"
	"cryptopay/internal/rates"
	"cryptopay/internal/store"
	"cryptopay/internal/view"
)

// Service is the invoice and quote service. It is safe for concurrent use.
type Service struct {
	store *store.Store
	rates rates.Provider
	log   *slog.Logger
	now   func() time.Time

	expireBatch int            // invoices per ExpireDue transaction (tests lower it)
	notifier    alert.Notifier // nil = no alerts
}

// New returns a Service. logger nil → slog.Default(); now nil → time.Now.
// now drives every time-dependent decision (expiry, grace period); give the
// store the same clock (store.WithClock) so written timestamps agree.
func New(st *store.Store, rp rates.Provider, logger *slog.Logger, now func() time.Time) *Service {
	if st == nil {
		panic("invoice: nil store")
	}
	if rp == nil {
		panic("invoice: nil rates provider")
	}
	if logger == nil {
		logger = slog.Default()
	}
	if now == nil {
		now = time.Now
	}
	return &Service{store: st, rates: rp, log: logger, now: now, expireBatch: defaultExpireBatch}
}

func (s *Service) clock() time.Time { return s.now().UTC().Truncate(time.Microsecond) }

// SetNotifier sets where operational alerts go (nil = nowhere). Call it
// before the service is used. n must not block (use an alert.Queue).
func (s *Service) SetNotifier(n alert.Notifier) { s.notifier = n }

// Committed reports the alert-worthy outcomes of evaluations whose
// transaction has committed: an invoice that just became underpaid. Call it
// after store.WithTx returned nil for every Evaluation obtained inside it.
// It never blocks on, fails or panics into the caller.
func (s *Service) Committed(ctx context.Context, evs ...Evaluation) {
	for _, ev := range evs {
		if ev.StatusChanged && ev.Invoice.Status == domain.InvoiceUnderpaid {
			inv := ev.Invoice
			alert.Notify(s.notifier, s.log, func(n alert.Notifier) { n.InvoiceUnderpaid(ctx, inv) })
		}
	}
}

// Details is an invoice with everything recorded for it.
type Details struct {
	store.InvoiceView                   // invoice, merchant name, current quote
	Quotes            []domain.Quote    // all quotes, oldest first
	Transfers         []domain.Transfer // transfers credited to the invoice, newest first
}

// GetView returns the invoice with its merchant's name and current quote
// (the paid quote, else the active one, else the newest). ErrNotFound if it
// does not exist.
func (s *Service) GetView(ctx context.Context, id uuid.UUID) (store.InvoiceView, error) {
	v, err := s.store.GetInvoiceView(ctx, id)
	if err != nil {
		return store.InvoiceView{}, mapNotFound(err)
	}
	return v, nil
}

// GetViewForMerchant is GetView restricted to one merchant: an invoice of
// another merchant yields ErrNotFound.
func (s *Service) GetViewForMerchant(ctx context.Context, merchantID, id uuid.UUID) (store.InvoiceView, error) {
	v, err := s.GetView(ctx, id)
	if err != nil {
		return store.InvoiceView{}, err
	}
	if v.MerchantID != merchantID {
		return store.InvoiceView{}, ErrNotFound
	}
	return v, nil
}

// Get returns the invoice with its current quote, all its quotes and the
// transfers credited to it. ErrNotFound if it does not exist.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (Details, error) {
	v, err := s.GetView(ctx, id)
	if err != nil {
		return Details{}, err
	}
	d := Details{InvoiceView: v}
	if d.Quotes, err = s.store.ListQuotesByInvoice(ctx, id); err != nil {
		return Details{}, err
	}
	if d.Transfers, err = s.store.ListTransfersByInvoice(ctx, id); err != nil {
		return Details{}, err
	}
	return d, nil
}

// Checkout is everything the public checkout page shows (docs/api.md,
// `CheckoutView`); View renders it.
type Checkout struct {
	store.InvoiceView                   // invoice, merchant name, current quote
	Transfers         []domain.Transfer // transfers credited to the invoice, newest first
	Estimates         []Estimate        // every asset payable right now for the merchant, in display order
	Catalog           *store.Catalog
	Settings          domain.Settings
	Now               time.Time // server time (the service clock)
}

// Checkout loads the checkout data of an invoice. ErrNotFound if it does
// not exist. Estimates are computed for every asset payable for the
// invoice's merchant whatever the invoice status (null without a rate);
// nothing is created.
func (s *Service) Checkout(ctx context.Context, id uuid.UUID) (Checkout, error) {
	v, err := s.GetView(ctx, id)
	if err != nil {
		return Checkout{}, err
	}
	c := Checkout{InvoiceView: v, Now: s.clock()}
	if c.Transfers, err = s.store.ListTransfersByInvoice(ctx, id); err != nil {
		return Checkout{}, err
	}
	if c.Estimates, err = s.Estimates(ctx, v.Invoice); err != nil {
		return Checkout{}, err
	}
	if c.Catalog, err = s.store.LoadCatalog(ctx); err != nil {
		return Checkout{}, err
	}
	if c.Settings, err = s.store.GetSettings(ctx); err != nil {
		return Checkout{}, err
	}
	return c, nil
}

// View renders c as the `CheckoutView` JSON document.
func (c Checkout) View() view.CheckoutView {
	assets := make([]view.CheckoutAsset, 0, len(c.Estimates))
	for _, e := range c.Estimates {
		assets = append(assets, view.NewCheckoutAsset(e.Asset, e.Amount, c.Catalog))
	}
	return view.NewCheckoutView(c.InvoiceView, assets, c.Transfers, c.Catalog, c.Settings, c.Now)
}

// mapNotFound converts the store's not-found error into ErrNotFound.
func mapNotFound(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("%w (%v)", ErrNotFound, err)
	}
	return err
}
