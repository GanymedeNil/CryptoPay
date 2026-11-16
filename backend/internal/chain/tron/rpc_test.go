package tron

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"cryptopay/internal/chain"
)

func respond(status int, body string) func(w http.ResponseWriter, r *http.Request, path string, n int) bool {
	return func(w http.ResponseWriter, r *http.Request, path string, n int) bool {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
		return true
	}
}

func TestFailoverOnServerErrorAndCooldown(t *testing.T) {
	bad, good := newFakeNode(t), newFakeNode(t)
	bad.setHook(respond(http.StatusServiceUnavailable, "upstream unavailable"))
	c := newTestClient(t, []string{bad.URL(), good.URL()})
	now := time.Unix(1_800_000_000, 0)
	c.now = func() time.Time { return now }

	h, err := c.Head(context.Background())
	if err != nil || h != nileHead {
		t.Fatalf("head = %d, %v", h, err)
	}
	if bad.callsTo("/wallet/getblock") != 1 || good.callsTo("/wallet/getblock") != 1 {
		t.Fatalf("calls bad=%d good=%d", bad.callsTo("/wallet/getblock"), good.callsTo("/wallet/getblock"))
	}
	// The failed endpoint cools down: the next call goes to the good one first.
	if _, err := c.Head(context.Background()); err != nil {
		t.Fatal(err)
	}
	if bad.callsTo("/wallet/getblock") != 1 || good.callsTo("/wallet/getblock") != 2 {
		t.Fatalf("cooldown not honoured: bad=%d good=%d", bad.callsTo("/wallet/getblock"), good.callsTo("/wallet/getblock"))
	}
	// After the cooldown the primary is tried first again.
	now = now.Add(endpointCooldown + time.Second)
	if _, err := c.Head(context.Background()); err != nil {
		t.Fatal(err)
	}
	if bad.callsTo("/wallet/getblock") != 2 {
		t.Fatalf("primary not retried after cooldown")
	}
}

func TestFailoverOnTransportError(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	good := newFakeNode(t)
	c := newTestClient(t, []string{deadURL, good.URL()})
	got, err := c.ScanTransfers(context.Background(), chain.ScanRequest{
		FromBlock: blkB, ToBlock: blkB, Native: true, Addresses: addrs(t, "4139c9df5dfc0ff9a73873ec781e0ac8675cb51cc5"),
	})
	if err != nil || len(got) != 1 {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestAllEndpointsFailing(t *testing.T) {
	a, b := newFakeNode(t), newFakeNode(t)
	a.setHook(respond(http.StatusBadGateway, "<html>bad gateway</html>"))
	b.setHook(respond(http.StatusInternalServerError, `{"Error":"boom"}`))
	c := newTestClient(t, []string{a.URL(), b.URL()}, WithRetries(2))
	_, err := c.Head(context.Background())
	if err == nil {
		t.Fatal("want error")
	}
	if a.callsTo("/wallet/getblock") != 3 || b.callsTo("/wallet/getblock") != 3 {
		t.Errorf("calls a=%d b=%d, want 3 each", a.callsTo("/wallet/getblock"), b.callsTo("/wallet/getblock"))
	}
	msg := err.Error()
	for _, want := range []string{"http 502", "http 500: boom", strings.TrimPrefix(a.URL(), "http://"), strings.TrimPrefix(b.URL(), "http://")} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q lacks %q", msg, want)
		}
	}
	if strings.Contains(msg, "bad gateway") {
		t.Errorf("non-JSON body leaked into error: %q", msg)
	}
}

func TestRateLimited(t *testing.T) {
	const tronGridLimit = `{"Error":"The key exceeds the frequency limit(15), and the query server is suspended for 30s"}`
	cases := map[string]func(w http.ResponseWriter, r *http.Request, path string, n int) bool{
		"429":               respond(http.StatusTooManyRequests, "Too Many Requests"),
		"403 trongrid":      respond(http.StatusForbidden, tronGridLimit),
		"503 trongrid":      respond(http.StatusServiceUnavailable, tronGridLimit),
		"200 node message":  respond(http.StatusOK, tronGridLimit),
		"java-tron limiter": respond(http.StatusOK, `{"Error":"class java.lang.IllegalAccessException : lack of computing resources"}`),
	}
	for name, hook := range cases {
		t.Run(name, func(t *testing.T) {
			node := newFakeNode(t)
			node.setHook(hook)
			c := newTestClient(t, []string{node.URL()}, WithRetries(2))
			_, err := c.ScanTransfers(context.Background(), chain.ScanRequest{
				FromBlock: blkB, ToBlock: blkB, Native: true, Addresses: addrs(t, "4139c9df5dfc0ff9a73873ec781e0ac8675cb51cc5"),
			})
			if !errors.Is(err, chain.ErrRateLimited) {
				t.Fatalf("err = %v, want ErrRateLimited", err)
			}
			if n := node.callsTo("/wallet/getblockbynum"); n != 3 {
				t.Errorf("calls = %d, want 3 (retried with backoff)", n)
			}
		})
	}
	t.Run("recovers", func(t *testing.T) {
		node := newFakeNode(t)
		node.setHook(func(w http.ResponseWriter, r *http.Request, path string, n int) bool {
			if n <= 2 {
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(http.StatusTooManyRequests)
				return true
			}
			return false
		})
		c := newTestClient(t, []string{node.URL()}, WithRetries(2))
		if h, err := c.Head(context.Background()); err != nil || h != nileHead {
			t.Fatalf("head = %d, %v", h, err)
		}
	})
}

func TestBackoff(t *testing.T) {
	c := newTestClient(t, []string{"http://127.0.0.1:1"}, WithBackoff(100*time.Millisecond, 2*time.Second))
	for n := 1; n <= 10; n++ {
		d := c.backoff(n, nil)
		hi := min(100*time.Millisecond<<(n-1), 2*time.Second)
		if d < hi/2 || d > hi {
			t.Errorf("backoff(%d) = %s, want in [%s, %s]", n, d, hi/2, hi)
		}
	}
	ra := []error{errors.New("x"), &rpcError{retryAfter: 1500 * time.Millisecond}}
	if d := c.backoff(1, ra); d != 1500*time.Millisecond {
		t.Errorf("Retry-After not honoured: %s", d)
	}
	ra = []error{&rpcError{retryAfter: time.Minute}}
	if d := c.backoff(1, ra); d != 2*time.Second {
		t.Errorf("Retry-After not capped: %s", d)
	}
	if d := parseRetryAfter(" 7 "); d != 7*time.Second {
		t.Errorf("parseRetryAfter = %s", d)
	}
}

func TestClientErrorsAreNotRetried(t *testing.T) {
	a, b := newFakeNode(t), newFakeNode(t)
	a.setHook(respond(http.StatusUnauthorized, `{"Error":"ApiKey not exists"}`))
	b.setHook(respond(http.StatusBadRequest, `{"Error":"bad request"}`))
	c := newTestClient(t, []string{a.URL(), b.URL()}, WithRetries(3))
	_, err := c.Head(context.Background())
	if err == nil || !strings.Contains(err.Error(), "http 401: ApiKey not exists") || !strings.Contains(err.Error(), "http 400") {
		t.Fatalf("err = %v", err)
	}
	// Each endpoint is tried once (failover), but no retry rounds.
	if a.callsTo("/wallet/getblock") != 1 || b.callsTo("/wallet/getblock") != 1 {
		t.Errorf("calls a=%d b=%d", a.callsTo("/wallet/getblock"), b.callsTo("/wallet/getblock"))
	}
}

// syncBuffer is a goroutine-safe log sink.
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

func TestAPIKeyQueryParamBecomesHeader(t *testing.T) {
	const key = "4b3a1e0f-SECRET-KEY-7c1d"
	node := newFakeNode(t)
	var logs syncBuffer
	c := newTestClient(t, []string{node.URL() + "/?foo=bar&apikey=" + key},
		WithLogger(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))))
	if _, err := c.Head(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls := node.allCalls()
	if len(calls) != 1 {
		t.Fatalf("calls = %+v", calls)
	}
	if calls[0].APIKey != key {
		t.Errorf("header %s = %q, want the key", apiKeyHeader, calls[0].APIKey)
	}
	if calls[0].Query != "foo=bar" || calls[0].Path != "/wallet/getblock" {
		t.Errorf("request went to %q ? %q", calls[0].Path, calls[0].Query)
	}

	// A node echoing the key in its error message must not leak it.
	node.setHook(respond(http.StatusInternalServerError, `{"Error":"key `+key+` is invalid"}`))
	_, err := c.Head(context.Background())
	if err == nil {
		t.Fatal("want error")
	}
	if strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), "***") {
		t.Errorf("key leaked or not redacted: %q", err)
	}
	if strings.Contains(logs.String(), key) || !strings.Contains(logs.String(), "tron rpc request failed") {
		t.Errorf("logs: %s", logs.String())
	}
}

func TestPathTokenNeverLeaks(t *testing.T) {
	const token = "qn0d3T0k3nXYZ123456"
	node := newFakeNode(t)
	var logs syncBuffer
	c := newTestClient(t, []string{node.URL() + "/" + token + "/"}, WithRetries(0), WithRequestTimeout(100*time.Millisecond),
		WithLogger(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))))
	if _, err := c.Head(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p := node.allCalls()[0].Path; p != "/"+token+"/wallet/getblock" {
		t.Fatalf("path = %q", p)
	}
	hooks := map[string]func(w http.ResponseWriter, r *http.Request, path string, n int) bool{
		"connection reset": func(w http.ResponseWriter, r *http.Request, path string, n int) bool {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return true
		},
		"html 404 echoing path": func(w http.ResponseWriter, r *http.Request, path string, n int) bool {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("<html>Not found: " + r.URL.String() + "</html>"))
			return true
		},
		"json error echoing path": respond(http.StatusOK, `{"Error":"no route `+"/"+token+`/wallet/getblock"}`),
		"timeout": func(w http.ResponseWriter, r *http.Request, path string, n int) bool {
			<-r.Context().Done()
			return true
		},
	}
	for name, hook := range hooks {
		t.Run(name, func(t *testing.T) {
			node.setHook(hook)
			_, err := c.Head(context.Background())
			if err == nil {
				t.Fatal("want error")
			}
			t.Log(err)
			if strings.Contains(err.Error(), token) {
				t.Errorf("token leaked: %q", err)
			}
		})
	}
	if strings.Contains(logs.String(), token) {
		t.Errorf("token leaked into logs: %s", logs.String())
	}
}

func TestContextCancellation(t *testing.T) {
	node := newFakeNode(t)
	node.setHook(func(w http.ResponseWriter, r *http.Request, path string, n int) bool {
		<-r.Context().Done() // hang until the client gives up
		return true
	})
	c := newTestClient(t, []string{node.URL()}, WithRequestTimeout(time.Minute))
	req := chain.ScanRequest{FromBlock: blkA, ToBlock: blkB, Native: true, Tokens: []string{usdtB58},
		Addresses: addrs(t, "4139c9df5dfc0ff9a73873ec781e0ac8675cb51cc5")}

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	start := time.Now()
	got, err := c.ScanTransfers(ctx, req)
	if !errors.Is(err, context.Canceled) || got != nil {
		t.Fatalf("got %v, %v; want context.Canceled", got, err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("cancellation took %s", d)
	}

	ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.Head(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("head: err = %v, want DeadlineExceeded", err)
	}
	if _, err := c.VerifyTransfer(ctx, chain.Transfer{TxHash: trxTxA, BlockNumber: blkA, BlockHash: blkAHash,
		To: b58(t, "4159b6dd1b64454154686ef2f2dc2397b4e27d2478"), Index: -1}); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("verify: err = %v, want DeadlineExceeded", err)
	}
}

func TestCancellationDuringBackoff(t *testing.T) {
	node := newFakeNode(t)
	node.setHook(respond(http.StatusServiceUnavailable, ""))
	c := newTestClient(t, []string{node.URL()}, WithBackoff(10*time.Second, 10*time.Second), WithRetries(5))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.Head(ctx)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "http 503") {
		t.Fatalf("err = %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("took %s", d)
	}
}

func TestRequestTimeoutIsRetried(t *testing.T) {
	node := newFakeNode(t)
	node.setHook(func(w http.ResponseWriter, r *http.Request, path string, n int) bool {
		if n == 1 {
			<-r.Context().Done()
			return true
		}
		return false
	})
	c := newTestClient(t, []string{node.URL()}, WithRequestTimeout(100*time.Millisecond))
	if h, err := c.Head(context.Background()); err != nil || h != nileHead {
		t.Fatalf("head = %d, %v", h, err)
	}
	if n := node.callsTo("/wallet/getblock"); n != 2 {
		t.Errorf("calls = %d, want 2", n)
	}

	node.setHook(func(w http.ResponseWriter, r *http.Request, path string, n int) bool {
		<-r.Context().Done()
		return true
	})
	_, err := newTestClient(t, []string{node.URL()}, WithRequestTimeout(50*time.Millisecond), WithRetries(1)).Head(context.Background())
	if err == nil || !strings.Contains(err.Error(), "timed out after 50ms") || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}

func TestConcurrencyLimit(t *testing.T) {
	node := newFakeNode(t)
	node.setHook(func(w http.ResponseWriter, r *http.Request, path string, n int) bool {
		time.Sleep(20 * time.Millisecond)
		return false
	})
	c := newTestClient(t, []string{node.URL()}, WithConcurrency(2))
	req := chain.ScanRequest{FromBlock: blkA, ToBlock: blkB, Native: true, Tokens: []string{usdtB58},
		Addresses: addrs(t, "4139c9df5dfc0ff9a73873ec781e0ac8675cb51cc5")}
	var wg sync.WaitGroup
	for range 3 { // concurrent calls share the limit
		wg.Go(func() {
			if _, err := c.ScanTransfers(context.Background(), req); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Go(func() {
		if _, err := c.Head(context.Background()); err != nil {
			t.Error(err)
		}
	})
	wg.Wait()
	if m := node.maxInFlight.Load(); m > 2 || m < 1 {
		t.Errorf("max in-flight requests = %d, want <= 2", m)
	}
}

func TestMaxResponseBytes(t *testing.T) {
	node := newFakeNode(t)
	c := newTestClient(t, []string{node.URL()}, WithMaxResponseBytes(4096))
	_, err := c.ScanTransfers(context.Background(), chain.ScanRequest{FromBlock: blkA, ToBlock: blkA, Native: true,
		Addresses: addrs(t, "4139c9df5dfc0ff9a73873ec781e0ac8675cb51cc5")})
	if err == nil || !strings.Contains(err.Error(), "larger than 4096 bytes") {
		t.Fatalf("err = %v", err)
	}
	if n := node.callsTo("/wallet/getblockbynum"); n != 1 {
		t.Errorf("oversized response retried %d times", n)
	}
	// The small empty block still fits.
	if _, err := c.ScanTransfers(context.Background(), chain.ScanRequest{FromBlock: blkEmpty, ToBlock: blkEmpty, Native: true,
		Addresses: addrs(t, "4139c9df5dfc0ff9a73873ec781e0ac8675cb51cc5")}); err != nil {
		t.Fatal(err)
	}
}

func TestEndpointParsing(t *testing.T) {
	ep, err := parseEndpoint("https://user:pw-secret@go.getblock.io/abcdef0123456789/?APIKEY=k1&x=y#frag", 1)
	if err != nil {
		t.Fatal(err)
	}
	if ep.apiKey != "k1" || ep.host != "go.getblock.io" {
		t.Fatalf("%+v", ep)
	}
	if u := ep.url("/wallet/getblock"); u != "https://user:pw-secret@go.getblock.io/abcdef0123456789/wallet/getblock?x=y" {
		t.Fatalf("url = %s", u)
	}
	red := ep.redact("x abcdef0123456789 k1 pw-secret user y")
	if strings.Contains(red, "abcdef0123456789") || strings.Contains(red, "pw-secret") || strings.Contains(red, "k1") {
		t.Fatalf("redact = %q", red)
	}
	if long := ep.redact(strings.Repeat("a", 1000)); len(long) > 300 {
		t.Errorf("redact does not bound length: %d", len(long))
	}
}
