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
| evm | Ethereum, BSC, Polygon, Arbitrum, Base (+ Sepolia) | ETH, BNB, USDT, USDC |
| tron | Tron (+ Nile) | TRX, USDT |
| utxo | Bitcoin, Litecoin (+ Bitcoin testnet) | BTC, LTC |
| solana | Solana (+ Devnet) | SOL, USDT, USDC |

## Repository layout

```
backend/                     Go module "cryptopay"
  cmd/gateway/               main: serve | migrate | seed | create-admin
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
  internal/api/              HTTP: merchant, checkout, admin
web/                         Next.js (App Router) + shadcn/ui
docs/                        this file, api.md
docker-compose.yml           postgres + backend + web
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
* **Secrets at rest are encrypted** (RPC URLs with API keys, webhook secrets)
  with AES-256-GCM keyed from `APP_SECRET`. API keys and session tokens are
  stored only as SHA-256 hashes.
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

A wallet belongs to one family and is assigned to chains (`chains.wallet_id`).

* **xpub** (EVM, Tron, UTXO): account-level extended public key. Each invoice
  gets a fresh address `xpub/0/index` (`wallets.next_index` is allocated
  atomically). Quotes of one invoice on the same wallet reuse the same index,
  so one invoice has one address across all EVM chains.
  * EVM: BIP-44 `m/44'/60'/0'` xpub → keccak address.
  * Tron: BIP-44 `m/44'/195'/0'` xpub → base58 address.
  * UTXO: BIP-84 `m/84'/coin'/0'` `xpub`/`zpub` (`tpub`/`vpub` testnet) → P2WPKH bech32.
* **static** (all families; the only option for Solana): one fixed address.
  Invoices are told apart by a **unique amount**: the quote amount is bumped
  in its last digits until `(chain, asset, address, amount_due)` is unique
  among live quotes. A transfer matches a quote only on the exact amount;
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

One loop per enabled chain with a wallet:

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
