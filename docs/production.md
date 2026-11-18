# Security and production

## Security notes

- **Non-custodial and read-only**: the gateway stores only extended **public** keys or static addresses, calls read-only RPC methods only, and never signs or broadcasts a transaction. Sweeping funds is done by the merchant in wallet software that holds the mnemonic, and is outside the scope of this system.
- **Never paste a private key or mnemonic.** Private keys such as `xprv` are rejected, and any instruction that asks you to type a mnemonic into the console is wrong.
- `APP_SECRET` encrypts the RPC URLs (which often contain API keys) and the webhook signing secrets in the database; API keys and session tokens are stored only as SHA-256 hashes. If `APP_SECRET` is lost, that ciphertext can no longer be decrypted; if it leaks, every RPC key and webhook secret must be rotated.
- Admin console: HttpOnly + SameSite=Lax session cookie (Secure in production), `X-Requested-With: cryptopay` required on every write (CSRF protection), sign-in rate limiting per IP + email, optional (or enforced) TOTP two-step verification with one-time recovery codes, session management, security-event emails, three roles (owner / admin / viewer), and an audit log of every change (without secrets, passwords, verification codes or full RPC URLs).
- The invoice UUID is the access credential of the checkout: do not leak a `checkout_url` in public.

> **Disclaimer.** Test your whole setup before you accept real funds. CryptoPay is provided as is, and any loss of funds is your responsibility, not the author's. See the [disclaimer in the README](../README.md#disclaimer).

## Production checklist

- [ ] `APP_ENV=production`, HTTPS everywhere (TLS terminated at a reverse proxy), and a correct `checkout_base_url`
- [ ] A strong random `APP_SECRET`, backed up offline; a strong `ADMIN_PASSWORD`, changed after the first sign-in; `POSTGRES_PASSWORD` changed
- [ ] Every admin has two-step verification enabled and their recovery codes stored offline, then "Require two-step verification" is turned on under "Settings → Security"; keep at least two owners and note the `gateway reset-2fa` [break-glass procedure](admin-security.md)
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
- Webhooks are delivered at least once and in no guaranteed order (see [Merchant integration](integration.md#3-receive-and-verify-the-webhook)).
