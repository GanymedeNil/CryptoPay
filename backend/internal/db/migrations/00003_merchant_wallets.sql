-- Merchant-level receiving wallets (docs/architecture.md, "Receiving wallets").
--
-- The wallet for (merchant, chain) is the merchant's binding in
-- merchant_wallets if present, otherwise the chain's default wallet
-- (chains.wallet_id), otherwise none.
--
-- Wallet rows must not share key material: two xpub wallets with the same
-- extended public key (or two static wallets of one family with the same
-- address) would hand out identical addresses to different merchants, and
-- the scanner could not tell their payments apart. Existing duplicates of
-- the same family are merged into the oldest row before the unique indexes
-- are created (references are re-pointed, the highest next_index is kept so
-- no derivation index is handed out twice, every merge is written to the
-- audit log as "wallet.merge"). The one case that cannot be merged — the
-- same xpub registered for two different families — stops the migration
-- with an explanation; delete one of the two wallets and start again.

-- +goose Up

CREATE TEMP TABLE wallet_merge ON COMMIT DROP AS
SELECT id AS dup_id,
       first_value(id) OVER (PARTITION BY family, kind, CASE WHEN kind = 'xpub' THEN xpub ELSE address END
                             ORDER BY created_at, id) AS keep_id
FROM wallets;
DELETE FROM wallet_merge WHERE dup_id = keep_id;

UPDATE wallets k SET next_index = m.next_index
FROM (SELECT wm.keep_id, max(w.next_index) AS next_index
      FROM wallet_merge wm JOIN wallets w ON w.id = wm.dup_id
      GROUP BY wm.keep_id) m
WHERE k.id = m.keep_id AND k.next_index < m.next_index;
UPDATE chains c SET wallet_id = m.keep_id FROM wallet_merge m WHERE c.wallet_id = m.dup_id;
UPDATE quotes q SET wallet_id = m.keep_id FROM wallet_merge m WHERE q.wallet_id = m.dup_id;
INSERT INTO audit_logs (actor_email, action, target_type, target_id, detail)
SELECT 'migration:00003_merchant_wallets', 'wallet.merge', 'wallet', m.keep_id::text,
       jsonb_build_object('merged_wallet_id', w.id, 'merged_wallet_name', w.name, 'reason', 'duplicate key material')
FROM wallet_merge m JOIN wallets w ON w.id = m.dup_id;
DELETE FROM wallets w USING wallet_merge m WHERE w.id = m.dup_id;

-- +goose StatementBegin
DO $$
DECLARE
    dups text;
BEGIN
    SELECT string_agg(ids, '; ') INTO dups FROM (
        SELECT string_agg(id::text || ' (' || family || ', "' || name || '")', ', ' ORDER BY created_at) AS ids
        FROM wallets WHERE kind = 'xpub' GROUP BY xpub HAVING count(*) > 1) d;
    IF dups IS NOT NULL THEN
        RAISE EXCEPTION 'migration 00003: these wallets share one extended public key across families and would derive the same keys: %. Delete all but one of them (admin console, Wallets) and restart.', dups;
    END IF;
END
$$;
-- +goose StatementEnd

CREATE UNIQUE INDEX wallets_xpub_key ON wallets (xpub) WHERE kind = 'xpub';
CREATE UNIQUE INDEX wallets_static_address_key ON wallets (family, address) WHERE kind = 'static';

CREATE TABLE merchant_wallets (
    merchant_id uuid NOT NULL REFERENCES merchants (id) ON DELETE CASCADE,
    chain_id    text NOT NULL REFERENCES chains (id) ON DELETE CASCADE,
    wallet_id   uuid NOT NULL REFERENCES wallets (id) ON DELETE RESTRICT,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (merchant_id, chain_id)
);
-- Wallet delete / in-use check and the wallet list's merchant bindings.
CREATE INDEX merchant_wallets_wallet_idx ON merchant_wallets (wallet_id);
-- Per-chain binding count (admin chain list, scanner: which chains to scan).
CREATE INDEX merchant_wallets_chain_idx ON merchant_wallets (chain_id);

-- +goose Down
DROP TABLE IF EXISTS merchant_wallets;
DROP INDEX IF EXISTS wallets_static_address_key;
DROP INDEX IF EXISTS wallets_xpub_key;
-- Wallets merged by the Up migration are not split again.
