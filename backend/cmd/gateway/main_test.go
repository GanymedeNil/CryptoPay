package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cryptopay/internal/catalog"
	"cryptopay/internal/config"
	"cryptopay/internal/domain"
	"cryptopay/internal/hdwallet/hdwallettest"
	"cryptopay/internal/invoice/invoicetest"
	"cryptopay/internal/secret"
	"cryptopay/internal/store"
	"cryptopay/internal/store/storetest"
)

const testSecret = "gateway-test-app-secret-0123456789abcdef"

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func runCmd(t *testing.T, env map[string]string, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	err := run(ctx, args, envOf(env), &out)
	return out.String(), err
}

func isUsage(err error) bool {
	var u usageError
	return errors.As(err, &u)
}

func TestRunCommandLine(t *testing.T) {
	out, err := runCmd(t, nil, "version")
	require.NoError(t, err)
	assert.Contains(t, out, "cryptopay gateway dev")

	out, err = runCmd(t, nil, "help")
	require.NoError(t, err)
	assert.Contains(t, out, "seed-dev")

	_, err = runCmd(t, nil, "frobnicate")
	require.Error(t, err)
	assert.True(t, isUsage(err))

	_, err = runCmd(t, nil, "serve", "--bogus")
	assert.True(t, isUsage(err), "%v", err)
	_, err = runCmd(t, nil, "--bogus") // serve is the default command
	assert.True(t, isUsage(err), "%v", err)
	_, err = runCmd(t, nil, "migrate", "extra")
	assert.True(t, isUsage(err), "%v", err)
	_, err = runCmd(t, nil, "version", "-h")
	assert.ErrorIs(t, err, flag.ErrHelp)

	// Configuration errors are reported before anything is opened.
	_, err = runCmd(t, map[string]string{}, "serve")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DATABASE_URL")
	_, err = runCmd(t, map[string]string{}, "migrate")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DATABASE_URL")
}

func TestCreateAdminValidation(t *testing.T) {
	env := map[string]string{"DATABASE_URL": "postgres://nobody@127.0.0.1:1/x", "APP_SECRET": testSecret}
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"--password", "long-enough"}, "--email is required"},
		{[]string{"--email", "not-an-email", "--password", "long-enough"}, "not a valid e-mail"},
		{[]string{"--email", "a@b.example", "--password", "short"}, "at least 8"},
		{[]string{"--email", "a@b.example", "--password", "long-enough", "--role", "root"}, "--role"},
	}
	for _, tc := range cases {
		_, err := runCmd(t, env, append([]string{"create-admin"}, tc.args...)...)
		require.Error(t, err, "%v", tc.args)
		assert.True(t, isUsage(err), "%v", err)
		assert.Contains(t, err.Error(), tc.want)
	}
}

func TestSeedDevRefusesProduction(t *testing.T) {
	// The refusal happens before the (unreachable) database is touched.
	env := map[string]string{"DATABASE_URL": "postgres://nobody@127.0.0.1:1/x", "APP_SECRET": testSecret, "APP_ENV": "production"}
	_, err := runCmd(t, env, "seed-dev")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "production")
}

// TestDevWalletsAreTestOnly re-derives the hard-coded seed-dev xpubs from the
// public Hardhat mnemonic and checks they only go to testnets.
func TestDevWalletsAreTestOnly(t *testing.T) {
	chains := map[string]domain.Chain{}
	for _, c := range catalog.Chains() {
		chains[c.ID] = c
	}
	for _, dw := range devWallets {
		assert.Equal(t, hdwallettest.AccountXpub(invoicetest.Mnemonic, dw.Path), dw.Xpub, dw.Name)
		c, ok := chains[dw.Chain]
		require.True(t, ok, dw.Chain)
		assert.True(t, c.IsTestnet, "%s must be a testnet", dw.Chain)
		assert.Equal(t, c.Family, dw.Family)
		assert.True(t, strings.HasPrefix(dw.Name, "TEST ONLY"))
	}
}

func TestHealthcheck(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer ok.Close()
	out, err := runCmd(t, nil, "healthcheck", "--url", ok.URL+"/healthz")
	require.NoError(t, err)
	assert.Equal(t, "ok\n", out)
	_, err = runCmd(t, nil, "healthcheck", "--url", ok.URL+"/nope")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
	// Default target: loopback on the HTTP_ADDR port.
	_, port, err := net.SplitHostPort(strings.TrimPrefix(ok.URL, "http://"))
	require.NoError(t, err)
	_, err = runCmd(t, map[string]string{"HTTP_ADDR": ":" + port}, "healthcheck")
	require.NoError(t, err)
	_, err = runCmd(t, map[string]string{"HTTP_ADDR": "127.0.0.1:1"}, "healthcheck", "--timeout", "500ms")
	require.Error(t, err)
}

func TestPoolSize(t *testing.T) {
	assert.Greater(t, poolSize(config.Config{ScannerEnabled: true}), poolSize(config.Config{}))
	assert.GreaterOrEqual(t, poolSize(config.Config{}), int32(20))
}

// dbEnv returns the environment for a fresh, empty database schema.
func dbEnv(t *testing.T) map[string]string {
	t.Helper()
	db := storetest.EmptySchema(t)
	return map[string]string{"DATABASE_URL": db.URL, "APP_SECRET": testSecret, "LOG_LEVEL": "warn"}
}

func openTestStore(t *testing.T, env map[string]string) *store.Store {
	t.Helper()
	cfg, err := config.LoadFrom(envOf(env))
	require.NoError(t, err)
	pool, err := openPool(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return store.New(pool, secret.MustNew(testSecret))
}

func TestMigrateCreateAdminSeedDev(t *testing.T) {
	env := dbEnv(t)
	out, err := runCmd(t, env, "migrate")
	require.NoError(t, err)
	assert.Regexp(t, `applied [1-9]\d* migration\(s\); schema version \d+`, out)
	out, err = runCmd(t, env, "migrate")
	require.NoError(t, err)
	assert.Contains(t, out, "applied 0 migration(s)")

	out, err = runCmd(t, env, "create-admin", "--email", "root@example.com", "--password", "correct-horse-1", "--role", "admin")
	require.NoError(t, err)
	assert.Contains(t, out, "created admin root@example.com")
	assert.NotContains(t, out, "correct-horse-1")
	_, err = runCmd(t, env, "create-admin", "--email", "ROOT@example.com", "--password", "correct-horse-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")

	out, err = runCmd(t, env, "seed-dev", "--webhook-url", "http://localhost:4000/hook")
	require.NoError(t, err)
	assert.Regexp(t, `api key:\s+sk_live_\S{43}`, out)
	assert.Regexp(t, `webhook secret:\s+whsec_`, out)
	assert.Contains(t, out, "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266")
	out2, err := runCmd(t, env, "seed-dev")
	require.NoError(t, err, out2)
	assert.Contains(t, out2, "webhook secret:  unchanged")

	st := openTestStore(t, env)
	ctx := context.Background()
	ws, err := st.ListWallets(ctx)
	require.NoError(t, err)
	assert.Len(t, ws, len(devWallets), "wallets are reused on a second run")
	for _, dw := range devWallets {
		c, err := st.GetChain(ctx, dw.Chain)
		require.NoError(t, err)
		assert.True(t, c.Enabled, dw.Chain)
		assert.NotNil(t, c.WalletID, dw.Chain)
	}
	for _, id := range []string{"ethereum", "tron", "bitcoin"} {
		c, err := st.GetChain(ctx, id)
		require.NoError(t, err)
		assert.Nil(t, c.WalletID, "mainnets get no test wallet")
	}
	ms, err := st.ListMerchants(ctx, store.MerchantFilter{})
	require.NoError(t, err)
	require.Equal(t, 1, ms.Total)
	assert.Equal(t, "http://localhost:4000/hook", ms.Items[0].WebhookURL)
	keys, err := st.ListAPIKeys(ctx, ms.Items[0].ID)
	require.NoError(t, err)
	assert.Len(t, keys, 2, "a new key per run")
}

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return addr
}

func TestServeLifecycle(t *testing.T) {
	env := dbEnv(t)
	addr := freePort(t)
	env["HTTP_ADDR"] = addr
	env["LOG_LEVEL"] = "info"
	env["ADMIN_EMAIL"] = "boot@example.com"
	env["ADMIN_PASSWORD"] = "bootstrap-pass-1"
	env["COINGECKO_BASE_URL"] = "http://127.0.0.1:1"                               // no network in tests
	env["SCANNER_ENABLED"] = "true"                                                // no chain has a wallet: nothing is scanned
	env["SMTP_HOST"], env["SMTP_PORT"], env["SMTP_TLS"] = "127.0.0.1", "1", "none" // nothing is mailed in this test
	env["SMTP_FROM"], env["SMTP_USERNAME"], env["SMTP_PASSWORD"] = "CryptoPay <no-reply@cryptopay.local>", "mailer", "smtp-pass-xyz"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out syncBuffer
	done := make(chan error, 1)
	go func() { done <- run(ctx, []string{"serve"}, envOf(env), &out) }()

	base := "http://" + addr
	require.Eventually(t, func() bool {
		res, err := http.Get(base + "/readyz")
		if err != nil {
			return false
		}
		res.Body.Close()
		return res.StatusCode == http.StatusOK
	}, 30*time.Second, 50*time.Millisecond, "server did not become ready: %s", out.String())

	req, err := http.NewRequest(http.MethodPost, base+"/api/admin/auth/login",
		strings.NewReader(`{"email":"boot@example.com","password":"bootstrap-pass-1"}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "cryptopay")
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	var me map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&me))
	res.Body.Close()
	require.Equal(t, http.StatusOK, res.StatusCode, "%v", me)
	assert.Equal(t, "owner", me["role"])

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(shutdownTimeout + 5*time.Second):
		t.Fatal("serve did not shut down")
	}
	logs := out.String()
	assert.Contains(t, logs, `"msg":"listening"`)
	assert.Contains(t, logs, `"msg":"e-mail enabled"`)
	assert.Contains(t, logs, `"msg":"stopped"`)
	for _, s := range []string{testSecret, "bootstrap-pass-1", "smtp-pass-xyz"} {
		assert.NotContains(t, logs, s, "secrets never reach the log")
	}

	// A second start does not create another owner and needs no migration.
	st := openTestStore(t, env)
	n, err := st.CountAdminUsers(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	_, err = runCmd(t, map[string]string{"DATABASE_URL": env["DATABASE_URL"], "APP_SECRET": testSecret, "HTTP_ADDR": "256.0.0.1:1",
		"SCANNER_ENABLED": "false", "COINGECKO_BASE_URL": "http://127.0.0.1:1"}, "serve", "--no-migrate")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "listen")
}

// syncBuffer is a goroutine-safe bytes.Buffer.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestReset2FA(t *testing.T) {
	_, err := runCmd(t, map[string]string{"DATABASE_URL": "postgres://nobody@127.0.0.1:1/x", "APP_SECRET": testSecret}, "reset-2fa")
	require.Error(t, err)
	assert.True(t, isUsage(err), "%v", err)
	assert.Contains(t, err.Error(), "--email is required")

	env := dbEnv(t)
	env["SMTP_HOST"], env["SMTP_FROM"], env["SMTP_TLS"] = "mailpit", "CryptoPay <no-reply@cryptopay.local>", "none"
	_, err = runCmd(t, env, "create-admin", "--email", "lost@example.com", "--password", "lost-device-1")
	require.NoError(t, err)
	st := openTestStore(t, env)
	ctx := context.Background()
	u, err := st.GetAdminUserByEmail(ctx, "lost@example.com")
	require.NoError(t, err)
	require.NoError(t, st.SetPendingTOTPSecret(ctx, u.ID, "JBSWY3DPEHPK3PXP"))
	_, err = st.EnableTOTP(ctx, u.ID, 1)
	require.NoError(t, err)
	require.NoError(t, st.WithTx(ctx, func(tx *store.Store) error { return tx.ReplaceRecoveryCodes(ctx, u.ID, []string{"h1", "h2"}) }))
	storetest.Session(t, st, u.ID)

	out, err := runCmd(t, env, "reset-2fa", "--email", "LOST@example.com")
	require.NoError(t, err)
	assert.Contains(t, out, "two-factor authentication of lost@example.com was reset; 1 session(s) signed out")
	assert.NotContains(t, out, "JBSWY3DPEHPK3PXP")
	u, err = st.GetAdminUser(ctx, u.ID)
	require.NoError(t, err)
	assert.False(t, u.TwoFactorEnabled())
	n, err := st.CountRecoveryCodes(ctx, u.ID)
	require.NoError(t, err)
	assert.Zero(t, n)
	var sessions int
	require.NoError(t, st.Pool().QueryRow(ctx, `SELECT count(*) FROM admin_sessions WHERE user_id = $1`, u.ID).Scan(&sessions))
	assert.Zero(t, sessions)
	logs, err := st.ListAuditLogs(ctx, store.AuditFilter{Action: "user.2fa_reset"})
	require.NoError(t, err)
	require.Equal(t, 1, logs.Total)
	assert.Equal(t, "cli:reset-2fa", logs.Items[0].ActorEmail)
	mails, err := st.ListEmails(ctx, store.EmailFilter{Kind: "security.2fa_reset"})
	require.NoError(t, err)
	require.Equal(t, 1, mails.Total, "the user is e-mailed when e-mail is configured")
	assert.Equal(t, "lost@example.com", mails.Items[0].To)
	assert.Contains(t, mails.Items[0].BodyText, "gateway reset-2fa")

	out, err = runCmd(t, env, "reset-2fa", "--email", "lost@example.com")
	require.NoError(t, err)
	assert.Contains(t, out, "nothing to reset")
	_, err = runCmd(t, env, "reset-2fa", "--email", "nobody@example.com")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no admin user")
}
