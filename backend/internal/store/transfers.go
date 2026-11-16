package store

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"cryptopay/internal/domain"
)

const transferCols = `t.id, t.chain_id, t.tx_hash, t.idx, t.block_number, t.block_hash, t.block_time, t.from_address,
	t.to_address, t.token_contract, t.asset_id, t.amount::text, t.quote_id, t.invoice_id, t.status, t.confirmations,
	t.detected_at, t.confirmed_at`

func scanTransfer(row pgx.Row) (domain.Transfer, error) {
	var x domain.Transfer
	var status string
	err := row.Scan(&x.ID, &x.ChainID, &x.TxHash, &x.Index, u64Scan{&x.BlockNumber}, &x.BlockHash, &x.BlockTime,
		&x.FromAddress, &x.ToAddress, &x.TokenContract, strScan{&x.AssetID}, bigScan{&x.Amount}, &x.QuoteID,
		&x.InvoiceID, &status, &x.Confirmations, &x.DetectedAt, &x.ConfirmedAt)
	x.Status = domain.TransferStatus(status)
	return x, err
}

func collectTransfers(rows pgx.Rows, err error, op string) ([]domain.Transfer, error) {
	if err != nil {
		return nil, wrap(op, err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.Transfer, error) { return scanTransfer(r) })
	return nonNil(out), wrap(op, err)
}

func validTransferStatus(st domain.TransferStatus) bool {
	return st == domain.TransferPending || st == domain.TransferConfirmed || st == domain.TransferOrphaned
}

// InsertTransferIfNew inserts a transfer unless one with the same
// (chain_id, tx_hash, idx) exists (ON CONFLICT DO NOTHING), so re-scanning a
// block range is a no-op. It returns the stored row — the new one, or the
// existing one unchanged with inserted=false.
//
// Zero ID → generated; Status "" → pending; zero DetectedAt → store clock;
// AssetID "" → NULL (unknown token). QuoteID/InvoiceID may be set when the
// scanner matched the transfer before inserting it.
//
// If the existing row is orphaned and the transfer was seen again (a reorg
// re-included it), call ReviveTransfer.
func (s *Store) InsertTransferIfNew(ctx context.Context, x domain.Transfer) (domain.Transfer, bool, error) {
	const op = "insert transfer"
	if x.Status == "" {
		x.Status = domain.TransferPending
	}
	if !validTransferStatus(x.Status) {
		return domain.Transfer{}, false, invalidf(op, "invalid status %q", x.Status)
	}
	if x.ChainID == "" || x.TxHash == "" || x.ToAddress == "" {
		return domain.Transfer{}, false, invalidf(op, "chain, tx hash and recipient are required")
	}
	if x.Amount == nil || x.Amount.Sign() < 0 {
		return domain.Transfer{}, false, invalidf(op, "amount must be non-negative")
	}
	block, err := u64Param(x.BlockNumber)
	if err != nil {
		return domain.Transfer{}, false, fmt.Errorf("store: %s: %w", op, err)
	}
	if x.BlockTime.IsZero() {
		x.BlockTime = s.Now()
	}
	var assetID *string
	if x.AssetID != "" {
		assetID = &x.AssetID
	}
	out, err := scanTransfer(s.db.QueryRow(ctx, `
		INSERT INTO transfers AS t (id, chain_id, tx_hash, idx, block_number, block_hash, block_time, from_address,
			to_address, token_contract, asset_id, amount, quote_id, invoice_id, status, confirmations, detected_at, confirmed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12::numeric, $13, $14, $15, $16, $17, $18)
		ON CONFLICT (chain_id, tx_hash, idx) DO NOTHING
		RETURNING `+transferCols,
		uuidOrNew(x.ID), x.ChainID, x.TxHash, x.Index, block, x.BlockHash, x.BlockTime.UTC(), x.FromAddress,
		x.ToAddress, x.TokenContract, assetID, numText(x.Amount), x.QuoteID, x.InvoiceID, string(x.Status),
		x.Confirmations, timeOr(x.DetectedAt, s.Now()), utcPtr(x.ConfirmedAt)))
	if err == nil {
		return out, true, nil
	}
	if !isNoRows(err) {
		return domain.Transfer{}, false, wrap(op, err)
	}
	existing, err := s.GetTransferByTx(ctx, x.ChainID, x.TxHash, x.Index)
	if err != nil {
		return domain.Transfer{}, false, err
	}
	return existing, false, nil
}

// GetTransfer returns a transfer by id.
func (s *Store) GetTransfer(ctx context.Context, id uuid.UUID) (domain.Transfer, error) {
	x, err := scanTransfer(s.db.QueryRow(ctx, `SELECT `+transferCols+` FROM transfers t WHERE t.id = $1`, id))
	return x, wrap("get transfer", err)
}

// GetTransferForUpdate returns a transfer and locks its row until the
// transaction ends. Must be called inside WithTx.
func (s *Store) GetTransferForUpdate(ctx context.Context, id uuid.UUID) (domain.Transfer, error) {
	const op = "get transfer for update"
	if err := s.requireTx(op); err != nil {
		return domain.Transfer{}, err
	}
	x, err := scanTransfer(s.db.QueryRow(ctx, `SELECT `+transferCols+` FROM transfers t WHERE t.id = $1 FOR UPDATE`, id))
	return x, wrap(op, err)
}

// GetTransferByTx returns the transfer identified by (chain, tx hash, index).
func (s *Store) GetTransferByTx(ctx context.Context, chainID, txHash string, idx int) (domain.Transfer, error) {
	x, err := scanTransfer(s.db.QueryRow(ctx, `SELECT `+transferCols+` FROM transfers t
		WHERE t.chain_id = $1 AND t.tx_hash = $2 AND t.idx = $3`, chainID, txHash, idx))
	return x, wrap("get transfer by tx", err)
}

// ListTransfersByInvoice returns the transfers credited to an invoice, newest first.
func (s *Store) ListTransfersByInvoice(ctx context.Context, invoiceID uuid.UUID) ([]domain.Transfer, error) {
	rows, err := s.db.Query(ctx, `SELECT `+transferCols+` FROM transfers t WHERE t.invoice_id = $1
		ORDER BY t.detected_at DESC, t.block_number DESC, t.id DESC`, invoiceID)
	return collectTransfers(rows, err, "list transfers by invoice")
}

// ListTransfersByQuote returns the transfers assigned to a quote, newest first.
func (s *Store) ListTransfersByQuote(ctx context.Context, quoteID uuid.UUID) ([]domain.Transfer, error) {
	rows, err := s.db.Query(ctx, `SELECT `+transferCols+` FROM transfers t WHERE t.quote_id = $1
		ORDER BY t.detected_at DESC, t.block_number DESC, t.id DESC`, quoteID)
	return collectTransfers(rows, err, "list transfers by quote")
}

// ListPendingTransfers returns the chain's transfers still waiting for
// confirmations (status pending), oldest block first.
func (s *Store) ListPendingTransfers(ctx context.Context, chainID string) ([]domain.Transfer, error) {
	rows, err := s.db.Query(ctx, `SELECT `+transferCols+` FROM transfers t WHERE t.chain_id = $1 AND t.status = 'pending'
		ORDER BY t.block_number, t.tx_hash, t.idx`, chainID)
	return collectTransfers(rows, err, "list pending transfers")
}

// UpdateTransferConfirmations stores the current confirmation count of a
// transfer (status unchanged).
func (s *Store) UpdateTransferConfirmations(ctx context.Context, id uuid.UUID, confirmations int) error {
	tag, err := s.db.Exec(ctx, `UPDATE transfers SET confirmations = $2 WHERE id = $1`, id, max(confirmations, 0))
	if err != nil {
		return wrap("update transfer confirmations", err)
	}
	if tag.RowsAffected() == 0 {
		return wrap("update transfer confirmations", ErrNotFound)
	}
	return nil
}

// SetTransferStatus sets status and confirmations. confirmed_at is set to
// the store clock when the transfer becomes confirmed (kept if already set)
// and cleared for pending / orphaned. Recompute the quote afterwards
// (RecomputeQuoteAmounts).
func (s *Store) SetTransferStatus(ctx context.Context, id uuid.UUID, status domain.TransferStatus, confirmations int) (domain.Transfer, error) {
	const op = "set transfer status"
	if !validTransferStatus(status) {
		return domain.Transfer{}, invalidf(op, "invalid status %q", status)
	}
	x, err := scanTransfer(s.db.QueryRow(ctx, `
		UPDATE transfers t SET status = $2, confirmations = $3,
			confirmed_at = CASE WHEN $2 = 'confirmed' THEN COALESCE(t.confirmed_at, $4) ELSE NULL END
		WHERE t.id = $1
		RETURNING `+transferCols, id, string(status), max(confirmations, 0), s.Now()))
	return x, wrap(op, err)
}

// ReviveTransfer puts an orphaned transfer that a re-scan found again (in a
// possibly different block) back to pending with the new block data.
// ErrConflict if the transfer is not orphaned.
func (s *Store) ReviveTransfer(ctx context.Context, id uuid.UUID, blockNumber uint64, blockHash string, blockTime time.Time) (domain.Transfer, error) {
	const op = "revive transfer"
	block, err := u64Param(blockNumber)
	if err != nil {
		return domain.Transfer{}, fmt.Errorf("store: %s: %w", op, err)
	}
	x, err := scanTransfer(s.db.QueryRow(ctx, `
		UPDATE transfers t SET status = 'pending', confirmations = 0, confirmed_at = NULL,
			block_number = $2, block_hash = $3, block_time = $4
		WHERE t.id = $1 AND t.status = 'orphaned'
		RETURNING `+transferCols, id, block, blockHash, blockTime.UTC()))
	if isNoRows(err) {
		return domain.Transfer{}, s.errNoRow(ctx, op, "transfers", id)
	}
	return x, wrap(op, err)
}

// AssignTransfer attaches an unmatched transfer to a quote and its invoice
// (manual assignment in the admin, or late matching by the scanner). It
// fails with ErrConflict when the transfer is already assigned. Checking
// that the quote's asset matches is the caller's job. Recompute the quote
// afterwards.
func (s *Store) AssignTransfer(ctx context.Context, id, quoteID, invoiceID uuid.UUID) (domain.Transfer, error) {
	const op = "assign transfer"
	x, err := scanTransfer(s.db.QueryRow(ctx, `
		UPDATE transfers t SET quote_id = $2, invoice_id = $3
		WHERE t.id = $1 AND t.quote_id IS NULL AND t.invoice_id IS NULL
		RETURNING `+transferCols, id, quoteID, invoiceID))
	if isNoRows(err) {
		return domain.Transfer{}, s.errNoRow(ctx, op, "transfers", id)
	}
	return x, wrap(op, err)
}

// QuoteTransferSums returns, in base units, the received (pending +
// confirmed) and confirmed totals of the transfers assigned to a quote,
// computed in SQL. Orphaned transfers are excluded.
func (s *Store) QuoteTransferSums(ctx context.Context, quoteID uuid.UUID) (received, confirmed *big.Int, err error) {
	err = s.db.QueryRow(ctx, `
		SELECT COALESCE(sum(amount) FILTER (WHERE status IN ('pending', 'confirmed')), 0)::text,
		       COALESCE(sum(amount) FILTER (WHERE status = 'confirmed'), 0)::text
		FROM transfers WHERE quote_id = $1`, quoteID).Scan(bigScan{&received}, bigScan{&confirmed})
	if err != nil {
		return nil, nil, wrap("quote transfer sums", err)
	}
	return received, confirmed, nil
}

// TransferFilter selects transfers for ListTransfers.
type TransferFilter struct {
	ChainID   string
	Status    domain.TransferStatus // "" = any
	Matched   *bool                 // true: assigned to an invoice; false: unmatched; nil: both
	InvoiceID *uuid.UUID
	// Q matches the tx hash, recipient or sender address exactly
	// (case-insensitive).
	Q        string
	Page     int // 1-based
	PageSize int
}

// ListTransfers returns a page of transfers, newest first.
func (s *Store) ListTransfers(ctx context.Context, f TransferFilter) (Page[domain.Transfer], error) {
	const op = "list transfers"
	page, size, offset := normPage(f.Page, f.PageSize)
	out := Page[domain.Transfer]{Items: []domain.Transfer{}, Page: page, PageSize: size}
	var c conds
	if f.ChainID != "" {
		c.add("t.chain_id = " + c.arg(f.ChainID))
	}
	if f.Status != "" {
		if !validTransferStatus(f.Status) {
			return out, invalidf(op, "invalid status %q", f.Status)
		}
		c.add("t.status = " + c.arg(string(f.Status)))
	}
	if f.Matched != nil {
		if *f.Matched {
			c.add("t.invoice_id IS NOT NULL")
		} else {
			c.add("t.invoice_id IS NULL")
		}
	}
	if f.InvoiceID != nil {
		c.add("t.invoice_id = " + c.arg(*f.InvoiceID))
	}
	if q := strings.TrimSpace(f.Q); q != "" {
		p := c.arg(q)
		c.add("(lower(t.tx_hash) = lower(" + p + ") OR lower(t.to_address) = lower(" + p + ") OR (t.from_address <> '' AND lower(t.from_address) = lower(" + p + ")))")
	}
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM transfers t`+c.where(), c.args...).Scan(&out.Total); err != nil {
		return out, wrap(op, err)
	}
	if out.Total == 0 || offset >= out.Total {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `SELECT `+transferCols+` FROM transfers t`+c.where()+
		fmt.Sprintf(` ORDER BY t.detected_at DESC, t.id DESC LIMIT %d OFFSET %d`, size, offset), c.args...)
	items, err := collectTransfers(rows, err, op)
	if err != nil {
		return out, err
	}
	out.Items = items
	return out, nil
}
