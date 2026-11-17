-- Admin security (docs/architecture.md, "Admin security"): TOTP two-factor
-- authentication with recovery codes and sign-in challenges, and the e-mail
-- outbox used for security notifications and operational alerts.
--
-- Existing admin users keep signing in with their password only: the new
-- columns default to "no two-factor authentication".

-- +goose Up

-- totp_secret_enc: the base32 TOTP secret, AES-GCM encrypted like the other
-- *_enc columns ('' = none). A secret without totp_enabled_at is pending
-- (set up but not confirmed yet). totp_last_step is the last accepted
-- 30-second time step: a code of an equal or older step is a replay.
ALTER TABLE admin_users
    ADD COLUMN totp_secret_enc text NOT NULL DEFAULT '',
    ADD COLUMN totp_enabled_at timestamptz,
    ADD COLUMN totp_last_step  bigint NOT NULL DEFAULT 0;

-- Single-use recovery codes, stored only as SHA-256 hashes.
CREATE TABLE admin_recovery_codes (
    id         uuid PRIMARY KEY,
    user_id    uuid NOT NULL REFERENCES admin_users (id) ON DELETE CASCADE,
    code_hash  text NOT NULL UNIQUE,
    used_at    timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX admin_recovery_codes_user_idx ON admin_recovery_codes (user_id);

-- The pending second step of a sign-in (password accepted, code missing).
-- token_hash is the SHA-256 of the challenge token handed to the client.
CREATE TABLE admin_login_challenges (
    id         uuid PRIMARY KEY,
    user_id    uuid NOT NULL REFERENCES admin_users (id) ON DELETE CASCADE,
    token_hash text NOT NULL UNIQUE,
    attempts   integer NOT NULL DEFAULT 0,
    ip         text NOT NULL DEFAULT '',
    user_agent text NOT NULL DEFAULT '',
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX admin_login_challenges_user_idx ON admin_login_challenges (user_id);
CREATE INDEX admin_login_challenges_expires_idx ON admin_login_challenges (expires_at);

-- "Has this user signed in from this IP before?" (new sign-in notification).
CREATE INDEX audit_logs_login_target_idx ON audit_logs (target_id, ip) WHERE action = 'auth.login';

-- Outgoing e-mail, delivered by a leased worker like webhook_events.
-- dedupe_key groups messages for throttling ("one alert per chain per hour").
CREATE TABLE email_outbox (
    id              uuid PRIMARY KEY,
    kind            text NOT NULL,
    dedupe_key      text NOT NULL DEFAULT '',
    to_address      text NOT NULL,
    subject         text NOT NULL,
    body_text       text NOT NULL,
    body_html       text NOT NULL DEFAULT '',
    status          text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'sent', 'failed')),
    attempts        integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    last_error      text NOT NULL DEFAULT '',
    sent_at         timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX email_outbox_due_idx ON email_outbox (next_attempt_at) WHERE status = 'pending';
CREATE INDEX email_outbox_dedupe_idx ON email_outbox (kind, dedupe_key, created_at DESC);
CREATE INDEX email_outbox_created_idx ON email_outbox (created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS email_outbox;
DROP INDEX IF EXISTS audit_logs_login_target_idx;
DROP TABLE IF EXISTS admin_login_challenges;
DROP TABLE IF EXISTS admin_recovery_codes;
ALTER TABLE admin_users
    DROP COLUMN IF EXISTS totp_last_step,
    DROP COLUMN IF EXISTS totp_enabled_at,
    DROP COLUMN IF EXISTS totp_secret_enc;
