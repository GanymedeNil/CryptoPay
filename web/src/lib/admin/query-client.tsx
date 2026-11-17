"use client";

import {
  MutationCache,
  QueryCache,
  QueryClient,
  QueryClientProvider,
} from "@tanstack/react-query";
import { isApiError } from "@/lib/api/client";

/**
 * Fired whenever any admin request comes back 401. The authenticated layout
 * listens and sends the operator to the login page (keeping the current URL
 * so they come back to it).
 */
export const UNAUTHORIZED_EVENT = "cryptopay:admin-unauthorized";

export function notifyUnauthorized(): void {
  if (typeof window !== "undefined") window.dispatchEvent(new Event(UNAUTHORIZED_EVENT));
}

/**
 * Fired when any admin request comes back 403 `two_factor_setup_required`
 * (the `require_2fa` setting is on and this account has no two-factor
 * authentication yet). The authenticated layout switches to the setup flow.
 */
export const TWO_FACTOR_REQUIRED_EVENT = "cryptopay:admin-2fa-required";

export function notifyTwoFactorRequired(): void {
  if (typeof window !== "undefined") window.dispatchEvent(new Event(TWO_FACTOR_REQUIRED_EVENT));
}

export function isTwoFactorSetupRequired(error: unknown): boolean {
  return isApiError(error) && error.code === "two_factor_setup_required";
}

function onAnyError(error: unknown) {
  if (isApiError(error) && error.isUnauthorized) notifyUnauthorized();
  else if (isTwoFactorSetupRequired(error)) notifyTwoFactorRequired();
}

/** Retry only what can succeed without user action (network, 429, 5xx). */
export function shouldRetry(failureCount: number, error: unknown): boolean {
  if (isApiError(error)) {
    if (!error.isTransient) return false;
    if (error.isRateLimited) return failureCount < 1;
  }
  return failureCount < 2;
}

export function makeQueryClient(): QueryClient {
  return new QueryClient({
    queryCache: new QueryCache({ onError: onAnyError }),
    mutationCache: new MutationCache({
      onError: (error, _vars, _ctx, mutation) => {
        // e.g. a wrong *current* password may come back as 401: not a lost session.
        if (mutation.meta?.skipAuthRedirect) return;
        onAnyError(error);
      },
    }),
    defaultOptions: {
      queries: {
        staleTime: 15_000,
        gcTime: 5 * 60_000,
        retry: shouldRetry,
        refetchOnWindowFocus: true,
      },
      mutations: { retry: false },
    },
  });
}

let browserClient: QueryClient | undefined;

function getQueryClient(): QueryClient {
  // Isolate server renders; keep one cache for the lifetime of the tab.
  if (typeof window === "undefined") return makeQueryClient();
  browserClient ??= makeQueryClient();
  return browserClient;
}

export function AdminQueryProvider({
  children,
  client,
}: {
  children: React.ReactNode;
  /** Injected by tests. */
  client?: QueryClient;
}) {
  return <QueryClientProvider client={client ?? getQueryClient()}>{children}</QueryClientProvider>;
}
