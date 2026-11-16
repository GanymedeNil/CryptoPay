/**
 * The checkout page state machine: maps the latest API data (+ local UI
 * intent) to exactly one screen. Kept pure so every branch is unit-tested.
 */
import type { ApiError } from "@/lib/api/client";
import type { CheckoutView } from "@/lib/api/types";

export type CheckoutScreen =
  | "loading"
  | "not_found"
  | "error"
  | "select_asset"
  | "pay"
  | "partial"
  | "confirming"
  | "paid"
  | "expired"
  | "underpaid"
  | "cancelled";

export interface ViewStateInput {
  data: CheckoutView | null;
  error: ApiError | null;
  /** The customer pressed "change coin". */
  changingAsset: boolean;
}

export function deriveScreen({ data, error, changingAsset }: ViewStateInput): CheckoutScreen {
  if (!data) {
    if (error?.isNotFound) return "not_found";
    if (error) return "error";
    return "loading";
  }
  const { invoice, quote } = data;
  switch (invoice.status) {
    case "paid":
      return "paid";
    case "expired":
      return "expired";
    case "underpaid":
      return "underpaid";
    case "cancelled":
      return "cancelled";
    case "confirming":
      return "confirming";
    case "partial":
      return "partial";
    case "pending":
      if (!quote || quote.status !== "active" || changingAsset) return "select_asset";
      return "pay";
  }
}

/** Screens that never change again → stop polling. */
export function isTerminalScreen(screen: CheckoutScreen): boolean {
  return (
    screen === "paid" ||
    screen === "expired" ||
    screen === "underpaid" ||
    screen === "cancelled" ||
    screen === "not_found"
  );
}
