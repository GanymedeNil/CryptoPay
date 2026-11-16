package store

import (
	"context"
	"math/big"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"cryptopay/internal/domain"
)

const quoteCols = `q.id, q.invoice_id, q.asset_id, q.chain_id, q.wallet_id, q.wallet_kind, q.address, q.address_index,
	q.rate::text, q.amount_due::text, q.amount_received::text, q.amount_confirmed::text, q.status, q.watch_until,
	q.created_at, q.updated_at`

func quoteDest(x *domain.Quote, kind, status *string) []any {
	return []any{&x.ID, &x.InvoiceID, &x.AssetID, &x.ChainID, &x.WalletID, kind, &x.Address,
		u32PtrScan{&x.AddressIndex}, decScan{&x.Rate}, bigScan{&x.AmountDue}, bigScan{&x.AmountReceived},
		bigScan{&x.AmountConfirmed}, status, &x.WatchUntil, &x.CreatedAt, &x.UpdatedAt}
}

func scanQuote(row pgx.Row) (domain.Quote, error) {
	var x domain.Quote
	var kind, status string
	err := row.Scan(quoteDest(&x, &kind, &status)...)
	x.WalletKind, x.Status = domain.WalletKind(kind), domain.QuoteStatus(status)
	return x, err
}

func collectQuotes(rows pgx.Rows, err error, op string) ([]domain.Quote, error) {
	if err != nil {
		return nil, wrap(op, err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.Quote, error) { return scanQuote(r) })
	return nonNil(out), wrap(op, err)
}

func validQuoteStatus(st domain.QuoteStatus) bool {
	switch st {
	case domain.QuoteActive, domain.QuoteSuperseded, domain.QuotePaid, domain.QuoteClosed:
		return true
	}
	return false
}

// liveQuote is the SQL predicate of a watched quote status.
const liveQuote = `q.status IN ('active', 'superseded')`

// CreateQuote inserts a quote. Zero ID → generated; Status "" → active;
// nil AmountReceived/AmountConfirmed → 0. Unique violations yield
// ErrConflict whose ConstraintName tells the cause:
//
//   - ConstraintQuoteStaticAmount: another live quote on the same static
//     address/asset already uses AmountDue — bump the amount and retry;
//   - ConstraintQuoteInvoiceAssetLive: the invoice already has a live quote
//     for this asset (reuse it via GetLiveQuote).
//
// Inside a transaction the insert runs under a savepoint, so a violation
// does not abort the caller's transaction and the retry loop can continue.
func (s *Store) CreateQuote(ctx context.Context, x domain.Quote) (domain.Quote, error) {
	const op = "create quote"
	if x.Status == "" {
		x.Status = domain.QuoteActive
	}
	if !validQuoteStatus(x.Status) {
		return domain.Quote{}, invalidf(op, "invalid status %q", x.Status)
	}
	if x.WalletKind != domain.WalletXpub && x.WalletKind != domain.WalletStatic {
		return domain.Quote{}, invalidf(op, "invalid wallet kind %q", x.WalletKind)
	}
	if x.AmountDue == nil || x.AmountDue.Sign() <= 0 {
		return domain.Quote{}, invalidf(op, "amount due must be positive")
	}
	if x.Address == "" || x.WatchUntil.IsZero() {
		return domain.Quote{}, invalidf(op, "address and watch_until are required")
	}
	var idx *int64
	if x.AddressIndex != nil {
		v := int64(*x.AddressIndex)
		idx = &v
	}
	now := s.Now()
	var out domain.Quote
	err := s.guarded(ctx, func(db dbtx) error {
		var err error
		out, err = scanQuote(db.QueryRow(ctx, `
			INSERT INTO quotes AS q (id, invoice_id, asset_id, chain_id, wallet_id, wallet_kind, address, address_index,
				rate, amount_due, amount_received, amount_confirmed, status, watch_until, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::numeric, $10::numeric, $11::numeric, $12::numeric, $13, $14, $15, $16)
			RETURNING `+quoteCols,
			uuidOrNew(x.ID), x.InvoiceID, x.AssetID, x.ChainID, x.WalletID, string(x.WalletKind), x.Address, idx,
			x.Rate.String(), numText(x.AmountDue), numText(x.AmountReceived), numText(x.AmountConfirmed),
			string(x.Status), x.WatchUntil.UTC(), timeOr(x.CreatedAt, now), timeOr(x.UpdatedAt, now)))
		return err
	})
	return out, wrap(op, err)
}

// GetQuote returns a quote by id.
func (s *Store) GetQuote(ctx context.Context, id uuid.UUID) (domain.Quote, error) {
	x, err := scanQuote(s.db.QueryRow(ctx, `SELECT `+quoteCols+` FROM quotes q WHERE q.id = $1`, id))
	return x, wrap("get quote", err)
}

// GetQuoteForUpdate returns a quote and locks its row until the transaction
// ends. Must be called inside WithTx (ErrTxRequired otherwise). Lock order
// convention: invoice first, then its quotes.
func (s *Store) GetQuoteForUpdate(ctx context.Context, id uuid.UUID) (domain.Quote, error) {
	const op = "get quote for update"
	if err := s.requireTx(op); err != nil {
		return domain.Quote{}, err
	}
	x, err := scanQuote(s.db.QueryRow(ctx, `SELECT `+quoteCols+` FROM quotes q WHERE q.id = $1 FOR UPDATE`, id))
	return x, wrap(op, err)
}

// ListQuotesByInvoice returns all quotes of an invoice, oldest first.
func (s *Store) ListQuotesByInvoice(ctx context.Context, invoiceID uuid.UUID) ([]domain.Quote, error) {
	rows, err := s.db.Query(ctx, `SELECT `+quoteCols+` FROM quotes q WHERE q.invoice_id = $1 ORDER BY q.created_at, q.id`, invoiceID)
	return collectQuotes(rows, err, "list quotes by invoice")
}

// GetLiveQuote returns the invoice's live (active or superseded) quote for
// an asset; there is at most one.
func (s *Store) GetLiveQuote(ctx context.Context, invoiceID uuid.UUID, assetID string) (domain.Quote, error) {
	x, err := scanQuote(s.db.QueryRow(ctx, `SELECT `+quoteCols+` FROM quotes q
		WHERE q.invoice_id = $1 AND q.asset_id = $2 AND `+liveQuote, invoiceID, assetID))
	return x, wrap("get live quote", err)
}

// GetActiveQuote returns the quote currently offered to the customer
// (status active; the newest if several).
func (s *Store) GetActiveQuote(ctx context.Context, invoiceID uuid.UUID) (domain.Quote, error) {
	x, err := scanQuote(s.db.QueryRow(ctx, `SELECT `+quoteCols+` FROM quotes q
		WHERE q.invoice_id = $1 AND q.status = 'active' ORDER BY q.created_at DESC, q.id DESC LIMIT 1`, invoiceID))
	return x, wrap("get active quote", err)
}

// currentQuoteOrder ranks an invoice's quotes for "current quote": the paid
// quote, else the newest active quote, else the newest quote of any status.
const currentQuoteOrder = `COALESCE(q.id = i.paid_quote_id, false) DESC, (q.status = 'active') DESC, q.created_at DESC, q.id DESC`

// GetCurrentQuote returns the quote to show with an invoice: the paid quote
// (invoices.paid_quote_id) if set, else the newest active quote, else the
// newest quote of any status (e.g. the closed quote of an expired invoice,
// which still carries the received amounts). ErrNotFound if the invoice has
// no quotes.
func (s *Store) GetCurrentQuote(ctx context.Context, invoiceID uuid.UUID) (domain.Quote, error) {
	x, err := scanQuote(s.db.QueryRow(ctx, `SELECT `+quoteCols+` FROM quotes q JOIN invoices i ON i.id = q.invoice_id
		WHERE q.invoice_id = $1 ORDER BY `+currentQuoteOrder+` LIMIT 1`, invoiceID))
	return x, wrap("get current quote", err)
}

// CurrentQuotes returns GetCurrentQuote for many invoices in one query,
// keyed by invoice id (invoices without quotes are absent).
func (s *Store) CurrentQuotes(ctx context.Context, invoiceIDs []uuid.UUID) (map[uuid.UUID]domain.Quote, error) {
	out := make(map[uuid.UUID]domain.Quote, len(invoiceIDs))
	if len(invoiceIDs) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `SELECT DISTINCT ON (q.invoice_id) `+quoteCols+`
		FROM quotes q JOIN invoices i ON i.id = q.invoice_id
		WHERE q.invoice_id = ANY($1)
		ORDER BY q.invoice_id, `+currentQuoteOrder, invoiceIDs)
	quotes, err := collectQuotes(rows, err, "current quotes")
	if err != nil {
		return nil, err
	}
	for _, x := range quotes {
		out[x.InvoiceID] = x
	}
	return out, nil
}

// FindInvoiceAddress returns the address (and derivation index, nil for
// static wallets) an earlier quote of the invoice got from the wallet, so a
// new quote on another chain of the same wallet reuses it. ErrNotFound if
// no quote of the invoice uses the wallet yet.
func (s *Store) FindInvoiceAddress(ctx context.Context, invoiceID, walletID uuid.UUID) (string, *uint32, error) {
	var addr string
	var idx *uint32
	err := s.db.QueryRow(ctx, `SELECT q.address, q.address_index FROM quotes q
		WHERE q.invoice_id = $1 AND q.wallet_id = $2 ORDER BY q.created_at, q.id LIMIT 1`,
		invoiceID, walletID).Scan(&addr, u32PtrScan{&idx})
	if err != nil {
		return "", nil, wrap("find invoice address", err)
	}
	return addr, idx, nil
}

// StaticAmountInUse reports whether a live quote already uses amount on a
// static address for the asset (a cheap pre-check; CreateQuote's
// ConstraintQuoteStaticAmount violation remains the authority).
func (s *Store) StaticAmountInUse(ctx context.Context, chainID, assetID, address string, amount *big.Int) (bool, error) {
	var used bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM quotes q
		WHERE q.chain_id = $1 AND q.asset_id = $2 AND q.address = $3 AND q.amount_due = $4::numeric
		  AND q.wallet_kind = 'static' AND `+liveQuote+`)`,
		chainID, assetID, address, numText(amount)).Scan(&used)
	return used, wrap("static amount in use", err)
}

// SetQuoteStatus sets a quote's status and returns the quote.
func (s *Store) SetQuoteStatus(ctx context.Context, id uuid.UUID, status domain.QuoteStatus) (domain.Quote, error) {
	const op = "set quote status"
	if !validQuoteStatus(status) {
		return domain.Quote{}, invalidf(op, "invalid status %q", status)
	}
	x, err := scanQuote(s.db.QueryRow(ctx, `UPDATE quotes q SET status = $2, updated_at = $3 WHERE q.id = $1
		RETURNING `+quoteCols, id, string(status), s.Now()))
	return x, wrap(op, err)
}

// SupersedeActiveQuotes marks the invoice's active quotes superseded (they
// stay watched until watch_until), e.g. before creating a quote for another
// asset. Returns the number of quotes changed.
func (s *Store) SupersedeActiveQuotes(ctx context.Context, invoiceID uuid.UUID) (int64, error) {
	tag, err := s.db.Exec(ctx, `UPDATE quotes SET status = 'superseded', updated_at = $2
		WHERE invoice_id = $1 AND status = 'active'`, invoiceID, s.Now())
	if err != nil {
		return 0, wrap("supersede active quotes", err)
	}
	return tag.RowsAffected(), nil
}

// CloseLiveQuotes closes the invoice's live quotes except keepID (uuid.Nil
// = none), e.g. when the invoice is cancelled. Returns the number closed.
func (s *Store) CloseLiveQuotes(ctx context.Context, invoiceID, keepID uuid.UUID) (int64, error) {
	tag, err := s.db.Exec(ctx, `UPDATE quotes q SET status = 'closed', updated_at = $3
		WHERE q.invoice_id = $1 AND q.id <> $2 AND `+liveQuote, invoiceID, keepID, s.Now())
	if err != nil {
		return 0, wrap("close live quotes", err)
	}
	return tag.RowsAffected(), nil
}

// UpdateQuoteAmounts sets the received / confirmed amounts (base units).
func (s *Store) UpdateQuoteAmounts(ctx context.Context, id uuid.UUID, received, confirmed *big.Int) (domain.Quote, error) {
	const op = "update quote amounts"
	if (received != nil && received.Sign() < 0) || (confirmed != nil && confirmed.Sign() < 0) {
		return domain.Quote{}, invalidf(op, "amounts must not be negative")
	}
	x, err := scanQuote(s.db.QueryRow(ctx, `UPDATE quotes q
		SET amount_received = $2::numeric, amount_confirmed = $3::numeric, updated_at = $4
		WHERE q.id = $1 RETURNING `+quoteCols, id, numText(received), numText(confirmed), s.Now()))
	return x, wrap(op, err)
}

// RecomputeQuoteAmounts sets amount_received (pending + confirmed
// transfers) and amount_confirmed (confirmed transfers) from the transfers
// assigned to the quote — orphaned transfers do not count — and returns the
// updated quote.
func (s *Store) RecomputeQuoteAmounts(ctx context.Context, id uuid.UUID) (domain.Quote, error) {
	x, err := scanQuote(s.db.QueryRow(ctx, `
		UPDATE quotes q SET amount_received = t.received, amount_confirmed = t.confirmed, updated_at = $2
		FROM (SELECT COALESCE(sum(amount) FILTER (WHERE status IN ('pending', 'confirmed')), 0) AS received,
		             COALESCE(sum(amount) FILTER (WHERE status = 'confirmed'), 0) AS confirmed
		      FROM transfers WHERE quote_id = $1) t
		WHERE q.id = $1
		RETURNING `+quoteCols, id, s.Now()))
	return x, wrap("recompute quote amounts", err)
}

// WatchedQuote is a live quote as the scanner needs it.
type WatchedQuote struct {
	domain.Quote
	TokenContract string // asset contract / mint; "" for the native coin
	Decimals      int
}

const watchedSQL = `SELECT ` + quoteCols + `, a.contract_address, a.decimals
	FROM quotes q JOIN assets a ON a.id = q.asset_id
	WHERE q.chain_id = $1 AND ` + liveQuote + ` AND q.watch_until > $2`

func collectWatched(rows pgx.Rows, err error, op string) ([]WatchedQuote, error) {
	if err != nil {
		return nil, wrap(op, err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (WatchedQuote, error) {
		var w WatchedQuote
		var kind, status string
		err := r.Scan(append(quoteDest(&w.Quote, &kind, &status), &w.TokenContract, &w.Decimals)...)
		w.WalletKind, w.Status = domain.WalletKind(kind), domain.QuoteStatus(status)
		return w, err
	})
	return nonNil(out), wrap(op, err)
}

// ListWatched returns every quote the scanner must watch on a chain at now:
// status active or superseded and watch_until > now, with the asset's token
// contract and decimals. Ordered by creation.
func (s *Store) ListWatched(ctx context.Context, chainID string, now time.Time) ([]WatchedQuote, error) {
	rows, err := s.db.Query(ctx, watchedSQL+` ORDER BY q.created_at, q.id`, chainID, now.UTC())
	return collectWatched(rows, err, "list watched quotes")
}

// ListWatchedByAddress is ListWatched restricted to one receiving address
// (canonical form), used to match an incoming transfer.
func (s *Store) ListWatchedByAddress(ctx context.Context, chainID, address string, now time.Time) ([]WatchedQuote, error) {
	rows, err := s.db.Query(ctx, watchedSQL+` AND q.address = $3 ORDER BY q.created_at, q.id`, chainID, now.UTC(), address)
	return collectWatched(rows, err, "list watched quotes by address")
}

// CloseExpiredQuotes closes live quotes whose watch_until <= now (freeing
// static amounts for reuse) and returns how many were closed.
func (s *Store) CloseExpiredQuotes(ctx context.Context, now time.Time) (int64, error) {
	tag, err := s.db.Exec(ctx, `UPDATE quotes q SET status = 'closed', updated_at = $2
		WHERE `+liveQuote+` AND q.watch_until <= $1`, now.UTC(), s.Now())
	if err != nil {
		return 0, wrap("close expired quotes", err)
	}
	return tag.RowsAffected(), nil
}

// ExtendQuoteWatch moves a live quote's watch_until (e.g. when the invoice's
// expiry changes).
func (s *Store) ExtendQuoteWatch(ctx context.Context, id uuid.UUID, watchUntil time.Time) (domain.Quote, error) {
	x, err := scanQuote(s.db.QueryRow(ctx, `UPDATE quotes q SET watch_until = $2, updated_at = $3 WHERE q.id = $1
		RETURNING `+quoteCols, id, watchUntil.UTC(), s.Now()))
	return x, wrap("extend quote watch", err)
}
