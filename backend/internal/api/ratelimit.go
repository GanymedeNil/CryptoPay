package api

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Limit is a token bucket: Rate tokens per second, at most Burst at once.
type Limit struct {
	Rate  rate.Limit
	Burst int
}

// Limits configures the in-memory rate limiters. Zero fields select the
// defaults (see DefaultLimits). Limits are per process; with several
// gateway instances each enforces its own.
type Limits struct {
	MerchantKey   Limit // per merchant API key, all /api/v1 requests
	CheckoutGet   Limit // per client IP, GET /api/checkout/{id} (polled every 3 s)
	CheckoutQuote Limit // per client IP, POST /api/checkout/{id}/quote
	LoginIP       Limit // per client IP, POST /api/admin/auth/login

	// LoginFailures failed logins for one (IP, e-mail) pair within
	// LoginWindow block further attempts for that pair (429) until the
	// window has passed.
	LoginFailures int
	LoginWindow   time.Duration
	// EmailTest limits POST /email/test per user.
	EmailTest Limit
}

// DefaultLimits are the production rate limits.
var DefaultLimits = Limits{
	MerchantKey:   Limit{Rate: 20, Burst: 60},
	CheckoutGet:   Limit{Rate: 3, Burst: 60},
	CheckoutQuote: Limit{Rate: rate.Every(6 * time.Second), Burst: 10},
	LoginIP:       Limit{Rate: rate.Every(3 * time.Second), Burst: 20},
	LoginFailures: 5,
	LoginWindow:   15 * time.Minute,
	EmailTest:     Limit{Rate: rate.Every(10 * time.Second), Burst: 5},
}

func (l Limits) withDefaults() Limits {
	d := DefaultLimits
	pick := func(v, def Limit) Limit {
		if v.Rate == 0 && v.Burst == 0 {
			return def
		}
		return v
	}
	l.MerchantKey = pick(l.MerchantKey, d.MerchantKey)
	l.CheckoutGet = pick(l.CheckoutGet, d.CheckoutGet)
	l.CheckoutQuote = pick(l.CheckoutQuote, d.CheckoutQuote)
	l.LoginIP = pick(l.LoginIP, d.LoginIP)
	l.EmailTest = pick(l.EmailTest, d.EmailTest)
	if l.LoginFailures <= 0 {
		l.LoginFailures = d.LoginFailures
	}
	if l.LoginWindow <= 0 {
		l.LoginWindow = d.LoginWindow
	}
	return l
}

// keyedLimiter is a set of token buckets keyed by string (API key id, IP).
// Idle buckets are evicted so the map cannot grow without bound.
type keyedLimiter struct {
	limit Limit
	now   func() time.Time

	mu        sync.Mutex
	buckets   map[string]*bucket
	lastSweep time.Time
}

type bucket struct {
	lim  *rate.Limiter
	seen time.Time
}

const (
	limiterIdleTTL   = 10 * time.Minute
	limiterSweepEach = time.Minute
	limiterMaxKeys   = 100_000
)

func newKeyedLimiter(l Limit, now func() time.Time) *keyedLimiter {
	return &keyedLimiter{limit: l, now: now, buckets: map[string]*bucket{}}
}

// allow takes one token from key's bucket. When none is available it
// returns false and how long until one will be.
func (k *keyedLimiter) allow(key string) (bool, time.Duration) {
	now := k.now()
	k.mu.Lock()
	defer k.mu.Unlock()
	k.sweep(now)
	b, ok := k.buckets[key]
	if !ok {
		b = &bucket{lim: rate.NewLimiter(k.limit.Rate, k.limit.Burst)}
		k.buckets[key] = b
	}
	b.seen = now
	res := b.lim.ReserveN(now, 1)
	if !res.OK() {
		return false, time.Minute
	}
	if d := res.DelayFrom(now); d > 0 {
		res.CancelAt(now)
		return false, d
	}
	return true, 0
}

// sweep drops buckets idle for limiterIdleTTL (at most once a minute, or
// whenever the map is too large).
func (k *keyedLimiter) sweep(now time.Time) {
	if now.Sub(k.lastSweep) < limiterSweepEach && len(k.buckets) < limiterMaxKeys {
		return
	}
	k.lastSweep = now
	ttl := limiterIdleTTL
	if len(k.buckets) >= limiterMaxKeys {
		ttl = time.Minute // under pressure: forget anything not seen very recently
	}
	for key, b := range k.buckets {
		if now.Sub(b.seen) >= ttl {
			delete(k.buckets, key)
		}
	}
}

func (k *keyedLimiter) size() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.buckets)
}

// failureThrottle counts failures per key in a fixed window and blocks a
// key that reached max failures until its window ends.
type failureThrottle struct {
	max    int
	window time.Duration
	now    func() time.Time

	mu        sync.Mutex
	entries   map[string]*failures
	lastSweep time.Time
}

type failures struct {
	count int
	start time.Time
}

func newFailureThrottle(max int, window time.Duration, now func() time.Time) *failureThrottle {
	return &failureThrottle{max: max, window: window, now: now, entries: map[string]*failures{}}
}

// blocked reports whether key is currently blocked and for how long.
func (f *failureThrottle) blocked(key string) (bool, time.Duration) {
	now := f.now()
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.entries[key]
	if !ok {
		return false, 0
	}
	end := e.start.Add(f.window)
	if !now.Before(end) {
		delete(f.entries, key)
		return false, 0
	}
	if e.count >= f.max {
		return true, end.Sub(now)
	}
	return false, 0
}

// fail records a failure for key. It reports whether this failure is the
// one that makes key blocked (so the caller can react once per block).
func (f *failureThrottle) fail(key string) (tripped bool) {
	now := f.now()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sweep(now)
	e, ok := f.entries[key]
	if !ok || !now.Before(e.start.Add(f.window)) {
		f.entries[key] = &failures{count: 1, start: now}
		return f.max <= 1
	}
	e.count++
	return e.count == f.max
}

// reset forgets key's failures (after a successful login).
func (f *failureThrottle) reset(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.entries, key)
}

func (f *failureThrottle) sweep(now time.Time) {
	if now.Sub(f.lastSweep) < limiterSweepEach && len(f.entries) < limiterMaxKeys {
		return
	}
	f.lastSweep = now
	for key, e := range f.entries {
		if !now.Before(e.start.Add(f.window)) {
			delete(f.entries, key)
		}
	}
}
