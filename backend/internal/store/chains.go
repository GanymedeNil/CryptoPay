package store

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"cryptopay/internal/catalog"
	"cryptopay/internal/config"
	"cryptopay/internal/domain"
)

const chainCols = `c.id, c.family, c.name, c.network_id, c.native_symbol, c.is_testnet, c.enabled, c.rpc_urls_enc,
	c.confirmations, c.poll_interval_ms, c.scan_batch, c.explorer_tx_url, c.explorer_addr_url, c.params::text,
	c.wallet_id, (SELECT count(*) FROM merchant_wallets mw WHERE mw.chain_id = c.id),
	c.scan_cursor, c.last_head, c.last_scan_at, c.last_error, c.created_at, c.updated_at`

func (s *Store) scanChain(row pgx.Row) (domain.Chain, error) {
	var c domain.Chain
	var family, enc, params string
	var pollMS int64
	err := row.Scan(&c.ID, &family, &c.Name, &c.NetworkID, &c.NativeSymbol, &c.IsTestnet, &c.Enabled, &enc,
		&c.Confirmations, &pollMS, &c.ScanBatch, &c.ExplorerTxURL, &c.ExplorerAddr, &params,
		&c.WalletID, &c.MerchantWallets, u64Scan{&c.ScanCursor}, u64Scan{&c.LastHead}, &c.LastScanAt, &c.LastError, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return domain.Chain{}, err
	}
	c.Family = domain.Family(family)
	c.PollInterval = time.Duration(pollMS) * time.Millisecond
	if params != "" {
		if err := json.Unmarshal([]byte(params), &c.Params); err != nil {
			return domain.Chain{}, fmt.Errorf("decode params of chain %s: %w", c.ID, err)
		}
	}
	if c.RPCURLs, err = s.decryptURLs(enc); err != nil {
		return domain.Chain{}, fmt.Errorf("rpc urls of chain %s: %w", c.ID, err)
	}
	return c, nil
}

func (s *Store) decryptURLs(enc string) ([]string, error) {
	plain, err := s.box.Decrypt(enc)
	if err != nil || plain == "" {
		return nil, err
	}
	var urls []string
	if err := json.Unmarshal([]byte(plain), &urls); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return urls, nil
}

func (s *Store) encryptURLs(urls []string) (string, error) {
	if len(urls) == 0 {
		return "", nil
	}
	raw, err := json.Marshal(urls)
	if err != nil {
		return "", err
	}
	return s.box.Encrypt(string(raw))
}

// ListChains returns all chains (mainnets first, then by name). RPCURLs
// holds only the admin-configured (decrypted) endpoints; use ResolveRPC /
// EffectiveChain for the endpoints to actually connect to.
func (s *Store) ListChains(ctx context.Context) ([]domain.Chain, error) {
	rows, err := s.db.Query(ctx, `SELECT `+chainCols+` FROM chains c ORDER BY c.is_testnet, c.name, c.id`)
	if err != nil {
		return nil, wrap("list chains", err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.Chain, error) { return s.scanChain(r) })
	return nonNil(out), wrap("list chains", err)
}

// GetChain returns a chain by id (RPCURLs: admin-configured endpoints only).
func (s *Store) GetChain(ctx context.Context, id string) (domain.Chain, error) {
	c, err := s.scanChain(s.db.QueryRow(ctx, `SELECT `+chainCols+` FROM chains c WHERE c.id = $1`, id))
	return c, wrap("get chain", err)
}

// ChainUpdate lists the admin-editable chain fields to change (nil =
// unchanged). It never touches the scanner state columns.
type ChainUpdate struct {
	Enabled *bool
	// RPCURLs replaces the configured endpoints; an empty (non-nil) slice
	// clears them so the chain falls back to env / built-in defaults.
	RPCURLs       *[]string
	Confirmations *int
	PollInterval  *time.Duration
	ScanBatch     *int
	// SetWallet applies WalletID: a wallet id makes that wallet the chain's
	// default wallet (it must belong to the chain's family), nil unassigns.
	// Merchant-specific wallets are set with SetMerchantWallet.
	SetWallet bool
	WalletID  *uuid.UUID
}

// UpdateChain applies an admin edit and returns the chain. Values violating
// the schema (confirmations < 1, poll interval < 500ms, scan batch < 1),
// malformed RPC URLs and wallets of another family yield ErrInvalid; an
// unknown wallet yields ErrForeignKey.
func (s *Store) UpdateChain(ctx context.Context, id string, upd ChainUpdate) (domain.Chain, error) {
	const op = "update chain"
	var enc *string
	if upd.RPCURLs != nil {
		urls := make([]string, 0, len(*upd.RPCURLs))
		for _, u := range *upd.RPCURLs {
			u = strings.TrimSpace(u)
			if u == "" {
				continue
			}
			if err := ValidateRPCURL(u); err != nil {
				return domain.Chain{}, invalidf(op, "%v", err)
			}
			urls = append(urls, u)
		}
		e, err := s.encryptURLs(urls)
		if err != nil {
			return domain.Chain{}, fmt.Errorf("store: %s: encrypt rpc urls: %w", op, err)
		}
		enc = &e
	}
	var pollMS *int64
	if upd.PollInterval != nil {
		ms := upd.PollInterval.Milliseconds()
		pollMS = &ms
	}
	if upd.SetWallet && upd.WalletID != nil {
		var ok bool
		err := s.db.QueryRow(ctx, `SELECT w.family = c.family FROM wallets w, chains c WHERE w.id = $1 AND c.id = $2`,
			*upd.WalletID, id).Scan(&ok)
		switch {
		case isNoRows(err):
			// Unknown wallet or chain: let the UPDATE below report it.
		case err != nil:
			return domain.Chain{}, wrap(op, err)
		case !ok:
			return domain.Chain{}, invalidf(op, "wallet %s belongs to another family than chain %s", *upd.WalletID, id)
		}
	}
	c, err := s.scanChain(s.db.QueryRow(ctx, `
		UPDATE chains c SET
			enabled          = COALESCE($2, c.enabled),
			rpc_urls_enc     = COALESCE($3, c.rpc_urls_enc),
			confirmations    = COALESCE($4, c.confirmations),
			poll_interval_ms = COALESCE($5, c.poll_interval_ms),
			scan_batch       = COALESCE($6, c.scan_batch),
			wallet_id        = CASE WHEN $7 THEN $8 ELSE c.wallet_id END,
			updated_at       = $9
		WHERE c.id = $1
		RETURNING `+chainCols,
		id, upd.Enabled, enc, upd.Confirmations, pollMS, upd.ScanBatch, upd.SetWallet, upd.WalletID, s.Now()))
	return c, wrap(op, err)
}

// ScanState is the scanner's progress for one chain.
type ScanState struct {
	Cursor    uint64    // last fully scanned block
	Head      uint64    // last observed chain head
	ScannedAt time.Time // when this state was observed
	Error     string    // "" after a successful iteration
}

// UpdateScanState persists scanner progress. It only touches scan_cursor,
// last_head, last_scan_at and last_error, so it never clobbers concurrent
// admin edits (and does not bump updated_at).
func (s *Store) UpdateScanState(ctx context.Context, chainID string, st ScanState) error {
	const op = "update scan state"
	cursor, err := u64Param(st.Cursor)
	if err != nil {
		return fmt.Errorf("store: %s: %w", op, err)
	}
	head, err := u64Param(st.Head)
	if err != nil {
		return fmt.Errorf("store: %s: %w", op, err)
	}
	tag, err := s.db.Exec(ctx, `
		UPDATE chains SET scan_cursor = $2, last_head = $3, last_scan_at = $4, last_error = $5 WHERE id = $1`,
		chainID, cursor, head, timeOr(st.ScannedAt, s.Now()), cleanText(st.Error, 2000))
	if err != nil {
		return wrap(op, err)
	}
	if tag.RowsAffected() == 0 {
		return wrap(op, ErrNotFound)
	}
	return nil
}

// ---- effective RPC endpoints ----

// RPCSource tells where a chain's effective RPC endpoints come from.
type RPCSource string

const (
	RPCSourceDatabase RPCSource = "database" // chains.rpc_urls_enc, set in the admin console
	RPCSourceEnv      RPCSource = "env"      // RPC_<CHAIN_ID>
	RPCSourceDefault  RPCSource = "default"  // free public endpoint from package catalog
	RPCSourceNone     RPCSource = ""         // nothing configured (custom chain without endpoints)
)

var catalogRPC = sync.OnceValue(func() map[string][]string {
	m := map[string][]string{}
	for _, c := range catalog.Chains() {
		m[c.ID] = c.RPCURLs
	}
	return m
})

// ResolveRPC returns the endpoints a chain should use and their source:
// the first non-empty of c.RPCURLs (database), RPC_<CHAIN_ID> (env, see
// config.RPCFromEnv) and the catalog default (docs/architecture.md).
func ResolveRPC(c domain.Chain) ([]string, RPCSource) {
	if len(c.RPCURLs) > 0 {
		return append([]string(nil), c.RPCURLs...), RPCSourceDatabase
	}
	if env := config.RPCFromEnv(c.ID); len(env) > 0 {
		return env, RPCSourceEnv
	}
	if def := catalogRPC()[c.ID]; len(def) > 0 {
		return append([]string(nil), def...), RPCSourceDefault
	}
	return nil, RPCSourceNone
}

// EffectiveChain returns a copy of c whose RPCURLs are the effective
// endpoints (ready for a chain.Factory), plus their source.
func EffectiveChain(c domain.Chain) (domain.Chain, RPCSource) {
	urls, src := ResolveRPC(c)
	c.RPCURLs = urls
	return c, src
}

// ValidateRPCURL checks that u is an absolute http(s) or ws(s) URL with a host.
func ValidateRPCURL(u string) error {
	p, err := url.Parse(u)
	if err != nil {
		return fmt.Errorf("invalid rpc url: %v", err)
	}
	switch p.Scheme {
	case "http", "https", "ws", "wss":
	default:
		return fmt.Errorf("rpc url %q must use http, https, ws or wss", redactURL(u))
	}
	if p.Host == "" {
		return fmt.Errorf("rpc url %q has no host", redactURL(u))
	}
	return nil
}

// redactURL keeps scheme and host only (RPC URLs often embed API keys).
func redactURL(u string) string {
	p, err := url.Parse(u)
	if err != nil || p.Host == "" {
		return "…"
	}
	return p.Scheme + "://" + p.Host + "/…"
}
