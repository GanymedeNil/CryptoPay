// Package utxo implements chain.Client for the Bitcoin family (Bitcoin,
// Bitcoin testnet, Litecoin) on top of the Bitcoin Core compatible JSON-RPC
// API exposed by full nodes and commercial providers (QuickNode, GetBlock,
// Alchemy, NOWNodes, Tatum...).
//
// Only three read-only methods are used: getblockcount, getblockhash and
// getblock <hash> 2. Blocks are stream-decoded one transaction at a time, so
// memory stays bounded by the largest transaction even though verbosity-2
// blocks are often more than 10 MB of JSON.
package utxo

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cryptopay/internal/chain"
	"cryptopay/internal/domain"
)

// Defaults; see the With* options.
const (
	// DefaultCallTimeout bounds one attempt of a small call (getblockcount,
	// getblockhash).
	DefaultCallTimeout = 20 * time.Second
	// DefaultBlockTimeout bounds one attempt of getblock verbosity 2,
	// including streaming the body: a full block is 10–30 MB of JSON.
	DefaultBlockTimeout = 3 * time.Minute
	// DefaultAttemptsPerURL is how many times each RPC URL is tried per call.
	DefaultAttemptsPerURL = 3
	// DefaultBackoffBase and DefaultBackoffMax shape the exponential backoff
	// between retry rounds (jittered; a 429 Retry-After is honoured up to max).
	DefaultBackoffBase = 500 * time.Millisecond
	DefaultBackoffMax  = 15 * time.Second
	// DefaultMaxBlockBytes caps a getblock response body. Consensus limits a
	// block to 4 MB of weight; its verbosity-2 JSON stays well below this.
	DefaultMaxBlockBytes int64 = 256 << 20

	maxSmallResponseBytes int64 = 1 << 20
)

// knownHRPs are the bech32 prefixes of the Bitcoin family. Addresses with one
// of them are compared case-insensitively (bech32 is case-insensitive; the
// canonical form is lower-case).
var knownHRPs = []string{"bc", "tb", "bcrt", "ltc", "tltc", "rltc"}

// Client is a chain.Client for one UTXO chain. It is safe for concurrent use.
type Client struct {
	chainID       string
	hrps          []string
	rpc           *rpcClient
	log           *slog.Logger
	callTimeout   time.Duration
	blockTimeout  time.Duration
	maxBlockBytes int64
}

var _ chain.Client = (*Client)(nil)

type config struct {
	httpClient    *http.Client
	logger        *slog.Logger
	callTimeout   time.Duration
	blockTimeout  time.Duration
	attempts      int
	backoffBase   time.Duration
	backoffMax    time.Duration
	maxBlockBytes int64
}

// Option configures a Client.
type Option func(*config)

// WithHTTPClient sets the HTTP client (transport, proxy, TLS). Its Timeout
// should be zero or generous: per-attempt deadlines are applied through the
// request context (see WithTimeouts).
func WithHTTPClient(hc *http.Client) Option {
	return func(c *config) {
		if hc != nil {
			c.httpClient = hc
		}
	}
}

// WithLogger sets the logger used for retry/failover warnings. Logs name the
// RPC host only, never the full URL.
func WithLogger(l *slog.Logger) Option {
	return func(c *config) {
		if l != nil {
			c.logger = l
		}
	}
}

// WithTimeouts sets the per-attempt timeouts of small calls and of getblock.
// Non-positive values keep the defaults.
func WithTimeouts(call, block time.Duration) Option {
	return func(c *config) {
		if call > 0 {
			c.callTimeout = call
		}
		if block > 0 {
			c.blockTimeout = block
		}
	}
}

// WithRetry sets how many times each RPC URL is tried per call and the
// backoff between rounds. Non-positive values keep the defaults.
func WithRetry(attemptsPerURL int, backoffBase, backoffMax time.Duration) Option {
	return func(c *config) {
		if attemptsPerURL > 0 {
			c.attempts = attemptsPerURL
		}
		if backoffBase > 0 {
			c.backoffBase = backoffBase
		}
		if backoffMax > 0 {
			c.backoffMax = backoffMax
		}
	}
}

// WithMaxBlockBytes caps the size of one getblock response body.
func WithMaxBlockBytes(n int64) Option {
	return func(c *config) {
		if n > 0 {
			c.maxBlockBytes = n
		}
	}
}

// New returns a client for chain c, which must belong to the UTXO family.
// c.RPCURLs are used in order: the first is primary, the rest are failovers.
// A URL may carry HTTP basic auth credentials as userinfo
// (https://user:pass@host/) or an API key in its path; neither ever appears in
// errors or logs.
func New(c domain.Chain, opts ...Option) (*Client, error) {
	if c.Family != domain.FamilyUTXO {
		return nil, fmt.Errorf("utxo: chain %q has family %q, want %q", c.ID, c.Family, domain.FamilyUTXO)
	}
	cfg := config{
		callTimeout:   DefaultCallTimeout,
		blockTimeout:  DefaultBlockTimeout,
		attempts:      DefaultAttemptsPerURL,
		backoffBase:   DefaultBackoffBase,
		backoffMax:    DefaultBackoffMax,
		maxBlockBytes: DefaultMaxBlockBytes,
	}
	for _, o := range opts {
		if o != nil {
			o(&cfg)
		}
	}
	if cfg.httpClient == nil {
		cfg.httpClient = &http.Client{}
	}
	if cfg.logger == nil {
		cfg.logger = slog.New(slog.DiscardHandler)
	}
	if cfg.backoffBase > cfg.backoffMax {
		cfg.backoffBase = cfg.backoffMax
	}

	var eps []endpoint
	for i, raw := range c.RPCURLs {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		ep, err := parseEndpoint(i, raw)
		if err != nil {
			return nil, fmt.Errorf("utxo %s: %w", c.ID, err)
		}
		eps = append(eps, ep)
	}
	if len(eps) == 0 {
		return nil, fmt.Errorf("utxo %s: %w", c.ID, chain.ErrNoRPC)
	}

	hrps := append([]string(nil), knownHRPs...)
	if h := strings.ToLower(strings.TrimSpace(c.Params.Bech32HRP)); h != "" {
		hrps = append(hrps, h)
	}

	return &Client{
		chainID:       c.ID,
		hrps:          hrps,
		log:           cfg.logger,
		callTimeout:   cfg.callTimeout,
		blockTimeout:  cfg.blockTimeout,
		maxBlockBytes: cfg.maxBlockBytes,
		rpc: &rpcClient{
			chainID:     c.ID,
			endpoints:   eps,
			hc:          cfg.httpClient,
			log:         cfg.logger,
			rounds:      cfg.attempts,
			backoffBase: cfg.backoffBase,
			backoffMax:  cfg.backoffMax,
		},
	}, nil
}

// Factory returns a chain.Factory that builds utxo clients with opts.
func Factory(opts ...Option) chain.Factory {
	return func(c domain.Chain) (chain.Client, error) {
		cl, err := New(c, opts...)
		if err != nil {
			return nil, err // avoid a non-nil interface holding a nil *Client
		}
		return cl, nil
	}
}

// Head returns the height of the node's best block (getblockcount).
func (c *Client) Head(ctx context.Context) (uint64, error) {
	var head uint64
	err := c.rpc.call(ctx, callSpec{
		method:   "getblockcount",
		timeout:  c.callTimeout,
		maxBytes: maxSmallResponseBytes,
		result: func(_ *json.Decoder, first json.Token) error {
			n, ok := first.(json.Number)
			if !ok {
				return badData("getblockcount result has type %s, want number", tokenKind(first))
			}
			v, err := strconv.ParseUint(n.String(), 10, 64)
			if err != nil {
				return badData("getblockcount result %q is not a block height", truncate(n.String(), 40))
			}
			head = v
			return nil
		},
	})
	if err != nil {
		return 0, fmt.Errorf("utxo %s: head: %w", c.chainID, err)
	}
	return head, nil
}

// ScanTransfers returns every output in blocks [FromBlock, ToBlock] that pays
// one of req.Addresses, ordered by (height, tx position, vout). Only the
// native coin exists on these chains, so it returns nothing unless req.Native.
//
// Index is the vout number, Amount is in satoshi/litoshi (exact), From is
// empty and To is the canonical watched address. Any error aborts the whole
// range: no partial result is ever returned.
func (c *Client) ScanTransfers(ctx context.Context, req chain.ScanRequest) ([]chain.Transfer, error) {
	if req.FromBlock > req.ToBlock {
		return nil, fmt.Errorf("utxo %s: invalid block range [%d, %d]", c.chainID, req.FromBlock, req.ToBlock)
	}
	if !req.Native {
		return nil, nil
	}
	watched := make(map[string]string, len(req.Addresses))
	for _, a := range req.Addresses {
		if k := c.canonical(a); k != "" {
			watched[k] = k
		}
	}
	if len(watched) == 0 {
		return nil, nil
	}
	match := func(addr string) (string, bool) {
		to, ok := watched[c.canonical(addr)]
		return to, ok
	}

	var out []chain.Transfer
	for h := req.FromBlock; ; h++ {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("utxo %s: scan [%d, %d]: %w", c.chainID, req.FromBlock, req.ToBlock, err)
		}
		hash, err := c.blockHash(ctx, h)
		if err != nil {
			return nil, fmt.Errorf("utxo %s: scan [%d, %d]: %w", c.chainID, req.FromBlock, req.ToBlock, err)
		}
		ts, err := c.scanBlock(ctx, h, hash, match)
		if err != nil {
			return nil, fmt.Errorf("utxo %s: scan [%d, %d]: %w", c.chainID, req.FromBlock, req.ToBlock, err)
		}
		out = append(out, ts...)
		if h == req.ToBlock { // not h < ToBlock in the loop header: ToBlock may be MaxUint64
			break
		}
	}
	c.log.LogAttrs(ctx, slog.LevelDebug, "utxo scan done",
		slog.String("chain", c.chainID), slog.Uint64("from", req.FromBlock), slog.Uint64("to", req.ToBlock),
		slog.Int("watched", len(watched)), slog.Int("transfers", len(out)))
	return out, nil
}

// VerifyTransfer reports whether t's block is still the canonical block at
// t.BlockNumber. A transaction cannot fail once mined on these chains, so the
// block hash is all there is to check. Lookup failures (including a height
// above a lagging node's tip) are errors, never "not included".
func (c *Client) VerifyTransfer(ctx context.Context, t chain.Transfer) (bool, error) {
	if !isHash(t.BlockHash) {
		return false, fmt.Errorf("utxo %s: verify %s:%d: invalid block hash %q", c.chainID, t.TxHash, t.Index, truncate(t.BlockHash, 80))
	}
	hash, err := c.blockHash(ctx, t.BlockNumber)
	if err != nil {
		return false, fmt.Errorf("utxo %s: verify %s:%d: %w", c.chainID, t.TxHash, t.Index, err)
	}
	return strings.EqualFold(hash, t.BlockHash), nil
}

// blockHash returns the lower-case hash of the active-chain block at height.
func (c *Client) blockHash(ctx context.Context, height uint64) (string, error) {
	var hash string
	err := c.rpc.call(ctx, callSpec{
		method:   "getblockhash",
		params:   []any{height},
		timeout:  c.callTimeout,
		maxBytes: maxSmallResponseBytes,
		result: func(dec *json.Decoder, first json.Token) error {
			s, ok := first.(string)
			if !ok {
				return badData("getblockhash result has type %s, want string", tokenKind(first))
			}
			if !isHash(s) {
				return badData("getblockhash returned malformed hash %q", truncate(s, 80))
			}
			hash = strings.ToLower(s)
			return nil
		},
	})
	if err != nil {
		return "", fmt.Errorf("block hash at height %d: %w", height, err)
	}
	return hash, nil
}

// scanBlock fetches block hash (at height) with getblock verbosity 2 and
// returns the outputs paying watched addresses.
func (c *Client) scanBlock(ctx context.Context, height uint64, hash string, match func(string) (string, bool)) ([]chain.Transfer, error) {
	bs := &blockScan{height: height, hash: hash, match: match}
	err := c.rpc.call(ctx, callSpec{
		method:   "getblock",
		params:   []any{hash, 2},
		timeout:  c.blockTimeout,
		maxBytes: c.maxBlockBytes,
		result:   bs.decode,
	})
	if err != nil {
		return nil, fmt.Errorf("block %d (%s): %w", height, hash, err)
	}
	return bs.transfers, nil
}

// canonical returns the canonical form of a UTXO address for comparison:
// bech32 addresses (single-case, known prefix) in lower case, base58
// addresses unchanged (they are case-sensitive).
func (c *Client) canonical(addr string) string {
	addr = strings.TrimSpace(addr)
	lower := strings.ToLower(addr)
	if lower != addr && strings.ToUpper(addr) != addr {
		return addr // mixed case: base58 (bech32 is never mixed case)
	}
	for _, hrp := range c.hrps {
		if strings.HasPrefix(lower, hrp+"1") {
			return lower
		}
	}
	return addr
}
