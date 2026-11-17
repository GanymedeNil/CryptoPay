# Assets

- `logo.svg` — the CryptoPay mark (a copy of `web/src/app/icon.svg`, the app's favicon). Works on light and dark backgrounds.
- `screenshots/` — images used by the top-level README.

The screenshots show the real UI running against the development mock (`web/mock/`), so every merchant, invoice,
address and amount in them is made-up demo data. To refresh them, start the mock and the web app, then capture the pages
at 2× device scale in the English UI (`cp_locale=en` cookie):

```bash
cd web
node mock/server.mjs                                   # mock API on :8787
BACKEND_URL=http://localhost:8787 pnpm exec next dev -p 3100
# checkout:  http://localhost:3100/pay/22222222-2222-4222-8222-222222222222  (scenario list: http://localhost:8787/mock)
# console:   http://localhost:3100/admin  (owner@cryptopay.test / owner-pass-123, see the mock's start-up output)
```
