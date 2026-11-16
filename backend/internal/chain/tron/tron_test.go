package tron

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"cryptopay/internal/chain"
	"cryptopay/internal/domain"
)

func amt(s string) *big.Int {
	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		panic(s)
	}
	return v
}

type tw struct {
	tx         string
	idx        int
	blk        uint64
	from, to   string // hex (20-byte or 41-prefixed)
	token, amt string // token hex ("" = TRX)
}

func expect(t *testing.T, ws ...tw) []chain.Transfer {
	t.Helper()
	var out []chain.Transfer
	for _, w := range ws {
		tr := chain.Transfer{
			TxHash: w.tx, Index: w.idx, BlockNumber: w.blk,
			From: b58(t, w.from), To: b58(t, w.to), Amount: amt(w.amt),
		}
		switch w.blk {
		case blkA:
			tr.BlockHash, tr.BlockTime = blkAHash, time.UnixMilli(blkATime).UTC()
		case blkB:
			tr.BlockHash, tr.BlockTime = blkBHash, time.UnixMilli(blkBTime).UTC()
		default:
			t.Fatalf("no fixture block %d", w.blk)
		}
		if w.token != "" {
			tr.Token = b58(t, w.token)
		}
		out = append(out, tr)
	}
	return out
}

func assertTransfers(t *testing.T, got, want []chain.Transfer) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d transfers, want %d:\n got: %+v\nwant: %+v", len(got), len(want), got, want)
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.Amount == nil || g.Amount.Cmp(w.Amount) != 0 {
			t.Errorf("#%d amount = %v, want %v", i, g.Amount, w.Amount)
		}
		g.Amount, w.Amount = nil, nil
		if !reflect.DeepEqual(g, w) {
			t.Errorf("#%d\n got %+v\nwant %+v", i, g, w)
		}
	}
}

func addrs(t *testing.T, hexes ...string) []string {
	var out []string
	for _, h := range hexes {
		out = append(out, b58(t, h))
	}
	return out
}

func TestTransferTopic(t *testing.T) {
	if transferTopic != "ddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef" {
		t.Fatal(transferTopic)
	}
	if b58(t, usdtHex) != usdtB58 {
		t.Fatal("usdt address conversion")
	}
}

func TestNew(t *testing.T) {
	if _, err := New(domain.Chain{ID: "eth", Family: domain.FamilyEVM, RPCURLs: []string{"https://x"}}); err == nil {
		t.Error("evm chain accepted")
	}
	for _, urls := range [][]string{nil, {}, {"", "  "}} {
		if _, err := New(tronChain(urls...)); !errors.Is(err, chain.ErrNoRPC) {
			t.Errorf("urls %q: err = %v, want ErrNoRPC", urls, err)
		}
	}
	for _, bad := range []string{"ftp://api.trongrid.io", "api.trongrid.io/?apikey=SECRETKEY", "https://", "http://[::1/?apikey=SECRETKEY"} {
		_, err := New(tronChain("https://api.trongrid.io", bad))
		if err == nil {
			t.Errorf("%q accepted", bad)
			continue
		}
		if strings.Contains(err.Error(), "SECRETKEY") || !strings.Contains(err.Error(), "#2") {
			t.Errorf("%q: unexpected error %q", bad, err)
		}
	}
	c, err := New(tronChain("https://api.trongrid.io", "", "https://nile.trongrid.io/?apikey=k"))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.endpoints) != 2 || c.endpoints[1].apiKey != "k" || c.endpoints[1].host != "nile.trongrid.io" {
		t.Fatalf("endpoints: %+v", c.endpoints)
	}
	f, err := NewFactory()(tronChain("https://api.trongrid.io"))
	if err != nil || f == nil {
		t.Fatalf("factory: %v", err)
	}
}

func TestHead(t *testing.T) {
	node := newFakeNode(t)
	c := newTestClient(t, []string{node.URL()})
	h, err := c.Head(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if h != nileHead {
		t.Fatalf("head = %d, want %d", h, nileHead)
	}
	calls := node.allCalls()
	if len(calls) != 1 || calls[0].Path != "/wallet/getblock" || calls[0].Body != `{"detail":false}` {
		t.Fatalf("calls = %+v", calls)
	}
}

func TestHeadFallsBackToGetNowBlock(t *testing.T) {
	node := newFakeNode(t)
	node.setHook(func(w http.ResponseWriter, r *http.Request, path string, n int) bool {
		if path == "/wallet/getblock" { // older node: Jetty 405 for unknown servlet
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusMethodNotAllowed)
			_, _ = w.Write([]byte("<html><h2>HTTP ERROR 405</h2></html>"))
			return true
		}
		return false
	})
	c := newTestClient(t, []string{node.URL()})
	now := time.Unix(1_800_000_000, 0)
	c.now = func() time.Time { return now }
	for range 2 {
		h, err := c.Head(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if h != blkB {
			t.Fatalf("head = %d, want %d", h, blkB)
		}
	}
	if n := node.callsTo("/wallet/getblock"); n != 1 {
		t.Errorf("getblock called %d times, want 1 (fallback is remembered)", n)
	}
	if n := node.callsTo("/wallet/getnowblock"); n != 2 {
		t.Errorf("getnowblock called %d times, want 2", n)
	}
	// Re-probed after a while; once the node serves it again it is used.
	node.setHook(nil)
	now = now.Add(getBlockReprobe + time.Minute)
	if h, err := c.Head(context.Background()); err != nil || h != nileHead {
		t.Fatalf("head = %d, %v", h, err)
	}
	if h, err := c.Head(context.Background()); err != nil || h != nileHead {
		t.Fatalf("head = %d, %v", h, err)
	}
	if n := node.callsTo("/wallet/getnowblock"); n != 2 {
		t.Errorf("getnowblock called %d times after re-probe, want 2", n)
	}
}

func TestHeadFallbackNotStickyWhenGetNowBlockFails(t *testing.T) {
	node := newFakeNode(t)
	node.setHook(func(w http.ResponseWriter, r *http.Request, path string, n int) bool {
		if path == "/wallet/getblock" && n == 1 {
			w.WriteHeader(http.StatusNotFound) // e.g. a gateway glitch
			return true
		}
		if path == "/wallet/getnowblock" {
			w.WriteHeader(http.StatusNotFound)
			return true
		}
		return false
	})
	c := newTestClient(t, []string{node.URL()})
	if _, err := c.Head(context.Background()); err == nil {
		t.Fatal("want error")
	}
	if h, err := c.Head(context.Background()); err != nil || h != nileHead {
		t.Fatalf("head = %d, %v; want getblock to be used again", h, err)
	}
}

func TestHeadMalformed(t *testing.T) {
	cases := map[string]string{
		"empty object":     `{}`,
		"not json":         `<html>bad gateway</html>`,
		"array":            `[]`,
		"id/number differ": `{"blockID":"0000000004562db8ed5f931798c5342cfdf495c2c5040cf6cf99585de5cd799a","block_header":{"raw_data":{"number":72756663,"timestamp":1794810906000}}}`,
		"bad id":           `{"blockID":"xyz","block_header":{"raw_data":{"number":5,"timestamp":1}}}`,
		"no timestamp":     `{"blockID":"0000000004562db7ed5f931798c5342cfdf495c2c5040cf6cf99585de5cd799a","block_header":{"raw_data":{"number":72756663}}}`,
		"zero height":      `{"blockID":"0000000000000000ed5f931798c5342cfdf495c2c5040cf6cf99585de5cd799a","block_header":{"raw_data":{}}}`,
		"node error":       `{"Error":"class java.lang.NullPointerException : null"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			node := newFakeNode(t)
			node.head = []byte(body)
			c := newTestClient(t, []string{node.URL()}, WithRetries(1))
			if h, err := c.Head(context.Background()); err == nil {
				t.Fatalf("head = %d, want error", h)
			}
			if n := node.callsTo("/wallet/getblock"); n != 2 {
				t.Errorf("getblock called %d times, want 2 (retried)", n)
			}
		})
	}
}

func TestScanNativeTRX(t *testing.T) {
	node := newFakeNode(t)
	c := newTestClient(t, []string{node.URL()})
	got, err := c.ScanTransfers(context.Background(), chain.ScanRequest{
		FromBlock: blkA, ToBlock: blkA, Native: true,
		Addresses: addrs(t,
			"41d1762b95e749b86aee198b0fca38698bdeabfb2a", // TRX, 1 sun
			"4159b6dd1b64454154686ef2f2dc2397b4e27d2478", // TRX, 2 TRX
			"41bb213a6a2db05085eb1e78d7744c91450dec4f7e", // TRC-10 recipient: not TRX
			"415751f5713f26469b0ad19e3f6acc780e682d2257", // resource delegation receiver: not a payment
			"25c53878d152bb8b7432c47f2483e21ee3133c72",   // USDT recipient: tokens not requested
		),
	})
	if err != nil {
		t.Fatal(err)
	}
	assertTransfers(t, got, expect(t,
		tw{"b31d2b818ee50d7f030cf27f35d4effd79a5a438e19407d1af109c6a5b53de8f", -1, blkA, "41387951649e5d5a11639f94f69141a02345f0eb97", "41d1762b95e749b86aee198b0fca38698bdeabfb2a", "", "1"},
		tw{trxTxA, -1, blkA, "412a22eeb5f60fe3895e905ac4cb07316fde08add2", "4159b6dd1b64454154686ef2f2dc2397b4e27d2478", "", "2000000"},
	))
	if n := node.callsTo("/wallet/gettransactioninfobyblocknum"); n != 0 {
		t.Errorf("tx infos fetched %d times for a native-only scan", n)
	}
}

func TestScanTRC20(t *testing.T) {
	node := newFakeNode(t)
	c := newTestClient(t, []string{node.URL()})
	got, err := c.ScanTransfers(context.Background(), chain.ScanRequest{
		FromBlock: blkA, ToBlock: blkA, Tokens: []string{usdtB58},
		Addresses: addrs(t,
			"25c53878d152bb8b7432c47f2483e21ee3133c72",   // plain USDT transfer
			"a31d689a84244bc01be56e07aeafb7686f56bb89",   // swap: USDT in at log 0, also topic of a non-Transfer event (log 4)
			"d8de75898a30b097da826464e932934a512c7be3",   // swap: USDT at log 3
			"e351a12ea04e2f68be3c308a1a2329beb91bcce5",   // payout tx log 1; also in a 4-topic event (log 2)
			"9cbf6ce3019768149f184e053874ac32a8018ba5",   // recipient of the REVERTed USDT transfer
			"e828f674ff8aedf2ae83668d34e15af0acb7e3af",   // recipient of the OUT_OF_ENERGY USDT transfer
			"41d1762b95e749b86aee198b0fca38698bdeabfb2a", // TRX recipient: native not requested
		),
	})
	if err != nil {
		t.Fatal(err)
	}
	assertTransfers(t, got, expect(t,
		tw{usdtTxA, 0, blkA, "ae9e6b73c1590be195cdb6a9c532366d24c59b57", "25c53878d152bb8b7432c47f2483e21ee3133c72", usdtHex, "90000000"},
		tw{swapTx, 0, blkA, "340e09556cb1f97e5bb7418425e1fdac74ac52c8", "a31d689a84244bc01be56e07aeafb7686f56bb89", usdtHex, "904374181"},
		tw{swapTx, 3, blkA, "a31d689a84244bc01be56e07aeafb7686f56bb89", "d8de75898a30b097da826464e932934a512c7be3", usdtHex, "896687001"},
		tw{payoutTxA, 1, blkA, "6fc872ec3df8f59909faa3574d5b3044ad092a22", "e351a12ea04e2f68be3c308a1a2329beb91bcce5", usdtHex, "15000000000"},
	))
}

func TestScanMixedBlockMultipleTokens(t *testing.T) {
	node := newFakeNode(t)
	c := newTestClient(t, []string{node.URL()})
	watch := addrs(t,
		"595c4a379ab80c202f0372bbf9bbf3fad6ca8768",   // USDT
		"4139c9df5dfc0ff9a73873ec781e0ac8675cb51cc5", // TRX 7 sun
		"63662d8a3e812969b4e716c598c35e0c2c1d4368",   // USDT out of a swap
		"b50b027637ab9a7f2ba5b70a24f778f8438bb279",   // WTRX into the pool
	)
	got, err := c.ScanTransfers(context.Background(), chain.ScanRequest{
		FromBlock: blkB, ToBlock: blkB, Native: true, Addresses: watch,
		Tokens: []string{usdtB58, b58(t, wtrxHex)},
	})
	if err != nil {
		t.Fatal(err)
	}
	all := expect(t,
		tw{"759fb41badccd6f6f76abf5eab3624e42817d90d5c5b36af5062a581a7514640", 0, blkB, "59f55564cb68b3e9e232b88f625cbc1de80a0d51", "595c4a379ab80c202f0372bbf9bbf3fad6ca8768", usdtHex, "150000000"},
		tw{"86c7c4b044df06df79bbce56c3effe1b716416a7f6bd6ed191e989d88c7e56e7", -1, blkB, "415d2688ee94fd329e52e2a4f862493a40a847d82c", "4139c9df5dfc0ff9a73873ec781e0ac8675cb51cc5", "", "7"},
		tw{"d837d685a98129672d788cff84d3f325242d182d566e79e3ddb663aa16e7c0b4", 0, blkB, "b50b027637ab9a7f2ba5b70a24f778f8438bb279", "63662d8a3e812969b4e716c598c35e0c2c1d4368", usdtHex, "1820999076"},
		tw{"d837d685a98129672d788cff84d3f325242d182d566e79e3ddb663aa16e7c0b4", 1, blkB, "63662d8a3e812969b4e716c598c35e0c2c1d4368", "b50b027637ab9a7f2ba5b70a24f778f8438bb279", wtrxHex, "6015699999"},
	)
	assertTransfers(t, got, all)

	// Only USDT watched: the WTRX transfer is not returned.
	got, err = c.ScanTransfers(context.Background(), chain.ScanRequest{
		FromBlock: blkB, ToBlock: blkB, Native: true, Addresses: watch, Tokens: []string{usdtB58},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertTransfers(t, got, all[:3])
}

func TestScanRangeDeterministic(t *testing.T) {
	node := newFakeNode(t)
	req := chain.ScanRequest{
		FromBlock: blkA, ToBlock: blkB, Native: true, Tokens: []string{usdtB58, b58(t, wtrxHex)},
		Addresses: addrs(t,
			"4139c9df5dfc0ff9a73873ec781e0ac8675cb51cc5", "4159b6dd1b64454154686ef2f2dc2397b4e27d2478",
			"a31d689a84244bc01be56e07aeafb7686f56bb89", "b50b027637ab9a7f2ba5b70a24f778f8438bb279",
		),
	}
	want := expect(t,
		tw{trxTxA, -1, blkA, "412a22eeb5f60fe3895e905ac4cb07316fde08add2", "4159b6dd1b64454154686ef2f2dc2397b4e27d2478", "", "2000000"},
		tw{swapTx, 0, blkA, "340e09556cb1f97e5bb7418425e1fdac74ac52c8", "a31d689a84244bc01be56e07aeafb7686f56bb89", usdtHex, "904374181"},
		tw{"86c7c4b044df06df79bbce56c3effe1b716416a7f6bd6ed191e989d88c7e56e7", -1, blkB, "415d2688ee94fd329e52e2a4f862493a40a847d82c", "4139c9df5dfc0ff9a73873ec781e0ac8675cb51cc5", "", "7"},
		tw{"d837d685a98129672d788cff84d3f325242d182d566e79e3ddb663aa16e7c0b4", 1, blkB, "63662d8a3e812969b4e716c598c35e0c2c1d4368", "b50b027637ab9a7f2ba5b70a24f778f8438bb279", wtrxHex, "6015699999"},
	)
	for _, conc := range []int{1, 3, 16} {
		c := newTestClient(t, []string{node.URL()}, WithConcurrency(conc))
		for range 3 {
			got, err := c.ScanTransfers(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			assertTransfers(t, got, want)
		}
	}
	// Empty blocks in between have no smart-contract calls: no tx info requests.
	for _, call := range node.allCalls() {
		if call.Path == "/wallet/gettransactioninfobyblocknum" && !strings.Contains(call.Body, "88045575") && !strings.Contains(call.Body, "88045581") {
			t.Fatalf("unexpected tx info request %s", call.Body)
		}
	}
}

func TestScanEmptyBlock(t *testing.T) {
	node := newFakeNode(t)
	c := newTestClient(t, []string{node.URL()})
	got, err := c.ScanTransfers(context.Background(), chain.ScanRequest{
		FromBlock: blkEmpty, ToBlock: blkEmpty, Native: true, Tokens: []string{usdtB58},
		Addresses: addrs(t, "4139c9df5dfc0ff9a73873ec781e0ac8675cb51cc5"),
	})
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v; want no transfers", got, err)
	}
	if calls := node.allCalls(); len(calls) != 1 || calls[0].Body != `{"num":1000,"visible":false}` {
		t.Fatalf("calls = %+v", calls)
	}
}

func TestScanMissingBlockIsAnError(t *testing.T) {
	node := newFakeNode(t)
	c := newTestClient(t, []string{node.URL()}, WithRetries(2))
	// blkB+1 is unknown to the node ("{}"): the whole range fails, no partial result.
	got, err := c.ScanTransfers(context.Background(), chain.ScanRequest{
		FromBlock: blkB, ToBlock: blkB + 1, Native: true, Tokens: []string{usdtB58},
		Addresses: addrs(t, "4139c9df5dfc0ff9a73873ec781e0ac8675cb51cc5"),
	})
	if err == nil || got != nil {
		t.Fatalf("got %v, %v; want error and no transfers", got, err)
	}
	if !errors.Is(err, errNotFound) || !strings.Contains(err.Error(), "88045582") {
		t.Errorf("err = %v", err)
	}
	if n := node.callsTo("/wallet/getblockbynum"); n < 4 { // 1 good + 3 attempts for the missing one
		t.Errorf("getblockbynum called %d times, want retries", n)
	}
}

func TestScanLaggingNodeRecovers(t *testing.T) {
	// Load-balanced providers: the info request may hit a node that has not
	// processed the block yet and answers "[]", the block request a node
	// that answers "{}". Both are retried.
	node := newFakeNode(t)
	node.setHook(func(w http.ResponseWriter, r *http.Request, path string, n int) bool {
		if n == 1 && path == "/wallet/getblockbynum" {
			writeJSON(w, `{}`)
			return true
		}
		if n == 1 && path == "/wallet/gettransactioninfobyblocknum" {
			writeJSON(w, `[]`)
			return true
		}
		return false
	})
	c := newTestClient(t, []string{node.URL()})
	got, err := c.ScanTransfers(context.Background(), chain.ScanRequest{
		FromBlock: blkB, ToBlock: blkB, Tokens: []string{usdtB58},
		Addresses: addrs(t, "595c4a379ab80c202f0372bbf9bbf3fad6ca8768"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Amount.String() != "150000000" {
		t.Fatalf("got %+v", got)
	}
}

func TestScanRejectsInconsistentResponses(t *testing.T) {
	watch := addrs(t, "595c4a379ab80c202f0372bbf9bbf3fad6ca8768", "4139c9df5dfc0ff9a73873ec781e0ac8675cb51cc5")
	type infos = []map[string]any
	cases := map[string]func(t *testing.T, node *fakeNode){
		"infos never indexed": func(t *testing.T, node *fakeNode) { node.setInfos(blkB, []byte(`[]`)) },
		"info missing": func(t *testing.T, node *fakeNode) {
			node.setInfos(blkB, jsonEdit(t, node.infos[blkB], func(v *infos) { *v = (*v)[:len(*v)-1] }))
		},
		"infos of another block": func(t *testing.T, node *fakeNode) {
			// same count, different txs (e.g. block and infos served from different forks)
			node.setInfos(blkB, jsonEdit(t, node.infos[blkA], func(v *infos) {
				*v = (*v)[:5]
				for _, i := range *v {
					i["blockNumber"] = blkB
				}
			}))
		},
		"info block number": func(t *testing.T, node *fakeNode) {
			node.setInfos(blkB, jsonEdit(t, node.infos[blkB], func(v *infos) { (*v)[0]["blockNumber"] = blkB + 1 }))
		},
		"infos not json": func(t *testing.T, node *fakeNode) { node.setInfos(blkB, []byte(`[{"id":`)) },
		"truncated block": func(t *testing.T, node *fakeNode) {
			// transactions missing although txTrieRoot says there are some
			node.setBlock(blkB, jsonEdit(t, node.blocks[blkB], func(v *obj) { delete(*v, "transactions") }))
		},
		"wrong height": func(t *testing.T, node *fakeNode) { node.setBlock(blkB, node.blocks[blkA]) },
		"block id height": func(t *testing.T, node *fakeNode) {
			node.setBlock(blkB, jsonEdit(t, node.blocks[blkB], func(v *obj) {
				(*v)["blockID"] = "00000000053f780e" + blkBHash[16:]
			}))
		},
		"empty block without txTrieRoot": func(t *testing.T, node *fakeNode) {
			// a stripped response must not pass for an empty block
			node.setBlock(blkB, jsonEdit(t, node.blocks[blkB], func(v *obj) {
				delete(*v, "transactions")
				delete((*v)["block_header"].(obj)["raw_data"].(obj), "txTrieRoot")
			}))
		},
		"no header": func(t *testing.T, node *fakeNode) {
			node.setBlock(blkB, jsonEdit(t, node.blocks[blkB], func(v *obj) { delete(*v, "block_header") }))
		},
		"duplicate tx": func(t *testing.T, node *fakeNode) {
			node.setBlock(blkB, jsonEdit(t, node.blocks[blkB], func(v *obj) {
				txs := (*v)["transactions"].([]any)
				(*v)["transactions"] = append(txs, txs[0])
			}))
		},
		"bad tx id": func(t *testing.T, node *fakeNode) {
			node.setBlock(blkB, jsonEdit(t, node.blocks[blkB], func(v *obj) {
				(*v)["transactions"].([]any)[1].(obj)["txID"] = "zz"
			}))
		},
		"bad TRX amount": func(t *testing.T, node *fakeNode) {
			node.setBlock(blkB, jsonEdit(t, node.blocks[blkB], func(v *obj) {
				tx := (*v)["transactions"].([]any)[1].(obj)
				tx["raw_data"].(obj)["contract"].([]any)[0].(obj)["parameter"].(obj)["value"].(obj)["amount"] = 1.5
			}))
		},
		"bad topic hex": func(t *testing.T, node *fakeNode) {
			node.setInfos(blkB, jsonEdit(t, node.infos[blkB], func(v *infos) {
				lg := (*v)[0]["log"].([]any)[0].(obj)
				lg["topics"].([]any)[2] = "0000000000000000000000zz595c4a379ab80c202f0372bbf9bbf3fad6ca8768"
			}))
		},
		"node error": func(t *testing.T, node *fakeNode) {
			node.setBlock(blkB, []byte(`{"Error":"class java.lang.NullPointerException : null"}`))
		},
		"html body": func(t *testing.T, node *fakeNode) { node.setBlock(blkB, []byte(`<html>502 Bad Gateway</html>`)) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			node := newFakeNode(t)
			mutate(t, node)
			c := newTestClient(t, []string{node.URL()}, WithRetries(1))
			got, err := c.ScanTransfers(context.Background(), chain.ScanRequest{
				FromBlock: blkB, ToBlock: blkB, Native: true, Tokens: []string{usdtB58}, Addresses: watch,
			})
			if err == nil || got != nil {
				t.Fatalf("got %+v, %v; want error", got, err)
			}
			t.Log(err)
		})
	}
}

func TestScanExcludesFailedAndZeroTransfers(t *testing.T) {
	node := newFakeNode(t)
	watch := "9cbf6ce3019768149f184e053874ac32a8018ba5" // intended recipient of the reverted USDT transfer
	topic := "000000000000000000000000" + watch
	data := "000000000000000000000000000000000000000000000000000000012a05f200"
	fakeLog := []any{obj{"address": usdtHex, "topics": []any{transferTopic, "000000000000000000000000866762e6c04bfb50379d9b987d27ce0b409e9327", topic}, "data": data}}
	// Defensive: failed txs never carry logs on the real API, but if a node
	// returned some, the receipt still excludes them.
	node.setInfos(blkA, jsonEdit(t, node.infos[blkA], func(v *[]map[string]any) {
		for _, i := range *v {
			switch i["id"] {
			case revertTx: // result FAILED, receipt REVERT
				i["log"] = fakeLog
			case oomTx: // inconsistent: receipt SUCCESS but result FAILED
				i["log"] = fakeLog
				i["receipt"].(obj)["result"] = "SUCCESS"
			case usdtTxA: // zero-value transfer (address poisoning)
				lg := i["log"].([]any)[0].(obj)
				lg["topics"].([]any)[2] = topic
				lg["data"] = strings.Repeat("0", 64)
			case payoutTxA: // non-standard data length
				lg := i["log"].([]any)[0].(obj)
				lg["topics"].([]any)[2] = topic
				lg["data"] = data + data
			}
		}
	}))
	// A TRX transfer with a failure result and one with amount 0.
	node.setBlock(blkA, jsonEdit(t, node.blocks[blkA], func(v *obj) {
		for _, x := range (*v)["transactions"].([]any) {
			tx := x.(obj)
			c := tx["raw_data"].(obj)["contract"].([]any)[0].(obj)
			if c["type"] != "TransferContract" {
				continue
			}
			val := c["parameter"].(obj)["value"].(obj)
			val["to_address"] = "41" + watch
			if tx["txID"] == trxTxA {
				tx["ret"] = []any{obj{"contractRet": "REVERT"}}
			} else {
				delete(val, "amount")
			}
		}
	}))
	c := newTestClient(t, []string{node.URL()})
	got, err := c.ScanTransfers(context.Background(), chain.ScanRequest{
		FromBlock: blkA, ToBlock: blkA, Native: true, Tokens: []string{usdtB58}, Addresses: addrs(t, watch),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("got %+v, want none", got)
	}
}

func TestScanRequestValidation(t *testing.T) {
	node := newFakeNode(t)
	c := newTestClient(t, []string{node.URL()}, WithMaxBlocksPerScan(10))
	ctx := context.Background()
	watch := addrs(t, "4139c9df5dfc0ff9a73873ec781e0ac8675cb51cc5")
	if _, err := c.ScanTransfers(ctx, chain.ScanRequest{FromBlock: 10, ToBlock: 9, Native: true, Addresses: watch}); err == nil {
		t.Error("inverted range accepted")
	}
	_, err := c.ScanTransfers(ctx, chain.ScanRequest{FromBlock: 100, ToBlock: 110, Native: true, Addresses: watch})
	if !errors.Is(err, chain.ErrRangeTooLarge) {
		t.Errorf("err = %v, want ErrRangeTooLarge", err)
	}
	if _, err := c.ScanTransfers(ctx, chain.ScanRequest{FromBlock: 0, ToBlock: ^uint64(0), Native: true, Addresses: watch}); !errors.Is(err, chain.ErrRangeTooLarge) {
		t.Errorf("full uint64 range: err = %v", err)
	}
	if _, err := c.ScanTransfers(ctx, chain.ScanRequest{FromBlock: 1, ToBlock: 1, Native: true, Addresses: []string{"0xnot-tron"}}); err == nil {
		t.Error("bad address accepted")
	}
	if _, err := c.ScanTransfers(ctx, chain.ScanRequest{FromBlock: 1, ToBlock: 1, Tokens: []string{"bogus"}, Addresses: watch}); err == nil {
		t.Error("bad token accepted")
	}
	// Nothing to look for: no RPC at all.
	if got, err := c.ScanTransfers(ctx, chain.ScanRequest{FromBlock: 1, ToBlock: 1, Addresses: watch}); err != nil || got != nil {
		t.Errorf("got %v, %v", got, err)
	}
	if got, err := c.ScanTransfers(ctx, chain.ScanRequest{FromBlock: 1, ToBlock: 1, Native: true}); err != nil || got != nil {
		t.Errorf("got %v, %v", got, err)
	}
	if n := len(node.allCalls()); n != 0 {
		t.Errorf("%d rpc calls for invalid/no-op requests", n)
	}
	// Addresses are accepted in hex form too and returned canonical.
	got, err := c.ScanTransfers(ctx, chain.ScanRequest{FromBlock: blkB, ToBlock: blkB, Native: true,
		Addresses: []string{"4139c9df5dfc0ff9a73873ec781e0ac8675cb51cc5"}})
	if err != nil || len(got) != 1 || got[0].To != b58(t, "4139c9df5dfc0ff9a73873ec781e0ac8675cb51cc5") {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestVerifyTransfer(t *testing.T) {
	node := newFakeNode(t)
	c := newTestClient(t, []string{node.URL()}, WithRetries(1))
	ctx := context.Background()
	scan := func(blk uint64, tokens []string, native bool, to ...string) []chain.Transfer {
		t.Helper()
		got, err := c.ScanTransfers(ctx, chain.ScanRequest{FromBlock: blk, ToBlock: blk, Native: native, Tokens: tokens, Addresses: addrs(t, to...)})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	trx := scan(blkB, nil, true, "4139c9df5dfc0ff9a73873ec781e0ac8675cb51cc5")[0]
	swap := scan(blkB, []string{usdtB58, b58(t, wtrxHex)}, false, "b50b027637ab9a7f2ba5b70a24f778f8438bb279")[0]
	usdt := scan(blkA, []string{usdtB58}, false, "e351a12ea04e2f68be3c308a1a2329beb91bcce5")[0]

	check := func(name string, tr chain.Transfer, want bool) {
		t.Helper()
		ok, err := c.VerifyTransfer(ctx, tr)
		if err != nil {
			t.Errorf("%s: %v", name, err)
		} else if ok != want {
			t.Errorf("%s: verify = %v, want %v", name, ok, want)
		}
	}
	check("trx", trx, true)
	check("wtrx log 1", swap, true)
	check("usdt log 1", usdt, true)

	mod := func(tr chain.Transfer, fn func(*chain.Transfer)) chain.Transfer {
		tr.Amount = new(big.Int).Set(tr.Amount)
		fn(&tr)
		return tr
	}
	check("reorged block hash", mod(trx, func(t *chain.Transfer) { t.BlockHash = blkAHash }), false)
	check("reorged token block hash", mod(usdt, func(t *chain.Transfer) { t.BlockHash = blkBHash }), false)
	check("trx amount differs", mod(trx, func(t *chain.Transfer) { t.Amount.SetInt64(8) }), false)
	check("token amount differs", mod(usdt, func(t *chain.Transfer) { t.Amount.SetInt64(1) }), false)
	check("token recipient differs", mod(usdt, func(t *chain.Transfer) { t.To = trx.To }), false)
	check("wrong log index", mod(swap, func(t *chain.Transfer) { t.Index = 0 }), false)
	check("log index out of range", mod(swap, func(t *chain.Transfer) { t.Index = 99 }), false)
	check("native with log index", mod(trx, func(t *chain.Transfer) { t.Index = 0 }), false)
	check("token became native", mod(usdt, func(t *chain.Transfer) { t.Token = "" }), false)
	check("tx not in block", mod(trx, func(t *chain.Transfer) { t.TxHash = usdtTxA }), false)
	check("upper-case hashes", mod(trx, func(t *chain.Transfer) {
		t.TxHash, t.BlockHash = strings.ToUpper(t.TxHash), strings.ToUpper(t.BlockHash)
	}), true)
	// The reverted USDT call is in the block but failed.
	revertTo := b58(t, "9cbf6ce3019768149f184e053874ac32a8018ba5")
	check("reverted tx", mod(usdt, func(t *chain.Transfer) { t.TxHash, t.Index, t.To = revertTx, 0, revertTo }), false)

	// The node cannot answer: error, never false.
	if ok, err := c.VerifyTransfer(ctx, mod(trx, func(t *chain.Transfer) { t.BlockNumber = blkB + 100 })); err == nil {
		t.Errorf("unknown block: ok=%v, want error", ok)
	}
	node.setHook(func(w http.ResponseWriter, r *http.Request, path string, n int) bool {
		if path == "/wallet/gettransactioninfobyid" {
			writeJSON(w, `{}`)
			return true
		}
		return false
	})
	if ok, err := c.VerifyTransfer(ctx, usdt); err == nil {
		t.Errorf("unknown tx info: ok=%v, want error", ok)
	}
	for _, bad := range []chain.Transfer{
		mod(trx, func(t *chain.Transfer) { t.TxHash = "abc" }),
		mod(trx, func(t *chain.Transfer) { t.BlockHash = "" }),
		mod(trx, func(t *chain.Transfer) { t.To = "nope" }),
		mod(usdt, func(t *chain.Transfer) { t.Token = "nope" }),
	} {
		if _, err := c.VerifyTransfer(ctx, bad); err == nil {
			t.Errorf("invalid transfer %+v accepted", bad)
		}
	}
}

func TestVerifyTransferTxInfoBlockMismatchIsError(t *testing.T) {
	node := newFakeNode(t)
	c := newTestClient(t, []string{node.URL()}, WithRetries(0))
	got, err := c.ScanTransfers(context.Background(), chain.ScanRequest{FromBlock: blkB, ToBlock: blkB, Tokens: []string{usdtB58},
		Addresses: addrs(t, "595c4a379ab80c202f0372bbf9bbf3fad6ca8768")})
	if err != nil || len(got) != 1 {
		t.Fatal(got, err)
	}
	node.setHook(func(w http.ResponseWriter, r *http.Request, path string, n int) bool {
		if path == "/wallet/gettransactioninfobyid" {
			var infos []map[string]any
			_ = json.Unmarshal(node.infos[blkB], &infos)
			infos[0]["blockNumber"] = blkB + 1
			b, _ := json.Marshal(infos[0])
			writeJSON(w, string(b))
			return true
		}
		return false
	})
	if ok, err := c.VerifyTransfer(context.Background(), got[0]); err == nil {
		t.Fatalf("ok=%v, want error", ok)
	}
}
