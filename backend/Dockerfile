# syntax=docker/dockerfile:1.7
#
# CryptoPay gateway (Go).
#
#   docker build -t cryptopay-backend backend/                 # runtime image (default target)
#   docker build --target test --build-context docs=docs -t cryptopay-backend-test backend/
#
# Build args VERSION / COMMIT / DATE are stamped into `gateway version`.

ARG GO_VERSION=1.27.3

# ---- build: static binary ---------------------------------------------------
FROM golang:${GO_VERSION}-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOTOOLCHAIN=local
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download && go mod verify
COPY . .
ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" \
      -o /out/gateway ./cmd/gateway \
 && /out/gateway version

# ---- docs: the API contract for the contract tests ---------------------------
# docker-compose.test.yml passes the repository's docs/ directory as the named
# build context "docs", which replaces this empty stage. (The contract tests
# read ../docs/api.md relative to the module, i.e. /docs/api.md here.)
FROM scratch AS docs

# ---- test: vet + race tests (needs cgo/glibc for -race) ---------------------
# Used by docker-compose.test.yml; expects TEST_DATABASE_URL.
FROM golang:${GO_VERSION}-trixie AS test
WORKDIR /src
ENV CGO_ENABLED=1 GOTOOLCHAIN=local GOFLAGS=-buildvcs=false
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY --from=docs / /docs/
COPY . .
CMD ["sh", "-c", "go vet ./... && go test -race -count=1 ./..."]

# ---- runtime: distroless, non-root ------------------------------------------
# distroless/static ships CA certificates (RPC / CoinGecko over TLS) and tzdata.
FROM gcr.io/distroless/static-debian12:nonroot AS runtime
COPY --from=build /out/gateway /usr/local/bin/gateway
USER nonroot:nonroot
ENV HTTP_ADDR=:8080
EXPOSE 8080
# No shell or curl in the image: the binary probes its own /healthz.
HEALTHCHECK --interval=10s --timeout=5s --start-period=20s --retries=3 \
  CMD ["/usr/local/bin/gateway", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/gateway"]
CMD ["serve"]
