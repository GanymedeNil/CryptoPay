# CryptoPay web

Next.js frontend of the CryptoPay gateway: the shared foundation (typed API
client, i18n, theme, UI kit, money utilities), the customer checkout at
`/pay/[id]`, and the admin console under `/admin`.

The HTTP contract is [`../docs/api.md`](../docs/api.md); architecture and
invoice lifecycle are in [`../docs/architecture.md`](../docs/architecture.md).

## Quick start

```bash
pnpm install

# Without the Go backend: start the mock API, then the app against it
pnpm mock                                   # http://localhost:8787/mock lists every scenario
BACKEND_URL=http://localhost:8787 pnpm dev  # http://localhost:3000/pay/22222222-2222-4222-8222-222222222222
                                            # http://localhost:3000/admin (mock accounts below)

# With the Go backend on :8080 (the default)
pnpm dev
```

| Script | |
|---|---|
| `pnpm dev` | dev server (Turbopack) |
| `pnpm build` / `pnpm start` | production build (`output: "standalone"`) / serve it |
| `pnpm lint` | ESLint (next core-web-vitals + TypeScript) |
| `pnpm typecheck` | `next typegen` + `tsc --noEmit` (strict) |
| `pnpm test` | Vitest + React Testing Library, single run (CI) — `pnpm test:watch` to watch |
| `pnpm mock` | dependency-free mock of the checkout + admin API (`mock/server.mjs`, `mock/admin.mjs`) |

Stack: Next.js 16 (App Router, `src/`, Cache Components + Partial
Prefetching on), React 19, TypeScript strict, Tailwind CSS 4, shadcn/ui
(`radix-nova` style, lucide icons), next-themes, TanStack Query 5 and
recharts 3 (admin), Vitest 5, pnpm, Node ≥ 20.9 (developed on Node 24).

## Environment

| Variable | Where | Default | |
|---|---|---|---|
| `BACKEND_URL` | server | `http://localhost:8080` | Go backend. `/api/*` and `/healthz` are proxied to it so the browser stays same-origin. |
| `NEXT_PUBLIC_SOLANA_RPC_URL` | browser, build time | `https://api.mainnet-beta.solana.com` | Solana RPC used only to fetch a recent blockhash for wallet payments. The public endpoint is rate limited: set your own in production. |
| `NEXT_PUBLIC_SOLANA_DEVNET_RPC_URL` | browser, build time | `https://api.devnet.solana.com` | Same, for devnet assets. |
| `PORT`, `HOSTNAME` | standalone server | `3000`, `0.0.0.0` | |

`BACKEND_URL` details: the `next.config.ts` rewrites are compiled into the
build (`.next/routes-manifest.json`), so the value present at `next build`
is the default. `src/proxy.ts` re-reads `BACKEND_URL` **at runtime** and,
when set, forwards to it instead — a prebuilt image can therefore be pointed
at any backend (`BACKEND_URL=http://backend:8080 node server.js`).

Standalone deploy: after `pnpm build`, copy `.next/static` to
`.next/standalone/.next/static` (and `public/` to `.next/standalone/public`),
then run `node .next/standalone/server.js`.

See `.env.example`.

## Layout

```
mock/server.mjs              mock checkout API (node:http, no deps) + mounts mock/admin.mjs
mock/admin.mjs               in-memory Admin API (sessions, roles, seeded data)
src/
  app/
    layout.tsx               fonts, theme provider, <Suspense> + locale cookie → I18nProvider
    globals.css              design tokens (light/dark), Tailwind theme mapping
    page.tsx                 landing (/)
    pay/[id]/page.tsx        checkout route (Suspense shell + client CheckoutPage)
    admin/                   admin console (see "Admin console" below)
    icon.svg
  proxy.ts                   runtime BACKEND_URL override for /api/* and /healthz
  components/
    ui/                      shadcn/ui components (generated; edit freely)
    brand/                   BrandMark, Guilloche (security-print pattern)
    checkout/                checkout page: screens, polling hook, wallet panel, QR …
    admin/                   admin console: building blocks + one folder per page
    site-header.tsx          brand + LocaleSwitcher + ThemeToggle
    locale-switcher.tsx, theme-toggle.tsx, providers.tsx, landing.tsx
  hooks/                     useNow, useIsMobile (UA), use-mobile (viewport < 768 px), useCopy
  lib/
    api/                     types.ts (docs/api.md verbatim), client.ts, checkout.ts, admin.ts, merchant.ts
    i18n/                    config, dictionaries/{zh,en}.ts, provider (useT/useLocale), server (getServerT)
    format/                  amount.ts (BigInt decimals), address.ts, countdown.ts, date.ts,
                             relative.ts (relative time, date inputs), units.ts (bps↔%, durations)
    checkout/                pure checkout logic: state machine, payment amounts/URIs, asset grouping
    admin/                   admin data layer (TanStack Query keys/hooks), errors, URL state, roles,
                             settings-form conversions, timeline, explorer links, wallet-input checks,
                             e-mail kinds
    wallets/                 wallet adapters per family (evm, tron, solana, bitcoin), deep links, detection
    explorer.ts, clipboard.ts, device.ts, env.ts, browser.ts, utils.ts (cn)
  test/                      setup, renderWithProviders, fixtures, admin.tsx (renderAdmin + admin fixtures)
```

## Conventions (read this before building `/admin`)

### API

* **Types** live in `src/lib/api/types.ts` and use the **exact names** from
  `docs/api.md` (`Invoice`, `Quote`, `Chain`, `Settings`, …, plus named request
  bodies such as `UpdateChainRequest`). When the contract changes, change
  `docs/api.md` and this file together. Don't add client-only fields.
* **Calls** go through `adminApi` (`src/lib/api/admin.ts`), one function per
  endpoint, e.g. `adminApi.listInvoices({ status: "paid", page: 2 })`,
  `adminApi.updateChain(id, { enabled: false })`. Every mutation automatically
  sends `X-Requested-With: cryptopay` and `credentials: "same-origin"` (the
  `cp_session` cookie). Paths are relative: the browser talks to Next.js,
  which proxies `/api/*` — never call the backend origin from the browser.
* **Errors** are `ApiError` (`status`, `code`, `message`, `fields?`) with
  helpers `isUnauthorized` (→ send to the login page), `isForbidden` (viewer
  role), `isConflict`, `isNotFound`, `isRateLimited`, `isTransient` (network /
  429 / 5xx; `status === 0` means no response). Map `fields` onto form errors
  (`form.setError(field, { message })`).
* `/api/*` is reserved for the backend (`beforeFiles` rewrite): don't create
  Next.js route handlers under `src/app/api`.
* Server-side calls (if ever needed) can use
  `createAdminApi(createApiClient({ baseUrl: process.env.BACKEND_URL }))`.

### Money

Amounts are decimal **strings** (`"12.5"`) and `*_base` integer strings.
They must never pass through a JS `number`. Use `src/lib/format/amount.ts`:
`formatAmount` (grouping, padding, rounding), `formatMoney(amount, currency,
locale)` (fiat symbol or `"12.5 USDT"`), `compareDecimal`, `addDecimal`,
`subtractDecimal`, `baseToDecimal`, `decimalToBase`, `roundDecimal`. Counts
and ratios (`conversion_rate`, `confirmations`) are plain numbers.

### i18n

* Dictionaries: `src/lib/i18n/dictionaries/zh.ts` is the source of truth for
  the shape; `en.ts` is typed against it, so a missing key fails
  `pnpm typecheck`, and a test checks both have the same keys and
  placeholders. Put admin strings under a new top-level `admin` namespace
  (e.g. `admin.invoices.title`). Placeholders: `{name}`.
* Client components: `const t = useT(); t("admin.invoices.title", { count })`.
  `useLocale()` gives `{ locale, setLocale }`.
* Server components: `const t = await getServerT()` (`@/lib/i18n/server`). It
  reads the `cp_locale` cookie, so it's dynamic data (see Next.js notes
  below). The `LocaleSwitcher` calls `router.refresh()` so server components
  re-render in the new language.
* Locale resolution: `cp_locale` cookie → `Accept-Language` → `zh`. The
  choice is persisted in the cookie and localStorage.
* Dates: `formatDateTime(iso, locale)` (`src/lib/format/date.ts`). Never
  hard-code user-visible text; brand names and API data are the exceptions.

### UI

* shadcn components are in `src/components/ui` (button, card, input, label,
  badge, dialog, alert-dialog, dropdown-menu, select, tabs, tooltip, skeleton,
  sonner, separator, alert, table, sheet, switch, textarea, field,
  input-group, checkbox, radio-group, popover, progress, scroll-area,
  pagination, avatar, spinner, empty, toggle, toggle-group, sidebar,
  breadcrumb, chart, collapsible). Add more with
  `pnpm dlx shadcn@4.23.0 add <name>` (e.g. `command`, `calendar`).
  Local edits: `sidebar.tsx` omits `data-active` when inactive (the
  `data-active:` variant matches presence) and takes `mobileTitle` /
  `mobileDescription` for the phone sheet.
* Forms: shadcn v4 replaced the old `form` component with `field`; use
  `Field`/`FieldLabel`/`FieldError` with `react-hook-form` + `zod` +
  `@hookform/resolvers` (installed).
* Toasts: `import { toast } from "sonner"` (the `<Toaster />` is mounted in
  the root layout). Tooltips work anywhere (provider in the root layout).
* `cn()` from `@/lib/utils`. Icons: `lucide-react` only, no emoji.
* Theme: light/dark/system via next-themes (`class` on `<html>`). Reuse
  `SiteHeader`, `ThemeToggle`, `LocaleSwitcher`.
* Design tokens are CSS variables in `src/app/globals.css` (standard shadcn
  set + `warning`, `warning-surface`, `success`, `success-surface`,
  `guilloche`, `slip-band`). Fonts: `font-heading` (Bricolage Grotesque),
  `font-sans` (Public Sans), `font-mono` (IBM Plex Mono; use it for
  amounts, addresses, hashes, with the `tabular` utility). CJK falls back to
  system fonts.
* Accessibility: visible focus rings (shadcn defaults), labels on every
  control, `aria-live` for async status, `prefers-reduced-motion` respected
  globally.

### Next.js 16 notes

* Read `node_modules/next/dist/docs/` for APIs — this version differs from
  older Next.js (see `AGENTS.md`). Middleware is now `src/proxy.ts`.
* `cacheComponents` + `partialPrefetching` are enabled. A server component
  that reads `cookies()`, `headers()`, `params` or `searchParams` must sit
  under a `<Suspense>` boundary **inside its page/layout segment**, otherwise
  dev shows an "instant navigation" error. Pattern: see
  `src/app/pay/[id]/page.tsx` (page renders `<Suspense fallback={shell}>`
  around an async child that awaits `params`). The root layout wraps the app
  in a Suspense boundary (it reads the locale cookie).
* Pages that only fetch on the client (most of the admin) can be client
  components using `useT()` and `adminApi` directly.

### Tests

Vitest + RTL in jsdom (`src/test/setup.ts`). `renderWithProviders(ui, {
locale })` wraps i18n + tooltip providers; `src/test/fixtures.ts` builds API
objects. `vi.useFakeTimers()` works with RTL/user-event (setup shims Jest's
timer API). Files needing Node's `Buffer`/crypto (Solana) use
`// @vitest-environment node`. `next/navigation` is mocked globally.

## Admin console (`/admin`)

Everything is client-rendered after `GET /api/admin/auth/me` (the session
cookie is HttpOnly and belongs to the API behind the `/api` proxy), so each
admin route exports `instant = false` and a localized `generateMetadata`
title (`src/lib/admin/metadata.ts`).

| Route | |
|---|---|
| `/admin/login` | email + password, 401 / 429 / network messages, `?next=` (console paths only) and `?reason=expired\|signedOut` |
| `/admin` | overview: 7/30/90-day range (`?days=`), KPIs, needs-attention (unmatched transfers, failed webhooks → filtered lists), daily volume + invoices chart (recharts via shadcn `chart`), by asset, by status, chain-monitoring strip |
| `/admin/invoices` | filters in the URL (`status`, `merchant_id`, `q`, `from`, `to`, `page`, `page_size`), create-invoice dialog → checkout link with copy / open |
| `/admin/invoices/[id]` | summary, timeline (rebuilt from quotes / transfers / webhook events), quotes, transfers, webhook events (payload + retry), cancel, mark paid (note required); refreshes every 10 s while open |
| `/admin/transfers` | `chain_id`, `status`, `matched`, `q`; unmatched → assign dialog (paste id or checkout link, invoice preview, asset compatibility check) |
| `/admin/webhooks` | `status`, `merchant_id`, `invoice_id`; expandable rows (payload, last error / response), retry |
| `/admin/merchants`, `/admin/merchants/[id]` | search, create (webhook secret shown once), profile (name / status / webhook URL), API keys (create → key once, revoke), rotate secret, receiving wallets (every enabled chain, mainnets first / testnets grouped, with its effective wallet — merchant / default / not configured — and a select: "use the default wallet" or a wallet of the chain's family → `PUT` / `DELETE /merchants/{id}/wallets/{chain_id}`; read-only for viewers), integration snippet (curl + Node.js / Python signature check) |
| `/admin/chains` | grouped mainnet / testnet: enable (optimistic), payable + reasons (payable = the default wallet; merchant-wallet count and "merchants with their own wallet can still be paid"), scanner state, RPC editor (masked list, replace / reset, per-URL test), default-RPC warning on mainnets, scan parameters, default receiving wallet (same family only) |
| `/admin/assets` | grouped by chain: enable (optimistic), edit precision / order / rate key / name, add custom token |
| `/admin/wallets` | list with usage (default wallet of chains · merchant wallet of merchants, linked), 3-step wizard (type → key + `POST /wallets/preview` + "I checked" → name) with the "never paste a private key" notice, local secret and duplicate-key detection (a 409 `fields.xpub` / `fields.address` sends the wizard back to the key), rename, delete (409 explained: quotes, or the merchants using it) |
| `/admin/settings` | every `Settings` field in operator units (minutes / hours, %), validation, sticky save bar; "Email notifications" (`GET /email/status` — host / sender, or how to configure `SMTP_*` —, security / alert email switches, alert recipients editor (max 10, validated, Enter adds), email language, "send test email" with the SMTP outcome) |
| `/admin/emails` | owner / admin only (hidden for viewers): the e-mail outbox, `status` / `kind` filters in the URL (kinds with readable labels), expandable rows that load the text body, retry for failed messages, "not configured" notice |
| `/admin/users` | owner only (hidden from the nav otherwise): create (random password generator), role, (de)activate, reset password, delete, "last owner" errors |
| `/admin/audit-logs` | `action`, `actor` filters, expandable detail JSON, links to the target |

**Roles.** `useAuth()` gives `{ user, canWrite, isOwner }`. Mutating controls
use `GatedButton` / `RoleTooltip`: disabled for viewers with a tooltip saying
why (forms render as disabled fieldsets, a read-only banner sits under the
header). A 401 anywhere fires `UNAUTHORIZED_EVENT` (query / mutation cache)
and `AuthGate` sends the user to `/admin/login?next=…&reason=expired`; 403
renders a "no access" state.

**Data layer** (`src/lib/admin/queries.ts`, TanStack Query 5): `adminKeys`
(everything under `["admin"]`, list + detail share a prefix), one hook per
read (`useInvoices(q)`, `useChains()`, …; lists keep the previous page while
the next loads), `useAdminMutation` (success / error toasts with the same
verb as the control, invalidates the given keys + the audit log, maps errors
via `describeError`) and `useOptimisticItemMutation` (switches). Retries only
transient errors. `applyFieldErrors(error, setError, fields, map)` puts
`ApiError.fields` on react-hook-form fields.

**Building blocks** (`src/components/admin/`): `PageHeader` / `Section` /
`DetailList`, `DataTable` (loading / empty / error+retry, row links,
expandable rows, pagination, cards under 768 px), `PaginationBar`,
`FilterBar` + `SearchFilter` (debounced) / `SelectFilter` / `DateRangeFilter`,
`useUrlState` (filters + page in the URL, page reset on filter change),
status badges (invoice / quote / transfer / webhook / merchant / role),
`CopyButton`, `MonoValue` (shortened mono value + copy + explorer link),
`Money` / `CryptoAmount` (string formatters only), `RelativeTime` /
`DateTimeStack`, `ConfirmDialog` (busy until the action settles),
`SecretRevealDialog` (shown once; can't close until "I've stored it"),
`JsonViewer`, `CodeBlock`, `FormField` / `FormError`, `ErrorState` /
`EmptyState` / skeletons.

### Mock accounts

`pnpm mock` seeds three accounts (also printed at startup):

| Role | Email | Password |
|---|---|---|
| owner | `owner@cryptopay.test` | `owner-pass-123` |
| admin | `admin@cryptopay.test` | `admin-pass-123` |
| viewer | `viewer@cryptopay.test` | `viewer-pass-123` |

(`former.ops@cryptopay.test` is a deactivated admin.)

## Checkout (`/pay/[id]`)

`CheckoutPage` (`src/components/checkout/checkout-page.tsx`) renders one
screen derived by the pure state machine `deriveScreen`
(`src/lib/checkout/view-state.ts`):

| Invoice | Screen |
|---|---|
| `pending`, no active quote (or "change coin") | choose asset — grouped by coin, network badges, `estimated_amount`, testnet badges → `POST …/quote` |
| `pending` + active quote | pay — exact amount (copy), static-wallet "exact amount" warning, address (copy, head/tail emphasised), network warning, QR (payment request ↔ address only), countdown, wallet panel, change coin |
| `partial` | received vs remaining + the pay UI for the remainder (QR/wallets use the remaining amount) |
| `confirming` | per-transfer confirmations `n / required`, explorer links |
| `paid` | success, overpaid / paid-late notes, redirect to `success_url` after 6 s (button + "stay") |
| `expired`, `underpaid`, `cancelled` | explanation, "check again", `cancel_url` link (http(s) only) |
| 404 / malformed id | not found |
| network error, no data | retry (also retried automatically with backoff) |

`useCheckout` polls `GET /api/checkout/{id}` every 3 s while the invoice is
open, stops in terminal states, pauses while the tab is hidden (refreshes on
return), backs off exponentially on errors (keeping the last data with a
"reconnecting" notice), drops stale responses, and keeps a clock offset from
`server_time` so the countdown is right even when the device clock is not.

### Wallets (`src/lib/wallets`)

Each family implements `WalletAdapter.pay({ option, quote, amountBase, onStep })`
→ `{ txHash }`, loaded lazily with `loadWalletAdapter(family)` (wagmi/viem and
@solana/web3.js are separate chunks, prefetched only when a wallet is
detected). Detection (`injected.ts`, `evm/eip6963.ts`) is dependency free.
Errors are normalised to `rejected | wrong_network | insufficient_funds |
not_installed | unsupported | pending | unknown`.

| Family | Wallets | Flow |
|---|---|---|
| EVM | every EIP-6963 wallet (MetaMask, OKX, Coinbase, Rabby, Trust, …) with its icon; legacy `window.ethereum`; WalletConnect when `walletconnect_project_id` is set | `@wagmi/core` 2: connect → `switchChain(network_id)` (adds the chain from viem's definition on 4902) → `sendTransaction` (native value, or ERC-20 `transfer(to, amount)` calldata on `contract_address`) |
| Tron | TronLink, OKX (`okxwallet.tronLink`), any injected `tronWeb` | `tron_requestAccounts` → network check from the node host (mainnet vs Nile per `chain_id`/`is_testnet`) → TRX `trx.sendTransaction(to, sun)` / TRC-20 `contract(abi, token).transfer(to, amount).send({ feeLimit: 100 TRX })` |
| Solana | Phantom, Solflare, OKX, Backpack, generic `window.solana` | connect → blockhash from `NEXT_PUBLIC_SOLANA_*_RPC_URL` → SOL `SystemProgram.transfer` / SPL `createAssociatedTokenAccountIdempotent` + `transferChecked` (Token-2022 detected) → `signAndSendTransaction` |
| Bitcoin | UniSat (`getChain`/`switchChain`, legacy `getNetwork`), OKX (`okxwallet.bitcoin` / `.bitcoinTestnet`) | `sendBitcoin(address, sats)` |
| Litecoin | none (no injected wallets exist) | QR + `litecoin:` link |

On a phone without an injected wallet the panel shows "open this page in a
wallet app" links (`deeplinks.ts`, table driven, unit tested) for the quote's
family, plus an "open wallet app" button with the payment URI (`ethereum:`,
`bitcoin:`, `litecoin:`, `solana:`). Deep-link formats and their sources are
documented at the top of `src/lib/wallets/deeplinks.ts`.

## Mock backend (`mock/server.mjs`)

`pnpm mock` (env `MOCK_PORT=8787`, `MOCK_CHECKOUT_BASE`, `MOCK_BLOCK_MS=2000`,
`MOCK_CLOCK_SKEW_MS`, `MOCK_WC_PROJECT_ID`, `MOCK_ADMIN_DELAY_MS=120`). It implements
`GET /api/checkout/{id}`, `POST /api/checkout/{id}/quote` (incl. 400/404/409/422)
and `/healthz` with the real catalog of assets, mock rates and static-wallet
unique amounts. Confirmations tick every `MOCK_BLOCK_MS` and `confirming`
turns into `paid` by itself.

Seeded scenarios (open `http://localhost:8787/mock` for links and controls):

| id | scenario |
|---|---|
| `11111111-1111-4111-8111-111111111111` | choose asset |
| `22222222-2222-4222-8222-222222222222` | pay USDT · Tron (xpub) |
| `33333333-3333-4333-8333-333333333333` | pay USDT · Ethereum, static wallet |
| `44444444-4444-4444-8444-444444444444` | partial BTC |
| `55555555-5555-4555-8555-555555555555` | confirming ETH · Base → paid |
| `66666666-6666-4666-8666-666666666666` | paid + overpaid + redirect |
| `77777777-7777-4777-8777-777777777777` | expired |
| `88888888-8888-4888-8888-888888888888` | underpaid |
| `99999999-9999-4999-8999-999999999999` | cancelled |
| `aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa` | USDC · Solana Devnet (testnet, static) |
| `bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb` | LTC, priced in CNY |
| `cccccccc-cccc-4ccc-8ccc-cccccccccccc` | expires in 90 s |
| `dddddddd-dddd-4ddd-8ddd-dddddddddddd` | ETH · Sepolia, priced in EUR |
| `eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee` | no payable assets |

Controls: `GET /mock/invoices/{id}/advance?to=next|pending|partial|confirming|paid|overpaid|expired|underpaid|cancelled`,
`POST /mock/invoices` `{ amount, currency, asset_id, wallet_kind, state, ttl_seconds, id }`,
`GET /mock/outage?seconds=20` (503s), `GET /mock/reset`. `MOCK_SMTP=off` starts
the mock with e-mail "not configured".

**Admin API** (`mock/admin.mjs`, every endpoint of docs/api.md §3, state in
memory, reset by `/mock/reset`): `cp_session` HttpOnly cookie, CSRF header
check on every mutation (403 without it), viewers get 403 on mutations and
non-owners on `/users`, login rate limit (5 failures / 10 min → 429),
validation errors with `fields`, 404 / 409 / 422, `page` / `page_size`
paging and the documented filters, an audit entry per mutation. Seeded with
deterministic data: 5 merchants (one with a failing webhook endpoint, one
disabled), ~150 invoices over 90 days in every status (the checkout
scenarios above included, so console-created invoices open at `/pay/{id}`),
unmatched / orphaned transfers, delivered / failed webhook events, 13 chains
with varied scanner state (lagging, erroring, disabled, default RPC, no
default wallet), 8 wallets, merchant wallets (Pixel Forge on Ethereum /
Polygon / Base, Nimbus VPN on Tron, Acme on Litecoin — which has no default
wallet), custom token without a rate, audit history. Assets disabled in the
console, or without a receiving wallet for the invoice's merchant (its own,
else the chain's default), disappear from the mock checkout. RPC tests fail for
hosts containing `bad` / `invalid` / `down`; webhook deliveries fail for URLs
containing `broken` / `down`. E-mail: `GET /email/status` reports
Mailpit, the outbox is seeded with security notices and alerts (some
failed), new security events queue mails while the toggles are on, queued
mails "send" after ~1.5 s, and addresses containing `bounce` / `fail` are
rejected (test mail and delivery). `mock/admin.test.mjs` smoke-tests it.
