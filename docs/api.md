# HTTP API contract

This file is the single source of truth shared by the Go backend
(`backend/internal/api`) and the Next.js frontend (`web/`). Types are written
in TypeScript notation.

## Conventions

* Base path `/api`. JSON in, JSON out, `snake_case` keys, UTF-8.
* Timestamps: RFC 3339 UTC strings. IDs: UUID strings unless noted.
* Amounts: decimal **strings** in whole units (`"12.5"`); `*_base` fields are
  integer strings in base units. Never numbers.
* Errors: non-2xx with `{ "error": { "code": string, "message": string, "fields"?: Record<string,string> } }`.
  Codes: `invalid_request` 400, `unauthorized` 401, `forbidden` 403, `not_found` 404,
  `conflict` 409, `unprocessable` 422, `rate_limited` 429, `internal` 500, `unavailable` 503.
* Lists: query `page` (1-based, default 1), `page_size` (default 20, max 100) →
  `Page<T> = { items: T[]; total: number; page: number; page_size: number }`.
* `GET /healthz` → `{status:"ok"}`; `GET /readyz` checks the database.

## Shared types

```ts
type InvoiceStatus = "pending"|"partial"|"confirming"|"paid"|"underpaid"|"expired"|"cancelled";
type Family = "evm"|"tron"|"utxo"|"solana";
type WalletKind = "xpub"|"static";

interface Invoice {
  id: string;
  merchant_id: string;
  order_id: string;              // merchant order id, "" if none
  title: string;
  description: string;
  amount: string;                // price amount
  currency: string;              // "USD", "EUR", "CNY", ... or a crypto symbol
  amount_usd: string | null;
  status: InvoiceStatus;
  overpaid: boolean;
  paid_late: boolean;
  success_url: string;
  cancel_url: string;
  customer_email: string;
  metadata: Record<string, unknown>;
  checkout_url: string;          // `${checkout_base_url}/pay/${id}`
  expires_at: string;
  paid_at: string | null;
  created_at: string;
  updated_at: string;
  quote: Quote | null;           // the active (or paid) quote
}

interface Quote {
  id: string;
  asset_id: string;
  symbol: string;
  chain_id: string;
  chain_name: string;
  family: Family;
  network_id: string;            // EVM chain id (decimal string) or ""
  is_testnet: boolean;
  contract_address: string;      // "" for native
  decimals: number;
  wallet_kind: WalletKind;
  address: string;
  rate: string;                  // price currency per 1 unit of asset
  amount: string;                // amount due, whole units
  amount_base: string;
  amount_received: string;       // seen on chain (pending + confirmed)
  amount_received_base: string;
  amount_confirmed: string;
  amount_confirmed_base: string;
  amount_remaining: string;      // max(due - received, 0)
  amount_remaining_base: string;
  payment_uri: string;           // EIP-681 / BIP-21 / Solana Pay / "" (Tron: plain address is used for QR)
  required_confirmations: number;
  status: "active"|"superseded"|"paid"|"closed";
  created_at: string;
}

interface Transfer {
  id: string;
  chain_id: string;
  asset_id: string;              // "" if unknown token
  symbol: string;
  tx_hash: string;
  index: number;
  block_number: number;
  from_address: string;
  to_address: string;
  amount: string;
  amount_base: string;
  status: "pending"|"confirmed"|"orphaned";
  confirmations: number;
  required_confirmations: number;
  explorer_url: string;
  invoice_id: string | null;     // null = unmatched
  quote_id: string | null;
  detected_at: string;
  confirmed_at: string | null;
}

interface AssetInfo {
  id: string; symbol: string; name: string;
  chain_id: string; chain_name: string; family: Family; network_id: string;
  is_testnet: boolean; contract_address: string; decimals: number; is_stablecoin: boolean;
}
```

## 1. Merchant API — `/api/v1`

Auth: `Authorization: Bearer <api key>` (`sk_live_…`). 401 on a missing/revoked key or disabled merchant.

| Method & path | Body / query | Response |
|---|---|---|
| `POST /api/v1/invoices` | `CreateInvoice` | `201 Invoice` (or `200` with the existing invoice when `order_id` was already used with the same amount+currency; `409 conflict` if it differs) |
| `GET /api/v1/invoices/{id}` | | `Invoice` |
| `GET /api/v1/invoices` | `status?`, `order_id?`, paging | `Page<Invoice>` |
| `POST /api/v1/invoices/{id}/cancel` | | `Invoice` (409 unless status is `pending`) |
| `GET /api/v1/assets` | | `{ items: AssetInfo[] }` (payable right now for this merchant: asset and chain enabled and a receiving wallet on the chain — the merchant's own, else the chain's default) |

```ts
interface CreateInvoice {
  amount: string;                // required, > 0
  currency: string;              // required
  order_id?: string;             // idempotency key, max 128
  title?: string;                // max 200
  description?: string;          // max 2000
  asset_id?: string;             // pre-select the asset (creates the quote immediately)
  success_url?: string;          // http(s) URL
  cancel_url?: string;
  customer_email?: string;
  expires_in?: number;           // seconds, 60..86400; default = settings.invoice_ttl_seconds
  metadata?: Record<string, unknown>;  // max 4 KB
}
```

### Webhooks (gateway → merchant)

Body `{ id: string; type: string; created_at: string; data: Invoice }`, types:
`invoice.payment_detected | invoice.confirming | invoice.paid | invoice.underpaid | invoice.expired | invoice.cancelled`.
Headers and signature: see `architecture.md`.

## 2. Checkout API — `/api/checkout` (public, rate limited per IP)

The invoice UUID is the bearer secret.

| Method & path | Body | Response |
|---|---|---|
| `GET /api/checkout/{invoice_id}` | | `CheckoutView` |
| `POST /api/checkout/{invoice_id}/quote` | `{ asset_id: string }` | `CheckoutView` (409 if the invoice is not open or funds were already detected on another quote; 422 if no rate / asset unavailable) |

```ts
interface CheckoutView {
  invoice: {
    id: string; merchant_name: string; title: string; description: string;
    amount: string; currency: string; status: InvoiceStatus;
    overpaid: boolean; paid_late: boolean;
    success_url: string; cancel_url: string;
    expires_at: string; paid_at: string | null; created_at: string;
  };
  assets: CheckoutAsset[];       // every asset payable right now for the invoice's merchant, sorted
  quote: Quote | null;           // active quote (or the paid one)
  transfers: Transfer[];         // transfers credited to this invoice, newest first
  walletconnect_project_id: string; // "" when not configured
  server_time: string;           // for countdown drift correction
}
interface CheckoutAsset extends AssetInfo {
  estimated_amount: string | null; // approx. amount due in this asset, null if no rate
}
```

The checkout page polls `GET` every 3 s while the invoice is open.

## 3. Admin API — `/api/admin`

Auth: `POST /auth/login` sets an HttpOnly `cp_session` cookie (SameSite=Lax, 7 days).
All mutating requests must send header `X-Requested-With: cryptopay` (CSRF guard).
Roles: `owner` > `admin` > `viewer`. Viewers get 403 on every non-GET (except logout / own password).
User management is owner-only. Every mutation writes an audit log entry.

```ts
interface AdminUser { id: string; email: string; role: "owner"|"admin"|"viewer"; active: boolean; last_login_at: string|null; created_at: string; }

// One row of the e-mail outbox (bodies only in GET /email/outbox/{id}).
interface EmailMessage {
  id: string; kind: string; to: string; subject: string;
  status: "pending"|"sent"|"failed"; attempts: number; last_error: string;
  created_at: string; sent_at: string | null;
}

interface Merchant { id: string; name: string; status: "active"|"disabled"; webhook_url: string; has_webhook_secret: boolean; created_at: string; updated_at: string; }
interface ApiKey { id: string; merchant_id: string; name: string; prefix: string; last_used_at: string|null; revoked_at: string|null; created_at: string; }

interface Wallet {
  id: string; name: string; family: Family; kind: WalletKind; xpub: string; address: string; next_index: number;
  chain_ids: string[];           // chains this wallet is the default wallet of
  merchant_bindings: { merchant_id: string; merchant_name: string; chain_id: string }[];  // merchants using it as their own wallet
  created_at: string;
}

// A merchant's own receiving wallet on one chain (overrides the chain's default wallet for that merchant).
interface MerchantWallet { chain_id: string; wallet_id: string; wallet_name: string; kind: WalletKind; }

interface Chain {
  id: string; family: Family; name: string; network_id: string; native_symbol: string;
  is_testnet: boolean; enabled: boolean;
  rpc_urls: string[];            // masked: scheme + host only, path/query replaced by "/…" when present
  rpc_source: "database"|"env"|"default";
  confirmations: number; poll_interval_ms: number; scan_batch: number;
  explorer_tx_url: string; explorer_addr_url: string;
  wallet_id: string | null;      // the chain's DEFAULT wallet (merchants without their own wallet)
  scan_cursor: number; last_head: number; lag: number;   // last_head - scan_cursor
  last_scan_at: string | null; last_error: string;
  payable: boolean;              // enabled && default wallet assigned && rpc configured
  merchant_wallets: number;      // merchants with their own wallet on this chain (they can be paid even when payable is false)
}

interface Asset extends AssetInfo { quote_precision: number; rate_key: string; enabled: boolean; sort_order: number; rate_usd: string | null; }

interface InvoiceDetail extends Invoice { merchant_name: string; quotes: Quote[]; transfers: Transfer[]; webhook_events: WebhookEvent[]; }
interface AdminInvoice extends Invoice { merchant_name: string; }

interface WebhookEvent {
  id: string; merchant_id: string; invoice_id: string; event_type: string; url: string;
  status: "pending"|"delivered"|"failed"; attempts: number; next_attempt_at: string;
  last_status_code: number; last_error: string; last_response: string;
  delivered_at: string | null; created_at: string; payload: unknown;
}

interface AuditLog { id: number; actor_email: string; action: string; target_type: string; target_id: string; detail: unknown; ip: string; created_at: string; }

interface Settings {
  invoice_ttl_seconds: number; grace_period_seconds: number; underpay_tolerance_bps: number;
  stablecoin_peg: boolean; rate_markup_bps: number;
  webhook_max_attempts: number; webhook_timeout_seconds: number;
  checkout_base_url: string; walletconnect_project_id: string;
  security_emails: boolean;      // e-mail admins about security events on their own account
  alert_emails: boolean;         // e-mail operational alerts
  alert_recipients: string[];    // max 10 addresses; [] = every active owner
  email_locale: "zh"|"en";       // language of every e-mail
}

interface Overview {
  range_days: number;
  totals: { invoices: number; paid_invoices: number; open_invoices: number; volume_usd: string; conversion_rate: number /* 0..1 */; unmatched_transfers: number; failed_webhooks: number; };
  daily: { date: string /* YYYY-MM-DD */; invoices: number; paid: number; volume_usd: string }[];  // one row per day, zero-filled
  by_asset: { asset_id: string; symbol: string; chain_id: string; chain_name: string; paid: number; volume_usd: string }[];
  by_status: { status: InvoiceStatus; count: number }[];
}
```

| Method & path | Body / query | Response |
|---|---|---|
| `POST /auth/login` | `{email, password}` | `AdminUser` (401 on bad credentials; 429 after repeated failures) |
| `POST /auth/logout` | | `204` |
| `GET /auth/me` | | `AdminUser` |
| `POST /auth/password` | `{current_password, new_password}` (min 8 chars) | `204` |
| `GET /overview` | `days?` (1..365, default 30) | `Overview` |
| `GET /invoices` | `status?`, `merchant_id?`, `q?` (invoice id, order id, address or tx hash), `from?`, `to?` (RFC 3339), paging | `Page<AdminInvoice>` |
| `POST /invoices` | `CreateInvoice & { merchant_id: string }` | `201 AdminInvoice` |
| `GET /invoices/{id}` | | `InvoiceDetail` |
| `POST /invoices/{id}/cancel` | | `AdminInvoice` |
| `POST /invoices/{id}/mark-paid` | `{ note: string }` | `AdminInvoice` (manual resolution; allowed unless already paid/cancelled) |
| `GET /transfers` | `chain_id?`, `status?`, `matched?` (`true`/`false`), `q?` (tx hash or address), paging | `Page<Transfer>` |
| `POST /transfers/{id}/assign` | `{ invoice_id: string }` | `Transfer` (only for unmatched transfers; the invoice must have a quote for the same asset) |
| `GET /merchants` | `q?`, paging | `Page<Merchant>` |
| `POST /merchants` | `{ name, webhook_url? }` | `201 { merchant: Merchant; webhook_secret: string }` (secret shown once) |
| `GET /merchants/{id}` | | `{ merchant: Merchant; api_keys: ApiKey[]; wallets: MerchantWallet[] }` (`wallets`: explicit bindings only) |
| `PATCH /merchants/{id}` | `{ name?, status?, webhook_url? }` | `Merchant` |
| `POST /merchants/{id}/api-keys` | `{ name }` | `201 { api_key: ApiKey; key: string }` (plaintext shown once) |
| `DELETE /merchants/{id}/api-keys/{key_id}` | | `204` (revokes) |
| `POST /merchants/{id}/webhook-secret` | | `{ webhook_secret: string }` (rotates) |
| `PUT /merchants/{id}/wallets/{chain_id}` | `{ wallet_id: string }` | `MerchantWallet` (insert or replace; 400 `fields.wallet_id` for an unknown wallet, a wallet of another family or a static address not valid on the chain; 404 unknown merchant or chain) |
| `DELETE /merchants/{id}/wallets/{chain_id}` | | `204` (the merchant falls back to the chain's default wallet; idempotent; 404 unknown merchant or chain) |
| `GET /chains` | | `{ items: Chain[] }` |
| `PATCH /chains/{id}` | `{ enabled?, rpc_urls? (string[]; [] resets to env/default), confirmations?, poll_interval_ms?, scan_batch?, wallet_id? (string \| null: the default wallet) }` | `Chain` |
| `POST /chains/{id}/test-rpc` | `{ rpc_url?: string }` (omit to test the configured endpoints) | `{ ok: boolean; head: number; latency_ms: number; error: string }` |
| `GET /assets` | | `{ items: Asset[] }` |
| `POST /assets` | `{ chain_id, symbol, name, contract_address, decimals, quote_precision, rate_key, is_stablecoin }` | `201 Asset` (custom token; id = `lower(symbol)-chain_id`) |
| `PATCH /assets/{id}` | `{ enabled?, quote_precision?, sort_order?, rate_key?, name? }` | `Asset` |
| `GET /wallets` | | `{ items: Wallet[] }` |
| `POST /wallets/preview` | `{ family, kind, xpub?, address?, chain_id }` | `{ addresses: string[] }` (first 3 derived addresses, or the normalised static address) |
| `POST /wallets` | `{ name, family, kind, xpub?, address? }` | `201 Wallet` (409 `conflict` with `fields.xpub` / `fields.address` when the key material is already registered) |
| `PATCH /wallets/{id}` | `{ name }` | `Wallet` |
| `DELETE /wallets/{id}` | | `204` (409 if referenced by quotes or used as a merchant's own wallet) |
| `GET /webhook-events` | `status?`, `merchant_id?`, `invoice_id?`, paging | `Page<WebhookEvent>` |
| `POST /webhook-events/{id}/retry` | | `WebhookEvent` (re-queued for immediate delivery) |
| `GET /settings` | | `Settings` |
| `PUT /settings` | `Settings` | `Settings` (every field required; invalid `alert_recipients` → 400 `fields.alert_recipients`) |
| `GET /users` | | `{ items: AdminUser[] }` |
| `POST /users` | `{ email, password, role }` | `201 AdminUser` |
| `PATCH /users/{id}` | `{ role?, active?, password? }` | `AdminUser` (cannot demote/deactivate the last owner) |
| `DELETE /users/{id}` | | `204` (not yourself, not the last owner) |
| `GET /audit-logs` | `action?`, `actor?`, paging | `Page<AuditLog>` |
| `GET /email/status` | | `{ configured: boolean; from: string; host: string }` (`host` only, never credentials; `""` when not configured) |
| `POST /email/test` | `{ to?: string }` (default: the caller's own address; invalid → 400 `fields.to`) | `{ ok: boolean; error: string }` (sends synchronously; `error` is a sanitized SMTP error; 409 when e-mail is not configured; 429 when used too often; owner/admin) |
| `GET /email/outbox` | `status?`, `kind?`, paging | `Page<EmailMessage>` (newest first; owner/admin) |
| `GET /email/outbox/{id}` | | `EmailMessage & { body_text: string }` (owner/admin) |
| `POST /email/outbox/{id}/retry` | | `EmailMessage` (a failed or pending message is re-queued for immediate delivery, keeping its attempt count; 409 for a message that was already sent; owner/admin) |

### Receiving wallets

The wallet an invoice of merchant *M* is paid to on chain *C* is *M*'s own
wallet on *C* (`PUT /merchants/{id}/wallets/{chain_id}`) if there is one,
otherwise *C*'s default wallet (`PATCH /chains/{id}` `wallet_id`), otherwise
none — then *C*'s assets are not payable for *M* (they are missing from
`GET /api/v1/assets` and the checkout `assets`, and selecting one answers 422).
See `architecture.md`, "Receiving wallets".

* Existing quotes keep the wallet and address they were created with:
  setting, changing or removing a merchant wallet (or a chain's default
  wallet) only affects quotes created afterwards. A customer who already
  sees an address on the checkout of an unpaid invoice can still pay it: a
  chain keeps being scanned while it has live quotes, even after its last
  wallet was unassigned (`architecture.md`, "Scanner").
* `merchant.wallet.set` / `merchant.wallet.unset` are written to the audit
  log (`target_type` `merchant`, `detail.chain_id`, `detail.wallet_id`);
  removing a binding that does not exist changes nothing and is not logged.
* A wallet used by any merchant cannot be deleted (409) until those
  merchants are switched back to the default wallet or to another wallet.

### E-mail

E-mail is configured with the `SMTP_*` environment variables (README); it
is *configured* when `SMTP_HOST` and `SMTP_FROM` are set. Without it nothing
is queued and `GET /email/status` reports `configured: false`.

Messages are written to an outbox (`GET /email/outbox`) and delivered by a
background worker with retries (`min(2^attempt × 30 s, 2 h)`, at most 8
attempts, then `failed`). `kind` is one of:

| kind | sent to | when | throttle |
|---|---|---|---|
| `security.new_login` | the user | sign-in from an IP address not seen for the account before (not on the account's first sign-in) | — |
| `security.login_blocked` | the user | sign-in blocked after repeated failed password attempts | 1 per account per hour |
| `security.password_changed` | the user | password changed by the user or by an owner | — |
| `security.account_created` / `security.role_changed` / `security.account_deactivated` / `security.account_reactivated` | the user | an owner created the account, changed its role, deactivated or reactivated it | — |
| `alert.webhook_failed` | alert recipients | a webhook event exhausted its retries | 1 per merchant per hour |
| `alert.transfer_unmatched` | alert recipients | a transfer was stored without a matching invoice | 1 per chain per hour |
| `alert.invoice_underpaid` | alert recipients | an invoice became `underpaid` | 1 per invoice |
| `alert.scanner_failing` | alert recipients | a chain's scanner has been failing for 10 minutes or longer | 1 per chain per 6 hours |

`security.*` mails are sent when `security_emails` is on, `alert.*` mails
when `alert_emails` is on; alert recipients are `alert_recipients`, or every
active owner when that list is empty. `email_locale` selects the language.
No mail ever contains a password, secret, code or token.
