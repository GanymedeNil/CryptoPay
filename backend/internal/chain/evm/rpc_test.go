package evm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"cryptopay/internal/chain"
)

// countingHandler counts requests before delegating.
type countingHandler struct {
	n atomic.Int64
	h http.Handler
}

func (c *countingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.n.Add(1)
	c.h.ServeHTTP(w, r)
}

func status(code int, body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	})
}

func TestFailover(t *testing.T) {
	closedURL := func(t *testing.T) string {
		srv, u := newRPCServer(t, status(200, ""))
		srv.Close()
		return u
	}
	wrongChain := newFakeNode(1)
	wrongChain.chainID = 1
	cases := map[string]struct {
		primary http.Handler // nil: closed server (transport error)
		opts    []Option
	}{
		"http 503":         {primary: status(503, "upstream down")},
		"http 502 json":    {primary: status(502, `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"bad gateway"}}`)},
		"http 429":         {primary: status(429, "slow down")},
		"http 401":         {primary: status(401, "invalid api key")},
		"html 200":         {primary: status(200, "<html>oops</html>")},
		"empty 200":        {primary: status(200, "")},
		"transport error":  {primary: nil},
		"wrong chain id":   {primary: wrongChain},
		"rate limit error": {primary: status(200, `{"jsonrpc":"2.0","id":1,"error":{"code":-32005,"message":"project ID request rate exceeded"}}`)},
		"timeout": {
			primary: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body) // lets net/http notice the client hanging up
				<-r.Context().Done()
			}),
			opts: []Option{WithRequestTimeout(150 * time.Millisecond)},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var primaryURL string
			primary := &countingHandler{h: tc.primary}
			if tc.primary == nil {
				primaryURL = closedURL(t)
			} else {
				_, primaryURL = newRPCServer(t, primary)
			}
			good := newFakeNode(777)
			_, goodURL := newRPCServer(t, good)
			c, err := New(testChain(primaryURL, goodURL), fastOpts(tc.opts...)...)
			require.NoError(t, err)

			h, err := c.Head(context.Background())
			require.NoError(t, err)
			require.Equal(t, uint64(777), h)
			if tc.primary != nil {
				require.Positive(t, primary.n.Load())
			}

			// The failed primary cools down: the next call goes straight to the backup.
			before := primary.n.Load()
			_, err = c.Head(context.Background())
			require.NoError(t, err)
			require.Equal(t, before, primary.n.Load())
		})
	}
}

func TestFailoverCooldownExpires(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	pnode := newFakeNode(1)
	primary := &countingHandler{h: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			w.WriteHeader(503)
			return
		}
		pnode.ServeHTTP(w, r)
	})}
	_, pu := newRPCServer(t, primary)
	_, su := newRPCServer(t, newFakeNode(2))
	c, err := New(testChain(pu, su), fastOpts(WithEndpointCooldown(50*time.Millisecond))...)
	require.NoError(t, err)
	h, err := c.Head(context.Background())
	require.NoError(t, err)
	require.Equal(t, uint64(2), h)
	fail.Store(false)
	time.Sleep(100 * time.Millisecond)
	h, err = c.Head(context.Background())
	require.NoError(t, err)
	require.Equal(t, uint64(1), h, "primary is preferred again after the cooldown")
}

func TestRetriesAreBounded(t *testing.T) {
	h := &countingHandler{h: status(503, "")}
	_, u := newRPCServer(t, h)
	c, err := New(testChain(u), fastOpts(WithRetries(2))...)
	require.NoError(t, err)
	_, err = c.Head(context.Background())
	requireNoSecret(t, err)
	require.ErrorContains(t, err, "http 503")
	require.ErrorContains(t, err, "gave up after 3 attempts")
	require.Equal(t, int64(3), h.n.Load())

	// Transient failures are absorbed.
	var n atomic.Int64
	f := newFakeNode(9)
	f.httpHook = func(w http.ResponseWriter, _ *http.Request, _ []byte) bool {
		if n.Add(1) <= 2 {
			w.WriteHeader(502)
			return true
		}
		return false
	}
	got, err := newTestClient(t, f).Head(context.Background())
	require.NoError(t, err)
	require.Equal(t, uint64(9), got)
}

func TestNonRetryableHTTPErrors(t *testing.T) {
	h := &countingHandler{h: status(400, "bad request")}
	_, u := newRPCServer(t, h)
	c, err := New(testChain(u), fastOpts(WithRetries(3))...)
	require.NoError(t, err)
	_, err = c.Head(context.Background())
	require.ErrorContains(t, err, "http 400")
	require.Equal(t, int64(1), h.n.Load())

	// A JSON-RPC error carried by a 4xx status is interpreted normally.
	f := erc20Fixture()
	f.httpHook = func(w http.ResponseWriter, _ *http.Request, b []byte) bool {
		if !strings.Contains(string(b), "eth_getLogs") {
			return false
		}
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32602,"message":"block range too large"}}`))
		return true
	}
	_, err = newTestClient(t, f, WithBatchSize(1)).ScanTransfers(context.Background(), scanReq(100, 105, false, []string{eip(tokUSDT)}, watchA))
	require.ErrorIs(t, err, chain.ErrRangeTooLarge)
}

func TestRateLimited(t *testing.T) {
	t.Run("http 429", func(t *testing.T) {
		h := &countingHandler{h: status(429, "Too Many Requests")}
		_, u := newRPCServer(t, h)
		c, err := New(testChain(u), fastOpts(WithRetries(1))...)
		require.NoError(t, err)
		_, err = c.Head(context.Background())
		require.ErrorIs(t, err, chain.ErrRateLimited)
		requireNoSecret(t, err)
		require.Equal(t, int64(2), h.n.Load())
	})
	t.Run("403 with quota text", func(t *testing.T) {
		_, u := newRPCServer(t, status(403, "daily quota exceeded for this key"))
		c, err := New(testChain(u), fastOpts()...)
		require.NoError(t, err)
		_, err = c.Head(context.Background())
		require.ErrorIs(t, err, chain.ErrRateLimited)
	})
	for name, e := range map[string]*rpcErrorObject{
		"infura":      {Code: -32005, Message: "daily request count exceeded, request rate limited"},
		"alchemy":     {Code: 429, Message: "Your app has exceeded its compute units per second capacity."},
		"quicknode":   {Code: -32007, Message: "15/second request limit reached - reduce calls per second"},
		"generic":     {Code: -32000, Message: "Too Many Requests"},
		"code -32029": {Code: -32029, Message: "slow down"},
	} {
		t.Run(name, func(t *testing.T) {
			f := erc20Fixture()
			f.hook = func(m string, _ []json.RawMessage) (any, *rpcErrorObject, bool) {
				return nil, e, m == "eth_getLogs"
			}
			_, err := newTestClient(t, f).ScanTransfers(context.Background(), scanReq(100, 105, false, []string{eip(tokUSDT)}, watchA))
			require.ErrorIs(t, err, chain.ErrRateLimited)
			require.NotErrorIs(t, err, chain.ErrRangeTooLarge)
			var rerr *RPCError
			require.ErrorAs(t, err, &rerr)
			require.Equal(t, e.Code, rerr.Code)
			require.Equal(t, 3, f.callCount("eth_getLogs"), "rate limits are retried")
		})
	}
	t.Run("recovers", func(t *testing.T) {
		f := erc20Fixture()
		var n atomic.Int64
		f.hook = func(m string, _ []json.RawMessage) (any, *rpcErrorObject, bool) {
			if m == "eth_getLogs" && n.Add(1) == 1 {
				return nil, &rpcErrorObject{Code: 429, Message: "rate limited"}, true
			}
			return nil, nil, false
		}
		got, err := newTestClient(t, f).ScanTransfers(context.Background(), scanReq(100, 105, false, []string{eip(tokUSDT), eip(tokUSDC)}, watchA, watchB))
		require.NoError(t, err)
		require.Equal(t, erc20Expected(f), got)
	})
}

func TestRangeTooLarge(t *testing.T) {
	rangeErrs := []rpcErrorObject{
		{Code: -32005, Message: "query returned more than 10000 results. Try with this block range [0x1, 0x2]."},
		{Code: -32005, Message: "limit exceeded"},
		{Code: -32602, Message: "Log response size exceeded. You can make eth_getLogs requests with up to a 2K block range and no limit on the response size"},
		{Code: -32000, Message: "exceed maximum block range: 5000"},
		{Code: -32602, Message: "eth_getLogs is limited to a 10,000 range"},
		{Code: -32614, Message: "eth_getLogs is limited to a 10000 range"},
		{Code: -32000, Message: "block range is too wide"},
		{Code: -32600, Message: "Exceeded max range limit for eth_getLogs: 800"},
		{Code: -32000, Message: "ranges over 10000 blocks are not supported on freetier"},
		{Code: -32602, Message: "query exceeds max block range 100000"},
		{Code: -32000, Message: "query timeout exceeded"},
		{Code: -32602, Message: "invalid params", Data: json.RawMessage(`"block range too large"`)},
	}
	for _, e := range rangeErrs {
		t.Run(e.Message, func(t *testing.T) {
			f := erc20Fixture()
			f.hook = func(m string, _ []json.RawMessage) (any, *rpcErrorObject, bool) {
				return nil, &e, m == "eth_getLogs"
			}
			_, err := newTestClient(t, f).ScanTransfers(context.Background(), scanReq(100, 105, false, []string{eip(tokUSDT)}, watchA))
			require.ErrorIs(t, err, chain.ErrRangeTooLarge)
			require.NotErrorIs(t, err, chain.ErrRateLimited)
			require.Equal(t, 1, f.callCount("eth_getLogs"), "range errors are not retried")
		})
	}
	t.Run("other errors are not range errors", func(t *testing.T) {
		f := erc20Fixture()
		f.hook = func(m string, _ []json.RawMessage) (any, *rpcErrorObject, bool) {
			return nil, &rpcErrorObject{Code: -32602, Message: "invalid argument 0: hex string has odd length"}, m == "eth_getLogs"
		}
		_, err := newTestClient(t, f).ScanTransfers(context.Background(), scanReq(100, 105, false, []string{eip(tokUSDT)}, watchA))
		require.Error(t, err)
		require.NotErrorIs(t, err, chain.ErrRangeTooLarge)
	})
	t.Run("only eth_getLogs has a range", func(t *testing.T) {
		f := nativeFixture()
		f.hook = func(m string, _ []json.RawMessage) (any, *rpcErrorObject, bool) {
			return nil, &rpcErrorObject{Code: -32005, Message: "limit exceeded"}, m == "eth_getBlockByNumber"
		}
		_, err := newTestClient(t, f).ScanTransfers(context.Background(), scanReq(200, 202, true, nil, watchA))
		require.Error(t, err)
		require.NotErrorIs(t, err, chain.ErrRangeTooLarge)
	})
}

func TestClassify(t *testing.T) {
	ep, err := newEndpoint("https://rpc.example.com/v2/"+testSecret, 0)
	require.NoError(t, err)
	type want int
	const (
		fatal want = iota
		retry
		rateLimited
		tooLarge
	)
	for _, tc := range []struct {
		method string
		e      rpcErrorObject
		want   want
	}{
		{"eth_getLogs", rpcErrorObject{Code: -32005, Message: "query returned more than 10000 results"}, tooLarge},
		{"eth_getLogs", rpcErrorObject{Code: -32005, Message: "project ID request rate exceeded"}, rateLimited},
		{"eth_getBlockByNumber", rpcErrorObject{Code: -32005, Message: "limit exceeded"}, fatal},
		{"eth_getBlockByNumber", rpcErrorObject{Code: -32603, Message: "internal error"}, retry},
		{"eth_getLogs", rpcErrorObject{Code: -32000, Message: "header not found"}, retry},
		{"eth_blockNumber", rpcErrorObject{Code: -32000, Message: "request timed out"}, retry},
		{"eth_blockNumber", rpcErrorObject{Code: -32601, Message: "method not found"}, fatal},
		{"eth_blockNumber", rpcErrorObject{Code: 429, Message: ""}, rateLimited},
	} {
		r, err := classify(ep, tc.method, &tc.e, tc.method)
		var got want
		switch {
		case errors.Is(err, chain.ErrRateLimited):
			got = rateLimited
			require.True(t, r)
		case errors.Is(err, chain.ErrRangeTooLarge):
			got = tooLarge
			require.False(t, r)
		case r:
			got = retry
		default:
			got = fatal
		}
		require.Equal(t, tc.want, got, "%s %+v", tc.method, tc.e)
		require.Contains(t, err.Error(), "rpc.example.com")
		require.NotContains(t, err.Error(), testSecret)
	}
}

func TestBatchResponseProblems(t *testing.T) {
	t.Run("missing entry", func(t *testing.T) {
		f := nativeFixture()
		f.httpHook = func(w http.ResponseWriter, _ *http.Request, b []byte) bool {
			if !bytes.HasPrefix(bytes.TrimSpace(b), []byte("[")) {
				return false
			}
			var reqs []fakeReq
			_ = json.Unmarshal(b, &reqs)
			writeJSON(w, []any{f.respond(reqs[0])}) // drop the rest
			return true
		}
		_, err := newTestClient(t, f).ScanTransfers(context.Background(), scanReq(200, 202, true, nil, watchA))
		require.ErrorContains(t, err, "no entry")
	})
	t.Run("single object answering a batch", func(t *testing.T) {
		f := nativeFixture()
		f.httpHook = func(w http.ResponseWriter, _ *http.Request, b []byte) bool {
			if !bytes.HasPrefix(bytes.TrimSpace(b), []byte("[")) {
				return false
			}
			writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": 1, "result": "0x1"})
			return true
		}
		_, err := newTestClient(t, f).ScanTransfers(context.Background(), scanReq(200, 202, true, nil, watchA))
		require.Error(t, err)
	})
	t.Run("batch rejected with rate limit is retried, not split", func(t *testing.T) {
		f := nativeFixture()
		var n atomic.Int64
		f.httpHook = func(w http.ResponseWriter, _ *http.Request, b []byte) bool {
			if bytes.HasPrefix(bytes.TrimSpace(b), []byte("[")) && n.Add(1) == 1 {
				writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": nil, "error": map[string]any{"code": -32005, "message": "rate limit exceeded"}})
				return true
			}
			return false
		}
		got, err := newTestClient(t, f).ScanTransfers(context.Background(), scanReq(200, 202, true, nil, watchA, watchB))
		require.NoError(t, err)
		require.Equal(t, nativeExpected(f), got)
	})
	t.Run("string ids and unknown fields", func(t *testing.T) {
		f := newFakeNode(5)
		f.httpHook = func(w http.ResponseWriter, _ *http.Request, b []byte) bool {
			var rq fakeReq
			_ = json.Unmarshal(b, &rq)
			res := f.respond(rq)
			res["id"] = string(rq.ID) // 1 -> "1"
			res["extra"] = map[string]any{"x": 1}
			writeJSON(w, res)
			return true
		}
		h, err := newTestClient(t, f).Head(context.Background())
		require.NoError(t, err)
		require.Equal(t, uint64(5), h)
	})
}

func TestResponseSizeLimit(t *testing.T) {
	f := nativeFixture()
	c := newTestClient(t, f, WithMaxResponseSize(512))
	_, err := c.ScanTransfers(context.Background(), scanReq(200, 202, true, nil, watchA))
	require.ErrorContains(t, err, "exceeds size limit")
}

func TestChainIDCheck(t *testing.T) {
	t.Run("mismatch", func(t *testing.T) {
		f := newFakeNode(5)
		f.chainID = 56
		c := newTestClient(t, f)
		_, err := c.Head(context.Background())
		require.ErrorContains(t, err, "chain id 56, want 1337")
		require.NotErrorIs(t, err, chain.ErrRateLimited)
		require.Zero(t, f.callCount("eth_blockNumber"))
	})
	t.Run("disabled", func(t *testing.T) {
		f := newFakeNode(5)
		f.chainID = 56
		h, err := newTestClient(t, f, WithChainIDCheck(false)).Head(context.Background())
		require.NoError(t, err)
		require.Equal(t, uint64(5), h)
		require.Zero(t, f.callCount("eth_chainId"))
	})
	t.Run("no network id", func(t *testing.T) {
		f := newFakeNode(5)
		_, u := newRPCServer(t, f)
		ch := testChain(u)
		ch.NetworkID = ""
		c, err := New(ch, fastOpts()...)
		require.NoError(t, err)
		_, err = c.Head(context.Background())
		require.NoError(t, err)
		require.Zero(t, f.callCount("eth_chainId"))
	})
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestSecretsNeverLeak(t *testing.T) {
	var logs syncBuffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	srv, closed := newRPCServer(t, status(200, ""))
	srv.Close()
	echo := newFakeNode(1)
	echo.hook = func(m string, _ []json.RawMessage) (any, *rpcErrorObject, bool) {
		return nil, &rpcErrorObject{Code: -32000, Message: "invalid api key " + testSecret}, m == "eth_blockNumber"
	}
	_, echoURL := newRPCServer(t, echo)
	_, errURL := newRPCServer(t, status(500, "internal error for key "+testSecret))

	for _, u := range []string{closed, echoURL, errURL} {
		c, err := New(testChain(u), fastOpts(WithLogger(logger))...)
		require.NoError(t, err)
		_, err = c.Head(context.Background())
		requireNoSecret(t, err)
		require.Contains(t, err.Error(), "127.0.0.1:") // the host is kept for diagnosis
	}
	require.NotEmpty(t, logs.String())
	require.NotContains(t, logs.String(), testSecret)
	require.NotContains(t, logs.String(), "/v2/")
}

func TestEndpointRedaction(t *testing.T) {
	ep, err := newEndpoint("https://user:p4ssw0rd99@rpc.example.com:8545/v3/abcdef123456?key=zzzzzz999&x=1", 0)
	require.NoError(t, err)
	require.Equal(t, "rpc.example.com:8545", ep.host)
	got := ep.redact("bad key abcdef123456 zzzzzz999 p4ssw0rd99 user v3")
	require.Equal(t, "bad key *** *** *** user v3", got)
	require.LessOrEqual(t, len(ep.redact(strings.Repeat("x", 5000))), 310)
}

func TestBackoff(t *testing.T) {
	r := &rpcClient{cfg: &config{backoffBase: 100 * time.Millisecond, backoffMax: time.Second}}
	for range 50 {
		d := r.backoff(1)
		require.GreaterOrEqual(t, d, 50*time.Millisecond)
		require.Less(t, d, 100*time.Millisecond)
		d = r.backoff(30)
		require.GreaterOrEqual(t, d, 500*time.Millisecond)
		require.Less(t, d, time.Second)
	}
	zero := &rpcClient{cfg: &config{}}
	require.Zero(t, zero.backoff(3))
}

func TestParallel(t *testing.T) {
	require.NoError(t, parallel(context.Background(), 0, 4, nil))

	var cur, peak atomic.Int64
	err := parallel(context.Background(), 20, 3, func(ctx context.Context, i int) error {
		n := cur.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		cur.Add(-1)
		return nil
	})
	require.NoError(t, err)
	require.LessOrEqual(t, peak.Load(), int64(3))

	boom := errors.New("boom")
	var started atomic.Int64
	err = parallel(context.Background(), 50, 2, func(ctx context.Context, i int) error {
		started.Add(1)
		if i == 1 {
			return boom
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
			return nil
		}
	})
	require.ErrorIs(t, err, boom)
	require.Less(t, started.Load(), int64(50), "remaining work is cancelled")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = parallel(ctx, 5, 2, func(ctx context.Context, i int) error { return ctx.Err() })
	require.ErrorIs(t, err, context.Canceled)
}

func TestHexParsers(t *testing.T) {
	for in, want := range map[string]uint64{"0x0": 0, "0x1b4": 436, "0X10": 16, "0x00ff": 255, "0xffffffffffffffff": 1<<64 - 1} {
		got, err := parseUint64(in)
		require.NoError(t, err, in)
		require.Equal(t, want, got, in)
	}
	for _, in := range []string{"", "0x", "12", "0x-1", "0x+1", "0xg", "0x1_0", "0x10000000000000000", " 0x1", "0x" + strings.Repeat("0", 65)} {
		_, err := parseUint64(in)
		require.Error(t, err, in)
	}

	v, err := parseBig("0x" + strings.Repeat("f", 64))
	require.NoError(t, err)
	require.Equal(t, 256, v.BitLen())
	for _, in := range []string{"0x", "-0x1", "0x" + strings.Repeat("f", 65), "0x1.5", "1"} {
		_, err := parseBig(in)
		require.Error(t, err, in)
	}
	v, err = parseBig("0xde0b6b3a7640000")
	require.NoError(t, err)
	require.Zero(t, v.Cmp(big.NewInt(1e18)))

	h, err := parseHash("0x" + strings.Repeat("AB", 32))
	require.NoError(t, err)
	require.Equal(t, "0x"+strings.Repeat("ab", 32), h)
	for _, in := range []string{"", "0x" + strings.Repeat("a", 63), "0x" + strings.Repeat("z", 64), strings.Repeat("a", 66)} {
		_, err := parseHash(in)
		require.Error(t, err, in)
	}

	d, err := parseData("0x")
	require.NoError(t, err)
	require.Empty(t, d)
	for _, in := range []string{"", "0x1", "zz", "0xgg"} {
		_, err := parseData(in)
		require.Error(t, err, in)
	}

	a, err := parseAddr("0x" + strings.Repeat("A", 40))
	require.NoError(t, err)
	require.Equal(t, "0x"+strings.Repeat("a", 40), addrHex(a))
	require.Equal(t, "0x000000000000000000000000"+strings.Repeat("a", 40), addrTopic(a))
	_, err = parseAddr("0x" + strings.Repeat("a", 39))
	require.Error(t, err)

	require.Equal(t, "0x0", formatUint64(0))
	require.Equal(t, "0x1b4", formatUint64(436))

	for raw, want := range map[string]uint64{`1`: 1, `"7"`: 7, ` 42 `: 42} {
		got, ok := parseID(json.RawMessage(raw))
		require.True(t, ok, raw)
		require.Equal(t, want, got)
	}
	for _, raw := range []string{``, `null`, `"x"`, `-1`} {
		_, ok := parseID(json.RawMessage(raw))
		require.False(t, ok, raw)
	}
}
