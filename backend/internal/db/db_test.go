package db

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testSchemaURL creates an empty, uniquely named schema and returns a URL
// whose search_path points at it. The schema is dropped on cleanup.
func testSchemaURL(t *testing.T) string {
	t.Helper()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	schema := "dbtest_" + hex.EncodeToString(b)

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, base)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize())
	require.NoError(t, err)
	require.NoError(t, conn.Close(ctx))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		conn, err := pgx.Connect(ctx, base)
		if err != nil {
			t.Logf("cleanup connect: %v", err)
			return
		}
		defer conn.Close(ctx)
		if _, err := conn.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); err != nil {
			t.Logf("drop schema %s: %v", schema, err)
		}
	})

	u, err := url.Parse(base)
	require.NoError(t, err)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}

func TestParseConfigDefaults(t *testing.T) {
	cfg, err := ParseConfig("postgres://u:p@localhost:5432/db?sslmode=disable")
	require.NoError(t, err)
	assert.EqualValues(t, DefaultMaxConns, cfg.MaxConns)
	assert.Equal(t, DefaultMaxConnLifetime, cfg.MaxConnLifetime)
	assert.Equal(t, DefaultConnectTimeout, cfg.ConnConfig.ConnectTimeout)
	assert.Equal(t, "cryptopay", cfg.ConnConfig.RuntimeParams["application_name"])
	assert.Equal(t, "UTC", cfg.ConnConfig.RuntimeParams["timezone"])
	assert.NotNil(t, cfg.AfterConnect)

	cfg, err = ParseConfig("postgres://u:p@localhost/db?pool_max_conns=3&application_name=x&connect_timeout=2")
	require.NoError(t, err)
	assert.EqualValues(t, 3, cfg.MaxConns)
	assert.Equal(t, "x", cfg.ConnConfig.RuntimeParams["application_name"])
	assert.Equal(t, 2*time.Second, cfg.ConnConfig.ConnectTimeout)

	_, err = ParseConfig("")
	require.Error(t, err)
	_, err = ParseConfig("postgres://u:p@localhost:notaport/db")
	require.Error(t, err)
}

func TestMigrationsEmbedded(t *testing.T) {
	entries, err := os.ReadDir("migrations")
	require.NoError(t, err)
	fsys := Migrations()
	for _, e := range entries {
		_, err := fsys.Open(e.Name())
		require.NoError(t, err, e.Name())
	}
	assert.NotEmpty(t, entries)
}

func TestOpenAndMigrate(t *testing.T) {
	u := testSchemaURL(t)
	ctx := context.Background()

	pool, err := Open(ctx, u)
	require.NoError(t, err)
	defer pool.Close()

	v, err := Version(ctx, pool)
	require.NoError(t, err)
	assert.EqualValues(t, 0, v)
	pending, err := HasPending(ctx, pool)
	require.NoError(t, err)
	assert.True(t, pending)

	applied, err := Migrate(ctx, pool)
	require.NoError(t, err)
	assert.Equal(t, []int64{1, 2, 3}, applied)

	v, err = Version(ctx, pool)
	require.NoError(t, err)
	assert.EqualValues(t, 3, v)
	pending, err = HasPending(ctx, pool)
	require.NoError(t, err)
	assert.False(t, pending)

	// Idempotent.
	applied, err = Migrate(ctx, pool)
	require.NoError(t, err)
	assert.Empty(t, applied)

	// Tables landed in the test schema, not public.
	var schema string
	require.NoError(t, pool.QueryRow(ctx, "SELECT current_schema()").Scan(&schema))
	var n int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM pg_tables WHERE schemaname = $1 AND tablename IN ('invoices','quotes','transfers','goose_db_version')`,
		schema).Scan(&n))
	assert.Equal(t, 4, n)

	// timestamptz values come back in UTC.
	var ts time.Time
	require.NoError(t, pool.QueryRow(ctx, "SELECT now()").Scan(&ts))
	assert.Equal(t, time.UTC, ts.Location())
}

func TestMigrateConcurrent(t *testing.T) {
	u := testSchemaURL(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Go(func() {
			pool, err := Open(ctx, u)
			if err != nil {
				errs[i] = err
				return
			}
			defer pool.Close()
			_, errs[i] = Migrate(ctx, pool)
		})
	}
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	pool, err := pgxpool.New(ctx, u)
	require.NoError(t, err)
	defer pool.Close()
	v, err := Version(ctx, pool)
	require.NoError(t, err)
	assert.EqualValues(t, 3, v)
}

func TestMigrateURL(t *testing.T) {
	u := testSchemaURL(t)
	applied, err := MigrateURL(context.Background(), u, WithoutLock())
	require.NoError(t, err)
	assert.Len(t, applied, 3)
}

func TestLockIDStable(t *testing.T) {
	assert.Equal(t, lockID("public"), lockID("public"))
	assert.NotEqual(t, lockID("public"), lockID("other"))
	assert.Positive(t, lockID("public"))
}

// migrateTo applies the embedded migrations up to (and including) version.
func migrateTo(t *testing.T, pool *pgxpool.Pool, version int64) error {
	t.Helper()
	provider, sqlDB, err := newProvider(context.Background(), pool, newMigrateConfig([]MigrateOption{WithoutLock()}))
	require.NoError(t, err)
	defer sqlDB.Close()
	_, err = provider.UpTo(context.Background(), version)
	return err
}

// TestMigrationMergesDuplicateWallets: 00003 adds unique indexes on wallet
// key material; wallets of one family that already share it are merged into
// the oldest one (references re-pointed, highest next_index kept, audited).
func TestMigrationMergesDuplicateWallets(t *testing.T) {
	ctx := context.Background()
	pool, err := Open(ctx, testSchemaURL(t))
	require.NoError(t, err)
	defer pool.Close()
	require.NoError(t, migrateTo(t, pool, 2))

	const (
		keep    = "00000000-0000-0000-0000-00000000000a"
		dup     = "00000000-0000-0000-0000-00000000000b"
		sKeep   = "00000000-0000-0000-0000-00000000000c"
		sDup    = "00000000-0000-0000-0000-00000000000d"
		unique  = "00000000-0000-0000-0000-00000000000e"
		invoice = "00000000-0000-0000-0000-0000000000f1"
	)
	_, err = pool.Exec(ctx, `
		INSERT INTO wallets (id, name, family, kind, xpub, address, next_index, created_at) VALUES
			('`+keep+`', 'EVM old', 'evm', 'xpub', 'xpubSAME', '', 3, '2026-01-01'),
			('`+dup+`', 'EVM copy', 'evm', 'xpub', 'xpubSAME', '', 7, '2026-01-02'),
			('`+sKeep+`', 'Tron old', 'tron', 'static', '', 'TSAME', 0, '2026-01-01'),
			('`+sDup+`', 'Tron copy', 'tron', 'static', '', 'TSAME', 0, '2026-01-03'),
			('`+unique+`', 'Other', 'evm', 'xpub', 'xpubOTHER', '', 1, '2026-01-01');
		INSERT INTO chains (id, family, name, native_symbol, confirmations, poll_interval_ms, scan_batch, wallet_id) VALUES
			('ethereum', 'evm', 'Ethereum', 'ETH', 12, 5000, 50, '`+dup+`'),
			('tron', 'tron', 'Tron', 'TRX', 19, 5000, 20, '`+sDup+`');
		INSERT INTO assets (id, chain_id, symbol, name, decimals, quote_precision, rate_key) VALUES
			('eth-ethereum', 'ethereum', 'ETH', 'Ether', 18, 6, 'ethereum');
		INSERT INTO merchants (id, name) VALUES ('00000000-0000-0000-0000-0000000000f0', 'Shop');
		INSERT INTO invoices (id, merchant_id, price_amount, price_currency, expires_at) VALUES
			('`+invoice+`', '00000000-0000-0000-0000-0000000000f0', 10, 'USD', '2026-01-02');
		INSERT INTO quotes (id, invoice_id, asset_id, chain_id, wallet_id, wallet_kind, address, address_index, rate, amount_due, watch_until) VALUES
			('00000000-0000-0000-0000-0000000000f2', '`+invoice+`', 'eth-ethereum', 'ethereum', '`+dup+`', 'xpub', '0xabc', 6, 2000, 1, '2026-01-03');`)
	require.NoError(t, err)

	require.NoError(t, migrateTo(t, pool, 3))
	ids := func(q string) []string {
		rows, err := pool.Query(ctx, q)
		require.NoError(t, err)
		out, err := pgx.CollectRows(rows, pgx.RowTo[string])
		require.NoError(t, err)
		return out
	}
	assert.Equal(t, []string{keep, sKeep, unique}, ids(`SELECT id::text FROM wallets ORDER BY id`))
	assert.Equal(t, []string{keep, sKeep}, ids(`SELECT wallet_id::text FROM chains ORDER BY id`))
	assert.Equal(t, []string{keep}, ids(`SELECT wallet_id::text FROM quotes`))
	assert.Equal(t, []string{"7"}, ids(`SELECT next_index::text FROM wallets WHERE id = '`+keep+`'`), "no index handed out twice")
	assert.Equal(t, []string{keep + " EVM copy", sKeep + " Tron copy"},
		ids(`SELECT target_id || ' ' || (detail->>'merged_wallet_name') FROM audit_logs WHERE action = 'wallet.merge' ORDER BY target_id`))

	// From now on the key material is unique.
	_, err = pool.Exec(ctx, `INSERT INTO wallets (id, name, family, kind, xpub) VALUES (gen_random_uuid(), 'again', 'tron', 'xpub', 'xpubSAME')`)
	require.ErrorContains(t, err, "wallets_xpub_key")
	_, err = pool.Exec(ctx, `INSERT INTO wallets (id, name, family, kind, address) VALUES (gen_random_uuid(), 'again', 'tron', 'static', 'TSAME')`)
	require.ErrorContains(t, err, "wallets_static_address_key")
}

// TestMigrationRefusesCrossFamilyXpub: one xpub registered for two families
// cannot be merged; the migration stops with an explanation.
func TestMigrationRefusesCrossFamilyXpub(t *testing.T) {
	ctx := context.Background()
	pool, err := Open(ctx, testSchemaURL(t))
	require.NoError(t, err)
	defer pool.Close()
	require.NoError(t, migrateTo(t, pool, 2))
	_, err = pool.Exec(ctx, `INSERT INTO wallets (id, name, family, kind, xpub) VALUES
		(gen_random_uuid(), 'EVM', 'evm', 'xpub', 'xpubSAME'), (gen_random_uuid(), 'Tron', 'tron', 'xpub', 'xpubSAME')`)
	require.NoError(t, err)
	err = migrateTo(t, pool, 3)
	require.ErrorContains(t, err, "share one extended public key across families")
	v, err := Version(ctx, pool)
	require.NoError(t, err)
	assert.EqualValues(t, 2, v, "nothing of 00003 applied")
}
