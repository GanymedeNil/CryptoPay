package rates

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"
)

// CoinGecko defaults.
const (
	// DefaultBaseURL is the public (free / demo key) CoinGecko API.
	DefaultBaseURL = "https://api.coingecko.com/api/v3"
	// DefaultProBaseURL is the CoinGecko Pro API, used when Pro is set.
	DefaultProBaseURL = "https://pro-api.coingecko.com/api/v3"
	// DemoAPIKeyHeader carries a demo (free tier) API key.
	DemoAPIKeyHeader = "x-cg-demo-api-key"
	// ProAPIKeyHeader carries a paid Pro API key.
	ProAPIKeyHeader = "x-cg-pro-api-key"

	// DefaultTTL is how long a fetched price is served without asking upstream.
	DefaultTTL = 60 * time.Second
	// DefaultMaxStale is how old a cached price may be when it is served
	// because the upstream is failing.
	DefaultMaxStale = 10 * time.Minute
	// DefaultFailureBackoff is how long upstream is left alone after a failed
	// refresh (requests are answered from the cache or fail fast meanwhile).
	DefaultFailureBackoff = 10 * time.Second
	// DefaultRequestTimeout bounds one upstream request.
	DefaultRequestTimeout = 10 * time.Second

	maxBodyBytes = 4 << 20
)

// CoinGeckoConfig configures NewCoinGecko. Every field is optional.
type CoinGeckoConfig struct {
	// BaseURL of the API without trailing slash. Default DefaultBaseURL, or
	// DefaultProBaseURL when Pro is set.
	BaseURL string
	// APIKey is sent in DemoAPIKeyHeader, or ProAPIKeyHeader when Pro is set.
	// Empty means anonymous access (public rate limits).
	APIKey string
	// Pro selects the paid Pro API (header and default base URL).
	Pro bool
	// HTTPClient used for requests. Default: a client with a 10 s timeout.
	HTTPClient *http.Client
	// TTL of a fetched price. Default DefaultTTL.
	TTL time.Duration
	// MaxStale is the maximum age of a cached price served while the upstream
	// is failing. Default DefaultMaxStale; negative disables stale serving.
	MaxStale time.Duration
	// FailureBackoff after a failed refresh. Default DefaultFailureBackoff;
	// negative disables the backoff.
	FailureBackoff time.Duration
	// RequestTimeout bounds one upstream request. Default DefaultRequestTimeout.
	RequestTimeout time.Duration
	// Now is the clock. Default time.Now.
	Now func() time.Time
	// Logger receives warnings about upstream failures. Default: discard.
	Logger *slog.Logger
	// RateKeys are CoinGecko coin ids refreshed in every batch (typically the
	// rate keys of all configured assets). Keys requested through Rate are
	// added automatically.
	RateKeys []string
	// Currencies refreshed in every batch, in addition to "usd" which is always
	// included. Unsupported codes are ignored. Currencies requested through
	// Rate are added automatically.
	Currencies []string
}

// CoinGecko is a Provider backed by the CoinGecko /simple/price endpoint.
//
// Every refresh fetches all known rate keys × all known currencies in one
// request; concurrent callers share one in-flight refresh. Prices are cached
// for TTL. When a refresh fails, cached prices up to MaxStale old are served;
// beyond that Rate returns an error wrapping ErrUnavailable.
//
// It is safe for concurrent use.
type CoinGecko struct {
	baseURL   string
	apiKey    string
	keyHeader string
	client    *http.Client
	ttl       time.Duration
	maxStale  time.Duration
	backoff   time.Duration
	timeout   time.Duration
	now       func() time.Time
	log       *slog.Logger

	mu         sync.Mutex
	keys       map[string]struct{}
	currencies map[string]struct{}
	cache      map[pair]entry
	inflight   *refresh
	lastFailAt time.Time
	lastErr    error
}

var (
	_ Provider = (*CoinGecko)(nil)
	_ Provider = (*Static)(nil)
)

type pair struct{ key, cur string }

type entry struct {
	rate    decimal.Decimal
	at      time.Time
	missing bool // upstream answered without a usable price
}

// refresh is one in-flight upstream request shared by all waiters.
type refresh struct {
	done chan struct{}
	keys map[string]struct{}
	curs map[string]struct{}
	err  error // set before done is closed
}

func (r *refresh) covers(p pair) bool {
	_, k := r.keys[p.key]
	_, c := r.curs[p.cur]
	return k && c
}

// NewCoinGecko returns a CoinGecko provider. See CoinGeckoConfig for defaults.
func NewCoinGecko(cfg CoinGeckoConfig) *CoinGecko {
	c := &CoinGecko{
		baseURL:    strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:     strings.TrimSpace(cfg.APIKey),
		keyHeader:  DemoAPIKeyHeader,
		client:     cfg.HTTPClient,
		ttl:        cfg.TTL,
		maxStale:   cfg.MaxStale,
		backoff:    cfg.FailureBackoff,
		timeout:    cfg.RequestTimeout,
		now:        cfg.Now,
		log:        cfg.Logger,
		keys:       map[string]struct{}{},
		currencies: map[string]struct{}{"usd": {}},
		cache:      map[pair]entry{},
	}
	if cfg.Pro {
		c.keyHeader = ProAPIKeyHeader
	}
	if c.baseURL == "" {
		c.baseURL = DefaultBaseURL
		if cfg.Pro {
			c.baseURL = DefaultProBaseURL
		}
	}
	if c.client == nil {
		c.client = &http.Client{Timeout: DefaultRequestTimeout}
	}
	if c.ttl <= 0 {
		c.ttl = DefaultTTL
	}
	if c.maxStale == 0 {
		c.maxStale = DefaultMaxStale
	}
	if c.backoff == 0 {
		c.backoff = DefaultFailureBackoff
	}
	if c.timeout <= 0 {
		c.timeout = DefaultRequestTimeout
	}
	if c.now == nil {
		c.now = time.Now
	}
	if c.log == nil {
		c.log = slog.New(slog.DiscardHandler)
	}
	for _, k := range cfg.RateKeys {
		if k, ok := normKey(k); ok {
			c.keys[k] = struct{}{}
		}
	}
	for _, cur := range cfg.Currencies {
		if cur = normCurrency(cur); IsSupportedCurrency(cur) {
			c.currencies[cur] = struct{}{}
		}
	}
	return c
}

// Rate implements Provider.
func (c *CoinGecko) Rate(ctx context.Context, rateKey, currency string) (decimal.Decimal, error) {
	cur := normCurrency(currency)
	if !IsSupportedCurrency(cur) {
		return decimal.Decimal{}, fmt.Errorf("%w: %q", ErrUnsupportedCurrency, currency)
	}
	key, ok := normKey(rateKey)
	if !ok {
		return decimal.Decimal{}, fmt.Errorf("%w: invalid rate key %q", ErrUnknownAsset, rateKey)
	}
	p := pair{key, cur}
	for {
		if err := ctx.Err(); err != nil {
			return decimal.Decimal{}, err
		}
		c.mu.Lock()
		now := c.now()
		if r, ok, err := c.freshLocked(p, now); ok {
			c.mu.Unlock()
			return r, err
		}
		c.keys[key] = struct{}{}
		c.currencies[cur] = struct{}{}
		rf := c.inflight
		if rf == nil {
			if c.backingOffLocked(now) {
				r, err := c.fallbackLocked(p, now, c.lastErr)
				c.mu.Unlock()
				return r, err
			}
			rf = c.startLocked(ctx)
		}
		covers := rf.covers(p)
		c.mu.Unlock()

		select {
		case <-rf.done:
		case <-ctx.Done():
			return decimal.Decimal{}, ctx.Err()
		}
		if !covers {
			// The refresh we waited for started before this pair was known;
			// loop to trigger (or join) one that includes it.
			continue
		}
		c.mu.Lock()
		r, err := c.resultLocked(p, c.now(), rf.err)
		c.mu.Unlock()
		return r, err
	}
}

// Prefetch refreshes every known rate key × currency now, ignoring the TTL
// and the failure backoff (it joins a refresh already in flight when that one
// covers everything). It returns nil when nothing is known yet. Errors wrap
// ErrUnavailable or are the context's error.
func (c *CoinGecko) Prefetch(ctx context.Context) error {
	for {
		c.mu.Lock()
		if len(c.keys) == 0 {
			c.mu.Unlock()
			return nil
		}
		rf := c.inflight
		full := rf != nil && coversAll(rf, c.keys, c.currencies)
		if rf == nil {
			rf = c.startLocked(ctx)
			full = true
		}
		c.mu.Unlock()

		select {
		case <-rf.done:
		case <-ctx.Done():
			return ctx.Err()
		}
		if !full {
			continue
		}
		if rf.err != nil {
			return fmt.Errorf("%w: %v", ErrUnavailable, rf.err)
		}
		return nil
	}
}

func coversAll(rf *refresh, keys, curs map[string]struct{}) bool {
	for k := range keys {
		if _, ok := rf.keys[k]; !ok {
			return false
		}
	}
	for cur := range curs {
		if _, ok := rf.curs[cur]; !ok {
			return false
		}
	}
	return true
}

// freshLocked returns the cached answer for p when it is younger than the TTL.
func (c *CoinGecko) freshLocked(p pair, now time.Time) (decimal.Decimal, bool, error) {
	e, ok := c.cache[p]
	if !ok || now.Sub(e.at) >= c.ttl {
		return decimal.Decimal{}, false, nil
	}
	if e.missing {
		return decimal.Decimal{}, true, fmt.Errorf("%w: %s/%s", ErrUnknownAsset, p.key, p.cur)
	}
	return e.rate, true, nil
}

func (c *CoinGecko) backingOffLocked(now time.Time) bool {
	return c.lastErr != nil && c.backoff > 0 && now.Sub(c.lastFailAt) < c.backoff
}

// resultLocked answers p after a refresh that covered it finished with err.
func (c *CoinGecko) resultLocked(p pair, now time.Time, err error) (decimal.Decimal, error) {
	if err != nil {
		return c.fallbackLocked(p, now, err)
	}
	e, ok := c.cache[p]
	if !ok || e.missing {
		return decimal.Decimal{}, fmt.Errorf("%w: %s/%s", ErrUnknownAsset, p.key, p.cur)
	}
	return e.rate, nil
}

// fallbackLocked serves a stale cached price for p if allowed, else an
// ErrUnavailable error carrying cause.
func (c *CoinGecko) fallbackLocked(p pair, now time.Time, cause error) (decimal.Decimal, error) {
	if e, ok := c.cache[p]; ok && !e.missing && c.maxStale > 0 {
		if age := now.Sub(e.at); age <= c.maxStale {
			c.log.Warn("rates: serving stale price", "rate_key", p.key, "currency", p.cur,
				"age", age.Round(time.Second).String(), "error", cause)
			return e.rate, nil
		}
	}
	return decimal.Decimal{}, fmt.Errorf("%w: %s/%s: %v", ErrUnavailable, p.key, p.cur, cause)
}

// startLocked launches a refresh of every known key × currency. The request
// runs detached from ctx's cancellation so one impatient caller does not fail
// the others; it is bounded by the request timeout.
func (c *CoinGecko) startLocked(ctx context.Context) *refresh {
	rf := &refresh{
		done: make(chan struct{}),
		keys: maps.Clone(c.keys),
		curs: maps.Clone(c.currencies),
	}
	c.inflight = rf
	go c.run(context.WithoutCancel(ctx), rf)
	return rf
}

func (c *CoinGecko) run(ctx context.Context, rf *refresh) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	keys := slices.Sorted(maps.Keys(rf.keys))
	curs := slices.Sorted(maps.Keys(rf.curs))
	prices, err := c.fetch(ctx, keys, curs)

	c.mu.Lock()
	now := c.now()
	if err != nil {
		c.lastErr, c.lastFailAt = err, now
		c.log.Warn("rates: coingecko refresh failed", "error", err, "ids", len(keys), "currencies", len(curs))
	} else {
		c.lastErr, c.lastFailAt = nil, time.Time{}
		for _, k := range keys {
			for _, cur := range curs {
				if r, ok := prices[k][cur]; ok {
					c.cache[pair{k, cur}] = entry{rate: r, at: now}
				} else {
					c.cache[pair{k, cur}] = entry{missing: true, at: now}
				}
			}
		}
	}
	rf.err = err
	c.inflight = nil
	c.mu.Unlock()
	close(rf.done)
}

// fetch performs one /simple/price request and returns the positive prices
// found, indexed by coin id then currency.
func (c *CoinGecko) fetch(ctx context.Context, keys, curs []string) (map[string]map[string]decimal.Decimal, error) {
	q := url.Values{}
	q.Set("ids", strings.Join(keys, ","))
	q.Set("vs_currencies", strings.Join(curs, ","))
	q.Set("precision", "full")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/simple/price?"+q.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("coingecko: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		req.Header.Set(c.keyHeader, c.apiKey)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("coingecko: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("coingecko: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusTooManyRequests {
			return nil, fmt.Errorf("coingecko: rate limited (HTTP 429): %s", snippet(body))
		}
		return nil, fmt.Errorf("coingecko: HTTP %d: %s", resp.StatusCode, snippet(body))
	}
	return parsePrices(body, keys)
}

// parsePrices decodes a /simple/price body. Numbers are kept as their JSON
// text and parsed with decimal.NewFromString: no float64 rounding.
func parsePrices(body []byte, keys []string) (map[string]map[string]decimal.Decimal, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil || top == nil {
		return nil, fmt.Errorf("coingecko: malformed response: %s", snippet(body))
	}
	requested := map[string]bool{}
	for _, k := range keys {
		requested[k] = true
	}
	for _, errKey := range []string{"error", "status"} {
		if _, ok := top[errKey]; ok && !requested[errKey] {
			return nil, fmt.Errorf("coingecko: error response: %s", snippet(body))
		}
	}
	out := make(map[string]map[string]decimal.Decimal, len(top))
	for id, raw := range top {
		if !requested[id] {
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var vals map[string]any
		if err := dec.Decode(&vals); err != nil {
			continue // not an object: treated as "no price"
		}
		m := map[string]decimal.Decimal{}
		for cur, v := range vals {
			n, ok := v.(json.Number)
			if !ok {
				continue
			}
			d, err := decimal.NewFromString(n.String())
			if err != nil || !d.IsPositive() {
				continue
			}
			m[strings.ToLower(cur)] = d
		}
		out[id] = m
	}
	return out, nil
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
