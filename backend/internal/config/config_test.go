package config

import (
	"log/slog"
	"net/netip"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const goodSecret = "0123456789abcdef0123456789abcdef"

// clearEnv blanks every variable Load reads so the host environment cannot leak in.
func clearEnv(t *testing.T) {
	for _, k := range []string{
		"DATABASE_URL", "APP_SECRET", "HTTP_ADDR", "APP_ENV", "LOG_LEVEL",
		"COINGECKO_API_KEY", "COINGECKO_PRO", "COINGECKO_BASE_URL", "ADMIN_EMAIL", "ADMIN_PASSWORD",
		"CORS_ORIGINS", "TRUST_PROXY_HEADERS", "TRUSTED_PROXIES", "SCANNER_ENABLED",
	} {
		t.Setenv(k, "")
	}
}

func setRequired(t *testing.T) {
	clearEnv(t)
	t.Setenv("DATABASE_URL", "postgres://u:p@localhost:5432/db?sslmode=disable")
	t.Setenv("APP_SECRET", goodSecret)
}

func TestLoadDefaults(t *testing.T) {
	setRequired(t)
	c, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "postgres://u:p@localhost:5432/db?sslmode=disable", c.DatabaseURL)
	assert.Equal(t, goodSecret, c.AppSecret)
	assert.Equal(t, ":8080", c.HTTPAddr)
	assert.Equal(t, EnvDevelopment, c.Env)
	assert.False(t, c.Production())
	assert.False(t, c.SecureCookies())
	assert.Equal(t, slog.LevelInfo, c.LogLevel)
	assert.Empty(t, c.CoinGeckoAPIKey)
	assert.False(t, c.CoinGeckoPro)
	assert.Empty(t, c.CoinGeckoBaseURL)
	assert.False(t, c.BootstrapAdmin())
	assert.Empty(t, c.CORSOrigins)
	assert.False(t, c.TrustProxyHeaders)
	assert.Empty(t, c.TrustedProxies)
	assert.True(t, c.ScannerEnabled)
}

func TestLoadAll(t *testing.T) {
	setRequired(t)
	t.Setenv("HTTP_ADDR", "127.0.0.1:9000")
	t.Setenv("APP_ENV", "production")
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("COINGECKO_API_KEY", "CG-abc")
	t.Setenv("COINGECKO_PRO", "true")
	t.Setenv("COINGECKO_BASE_URL", "https://pro-api.coingecko.com/api/v3/")
	t.Setenv("ADMIN_EMAIL", "Owner@Example.com")
	t.Setenv("ADMIN_PASSWORD", "correct horse")
	t.Setenv("CORS_ORIGINS", "https://Shop.Example.com/, http://localhost:3000 ,")
	t.Setenv("TRUSTED_PROXIES", "10.0.0.0/8, 192.168.1.7, ::1")
	t.Setenv("SCANNER_ENABLED", "false")

	c, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1:9000", c.HTTPAddr)
	assert.True(t, c.Production())
	assert.True(t, c.SecureCookies())
	assert.Equal(t, slog.LevelDebug, c.LogLevel)
	assert.Equal(t, "CG-abc", c.CoinGeckoAPIKey)
	assert.True(t, c.CoinGeckoPro)
	assert.Equal(t, "https://pro-api.coingecko.com/api/v3", c.CoinGeckoBaseURL)
	assert.True(t, c.BootstrapAdmin())
	assert.Equal(t, "Owner@Example.com", c.AdminEmail)
	assert.Equal(t, []string{"https://shop.example.com", "http://localhost:3000"}, c.CORSOrigins)
	assert.True(t, c.TrustProxyHeaders)
	require.Len(t, c.TrustedProxies, 3)
	assert.Equal(t, "10.0.0.0/8", c.TrustedProxies[0].String())
	assert.Equal(t, "192.168.1.7/32", c.TrustedProxies[1].String())
	assert.Equal(t, "::1/128", c.TrustedProxies[2].String())
	assert.True(t, c.IsTrustedProxy(netip.MustParseAddr("10.1.2.3")))
	assert.True(t, c.IsTrustedProxy(netip.MustParseAddr("::ffff:10.1.2.3")))
	assert.False(t, c.IsTrustedProxy(netip.MustParseAddr("8.8.8.8")))
	assert.False(t, c.ScannerEnabled)
}

func TestTrustProxyFlags(t *testing.T) {
	setRequired(t)
	t.Setenv("TRUST_PROXY_HEADERS", "yes")
	c, err := Load()
	require.NoError(t, err)
	assert.True(t, c.TrustProxyHeaders)
	assert.True(t, c.IsTrustedProxy(netip.MustParseAddr("1.2.3.4")), "no list = trust any peer")

	setRequired(t)
	t.Setenv("TRUSTED_PROXIES", "true")
	c, err = Load()
	require.NoError(t, err)
	assert.True(t, c.TrustProxyHeaders)
	assert.Empty(t, c.TrustedProxies)

	setRequired(t)
	c, err = Load()
	require.NoError(t, err)
	assert.False(t, c.IsTrustedProxy(netip.MustParseAddr("1.2.3.4")))
}

func TestCORSWildcard(t *testing.T) {
	setRequired(t)
	t.Setenv("CORS_ORIGINS", "*")
	c, err := Load()
	require.NoError(t, err)
	assert.Equal(t, []string{"*"}, c.CORSOrigins)
}

func TestValidationErrors(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"missing db", map[string]string{"DATABASE_URL": ""}, "DATABASE_URL is required"},
		{"missing secret", map[string]string{"APP_SECRET": ""}, "APP_SECRET is required"},
		{"short secret", map[string]string{"APP_SECRET": strings.Repeat("x", 31)}, "at least 32"},
		{"bad env", map[string]string{"APP_ENV": "staging"}, "APP_ENV"},
		{"bad log level", map[string]string{"LOG_LEVEL": "loud"}, "LOG_LEVEL"},
		{"bad bool", map[string]string{"SCANNER_ENABLED": "maybe"}, "SCANNER_ENABLED"},
		{"bad pro bool", map[string]string{"COINGECKO_PRO": "pro"}, "COINGECKO_PRO"},
		{"bad cg url", map[string]string{"COINGECKO_BASE_URL": "ftp://x"}, "COINGECKO_BASE_URL"},
		{"admin email only", map[string]string{"ADMIN_EMAIL": "a@b.c"}, "set together"},
		{"admin password only", map[string]string{"ADMIN_PASSWORD": "longenough"}, "set together"},
		{"admin short password", map[string]string{"ADMIN_EMAIL": "a@b.c", "ADMIN_PASSWORD": "short"}, "ADMIN_PASSWORD must be at least"},
		{"admin bad email", map[string]string{"ADMIN_EMAIL": "nobody", "ADMIN_PASSWORD": "longenough"}, "not an e-mail"},
		{"bad origin", map[string]string{"CORS_ORIGINS": "https://ok.example,shop.example.com"}, "CORS_ORIGINS"},
		{"origin with path", map[string]string{"CORS_ORIGINS": "https://ok.example/path"}, "CORS_ORIGINS"},
		{"bad proxy", map[string]string{"TRUSTED_PROXIES": "10.0.0.0/33"}, "TRUSTED_PROXIES"},
		{"bad trust flag", map[string]string{"TRUST_PROXY_HEADERS": "sometimes"}, "TRUST_PROXY_HEADERS"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setRequired(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			_, err := Load()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestAllErrorsReported(t *testing.T) {
	clearEnv(t)
	t.Setenv("APP_ENV", "nope")
	_, err := Load()
	require.Error(t, err)
	for _, want := range []string{"DATABASE_URL", "APP_SECRET", "APP_ENV"} {
		assert.Contains(t, err.Error(), want)
	}
}

func TestLoadFrom(t *testing.T) {
	env := map[string]string{"DATABASE_URL": "postgres://x", "APP_SECRET": goodSecret, "APP_ENV": "prod"}
	c, err := LoadFrom(func(k string) string { return env[k] })
	require.NoError(t, err)
	assert.Equal(t, EnvProduction, c.Env)
}

func TestRPCFromEnv(t *testing.T) {
	assert.Equal(t, "RPC_ETHEREUM", RPCEnvName("ethereum"))
	assert.Equal(t, "RPC_TRON_NILE", RPCEnvName("tron-nile"))
	assert.Equal(t, "RPC_BITCOIN_TESTNET", RPCEnvName("bitcoin-testnet"))

	t.Setenv("RPC_TRON_NILE", " https://a.example/key , ,https://b.example ")
	assert.Equal(t, []string{"https://a.example/key", "https://b.example"}, RPCFromEnv("tron-nile"))

	t.Setenv("RPC_ETHEREUM", "")
	assert.Nil(t, RPCFromEnv("ethereum"))
	assert.Nil(t, RPCFromEnv("never-set-chain"))
}

func TestRedaction(t *testing.T) {
	setRequired(t)
	t.Setenv("COINGECKO_API_KEY", "CG-supersecret")
	t.Setenv("ADMIN_EMAIL", "a@b.c")
	t.Setenv("ADMIN_PASSWORD", "hunter22hunter22")
	c, err := Load()
	require.NoError(t, err)
	s := c.String()
	for _, secret := range []string{goodSecret, "CG-supersecret", "hunter22hunter22", ":p@"} {
		assert.NotContains(t, s, secret)
	}
	assert.Contains(t, s, "localhost:5432")
	assert.Equal(t, "postgres://u:xxxxx@h/db", RedactURL("postgres://u:pw@h/db?password=x"))
	assert.Equal(t, "[redacted]", RedactURL("host=h password=x"))
	assert.Equal(t, "", RedactURL(""))
}

func TestParseLogLevel(t *testing.T) {
	l, err := ParseLogLevel("WARN")
	require.NoError(t, err)
	assert.Equal(t, slog.LevelWarn, l)
	_, err = ParseLogLevel("nope")
	require.Error(t, err)
}
