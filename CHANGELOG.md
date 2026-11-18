# Changelog

All notable changes to CryptoPay are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). While the major
version is 0, a minor release (0.x.0) may contain breaking changes; they are
called out under **Changed** or **Removed**.

## [Unreleased]

## [0.1.0] - 2026-11-18

First release.

### Added

- Non-custodial, watch-only payment gateway: every invoice gets a fresh
  receiving address derived from an extended public key (EVM, Tron, Bitcoin,
  Litecoin), or a static address matched by a unique amount (Solana). The server
  never holds a private key.
- Chain adapters for EVM chains, Tron, UTXO chains and Solana, with a built-in
  catalog of nine mainnets and four testnets: USDT, USDC and PYUSD plus BTC,
  ETH, LTC, TRX, SOL and BNB. Custom ERC-20 / TRC-20 / SPL tokens can be added
  from the console.
- Invoice lifecycle with partial payments, overpayments, an underpayment
  tolerance, a grace period for late payments, confirmation depth per chain and
  reorg handling.
- Checkout page with browser-wallet payments (MetaMask, TronLink, UniSat, OKX,
  Phantom...) and payment-request QR codes for phone wallets; layout for small
  screens; English and Chinese.
- Merchant API (`/api/v1`) with API keys, idempotent invoice creation on
  `order_id`, and signed webhooks (HMAC-SHA256, exponential backoff, at-least-once
  delivery with a stable event id).
- Several merchants, each with its own API keys, webhook secret and optional
  receiving wallets, and a per-merchant overview.
- Admin console: chains and RPC endpoints (stored encrypted), wallets, invoices,
  transfers, webhook deliveries, audit log, owner / admin / viewer roles,
  two-step verification with recovery codes, session management and e-mail
  notifications and operational alerts.
- Commercial RPC providers configured in the console, with free public nodes as
  the default for development.
- Docker-based development: `docker compose up`, `make test` for the backend and
  web test suites, a `gateway` CLI (`serve`, `migrate`, `create-admin`,
  `reset-2fa`, `seed-dev`, `healthcheck`, `version`).
- Documentation: getting started, integration guide, API reference,
  architecture, configuration, production checklist and a testnet walkthrough.

[Unreleased]: https://github.com/GanymedeNil/CryptoPay/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/GanymedeNil/CryptoPay/releases/tag/v0.1.0
