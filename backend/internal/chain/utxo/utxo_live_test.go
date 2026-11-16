//go:build live

// Live tests against the free public endpoints of package catalog (or the
// RPC_<CHAIN_ID> environment variable, comma-separated, when set):
//
//	go test -tags live -v ./internal/chain/utxo/...
package utxo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"cryptopay/internal/catalog"
	"cryptopay/internal/chain"
	"cryptopay/internal/domain"
)

func TestLiveBitcoin(t *testing.T) {
	runLive(t, "bitcoin", true)
}

func TestLiveBitcoinTestnet(t *testing.T) {
	runLive(t, "bitcoin-testnet", true)
}

// The only keyless Litecoin JSON-RPC found (Tatum gateway) allows 5 requests
// per minute, so this test uses as few calls as possible and waits out 429s
// (Retry-After: 60); expect it to take about a minute. Set
// UTXO_LIVE_THOROUGH=1 (or RPC_LITECOIN to a keyed endpoint) for the full
// all-address check.
func TestLiveLitecoin(t *testing.T) {
	runLive(t, "litecoin", os.Getenv("UTXO_LIVE_THOROUGH") != "" || os.Getenv("RPC_LITECOIN") != "")
}

func liveChain(t *testing.T, id string) domain.Chain {
	t.Helper()
	for _, c := range catalog.Chains() {
		if c.ID != id {
			continue
		}
		env := "RPC_" + strings.ToUpper(strings.ReplaceAll(id, "-", "_"))
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			c.RPCURLs = strings.Split(v, ",")
			t.Logf("%s: using RPC URLs from %s", id, env)
		}
		return c
	}
	t.Fatalf("chain %q not in catalog", id)
	return domain.Chain{}
}

type liveVout struct {
	Value        json.Number `json:"value"`
	N            int         `json:"n"`
	ScriptPubKey struct {
		Address   string   `json:"address"`
		Addresses []string `json:"addresses"`
	} `json:"scriptPubKey"`
}

func (v liveVout) addr() string {
	if v.ScriptPubKey.Address != "" {
		return v.ScriptPubKey.Address
	}
	if len(v.ScriptPubKey.Addresses) == 1 {
		return v.ScriptPubKey.Addresses[0]
	}
	return ""
}

type liveBlock struct {
	Hash   string `json:"hash"`
	Height uint64 `json:"height"`
	Time   int64  `json:"time"`
	Tx     []struct {
		Txid string     `json:"txid"`
		Vout []liveVout `json:"vout"`
	} `json:"tx"`
}

func runLive(t *testing.T, chainID string, thorough bool) {
	c := liveChain(t, chainID)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(testLogWriter{t}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cl, err := New(c, WithLogger(logger), WithRetry(4, 2*time.Second, 70*time.Second))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	head, err := cl.Head(ctx)
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if head == 0 {
		t.Fatal("Head = 0")
	}
	t.Logf("%s head = %d", chainID, head)

	// A few blocks below the tip so a reorg between our calls is unlikely.
	height := head - 3
	rpcURL := c.RPCURLs[0]
	var hash string
	rawCall(t, ctx, rpcURL, "getblockhash", []any{height}, &hash)
	var blk liveBlock
	rawCall(t, ctx, rpcURL, "getblock", []any{hash, 2}, &blk)
	if blk.Hash != hash || blk.Height != height || len(blk.Tx) == 0 {
		t.Fatalf("raw getblock: hash %s height %d with %d txs; want %s at %d", blk.Hash, blk.Height, len(blk.Tx), hash, height)
	}

	// Pick a real output address, preferring a non-coinbase transaction.
	var target string
	for pass := 0; pass < 2 && target == ""; pass++ {
		for i, tx := range blk.Tx {
			if (pass == 0) == (i == 0) {
				continue
			}
			for _, v := range tx.Vout {
				if a := v.addr(); a != "" && sats(t, v.Value).Sign() > 0 {
					target = a
					break
				}
			}
			if target != "" {
				break
			}
		}
	}
	if target == "" {
		t.Fatalf("block %d has no output with an address", height)
	}

	// Every output of the block paying target, computed independently.
	type key struct {
		tx     string
		n      int
		amount string
	}
	var want []key
	for _, tx := range blk.Tx {
		for _, v := range tx.Vout {
			if v.addr() == target {
				if amt := sats(t, v.Value); amt.Sign() > 0 {
					want = append(want, key{tx.Txid, v.N, amt.String()})
				}
			}
		}
	}
	t.Logf("block %d (%s, %d txs): watching %s, expecting %v", height, hash, len(blk.Tx), target, want)

	got, err := cl.ScanTransfers(ctx, chain.ScanRequest{FromBlock: height, ToBlock: height, Native: true, Addresses: []string{target}})
	if err != nil {
		t.Fatalf("ScanTransfers: %v", err)
	}
	var gotKeys []key
	for _, g := range got {
		gotKeys = append(gotKeys, key{g.TxHash, g.Index, g.Amount.String()})
		if g.BlockNumber != height || g.BlockHash != hash || g.BlockTime.Unix() != blk.Time || g.To != target {
			t.Errorf("transfer %+v: wrong block/recipient", g)
		}
	}
	if fmt.Sprint(gotKeys) != fmt.Sprint(want) {
		t.Fatalf("ScanTransfers = %v, want %v", gotKeys, want)
	}

	ok, err := cl.VerifyTransfer(ctx, got[0])
	if err != nil || !ok {
		t.Fatalf("VerifyTransfer = %v, %v; want true", ok, err)
	}
	if thorough {
		// Watch every address of the block: the adapter must reproduce every
		// paying output, in order, with exact amounts.
		seen := map[string]bool{}
		var all []string
		want = want[:0]
		for _, tx := range blk.Tx {
			for _, v := range tx.Vout {
				a := v.addr()
				amt := sats(t, v.Value)
				if a == "" || amt.Sign() == 0 {
					continue
				}
				if !seen[a] {
					seen[a] = true
					all = append(all, a)
				}
				want = append(want, key{tx.Txid, v.N, amt.String()})
			}
		}
		got, err := cl.ScanTransfers(ctx, chain.ScanRequest{FromBlock: height, ToBlock: height, Native: true, Addresses: all})
		if err != nil {
			t.Fatalf("ScanTransfers(all %d addresses): %v", len(all), err)
		}
		gotKeys = gotKeys[:0]
		for _, g := range got {
			gotKeys = append(gotKeys, key{g.TxHash, g.Index, g.Amount.String()})
		}
		if fmt.Sprint(gotKeys) != fmt.Sprint(want) {
			t.Fatalf("ScanTransfers(all addresses) returned %d transfers, want %d (or contents differ)", len(gotKeys), len(want))
		}
		t.Logf("all-address scan: %d addresses, %d transfers match the raw block exactly", len(all), len(got))

		stale := got[0]
		stale.BlockHash = strings.Repeat("0", 64)
		if ok, err := cl.VerifyTransfer(ctx, stale); err != nil || ok {
			t.Fatalf("VerifyTransfer(wrong hash) = %v, %v; want false", ok, err)
		}
	}
}

// sats converts a node value ("0.00114662") to base units with big.Rat,
// independently of parseAmount.
func sats(t *testing.T, v json.Number) *big.Int {
	t.Helper()
	r, ok := new(big.Rat).SetString(v.String())
	if !ok {
		t.Fatalf("bad value %q", v)
	}
	r.Mul(r, new(big.Rat).SetInt64(100_000_000))
	if !r.IsInt() {
		t.Fatalf("value %q is not a whole number of base units", v)
	}
	return new(big.Int).Set(r.Num())
}

// rawCall is a minimal JSON-RPC client sharing no code with the adapter. It
// waits out 429 responses (free endpoints are heavily rate limited).
func rawCall(t *testing.T, ctx context.Context, rpcURL, method string, params []any, out any) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	for attempt := 1; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, rpcURL, bytes.NewReader(body))
		if err != nil {
			t.Fatalf("%s: build request failed", method)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			var ue *url.Error
			if errors.As(err, &ue) {
				err = ue.Err // *url.Error quotes the URL, which may hold an API key
			}
			t.Fatalf("%s via %s: %v", method, req.URL.Host, err)
		}
		if resp.StatusCode == http.StatusTooManyRequests && attempt < 5 {
			wait, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
			resp.Body.Close()
			wait = max(wait, 5)
			t.Logf("%s: rate limited, waiting %ds", method, wait)
			select {
			case <-ctx.Done():
				t.Fatalf("%s: %v", method, ctx.Err())
			case <-time.After(time.Duration(wait) * time.Second):
			}
			continue
		}
		var env struct {
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		err = json.NewDecoder(resp.Body).Decode(&env)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("%s: http %d, decode: %v", method, resp.StatusCode, err)
		}
		if len(env.Error) > 0 && string(env.Error) != "null" {
			t.Fatalf("%s: rpc error %s", method, env.Error)
		}
		if err := json.Unmarshal(env.Result, out); err != nil {
			t.Fatalf("%s: decode result: %v", method, err)
		}
		return
	}
}

type testLogWriter struct{ t *testing.T }

func (w testLogWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimSpace(string(p)))
	return len(p), nil
}
