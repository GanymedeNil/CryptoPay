package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"cryptopay/internal/domain"
)

const invoiceCols = `i.id, i.merchant_id, i.merchant_order_id, i.title, i.description, i.price_amount::text,
	i.price_currency, i.amount_usd::text, i.status, i.overpaid, i.paid_late, i.paid_quote_id, i.success_url,
	i.cancel_url, i.customer_email, i.metadata::text, i.expires_at, i.paid_at, i.created_at, i.updated_at`

func invoiceDest(inv *domain.Invoice, status *string) []any {
	return []any{&inv.ID, &inv.MerchantID, &inv.MerchantOrderID, &inv.Title, &inv.Description,
		decScan{&inv.PriceAmount}, &inv.PriceCurrency, decPtrScan{&inv.AmountUSD}, status, &inv.Overpaid,
		&inv.PaidLate, &inv.PaidQuoteID, &inv.SuccessURL, &inv.CancelURL, &inv.CustomerEmail,
		jsonScan{&inv.Metadata}, &inv.ExpiresAt, &inv.PaidAt, &inv.CreatedAt, &inv.UpdatedAt}
}

func scanInvoice(row pgx.Row) (domain.Invoice, error) {
	var inv domain.Invoice
	var status string
	err := row.Scan(invoiceDest(&inv, &status)...)
	inv.Status = domain.InvoiceStatus(status)
	return inv, err
}

func collectInvoices(rows pgx.Rows, err error, op string) ([]domain.Invoice, error) {
	if err != nil {
		return nil, wrap(op, err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.Invoice, error) { return scanInvoice(r) })
	return nonNil(out), wrap(op, err)
}

// InvoiceView is an invoice with its merchant's name and its current quote
// (see GetCurrentQuote), as needed by the merchant, checkout and admin APIs.
type InvoiceView struct {
	domain.Invoice
	MerchantName string
	Quote        *domain.Quote // nil when the invoice has no quote yet
}

// CreateInvoice inserts an invoice. Zero ID → new v4 UUID; Status "" →
// pending; nil Metadata → {}. A non-empty MerchantOrderID already used by
// the same merchant yields ErrConflict with ConstraintInvoiceMerchantOrder
// (inside a transaction the transaction stays usable, so the caller can load
// the existing invoice with GetInvoiceByOrderID). Unknown merchant →
// ErrForeignKey; PriceAmount <= 0 → ErrInvalid.
func (s *Store) CreateInvoice(ctx context.Context, inv domain.Invoice) (domain.Invoice, error) {
	const op = "create invoice"
	if inv.Status == "" {
		inv.Status = domain.InvoicePending
	}
	if !inv.Status.Valid() {
		return domain.Invoice{}, invalidf(op, "invalid status %q", inv.Status)
	}
	if inv.PriceCurrency == "" {
		return domain.Invoice{}, invalidf(op, "price currency is required")
	}
	if inv.ExpiresAt.IsZero() {
		return domain.Invoice{}, invalidf(op, "expires_at is required")
	}
	var usd *string
	if inv.AmountUSD != nil {
		v := inv.AmountUSD.String()
		usd = &v
	}
	now := s.Now()
	var out domain.Invoice
	err := s.guarded(ctx, func(db dbtx) error {
		var err error
		out, err = scanInvoice(db.QueryRow(ctx, `
			INSERT INTO invoices AS i (id, merchant_id, merchant_order_id, title, description, price_amount,
				price_currency, amount_usd, status, overpaid, paid_late, paid_quote_id, success_url, cancel_url,
				customer_email, metadata, expires_at, paid_at, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6::numeric, $7, $8::numeric, $9, $10, $11, $12, $13, $14, $15,
				$16::jsonb, $17, $18, $19, $20)
			RETURNING `+invoiceCols,
			uuidOrNew(inv.ID), inv.MerchantID, inv.MerchantOrderID, inv.Title, inv.Description,
			inv.PriceAmount.String(), inv.PriceCurrency, usd, string(inv.Status), inv.Overpaid, inv.PaidLate,
			inv.PaidQuoteID, inv.SuccessURL, inv.CancelURL, inv.CustomerEmail, jsonText(inv.Metadata, "{}"),
			inv.ExpiresAt.UTC(), utcPtr(inv.PaidAt), timeOr(inv.CreatedAt, now), timeOr(inv.UpdatedAt, now)))
		return err
	})
	return out, wrap(op, err)
}

// GetInvoice returns an invoice by id.
func (s *Store) GetInvoice(ctx context.Context, id uuid.UUID) (domain.Invoice, error) {
	inv, err := scanInvoice(s.db.QueryRow(ctx, `SELECT `+invoiceCols+` FROM invoices i WHERE i.id = $1`, id))
	return inv, wrap("get invoice", err)
}

// GetInvoiceForUpdate returns an invoice and locks its row until the
// transaction ends (SELECT … FOR UPDATE). Every state change of an invoice
// (crediting, expiring, cancelling, creating quotes) should go through this
// lock. Must be called inside WithTx (ErrTxRequired otherwise).
func (s *Store) GetInvoiceForUpdate(ctx context.Context, id uuid.UUID) (domain.Invoice, error) {
	const op = "get invoice for update"
	if err := s.requireTx(op); err != nil {
		return domain.Invoice{}, err
	}
	inv, err := scanInvoice(s.db.QueryRow(ctx, `SELECT `+invoiceCols+` FROM invoices i WHERE i.id = $1 FOR UPDATE`, id))
	return inv, wrap(op, err)
}

// GetInvoiceByOrderID returns the merchant's invoice with the given
// (non-empty) merchant order id.
func (s *Store) GetInvoiceByOrderID(ctx context.Context, merchantID uuid.UUID, orderID string) (domain.Invoice, error) {
	const op = "get invoice by order id"
	if orderID == "" {
		return domain.Invoice{}, fmt.Errorf("store: %s: %w", op, ErrNotFound)
	}
	inv, err := scanInvoice(s.db.QueryRow(ctx, `SELECT `+invoiceCols+` FROM invoices i
		WHERE i.merchant_id = $1 AND i.merchant_order_id = $2 AND i.merchant_order_id <> ''`, merchantID, orderID))
	return inv, wrap(op, err)
}

// GetInvoiceView returns the invoice with merchant name and current quote.
func (s *Store) GetInvoiceView(ctx context.Context, id uuid.UUID) (InvoiceView, error) {
	const op = "get invoice view"
	var v InvoiceView
	var status string
	dest := append(invoiceDest(&v.Invoice, &status), &v.MerchantName)
	err := s.db.QueryRow(ctx, `SELECT `+invoiceCols+`, m.name FROM invoices i JOIN merchants m ON m.id = i.merchant_id
		WHERE i.id = $1`, id).Scan(dest...)
	if err != nil {
		return InvoiceView{}, wrap(op, err)
	}
	v.Status = domain.InvoiceStatus(status)
	q, err := s.GetCurrentQuote(ctx, id)
	switch {
	case err == nil:
		v.Quote = &q
	case !isNotFound(err):
		return InvoiceView{}, err
	}
	return v, nil
}

// InvoiceState is the mutable lifecycle state of an invoice, written as a
// whole by UpdateInvoiceState (read-modify-write under GetInvoiceForUpdate).
type InvoiceState struct {
	Status      domain.InvoiceStatus
	Overpaid    bool
	PaidLate    bool
	PaidQuoteID *uuid.UUID
	PaidAt      *time.Time
}

// StateOf returns the current lifecycle state of inv, to be modified and
// passed to UpdateInvoiceState.
func StateOf(inv domain.Invoice) InvoiceState {
	return InvoiceState{Status: inv.Status, Overpaid: inv.Overpaid, PaidLate: inv.PaidLate,
		PaidQuoteID: inv.PaidQuoteID, PaidAt: inv.PaidAt}
}

// UpdateInvoiceState writes status, overpaid, paid_late, paid_quote_id and
// paid_at and returns the updated invoice. Transition rules are the invoice
// service's job; enqueue the matching webhook in the same transaction.
func (s *Store) UpdateInvoiceState(ctx context.Context, id uuid.UUID, st InvoiceState) (domain.Invoice, error) {
	const op = "update invoice state"
	if !st.Status.Valid() {
		return domain.Invoice{}, invalidf(op, "invalid status %q", st.Status)
	}
	inv, err := scanInvoice(s.db.QueryRow(ctx, `
		UPDATE invoices i SET status = $2, overpaid = $3, paid_late = $4, paid_quote_id = $5, paid_at = $6, updated_at = $7
		WHERE i.id = $1
		RETURNING `+invoiceCols,
		id, string(st.Status), st.Overpaid, st.PaidLate, st.PaidQuoteID, utcPtr(st.PaidAt), s.Now()))
	return inv, wrap(op, err)
}

// InvoiceFilter selects invoices for ListInvoices.
type InvoiceFilter struct {
	MerchantID *uuid.UUID             // nil = all merchants
	Statuses   []domain.InvoiceStatus // empty = any
	OrderID    string                 // exact merchant order id
	// Q matches (exactly; addresses and tx hashes case-insensitively) the
	// invoice id, the merchant order id, any quote address or the hash of
	// any transfer credited to the invoice.
	Q        string
	From, To time.Time // created_at in [From, To); zero = unbounded
	Page     int       // 1-based
	PageSize int
}

// ListInvoices returns a page of invoices (with merchant name and current
// quote), newest first.
func (s *Store) ListInvoices(ctx context.Context, f InvoiceFilter) (Page[InvoiceView], error) {
	const op = "list invoices"
	page, size, offset := normPage(f.Page, f.PageSize)
	out := Page[InvoiceView]{Items: []InvoiceView{}, Page: page, PageSize: size}
	var c conds
	if f.MerchantID != nil {
		c.add("i.merchant_id = " + c.arg(*f.MerchantID))
	}
	if len(f.Statuses) > 0 {
		sts := make([]string, len(f.Statuses))
		for i, st := range f.Statuses {
			if !st.Valid() {
				return out, invalidf(op, "invalid status %q", st)
			}
			sts[i] = string(st)
		}
		c.add("i.status = ANY(" + c.arg(sts) + ")")
	}
	if f.OrderID != "" {
		// The redundant "<> ''" lets generic plans use the partial indexes.
		c.add("i.merchant_order_id = " + c.arg(f.OrderID) + " AND i.merchant_order_id <> ''")
	}
	if q := strings.TrimSpace(f.Q); q != "" {
		p := c.arg(q)
		sub := `SELECT qq.invoice_id FROM quotes qq WHERE lower(qq.address) = lower(` + p + `)
			UNION ALL SELECT tt.invoice_id FROM transfers tt WHERE lower(tt.tx_hash) = lower(` + p + `) AND tt.invoice_id IS NOT NULL
			UNION ALL SELECT ii.id FROM invoices ii WHERE ii.merchant_order_id = ` + p + ` AND ii.merchant_order_id <> ''`
		if id, err := uuid.Parse(q); err == nil {
			sub += ` UNION ALL SELECT ` + c.arg(id) + `::uuid`
		}
		c.add("i.id IN (" + sub + ")")
	}
	if !f.From.IsZero() {
		c.add("i.created_at >= " + c.arg(f.From.UTC()))
	}
	if !f.To.IsZero() {
		c.add("i.created_at < " + c.arg(f.To.UTC()))
	}
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM invoices i`+c.where(), c.args...).Scan(&out.Total); err != nil {
		return out, wrap(op, err)
	}
	if out.Total == 0 || offset >= out.Total {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `SELECT `+invoiceCols+`, m.name FROM invoices i JOIN merchants m ON m.id = i.merchant_id`+
		c.where()+fmt.Sprintf(` ORDER BY i.created_at DESC, i.id DESC LIMIT %d OFFSET %d`, size, offset), c.args...)
	if err != nil {
		return out, wrap(op, err)
	}
	items, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (InvoiceView, error) {
		var v InvoiceView
		var status string
		err := r.Scan(append(invoiceDest(&v.Invoice, &status), &v.MerchantName)...)
		v.Status = domain.InvoiceStatus(status)
		return v, err
	})
	if err != nil {
		return out, wrap(op, err)
	}
	ids := make([]uuid.UUID, len(items))
	for i := range items {
		ids[i] = items[i].ID
	}
	quotes, err := s.CurrentQuotes(ctx, ids)
	if err != nil {
		return out, err
	}
	for i := range items {
		if q, ok := quotes[items[i].ID]; ok {
			items[i].Quote = &q
		}
	}
	out.Items = items
	return out, nil
}

// DefaultExpiryStatuses are the statuses ListInvoicesToExpire considers when
// none are given: pending → expired, partial → underpaid. (confirming
// invoices already received the full amount and wait for confirmations.)
var DefaultExpiryStatuses = []domain.InvoiceStatus{domain.InvoicePending, domain.InvoicePartial}

// ListInvoicesToExpire returns up to limit invoices whose expires_at <= now
// and whose status is one of statuses (default DefaultExpiryStatuses),
// oldest expiry first. The rows are selected FOR UPDATE SKIP LOCKED: inside
// WithTx they stay locked until commit and concurrent workers get disjoint
// batches. (Outside a transaction the lock is released immediately; then
// re-check each invoice under GetInvoiceForUpdate.)
func (s *Store) ListInvoicesToExpire(ctx context.Context, now time.Time, limit int, statuses ...domain.InvoiceStatus) ([]domain.Invoice, error) {
	const op = "list invoices to expire"
	if len(statuses) == 0 {
		statuses = DefaultExpiryStatuses
	}
	sts := make([]string, len(statuses))
	for i, st := range statuses {
		if !st.Valid() {
			return nil, invalidf(op, "invalid status %q", st)
		}
		sts[i] = string(st)
	}
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(ctx, `SELECT `+invoiceCols+` FROM invoices i
		WHERE i.status = ANY($1) AND i.expires_at <= $2
		ORDER BY i.expires_at, i.id LIMIT $3 FOR UPDATE SKIP LOCKED`, sts, now.UTC(), limit)
	return collectInvoices(rows, err, op)
}
