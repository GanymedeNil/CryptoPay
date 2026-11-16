package utxo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cryptopay/internal/chain"
)

// closedURL returns the URL of a server that is no longer listening.
func closedURL(t *testing.T, pathAndQuery string) string {
	t.Helper()
	srv := httptest.NewServer(http.NotFoundHandler())
	u := srv.URL
	srv.Close()
	return u + pathAndQuery
}

func TestRPCErrorObject(t *testing.T) {
	for _, legacy := range []bool{true, false} {
		t.Run(fmt.Sprintf("legacy=%v", legacy), func(t *testing.T) {
			f := newFakeNode(t)
			f.legacyErrors = legacy // HTTP 500 (JSON-RPC 1.0) or 200 (2.0)
			cl := newTestClient(t, testChain(f.URL()))

			_, err := cl.ScanTransfers(context.Background(), chain.ScanRequest{FromBlock: 9, ToBlock: 9, Native: true, Addresses: []string{"x"}})
			var rpcErr *RPCError
			if !errors.As(err, &rpcErr) {
				t.Fatalf("error %v does not wrap *RPCError", err)
			}
			if rpcErr.Code != -8 || rpcErr.Message != "Block height out of range" {
				t.Errorf("RPCError = %+v", rpcErr)
			}
			if n := f.count("getblockhash"); n != 1 {
				t.Errorf("deterministic RPC error retried: %d calls", n)
			}
			if errors.Is(err, chain.ErrRateLimited) {
				t.Error("RPC error mapped to ErrRateLimited")
			}
		})
	}
}

func TestRPCErrorShapes(t *testing.T) {
	cases := map[string]string{
		`{"result":null,"error":"plain string error","id":1}`:                           "plain string error",
		`{"error":{"code":-5,"message":"Block not found"},"result":null}`:               "Block not found",
		`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"Method not found"}}`: "-32601",
	}
	for body, wantMsg := range cases {
		f := newFakeNode(t)
		f.intercept = func(w http.ResponseWriter, _ *http.Request, _ rpcCall) bool {
			fmt.Fprint(w, body)
			return true
		}
		cl := newTestClient(t, testChain(f.URL()))
		_, err := cl.Head(context.Background())
		var rpcErr *RPCError
		if !errors.As(err, &rpcErr) || !strings.Contains(err.Error(), wantMsg) {
			t.Errorf("body %s: error %v, want RPCError mentioning %q", body, err, wantMsg)
		}
		if n := f.count(""); n != 1 {
			t.Errorf("body %s: %d calls, want 1", body, n)
		}
	}
}

func TestGatewayErrors(t *testing.T) {
	t.Run("server error code is retried", func(t *testing.T) {
		f := newFakeNode(t)
		f.intercept = func(w http.ResponseWriter, _ *http.Request, c rpcCall) bool {
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32000,"message":"upstream request failed"}}`, c.ID)
			return true
		}
		cl := newTestClient(t, testChain(f.URL()))
		_, err := cl.Head(context.Background())
		var rpcErr *RPCError
		if !errors.As(err, &rpcErr) || rpcErr.Code != -32000 {
			t.Fatalf("error = %v, want RPCError -32000", err)
		}
		if n := f.count(""); n != 3 {
			t.Errorf("calls = %d, want 3", n)
		}
	})
	t.Run("403 with json-rpc body fails over", func(t *testing.T) {
		keyless := newFakeNode(t)
		keyless.intercept = func(w http.ResponseWriter, _ *http.Request, _ rpcCall) bool {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"error":"message: API key is not allowed to access blockchain, json-rpc code: -32052, rest code: 403"}`)
			return true
		}
		good := newFakeNode(t)
		good.head = 4
		cl := newTestClient(t, testChain(keyless.URL(), good.URL()))
		if h, err := cl.Head(context.Background()); err != nil || h != 4 {
			t.Fatalf("Head = %d, %v; want 4 from the second endpoint", h, err)
		}
		if keyless.count("") != 1 {
			t.Errorf("forbidden endpoint called %d times", keyless.count(""))
		}
	})
}

func TestWarmupRetried(t *testing.T) {
	f := newFakeNode(t)
	f.head = 7
	var n atomic.Int32
	f.intercept = func(w http.ResponseWriter, _ *http.Request, c rpcCall) bool {
		if n.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, `{"result":null,"error":{"code":-28,"message":"Loading block index..."},"id":%s}`, c.ID)
			return true
		}
		return false
	}
	cl := newTestClient(t, testChain(f.URL()))
	if h, err := cl.Head(context.Background()); err != nil || h != 7 {
		t.Fatalf("Head = %d, %v; want 7 after warm-up retry", h, err)
	}
}

func TestFailover(t *testing.T) {
	down := closedURL(t, "/")
	broken := newFakeNode(t)
	broken.intercept = func(w http.ResponseWriter, _ *http.Request, _ rpcCall) bool {
		http.Error(w, "upstream unavailable", http.StatusServiceUnavailable)
		return true
	}
	good := newFakeNode(t)
	good.head = 123

	cl := newTestClient(t, testChain(down, broken.URL(), good.URL()))
	h, err := cl.Head(context.Background())
	if err != nil || h != 123 {
		t.Fatalf("Head = %d, %v; want 123 from the third endpoint", h, err)
	}
	if broken.count("") != 1 || good.count("") != 1 {
		t.Errorf("calls: broken %d, good %d; want 1 each", broken.count(""), good.count(""))
	}
}

// A block that breaks off mid-stream on the primary is re-read in full from
// the failover endpoint, without duplicating the outputs decoded the first time.
func TestFailoverAfterPartialBlock(t *testing.T) {
	const (
		height = 976080
		hash   = "00000000000000000001f5d21eb2b91d6d53fe51a700ff175202da9e98142ad6"
		addr   = "bc1qq2mvrp4g3ugd424dw4xv53rgsf8szkrv853jrc"
	)
	block := readFixture(t, "btc_block_976080.json")
	primary := newFakeNode(t)
	primary.addBlock(height, hash, block)
	primary.intercept = func(w http.ResponseWriter, _ *http.Request, c rpcCall) bool {
		if c.Method != "getblock" {
			return false
		}
		full := fmt.Sprintf(`{"result":%s,"error":null,"id":%s}`, block, c.ID)
		// Cut right after the last tx so every match has been decoded already.
		cut := strings.LastIndex(full, `]`)
		w.Header().Set("Content-Length", fmt.Sprint(len(full)))
		_, _ = io.WriteString(w, full[:cut])
		return true // the server closes the connection: short body
	}
	secondary := newFakeNode(t)
	secondary.addBlock(height, hash, block)

	cl := newTestClient(t, testChain(primary.URL(), secondary.URL()))
	got, err := cl.ScanTransfers(context.Background(), chain.ScanRequest{FromBlock: height, ToBlock: height, Native: true, Addresses: []string{addr}})
	if err != nil {
		t.Fatalf("ScanTransfers: %v", err)
	}
	if len(got) != 2 || got[0].Index != 0 || got[1].Index != 3 {
		t.Fatalf("got %+v, want exactly vouts 0 and 3 of b14e…", got)
	}
	if primary.count("getblock") != 1 || secondary.count("getblock") != 1 {
		t.Errorf("getblock calls: primary %d, secondary %d", primary.count("getblock"), secondary.count("getblock"))
	}
}

// 4xx other than 408/429 means the endpoint is misconfigured: it is not tried
// again in later rounds, while transient failures elsewhere are.
func TestFailoverSkipsMisconfiguredEndpoint(t *testing.T) {
	unauthorized := newFakeNode(t)
	unauthorized.intercept = func(w http.ResponseWriter, _ *http.Request, _ rpcCall) bool {
		w.WriteHeader(http.StatusUnauthorized)
		return true
	}
	flaky := newFakeNode(t)
	flaky.intercept = func(w http.ResponseWriter, _ *http.Request, _ rpcCall) bool {
		w.WriteHeader(http.StatusBadGateway)
		return true
	}
	cl := newTestClient(t, testChain(unauthorized.URL(), flaky.URL()))
	_, err := cl.Head(context.Background())
	if err == nil {
		t.Fatal("Head succeeded")
	}
	if !strings.Contains(err.Error(), "502") {
		t.Errorf("error %v should report the last failure", err)
	}
	if unauthorized.count("") != 1 || flaky.count("") != 3 {
		t.Errorf("calls: 401 endpoint %d (want 1), 502 endpoint %d (want 3)", unauthorized.count(""), flaky.count(""))
	}
}

func TestRateLimited(t *testing.T) {
	t.Run("http 429", func(t *testing.T) {
		f := newFakeNode(t)
		f.intercept = func(w http.ResponseWriter, _ *http.Request, _ rpcCall) bool {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"statusCode":429,"message":"You have exceeded your limit"}`)
			return true
		}
		cl := newTestClient(t, testChain(f.URL()))
		_, err := cl.Head(context.Background())
		if !errors.Is(err, chain.ErrRateLimited) {
			t.Fatalf("error %v does not wrap ErrRateLimited", err)
		}
		if n := f.count(""); n != 3 {
			t.Errorf("calls = %d, want 3 (retried)", n)
		}
	})
	t.Run("http 429 with json-rpc error", func(t *testing.T) {
		f := newFakeNode(t)
		f.intercept = func(w http.ResponseWriter, _ *http.Request, c rpcCall) bool {
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32000,"message":"slow down"}}`, c.ID)
			return true
		}
		cl := newTestClient(t, testChain(f.URL()))
		_, err := cl.Head(context.Background())
		if !errors.Is(err, chain.ErrRateLimited) {
			t.Fatalf("error %v does not wrap ErrRateLimited", err)
		}
		var rpcErr *RPCError
		if !errors.As(err, &rpcErr) || rpcErr.Message != "slow down" {
			t.Errorf("error %v should keep the provider message", err)
		}
	})
	t.Run("json-rpc rate limit code on 200", func(t *testing.T) {
		f := newFakeNode(t)
		f.intercept = func(w http.ResponseWriter, _ *http.Request, c rpcCall) bool {
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32005,"message":"limit exceeded"}}`, c.ID)
			return true
		}
		cl := newTestClient(t, testChain(f.URL()))
		if _, err := cl.Head(context.Background()); !errors.Is(err, chain.ErrRateLimited) {
			t.Fatalf("error %v does not wrap ErrRateLimited", err)
		}
	})
	t.Run("recovers", func(t *testing.T) {
		f := newFakeNode(t)
		f.head = 5
		var n atomic.Int32
		f.intercept = func(w http.ResponseWriter, _ *http.Request, _ rpcCall) bool {
			if n.Add(1) <= 2 {
				w.WriteHeader(http.StatusTooManyRequests)
				return true
			}
			return false
		}
		cl := newTestClient(t, testChain(f.URL()))
		if h, err := cl.Head(context.Background()); err != nil || h != 5 {
			t.Fatalf("Head = %d, %v; want 5", h, err)
		}
	})
	t.Run("flagged when another endpoint fails last", func(t *testing.T) {
		limited := newFakeNode(t)
		limited.intercept = func(w http.ResponseWriter, _ *http.Request, _ rpcCall) bool {
			w.WriteHeader(http.StatusTooManyRequests)
			return true
		}
		cl := newTestClient(t, testChain(limited.URL(), closedURL(t, "/")))
		if _, err := cl.Head(context.Background()); !errors.Is(err, chain.ErrRateLimited) {
			t.Fatalf("error %v does not wrap ErrRateLimited", err)
		}
	})
}

func TestBasicAuthAndURLPassthrough(t *testing.T) {
	f := newFakeNode(t)
	f.head = 1
	u, _ := url.Parse(f.URL())
	u.User = url.UserPassword("rpcuser", "p@ss:wörd/")
	u.Path = "/v1/secret-key-123/"
	u.RawQuery = "token=abc"
	cl := newTestClient(t, testChain(u.String()))

	if _, err := cl.Head(context.Background()); err != nil {
		t.Fatalf("Head: %v", err)
	}
	c := f.lastCall("getblockcount")
	if !c.hasAuth || c.user != "rpcuser" || c.pass != "p@ss:wörd/" {
		t.Errorf("basic auth = %v %q %q", c.hasAuth, c.user, c.pass)
	}
	if c.path != "/v1/secret-key-123/" || c.query != "token=abc" {
		t.Errorf("path/query = %q %q", c.path, c.query)
	}

	// Without userinfo no Authorization header is sent.
	f2 := newFakeNode(t)
	cl2 := newTestClient(t, testChain(f2.URL()))
	_, _ = cl2.Head(context.Background())
	if f2.lastCall("getblockcount").hasAuth {
		t.Error("Authorization sent without credentials")
	}
}

func TestErrorsAndLogsDoNotLeakSecrets(t *testing.T) {
	echo := newFakeNode(t)
	echo.intercept = func(w http.ResponseWriter, r *http.Request, _ rpcCall) bool {
		// A provider error page echoing the request path.
		http.Error(w, "bad gateway for "+r.URL.String(), http.StatusBadGateway)
		return true
	}
	eu, _ := url.Parse(echo.URL())
	eu.User = url.UserPassword("alice", "hunter2")
	eu.Path = "/apikey-123/"

	down := closedURL(t, "/apikey-456/?key=apikey-789")
	du, _ := url.Parse(down)
	du.User = url.UserPassword("bob", "hunter3")

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cl := newTestClient(t, testChain(eu.String(), du.String()), WithLogger(logger))

	_, err := cl.Head(context.Background())
	if err == nil {
		t.Fatal("Head succeeded")
	}
	for _, out := range []string{err.Error(), logs.String()} {
		for _, secret := range []string{"hunter2", "hunter3", "apikey-123", "apikey-456", "apikey-789", "alice", "bob"} {
			if strings.Contains(out, secret) {
				t.Errorf("%q leaked in %q", secret, out)
			}
		}
	}
	if !strings.Contains(err.Error(), du.Host) {
		t.Errorf("error %q should name the host %s", err, du.Host)
	}
	if !strings.Contains(logs.String(), eu.Host) {
		t.Errorf("logs should name the host %s: %s", eu.Host, logs.String())
	}
}

func TestContextCancelledMidStream(t *testing.T) {
	const addr = "bc1qrad6js2ypwel62292dvt6pgv3z0jl049u8epjw"
	hash := fakeHash(9)
	started := make(chan struct{})
	f := newFakeNode(t)
	f.hashes[9] = hash
	f.intercept = func(w http.ResponseWriter, r *http.Request, c rpcCall) bool {
		if c.Method != "getblock" {
			return false
		}
		// Stream the first transaction (a match), then stall.
		fmt.Fprintf(w, `{"result":{"hash":%q,"height":9,"tx":[%s,`, hash, synthTx(fakeTxid(1), vout(0, "1", addr, "witness_v0_keyhash")))
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
		return true
	}
	cl := newTestClient(t, testChain(f.URL()))

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-started
		cancel()
	}()
	begin := time.Now()
	got, err := cl.ScanTransfers(ctx, chain.ScanRequest{FromBlock: 9, ToBlock: 9, Native: true, Addresses: []string{addr}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if got != nil {
		t.Errorf("partial result %v", got)
	}
	if d := time.Since(begin); d > 3*time.Second {
		t.Errorf("cancellation took %s", d)
	}
	if n := f.count("getblock"); n != 1 {
		t.Errorf("getblock retried after cancellation: %d calls", n)
	}
}

func TestContextCancelledBeforeCall(t *testing.T) {
	f := newFakeNode(t)
	cl := newTestClient(t, testChain(f.URL()))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := cl.Head(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Head error = %v, want context.Canceled", err)
	}
	if _, err := cl.ScanTransfers(ctx, chain.ScanRequest{FromBlock: 1, ToBlock: 1, Native: true, Addresses: []string{"x"}}); !errors.Is(err, context.Canceled) {
		t.Errorf("ScanTransfers error = %v, want context.Canceled", err)
	}
	if n := f.count(""); n != 0 {
		t.Errorf("%d calls after cancellation", n)
	}
}

func TestPerAttemptTimeout(t *testing.T) {
	f := newFakeNode(t)
	f.head = 3
	var n atomic.Int32
	f.intercept = func(w http.ResponseWriter, r *http.Request, _ rpcCall) bool {
		if n.Add(1) == 1 {
			<-r.Context().Done() // hang until the client gives up
			return true
		}
		return false
	}
	cl := newTestClient(t, testChain(f.URL()), WithTimeouts(100*time.Millisecond, 0))
	if h, err := cl.Head(context.Background()); err != nil || h != 3 {
		t.Fatalf("Head = %d, %v; want 3 after a timed-out attempt", h, err)
	}

	f.intercept = func(w http.ResponseWriter, r *http.Request, _ rpcCall) bool {
		<-r.Context().Done()
		return true
	}
	_, err := cl.Head(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "timed out after 100ms") {
		t.Fatalf("error = %v, want per-attempt timeout", err)
	}
}

func TestResponseTooLarge(t *testing.T) {
	const height = 976080
	hash := "00000000000000000001f5d21eb2b91d6d53fe51a700ff175202da9e98142ad6"
	f := newFakeNode(t)
	f.addBlock(height, hash, readFixture(t, "btc_block_976080.json"))
	cl := newTestClient(t, testChain(f.URL()), WithMaxBlockBytes(4096))
	_, err := cl.ScanTransfers(context.Background(), chain.ScanRequest{FromBlock: height, ToBlock: height, Native: true, Addresses: []string{"x"}})
	if !errors.Is(err, errResponseTooLarge) {
		t.Fatalf("error = %v, want errResponseTooLarge", err)
	}
	if n := f.count("getblock"); n != 1 {
		t.Errorf("oversized response retried on the same endpoint: %d calls", n)
	}
}

func TestLimitedReader(t *testing.T) {
	for _, tc := range []struct {
		size, limit int
		wantErr     bool
	}{
		{10, 10, false}, {9, 10, false}, {11, 10, true}, {0, 0, false}, {1, 0, true}, {5000, 4096, true},
	} {
		lr := &limitedReader{r: bytes.NewReader(bytes.Repeat([]byte("x"), tc.size)), n: int64(tc.limit)}
		got, err := io.ReadAll(lr)
		if tc.wantErr {
			if !errors.Is(err, errResponseTooLarge) || len(got) > tc.limit {
				t.Errorf("size %d limit %d: read %d, err %v", tc.size, tc.limit, len(got), err)
			}
		} else if err != nil || len(got) != tc.size {
			t.Errorf("size %d limit %d: read %d, err %v", tc.size, tc.limit, len(got), err)
		}
	}
}

func TestBackoff(t *testing.T) {
	r := &rpcClient{backoffBase: 100 * time.Millisecond, backoffMax: time.Second}
	for round, max := range map[int]time.Duration{1: 100 * time.Millisecond, 2: 200 * time.Millisecond, 3: 400 * time.Millisecond, 5: time.Second, 80: time.Second} {
		for i := 0; i < 50; i++ {
			if d := r.backoff(round, 0); d < max/2 || d > max {
				t.Fatalf("backoff(%d) = %s, want in [%s, %s]", round, d, max/2, max)
			}
		}
	}
	if d := r.backoff(1, 700*time.Millisecond); d != 700*time.Millisecond {
		t.Errorf("Retry-After 700ms gave %s", d)
	}
	if d := r.backoff(1, time.Hour); d != time.Second {
		t.Errorf("Retry-After 1h gave %s, want capped at 1s", d)
	}
}

func TestParseRetryAfter(t *testing.T) {
	if d := parseRetryAfter("60"); d != time.Minute {
		t.Errorf("60 → %s", d)
	}
	for _, v := range []string{"", "-1", "soon"} {
		if d := parseRetryAfter(v); d != 0 {
			t.Errorf("%q → %s", v, d)
		}
	}
	if d := parseRetryAfter(time.Now().Add(30 * time.Second).UTC().Format(http.TimeFormat)); d < 25*time.Second || d > 31*time.Second {
		t.Errorf("http-date → %s", d)
	}
}

type countingTransport struct{ n atomic.Int32 }

func (c *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.n.Add(1)
	return http.DefaultTransport.RoundTrip(r)
}

func TestWithHTTPClient(t *testing.T) {
	f := newFakeNode(t)
	f.head = 9
	tr := &countingTransport{}
	cl := newTestClient(t, testChain(f.URL()), WithHTTPClient(&http.Client{Transport: tr}))
	if h, err := cl.Head(context.Background()); err != nil || h != 9 {
		t.Fatalf("Head = %d, %v", h, err)
	}
	if tr.n.Load() != 1 {
		t.Errorf("custom transport used %d times, want 1", tr.n.Load())
	}
}

func TestMalformedBlockHash(t *testing.T) {
	for _, res := range []string{`"zz"`, `"` + strings.Repeat("g", 64) + `"`, `12`, `null`} {
		f := newFakeNode(t)
		f.intercept = func(w http.ResponseWriter, _ *http.Request, c rpcCall) bool {
			writeResult(w, c.ID, res)
			return true
		}
		cl := newTestClient(t, testChain(f.URL()))
		ok, err := cl.VerifyTransfer(context.Background(), chain.Transfer{BlockNumber: 1, BlockHash: fakeHash(1)})
		if err == nil || ok {
			t.Errorf("getblockhash result %s: VerifyTransfer = %v, %v; want error", res, ok, err)
		}
	}
}

// Transport-level errors must not quote the URL (net/http's *url.Error does).
func TestTransportErrorStripsURL(t *testing.T) {
	inner := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	err := transportError(context.Background(), context.Background(),
		&url.Error{Op: "Post", URL: "https://node.example/secret-key/", Err: inner}, time.Second)
	if strings.Contains(err.Error(), "secret-key") || !errors.Is(err, inner) {
		t.Errorf("transportError = %v", err)
	}
}
