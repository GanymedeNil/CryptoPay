package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"cryptopay/internal/domain"
)

// ---- merchants ----

const merchantCols = `m.id, m.name, m.status, m.webhook_url, m.webhook_secret_enc, m.created_at, m.updated_at`

func (s *Store) scanMerchant(row pgx.Row) (domain.Merchant, error) {
	var m domain.Merchant
	var status, enc string
	if err := row.Scan(&m.ID, &m.Name, &status, &m.WebhookURL, &enc, &m.CreatedAt, &m.UpdatedAt); err != nil {
		return domain.Merchant{}, err
	}
	m.Status = domain.MerchantStatus(status)
	secret, err := s.box.Decrypt(enc)
	if err != nil {
		return domain.Merchant{}, fmt.Errorf("decrypt webhook secret of merchant %s: %w", m.ID, err)
	}
	m.WebhookSecret = secret
	return m, nil
}

func validMerchantStatus(st domain.MerchantStatus) bool {
	return st == domain.MerchantActive || st == domain.MerchantDisabled
}

// MerchantUpdate lists the merchant fields to change (nil = unchanged).
type MerchantUpdate struct {
	Name       *string
	Status     *domain.MerchantStatus
	WebhookURL *string
}

// MerchantFilter selects merchants for ListMerchants.
type MerchantFilter struct {
	// Q matches the merchant id exactly or the name as a case-insensitive substring.
	Q        string
	Status   domain.MerchantStatus // "" = any
	Page     int                   // 1-based
	PageSize int
}

// CreateMerchant inserts a merchant. WebhookSecret (typically
// auth.NewWebhookSecret()) is encrypted at rest; Status "" means active.
func (s *Store) CreateMerchant(ctx context.Context, m domain.Merchant) (domain.Merchant, error) {
	const op = "create merchant"
	if strings.TrimSpace(m.Name) == "" {
		return domain.Merchant{}, invalidf(op, "name is required")
	}
	if m.Status == "" {
		m.Status = domain.MerchantActive
	}
	if !validMerchantStatus(m.Status) {
		return domain.Merchant{}, invalidf(op, "invalid status %q", m.Status)
	}
	enc, err := s.box.Encrypt(m.WebhookSecret)
	if err != nil {
		return domain.Merchant{}, fmt.Errorf("store: %s: %w", op, err)
	}
	now := s.Now()
	out, err := s.scanMerchant(s.db.QueryRow(ctx, `
		INSERT INTO merchants AS m (id, name, status, webhook_url, webhook_secret_enc, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING `+merchantCols,
		uuidOrNew(m.ID), strings.TrimSpace(m.Name), string(m.Status), m.WebhookURL, enc,
		timeOr(m.CreatedAt, now), timeOr(m.UpdatedAt, now)))
	return out, wrap(op, err)
}

// GetMerchant returns a merchant (with its decrypted webhook secret).
func (s *Store) GetMerchant(ctx context.Context, id uuid.UUID) (domain.Merchant, error) {
	m, err := s.scanMerchant(s.db.QueryRow(ctx, `SELECT `+merchantCols+` FROM merchants m WHERE m.id = $1`, id))
	return m, wrap("get merchant", err)
}

// ListMerchants returns a page of merchants, newest first.
func (s *Store) ListMerchants(ctx context.Context, f MerchantFilter) (Page[domain.Merchant], error) {
	const op = "list merchants"
	page, size, offset := normPage(f.Page, f.PageSize)
	var c conds
	if q := strings.TrimSpace(f.Q); q != "" {
		if id, err := uuid.Parse(q); err == nil {
			c.add("m.id = " + c.arg(id))
		} else {
			c.add("m.name ILIKE '%' || " + c.arg(likeEscape(q)) + " || '%'")
		}
	}
	if f.Status != "" {
		c.add("m.status = " + c.arg(string(f.Status)))
	}
	out := Page[domain.Merchant]{Items: []domain.Merchant{}, Page: page, PageSize: size}
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM merchants m`+c.where(), c.args...).Scan(&out.Total); err != nil {
		return out, wrap(op, err)
	}
	if out.Total == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `SELECT `+merchantCols+` FROM merchants m`+c.where()+
		fmt.Sprintf(` ORDER BY m.created_at DESC, m.id DESC LIMIT %d OFFSET %d`, size, offset), c.args...)
	if err != nil {
		return out, wrap(op, err)
	}
	items, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.Merchant, error) { return s.scanMerchant(r) })
	out.Items = nonNil(items)
	return out, wrap(op, err)
}

// UpdateMerchant changes the given fields and returns the merchant.
func (s *Store) UpdateMerchant(ctx context.Context, id uuid.UUID, upd MerchantUpdate) (domain.Merchant, error) {
	const op = "update merchant"
	var name, status *string
	if upd.Name != nil {
		n := strings.TrimSpace(*upd.Name)
		if n == "" {
			return domain.Merchant{}, invalidf(op, "name is required")
		}
		name = &n
	}
	if upd.Status != nil {
		if !validMerchantStatus(*upd.Status) {
			return domain.Merchant{}, invalidf(op, "invalid status %q", *upd.Status)
		}
		st := string(*upd.Status)
		status = &st
	}
	m, err := s.scanMerchant(s.db.QueryRow(ctx, `
		UPDATE merchants m SET
			name        = COALESCE($2, m.name),
			status      = COALESCE($3, m.status),
			webhook_url = COALESCE($4, m.webhook_url),
			updated_at  = $5
		WHERE m.id = $1
		RETURNING `+merchantCols, id, name, status, upd.WebhookURL, s.Now()))
	return m, wrap(op, err)
}

// RotateWebhookSecret replaces the merchant's webhook signing secret (pass a
// fresh auth.NewWebhookSecret()) and returns the merchant.
func (s *Store) RotateWebhookSecret(ctx context.Context, id uuid.UUID, newSecret string) (domain.Merchant, error) {
	const op = "rotate webhook secret"
	if newSecret == "" {
		return domain.Merchant{}, invalidf(op, "secret is required")
	}
	enc, err := s.box.Encrypt(newSecret)
	if err != nil {
		return domain.Merchant{}, fmt.Errorf("store: %s: %w", op, err)
	}
	m, err := s.scanMerchant(s.db.QueryRow(ctx, `
		UPDATE merchants m SET webhook_secret_enc = $2, updated_at = $3 WHERE m.id = $1
		RETURNING `+merchantCols, id, enc, s.Now()))
	return m, wrap(op, err)
}

// DeleteMerchant deletes a merchant together with its API keys and webhook
// events. It fails with ErrInUse while invoices reference the merchant
// (disable it instead).
func (s *Store) DeleteMerchant(ctx context.Context, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM merchants WHERE id = $1`, id)
	if err != nil {
		return wrapDelete("delete merchant", err)
	}
	if tag.RowsAffected() == 0 {
		return wrap("delete merchant", ErrNotFound)
	}
	return nil
}

// ---- API keys ----

const apiKeyCols = `k.id, k.merchant_id, k.name, k.prefix, k.key_hash, k.last_used_at, k.revoked_at, k.created_at`

func scanAPIKey(row pgx.Row) (domain.APIKey, error) {
	var k domain.APIKey
	err := row.Scan(&k.ID, &k.MerchantID, &k.Name, &k.Prefix, &k.KeyHash, &k.LastUsedAt, &k.RevokedAt, &k.CreatedAt)
	return k, err
}

// APIKeyTouchInterval throttles TouchAPIKey: last_used_at is only written
// when the stored value is older than this.
const APIKeyTouchInterval = time.Minute

// CreateAPIKey stores an API key (see auth.NewAPIKey for Prefix and
// KeyHash; the plaintext key is never stored).
func (s *Store) CreateAPIKey(ctx context.Context, k domain.APIKey) (domain.APIKey, error) {
	const op = "create api key"
	if k.KeyHash == "" || k.Prefix == "" {
		return domain.APIKey{}, invalidf(op, "prefix and key hash are required")
	}
	out, err := scanAPIKey(s.db.QueryRow(ctx, `
		INSERT INTO api_keys AS k (id, merchant_id, name, prefix, key_hash, last_used_at, revoked_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING `+apiKeyCols,
		uuidOrNew(k.ID), k.MerchantID, k.Name, k.Prefix, k.KeyHash, utcPtr(k.LastUsedAt), utcPtr(k.RevokedAt),
		timeOr(k.CreatedAt, s.Now())))
	return out, wrap(op, err)
}

// GetAPIKey returns an API key by id (revoked or not).
func (s *Store) GetAPIKey(ctx context.Context, id uuid.UUID) (domain.APIKey, error) {
	k, err := scanAPIKey(s.db.QueryRow(ctx, `SELECT `+apiKeyCols+` FROM api_keys k WHERE k.id = $1`, id))
	return k, wrap("get api key", err)
}

// ListAPIKeys returns all keys of a merchant (including revoked), newest first.
func (s *Store) ListAPIKeys(ctx context.Context, merchantID uuid.UUID) ([]domain.APIKey, error) {
	rows, err := s.db.Query(ctx, `SELECT `+apiKeyCols+` FROM api_keys k WHERE k.merchant_id = $1
		ORDER BY k.created_at DESC, k.id DESC`, merchantID)
	if err != nil {
		return nil, wrap("list api keys", err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.APIKey, error) { return scanAPIKey(r) })
	return nonNil(out), wrap("list api keys", err)
}

// RevokeAPIKey revokes a key of the given merchant. Revoking an already
// revoked key keeps the original revoked_at. ErrNotFound if the key does not
// belong to the merchant.
func (s *Store) RevokeAPIKey(ctx context.Context, merchantID, keyID uuid.UUID) (domain.APIKey, error) {
	k, err := scanAPIKey(s.db.QueryRow(ctx, `
		UPDATE api_keys k SET revoked_at = COALESCE(k.revoked_at, $3)
		WHERE k.id = $1 AND k.merchant_id = $2
		RETURNING `+apiKeyCols, keyID, merchantID, s.Now()))
	return k, wrap("revoke api key", err)
}

// LookupAPIKey authenticates an API key by its hash (auth.HashToken of the
// bearer token) and returns the key and its merchant. Revoked keys yield
// ErrNotFound. The merchant is returned whatever its status: callers must
// reject domain.MerchantDisabled.
func (s *Store) LookupAPIKey(ctx context.Context, keyHash string) (domain.APIKey, domain.Merchant, error) {
	const op = "lookup api key"
	row := s.db.QueryRow(ctx, `
		SELECT `+apiKeyCols+`, `+merchantCols+`
		FROM api_keys k JOIN merchants m ON m.id = k.merchant_id
		WHERE k.key_hash = $1 AND k.revoked_at IS NULL`, keyHash)
	var k domain.APIKey
	var m domain.Merchant
	var status, enc string
	err := row.Scan(&k.ID, &k.MerchantID, &k.Name, &k.Prefix, &k.KeyHash, &k.LastUsedAt, &k.RevokedAt, &k.CreatedAt,
		&m.ID, &m.Name, &status, &m.WebhookURL, &enc, &m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return domain.APIKey{}, domain.Merchant{}, wrap(op, err)
	}
	m.Status = domain.MerchantStatus(status)
	if m.WebhookSecret, err = s.box.Decrypt(enc); err != nil {
		return domain.APIKey{}, domain.Merchant{}, fmt.Errorf("store: %s: decrypt webhook secret: %w", op, err)
	}
	return k, m, nil
}

// TouchAPIKey records that a key was used. It only writes when the stored
// last_used_at is older than APIKeyTouchInterval, so it is cheap to call on
// every request.
func (s *Store) TouchAPIKey(ctx context.Context, id uuid.UUID) error {
	now := s.Now()
	_, err := s.db.Exec(ctx, `
		UPDATE api_keys SET last_used_at = $2
		WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < $3)`,
		id, now, now.Add(-APIKeyTouchInterval))
	return wrap("touch api key", err)
}
