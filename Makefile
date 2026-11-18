# CryptoPay — common tasks. Everything runs in Docker; no local Go / Node needed.

COMPOSE      ?= docker compose
COMPOSE_TEST ?= docker compose -f docker-compose.test.yml

# The release version lives in VERSION (see docs/releasing.md). It is stamped,
# with the commit and the build time, into the backend image: `gateway version`.
export CRYPTOPAY_VERSION    ?= $(shell cat VERSION)
export CRYPTOPAY_COMMIT     ?= $(shell git describe --always --dirty --abbrev=12 --exclude='*' 2>/dev/null || echo unknown)
export CRYPTOPAY_BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

.PHONY: help up down logs ps build seed-dev test test-backend test-web test-live smoke lint version check-version clean

help: ## list targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z_-]+:.*## / {printf "  %-14s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

up: ## build and start postgres + backend + web (http://localhost:3000) + mailpit
	$(COMPOSE) up --build -d
	@echo "web:     http://localhost:$${WEB_PORT:-3000}  (admin: /admin)"
	@echo "backend: http://127.0.0.1:$${BACKEND_PORT:-8080}/healthz"
	@echo "mailpit: http://localhost:$${MAILPIT_PORT:-8025}  (e-mail sent by the gateway, captured locally)"

down: ## stop the stack (data volume is kept; `make clean` removes it)
	$(COMPOSE) down

logs: ## follow the logs of all services
	$(COMPOSE) logs -f --tail=200

ps: ## show service status
	$(COMPOSE) ps

build: ## build the backend and web images
	$(COMPOSE) build

seed-dev: ## demo merchant + API key + TEST-ONLY testnet wallets (refused in production)
	$(COMPOSE) exec backend /usr/local/bin/gateway seed-dev

test: test-backend test-web ## run every test suite in Docker

# `run` exits with the test command's status; the stack is removed either way.
test-backend: ## gofmt + go vet + go test -race ./... against a throw-away PostgreSQL
	@$(COMPOSE_TEST) run --rm --build backend-test; status=$$?; \
	  $(COMPOSE_TEST) down --remove-orphans >/dev/null 2>&1; exit $$status

test-web: ## pnpm lint && pnpm typecheck && pnpm test (vitest)
	@$(COMPOSE_TEST) run --rm --build web-test; status=$$?; \
	  $(COMPOSE_TEST) down --remove-orphans >/dev/null 2>&1; exit $$status

test-live: ## chain adapters against free public RPCs (network-dependent, rate limited)
	@$(COMPOSE_TEST) --profile live run --rm --build backend-live; status=$$?; \
	  $(COMPOSE_TEST) --profile live down --remove-orphans >/dev/null 2>&1; exit $$status

smoke: ## smoke-test the running stack (`make up` first; ADMIN_EMAIL / ADMIN_PASSWORD if .env changes them)
	scripts/smoke.sh

lint: ## gofmt + go vet (backend) and eslint + tsc (web), in Docker
	@docker build -q --target test --build-context docs=./docs -t cryptopay-backend-test ./backend >/dev/null
	docker run --rm cryptopay-backend-test sh -c 'test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }; go vet ./...'
	@docker build -q --target test -t cryptopay-web-test ./web >/dev/null
	docker run --rm cryptopay-web-test sh -c 'pnpm lint && pnpm typecheck'

version: ## print the release version (VERSION)
	@echo $(CRYPTOPAY_VERSION)

check-version: ## VERSION, web/package.json and CHANGELOG.md agree
	scripts/check-version.sh

clean: ## stop everything and delete the database volume (DESTROYS DATA)
	$(COMPOSE) down -v --remove-orphans
	$(COMPOSE_TEST) --profile live down -v --remove-orphans
