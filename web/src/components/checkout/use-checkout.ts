"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { checkoutApi as defaultApi, type CheckoutApi } from "@/lib/api/checkout";
import { ApiError, isAbortError } from "@/lib/api/client";
import type { CheckoutView } from "@/lib/api/types";
import { isOpenStatus } from "@/lib/checkout/payment";
import { computeClockOffset } from "@/lib/format/countdown";

export const POLL_INTERVAL_MS = 3000;
const MAX_BACKOFF_MS = 30_000;

export interface UseCheckoutOptions {
  api?: CheckoutApi;
  intervalMs?: number;
}

export interface UseCheckoutResult {
  data: CheckoutView | null;
  /** Last request error (cleared by the next success). */
  error: ApiError | null;
  /** server clock − client clock, from the last response (ms). */
  clockOffsetMs: number;
  /** A manual refresh is in flight. */
  refreshing: boolean;
  /** Re-fetch now (retry / "check again"), then resume polling if open. */
  refresh: () => Promise<void>;
  /** POST a quote for an asset; resolves with the new view, throws ApiError. */
  selectAsset: (assetId: string) => Promise<CheckoutView>;
}

function toApiError(e: unknown): ApiError {
  if (e instanceof ApiError) return e;
  return new ApiError({
    status: 0,
    code: "network_error",
    message: e instanceof Error ? e.message : String(e),
    cause: e,
  });
}

interface Engine {
  refresh(): Promise<void>;
  selectAsset(assetId: string): Promise<CheckoutView>;
}

/**
 * Loads `GET /api/checkout/{id}` and polls it every 3 s while the invoice is
 * open (pending / partial / confirming). Polling stops in terminal states,
 * pauses while the tab is hidden (refreshing as soon as it is visible again)
 * and backs off exponentially on errors. Responses are sequenced so a slow
 * poll can never overwrite a newer quote.
 */
export function useCheckout(invoiceId: string | null, options: UseCheckoutOptions = {}): UseCheckoutResult {
  const api = options.api ?? defaultApi;
  const interval = options.intervalMs ?? POLL_INTERVAL_MS;

  const [data, setData] = useState<CheckoutView | null>(null);
  const [error, setError] = useState<ApiError | null>(null);
  const [clockOffsetMs, setClockOffsetMs] = useState(0);
  const [refreshing, setRefreshing] = useState(false);
  const engine = useRef<Engine | null>(null);

  useEffect(() => {
    if (!invoiceId) return;
    const id = invoiceId;
    let alive = true;
    let timer: ReturnType<typeof setTimeout> | null = null;
    let seq = 0;
    let applied = 0;
    let failures = 0;
    let latest: { data: CheckoutView | null; error: ApiError | null } = { data: null, error: null };
    let inflight: AbortController | null = null;

    function apply(n: number, view: CheckoutView) {
      if (!alive || n < applied) return;
      applied = n;
      failures = 0;
      latest = { data: view, error: null };
      setData(view);
      setError(null);
      setClockOffsetMs(computeClockOffset(view.server_time, Date.now()));
    }

    function fail(n: number, e: unknown) {
      if (!alive || n < applied) return;
      const err = toApiError(e);
      failures += 1;
      latest = { ...latest, error: err };
      setError(err);
    }

    async function fetchOnce() {
      const n = ++seq;
      inflight?.abort();
      const controller = new AbortController();
      inflight = controller;
      try {
        apply(n, await api.get(id, { signal: controller.signal }));
      } catch (e) {
        if (!isAbortError(e)) fail(n, e);
      } finally {
        if (inflight === controller) inflight = null;
      }
    }

    function shouldPoll(): boolean {
      if (latest.data) return isOpenStatus(latest.data.invoice.status);
      // No data yet: keep retrying transient failures, never a 404.
      return !latest.error || !latest.error.isNotFound;
    }

    function clear() {
      if (timer) clearTimeout(timer);
      timer = null;
    }

    function schedule() {
      clear();
      if (!alive || document.visibilityState === "hidden" || !shouldPoll()) return;
      const delay = failures > 0 ? Math.min(interval * 2 ** failures, MAX_BACKOFF_MS) : interval;
      timer = setTimeout(async () => {
        timer = null;
        await fetchOnce();
        schedule();
      }, delay);
    }

    function onVisibility() {
      if (document.visibilityState === "hidden") clear();
      else if (!timer && shouldPoll()) void fetchOnce().then(schedule);
    }

    engine.current = {
      async refresh() {
        clear();
        await fetchOnce();
        schedule();
      },
      async selectAsset(assetId: string) {
        const n = ++seq;
        // A poll started before this POST must not overwrite its result.
        inflight?.abort();
        clear();
        try {
          const view = await api.createQuote(id, assetId);
          apply(n, view);
          return view;
        } finally {
          schedule();
        }
      },
    };

    void fetchOnce().then(schedule);
    document.addEventListener("visibilitychange", onVisibility);
    return () => {
      alive = false;
      engine.current = null;
      clear();
      inflight?.abort();
      document.removeEventListener("visibilitychange", onVisibility);
    };
  }, [api, interval, invoiceId]);

  const refresh = useCallback(async () => {
    const e = engine.current;
    if (!e) return;
    setRefreshing(true);
    try {
      await e.refresh();
    } finally {
      setRefreshing(false);
    }
  }, []);

  const selectAsset = useCallback(async (assetId: string) => {
    const e = engine.current;
    if (!e) throw new Error("Checkout is not loaded");
    return e.selectAsset(assetId);
  }, []);

  return { data, error, clockOffsetMs, refreshing, refresh, selectAsset };
}
