package solana

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"cryptopay/internal/chain"
)

// RPCError is a JSON-RPC error object returned by a Solana node. Callers can
// inspect it with errors.As.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message) }

// failure classifies how a failed attempt is handled.
type failure int

const (
	// failRetry: transient (transport, timeout, 429, 5xx, node behind, ...):
	// try the next endpoint, then retry all endpoints after a backoff.
	failRetry failure = iota
	// failNext: specific to this endpoint (401/403, method not offered):
	// try the next endpoint but do not schedule another round for it.
	failNext
	// failStop: deterministic (invalid params, unsupported tx version):
	// retrying anywhere cannot help, return immediately.
	failStop
)

// endpoint is one RPC URL. The full URL may embed an API key, so it is never
// put in errors or logs: only host is.
type endpoint struct {
	url  string
	host string
}

func parseEndpoint(raw string) (endpoint, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return endpoint{}, errors.New("must be an absolute http(s) URL")
	}
	return endpoint{url: u.String(), host: u.Host}, nil
}

// rpcClient is a minimal JSON-RPC 2.0 client with per-request timeout,
// failover across endpoints and bounded retries with backoff.
type rpcClient struct {
	endpoints    []endpoint
	hc           *http.Client
	timeout      time.Duration // per HTTP attempt
	retries      int           // extra rounds over all endpoints after the first
	backoffBase  time.Duration
	backoffMax   time.Duration
	maxRetryWait time.Duration // longest Retry-After we are willing to sleep
	maxBody      int64
	log          *slog.Logger
	nextID       atomic.Uint64
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      uint64 `json:"id"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *RPCError       `json:"error"`
}

// attemptError is the failure of one HTTP attempt against one endpoint.
type attemptError struct {
	host       string
	class      failure
	retryAfter time.Duration
	err        error
}

func (e *attemptError) Error() string { return e.host + ": " + e.err.Error() }
func (e *attemptError) Unwrap() error { return e.err }

// callError reports a call that failed on every endpoint. It wraps the
// per-endpoint errors of the last round, so errors.Is(err,
// chain.ErrRateLimited) holds when any endpoint throttled us.
type callError struct {
	method   string
	attempts int
	errs     []error
}

func (e *callError) Error() string {
	parts := make([]string, len(e.errs))
	for i, err := range e.errs {
		parts[i] = err.Error()
	}
	return fmt.Sprintf("solana rpc %s failed after %d attempt(s): %s", e.method, e.attempts, strings.Join(parts, "; "))
}

func (e *callError) Unwrap() []error { return e.errs }

// call invokes method with params and decodes the result into out (which may
// be nil). See callOpt.
func (r *rpcClient) call(ctx context.Context, method string, params []any, out any) error {
	return r.callOpt(ctx, method, params, out, nil)
}

// callOpt is call with an optional nullErr: when non-nil, a null result is
// treated as a transient failure wrapping nullErr (failover + retry), for
// lookups whose absence on one node may just mean that node lags behind.
func (r *rpcClient) callOpt(ctx context.Context, method string, params []any, out any, nullErr error) error {
	if params == nil {
		params = []any{}
	}
	attempts := 0
	var lastRound []error
	for round := 0; ; round++ {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("solana rpc %s: %w", method, err)
		}
		// A fresh id per round is enough; endpoints are tried sequentially.
		body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: r.nextID.Add(1), Method: method, Params: params})
		if err != nil {
			return fmt.Errorf("solana rpc %s: encode request: %w", method, err)
		}

		lastRound = lastRound[:0]
		retry := false
		var wait time.Duration
		for _, ep := range r.endpoints {
			attempts++
			raw, aerr := r.post(ctx, ep, body)
			if aerr == nil && nullErr != nil && isNullJSON(raw) {
				aerr = &attemptError{host: ep.host, class: failRetry, err: nullErr}
			}
			if aerr == nil {
				if out == nil {
					return nil
				}
				if err := json.Unmarshal(raw, out); err != nil {
					return fmt.Errorf("solana rpc %s: %s: decode result: %w", method, ep.host, err)
				}
				return nil
			}
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("solana rpc %s: %w", method, err)
			}
			r.log.Debug("solana rpc attempt failed", "method", method, "host", ep.host, "attempt", attempts, "err", aerr.err)
			if aerr.class == failStop {
				return fmt.Errorf("solana rpc %s: %w", method, aerr)
			}
			if aerr.class == failRetry {
				retry = true
				wait = max(wait, aerr.retryAfter)
			}
			lastRound = append(lastRound, aerr)
		}

		failed := &callError{method: method, attempts: attempts, errs: append([]error(nil), lastRound...)}
		if !retry || round >= r.retries {
			return failed
		}
		if wait > r.maxRetryWait {
			// The provider asks us to back off longer than we are willing to
			// block a scan; give up now so the caller can schedule the retry.
			return failed
		}
		d := max(r.backoff(round), wait)
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < d {
			return failed // sleeping would only end in a deadline error
		}
		r.log.Warn("solana rpc retrying", "method", method, "round", round+1, "wait", d, "err", failed)
		if err := sleepCtx(ctx, d); err != nil {
			return fmt.Errorf("solana rpc %s: %w", method, err)
		}
	}
}

// backoff returns the wait before retry round+1: exponential with jitter in
// [d/2, d], capped at backoffMax.
func (r *rpcClient) backoff(round int) time.Duration {
	d := r.backoffBase
	for i := 0; i < round && d < r.backoffMax; i++ {
		d *= 2
	}
	d = min(d, r.backoffMax)
	if d <= 1 {
		return d
	}
	half := d / 2
	return half + rand.N(d-half+1)
}

// post performs one HTTP attempt. Errors never contain the endpoint URL.
func (r *rpcClient) post(ctx context.Context, ep endpoint, body []byte) (json.RawMessage, *attemptError) {
	actx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	fail := func(class failure, err error) *attemptError {
		return &attemptError{host: ep.host, class: class, err: err}
	}

	req, err := http.NewRequestWithContext(actx, http.MethodPost, ep.url, bytes.NewReader(body))
	if err != nil {
		return nil, fail(failNext, errors.New("cannot build http request"))
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := r.hc.Do(req)
	if err != nil {
		return nil, fail(failRetry, r.transportError(ctx, actx, ep, err))
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, r.maxBody+1))
	if err != nil {
		return nil, fail(failRetry, r.transportError(ctx, actx, ep, err))
	}
	if int64(len(data)) > r.maxBody {
		return nil, fail(failStop, fmt.Errorf("response larger than %d bytes", r.maxBody))
	}

	var env rpcResponse
	decodeErr := json.Unmarshal(data, &env)

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		ae := fail(failNext, fmt.Errorf("http %d", resp.StatusCode))
		if decodeErr == nil && env.Error != nil {
			ae.err = fmt.Errorf("http %d: %w", resp.StatusCode, env.Error)
		}
		switch {
		case resp.StatusCode == http.StatusTooManyRequests:
			ae.class = failRetry
			ae.err = fmt.Errorf("%w: %w", ae.err, chain.ErrRateLimited)
			ae.retryAfter = parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		case resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode >= 500:
			ae.class = failRetry
			ae.retryAfter = parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		}
		return nil, ae
	}
	if decodeErr != nil {
		return nil, fail(failRetry, fmt.Errorf("invalid json-rpc response: %v", decodeErr))
	}
	if env.Error != nil {
		class, limited := classifyRPCError(env.Error)
		var err error = env.Error
		if limited {
			err = fmt.Errorf("%w: %w", env.Error, chain.ErrRateLimited)
		}
		return nil, fail(class, err)
	}
	if env.Result == nil {
		return nil, fail(failRetry, errors.New("json-rpc response has neither result nor error"))
	}
	return env.Result, nil
}

// transportError turns an http.Client error into one that never contains the
// request URL (url.Error embeds it, including any API key in path or query).
func (r *rpcClient) transportError(parent, attempt context.Context, ep endpoint, err error) error {
	if parent.Err() == nil && attempt.Err() != nil {
		return fmt.Errorf("request timed out after %s", r.timeout)
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	msg := err.Error()
	if ep.url != "" {
		msg = strings.ReplaceAll(msg, ep.url, ep.host)
	}
	return errors.New("transport: " + msg)
}

// classifyRPCError decides how a JSON-RPC error is handled and whether it
// signals throttling. Codes: https://github.com/anza-xyz/agave/blob/master/rpc-client-api/src/custom_error.rs
func classifyRPCError(e *RPCError) (failure, bool) {
	msg := strings.ToLower(e.Message)
	if e.Code == 429 || e.Code == -32429 || strings.Contains(msg, "rate limit") || strings.Contains(msg, "too many requests") {
		return failRetry, true
	}
	switch e.Code {
	case -32600, // invalid request
		-32602, // invalid params
		-32700, // parse error
		-32015: // transaction version not supported by the requested maxSupportedTransactionVersion
		return failStop, false
	case -32601, // method not found: this provider does not offer it
		-32010, // key excluded from this node's secondary indexes
		-32011: // transaction history not available from this node
		return failNext, false
	}
	// -32004 block not available, -32005 node unhealthy/behind, -32014 block
	// status not yet available, -32016 min context slot not reached, -32019
	// long-term storage unreachable, -32603 internal error, provider codes...
	return failRetry, false
}

// parseRetryAfter parses a Retry-After header (delta seconds or HTTP date).
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.ParseInt(v, 10, 64); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(min(secs, 24*3600)) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// isNullJSON reports whether raw is absent or the JSON literal null.
func isNullJSON(raw json.RawMessage) bool {
	s := bytes.TrimSpace(raw)
	return len(s) == 0 || bytes.Equal(s, []byte("null"))
}
