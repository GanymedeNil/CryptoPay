package utxo

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"cryptopay/internal/domain"
)

// rpcCall is one request received by fakeNode.
type rpcCall struct {
	JSONRPC string            `json:"jsonrpc"`
	ID      json.RawMessage   `json:"id"`
	Method  string            `json:"method"`
	Params  []json.RawMessage `json:"params"`

	rawParams   json.RawMessage
	path        string
	query       string
	contentType string
	user, pass  string
	hasAuth     bool
}

// fakeNode is an httptest JSON-RPC server speaking the Bitcoin Core dialect
// for getblockcount / getblockhash / getblock.
type fakeNode struct {
	t *testing.T

	mu     sync.Mutex
	head   uint64
	hashes map[uint64]string // height → hash
	blocks map[string]string // hash → raw getblock verbosity-2 result
	calls  []rpcCall

	// legacyErrors makes RPC errors use HTTP 500 like Bitcoin Core in
	// JSON-RPC 1.0 mode; otherwise HTTP 200 like JSON-RPC 2.0 mode.
	legacyErrors bool
	// intercept, when set, may answer a request itself (returns true).
	intercept func(w http.ResponseWriter, r *http.Request, c rpcCall) bool

	srv *httptest.Server
}

func newFakeNode(t *testing.T) *fakeNode {
	t.Helper()
	f := &fakeNode{t: t, hashes: map[uint64]string{}, blocks: map[string]string{}}
	f.srv = httptest.NewServer(f)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeNode) URL() string { return f.srv.URL }

func (f *fakeNode) addBlock(height uint64, hash, raw string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hashes[height] = hash
	f.blocks[hash] = raw
	if height > f.head {
		f.head = height
	}
}

func (f *fakeNode) count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if method == "" || c.Method == method {
			n++
		}
	}
	return n
}

func (f *fakeNode) lastCall(method string) rpcCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.calls) - 1; i >= 0; i-- {
		if f.calls[i].Method == method {
			return f.calls[i]
		}
	}
	f.t.Fatalf("no %s call recorded", method)
	return rpcCall{}
}

func (f *fakeNode) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read", http.StatusBadRequest)
		return
	}
	var c rpcCall
	if err := json.Unmarshal(body, &c); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	var raw struct {
		Params json.RawMessage `json:"params"`
	}
	_ = json.Unmarshal(body, &raw)
	c.rawParams = raw.Params
	c.path = r.URL.Path
	c.query = r.URL.RawQuery
	c.contentType = r.Header.Get("Content-Type")
	c.user, c.pass, c.hasAuth = r.BasicAuth()

	f.mu.Lock()
	f.calls = append(f.calls, c)
	intercept := f.intercept
	f.mu.Unlock()

	if intercept != nil && intercept(w, r, c) {
		return
	}

	switch c.Method {
	case "getblockcount":
		f.mu.Lock()
		head := f.head
		f.mu.Unlock()
		writeResult(w, c.ID, strconv.FormatUint(head, 10))
	case "getblockhash":
		if len(c.Params) != 1 {
			f.writeError(w, c.ID, -1, "getblockhash expects 1 param")
			return
		}
		h, err := strconv.ParseUint(string(c.Params[0]), 10, 64)
		if err != nil {
			f.writeError(w, c.ID, -3, "Expected type number")
			return
		}
		f.mu.Lock()
		hash, ok := f.hashes[h]
		f.mu.Unlock()
		if !ok {
			f.writeError(w, c.ID, -8, "Block height out of range")
			return
		}
		writeResult(w, c.ID, strconv.Quote(hash))
	case "getblock":
		if len(c.Params) != 2 || string(c.Params[1]) != "2" {
			f.writeError(w, c.ID, -1, "test node only serves verbosity 2")
			return
		}
		var hash string
		_ = json.Unmarshal(c.Params[0], &hash)
		f.mu.Lock()
		raw, ok := f.blocks[hash]
		f.mu.Unlock()
		if !ok {
			f.writeError(w, c.ID, -5, "Block not found")
			return
		}
		writeResult(w, c.ID, raw)
	default:
		f.writeError(w, c.ID, -32601, "Method not found")
	}
}

func writeResult(w http.ResponseWriter, id json.RawMessage, rawResult string) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"result":%s,"error":null,"id":%s}`, rawResult, idOrNull(id))
}

func (f *fakeNode) writeError(w http.ResponseWriter, id json.RawMessage, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	if f.legacyErrors {
		w.WriteHeader(http.StatusInternalServerError)
	}
	fmt.Fprintf(w, `{"result":null,"error":{"code":%d,"message":%q},"id":%s}`, code, msg, idOrNull(id))
}

func idOrNull(id json.RawMessage) string {
	if len(id) == 0 {
		return "null"
	}
	return string(id)
}

// ---- fixtures ----

func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return strings.TrimSpace(string(b))
}

func fakeHash(n uint64) string { return fmt.Sprintf("%064x", n) }

func fakeTxid(n int) string { return fmt.Sprintf("%064x", 0xabc000+n) }

// synthBlock builds a verbosity-2 block. extra is spliced in verbatim (raw
// members, e.g. `"mweb":{...}`) before "tx"; "time" deliberately comes after
// "tx" as in Litecoin Core.
func synthBlock(hash string, height uint64, blockTime int64, extra string, txs ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `{"hash":%q,"confirmations":3,"height":%d,"version":536870912,`, hash, height)
	if extra != "" {
		b.WriteString(extra)
		b.WriteString(",")
	}
	fmt.Fprintf(&b, `"tx":[%s],"time":%d,"mediantime":%d,"nTx":%d}`, strings.Join(txs, ","), blockTime, blockTime-600, len(txs))
	return b.String()
}

func synthTx(txid string, vouts ...string) string {
	return fmt.Sprintf(`{"txid":%q,"hash":%q,"version":2,"size":200,"vsize":150,"weight":600,"locktime":0,`+
		`"vin":[{"txid":"%064x","vout":0,"scriptSig":{"asm":"","hex":""},"txinwitness":["3044","02aa"],"sequence":4294967295}],`+
		`"vout":[%s],"fee":0.00001117,"hex":"0200000001"}`, txid, txid, 1, strings.Join(vouts, ","))
}

// vout builds a modern output (Bitcoin Core >= 22: single "address").
func vout(n int, value, addr, typ string) string {
	return fmt.Sprintf(`{"value":%s,"n":%d,"scriptPubKey":{"asm":"0 abcd","desc":"addr(%s)#xxxx","hex":"0014abcd","address":%q,"type":%q}}`,
		value, n, addr, addr, typ)
}

// legacyVout builds an output in the pre-22 / Litecoin Core 0.21 shape.
func legacyVout(n int, value string, addrs ...string) string {
	quoted := make([]string, len(addrs))
	for i, a := range addrs {
		quoted[i] = strconv.Quote(a)
	}
	return fmt.Sprintf(`{"ismweb":false,"value":%s,"n":%d,"scriptPubKey":{"asm":"OP_DUP OP_HASH160 ab OP_EQUALVERIFY OP_CHECKSIG","hex":"76a9","reqSigs":%d,"type":"pubkeyhash","addresses":[%s]}}`,
		value, n, len(addrs), strings.Join(quoted, ","))
}

func nulldataVout(n int) string {
	return fmt.Sprintf(`{"value":0.00000000,"n":%d,"scriptPubKey":{"asm":"OP_RETURN 6a","desc":"raw(6a)#x","hex":"6a046869","type":"nulldata"}}`, n)
}

// ---- client helpers ----

func testChain(urls ...string) domain.Chain {
	return domain.Chain{
		ID: "bitcoin", Family: domain.FamilyUTXO, Name: "Bitcoin", NativeSymbol: "BTC",
		RPCURLs: urls,
		Params:  domain.ChainParam{Bech32HRP: "bc", P2PKHVersion: 0x00, P2SHVersion: 0x05},
	}
}

func litecoinChain(urls ...string) domain.Chain {
	return domain.Chain{
		ID: "litecoin", Family: domain.FamilyUTXO, Name: "Litecoin", NativeSymbol: "LTC",
		RPCURLs: urls,
		Params:  domain.ChainParam{Bech32HRP: "ltc", P2PKHVersion: 0x30, P2SHVersion: 0x32},
	}
}

// fastOpts keeps retries quick in tests.
func fastOpts(extra ...Option) []Option {
	return append([]Option{
		WithRetry(3, time.Millisecond, 5*time.Millisecond),
		WithTimeouts(2*time.Second, 5*time.Second),
	}, extra...)
}

func newTestClient(t *testing.T, c domain.Chain, opts ...Option) *Client {
	t.Helper()
	cl, err := New(c, fastOpts(opts...)...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return cl
}
