package store

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"

	"cryptopay/internal/domain"
)

const assetCols = `a.id, a.chain_id, a.symbol, a.name, a.contract_address, a.decimals, a.quote_precision,
	a.rate_key, a.is_stablecoin, a.enabled, a.sort_order, a.created_at, a.updated_at`

func scanAsset(row pgx.Row) (domain.Asset, error) {
	var a domain.Asset
	err := row.Scan(&a.ID, &a.ChainID, &a.Symbol, &a.Name, &a.ContractAddress, &a.Decimals, &a.QuotePrecision,
		&a.RateKey, &a.IsStablecoin, &a.Enabled, &a.SortOrder, &a.CreatedAt, &a.UpdatedAt)
	return a, err
}

func collectAssets(rows pgx.Rows, err error, op string) ([]domain.Asset, error) {
	if err != nil {
		return nil, wrap(op, err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.Asset, error) { return scanAsset(r) })
	return nonNil(out), wrap(op, err)
}

// AssetID returns the conventional id of a custom asset: lower(symbol) + "-" + chainID.
func AssetID(symbol, chainID string) string {
	return strings.ToLower(strings.TrimSpace(symbol)) + "-" + chainID
}

// AssetFilter selects assets for ListAssets.
type AssetFilter struct {
	ChainID     string // "" = all chains
	EnabledOnly bool   // only assets with enabled = true (chain state is ignored; see ListPayableAssets)
}

// ListAssets returns assets ordered by sort_order, id.
func (s *Store) ListAssets(ctx context.Context, f AssetFilter) ([]domain.Asset, error) {
	var c conds
	if f.ChainID != "" {
		c.add("a.chain_id = " + c.arg(f.ChainID))
	}
	if f.EnabledOnly {
		c.add("a.enabled")
	}
	rows, err := s.db.Query(ctx, `SELECT `+assetCols+` FROM assets a`+c.where()+` ORDER BY a.sort_order, a.id`, c.args...)
	return collectAssets(rows, err, "list assets")
}

// GetAsset returns an asset by id.
func (s *Store) GetAsset(ctx context.Context, id string) (domain.Asset, error) {
	a, err := scanAsset(s.db.QueryRow(ctx, `SELECT `+assetCols+` FROM assets a WHERE a.id = $1`, id))
	return a, wrap("get asset", err)
}

// payableJoin restricts assets to those payable right now: asset enabled,
// chain enabled and a wallet assigned to the chain.
const payableJoin = ` FROM assets a JOIN chains ch ON ch.id = a.chain_id
	WHERE a.enabled AND ch.enabled AND ch.wallet_id IS NOT NULL`

// ListPayableAssets returns the assets customers can pay with right now
// (asset enabled ∧ chain enabled ∧ chain has a wallet), by sort_order, id.
func (s *Store) ListPayableAssets(ctx context.Context) ([]domain.Asset, error) {
	rows, err := s.db.Query(ctx, `SELECT `+assetCols+payableJoin+` ORDER BY a.sort_order, a.id`)
	return collectAssets(rows, err, "list payable assets")
}

// GetPayableAsset returns the asset if it is payable right now, else ErrNotFound.
func (s *Store) GetPayableAsset(ctx context.Context, id string) (domain.Asset, error) {
	a, err := scanAsset(s.db.QueryRow(ctx, `SELECT `+assetCols+payableJoin+` AND a.id = $1`, id))
	return a, wrap("get payable asset", err)
}

// CreateAsset inserts a (custom) asset. An empty ID defaults to
// AssetID(symbol, chain). Duplicate id → ErrConflict (ConstraintAssetPK);
// duplicate (chain, contract) → ErrConflict (ConstraintAssetChainContract);
// unknown chain → ErrForeignKey; quote_precision > decimals → ErrInvalid.
// Enabled and SortOrder are stored as given.
func (s *Store) CreateAsset(ctx context.Context, a domain.Asset) (domain.Asset, error) {
	const op = "create asset"
	if strings.TrimSpace(a.Symbol) == "" || a.ChainID == "" || strings.TrimSpace(a.RateKey) == "" {
		return domain.Asset{}, invalidf(op, "symbol, chain and rate key are required")
	}
	if a.ID == "" {
		a.ID = AssetID(a.Symbol, a.ChainID)
	}
	if strings.TrimSpace(a.Name) == "" {
		a.Name = a.Symbol
	}
	now := s.Now()
	var out domain.Asset
	err := s.guarded(ctx, func(db dbtx) error {
		var err error
		out, err = scanAsset(db.QueryRow(ctx, `
			INSERT INTO assets AS a (id, chain_id, symbol, name, contract_address, decimals, quote_precision,
			                         rate_key, is_stablecoin, enabled, sort_order, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
			RETURNING `+assetCols,
			a.ID, a.ChainID, strings.TrimSpace(a.Symbol), strings.TrimSpace(a.Name), a.ContractAddress, a.Decimals,
			a.QuotePrecision, strings.TrimSpace(a.RateKey), a.IsStablecoin, a.Enabled, a.SortOrder,
			timeOr(a.CreatedAt, now), timeOr(a.UpdatedAt, now)))
		return err
	})
	return out, wrap(op, err)
}

// AssetUpdate lists the admin-editable asset fields to change (nil = unchanged).
type AssetUpdate struct {
	Enabled        *bool
	QuotePrecision *int
	SortOrder      *int
	RateKey        *string
	Name           *string
}

// UpdateAsset applies an admin edit and returns the asset (quote_precision
// > decimals → ErrInvalid).
func (s *Store) UpdateAsset(ctx context.Context, id string, upd AssetUpdate) (domain.Asset, error) {
	const op = "update asset"
	if upd.RateKey != nil && strings.TrimSpace(*upd.RateKey) == "" {
		return domain.Asset{}, invalidf(op, "rate key must not be empty")
	}
	if upd.Name != nil && strings.TrimSpace(*upd.Name) == "" {
		return domain.Asset{}, invalidf(op, "name must not be empty")
	}
	a, err := scanAsset(s.db.QueryRow(ctx, `
		UPDATE assets a SET
			enabled         = COALESCE($2, a.enabled),
			quote_precision = COALESCE($3, a.quote_precision),
			sort_order      = COALESCE($4, a.sort_order),
			rate_key        = COALESCE(btrim($5), a.rate_key),
			name            = COALESCE(btrim($6), a.name),
			updated_at      = $7
		WHERE a.id = $1
		RETURNING `+assetCols,
		id, upd.Enabled, upd.QuotePrecision, upd.SortOrder, upd.RateKey, upd.Name, s.Now()))
	return a, wrap(op, err)
}

// ---- catalog snapshot ----

// Catalog is an in-memory snapshot of all chains and assets, convenient for
// enriching quotes / transfers / assets in API responses without per-row
// queries. Build it with LoadCatalog.
type Catalog struct {
	Chains []domain.Chain // as ListChains (RPCURLs = admin-configured only)
	Assets []domain.Asset // as ListAssets, by sort_order

	chains map[string]int
	assets map[string]int
}

// LoadCatalog loads all chains and assets.
func (s *Store) LoadCatalog(ctx context.Context) (*Catalog, error) {
	chains, err := s.ListChains(ctx)
	if err != nil {
		return nil, err
	}
	assets, err := s.ListAssets(ctx, AssetFilter{})
	if err != nil {
		return nil, err
	}
	return NewCatalog(chains, assets), nil
}

// NewCatalog builds a Catalog from chains and assets (handy in tests).
func NewCatalog(chains []domain.Chain, assets []domain.Asset) *Catalog {
	c := &Catalog{Chains: chains, Assets: assets, chains: map[string]int{}, assets: map[string]int{}}
	for i, ch := range chains {
		c.chains[ch.ID] = i
	}
	for i, a := range assets {
		c.assets[a.ID] = i
	}
	return c
}

// Chain returns the chain with the given id.
func (c *Catalog) Chain(id string) (domain.Chain, bool) {
	i, ok := c.chains[id]
	if !ok {
		return domain.Chain{}, false
	}
	return c.Chains[i], true
}

// Asset returns the asset with the given id.
func (c *Catalog) Asset(id string) (domain.Asset, bool) {
	i, ok := c.assets[id]
	if !ok {
		return domain.Asset{}, false
	}
	return c.Assets[i], true
}

// AssetByContract finds the asset of a chain by token contract ("" = the
// native coin). Contracts are compared exactly (canonical form).
func (c *Catalog) AssetByContract(chainID, contract string) (domain.Asset, bool) {
	for _, a := range c.Assets {
		if a.ChainID == chainID && a.ContractAddress == contract {
			return a, true
		}
	}
	return domain.Asset{}, false
}

// Payable reports whether an asset is payable right now (asset enabled,
// chain enabled, wallet assigned) — the same rule as ListPayableAssets.
func (c *Catalog) Payable(a domain.Asset) bool {
	ch, ok := c.Chain(a.ChainID)
	return ok && a.Enabled && ch.Enabled && ch.WalletID != nil
}

// PayableAssets returns the payable assets in sort order.
func (c *Catalog) PayableAssets() []domain.Asset {
	out := []domain.Asset{}
	for _, a := range c.Assets {
		if c.Payable(a) {
			out = append(out, a)
		}
	}
	return out
}
