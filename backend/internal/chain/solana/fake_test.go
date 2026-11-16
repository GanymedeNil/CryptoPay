package solana

import (
	"bytes"
	"crypto/sha512"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcutil/base58"
	"github.com/stretchr/testify/require"

	"cryptopay/internal/domain"
)

// Mints used by the fixtures.
const (
	usdcDevnet  = "4zMMC9srt5Ri5X14GAgXhaHii3GnPAEERYPJgZJDncDU"
	usdcMainnet = "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"
	usdtMainnet = "Es9vMFrzaCERmJfrF4H2FYD4KCoNkY11McCe8BenwNYB"
	wsolMint    = "So11111111111111111111111111111111111111112"
)

// fakeNode is an in-process Solana JSON-RPC node serving captured
// getTransaction results (testdata/*.json, recorded from mainnet-beta and
// devnet) and getSignaturesForAddress / getTokenAccountsByOwner /
// getSignatureStatuses responses shaped like the real ones.
type fakeNode struct {
	t   *testing.T
	srv *httptest.Server

	mu            sync.Mutex
	head          uint64
	txs           map[string]json.RawMessage // signature → getTransaction result
	txVersion     map[string]int             // signature → version (legacy = -1)
	sigs          map[string][]sigEntry      // address → newest first
	tokenAccounts map[string][]tokenAcct     // owner → token accounts
	statuses      map[string]string          // signature → raw status object
	calls         map[string]int             // method → count
	txCalls       map[string]int             // signature → getTransaction count
	pageCap       int                        // >0: serve at most this many signatures per page
	// intercept, when set, may answer a request itself (return true).
	intercept func(w http.ResponseWriter, method string, params []json.RawMessage) bool
}

type sigEntry struct {
	Signature string
	Slot      uint64
	BlockTime int64
	Err       json.RawMessage // nil = success
}

type tokenAcct struct {
	Pubkey, Mint, Amount string
}

func newFakeNode(t *testing.T) *fakeNode {
	f := &fakeNode{
		t:             t,
		head:          600_000_000,
		txs:           map[string]json.RawMessage{},
		txVersion:     map[string]int{},
		sigs:          map[string][]sigEntry{},
		tokenAccounts: map[string][]tokenAcct{},
		statuses:      map[string]string{},
		calls:         map[string]int{},
		txCalls:       map[string]int{},
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

// fixture is the identifying part of a captured transaction.
type fixture struct {
	Sig       string
	Slot      uint64
	BlockTime int64
	Keys      []string
	Err       json.RawMessage
	Raw       json.RawMessage
}

func loadFixture(t *testing.T, name string) fixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	return parseFixture(t, raw)
}

func parseFixture(t *testing.T, raw []byte) fixture {
	t.Helper()
	var tx struct {
		Slot        uint64 `json:"slot"`
		BlockTime   int64  `json:"blockTime"`
		Transaction struct {
			Signatures []string `json:"signatures"`
			Message    struct {
				AccountKeys []accountKey `json:"accountKeys"`
			} `json:"message"`
		} `json:"transaction"`
		Meta struct {
			Err json.RawMessage `json:"err"`
		} `json:"meta"`
	}
	require.NoError(t, json.Unmarshal(raw, &tx))
	f := fixture{Sig: tx.Transaction.Signatures[0], Slot: tx.Slot, BlockTime: tx.BlockTime, Raw: raw}
	if !isNullJSON(tx.Meta.Err) {
		f.Err = tx.Meta.Err
	}
	for _, k := range tx.Transaction.Message.AccountKeys {
		f.Keys = append(f.Keys, k.Pubkey)
	}
	return f
}

// rewriteFixture returns a copy of raw with a new signature and slot (numbers
// are kept verbatim: balances exceed 2^53).
func rewriteFixture(t *testing.T, raw json.RawMessage, sig string, slot uint64, edit func(m map[string]any)) json.RawMessage {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	require.NoError(t, dec.Decode(&m))
	if sig != "" {
		m["transaction"].(map[string]any)["signatures"].([]any)[0] = sig
	}
	if slot != 0 {
		m["slot"] = json.Number(fmt.Sprint(slot))
	}
	if edit != nil {
		edit(m)
	}
	out, err := json.Marshal(m)
	require.NoError(t, err)
	return out
}

// fakeSig returns a deterministic, well-formed base58 64-byte signature.
func fakeSig(seed string) string {
	h := sha512.Sum512([]byte(seed))
	return base58.Encode(h[:])
}

// addFixture registers a captured transaction: getTransaction serves it and
// its signature is listed for every account key, exactly as a node indexes it.
func (f *fakeNode) addFixture(fx fixture) fixture {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.txs[fx.Sig] = fx.Raw
	var v struct {
		Version any `json:"version"`
	}
	_ = json.Unmarshal(fx.Raw, &v)
	switch ver := v.Version.(type) {
	case float64:
		f.txVersion[fx.Sig] = int(ver)
	default:
		f.txVersion[fx.Sig] = -1
	}
	for _, k := range fx.Keys {
		f.addSigLocked(k, sigEntry{Signature: fx.Sig, Slot: fx.Slot, BlockTime: fx.BlockTime, Err: fx.Err})
	}
	return fx
}

// putTx serves raw for sig without listing it anywhere.
func (f *fakeNode) putTx(sig string, raw json.RawMessage, version int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.txs[sig] = raw
	f.txVersion[sig] = version
}

func (f *fakeNode) setHead(h uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.head = h
}

func (f *fakeNode) setPageCap(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pageCap = n
}

func (f *fakeNode) setIntercept(fn func(w http.ResponseWriter, method string, params []json.RawMessage) bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.intercept = fn
}

func (f *fakeNode) setStatus(sig, raw string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses[sig] = raw
}

func (f *fakeNode) addSig(addr string, e sigEntry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.addSigLocked(addr, e)
}

func (f *fakeNode) addSigLocked(addr string, e sigEntry) {
	l := append(f.sigs[addr], e)
	// newest first; ties broken by signature (descending) for determinism
	slices.SortStableFunc(l, func(a, b sigEntry) int {
		if a.Slot != b.Slot {
			if a.Slot > b.Slot {
				return -1
			}
			return 1
		}
		return strings.Compare(b.Signature, a.Signature)
	})
	f.sigs[addr] = l
}

func (f *fakeNode) addTokenAccount(owner string, a tokenAcct) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokenAccounts[owner] = append(f.tokenAccounts[owner], a)
}

func (f *fakeNode) count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[method]
}

func (f *fakeNode) txCount(sig string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.txCalls[sig]
}

func (f *fakeNode) client(t *testing.T, opts ...Option) *Client {
	t.Helper()
	return newTestClient(t, []string{f.srv.URL}, opts...)
}

func newTestClient(t *testing.T, urls []string, opts ...Option) *Client {
	t.Helper()
	base := []Option{WithBackoff(time.Millisecond, 4*time.Millisecond), WithRequestTimeout(5 * time.Second)}
	c, err := New(domain.Chain{ID: "solana-test", Family: domain.FamilySolana, RPCURLs: urls}, append(base, opts...)...)
	require.NoError(t, err)
	return c
}

type rpcReq struct {
	ID     json.RawMessage   `json:"id"`
	Method string            `json:"method"`
	Params []json.RawMessage `json:"params"`
}

func writeResult(w http.ResponseWriter, id json.RawMessage, result any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func writeRPCError(w http.ResponseWriter, id json.RawMessage, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": msg}})
}

func (f *fakeNode) serve(w http.ResponseWriter, r *http.Request) {
	var req rpcReq
	if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&req) != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.calls[req.Method]++
	intercept := f.intercept
	f.mu.Unlock()
	if intercept != nil && intercept(w, req.Method, req.Params) {
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	param := func(i int, v any) bool {
		if i >= len(req.Params) || json.Unmarshal(req.Params[i], v) != nil {
			writeRPCError(w, req.ID, -32602, fmt.Sprintf("Invalid params: param %d", i))
			return false
		}
		return true
	}
	type cfg struct {
		Commitment     string  `json:"commitment"`
		Encoding       string  `json:"encoding"`
		MinContextSlot *uint64 `json:"minContextSlot"`
		Limit          int     `json:"limit"`
		Before         string  `json:"before"`
		MaxVersion     *int    `json:"maxSupportedTransactionVersion"`
		SearchHistory  bool    `json:"searchTransactionHistory"`
	}
	minSlotOK := func(c cfg) bool {
		if c.MinContextSlot != nil && *c.MinContextSlot > f.head {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","error":{"code":-32016,"message":"Minimum context slot has not been reached","data":{"contextSlot":%d}},"id":%s}`, f.head, req.ID)
			return false
		}
		return true
	}

	switch req.Method {
	case "getSlot":
		var c cfg
		if !param(0, &c) {
			return
		}
		if c.Commitment != "finalized" {
			writeRPCError(w, req.ID, -32602, "expected finalized commitment")
			return
		}
		writeResult(w, req.ID, f.head)

	case "getTokenAccountsByOwner":
		var owner string
		var filter struct {
			Mint string `json:"mint"`
		}
		var c cfg
		if !param(0, &owner) || !param(1, &filter) || !param(2, &c) || !minSlotOK(c) {
			return
		}
		if c.Encoding != "jsonParsed" || c.Commitment != "finalized" || filter.Mint == "" {
			writeRPCError(w, req.ID, -32602, "unexpected getTokenAccountsByOwner config")
			return
		}
		value := []any{}
		for _, a := range f.tokenAccounts[owner] {
			if a.Mint != filter.Mint {
				continue
			}
			value = append(value, map[string]any{
				"pubkey": a.Pubkey,
				"account": map[string]any{
					"lamports": 2039280, "executable": false, "space": 165,
					"owner": "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA", "rentEpoch": json.Number("18446744073709551615"),
					"data": map[string]any{"program": "spl-token", "space": 165, "parsed": map[string]any{
						"type": "account",
						"info": map[string]any{
							"isNative": false, "mint": a.Mint, "owner": owner, "state": "initialized",
							"tokenAmount": map[string]any{"amount": a.Amount, "decimals": 6, "uiAmountString": "0"},
						},
					}},
				},
			})
		}
		writeResult(w, req.ID, map[string]any{"context": map[string]any{"apiVersion": "4.4.0", "slot": f.head}, "value": value})

	case "getSignaturesForAddress":
		var addr string
		var c cfg
		if !param(0, &addr) || !param(1, &c) || !minSlotOK(c) {
			return
		}
		if c.Commitment != "finalized" || c.Limit <= 0 || c.Limit > 1000 {
			writeRPCError(w, req.ID, -32602, "unexpected getSignaturesForAddress config")
			return
		}
		list := f.sigs[addr]
		start := 0
		if c.Before != "" {
			start = slices.IndexFunc(list, func(e sigEntry) bool { return e.Signature == c.Before })
			if start < 0 {
				writeRPCError(w, req.ID, -32602, "Invalid param: before signature not found")
				return
			}
			start++
		}
		limit := c.Limit
		if f.pageCap > 0 {
			limit = min(limit, f.pageCap)
		}
		out := []any{}
		for _, e := range list[start:min(len(list), start+limit)] {
			if e.Slot > f.head {
				continue
			}
			var errv any
			if e.Err != nil {
				errv = e.Err
			}
			out = append(out, map[string]any{
				"blockTime": e.BlockTime, "confirmationStatus": "finalized", "err": errv,
				"memo": nil, "signature": e.Signature, "slot": e.Slot, "transactionIndex": 7,
			})
		}
		writeResult(w, req.ID, out)

	case "getTransaction":
		var sig string
		var c cfg
		if !param(0, &sig) || !param(1, &c) {
			return
		}
		f.txCalls[sig]++
		if c.Encoding != "jsonParsed" || c.Commitment != "finalized" || c.MaxVersion == nil {
			writeRPCError(w, req.ID, -32602, "unexpected getTransaction config")
			return
		}
		raw, ok := f.txs[sig]
		if !ok {
			writeResult(w, req.ID, nil)
			return
		}
		if v := f.txVersion[sig]; v > *c.MaxVersion {
			writeRPCError(w, req.ID, -32015, fmt.Sprintf("Transaction version (%d) is not supported by the requesting client. Please try the request again with the following configuration parameter: \"maxSupportedTransactionVersion\": %d", v, v))
			return
		}
		writeResult(w, req.ID, raw)

	case "getSignatureStatuses":
		var sigs []string
		var c cfg
		if !param(0, &sigs) || !param(1, &c) {
			return
		}
		if !c.SearchHistory {
			writeRPCError(w, req.ID, -32602, "expected searchTransactionHistory")
			return
		}
		value := make([]json.RawMessage, len(sigs))
		for i, s := range sigs {
			if st, ok := f.statuses[s]; ok {
				value[i] = json.RawMessage(st)
			} else {
				value[i] = json.RawMessage("null")
			}
		}
		writeResult(w, req.ID, map[string]any{"context": map[string]any{"apiVersion": "4.4.0", "slot": f.head}, "value": value})

	default:
		writeRPCError(w, req.ID, -32601, "Method not found")
	}
}
