package invoice

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"cryptopay/internal/domain"
	"cryptopay/internal/store"
)

// defaultExpireBatch is how many due invoices one ExpireDue transaction
// handles (and keeps locked while it does).
const defaultExpireBatch = 100

// expireMaxRounds bounds the batches per status one ExpireDue call works
// through; the next run continues.
const expireMaxRounds = 100

// Cancel cancels an invoice that is still pending (no funds seen) and
// enqueues invoice.cancelled. Any other status → ErrConflict; unknown
// invoice → ErrNotFound. actor names who did it (for the log; the audit
// trail is the API layer's job).
//
// The invoice's quotes stay watched until their watch_until: a payment that
// was already on its way is still recorded (visible in the admin console as
// a transfer of the invoice) although the invoice remains cancelled.
func (s *Service) Cancel(ctx context.Context, invoiceID uuid.UUID, actor string) (store.InvoiceView, error) {
	err := s.store.WithTx(ctx, func(tx *store.Store) error {
		inv, err := tx.GetInvoiceForUpdate(ctx, invoiceID)
		if err != nil {
			return mapNotFound(err)
		}
		if inv.Status != domain.InvoicePending {
			return fmt.Errorf("%w: only pending invoices can be cancelled (status %s)", ErrConflict, inv.Status)
		}
		_, err = s.apply(ctx, tx, inv, store.InvoiceState{Status: domain.InvoiceCancelled}, nil)
		return err
	})
	if err != nil {
		return store.InvoiceView{}, err
	}
	s.log.InfoContext(ctx, "invoice cancelled", "invoice_id", invoiceID, "actor", actor)
	return s.GetView(ctx, invoiceID)
}

// MarkPaid is the admin's manual resolution: it moves the invoice to paid
// regardless of the funds recorded (e.g. an underpaid or expired invoice
// settled out of band) and enqueues invoice.paid. Unknown invoice →
// ErrNotFound. Refused with ErrConflict when the invoice is already paid,
// or cancelled without any funds recorded on it; a cancelled invoice that
// did receive funds (the customer paid anyway) can be resolved as paid.
// paid_late is set when this happens after expires_at. The quote that
// received the most becomes the paid quote (the active or newest one when
// nothing was received; none if the invoice has no quotes), all other live
// quotes are closed. note and actor go to the log only (the audit trail is
// the API layer's job).
func (s *Service) MarkPaid(ctx context.Context, invoiceID uuid.UUID, note, actor string) (store.InvoiceView, error) {
	err := s.store.WithTx(ctx, func(tx *store.Store) error {
		inv, err := tx.GetInvoiceForUpdate(ctx, invoiceID)
		if err != nil {
			return mapNotFound(err)
		}
		if inv.Status == domain.InvoicePaid {
			return fmt.Errorf("%w: invoice is already paid", ErrConflict)
		}
		quotes, err := tx.ListQuotesByInvoice(ctx, invoiceID)
		if err != nil {
			return err
		}
		if inv.Status == domain.InvoiceCancelled && !anyFunds(quotes) {
			return fmt.Errorf("%w: invoice is cancelled and received no funds", ErrConflict)
		}
		var best *domain.Quote
		for i := range quotes {
			q := &quotes[i]
			if best == nil || betterPaidQuote(q, best) {
				best = q
			}
		}
		now := s.clock()
		next := store.InvoiceState{Status: domain.InvoicePaid, PaidAt: &now, PaidLate: !now.Before(inv.ExpiresAt)}
		if best != nil {
			id := best.ID
			next.PaidQuoteID = &id
			next.Overpaid = cmp(best.AmountReceived, best.AmountDue) > 0
		}
		_, err = s.apply(ctx, tx, inv, next, best)
		return err
	})
	if err != nil {
		return store.InvoiceView{}, err
	}
	s.log.InfoContext(ctx, "invoice marked paid manually", "invoice_id", invoiceID, "actor", actor, "note", note)
	return s.GetView(ctx, invoiceID)
}

// anyFunds reports whether any quote has received (non-orphaned) funds.
func anyFunds(quotes []domain.Quote) bool {
	for i := range quotes {
		if sign(quotes[i].AmountReceived) > 0 {
			return true
		}
	}
	return false
}

// betterPaidQuote reports whether a is a better "paid quote" than b for a
// manual resolution: more confirmed, then more received, then active, then
// newer.
func betterPaidQuote(a, b *domain.Quote) bool {
	if c := cmp(a.AmountConfirmed, b.AmountConfirmed); c != 0 {
		return c > 0
	}
	if c := cmp(a.AmountReceived, b.AmountReceived); c != 0 {
		return c > 0
	}
	if (a.Status == domain.QuoteActive) != (b.Status == domain.QuoteActive) {
		return a.Status == domain.QuoteActive
	}
	return a.CreatedAt.After(b.CreatedAt)
}

// AssignTransfer attaches an unmatched transfer to an invoice (admin
// action) and re-evaluates the invoice, so a confirmed transfer of the full
// amount pays it — also after expiry or the grace period, it is the admin's
// call. The invoice must have a quote for the transfer's chain and asset
// (ErrNoMatchingQuote otherwise; a transfer of an unknown token can never be
// assigned); among several the paid, then active, then superseded, then
// newest quote is used. A transfer that is already matched, or was dropped
// by a reorg (orphaned), → ErrConflict; unknown transfer or invoice →
// ErrNotFound. A cancelled or paid invoice keeps its status; the transfer
// is recorded on it (a cancelled one can then be resolved with MarkPaid).
// A pending transfer only counts as received until the scanner confirms it.
func (s *Service) AssignTransfer(ctx context.Context, transferID, invoiceID uuid.UUID) (domain.Transfer, error) {
	var out domain.Transfer
	var ev Evaluation
	err := s.store.WithTx(ctx, func(tx *store.Store) error {
		ev = Evaluation{} // WithTx may run this more than once
		// Lock order: the invoice first (as everywhere), then the transfer.
		if _, err := tx.GetInvoiceForUpdate(ctx, invoiceID); err != nil {
			return mapNotFound(err)
		}
		t, err := tx.GetTransferForUpdate(ctx, transferID)
		if err != nil {
			return mapNotFound(err)
		}
		if t.QuoteID != nil || t.InvoiceID != nil {
			return fmt.Errorf("%w: transfer is already assigned", ErrConflict)
		}
		if t.Status == domain.TransferOrphaned {
			return fmt.Errorf("%w: transfer was dropped by a chain reorganisation", ErrConflict)
		}
		if t.AssetID == "" {
			return fmt.Errorf("%w: the transfer's token is not a configured asset", ErrNoMatchingQuote)
		}
		quotes, err := tx.ListQuotesByInvoice(ctx, invoiceID)
		if err != nil {
			return err
		}
		var target *domain.Quote
		for i := range quotes {
			q := &quotes[i]
			if q.ChainID != t.ChainID || q.AssetID != t.AssetID {
				continue
			}
			if target == nil || assignRank(q.Status) < assignRank(target.Status) ||
				(assignRank(q.Status) == assignRank(target.Status) && q.CreatedAt.After(target.CreatedAt)) {
				target = q
			}
		}
		if target == nil {
			return ErrNoMatchingQuote
		}
		if out, err = tx.AssignTransfer(ctx, transferID, target.ID, invoiceID); err != nil {
			if errors.Is(err, store.ErrConflict) {
				return fmt.Errorf("%w: transfer is already assigned", ErrConflict)
			}
			return mapNotFound(err)
		}
		ev, err = s.Reevaluate(ctx, tx, invoiceID)
		return err
	})
	if err != nil {
		return domain.Transfer{}, err
	}
	s.Committed(ctx, ev)
	s.log.InfoContext(ctx, "transfer assigned manually", "transfer_id", transferID, "invoice_id", invoiceID)
	return out, nil
}

func assignRank(st domain.QuoteStatus) int {
	switch st {
	case domain.QuotePaid:
		return 0
	case domain.QuoteActive:
		return 1
	case domain.QuoteSuperseded:
		return 2
	}
	return 3
}

// ExpireResult summarises one ExpireDue run.
type ExpireResult struct {
	Examined     int   // due invoices looked at
	Changed      int   // invoices whose status changed (expired / underpaid / …)
	QuotesClosed int64 // quotes closed because their watch_until passed
}

// ExpireDue is the periodic expiry job. It re-evaluates the open invoices
// whose expires_at has passed and closes the quotes whose watch_until has
// passed (which ends the grace period and frees static-wallet amounts):
//
//   - pending → expired (no funds);
//   - partial → underpaid once all its funds are confirmed; while some are
//     unconfirmed the invoice stays partial and the scanner finalises it
//     when they confirm or are orphaned;
//   - confirming invoices are not touched: they wait for confirmations and
//     the scanner moves them on (paid with paid_late, or back after a reorg).
//
// Candidates are processed in transactions of up to 100 invoices
// selected with FOR UPDATE SKIP LOCKED (so concurrent instances work on
// disjoint batches, and invoices the scanner is crediting right now are
// left for the next run) and paged with a keyset cursor (so invoices that
// legitimately stay partial are not read over and over). Each invoice is
// re-evaluated under its row lock in its own savepoint: a second worker
// finds nothing left to change, no event is enqueued twice, and a failure
// on one invoice does not stop the others; the first such error is returned
// after the run.
func (s *Service) ExpireDue(ctx context.Context) (ExpireResult, error) {
	var res ExpireResult
	var errs []error
	now := s.clock()
	for _, status := range []domain.InvoiceStatus{domain.InvoicePending, domain.InvoicePartial} {
		var after domain.Invoice // keyset cursor; zero = from the start
		for range expireMaxRounds {
			var batch []domain.Invoice
			var examined, changed int
			var failed []error
			var evs []Evaluation
			err := s.store.WithTx(ctx, func(tx *store.Store) error {
				// Reset per attempt: WithTx may retry the whole function.
				examined, changed, failed, evs = 0, 0, nil, nil
				var err error
				batch, err = tx.ListInvoicesToExpireAfter(ctx, now, after.ExpiresAt, after.ID, s.expireBatch, status)
				if err != nil {
					return err
				}
				for _, inv := range batch {
					examined++
					var ev Evaluation
					err := tx.WithTx(ctx, func(sp *store.Store) error { // savepoint
						var err error
						ev, err = s.Reevaluate(ctx, sp, inv.ID)
						return err
					})
					if err != nil {
						if cerr := ctx.Err(); cerr != nil {
							return cerr
						}
						failed = append(failed, fmt.Errorf("invoice: expire %s: %w", inv.ID, err))
						continue
					}
					if ev.StatusChanged {
						changed++
						evs = append(evs, ev)
					}
				}
				return nil
			})
			if err != nil {
				return res, fmt.Errorf("invoice: expire due invoices: %w", err)
			}
			s.Committed(ctx, evs...)
			res.Examined += examined
			res.Changed += changed
			for _, ferr := range failed {
				s.log.ErrorContext(ctx, "expire invoice failed", "error", ferr)
			}
			errs = append(errs, failed...)
			if len(batch) < s.expireBatch {
				break
			}
			after = batch[len(batch)-1]
		}
	}
	// One statement, but run it in a transaction so a deadlock with a
	// concurrent crediting transaction (which updates the same quote rows)
	// is retried by WithTx instead of failing the run.
	err := s.store.WithTx(ctx, func(tx *store.Store) error {
		var err error
		res.QuotesClosed, err = tx.CloseExpiredQuotes(ctx, now)
		return err
	})
	if err != nil {
		return res, fmt.Errorf("invoice: close expired quotes: %w", err)
	}
	if res.Changed > 0 || res.QuotesClosed > 0 {
		s.log.InfoContext(ctx, "expiry run", "examined", res.Examined, "changed", res.Changed, "quotes_closed", res.QuotesClosed)
	}
	if len(errs) > 0 {
		return res, errs[0]
	}
	return res, nil
}
