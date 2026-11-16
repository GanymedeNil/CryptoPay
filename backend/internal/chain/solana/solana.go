// Package solana implements chain.Client for Solana (mainnet-beta, devnet)
// over the standard Solana JSON-RPC API, so it works with any provider
// (Helius, QuickNode, Alchemy, Triton, the public endpoints, ...).
//
// The adapter is read-only. Block numbers are slots and every read uses the
// "finalized" commitment, so returned transfers can no longer be rolled back.
//
// Detection is balance based: for every candidate transaction the incoming
// amount is post − pre balance of the watched account (lamports for SOL,
// meta.pre/postTokenBalances for SPL tokens), which is robust against any
// instruction layout, CPI/inner instructions and multi-transfer transactions.
// Candidate transactions are found with getSignaturesForAddress on the watched
// owner and on each of its token accounts for the requested mints.
//
// Transfer fields:
//   - BlockNumber is the slot, BlockTime the block time (zero when the node
//     does not know it), BlockHash is always "" (fetching it would cost one
//     getBlock per slot; finalized slots cannot be reorganised anyway).
//   - Index is the position of the credited account in the transaction's
//     account list (static keys followed by address-lookup-table keys): the
//     owner's own index for SOL, the token account's index for SPL. It is
//     stable across re-scans and unique per transaction. A token account that
//     is its own owner (only possible for program-controlled accounts) gets
//     SelfOwnedIndexOffset+index so it cannot collide with its SOL credit.
//   - To is the watched owner address (never the token account), Token the
//     mint ("" for SOL), Amount the net credit in base units.
//   - From is best effort: for SPL the owner of the same-mint balance that
//     decreased the most, for SOL the signer whose balance decreased the most;
//     the fee payer when nothing decreased.
package solana

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/btcsuite/btcd/btcutil/base58"

	"cryptopay/internal/chain"
	"cryptopay/internal/domain"
)

const (
	commitmentFinalized = "finalized"

	// signaturePageSize is the getSignaturesForAddress limit (the RPC maximum).
	signaturePageSize = 1000

	// SelfOwnedIndexOffset is added to the Index of an SPL credit to a token
	// account whose owner is the account itself (see the package doc).
	SelfOwnedIndexOffset = 1 << 16

	defaultRequestTimeout   = 30 * time.Second
	defaultRetries          = 3
	defaultBackoffBase      = 500 * time.Millisecond
	defaultBackoffMax       = 8 * time.Second
	defaultMaxRetryWait     = 30 * time.Second
	defaultConcurrency      = 4
	defaultMaxSigPages      = 10
	defaultMaxTxVersion     = 1
	defaultMaxResponseBytes = 64 << 20
)

var (
	// ErrTooManyPages is returned (wrapped) when an account has more
	// signatures to page through than the configured page budget
	// (WithMaxSignaturePages). When the budget ran out inside the requested
	// slot range the error also wraps chain.ErrRangeTooLarge.
	ErrTooManyPages = errors.New("solana: signature page limit reached")
	// ErrTransactionNotFound is returned (wrapped) when a finalized signature
	// listed for the range cannot be fetched with getTransaction. It is
	// transient (a lagging or history-less node); retry the range.
	ErrTransactionNotFound = errors.New("solana: finalized transaction not found")
)

// Option customises a Client.
type Option func(*options)

type options struct {
	httpClient     *http.Client
	requestTimeout time.Duration
	retries        int
	backoffBase    time.Duration
	backoffMax     time.Duration
	maxRetryWait   time.Duration
	concurrency    int
	maxSigPages    int
	maxTxVersion   int
	maxResponse    int64
	logger         *slog.Logger
}

// WithHTTPClient sets the HTTP client used for RPC calls (default: a
// dedicated client with a pooled transport).
func WithHTTPClient(hc *http.Client) Option {
	return func(o *options) {
		if hc != nil {
			o.httpClient = hc
		}
	}
}

// WithRequestTimeout bounds every single HTTP attempt (default 30s).
func WithRequestTimeout(d time.Duration) Option {
	return func(o *options) {
		if d > 0 {
			o.requestTimeout = d
		}
	}
}

// WithRetries sets how many extra rounds over all endpoints a call makes
// after transient failures (default 3; 0 disables retries, failover remains).
func WithRetries(n int) Option {
	return func(o *options) {
		if n >= 0 {
			o.retries = n
		}
	}
}

// WithBackoff sets the exponential backoff between retry rounds (default
// 500ms doubling up to 8s, with jitter). A larger Retry-After wins.
func WithBackoff(base, maxDelay time.Duration) Option {
	return func(o *options) {
		if base > 0 {
			o.backoffBase = base
		}
		if maxDelay > 0 {
			o.backoffMax = maxDelay
		}
	}
}

// WithMaxRetryWait caps how long a call honours a Retry-After header
// (default 30s). Longer requested waits fail the call immediately with an
// error wrapping chain.ErrRateLimited.
func WithMaxRetryWait(d time.Duration) Option {
	return func(o *options) {
		if d >= 0 {
			o.maxRetryWait = d
		}
	}
}

// WithConcurrency bounds the number of in-flight RPC requests issued by one
// ScanTransfers call (default 4).
func WithConcurrency(n int) Option {
	return func(o *options) {
		if n > 0 {
			o.concurrency = n
		}
	}
}

// WithMaxSignaturePages bounds getSignaturesForAddress paging per account and
// scan (pages of 1000; default 10). Exceeding it fails the scan with
// ErrTooManyPages instead of silently truncating.
func WithMaxSignaturePages(n int) Option {
	return func(o *options) {
		if n > 0 {
			o.maxSigPages = n
		}
	}
}

// WithMaxTransactionVersion sets getTransaction's
// maxSupportedTransactionVersion (default 1: legacy, v0 and v1 transactions).
// A newer version on chain fails the scan (RPC error -32015) rather than
// being skipped.
func WithMaxTransactionVersion(v int) Option {
	return func(o *options) {
		if v >= 0 && v <= 255 {
			o.maxTxVersion = v
		}
	}
}

// WithMaxResponseBytes caps the size of a single RPC response (default 64 MiB).
func WithMaxResponseBytes(n int64) Option {
	return func(o *options) {
		if n > 0 {
			o.maxResponse = n
		}
	}
}

// WithLogger sets the logger for retry diagnostics (default: discard).
// Only endpoint hosts are logged, never full URLs.
func WithLogger(l *slog.Logger) Option {
	return func(o *options) {
		if l != nil {
			o.logger = l
		}
	}
}

// Client is a Solana chain.Client. It is safe for concurrent use.
type Client struct {
	chainID      string
	rpc          *rpcClient
	concurrency  int
	maxSigPages  int
	maxTxVersion int
}

var _ chain.Client = (*Client)(nil)

// New builds a Client for chain c (family solana). c.RPCURLs are tried in
// order: the first is primary, the rest are failover endpoints.
func New(c domain.Chain, opts ...Option) (*Client, error) {
	if c.Family != domain.FamilySolana {
		return nil, fmt.Errorf("solana: chain %q has family %q, want %q", c.ID, c.Family, domain.FamilySolana)
	}
	o := options{
		requestTimeout: defaultRequestTimeout,
		retries:        defaultRetries,
		backoffBase:    defaultBackoffBase,
		backoffMax:     defaultBackoffMax,
		maxRetryWait:   defaultMaxRetryWait,
		concurrency:    defaultConcurrency,
		maxSigPages:    defaultMaxSigPages,
		maxTxVersion:   defaultMaxTxVersion,
		maxResponse:    defaultMaxResponseBytes,
	}
	for _, opt := range opts {
		opt(&o)
	}
	if o.backoffMax < o.backoffBase {
		o.backoffMax = o.backoffBase
	}

	var eps []endpoint
	for i, raw := range c.RPCURLs {
		if raw == "" {
			continue
		}
		ep, err := parseEndpoint(raw)
		if err != nil {
			// Do not echo the URL: it may contain an API key.
			return nil, fmt.Errorf("solana: chain %q rpc url #%d: %w", c.ID, i+1, err)
		}
		eps = append(eps, ep)
	}
	if len(eps) == 0 {
		return nil, fmt.Errorf("solana: chain %q: %w", c.ID, chain.ErrNoRPC)
	}

	hc := o.httpClient
	if hc == nil {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.MaxIdleConnsPerHost = max(o.concurrency*2, 8)
		hc = &http.Client{Transport: tr}
	}
	logger := o.logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	return &Client{
		chainID: c.ID,
		rpc: &rpcClient{
			endpoints:    eps,
			hc:           hc,
			timeout:      o.requestTimeout,
			retries:      o.retries,
			backoffBase:  o.backoffBase,
			backoffMax:   o.backoffMax,
			maxRetryWait: o.maxRetryWait,
			maxBody:      o.maxResponse,
			log:          logger.With("chain", c.ID),
		},
		concurrency:  o.concurrency,
		maxSigPages:  o.maxSigPages,
		maxTxVersion: o.maxTxVersion,
	}, nil
}

// Factory returns a chain.Factory building Solana clients with opts.
func Factory(opts ...Option) chain.Factory {
	return func(c domain.Chain) (chain.Client, error) {
		cl, err := New(c, opts...)
		if err != nil {
			return nil, err // never a typed-nil interface
		}
		return cl, nil
	}
}

// Head returns the latest finalized slot.
func (c *Client) Head(ctx context.Context) (uint64, error) {
	var slot *uint64
	if err := c.rpc.call(ctx, "getSlot", []any{map[string]any{"commitment": commitmentFinalized}}, &slot); err != nil {
		return 0, err
	}
	if slot == nil {
		return 0, errors.New("solana rpc getSlot: null result")
	}
	return *slot, nil
}

// signatureStatus is one entry of getSignatureStatuses.
type signatureStatus struct {
	Slot               uint64          `json:"slot"`
	Confirmations      *uint64         `json:"confirmations"`
	Err                json.RawMessage `json:"err"`
	ConfirmationStatus string          `json:"confirmationStatus"`
}

// VerifyTransfer reports whether t's transaction is finalized, successful and
// in slot t.BlockNumber. A transaction the node does not know (or that
// failed) yields false; one that is known but not yet finalized yields an
// error so the caller retries later instead of orphaning it. It needs an RPC
// node with transaction history (searchTransactionHistory), which every
// mainstream provider offers.
func (c *Client) VerifyTransfer(ctx context.Context, t chain.Transfer) (bool, error) {
	if !validSignature(t.TxHash) {
		return false, fmt.Errorf("solana: verify transfer: invalid transaction signature %q", t.TxHash)
	}
	var res struct {
		Value []*signatureStatus `json:"value"`
	}
	params := []any{[]string{t.TxHash}, map[string]any{"searchTransactionHistory": true}}
	if err := c.rpc.call(ctx, "getSignatureStatuses", params, &res); err != nil {
		return false, err
	}
	if len(res.Value) != 1 {
		return false, fmt.Errorf("solana rpc getSignatureStatuses: got %d statuses for 1 signature", len(res.Value))
	}
	st := res.Value[0]
	if st == nil || !isNullJSON(st.Err) {
		return false, nil
	}
	// Nodes predating confirmationStatus report rooted (finalized)
	// transactions with confirmations == null.
	finalized := st.ConfirmationStatus == commitmentFinalized || (st.ConfirmationStatus == "" && st.Confirmations == nil)
	if !finalized {
		return false, fmt.Errorf("solana: transaction %s is %q, not finalized yet", t.TxHash, st.ConfirmationStatus)
	}
	if t.BlockNumber != 0 && st.Slot != t.BlockNumber {
		return false, nil
	}
	return true, nil
}

// validSignature reports whether s is a base58 encoded 64-byte signature.
func validSignature(s string) bool {
	if len(s) < 64 || len(s) > 88 {
		return false
	}
	b := base58.Decode(s)
	return len(b) == 64 && base58.Encode(b) == s
}
