# Architecture

CryptoPay is a self-hosted, **non-custodial** crypto payment gateway. The server
never holds private keys: it derives receiving addresses from extended public
keys (or uses a fixed address) and only *reads* chains to detect payments.

```
merchant backend ──(REST, API key)──▶ ┌──────────────┐ ◀──(cookie session)── admin console (Next.js /admin)
                                      │  Go gateway  │
customer browser ──(public REST)────▶ │  (backend/)  │ ──(signed webhooks)──▶ merchant backend
  checkout (Next.js /pay/{id})        └──────┬───────┘
                                             │ scanners (read-only RPC)
             EVM · Tron · Bitcoin/Litecoin · Solana        PostgreSQL
```

## Supported assets (see `backend/internal/catalog`)

Chosen from payment-processor data (CoinGate / CoinsBee / NOWPayments 2025–2026):
stablecoins are ~60 % of crypto payment activity, led by USDT (Tron first) and
USDC; BTC, LTC, ETH and TRX are the most used native coins.

| Family | Chains | Assets |
|---|---|---|
| evm | Ethereum, BSC, Polygon, Arbitrum, Base (+ Sepolia) | ETH, BNB, USDT, USDC, PYUSD (Ethereum, Arbitrum) |
| tron | Tron (+ Nile) | TRX, USDT |
| utxo | Bitcoin, Litecoin (+ Bitcoin testnet) | BTC, LTC |
| solana | Solana (+ Devnet) | SOL, USDT, USDC, PYUSD (Token-2022) |

## Repository layout

```
backend/                     Go module "cryptopay"
  cmd/gateway/               main: serve | migrate | create-admin | reset-2fa | seed-dev | healthcheck
  internal/domain/           shared business types (no deps)            [done]
  internal/chain/            chain.Client interface                      [done]
  internal/address/          address validation + canonical forms        [done]
  internal/catalog/          built-in chains & assets                    [done]
  internal/db/migrations/    goose SQL migrations (embedded)             [done]
  internal/chain/evm|tron|utxo|solana/   chain adapters
  internal/hdwallet/         xpub → address derivation
  internal/paymenturi/       EIP-681 / BIP-21 / Solana Pay URIs
  internal/secret/           AES-GCM encryption of secrets at rest
  internal/config/           env configuration
  internal/store/            PostgreSQL repositories (pgx)
  internal/auth/             password hashing, session + API key tokens
  internal/rates/            price provider (CoinGecko) + cache
  internal/invoice/          invoice + quote service, state machine
  internal/scanner/          per-chain scan loop, confirmations, reorgs
  internal/webhook/          outbox dispatcher (HMAC signed, retries)
  internal/alert/            Notifier seam between the money path and alerts (+ async Queue)
  internal/notify/           which e-mail to send when, to whom; bilingual templates
  internal/mailer/           SMTP sender, MIME, e-mail outbox dispatcher
  internal/api/              HTTP: merchant, checkout, admin
web/                         Next.js (App Router) + shadcn/ui
docs/                        this file, api.md
docker-compose.yml           postgres + backend + web + mailpit (local mail catcher)
docker-compose.test.yml      runs every test suite in containers
```

## Core rules (apply to every package)

* **Money is never a float.** On-chain amounts are `*big.Int` base units
  (`numeric(78,0)` in SQL); prices/rates are `decimal.Decimal`. JSON carries
  amounts as decimal **strings**.
* **Addresses are stored and compared in canonical form** (`internal/address`).
* **Idempotency everywhere.** A transfer is unique by `(chain_id, tx_hash, idx)`;
  re-scanning a block range must be a no-op. Invoice creation is idempotent on
  `(merchant_id, merchant_order_id)`.
* **State changes are transactional**: crediting a transfer, moving the invoice
  status and enqueueing the webhook happen in one DB transaction.
* **Secrets at rest are encrypted** (RPC URLs with API keys, webhook secrets,
  admin TOTP secrets) with AES-256-GCM keyed from `APP_SECRET`. API keys,
  session tokens, sign-in challenge tokens and recovery codes are stored only
  as SHA-256 hashes.
* Logging uses `log/slog`. No global state; dependencies are passed in.
  Time is injected (`func() time.Time`) where logic depends on it.
* Tests: unit tests use `httptest` fakes for RPC/HTTP. Tests that need
  PostgreSQL read `TEST_DATABASE_URL` and `t.Skip` when it is unset. Tests that
  hit real public RPC endpoints carry the build tag `live` (`go test -tags live`).

## RPC endpoints

Per chain, the effective endpoint list is the first non-empty of:

1. `chains.rpc_urls_enc` – set in the admin console (commercial RPC for production),
2. env `RPC_<CHAIN_ID>` (upper-case, `-` → `_`, comma-separated URLs),
3. the free public default from `internal/catalog` (tests / local dev only).

Adapters fail over to the next URL on transport errors / 5xx / 429.

## Receiving wallets

A wallet belongs to one family. It is used in two ways:

* as a chain's **default wallet** (`chains.wallet_id`): every merchant without
  a wallet of its own on that chain is paid to it;
* as a **merchant wallet** (`merchant_wallets`, primary key
  `(merchant_id, chain_id)`): that merchant's invoices on that chain are paid
  to it instead — use this when merchants are separate businesses whose funds
  must not land in one wallet.

**Resolution rule.** The wallet for (merchant, chain) is the merchant's
binding if present, otherwise the chain's default wallet, otherwise none.
`store.ResolveWallet` implements it, and the payable-asset queries share its
SQL, so the rule exists once. An asset is *payable for a merchant* iff the
asset is enabled ∧ its chain is enabled ∧ a wallet resolves for (merchant,
chain); this drives `GET /api/v1/assets`, the checkout asset list, estimates
and quote creation. A binding must use a wallet of the chain's family, and a
static wallet's address must be valid on that chain (the same checks as for
the default wallet). Several merchants may share one wallet. A wallet used by
any binding cannot be deleted; deleting a merchant deletes its bindings.

**Existing quotes keep their wallet.** A quote stores the wallet id and the
address it was created with. Changing or removing a binding (or the default
wallet) only affects quotes created afterwards: an unpaid invoice whose
customer already sees an address can still be paid to it (as long as the
chain is still scanned, see below), and switching back to that asset
re-activates the quote with its original address.

**Key material is unique.** Two wallet rows with the same xpub would each
allocate indices from 0 and so derive *identical* addresses for invoices of
different merchants — the scanner could not tell their payments apart and
funds of one merchant would land in another merchant's wallet configuration.
Hence `UNIQUE (xpub) WHERE kind = 'xpub'` (across families: an EVM and a Tron
wallet with one xpub share the same keys) and `UNIQUE (family, address) WHERE
kind = 'static'`; `POST /wallets` answers 409 on the offending field. To give
two merchants independent xpub wallets, use different accounts
(`m/44'/60'/0'`, `m/44'/60'/1'`, … or different seeds). Migration 00003
merges pre-existing same-family duplicates into the oldest row (references
re-pointed, highest `next_index` kept, logged as `wallet.merge`) and refuses
to run when one xpub is registered for two families.

Each wallet is of one of two kinds:

* **xpub** (EVM, Tron, UTXO): account-level extended public key. Each invoice
  gets a fresh address `xpub/0/index` (`wallets.next_index` is allocated
  atomically). Quotes of one invoice on the same wallet reuse the same index,
  so one invoice has one address across all EVM chains using that wallet.
  * EVM: BIP-44 `m/44'/60'/0'` xpub → keccak address.
  * Tron: BIP-44 `m/44'/195'/0'` xpub → base58 address.
  * UTXO: BIP-84 `m/84'/coin'/0'` `xpub`/`zpub` (`tpub`/`vpub` testnet) → P2WPKH bech32.
* **static** (all families; the only option for Solana): one fixed address.
  Invoices are told apart by a **unique amount**: the quote amount is bumped
  in its last digits until `(chain, asset, address, amount_due)` is unique
  among live quotes (per address: merchants with different static addresses
  each get the plain amount). A transfer matches a quote only on the exact amount;
  anything else is stored as *unmatched* for manual assignment in the admin.

Sweeping funds from derived addresses is done by the merchant's own wallet
software holding the seed; it is deliberately out of scope for the server.

## Invoice lifecycle

```
            quote selected, funds seen < due            full amount seen          confirmations reached
 pending ───────────────────────────────▶ partial ───────────────────▶ confirming ───────────────────▶ paid
    │                                        │                              │
    │ expires_at                             │ expires_at                   │ reorg drops funds → back to partial/pending
    ▼                                        ▼
 expired ◀── (no confirmed funds)        underpaid (confirmed funds < due)
    └── funds arriving within the grace period still move the invoice to paid (paid_late = true)
 cancelled: merchant/admin action while pending
```

* Amount due per quote: `ceil(price / rate × (1 + markup))` rounded **up** to
  the asset's `quote_precision`. Stablecoins are pegged 1:1 to USD when
  `stablecoin_peg` is on. If `price_currency` equals the asset symbol the rate is 1.
* *Paid* means `amount_confirmed ≥ amount_due × (1 − underpay_tolerance)`.
  More than due sets `overpaid`.
* Every status change enqueues a webhook event in the same transaction.

## Scanner

One loop per enabled chain with a default wallet **or at least one merchant
wallet** (`chains.wallet_id IS NOT NULL OR` a `merchant_wallets` row). A
chain left without either keeps being scanned for as long as quotes created
earlier are still watched on it, so unassigning a wallet never strands an
in-flight invoice; it stops once those quotes are paid or closed. Disabling
a chain stops scanning immediately.
Matching does not care whose wallet an address belongs to: it is by
(chain, address, asset[, exact amount]) of the live quotes, so merchants with
different wallets on one chain are credited independently, and two merchants
sharing one static address are still told apart by unique amounts.

1. `head ← client.Head()`; scan `[cursor+1, min(cursor+batch, head)]` for the
   addresses of live quotes on that chain (skip the RPC call and just advance
   the cursor when nobody is watching; a never-scanned chain starts at head).
2. Insert new transfers (idempotent), match to quotes (xpub: by address + asset;
   static: by address + asset + exact amount), update quote/invoice in one tx.
3. For pending transfers: `confirmations = head − block + 1`; once
   `≥ chain.confirmations` call `VerifyTransfer`; confirmed → credit, not
   included → mark `orphaned` and recompute the quote.
4. Persist cursor/head/last_error. Errors back off exponentially; the range is
   retried, never skipped.

A separate ticker expires invoices and closes quotes past `watch_until`.

## Webhooks

`POST merchant.webhook_url` with JSON body `{id, type, created_at, data: Invoice}` and headers

```
X-CryptoPay-Event: invoice.paid
X-CryptoPay-Delivery: <event id>
X-CryptoPay-Timestamp: <unix seconds>
X-CryptoPay-Signature: hex(HMAC-SHA256(secret, timestamp + "." + body))
```

2xx = delivered. Otherwise retried with exponential backoff
(`min(2^attempt × 15s, 6h)`) up to `webhook_max_attempts`.

## Admin security

**Two-factor authentication.** TOTP per RFC 6238 (HMAC-SHA1, 6 digits,
30 s steps, the previous/current/next step accepted), implemented in
`internal/auth` with the standard library. The 160-bit base32 secret is
encrypted in `admin_users.totp_secret_enc`; a secret without
`totp_enabled_at` is *pending* (set up, not yet confirmed with a code).
`totp_last_step` makes every code single-use: a code is accepted only for a
time step newer than the last accepted one, and the update is a conditional
`UPDATE … WHERE totp_last_step < $step` under the user row lock, so two
concurrent requests cannot both use one code. Ten recovery codes
(`xxxxx-xxxxx`, Crockford base32, 50 bits each) are stored as SHA-256
hashes bound to the user id and are consumed on use.

**Sign-in.** `POST /auth/login` verifies the password exactly as before
(same timing and per-IP+e-mail throttle). For an account with two-factor
authentication it creates an `admin_login_challenges` row (token stored as a
hash, 5 minutes, at most 5 wrong codes) and answers `202` without a cookie;
`POST /auth/login/2fa` checks the code (or consumes a recovery code) under
the challenge and user row locks, deletes the challenge and only then
creates the session. All failures look the same (`401`); wrong codes are
also counted per account across IPs and challenges (`429` after 10 within
15 minutes). Deactivating a user, a password change or a reset deletes the
user's pending challenges.

**Enforcement.** With the setting `require_2fa` on, a user without two-factor
authentication is limited to `/auth/me`, `/auth/logout`, `/auth/password`,
`/auth/2fa/setup` and `/auth/2fa/enable` (`403 two_factor_setup_required`
elsewhere, a middleware after the session check); the console then shows
only the setup flow. Only an admin who has two-factor authentication can
turn the setting on, and nobody can turn their own off while it is on. An
owner can reset another user's two-factor authentication; when no owner
can sign in, `gateway reset-2fa --email` does the same from the server.

**E-mail.** `internal/notify` decides what is mailed; messages go into the
`email_outbox` table and `internal/mailer.Dispatcher` delivers them like the
webhook dispatcher (claim with `FOR UPDATE SKIP LOCKED` and a lease, so
several instances never send a message twice; retries
`min(2^attempt × 30 s, 2 h)`, 8 attempts). Nothing is queued unless
`SMTP_HOST` and `SMTP_FROM` are set. The SMTP sender requires STARTTLS in
mode `starttls` (it refuses to fall back to clear text), sends credentials
only over TLS and never logs the password.

* *Security notifications* (`security.*`, to the affected admin, setting
  `security_emails`) are enqueued on the transaction of the change they
  report — a rolled back change mails nothing: sign-in from an IP the
  account never used before (judged from earlier `auth.login` audit entries
  and sessions; never on the account's first sign-in), sign-in blocked by
  the failure throttle (one per account per hour), password changed, two-
  factor authentication enabled / disabled / reset, recovery codes
  regenerated or used (with the number left), account created, role
  changed, deactivated, reactivated.
* *Operational alerts* (`alert.*`, setting `alert_emails`, recipients
  `alert_recipients` or every active owner): a webhook event that reached
  `failed`, an unmatched transfer, an invoice that became `underpaid`, a
  chain whose scanner has failed for 10 minutes. The invoice service,
  scanner and webhook dispatcher only see the small `alert.Notifier`
  interface; they call it **after** their transaction committed
  (`invoice.Service.Committed`, the scanner after recording a transfer, the
  dispatcher after recording the final failure), through `alert.Notify`
  (nil-safe, recovers panics). The production notifier is an `alert.Queue`:
  a bounded buffer drained by one goroutine, so a slow or failing mail path
  can neither roll back, fail nor delay payment processing — an alert is
  dropped (and logged) rather than waited for. Each kind is throttled with
  the outbox's `(kind, dedupe_key, created_at)` index under an advisory lock
  (per merchant per hour, per chain per hour, per invoice once, per chain
  per 6 hours), so a storm cannot flood a mailbox, also with several
  gateway instances.
