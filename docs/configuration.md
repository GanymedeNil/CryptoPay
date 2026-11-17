# Configuration reference

Environment variables and `gateway` commands.

| Variable | Default | Description |
|---|---|---|
| `DATABASE_URL` | — (required) | PostgreSQL URL; parameters such as `pool_max_conns` override the pool size |
| `APP_SECRET` | — (required, ≥ 32 characters) | Master key for encryption at rest (RPC URLs, webhook secrets). **Keep it unchanged and back it up safely** |
| `APP_ENV` | `development` | `production` enables Secure cookies and HSTS, forbids webhooks to private networks and makes `seed-dev` refuse to run |
| `HTTP_ADDR` | `:8080` | Listen address |
| `LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` (JSON logs; request headers, bodies and query strings are never logged) |
| `ADMIN_EMAIL` / `ADMIN_PASSWORD` | — | Create the owner on first start when there is no admin yet |
| `SCANNER_ENABLED` | `true` | Whether this process runs the chain scanner and the invoice-expiry job |
| `WEBHOOK_ALLOW_PRIVATE` | `true` in development, `false` in production | Whether webhooks may target 127.0.0.1 / private-network addresses |
| `TRUST_PROXY_HEADERS` / `TRUSTED_PROXIES` | off / — | Honor `X-Forwarded-For` (rate limiting, audit IP) only when the direct peer is a trusted proxy |
| `CORS_ORIGINS` | — | Origins allowed to call `/api/v1` directly from a browser (usually leave empty) |
| `COINGECKO_API_KEY` / `COINGECKO_PRO` / `COINGECKO_BASE_URL` | — | CoinGecko, the exchange-rate source |
| `RPC_<CHAIN_ID>` | built-in public node | RPC per chain; separate several with commas for failover, for example `RPC_TRON_NILE`. A value set in the console takes precedence |
| `SMTP_HOST` / `SMTP_FROM` | `mailpit` / `CryptoPay <no-reply@cryptopay.local>` in compose | Mail server and sender. Email is enabled only when both are set (an empty `SMTP_HOST` turns email off; setting `SMTP_HOST` without `SMTP_FROM` makes startup fail) |
| `SMTP_PORT` | `587` | SMTP port (implicit TLS is usually 465, Mailpit uses 1025) |
| `SMTP_USERNAME` / `SMTP_PASSWORD` | — | SMTP authentication (AUTH PLAIN, sent only over a TLS connection); the password is never logged |
| `SMTP_TLS` | `starttls` | `starttls` (must upgrade to TLS; if the server cannot, sending is refused instead of falling back to plaintext), `tls` (implicit TLS), `none` (plaintext, only for a local or internal relay such as Mailpit) |
| `WEB_PORT` / `BACKEND_PORT` / `MAILPIT_PORT` / `POSTGRES_PASSWORD` | `3000` / `8080` / `8025` / a development password | Used by docker compose only |

Runtime parameters (lifetime, grace period, tolerance, markup, webhook attempts and timeout, `checkout_base_url`, whether two-step verification is required, the email notification switches, alert recipients, email language) are changed under "Settings" in the admin console. Each variable is documented in [`.env.example`](../.env.example).

`gateway` commands: `serve` (the default; migrates on start, `--no-migrate` turns that off), `migrate`, `create-admin --email --password --role`, `reset-2fa --email` (emergency reset of an admin's two-step verification), `seed-dev`, `healthcheck`, `version`.
