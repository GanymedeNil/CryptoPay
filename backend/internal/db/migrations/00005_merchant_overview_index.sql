-- Per-merchant overview (docs/api.md: GET /overview?merchant_id=,
-- GET /overview/merchants). No table changes.

-- +goose Up

-- A merchant's newest payment (last_paid_at) and its payments in a range.
-- Without it the "newest paid_at" lookup of a merchant with few or no
-- payments walks the whole invoices_paid_at_idx.
CREATE INDEX invoices_merchant_paid_at_idx ON invoices (merchant_id, paid_at) WHERE status = 'paid';

-- +goose Down
DROP INDEX IF EXISTS invoices_merchant_paid_at_idx;
