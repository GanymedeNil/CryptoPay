// Package store is the PostgreSQL persistence layer of the gateway
// (pgx/v5, hand-written SQL).
//
// # Conventions
//
//   - One *Store wraps the connection pool. WithTx runs a function with a
//     *Store bound to a transaction that has exactly the same method set.
//   - Create… methods take the domain value: a zero ID is generated
//     (uuid v4), zero CreatedAt/UpdatedAt default to the store clock, and the
//     row as stored (RETURNING) is returned.
//   - Update… methods with a …Update struct change only the non-nil fields.
//   - Errors are wrapped with the operation name; test them with errors.Is
//     against ErrNotFound, ErrConflict, ErrForeignKey, ErrInUse, ErrInvalid,
//     ErrTxRequired and ErrSerialization. Unique violations carry the
//     constraint name (ConstraintName / IsConstraint).
//   - Amounts: numeric(78,0) ↔ *big.Int and numeric(38,18) ↔
//     decimal.Decimal, always converted through their exact text form.
//   - Secrets (chains.rpc_urls_enc, merchants.webhook_secret_enc) are
//     encrypted/decrypted transparently with the *secret.Box given to New.
//   - Timestamps written by the store come from its clock (WithClock,
//     default time.Now) truncated to microseconds (PostgreSQL precision);
//     "as of" queries (expiry, watch windows, due webhooks, sessions) take an
//     explicit now argument so callers with an injected clock stay in
//     control. All times returned are UTC.
//   - List methods never return nil slices.
package store

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"cryptopay/internal/secret"
)

// dbtx is the subset of pgxpool.Pool / pgx.Tx the store uses.
type dbtx interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// DefaultTxRetries is how many times WithTx retries a transaction that
// failed with a serialization failure or deadlock.
const DefaultTxRetries = 3

// Store provides all repositories. It is safe for concurrent use; a *Store
// passed to a WithTx callback must only be used inside that callback.
type Store struct {
	pool       *pgxpool.Pool
	db         dbtx
	tx         pgx.Tx // non-nil inside WithTx
	box        *secret.Box
	now        func() time.Time
	maxRetries int
}

// Option configures a Store.
type Option func(*Store)

// WithClock sets the clock used for timestamps written by the store
// (created_at, updated_at, revoked_at, ...). Default time.Now.
func WithClock(now func() time.Time) Option {
	return func(s *Store) {
		if now != nil {
			s.now = now
		}
	}
}

// WithTxRetries sets how many times WithTx retries on serialization
// failures / deadlocks (0 disables retrying). Default DefaultTxRetries.
func WithTxRetries(n int) Option {
	return func(s *Store) {
		if n >= 0 {
			s.maxRetries = n
		}
	}
}

// New returns a Store using pool, encrypting secrets with box. It panics if
// pool or box is nil (programming error).
func New(pool *pgxpool.Pool, box *secret.Box, opts ...Option) *Store {
	if pool == nil {
		panic("store: nil pool")
	}
	if box == nil {
		panic("store: nil secret box")
	}
	s := &Store{pool: pool, db: pool, box: box, now: time.Now, maxRetries: DefaultTxRetries}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Pool returns the underlying connection pool.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Box returns the encryption box used for secrets at rest.
func (s *Store) Box() *secret.Box { return s.box }

// InTx reports whether s is bound to a transaction (inside WithTx).
func (s *Store) InTx() bool { return s.tx != nil }

// Now returns the store clock's current time (UTC, microsecond precision).
func (s *Store) Now() time.Time { return s.now().UTC().Truncate(time.Microsecond) }

// Ping checks database connectivity (for readiness probes).
func (s *Store) Ping(ctx context.Context) error {
	if err := s.pool.Ping(ctx); err != nil {
		return fmt.Errorf("store: ping: %w", err)
	}
	return nil
}

// Isolation is a transaction isolation level.
type Isolation string

const (
	IsolationDefault        Isolation = "" // database default (read committed)
	IsolationReadCommitted  Isolation = Isolation(pgx.ReadCommitted)
	IsolationRepeatableRead Isolation = Isolation(pgx.RepeatableRead)
	IsolationSerializable   Isolation = Isolation(pgx.Serializable)
)

// TxOptions configures WithTxOptions.
type TxOptions struct {
	Isolation Isolation
	ReadOnly  bool
}

// WithTx runs fn inside a read-committed transaction. fn receives a *Store
// bound to the transaction; returning an error (or panicking) rolls back,
// returning nil commits.
//
// If the transaction fails with a serialization failure or deadlock it is
// retried from scratch up to the configured number of times (WithTxRetries),
// so fn must not have side effects outside the database (or must tolerate
// running more than once). When retries are exhausted the error matches
// ErrSerialization.
//
// Calling WithTx on a Store that is already inside a transaction creates a
// SAVEPOINT: an error from fn rolls back to it without aborting the outer
// transaction (no retry at that level).
func (s *Store) WithTx(ctx context.Context, fn func(tx *Store) error) error {
	return s.WithTxOptions(ctx, TxOptions{}, fn)
}

// WithTxOptions is WithTx with an explicit isolation level / access mode.
// Options are ignored for nested (savepoint) calls.
func (s *Store) WithTxOptions(ctx context.Context, opts TxOptions, fn func(tx *Store) error) error {
	if s.tx != nil {
		return s.savepoint(ctx, fn)
	}
	for attempt := 0; ; attempt++ {
		err := s.runTx(ctx, opts, fn)
		if err == nil {
			return nil
		}
		if !isRetryable(err) {
			return err
		}
		if attempt >= s.maxRetries {
			return fmt.Errorf("store: transaction failed after %d attempts: %w: %w", attempt+1, ErrSerialization, err)
		}
		// Short jittered backoff before retrying.
		d := time.Duration(5<<attempt)*time.Millisecond + time.Duration(rand.IntN(10))*time.Millisecond
		select {
		case <-ctx.Done():
			return fmt.Errorf("store: transaction retry: %w (last error: %w)", ctx.Err(), err)
		case <-time.After(d):
		}
	}
}

func (s *Store) runTx(ctx context.Context, opts TxOptions, fn func(tx *Store) error) (err error) {
	txOpts := pgx.TxOptions{IsoLevel: pgx.TxIsoLevel(opts.Isolation)}
	if opts.ReadOnly {
		txOpts.AccessMode = pgx.ReadOnly
	}
	tx, err := s.pool.BeginTx(ctx, txOpts)
	if err != nil {
		return fmt.Errorf("store: begin transaction: %w", err)
	}
	defer func() {
		// Panics are rolled back (and re-raised) by callFn.
		if err != nil {
			// Rollback with a fresh context: ctx may already be cancelled.
			rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			_ = tx.Rollback(rctx)
			cancel()
		}
	}()
	child := s.bind(tx)
	if err = callFn(fn, child); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	return nil
}

// callFn invokes fn, converting a panic into a rollback followed by a re-panic.
func callFn(fn func(tx *Store) error, tx *Store) (err error) {
	panicked := true
	defer func() {
		if panicked {
			r := recover()
			_ = tx.tx.Rollback(context.Background())
			panic(r)
		}
	}()
	err = fn(tx)
	panicked = false
	return err
}

func (s *Store) savepoint(ctx context.Context, fn func(tx *Store) error) (err error) {
	sp, err := s.tx.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: savepoint: %w", err)
	}
	child := s.bind(sp)
	if err = callFn(fn, child); err != nil {
		_ = sp.Rollback(ctx)
		return err
	}
	if err = sp.Commit(ctx); err != nil {
		return fmt.Errorf("store: release savepoint: %w", err)
	}
	return nil
}

func (s *Store) bind(tx pgx.Tx) *Store {
	c := *s
	c.db = tx
	c.tx = tx
	return &c
}

// guarded runs a single statement inside a savepoint when s is in a
// transaction, so that an expected constraint violation (e.g. a static
// amount collision) does not abort the caller's transaction. Outside a
// transaction it simply runs fn against the pool.
func (s *Store) guarded(ctx context.Context, fn func(db dbtx) error) error {
	if s.tx == nil {
		return fn(s.db)
	}
	sp, err := s.tx.Begin(ctx)
	if err != nil {
		return err
	}
	if err := fn(sp); err != nil {
		_ = sp.Rollback(ctx)
		return err
	}
	return sp.Commit(ctx)
}

func (s *Store) requireTx(op string) error {
	if s.tx == nil {
		return fmt.Errorf("store: %s: %w", op, ErrTxRequired)
	}
	return nil
}

// errNoRow distinguishes "row missing" from "row in the wrong state" after
// a conditional UPDATE matched nothing.
func (s *Store) errNoRow(ctx context.Context, op, table string, id any) error {
	var exists bool
	q := "SELECT EXISTS (SELECT 1 FROM " + table + " WHERE id = $1)"
	if err := s.db.QueryRow(ctx, q, id).Scan(&exists); err != nil {
		return wrap(op, err)
	}
	if !exists {
		return fmt.Errorf("store: %s: %w", op, ErrNotFound)
	}
	return fmt.Errorf("store: %s: %w", op, ErrConflict)
}

// isNoRows reports whether err is pgx.ErrNoRows.
func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }
