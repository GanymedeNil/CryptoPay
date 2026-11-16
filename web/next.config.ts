import type { NextConfig } from "next";

/**
 * The browser always talks to this Next.js server (same origin). Requests to
 * `/api/*` and `/healthz` are proxied to the Go backend.
 *
 * NOTE: rewrites are resolved when `next build` runs and are written into the
 * build output, so for `next build` / `output: "standalone"` set BACKEND_URL
 * at build time (e.g. a Docker build ARG). `next dev` reads it on start.
 */
const backendUrl = (process.env.BACKEND_URL || "http://localhost:8080").replace(
  /\/+$/,
  "",
);

const nextConfig: NextConfig = {
  output: "standalone",
  poweredByHeader: false,
  cacheComponents: true,
  partialPrefetching: true,
  async rewrites() {
    return {
      // `/api/*` is reserved for the Go backend: it wins over any app route.
      beforeFiles: [
        { source: "/api/:path*", destination: `${backendUrl}/api/:path*` },
        { source: "/healthz", destination: `${backendUrl}/healthz` },
      ],
      afterFiles: [],
      fallback: [],
    };
  },
  async headers() {
    return [
      {
        // The invoice id in the URL is a bearer secret: never leak it through
        // the Referer header (explorer links, merchant redirects) and keep the
        // page out of search engines.
        source: "/pay/:path*",
        headers: [
          { key: "Referrer-Policy", value: "no-referrer" },
          { key: "X-Robots-Tag", value: "noindex, nofollow" },
        ],
      },
      {
        source: "/:path*",
        headers: [{ key: "X-Content-Type-Options", value: "nosniff" }],
      },
    ];
  },
  turbopack: {
    rules: {
      "*.css": {
        loaders: ["@tailwindcss/turbopack"],
        as: "*.css",
      },
    },
  },
};

export default nextConfig;
