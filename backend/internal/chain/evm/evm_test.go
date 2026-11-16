package evm

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cryptopay/internal/address"
	"cryptopay/internal/chain"
	"cryptopay/internal/domain"
)

var (
	tokUSDT   = addrN(100)
	tokUSDC   = addrN(101)
	tokOther  = addrN(102)
	watchA    = addrN(1)
	watchB    = addrN(2)
	strangerC = addrN(3)
	senderS   = addrN(9)
	senderS2  = addrN(10)
)

func TestTransferTopic(t *testing.T) {
	want := "0x" + hex.EncodeToString(address.Keccak256([]byte("Transfer(address,address,uint256)")))
	require.Equal(t, want, TransferTopic)
}

func TestNewValidation(t *testing.T) {
	_, u := newRPCServer(t, newFakeNode(1))

	t.Run("wrong family", func(t *testing.T) {
		c := testChain(u)
		c.Family = domain.FamilyTron
		_, err := New(c)
		require.ErrorContains(t, err, "family")
	})
	t.Run("no rpc", func(t *testing.T) {
		_, err := New(testChain())
		require.ErrorIs(t, err, chain.ErrNoRPC)
		_, err = New(testChain("", "  "))
		require.ErrorIs(t, err, chain.ErrNoRPC)
	})
	t.Run("bad url does not leak", func(t *testing.T) {
		for _, raw := range []string{"ftp://host/" + testSecret, "://bad/" + testSecret, "/v2/" + testSecret, "https:///" + testSecret} {
			_, err := New(testChain(raw))
			requireNoSecret(t, err)
		}
	})
	t.Run("bad network id", func(t *testing.T) {
		c := testChain(u)
		c.NetworkID = "mainnet"
		_, err := New(c)
		require.ErrorContains(t, err, "network id")
		c.NetworkID = "0"
		_, err = New(c)
		require.Error(t, err)
		// Disabled check ignores the network id.
		_, err = New(c, WithChainIDCheck(false))
		require.NoError(t, err)
	})
	t.Run("bad options", func(t *testing.T) {
		for name, o := range map[string]Option{
			"http client": WithHTTPClient(nil),
			"timeout":     WithRequestTimeout(0),
			"retries":     WithRetries(-1),
			"backoff":     WithBackoff(2*time.Second, time.Second),
			"batch":       WithBatchSize(0),
			"concurrency": WithConcurrency(0),
			"chunk":       WithAddressChunkSize(0),
			"max resp":    WithMaxResponseSize(0),
			"cooldown":    WithEndpointCooldown(-1),
		} {
			_, err := New(testChain(u), o)
			require.Error(t, err, name)
		}
	})
	t.Run("factory", func(t *testing.T) {
		cl, err := Factory(fastOpts()...)(testChain(u))
		require.NoError(t, err)
		require.NotNil(t, cl)
		cl, err = Factory()(testChain())
		require.ErrorIs(t, err, chain.ErrNoRPC)
		require.True(t, cl == nil, "a nil interface, not a typed nil")
	})
}

func TestHead(t *testing.T) {
	f := newFakeNode(0x1234)
	c := newTestClient(t, f)
	h, err := c.Head(context.Background())
	require.NoError(t, err)
	require.Equal(t, uint64(0x1234), h)
	require.Equal(t, 1, f.callCount("eth_chainId"))

	// The chain id is verified once per endpoint.
	_, err = c.Head(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, f.callCount("eth_chainId"))
}

func TestHeadMalformed(t *testing.T) {
	for name, result := range map[string]any{
		"not hex":   "0xzz",
		"no prefix": "1234",
		"empty":     "0x",
		"number":    1234,
		"null":      nil,
		"overflow":  "0x1ffffffffffffffff",
		"object":    map[string]any{"a": 1},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeNode(1)
			f.hook = func(m string, _ []json.RawMessage) (any, *rpcErrorObject, bool) {
				return result, nil, m == "eth_blockNumber"
			}
			_, err := newTestClient(t, f).Head(context.Background())
			require.Error(t, err)
			require.Contains(t, err.Error(), "evm testchain: head")
		})
	}
}

// erc20Fixture: blocks 100..110 (head 110), logs in 101..106.
func erc20Fixture() *fakeNode {
	f := newFakeNode(110)
	f.ensureBlocks(95, 110)
	// Block 101: two watched recipients, one stranger, one unrequested token.
	f.addLog(101, transferLog(tokUSDT, senderS, watchA, 1_000_000, hashN("tx", 1), 0))
	f.addLog(101, transferLog(tokUSDC, senderS, watchB, 2_500_000, hashN("tx", 2), 3))
	f.addLog(101, transferLog(tokUSDT, senderS, strangerC, 99, hashN("tx", 3), 4))
	f.addLog(101, transferLog(tokOther, senderS, watchA, 77, hashN("tx", 4), 5))
	// Block 102: removed, ERC-721, zero amount, wrong data length, wrong event.
	removed := transferLog(tokUSDT, senderS, watchA, 5, hashN("tx", 5), 0)
	removed.removed = true
	f.addLog(102, removed)
	f.addLog(102, fakeLog{address: tokUSDT, topics: []string{TransferTopic, addrTopic(senderS), addrTopic(watchA), amountData(big.NewInt(42))}, data: "0x", txHash: hashN("tx", 6), logIndex: 1})
	f.addLog(102, transferLog(tokUSDC, senderS, watchA, 0, hashN("tx", 7), 2))
	f.addLog(102, fakeLog{address: tokUSDC, topics: []string{TransferTopic, addrTopic(senderS), addrTopic(watchA)}, data: amountData(big.NewInt(1)) + strings.Repeat("0", 64), txHash: hashN("tx", 8), logIndex: 3})
	approval := transferLog(tokUSDC, senderS, watchA, 9, hashN("tx", 80), 4)
	approval.topics[0] = hashN("approval", 0)
	f.addLog(102, approval)
	// Block 103: one tx paying watchA twice with different tokens.
	f.addLog(103, transferLog(tokUSDT, senderS, watchA, 8, hashN("tx", 9), 2))
	f.addLog(103, transferLog(tokUSDC, senderS, watchA, 7, hashN("tx", 9), 1))
	// Block 106: outside the scanned range.
	f.addLog(106, transferLog(tokUSDT, senderS, watchA, 6, hashN("tx", 10), 0))
	return f
}

func eip(a [20]byte) string { return eip55(a) }

func erc20Expected(f *fakeNode) []chain.Transfer {
	tr := func(block uint64, tx string, idx int, token, to [20]byte, amt int64) chain.Transfer {
		b := f.block(block)
		return chain.Transfer{
			TxHash: tx, Index: idx, BlockNumber: block, BlockHash: b.hash,
			BlockTime: time.Unix(int64(b.time), 0).UTC(),
			From:      eip(senderS), To: eip(to), Token: eip(token), Amount: big.NewInt(amt),
		}
	}
	return []chain.Transfer{
		tr(101, hashN("tx", 1), 0, tokUSDT, watchA, 1_000_000),
		tr(101, hashN("tx", 2), 3, tokUSDC, watchB, 2_500_000),
		tr(103, hashN("tx", 9), 1, tokUSDC, watchA, 7),
		tr(103, hashN("tx", 9), 2, tokUSDT, watchA, 8),
	}
}

func scanReq(from, to uint64, native bool, tokens []string, addrs ...[20]byte) chain.ScanRequest {
	r := chain.ScanRequest{FromBlock: from, ToBlock: to, Tokens: tokens, Native: native}
	for _, a := range addrs {
		r.Addresses = append(r.Addresses, eip(a))
	}
	return r
}

func TestScanERC20(t *testing.T) {
	for _, tc := range []struct {
		name       string
		sloppy     bool
		omitTS     bool
		wantBlocks int // eth_getBlockByNumber calls
	}{
		{name: "with blockTimestamp", wantBlocks: 1},
		{name: "headers fetched when blockTimestamp missing", omitTS: true, wantBlocks: 3},
		{name: "provider ignoring filters", sloppy: true, wantBlocks: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := erc20Fixture()
			f.sloppyLogs, f.omitLogTimestamp = tc.sloppy, tc.omitTS
			c := newTestClient(t, f)
			// Lower-case token input is accepted too.
			req := scanReq(100, 105, false, []string{eip(tokUSDT), strings.ToLower(eip(tokUSDC))}, watchA, watchB)
			got, err := c.ScanTransfers(context.Background(), req)
			require.NoError(t, err)
			require.Equal(t, erc20Expected(f), got)
			require.Equal(t, 1, f.callCount("eth_getLogs"))
			require.Equal(t, tc.wantBlocks, f.callCount("eth_getBlockByNumber"))
			require.Zero(t, f.callCount("eth_getTransactionReceipt"))
		})
	}
}

func TestScanERC20Chunking(t *testing.T) {
	f := newFakeNode(50)
	f.ensureBlocks(40, 50)
	f.maxTopicOr = 100
	var watched [][20]byte
	for i := range 250 {
		watched = append(watched, addrN(1000+i))
	}
	f.addLog(41, transferLog(tokUSDT, senderS, watched[0], 1, hashN("c", 0), 0))
	f.addLog(42, transferLog(tokUSDT, senderS, watched[120], 2, hashN("c", 1), 0))
	f.addLog(43, transferLog(tokUSDT, senderS, watched[249], 3, hashN("c", 2), 0))
	req := scanReq(40, 45, false, []string{eip(tokUSDT)}, watched...)

	t.Run("chunked", func(t *testing.T) {
		c := newTestClient(t, f, WithAddressChunkSize(100))
		got, err := c.ScanTransfers(context.Background(), req)
		require.NoError(t, err)
		require.Len(t, got, 3)
		require.Equal(t, 3, f.callCount("eth_getLogs"))
		for i, tr := range got {
			require.Equal(t, int64(i+1), tr.Amount.Int64())
		}
	})
	t.Run("provider rejects oversized list", func(t *testing.T) {
		c := newTestClient(t, f, WithAddressChunkSize(300))
		_, err := c.ScanTransfers(context.Background(), req)
		require.ErrorContains(t, err, "too many topics")
		require.NotErrorIs(t, err, chain.ErrRangeTooLarge)
	})
	t.Run("tokens chunked too", func(t *testing.T) {
		g := newFakeNode(50)
		g.ensureBlocks(40, 50)
		g.addLog(41, transferLog(addrN(500), senderS, watchA, 1, hashN("t", 0), 0))
		g.addLog(41, transferLog(addrN(502), senderS, watchB, 2, hashN("t", 1), 1))
		c := newTestClient(t, g, WithAddressChunkSize(2))
		toks := []string{eip(addrN(500)), eip(addrN(501)), eip(addrN(502))}
		got, err := c.ScanTransfers(context.Background(), scanReq(40, 45, false, toks, watchA, watchB, strangerC))
		require.NoError(t, err)
		require.Len(t, got, 2)
		require.Equal(t, 4, g.callCount("eth_getLogs")) // 2 token chunks × 2 address chunks
	})
}

// nativeFixture: blocks 200..202 with plain value transfers.
func nativeFixture() *fakeNode {
	f := newFakeNode(210)
	f.ensureBlocks(195, 210)
	f.addTx(200, fakeTx{hash: hashN("n", 0), from: senderS, to: ptr(watchA), value: eth(1), status: 1})
	f.addTx(200, fakeTx{hash: hashN("n", 1), from: senderS, to: ptr(watchA), value: big.NewInt(0), status: 1}) // zero value
	f.addTx(200, fakeTx{hash: hashN("n", 2), from: senderS, to: ptr(strangerC), value: eth(5), status: 1})     // not watched
	f.addTx(200, fakeTx{hash: hashN("n", 3), from: senderS, to: nil, value: eth(5), status: 1})                // contract creation
	f.addTx(200, fakeTx{hash: hashN("n", 4), from: senderS, to: ptr(watchB), value: eth(2), status: 0})        // reverted
	f.addTx(201, fakeTx{hash: hashN("n", 6), from: senderS2, to: ptr(watchB), value: big.NewInt(3), status: 1})
	f.addTx(201, fakeTx{hash: hashN("n", 5), from: senderS, to: ptr(watchB), value: big.NewInt(4), status: 1})
	return f
}

func nativeExpected(f *fakeNode) []chain.Transfer {
	tr := func(block uint64, tx string, from, to [20]byte, amt *big.Int) chain.Transfer {
		b := f.block(block)
		return chain.Transfer{
			TxHash: tx, Index: -1, BlockNumber: block, BlockHash: b.hash,
			BlockTime: time.Unix(int64(b.time), 0).UTC(), From: eip(from), To: eip(to), Amount: amt,
		}
	}
	return []chain.Transfer{
		tr(200, hashN("n", 0), senderS, watchA, eth(1)),
		tr(201, hashN("n", 6), senderS2, watchB, big.NewInt(3)), // tx order inside the block
		tr(201, hashN("n", 5), senderS, watchB, big.NewInt(4)),
	}
}

func TestScanNative(t *testing.T) {
	f := nativeFixture()
	c := newTestClient(t, f)
	got, err := c.ScanTransfers(context.Background(), scanReq(200, 202, true, nil, watchA, watchB))
	require.NoError(t, err)
	require.Equal(t, nativeExpected(f), got)
	require.Equal(t, 3, f.callCount("eth_getBlockByNumber"))
	require.Equal(t, 4, f.callCount("eth_getTransactionReceipt"), "receipts only for candidates")
	require.Zero(t, f.callCount("eth_getLogs"))
}

func TestScanNativeAndTokens(t *testing.T) {
	f := nativeFixture()
	f.addLog(200, transferLog(tokUSDT, senderS, watchA, 10, hashN("tx", 100), 7))
	f.addLog(200, transferLog(tokUSDT, senderS, watchA, 11, hashN("tx", 101), 2))
	c := newTestClient(t, f)
	got, err := c.ScanTransfers(context.Background(), scanReq(200, 202, true, []string{eip(tokUSDT)}, watchA, watchB))
	require.NoError(t, err)
	require.Len(t, got, 5)
	// Block 200: native (-1) first, then logs by index.
	assert.Equal(t, []int{-1, 2, 7, -1, -1}, []int{got[0].Index, got[1].Index, got[2].Index, got[3].Index, got[4].Index})
	assert.Equal(t, []uint64{200, 200, 200, 201, 201}, []uint64{got[0].BlockNumber, got[1].BlockNumber, got[2].BlockNumber, got[3].BlockNumber, got[4].BlockNumber})
	assert.Equal(t, eip(tokUSDT), got[1].Token)
	assert.Empty(t, got[0].Token)
	// Headers of the native scan are reused: no extra header fetch for logs.
	require.Equal(t, 3, f.callCount("eth_getBlockByNumber"))
}

func TestScanNothingToDo(t *testing.T) {
	f := newFakeNode(10)
	c := newTestClient(t, f)
	got, err := c.ScanTransfers(context.Background(), scanReq(1, 5, false, nil, watchA))
	require.NoError(t, err)
	require.Empty(t, got)
	got, err = c.ScanTransfers(context.Background(), scanReq(1, 5, true, []string{eip(tokUSDT)}))
	require.NoError(t, err)
	require.Empty(t, got)
	require.Zero(t, f.requests())
}

func TestScanInvalidRequest(t *testing.T) {
	f := newFakeNode(10)
	c := newTestClient(t, f)
	_, err := c.ScanTransfers(context.Background(), scanReq(5, 4, true, nil, watchA))
	require.ErrorContains(t, err, "invalid block range")
	_, err = c.ScanTransfers(context.Background(), chain.ScanRequest{FromBlock: 1, ToBlock: 2, Native: true, Addresses: []string{"0x1234"}})
	require.ErrorContains(t, err, "watched address")
	// Flip the case of one letter of the EIP-55 form: mixed case, wrong checksum.
	good := eip(watchA)
	i := strings.IndexAny(good[2:], "abcdefABCDEF") + 2
	require.Greater(t, strings.Count(strings.ToLower(good[2:]), "a")+strings.Count(strings.ToLower(good[2:]), "b")+
		strings.Count(strings.ToLower(good[2:]), "c")+strings.Count(strings.ToLower(good[2:]), "d")+
		strings.Count(strings.ToLower(good[2:]), "e")+strings.Count(strings.ToLower(good[2:]), "f"), 1)
	flipped := good[i] ^ 0x20
	bad := good[:i] + string(flipped) + good[i+1:]
	_, err = c.ScanTransfers(context.Background(), chain.ScanRequest{FromBlock: 1, ToBlock: 2, Native: true, Addresses: []string{bad}})
	require.ErrorIs(t, err, address.ErrInvalid)
	_, err = c.ScanTransfers(context.Background(), chain.ScanRequest{FromBlock: 1, ToBlock: 2, Tokens: []string{"TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"}, Addresses: []string{eip(watchA)}})
	require.ErrorContains(t, err, "token")
	require.Zero(t, f.requests())
}

func TestScanDuplicateAddresses(t *testing.T) {
	f := erc20Fixture()
	c := newTestClient(t, f)
	req := scanReq(100, 105, false, []string{eip(tokUSDT), eip(tokUSDC), eip(tokUSDT)}, watchA, watchB, watchA)
	req.Addresses = append(req.Addresses, strings.ToLower(eip(watchB)))
	got, err := c.ScanTransfers(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, erc20Expected(f), got)
}

func TestScanBatching(t *testing.T) {
	for _, conc := range []int{1, 4} {
		f := newFakeNode(400)
		f.ensureBlocks(300, 400)
		for i := range 25 {
			f.addTx(uint64(300+i), fakeTx{hash: hashN("b", i), from: senderS, to: ptr(watchA), value: big.NewInt(int64(i + 1)), status: 1})
		}
		c := newTestClient(t, f, WithBatchSize(10), WithConcurrency(conc))
		got, err := c.ScanTransfers(context.Background(), scanReq(300, 324, true, nil, watchA))
		require.NoError(t, err)
		require.Len(t, got, 25)
		for i, tr := range got {
			require.Equal(t, uint64(300+i), tr.BlockNumber)
			require.Equal(t, int64(i+1), tr.Amount.Int64())
		}
		f.mu.Lock()
		sizes := append([]int(nil), f.batchSizes...)
		f.mu.Unlock()
		// 25 blocks in 10+10+5, then 25 receipts in 10+10+5.
		require.ElementsMatch(t, []int{10, 10, 5, 10, 10, 5}, sizes, "concurrency %d", conc)
	}
}

func TestBatchRejectedFallsBackToSingleCalls(t *testing.T) {
	f := nativeFixture()
	f.rejectBatches = true
	c := newTestClient(t, f)
	got, err := c.ScanTransfers(context.Background(), scanReq(200, 202, true, nil, watchA, watchB))
	require.NoError(t, err)
	require.Equal(t, nativeExpected(f), got)
}

func TestScanNodeBehind(t *testing.T) {
	t.Run("logs", func(t *testing.T) {
		f := erc20Fixture()
		f.head = 104 // the provider has not seen block 105 yet and clamps eth_getLogs
		_, err := newTestClient(t, f).ScanTransfers(context.Background(), scanReq(100, 105, false, []string{eip(tokUSDT)}, watchA))
		require.ErrorContains(t, err, "block 105 not available")
	})
	t.Run("native", func(t *testing.T) {
		f := nativeFixture()
		f.head = 201
		_, err := newTestClient(t, f).ScanTransfers(context.Background(), scanReq(200, 202, true, nil, watchA))
		require.ErrorContains(t, err, "block 202 not available")
	})
	t.Run("receipt missing", func(t *testing.T) {
		f := nativeFixture()
		f.blocks[200].txs[0].noReceipt = true
		got, err := newTestClient(t, f).ScanTransfers(context.Background(), scanReq(200, 202, true, nil, watchA))
		require.ErrorContains(t, err, "not available")
		require.Nil(t, got)
	})
}

func TestScanReorgDuringScan(t *testing.T) {
	t.Run("log block hash differs from header", func(t *testing.T) {
		f := erc20Fixture()
		l := transferLog(tokUSDT, senderS, watchA, 5, hashN("tx", 50), 9)
		l.blockHash = hashN("other-fork", 105)
		f.addLog(105, l)
		_, err := newTestClient(t, f).ScanTransfers(context.Background(), scanReq(100, 105, false, []string{eip(tokUSDT)}, watchA))
		require.ErrorContains(t, err, "reorg")
	})
	t.Run("native receipt in other block", func(t *testing.T) {
		f := nativeFixture()
		f.hook = func(m string, p []json.RawMessage) (any, *rpcErrorObject, bool) {
			if m != "eth_getTransactionReceipt" {
				return nil, nil, false
			}
			return map[string]any{"blockHash": hashN("other-fork", 1), "status": "0x1"}, nil, true
		}
		_, err := newTestClient(t, f).ScanTransfers(context.Background(), scanReq(200, 202, true, nil, watchA))
		require.ErrorContains(t, err, "reorg")
	})
}

func TestScanErrorReturnsNoPartialResult(t *testing.T) {
	f := newFakeNode(50)
	f.ensureBlocks(40, 50)
	for i := range 250 {
		f.addLog(41, transferLog(tokUSDT, senderS, addrN(1000+i), 1, hashN("p", i), uint64(i)))
	}
	var n int
	var mu sync.Mutex
	f.hook = func(m string, _ []json.RawMessage) (any, *rpcErrorObject, bool) {
		if m != "eth_getLogs" {
			return nil, nil, false
		}
		mu.Lock()
		defer mu.Unlock()
		n++
		if n == 2 { // the second chunk fails permanently
			return nil, &rpcErrorObject{Code: -32602, Message: "invalid argument"}, true
		}
		return nil, nil, false
	}
	var watched [][20]byte
	for i := range 250 {
		watched = append(watched, addrN(1000+i))
	}
	c := newTestClient(t, f, WithAddressChunkSize(100), WithBatchSize(1), WithConcurrency(1))
	got, err := c.ScanTransfers(context.Background(), scanReq(40, 45, false, []string{eip(tokUSDT)}, watched...))
	require.Error(t, err)
	require.Nil(t, got)
	var rerr *RPCError
	require.ErrorAs(t, err, &rerr)
	require.Equal(t, -32602, rerr.Code)
}

func TestScanMalformed(t *testing.T) {
	goodLog := func() map[string]any {
		return map[string]any{
			"address":         addrHex(tokUSDT),
			"topics":          []string{TransferTopic, addrTopic(senderS), addrTopic(watchA)},
			"data":            amountData(big.NewInt(5)),
			"blockNumber":     "0x65",
			"blockHash":       hashN("block", 101),
			"blockTimestamp":  "0x10",
			"transactionHash": hashN("tx", 1),
			"logIndex":        "0x0",
			"removed":         false,
		}
	}
	logWith := func(k string, v any) any {
		l := goodLog()
		l[k] = v
		return []any{l}
	}
	logCases := map[string]any{
		"result object":    map[string]any{"a": 1},
		"result string":    "0x",
		"bad block number": logWith("blockNumber", "0xZZ"),
		"odd data":         logWith("data", "0x123"),
		"short from topic": logWith("topics", []string{TransferTopic, "0x1234", addrTopic(watchA)}),
		"bad tx hash":      logWith("transactionHash", "0x12"),
		"bad block hash":   logWith("blockHash", 7),
		"bad log index":    logWith("logIndex", "0x"),
		"huge log index":   logWith("logIndex", "0xffffffffff"),
		"bad timestamp":    logWith("blockTimestamp", "yesterday"),
		"bad address":      logWith("address", "0xabc"),
		"topics not array": logWith("topics", "x"),
		"entries not logs": []any{1, 2},
		"removed not bool": logWith("removed", "no"),
	}
	for name, res := range logCases {
		t.Run("logs/"+name, func(t *testing.T) {
			f := erc20Fixture()
			f.hook = func(m string, _ []json.RawMessage) (any, *rpcErrorObject, bool) {
				return res, nil, m == "eth_getLogs"
			}
			got, err := newTestClient(t, f).ScanTransfers(context.Background(), scanReq(100, 105, false, []string{eip(tokUSDT)}, watchA))
			require.Error(t, err)
			require.Nil(t, got)
		})
	}

	t.Run("logs/null means none", func(t *testing.T) {
		f := erc20Fixture()
		f.hook = func(m string, _ []json.RawMessage) (any, *rpcErrorObject, bool) { return nil, nil, m == "eth_getLogs" }
		got, err := newTestClient(t, f).ScanTransfers(context.Background(), scanReq(100, 105, false, []string{eip(tokUSDT)}, watchA))
		require.NoError(t, err)
		require.Empty(t, got)
	})

	blockCases := map[string]func(b map[string]any){
		"number mismatch":  func(b map[string]any) { b["number"] = "0x1" },
		"missing hash":     func(b map[string]any) { delete(b, "hash") },
		"bad timestamp":    func(b map[string]any) { b["timestamp"] = "0x" },
		"tx hashes only":   func(b map[string]any) { b["transactions"] = []string{hashN("n", 0)} },
		"bad value":        func(b map[string]any) { setTx(b, "value", "-0x1") },
		"value too big":    func(b map[string]any) { setTx(b, "value", "0x1"+strings.Repeat("0", 64)) },
		"bad to":           func(b map[string]any) { setTx(b, "to", "0x12") },
		"bad from":         func(b map[string]any) { setTx(b, "from", 5) },
		"bad hash":         func(b map[string]any) { setTx(b, "hash", "0xnothex") },
		"txs not an array": func(b map[string]any) { b["transactions"] = "nope" },
	}
	for name, mutate := range blockCases {
		t.Run("block/"+name, func(t *testing.T) {
			f := nativeFixture()
			f.hook = func(m string, p []json.RawMessage) (any, *rpcErrorObject, bool) {
				if m != "eth_getBlockByNumber" {
					return nil, nil, false
				}
				res, _ := f.dispatch(m, p)
				b := res.(map[string]any)
				if b["number"] == "0xc8" {
					mutate(b)
				}
				return b, nil, true
			}
			got, err := newTestClient(t, f).ScanTransfers(context.Background(), scanReq(200, 202, true, nil, watchA))
			require.Error(t, err)
			require.Nil(t, got)
		})
	}

	receiptCases := map[string]any{
		"no status":      map[string]any{"blockHash": hashN("block", 200)},
		"status 2":       map[string]any{"blockHash": hashN("block", 200), "status": "0x2"},
		"bad status":     map[string]any{"blockHash": hashN("block", 200), "status": true},
		"bad block hash": map[string]any{"blockHash": "0x", "status": "0x1"},
		"not an object":  []int{1},
	}
	for name, res := range receiptCases {
		t.Run("receipt/"+name, func(t *testing.T) {
			f := nativeFixture()
			f.hook = func(m string, _ []json.RawMessage) (any, *rpcErrorObject, bool) {
				return res, nil, m == "eth_getTransactionReceipt"
			}
			got, err := newTestClient(t, f).ScanTransfers(context.Background(), scanReq(200, 202, true, nil, watchA))
			require.Error(t, err)
			require.Nil(t, got)
		})
	}
}

func setTx(b map[string]any, k string, v any) {
	txs := b["transactions"].([]any)
	txs[0].(map[string]any)[k] = v
}

func TestVerifyTransfer(t *testing.T) {
	f := nativeFixture()
	f.addLog(201, transferLog(tokUSDT, senderS, watchA, 10, hashN("n", 5), 0))
	c := newTestClient(t, f)
	ctx := context.Background()
	got, err := c.ScanTransfers(ctx, scanReq(200, 202, true, []string{eip(tokUSDT)}, watchA, watchB))
	require.NoError(t, err)
	require.Len(t, got, 4)
	for _, tr := range got {
		ok, err := c.VerifyTransfer(ctx, tr)
		require.NoError(t, err)
		require.True(t, ok, "%s#%d", tr.TxHash, tr.Index)
	}
	native := got[0]

	t.Run("upper-case hashes accepted", func(t *testing.T) {
		tr := native
		tr.TxHash = "0x" + strings.ToUpper(tr.TxHash[2:])
		tr.BlockHash = "0x" + strings.ToUpper(tr.BlockHash[2:])
		ok, err := c.VerifyTransfer(ctx, tr)
		require.NoError(t, err)
		require.True(t, ok)
	})
	t.Run("reverted", func(t *testing.T) {
		tr := native
		tr.TxHash = hashN("n", 4)
		ok, err := c.VerifyTransfer(ctx, tr)
		require.NoError(t, err)
		require.False(t, ok)
	})
	t.Run("reorg replaced the block", func(t *testing.T) {
		g := nativeFixture()
		cg := newTestClient(t, g)
		res, err := cg.ScanTransfers(ctx, scanReq(200, 200, true, nil, watchA))
		require.NoError(t, err)
		require.Len(t, res, 1)
		g.mu.Lock()
		g.blocks[200].hash = hashN("fork", 200)
		g.blocks[200].txs = nil // tx dropped
		g.mu.Unlock()
		ok, err := cg.VerifyTransfer(ctx, res[0])
		require.NoError(t, err)
		require.False(t, ok)
	})
	t.Run("reorg moved the tx to another block", func(t *testing.T) {
		g := nativeFixture()
		cg := newTestClient(t, g)
		res, err := cg.ScanTransfers(ctx, scanReq(200, 200, true, nil, watchA))
		require.NoError(t, err)
		g.mu.Lock()
		tx := g.blocks[200].txs[0]
		g.blocks[200].hash = hashN("fork", 200)
		g.blocks[200].txs = g.blocks[200].txs[1:]
		g.blocks[203] = &fakeBlock{number: 203, hash: hashN("block", 203), time: baseTime, txs: []fakeTx{tx}}
		g.mu.Unlock()
		ok, err := cg.VerifyTransfer(ctx, res[0])
		require.NoError(t, err)
		require.False(t, ok)
	})
	t.Run("node behind", func(t *testing.T) {
		tr := native
		tr.BlockNumber = 500
		_, err := c.VerifyTransfer(ctx, tr)
		require.ErrorContains(t, err, "not available")
	})
	t.Run("canonical block but receipt missing", func(t *testing.T) {
		g := nativeFixture()
		cg := newTestClient(t, g)
		res, err := cg.ScanTransfers(ctx, scanReq(200, 200, true, nil, watchA))
		require.NoError(t, err)
		g.mu.Lock()
		g.blocks[200].txs[0].noReceipt = true
		g.mu.Unlock()
		_, err = cg.VerifyTransfer(ctx, res[0])
		require.ErrorContains(t, err, "no receipt")
	})
	t.Run("unknown tx in reorged block", func(t *testing.T) {
		tr := native
		tr.TxHash = hashN("never", 1)
		tr.BlockHash = hashN("fork", 1)
		ok, err := c.VerifyTransfer(ctx, tr)
		require.NoError(t, err)
		require.False(t, ok)
	})
	t.Run("invalid input", func(t *testing.T) {
		tr := native
		tr.TxHash = "0x1"
		_, err := c.VerifyTransfer(ctx, tr)
		require.Error(t, err)
		tr = native
		tr.BlockHash = ""
		_, err = c.VerifyTransfer(ctx, tr)
		require.Error(t, err)
	})
}

func TestConcurrentUse(t *testing.T) {
	f := nativeFixture()
	f.addLog(201, transferLog(tokUSDT, senderS, watchA, 10, hashN("n", 5), 0))
	c := newTestClient(t, f, WithBatchSize(2), WithConcurrency(3))
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for range 16 {
		wg.Go(func() {
			got, err := c.ScanTransfers(context.Background(), scanReq(195, 205, true, []string{eip(tokUSDT)}, watchA, watchB))
			if err == nil && len(got) != 4 {
				err = errors.New("wrong result count")
			}
			if err == nil {
				_, err = c.Head(context.Background())
			}
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
}

func TestContextCancellation(t *testing.T) {
	hang := func(w http.ResponseWriter, r *http.Request, _ []byte) bool {
		<-r.Context().Done()
		return true
	}
	t.Run("in flight", func(t *testing.T) {
		f := newFakeNode(10)
		f.httpHook = hang
		c := newTestClient(t, f, WithChainIDCheck(false))
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := c.Head(ctx)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.Less(t, time.Since(start), 3*time.Second)
	})
	t.Run("already cancelled", func(t *testing.T) {
		f := nativeFixture()
		c := newTestClient(t, f)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := c.ScanTransfers(ctx, scanReq(200, 202, true, []string{eip(tokUSDT)}, watchA))
		require.ErrorIs(t, err, context.Canceled)
		_, err = c.VerifyTransfer(ctx, chain.Transfer{TxHash: hashN("n", 0), BlockHash: hashN("block", 200), BlockNumber: 200})
		require.ErrorIs(t, err, context.Canceled)
		require.Zero(t, f.requests())
	})
	t.Run("during scan", func(t *testing.T) {
		f := nativeFixture()
		f.ensureBlocks(200, 300)
		var once sync.Once
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		f.httpHook = func(w http.ResponseWriter, r *http.Request, b []byte) bool {
			if !strings.Contains(string(b), "eth_getBlockByNumber") {
				return false
			}
			once.Do(cancel)
			<-r.Context().Done()
			return true
		}
		c := newTestClient(t, f, WithBatchSize(5), WithConcurrency(3))
		got, err := c.ScanTransfers(ctx, scanReq(200, 300, true, nil, watchA))
		require.ErrorIs(t, err, context.Canceled)
		require.Nil(t, got)
	})
	t.Run("during backoff", func(t *testing.T) {
		f := newFakeNode(10)
		f.httpHook = func(w http.ResponseWriter, _ *http.Request, _ []byte) bool {
			w.WriteHeader(http.StatusServiceUnavailable)
			return true
		}
		_, u := newRPCServer(t, f)
		c, err := New(testChain(u), WithBackoff(10*time.Second, 10*time.Second), WithRetries(5))
		require.NoError(t, err)
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err = c.Head(ctx)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.Less(t, time.Since(start), 3*time.Second)
	})
}
