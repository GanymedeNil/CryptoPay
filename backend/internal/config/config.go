// Package config loads the gateway configuration from environment variables.
//
// Variables (defaults in brackets):
//
//	DATABASE_URL          PostgreSQL URL (required)
//	APP_SECRET            >= 32 characters; keys the encryption of secrets at rest (required)
//	HTTP_ADDR             listen address [":8080"]
//	APP_ENV               "development" | "production" ["development"]; production ⇒ Secure cookies
//	LOG_LEVEL             "debug" | "info" | "warn" | "error" ["info"]
//	COINGECKO_API_KEY     optional CoinGecko API key
//	COINGECKO_PRO         bool: the key is a Pro key (x-cg-pro-api-key, pro base URL) [false]
//	COINGECKO_BASE_URL    optional override of the CoinGecko API base URL
//	ADMIN_EMAIL           optional: bootstrap an owner account on first run …
//	ADMIN_PASSWORD        … with this password (both or neither, min 8 chars)
//	CORS_ORIGINS          comma separated allowed origins ("https://a.example,https://b.example" or "*")
//	TRUST_PROXY_HEADERS   bool: honour X-Forwarded-For / X-Real-IP [false]
//	TRUSTED_PROXIES       comma separated IPs/CIDRs of trusted reverse proxies; setting it
//	                      implies TRUST_PROXY_HEADERS (a bool value is accepted as an alias)
//	SCANNER_ENABLED       bool: run the chain scanners in this process [true]
//	RPC_<CHAIN_ID>        per-chain RPC URLs, see RPCFromEnv
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// Env is the deployment environment.
type Env string

const (
	// EnvDevelopment is the default: relaxed cookies, verbose defaults.
	EnvDevelopment Env = "development"
	// EnvProduction enables Secure cookies and production hardening.
	EnvProduction Env = "production"
)

// MinAppSecretLength is the minimum accepted length of APP_SECRET.
const MinAppSecretLength = 32

// MinAdminPasswordLength is the minimum length of ADMIN_PASSWORD.
const MinAdminPasswordLength = 8

// DefaultHTTPAddr is the default listen address.
const DefaultHTTPAddr = ":8080"

// Config is the validated process configuration.
type Config struct {
	DatabaseURL string
	AppSecret   string
	HTTPAddr    string
	Env         Env
	LogLevel    slog.Level

	CoinGeckoAPIKey  string
	CoinGeckoPro     bool
	CoinGeckoBaseURL string // "" = provider default

	// AdminEmail / AdminPassword bootstrap an owner on first run (both set or both empty).
	AdminEmail    string
	AdminPassword string

	// CORSOrigins are the allowed browser origins; ["*"] allows any origin.
	CORSOrigins []string

	// TrustProxyHeaders makes the HTTP layer take the client IP from
	// X-Forwarded-For / X-Real-IP. When TrustedProxies is non-empty the
	// headers should only be honoured if the direct peer is in that list.
	TrustProxyHeaders bool
	TrustedProxies    []netip.Prefix

	// ScannerEnabled runs the chain scanners and background workers.
	ScannerEnabled bool
}

// Production reports whether APP_ENV is production.
func (c Config) Production() bool { return c.Env == EnvProduction }

// SecureCookies reports whether session cookies must carry the Secure flag.
func (c Config) SecureCookies() bool { return c.Production() }

// BootstrapAdmin reports whether ADMIN_EMAIL / ADMIN_PASSWORD are configured.
func (c Config) BootstrapAdmin() bool { return c.AdminEmail != "" && c.AdminPassword != "" }

// IsTrustedProxy reports whether a direct peer address may set forwarding
// headers: always false when TrustProxyHeaders is off, always true when it is
// on and no TrustedProxies are configured.
func (c Config) IsTrustedProxy(peer netip.Addr) bool {
	if !c.TrustProxyHeaders {
		return false
	}
	if len(c.TrustedProxies) == 0 {
		return true
	}
	peer = peer.Unmap()
	for _, p := range c.TrustedProxies {
		if p.Contains(peer) {
			return true
		}
	}
	return false
}

// LogValue implements slog.LogValuer and redacts secrets so the
// configuration can be logged at startup.
func (c Config) LogValue() slog.Value {
	proxies := make([]string, len(c.TrustedProxies))
	for i, p := range c.TrustedProxies {
		proxies[i] = p.String()
	}
	return slog.GroupValue(
		slog.String("database_url", RedactURL(c.DatabaseURL)),
		slog.String("app_secret", redact(c.AppSecret)),
		slog.String("http_addr", c.HTTPAddr),
		slog.String("env", string(c.Env)),
		slog.String("log_level", c.LogLevel.String()),
		slog.String("coingecko_api_key", redact(c.CoinGeckoAPIKey)),
		slog.Bool("coingecko_pro", c.CoinGeckoPro),
		slog.String("coingecko_base_url", c.CoinGeckoBaseURL),
		slog.String("admin_email", c.AdminEmail),
		slog.String("admin_password", redact(c.AdminPassword)),
		slog.String("cors_origins", strings.Join(c.CORSOrigins, ",")),
		slog.Bool("trust_proxy_headers", c.TrustProxyHeaders),
		slog.String("trusted_proxies", strings.Join(proxies, ",")),
		slog.Bool("scanner_enabled", c.ScannerEnabled),
	)
}

func redact(s string) string {
	if s == "" {
		return ""
	}
	return "[redacted]"
}

// RedactURL removes the password (and query string) from a URL for logging.
// Inputs that are not URLs are fully redacted.
func RedactURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return "[redacted]"
	}
	if _, has := u.User.Password(); has {
		u.User = url.UserPassword(u.User.Username(), "xxxxx")
	}
	u.RawQuery = ""
	return u.String()
}

// Load reads and validates the configuration from the process environment.
func Load() (Config, error) { return LoadFrom(os.Getenv) }

// LoadFrom is Load with an injectable variable lookup (getenv returns "" for
// unset variables).
func LoadFrom(getenv func(string) string) (Config, error) {
	get := func(k string) string { return strings.TrimSpace(getenv(k)) }
	var errs []error
	fail := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	c := Config{
		DatabaseURL:      get("DATABASE_URL"),
		AppSecret:        getenv("APP_SECRET"), // not trimmed: whitespace may be part of it
		HTTPAddr:         get("HTTP_ADDR"),
		CoinGeckoAPIKey:  get("COINGECKO_API_KEY"),
		CoinGeckoBaseURL: get("COINGECKO_BASE_URL"),
		AdminEmail:       get("ADMIN_EMAIL"),
		AdminPassword:    getenv("ADMIN_PASSWORD"),
	}

	if c.DatabaseURL == "" {
		fail("DATABASE_URL is required")
	}
	if c.AppSecret == "" {
		fail("APP_SECRET is required")
	} else if len(c.AppSecret) < MinAppSecretLength {
		fail("APP_SECRET must be at least %d characters (got %d)", MinAppSecretLength, len(c.AppSecret))
	}
	if c.HTTPAddr == "" {
		c.HTTPAddr = DefaultHTTPAddr
	}

	switch env := strings.ToLower(get("APP_ENV")); env {
	case "", "dev", "development":
		c.Env = EnvDevelopment
	case "prod", "production":
		c.Env = EnvProduction
	default:
		fail("APP_ENV must be development or production, got %q", env)
	}

	if lvl := get("LOG_LEVEL"); lvl != "" {
		if err := c.LogLevel.UnmarshalText([]byte(lvl)); err != nil {
			fail("LOG_LEVEL: invalid level %q (debug, info, warn, error)", lvl)
		}
	} else {
		c.LogLevel = slog.LevelInfo
	}

	var err error
	if c.CoinGeckoPro, err = parseBool(get("COINGECKO_PRO"), false); err != nil {
		fail("COINGECKO_PRO: %v", err)
	}
	if c.CoinGeckoBaseURL != "" {
		if err := checkHTTPURL(c.CoinGeckoBaseURL); err != nil {
			fail("COINGECKO_BASE_URL: %v", err)
		}
		c.CoinGeckoBaseURL = strings.TrimRight(c.CoinGeckoBaseURL, "/")
	}

	switch {
	case c.AdminEmail == "" && c.AdminPassword == "":
	case c.AdminEmail == "" || c.AdminPassword == "":
		fail("ADMIN_EMAIL and ADMIN_PASSWORD must be set together")
	default:
		if !strings.Contains(c.AdminEmail, "@") {
			fail("ADMIN_EMAIL %q is not an e-mail address", c.AdminEmail)
		}
		if len([]rune(c.AdminPassword)) < MinAdminPasswordLength {
			fail("ADMIN_PASSWORD must be at least %d characters", MinAdminPasswordLength)
		}
	}

	for _, o := range splitList(get("CORS_ORIGINS")) {
		if o == "*" {
			c.CORSOrigins = append(c.CORSOrigins, o)
			continue
		}
		norm, err := normalizeOrigin(o)
		if err != nil {
			fail("CORS_ORIGINS: %v", err)
			continue
		}
		c.CORSOrigins = append(c.CORSOrigins, norm)
	}

	if c.TrustProxyHeaders, err = parseBool(get("TRUST_PROXY_HEADERS"), false); err != nil {
		fail("TRUST_PROXY_HEADERS: %v", err)
	}
	if tp := get("TRUSTED_PROXIES"); tp != "" {
		if b, err := parseBool(tp, false); err == nil {
			// Accept TRUSTED_PROXIES=true|false as an alias of TRUST_PROXY_HEADERS.
			c.TrustProxyHeaders = c.TrustProxyHeaders || b
		} else {
			for _, p := range splitList(tp) {
				prefix, err := parsePrefix(p)
				if err != nil {
					fail("TRUSTED_PROXIES: %v", err)
					continue
				}
				c.TrustedProxies = append(c.TrustedProxies, prefix)
			}
			if len(c.TrustedProxies) > 0 {
				c.TrustProxyHeaders = true
			}
		}
	}

	if c.ScannerEnabled, err = parseBool(get("SCANNER_ENABLED"), true); err != nil {
		fail("SCANNER_ENABLED: %v", err)
	}

	if len(errs) > 0 {
		return Config{}, fmt.Errorf("config: %w", errors.Join(errs...))
	}
	return c, nil
}

// RPCEnvName returns the environment variable holding RPC URLs for a chain:
// "RPC_" + upper-case chain id with '-' replaced by '_' (e.g. "tron-nile" →
// "RPC_TRON_NILE").
func RPCEnvName(chainID string) string {
	return "RPC_" + strings.ToUpper(strings.ReplaceAll(chainID, "-", "_"))
}

// RPCFromEnv returns the comma separated URLs of RPC_<CHAIN_ID>, trimmed and
// without empty entries, or nil when the variable is unset or empty.
func RPCFromEnv(chainID string) []string {
	return splitList(os.Getenv(RPCEnvName(chainID)))
}

func splitList(s string) []string {
	var out []string
	for part := range strings.SplitSeq(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func parseBool(s string, def bool) (bool, error) {
	if s == "" {
		return def, nil
	}
	switch strings.ToLower(s) {
	case "1", "t", "true", "y", "yes", "on":
		return true, nil
	case "0", "f", "false", "n", "no", "off":
		return false, nil
	}
	return def, fmt.Errorf("invalid boolean %q", s)
}

func checkHTTPURL(s string) error {
	u, err := url.Parse(s)
	if err != nil {
		return err
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%q is not an http(s) URL", s)
	}
	return nil
}

// normalizeOrigin validates an origin ("scheme://host[:port]") and returns it
// lower-cased without a trailing slash.
func normalizeOrigin(s string) (string, error) {
	u, err := url.Parse(strings.TrimRight(s, "/"))
	if err != nil {
		return "", fmt.Errorf("invalid origin %q: %v", s, err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.User != nil {
		return "", fmt.Errorf("invalid origin %q (want scheme://host[:port])", s)
	}
	return strings.ToLower(u.Scheme + "://" + u.Host), nil
}

func parsePrefix(s string) (netip.Prefix, error) {
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return netip.Prefix{}, fmt.Errorf("invalid CIDR %q", s)
		}
		return p.Masked(), nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("invalid IP %q", s)
	}
	a = a.Unmap()
	return netip.PrefixFrom(a, a.BitLen()), nil
}

// ParseLogLevel is a helper for callers that need to parse a level string
// outside Load ("debug", "info", "warn", "error").
func ParseLogLevel(s string) (slog.Level, error) {
	var l slog.Level
	if err := l.UnmarshalText([]byte(strings.TrimSpace(s))); err != nil {
		return slog.LevelInfo, fmt.Errorf("invalid log level %q", s)
	}
	return l, nil
}

// String returns a human readable, secret-free summary (same content as LogValue).
func (c Config) String() string {
	var b strings.Builder
	for _, a := range c.LogValue().Group() {
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(a.Key)
		b.WriteByte('=')
		b.WriteString(strconv.Quote(a.Value.String()))
	}
	return b.String()
}
