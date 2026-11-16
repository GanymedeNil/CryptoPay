package tron

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

// apiKeyHeader is the header TronGrid expects its API key in.
const apiKeyHeader = "TRON-PRO-API-KEY"

// endpointCooldown is how long an endpoint that just failed with a
// retryable error is tried only after the healthy ones.
const endpointCooldown = 30 * time.Second

var (
	// errNotFound: the node does not (yet) know the requested block / tx.
	errNotFound = errors.New("not found on node")
	// errUnsupported: the node does not serve this API path (HTTP 404/405/501).
	errUnsupported = errors.New("api not supported by node")
	// errMalformed: the response could not be decoded or failed validation.
	errMalformed = errors.New("malformed response")
)

// endpoint is one configured RPC URL. The full URL may carry credentials
// (path tokens, user info, query parameters) so it is never printed: errors
// and logs only ever mention host.
type endpoint struct {
	base    url.URL // scheme, user info, host, path without trailing "/", query without apikey
	host    string
	apiKey  string
	secrets []string // substrings scrubbed from any message derived from a response

	downUntil atomic.Int64 // unix nanos; 0 = healthy
}

func parseEndpoint(raw string, n int) (*endpoint, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	// url.Parse errors quote the input, which may contain a key: report the position only.
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("rpc url #%d is not a valid http(s) URL", n)
	}
	ep := &endpoint{host: u.Host}
	q := u.Query()
	for k, v := range q {
		if strings.EqualFold(k, "apikey") {
			if ep.apiKey == "" && len(v) > 0 {
				ep.apiKey = strings.TrimSpace(v[0])
			}
			delete(q, k)
			continue
		}
		ep.secrets = append(ep.secrets, v...)
	}
	u.RawQuery = q.Encode()
	u.Fragment, u.RawFragment = "", ""
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	if ep.apiKey != "" {
		ep.secrets = append(ep.secrets, ep.apiKey)
	}
	for _, seg := range strings.Split(u.Path, "/") {
		if len(seg) >= 6 { // path tokens (QuickNode, GetBlock, ...)
			ep.secrets = append(ep.secrets, seg)
		}
	}
	if u.User != nil {
		ep.secrets = append(ep.secrets, u.User.Username())
		if p, ok := u.User.Password(); ok {
			ep.secrets = append(ep.secrets, p)
		}
	}
	ep.base = *u
	return ep, nil
}

func (e *endpoint) url(path string) string {
	u := e.base
	u.Path = e.base.Path + path
	return u.String()
}

// redact scrubs any credential of this endpoint from s and bounds its length.
func (e *endpoint) redact(s string) string {
	for _, sec := range e.secrets {
		if sec != "" {
			s = strings.ReplaceAll(s, sec, "***")
		}
	}
	const max = 256
	if len(s) > max {
		s = s[:max] + "..."
	}
	return s
}

// rpcError is a failed request to one endpoint. Its message names the host
// and API path only.
type rpcError struct {
	host       string
	path       string
	msg        string
	cause      error
	retryable  bool
	retryAfter time.Duration
}

func (e *rpcError) Error() string {
	return fmt.Sprintf("%s %s: %s", e.host, e.path, e.msg)
}

func (e *rpcError) Unwrap() error { return e.cause }

func retryableErr(err error) bool {
	var re *rpcError
	return errors.As(err, &re) && re.retryable
}

// orderedEndpoints returns healthy endpoints in configured order followed by
// the ones still cooling down after a failure (also in configured order).
func (c *Client) orderedEndpoints() []*endpoint {
	now := c.now().UnixNano()
	out := make([]*endpoint, 0, len(c.endpoints))
	var cooling []*endpoint
	for _, ep := range c.endpoints {
		if d := ep.downUntil.Load(); d != 0 && now < d {
			cooling = append(cooling, ep)
			continue
		}
		out = append(out, ep)
	}
	return append(out, cooling...)
}

// call POSTs req (JSON-encoded) to path and hands the 200 response body to
// decode. It fails over across endpoints and retries whole rounds with
// exponential backoff while errors are retryable. decode may return an error
// wrapping errNotFound or errMalformed; both are retried on other endpoints
// (load-balanced providers routinely lag by a block).
func (c *Client) call(ctx context.Context, path string, req any, decode func([]byte) error) error {
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("encode %s request: %w", path, err)
	}
	var errs []error // failures of the current round, one per endpoint
	for round := 0; round <= c.cfg.retries; round++ {
		if round > 0 {
			if err := sleepCtx(ctx, c.backoff(round, errs)); err != nil {
				return fmt.Errorf("%s: %w (last error: %v)", path, err, errors.Join(errs...))
			}
		}
		errs = errs[:0]
		again := false
		for _, ep := range c.orderedEndpoints() {
			err := c.do(ctx, ep, path, body, decode)
			if err == nil {
				ep.downUntil.Store(0)
				return nil
			}
			if ctxErr := ctx.Err(); ctxErr != nil {
				return fmt.Errorf("%s: %w", path, ctxErr)
			}
			errs = append(errs, err)
			if retryableErr(err) {
				again = true
				ep.downUntil.Store(c.now().Add(endpointCooldown).UnixNano())
			}
			c.cfg.logger.LogAttrs(ctx, slog.LevelDebug, "tron rpc request failed",
				slog.String("chain", c.chainID), slog.String("host", ep.host), slog.String("path", path),
				slog.Int("round", round), slog.String("error", err.Error()))
		}
		if !again {
			break
		}
	}
	if len(errs) == 1 {
		return errs[0]
	}
	return errors.Join(errs...)
}

// do performs one HTTP request against one endpoint.
func (c *Client) do(ctx context.Context, ep *endpoint, path string, body []byte, decode func([]byte) error) error {
	fail := func(msg string, cause error, retryable bool) *rpcError {
		return &rpcError{host: ep.host, path: path, msg: msg, cause: cause, retryable: retryable}
	}

	select {
	case c.sem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-c.sem }()

	rctx, cancel := context.WithTimeout(ctx, c.cfg.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, ep.url(path), bytes.NewReader(body))
	if err != nil {
		return fail("cannot build request", nil, false)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if ep.apiKey != "" {
		req.Header.Set(apiKeyHeader, ep.apiKey)
	}

	resp, err := c.cfg.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// *url.Error embeds the full URL: keep only the inner error.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		if rctx.Err() != nil {
			// No cause: callers must not mistake this for their own ctx expiring.
			return fail(fmt.Sprintf("request timed out after %s", c.cfg.timeout), nil, true)
		}
		return fail("transport: "+ep.redact(err.Error()), err, true)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, c.cfg.maxBody+1))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fail("reading response: "+ep.redact(err.Error()), err, true)
	}
	if int64(len(data)) > c.cfg.maxBody {
		return fail(fmt.Sprintf("response larger than %d bytes", c.cfg.maxBody), errMalformed, false)
	}

	nodeErr := nodeErrorMessage(data)
	if resp.StatusCode == http.StatusTooManyRequests || isRateLimitMessage(nodeErr) ||
		(resp.StatusCode != http.StatusOK && isRateLimitMessage(string(data[:min(len(data), 4096)]))) {
		e := fail(fmt.Sprintf("http %d: rate limited", resp.StatusCode), chain.ErrRateLimited, true)
		e.retryAfter = parseRetryAfter(resp.Header.Get("Retry-After"))
		return e
	}
	if resp.StatusCode != http.StatusOK {
		msg := fmt.Sprintf("http %d", resp.StatusCode)
		if nodeErr != "" {
			msg += ": " + ep.redact(nodeErr)
		}
		switch {
		case resp.StatusCode >= 500 && resp.StatusCode != http.StatusNotImplemented,
			resp.StatusCode == http.StatusRequestTimeout:
			return fail(msg, nil, true)
		case resp.StatusCode == http.StatusNotFound, resp.StatusCode == http.StatusMethodNotAllowed,
			resp.StatusCode == http.StatusNotImplemented:
			return fail(msg, errUnsupported, false)
		}
		return fail(msg, nil, false)
	}
	if nodeErr != "" {
		return fail("node error: "+ep.redact(nodeErr), nil, true)
	}
	if err := decode(data); err != nil {
		return fail(ep.redact(err.Error()), err, true)
	}
	return nil
}

// nodeErrorMessage extracts java-tron's {"Error": "..."} message, if any.
func nodeErrorMessage(data []byte) string {
	d := bytes.TrimSpace(data)
	if len(d) == 0 || d[0] != '{' || len(d) > 64<<10 {
		return ""
	}
	var e struct {
		Error   json.RawMessage `json:"Error"` // matched case-insensitively
		Message string          `json:"message"`
	}
	if json.Unmarshal(d, &e) != nil || len(e.Error) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(e.Error, &s) != nil {
		s = string(e.Error)
	}
	if s == "" {
		s = e.Message
	}
	if s == "" {
		s = "unspecified error"
	}
	return s
}

// isRateLimitMessage recognises throttling messages: TronGrid ("exceeds the
// frequency limit"), java-tron's own limiter ("lack of computing resources")
// and generic gateways.
func isRateLimitMessage(s string) bool {
	s = strings.ToLower(s)
	return strings.Contains(s, "frequency limit") || strings.Contains(s, "lack of computing resources") ||
		strings.Contains(s, "rate limit") || strings.Contains(s, "too many requests")
}

func parseRetryAfter(v string) time.Duration {
	if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	return 0
}

// backoff returns the pause before retry round n (n >= 1): exponential with
// jitter, at least the largest Retry-After seen, capped at backoffMax.
func (c *Client) backoff(n int, errs []error) time.Duration {
	d := c.cfg.backoffBase << min(n-1, 16)
	if d <= 0 || d > c.cfg.backoffMax {
		d = c.cfg.backoffMax
	}
	d = d/2 + rand.N(d/2+1)
	for _, err := range errs {
		var re *rpcError
		if errors.As(err, &re) && re.retryAfter > d {
			d = re.retryAfter
		}
	}
	return min(d, c.cfg.backoffMax)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
