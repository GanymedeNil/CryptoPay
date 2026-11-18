# Security policy

CryptoPay handles payment data for merchants and customers, so security reports are taken seriously and handled privately.

## Reporting a vulnerability

**Do not open a public issue, discussion or pull request for a vulnerability.**

Report it privately through GitHub: open the repository's **Security** tab and choose **Report a vulnerability** (a private security advisory). Include:

- what the problem is and which component it affects (checkout, merchant API, admin console, webhook delivery, chain scanner, the Docker images...),
- the version (`gateway version`) and how to reproduce it,
- the impact you believe it has, for example funds sent to a wrong address, an invoice marked paid without a payment, an authentication bypass or a leaked secret.

Never include real private keys or seed phrases: the gateway is watch-only and does not need them.

You can expect an acknowledgement within a few days. Fixes are prepared in a private branch, released, and then disclosed in the release notes and the advisory, crediting you if you wish.

## Supported versions

Security fixes go into the latest release. CryptoPay is pre-1.0: upgrade to the newest `0.x` release to receive fixes.

## Scope notes

- The design goal is that the server can never spend funds: it only holds extended public keys and addresses. A way to make it hold or use a private key would be a vulnerability.
- Configuration mistakes such as running with the development defaults (`APP_SECRET`, `ADMIN_PASSWORD`) in production are documented in [docs/production.md](docs/production.md); they are not vulnerabilities.
- Known limitations are listed in [docs/production.md](docs/production.md#known-limitations).
