//go:build live

package tron

// Live tests against the public TronGrid endpoints (no API key). They issue
// about a dozen requests, back off on rate limiting and skip only when the
// network is unreachable.
//
//	go test -tags live ./internal/chain/tron/...

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"cryptopay/internal/address"
	"cryptopay/internal/catalog"
	"cryptopay/internal/chain"
	"cryptopay/internal/domain"
)

func catalogChain(t *testing.T, id string) domain.Chain {
	t.Helper()
	for _, c := range catalog.Chains() {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("chain %q not in catalog", id)
	return domain.Chain{}
}

// rawPost is an independent minimal client used to discover expected data.
// It retries on 429/5xx/"frequency limit" with backoff.
func rawPost(ctx context.Context, base, path, body string) ([]byte, error) {
	var lastErr error
	for attempt := range 6 {
		if attempt > 0 {
			select {
			case <-time.After(time.Duration(1<<attempt) * 500 * time.Millisecond):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode == 429 || resp.StatusCode >= 500 || bytes.Contains(data, []byte("frequency limit")) {
			lastErr = fmt.Errorf("%s: http %d: %.200s", path, resp.StatusCode, data)
			continue
		}
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("%s: http %d: %.200s", path, resp.StatusCode, data)
		}
		return data, nil
	}
	return nil, lastErr
}

func skipIfOffline(t *testing.T, base string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err := rawPost(ctx, base, "/wallet/getblock", `{"detail":false}`)
	var netErr net.Error
	var dnsErr *net.DNSError
	var opErr *net.OpError
	if err != nil && (errors.As(err, &netErr) || errors.As(err, &dnsErr) || errors.As(err, &opErr)) {
		t.Skipf("network unavailable: %v", err)
	}
	if err != nil {
		t.Fatalf("probe %s: %v", base, err)
	}
}

func liveClient(t *testing.T, id string) *Client {
	t.Helper()
	c, err := New(catalogChain(t, id), WithConcurrency(2), WithRetries(4), WithBackoff(time.Second, 8*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

type expTransfer struct {
	tx     string
	idx    int
	to     string
	token  string
	amount *big.Int
}

// discover parses a raw block and its tx infos with plain maps (independently
// of the adapter's decoding) and returns the first successful TRX transfer
// and the first successful USDT Transfer event of the block, plus every
// expected transfer to their recipients.
func discover(t *testing.T, block, infos []byte) (trx, usdt *expTransfer, all func(to ...string) []expTransfer) {
	t.Helper()
	var b struct {
		Transactions []struct {
			TxID string `json:"txID"`
			Ret  []struct {
				ContractRet string `json:"contractRet"`
			} `json:"ret"`
			RawData struct {
				Contract []struct {
					Type      string `json:"type"`
					Parameter struct {
						Value map[string]any `json:"value"`
					} `json:"parameter"`
				} `json:"contract"`
			} `json:"raw_data"`
		} `json:"transactions"`
	}
	var is []map[string]any
	dec := json.NewDecoder(bytes.NewReader(block))
	dec.UseNumber() // exact int64 amounts
	if err := dec.Decode(&b); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(infos, &is); err != nil {
		t.Fatal(err)
	}
	var cands []expTransfer
	for _, tx := range b.Transactions {
		if len(tx.Ret) == 0 || tx.Ret[0].ContractRet != "SUCCESS" || len(tx.RawData.Contract) != 1 ||
			tx.RawData.Contract[0].Type != "TransferContract" {
			continue
		}
		v := tx.RawData.Contract[0].Parameter.Value
		n, _ := v["amount"].(json.Number)
		a, ok := new(big.Int).SetString(n.String(), 10)
		if !ok || a.Sign() <= 0 {
			continue
		}
		to, err := address.ParseTron(v["to_address"].(string))
		if err != nil {
			t.Fatal(err)
		}
		cands = append(cands, expTransfer{tx: tx.TxID, idx: -1, to: to, amount: a})
	}
	for _, inf := range is {
		rcpt, _ := inf["receipt"].(map[string]any)
		if inf["result"] != nil || rcpt == nil || rcpt["result"] != "SUCCESS" {
			continue
		}
		logs, _ := inf["log"].([]any)
		for i, l := range logs {
			lg := l.(map[string]any)
			topics, _ := lg["topics"].([]any)
			if lg["address"] != usdtHex || len(topics) != 3 || topics[0] != transferTopic {
				continue
			}
			raw, _ := hex.DecodeString(lg["data"].(string))
			v := new(big.Int).SetBytes(raw)
			if v.Sign() == 0 || len(raw) != 32 {
				continue
			}
			to, err := address.ParseTron(topics[2].(string)[24:])
			if err != nil {
				t.Fatal(err)
			}
			cands = append(cands, expTransfer{tx: inf["id"].(string), idx: i, to: to, token: usdtB58, amount: v})
		}
	}
	for i := range cands {
		c := &cands[i]
		if c.token == "" && trx == nil {
			trx = c
		}
		if c.token != "" && usdt == nil {
			usdt = c
		}
	}
	return trx, usdt, func(to ...string) []expTransfer {
		var out []expTransfer
		for _, c := range cands {
			for _, a := range to {
				if c.to == a {
					out = append(out, c)
				}
			}
		}
		return out
	}
}

func TestLiveTronMainnet(t *testing.T) {
	const base = "https://api.trongrid.io"
	skipIfOffline(t, base)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	c := liveClient(t, "tron")

	head, err := c.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if head < 80_000_000 {
		t.Fatalf("head = %d, implausibly low", head)
	}
	t.Logf("mainnet head %d", head)

	// A block ~20 behind head (≈1 min, solidified): no reorg risk, indexed everywhere.
	var (
		num             uint64
		blockJSON       []byte
		trx, usdt       *expTransfer
		expectedFor     func(to ...string) []expTransfer
		rawBlockID      string
		rawBlockTimeMil int64
	)
	for n := head - 20; n > head-25; n-- {
		blk, err := rawPost(ctx, base, "/wallet/getblockbynum", fmt.Sprintf(`{"num":%d}`, n))
		if err != nil {
			t.Fatal(err)
		}
		infos, err := rawPost(ctx, base, "/wallet/gettransactioninfobyblocknum", fmt.Sprintf(`{"num":%d}`, n))
		if err != nil {
			t.Fatal(err)
		}
		trx, usdt, expectedFor = discover(t, blk, infos)
		if trx != nil && usdt != nil {
			num, blockJSON = n, blk
			break
		}
		t.Logf("block %d has no TRX+USDT pair, trying the previous one", n)
	}
	if num == 0 {
		t.Fatal("no block with both a TRX and a USDT transfer found")
	}
	var hdr struct {
		BlockID     string `json:"blockID"`
		BlockHeader struct {
			RawData struct {
				Timestamp int64 `json:"timestamp"`
			} `json:"raw_data"`
		} `json:"block_header"`
	}
	if err := json.Unmarshal(blockJSON, &hdr); err != nil {
		t.Fatal(err)
	}
	rawBlockID, rawBlockTimeMil = hdr.BlockID, hdr.BlockHeader.RawData.Timestamp
	t.Logf("block %d: TRX %s -> %s %s sun; USDT %s#%d -> %s %s", num, trx.tx, trx.to, trx.amount, usdt.tx, usdt.idx, usdt.to, usdt.amount)

	watched := []string{trx.to, usdt.to}
	got, err := c.ScanTransfers(ctx, chain.ScanRequest{
		FromBlock: num, ToBlock: num, Addresses: watched, Tokens: []string{usdtB58}, Native: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Exactly the transfers the independent parse expects to these recipients.
	want := expectedFor(watched...)
	if len(got) != len(want) {
		t.Fatalf("got %d transfers, want %d:\n got %+v\nwant %+v", len(got), len(want), got, want)
	}
	byKey := map[string]chain.Transfer{}
	for _, g := range got {
		if g.BlockNumber != num || g.BlockHash != rawBlockID || !g.BlockTime.Equal(time.UnixMilli(rawBlockTimeMil)) {
			t.Errorf("block fields of %+v", g)
		}
		byKey[fmt.Sprintf("%s#%d", g.TxHash, g.Index)] = g
	}
	for _, w := range want {
		g, ok := byKey[fmt.Sprintf("%s#%d", w.tx, w.idx)]
		if !ok {
			t.Errorf("missing %+v", w)
			continue
		}
		if g.To != w.to || g.Token != w.token || g.Amount.Cmp(w.amount) != 0 {
			t.Errorf("got %+v, want %+v", g, w)
		}
	}

	for _, w := range []*expTransfer{trx, usdt} {
		g := byKey[fmt.Sprintf("%s#%d", w.tx, w.idx)]
		ok, err := c.VerifyTransfer(ctx, g)
		if err != nil || !ok {
			t.Errorf("verify %s#%d = %v, %v; want true", w.tx, w.idx, ok, err)
		}
	}
	// A transfer claimed in a different block hash is reported as reorged.
	g := byKey[fmt.Sprintf("%s#%d", trx.tx, trx.idx)]
	g.BlockHash = strings.Repeat("0", 16) + g.BlockHash[16:32] + strings.Repeat("f", 32)
	if ok, err := c.VerifyTransfer(ctx, g); err != nil || ok {
		t.Errorf("verify with wrong block hash = %v, %v; want false", ok, err)
	}
}

func TestLiveTronNileHead(t *testing.T) {
	const base = "https://nile.trongrid.io"
	skipIfOffline(t, base)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	head, err := liveClient(t, "tron-nile").Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if head < 60_000_000 {
		t.Fatalf("nile head = %d, implausibly low", head)
	}
	t.Logf("nile head %d", head)
}
