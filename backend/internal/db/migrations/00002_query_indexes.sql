-- Indexes for the admin search/filter endpoints, the background workers and
-- the overview statistics (docs/api.md). No table changes.

-- +goose Up

-- Admin invoice search `q`: by merchant order id across merchants, by any
-- quote address and by transfer tx hash (case-insensitive: EVM addresses are
-- stored EIP-55 mixed-case but are often pasted lower-case).
CREATE INDEX invoices_order_id_idx ON invoices (merchant_order_id) WHERE merchant_order_id <> '';
CREATE INDEX quotes_address_lower_idx ON quotes (lower(address));
CREATE INDEX transfers_tx_hash_lower_idx ON transfers (lower(tx_hash));

-- Admin transfer search `q` (tx hash or address) and the unmatched filter/count.
CREATE INDEX transfers_to_address_lower_idx ON transfers (lower(to_address));
CREATE INDEX transfers_from_address_lower_idx ON transfers (lower(from_address)) WHERE from_address <> '';
CREATE INDEX transfers_unmatched_idx ON transfers (detected_at DESC) WHERE invoice_id IS NULL;

-- Wallet delete: "is this wallet referenced by any quote?" (also backs the FK).
CREATE INDEX quotes_wallet_idx ON quotes (wallet_id);
-- Closing quotes whose watch window has passed.
CREATE INDEX quotes_live_watch_until_idx ON quotes (watch_until) WHERE status IN ('active', 'superseded');

-- Overview statistics: paid volume by paid_at.
CREATE INDEX invoices_paid_at_idx ON invoices (paid_at) WHERE status = 'paid';

-- Webhook event list filtered by merchant; failed-event count.
CREATE INDEX webhook_events_merchant_created_idx ON webhook_events (merchant_id, created_at DESC);
CREATE INDEX webhook_events_failed_idx ON webhook_events (created_at DESC) WHERE status = 'failed';

-- Audit log filter by action.
CREATE INDEX audit_logs_action_idx ON audit_logs (action, created_at DESC);

-- Periodic cleanup of expired admin sessions.
CREATE INDEX admin_sessions_expires_idx ON admin_sessions (expires_at);

-- +goose Down
DROP INDEX IF EXISTS admin_sessions_expires_idx;
DROP INDEX IF EXISTS audit_logs_action_idx;
DROP INDEX IF EXISTS webhook_events_failed_idx;
DROP INDEX IF EXISTS webhook_events_merchant_created_idx;
DROP INDEX IF EXISTS invoices_paid_at_idx;
DROP INDEX IF EXISTS quotes_live_watch_until_idx;
DROP INDEX IF EXISTS quotes_wallet_idx;
DROP INDEX IF EXISTS transfers_unmatched_idx;
DROP INDEX IF EXISTS transfers_from_address_lower_idx;
DROP INDEX IF EXISTS transfers_to_address_lower_idx;
DROP INDEX IF EXISTS transfers_tx_hash_lower_idx;
DROP INDEX IF EXISTS quotes_address_lower_idx;
DROP INDEX IF EXISTS invoices_order_id_idx;
