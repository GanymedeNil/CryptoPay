# CryptoPay — common tasks. Everything runs in Docker; no local Go / Node needed.

COMPOSE      ?= docker compose
COMPOSE_TEST ?= docker compose -f docker-compose.test.yml

.PHONY: help up down logs ps build seed-dev test test-backend test-web test-live lint clean

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
test-backend: ## go vet + go test -race ./... against a throw-away PostgreSQL
	@$(COMPOSE_TEST) run --rm --build backend-test; status=$$?; \
	  $(COMPOSE_TEST) down --remove-orphans >/dev/null 2>&1; exit $$status

test-web: ## pnpm lint && pnpm typecheck && pnpm test (vitest)
	@$(COMPOSE_TEST) run --rm --build web-test; status=$$?; \
	  $(COMPOSE_TEST) down --remove-orphans >/dev/null 2>&1; exit $$status

test-live: ## chain adapters against free public RPCs (network-dependent, rate limited)
	@$(COMPOSE_TEST) --profile live run --rm --build backend-live; status=$$?; \
	  $(COMPOSE_TEST) --profile live down --remove-orphans >/dev/null 2>&1; exit $$status

lint: ## gofmt + go vet (backend) and eslint + tsc (web), in Docker
	@docker build -q --target test --build-context docs=./docs -t cryptopay-backend-test ./backend >/dev/null
	docker run --rm cryptopay-backend-test sh -c 'test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }; go vet ./...'
	@docker build -q --target test -t cryptopay-web-test ./web >/dev/null
	docker run --rm cryptopay-web-test sh -c 'pnpm lint && pnpm typecheck'

clean: ## stop everything and delete the database volume (DESTROYS DATA)
	$(COMPOSE) down -v --remove-orphans
	$(COMPOSE_TEST) --profile live down -v --remove-orphans
