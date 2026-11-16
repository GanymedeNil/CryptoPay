package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"cryptopay/internal/domain"
)

const walletCols = `w.id, w.name, w.family, w.kind, w.xpub, w.address, w.next_index, w.created_at, w.updated_at`

// MaxAddressIndex is the largest derivation index AllocateAddressIndex hands
// out (BIP-32 non-hardened indices are 0 … 2^31-1).
const MaxAddressIndex = 1<<31 - 1

func scanWallet(row pgx.Row) (domain.Wallet, error) {
	var w domain.Wallet
	var family, kind string
	var next int64
	err := row.Scan(&w.ID, &w.Name, &family, &kind, &w.Xpub, &w.Address, &next, &w.CreatedAt, &w.UpdatedAt)
	w.Family, w.Kind = domain.Family(family), domain.WalletKind(kind)
	w.NextIndex = uint32(min(max(next, 0), MaxAddressIndex+1))
	return w, err
}

// WalletView is a wallet with the ids of the chains it is the default
// wallet of and the merchants that use it as their own wallet.
type WalletView struct {
	domain.Wallet
	ChainIDs         []string        // chains with this default wallet, sorted; never nil
	MerchantBindings []WalletBinding // by merchant name, chain; never nil
}

// WalletBinding is one merchant using a wallet as its receiving wallet on a
// chain (see SetMerchantWallet).
type WalletBinding struct {
	MerchantID   uuid.UUID `json:"merchant_id"`
	MerchantName string    `json:"merchant_name"`
	ChainID      string    `json:"chain_id"`
}

// CreateWallet inserts a receiving wallet. Xpub must be set for xpub
// wallets and Address (canonical form) for static wallets; validating the
// key / address itself is the caller's job (hdwallet / address packages).
// Key material is unique: an xpub that is already registered yields
// ErrConflict (ConstraintWalletXpub), as does a static address already
// registered for the family (ConstraintWalletStaticAddress).
func (s *Store) CreateWallet(ctx context.Context, w domain.Wallet) (domain.Wallet, error) {
	const op = "create wallet"
	if strings.TrimSpace(w.Name) == "" {
		return domain.Wallet{}, invalidf(op, "name is required")
	}
	if !w.Family.Valid() {
		return domain.Wallet{}, invalidf(op, "invalid family %q", w.Family)
	}
	switch w.Kind {
	case domain.WalletXpub:
		if w.Xpub == "" {
			return domain.Wallet{}, invalidf(op, "xpub wallet needs an extended public key")
		}
		w.Address = ""
	case domain.WalletStatic:
		if w.Address == "" {
			return domain.Wallet{}, invalidf(op, "static wallet needs an address")
		}
		w.Xpub, w.NextIndex = "", 0
	default:
		return domain.Wallet{}, invalidf(op, "invalid kind %q", w.Kind)
	}
	now := s.Now()
	out, err := scanWallet(s.db.QueryRow(ctx, `
		INSERT INTO wallets AS w (id, name, family, kind, xpub, address, next_index, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING `+walletCols,
		uuidOrNew(w.ID), strings.TrimSpace(w.Name), string(w.Family), string(w.Kind), w.Xpub, w.Address,
		int64(w.NextIndex), timeOr(w.CreatedAt, now), timeOr(w.UpdatedAt, now)))
	return out, wrap(op, err)
}

// GetWallet returns a wallet by id.
func (s *Store) GetWallet(ctx context.Context, id uuid.UUID) (domain.Wallet, error) {
	w, err := scanWallet(s.db.QueryRow(ctx, `SELECT `+walletCols+` FROM wallets w WHERE w.id = $1`, id))
	return w, wrap("get wallet", err)
}

const walletViewSQL = `SELECT ` + walletCols + `,
	COALESCE(ARRAY(SELECT c.id FROM chains c WHERE c.wallet_id = w.id ORDER BY c.id), '{}'),
	COALESCE((SELECT json_agg(json_build_object('merchant_id', mw.merchant_id, 'merchant_name', m.name, 'chain_id', mw.chain_id)
		ORDER BY m.name, m.id, mw.chain_id)
		FROM merchant_wallets mw JOIN merchants m ON m.id = mw.merchant_id WHERE mw.wallet_id = w.id), '[]')::text
	FROM wallets w`

func scanWalletView(row pgx.Row) (WalletView, error) {
	var v WalletView
	var family, kind, bindings string
	var next int64
	err := row.Scan(&v.ID, &v.Name, &family, &kind, &v.Xpub, &v.Address, &next, &v.CreatedAt, &v.UpdatedAt, &v.ChainIDs, &bindings)
	if err != nil {
		return WalletView{}, err
	}
	v.Family, v.Kind = domain.Family(family), domain.WalletKind(kind)
	v.NextIndex = uint32(min(max(next, 0), MaxAddressIndex+1))
	v.ChainIDs = nonNil(v.ChainIDs)
	if err := json.Unmarshal([]byte(bindings), &v.MerchantBindings); err != nil {
		return WalletView{}, fmt.Errorf("decode merchant bindings of wallet %s: %w", v.ID, err)
	}
	v.MerchantBindings = nonNil(v.MerchantBindings)
	return v, nil
}

// GetWalletView returns a wallet with its assigned chain ids and merchant bindings.
func (s *Store) GetWalletView(ctx context.Context, id uuid.UUID) (WalletView, error) {
	v, err := scanWalletView(s.db.QueryRow(ctx, walletViewSQL+` WHERE w.id = $1`, id))
	return v, wrap("get wallet", err)
}

// ListWallets returns all wallets with their assigned chain ids and merchant
// bindings, oldest first.
func (s *Store) ListWallets(ctx context.Context) ([]WalletView, error) {
	rows, err := s.db.Query(ctx, walletViewSQL+` ORDER BY w.created_at, w.id`)
	if err != nil {
		return nil, wrap("list wallets", err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (WalletView, error) { return scanWalletView(r) })
	return nonNil(out), wrap("list wallets", err)
}

// RenameWallet changes a wallet's name (the only mutable attribute: the key
// material is immutable once quotes may reference derived addresses).
func (s *Store) RenameWallet(ctx context.Context, id uuid.UUID, name string) (domain.Wallet, error) {
	const op = "rename wallet"
	name = strings.TrimSpace(name)
	if name == "" {
		return domain.Wallet{}, invalidf(op, "name is required")
	}
	w, err := scanWallet(s.db.QueryRow(ctx, `UPDATE wallets w SET name = $2, updated_at = $3 WHERE w.id = $1
		RETURNING `+walletCols, id, name, s.Now()))
	return w, wrap(op, err)
}

// WalletInUse reports whether any quote or merchant binding references the
// wallet (a chain's default wallet assignment does not count).
func (s *Store) WalletInUse(ctx context.Context, id uuid.UUID) (bool, error) {
	var used bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM quotes WHERE wallet_id = $1)
		OR EXISTS (SELECT 1 FROM merchant_wallets WHERE wallet_id = $1)`, id).Scan(&used)
	return used, wrap("wallet in use", err)
}

// DeleteWallet deletes a wallet; chains it was the default wallet of are
// unassigned. It fails with ErrInUse while quotes or merchant bindings
// reference the wallet.
func (s *Store) DeleteWallet(ctx context.Context, id uuid.UUID) error {
	const op = "delete wallet"
	used, err := s.WalletInUse(ctx, id)
	if err != nil {
		return err
	}
	if used {
		return fmt.Errorf("store: %s: %w", op, ErrInUse)
	}
	tag, err := s.db.Exec(ctx, `DELETE FROM wallets WHERE id = $1`, id)
	if err != nil {
		return wrapDelete(op, err) // a quote / binding created concurrently still trips the FK
	}
	if tag.RowsAffected() == 0 {
		return wrap(op, ErrNotFound)
	}
	return nil
}

// AllocateAddressIndex atomically reserves the next derivation index of an
// xpub wallet and returns it (concurrent callers always get distinct
// indices). ErrNotFound for an unknown wallet; ErrInvalid for a static
// wallet or when the index space is exhausted.
//
// To give one invoice the same address on every chain of a wallet, first
// look for an existing assignment with FindInvoiceAddress — inside a
// transaction holding GetInvoiceForUpdate so concurrent quote creation for
// the same invoice cannot allocate twice.
func (s *Store) AllocateAddressIndex(ctx context.Context, walletID uuid.UUID) (uint32, error) {
	const op = "allocate address index"
	var idx int64
	err := s.db.QueryRow(ctx, `
		UPDATE wallets SET next_index = next_index + 1, updated_at = $2
		WHERE id = $1 AND kind = 'xpub' AND next_index <= $3
		RETURNING next_index - 1`, walletID, s.Now(), int64(MaxAddressIndex)).Scan(&idx)
	if err == nil {
		return uint32(idx), nil
	}
	if !isNoRows(err) {
		return 0, wrap(op, err)
	}
	w, gerr := s.GetWallet(ctx, walletID)
	if gerr != nil {
		return 0, fmt.Errorf("store: %s: %w", op, ErrNotFound)
	}
	if w.Kind != domain.WalletXpub {
		return 0, invalidf(op, "wallet %s is not an xpub wallet", walletID)
	}
	return 0, invalidf(op, "wallet %s has no derivation indices left", walletID)
}
