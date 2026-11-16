package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

type ctxKey int

const (
	keyInfo ctxKey = iota
	keyMerchant
	keyAdmin
)

// requestInfo is per-request metadata shared between middlewares. The
// authentication middlewares fill in who is calling so the access log can
// record it.
type reqInfo struct {
	ID         string
	IP         string
	MerchantID string
	AdminID    string
}

func infoFrom(ctx context.Context) *reqInfo {
	if v, ok := ctx.Value(keyInfo).(*reqInfo); ok {
		return v
	}
	return &reqInfo{}
}

func requestIDFrom(ctx context.Context) string { return infoFrom(ctx).ID }

// clientIPFrom returns the client IP determined by the requestInfo
// middleware.
func clientIPFrom(ctx context.Context) string { return infoFrom(ctx).IP }

// requestInfo assigns the request id (a well-formed incoming X-Request-ID
// is kept, else a random one) and determines the client IP.
func (a *API) requestInfo(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if !validRequestID(id) {
			id = newRequestID()
		}
		w.Header().Set("X-Request-ID", id)
		info := &reqInfo{ID: id, IP: a.clientIP(r)}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), keyInfo, info)))
	})
}

func validRequestID(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

func newRequestID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// clientIP returns the address of the client. Forwarding headers are only
// honoured when the direct peer is a trusted proxy (config
// TRUST_PROXY_HEADERS / TRUSTED_PROXIES): X-Forwarded-For is walked from the
// right, skipping trusted proxies, and the first untrusted hop is the
// client; X-Real-IP is used when there is no X-Forwarded-For.
func (a *API) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	peer = peer.Unmap()
	if !a.cfg.IsTrustedProxy(peer) {
		return peer.String()
	}
	var hops []string
	for _, h := range r.Header.Values("X-Forwarded-For") {
		for _, p := range strings.Split(h, ",") {
			if p = strings.TrimSpace(p); p != "" {
				hops = append(hops, p)
			}
		}
	}
	if len(hops) == 0 {
		if ip, ok := parseHop(r.Header.Get("X-Real-IP")); ok {
			return ip.String()
		}
		return peer.String()
	}
	client := peer
	for i := len(hops) - 1; i >= 0; i-- {
		ip, ok := parseHop(hops[i])
		if !ok {
			break
		}
		client = ip
		if !a.cfg.IsTrustedProxy(ip) {
			break
		}
	}
	return client.String()
}

func parseHop(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return netip.Addr{}, false
	}
	if ip, err := netip.ParseAddr(strings.Trim(s, "[]")); err == nil {
		return ip.Unmap(), true
	}
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().Unmap(), true
	}
	return netip.Addr{}, false
}

// recorder captures the status and size of a response.
type recorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (rw *recorder) WriteHeader(code int) {
	if rw.status == 0 {
		rw.status = code
	}
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *recorder) Write(b []byte) (int, error) {
	if rw.status == 0 {
		rw.status = http.StatusOK
	}
	n, err := rw.ResponseWriter.Write(b)
	rw.bytes += n
	return n, err
}

func (rw *recorder) Unwrap() http.ResponseWriter { return rw.ResponseWriter }

// accessLog writes one structured line per request. It deliberately never
// logs headers (Authorization, cookies), query strings or bodies.
func (a *API) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &recorder{ResponseWriter: w}
		next.ServeHTTP(rw, r)
		info := infoFrom(r.Context())
		route := ""
		if rc := chi.RouteContext(r.Context()); rc != nil {
			route = rc.RoutePattern()
		}
		path := r.URL.Path
		if strings.HasPrefix(path, "/api/checkout/") {
			// The invoice id is the checkout's bearer secret.
			path = route
		}
		status := rw.status
		if status == 0 {
			status = http.StatusOK
		}
		level := slog.LevelInfo
		switch {
		case status >= 500 && status != 503:
			level = slog.LevelError
		case r.URL.Path == "/healthz" || r.URL.Path == "/readyz":
			level = slog.LevelDebug
		}
		attrs := []slog.Attr{
			slog.String("request_id", info.ID),
			slog.String("method", r.Method),
			slog.String("path", path),
			slog.String("route", route),
			slog.Int("status", status),
			slog.Int("bytes", rw.bytes),
			slog.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000),
			slog.String("ip", info.IP),
		}
		if info.MerchantID != "" {
			attrs = append(attrs, slog.String("merchant_id", info.MerchantID))
		}
		if info.AdminID != "" {
			attrs = append(attrs, slog.String("admin_id", info.AdminID))
		}
		a.log.LogAttrs(r.Context(), level, "http request", attrs...)
	})
}

// recoverer turns a panic into a logged 500 JSON response.
func (a *API) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rw, _ := w.(*recorder)
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			if v == http.ErrAbortHandler {
				panic(v)
			}
			a.log.ErrorContext(r.Context(), "panic serving request", "request_id", requestIDFrom(r.Context()),
				"panic", fmt.Sprint(v), "stack", string(debug.Stack()))
			if rw != nil && rw.status != 0 {
				return // headers already sent
			}
			a.fail(w, r, &Error{Status: http.StatusInternalServerError, Code: CodeInternal, Message: "internal server error"})
		}()
		next.ServeHTTP(w, r)
	})
}

// securityHeaders sets headers appropriate for a JSON API.
func (a *API) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		h.Set("Cache-Control", "no-store")
		if a.cfg.Production() {
			h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

// withTimeout bounds the request's context.
func (a *API) withTimeout(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), a.timeout)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// limitBody caps the request body at MaxBodyBytes.
func (a *API) limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > MaxBodyBytes {
			a.fail(w, r, errInvalid(fmt.Sprintf("the request body is larger than %d bytes", MaxBodyBytes), nil))
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)
		next.ServeHTTP(w, r)
	})
}

// cors implements CORS for the merchant API when CORS_ORIGINS is
// configured (browsers calling /api/v1 directly; API keys belong on servers,
// so this is opt-in). Credentials (cookies) are never allowed.
func (a *API) cors(next http.Handler) http.Handler {
	origins := a.cfg.CORSOrigins
	wildcard := slices.Contains(origins, "*")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" || len(origins) == 0 {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Add("Vary", "Origin")
		allowed := wildcard || slices.Contains(origins, strings.ToLower(strings.TrimRight(origin, "/")))
		if !allowed {
			next.ServeHTTP(w, r)
			return
		}
		h := w.Header()
		if wildcard {
			h.Set("Access-Control-Allow-Origin", "*")
		} else {
			h.Set("Access-Control-Allow-Origin", origin)
		}
		h.Set("Access-Control-Expose-Headers", "X-Request-ID, Retry-After")
		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			h.Add("Vary", "Access-Control-Request-Method")
			h.Add("Vary", "Access-Control-Request-Headers")
			h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-ID")
			h.Set("Access-Control-Max-Age", "600")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// rateLimited writes a 429 with Retry-After.
func (a *API) rateLimited(w http.ResponseWriter, r *http.Request, retryAfter time.Duration) {
	secs := int((retryAfter + time.Second - 1) / time.Second)
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", fmt.Sprint(secs))
	a.fail(w, r, &Error{Status: http.StatusTooManyRequests, Code: CodeRateLimited, Message: "too many requests, slow down"})
}

// limitByIP applies a per-client-IP rate limit.
func (a *API) limitByIP(l *keyedLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if ok, wait := l.allow(clientIPFrom(r.Context())); !ok {
				a.rateLimited(w, r, wait)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
