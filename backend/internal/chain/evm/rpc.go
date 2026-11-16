package evm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cryptopay/internal/chain"
)

// RPCError is a JSON-RPC error object returned by a node. It is reachable
// with errors.As from errors returned by Client. Message is truncated and has
// URL path / query secrets of the endpoint redacted.
type RPCError struct {
	Code    int
	Message string
}

func (e *RPCError) Error() string { return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message) }

// errBatchRejected marks a provider answering a whole batch with one error
// (batching unsupported or batch too large). The caller retries the calls
// one by one.
var errBatchRejected = errors.New("provider rejected batch request")

// ---- wire format ----

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      uint64 `json:"id"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
}

type rpcErrorObject struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type rpcResponse struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *rpcErrorObject `json:"error"`
}

// rpcCall is one method invocation. result is set on success; it is nil or
// the literal null when the node returned null.
type rpcCall struct {
	method string
	params []any
	result json.RawMessage
}

func newCall(method string, params ...any) *rpcCall {
	if params == nil {
		params = []any{}
	}
	return &rpcCall{method: method, params: params}
}

func isNull(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) == 0 || string(raw) == "null"
}

// decodeResult unmarshals a call result into v. It reports null results
// separately so callers can tell "not found" from malformed data.
func decodeResult(raw json.RawMessage, v any) (null bool, err error) {
	if isNull(raw) {
		return true, nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return false, fmt.Errorf("malformed result: %w", err)
	}
	return false, nil
}

// ---- endpoints ----

type endpoint struct {
	url       string
	host      string   // the only part of the URL that may appear in errors and logs
	secrets   []string // path segments, query values and userinfo, redacted from messages
	downUntil atomic.Int64
	chainOK   atomic.Bool
}

func newEndpoint(raw string, idx int) (*endpoint, error) {
	u, err := url.Parse(raw)
	// Never echo raw or err: both contain the full URL (API key).
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("rpc url #%d is not a valid http(s) url", idx+1)
	}
	ep := &endpoint{url: raw, host: u.Host}
	add := func(s string) {
		if len(s) >= 6 {
			ep.secrets = append(ep.secrets, s)
		}
	}
	for seg := range strings.SplitSeq(u.Path, "/") {
		add(seg)
	}
	for _, vs := range u.Query() {
		for _, v := range vs {
			add(v)
		}
	}
	if u.User != nil {
		add(u.User.Username())
		if p, ok := u.User.Password(); ok {
			add(p)
		}
	}
	return ep, nil
}

// redact removes endpoint secrets from provider supplied text and bounds its length.
func (e *endpoint) redact(s string) string {
	for _, sec := range e.secrets {
		s = strings.ReplaceAll(s, sec, "***")
	}
	const n = 300
	if len(s) > n {
		s = s[:n] + "…"
	}
	return s
}

// ---- client ----

type rpcClient struct {
	eps     []*endpoint
	cfg     *config
	chainID *big.Int // expected eth_chainId; nil disables the check
	ids     atomic.Uint64
}

// call performs a single request.
func (r *rpcClient) call(ctx context.Context, method string, params ...any) (json.RawMessage, error) {
	c := newCall(method, params...)
	if err := r.send(ctx, []*rpcCall{c}); err != nil {
		return nil, err
	}
	return c.result, nil
}

// batch performs all calls, at most cfg.batchSize per HTTP request and
// cfg.concurrency requests in flight. It fails as a whole on the first error.
func (r *rpcClient) batch(ctx context.Context, calls []*rpcCall) error {
	size := r.cfg.batchSize
	n := (len(calls) + size - 1) / size
	return parallel(ctx, n, r.cfg.concurrency, func(ctx context.Context, i int) error {
		chunk := calls[i*size : min((i+1)*size, len(calls))]
		err := r.send(ctx, chunk)
		if len(chunk) > 1 && errors.Is(err, errBatchRejected) {
			r.cfg.logger.LogAttrs(ctx, slog.LevelWarn, "evm rpc batch rejected, falling back to single requests",
				slog.Int("calls", len(chunk)), slog.String("error", err.Error()))
			for _, c := range chunk {
				if err := r.send(ctx, []*rpcCall{c}); err != nil {
					return err
				}
			}
			return nil
		}
		return err
	})
}

// send performs one logical request (a single call or one batch) with
// bounded retries, exponential backoff and failover across endpoints.
func (r *rpcClient) send(ctx context.Context, calls []*rpcCall) error {
	desc := describe(calls)
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("evm rpc %s: %w", desc, err)
	}
	ids := make([]uint64, len(calls))
	reqs := make([]rpcRequest, len(calls))
	for i, c := range calls {
		ids[i] = r.ids.Add(1)
		reqs[i] = rpcRequest{JSONRPC: "2.0", ID: ids[i], Method: c.method, Params: c.params}
	}
	var body []byte
	var err error
	if len(reqs) == 1 {
		body, err = json.Marshal(reqs[0])
	} else {
		body, err = json.Marshal(reqs)
	}
	if err != nil {
		return fmt.Errorf("evm rpc %s: encode request: %w", desc, err)
	}

	attempts := r.cfg.retries + 1
	first := r.pick()
	var lastErr error
	for i := range attempts {
		if i > 0 {
			if err := sleepCtx(ctx, r.backoff(i)); err != nil {
				return fmt.Errorf("evm rpc %s: %w (last error: %v)", desc, err, lastErr)
			}
		}
		ep := r.eps[(first+i)%len(r.eps)]
		retry, err := r.attempt(ctx, ep, calls, ids, body)
		if err == nil {
			ep.downUntil.Store(0)
			return nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("evm rpc %s @ %s: %w", desc, ep.host, ctxErr)
		}
		if !retry {
			return err
		}
		lastErr = err
		ep.downUntil.Store(time.Now().Add(r.cfg.cooldown).UnixNano())
		r.cfg.logger.LogAttrs(ctx, slog.LevelWarn, "evm rpc attempt failed",
			slog.String("host", ep.host), slog.String("call", desc),
			slog.Int("attempt", i+1), slog.Int("max_attempts", attempts), slog.String("error", err.Error()))
	}
	return fmt.Errorf("%w (gave up after %d attempts)", lastErr, attempts)
}

// pick returns the index of the first endpoint (in priority order) that is not
// cooling down after a failure, or the one that recovers soonest.
func (r *rpcClient) pick() int {
	now := time.Now().UnixNano()
	best, bestUntil := 0, int64(0)
	for i, ep := range r.eps {
		until := ep.downUntil.Load()
		if until <= now {
			return i
		}
		if i == 0 || until < bestUntil {
			best, bestUntil = i, until
		}
	}
	return best
}

func (r *rpcClient) backoff(attempt int) time.Duration {
	d := r.cfg.backoffBase << min(attempt-1, 20)
	if d <= 0 || d > r.cfg.backoffMax {
		d = r.cfg.backoffMax
	}
	// Equal jitter: [d/2, d).
	half := d / 2
	if half <= 0 {
		return d
	}
	return half + rand.N(half)
}

// attempt sends body once to ep and stores results into calls.
func (r *rpcClient) attempt(ctx context.Context, ep *endpoint, calls []*rpcCall, ids []uint64, body []byte) (retry bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, r.cfg.requestTimeout)
	defer cancel()
	if retry, err := r.checkChainID(ctx, ep); err != nil {
		return retry, err
	}
	status, data, retry, err := r.post(ctx, ep, body)
	if err != nil {
		return retry, fmt.Errorf("evm rpc %s @ %s: %w", describe(calls), ep.host, err)
	}
	return r.handle(ep, status, data, calls, ids)
}

// checkChainID verifies once per endpoint that it serves the expected network,
// so a misconfigured URL can never credit payments seen on another chain.
func (r *rpcClient) checkChainID(ctx context.Context, ep *endpoint) (bool, error) {
	if r.chainID == nil || ep.chainOK.Load() {
		return false, nil
	}
	c := newCall("eth_chainId")
	id := r.ids.Add(1)
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: id, Method: c.method, Params: c.params})
	if err != nil {
		return false, fmt.Errorf("evm rpc eth_chainId: encode request: %w", err)
	}
	status, data, retry, err := r.post(ctx, ep, body)
	if err != nil {
		return retry, fmt.Errorf("evm rpc eth_chainId @ %s: %w", ep.host, err)
	}
	if retry, err := r.handle(ep, status, data, []*rpcCall{c}, []uint64{id}); err != nil {
		return retry, err
	}
	var s string
	if null, err := decodeResult(c.result, &s); err != nil || null {
		return true, fmt.Errorf("evm rpc eth_chainId @ %s: invalid result", ep.host)
	}
	got, err := parseBig(s)
	if err != nil {
		return true, fmt.Errorf("evm rpc eth_chainId @ %s: %w", ep.host, err)
	}
	if got.Cmp(r.chainID) != 0 {
		// Retryable so that another configured endpoint is tried.
		return true, fmt.Errorf("evm rpc @ %s: endpoint serves chain id %s, want %s", ep.host, got, r.chainID)
	}
	ep.chainOK.Store(true)
	return false, nil
}

var errResponseTooLarge = errors.New("response body exceeds size limit")

// post performs the HTTP round trip. Errors never contain the URL.
func (r *rpcClient) post(ctx context.Context, ep *endpoint, body []byte) (status int, data []byte, retry bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep.url, bytes.NewReader(body))
	if err != nil {
		return 0, nil, false, errors.New("cannot build http request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := r.cfg.httpClient.Do(req)
	if err != nil {
		return 0, nil, true, transportError(err)
	}
	defer resp.Body.Close()
	data, err = io.ReadAll(io.LimitReader(resp.Body, r.cfg.maxResponse+1))
	if err != nil {
		return 0, nil, true, fmt.Errorf("read response: %w", transportError(err))
	}
	if int64(len(data)) > r.cfg.maxResponse {
		return resp.StatusCode, nil, false, fmt.Errorf("%w (%d bytes)", errResponseTooLarge, r.cfg.maxResponse)
	}
	return resp.StatusCode, data, false, nil
}

// transportError strips the *url.Error wrapper, whose message holds the full
// request URL including any API key.
func transportError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("transport: %w", ue.Err)
	}
	return fmt.Errorf("transport: %w", err)
}

// handle interprets an HTTP response and stores results into calls.
func (r *rpcClient) handle(ep *endpoint, status int, data []byte, calls []*rpcCall, ids []uint64) (retry bool, err error) {
	desc := describe(calls)
	resps, perr := parseEnvelopes(data)
	httpFail := func(retry bool) (bool, error) {
		msg := fmt.Sprintf("http %d", status)
		if perr == nil && len(resps) > 0 && resps[0].Error != nil {
			msg += ": " + ep.redact(resps[0].Error.Message)
		}
		if status == http.StatusTooManyRequests || isRateLimitText(strings.ToLower(string(data[:min(len(data), 1024)]))) {
			return true, fmt.Errorf("evm rpc %s @ %s: %w: %s", desc, ep.host, chain.ErrRateLimited, msg)
		}
		return retry, fmt.Errorf("evm rpc %s @ %s: %s", desc, ep.host, msg)
	}
	switch {
	case status == http.StatusTooManyRequests:
		return httpFail(true)
	case status == http.StatusRequestTimeout || status >= 500 ||
		status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusNotFound:
		// Server side trouble or a misconfigured endpoint: another endpoint may work.
		return httpFail(true)
	case status < 200 || status > 299:
		if perr != nil || len(resps) == 0 {
			return httpFail(false)
		}
		// A JSON-RPC body with a non-2xx status: interpret it below.
	case perr != nil:
		return true, fmt.Errorf("evm rpc %s @ %s: invalid json-rpc response: %v", desc, ep.host, perr)
	}

	byID := make(map[uint64]*rpcResponse, len(resps))
	var anon *rpcResponse // error without a usable id: applies to the whole request
	for i := range resps {
		if id, ok := parseID(resps[i].ID); ok {
			byID[id] = &resps[i]
		} else if resps[i].Error != nil && anon == nil {
			anon = &resps[i]
		}
	}
	if len(calls) == 1 && len(resps) == 1 && byID[ids[0]] == nil {
		// Some proxies do not echo the id of a single request.
		byID[ids[0]] = &resps[0]
	}

	var fatalErr, retryErr error
	for i, c := range calls {
		resp := byID[ids[i]]
		if resp == nil {
			if anon != nil {
				retry, err := classify(ep, desc, anon.Error, commonMethod(calls))
				if !retry && len(calls) > 1 && !errors.Is(err, chain.ErrRangeTooLarge) {
					err = fmt.Errorf("%w: %w", errBatchRejected, err)
				}
				return retry, err
			}
			return true, fmt.Errorf("evm rpc %s @ %s: response has no entry for %s (id %d)", desc, ep.host, c.method, ids[i])
		}
		if resp.Error != nil {
			retry, err := classify(ep, c.method, resp.Error, c.method)
			if retry {
				retryErr = cmpOr(retryErr, err)
			} else {
				fatalErr = cmpOr(fatalErr, err)
			}
			continue
		}
		c.result = resp.Result
	}
	if fatalErr != nil {
		return false, fatalErr
	}
	if retryErr != nil {
		return true, retryErr
	}
	return false, nil
}

func cmpOr(a, b error) error {
	if a != nil {
		return a
	}
	return b
}

func parseEnvelopes(data []byte) ([]rpcResponse, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, errors.New("empty body")
	}
	switch data[0] {
	case '[':
		var out []rpcResponse
		if err := json.Unmarshal(data, &out); err != nil {
			return nil, errors.New("malformed json-rpc batch response")
		}
		return out, nil
	case '{':
		var one rpcResponse
		if err := json.Unmarshal(data, &one); err != nil {
			return nil, errors.New("malformed json-rpc response")
		}
		return []rpcResponse{one}, nil
	}
	return nil, errors.New("body is not json")
}

func parseID(raw json.RawMessage) (uint64, bool) {
	s := strings.Trim(string(bytes.TrimSpace(raw)), `"`)
	if s == "" || s == "null" {
		return 0, false
	}
	id, err := strconv.ParseUint(s, 10, 64)
	return id, err == nil
}

// commonMethod returns the method shared by all calls, or "".
func commonMethod(calls []*rpcCall) string {
	m := calls[0].method
	for _, c := range calls[1:] {
		if c.method != m {
			return ""
		}
	}
	return m
}

func describe(calls []*rpcCall) string {
	if len(calls) == 1 {
		return calls[0].method
	}
	if m := commonMethod(calls); m != "" {
		return fmt.Sprintf("batch of %d %s", len(calls), m)
	}
	return fmt.Sprintf("batch of %d calls", len(calls))
}

// ---- error classification ----

var rateLimitPatterns = []string{
	"rate limit", "ratelimit", "rate-limit", "rate limited", "too many requests",
	"request rate", "requests per second", "per second", "compute units", "request limit reached",
	"daily request", "quota", "capacity exceeded", "throttl",
}

var rangePatterns = []string{
	"block range", "range too large", "range is too large", "range too wide", "range is too wide",
	"range limit", "range exceeds", "exceeds the range", "max range", "maximum range", "max block range",
	"maximum block range", "ranges over", "too many blocks", "query returned more than", "returned more than",
	"too many results", "more than 10000", "response size", "response too large", "result set too large",
	"limit exceeded", "is limited to", "logs limit", "log limit", "query timeout", "execution timeout",
	"exceeded max range", "too many logs",
}

var transientPatterns = []string{
	"header not found", "unknown block", "timeout", "timed out", "temporarily unavailable",
	"try again", "service unavailable", "bad gateway", "upstream", "no healthy", "overloaded", "busy",
}

func containsAny(s string, pats []string) bool {
	for _, p := range pats {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}

func isRateLimitText(s string) bool { return containsAny(s, rateLimitPatterns) }

func errorText(e *rpcErrorObject) string {
	s := e.Message
	if len(e.Data) > 0 && !isNull(e.Data) {
		s += " " + string(e.Data[:min(len(e.Data), 512)])
	}
	return strings.ToLower(s)
}

// classify maps a JSON-RPC error to a wrapped sentinel and reports whether
// repeating the request (possibly on another endpoint) can help.
func classify(ep *endpoint, what string, e *rpcErrorObject, method string) (retry bool, err error) {
	rerr := &RPCError{Code: e.Code, Message: ep.redact(e.Message)}
	text := errorText(e)
	prefix := fmt.Sprintf("evm rpc %s @ %s", what, ep.host)
	switch {
	case e.Code == 429 || e.Code == -32029 || isRateLimitText(text):
		return true, fmt.Errorf("%s: %w: %w", prefix, chain.ErrRateLimited, rerr)
	case method == "eth_getLogs" && (e.Code == -32005 || e.Code == -32614 || containsAny(text, rangePatterns)):
		return false, fmt.Errorf("%s: %w: %w", prefix, chain.ErrRangeTooLarge, rerr)
	case e.Code == -32603 || containsAny(text, transientPatterns):
		return true, fmt.Errorf("%s: %w", prefix, rerr)
	}
	return false, fmt.Errorf("%s: %w", prefix, rerr)
}

// ---- helpers ----

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

// parallel runs fn(0..n-1) with at most limit in flight. The first error
// cancels the rest and is returned.
func parallel(ctx context.Context, n, limit int, fn func(ctx context.Context, i int) error) error {
	if n <= 0 {
		return nil
	}
	if n == 1 || limit <= 1 {
		for i := range n {
			if err := fn(ctx, i); err != nil {
				return err
			}
		}
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		wg       sync.WaitGroup
		once     sync.Once
		firstErr error
	)
	sem := make(chan struct{}, limit)
loop:
	for i := range n {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			break loop
		}
		if ctx.Err() != nil { // select picks randomly when both are ready
			break
		}
		wg.Go(func() {
			defer func() { <-sem }()
			if err := fn(ctx, i); err != nil {
				once.Do(func() {
					firstErr = err
					cancel()
				})
			}
		})
	}
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	return ctx.Err()
}
