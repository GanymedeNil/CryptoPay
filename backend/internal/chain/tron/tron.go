// Package tron implements chain.Client for Tron mainnet and the Nile testnet
// on top of the java-tron full-node HTTP API (/wallet/...), which TronGrid and
// commercial providers (QuickNode, GetBlock, Ankr, ...) expose.
//
// The adapter is read-only. Per block it fetches /wallet/getblockbynum
// (native TRX TransferContract transactions, block hash and time) and, when
// TRC-20 tokens are watched and the block contains smart-contract calls,
// /wallet/gettransactioninfobyblocknum (receipts and event logs). Every
// response is validated (height, block id, tx set, txTrieRoot) so that a
// missing, truncated or not-yet-indexed block is reported as an error instead
// of silently yielding "no transfers".
//
// RPC URLs may carry a TronGrid key as query parameter "apikey"; it is moved
// to the TRON-PRO-API-KEY header. Errors and logs only ever name the host.
package tron

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cryptopay/internal/chain"
	"cryptopay/internal/domain"
)

// Option customises a Client.
type Option func(*config)

type config struct {
	httpClient  *http.Client
	concurrency int
	timeout     time.Duration
	retries     int
	backoffBase time.Duration
	backoffMax  time.Duration
	maxBody     int64
	maxRange    uint64
	logger      *slog.Logger
}

func defaultConfig() config {
	return config{
		httpClient:  &http.Client{},
		concurrency: 4,
		timeout:     20 * time.Second,
		retries:     2,
		backoffBase: 500 * time.Millisecond,
		backoffMax:  5 * time.Second,
		maxBody:     32 << 20,
		maxRange:    1000,
		logger:      slog.New(slog.DiscardHandler),
	}
}

// WithHTTPClient sets the HTTP client (transport, proxies, TLS). Its own
// Timeout, if any, applies in addition to the per-request timeout.
func WithHTTPClient(h *http.Client) Option {
	return func(c *config) {
		if h != nil {
			c.httpClient = h
		}
	}
}

// WithConcurrency bounds the number of in-flight HTTP requests of the client
// (shared by all calls) and the number of blocks scanned in parallel.
// Default 4.
func WithConcurrency(n int) Option {
	return func(c *config) {
		if n > 0 {
			c.concurrency = n
		}
	}
}

// WithRequestTimeout bounds every single HTTP request. Default 20s.
func WithRequestTimeout(d time.Duration) Option {
	return func(c *config) {
		if d > 0 {
			c.timeout = d
		}
	}
}

// WithRetries sets how many extra rounds over all endpoints are attempted
// after a round in which every endpoint failed with a retryable error
// (transport error, timeout, 5xx, 429, node lagging behind). Default 2.
func WithRetries(n int) Option {
	return func(c *config) {
		if n >= 0 {
			c.retries = n
		}
	}
}

// WithBackoff sets the exponential backoff between retry rounds: base delay
// (doubled each round, with jitter) and cap. Defaults 500ms and 5s.
func WithBackoff(base, max time.Duration) Option {
	return func(c *config) {
		if base > 0 {
			c.backoffBase = base
		}
		if max > 0 {
			c.backoffMax = max
		}
		if c.backoffMax < c.backoffBase {
			c.backoffMax = c.backoffBase
		}
	}
}

// WithMaxResponseBytes bounds the size of a single RPC response.
// Default 32 MiB (a full mainnet block is typically 0.3-1 MiB of JSON).
func WithMaxResponseBytes(n int64) Option {
	return func(c *config) {
		if n > 0 {
			c.maxBody = n
		}
	}
}

// WithMaxBlocksPerScan bounds ToBlock-FromBlock+1 of ScanTransfers; larger
// requests fail with chain.ErrRangeTooLarge. Default 1000.
func WithMaxBlocksPerScan(n uint64) Option {
	return func(c *config) {
		if n > 0 {
			c.maxRange = n
		}
	}
}

// WithLogger sets the logger used for retry diagnostics (debug level).
func WithLogger(l *slog.Logger) Option {
	return func(c *config) {
		if l != nil {
			c.logger = l
		}
	}
}

// Client is a Tron chain.Client. It is safe for concurrent use.
type Client struct {
	chainID   string
	endpoints []*endpoint
	cfg       config
	sem       chan struct{}
	now       func() time.Time

	// getBlockUnsupportedAt (unix nanos) is set when the nodes turned out
	// not to serve /wallet/getblock; Head then uses /wallet/getnowblock.
	getBlockUnsupportedAt atomic.Int64
}

var _ chain.Client = (*Client)(nil)

// New returns a client for the Tron chain c (mainnet or Nile; the API is the
// same). c.RPCURLs are tried in order, failing over on errors.
func New(c domain.Chain, opts ...Option) (*Client, error) {
	if c.Family != domain.FamilyTron {
		return nil, fmt.Errorf("tron: chain %q has family %q, want %q", c.ID, c.Family, domain.FamilyTron)
	}
	cfg := defaultConfig()
	for _, o := range opts {
		o(&cfg)
	}
	cl := &Client{chainID: c.ID, cfg: cfg, sem: make(chan struct{}, cfg.concurrency), now: time.Now}
	for i, raw := range c.RPCURLs {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		ep, err := parseEndpoint(raw, i+1)
		if err != nil {
			return nil, fmt.Errorf("tron(%s): %w", c.ID, err)
		}
		cl.endpoints = append(cl.endpoints, ep)
	}
	if len(cl.endpoints) == 0 {
		return nil, fmt.Errorf("tron(%s): %w", c.ID, chain.ErrNoRPC)
	}
	return cl, nil
}

// NewFactory returns a chain.Factory building clients with opts.
func NewFactory(opts ...Option) chain.Factory {
	return func(c domain.Chain) (chain.Client, error) {
		cl, err := New(c, opts...)
		if err != nil {
			return nil, err
		}
		return cl, nil
	}
}

func (c *Client) errorf(format string, a ...any) error {
	return fmt.Errorf("tron(%s): "+format, append([]any{c.chainID}, a...)...)
}

type numRequest struct {
	Num     uint64 `json:"num"`
	Visible bool   `json:"visible"`
}

// getBlockReprobe is how long Head keeps using /wallet/getnowblock after
// /wallet/getblock turned out to be unsupported before trying it again.
const getBlockReprobe = time.Hour

// Head returns the number of the latest block. It uses /wallet/getblock with
// detail=false (header only, a few hundred bytes instead of the ~0.5 MiB of a
// full mainnet block) and falls back to /wallet/getnowblock on nodes that do
// not serve it (HTTP 404/405/501).
func (c *Client) Head(ctx context.Context) (uint64, error) {
	if since := c.getBlockUnsupportedAt.Load(); since == 0 || c.now().UnixNano()-since > int64(getBlockReprobe) {
		n, err := c.head(ctx, "/wallet/getblock", struct {
			Detail bool `json:"detail"`
		}{false})
		if err == nil {
			c.getBlockUnsupportedAt.Store(0)
			return n, nil
		}
		if ctx.Err() != nil || !errors.Is(err, errUnsupported) {
			return 0, c.errorf("head: %w", err)
		}
		n, err2 := c.head(ctx, "/wallet/getnowblock", struct{}{})
		if err2 != nil {
			return 0, c.errorf("head: %w", errors.Join(err, err2))
		}
		if since == 0 {
			c.cfg.logger.LogAttrs(ctx, slog.LevelInfo, "tron node lacks /wallet/getblock, using /wallet/getnowblock",
				slog.String("chain", c.chainID))
		}
		c.getBlockUnsupportedAt.Store(c.now().UnixNano())
		return n, nil
	}
	n, err := c.head(ctx, "/wallet/getnowblock", struct{}{})
	if err != nil {
		return 0, c.errorf("head: %w", err)
	}
	return n, nil
}

func (c *Client) head(ctx context.Context, path string, req any) (uint64, error) {
	var n uint64
	err := c.call(ctx, path, req, func(data []byte) error {
		var b apiBlock
		if err := json.Unmarshal(data, &b); err != nil {
			return fmt.Errorf("latest block: %w: %v", errMalformed, err)
		}
		if b.BlockHeader == nil {
			return fmt.Errorf("latest block: %w: no block_header", errMalformed)
		}
		num := b.BlockHeader.RawData.Number
		if num == 0 {
			return fmt.Errorf("latest block: %w: height 0", errMalformed)
		}
		if err := validateBlock(&b, num, false); err != nil {
			return err
		}
		n = num
		return nil
	})
	return n, err
}

// getBlock fetches and validates block num (with transactions).
func (c *Client) getBlock(ctx context.Context, num uint64) (*apiBlock, error) {
	var blk *apiBlock
	err := c.call(ctx, "/wallet/getblockbynum", numRequest{Num: num}, func(data []byte) error {
		var b apiBlock
		if err := json.Unmarshal(data, &b); err != nil {
			return fmt.Errorf("block %d: %w: %v", num, errMalformed, err)
		}
		if err := validateBlock(&b, num, true); err != nil {
			return err
		}
		blk = &b
		return nil
	})
	return blk, err
}

// getTxInfos fetches the transaction infos of blk and checks they cover
// exactly its transactions.
func (c *Client) getTxInfos(ctx context.Context, blk *apiBlock, num uint64) (map[string]*apiTxInfo, error) {
	var byID map[string]*apiTxInfo
	err := c.call(ctx, "/wallet/gettransactioninfobyblocknum", numRequest{Num: num}, func(data []byte) error {
		var infos []apiTxInfo
		if err := json.Unmarshal(data, &infos); err != nil {
			return fmt.Errorf("transaction infos of block %d: %w: %v", num, errMalformed, err)
		}
		m, err := validateTxInfos(infos, blk, num)
		if err != nil {
			return err
		}
		byID = m
		return nil
	})
	return byID, err
}

func (c *Client) newFilter(req chain.ScanRequest) (*filter, error) {
	f := &filter{
		native:    req.Native,
		addresses: make(map[addrKey]string, len(req.Addresses)),
		tokens:    make(map[addrKey]string, len(req.Tokens)),
	}
	for _, a := range req.Addresses {
		k, err := tronKey(strings.TrimSpace(a))
		if err != nil {
			return nil, fmt.Errorf("watched address %q: %w", truncate(a), err)
		}
		f.addresses[k] = canonical(k)
	}
	for _, t := range req.Tokens {
		k, err := tronKey(strings.TrimSpace(t))
		if err != nil {
			return nil, fmt.Errorf("token contract %q: %w", truncate(t), err)
		}
		f.tokens[k] = canonical(k)
	}
	return f, nil
}

// ScanTransfers returns the successful incoming TRX (req.Native) and TRC-20
// (req.Tokens) transfers to req.Addresses in [FromBlock, ToBlock], ordered by
// block, position of the transaction in the block, then log index (native
// transfers have Index -1). Either every block of the range is read or an
// error is returned.
func (c *Client) ScanTransfers(ctx context.Context, req chain.ScanRequest) ([]chain.Transfer, error) {
	if req.FromBlock > req.ToBlock {
		return nil, c.errorf("scan: invalid range %d-%d", req.FromBlock, req.ToBlock)
	}
	if span := req.ToBlock - req.FromBlock; span >= c.cfg.maxRange {
		return nil, c.errorf("scan %d-%d: %w: %d blocks, max %d",
			req.FromBlock, req.ToBlock, chain.ErrRangeTooLarge, span+1, c.cfg.maxRange)
	}
	f, err := c.newFilter(req)
	if err != nil {
		return nil, c.errorf("scan: %w", err)
	}
	if len(f.addresses) == 0 || (!f.native && len(f.tokens) == 0) {
		return nil, nil // nothing can match
	}

	n := req.ToBlock - req.FromBlock + 1
	results := make([][]chain.Transfer, n)
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		next     atomic.Uint64
		once     sync.Once
		firstErr error
		wg       sync.WaitGroup
	)
	for range min(uint64(c.cfg.concurrency), n) {
		wg.Go(func() {
			for {
				i := next.Add(1) - 1
				if i >= n || wctx.Err() != nil {
					return
				}
				ts, err := c.scanBlock(wctx, req.FromBlock+i, f)
				if err != nil {
					once.Do(func() { firstErr = err; cancel() })
					return
				}
				results[i] = ts
			}
		})
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, c.errorf("scan %d-%d: %w", req.FromBlock, req.ToBlock, err)
	}
	if firstErr != nil {
		return nil, c.errorf("scan %d-%d: %w", req.FromBlock, req.ToBlock, firstErr)
	}
	var total int
	for _, r := range results {
		total += len(r)
	}
	if total == 0 {
		return nil, nil
	}
	out := make([]chain.Transfer, 0, total)
	for _, r := range results {
		out = append(out, r...)
	}
	return out, nil
}

func (c *Client) scanBlock(ctx context.Context, num uint64, f *filter) ([]chain.Transfer, error) {
	blk, err := c.getBlock(ctx, num)
	if err != nil {
		return nil, err
	}
	var infos map[string]*apiTxInfo
	if len(f.tokens) > 0 && needsTxInfo(blk) {
		if infos, err = c.getTxInfos(ctx, blk, num); err != nil {
			return nil, err
		}
	}
	ref := refOf(blk)
	var out []chain.Transfer
	for i := range blk.Transactions {
		tx := &blk.Transactions[i]
		if f.native {
			t, err := nativeTransfer(tx, ref, f.addresses)
			if err != nil {
				return nil, fmt.Errorf("block %d: %w", num, err)
			}
			if t != nil {
				out = append(out, *t)
			}
		}
		if infos != nil {
			ts, err := tokenTransfers(infos[tx.TxID], ref, f)
			if err != nil {
				return nil, fmt.Errorf("block %d: %w", num, err)
			}
			out = append(out, ts...)
		}
	}
	return out, nil
}

// VerifyTransfer reports whether t is still in the canonical chain: the block
// at t.BlockNumber still has id t.BlockHash and still contains the successful
// transaction carrying this exact transfer (recipient, token, amount, index).
// It returns an error rather than false when the node cannot answer (block
// or transaction info unknown to it), so a lagging node never orphans a
// transfer.
func (c *Client) VerifyTransfer(ctx context.Context, t chain.Transfer) (bool, error) {
	txHash := strings.ToLower(strings.TrimPrefix(t.TxHash, "0x"))
	if !isHex(txHash, 64) {
		return false, c.errorf("verify: invalid tx hash %q", truncate(t.TxHash))
	}
	if t.BlockHash == "" {
		return false, c.errorf("verify %s: missing block hash", txHash)
	}
	to, err := tronKey(t.To)
	if err != nil {
		return false, c.errorf("verify %s: recipient %q: %w", txHash, truncate(t.To), err)
	}
	f := &filter{addresses: map[addrKey]string{to: canonical(to)}, tokens: map[addrKey]string{}}
	if t.Token != "" {
		tok, err := tronKey(t.Token)
		if err != nil {
			return false, c.errorf("verify %s: token %q: %w", txHash, truncate(t.Token), err)
		}
		f.tokens[tok] = canonical(tok)
	}

	blk, err := c.getBlock(ctx, t.BlockNumber)
	if err != nil {
		return false, c.errorf("verify %s: %w", txHash, err)
	}
	if !strings.EqualFold(blk.BlockID, strings.TrimPrefix(t.BlockHash, "0x")) {
		return false, nil // reorged
	}
	var tx *apiTx
	for i := range blk.Transactions {
		if blk.Transactions[i].TxID == txHash {
			tx = &blk.Transactions[i]
			break
		}
	}
	if tx == nil || len(tx.Ret) == 0 || tx.Ret[0].ContractRet != retSuccess {
		return false, nil
	}
	ref := refOf(blk)

	if t.Token == "" {
		if t.Index != -1 {
			return false, nil
		}
		nt, err := nativeTransfer(tx, ref, f.addresses)
		if err != nil {
			return false, c.errorf("verify %s: %w", txHash, err)
		}
		return nt != nil && sameAmount(nt.Amount, t.Amount), nil
	}

	if t.Index < 0 {
		return false, nil
	}
	var inf apiTxInfo
	err = c.call(ctx, "/wallet/gettransactioninfobyid", struct {
		Value string `json:"value"`
	}{txHash}, func(data []byte) error {
		inf = apiTxInfo{}
		if err := json.Unmarshal(data, &inf); err != nil {
			return fmt.Errorf("transaction info %s: %w: %v", txHash, errMalformed, err)
		}
		if inf.ID == "" {
			return fmt.Errorf("transaction info %s: %w", txHash, errNotFound)
		}
		if !strings.EqualFold(inf.ID, txHash) {
			return fmt.Errorf("transaction info %s: %w: node returned %s", txHash, errMalformed, truncate(inf.ID))
		}
		if inf.BlockNumber != t.BlockNumber {
			// The node's block at this height matched, so its tx index
			// disagreeing means inconsistent node state: retry elsewhere.
			return fmt.Errorf("transaction info %s: %w: in block %d, expected %d",
				txHash, errMalformed, inf.BlockNumber, t.BlockNumber)
		}
		return nil
	})
	if err != nil {
		return false, c.errorf("verify %s: %w", txHash, err)
	}
	if !infoSucceeded(&inf) || t.Index >= len(inf.Log) {
		return false, nil
	}
	tr, err := decodeTransferLog(&inf.Log[t.Index], f)
	if err != nil {
		return false, c.errorf("verify %s: log %d: %w", txHash, t.Index, err)
	}
	return tr != nil && sameAmount(tr.Amount, t.Amount), nil
}

// sameAmount compares the on-chain amount with the expected one (nil = any).
func sameAmount(got, want *big.Int) bool {
	return want == nil || got.Cmp(want) == 0
}
