package utxo

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"cryptopay/internal/chain"
	"cryptopay/internal/domain"
)

// want is a compact expected transfer.
type want struct {
	tx     string
	index  int
	to     string
	amount string
}

func checkTransfers(t *testing.T, got []chain.Transfer, height uint64, hash string, blockTime int64, wants []want) {
	t.Helper()
	if len(got) != len(wants) {
		for _, g := range got {
			t.Logf("got %s:%d %s %s", g.TxHash, g.Index, g.To, g.Amount)
		}
		t.Fatalf("got %d transfers, want %d", len(got), len(wants))
	}
	for i, w := range wants {
		g := got[i]
		if g.TxHash != w.tx || g.Index != w.index || g.To != w.to || g.Amount == nil || g.Amount.String() != w.amount {
			t.Errorf("transfer %d = {%s:%d to %s amount %v}, want {%s:%d to %s amount %s}",
				i, g.TxHash, g.Index, g.To, g.Amount, w.tx, w.index, w.to, w.amount)
		}
		if g.BlockNumber != height || g.BlockHash != hash {
			t.Errorf("transfer %d block = %d/%s, want %d/%s", i, g.BlockNumber, g.BlockHash, height, hash)
		}
		if !g.BlockTime.Equal(time.Unix(blockTime, 0)) || g.BlockTime.Location() != time.UTC {
			t.Errorf("transfer %d block time = %v, want %v UTC", i, g.BlockTime, time.Unix(blockTime, 0).UTC())
		}
		if g.From != "" || g.Token != "" {
			t.Errorf("transfer %d from/token = %q/%q, want empty", i, g.From, g.Token)
		}
	}
}

func TestNew(t *testing.T) {
	if _, err := New(domain.Chain{ID: "ethereum", Family: domain.FamilyEVM, RPCURLs: []string{"http://x"}}); err == nil {
		t.Error("New accepted an EVM chain")
	}
	for _, urls := range [][]string{nil, {}, {"", "  "}} {
		if _, err := New(testChain(urls...)); !errors.Is(err, chain.ErrNoRPC) {
			t.Errorf("New(%q) error = %v, want ErrNoRPC", urls, err)
		}
	}
	for _, bad := range []string{"ftp://user:hunter2@host/key-123", "host-without-scheme/key-123", "http://", "http://user:hunter2@[::1/key-123"} {
		_, err := New(testChain("http://ok.example", bad))
		if err == nil {
			t.Errorf("New accepted %q", bad)
			continue
		}
		if msg := err.Error(); strings.Contains(msg, "hunter2") || strings.Contains(msg, "key-123") {
			t.Errorf("error leaks URL secrets: %v", err)
		}
	}
	cl, err := New(testChain("http://u:p@127.0.0.1:8332/", "https://node.example/key"))
	if err != nil || cl == nil {
		t.Fatalf("New: %v", err)
	}

	f := Factory(WithRetry(1, 0, 0))
	if c, err := f(testChain()); err == nil || c != nil {
		t.Errorf("Factory with no URLs = %v, %v; want nil interface and error", c, err)
	}
	if c, err := f(testChain("http://127.0.0.1:8332")); err != nil || c == nil {
		t.Errorf("Factory = %v, %v", c, err)
	}
}

func TestHead(t *testing.T) {
	f := newFakeNode(t)
	f.head = 976086
	cl := newTestClient(t, testChain(f.URL()))

	head, err := cl.Head(context.Background())
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if head != 976086 {
		t.Fatalf("Head = %d, want 976086", head)
	}
	c := f.lastCall("getblockcount")
	if c.JSONRPC != "2.0" || len(c.ID) == 0 || string(c.rawParams) != "[]" {
		t.Errorf("request = jsonrpc %q id %s params %s; want 2.0, an id and []", c.JSONRPC, c.ID, c.rawParams)
	}
	if c.contentType != "application/json" {
		t.Errorf("Content-Type = %q", c.contentType)
	}
}

func TestHeadBadResult(t *testing.T) {
	for _, res := range []string{`"976086"`, `-1`, `1.5`, `{}`, `null`} {
		t.Run(res, func(t *testing.T) {
			f := newFakeNode(t)
			f.intercept = func(w http.ResponseWriter, _ *http.Request, c rpcCall) bool {
				writeResult(w, c.ID, res)
				return true
			}
			cl := newTestClient(t, testChain(f.URL()))
			if h, err := cl.Head(context.Background()); err == nil {
				t.Fatalf("Head = %d, want error", h)
			}
		})
	}
}

// Real Bitcoin mainnet block 976080 (trimmed to 7 of its 7637 txs): segwit
// v0/v1, P2PKH, P2SH, P2WSH, OP_RETURN outputs, and a tx (b14e…) paying the
// same address twice.
func TestScanRealBitcoinBlock(t *testing.T) {
	const (
		height = 976080
		hash   = "00000000000000000001f5d21eb2b91d6d53fe51a700ff175202da9e98142ad6"
		btime  = 1794808156
	)
	f := newFakeNode(t)
	f.addBlock(height, hash, readFixture(t, "btc_block_976080.json"))
	cl := newTestClient(t, testChain(f.URL()))

	got, err := cl.ScanTransfers(context.Background(), chain.ScanRequest{
		FromBlock: height, ToBlock: height, Native: true,
		Addresses: []string{
			"bc1qq2mvrp4g3ugd424dw4xv53rgsf8szkrv853jrc",                     // twice in one tx
			"bc1phpuf5cccjdqps47tyemk75eez7fztgsad7e5gk9vm2jkv6cgffeqsp328c", // taproot
			"16GM3SzPJfxsovQj3jhcqUAAmbKs6DiBwZ",                             // P2PKH
			"3K9KZZPB8NRwZVP5wNKX4VYhnswrJxpgZ4",                             // P2SH, coinbase output
			"BC1QF3DECNV7JHZQCD9PEHREVHGMU774AFE40PWD05AT0SLRKXJD9UPSSTH3D4", // P2WSH given upper-case
			"bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq",                     // not in block
		},
	})
	if err != nil {
		t.Fatalf("ScanTransfers: %v", err)
	}
	checkTransfers(t, got, height, hash, btime, []want{
		{"a44197074c867650b785a3c0b9248817aae8bcfeb05712897bf93b0376285985", 0, "3K9KZZPB8NRwZVP5wNKX4VYhnswrJxpgZ4", "546"},
		{"a6f6fa2ba08f56cc62cac3059ff97f81759cd4a47cc3cf2324a459bc2f0f0c60", 0, "bc1phpuf5cccjdqps47tyemk75eez7fztgsad7e5gk9vm2jkv6cgffeqsp328c", "25701"},
		{"a6f6fa2ba08f56cc62cac3059ff97f81759cd4a47cc3cf2324a459bc2f0f0c60", 1, "16GM3SzPJfxsovQj3jhcqUAAmbKs6DiBwZ", "1528000000"},
		{"97c4b04f9d718b7135e56ce29762f5a51eb95bf950aaeb26788c0771c951b4ef", 0, "bc1qf3decnv7jhzqcd9pehrevhgmu774afe40pwd05at0slrkxjd9upssth3d4", "4402"},
		{"b14e4dbac40cdf2f0637a59f6fa9ed17ccbf914297582d5d501a087f137e0e5c", 0, "bc1qq2mvrp4g3ugd424dw4xv53rgsf8szkrv853jrc", "4254270"},
		{"b14e4dbac40cdf2f0637a59f6fa9ed17ccbf914297582d5d501a087f137e0e5c", 3, "bc1qq2mvrp4g3ugd424dw4xv53rgsf8szkrv853jrc", "498216"},
	})

	c := f.lastCall("getblock")
	if want := fmt.Sprintf(`[%q,2]`, hash); string(c.rawParams) != want {
		t.Errorf("getblock params = %s, want %s", c.rawParams, want)
	}
	if c := f.lastCall("getblockhash"); string(c.rawParams) != "[976080]" {
		t.Errorf("getblockhash params = %s", c.rawParams)
	}
}

// Real Litecoin block 3213990 from Litecoin Core 0.21.5: legacy "addresses"
// arrays, "ismweb" flags, an "mweb" block section, the MWEB HogEx tx with a
// witness_mweb_hogaddr output, and "tx" serialized before "time".
func TestScanRealLitecoinBlock(t *testing.T) {
	const (
		height = 3213990
		hash   = "27c5fa41a77962f628bbffb23d0d5d400b38102d3d18d230f61b8120ad138c28"
		btime  = 1794809183
	)
	f := newFakeNode(t)
	f.addBlock(height, hash, readFixture(t, "ltc_block_3213990.json"))
	cl := newTestClient(t, litecoinChain(f.URL()))

	got, err := cl.ScanTransfers(context.Background(), chain.ScanRequest{
		FromBlock: height, ToBlock: height, Native: true,
		Addresses: []string{
			"LQEckxsZGrVyvwoCDt37CwYJwHPkYWEZ3S",
			"LTC1QEJCEXWSNH003X4XCSH380Z3UWG2UL58DN6L02R", // upper-case bech32
			"MG43fLmhjKXiuBBQF3ZnkZGcLt61u4cb6K",
			"LdTHmBNQewwFpGEGdFf3rRWywvTBYNTH63", // coinbase
		},
	})
	if err != nil {
		t.Fatalf("ScanTransfers: %v", err)
	}
	checkTransfers(t, got, height, hash, btime, []want{
		{"c2d84d8e5f284b3a148484e92a3fa3c0a0d1c86db1284269294af82436f5b48b", 1, "LdTHmBNQewwFpGEGdFf3rRWywvTBYNTH63", "625002655"},
		{"089cdecfebcec46affc43988e1d3f4da2dc890e08ab2d489408102661a4f26e1", 0, "LQEckxsZGrVyvwoCDt37CwYJwHPkYWEZ3S", "1700533264"},
		{"86f7ec5f9df17384864297c697898f6408bb21a1b5b89d74020b1657167cdbff", 0, "MG43fLmhjKXiuBBQF3ZnkZGcLt61u4cb6K", "898674455"},
		{"86f7ec5f9df17384864297c697898f6408bb21a1b5b89d74020b1657167cdbff", 1, "ltc1qejcexwsnh003x4xcsh380z3uwg2ul58dn6l02r", "36180999123"},
	})
}

// Real Bitcoin testnet3 block from Bitcoin Core 30 (has the new "coinbase_tx"
// object member and "target").
func TestScanRealTestnetBlock(t *testing.T) {
	const (
		height = 5368670
		hash   = "000000000000033d2952629bc4ac16732c9c04fab4b6457879d205f1050cbf64"
		btime  = 1794816300
	)
	f := newFakeNode(t)
	f.addBlock(height, hash, readFixture(t, "tbtc_block_5368670.json"))
	c := testChain(f.URL())
	c.ID, c.Params.Bech32HRP = "bitcoin-testnet", "tb"
	cl := newTestClient(t, c)

	got, err := cl.ScanTransfers(context.Background(), chain.ScanRequest{
		FromBlock: height, ToBlock: height, Native: true,
		Addresses: []string{"tb1q9cqn0svtrm2lrzdhv55s4n6zultk2zahl500e5"},
	})
	if err != nil {
		t.Fatalf("ScanTransfers: %v", err)
	}
	checkTransfers(t, got, height, hash, btime, []want{
		{"1529bda03e44daab2bd1d7cceb0303c50a275d0954a098e8fb38b69ea028b59c", 0, "tb1q9cqn0svtrm2lrzdhv55s4n6zultk2zahl500e5", "149"},
	})
}

// Synthetic block exercising output shapes and amount spellings.
func TestScanOutputShapesAndAmounts(t *testing.T) {
	const (
		a = "bc1qrad6js2ypwel62292dvt6pgv3z0jl049u8epjw"
		b = "1BvBMSEYstWetqTFn5Au4m4GFg7xJaNVN2"
		c = "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq" // not watched
		d = "bc1p5d7rjq7g6rdk2yhzks9smlaqtedr4dekq08ge8ztwac72sfr9rusxg3297"
	)
	hash := fakeHash(500)
	block := synthBlock(hash, 500, 1700000000,
		// Unknown members of every shape must be skipped, wherever they are.
		`"coinbase_tx":{"version":1,"coinbase":"03"},"mweb":{"hash":"ee","inputs":[],"outputs":[{"output_id":"aa","nested":[[1,2],{"x":null}]}],"kernels":[]},"target":"00ff"`,
		synthTx(fakeTxid(1),
			vout(0, "0.00000001", a, "witness_v0_keyhash"),
			nulldataVout(1),
			vout(2, "20999999.9769", a, "witness_v0_keyhash"), // same address again
		),
		synthTx(fakeTxid(2),
			legacyVout(0, "1e-8", b),          // exponent form
			legacyVout(1, "5.00000000", a, b), // bare multisig: no single owner
			legacyVout(2, "2.09999999769E7", b),
			`{"ismweb":false,"value":"0.5","n":3,"scriptPubKey":{"type":"pubkeyhash","addresses":["`+b+`"]}}`, // quoted value
		),
		synthTx(fakeTxid(3),
			`{"ismweb":true,"output_id":"9f3c0e1a"}`, // Litecoin MWEB output
			`"garbage"`,                              // not an object
			`{"value":1,"n":2}`,                      // no scriptPubKey
			`{"value":1,"n":3,"scriptPubKey":{"asm":"OP_1","hex":"51","type":"nonstandard"}}`, // no address
			vout(4, "0.00000000", a, "witness_v0_keyhash"),                                    // zero value: no transfer
			`{"value":1,"n":5,"scriptPubKey":{"address":123,"addresses":"nope"}}`,             // wrong types
			vout(6, "1", c, "witness_v0_keyhash"),                                             // unwatched
			`{"value":1,"n":7,"scriptPubKey":{"addresses":[]}}`,
		),
		synthTx(fakeTxid(4),
			vout(0, "21000000", d, "witness_v1_taproot"),
		),
	)
	f := newFakeNode(t)
	f.addBlock(500, hash, block)
	cl := newTestClient(t, testChain(f.URL()))

	got, err := cl.ScanTransfers(context.Background(), chain.ScanRequest{
		FromBlock: 500, ToBlock: 500, Native: true,
		Tokens:    []string{"ignored"},
		Addresses: []string{a, b, strings.ToUpper(d), " " + a + " "},
	})
	if err != nil {
		t.Fatalf("ScanTransfers: %v", err)
	}
	checkTransfers(t, got, 500, hash, 1700000000, []want{
		{fakeTxid(1), 0, a, "1"},
		{fakeTxid(1), 2, a, "2099999997690000"},
		{fakeTxid(2), 0, b, "1"},
		{fakeTxid(2), 2, b, "2099999997690000"},
		{fakeTxid(2), 3, b, "50000000"},
		{fakeTxid(4), 0, d, "2100000000000000"},
	})
}

func TestScanMultiBlockRange(t *testing.T) {
	const addr = "bc1qrad6js2ypwel62292dvt6pgv3z0jl049u8epjw"
	f := newFakeNode(t)
	for h := uint64(100); h <= 103; h++ {
		var txs []string
		switch h {
		case 100:
			txs = []string{synthTx(fakeTxid(1), vout(0, "0.1", addr, "witness_v0_keyhash")), synthTx(fakeTxid(2), vout(0, "0.2", addr, "witness_v0_keyhash"))}
		case 101:
			txs = []string{synthTx(fakeTxid(3), nulldataVout(0))}
		case 102:
			txs = []string{synthTx(fakeTxid(4), nulldataVout(0), vout(1, "0.3", addr, "witness_v0_keyhash"))}
		case 103:
			txs = []string{synthTx(fakeTxid(5), vout(0, "9", addr, "witness_v0_keyhash"))} // outside range
		}
		f.addBlock(h, fakeHash(h), synthBlock(fakeHash(h), h, 1700000000+int64(h), "", txs...))
	}
	cl := newTestClient(t, testChain(f.URL()))

	got, err := cl.ScanTransfers(context.Background(), chain.ScanRequest{FromBlock: 100, ToBlock: 102, Native: true, Addresses: []string{addr}})
	if err != nil {
		t.Fatalf("ScanTransfers: %v", err)
	}
	type key struct {
		height uint64
		hash   string
		tx     string
		index  int
		amount string
		time   int64
	}
	var keys []key
	for _, g := range got {
		keys = append(keys, key{g.BlockNumber, g.BlockHash, g.TxHash, g.Index, g.Amount.String(), g.BlockTime.Unix()})
	}
	wantKeys := []key{
		{100, fakeHash(100), fakeTxid(1), 0, "10000000", 1700000100},
		{100, fakeHash(100), fakeTxid(2), 0, "20000000", 1700000100},
		{102, fakeHash(102), fakeTxid(4), 1, "30000000", 1700000102},
	}
	if !reflect.DeepEqual(keys, wantKeys) {
		t.Fatalf("got %+v\nwant %+v", keys, wantKeys)
	}
	if n := f.count("getblockhash"); n != 3 {
		t.Errorf("getblockhash calls = %d, want 3", n)
	}
	if n := f.count("getblock"); n != 3 {
		t.Errorf("getblock calls = %d, want 3", n)
	}
}

func TestScanNoWork(t *testing.T) {
	f := newFakeNode(t)
	cl := newTestClient(t, testChain(f.URL()))
	ctx := context.Background()

	got, err := cl.ScanTransfers(ctx, chain.ScanRequest{FromBlock: 1, ToBlock: 5, Addresses: []string{"bc1qrad6js2ypwel62292dvt6pgv3z0jl049u8epjw"}, Tokens: []string{"x"}})
	if err != nil || got != nil {
		t.Errorf("non-native scan = %v, %v; want nil, nil", got, err)
	}
	got, err = cl.ScanTransfers(ctx, chain.ScanRequest{FromBlock: 1, ToBlock: 5, Native: true, Addresses: []string{" "}})
	if err != nil || got != nil {
		t.Errorf("scan without addresses = %v, %v; want nil, nil", got, err)
	}
	if _, err := cl.ScanTransfers(ctx, chain.ScanRequest{FromBlock: 6, ToBlock: 5, Native: true, Addresses: []string{"x"}}); err == nil {
		t.Error("inverted range accepted")
	}
	if n := f.count(""); n != 0 {
		t.Errorf("made %d RPC calls, want 0", n)
	}
}

// Every way a block can be unusable must fail the whole range: no partial
// result, so the scanner retries the same range.
func TestScanBadBlocks(t *testing.T) {
	const addr = "bc1qrad6js2ypwel62292dvt6pgv3z0jl049u8epjw"
	good := func(hash string) string {
		return synthBlock(hash, 7, 1700000000, "", synthTx(fakeTxid(1), vout(0, "0.1", addr, "witness_v0_keyhash")))
	}
	cases := []struct {
		name     string
		raw      func(hash string) string // getblock result; "" = use writeBody
		body     func(hash string) string // full HTTP body
		wantMsg  string
		attempts int // expected getblock calls (3 = retried, 1 = not retried)
	}{
		{name: "truncated", body: func(h string) string { s := `{"result":` + good(h); return s[:len(s)-40] },
			wantMsg: "reading response", attempts: 3},
		{name: "malformed", body: func(string) string { return `{"result":{"hash": nope}}` }, wantMsg: "invalid character", attempts: 3},
		{name: "html error page", body: func(string) string { return `<html>502 Bad Gateway</html>` }, wantMsg: "invalid character", attempts: 3},
		{name: "verbosity 1", raw: func(h string) string {
			return fmt.Sprintf(`{"hash":%q,"height":7,"time":1,"nTx":1,"tx":[%q]}`, h, fakeTxid(1))
		}, wantMsg: "verbosity 2", attempts: 1},
		{name: "verbosity 0", raw: func(string) string { return `"0100000000"` }, wantMsg: "has type string, not object", attempts: 1},
		{name: "nTx mismatch", raw: func(h string) string { return strings.Replace(good(h), `"nTx":1`, `"nTx":2`, 1) },
			wantMsg: "nTx=2", attempts: 1},
		{name: "hash mismatch", raw: func(string) string { return good(fakeHash(8)) }, wantMsg: "getblock returned block", attempts: 1},
		{name: "height mismatch", raw: func(h string) string { return strings.Replace(good(h), `"height":7`, `"height":8`, 1) },
			wantMsg: "height 8", attempts: 1},
		{name: "no time", raw: func(h string) string { return strings.Replace(good(h), `"time":1700000000,`, ``, 1) },
			wantMsg: "no time", attempts: 1},
		{name: "no tx", raw: func(h string) string { return fmt.Sprintf(`{"hash":%q,"height":7,"time":1}`, h) },
			wantMsg: "no tx array", attempts: 1},
		{name: "tx not an array", raw: func(h string) string { return fmt.Sprintf(`{"hash":%q,"height":7,"time":1,"tx":{}}`, h) },
			wantMsg: "expected", attempts: 1},
		{name: "height as string", raw: func(h string) string { return strings.Replace(good(h), `"height":7`, `"height":"7"`, 1) },
			wantMsg: `"height" has type string`, attempts: 1},
		{name: "hash as object", raw: func(h string) string {
			return strings.Replace(good(h), fmt.Sprintf(`"hash":%q`, h), `"hash":{"a":[1]}`, 1)
		},
			wantMsg: `"hash" has type object`, attempts: 1},
		{name: "time not an integer", raw: func(h string) string { return strings.Replace(good(h), `"time":1700000000`, `"time":1.5`, 1) },
			wantMsg: "not an integer", attempts: 1},
		{name: "stale block", raw: func(h string) string { return strings.Replace(good(h), `"confirmations":3`, `"confirmations":-1`, 1) },
			wantMsg: "no longer in the active chain", attempts: 1},
		{name: "watched output with sub-satoshi value", raw: func(h string) string {
			return synthBlock(h, 7, 1, "", synthTx(fakeTxid(1), vout(0, "0.000000001", addr, "witness_v0_keyhash")))
		}, wantMsg: "more than 8 decimal places", attempts: 1},
		{name: "watched output without n", raw: func(h string) string {
			return synthBlock(h, 7, 1, "", synthTx(fakeTxid(1), `{"value":1,"scriptPubKey":{"address":"`+addr+`"}}`))
		}, wantMsg: "n: missing", attempts: 1},
		{name: "watched output with bad txid", raw: func(h string) string {
			return synthBlock(h, 7, 1, "", synthTx("xyz", vout(0, "1", addr, "witness_v0_keyhash")))
		}, wantMsg: "invalid txid", attempts: 1},
		{name: "tx not decodable", raw: func(h string) string {
			return synthBlock(h, 7, 1, "", `{"txid":1,"vout":[]}`)
		}, wantMsg: "tx #0", attempts: 1},
		{name: "null result", body: func(string) string { return `{"result":null,"error":null,"id":1}` }, wantMsg: "null result", attempts: 1},
		{name: "no result", body: func(string) string { return `{"id":1}` }, wantMsg: "no result", attempts: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hash := fakeHash(7)
			f := newFakeNode(t)
			f.hashes[7] = hash
			f.intercept = func(w http.ResponseWriter, _ *http.Request, c rpcCall) bool {
				if c.Method != "getblock" {
					return false
				}
				if tc.raw != nil {
					writeResult(w, c.ID, tc.raw(hash))
				} else {
					fmt.Fprint(w, tc.body(hash))
				}
				return true
			}
			cl := newTestClient(t, testChain(f.URL()))
			got, err := cl.ScanTransfers(context.Background(), chain.ScanRequest{FromBlock: 7, ToBlock: 7, Native: true, Addresses: []string{addr}})
			if err == nil {
				t.Fatalf("ScanTransfers = %v, want error", got)
			}
			if got != nil {
				t.Errorf("partial result returned with error: %v", got)
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("error %q does not mention %q", err, tc.wantMsg)
			}
			if n := f.count("getblock"); n != tc.attempts {
				t.Errorf("getblock calls = %d, want %d", n, tc.attempts)
			}
		})
	}
}

// A failure on a later block of the range discards earlier matches.
func TestScanNoPartialResult(t *testing.T) {
	const addr = "bc1qrad6js2ypwel62292dvt6pgv3z0jl049u8epjw"
	f := newFakeNode(t)
	f.addBlock(1, fakeHash(1), synthBlock(fakeHash(1), 1, 1, "", synthTx(fakeTxid(1), vout(0, "1", addr, "witness_v0_keyhash"))))
	// Height 2 is unknown to the node: getblockhash fails with -8.
	cl := newTestClient(t, testChain(f.URL()))
	got, err := cl.ScanTransfers(context.Background(), chain.ScanRequest{FromBlock: 1, ToBlock: 2, Native: true, Addresses: []string{addr}})
	if err == nil || got != nil {
		t.Fatalf("ScanTransfers = %v, %v; want nil and error", got, err)
	}
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != -8 {
		t.Errorf("error %v does not wrap RPC error -8", err)
	}
}

func TestVerifyTransfer(t *testing.T) {
	f := newFakeNode(t)
	f.addBlock(42, fakeHash(42), "{}")
	cl := newTestClient(t, testChain(f.URL()))
	ctx := context.Background()

	ok, err := cl.VerifyTransfer(ctx, chain.Transfer{TxHash: fakeTxid(1), BlockNumber: 42, BlockHash: fakeHash(42)})
	if err != nil || !ok {
		t.Errorf("same hash: %v, %v; want true", ok, err)
	}
	ok, err = cl.VerifyTransfer(ctx, chain.Transfer{TxHash: fakeTxid(1), BlockNumber: 42, BlockHash: strings.ToUpper(fakeHash(42))})
	if err != nil || !ok {
		t.Errorf("same hash upper-case: %v, %v; want true", ok, err)
	}
	ok, err = cl.VerifyTransfer(ctx, chain.Transfer{TxHash: fakeTxid(1), BlockNumber: 42, BlockHash: fakeHash(43)})
	if err != nil || ok {
		t.Errorf("reorged: %v, %v; want false", ok, err)
	}
	// A node that does not know the height (lagging failover node, shorter
	// fork) must not make the transfer look orphaned.
	ok, err = cl.VerifyTransfer(ctx, chain.Transfer{TxHash: fakeTxid(1), BlockNumber: 43, BlockHash: fakeHash(43)})
	if err == nil || ok {
		t.Errorf("unknown height: %v, %v; want error", ok, err)
	}
	calls := f.count("")
	if _, err := cl.VerifyTransfer(ctx, chain.Transfer{BlockNumber: 42, BlockHash: "nothex"}); err == nil {
		t.Error("invalid block hash accepted")
	}
	if f.count("") != calls {
		t.Error("invalid block hash caused an RPC call")
	}
}

func TestConcurrentUse(t *testing.T) {
	const addr = "bc1qrad6js2ypwel62292dvt6pgv3z0jl049u8epjw"
	f := newFakeNode(t)
	for h := uint64(1); h <= 4; h++ {
		f.addBlock(h, fakeHash(h), synthBlock(fakeHash(h), h, int64(h), "", synthTx(fakeTxid(int(h)), vout(0, "0.5", addr, "witness_v0_keyhash"))))
	}
	cl := newTestClient(t, testChain(f.URL()))

	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := cl.ScanTransfers(context.Background(), chain.ScanRequest{FromBlock: 1, ToBlock: 4, Native: true, Addresses: []string{addr}})
			if err != nil {
				errs <- err
				return
			}
			sum := new(big.Int)
			for _, g := range got {
				sum.Add(sum, g.Amount)
			}
			if len(got) != 4 || sum.String() != "200000000" {
				errs <- fmt.Errorf("got %d transfers summing %s", len(got), sum)
			}
			if _, err := cl.Head(context.Background()); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
