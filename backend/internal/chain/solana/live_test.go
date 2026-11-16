//go:build live

// Live tests against real Solana clusters (the catalog's public endpoints by
// default; override with SOLANA_RPC_SOLANA / SOLANA_RPC_SOLANA_DEVNET,
// comma-separated). They discover a recent finalized incoming transfer with
// raw RPC calls, then check that ScanTransfers over its slot reports it.
//
//	go test -tags live -timeout 15m -v ./internal/chain/solana/...
package solana

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cryptopay/internal/catalog"
	"cryptopay/internal/chain"
	"cryptopay/internal/domain"
)

func liveChain(t *testing.T, id string) domain.Chain {
	t.Helper()
	for _, c := range catalog.Chains() {
		if c.ID != id {
			continue
		}
		env := "SOLANA_RPC_" + strings.ToUpper(strings.ReplaceAll(id, "-", "_"))
		if v := os.Getenv(env); v != "" {
			c.RPCURLs = strings.Split(v, ",")
		}
		return c
	}
	t.Fatalf("chain %q not in catalog", id)
	return domain.Chain{}
}

func liveMint(t *testing.T, chainID, symbol string) string {
	t.Helper()
	for _, a := range catalog.Assets() {
		if a.ChainID == chainID && a.Symbol == symbol {
			return a.ContractAddress
		}
	}
	t.Fatalf("asset %s on %s not in catalog", symbol, chainID)
	return ""
}

type testLogWriter struct{ t *testing.T }

func (w testLogWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimSpace(string(p)))
	return len(p), nil
}

// newLiveClient is gentle with public endpoints: one request at a time,
// patient retries honouring Retry-After (the public nodes send 10s).
func newLiveClient(t *testing.T, c domain.Chain) *Client {
	t.Helper()
	cl, err := New(c,
		WithConcurrency(1),
		WithRetries(6),
		WithBackoff(time.Second, 15*time.Second),
		WithMaxRetryWait(30*time.Second),
		WithMaxSignaturePages(3),
		WithLogger(slog.New(slog.NewTextHandler(testLogWriter{t}, &slog.HandlerOptions{Level: slog.LevelWarn}))),
	)
	require.NoError(t, err)
	return cl
}

// requireLive fails on err unless it means the network is unreachable or the
// provider keeps throttling us, in which case the test is skipped.
func requireLive(t *testing.T, err error, what string) {
	t.Helper()
	if err == nil {
		return
	}
	if errors.Is(err, chain.ErrRateLimited) {
		t.Skipf("%s: persistently rate limited: %v", what, err)
	}
	var ce *callError
	if errors.As(err, &ce) {
		unreachable, refused := true, true
		for _, e := range ce.errs {
			msg := e.Error()
			if !strings.Contains(msg, "transport:") && !strings.Contains(msg, "timed out") && !strings.Contains(msg, "http 5") {
				unreachable = false
			}
			// e.g. api.mainnet-beta.solana.com: "http 403: rpc error 403:
			// Your IP or provider is blocked from this endpoint".
			if !strings.Contains(msg, "http 401") && !strings.Contains(msg, "http 403") {
				refused = false
			}
		}
		if unreachable {
			t.Skipf("%s: network unavailable: %v", what, err)
		}
		if refused {
			t.Skipf("%s: endpoint refuses this host (set SOLANA_RPC_<CHAIN> to a usable RPC): %v", what, err)
		}
	}
	t.Fatalf("%s: %v", what, err)
}

// liveTx is decoded independently of the adapter's own types.
type liveTx struct {
	Slot uint64 `json:"slot"`
	Meta *struct {
		Err               json.RawMessage `json:"err"`
		PreBalances       []json.Number   `json:"preBalances"`
		PostBalances      []json.Number   `json:"postBalances"`
		PreTokenBalances  []liveTokenBal  `json:"preTokenBalances"`
		PostTokenBalances []liveTokenBal  `json:"postTokenBalances"`
	} `json:"meta"`
	Transaction struct {
		Signatures []string `json:"signatures"`
		Message    struct {
			AccountKeys []struct {
				Pubkey string `json:"pubkey"`
				Signer bool   `json:"signer"`
			} `json:"accountKeys"`
		} `json:"message"`
	} `json:"transaction"`
}

type liveTokenBal struct {
	AccountIndex  int    `json:"accountIndex"`
	Mint          string `json:"mint"`
	Owner         string `json:"owner"`
	UITokenAmount struct {
		Amount string `json:"amount"`
	} `json:"uiTokenAmount"`
}

type candidate struct {
	sig    string
	slot   uint64
	owner  string
	mint   string // "" = SOL
	index  int
	amount *big.Int
}

func bigOf(s string) *big.Int {
	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		return nil
	}
	return v
}

// discover returns incoming transfers found in recent finalized successful
// transactions touching seed: SPL credits of mint (mint != "") or SOL
// credits to accounts that are not token accounts (mint == "").
func discover(t *testing.T, ctx context.Context, c *Client, seed, mint string, maxTx int) []candidate {
	t.Helper()
	var sigs []signatureInfo
	err := c.rpc.call(ctx, "getSignaturesForAddress", []any{seed, map[string]any{"limit": 25, "commitment": "finalized"}}, &sigs)
	requireLive(t, err, "discover signatures")
	var out []candidate
	fetched := 0
	for _, s := range sigs {
		if !isNullJSON(s.Err) || fetched >= maxTx {
			continue
		}
		fetched++
		var tx *liveTx
		err := c.rpc.call(ctx, "getTransaction", []any{s.Signature, map[string]any{
			"encoding": "jsonParsed", "maxSupportedTransactionVersion": 1, "commitment": "finalized",
		}}, &tx)
		var rpcErr *RPCError
		if errors.As(err, &rpcErr) && rpcErr.Code == -32015 {
			continue // a newer tx version: not ours to judge here
		}
		requireLive(t, err, "discover transaction")
		if tx == nil || tx.Meta == nil || !isNullJSON(tx.Meta.Err) {
			continue
		}
		keys := tx.Transaction.Message.AccountKeys
		if mint != "" {
			pre := map[int]liveTokenBal{}
			for _, b := range tx.Meta.PreTokenBalances {
				pre[b.AccountIndex] = b
			}
			for _, b := range tx.Meta.PostTokenBalances {
				if b.Mint != mint || b.Owner == "" || b.AccountIndex >= len(keys) || keys[b.AccountIndex].Pubkey == b.Owner {
					continue
				}
				d := bigOf(b.UITokenAmount.Amount)
				if p, ok := pre[b.AccountIndex]; ok && p.Mint == mint {
					d.Sub(d, bigOf(p.UITokenAmount.Amount))
				}
				if d.Sign() > 0 {
					out = append(out, candidate{s.Signature, tx.Slot, b.Owner, mint, b.AccountIndex, d})
				}
			}
			continue
		}
		tokenIdx := map[int]bool{}
		for _, b := range append(tx.Meta.PreTokenBalances, tx.Meta.PostTokenBalances...) {
			tokenIdx[b.AccountIndex] = true
		}
		if len(tx.Meta.PreBalances) != len(keys) || len(tx.Meta.PostBalances) != len(keys) {
			continue
		}
		for i, k := range keys {
			if tokenIdx[i] {
				continue
			}
			d := bigOf(string(tx.Meta.PostBalances[i]))
			d.Sub(d, bigOf(string(tx.Meta.PreBalances[i])))
			if d.Sign() > 0 {
				out = append(out, candidate{s.Signature, tx.Slot, k.Pubkey, "", i, d})
			}
		}
	}
	return out
}

// quiet reports whether scanning cand.owner over cand.slot stays cheap:
// the owner's signatures since that slot fit in one page and it has few
// token accounts for the mint (pool authorities can own thousands).
func quiet(t *testing.T, ctx context.Context, c *Client, cand candidate) bool {
	t.Helper()
	var sigs []signatureInfo
	err := c.rpc.call(ctx, "getSignaturesForAddress", []any{cand.owner, map[string]any{"limit": 300, "commitment": "finalized"}}, &sigs)
	requireLive(t, err, "owner activity")
	if len(sigs) == 300 && sigs[len(sigs)-1].Slot > cand.slot {
		return false
	}
	if cand.mint == "" {
		return true
	}
	var res tokenAccountsResult
	err = c.rpc.call(ctx, "getTokenAccountsByOwner", []any{
		cand.owner, map[string]any{"mint": cand.mint},
		map[string]any{"encoding": "base64", "commitment": "finalized", "dataSlice": map[string]any{"offset": 0, "length": 0}},
	}, &res)
	requireLive(t, err, "owner token accounts")
	return len(res.Value) <= 3
}

func checkLiveTransfer(t *testing.T, ctx context.Context, c *Client, cands []candidate) {
	t.Helper()
	tried := 0
	for _, cand := range cands {
		if tried == 4 {
			break
		}
		if !quiet(t, ctx, c, cand) {
			t.Logf("skip busy owner %s", cand.owner)
			continue
		}
		tried++
		req := chain.ScanRequest{FromBlock: cand.slot, ToBlock: cand.slot, Addresses: []string{cand.owner}}
		if cand.mint == "" {
			req.Native = true
		} else {
			req.Tokens = []string{cand.mint}
		}
		start := time.Now()
		got, err := c.ScanTransfers(ctx, req)
		if errors.Is(err, ErrTooManyPages) {
			t.Logf("owner %s too busy: %v", cand.owner, err)
			continue
		}
		requireLive(t, err, "ScanTransfers")
		t.Logf("scanned %s over slot %d in %s: %d transfer(s)", cand.owner, cand.slot, time.Since(start).Round(time.Millisecond), len(got))

		var found *chain.Transfer
		for i := range got {
			if got[i].TxHash == cand.sig && got[i].Index == cand.index {
				found = &got[i]
			}
		}
		require.NotNil(t, found, "transfer %s#%d (%s %s) not reported: %+v", cand.sig, cand.index, cand.amount, cand.mint, got)
		assert.Equal(t, cand.amount.String(), found.Amount.String())
		assert.Equal(t, cand.mint, found.Token)
		assert.Equal(t, cand.owner, found.To)
		assert.Equal(t, cand.slot, found.BlockNumber)
		assert.False(t, found.BlockTime.IsZero())
		assert.NotEmpty(t, found.From)
		for _, tr := range got {
			assert.Equal(t, cand.owner, tr.To)
			assert.Equal(t, cand.slot, tr.BlockNumber)
		}

		ok, err := c.VerifyTransfer(ctx, *found)
		requireLive(t, err, "VerifyTransfer")
		assert.True(t, ok, "finalized transfer verifies")

		moved := *found
		moved.BlockNumber++
		ok, err = c.VerifyTransfer(ctx, moved)
		requireLive(t, err, "VerifyTransfer (wrong slot)")
		assert.False(t, ok)

		ok, err = c.VerifyTransfer(ctx, chain.Transfer{TxHash: fakeSig(cand.sig), BlockNumber: cand.slot})
		requireLive(t, err, "VerifyTransfer (unknown)")
		assert.False(t, ok)
		return
	}
	t.Skip("no suitable recent transfer found (all candidates too busy)")
}

func TestLiveHead(t *testing.T) {
	for _, id := range []string{"solana", "solana-devnet"} {
		t.Run(id, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			c := newLiveClient(t, liveChain(t, id))
			h1, err := c.Head(ctx)
			requireLive(t, err, "Head")
			assert.Greater(t, h1, uint64(100_000_000))
			h2, err := c.Head(ctx)
			requireLive(t, err, "Head")
			assert.GreaterOrEqual(t, h2, h1)
			t.Logf("%s finalized slot %d", id, h2)
		})
	}
}

func TestLiveScanSPLTransfer(t *testing.T) {
	for _, tc := range []struct{ chainID, symbol string }{
		{"solana-devnet", "USDC"},
		{"solana", "USDC"},
	} {
		t.Run(tc.chainID, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
			defer cancel()
			c := newLiveClient(t, liveChain(t, tc.chainID))
			mint := liveMint(t, tc.chainID, tc.symbol)
			cands := discover(t, ctx, c, mint, mint, 8)
			if len(cands) == 0 {
				t.Skip("no recent incoming transfer of the mint found")
			}
			checkLiveTransfer(t, ctx, c, cands)
		})
	}
}

func TestLiveScanNativeTransfer(t *testing.T) {
	for _, tc := range []struct {
		chainID string
		seeds   []string // accounts whose recent transactions move SOL
	}{
		// A devnet account receiving many plain system transfers, then USDC flows.
		{"solana-devnet", []string{"9B5XszUGdMaxCZ7uSQhPzdks5ZQSmWxrmzCSvtJ6Ns6g", liveMint(t, "solana-devnet", "USDC")}},
		{"solana", []string{liveMint(t, "solana", "USDC")}},
	} {
		t.Run(tc.chainID, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
			defer cancel()
			c := newLiveClient(t, liveChain(t, tc.chainID))
			var cands []candidate
			for _, seed := range tc.seeds {
				cands = append(cands, discover(t, ctx, c, seed, "", 5)...)
				if len(cands) > 0 {
					break
				}
			}
			if len(cands) == 0 {
				t.Skip("no recent native transfer found")
			}
			checkLiveTransfer(t, ctx, c, cands)
		})
	}
}
