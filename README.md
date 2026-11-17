# CryptoPay

CryptoPay is a **self-hosted, non-custodial** cryptocurrency payment gateway. A merchant creates an invoice through the REST API and redirects the customer to the checkout page. The customer pays from a wallet. The gateway only **reads** the blockchain to detect the payment, and once it is confirmed it notifies the merchant with a signed webhook. **No private key is ever stored on the server**: receiving addresses are derived from an extended public key (xpub), or a single static address is used.

- Backend: Go (`backend/`, module `cryptopay`) — HTTP API, chain scanner, webhook delivery
- Frontend: Next.js (`web/`) — the checkout at `/pay/{id}` and the admin console at `/admin`
- Database: PostgreSQL

## Supported coins and networks

Stablecoins account for roughly **60%** of crypto payment volume (led by USDT, most of it on Tron, followed by USDC). Among native coins, BTC, LTC, ETH and TRX are used most across payment processors (CoinGate / CoinsBee / NOWPayments statistics for 2025–2026). The built-in catalog follows that:

| Family | Network | Assets | Default confirmations |
|---|---|---|---|
| EVM | Ethereum | ETH, USDT, USDC, PYUSD | 12 |
| EVM | BNB Smart Chain | BNB, USDT, USDC | 15 |
| EVM | Polygon | USDT, USDC | 64 |
| EVM | Arbitrum One | ETH, USDT, USDC, PYUSD | 40 |
| EVM | Base | ETH, USDC | 20 |
| Tron | Tron | TRX, USDT | 19 |
| UTXO | Bitcoin | BTC | 2 |
| UTXO | Litecoin | LTC | 6 |
| Solana | Solana | SOL, USDT, USDC, PYUSD | finalized |
| Testnets | Sepolia · Tron Nile · Bitcoin Testnet · Solana Devnet | ETH/USDC/PYUSD · TRX/USDT · BTC · SOL/USDC/PYUSD | — |

Mainnets are enabled by default and testnets are disabled. A chain can take payments only once it has a **receiving wallet** (the chain's default wallet, or a merchant's own wallet). Custom tokens (ERC-20 / TRC-20 / SPL) can be added in the admin console.

## Architecture

```
Merchant backend ──(REST, API key)──▶ ┌────────────┐ ◀──(session cookie)── Admin console (Next.js /admin)
                                      │ Go gateway │
Customer browser ──(public REST)────▶ │ (backend/) │ ──(HMAC-signed webhook)──▶ Merchant backend
  Checkout (Next.js /pay/{id})        └─────┬──────┘
                                            │ read-only RPC scanning
              EVM · Tron · Bitcoin/Litecoin · Solana              PostgreSQL
```

- [`docs/api.md`](docs/api.md): the HTTP contract (the single source of truth shared by frontend and backend)
- [`docs/architecture.md`](docs/architecture.md): architecture, invoice lifecycle, scanner, webhooks
- [`web/README.md`](web/README.md): frontend notes

The browser only talks to Next.js (same origin), which proxies `/api/*` to the Go backend.

## Quick start (Docker)

Requires Docker with Compose v2.

```bash
cp .env.example .env
# Edit .env: at the very least change APP_SECRET (openssl rand -base64 48) and ADMIN_PASSWORD
docker compose up --build -d        # or: make up
docker compose ps                   # wait until backend shows healthy
```

- Admin console: http://localhost:3000/admin (sign in with `ADMIN_EMAIL` / `ADMIN_PASSWORD` from `.env`; on first start, when the database has no admin yet, an owner is created automatically)
- Backend health check: http://127.0.0.1:8080/healthz (the backend port is bound to 127.0.0.1 only)
- If a port is already taken, change `WEB_PORT` / `BACKEND_PORT` in `.env`
- Development demo data: `make seed-dev` creates a demo merchant and an API key (shown only once), assigns **test-only** xpub wallets derived from the public Hardhat test mnemonic as the default wallets of Sepolia / Tron Nile / Bitcoin Testnet, and enables those testnets (it refuses to run when `APP_ENV=production`; **never send real assets to these addresses**)
- To walk through an order, payment, confirmation and webhook on a testnet by hand, see [docs/testnet-walkthrough.md](docs/testnet-walkthrough.md) (local webhook receiver: `scripts/webhook-receiver.py`)
- Local email: compose ships with [Mailpit](https://mailpit.axllent.org/), which catches every email the gateway sends (security notices, alerts, test emails). Read them at http://localhost:8025 (change the port with `MAILPIT_PORT`). Mailpit only receives mail locally; it does **not** deliver anything to the outside world
- Stop: `make down`; delete the data volume: `make clean`

Without Docker: `cd backend && go run ./cmd/gateway` (needs `DATABASE_URL` and `APP_SECRET`); for the frontend see `web/README.md`.

## First run: set up the admin console

1. **Create a receiving wallet** (Receiving wallets → New)
   - EVM / Tron / Bitcoin / Litecoin: paste the **account-level extended public key** (EVM `m/44'/60'/0'`, Tron `m/44'/195'/0'`, BTC `m/84'/0'/0'` as `xpub`/`zpub`, `tpub`/`vpub` on testnets). Before saving you can preview the first 3 derived addresses; compare them with your own wallet software. Every invoice gets a fresh address `xpub/0/index`.
   - Solana (or whenever you do not want to use an xpub): use a **static address**. Invoices on the same address are told apart by a **unique amount** (the last digits of the amount are nudged), so the customer must pay exactly the amount shown.
   - Pasting a private key (`xprv`…) is explicitly rejected.
   - The same xpub (or the same static address within one chain family) can be registered only once. Two records sharing one key would derive identical addresses for different merchants, so the console returns 409. To give several merchants independent xpub wallets, use different accounts (for example EVM `m/44'/60'/1'`, `m/44'/60'/2'`) or different mnemonics.
2. **Decide which wallet receives the money.** The two options can be mixed:
   - **The chain's default wallet** (Chains & RPC → pick a chain → Default receiving wallet): every merchant without a wallet of their own is paid here. If there is only one merchant, or all merchants are the same company and the funds go to the same wallet, setting only the default wallet is enough. Make sure the chain is enabled; its "Payable" column then shows yes.
   - **A merchant's own wallet** (Merchants → pick a merchant → Receiving wallets → choose a wallet for a chain): that merchant's invoices on that chain are paid to their own wallet, overriding the default. Use it when the merchants on your platform are **separate legal entities whose funds must be kept apart**. A chain without a default wallet is still scanned as soon as one merchant has a wallet of their own on it, but it is payable only for those merchants.
   - The rule: a merchant's receiving wallet on a chain is their own wallet if they have one, otherwise the chain's default wallet, otherwise the chain is not payable for that merchant (it appears neither in `GET /api/v1/assets` nor on the checkout). Changing or removing a wallet only affects quotes created afterwards: unpaid invoices already shown to a customer keep using the original address.
3. **Configure a commercial RPC** (Chains & RPC → RPC). The built-in defaults are free public nodes, good only for development and testing. In production, enter an Alchemy / Infura / QuickNode (EVM), TronGrid (`?apikey=`), Helius (Solana), GetBlock or self-hosted node (BTC/LTC) URL for every chain, and use "Test" to check it. RPC URLs are stored encrypted in the database and the UI only shows `scheme://host/…`.
4. **Create a merchant and an API key** (Merchants → New). The webhook signing secret and the API key are shown in clear text **only once**, so save them to the merchant's backend right away. If the merchant needs separate funds, choose that merchant's own wallet for each chain under "Receiving wallets" on the merchant page (see step 2).
5. Adjust **Settings** as needed: invoice lifetime, grace period, underpayment tolerance, rate markup, `checkout_base_url` (the public address of the checkout, for example `https://pay.example.com`), the WalletConnect project id, and so on.
6. **Protect the admin accounts**: turn on two-step verification under "Account security" and keep the recovery codes; once SMTP is configured, send a test email from "Settings → Email notifications" and turn on operational alerts if you want them (see "Admin account security" below).

## Admin account security

### Two-step verification (TOTP)

- Every admin can enable two-step verification under **Account security** (the user menu in the top right, or the sidebar): enter the current password → scan the QR code with an authenticator app (Google Authenticator, 1Password, Microsoft Authenticator, Authy, …) or type the grouped secret by hand → enter the 6-digit code to confirm. The algorithm is RFC 6238 (HMAC-SHA1, 6 digits, 30 seconds); one time step either side is accepted, and a code can be used only once.
- Once enabled, signing in takes two steps: email and password first, then the 6-digit code from the authenticator (within 5 minutes, at most 5 wrong attempts, after which the admin has to sign in again; repeated failures on one account are blocked for a while). Enabling two-step verification signs the account out of its sessions on other devices.
- **Recovery codes**: enabling shows 10 one-time recovery codes (shaped like `abcde-fghjk`) **only once**. Copy them or download the `.txt` and keep them offline. If the phone is lost, use one of them on the second sign-in step instead; each code works once, and an email tells the admin how many remain after use. An admin can generate a new set under "Account security" with the password and a current code (all the old ones stop working).
- Authenticator secrets are stored encrypted with `APP_SECRET`; recovery codes and sign-in challenge tokens are stored only as SHA-256 hashes. None of these ever appears in the audit log, the application log or any email.
- **Require it for every admin**: turn on "Require two-step verification for every admin" under "Settings → Security" (only an admin who already has two-step verification can turn it on). While it is on, admins who have not set it up can only see the setup flow after signing in, and every other endpoint returns `403 two_factor_setup_required` until they finish; nobody can turn off their own two-step verification meanwhile.
- **Reset**: an owner can reset another user's two-step verification on the Admins page (it deletes the secret and recovery codes, signs the user out of every session and emails them). Owners cannot reset their own; turn it off under "Account security" instead.
- **Break glass**: if the only owner has lost the authenticator and has no recovery code, run this on the server:

  ```bash
  docker compose exec backend /usr/local/bin/gateway reset-2fa --email owner@example.com
  ```

  The command removes that user's two-step verification, signs them out of every session, writes an audit log entry (actor `cli:reset-2fa`) and, if email is configured, notifies the user. They can then sign in with the password alone; if "Require two-step verification" is on, they have to set it up again after signing in.

### Session management

The "Account security" page lists all of your active sessions (IP and browser at sign-in, with the current device marked). You can sign out a single session or every other session at once. Changing the password or enabling two-step verification also signs out the other sessions.

### Email notifications

Once SMTP is configured (see "Configuration reference" below and `.env.example`), the gateway sends email through an outbox (failures are retried after `min(2^n × 30s, 2h)`, at most 8 times):

- **Security notices** (sent to the admin concerned; controlled by "Settings → Email notifications → Security emails", on by default): sign-in from a never-seen IP (not sent for an account's first sign-in), repeated failed sign-ins that temporarily blocked the account (at most one email per account per hour), password changed (by the admin or an owner), two-step verification enabled / disabled / reset by an owner, recovery codes regenerated, sign-in with a recovery code (including how many remain), account created, role changed, deactivated / reactivated.
- **Operational alerts** (off by default; recipients are the "Alert recipients", or every active owner when the list is empty): a webhook event finally failed to deliver (at most one email per merchant per hour), a transfer that cannot be matched arrived (at most one per chain per hour), an invoice became underpaid (one per invoice), a chain's scanner has been failing for more than 10 minutes (at most one per chain every 6 hours). Alerts are raised asynchronously after the database transaction commits, so a failing or slow email never affects payment processing.
- The email language follows the "Email language" setting (Chinese / English); all times are shown in UTC. Emails never contain passwords, secrets, verification codes or tokens.
- The "Email log" page shows the outbox, filters it by status / type, shows message bodies and retries failed emails; "Settings → Email notifications" can send a test email and shows the error SMTP returned.

## Merchant integration

### 1. Create an invoice

```bash
curl -X POST https://pay.example.com/api/v1/invoices \
  -H "Authorization: Bearer sk_live_xxx" \
  -H "Content-Type: application/json" \
  -d '{
        "amount": "25.00",
        "currency": "USD",
        "order_id": "ORDER-1001",
        "title": "Pro plan",
        "success_url": "https://shop.example/thanks",
        "cancel_url": "https://shop.example/cart",
        "metadata": {"customer_id": "42"}
      }'
```

- Returns `201` and an `Invoice`. Submitting the same `order_id` again with the same amount and currency returns `200` and the original invoice (idempotent); a different amount or currency returns `409`.
- `currency` can be a fiat code (USD, EUR, CNY…) or the symbol of an enabled asset (such as `USDT`).
- The optional `asset_id` (for example `usdt-tron`) pins the payment asset; `GET /api/v1/assets` lists the assets that can currently be paid.
- Amounts are always **strings** (`"25.00"`). Never use floating-point numbers.

### 2. Redirect the customer to `checkout_url`

`checkout_url` looks like `https://pay.example.com/pay/{invoice_id}`. The checkout lets the customer choose a coin and a network, shows the address, the amount, a QR code and wallet buttons, and follows the confirmation progress live. When the payment completes it redirects to `success_url`.

### 3. Receive and verify the webhook

The body is `{ "id", "type", "created_at", "data": Invoice }`. Event types: `invoice.payment_detected`, `invoice.confirming`, `invoice.paid`, `invoice.underpaid`, `invoice.expired`, `invoice.cancelled`. The signature is `hex(HMAC-SHA256(secret, timestamp + "." + raw request body))`:

```go
func cryptopayWebhook(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	ts, _ := strconv.ParseInt(r.Header.Get("X-CryptoPay-Timestamp"), 10, 64)

	mac := hmac.New(sha256.New, []byte(webhookSecret)) // whsec_...
	fmt.Fprintf(mac, "%d.", ts)
	mac.Write(body) // the raw bytes; do not re-serialize
	want := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(want), []byte(r.Header.Get("X-CryptoPay-Signature"))) ||
		time.Since(time.Unix(ts, 0)).Abs() > 5*time.Minute {
		http.Error(w, "bad signature", http.StatusUnauthorized)
		return
	}
	var ev struct {
		ID   string          `json:"id"`
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	_ = json.Unmarshal(body, &ev)
	// Deduplicate on ev.ID (= X-CryptoPay-Delivery) before processing, then return any 2xx.
	w.WriteHeader(http.StatusNoContent)
}
```

- Delivery is **at-least-once**: a non-2xx response or a timeout is retried with the backoff `min(2^n × 15s, 6h)` up to the configured maximum number of attempts. The same event can arrive more than once, so you **must deduplicate on the event `id`**.
- Several events can be delivered in parallel and **their arrival order is not guaranteed**: trust `data.status` (and `data.updated_at`), and only ship the goods on `invoice.paid`.
- The admin console shows the status code and response of every delivery and lets you retry one by hand.

### 4. Other endpoints

`GET /api/v1/invoices/{id}`, `GET /api/v1/invoices?status=&order_id=&page=&page_size=`, `POST /api/v1/invoices/{id}/cancel` (only `pending` invoices can be cancelled). The full contract is in [`docs/api.md`](docs/api.md).

## Configuration reference (environment variables)

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

Runtime parameters (lifetime, grace period, tolerance, markup, webhook attempts and timeout, `checkout_base_url`, whether two-step verification is required, the email notification switches, alert recipients, email language) are changed under "Settings" in the admin console. Each variable is documented in [`.env.example`](.env.example).

`gateway` commands: `serve` (the default; migrates on start, `--no-migrate` turns that off), `migrate`, `create-admin --email --password --role`, `reset-2fa --email` (emergency reset of an admin's two-step verification), `seed-dev`, `healthcheck`, `version`.

## Running the tests in Docker

Every test runs in a container, so neither Go nor Node is needed on the host:

```bash
make test           # backend + frontend
make test-backend   # go vet + go test -race ./... (uses a throwaway PostgreSQL container)
make test-web       # pnpm lint && pnpm typecheck && pnpm test
make test-live      # optional: exercise the chain adapters against free public RPCs (needs the network, subject to rate limits, may fail now and then)
make lint           # gofmt / go vet / eslint / tsc
```

The equivalent compose command is `docker compose -f docker-compose.test.yml run --rm --build backend-test` (its exit code is the test result), followed by `docker compose -f docker-compose.test.yml down -v`.

To run the backend tests directly on the host: `export TEST_DATABASE_URL='postgres://postgres:postgres@localhost:54329/cryptopay_test?sslmode=disable' && cd backend && go test -race ./...` (the database tests are skipped when it is not set).

## Security notes

- **Non-custodial and read-only**: the gateway stores only extended **public** keys or static addresses, calls read-only RPC methods only, and never signs or broadcasts a transaction. Sweeping funds is done by the merchant in wallet software that holds the mnemonic, and is outside the scope of this system.
- **Never paste a private key or mnemonic.** Private keys such as `xprv` are rejected, and any instruction that asks you to type a mnemonic into the console is wrong.
- `APP_SECRET` encrypts the RPC URLs (which often contain API keys) and the webhook signing secrets in the database; API keys and session tokens are stored only as SHA-256 hashes. If `APP_SECRET` is lost, that ciphertext can no longer be decrypted; if it leaks, every RPC key and webhook secret must be rotated.
- Admin console: HttpOnly + SameSite=Lax session cookie (Secure in production), `X-Requested-With: cryptopay` required on every write (CSRF protection), sign-in rate limiting per IP + email, optional (or enforced) TOTP two-step verification with one-time recovery codes, session management, security-event emails, three roles (owner / admin / viewer), and an audit log of every change (without secrets, passwords, verification codes or full RPC URLs).
- The invoice UUID is the access credential of the checkout: do not leak a `checkout_url` in public.

### Production checklist

- [ ] `APP_ENV=production`, HTTPS everywhere (TLS terminated at a reverse proxy), and a correct `checkout_base_url`
- [ ] A strong random `APP_SECRET`, backed up offline; a strong `ADMIN_PASSWORD`, changed after the first sign-in; `POSTGRES_PASSWORD` changed
- [ ] Every admin has two-step verification enabled and their recovery codes stored offline, then "Require two-step verification" is turned on under "Settings → Security"; keep at least two owners and note the `gateway reset-2fa` break-glass procedure
- [ ] A real SMTP service is configured (`SMTP_TLS=starttls` or `tls`; never `none` over the public internet; remove Mailpit or do not expose it in production), and a test email arrives; SPF / DKIM are set up on the sending domain so notifications do not land in spam
- [ ] Operational alerts are on and alert recipients are set (or someone actually reads every owner's mailbox)
- [ ] Every enabled chain has a commercial RPC (do not rely on the free public nodes), checked with "Test"
- [ ] The wallet preview addresses were compared with your own wallet software; the test-only Hardhat wallets are **not** assigned to any mainnet (neither as a default wallet nor as a merchant's own wallet)
- [ ] The reverse proxy overwrites/appends `X-Forwarded-For`, and the proxy's address is set in `TRUSTED_PROXIES` (otherwise every customer shares one rate-limit bucket)
- [ ] The backend port is not open to the internet, only the web port is exposed; the database is backed up regularly
- [ ] Merchant webhook handlers verify the signature, check the timestamp and deduplicate on the event id
- [ ] With several instances, enable `SCANNER_ENABLED=true` on only one or a few of them (scanning is protected by a distributed lock, so several may run, but not all of them need to)

## Known limitations

- **Internal native-coin transfers on EVM / Tron are not detected.** Only top-level ETH/BNB/TRX transfers and ERC-20/TRC-20 `Transfer` events are recognized. Native coins that arrive through an internal contract call (some smart-contract wallets, exchange batch withdrawals) are not seen, and the invoice has to be marked as paid by hand in the console.
- **Litecoin MWEB** privacy transactions cannot be observed; pay to a regular address.
- **PYUSD on Solana is a Token-2022 token.** Its issuer, Paxos, can switch on a transfer fee (currently 0). Once it does, the amount that arrives is smaller than what the customer sent and may be judged a partial payment; the issuer also holds a permanent delegate that can move PYUSD out of any account. Detection uses the amount that actually arrived.
- There is no free Litecoin JSON-RPC that needs no key: the built-in default node allows only a few anonymous requests per minute, which is enough for development only. Production must configure its own RPC.
- **Sweeping funds is out of scope.** Funds on each invoice's derived address have to be collected with your own wallet software (the same wallet mnemonic).
- **Static-address wallets** tell invoices apart by a unique amount: a payment whose amount is not exact (an exchange deducting its fee, a customer editing the amount) ends up under "Unmatched transfers" and an admin has to assign it by hand. The same amount is reused once the invoice is closed (after the grace period).
- The scanner does not look back in history: the first time a chain is scanned it starts at the current block height.
- After the last wallet of a chain is removed, the chain keeps being scanned while any quote is still waiting for payment, until those quotes are paid or closed. **Disabling** a chain, however, stops scanning immediately, so make sure there are no unpaid orders before disabling it.
- Exchange rates come from CoinGecko (with a lower rate limit without a key). When no rate is available that asset cannot be quoted (stablecoins are quoted at 1 USD while pegging is on).
- Rate-limit counters live in process memory, so with several instances each instance counts separately.
- Webhooks are delivered at least once and in no guaranteed order (see above).
