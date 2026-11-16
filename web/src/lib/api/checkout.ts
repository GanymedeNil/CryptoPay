/** Checkout API — `/api/checkout` (public; the invoice UUID is the secret). */
import { apiClient, seg, type ApiClient } from "./client";
import type { CheckoutView, CreateQuoteRequest } from "./types";

export function createCheckoutApi(client: ApiClient = apiClient) {
  return {
    /** `GET /api/checkout/{invoice_id}` */
    get(invoiceId: string, options?: { signal?: AbortSignal }): Promise<CheckoutView> {
      return client.get<CheckoutView>(`/api/checkout/${seg(invoiceId)}`, undefined, options);
    },
    /**
     * `POST /api/checkout/{invoice_id}/quote` — 409 if the invoice is not open
     * or funds were already detected on another quote; 422 if no rate / asset
     * unavailable.
     */
    createQuote(
      invoiceId: string,
      assetId: string,
      options?: { signal?: AbortSignal },
    ): Promise<CheckoutView> {
      const body: CreateQuoteRequest = { asset_id: assetId };
      return client.post<CheckoutView>(`/api/checkout/${seg(invoiceId)}/quote`, body, options);
    },
  };
}

export type CheckoutApi = ReturnType<typeof createCheckoutApi>;

export const checkoutApi: CheckoutApi = createCheckoutApi();
