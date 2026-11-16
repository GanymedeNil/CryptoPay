//go:build live

package evm_test

// Live tests against the free public Ethereum endpoint from the catalog.
// Run with: go test -tags live ./internal/chain/evm/...
//
// Expectations are computed independently with raw JSON-RPC calls, then
// compared with what the adapter returns. The test skips only when the
// network is unreachable.

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"cryptopay/internal/address"
	"cryptopay/internal/catalog"
	"cryptopay/internal/chain"
	"cryptopay/internal/chain/evm"
	"cryptopay/internal/domain"
)

const usdtEthereum = "0xdAC17F958D2ee523a2206206994597C13D831ec7"

// confirmations keeps the test away from the chain tip (reorgs).
const confirmations = 8

type transportError struct{ err error }

func (e transportError) Error() string { return "transport: " + e.err.Error() }

type rawClient struct {
	url string
	hc  *http.Client
}

func (r *rawClient) once(ctx context.Context, method string, params []any, out any) error {
	if params == nil {
		params = []any{}
	}
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.hc.Do(req)
	if err != nil {
		return transportError{err}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: http %d", method, resp.StatusCode)
	}
	var env struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return fmt.Errorf("%s: decode: %w", method, err)
	}
	if env.Error != nil {
		return fmt.Errorf("%s: rpc error %d: %s", method, env.Error.Code, env.Error.Message)
	}
	return json.Unmarshal(env.Result, out)
}

// call retries every failure a few times: public endpoints are flaky.
func (r *rawClient) call(ctx context.Context, method string, params []any, out any) error {
	var err error
	for attempt := range 5 {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(1<<attempt) * 500 * time.Millisecond):
			}
		}
		if err = r.once(ctx, method, params, out); err == nil {
			return nil
		}
	}
	return err
}

func hexU(n uint64) string { return "0x" + strconv.FormatUint(n, 16) }

func mustU(t *testing.T, s string) uint64 {
	t.Helper()
	v, err := strconv.ParseUint(strings.TrimPrefix(s, "0x"), 16, 64)
	require.NoError(t, err, s)
	return v
}

func mustBig(t *testing.T, s string) *big.Int {
	t.Helper()
	v, ok := new(big.Int).SetString(strings.TrimPrefix(s, "0x"), 16)
	require.True(t, ok, s)
	return v
}

func eip55(t *testing.T, s string) string {
	t.Helper()
	b, err := hex.DecodeString(strings.TrimPrefix(s, "0x"))
	require.NoError(t, err)
	out, err := address.EVMFromBytes(b[len(b)-20:])
	require.NoError(t, err)
	return out
}

type rawLog struct {
	Address         string   `json:"address"`
	Topics          []string `json:"topics"`
	Data            string   `json:"data"`
	BlockNumber     string   `json:"blockNumber"`
	BlockHash       string   `json:"blockHash"`
	TransactionHash string   `json:"transactionHash"`
	LogIndex        string   `json:"logIndex"`
	Removed         bool     `json:"removed"`
}

type rawTx struct {
	Hash  string  `json:"hash"`
	From  string  `json:"from"`
	To    *string `json:"to"`
	Value string  `json:"value"`
}

type rawBlock struct {
	Number       string  `json:"number"`
	Hash         string  `json:"hash"`
	Timestamp    string  `json:"timestamp"`
	Transactions []rawTx `json:"transactions"`
}

type rawReceipt struct {
	BlockHash string `json:"blockHash"`
	Status    string `json:"status"`
}

func isERC20Transfer(l rawLog) bool {
	return !l.Removed && len(l.Topics) == 3 && strings.EqualFold(l.Topics[0], evm.TransferTopic) &&
		len(l.Data) == 66 && strings.Trim(l.Data[2:], "0") != ""
}

func liveChain(t *testing.T) domain.Chain {
	for _, c := range catalog.Chains() {
		if c.ID == "ethereum" {
			return c
		}
	}
	t.Fatal("ethereum not in catalog")
	return domain.Chain{}
}

func TestLiveEthereum(t *testing.T) {
	ch := liveChain(t)
	raw := &rawClient{url: ch.RPCURLs[0], hc: &http.Client{Timeout: 30 * time.Second}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var headHex string
	if err := raw.call(ctx, "eth_blockNumber", nil, &headHex); err != nil {
		var te transportError
		if errors.As(err, &te) {
			t.Skipf("network unavailable: %v", err)
		}
		t.Fatalf("probe: %v", err)
	}
	rawHead := mustU(t, headHex)

	c, err := evm.New(ch,
		evm.WithRetries(6),
		evm.WithBackoff(500*time.Millisecond, 8*time.Second),
		evm.WithRequestTimeout(30*time.Second),
	)
	require.NoError(t, err)

	head, err := c.Head(ctx)
	require.NoError(t, err)
	require.Positive(t, head)
	require.InDelta(t, float64(rawHead), float64(head), 100)

	type rawHeader struct {
		Hash      string `json:"hash"`
		Timestamp string `json:"timestamp"`
	}
	header := func(t *testing.T, n uint64) rawHeader {
		var b rawHeader
		require.NoError(t, raw.call(ctx, "eth_getBlockByNumber", []any{hexU(n), false}, &b))
		require.NotEmpty(t, b.Hash, "block %d", n)
		return b
	}

	t.Run("USDT transfer", func(t *testing.T) {
		// Discover a real recent USDT Transfer.
		var pick *rawLog
		top := head - confirmations
		for b := top; b > top-40 && pick == nil; b-- {
			var logs []rawLog
			filter := map[string]any{"fromBlock": hexU(b), "toBlock": hexU(b), "address": usdtEthereum, "topics": []any{evm.TransferTopic}}
			require.NoError(t, raw.call(ctx, "eth_getLogs", []any{filter}, &logs))
			for i := range logs {
				if isERC20Transfer(logs[i]) {
					pick = &logs[i]
					break
				}
			}
		}
		require.NotNil(t, pick, "no USDT transfer in the last 40 blocks")
		blk := mustU(t, pick.BlockNumber)
		to := eip55(t, pick.Topics[2])
		from, until := blk-1, blk+1

		// Everything the adapter must return: all USDT transfers to `to` in range.
		var logs []rawLog
		filter := map[string]any{
			"fromBlock": hexU(from), "toBlock": hexU(until), "address": usdtEthereum,
			"topics": []any{evm.TransferTopic, nil, strings.ToLower(pick.Topics[2])},
		}
		require.NoError(t, raw.call(ctx, "eth_getLogs", []any{filter}, &logs))
		var want []chain.Transfer
		for _, l := range logs {
			if !isERC20Transfer(l) {
				continue
			}
			n := mustU(t, l.BlockNumber)
			want = append(want, chain.Transfer{
				TxHash:      strings.ToLower(l.TransactionHash),
				Index:       int(mustU(t, l.LogIndex)),
				BlockNumber: n,
				BlockHash:   strings.ToLower(l.BlockHash),
				BlockTime:   time.Unix(int64(mustU(t, header(t, n).Timestamp)), 0).UTC(),
				From:        eip55(t, l.Topics[1]),
				To:          to,
				Token:       usdtEthereum,
				Amount:      mustBig(t, l.Data),
			})
		}
		require.NotEmpty(t, want)

		got, err := c.ScanTransfers(ctx, chain.ScanRequest{
			FromBlock: from, ToBlock: until, Addresses: []string{to}, Tokens: []string{usdtEthereum},
		})
		require.NoError(t, err)
		require.Equal(t, want, got)
		t.Logf("USDT: %d transfer(s) to %s in blocks %d-%d, first %s#%d amount %s",
			len(got), to, from, until, got[0].TxHash, got[0].Index, got[0].Amount)

		for _, tr := range got {
			ok, err := c.VerifyTransfer(ctx, tr)
			require.NoError(t, err)
			require.True(t, ok, tr.TxHash)
		}
		forked := got[0]
		forked.BlockHash = "0x" + strings.Repeat("ab", 32)
		ok, err := c.VerifyTransfer(ctx, forked)
		require.NoError(t, err)
		require.False(t, ok, "a different block hash at that height means reorg")
	})

	t.Run("native transfer", func(t *testing.T) {
		var blk rawBlock
		var to string
		var n uint64
		top := head - confirmations
		receipts := map[string]rawReceipt{}
		receipt := func(hash string) rawReceipt {
			if r, ok := receipts[hash]; ok {
				return r
			}
			var r rawReceipt
			require.NoError(t, raw.call(ctx, "eth_getTransactionReceipt", []any{hash}, &r))
			receipts[hash] = r
			return r
		}
		// Discover a real successful plain value transfer.
	search:
		for n = top; n > top-10; n-- {
			blk = rawBlock{}
			require.NoError(t, raw.call(ctx, "eth_getBlockByNumber", []any{hexU(n), true}, &blk))
			tries := 0
			for _, tx := range blk.Transactions {
				if tx.To == nil || mustBig(t, tx.Value).Sign() == 0 {
					continue
				}
				if tries++; tries > 20 {
					break
				}
				if receipt(tx.Hash).Status == "0x1" {
					to = eip55(t, *tx.To)
					break search
				}
			}
		}
		require.NotEmpty(t, to, "no successful value transfer found")

		bt := time.Unix(int64(mustU(t, blk.Timestamp)), 0).UTC()
		var want []chain.Transfer
		for _, tx := range blk.Transactions {
			if tx.To == nil || !strings.EqualFold(*tx.To, to) || mustBig(t, tx.Value).Sign() == 0 {
				continue
			}
			if receipt(tx.Hash).Status != "0x1" {
				continue
			}
			want = append(want, chain.Transfer{
				TxHash: strings.ToLower(tx.Hash), Index: -1, BlockNumber: n,
				BlockHash: strings.ToLower(blk.Hash), BlockTime: bt,
				From: eip55(t, tx.From), To: to, Amount: mustBig(t, tx.Value),
			})
		}
		require.NotEmpty(t, want)

		got, err := c.ScanTransfers(ctx, chain.ScanRequest{FromBlock: n, ToBlock: n, Addresses: []string{to}, Native: true})
		require.NoError(t, err)
		require.Equal(t, want, got)
		t.Logf("native: %d transfer(s) to %s in block %d, first %s amount %s wei",
			len(got), to, n, got[0].TxHash, got[0].Amount)

		for _, tr := range got {
			ok, err := c.VerifyTransfer(ctx, tr)
			require.NoError(t, err)
			require.True(t, ok, tr.TxHash)
		}
	})
}
