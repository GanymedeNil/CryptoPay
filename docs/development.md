# Development and tests

## Running the tests in Docker

Every test runs in a container, so neither Go nor Node is needed on the host:

```bash
make test           # backend + frontend
make test-backend   # gofmt + go vet + go test -race ./... (uses a throwaway PostgreSQL container)
make test-web       # pnpm lint && pnpm typecheck && pnpm test
make test-live      # optional: exercise the chain adapters against free public RPCs (needs the network, subject to rate limits, may fail now and then)
make lint           # gofmt / go vet / eslint / tsc
make smoke          # smoke-test the running stack (after `make up`): health, web, admin sign-in through the proxy
```

The equivalent compose command is `docker compose -f docker-compose.test.yml run --rm --build backend-test` (its exit code is the test result), followed by `docker compose -f docker-compose.test.yml down -v`.

To run the backend tests directly on the host: `export TEST_DATABASE_URL='postgres://postgres:postgres@localhost:54329/cryptopay_test?sslmode=disable' && cd backend && go test -race ./...` (the database tests are skipped when it is not set).

## Continuous integration

[`.github/workflows/ci.yml`](../.github/workflows/ci.yml) runs on every pull request into `main` and on every push to `main` (that is, every merge). It uses the same Make targets as above, so a green `make test` locally means the same thing in CI:

| Job | What it runs |
|---|---|
| Backend tests | `make test-backend`: gofmt, `go vet`, `go test -race ./...` against PostgreSQL |
| Web tests | `make test-web`: eslint, `tsc`, vitest |
| Docker stack smoke test | `docker compose up --build --wait`, then [`scripts/smoke.sh`](../scripts/smoke.sh). This builds the production images (including `next build`, which the unit tests do not run) and checks health, the web pages, the `/api` proxy and the admin sign-in |
| Repository checks | compose files are valid, shell scripts parse, workflows pass [actionlint](https://github.com/rhysd/actionlint) |
| **CI** | Succeeds only when every job above did. It is the one status check that branch protection requires, so adding a job to the workflow only means listing it in this job's `needs` |

A new push to a pull request cancels that pull request's earlier run; runs on `main` are never cancelled.
