package store

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Sentinel errors. Every error returned by the store is wrapped with the
// operation name, so compare with errors.Is.
var (
	// ErrNotFound: the requested row does not exist (or is not visible under
	// the method's documented conditions, e.g. a revoked API key).
	ErrNotFound = errors.New("not found")
	// ErrConflict: a unique constraint was violated, or a conditional update
	// found the row in a state that does not allow the change. For unique
	// violations the error is also a *ConstraintError carrying the name.
	ErrConflict = errors.New("conflict")
	// ErrForeignKey: a referenced row does not exist (insert/update), or the
	// row is still referenced (delete).
	ErrForeignKey = errors.New("foreign key violation")
	// ErrInUse: a delete was refused because other rows still reference the
	// row (wallet used by quotes, merchant with invoices). Also matches
	// ErrForeignKey.
	ErrInUse = errors.New("still referenced")
	// ErrInvalid: a value was rejected by a CHECK / NOT NULL constraint, could
	// not be represented, or failed the store's own validation.
	ErrInvalid = errors.New("invalid value")
	// ErrTxRequired: a locking method (…ForUpdate) was called outside WithTx.
	ErrTxRequired = errors.New("must be called inside WithTx")
	// ErrSerialization: the transaction kept failing with serialization
	// failures / deadlocks after the configured number of retries.
	ErrSerialization = errors.New("transaction serialization failure")
)

// Names of constraints callers may want to tell apart (see ConstraintName).
const (
	ConstraintInvoiceMerchantOrder  = "invoices_merchant_order_key"   // (merchant_id, merchant_order_id)
	ConstraintQuoteInvoiceAssetLive = "quotes_invoice_asset_live_key" // one live quote per (invoice, asset)
	ConstraintQuoteStaticAmount     = "quotes_static_amount_key"      // static wallet amount collision
	ConstraintAdminUserEmail        = "admin_users_email_key"         // lower(email)
	ConstraintAssetPK               = "assets_pkey"                   // asset id
	ConstraintAssetChainContract    = "assets_chain_id_contract_address_key"
	ConstraintAPIKeyHash            = "api_keys_key_hash_key"
	ConstraintSessionTokenHash      = "admin_sessions_token_hash_key"
	ConstraintTransferUnique        = "transfers_chain_id_tx_hash_idx_key"
)

// PostgreSQL SQLSTATE codes used by the store.
const (
	codeUniqueViolation      = "23505"
	codeForeignKeyViolation  = "23503"
	codeCheckViolation       = "23514"
	codeNotNullViolation     = "23502"
	codeExclusionViolation   = "23P01"
	codeSerializationFailure = "40001"
	codeDeadlockDetected     = "40P01"
)

// ConstraintError describes a violated database constraint. It matches
// (errors.Is) its Kind — ErrConflict, ErrForeignKey or ErrInvalid — and
// unwraps to the underlying *pgconn.PgError.
type ConstraintError struct {
	Kind       error
	Constraint string // e.g. "quotes_static_amount_key"
	Table      string
	Column     string
	Detail     string
	pg         *pgconn.PgError
}

func (e *ConstraintError) Error() string {
	msg := e.Kind.Error()
	if e.Constraint != "" {
		msg += fmt.Sprintf(" (constraint %s", e.Constraint)
		if e.Table != "" {
			msg += " on " + e.Table
		}
		msg += ")"
	} else if e.Column != "" {
		msg += fmt.Sprintf(" (column %s.%s)", e.Table, e.Column)
	}
	return msg
}

// Unwrap returns the kind sentinel and the PostgreSQL error.
func (e *ConstraintError) Unwrap() []error { return []error{e.Kind, e.pg} }

// ConstraintName returns the name of the violated constraint if err (or an
// error it wraps) is a *ConstraintError, else "".
func ConstraintName(err error) string {
	var ce *ConstraintError
	if errors.As(err, &ce) {
		return ce.Constraint
	}
	return ""
}

// IsConstraint reports whether err is a violation of the named constraint.
func IsConstraint(err error, name string) bool { return ConstraintName(err) == name }

// mapErr translates pgx / PostgreSQL errors into the store's sentinels.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var mapped *ConstraintError
	if errors.As(err, &mapped) {
		return err // already mapped
	}
	var pg *pgconn.PgError
	if !errors.As(err, &pg) {
		return err
	}
	ce := &ConstraintError{Constraint: pg.ConstraintName, Table: pg.TableName, Column: pg.ColumnName, Detail: pg.Detail, pg: pg}
	switch pg.Code {
	case codeUniqueViolation, codeExclusionViolation:
		ce.Kind = ErrConflict
		return ce
	case codeForeignKeyViolation:
		ce.Kind = ErrForeignKey
		return ce
	case codeCheckViolation, codeNotNullViolation:
		ce.Kind = ErrInvalid
		return ce
	}
	// Class 22 — data exception (invalid text representation, numeric out
	// of range, invalid JSON, ...).
	if len(pg.Code) == 5 && pg.Code[:2] == "22" {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return err
}

// wrap prefixes err with the operation name after mapping it.
func wrap(op string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("store: %s: %w", op, mapErr(err))
}

// wrapDelete is wrap for DELETE statements: foreign key violations become
// ErrInUse (still matching ErrForeignKey and the *ConstraintError).
func wrapDelete(op string, err error) error {
	if err == nil {
		return nil
	}
	m := mapErr(err)
	if errors.Is(m, ErrForeignKey) {
		return fmt.Errorf("store: %s: %w: %w", op, ErrInUse, m)
	}
	return fmt.Errorf("store: %s: %w", op, m)
}

func invalidf(op, format string, a ...any) error {
	return fmt.Errorf("store: %s: %w: %s", op, ErrInvalid, fmt.Sprintf(format, a...))
}

func isNotFound(err error) bool { return errors.Is(err, ErrNotFound) }

// isRetryable reports whether a transaction failed with a serialization
// failure or deadlock and can be retried from the start.
func isRetryable(err error) bool {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return pg.Code == codeSerializationFailure || pg.Code == codeDeadlockDetected
	}
	return false
}
