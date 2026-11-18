#!/usr/bin/env bash
# Smoke test of a running CryptoPay stack (docker compose up -d --wait).
#
#   docker compose up -d --build --wait && scripts/smoke.sh      (or: make smoke)
#
# It proves the *runtime images* work end to end, which the unit tests (they
# run against the "test" build targets) cannot: the gateway is up and migrated,
# the web container serves pages and proxies /api to the gateway, and the
# bootstrap admin can sign in through that proxy.
#
# Environment (defaults match docker-compose.yml without a .env file):
#   BACKEND_PORT (8080)  WEB_PORT (3000)  ADMIN_EMAIL  ADMIN_PASSWORD
# Set ADMIN_EMAIL / ADMIN_PASSWORD when your .env changes them.
set -euo pipefail

BACKEND="http://127.0.0.1:${BACKEND_PORT:-8080}"
WEB="http://127.0.0.1:${WEB_PORT:-3000}"
EMAIL="${ADMIN_EMAIL:-admin@example.com}"
PASSWORD="${ADMIN_PASSWORD:-change-me-please}"

# shellcheck disable=SC1003 # the patterns match a literal backslash
case "$EMAIL$PASSWORD" in
  *'"'* | *'\'*) echo "smoke: ADMIN_EMAIL / ADMIN_PASSWORD must not contain \" or \\" >&2; exit 2 ;;
esac

jar="$(mktemp)"
trap 'rm -f "$jar"' EXIT
failed=0

pass() { printf '  ok    %s\n' "$1"; }
fail() { printf '  FAIL  %s\n' "$1" >&2; failed=1; }

# status URL [curl args...] -> HTTP status code ("000" when unreachable)
status() {
  local url="$1"
  shift
  curl -s -o /dev/null -w '%{http_code}' --max-time 15 "$@" "$url" || true
}

expect_status() { # description url want [curl args...]
  local what="$1" url="$2" want="$3"
  shift 3
  local got
  got="$(status "$url" "$@")"
  if [ "$got" = "$want" ]; then pass "$what ($got)"; else fail "$what: want $want, got $got ($url)"; fi
}

echo "gateway $BACKEND"
expect_status "GET /healthz" "$BACKEND/healthz" 200
expect_status "GET /readyz (database reachable, migrated)" "$BACKEND/readyz" 200
expect_status "GET /api/v1/invoices without an API key is refused" "$BACKEND/api/v1/invoices" 401

echo "web $WEB"
expect_status "GET / (checkout landing page)" "$WEB/" 200
expect_status "GET /admin/login" "$WEB/admin/login" 200
expect_status "GET /api/checkout/... is proxied to the gateway (unknown invoice -> 404)" \
  "$WEB/api/checkout/00000000-0000-4000-8000-000000000000" 404

echo "admin sign-in through the web proxy"
login_status="$(curl -s -o /dev/null -w '%{http_code}' --max-time 15 -c "$jar" \
  -H 'Content-Type: application/json' -H 'X-Requested-With: cryptopay' \
  --data "{\"email\":\"$EMAIL\",\"password\":\"$PASSWORD\"}" "$WEB/api/admin/auth/login" || true)"
case "$login_status" in
  200) pass "POST /api/admin/auth/login ($login_status)" ;;
  401) fail "POST /api/admin/auth/login: 401 - wrong ADMIN_EMAIL / ADMIN_PASSWORD for this stack?" ;;
  *) fail "POST /api/admin/auth/login: want 200, got $login_status" ;;
esac
if [ "$login_status" = 200 ]; then
  me="$(curl -s --max-time 15 -b "$jar" "$WEB/api/admin/auth/me" || true)"
  case "$me" in
    *"\"email\":\"$EMAIL\""*) pass "GET /api/admin/auth/me returns the signed-in admin" ;;
    *) fail "GET /api/admin/auth/me did not return $EMAIL" ;;
  esac
fi

if [ "$failed" -ne 0 ]; then
  echo "smoke test FAILED" >&2
  exit 1
fi
echo "smoke test passed"
