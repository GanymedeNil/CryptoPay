// Package scanner watches the configured chains for incoming payments.
//
// A Manager runs one loop per chain that is enabled and has a receiving
// wallet — a default wallet or at least one merchant-specific wallet — plus
// a ticker for the invoice expiry job. The set of loops follows
// the chain configuration in the database (re-read periodically), and every
// tick reads the chain row again, so admin changes — enabling or disabling a
// chain, RPC endpoints, confirmations, batch size, poll interval, default
// and merchant wallets — take effect without a restart.
//
// # One tick (docs/architecture.md, "Scanner")
//
//  1. head ← client.Head(). A chain that was never scanned (cursor 0)
//     starts at the head: there is no backfill.
//  2. While the cursor is behind the head (bounded number of batches per
//     tick): scan [cursor+1, min(cursor+batch, head)] for the addresses of
//     the watched (live, inside their watch window) quotes. When nobody is
//     watching, the cursor jumps to the head without any RPC call.
//  3. Each returned transfer is stored idempotently and matched to a quote —
//     xpub wallets by (address, asset), static wallets by (address, asset,
//     exact amount due); anything else is kept unmatched for the admin — and
//     the invoice is re-evaluated, all in one transaction per transfer.
//     The cursor is persisted after every completed batch; on any error it
//     stays where it is, so a range is retried and never skipped.
//  4. Pending transfers get their confirmation count updated
//     (head − block + 1). At the chain's required confirmations
//     VerifyTransfer decides: still canonical → confirmed; gone → orphaned,
//     and the cursor is rewound to just before that block so a transaction
//     re-included at a height the cursor already passed is found again (an
//     existing orphaned row is revived, so it is credited exactly once);
//     an RPC error → the transfer stays pending and is retried next tick.
//     This step also runs when step 2 failed.
//
// Nothing depends on Transfer.BlockHash (empty on Solana) or
// Transfer.From (empty on UTXO chains).
//
// # Several gateway instances
//
// A tick runs under a PostgreSQL session-level advisory lock per chain
// (scoped to the database schema), so only one instance scans a chain at a
// time; the others skip their tick. Independently of the lock, transfer
// inserts and the invoice state machine are idempotent: re-scanning a range
// changes nothing and emits no webhook twice.
//
// # Errors
//
// chain.ErrRangeTooLarge halves the chain's batch size (down to one block)
// and retries at once; the batch grows back gradually on success. Any other
// error ends the tick: it is recorded in chains.last_error (with URLs
// reduced to scheme and host) and the chain's loop backs off exponentially
// with jitter; the cursor stays at the last completed batch, so no range is
// ever skipped. A failed VerifyTransfer is softer: it is recorded in
// last_error but only a rate-limit error triggers the backoff. A panic
// inside a tick is recovered, logged and treated like an error.
package scanner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/url"
	"regexp"
	"runtime/debug"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"cryptopay/internal/chain"
	"cryptopay/internal/domain"
	"cryptopay/internal/invoice"
	"cryptopay/internal/store"
)

// ClientProvider hands out chain clients (*registry.Registry satisfies it).
type ClientProvider interface {
	// Get returns the client for the chain's current configuration.
	Get(ctx context.Context, chainID string) (chain.Client, error)
	// Invalidate drops a cached client.
	Invalidate(chainID string)
}

// Config tunes a Manager. Zero values select the defaults.
type Config struct {
	// ReloadInterval is how often the set of chains to scan is re-read.
	ReloadInterval time.Duration // default 15s
	// ExpireInterval is the period of the invoice expiry job
	// (invoice.Service.ExpireDue).
	ExpireInterval time.Duration // default 15s
	// MaxBatchesPerTick bounds how many block ranges one tick scans while
	// the chain is behind; the loop then ticks again immediately.
	MaxBatchesPerTick int // default 20
	// BackoffBase / BackoffMax shape the exponential backoff after a failed
	// tick: min(BackoffBase × 2^(failures−1), BackoffMax), jittered ±25 %.
	BackoffBase time.Duration // default 2s
	BackoffMax  time.Duration // default 2m
	// InvalidateAfter drops the chain's cached client after this many
	// consecutive failed ticks (and every multiple of it), so the next tick
	// starts with a fresh one.
	InvalidateAfter int // default 5
	// TickTimeout bounds one tick, so a hung RPC call cannot stall a chain.
	TickTimeout time.Duration // default 5m
	// DefaultPollInterval is used when a chain has no poll interval.
	DefaultPollInterval time.Duration // default 5s
}

func (c Config) withDefaults() Config {
	def := func(v *time.Duration, d time.Duration) {
		if *v <= 0 {
			*v = d
		}
	}
	def(&c.ReloadInterval, 15*time.Second)
	def(&c.ExpireInterval, 15*time.Second)
	def(&c.BackoffBase, 2*time.Second)
	def(&c.BackoffMax, 2*time.Minute)
	def(&c.TickTimeout, 5*time.Minute)
	def(&c.DefaultPollInterval, 5*time.Second)
	if c.BackoffMax < c.BackoffBase {
		c.BackoffMax = c.BackoffBase
	}
	if c.MaxBatchesPerTick <= 0 {
		c.MaxBatchesPerTick = 20
	}
	if c.InvalidateAfter <= 0 {
		c.InvalidateAfter = 5
	}
	return c
}

// Reasons a tick did not run (TickResult.Skipped).
const (
	SkipInactive = "inactive" // chain disabled, or neither a default nor a merchant wallet
	SkipLocked   = "locked"   // another instance is scanning the chain
)

// TickResult describes what one tick did.
type TickResult struct {
	ChainID       string
	Skipped       string // "" when the tick ran, else SkipInactive / SkipLocked
	Head          uint64 // chain head observed
	Cursor        uint64 // last fully scanned block after the tick
	Batches       int    // block ranges scanned (ScanTransfers calls that succeeded)
	FastForwarded bool   // nobody was watching: the cursor jumped to the head
	NewTransfers  int    // transfers stored for the first time
	Revived       int    // orphaned transfers found again
	Confirmed     int    // transfers finalised
	Orphaned      int    // transfers dropped by a reorg
	VerifyErrors  int    // pending transfers whose verification failed (retried next tick)
	Rewound       bool   // the cursor was moved back after an orphaned transfer
	Behind        bool   // the cursor is still behind the head (tick again soon)
	PollInterval  time.Duration
}

// Manager runs the chain scanners. Create it with New; it is safe for
// concurrent use.
type Manager struct {
	store    *store.Store
	clients  ClientProvider
	invoices *invoice.Service
	log      *slog.Logger
	now      func() time.Time
	cfg      Config

	// slots bounds the ticks running at once. A tick holds one pooled
	// connection for its advisory lock and needs another for its queries, so
	// without a bound many chains could exhaust the pool and starve each
	// other; at most (pool size − 2) / 2 ticks (at least one) run in parallel.
	slots chan struct{}

	mu     sync.Mutex
	limits map[string]int // per chain: reduced batch size after ErrRangeTooLarge (absent = no limit)
}

// New returns a Manager. logger nil → slog.Default(); now nil → time.Now
// (use the same clock as the store and the invoice service).
func New(st *store.Store, clients ClientProvider, invoices *invoice.Service, logger *slog.Logger, now func() time.Time, cfg Config) *Manager {
	if st == nil || clients == nil || invoices == nil {
		panic("scanner: store, client provider and invoice service are required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	if now == nil {
		now = time.Now
	}
	slots := max(1, (int(st.Pool().Config().MaxConns)-2)/2)
	return &Manager{store: st, clients: clients, invoices: invoices, log: logger, now: now,
		cfg: cfg.withDefaults(), limits: map[string]int{}, slots: make(chan struct{}, slots)}
}

func (m *Manager) clock() time.Time { return m.now().UTC().Truncate(time.Microsecond) }

// Run scans all active chains and runs the expiry job until ctx is
// cancelled, then stops every loop, waits for them to finish and returns
// nil. Failures (database or RPC) are logged and retried, never returned:
// a chain list that cannot be read is simply read again at the next reload.
func (m *Manager) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	running := map[string]context.CancelFunc{}
	defer func() {
		for _, cancel := range running {
			cancel()
		}
		wg.Wait()
	}()

	wg.Go(func() { m.runExpiry(ctx) })

	reload := func() {
		chains, err := m.store.ListChains(ctx)
		if err != nil {
			if ctx.Err() == nil {
				m.log.ErrorContext(ctx, "scanner: cannot load chains", "error", err)
			}
			return
		}
		want := map[string]bool{}
		for _, c := range chains {
			if active(c) {
				want[c.ID] = true
			}
		}
		for id, cancel := range running {
			if !want[id] {
				cancel()
				delete(running, id)
				m.clients.Invalidate(id)
				m.log.InfoContext(ctx, "scanner: chain stopped", "chain_id", id)
			}
		}
		for id := range want {
			if _, ok := running[id]; ok {
				continue
			}
			cctx, cancel := context.WithCancel(ctx)
			running[id] = cancel
			wg.Go(func() { m.runChain(cctx, id) })
			m.log.InfoContext(ctx, "scanner: chain started", "chain_id", id)
		}
	}

	reload()
	t := time.NewTicker(m.cfg.ReloadInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			reload()
		}
	}
}

// active reports whether a chain is scanned: enabled, with a default wallet
// or at least one merchant-specific wallet. Matching is by (chain, address,
// asset[, exact amount]) of the live quotes, so whose wallet an address
// belongs to does not matter.
func active(c domain.Chain) bool { return c.Enabled && (c.WalletID != nil || c.MerchantWallets > 0) }

// runChain ticks one chain until ctx is cancelled.
func (m *Manager) runChain(ctx context.Context, chainID string) {
	failures := 0
	for ctx.Err() == nil {
		tctx, cancel := context.WithTimeout(ctx, m.cfg.TickTimeout)
		res, err := m.TickOnce(tctx, chainID)
		cancel()
		if ctx.Err() != nil {
			return
		}
		var wait time.Duration
		switch {
		case err != nil:
			failures++
			wait = m.Backoff(failures)
			m.log.WarnContext(ctx, "scanner: tick failed", "chain_id", chainID, "failures", failures,
				"retry_in", wait.String(), "error", SanitizeError(err))
			if failures%m.cfg.InvalidateAfter == 0 {
				m.clients.Invalidate(chainID)
			}
		case res.Behind:
			failures = 0
			wait = 20 * time.Millisecond
		default:
			failures = 0
			wait = res.PollInterval
			if wait <= 0 {
				wait = m.cfg.DefaultPollInterval
			}
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// runExpiry runs the invoice expiry job periodically.
func (m *Manager) runExpiry(ctx context.Context) {
	run := func() {
		defer func() {
			if r := recover(); r != nil {
				m.log.ErrorContext(ctx, "scanner: panic in expiry job", "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
			}
		}()
		if _, err := m.invoices.ExpireDue(ctx); err != nil && ctx.Err() == nil {
			m.log.ErrorContext(ctx, "scanner: expiry job failed", "error", err)
		}
	}
	t := time.NewTicker(m.cfg.ExpireInterval)
	defer t.Stop()
	for {
		run()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Backoff returns the delay before the next tick after `failures`
// consecutive failed ticks: min(BackoffBase × 2^(failures−1), BackoffMax)
// with ±25 % jitter.
func (m *Manager) Backoff(failures int) time.Duration {
	d := m.cfg.BackoffBase
	for i := 1; i < failures && d < m.cfg.BackoffMax; i++ {
		d *= 2
	}
	d = min(d, m.cfg.BackoffMax)
	jitter := time.Duration(rand.Int64N(int64(d)/2+1)) - d/4
	return d + jitter
}

// TickOnce runs a single scan iteration of one chain (see the package
// documentation) and reports what it did. It is what the per-chain loops
// call, and is exported for "scan now" actions and tests.
//
// The tick is skipped (no error) when the chain is disabled or has no
// wallet at all — neither a default nor a merchant wallet — (SkipInactive)
// or another instance holds the chain's lock
// (SkipLocked). An error means the tick could not complete; progress made
// before the error is kept, the failing range is retried by the next tick,
// and the error has been recorded in chains.last_error. Transfers whose
// verification failed are counted in VerifyErrors without making the tick
// fail (unless the provider rate-limited us). A panic is recovered and
// returned as an error.
func (m *Manager) TickOnce(ctx context.Context, chainID string) (res TickResult, err error) {
	res.ChainID = chainID
	defer func() {
		if r := recover(); r != nil {
			m.log.ErrorContext(ctx, "scanner: panic in tick", "chain_id", chainID, "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
			err = fmt.Errorf("scanner: panic while scanning %s: %v", chainID, r)
			m.recordPanic(chainID)
		}
	}()

	c, err := m.store.GetChain(ctx, chainID)
	if err != nil {
		return res, fmt.Errorf("scanner: %w", err)
	}
	res.PollInterval = c.PollInterval
	if !active(c) {
		res.Skipped = SkipInactive
		return res, nil
	}
	select {
	case m.slots <- struct{}{}:
		defer func() { <-m.slots }()
	case <-ctx.Done():
		return res, fmt.Errorf("scanner: %s: %w", chainID, ctx.Err())
	}
	unlock, ok, err := m.lockChain(ctx, chainID)
	if err != nil {
		return res, err
	}
	if !ok {
		res.Skipped = SkipLocked
		return res, nil
	}
	defer unlock()

	// Read the chain again now that the lock is held: another instance may
	// have moved the cursor in between.
	if c, err = m.store.GetChain(ctx, chainID); err != nil {
		return res, fmt.Errorf("scanner: %w", err)
	}
	res.PollInterval = c.PollInterval
	if !active(c) {
		res.Skipped = SkipInactive
		return res, nil
	}
	t := &tick{m: m, chain: c, cursor: c.ScanCursor, head: c.LastHead, res: &res}
	err = t.run(ctx)
	res.Cursor, res.Head = t.cursor, t.head
	if err != nil {
		// Record the failure (cursor unchanged since the last completed
		// batch), unless the tick was merely cancelled (shutdown, chain
		// stopped): that is not an RPC problem worth showing.
		if !errors.Is(err, context.Canceled) && !errors.Is(ctx.Err(), context.Canceled) {
			if perr := t.persist(context.WithoutCancel(ctx), SanitizeError(err)); perr != nil {
				m.log.ErrorContext(ctx, "scanner: cannot record scan error", "chain_id", chainID, "error", perr)
			}
		}
		return res, fmt.Errorf("scanner: %s: %w", chainID, err)
	}
	return res, nil
}

// recordPanic notes a recovered panic in chains.last_error (best effort).
func (m *Manager) recordPanic(chainID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := m.store.GetChain(ctx, chainID)
	if err != nil {
		return
	}
	_ = m.store.UpdateScanState(ctx, chainID, store.ScanState{Cursor: c.ScanCursor, Head: c.LastHead,
		ScannedAt: m.clock(), Error: "internal error while scanning (panic); see the server log"})
}

// lockChain takes the chain's advisory lock on a dedicated connection
// (session level, so it spans the tick's transactions). ok=false means
// another session holds it. unlock releases lock and connection.
func (m *Manager) lockChain(ctx context.Context, chainID string) (unlock func(), ok bool, err error) {
	conn, err := m.store.Pool().Acquire(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("scanner: acquire lock connection: %w", err)
	}
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(`+lockKeySQL+`)`, chainID).Scan(&ok); err != nil {
		conn.Release()
		return nil, false, fmt.Errorf("scanner: advisory lock: %w", err)
	}
	if !ok {
		conn.Release()
		return nil, false, nil
	}
	return func() { releaseLock(conn, chainID) }, true, nil
}

// lockKeySQL derives the lock key from the schema and the chain id ($1), so
// separate deployments (or test schemas) sharing a database do not block
// each other.
const lockKeySQL = `hashtextextended('cryptopay:scanner:' || current_schema() || ':' || $1::text, 0)`

func releaseLock(conn *pgxpool.Conn, chainID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var released bool
	err := conn.QueryRow(ctx, `SELECT pg_advisory_unlock(`+lockKeySQL+`)`, chainID).Scan(&released)
	if err != nil || !released {
		// Closing the session releases its advisory locks for sure.
		_ = conn.Conn().Close(ctx)
	}
	conn.Release()
}

// ---- one tick ----

type tick struct {
	m      *Manager
	chain  domain.Chain
	client chain.Client
	cursor uint64 // last fully scanned block (always equal to the persisted value after persist)
	head   uint64
	res    *TickResult
}

// persist writes cursor, head and error text.
func (t *tick) persist(ctx context.Context, errText string) error {
	return t.m.store.UpdateScanState(ctx, t.chain.ID, store.ScanState{
		Cursor: t.cursor, Head: t.head, ScannedAt: t.m.clock(), Error: errText})
}

func (t *tick) run(ctx context.Context) error {
	m, c := t.m, t.chain
	client, err := m.clients.Get(ctx, c.ID)
	if err != nil {
		return err
	}
	t.client = client
	head, err := client.Head(ctx)
	if err != nil {
		return fmt.Errorf("head: %w", err)
	}
	t.head = head
	if t.cursor == 0 {
		// Never scanned: start at the head, no backfill.
		t.cursor = head
		if err := t.persist(ctx, ""); err != nil {
			return err
		}
	}
	scanErr := t.scan(ctx)
	// Pending transfers are confirmed even when scanning failed, so payments
	// already detected do not wait for a range the provider keeps rejecting.
	verifyErr, confirmErr := t.confirm(ctx)
	if scanErr != nil {
		return scanErr
	}
	if confirmErr != nil {
		return confirmErr
	}
	t.res.Behind = t.cursor < t.head
	errText := ""
	if verifyErr != nil {
		verifyErr = fmt.Errorf("verify transfer: %w", verifyErr)
		if errors.Is(verifyErr, chain.ErrRateLimited) {
			return verifyErr // back off like any throttled call
		}
		// Soft failure: the node could not answer for some transfer. It
		// stays pending and is verified again next tick; the error is shown
		// in last_error, but scanning for new payments is not slowed down.
		errText = SanitizeError(verifyErr)
		m.log.WarnContext(ctx, "scanner: transfer verification failed, retrying next tick", "chain_id", c.ID,
			"transfers", t.res.VerifyErrors, "error", errText)
	}
	return t.persist(ctx, errText)
}

// scan advances the cursor towards the head in batches.
func (t *tick) scan(ctx context.Context) error {
	m, c := t.m, t.chain
	var assets map[string]string // token contract → asset id
	for i := 0; i < m.cfg.MaxBatchesPerTick && t.cursor < t.head; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		watched, err := m.store.ListWatched(ctx, c.ID, m.clock())
		if err != nil {
			return err
		}
		if len(watched) == 0 {
			// Nobody is watching: blocks up to the head (observed before
			// this list was read) cannot contain a payment for a quote
			// created later.
			t.cursor = t.head
			t.res.FastForwarded = true
			return t.persist(ctx, "")
		}
		size := m.batchSize(c)
		to := t.head
		if t.head-t.cursor > uint64(size) {
			to = t.cursor + uint64(size)
		}
		req := buildRequest(t.cursor+1, to, watched)
		transfers, err := t.client.ScanTransfers(ctx, req)
		if err != nil {
			if errors.Is(err, chain.ErrRangeTooLarge) && size > 1 {
				m.setBatchLimit(c.ID, size/2)
				m.log.WarnContext(ctx, "scanner: block range too large, halving the batch", "chain_id", c.ID, "batch", size/2)
				continue
			}
			return fmt.Errorf("scan blocks %d-%d: %w", req.FromBlock, req.ToBlock, err)
		}
		if assets == nil {
			if assets, err = t.assetIndex(ctx); err != nil {
				return err
			}
		}
		sort.SliceStable(transfers, func(i, j int) bool {
			a, b := transfers[i], transfers[j]
			if a.BlockNumber != b.BlockNumber {
				return a.BlockNumber < b.BlockNumber
			}
			if a.TxHash != b.TxHash {
				return a.TxHash < b.TxHash
			}
			return a.Index < b.Index
		})
		for _, tr := range transfers {
			if err := t.record(ctx, tr, watched, assets); err != nil {
				return fmt.Errorf("record transfer %s/%d: %w", tr.TxHash, tr.Index, err)
			}
		}
		t.cursor = to
		if err := t.persist(ctx, ""); err != nil {
			return err
		}
		t.res.Batches++
		m.growBatch(c)
	}
	return nil
}

// assetIndex maps the chain's token contracts ("" = native coin) to asset ids.
func (t *tick) assetIndex(ctx context.Context) (map[string]string, error) {
	assets, err := t.m.store.ListAssets(ctx, store.AssetFilter{ChainID: t.chain.ID})
	if err != nil {
		return nil, err
	}
	idx := make(map[string]string, len(assets))
	for _, a := range assets {
		idx[a.ContractAddress] = a.ID
	}
	return idx, nil
}

// buildRequest assembles the scan request for the watched quotes:
// de-duplicated addresses, the distinct token contracts, and Native iff a
// watched quote is for the native coin.
func buildRequest(from, to uint64, watched []store.WatchedQuote) chain.ScanRequest {
	req := chain.ScanRequest{FromBlock: from, ToBlock: to}
	addrs, tokens := map[string]bool{}, map[string]bool{}
	for _, w := range watched {
		if !addrs[w.Address] {
			addrs[w.Address] = true
			req.Addresses = append(req.Addresses, w.Address)
		}
		if w.TokenContract == "" {
			req.Native = true
		} else if !tokens[w.TokenContract] {
			tokens[w.TokenContract] = true
			req.Tokens = append(req.Tokens, w.TokenContract)
		}
	}
	sort.Strings(req.Addresses)
	sort.Strings(req.Tokens)
	return req
}

// match finds the watched quote a transfer pays: same address and asset,
// and for static wallets exactly the amount due. nil = unmatched.
func match(watched []store.WatchedQuote, tr chain.Transfer, assetID string) *store.WatchedQuote {
	if assetID == "" || tr.Amount == nil {
		return nil
	}
	for i := range watched {
		w := &watched[i]
		if w.Address != tr.To || w.AssetID != assetID {
			continue
		}
		if w.WalletKind == domain.WalletStatic && (w.AmountDue == nil || w.AmountDue.Cmp(tr.Amount) != 0) {
			continue
		}
		return w
	}
	return nil
}

// record stores one scanned transfer and credits it, in one transaction.
// It is idempotent: a transfer that is already stored (and neither orphaned
// nor moved to another block) changes nothing.
func (t *tick) record(ctx context.Context, tr chain.Transfer, watched []store.WatchedQuote, assets map[string]string) error {
	m, c := t.m, t.chain
	if tr.TxHash == "" || tr.To == "" || tr.Amount == nil || tr.Amount.Sign() <= 0 {
		m.log.WarnContext(ctx, "scanner: ignoring malformed transfer", "chain_id", c.ID, "tx_hash", tr.TxHash, "index", tr.Index)
		return nil
	}
	assetID := assets[tr.Token]
	cand := match(watched, tr, assetID)
	var isNew, revived bool
	err := m.store.WithTx(ctx, func(tx *store.Store) error {
		isNew, revived = false, false
		existing, err := tx.GetTransferByTx(ctx, c.ID, tr.TxHash, tr.Index)
		found := err == nil
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		// Lock order: invoice first, then the transfer row.
		var invoiceID *uuid.UUID
		var quoteID *uuid.UUID
		switch {
		case found:
			invoiceID = existing.InvoiceID
		case cand != nil:
			invoiceID, quoteID = &cand.InvoiceID, &cand.ID
		}
		if invoiceID != nil {
			if _, err := tx.GetInvoiceForUpdate(ctx, *invoiceID); err != nil {
				if !errors.Is(err, store.ErrNotFound) {
					return err
				}
				invoiceID, quoteID = nil, nil // invoice deleted meanwhile: keep the transfer unmatched
			}
		}
		changed := false
		if !found {
			_, inserted, err := tx.InsertTransferIfNew(ctx, domain.Transfer{
				ChainID: c.ID, TxHash: tr.TxHash, Index: tr.Index, BlockNumber: tr.BlockNumber, BlockHash: tr.BlockHash,
				BlockTime: tr.BlockTime, FromAddress: tr.From, ToAddress: tr.To, TokenContract: tr.Token,
				AssetID: assetID, Amount: tr.Amount, QuoteID: quoteID, InvoiceID: invoiceID,
				Status: domain.TransferPending, DetectedAt: m.clock(),
			})
			if err != nil {
				return err
			}
			if !inserted {
				// Someone stored it between our read and the insert; let the
				// retried range take the "existing" path.
				return errors.New("transfer was inserted concurrently")
			}
			isNew, changed = true, true
		} else {
			switch existing.Status {
			case domain.TransferOrphaned:
				// Re-included after a reorg.
				if _, err := tx.ReviveTransfer(ctx, existing.ID, tr.BlockNumber, tr.BlockHash, blockTime(tr, m.clock())); err != nil {
					return err
				}
				revived, changed = true, true
			case domain.TransferPending:
				if existing.BlockNumber != tr.BlockNumber || existing.BlockHash != tr.BlockHash {
					// Still unconfirmed but now in another block: take over
					// the new block data so verification checks the right one.
					if _, err := tx.SetTransferStatus(ctx, existing.ID, domain.TransferOrphaned, 0); err != nil {
						return err
					}
					if _, err := tx.ReviveTransfer(ctx, existing.ID, tr.BlockNumber, tr.BlockHash, blockTime(tr, m.clock())); err != nil {
						return err
					}
				}
			}
		}
		if changed && invoiceID != nil {
			if _, err := m.invoices.Reevaluate(ctx, tx, *invoiceID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	switch {
	case isNew:
		t.res.NewTransfers++
		m.log.InfoContext(ctx, "scanner: transfer detected", "chain_id", c.ID, "tx_hash", tr.TxHash, "index", tr.Index,
			"block", tr.BlockNumber, "asset_id", assetID, "amount", tr.Amount.String(), "matched", cand != nil)
	case revived:
		t.res.Revived++
		m.log.InfoContext(ctx, "scanner: orphaned transfer seen again", "chain_id", c.ID, "tx_hash", tr.TxHash,
			"index", tr.Index, "block", tr.BlockNumber)
	}
	return nil
}

func blockTime(tr chain.Transfer, fallback time.Time) time.Time {
	if tr.BlockTime.IsZero() {
		return fallback
	}
	return tr.BlockTime
}

// confirm updates the confirmation counts of the chain's pending transfers
// and finalises those that reached the required depth. It returns the first
// verification error separately (soft failure).
func (t *tick) confirm(ctx context.Context) (verifyErr, err error) {
	m, c := t.m, t.chain
	pending, err := m.store.ListPendingTransfers(ctx, c.ID)
	if err != nil {
		return nil, err
	}
	required := max(c.Confirmations, 1)
	rewind := t.cursor
	for _, p := range pending {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		conf := confirmations(t.head, p.BlockNumber)
		progress := func() error {
			if conf == p.Confirmations {
				return nil
			}
			if err := m.store.UpdateTransferConfirmations(ctx, p.ID, conf); err != nil && !errors.Is(err, store.ErrNotFound) {
				return err
			}
			return nil
		}
		if conf < required {
			if err := progress(); err != nil {
				return nil, err
			}
			continue
		}
		ok, verr := t.client.VerifyTransfer(ctx, chain.Transfer{
			TxHash: p.TxHash, Index: p.Index, BlockNumber: p.BlockNumber, BlockHash: p.BlockHash, BlockTime: p.BlockTime,
			From: p.FromAddress, To: p.ToAddress, Token: p.TokenContract, Amount: p.Amount,
		})
		if verr != nil {
			// The node could not answer: keep it pending, retry next tick.
			t.res.VerifyErrors++
			if verifyErr == nil {
				verifyErr = verr
			}
			if err := progress(); err != nil {
				return nil, err
			}
			continue
		}
		status := domain.TransferConfirmed
		if !ok {
			status, conf = domain.TransferOrphaned, 0
		}
		done, err := t.finalise(ctx, p.ID, status, conf)
		if err != nil {
			return nil, fmt.Errorf("finalise transfer %s/%d: %w", p.TxHash, p.Index, err)
		}
		if !done {
			continue
		}
		if ok {
			t.res.Confirmed++
			m.log.InfoContext(ctx, "scanner: transfer confirmed", "chain_id", c.ID, "tx_hash", p.TxHash, "index", p.Index, "confirmations", conf)
			continue
		}
		t.res.Orphaned++
		m.log.WarnContext(ctx, "scanner: transfer orphaned by a reorg", "chain_id", c.ID, "tx_hash", p.TxHash, "index", p.Index, "block", p.BlockNumber)
		// Rewind so the transaction is found again if it was re-included at
		// a height the cursor already passed. (Never to 0, which means
		// "never scanned".)
		if target := max(p.BlockNumber, 2) - 1; target < rewind {
			rewind = target
		}
	}
	if rewind < t.cursor {
		t.cursor = rewind
		t.res.Rewound = true
		if err := t.persist(ctx, ""); err != nil {
			return nil, err
		}
	}
	return verifyErr, nil
}

// confirmations is head − block + 1, or 0 for a block above the head.
func confirmations(head, block uint64) int {
	if block == 0 || head < block {
		return 0
	}
	n := head - block + 1
	if n > 1<<30 {
		n = 1 << 30
	}
	return int(n)
}

// finalise sets a pending transfer to confirmed / orphaned and re-evaluates
// its invoice in one transaction. done=false means the transfer was not
// pending any more or was being re-assigned; nothing was changed.
func (t *tick) finalise(ctx context.Context, id uuid.UUID, status domain.TransferStatus, conf int) (done bool, err error) {
	m := t.m
	err = m.store.WithTx(ctx, func(tx *store.Store) error {
		done = false
		cur, err := tx.GetTransfer(ctx, id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil
			}
			return err
		}
		// Lock order: invoice first, then the transfer row.
		if cur.InvoiceID != nil {
			if _, err := tx.GetInvoiceForUpdate(ctx, *cur.InvoiceID); err != nil {
				return err
			}
		}
		locked, err := tx.GetTransferForUpdate(ctx, id)
		if err != nil {
			return err
		}
		if locked.Status != domain.TransferPending || !sameID(locked.InvoiceID, cur.InvoiceID) {
			return nil // handled elsewhere or just assigned: next tick
		}
		if _, err := tx.SetTransferStatus(ctx, id, status, conf); err != nil {
			return err
		}
		if locked.InvoiceID != nil {
			if _, err := m.invoices.Reevaluate(ctx, tx, *locked.InvoiceID); err != nil {
				return err
			}
		}
		done = true
		return nil
	})
	return done, err
}

func sameID(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// ---- adaptive batch size ----

// batchSize is the chain's configured scan batch, reduced while the
// provider rejects larger ranges.
func (m *Manager) batchSize(c domain.Chain) int {
	size := max(c.ScanBatch, 1)
	m.mu.Lock()
	defer m.mu.Unlock()
	if limit, ok := m.limits[c.ID]; ok && limit < size {
		return max(limit, 1)
	}
	return size
}

func (m *Manager) setBatchLimit(chainID string, limit int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.limits[chainID] = max(limit, 1)
}

// growBatch lets a reduced batch recover slowly (+12.5 %, at least one
// block, per successful batch) until the configured size is reached again.
func (m *Manager) growBatch(c domain.Chain) {
	m.mu.Lock()
	defer m.mu.Unlock()
	limit, ok := m.limits[c.ID]
	if !ok {
		return
	}
	limit += max(limit/8, 1)
	if limit >= max(c.ScanBatch, 1) {
		delete(m.limits, c.ID)
		return
	}
	m.limits[c.ID] = limit
}

// BatchLimit returns the chain's currently reduced batch size, or 0 when
// the configured scan batch applies.
func (m *Manager) BatchLimit(chainID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.limits[chainID]
}

// ---- error text ----

var urlPattern = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.\-]*://[^\s"'<>]+`)

// MaxErrorLen bounds the error text stored in chains.last_error.
const MaxErrorLen = 500

// SanitizeError renders err for chains.last_error and logs: URLs are
// reduced to scheme and host (RPC URLs often embed API keys in the path or
// query) and the text is truncated to MaxErrorLen bytes.
func SanitizeError(err error) string {
	if err == nil {
		return ""
	}
	s := urlPattern.ReplaceAllStringFunc(err.Error(), func(raw string) string {
		u, perr := url.Parse(raw)
		if perr != nil || u.Host == "" {
			return "[url]"
		}
		return u.Scheme + "://" + u.Hostname() + "/…"
	})
	if len(s) > MaxErrorLen {
		s = s[:MaxErrorLen]
		for len(s) > 0 && s[len(s)-1]&0xC0 == 0x80 { // do not cut a UTF-8 sequence
			s = s[:len(s)-1]
		}
		if len(s) > 0 && s[len(s)-1] >= 0xC0 {
			s = s[:len(s)-1]
		}
	}
	return s
}
