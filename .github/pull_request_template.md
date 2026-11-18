<!--
Every change to `main` goes through a pull request; the `CI` check must pass
before it can be merged. Keep one feature (or fix) per pull request and, inside
it, one commit per feature.
-->

## What and why

<!-- What does this change, and why is it needed? Link the issue: "Closes #123". -->

## How it was tested

<!-- `make test` runs everything in Docker. Say what you added or ran by hand
(chain, asset, scenario) when the change touches payments or scanning. -->

## Checklist

- [ ] `make test` passes locally (or I explain below why it could not run)
- [ ] New behaviour has tests; a bug fix has a test that fails without the fix
- [ ] Documentation is updated (`docs/`, `README.md`, `docs/api.md` for API changes)
- [ ] `CHANGELOG.md` has an entry under **Unreleased** for anything a user or operator would notice
- [ ] A database change ships as a new migration and is backward compatible with the previous release
- [ ] No secrets, private keys, seed phrases or real addresses in code, tests, fixtures or screenshots
