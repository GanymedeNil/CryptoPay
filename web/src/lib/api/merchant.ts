/**
 * Merchant API — `/api/v1`, authenticated with `Authorization: Bearer sk_…`.
 *
 * Server-to-server only: never ship a merchant API key to a browser. This
 * typed client exists for tooling, server components and tests; no UI uses it.
 */
import { createApiClient, seg, type ApiClient } from "./client";
import type {
  AssetInfo,
  CreateInvoice,
  Invoice,
  ItemsResponse,
  MerchantInvoiceQuery,
  Page,
} from "./types";

export interface MerchantApiOptions {
  apiKey: string;
  /** e.g. the backend URL when used server-side; "" = same origin. */
  baseUrl?: string;
  fetch?: typeof fetch;
}

export function createMerchantApi(opts: MerchantApiOptions | { client: ApiClient }) {
  const client =
    "client" in opts
      ? opts.client
      : createApiClient({
          baseUrl: opts.baseUrl,
          fetch: opts.fetch,
          headers: { Authorization: `Bearer ${opts.apiKey}` },
        });

  return {
    /** `POST /api/v1/invoices` → 201 (or 200 for an idempotent replay). */
    createInvoice: (body: CreateInvoice) => client.post<Invoice>("/api/v1/invoices", body),
    /** `GET /api/v1/invoices/{id}` */
    getInvoice: (id: string) => client.get<Invoice>(`/api/v1/invoices/${seg(id)}`),
    /** `GET /api/v1/invoices` */
    listInvoices: (query: MerchantInvoiceQuery = {}) =>
      client.get<Page<Invoice>>("/api/v1/invoices", { ...query }),
    /** `POST /api/v1/invoices/{id}/cancel` (409 unless pending). */
    cancelInvoice: (id: string) => client.post<Invoice>(`/api/v1/invoices/${seg(id)}/cancel`),
    /** `GET /api/v1/assets` — payable right now. */
    listAssets: () => client.get<ItemsResponse<AssetInfo>>("/api/v1/assets"),
  };
}

export type MerchantApi = ReturnType<typeof createMerchantApi>;
