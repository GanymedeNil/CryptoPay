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
	assert.Equal(t, []int64{1, 2}, applied)

	v, err = Version(ctx, pool)
	require.NoError(t, err)
	assert.EqualValues(t, 2, v)
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
	assert.EqualValues(t, 2, v)
}

func TestMigrateURL(t *testing.T) {
	u := testSchemaURL(t)
	applied, err := MigrateURL(context.Background(), u, WithoutLock())
	require.NoError(t, err)
	assert.Len(t, applied, 2)
}

func TestLockIDStable(t *testing.T) {
	assert.Equal(t, lockID("public"), lockID("public"))
	assert.NotEqual(t, lockID("public"), lockID("other"))
	assert.Positive(t, lockID("public"))
}
