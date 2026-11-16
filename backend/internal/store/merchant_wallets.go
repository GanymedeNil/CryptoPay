package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"cryptopay/internal/domain"
)

// MerchantWallet binds a merchant-specific receiving wallet to one chain:
// the merchant's invoices on that chain are paid to it instead of to the
// chain's default wallet (docs/architecture.md, "Receiving wallets").
type MerchantWallet struct {
	MerchantID uuid.UUID
	ChainID    string
	WalletID   uuid.UUID
	WalletName string            // read-only, from the wallet
	WalletKind domain.WalletKind // read-only, from the wallet
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

const merchantWalletCols = `mw.merchant_id, mw.chain_id, mw.wallet_id, w.name, w.kind, mw.created_at, mw.updated_at`

func scanMerchantWallet(row pgx.Row) (MerchantWallet, error) {
	var b MerchantWallet
	var kind string
	err := row.Scan(&b.MerchantID, &b.ChainID, &b.WalletID, &b.WalletName, &kind, &b.CreatedAt, &b.UpdatedAt)
	b.WalletKind = domain.WalletKind(kind)
	return b, err
}

// ListMerchantWallets returns the explicit wallet bindings of a merchant
// (chains without one use their default wallet), ordered like ListChains:
// mainnets first, then by chain name. Never nil.
func (s *Store) ListMerchantWallets(ctx context.Context, merchantID uuid.UUID) ([]MerchantWallet, error) {
	rows, err := s.db.Query(ctx, `SELECT `+merchantWalletCols+`
		FROM merchant_wallets mw JOIN wallets w ON w.id = mw.wallet_id JOIN chains c ON c.id = mw.chain_id
		WHERE mw.merchant_id = $1
		ORDER BY c.is_testnet, c.name, c.id`, merchantID)
	if err != nil {
		return nil, wrap("list merchant wallets", err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (MerchantWallet, error) { return scanMerchantWallet(r) })
	return nonNil(out), wrap("list merchant wallets", err)
}

// GetMerchantWallet returns the merchant's binding on a chain, ErrNotFound
// when there is none.
func (s *Store) GetMerchantWallet(ctx context.Context, merchantID uuid.UUID, chainID string) (MerchantWallet, error) {
	b, err := scanMerchantWallet(s.db.QueryRow(ctx, `SELECT `+merchantWalletCols+`
		FROM merchant_wallets mw JOIN wallets w ON w.id = mw.wallet_id
		WHERE mw.merchant_id = $1 AND mw.chain_id = $2`, merchantID, chainID))
	return b, wrap("get merchant wallet", err)
}

// SetMerchantWallet makes walletID the merchant's receiving wallet on
// chainID (insert or replace). The wallet must belong to the chain's family
// (else ErrInvalid); an unknown merchant, chain or wallet yields
// ErrForeignKey. As for UpdateChain, checking that a static wallet's address
// is valid on the chain is the caller's job (address package).
//
// Quotes already created keep the wallet and address they were created
// with; only new quotes use the new binding.
func (s *Store) SetMerchantWallet(ctx context.Context, merchantID uuid.UUID, chainID string, walletID uuid.UUID) (MerchantWallet, error) {
	const op = "set merchant wallet"
	var ok bool
	err := s.db.QueryRow(ctx, `SELECT w.family = c.family FROM wallets w, chains c WHERE w.id = $1 AND c.id = $2`,
		walletID, chainID).Scan(&ok)
	switch {
	case isNoRows(err):
		// Unknown wallet or chain: let the INSERT below report it.
	case err != nil:
		return MerchantWallet{}, wrap(op, err)
	case !ok:
		return MerchantWallet{}, invalidf(op, "wallet %s belongs to another family than chain %s", walletID, chainID)
	}
	b, err := scanMerchantWallet(s.db.QueryRow(ctx, `
		WITH up AS (
			INSERT INTO merchant_wallets AS mw (merchant_id, chain_id, wallet_id, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $4)
			ON CONFLICT (merchant_id, chain_id) DO UPDATE SET wallet_id = EXCLUDED.wallet_id, updated_at = EXCLUDED.updated_at
			RETURNING mw.*)
		SELECT `+merchantWalletCols+` FROM up mw JOIN wallets w ON w.id = mw.wallet_id`,
		merchantID, chainID, walletID, s.Now()))
	return b, wrap(op, err)
}

// DeleteMerchantWallet removes the merchant's binding on a chain, so the
// merchant falls back to the chain's default wallet. It reports whether a
// binding existed (removing a missing one is not an error).
func (s *Store) DeleteMerchantWallet(ctx context.Context, merchantID uuid.UUID, chainID string) (bool, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM merchant_wallets WHERE merchant_id = $1 AND chain_id = $2`, merchantID, chainID)
	if err != nil {
		return false, wrap("delete merchant wallet", err)
	}
	return tag.RowsAffected() > 0, nil
}

// WalletSource tells where a resolved wallet comes from.
type WalletSource string

const (
	WalletSourceMerchant WalletSource = "merchant" // the merchant's own wallet on the chain
	WalletSourceDefault  WalletSource = "default"  // the chain's default wallet
)

// ResolvedWallet is the receiving wallet of a merchant on a chain.
type ResolvedWallet struct {
	domain.Wallet
	Source WalletSource
}

// resolveWalletJoin implements the wallet resolution rule for the merchant
// given as $1 on chain ch: the merchant's binding (mw) if present,
// otherwise the chain's default wallet; w is the resolved wallet and
// w.id IS NULL when there is none. ResolveWallet and the payable-asset
// queries all use it, so the rule exists exactly once.
const resolveWalletJoin = `
	LEFT JOIN merchant_wallets mw ON mw.chain_id = ch.id AND mw.merchant_id = $1
	LEFT JOIN wallets w ON w.id = COALESCE(mw.wallet_id, ch.wallet_id)`

// ResolveWallet returns the receiving wallet of a merchant on a chain: the
// merchant's own wallet when one is bound, otherwise the chain's default
// wallet. ErrNotFound when there is neither (or the chain does not exist).
// Whether the chain is enabled is not considered (see ListPayableAssets).
func (s *Store) ResolveWallet(ctx context.Context, merchantID uuid.UUID, chainID string) (ResolvedWallet, error) {
	var out ResolvedWallet
	var family, kind string
	var next int64
	var own bool
	err := s.db.QueryRow(ctx, `SELECT `+walletCols+`, mw.wallet_id IS NOT NULL
		FROM chains ch`+resolveWalletJoin+`
		WHERE ch.id = $2 AND w.id IS NOT NULL`, merchantID, chainID).
		Scan(&out.ID, &out.Name, &family, &kind, &out.Xpub, &out.Address, &next, &out.CreatedAt, &out.UpdatedAt, &own)
	if err != nil {
		return ResolvedWallet{}, wrap("resolve wallet", err)
	}
	out.Family, out.Kind = domain.Family(family), domain.WalletKind(kind)
	out.NextIndex = uint32(min(max(next, 0), MaxAddressIndex+1))
	out.Source = WalletSourceDefault
	if own {
		out.Source = WalletSourceMerchant
	}
	return out, nil
}
