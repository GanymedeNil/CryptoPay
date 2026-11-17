# Getting started

Run CryptoPay locally with Docker, then configure it in the admin console.

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
- To walk through an order, payment, confirmation and webhook on a testnet by hand, see [testnet-walkthrough.md](testnet-walkthrough.md) (local webhook receiver: `scripts/webhook-receiver.py`)
- Local email: compose ships with [Mailpit](https://mailpit.axllent.org/), which catches every email the gateway sends (security notices, alerts, test emails). Read them at http://localhost:8025 (change the port with `MAILPIT_PORT`). Mailpit only receives mail locally; it does **not** deliver anything to the outside world
- Stop: `make down`; delete the data volume: `make clean`

Without Docker: `cd backend && go run ./cmd/gateway` (needs `DATABASE_URL` and `APP_SECRET`); for the frontend see [`web/README.md`](../web/README.md).

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
6. **Protect the admin accounts**: turn on two-step verification under "Account security" and keep the recovery codes; once SMTP is configured, send a test email from "Settings → Email notifications" and turn on operational alerts if you want them (see [Admin account security](admin-security.md)).
