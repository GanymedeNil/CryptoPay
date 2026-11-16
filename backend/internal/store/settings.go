package store

import (
	"context"
	"encoding/json"
	"fmt"

	"cryptopay/internal/catalog"
	"cryptopay/internal/domain"
)

// settingsKey is the key of the single settings row.
const settingsKey = "app"

// GetSettings returns the runtime settings. Fields missing from the stored
// JSON (or a missing row) fall back to domain.DefaultSettings().
func (s *Store) GetSettings(ctx context.Context) (domain.Settings, error) {
	const op = "get settings"
	out := domain.DefaultSettings()
	var raw string
	err := s.db.QueryRow(ctx, `SELECT value::text FROM settings WHERE key = $1`, settingsKey).Scan(&raw)
	if isNoRows(err) {
		return out, nil
	}
	if err != nil {
		return out, wrap(op, err)
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return domain.DefaultSettings(), fmt.Errorf("store: %s: decode: %w", op, err)
	}
	return out, nil
}

// PutSettings replaces the runtime settings and returns them as stored.
// Validation of the values is the caller's job.
func (s *Store) PutSettings(ctx context.Context, v domain.Settings) (domain.Settings, error) {
	const op = "put settings"
	raw, err := json.Marshal(v)
	if err != nil {
		return domain.Settings{}, fmt.Errorf("store: %s: encode: %w", op, err)
	}
	_, err = s.db.Exec(ctx, `
		INSERT INTO settings (key, value, updated_at) VALUES ($1, $2::jsonb, $3)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = EXCLUDED.updated_at`,
		settingsKey, string(raw), s.Now())
	if err != nil {
		return domain.Settings{}, wrap(op, err)
	}
	return s.GetSettings(ctx)
}

// EnsureSettings creates the settings row with defaults if it is missing,
// and adds default values for fields introduced since the row was written
// (existing values are never overwritten).
func (s *Store) EnsureSettings(ctx context.Context) error {
	raw, err := json.Marshal(domain.DefaultSettings())
	if err != nil {
		return fmt.Errorf("store: ensure settings: encode: %w", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return fmt.Errorf("store: ensure settings: encode: %w", err)
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	// "EXCLUDED.value || settings.value": stored values win, missing keys
	// take the default. Only touch the row when a key is actually missing.
	_, err = s.db.Exec(ctx, `
		INSERT INTO settings (key, value, updated_at) VALUES ($1, $2::jsonb, $3)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value || settings.value, updated_at = EXCLUDED.updated_at
		WHERE NOT (settings.value ?& $4::text[])`,
		settingsKey, string(raw), s.Now(), keys)
	return wrap("ensure settings", err)
}

// CatalogResult reports what EnsureCatalog inserted.
type CatalogResult struct {
	ChainsInserted int
	AssetsInserted int
}

// EnsureCatalog inserts the given chains and assets when they do not exist
// yet. Existing rows (which an admin may have edited) are never modified.
// The RPCURLs of the given chains are NOT stored: built-in defaults are
// resolved at runtime (see ResolveRPC), so the database only holds
// admin-configured endpoints. Chains are inserted before assets; run it
// inside WithTx for all-or-nothing behaviour (Bootstrap does).
func (s *Store) EnsureCatalog(ctx context.Context, chains []domain.Chain, assets []domain.Asset) (CatalogResult, error) {
	const op = "ensure catalog"
	var res CatalogResult
	now := s.Now()
	for _, c := range chains {
		params, err := json.Marshal(c.Params)
		if err != nil {
			return res, fmt.Errorf("store: %s: chain %s params: %w", op, c.ID, err)
		}
		tag, err := s.db.Exec(ctx, `
			INSERT INTO chains (id, family, name, network_id, native_symbol, is_testnet, enabled,
			                    confirmations, poll_interval_ms, scan_batch, explorer_tx_url, explorer_addr_url,
			                    params, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13::jsonb, $14, $14)
			ON CONFLICT (id) DO NOTHING`,
			c.ID, string(c.Family), c.Name, c.NetworkID, c.NativeSymbol, c.IsTestnet, c.Enabled,
			c.Confirmations, c.PollInterval.Milliseconds(), c.ScanBatch, c.ExplorerTxURL, c.ExplorerAddr,
			string(params), now)
		if err != nil {
			return res, wrap(op+" chain "+c.ID, err)
		}
		res.ChainsInserted += int(tag.RowsAffected())
	}
	for _, a := range assets {
		tag, err := s.db.Exec(ctx, `
			INSERT INTO assets (id, chain_id, symbol, name, contract_address, decimals, quote_precision,
			                    rate_key, is_stablecoin, enabled, sort_order, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $12)
			ON CONFLICT DO NOTHING`,
			a.ID, a.ChainID, a.Symbol, a.Name, a.ContractAddress, a.Decimals, a.QuotePrecision,
			a.RateKey, a.IsStablecoin, a.Enabled, a.SortOrder, now)
		if err != nil {
			return res, wrap(op+" asset "+a.ID, err)
		}
		res.AssetsInserted += int(tag.RowsAffected())
	}
	return res, nil
}

// Bootstrap runs EnsureCatalog with the built-in catalog
// (catalog.Chains/Assets) and EnsureSettings in one transaction. It is
// idempotent and meant to run at every startup after migrations.
func (s *Store) Bootstrap(ctx context.Context) (CatalogResult, error) {
	var res CatalogResult
	err := s.WithTx(ctx, func(tx *Store) error {
		var err error
		if res, err = tx.EnsureCatalog(ctx, catalog.Chains(), catalog.Assets()); err != nil {
			return err
		}
		return tx.EnsureSettings(ctx)
	})
	return res, err
}
