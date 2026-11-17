-- Admin security (docs/architecture.md, "Admin security"): the e-mail
-- outbox used for security notifications and operational alerts.

-- +goose Up

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
