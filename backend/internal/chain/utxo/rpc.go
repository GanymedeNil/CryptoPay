package utxo

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

// RPCError is a JSON-RPC error object returned by the node or the provider
// (for example Bitcoin Core's -8 "Block height out of range"). Errors returned
// by the client wrap it, so callers can use errors.As.
type RPCError struct {
	Code    int
	Message string
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message)
}

// Bitcoin Core RPC error codes the client treats specially.
const (
	rpcInWarmup        = -28    // node is starting up: transient
	rpcInternalError   = -32603 // JSON-RPC internal error
	rpcServerErrorMax  = -32000 // JSON-RPC implementation-defined server errors
	rpcServerErrorMin  = -32099 //   (-32099..-32000)
	rpcLimitExceeded   = -32005 // used by several providers for throttling
	rpcHTTPTooManyReqs = 429    // some gateways put the HTTP status in the code
)

// errClass tells the retry loop what to do after a failed attempt.
type errClass int

const (
	classRetry    errClass = iota // transient: retry, this endpoint included
	classFailover                 // endpoint-specific: try the other endpoints only
	classFatal                    // deterministic: give up immediately
)

// dataError marks a well-formed response whose content is unusable (missing
// fields, wrong block, unsupported verbosity...). Another endpoint may do
// better, the same one will not.
type dataError struct{ err error }

func (e *dataError) Error() string { return e.err.Error() }
func (e *dataError) Unwrap() error { return e.err }

func badData(format string, a ...any) error {
	return &dataError{err: fmt.Errorf(format, a...)}
}

// fatalError marks a condition no retry or failover can fix within this call.
type fatalError struct{ err error }

func (e *fatalError) Error() string { return e.err.Error() }
func (e *fatalError) Unwrap() error { return e.err }

var errResponseTooLarge = errors.New("response body exceeds size limit")

// endpoint is one configured RPC URL. The URL may embed credentials
// (userinfo → HTTP basic auth) or an API key in its path, so only host is
// ever used in errors and logs.
type endpoint struct {
	url  string // request URL, userinfo removed
	host string // host[:port], safe to log
	user string
	pass string
	auth bool
}

func parseEndpoint(i int, raw string) (endpoint, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	// Do not wrap err: url.Parse errors quote the full URL.
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return endpoint{}, fmt.Errorf("rpc url #%d is not a valid http(s) URL", i+1)
	}
	ep := endpoint{host: u.Host}
	if u.User != nil {
		ep.user = u.User.Username()
		ep.pass, _ = u.User.Password()
		ep.auth = true
		u.User = nil
	}
	u.Fragment = ""
	ep.url = u.String()
	return ep, nil
}

// resultFunc decodes the "result" member of a successful response. first is
// the value's first token, already consumed (a json.Delim for objects and
// arrays, the whole value for scalars; never nil). The function must consume
// the rest of the value. It is invoked once per attempt, so it must reset any
// state it accumulates.
type resultFunc func(dec *json.Decoder, first json.Token) error

type callSpec struct {
	method   string
	params   []any
	timeout  time.Duration // per attempt, including reading the body
	maxBytes int64         // response body limit
	result   resultFunc
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      uint64 `json:"id"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
}

type rpcClient struct {
	chainID     string
	endpoints   []endpoint
	hc          *http.Client
	log         *slog.Logger
	rounds      int // attempts per endpoint
	backoffBase time.Duration
	backoffMax  time.Duration
	nextID      atomic.Uint64
}

// call performs one JSON-RPC call with bounded retries. Each round tries every
// usable endpoint in order (primary first), so a failing endpoint fails over to
// the next one immediately; rounds are separated by an exponential backoff.
func (r *rpcClient) call(ctx context.Context, cs callSpec) error {
	params := cs.params
	if params == nil {
		params = []any{}
	}
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: r.nextID.Add(1), Method: cs.method, Params: params})
	if err != nil {
		return fmt.Errorf("%s: encode request: %w", cs.method, err)
	}

	var (
		lastErr     error
		attempts    int
		rateLimited bool
		retryAfter  time.Duration
		skip        = make([]bool, len(r.endpoints))
	)
	for round := 0; round < r.rounds; round++ {
		if round > 0 {
			if err := sleepCtx(ctx, r.backoff(round, retryAfter)); err != nil {
				return fmt.Errorf("%s: %w (last error: %v)", cs.method, err, lastErr)
			}
			retryAfter = 0
		}
		tried := false
		for i := range r.endpoints {
			if skip[i] {
				continue
			}
			if err := ctx.Err(); err != nil {
				if lastErr != nil {
					return fmt.Errorf("%s: %w (last error: %v)", cs.method, err, lastErr)
				}
				return fmt.Errorf("%s: %w", cs.method, err)
			}
			tried = true
			attempts++
			ep := &r.endpoints[i]
			res := r.attempt(ctx, ep, cs, body)
			if res.err == nil {
				return nil
			}
			if ctxErr := ctx.Err(); ctxErr != nil {
				// The caller gave up; whatever the attempt reported is a consequence.
				return fmt.Errorf("%s via %s: %w", cs.method, ep.host, ctxErr)
			}
			lastErr = fmt.Errorf("via %s: %w", ep.host, res.err)
			if res.rateLimited {
				rateLimited = true
				retryAfter = max(retryAfter, res.retryAfter)
			}
			r.log.LogAttrs(ctx, slog.LevelWarn, "utxo rpc attempt failed",
				slog.String("chain", r.chainID), slog.String("method", cs.method),
				slog.String("host", ep.host), slog.Int("attempt", attempts), slog.Any("error", res.err))
			switch res.class {
			case classFatal:
				return fmt.Errorf("%s %w", cs.method, lastErr)
			case classFailover:
				skip[i] = true
			}
		}
		if !tried {
			break
		}
	}
	if rateLimited && !errors.Is(lastErr, chain.ErrRateLimited) {
		return fmt.Errorf("%s: %d attempt(s) failed, last %w (%w)", cs.method, attempts, lastErr, chain.ErrRateLimited)
	}
	return fmt.Errorf("%s: %d attempt(s) failed, last %w", cs.method, attempts, lastErr)
}

type attemptResult struct {
	err         error
	class       errClass
	rateLimited bool
	retryAfter  time.Duration
}

func (r *rpcClient) attempt(ctx context.Context, ep *endpoint, cs callSpec, body []byte) attemptResult {
	actx, cancel := context.WithTimeout(ctx, cs.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(actx, http.MethodPost, ep.url, bytes.NewReader(body))
	if err != nil {
		// Never include err: it may quote the URL.
		return attemptResult{err: errors.New("cannot build http request"), class: classFailover}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if ep.auth {
		req.SetBasicAuth(ep.user, ep.pass)
	}

	resp, err := r.hc.Do(req)
	if err != nil {
		return attemptResult{err: transportError(ctx, actx, err, cs.timeout), class: classRetry}
	}
	defer func() {
		// Drain a little so the connection can be reused; abandon big leftovers.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return statusResult(resp)
	}

	dec := json.NewDecoder(&limitedReader{r: resp.Body, n: cs.maxBytes})
	dec.UseNumber()
	rpcErr, err := decodeEnvelope(dec, cs.result)
	if err != nil {
		var (
			de *dataError
			fe *fatalError
		)
		switch {
		case actx.Err() != nil && ctx.Err() == nil:
			return attemptResult{err: fmt.Errorf("reading response: timed out after %s: %w", cs.timeout, context.DeadlineExceeded), class: classRetry}
		case errors.As(err, &fe):
			return attemptResult{err: err, class: classFatal}
		case errors.As(err, &de), errors.Is(err, errResponseTooLarge):
			return attemptResult{err: err, class: classFailover}
		default: // truncated body, syntax error, connection reset...
			return attemptResult{err: fmt.Errorf("reading response: %w", err), class: classRetry}
		}
	}
	if rpcErr != nil {
		return rpcErrorResult(rpcErr)
	}
	return attemptResult{}
}

// statusResult classifies a non-2xx HTTP response. Bitcoin Core in legacy
// JSON-RPC mode reports RPC errors with HTTP 500/404 and a JSON body, so the
// body is checked for an error object first.
func statusResult(resp *http.Response) attemptResult {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var env struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(data, &env) == nil {
		if e := parseRPCError(env.Error); e != nil {
			res := rpcErrorResult(e)
			switch {
			case resp.StatusCode == http.StatusTooManyRequests && !res.rateLimited:
				res = attemptResult{err: fmt.Errorf("%w: %w", chain.ErrRateLimited, e), class: classRetry, rateLimited: true}
			case (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) && !res.rateLimited:
				res.class = classFailover // bad or unentitled API key: other endpoints may work
			}
			if res.rateLimited {
				res.retryAfter = parseRetryAfter(resp.Header.Get("Retry-After"))
			}
			return res
		}
	}
	// Never include the body: provider error pages may echo the request path.
	err := fmt.Errorf("http status %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return attemptResult{
			err:         fmt.Errorf("%w: %w", chain.ErrRateLimited, err),
			class:       classRetry,
			rateLimited: true,
			retryAfter:  parseRetryAfter(resp.Header.Get("Retry-After")),
		}
	case resp.StatusCode >= 500, resp.StatusCode == http.StatusRequestTimeout:
		return attemptResult{err: err, class: classRetry}
	default: // 401, 403, 404...: this endpoint is misconfigured
		return attemptResult{err: err, class: classFailover}
	}
}

func rpcErrorResult(e *RPCError) attemptResult {
	msg := strings.ToLower(e.Message)
	switch {
	case e.Code == rpcHTTPTooManyReqs || e.Code == rpcLimitExceeded ||
		strings.Contains(msg, "rate limit") || strings.Contains(msg, "too many requests"):
		return attemptResult{err: fmt.Errorf("%w: %w", chain.ErrRateLimited, e), class: classRetry, rateLimited: true}
	case e.Code == rpcInWarmup:
		return attemptResult{err: e, class: classRetry}
	case e.Code == rpcInternalError || (e.Code <= rpcServerErrorMax && e.Code >= rpcServerErrorMin):
		// JSON-RPC "internal/server error": what gateways report for upstream
		// trouble. Bitcoin Core's own application errors are -1..-99.
		return attemptResult{err: e, class: classRetry}
	default: // -8 height out of range, -5 not found, -32601 method not found...
		return attemptResult{err: e, class: classFatal}
	}
}

// parseRPCError decodes a JSON-RPC "error" member; nil when absent or null.
func parseRPCError(raw json.RawMessage) *RPCError {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil
	}
	var obj struct {
		Code    json.Number `json:"code"`
		Message string      `json:"message"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		code, _ := strconv.Atoi(obj.Code.String())
		return &RPCError{Code: code, Message: obj.Message}
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return &RPCError{Message: s}
	}
	return &RPCError{Message: truncate(string(raw), 200)}
}

// decodeEnvelope walks a JSON-RPC response object, handing "result" to fn as
// a stream. Member order is not assumed (providers re-serialize responses).
func decodeEnvelope(dec *json.Decoder, fn resultFunc) (*RPCError, error) {
	if err := expectDelim(dec, '{'); err != nil {
		return nil, err
	}
	var gotResult, nullResult bool
	for dec.More() {
		key, err := readKey(dec)
		if err != nil {
			return nil, err
		}
		switch key {
		case "result":
			tok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			if tok == nil {
				nullResult = true
				continue
			}
			if err := fn(dec, tok); err != nil {
				return nil, err
			}
			gotResult = true
		case "error":
			var raw json.RawMessage
			if err := dec.Decode(&raw); err != nil {
				return nil, err
			}
			if e := parseRPCError(raw); e != nil {
				return e, nil
			}
		default:
			if err := skipValue(dec); err != nil {
				return nil, err
			}
		}
	}
	if err := expectDelim(dec, '}'); err != nil {
		return nil, err
	}
	switch {
	case gotResult:
		return nil, nil
	case nullResult:
		return nil, badData("null result without error")
	default:
		return nil, badData("response has no result")
	}
}

// transportError strips the URL that net/http puts into *url.Error (it may
// carry an API key in its path) and names per-attempt timeouts.
func transportError(parent, attemptCtx context.Context, err error, timeout time.Duration) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	if attemptCtx.Err() != nil && parent.Err() == nil {
		return fmt.Errorf("timed out after %s: %w", timeout, context.DeadlineExceeded)
	}
	return err
}

func (r *rpcClient) backoff(round int, retryAfter time.Duration) time.Duration {
	d := r.backoffMax
	if shift := round - 1; shift < 30 {
		if b := r.backoffBase << shift; b > 0 && b < d {
			d = b
		}
	}
	d = d/2 + rand.N(d/2+1) // jitter in [d/2, d]
	if retryAfter > d {
		d = min(retryAfter, r.backoffMax)
	}
	return d
}

func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
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

// limitedReader fails (instead of returning EOF like io.LimitReader) once more
// than n bytes are read, so an oversized body is an error, not a truncation.
type limitedReader struct {
	r io.Reader
	n int64
}

func (l *limitedReader) Read(p []byte) (int, error) {
	if l.n < 0 {
		return 0, errResponseTooLarge
	}
	// Allow one byte past the limit to tell "exactly n bytes" from "more".
	if int64(len(p)) > l.n+1 {
		p = p[:l.n+1]
	}
	n, err := l.r.Read(p)
	l.n -= int64(n)
	if l.n < 0 {
		return n - 1, errResponseTooLarge
	}
	return n, err
}

// ---- json.Decoder token helpers ----

func expectDelim(dec *json.Decoder, want json.Delim) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); !ok || d != want {
		return badData("expected %q, got %s", want, tokenKind(tok))
	}
	return nil
}

func readKey(dec *json.Decoder) (string, error) {
	tok, err := dec.Token()
	if err != nil {
		return "", err
	}
	key, ok := tok.(string)
	if !ok {
		return "", badData("expected object key, got %s", tokenKind(tok))
	}
	return key, nil
}

// skipValue consumes the next value. It walks tokens instead of buffering the
// value so large unknown members (e.g. Litecoin's "mweb") cost no extra memory.
func skipValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	return skipRest(dec, tok)
}

// skipRest consumes the remainder of a value whose first token was first.
func skipRest(dec *json.Decoder, first json.Token) error {
	d, ok := first.(json.Delim)
	if !ok || (d != '{' && d != '[') {
		return nil
	}
	for depth := 1; depth > 0; {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		if d, ok := tok.(json.Delim); ok {
			if d == '{' || d == '[' {
				depth++
			} else {
				depth--
			}
		}
	}
	return nil
}

func tokenKind(tok json.Token) string {
	switch t := tok.(type) {
	case nil:
		return "null"
	case json.Delim:
		if t == '{' || t == '}' {
			return "object"
		}
		return "array"
	case string:
		return "string"
	case json.Number, float64:
		return "number"
	case bool:
		return "bool"
	}
	return fmt.Sprintf("%T", tok)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
