# Development and tests

## Running the tests in Docker

Every test runs in a container, so neither Go nor Node is needed on the host:

```bash
make test           # backend + frontend
make test-backend   # go vet + go test -race ./... (uses a throwaway PostgreSQL container)
make test-web       # pnpm lint && pnpm typecheck && pnpm test
make test-live      # optional: exercise the chain adapters against free public RPCs (needs the network, subject to rate limits, may fail now and then)
make lint           # gofmt / go vet / eslint / tsc
```

The equivalent compose command is `docker compose -f docker-compose.test.yml run --rm --build backend-test` (its exit code is the test result), followed by `docker compose -f docker-compose.test.yml down -v`.

To run the backend tests directly on the host: `export TEST_DATABASE_URL='postgres://postgres:postgres@localhost:54329/cryptopay_test?sslmode=disable' && cd backend && go test -race ./...` (the database tests are skipped when it is not set).
