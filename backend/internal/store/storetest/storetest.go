// Package storetest provides isolated PostgreSQL-backed stores and fixture
// builders for tests of any package.
//
// Every call to New / NewDB creates a uniquely named schema in the database
// of TEST_DATABASE_URL, runs the embedded migrations into it, binds a small
// connection pool to it (search_path) and drops it when the test ends, so
// tests and packages can run in parallel against one server. Tests are
// skipped when TEST_DATABASE_URL is unset.
//
//	func TestSomething(t *testing.T) {
//		t.Parallel()
//		s := storetest.NewSeeded(t) // migrated schema + built-in catalog + settings
//		m := storetest.Merchant(t, s)
//		inv := storetest.Invoice(t, s, m.ID, func(i *domain.Invoice) { i.MerchantOrderID = "A-1" })
//		...
//	}
package storetest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"cryptopay/internal/db"
	"cryptopay/internal/secret"
	"cryptopay/internal/store"
)

// EnvURL is the environment variable holding the test server URL.
const EnvURL = "TEST_DATABASE_URL"

// AppSecret is the fixed application secret of Box (tests only).
const AppSecret = "storetest-app-secret-not-for-production-0123456789"

// DefaultMaxConns is the pool size of each test store. Keep it small:
// many tests may hold pools concurrently against one server. Override with
// TEST_DB_MAX_CONNS.
const DefaultMaxConns = 6

var box = sync.OnceValue(func() *secret.Box { return secret.MustNew(AppSecret) })

// Box returns the secret box test stores encrypt with.
func Box() *secret.Box { return box() }

// BaseURL returns TEST_DATABASE_URL or skips the test when it is unset.
func BaseURL(t testing.TB) string {
	t.Helper()
	u := os.Getenv(EnvURL)
	if u == "" {
		t.Skipf("%s not set; skipping PostgreSQL test", EnvURL)
	}
	return u
}

// DB is an isolated test database schema.
type DB struct {
	Store  *store.Store
	Pool   *pgxpool.Pool // bound to Schema via search_path
	Schema string
	// URL is TEST_DATABASE_URL with search_path=Schema, usable with
	// db.Open or by code that opens its own pool.
	URL string
}

// New returns a Store on a fresh, migrated, empty schema (no catalog rows).
func New(t testing.TB, opts ...store.Option) *store.Store {
	t.Helper()
	return NewDB(t, opts...).Store
}

// NewSeeded is New plus the built-in catalog (chains, assets) and default
// settings (store.Bootstrap).
func NewSeeded(t testing.TB, opts ...store.Option) *store.Store {
	t.Helper()
	s := New(t, opts...)
	SeedCatalog(t, s)
	return s
}

// NewDB is New returning the schema details as well.
func NewDB(t testing.TB, opts ...store.Option) *DB {
	t.Helper()
	d := EmptySchema(t)
	ctx := context.Background()
	if _, err := db.Migrate(ctx, d.Pool, db.WithoutLock()); err != nil {
		t.Fatalf("storetest: migrate schema %s: %v", d.Schema, err)
	}
	d.Store = store.New(d.Pool, Box(), opts...)
	return d
}

// EmptySchema creates a uniquely named schema without running migrations
// and returns it with a pool bound to it (DB.Store is nil). The schema and
// pool are cleaned up when the test ends.
func EmptySchema(t testing.TB) *DB {
	t.Helper()
	base := BaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	schema := "t_" + randomHex(8)
	admin := adminPool(t, base)
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatalf("storetest: create schema: %v", err)
	}

	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("storetest: parse %s: %v", EnvURL, err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	schemaURL := u.String()

	cfg, err := db.ParseConfig(schemaURL)
	if err != nil {
		t.Fatalf("storetest: %v", err)
	}
	cfg.MaxConns = int32(maxConns())
	cfg.MinConns = 0
	pool, err := db.OpenConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("storetest: open pool: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); err != nil {
			t.Logf("storetest: drop schema %s: %v", schema, err)
		}
	})
	return &DB{Pool: pool, Schema: schema, URL: schemaURL}
}

func maxConns() int {
	if v, err := strconv.Atoi(os.Getenv("TEST_DB_MAX_CONNS")); err == nil && v > 0 {
		return v
	}
	return DefaultMaxConns
}

var (
	adminMu    sync.Mutex
	adminPools = map[string]*pgxpool.Pool{}
)

// adminPool returns a process-wide small pool on the base URL used to create
// and drop schemas (it lives until the test binary exits).
func adminPool(t testing.TB, base string) *pgxpool.Pool {
	t.Helper()
	adminMu.Lock()
	defer adminMu.Unlock()
	if p, ok := adminPools[base]; ok {
		return p
	}
	cfg, err := pgxpool.ParseConfig(base)
	if err != nil {
		t.Fatalf("storetest: parse %s: %v", EnvURL, err)
	}
	cfg.MaxConns = 2
	cfg.MinConns = 0
	cfg.MaxConnIdleTime = 5 * time.Second
	p, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("storetest: connect: %v", err)
	}
	adminPools[base] = p
	return p
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
