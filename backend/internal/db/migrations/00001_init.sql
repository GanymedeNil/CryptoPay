-- +goose Up

CREATE TABLE admin_users (
    id            uuid PRIMARY KEY,
    email         text NOT NULL,
    password_hash text NOT NULL,
    role          text NOT NULL CHECK (role IN ('owner', 'admin', 'viewer')),
    active        boolean NOT NULL DEFAULT true,
    last_login_at timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX admin_users_email_key ON admin_users (lower(email));

CREATE TABLE admin_sessions (
    id         uuid PRIMARY KEY,
    user_id    uuid NOT NULL REFERENCES admin_users (id) ON DELETE CASCADE,
    token_hash text NOT NULL UNIQUE,
    ip         text NOT NULL DEFAULT '',
    user_agent text NOT NULL DEFAULT '',
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX admin_sessions_user_idx ON admin_sessions (user_id);

CREATE TABLE merchants (
    id                 uuid PRIMARY KEY,
    name               text NOT NULL,
    status             text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    webhook_url        text NOT NULL DEFAULT '',
    webhook_secret_enc text NOT NULL DEFAULT '', -- AES-GCM encrypted
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE api_keys (
    id           uuid PRIMARY KEY,
    merchant_id  uuid NOT NULL REFERENCES merchants (id) ON DELETE CASCADE,
    name         text NOT NULL DEFAULT '',
    prefix       text NOT NULL,
    key_hash     text NOT NULL UNIQUE, -- hex sha256 of the full key
    last_used_at timestamptz,
    revoked_at   timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX api_keys_merchant_idx ON api_keys (merchant_id);

CREATE TABLE wallets (
    id         uuid PRIMARY KEY,
    name       text NOT NULL,
    family     text NOT NULL CHECK (family IN ('evm', 'tron', 'utxo', 'solana')),
    kind       text NOT NULL CHECK (kind IN ('xpub', 'static')),
    xpub       text NOT NULL DEFAULT '',
    address    text NOT NULL DEFAULT '',
    next_index bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((kind = 'xpub' AND xpub <> '') OR (kind = 'static' AND address <> ''))
);

CREATE TABLE chains (
    id               text PRIMARY KEY,
    family           text NOT NULL CHECK (family IN ('evm', 'tron', 'utxo', 'solana')),
    name             text NOT NULL,
    network_id       text NOT NULL DEFAULT '',
    native_symbol    text NOT NULL,
    is_testnet       boolean NOT NULL DEFAULT false,
    enabled          boolean NOT NULL DEFAULT true,
    rpc_urls_enc     text NOT NULL DEFAULT '', -- AES-GCM encrypted JSON array; '' = use env / built-in default
    confirmations    integer NOT NULL CHECK (confirmations >= 1),
    poll_interval_ms integer NOT NULL CHECK (poll_interval_ms >= 500),
    scan_batch       integer NOT NULL CHECK (scan_batch >= 1),
    explorer_tx_url  text NOT NULL DEFAULT '',
    explorer_addr_url text NOT NULL DEFAULT '',
    params           jsonb NOT NULL DEFAULT '{}',
    wallet_id        uuid REFERENCES wallets (id) ON DELETE SET NULL,
    scan_cursor      bigint NOT NULL DEFAULT 0,
    last_head        bigint NOT NULL DEFAULT 0,
    last_scan_at     timestamptz,
    last_error       text NOT NULL DEFAULT '',
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE assets (
    id               text PRIMARY KEY,
    chain_id         text NOT NULL REFERENCES chains (id) ON DELETE CASCADE,
    symbol           text NOT NULL,
    name             text NOT NULL,
    contract_address text NOT NULL DEFAULT '',
    decimals         integer NOT NULL CHECK (decimals BETWEEN 0 AND 36),
    quote_precision  integer NOT NULL CHECK (quote_precision >= 0),
    rate_key         text NOT NULL,
    is_stablecoin    boolean NOT NULL DEFAULT false,
    enabled          boolean NOT NULL DEFAULT true,
    sort_order       integer NOT NULL DEFAULT 100,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    CHECK (quote_precision <= decimals),
    UNIQUE (chain_id, contract_address)
);

CREATE TABLE invoices (
    id                uuid PRIMARY KEY,
    merchant_id       uuid NOT NULL REFERENCES merchants (id),
    merchant_order_id text NOT NULL DEFAULT '',
    title             text NOT NULL DEFAULT '',
    description       text NOT NULL DEFAULT '',
    price_amount      numeric(38, 18) NOT NULL CHECK (price_amount > 0),
    price_currency    text NOT NULL,
    amount_usd        numeric(38, 18),
    status            text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'partial', 'confirming', 'paid', 'underpaid', 'expired', 'cancelled')),
    overpaid          boolean NOT NULL DEFAULT false,
    paid_late         boolean NOT NULL DEFAULT false,
    paid_quote_id     uuid,
    success_url       text NOT NULL DEFAULT '',
    cancel_url        text NOT NULL DEFAULT '',
    customer_email    text NOT NULL DEFAULT '',
    metadata          jsonb NOT NULL DEFAULT '{}',
    expires_at        timestamptz NOT NULL,
    paid_at           timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX invoices_merchant_order_key ON invoices (merchant_id, merchant_order_id) WHERE merchant_order_id <> '';
CREATE INDEX invoices_status_expires_idx ON invoices (status, expires_at);
CREATE INDEX invoices_merchant_created_idx ON invoices (merchant_id, created_at DESC);
CREATE INDEX invoices_created_idx ON invoices (created_at DESC);

CREATE TABLE quotes (
    id               uuid PRIMARY KEY,
    invoice_id       uuid NOT NULL REFERENCES invoices (id) ON DELETE CASCADE,
    asset_id         text NOT NULL REFERENCES assets (id),
    chain_id         text NOT NULL REFERENCES chains (id),
    wallet_id        uuid NOT NULL REFERENCES wallets (id),
    wallet_kind      text NOT NULL CHECK (wallet_kind IN ('xpub', 'static')),
    address          text NOT NULL,
    address_index    bigint,
    rate             numeric(38, 18) NOT NULL,
    amount_due       numeric(78, 0) NOT NULL CHECK (amount_due > 0),
    amount_received  numeric(78, 0) NOT NULL DEFAULT 0,
    amount_confirmed numeric(78, 0) NOT NULL DEFAULT 0,
    status           text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'superseded', 'paid', 'closed')),
    watch_until      timestamptz NOT NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX quotes_invoice_idx ON quotes (invoice_id);
-- Lookup used by the scanner: who is watching this address on this chain?
CREATE INDEX quotes_watch_idx ON quotes (chain_id, address) WHERE status IN ('active', 'superseded');
-- At most one live quote per invoice and asset.
CREATE UNIQUE INDEX quotes_invoice_asset_live_key ON quotes (invoice_id, asset_id) WHERE status IN ('active', 'superseded');
-- Static wallets tell invoices apart by amount: it must be unique among watched quotes.
CREATE UNIQUE INDEX quotes_static_amount_key ON quotes (chain_id, asset_id, address, amount_due)
    WHERE wallet_kind = 'static' AND status IN ('active', 'superseded');

CREATE TABLE transfers (
    id             uuid PRIMARY KEY,
    chain_id       text NOT NULL REFERENCES chains (id),
    tx_hash        text NOT NULL,
    idx            integer NOT NULL,
    block_number   bigint NOT NULL,
    block_hash     text NOT NULL DEFAULT '',
    block_time     timestamptz NOT NULL,
    from_address   text NOT NULL DEFAULT '',
    to_address     text NOT NULL,
    token_contract text NOT NULL DEFAULT '',
    asset_id       text REFERENCES assets (id),
    amount         numeric(78, 0) NOT NULL,
    quote_id       uuid REFERENCES quotes (id) ON DELETE SET NULL,
    invoice_id     uuid REFERENCES invoices (id) ON DELETE SET NULL,
    status         text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'confirmed', 'orphaned')),
    confirmations  integer NOT NULL DEFAULT 0,
    detected_at    timestamptz NOT NULL DEFAULT now(),
    confirmed_at   timestamptz,
    UNIQUE (chain_id, tx_hash, idx)
);
CREATE INDEX transfers_invoice_idx ON transfers (invoice_id);
CREATE INDEX transfers_quote_idx ON transfers (quote_id);
CREATE INDEX transfers_pending_idx ON transfers (chain_id) WHERE status = 'pending';
CREATE INDEX transfers_detected_idx ON transfers (detected_at DESC);

CREATE TABLE webhook_events (
    id               uuid PRIMARY KEY,
    merchant_id      uuid NOT NULL REFERENCES merchants (id) ON DELETE CASCADE,
    invoice_id       uuid NOT NULL REFERENCES invoices (id) ON DELETE CASCADE,
    event_type       text NOT NULL,
    payload          jsonb NOT NULL,
    url              text NOT NULL,
    status           text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'delivered', 'failed')),
    attempts         integer NOT NULL DEFAULT 0,
    next_attempt_at  timestamptz NOT NULL DEFAULT now(),
    last_status_code integer NOT NULL DEFAULT 0,
    last_error       text NOT NULL DEFAULT '',
    last_response    text NOT NULL DEFAULT '',
    delivered_at     timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX webhook_events_due_idx ON webhook_events (next_attempt_at) WHERE status = 'pending';
CREATE INDEX webhook_events_invoice_idx ON webhook_events (invoice_id);
CREATE INDEX webhook_events_created_idx ON webhook_events (created_at DESC);

CREATE TABLE settings (
    key        text PRIMARY KEY,
    value      jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE audit_logs (
    id          bigserial PRIMARY KEY,
    actor_id    uuid,
    actor_email text NOT NULL DEFAULT '',
    action      text NOT NULL,
    target_type text NOT NULL DEFAULT '',
    target_id   text NOT NULL DEFAULT '',
    detail      jsonb NOT NULL DEFAULT '{}',
    ip          text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_logs_created_idx ON audit_logs (created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS audit_logs, settings, webhook_events, transfers, quotes, invoices, assets, chains, wallets, api_keys, merchants, admin_sessions, admin_users;
