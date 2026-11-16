package solana

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cryptopay/internal/chain"
	"cryptopay/internal/domain"
)

// publicNode429 is the exact body api.devnet.solana.com sends with HTTP 429.
const publicNode429 = ` {"jsonrpc":"2.0","error":{"code": 429,"message":"Too many requests for a specific RPC call"}, "id": 1 } ` + "\r\n"

// scripted serves responses from fn and counts requests.
type scripted struct {
	srv   *httptest.Server
	calls atomic.Int32
}

func newScripted(t *testing.T, fn func(n int, w http.ResponseWriter, r *http.Request)) *scripted {
	s := &scripted{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(s.calls.Add(1))
		fn(n, w, r)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func okSlot(w http.ResponseWriter, slot uint64) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"jsonrpc":"2.0","result":%d,"id":1}`, slot)
}

func rateLimited(w http.ResponseWriter, retryAfter string) {
	if retryAfter != "" {
		w.Header().Set("Retry-After", retryAfter)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusTooManyRequests)
	_, _ = w.Write([]byte(publicNode429))
}

func TestRateLimitHTTP429(t *testing.T) {
	s := newScripted(t, func(_ int, w http.ResponseWriter, _ *http.Request) { rateLimited(w, "0") })
	_, err := newTestClient(t, []string{s.srv.URL}, WithRetries(2)).Head(context.Background())
	require.ErrorIs(t, err, chain.ErrRateLimited)
	var rpcErr *RPCError
	require.ErrorAs(t, err, &rpcErr)
	assert.Equal(t, 429, rpcErr.Code)
	assert.EqualValues(t, 3, s.calls.Load(), "first attempt + 2 retries")
}

func TestRateLimitJSONRPCError(t *testing.T) {
	// Some providers answer 200 with a JSON-RPC throttling error.
	s := newScripted(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"jsonrpc":"2.0","error":{"code":-32005,"message":"Rate limit exceeded, slow down"},"id":1}`)
	})
	_, err := newTestClient(t, []string{s.srv.URL}, WithRetries(1)).Head(context.Background())
	require.ErrorIs(t, err, chain.ErrRateLimited)
	assert.EqualValues(t, 2, s.calls.Load())
}

func TestRetryAfterHonoured(t *testing.T) {
	s := newScripted(t, func(n int, w http.ResponseWriter, _ *http.Request) {
		if n == 1 {
			rateLimited(w, "1")
			return
		}
		okSlot(w, 42)
	})
	start := time.Now()
	got, err := newTestClient(t, []string{s.srv.URL}).Head(context.Background())
	require.NoError(t, err)
	assert.Equal(t, uint64(42), got)
	assert.GreaterOrEqual(t, time.Since(start), time.Second, "waited for Retry-After")
}

func TestRetryAfterLongerThanMaxWaitFailsFast(t *testing.T) {
	s := newScripted(t, func(_ int, w http.ResponseWriter, _ *http.Request) { rateLimited(w, "120") })
	start := time.Now()
	_, err := newTestClient(t, []string{s.srv.URL}, WithMaxRetryWait(5*time.Second)).Head(context.Background())
	require.ErrorIs(t, err, chain.ErrRateLimited)
	assert.Less(t, time.Since(start), time.Second)
	assert.EqualValues(t, 1, s.calls.Load())
}

func TestRetryStopsBeforeContextDeadline(t *testing.T) {
	s := newScripted(t, func(_ int, w http.ResponseWriter, _ *http.Request) { rateLimited(w, "10") })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	_, err := newTestClient(t, []string{s.srv.URL}).Head(ctx)
	require.ErrorIs(t, err, chain.ErrRateLimited, "reports the throttling, not a deadline error")
	assert.Less(t, time.Since(start), time.Second)
}

func TestFailover(t *testing.T) {
	t.Run("5xx", func(t *testing.T) {
		bad := newScripted(t, func(_ int, w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) })
		good := newScripted(t, func(_ int, w http.ResponseWriter, _ *http.Request) { okSlot(w, 7) })
		got, err := newTestClient(t, []string{bad.srv.URL, good.srv.URL}).Head(context.Background())
		require.NoError(t, err)
		assert.Equal(t, uint64(7), got)
		assert.EqualValues(t, 1, bad.calls.Load())
		assert.EqualValues(t, 1, good.calls.Load())
	})
	t.Run("transport error", func(t *testing.T) {
		dead := httptest.NewServer(http.NotFoundHandler())
		deadURL := dead.URL
		dead.Close()
		good := newScripted(t, func(_ int, w http.ResponseWriter, _ *http.Request) { okSlot(w, 8) })
		got, err := newTestClient(t, []string{deadURL, good.srv.URL}).Head(context.Background())
		require.NoError(t, err)
		assert.Equal(t, uint64(8), got)
	})
	t.Run("429 on primary", func(t *testing.T) {
		limited := newScripted(t, func(_ int, w http.ResponseWriter, _ *http.Request) { rateLimited(w, "30") })
		good := newScripted(t, func(_ int, w http.ResponseWriter, _ *http.Request) { okSlot(w, 9) })
		start := time.Now()
		got, err := newTestClient(t, []string{limited.srv.URL, good.srv.URL}).Head(context.Background())
		require.NoError(t, err)
		assert.Equal(t, uint64(9), got)
		assert.Less(t, time.Since(start), time.Second, "no sleep when another endpoint answers")
	})
	t.Run("401 on primary is not retried", func(t *testing.T) {
		unauthorized := newScripted(t, func(_ int, w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
		_, err := newTestClient(t, []string{unauthorized.srv.URL}, WithRetries(3)).Head(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "http 401")
		assert.EqualValues(t, 1, unauthorized.calls.Load())
	})
	t.Run("all endpoints retried each round", func(t *testing.T) {
		a := newScripted(t, func(_ int, w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) })
		b := newScripted(t, func(n int, w http.ResponseWriter, _ *http.Request) {
			if n < 3 {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			okSlot(w, 10)
		})
		got, err := newTestClient(t, []string{a.srv.URL, b.srv.URL}, WithRetries(3)).Head(context.Background())
		require.NoError(t, err)
		assert.Equal(t, uint64(10), got)
		assert.EqualValues(t, 3, a.calls.Load())
		assert.EqualValues(t, 3, b.calls.Load())
	})
}

func TestErrorsNeverContainFullURL(t *testing.T) {
	const secret = "sk_live_0123456789abcdef"
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL + "/?api-key=" + secret
	dead.Close()
	failing := newScripted(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	release := make(chan struct{})
	slow := newScripted(t, func(_ int, w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	t.Cleanup(func() { close(release) }) // runs before the server is closed
	var logs strings.Builder
	c := newTestClient(t,
		[]string{deadURL, failing.srv.URL + "/v2/" + secret, slow.srv.URL + "/" + secret + "?x=1"},
		WithRetries(1), WithRequestTimeout(200*time.Millisecond),
		WithLogger(slogTo(&logs)))
	_, err := c.Head(context.Background())
	require.Error(t, err)
	msg := err.Error()
	assert.NotContains(t, msg, secret)
	assert.Contains(t, msg, strings.TrimPrefix(failing.srv.URL, "http://"), "host is reported")
	assert.Contains(t, msg, "timed out")
	assert.NotErrorIs(t, err, context.DeadlineExceeded, "per-attempt timeout is not the caller's deadline")
	for e := error(err); e != nil; e = errors.Unwrap(e) {
		assert.NotContains(t, e.Error(), secret)
	}
	assert.NotContains(t, logs.String(), secret)
	assert.Contains(t, logs.String(), "solana rpc")

	_, err = New(domain.Chain{ID: "x", Family: domain.FamilySolana, RPCURLs: []string{"ftp://" + secret + "@host/"}})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), secret)
}

func TestDeterministicRPCErrorIsNotRetried(t *testing.T) {
	s := newScripted(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"jsonrpc":"2.0","error":{"code":-32602,"message":"Invalid param: WrongSize"},"id":1}`)
	})
	other := newScripted(t, func(_ int, w http.ResponseWriter, _ *http.Request) { okSlot(w, 1) })
	_, err := newTestClient(t, []string{s.srv.URL, other.srv.URL}).Head(context.Background())
	var rpcErr *RPCError
	require.ErrorAs(t, err, &rpcErr)
	assert.Equal(t, -32602, rpcErr.Code)
	assert.EqualValues(t, 1, s.calls.Load())
	assert.Zero(t, other.calls.Load(), "invalid params fail everywhere: no failover")
}

func TestMalformedResponsesAreRetried(t *testing.T) {
	s := newScripted(t, func(n int, w http.ResponseWriter, _ *http.Request) {
		switch n {
		case 1:
			fmt.Fprint(w, `<html>gateway</html>`)
		case 2:
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1}`)
		default:
			okSlot(w, 5)
		}
	})
	got, err := newTestClient(t, []string{s.srv.URL}).Head(context.Background())
	require.NoError(t, err)
	assert.Equal(t, uint64(5), got)
	assert.EqualValues(t, 3, s.calls.Load())
}

func TestNullHead(t *testing.T) {
	s := newScripted(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"jsonrpc":"2.0","result":null,"id":1}`)
	})
	_, err := newTestClient(t, []string{s.srv.URL}).Head(context.Background())
	require.Error(t, err)
}

func TestResponseSizeLimit(t *testing.T) {
	s := newScripted(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"jsonrpc":"2.0","result":[%s1],"id":1}`, strings.Repeat("1,", 1000))
	})
	_, err := newTestClient(t, []string{s.srv.URL}, WithMaxResponseBytes(512)).Head(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "larger than 512 bytes")
	assert.EqualValues(t, 1, s.calls.Load())
}

func TestHeadContextCancellation(t *testing.T) {
	release := make(chan struct{})
	s := newScripted(t, func(_ int, w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	t.Cleanup(func() { close(release) })
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	start := time.Now()
	_, err := newTestClient(t, []string{s.srv.URL}).Head(ctx)
	require.ErrorIs(t, err, context.Canceled)
	assert.Less(t, time.Since(start), 2*time.Second)

	// Already-cancelled context: no request at all.
	before := s.calls.Load()
	_, err = newTestClient(t, []string{s.srv.URL}).Head(ctx)
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, before, s.calls.Load())
}

func TestRequestShape(t *testing.T) {
	var got map[string]any
	s := newScripted(t, func(_ int, w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		okSlot(w, 1)
	})
	_, err := newTestClient(t, []string{s.srv.URL}).Head(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "2.0", got["jsonrpc"])
	assert.Equal(t, "getSlot", got["method"])
	assert.Equal(t, []any{map[string]any{"commitment": "finalized"}}, got["params"])
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	assert.Equal(t, 10*time.Second, parseRetryAfter("10", now))
	assert.Equal(t, time.Duration(0), parseRetryAfter("0", now))
	assert.Equal(t, time.Duration(0), parseRetryAfter("-3", now))
	assert.Equal(t, time.Duration(0), parseRetryAfter("soon", now))
	assert.Equal(t, time.Duration(0), parseRetryAfter("", now))
	assert.Equal(t, 24*time.Hour, parseRetryAfter("999999999999", now))
	assert.Equal(t, 30*time.Second, parseRetryAfter(now.Add(30*time.Second).Format(http.TimeFormat), now))
	assert.Equal(t, time.Duration(0), parseRetryAfter(now.Add(-time.Minute).Format(http.TimeFormat), now))
}

func TestBackoffBounds(t *testing.T) {
	r := &rpcClient{backoffBase: 100 * time.Millisecond, backoffMax: time.Second}
	for round := range 10 {
		d := r.backoff(round)
		ceil := min(100*time.Millisecond<<round, time.Second)
		assert.GreaterOrEqual(t, d, ceil/2, "round %d", round)
		assert.LessOrEqual(t, d, ceil, "round %d", round)
	}
}
