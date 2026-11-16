# syntax=docker/dockerfile:1.7
#
# CryptoPay web (Next.js standalone output).
#
#   docker build -t cryptopay-web web/                      # runtime image (default target)
#   docker build --target test -t cryptopay-web-test web/   # lint + typecheck + vitest
#
# BACKEND_URL is read at runtime by src/proxy.ts (the build-time value only
# seeds the compiled rewrites), so one image can point at any backend:
#   docker run -e BACKEND_URL=http://backend:8080 -p 3000:3000 cryptopay-web

ARG NODE_VERSION=24

# ---- base: node + pnpm (version pinned by package.json "packageManager") -----
FROM node:${NODE_VERSION}-trixie-slim AS base
ENV PNPM_HOME=/pnpm \
    PATH=/pnpm:$PATH \
    NEXT_TELEMETRY_DISABLED=1 \
    COREPACK_ENABLE_DOWNLOAD_PROMPT=0
RUN corepack enable
WORKDIR /app

# ---- deps: install from the lockfile (cached) --------------------------------
FROM base AS deps
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
RUN --mount=type=cache,id=pnpm-store,target=/pnpm/store \
    pnpm install --frozen-lockfile

# ---- source ------------------------------------------------------------------
FROM deps AS source
COPY . .
# public/ may be absent in a clean checkout (git does not track empty dirs).
RUN mkdir -p public

# ---- test: pnpm lint && pnpm typecheck && pnpm test --------------------------
FROM source AS test
CMD ["sh", "-c", "pnpm lint && pnpm typecheck && pnpm test"]

# ---- build -------------------------------------------------------------------
FROM source AS build
ARG BACKEND_URL=http://backend:8080
ARG NEXT_PUBLIC_SOLANA_RPC_URL=https://api.mainnet-beta.solana.com
ARG NEXT_PUBLIC_SOLANA_DEVNET_RPC_URL=https://api.devnet.solana.com
ENV BACKEND_URL=$BACKEND_URL \
    NEXT_PUBLIC_SOLANA_RPC_URL=$NEXT_PUBLIC_SOLANA_RPC_URL \
    NEXT_PUBLIC_SOLANA_DEVNET_RPC_URL=$NEXT_PUBLIC_SOLANA_DEVNET_RPC_URL
RUN pnpm build

# ---- runtime: standalone server, non-root -------------------------------------
FROM node:${NODE_VERSION}-trixie-slim AS runtime
ENV NODE_ENV=production \
    NEXT_TELEMETRY_DISABLED=1 \
    PORT=3000 \
    HOSTNAME=0.0.0.0 \
    BACKEND_URL=http://backend:8080
WORKDIR /app
COPY --from=build --chown=node:node /app/.next/standalone ./
COPY --from=build --chown=node:node /app/.next/static ./.next/static
COPY --from=build --chown=node:node /app/public ./public
USER node
EXPOSE 3000
HEALTHCHECK --interval=10s --timeout=5s --start-period=20s --retries=3 \
  CMD ["node", "-e", "fetch('http://127.0.0.1:'+(process.env.PORT||3000)+'/').then(r=>process.exit(r.status<500?0:1)).catch(()=>process.exit(1))"]
CMD ["node", "server.js"]
