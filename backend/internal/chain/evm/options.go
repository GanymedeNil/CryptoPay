package evm

import (
	"errors"
	"log/slog"
	"net/http"
	"time"
)

// Option configures a Client.
type Option func(*config)

type config struct {
	httpClient     *http.Client
	requestTimeout time.Duration
	retries        int
	backoffBase    time.Duration
	backoffMax     time.Duration
	batchSize      int
	concurrency    int
	chunkSize      int
	maxResponse    int64
	cooldown       time.Duration
	chainIDCheck   bool
	logger         *slog.Logger
}

// Defaults. They are conservative so the free public endpoints work too.
const (
	DefaultRequestTimeout   = 30 * time.Second
	DefaultRetries          = 3
	DefaultBackoffBase      = 250 * time.Millisecond
	DefaultBackoffMax       = 5 * time.Second
	DefaultBatchSize        = 10
	DefaultConcurrency      = 4
	DefaultAddressChunkSize = 100
	DefaultMaxResponseSize  = 128 << 20
	DefaultEndpointCooldown = 30 * time.Second
)

func defaultConfig() config {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxIdleConnsPerHost = 16
	return config{
		httpClient:     &http.Client{Transport: tr},
		requestTimeout: DefaultRequestTimeout,
		retries:        DefaultRetries,
		backoffBase:    DefaultBackoffBase,
		backoffMax:     DefaultBackoffMax,
		batchSize:      DefaultBatchSize,
		concurrency:    DefaultConcurrency,
		chunkSize:      DefaultAddressChunkSize,
		maxResponse:    DefaultMaxResponseSize,
		cooldown:       DefaultEndpointCooldown,
		chainIDCheck:   true,
		logger:         slog.New(slog.DiscardHandler),
	}
}

func (c *config) validate() error {
	switch {
	case c.httpClient == nil:
		return errors.New("http client is nil")
	case c.requestTimeout <= 0:
		return errors.New("request timeout must be > 0")
	case c.retries < 0:
		return errors.New("retries must be >= 0")
	case c.backoffBase < 0 || c.backoffMax < c.backoffBase:
		return errors.New("backoff must satisfy 0 <= base <= max")
	case c.batchSize < 1:
		return errors.New("batch size must be >= 1")
	case c.concurrency < 1:
		return errors.New("concurrency must be >= 1")
	case c.chunkSize < 1:
		return errors.New("address chunk size must be >= 1")
	case c.maxResponse < 1:
		return errors.New("max response size must be >= 1")
	case c.cooldown < 0:
		return errors.New("endpoint cooldown must be >= 0")
	}
	return nil
}

// WithHTTPClient sets the HTTP client used for all requests. Its Timeout, if
// any, applies in addition to WithRequestTimeout.
func WithHTTPClient(hc *http.Client) Option { return func(c *config) { c.httpClient = hc } }

// WithRequestTimeout bounds one HTTP attempt (connect, request and reading the
// response). Default DefaultRequestTimeout.
func WithRequestTimeout(d time.Duration) Option { return func(c *config) { c.requestTimeout = d } }

// WithRetries sets how many times a failed request is retried (each retry
// moves to the next endpoint). Only transport errors, timeouts, HTTP 5xx /
// 408 / 429 / 401 / 403 / 404, rate limits and transient node errors are
// retried. Default DefaultRetries.
func WithRetries(n int) Option { return func(c *config) { c.retries = n } }

// WithBackoff sets the exponential backoff between retries: base·2^(n-1),
// capped at max, with jitter. Defaults DefaultBackoffBase, DefaultBackoffMax.
func WithBackoff(base, maxDelay time.Duration) Option {
	return func(c *config) { c.backoffBase, c.backoffMax = base, maxDelay }
}

// WithBatchSize sets the maximum number of calls in one JSON-RPC batch
// request; 1 disables batching. Full blocks are fetched in batches of this
// size, so it also bounds response size. Default DefaultBatchSize.
func WithBatchSize(n int) Option { return func(c *config) { c.batchSize = n } }

// WithConcurrency sets how many batch requests one scan keeps in flight.
// Default DefaultConcurrency.
func WithConcurrency(n int) Option { return func(c *config) { c.concurrency = n } }

// WithAddressChunkSize sets how many watched addresses (and token contracts)
// go into one eth_getLogs filter. Providers cap topic OR-lists; larger watch
// lists are split into several eth_getLogs calls. Default
// DefaultAddressChunkSize.
func WithAddressChunkSize(n int) Option { return func(c *config) { c.chunkSize = n } }

// WithMaxResponseSize bounds the body of one HTTP response in bytes.
// Default DefaultMaxResponseSize.
func WithMaxResponseSize(n int64) Option { return func(c *config) { c.maxResponse = n } }

// WithEndpointCooldown sets how long an endpoint that failed is skipped in
// favour of the next one before it is preferred again. Default
// DefaultEndpointCooldown.
func WithEndpointCooldown(d time.Duration) Option { return func(c *config) { c.cooldown = d } }

// WithChainIDCheck enables or disables verifying eth_chainId of every
// endpoint against domain.Chain.NetworkID before first use (enabled by
// default; skipped when NetworkID is empty).
func WithChainIDCheck(enabled bool) Option { return func(c *config) { c.chainIDCheck = enabled } }

// WithLogger sets the logger for retry / failover warnings. Log records carry
// the endpoint host only, never the URL. Default: discard.
func WithLogger(l *slog.Logger) Option {
	return func(c *config) {
		if l != nil {
			c.logger = l
		}
	}
}
