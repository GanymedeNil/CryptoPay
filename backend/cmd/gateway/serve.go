package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"cryptopay/internal/alert"
	"cryptopay/internal/api"
	"cryptopay/internal/auth"
	"cryptopay/internal/chain/registry"
	"cryptopay/internal/config"
	"cryptopay/internal/db"
	"cryptopay/internal/invoice"
	"cryptopay/internal/mailer"
	"cryptopay/internal/notify"
	"cryptopay/internal/rates"
	"cryptopay/internal/scanner"
	"cryptopay/internal/secret"
	"cryptopay/internal/store"
	"cryptopay/internal/webhook"
)

const (
	shutdownTimeout        = 20 * time.Second
	sessionCleanupInterval = time.Hour

	// Connection budget when DATABASE_URL does not set pool_max_conns: the
	// HTTP API, the webhook dispatcher (one connection per parallel
	// delivery), and — with the scanner — one advisory-lock connection plus
	// one working connection per scanned chain, the expiry job and the
	// session cleanup, plus the e-mail dispatcher and the alert queue.
	poolAPIConns     = 20
	poolWebhookConns = webhook.DefaultConcurrency
	poolScannerConns = 2*16 + 2
	poolMiscConns    = 2
	poolEmailConns   = mailer.DefaultConcurrency + 1
)

// poolSize is the default connection pool size for cfg.
func poolSize(cfg config.Config) int32 {
	n := poolAPIConns + poolWebhookConns + poolMiscConns
	if cfg.EmailConfigured() {
		n += poolEmailConns
	}
	if cfg.ScannerEnabled {
		n += poolScannerConns
	}
	return int32(n)
}

// openPool opens the database pool sized for this process.
func openPool(ctx context.Context, cfg config.Config) (*pgxpool.Pool, error) {
	pcfg, err := db.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	if !strings.Contains(cfg.DatabaseURL, "pool_max_conns") {
		pcfg.MaxConns = poolSize(cfg)
	}
	return db.OpenConfig(ctx, pcfg)
}

func runServe(ctx context.Context, args []string, getenv func(string) string, stdout io.Writer) error {
	fs := newFlagSet("serve", stdout)
	noMigrate := fs.Bool("no-migrate", false, "do not apply pending database migrations on start")
	addr := fs.String("addr", "", "listen address (overrides HTTP_ADDR)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	cfg, err := config.LoadFrom(getenv)
	if err != nil {
		return err
	}
	if *addr != "" {
		cfg.HTTPAddr = *addr
	}
	log := newLogger(stdout, cfg.LogLevel, true)
	log.Info("starting cryptopay gateway", "version", version, "commit", commit, "config", cfg)

	pool, err := openPool(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	if *noMigrate {
		if pending, err := db.HasPending(ctx, pool); err != nil {
			return err
		} else if pending {
			log.Warn("database migrations are pending and --no-migrate is set; run `gateway migrate`")
		}
	} else if applied, err := db.Migrate(ctx, pool, db.WithLogger(log)); err != nil {
		return err
	} else if len(applied) > 0 {
		log.Info("database migrated", "applied", applied)
	}

	box, err := secret.New(cfg.AppSecret)
	if err != nil {
		return err
	}
	st := store.New(pool, box)
	if res, err := st.Bootstrap(ctx); err != nil {
		return err
	} else if res.ChainsInserted+res.AssetsInserted > 0 {
		log.Info("catalog bootstrapped", "chains_inserted", res.ChainsInserted, "assets_inserted", res.AssetsInserted)
	}
	if cfg.BootstrapAdmin() {
		if err := ensureOwner(ctx, st, cfg, log); err != nil {
			return err
		}
	}

	// E-mail: security notifications and operational alerts go through the
	// outbox; without SMTP_HOST/SMTP_FROM nothing is queued and no
	// dispatcher runs.
	notifier := notify.New(st, cfg.EmailConfigured(), log.With("component", "notify"), time.Now)
	var sender *mailer.SMTP
	var mailDispatcher *mailer.Dispatcher
	var alerts alert.Notifier // nil without e-mail: the money path reports nothing
	var alertQueue *alert.Queue
	if cfg.EmailConfigured() {
		if sender, err = mailer.NewSMTP(cfg.SMTP); err != nil {
			return err
		}
		mailDispatcher = mailer.NewDispatcher(st, sender, log.With("component", "mailer"), time.Now)
		alertQueue = alert.NewQueue(notifier, 0, log.With("component", "alert"))
		alerts = alertQueue
		log.Info("e-mail enabled", "smtp_host", cfg.SMTP.Host, "smtp_tls", cfg.SMTP.TLS)
	} else {
		log.Info("e-mail not configured (SMTP_HOST / SMTP_FROM): no notifications are sent")
	}

	rp, prefetchRates := newRates(ctx, st, cfg, log)
	invoices := invoice.New(st, rp, log, time.Now)
	invoices.SetNotifier(alerts)
	reg := registry.New(st, registry.DefaultFactories(log))
	dispatcher := webhook.New(st, log.With("component", "webhook"), time.Now,
		webhook.AllowPrivateTargets(cfg.WebhookAllowPrivate), webhook.WithNotifier(alerts))
	var mgr *scanner.Manager
	if cfg.ScannerEnabled {
		mgr = scanner.New(st, reg, invoices, log.With("component", "scanner"), time.Now, scanner.Config{Notifier: alerts})
	} else {
		log.Warn("SCANNER_ENABLED=false: this instance does not scan chains or expire invoices")
	}

	bg, stopBg := context.WithCancel(context.WithoutCancel(ctx))
	defer stopBg()
	var wg sync.WaitGroup
	webhookKick := make(chan struct{}, 1)
	emailKick := make(chan struct{}, 1)
	chainKick := make(chan string, 32)

	hooks := api.Hooks{
		WebhookKick: func() {
			select {
			case webhookKick <- struct{}{}:
			default: // a kick is already pending
			}
		},
	}
	if mailDispatcher != nil {
		hooks.EmailKick = func() {
			select {
			case emailKick <- struct{}{}:
			default:
			}
		}
	}
	if mgr != nil {
		hooks.ChainChanged = func(chainID string) {
			select {
			case chainKick <- chainID:
			default:
			}
		}
	}

	deps := api.Deps{
		Store: st, Invoices: invoices, Registry: reg, Rates: rp, Config: cfg,
		Logger: log.With("component", "http"), Now: time.Now, Hooks: hooks, Notifier: notifier,
	}
	if sender != nil {
		deps.Mailer = sender
	}
	handler := api.New(deps)
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      2 * api.DefaultRequestTimeout,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	ln, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.HTTPAddr, err)
	}

	if mgr != nil {
		wg.Go(func() { _ = mgr.Run(bg) })
		wg.Go(func() { scanOnChange(bg, mgr, chainKick, log) })
	}
	wg.Go(func() { prefetchRates(bg) })
	wg.Go(func() { _ = dispatcher.Run(bg) })
	wg.Go(func() { deliverOnKick(bg, dispatcher, webhookKick, log) })
	wg.Go(func() { cleanupSessions(bg, st, log) })
	if mailDispatcher != nil {
		wg.Go(func() { _ = mailDispatcher.Run(bg) })
		wg.Go(func() { sendOnKick(bg, mailDispatcher, emailKick, log) })
		wg.Go(func() { alertQueue.Run(bg) })
	}

	srvErr := make(chan error, 1)
	go func() { srvErr <- srv.Serve(ln) }()
	log.Info("listening", "addr", ln.Addr().String())

	select {
	case <-ctx.Done():
	case err := <-srvErr:
		stopBg()
		wg.Wait()
		return fmt.Errorf("http server: %w", err)
	}

	log.Info("shutting down", "timeout", shutdownTimeout.String())
	shCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	shutdownErr := srv.Shutdown(shCtx)
	stopBg()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-shCtx.Done():
		log.Warn("background workers did not stop before the shutdown deadline")
	}
	if shutdownErr != nil && !errors.Is(shutdownErr, http.ErrServerClosed) {
		return fmt.Errorf("shutdown: %w", shutdownErr)
	}
	log.Info("stopped")
	return nil
}

// ensureOwner creates the first owner from ADMIN_EMAIL / ADMIN_PASSWORD when
// there are no admin users yet.
func ensureOwner(ctx context.Context, st *store.Store, cfg config.Config, log *slog.Logger) error {
	n, err := st.CountAdminUsers(ctx)
	if err != nil || n > 0 {
		return err
	}
	if err := auth.ValidatePassword(cfg.AdminPassword); err != nil {
		return fmt.Errorf("ADMIN_PASSWORD: %w", err)
	}
	hash, err := auth.HashPassword(cfg.AdminPassword)
	if err != nil {
		return err
	}
	created, err := st.EnsureOwner(ctx, cfg.AdminEmail, hash)
	if err != nil {
		return err
	}
	if created {
		log.Info("bootstrap owner created", "email", cfg.AdminEmail)
	}
	return nil
}

// newRates builds the CoinGecko provider, preloading the rate keys of all
// configured assets so one upstream request covers them. The returned
// function warms the cache; run it in the background.
func newRates(ctx context.Context, st *store.Store, cfg config.Config, log *slog.Logger) (*rates.CoinGecko, func(context.Context)) {
	var keys []string
	if assets, err := st.ListAssets(ctx, store.AssetFilter{}); err == nil {
		seen := map[string]bool{}
		for _, a := range assets {
			if a.RateKey != "" && !seen[a.RateKey] {
				seen[a.RateKey] = true
				keys = append(keys, a.RateKey)
			}
		}
	}
	cg := rates.NewCoinGecko(rates.CoinGeckoConfig{
		BaseURL: cfg.CoinGeckoBaseURL, APIKey: cfg.CoinGeckoAPIKey, Pro: cfg.CoinGeckoPro,
		Logger: log.With("component", "rates"), RateKeys: keys,
	})
	prefetch := func(ctx context.Context) {
		pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err := cg.Prefetch(pctx); err != nil && ctx.Err() == nil {
			log.Warn("initial price fetch failed (prices are fetched on demand)", "error", err)
		}
	}
	return cg, prefetch
}

// deliverOnKick delivers due webhooks right away when an admin re-queued
// one, instead of waiting for the dispatcher's next poll.
func deliverOnKick(ctx context.Context, d *webhook.Dispatcher, kick <-chan struct{}, log *slog.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-kick:
			if _, _, err := d.DeliverDue(ctx); err != nil && ctx.Err() == nil {
				log.Warn("webhook delivery after retry failed", "error", err)
			}
		}
	}
}

// scanOnChange runs one scan tick of a chain after its configuration was
// changed in the admin console, so the effect shows without waiting for
// the scanner's reload interval.
func scanOnChange(ctx context.Context, mgr *scanner.Manager, kick <-chan string, log *slog.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-kick:
			tctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			if _, err := mgr.TickOnce(tctx, id); err != nil && ctx.Err() == nil {
				log.Info("scan after configuration change failed", "chain_id", id, "error", scanner.SanitizeError(err))
			}
			cancel()
		}
	}
}

// sendOnKick sends due e-mail right away when one was queued by the API.
func sendOnKick(ctx context.Context, d *mailer.Dispatcher, kick <-chan struct{}, log *slog.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-kick:
			if _, _, err := d.SendDue(ctx); err != nil && ctx.Err() == nil {
				log.Warn("e-mail delivery after kick failed", "error", err)
			}
		}
	}
}

// cleanupSessions deletes expired admin sessions periodically.
func cleanupSessions(ctx context.Context, st *store.Store, log *slog.Logger) {
	t := time.NewTicker(sessionCleanupInterval)
	defer t.Stop()
	for {
		if n, err := st.DeleteExpiredSessions(ctx, time.Now()); err != nil && ctx.Err() == nil {
			log.Warn("session cleanup failed", "error", err)
		} else if n > 0 {
			log.Info("expired admin sessions deleted", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
