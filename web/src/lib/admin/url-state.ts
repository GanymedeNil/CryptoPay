"use client";

import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { useCallback, useMemo } from "react";

export type UrlPatch = Record<string, string | number | boolean | null | undefined>;

/** Applies a patch to search params: empty / null removes the key. */
export function patchSearchParams(current: URLSearchParams, patch: UrlPatch, resetPage: boolean): URLSearchParams {
  const next = new URLSearchParams(current.toString());
  for (const [key, value] of Object.entries(patch)) {
    if (value === null || value === undefined || value === "" || value === false) next.delete(key);
    else next.set(key, String(value));
  }
  // Changing a filter invalidates the page number.
  if (resetPage && !("page" in patch)) next.delete("page");
  return next;
}

/** Positive integer from a search param, or the fallback. */
export function intParam(value: string | null, fallback: number, max = Number.MAX_SAFE_INTEGER): number {
  if (!value || !/^\d+$/.test(value)) return fallback;
  const n = Number(value);
  return n >= 1 && n <= max ? n : fallback;
}

/** One of the allowed values, or undefined. */
export function enumParam<T extends string>(value: string | null, allowed: readonly T[]): T | undefined {
  return value && (allowed as readonly string[]).includes(value) ? (value as T) : undefined;
}

export const PAGE_SIZES = [20, 50, 100] as const;

/**
 * List-page state kept in the URL (shareable, survives reloads, back button).
 * `set` merges a patch and resets `page` unless the patch sets it.
 */
export function useUrlState() {
  const router = useRouter();
  const pathname = usePathname();
  const params = useSearchParams();
  const qs = params.toString();

  const set = useCallback(
    (patch: UrlPatch, opts: { resetPage?: boolean } = {}) => {
      const next = patchSearchParams(new URLSearchParams(qs), patch, opts.resetPage ?? true);
      const s = next.toString();
      router.replace(s ? `${pathname}?${s}` : pathname, { scroll: false });
    },
    [router, pathname, qs],
  );

  const clear = useCallback(
    (keep: readonly string[] = []) => {
      const next = new URLSearchParams();
      const cur = new URLSearchParams(qs);
      for (const k of keep) {
        const v = cur.get(k);
        if (v) next.set(k, v);
      }
      const s = next.toString();
      router.replace(s ? `${pathname}?${s}` : pathname, { scroll: false });
    },
    [router, pathname, qs],
  );

  return useMemo(() => {
    const p = new URLSearchParams(qs);
    return {
      params: p,
      get: (key: string) => p.get(key) ?? "",
      page: intParam(p.get("page"), 1),
      pageSize: (PAGE_SIZES as readonly number[]).includes(Number(p.get("page_size")))
        ? Number(p.get("page_size"))
        : PAGE_SIZES[0],
      set,
      clear,
    };
  }, [qs, set, clear]);
}
