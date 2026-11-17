import type { MerchantOverview, MerchantOverviewSort, SortOrder } from "@/lib/api/types";
import { enumParam } from "./url-state";

/** Ranges offered by the overview and the merchant page (days). 30 is the API default. */
export const OVERVIEW_RANGES = [7, 30, 90] as const;
export const DEFAULT_RANGE = 30;

/** `?days=` → one of OVERVIEW_RANGES (anything else: the default). */
export function rangeParam(value: string | null): number {
  const n = Number(value);
  return (OVERVIEW_RANGES as readonly number[]).includes(n) ? n : DEFAULT_RANGE;
}

/** Overview URL parameter holding the merchant filter (`/admin?merchant=<id>`). */
export const MERCHANT_PARAM = "merchant";

export const MERCHANT_SORTS: readonly MerchantOverviewSort[] = [
  "volume_usd",
  "invoices",
  "paid_invoices",
  "conversion_rate",
  "open_invoices",
  "failed_webhooks",
  "name",
];
export const DEFAULT_MERCHANT_SORT: MerchantOverviewSort = "volume_usd";

/** The server's default order for a key: names A→Z, figures largest first. */
export function defaultOrder(sort: MerchantOverviewSort): SortOrder {
  return sort === "name" ? "asc" : "desc";
}

export interface MerchantSortState {
  sort: MerchantOverviewSort;
  order: SortOrder;
}

/** Reads `?sort=&order=` (invalid values fall back to the defaults). */
export function sortFromParams(sort: string | null, order: string | null): MerchantSortState {
  const s = enumParam(sort, MERCHANT_SORTS) ?? DEFAULT_MERCHANT_SORT;
  return { sort: s, order: enumParam(order, ["asc", "desc"] as const) ?? defaultOrder(s) };
}

/** Header click: the sorted column flips its order; another column starts at its default order. */
export function nextSort(current: MerchantSortState, clicked: MerchantOverviewSort): MerchantSortState {
  if (current.sort === clicked) return { sort: clicked, order: current.order === "asc" ? "desc" : "asc" };
  return { sort: clicked, order: defaultOrder(clicked) };
}

/** URL patch for a sort state; defaults stay out of the URL. */
export function sortPatch(s: MerchantSortState): { sort: string | null; order: string | null } {
  return {
    sort: s.sort === DEFAULT_MERCHANT_SORT ? null : s.sort,
    order: s.order === defaultOrder(s.sort) ? null : s.order,
  };
}

/** No invoice created and none paid in the range (shown muted: "who is idle"). */
export function isIdle(row: Pick<MerchantOverview, "invoices" | "paid_invoices">): boolean {
  return row.invoices === 0 && row.paid_invoices === 0;
}

function withQuery(path: string, params: Record<string, string | undefined>): string {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) if (v) q.set(k, v);
  const s = q.toString();
  return s ? `${path}?${s}` : path;
}

/** The overview restricted to a merchant (the range is kept unless it is the default). */
export function overviewHref(merchantId: string, days: number = DEFAULT_RANGE): string {
  return withQuery("/admin", { days: days === DEFAULT_RANGE ? undefined : String(days), [MERCHANT_PARAM]: merchantId });
}

/** Invoice list, optionally filtered by status and merchant. */
export function invoicesHref(merchantId?: string | null, status?: string): string {
  return withQuery("/admin/invoices", { status, merchant_id: merchantId || undefined });
}

/** Failed webhook deliveries, optionally of one merchant. */
export function failedWebhooksHref(merchantId?: string | null): string {
  return withQuery("/admin/webhooks", { status: "failed", merchant_id: merchantId || undefined });
}
