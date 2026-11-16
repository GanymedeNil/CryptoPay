package evm

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"slices"
	"strings"
	"time"

	"cryptopay/internal/address"
	"cryptopay/internal/chain"
	"cryptopay/internal/domain"
)

// TransferTopic is keccak256("Transfer(address,address,uint256)").
const TransferTopic = "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"

// Client is a read-only chain.Client for one EVM network. It is safe for
// concurrent use.
type Client struct {
	chainID   string
	rpc       *rpcClient
	chunkSize int
	window    int // blocks requested per round of a native scan
}

var _ chain.Client = (*Client)(nil)

// New builds a client for chain c. c.RPCURLs lists the endpoints in priority
// order (first is primary, the rest are failover).
func New(c domain.Chain, opts ...Option) (*Client, error) {
	if c.Family != domain.FamilyEVM {
		return nil, fmt.Errorf("evm: chain %q has family %q, want %q", c.ID, c.Family, domain.FamilyEVM)
	}
	cfg := defaultConfig()
	for _, o := range opts {
		if o != nil {
			o(&cfg)
		}
	}
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("evm: chain %q: invalid option: %w", c.ID, err)
	}
	var eps []*endpoint
	for i, raw := range c.RPCURLs {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		ep, err := newEndpoint(raw, i)
		if err != nil {
			return nil, fmt.Errorf("evm: chain %q: %w", c.ID, err)
		}
		eps = append(eps, ep)
	}
	if len(eps) == 0 {
		return nil, fmt.Errorf("evm: chain %q: %w", c.ID, chain.ErrNoRPC)
	}
	rc := &rpcClient{eps: eps, cfg: &cfg}
	if cfg.chainIDCheck && c.NetworkID != "" {
		id, ok := new(big.Int).SetString(c.NetworkID, 10)
		if !ok || id.Sign() <= 0 {
			return nil, fmt.Errorf("evm: chain %q: network id %q is not a positive decimal integer", c.ID, c.NetworkID)
		}
		rc.chainID = id
	}
	return &Client{
		chainID:   c.ID,
		rpc:       rc,
		chunkSize: cfg.chunkSize,
		window:    cfg.batchSize * cfg.concurrency,
	}, nil
}

// Factory returns a chain.Factory that builds EVM clients with opts.
func Factory(opts ...Option) chain.Factory {
	return func(c domain.Chain) (chain.Client, error) {
		cl, err := New(c, opts...)
		if err != nil {
			return nil, err
		}
		return cl, nil
	}
}

func (c *Client) errorf(format string, a ...any) error {
	return fmt.Errorf("evm %s: "+format, append([]any{c.chainID}, a...)...)
}

// Head returns the latest block number (eth_blockNumber).
func (c *Client) Head(ctx context.Context) (uint64, error) {
	raw, err := c.rpc.call(ctx, "eth_blockNumber")
	if err != nil {
		return 0, c.errorf("head: %w", err)
	}
	var s string
	null, err := decodeResult(raw, &s)
	if err == nil && null {
		err = errors.New("null result")
	}
	if err != nil {
		return 0, c.errorf("head: eth_blockNumber: %w", err)
	}
	n, err := parseUint64(s)
	if err != nil {
		return 0, c.errorf("head: eth_blockNumber: %w", err)
	}
	return n, nil
}

// ScanTransfers implements chain.Client. See the package documentation for
// what is detected.
func (c *Client) ScanTransfers(ctx context.Context, req chain.ScanRequest) ([]chain.Transfer, error) {
	if len(req.Addresses) == 0 || (len(req.Tokens) == 0 && !req.Native) {
		return nil, nil
	}
	if req.FromBlock > req.ToBlock {
		return nil, c.errorf("scan: invalid block range [%d, %d]", req.FromBlock, req.ToBlock)
	}
	watched, err := parseAddrList(req.Addresses)
	if err != nil {
		return nil, c.errorf("scan: watched address: %w", err)
	}
	tokens, err := parseAddrList(req.Tokens)
	if err != nil {
		return nil, c.errorf("scan: token: %w", err)
	}
	s := &scan{
		c:       c,
		from:    req.FromBlock,
		to:      req.ToBlock,
		watched: make(map[[20]byte]struct{}, len(watched)),
		list:    watched,
		headers: make(map[uint64]blockRef),
	}
	for _, a := range watched {
		s.watched[a] = struct{}{}
	}
	if req.Native {
		if err := s.native(ctx); err != nil {
			return nil, c.errorf("scan blocks %d-%d: native: %w", s.from, s.to, err)
		}
	}
	if len(tokens) > 0 {
		if err := s.tokens(ctx, tokens); err != nil {
			return nil, c.errorf("scan blocks %d-%d: tokens: %w", s.from, s.to, err)
		}
	}
	return s.result(), nil
}

// VerifyTransfer implements chain.Client: true when the block at
// t.BlockNumber still has hash t.BlockHash and the transaction succeeded in
// it; false after a reorg or for a failed transaction; an error when the node
// cannot answer conclusively.
func (c *Client) VerifyTransfer(ctx context.Context, t chain.Transfer) (bool, error) {
	txHash, err := parseHash(t.TxHash)
	if err != nil {
		return false, c.errorf("verify: tx: %w", err)
	}
	blockHash, err := parseHash(t.BlockHash)
	if err != nil {
		return false, c.errorf("verify %s: block: %w", txHash, err)
	}
	hc := newCall("eth_getBlockByNumber", formatUint64(t.BlockNumber), false)
	rc := newCall("eth_getTransactionReceipt", txHash)
	if err := c.rpc.batch(ctx, []*rpcCall{hc, rc}); err != nil {
		return false, c.errorf("verify %s: %w", txHash, err)
	}
	var h rpcHeader
	null, err := decodeResult(hc.result, &h)
	if err != nil {
		return false, c.errorf("verify %s: block %d: %w", txHash, t.BlockNumber, err)
	}
	if null {
		return false, c.errorf("verify %s: block %d not available from provider yet", txHash, t.BlockNumber)
	}
	ref, err := h.ref(t.BlockNumber)
	if err != nil {
		return false, c.errorf("verify %s: block %d: %w", txHash, t.BlockNumber, err)
	}
	if ref.hash != blockHash {
		return false, nil // the block was replaced by a reorg
	}
	var r rpcReceipt
	null, err = decodeResult(rc.result, &r)
	if err != nil {
		return false, c.errorf("verify %s: receipt: %w", txHash, err)
	}
	if null {
		return false, c.errorf("verify %s: block %d is canonical but the provider has no receipt", txHash, t.BlockNumber)
	}
	rbh, err := parseHash(r.BlockHash)
	if err != nil {
		return false, c.errorf("verify %s: receipt: %w", txHash, err)
	}
	if rbh != blockHash {
		return false, c.errorf("verify %s: receipt is in block %s but canonical block %d is %s (inconsistent provider state)",
			txHash, rbh, t.BlockNumber, blockHash)
	}
	ok, err := receiptStatus(r.Status)
	if err != nil {
		return false, c.errorf("verify %s: receipt: %w", txHash, err)
	}
	return ok, nil
}

// ---- wire types ----

type rpcHeader struct {
	Number    string `json:"number"`
	Hash      string `json:"hash"`
	Timestamp string `json:"timestamp"`
}

type rpcTx struct {
	Hash  string  `json:"hash"`
	From  string  `json:"from"`
	To    *string `json:"to"`
	Value string  `json:"value"`
}

type rpcFullBlock struct {
	rpcHeader
	Transactions []rpcTx `json:"transactions"`
}

type rpcLog struct {
	Address         string   `json:"address"`
	Topics          []string `json:"topics"`
	Data            string   `json:"data"`
	BlockNumber     string   `json:"blockNumber"`
	BlockHash       string   `json:"blockHash"`
	BlockTimestamp  string   `json:"blockTimestamp"`
	TransactionHash string   `json:"transactionHash"`
	LogIndex        string   `json:"logIndex"`
	Removed         bool     `json:"removed"`
}

type rpcReceipt struct {
	BlockHash string  `json:"blockHash"`
	Status    *string `json:"status"`
}

type logFilter struct {
	FromBlock string   `json:"fromBlock"`
	ToBlock   string   `json:"toBlock"`
	Address   []string `json:"address"`
	Topics    []any    `json:"topics"`
}

type blockRef struct {
	hash string
	time time.Time
}

func (h *rpcHeader) ref(want uint64) (blockRef, error) {
	n, err := parseUint64(h.Number)
	if err != nil {
		return blockRef{}, fmt.Errorf("number: %w", err)
	}
	if n != want {
		return blockRef{}, fmt.Errorf("provider returned block %d instead", n)
	}
	hash, err := parseHash(h.Hash)
	if err != nil {
		return blockRef{}, err
	}
	ts, err := parseUint64(h.Timestamp)
	if err != nil {
		return blockRef{}, fmt.Errorf("timestamp: %w", err)
	}
	t, err := unixTime(ts)
	if err != nil {
		return blockRef{}, err
	}
	return blockRef{hash: hash, time: t}, nil
}

func unixTime(ts uint64) (time.Time, error) {
	if ts > math.MaxInt64 {
		return time.Time{}, fmt.Errorf("timestamp %d out of range", ts)
	}
	return time.Unix(int64(ts), 0).UTC(), nil
}

func receiptStatus(s *string) (bool, error) {
	if s == nil {
		return false, errors.New("receipt has no status field")
	}
	v, err := parseUint64(*s)
	if err != nil {
		return false, fmt.Errorf("status: %w", err)
	}
	switch v {
	case 1:
		return true, nil
	case 0:
		return false, nil
	}
	return false, fmt.Errorf("unexpected receipt status %d", v)
}

// parseAddrList validates EVM addresses and returns them deduplicated and
// sorted (deterministic filters).
func parseAddrList(in []string) ([][20]byte, error) {
	seen := make(map[[20]byte]struct{}, len(in))
	out := make([][20]byte, 0, len(in))
	for _, s := range in {
		canon, err := address.ParseEVM(strings.TrimSpace(s))
		if err != nil {
			return nil, fmt.Errorf("%q: %w", clip(s), err)
		}
		a, err := parseAddr(canon)
		if err != nil {
			return nil, err
		}
		if _, dup := seen[a]; !dup {
			seen[a] = struct{}{}
			out = append(out, a)
		}
	}
	slices.SortFunc(out, func(a, b [20]byte) int { return strings.Compare(string(a[:]), string(b[:])) })
	return out, nil
}

func eip55(a [20]byte) string {
	s, _ := address.EVMFromBytes(a[:]) // cannot fail for 20 bytes
	return s
}

// ---- scan ----

type item struct {
	t   chain.Transfer
	pos int // transaction position in the block (native) for stable ordering
}

type scan struct {
	c        *Client
	from, to uint64
	watched  map[[20]byte]struct{}
	list     [][20]byte // watched, sorted
	headers  map[uint64]blockRef
	items    []item
}

// header decodes an eth_getBlockByNumber result for block n into s.headers.
func (s *scan) header(n uint64, raw json.RawMessage) error {
	var h rpcHeader
	null, err := decodeResult(raw, &h)
	if err != nil {
		return fmt.Errorf("block %d: %w", n, err)
	}
	if null {
		return fmt.Errorf("block %d not available from provider yet", n)
	}
	ref, err := h.ref(n)
	if err != nil {
		return fmt.Errorf("block %d: %w", n, err)
	}
	s.headers[n] = ref
	return nil
}

// native finds plain value transfers to watched addresses.
func (s *scan) native(ctx context.Context) error {
	var cand []item
	window := uint64(s.c.window)
	for start := s.from; ; {
		end := start + window - 1
		if end < start || end > s.to {
			end = s.to
		}
		calls := make([]*rpcCall, 0, end-start+1)
		for n := start; ; n++ {
			calls = append(calls, newCall("eth_getBlockByNumber", formatUint64(n), true))
			if n == end {
				break
			}
		}
		if err := s.c.rpc.batch(ctx, calls); err != nil {
			return err
		}
		for i, call := range calls {
			found, err := s.nativeBlock(start+uint64(i), call.result)
			call.result = nil // release the block body early
			if err != nil {
				return err
			}
			cand = append(cand, found...)
		}
		if end == s.to {
			break
		}
		start = end + 1
	}
	if len(cand) == 0 {
		return nil
	}

	calls := make([]*rpcCall, len(cand))
	for i, it := range cand {
		calls[i] = newCall("eth_getTransactionReceipt", it.t.TxHash)
	}
	if err := s.c.rpc.batch(ctx, calls); err != nil {
		return err
	}
	for i, call := range calls {
		it := cand[i]
		var r rpcReceipt
		null, err := decodeResult(call.result, &r)
		if err != nil {
			return fmt.Errorf("receipt %s: %w", it.t.TxHash, err)
		}
		if null {
			return fmt.Errorf("receipt %s (block %d) not available from provider yet", it.t.TxHash, it.t.BlockNumber)
		}
		bh, err := parseHash(r.BlockHash)
		if err != nil {
			return fmt.Errorf("receipt %s: %w", it.t.TxHash, err)
		}
		if bh != it.t.BlockHash {
			return fmt.Errorf("receipt %s is in block %s, scanned block %d is %s (reorg in progress)",
				it.t.TxHash, bh, it.t.BlockNumber, it.t.BlockHash)
		}
		ok, err := receiptStatus(r.Status)
		if err != nil {
			return fmt.Errorf("receipt %s: %w", it.t.TxHash, err)
		}
		if ok {
			s.items = append(s.items, it)
		}
	}
	return nil
}

func (s *scan) nativeBlock(n uint64, raw json.RawMessage) ([]item, error) {
	var b rpcFullBlock
	null, err := decodeResult(raw, &b)
	if err != nil {
		return nil, fmt.Errorf("block %d: %w", n, err)
	}
	if null {
		return nil, fmt.Errorf("block %d not available from provider yet", n)
	}
	ref, err := b.ref(n)
	if err != nil {
		return nil, fmt.Errorf("block %d: %w", n, err)
	}
	s.headers[n] = ref
	var out []item
	for pos, tx := range b.Transactions {
		if tx.To == nil || *tx.To == "" {
			continue // contract creation
		}
		to, err := parseAddr(*tx.To)
		if err != nil {
			return nil, fmt.Errorf("block %d tx #%d: to: %w", n, pos, err)
		}
		if _, ok := s.watched[to]; !ok {
			continue
		}
		value, err := parseBig(tx.Value)
		if err != nil {
			return nil, fmt.Errorf("block %d tx #%d: value: %w", n, pos, err)
		}
		if value.Sign() == 0 {
			continue
		}
		hash, err := parseHash(tx.Hash)
		if err != nil {
			return nil, fmt.Errorf("block %d tx #%d: %w", n, pos, err)
		}
		from, err := parseAddr(tx.From)
		if err != nil {
			return nil, fmt.Errorf("block %d tx %s: from: %w", n, hash, err)
		}
		out = append(out, item{pos: pos, t: chain.Transfer{
			TxHash:      hash,
			Index:       -1,
			BlockNumber: n,
			BlockHash:   ref.hash,
			BlockTime:   ref.time,
			From:        eip55(from),
			To:          eip55(to),
			Amount:      value,
		}})
	}
	return out, nil
}

// tokens finds ERC-20 Transfer events to watched addresses.
func (s *scan) tokens(ctx context.Context, tokens [][20]byte) error {
	tokenSet := make(map[[20]byte]struct{}, len(tokens))
	tokenHex := make([]string, len(tokens))
	for i, t := range tokens {
		tokenSet[t] = struct{}{}
		tokenHex[i] = addrHex(t)
	}
	topics := make([]string, len(s.list))
	topicSet := make(map[string][20]byte, len(s.list))
	for i, a := range s.list {
		topics[i] = addrTopic(a)
		topicSet[topics[i]] = a
	}

	var calls []*rpcCall
	// Ask for the header of the last block in the same batch: a node lagging
	// behind the range may silently return logs only up to its own head.
	var headCall *rpcCall
	if _, ok := s.headers[s.to]; !ok {
		headCall = newCall("eth_getBlockByNumber", formatUint64(s.to), false)
		calls = append(calls, headCall)
	}
	first := len(calls)
	k := s.c.chunkSize
	for ti := 0; ti < len(tokenHex); ti += k {
		for ai := 0; ai < len(topics); ai += k {
			calls = append(calls, newCall("eth_getLogs", logFilter{
				FromBlock: formatUint64(s.from),
				ToBlock:   formatUint64(s.to),
				Address:   tokenHex[ti:min(ti+k, len(tokenHex))],
				Topics:    []any{TransferTopic, nil, topics[ai:min(ai+k, len(topics))]},
			}))
		}
	}
	if err := s.c.rpc.batch(ctx, calls); err != nil {
		return err
	}
	if headCall != nil {
		if err := s.header(s.to, headCall.result); err != nil {
			return err
		}
	}

	var found []item
	var hasTime []bool
	for _, call := range calls[first:] {
		var logs []rpcLog
		if _, err := decodeResult(call.result, &logs); err != nil { // null = no logs
			return fmt.Errorf("eth_getLogs: %w", err)
		}
		call.result = nil
		for i := range logs {
			it, timed, ok, err := s.parseLog(&logs[i], tokenSet, topicSet)
			if err != nil {
				return err
			}
			if ok {
				found = append(found, it)
				hasTime = append(hasTime, timed)
			}
		}
	}

	// Providers without "blockTimestamp" in logs: fetch the headers.
	var missing []uint64
	queued := make(map[uint64]struct{})
	for i, it := range found {
		n := it.t.BlockNumber
		if _, have := s.headers[n]; have || hasTime[i] {
			continue
		}
		if _, dup := queued[n]; !dup {
			queued[n] = struct{}{}
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		hcalls := make([]*rpcCall, len(missing))
		for i, n := range missing {
			hcalls[i] = newCall("eth_getBlockByNumber", formatUint64(n), false)
		}
		if err := s.c.rpc.batch(ctx, hcalls); err != nil {
			return err
		}
		for i, n := range missing {
			if err := s.header(n, hcalls[i].result); err != nil {
				return err
			}
		}
	}
	for i := range found {
		t := &found[i].t
		h, ok := s.headers[t.BlockNumber]
		if ok && h.hash != t.BlockHash {
			return fmt.Errorf("log %s#%d is in block %s, block %d is %s (reorg in progress)",
				t.TxHash, t.Index, t.BlockHash, t.BlockNumber, h.hash)
		}
		if !hasTime[i] {
			if !ok {
				return fmt.Errorf("no timestamp for block %d", t.BlockNumber) // unreachable
			}
			t.BlockTime = h.time
		}
	}
	s.items = append(s.items, found...)
	return nil
}

// parseLog validates one log. ok is false for logs that are not ERC-20
// transfers to a watched address in range; err is set for malformed data.
func (s *scan) parseLog(l *rpcLog, tokens map[[20]byte]struct{}, topics map[string][20]byte) (it item, timed, ok bool, err error) {
	if l.Removed || len(l.Topics) != 3 || !strings.EqualFold(l.Topics[0], TransferTopic) {
		return it, false, false, nil // ERC-721 Transfer has 4 topics
	}
	to, watched := topics[strings.ToLower(l.Topics[2])]
	if !watched {
		return it, false, false, nil
	}
	token, err := parseAddr(l.Address)
	if err != nil {
		return it, false, false, fmt.Errorf("log address: %w", err)
	}
	if _, wanted := tokens[token]; !wanted {
		return it, false, false, nil
	}
	n, err := parseUint64(l.BlockNumber)
	if err != nil {
		return it, false, false, fmt.Errorf("log block number: %w", err)
	}
	if n < s.from || n > s.to {
		return it, false, false, nil
	}
	data, err := parseData(l.Data)
	if err != nil {
		return it, false, false, fmt.Errorf("log data: %w", err)
	}
	if len(data) != 32 {
		return it, false, false, nil
	}
	amount := new(big.Int).SetBytes(data)
	if amount.Sign() == 0 {
		return it, false, false, nil
	}
	fromTopic, err := parseFixed(l.Topics[1], 32)
	if err != nil {
		return it, false, false, fmt.Errorf("log from topic: %w", err)
	}
	var from [20]byte
	copy(from[:], fromTopic[12:])
	txHash, err := parseHash(l.TransactionHash)
	if err != nil {
		return it, false, false, fmt.Errorf("log tx: %w", err)
	}
	blockHash, err := parseHash(l.BlockHash)
	if err != nil {
		return it, false, false, fmt.Errorf("log %s block: %w", txHash, err)
	}
	idx, err := parseUint64(l.LogIndex)
	if err != nil || idx > math.MaxInt32 {
		return it, false, false, fmt.Errorf("log %s: invalid log index %q", txHash, clip(l.LogIndex))
	}
	var bt time.Time
	if l.BlockTimestamp != "" {
		ts, err := parseUint64(l.BlockTimestamp)
		if err == nil {
			bt, err = unixTime(ts)
		}
		if err != nil {
			return it, false, false, fmt.Errorf("log %s blockTimestamp: %w", txHash, err)
		}
		timed = true
	}
	return item{t: chain.Transfer{
		TxHash:      txHash,
		Index:       int(idx),
		BlockNumber: n,
		BlockHash:   blockHash,
		BlockTime:   bt,
		From:        eip55(from),
		To:          eip55(to),
		Token:       eip55(token),
		Amount:      amount,
	}}, timed, true, nil
}

// result orders (block, index, tx position) and deduplicates by (tx, index).
func (s *scan) result() []chain.Transfer {
	slices.SortFunc(s.items, func(a, b item) int {
		return cmp.Or(
			cmp.Compare(a.t.BlockNumber, b.t.BlockNumber),
			cmp.Compare(a.t.Index, b.t.Index),
			cmp.Compare(a.pos, b.pos),
			strings.Compare(a.t.TxHash, b.t.TxHash),
		)
	})
	type key struct {
		tx  string
		idx int
	}
	seen := make(map[key]struct{}, len(s.items))
	out := make([]chain.Transfer, 0, len(s.items))
	for _, it := range s.items {
		k := key{it.t.TxHash, it.t.Index}
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, it.t)
	}
	return out
}
