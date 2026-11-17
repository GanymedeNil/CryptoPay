package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"cryptopay/internal/auth"
	"cryptopay/internal/config"
	"cryptopay/internal/db"
	"cryptopay/internal/domain"
	"cryptopay/internal/hdwallet"
	"cryptopay/internal/notify"
	"cryptopay/internal/secret"
	"cryptopay/internal/store"
)

func runMigrate(ctx context.Context, args []string, getenv func(string) string, stdout io.Writer) error {
	if err := parseFlags(newFlagSet("migrate", stdout), args); err != nil {
		return err
	}
	// Only the database is needed: do not insist on the rest of the config.
	dbURL := strings.TrimSpace(getenv("DATABASE_URL"))
	if dbURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	pool, err := db.Open(ctx, dbURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	applied, err := db.Migrate(ctx, pool, db.WithLogger(newLogger(stdout, slog.LevelInfo, false)))
	if err != nil {
		return err
	}
	v, err := db.Version(ctx, pool)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "applied %d migration(s); schema version %d\n", len(applied), v)
	return nil
}

// openStore loads the configuration, opens the pool, applies pending
// migrations and bootstraps the catalog (all idempotent).
func openStore(ctx context.Context, cfg config.Config, stdout io.Writer) (*store.Store, func(), error) {
	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, nil, err
	}
	if _, err := db.Migrate(ctx, pool, db.WithLogger(newLogger(stdout, slog.LevelWarn, false))); err != nil {
		pool.Close()
		return nil, nil, err
	}
	box, err := secret.New(cfg.AppSecret)
	if err != nil {
		pool.Close()
		return nil, nil, err
	}
	st := store.New(pool, box)
	if _, err := st.Bootstrap(ctx); err != nil {
		pool.Close()
		return nil, nil, err
	}
	return st, pool.Close, nil
}

func runCreateAdmin(ctx context.Context, args []string, getenv func(string) string, stdout io.Writer) error {
	fs := newFlagSet("create-admin", stdout)
	email := fs.String("email", "", "e-mail address (login name), required")
	password := fs.String("password", "", "password, at least 8 characters, required")
	role := fs.String("role", string(domain.RoleOwner), "role: owner, admin or viewer")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	e := strings.TrimSpace(*email)
	if e == "" {
		return usagef("create-admin: --email is required")
	}
	if a, err := mail.ParseAddress(e); err != nil || a.Address != e {
		return usagef("create-admin: --email %q is not a valid e-mail address", e)
	}
	if err := auth.ValidatePassword(*password); err != nil {
		return usagef("create-admin: --password %s", strings.TrimPrefix(err.Error(), auth.ErrWeakPassword.Error()+": "))
	}
	r := domain.Role(*role)
	if !r.Valid() {
		return usagef("create-admin: --role must be owner, admin or viewer")
	}
	cfg, err := config.LoadFrom(getenv)
	if err != nil {
		return err
	}
	hash, err := auth.HashPassword(*password)
	if err != nil {
		return err
	}
	st, closeFn, err := openStore(ctx, cfg, stdout)
	if err != nil {
		return err
	}
	defer closeFn()
	u, err := st.CreateAdminUser(ctx, domain.AdminUser{Email: e, PasswordHash: hash, Role: r, Active: true})
	if store.IsConstraint(err, store.ConstraintAdminUserEmail) {
		return fmt.Errorf("an admin user with e-mail %s already exists", e)
	}
	if err != nil {
		return err
	}
	if _, err := st.InsertAuditLog(ctx, domain.AuditLog{Action: "user.create", TargetType: "admin_user",
		TargetID: u.ID.String(), ActorEmail: "cli:create-admin",
		Detail: []byte(fmt.Sprintf(`{"email":%q,"role":%q}`, u.Email, u.Role))}); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "created %s %s (id %s)\n", u.Role, u.Email, u.ID)
	return nil
}

// runReset2FA is the break-glass reset of an admin's two-factor
// authentication (lost device and no recovery codes, no other owner who
// could reset it in the console): it removes the TOTP secret and recovery
// codes and signs the user out everywhere. Audited; the user is e-mailed
// when e-mail is configured.
func runReset2FA(ctx context.Context, args []string, getenv func(string) string, stdout io.Writer) error {
	fs := newFlagSet("reset-2fa", stdout)
	email := fs.String("email", "", "e-mail address of the admin user, required")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	e := strings.TrimSpace(*email)
	if e == "" {
		return usagef("reset-2fa: --email is required")
	}
	cfg, err := config.LoadFrom(getenv)
	if err != nil {
		return err
	}
	st, closeFn, err := openStore(ctx, cfg, stdout)
	if err != nil {
		return err
	}
	defer closeFn()
	u, err := st.GetAdminUserByEmail(ctx, e)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("no admin user with e-mail %s", e)
	}
	if err != nil {
		return err
	}
	log := newLogger(stdout, slog.LevelWarn, false)
	notifier := notify.New(st, cfg.EmailConfigured(), log, time.Now)
	var reset bool
	var sessions int64
	err = st.WithTx(ctx, func(tx *store.Store) error {
		var err error
		if reset, sessions, err = tx.ResetAdminTwoFactor(ctx, u.ID); err != nil || !reset {
			return err
		}
		detail, _ := json.Marshal(map[string]any{"email": u.Email, "sessions_revoked": sessions})
		if _, err := tx.InsertAuditLog(ctx, domain.AuditLog{Action: "user.2fa_reset", TargetType: "admin_user",
			TargetID: u.ID.String(), ActorEmail: "cli:reset-2fa", Detail: detail}); err != nil {
			return err
		}
		if err := tx.WithTx(ctx, func(sp *store.Store) error {
			return notifier.Security(ctx, sp, u, notify.Event{Kind: notify.Kind2FAReset, ActorCLI: true})
		}); err != nil {
			log.Warn("could not queue the notification e-mail", "error", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !reset {
		fmt.Fprintf(stdout, "%s has no two-factor authentication; nothing to reset\n", u.Email)
		return nil
	}
	fmt.Fprintf(stdout, "two-factor authentication of %s was reset; %d session(s) signed out\n", u.Email, sessions)
	fmt.Fprintln(stdout, "they can sign in with their password now (and must set up two-factor authentication again when it is required)")
	return nil
}

// devWallet is a TEST-ONLY receiving wallet for seed-dev. The extended
// public keys are derived from the public Hardhat / Anvil development
// mnemonic "test test test test test test test test test test test junk"
// (cmd/gateway tests re-derive them). Anyone can spend funds sent to these
// addresses — they are only ever assigned to testnets.
type devWallet struct {
	Name   string
	Family domain.Family
	Path   string
	Xpub   string
	Chain  string // testnet chain it is assigned to
}

var devWallets = []devWallet{
	{
		Name: "TEST ONLY - Hardhat mnemonic (EVM m/44'/60'/0')", Family: domain.FamilyEVM, Path: "m/44'/60'/0'",
		Xpub:  "xpub6Ce9NcJvTk36xtLSrJLZqE7wtgA5deCeYs7rSQtreh4cj6ByPtrg9sD7V2FNFLPnf8heNP3FGkeV9qwfzvZNSd54JoNXVsXFYSYwHsnJxqP",
		Chain: "sepolia",
	},
	{
		Name: "TEST ONLY - Hardhat mnemonic (Tron m/44'/195'/0')", Family: domain.FamilyTron, Path: "m/44'/195'/0'",
		Xpub:  "xpub6BuRy7ZmkGfP2RWhoor1kWB1w9DvrtmJbFBvnC1bQ4SB5NbCHTyQYCJSXZtxDbFyHTmC7wkU2he3nYYdpZ3Vf6qUAkXSnL6RPif4BDtDEKr",
		Chain: "tron-nile",
	},
	{
		Name: "TEST ONLY - Hardhat mnemonic (Bitcoin testnet m/84'/1'/0')", Family: domain.FamilyUTXO, Path: "m/84'/1'/0'",
		Xpub:  "xpub6C5xRBuLDXFQzVcTxcqBS3Ne9Q91yk9bG4HHoZPosRFqNcFkbin9xHjYCkZNtS8LF3VEuxaWyVxGfmbXQVFwfNCe53gfmPqLkcNM6DxzTqa",
		Chain: "bitcoin-testnet",
	},
}

func runSeedDev(ctx context.Context, args []string, getenv func(string) string, stdout io.Writer) error {
	fs := newFlagSet("seed-dev", stdout)
	name := fs.String("merchant-name", "Demo Shop (dev)", "name of the demo merchant (reused when it exists)")
	hook := fs.String("webhook-url", "", "optional webhook URL of the demo merchant")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	cfg, err := config.LoadFrom(getenv)
	if err != nil {
		return err
	}
	if cfg.Production() {
		return errors.New("seed-dev refuses to run with APP_ENV=production: it creates a demo API key and wallets derived from a PUBLIC test mnemonic")
	}
	if *hook != "" {
		if u, err := url.Parse(*hook); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return usagef("seed-dev: --webhook-url must be an http(s) URL")
		}
	}
	st, closeFn, err := openStore(ctx, cfg, stdout)
	if err != nil {
		return err
	}
	defer closeFn()

	m, secretShown, err := seedMerchant(ctx, st, strings.TrimSpace(*name), *hook)
	if err != nil {
		return err
	}
	plain, prefix, hash := auth.NewAPIKey()
	if _, err := st.CreateAPIKey(ctx, domain.APIKey{MerchantID: m.ID, Name: "seed-dev", Prefix: prefix, KeyHash: hash}); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "merchant:        %s (id %s)\n", m.Name, m.ID)
	fmt.Fprintf(stdout, "api key:         %s   (shown once; store it now)\n", plain)
	if secretShown != "" {
		fmt.Fprintf(stdout, "webhook secret:  %s   (shown once)\n", secretShown)
	} else {
		fmt.Fprintln(stdout, "webhook secret:  unchanged (rotate it in the admin console to see a new one)")
	}

	existing, err := st.ListWallets(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, "\nTEST-ONLY default wallets of the testnets (public Hardhat mnemonic; NEVER send real funds).")
	fmt.Fprintln(stdout, "Every merchant without its own wallet on a chain is paid to the chain's default wallet:")
	for _, dw := range devWallets {
		w, err := seedWallet(ctx, st, existing, dw)
		if err != nil {
			return err
		}
		enabled := true
		c, err := st.UpdateChain(ctx, dw.Chain, store.ChainUpdate{Enabled: &enabled, SetWallet: true, WalletID: &w.ID})
		if err != nil {
			return fmt.Errorf("assign wallet to %s: %w", dw.Chain, err)
		}
		first, err := hdwallet.Derive(c, dw.Xpub, 0)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "  %-16s enabled, default wallet %q, first address %s\n", c.ID, w.Name, first)
	}
	if _, err := st.InsertAuditLog(ctx, domain.AuditLog{Action: "dev.seed", TargetType: "merchant", TargetID: m.ID.String(),
		ActorEmail: "cli:seed-dev", Detail: []byte(fmt.Sprintf(`{"api_key_prefix":%q}`, prefix))}); err != nil {
		return err
	}
	return nil
}

// seedMerchant returns the merchant with the given name, creating it (and
// returning its new webhook secret) when it does not exist.
func seedMerchant(ctx context.Context, st *store.Store, name, hook string) (domain.Merchant, string, error) {
	p, err := st.ListMerchants(ctx, store.MerchantFilter{Q: name, PageSize: store.MaxPageSize})
	if err != nil {
		return domain.Merchant{}, "", err
	}
	for _, m := range p.Items {
		if m.Name == name {
			if hook != "" && hook != m.WebhookURL {
				if m, err = st.UpdateMerchant(ctx, m.ID, store.MerchantUpdate{WebhookURL: &hook}); err != nil {
					return domain.Merchant{}, "", err
				}
			}
			return m, "", nil
		}
	}
	secretValue := auth.NewWebhookSecret()
	m, err := st.CreateMerchant(ctx, domain.Merchant{Name: name, WebhookURL: hook, WebhookSecret: secretValue, Status: domain.MerchantActive})
	return m, secretValue, err
}

func seedWallet(ctx context.Context, st *store.Store, existing []store.WalletView, dw devWallet) (domain.Wallet, error) {
	for _, w := range existing {
		if w.Kind == domain.WalletXpub && w.Xpub == dw.Xpub && w.Family == dw.Family {
			return w.Wallet, nil
		}
	}
	if err := hdwallet.Validate(dw.Family, dw.Xpub); err != nil {
		return domain.Wallet{}, err
	}
	w, err := st.CreateWallet(ctx, domain.Wallet{Name: dw.Name, Family: dw.Family, Kind: domain.WalletXpub, Xpub: dw.Xpub})
	if store.IsConstraint(err, store.ConstraintWalletXpub) {
		return domain.Wallet{}, fmt.Errorf("wallet %q: its xpub is already registered for another family; delete that wallet first: %w", dw.Name, err)
	}
	return w, err
}

// runHealthcheck GETs the server's /healthz (by default on the port of
// HTTP_ADDR on loopback) and fails unless it answers 200. The runtime image
// has no shell or curl, so the container HEALTHCHECK runs this.
func runHealthcheck(ctx context.Context, args []string, getenv func(string) string, stdout io.Writer) error {
	fs := newFlagSet("healthcheck", stdout)
	target := fs.String("url", "", "URL to probe (default http://127.0.0.1:<HTTP_ADDR port>/healthz)")
	timeout := fs.Duration("timeout", 3*time.Second, "request timeout")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	u := *target
	if u == "" {
		addr := strings.TrimSpace(getenv("HTTP_ADDR"))
		if addr == "" {
			addr = config.DefaultHTTPAddr
		}
		_, port, err := net.SplitHostPort(addr)
		if err != nil || port == "" {
			return fmt.Errorf("healthcheck: cannot derive a port from HTTP_ADDR %q", addr)
		}
		u = "http://127.0.0.1:" + port + "/healthz"
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return usagef("healthcheck: %v", err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("healthcheck: %w", err)
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<16))
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck: %s answered %d", u, res.StatusCode)
	}
	fmt.Fprintln(stdout, "ok")
	return nil
}
