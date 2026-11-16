import { NextResponse, type NextRequest } from "next/server";

/**
 * Runtime override for the `/api/*` + `/healthz` → Go backend rewrites.
 *
 * `next.config.ts` rewrites are compiled at build time, so a prebuilt
 * standalone image would keep the BACKEND_URL it was built with. When
 * BACKEND_URL is set in the *runtime* environment, this proxy forwards to it
 * instead; otherwise the request falls through to the build-time rewrites.
 */
export function proxy(request: NextRequest) {
  const backend = process.env.BACKEND_URL;
  if (!backend) return NextResponse.next();
  const { pathname, search } = request.nextUrl;
  return NextResponse.rewrite(new URL(`${pathname}${search}`, backend.replace(/\/+$/, "") + "/"));
}

export const config = {
  matcher: ["/api/:path*", "/healthz"],
};
