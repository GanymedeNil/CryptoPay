package evm

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"cryptopay/internal/address"
	"cryptopay/internal/domain"
)

// fakeNode is an in-memory Ethereum JSON-RPC node good enough to exercise
// the adapter: blocks, transactions, receipts, logs with real eth_getLogs
// filter semantics, single and batch requests, and hooks to inject failures.

const (
	testSecret  = "k3yS3cr3tABCDEF"
	testChainID = 1337
	baseTime    = 1_700_000_000
)

type fakeTx struct {
	hash      string
	from      [20]byte
	to        *[20]byte // nil: contract creation
	value     *big.Int
	status    uint64 // 1 success, 0 reverted
	noReceipt bool
}

type fakeLog struct {
	address   [20]byte
	topics    []string
	data      string
	txHash    string
	logIndex  uint64
	removed   bool
	blockHash string // overrides the block's hash when set
}

type fakeBlock struct {
	number uint64
	hash   string
	time   uint64
	txs    []fakeTx
	logs   []fakeLog
}

type callHook func(method string, params []json.RawMessage) (result any, rerr *rpcErrorObject, handled bool)

type fakeNode struct {
	mu               sync.Mutex
	chainID          uint64
	head             uint64
	blocks           map[uint64]*fakeBlock
	omitLogTimestamp bool
	sloppyLogs       bool // ignore address/topic filters like a buggy provider
	maxTopicOr       int  // reject filters with longer OR lists (0: unlimited)
	rejectBatches    bool
	hook             callHook
	httpHook         func(w http.ResponseWriter, r *http.Request, body []byte) bool

	httpRequests int
	calls        map[string]int
	batchSizes   []int
	topicOrSizes []int
}

func newFakeNode(head uint64) *fakeNode {
	return &fakeNode{chainID: testChainID, head: head, blocks: map[uint64]*fakeBlock{}, calls: map[string]int{}}
}

func (f *fakeNode) block(n uint64) *fakeBlock {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.blocks[n]
	if !ok {
		b = &fakeBlock{number: n, hash: hashN("block", int(n)), time: baseTime + n*12}
		f.blocks[n] = b
	}
	return b
}

// ensureBlocks creates empty blocks [from, to].
func (f *fakeNode) ensureBlocks(from, to uint64) {
	for n := from; n <= to; n++ {
		f.block(n)
	}
}

func (f *fakeNode) callCount(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[method]
}

func (f *fakeNode) requests() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.httpRequests
}

type fakeReq struct {
	ID     json.RawMessage   `json:"id"`
	Method string            `json:"method"`
	Params []json.RawMessage `json:"params"`
}

func (f *fakeNode) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.httpRequests++
	hook := f.httpHook
	f.mu.Unlock()
	if hook != nil && hook(w, r, body) {
		return
	}
	body = bytes.TrimSpace(body)
	if len(body) > 0 && body[0] == '[' {
		var reqs []fakeReq
		if err := json.Unmarshal(body, &reqs); err != nil {
			writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": nil, "error": map[string]any{"code": -32700, "message": "parse error"}})
			return
		}
		f.mu.Lock()
		f.batchSizes = append(f.batchSizes, len(reqs))
		reject := f.rejectBatches
		f.mu.Unlock()
		if reject {
			writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": nil, "error": map[string]any{"code": -32600, "message": "batch requests are not supported"}})
			return
		}
		out := make([]any, len(reqs))
		for i, rq := range reqs {
			out[len(reqs)-1-i] = f.respond(rq) // reversed: clients must match by id
		}
		writeJSON(w, out)
		return
	}
	var rq fakeReq
	if err := json.Unmarshal(body, &rq); err != nil {
		writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": nil, "error": map[string]any{"code": -32700, "message": "parse error"}})
		return
	}
	writeJSON(w, f.respond(rq))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakeNode) respond(rq fakeReq) map[string]any {
	resp := map[string]any{"jsonrpc": "2.0", "id": rq.ID}
	f.mu.Lock()
	f.calls[rq.Method]++
	hook := f.hook
	f.mu.Unlock()
	if hook != nil {
		if res, rerr, ok := hook(rq.Method, rq.Params); ok {
			if rerr != nil {
				resp["error"] = rerr
			} else {
				resp["result"] = res
			}
			return resp
		}
	}
	res, rerr := f.dispatch(rq.Method, rq.Params)
	if rerr != nil {
		resp["error"] = rerr
	} else {
		resp["result"] = res
	}
	return resp
}

func badParams(msg string) *rpcErrorObject { return &rpcErrorObject{Code: -32602, Message: msg} }

func (f *fakeNode) dispatch(method string, params []json.RawMessage) (any, *rpcErrorObject) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch method {
	case "eth_chainId":
		return formatUint64(f.chainID), nil
	case "eth_blockNumber":
		return formatUint64(f.head), nil
	case "eth_getBlockByNumber":
		if len(params) != 2 {
			return nil, badParams("want 2 params")
		}
		var tag string
		var full bool
		if json.Unmarshal(params[0], &tag) != nil || json.Unmarshal(params[1], &full) != nil {
			return nil, badParams("bad params")
		}
		n := f.head
		if tag != "latest" {
			v, err := parseUint64(tag)
			if err != nil {
				return nil, badParams("bad block number")
			}
			n = v
		}
		b, ok := f.blocks[n]
		if !ok || n > f.head {
			return nil, nil
		}
		return f.blockJSON(b, full), nil
	case "eth_getTransactionReceipt":
		var h string
		if len(params) != 1 || json.Unmarshal(params[0], &h) != nil {
			return nil, badParams("bad params")
		}
		for n, b := range f.blocks {
			if n > f.head {
				continue
			}
			for i, tx := range b.txs {
				if tx.hash == strings.ToLower(h) {
					if tx.noReceipt {
						return nil, nil
					}
					return map[string]any{
						"transactionHash":  tx.hash,
						"transactionIndex": formatUint64(uint64(i)),
						"blockHash":        b.hash,
						"blockNumber":      formatUint64(b.number),
						"status":           formatUint64(tx.status),
						"logs":             []any{},
					}, nil
				}
			}
		}
		return nil, nil
	case "eth_getLogs":
		if len(params) != 1 {
			return nil, badParams("want 1 param")
		}
		return f.getLogs(params[0])
	}
	return nil, &rpcErrorObject{Code: -32601, Message: "the method " + method + " does not exist/is not available"}
}

func (f *fakeNode) blockJSON(b *fakeBlock, full bool) map[string]any {
	txs := make([]any, len(b.txs))
	for i, tx := range b.txs {
		if !full {
			txs[i] = tx.hash
			continue
		}
		var to any
		if tx.to != nil {
			to = addrHex(*tx.to)
		}
		txs[i] = map[string]any{
			"hash":             tx.hash,
			"from":             addrHex(tx.from),
			"to":               to,
			"value":            "0x" + tx.value.Text(16),
			"blockHash":        b.hash,
			"blockNumber":      formatUint64(b.number),
			"transactionIndex": formatUint64(uint64(i)),
			"input":            "0x",
			"gas":              "0x5208",
			"nonce":            "0x1",
			"type":             "0x2",
		}
	}
	return map[string]any{
		"number":       formatUint64(b.number),
		"hash":         b.hash,
		"parentHash":   hashN("block", int(b.number)-1),
		"timestamp":    formatUint64(b.time),
		"gasUsed":      "0x0",
		"transactions": txs,
	}
}

func (f *fakeNode) getLogs(raw json.RawMessage) (any, *rpcErrorObject) {
	var flt struct {
		FromBlock string            `json:"fromBlock"`
		ToBlock   string            `json:"toBlock"`
		Address   json.RawMessage   `json:"address"`
		Topics    []json.RawMessage `json:"topics"`
	}
	if err := json.Unmarshal(raw, &flt); err != nil {
		return nil, badParams("bad filter")
	}
	from, err1 := parseUint64(flt.FromBlock)
	to, err2 := parseUint64(flt.ToBlock)
	if err1 != nil || err2 != nil {
		return nil, badParams("bad block range")
	}
	addrs, err := stringOrList(flt.Address)
	if err != nil {
		return nil, badParams("bad address")
	}
	if f.maxTopicOr > 0 && len(addrs) > f.maxTopicOr {
		return nil, &rpcErrorObject{Code: -32000, Message: "invalid filter: too many addresses"}
	}
	topics := make([][]string, len(flt.Topics))
	for i, t := range flt.Topics {
		if topics[i], err = stringOrList(t); err != nil {
			return nil, badParams("bad topics")
		}
		f.topicOrSizes = append(f.topicOrSizes, len(topics[i]))
		if f.maxTopicOr > 0 && len(topics[i]) > f.maxTopicOr {
			return nil, &rpcErrorObject{Code: -32000, Message: "invalid filter: too many topics"}
		}
	}
	// Like older geth: silently clamp to the node's head.
	to = min(to, f.head)
	out := []any{}
	for n := from; n <= to; n++ {
		b, ok := f.blocks[n]
		if !ok {
			continue
		}
		for _, l := range b.logs {
			if !f.sloppyLogs && !logMatches(l, addrs, topics) {
				continue
			}
			bh := b.hash
			if l.blockHash != "" {
				bh = l.blockHash
			}
			m := map[string]any{
				"address":          addrHex(l.address),
				"topics":           l.topics,
				"data":             l.data,
				"blockNumber":      formatUint64(n),
				"blockHash":        bh,
				"transactionHash":  l.txHash,
				"transactionIndex": "0x0",
				"logIndex":         formatUint64(l.logIndex),
				"removed":          l.removed,
			}
			if !f.omitLogTimestamp {
				m["blockTimestamp"] = formatUint64(b.time)
			}
			out = append(out, m)
		}
	}
	return out, nil
}

func stringOrList(raw json.RawMessage) ([]string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	if raw[0] == '"' {
		var s string
		err := json.Unmarshal(raw, &s)
		return []string{s}, err
	}
	var l []string
	err := json.Unmarshal(raw, &l)
	return l, err
}

func logMatches(l fakeLog, addrs []string, topics [][]string) bool {
	if len(addrs) > 0 && !slices.ContainsFunc(addrs, func(a string) bool { return strings.EqualFold(a, addrHex(l.address)) }) {
		return false
	}
	if len(topics) > len(l.topics) {
		return false
	}
	for i, alts := range topics {
		if len(alts) == 0 {
			continue
		}
		if !slices.ContainsFunc(alts, func(t string) bool { return strings.EqualFold(t, l.topics[i]) }) {
			return false
		}
	}
	return true
}

// ---- builders ----

func hashN(label string, i int) string {
	return "0x" + hex.EncodeToString(address.Keccak256([]byte(fmt.Sprintf("%s-%d", label, i))))
}

func addrN(i int) [20]byte {
	var a [20]byte
	copy(a[:], address.Keccak256([]byte(fmt.Sprintf("addr-%d", i)))[12:])
	return a
}

func ptr[T any](v T) *T { return &v }

func amountData(v *big.Int) string { return fmt.Sprintf("0x%064x", v) }

func transferLog(token, from, to [20]byte, amount int64, txHash string, logIndex uint64) fakeLog {
	return fakeLog{
		address:  token,
		topics:   []string{TransferTopic, addrTopic(from), addrTopic(to)},
		data:     amountData(big.NewInt(amount)),
		txHash:   txHash,
		logIndex: logIndex,
	}
}

func (f *fakeNode) addLog(n uint64, l fakeLog) {
	b := f.block(n)
	f.mu.Lock()
	defer f.mu.Unlock()
	b.logs = append(b.logs, l)
}

func (f *fakeNode) addTx(n uint64, tx fakeTx) {
	b := f.block(n)
	f.mu.Lock()
	defer f.mu.Unlock()
	b.txs = append(b.txs, tx)
}

func eth(n int64) *big.Int { return new(big.Int).Mul(big.NewInt(n), big.NewInt(1e18)) }

// ---- client helpers ----

func newRPCServer(t *testing.T, h http.Handler) (*httptest.Server, string) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, srv.URL + "/v2/" + testSecret + "?apikey=" + testSecret
}

func testChain(urls ...string) domain.Chain {
	return domain.Chain{ID: "testchain", Family: domain.FamilyEVM, Name: "Test", NetworkID: fmt.Sprint(testChainID), RPCURLs: urls}
}

func fastOpts(extra ...Option) []Option {
	return append([]Option{
		WithBackoff(time.Millisecond, 5*time.Millisecond),
		WithRequestTimeout(5 * time.Second),
		WithRetries(2),
	}, extra...)
}

func newTestClient(t *testing.T, f *fakeNode, opts ...Option) *Client {
	t.Helper()
	_, u := newRPCServer(t, f)
	c, err := New(testChain(u), fastOpts(opts...)...)
	require.NoError(t, err)
	return c
}

func requireNoSecret(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	require.NotContains(t, err.Error(), testSecret)
	require.NotContains(t, err.Error(), "/v2/")
}
