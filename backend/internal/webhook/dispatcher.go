package webhook

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cryptopay/internal/auth"
	"cryptopay/internal/domain"
	"cryptopay/internal/store"
)

// Defaults and limits.
const (
	DefaultPollInterval = 2 * time.Second
	DefaultConcurrency  = 8
	UserAgent           = "CryptoPay-Webhook/1"

	baseBackoff = 15 * time.Second
	maxBackoff  = 6 * time.Hour
	maxSnippet  = store.MaxWebhookResponseBytes
	// DefaultJitter is the maximum random extra delay as a fraction of the backoff.
	DefaultJitter = 0.10
)

// Dispatcher delivers outbox events. Create with New; safe for concurrent use.
type Dispatcher struct {
	store        *store.Store
	log          *slog.Logger
	now          func() time.Time
	poll         time.Duration
	concurrency  int
	batch        int
	allowPrivate bool
	jitter       float64
	rt           http.RoundTripper // custom transport (tests); bypasses the guard
	lookup       lookupFunc
	client       *http.Client
}

// Option configures a Dispatcher.
type Option func(*Dispatcher)

// WithPollInterval sets how often Run looks for due events (default 2s).
func WithPollInterval(d time.Duration) Option {
	return func(x *Dispatcher) {
		if d > 0 {
			x.poll = d
		}
	}
}

// WithConcurrency bounds parallel deliveries per dispatcher (default 8).
func WithConcurrency(n int) Option {
	return func(x *Dispatcher) {
		if n > 0 {
			x.concurrency = n
		}
	}
}

// WithBatchSize sets how many events one claim takes (default = concurrency).
func WithBatchSize(n int) Option {
	return func(x *Dispatcher) {
		if n > 0 {
			x.batch = n
		}
	}
}

// AllowPrivateTargets disables the SSRF guard (local development, tests).
func AllowPrivateTargets(allow bool) Option { return func(x *Dispatcher) { x.allowPrivate = allow } }

// WithJitter sets the maximum random extra backoff as a fraction of the
// delay (default 0.10; 0 disables jitter).
func WithJitter(f float64) Option {
	return func(x *Dispatcher) {
		if f >= 0 {
			x.jitter = f
		}
	}
}

// WithTransport replaces the HTTP transport. The SSRF guard lives in the
// default transport's dialer, so a custom transport bypasses it; intended
// for tests.
func WithTransport(rt http.RoundTripper) Option { return func(x *Dispatcher) { x.rt = rt } }

// WithResolver replaces the DNS lookup used by the SSRF-guarded dialer.
func WithResolver(r *net.Resolver) Option {
	return func(x *Dispatcher) { x.lookup = netLookup(r) }
}

// New creates a Dispatcher. now may be nil (time.Now); log may be nil.
func New(st *store.Store, log *slog.Logger, now func() time.Time, opts ...Option) *Dispatcher {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if now == nil {
		now = time.Now
	}
	d := &Dispatcher{
		store: st, log: log, now: now,
		poll: DefaultPollInterval, concurrency: DefaultConcurrency, jitter: DefaultJitter,
		lookup: netLookup(nil),
	}
	for _, o := range opts {
		o(d)
	}
	if d.batch == 0 {
		d.batch = d.concurrency
	}
	if d.rt != nil {
		d.client = &http.Client{Transport: d.rt}
	} else {
		tr := &http.Transport{
			Proxy:                  nil, // never route through env proxies: they would bypass the guard
			DisableKeepAlives:      true,
			TLSHandshakeTimeout:    10 * time.Second,
			ResponseHeaderTimeout:  0, // bounded by the per-request context
			MaxResponseHeaderBytes: 64 << 10,
		}
		if d.allowPrivate {
			tr.DialContext = (&net.Dialer{Timeout: 10 * time.Second}).DialContext
		} else {
			tr.DialContext = guardedDialContext(d.lookup, 10*time.Second)
		}
		d.client = &http.Client{Transport: tr}
	}
	d.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return d
}

// Run polls and delivers until ctx is done, then returns nil. Errors of a
// single pass are logged and do not stop the loop.
func (d *Dispatcher) Run(ctx context.Context) error {
	t := time.NewTicker(d.poll)
	defer t.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		ok, f, err := d.DeliverDue(ctx)
		switch {
		case ctx.Err() != nil:
			return nil
		case err != nil:
			d.log.Error("webhook pass failed", "err", err)
		case ok+f > 0:
			d.log.Debug("webhook pass", "delivered", ok, "failed", f)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// DeliverDue claims and delivers every currently due event (repeating the
// claim while batches come back full) and returns how many attempts
// succeeded and failed. Failed events are rescheduled in the future, so the
// pass terminates.
func (d *Dispatcher) DeliverDue(ctx context.Context) (delivered, failed int, err error) {
	settings, serr := d.store.GetSettings(ctx)
	if serr != nil {
		if !errors.Is(serr, store.ErrNotFound) {
			return 0, 0, fmt.Errorf("webhook: load settings: %w", serr)
		}
		settings = domain.DefaultSettings()
	}
	timeout := time.Duration(max(settings.WebhookTimeoutSeconds, 1)) * time.Second
	maxAttempts := max(settings.WebhookMaxAttempts, 1)
	waves := (d.batch + d.concurrency - 1) / d.concurrency
	lease := timeout*time.Duration(waves+1) + 30*time.Second

	var nOK, nFail atomic.Int64
	for {
		events, cerr := d.store.ClaimDueWebhooks(ctx, d.now(), d.batch, lease)
		if cerr != nil {
			return int(nOK.Load()), int(nFail.Load()), fmt.Errorf("webhook: claim: %w", cerr)
		}
		var wg sync.WaitGroup
		sem := make(chan struct{}, d.concurrency)
		for _, e := range events {
			sem <- struct{}{}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				switch d.deliverSafe(ctx, e, timeout, maxAttempts) {
				case outcomeDelivered:
					nOK.Add(1)
				case outcomeFailed:
					nFail.Add(1)
				}
			}()
		}
		wg.Wait()
		if len(events) < d.batch || ctx.Err() != nil {
			break
		}
	}
	return int(nOK.Load()), int(nFail.Load()), ctx.Err()
}

type outcome int

const (
	outcomeSkipped outcome = iota // not recorded (shutdown, lost race)
	outcomeDelivered
	outcomeFailed
)

func (d *Dispatcher) deliverSafe(ctx context.Context, e domain.WebhookEvent, timeout time.Duration, maxAttempts int) (out outcome) {
	defer func() {
		if r := recover(); r != nil {
			d.log.Error("webhook delivery panicked", "event", e.ID, "panic", fmt.Sprint(r))
			out = d.fail(ctx, e, store.WebhookAttempt{Error: fmt.Sprintf("internal error: panic: %v", r)}, maxAttempts, false)
		}
	}()
	return d.deliver(ctx, e, timeout, maxAttempts)
}

func (d *Dispatcher) deliver(ctx context.Context, e domain.WebhookEvent, timeout time.Duration, maxAttempts int) outcome {
	m, err := d.store.GetMerchant(ctx, e.MerchantID)
	switch {
	case ctx.Err() != nil:
		return outcomeSkipped
	case errors.Is(err, store.ErrNotFound):
		return d.fail(ctx, e, store.WebhookAttempt{Error: "merchant no longer exists; delivery abandoned"}, maxAttempts, true)
	case err != nil:
		// Transient store problem: leave the claim to expire, retried after the lease.
		d.log.Error("webhook: load merchant", "event", e.ID, "err", err)
		return outcomeSkipped
	case m.Status != domain.MerchantActive:
		return d.fail(ctx, e, store.WebhookAttempt{Error: "merchant is " + string(m.Status) + "; delivery abandoned"}, maxAttempts, true)
	case m.WebhookSecret == "":
		return d.fail(ctx, e, store.WebhookAttempt{Error: "merchant has no webhook secret; delivery abandoned"}, maxAttempts, true)
	}

	att := d.send(ctx, e, m.WebhookSecret, timeout)
	if ctx.Err() != nil && att.StatusCode == 0 {
		return outcomeSkipped // shutting down: let the lease expire
	}
	if att.Error == "" {
		return d.mark(ctx, e, func(c context.Context) error {
			_, err := d.store.MarkWebhookDelivered(c, e.ID, att)
			return err
		}, outcomeDelivered)
	}
	return d.fail(ctx, e, att, maxAttempts, false)
}

// fail records a failed attempt. permanent forces the event to status failed.
func (d *Dispatcher) fail(ctx context.Context, e domain.WebhookEvent, att store.WebhookAttempt, maxAttempts int, permanent bool) outcome {
	now := d.now()
	att.At = now
	attempt := e.Attempts + 1
	next := now.Add(d.backoff(attempt))
	if permanent {
		maxAttempts = attempt
	}
	d.log.Warn("webhook attempt failed", "event", e.ID, "merchant", e.MerchantID, "attempt", attempt,
		"status", att.StatusCode, "error", att.Error, "permanent", permanent || attempt >= maxAttempts)
	return d.mark(ctx, e, func(c context.Context) error {
		_, err := d.store.MarkWebhookFailed(c, e.ID, att, next, maxAttempts)
		return err
	}, outcomeFailed)
}

func (d *Dispatcher) mark(ctx context.Context, e domain.WebhookEvent, f func(context.Context) error, ok outcome) outcome {
	// Detach from ctx so a shutdown does not lose an attempt that completed.
	c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := f(c); err != nil {
		if errors.Is(err, store.ErrConflict) {
			d.log.Warn("webhook: event already finalised by another worker", "event", e.ID)
		} else {
			d.log.Error("webhook: record outcome", "event", e.ID, "err", err)
		}
		return outcomeSkipped
	}
	return ok
}

// backoff returns min(2^attempt x 15s, 6h) plus jitter (attempt >= 1).
func (d *Dispatcher) backoff(attempt int) time.Duration {
	delay := maxBackoff
	if attempt < 20 {
		delay = min(baseBackoff<<uint(attempt), maxBackoff)
	}
	if d.jitter > 0 {
		delay += time.Duration(rand.Float64() * d.jitter * float64(delay))
	}
	return min(delay, maxBackoff)
}

// send performs one HTTP attempt. A zero Error means success (2xx).
func (d *Dispatcher) send(ctx context.Context, e domain.WebhookEvent, secret string, timeout time.Duration) store.WebhookAttempt {
	u, err := url.Parse(e.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return store.WebhookAttempt{Error: "invalid webhook url: only absolute http/https URLs are allowed"}
	}
	if !d.allowPrivate {
		// Fast, clear refusal for literal addresses; the dialer enforces the rest.
		if ip, perr := netip.ParseAddr(u.Hostname()); perr == nil && isBlockedIP(ip) {
			return store.WebhookAttempt{Error: fmt.Sprintf("%v: %s", ErrBlockedTarget, ip.Unmap())}
		}
	}
	body := []byte(e.Payload)
	ts := d.now().Unix()
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, e.URL, bytes.NewReader(body))
	if err != nil {
		return store.WebhookAttempt{Error: "build request: " + err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set(auth.HeaderEvent, e.EventType)
	req.Header.Set(auth.HeaderDelivery, e.ID.String())
	req.Header.Set(auth.HeaderTimestamp, strconv.FormatInt(ts, 10))
	req.Header.Set(auth.HeaderSignature, auth.Sign(secret, ts, body))

	resp, err := d.client.Do(req)
	if err != nil {
		return store.WebhookAttempt{Error: describeErr(rctx, err, timeout)}
	}
	defer resp.Body.Close()
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxSnippet))
	att := store.WebhookAttempt{StatusCode: resp.StatusCode, Response: strings.ToValidUTF8(string(snippet), "�")}
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		att.Error = fmt.Sprintf("HTTP %d: redirects are not followed", resp.StatusCode)
	default:
		att.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	return att
}

// describeErr turns a transport error into a concise message without the
// request URL (which may carry credentials).
func describeErr(rctx context.Context, err error, timeout time.Duration) string {
	if errors.Is(rctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Sprintf("timeout after %s", timeout)
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return "request failed: " + err.Error()
}
