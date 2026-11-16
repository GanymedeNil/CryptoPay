// Package db opens the PostgreSQL connection pool and applies the embedded
// goose migrations in migrations/.
package db

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"hash/fnv"
	"io/fs"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

//go:embed migrations/*.sql
var embedded embed.FS

// Migrations returns the embedded migration files (rooted at the migration
// directory, e.g. "00001_init.sql").
func Migrations() fs.FS {
	sub, err := fs.Sub(embedded, "migrations")
	if err != nil {
		panic(err) // the directory is embedded at compile time
	}
	return sub
}

// Pool defaults applied by ParseConfig unless the URL sets them explicitly
// (pool_max_conns, pool_min_conns, ... and connect_timeout).
const (
	DefaultMaxConns          = 20
	DefaultMinConns          = 0
	DefaultMaxConnLifetime   = time.Hour
	DefaultMaxConnIdleTime   = 15 * time.Minute
	DefaultHealthCheckPeriod = 30 * time.Second
	DefaultConnectTimeout    = 10 * time.Second
)

// ParseConfig parses a PostgreSQL URL / DSN into a pool configuration with
// the gateway defaults: the pool limits above (unless set in the URL),
// application_name "cryptopay", session time zone UTC, and timestamptz
// values scanned as UTC time.Time.
func ParseConfig(url string) (*pgxpool.Config, error) {
	if strings.TrimSpace(url) == "" {
		return nil, errors.New("db: empty database url")
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("db: parse database url: %w", err)
	}
	has := func(param string) bool { return strings.Contains(url, param) }
	if !has("pool_max_conns") {
		cfg.MaxConns = DefaultMaxConns
	}
	if !has("pool_min_conns") {
		cfg.MinConns = DefaultMinConns
	}
	if !has("pool_max_conn_lifetime") {
		cfg.MaxConnLifetime = DefaultMaxConnLifetime
	}
	if !has("pool_max_conn_idle_time") {
		cfg.MaxConnIdleTime = DefaultMaxConnIdleTime
	}
	if !has("pool_health_check_period") {
		cfg.HealthCheckPeriod = DefaultHealthCheckPeriod
	}
	if !has("connect_timeout") {
		cfg.ConnConfig.ConnectTimeout = DefaultConnectTimeout
	}
	rp := cfg.ConnConfig.RuntimeParams
	if rp["application_name"] == "" {
		rp["application_name"] = "cryptopay"
	}
	if rp["timezone"] == "" && rp["TimeZone"] == "" {
		rp["timezone"] = "UTC"
	}
	prev := cfg.AfterConnect
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		conn.TypeMap().RegisterType(&pgtype.Type{
			Name: "timestamptz", OID: pgtype.TimestamptzOID,
			Codec: &pgtype.TimestamptzCodec{ScanLocation: time.UTC},
		})
		if prev != nil {
			return prev(ctx, conn)
		}
		return nil
	}
	return cfg, nil
}

// Open creates a connection pool for url (see ParseConfig) and verifies it
// with a ping.
func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := ParseConfig(url)
	if err != nil {
		return nil, err
	}
	return OpenConfig(ctx, cfg)
}

// OpenConfig creates a connection pool from cfg and pings the database.
func OpenConfig(ctx context.Context, cfg *pgxpool.Config) (*pgxpool.Pool, error) {
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("db: create pool: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: ping: %w", err)
	}
	return pool, nil
}

// MigrateOption configures Migrate.
type MigrateOption func(*migrateConfig)

type migrateConfig struct {
	logger *slog.Logger
	lock   bool
}

// WithLogger makes Migrate log applied migrations to l.
func WithLogger(l *slog.Logger) MigrateOption {
	return func(c *migrateConfig) { c.logger = l }
}

// WithoutLock disables the advisory lock that serialises concurrent
// migrators (only useful in tests that already guarantee exclusivity).
func WithoutLock() MigrateOption {
	return func(c *migrateConfig) { c.lock = false }
}

func newMigrateConfig(opts []MigrateOption) migrateConfig {
	c := migrateConfig{lock: true}
	for _, o := range opts {
		o(&c)
	}
	if c.logger == nil {
		c.logger = slog.New(slog.DiscardHandler)
	}
	return c
}

// Migrate applies all pending embedded migrations to the database (and
// schema, i.e. the first entry of search_path) the pool connects to. It
// returns the versions it applied.
//
// Concurrent migrators against the same schema are serialised with a
// PostgreSQL session advisory lock whose id is derived from the schema name,
// so parallel test schemas do not block each other.
func Migrate(ctx context.Context, pool *pgxpool.Pool, opts ...MigrateOption) ([]int64, error) {
	cfg := newMigrateConfig(opts)
	provider, sqlDB, err := newProvider(ctx, pool, cfg)
	if err != nil {
		return nil, err
	}
	defer sqlDB.Close()
	results, err := provider.Up(ctx)
	if err != nil {
		return nil, fmt.Errorf("db: migrate: %w", err)
	}
	applied := make([]int64, 0, len(results))
	for _, r := range results {
		applied = append(applied, r.Source.Version)
		cfg.logger.InfoContext(ctx, "migration applied", "version", r.Source.Version,
			"file", r.Source.Path, "duration", r.Duration)
	}
	return applied, nil
}

// MigrateURL opens a temporary pool for url, applies the migrations and
// closes the pool.
func MigrateURL(ctx context.Context, url string, opts ...MigrateOption) ([]int64, error) {
	pool, err := Open(ctx, url)
	if err != nil {
		return nil, err
	}
	defer pool.Close()
	return Migrate(ctx, pool, opts...)
}

// Version returns the current schema version (0 when nothing is applied).
func Version(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	provider, sqlDB, err := newProvider(ctx, pool, newMigrateConfig([]MigrateOption{WithoutLock()}))
	if err != nil {
		return 0, err
	}
	defer sqlDB.Close()
	v, err := provider.GetDBVersion(ctx)
	if err != nil {
		return 0, fmt.Errorf("db: get version: %w", err)
	}
	return v, nil
}

// HasPending reports whether there are embedded migrations not yet applied.
func HasPending(ctx context.Context, pool *pgxpool.Pool) (bool, error) {
	provider, sqlDB, err := newProvider(ctx, pool, newMigrateConfig([]MigrateOption{WithoutLock()}))
	if err != nil {
		return false, err
	}
	defer sqlDB.Close()
	pending, err := provider.HasPending(ctx)
	if err != nil {
		return false, fmt.Errorf("db: check pending migrations: %w", err)
	}
	return pending, nil
}

func newProvider(ctx context.Context, pool *pgxpool.Pool, cfg migrateConfig) (*goose.Provider, interface{ Close() error }, error) {
	if pool == nil {
		return nil, nil, errors.New("db: nil pool")
	}
	opts := []goose.ProviderOption{
		goose.WithDisableGlobalRegistry(true),
		goose.WithSlog(cfg.logger),
	}
	if cfg.lock {
		var schema string
		if err := pool.QueryRow(ctx, "SELECT current_schema()").Scan(&schema); err != nil {
			return nil, nil, fmt.Errorf("db: read current schema: %w", err)
		}
		locker, err := lock.NewPostgresSessionLocker(
			lock.WithLockID(lockID(schema)),
			lock.WithLockTimeout(1, 600), // retry every second for up to 10 minutes
		)
		if err != nil {
			return nil, nil, fmt.Errorf("db: create migration lock: %w", err)
		}
		opts = append(opts, goose.WithSessionLocker(locker))
	}
	sqlDB := stdlib.OpenDBFromPool(pool)
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, Migrations(), opts...)
	if err != nil {
		_ = sqlDB.Close()
		return nil, nil, fmt.Errorf("db: create migration provider: %w", err)
	}
	return provider, sqlDB, nil
}

// lockID derives a stable, positive advisory lock id for a schema.
func lockID(schema string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte("cryptopay/migrate/" + schema))
	return int64(h.Sum64() & 0x7fffffffffffffff)
}
