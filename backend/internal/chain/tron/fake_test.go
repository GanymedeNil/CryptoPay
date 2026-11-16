package tron

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cryptopay/internal/address"
	"cryptopay/internal/domain"
)

// Fixtures in testdata/ are real TronGrid mainnet / Nile responses
// (captured 2026-11-16), trimmed to a subset of the transactions of each
// block. Their infos are kept in the same order as the block transactions.
const (
	blkA      = 88045575 // TRX, TRC-10, delegate, USDT (single + multi-log), REVERT and OUT_OF_ENERGY USDT calls
	blkB      = 88045581 // TRX, USDT, USDT+WTRX swap, account create, OUT_OF_ENERGY
	blkEmpty  = 1000     // real empty block
	nileHead  = 72756663 // testdata/getblock_nile_head.json
	usdtHex   = "a614f803b6fd780986a42c78ec9c7f77e6ded13c"
	wtrxHex   = "891cdb91d149f23b1a45d9c5ca78a88d0cb44c18"
	blkAHash  = "00000000053f78071832cd69f0c83246eeb605f02d5386b47b86da9b8aa05930"
	blkBHash  = "00000000053f780d532285cfb608180922328d57087035807585494a53f9dd1c"
	blkATime  = 1794810660000
	blkBTime  = 1794810678000
	usdtB58   = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"
	revertTx  = "c74f51a7021296c7bbeaeecff72f7535e5e4d2cce18b3fb2973335d49e0ece43"
	oomTx     = "5dbc89f361bebf96ca6ea159062ec9ff87be10d1c8be3ef176716e1c91a3efdf"
	swapTx    = "eedca1182c6c0e930cf80bcdae37524a48733b5b3ed07f2e648d3550163d87eb"
	trxTxA    = "2f76d34cd3ebf758293230cb51af6a33b48d44aadccda585ad2ea07217ed21d8"
	usdtTxA   = "9a55ed2754448533b9709ecb8bd0f90f5a680e1798f0d0729a051ae298c37500"
	payoutTxA = "efdf1b447c7572972bad03a891d9a22ca97fae16451b053bdd0c98c944fbe7e1"
)

// b58 converts a 20-byte hex account (as found in logs / topics) or a
// "41"-hex address to base58.
func b58(t testing.TB, h string) string {
	t.Helper()
	s, err := address.ParseTron(h)
	if err != nil {
		t.Fatalf("b58(%s): %v", h, err)
	}
	return s
}

func readFixture(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// jsonEdit decodes data, applies fn and re-encodes it.
func jsonEdit[T any](t testing.TB, data []byte, fn func(*T)) []byte {
	t.Helper()
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	fn(&v)
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

type obj = map[string]any

// emptyBlockJSON returns a block with the exact shape of the real empty
// block testdata/block_1000.json, moved to height num.
func emptyBlockJSON(t testing.TB, num uint64, tsMillis int64) []byte {
	return jsonEdit(t, readFixture(t, "block_1000.json"), func(b *obj) {
		id := (*b)["blockID"].(string)
		(*b)["blockID"] = fmt.Sprintf("%016x", num) + id[16:]
		raw := (*b)["block_header"].(obj)["raw_data"].(obj)
		raw["number"] = num
		raw["timestamp"] = tsMillis
	})
}

type rpcCall struct {
	Path   string
	Body   string
	APIKey string
	Query  string
}

// fakeNode is an httptest java-tron full node serving fixtures.
type fakeNode struct {
	t   testing.TB
	srv *httptest.Server

	mu       sync.Mutex
	blocks   map[uint64][]byte // /wallet/getblockbynum
	infos    map[uint64][]byte // /wallet/gettransactioninfobyblocknum
	head     []byte            // /wallet/getblock (detail=false)
	nowBlock []byte            // /wallet/getnowblock
	calls    []rpcCall
	// hook may answer a request itself (return true); n is the 1-based
	// number of requests seen so far for this path.
	hook func(w http.ResponseWriter, r *http.Request, path string, n int) bool

	inFlight, maxInFlight atomic.Int32
}

func newFakeNode(t testing.TB) *fakeNode {
	f := &fakeNode{
		t:      t,
		blocks: map[uint64][]byte{},
		infos:  map[uint64][]byte{},
	}
	f.blocks[blkA] = readFixture(t, "block_88045575.json")
	f.infos[blkA] = readFixture(t, "txinfo_88045575.json")
	f.blocks[blkB] = readFixture(t, "block_88045581.json")
	f.infos[blkB] = readFixture(t, "txinfo_88045581.json")
	f.blocks[blkEmpty] = readFixture(t, "block_1000.json")
	f.infos[blkEmpty] = []byte("[]")
	// Blocks between A and B: empty, so ranges across both are contiguous.
	for n := uint64(blkA + 1); n < blkB; n++ {
		f.blocks[n] = emptyBlockJSON(t, n, blkATime+int64(n-blkA)*3000)
		f.infos[n] = []byte("[]")
	}
	f.head = readFixture(t, "getblock_nile_head.json")
	f.nowBlock = f.blocks[blkB]
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeNode) URL() string { return f.srv.URL }

func (f *fakeNode) setBlock(n uint64, b []byte) { f.mu.Lock(); f.blocks[n] = b; f.mu.Unlock() }
func (f *fakeNode) setInfos(n uint64, b []byte) { f.mu.Lock(); f.infos[n] = b; f.mu.Unlock() }
func (f *fakeNode) setHook(h func(w http.ResponseWriter, r *http.Request, path string, n int) bool) {
	f.mu.Lock()
	f.hook = h
	f.mu.Unlock()
}

func (f *fakeNode) callsTo(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasSuffix(c.Path, path) {
			n++
		}
	}
	return n
}

func (f *fakeNode) allCalls() []rpcCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]rpcCall(nil), f.calls...)
}

func (f *fakeNode) serve(w http.ResponseWriter, r *http.Request) {
	cur := f.inFlight.Add(1)
	defer f.inFlight.Add(-1)
	for {
		m := f.maxInFlight.Load()
		if cur <= m || f.maxInFlight.CompareAndSwap(m, cur) {
			break
		}
	}

	body, _ := io.ReadAll(r.Body)
	path := r.URL.Path
	if i := strings.Index(path, "/wallet/"); i >= 0 {
		path = path[i:]
	}
	f.mu.Lock()
	f.calls = append(f.calls, rpcCall{Path: r.URL.Path, Body: string(body), APIKey: r.Header.Get(apiKeyHeader), Query: r.URL.RawQuery})
	n := 0
	for _, c := range f.calls {
		if strings.HasSuffix(c.Path, path) {
			n++
		}
	}
	hook := f.hook
	f.mu.Unlock()

	if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
		http.Error(w, "bad method or content type", http.StatusBadRequest)
		return
	}
	if hook != nil && hook(w, r, path, n) {
		return
	}

	var req struct {
		Num    *uint64 `json:"num"`
		Value  string  `json:"value"`
		Detail *bool   `json:"detail"`
	}
	if len(body) > 0 && json.Unmarshal(body, &req) != nil {
		writeJSON(w, `{"Error":"class org.tron.core.services.http.JsonFormat$ParseException : 1:2: Expected identifier. -{"}`)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch path {
	case "/wallet/getblock":
		writeJSON(w, string(f.head))
	case "/wallet/getnowblock":
		writeJSON(w, string(f.nowBlock))
	case "/wallet/getblockbynum":
		if req.Num == nil {
			writeJSON(w, `{}`)
			return
		}
		if b, ok := f.blocks[*req.Num]; ok {
			writeJSON(w, string(b))
			return
		}
		writeJSON(w, `{}`) // what java-tron answers for an unknown height
	case "/wallet/gettransactioninfobyblocknum":
		if req.Num == nil {
			writeJSON(w, `[]`)
			return
		}
		if b, ok := f.infos[*req.Num]; ok {
			writeJSON(w, string(b))
			return
		}
		writeJSON(w, `[]`)
	case "/wallet/gettransactioninfobyid":
		for _, raw := range f.infos {
			var infos []json.RawMessage
			_ = json.Unmarshal(raw, &infos)
			for _, inf := range infos {
				var id struct {
					ID string `json:"id"`
				}
				_ = json.Unmarshal(inf, &id)
				if id.ID == req.Value {
					writeJSON(w, string(inf))
					return
				}
			}
		}
		writeJSON(w, `{}`)
	default:
		// java-tron answers unknown servlets with a Jetty 405 HTML page.
		w.Header().Set("Content-Type", "text/html;charset=iso-8859-1")
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = io.WriteString(w, "<html><body><h2>HTTP ERROR 405 HTTP method POST is not supported by this URL</h2><td>"+r.URL.Path+"</td></body></html>")
	}
}

func writeJSON(w http.ResponseWriter, s string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = io.WriteString(w, s)
}

func tronChain(urls ...string) domain.Chain {
	return domain.Chain{ID: "tron", Family: domain.FamilyTron, Name: "Tron", NativeSymbol: "TRX", RPCURLs: urls}
}

// fastOpts keeps retries quick in tests.
func fastOpts(extra ...Option) []Option {
	return append([]Option{WithBackoff(time.Millisecond, 5*time.Millisecond), WithRequestTimeout(5 * time.Second)}, extra...)
}

func newTestClient(t testing.TB, urls []string, opts ...Option) *Client {
	t.Helper()
	c, err := New(tronChain(urls...), fastOpts(opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func mustHex(t testing.TB, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
