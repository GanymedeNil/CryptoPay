import type { InvoiceDetail } from "@/lib/api/types";

export type TimelineKind =
  | "created"
  | "quote"
  | "quoteSuperseded"
  | "detected"
  | "confirmed"
  | "orphaned"
  | "paid"
  | "expired"
  | "underpaid"
  | "cancelled"
  | "expiresAt"
  | "webhook"
  | "webhookFailed";

export interface TimelineEntry {
  key: string;
  at: string;
  kind: TimelineKind;
  /** Placeholder values for the label (asset, amount, event). */
  vars?: Record<string, string>;
  /** Not happened yet (e.g. the expiry of an open invoice). */
  future?: boolean;
}

const OPEN = new Set(["pending", "partial", "confirming"]);

/**
 * Status history reconstructed from the invoice detail: there is no status
 * log in the API, so every timestamp the invoice, its quotes, transfers and
 * webhook events carry becomes one entry, oldest first.
 */
export function buildTimeline(inv: InvoiceDetail): TimelineEntry[] {
  const out: TimelineEntry[] = [{ key: "created", at: inv.created_at, kind: "created" }];

  for (const q of inv.quotes) {
    out.push({
      key: `q-${q.id}`,
      at: q.created_at,
      kind: q.status === "superseded" ? "quoteSuperseded" : "quote",
      vars: { asset: `${q.symbol} · ${q.chain_name}` },
    });
  }
  for (const tr of inv.transfers) {
    const amount = `${tr.amount} ${tr.symbol}`.trim();
    out.push({ key: `t-${tr.id}`, at: tr.detected_at, kind: "detected", vars: { amount } });
    if (tr.status === "confirmed" && tr.confirmed_at) {
      out.push({ key: `tc-${tr.id}`, at: tr.confirmed_at, kind: "confirmed", vars: { amount } });
    }
    if (tr.status === "orphaned") {
      out.push({ key: `to-${tr.id}`, at: tr.confirmed_at ?? tr.detected_at, kind: "orphaned", vars: { amount } });
    }
  }
  for (const w of inv.webhook_events) {
    out.push({
      key: `w-${w.id}`,
      at: w.created_at,
      kind: w.status === "failed" ? "webhookFailed" : "webhook",
      vars: { event: w.event_type },
    });
  }

  switch (inv.status) {
    case "paid":
      if (inv.paid_at) out.push({ key: "paid", at: inv.paid_at, kind: "paid" });
      break;
    case "expired":
      out.push({ key: "expired", at: inv.expires_at, kind: "expired" });
      break;
    case "underpaid":
      out.push({ key: "underpaid", at: inv.expires_at, kind: "underpaid" });
      break;
    case "cancelled":
      out.push({ key: "cancelled", at: inv.updated_at, kind: "cancelled" });
      break;
  }
  if (OPEN.has(inv.status)) out.push({ key: "expires", at: inv.expires_at, kind: "expiresAt", future: true });

  return out
    .filter((e) => !Number.isNaN(Date.parse(e.at)))
    .sort((a, b) => Date.parse(a.at) - Date.parse(b.at) || order(a.kind) - order(b.kind));
}

const ORDER: TimelineKind[] = [
  "created",
  "quote",
  "quoteSuperseded",
  "detected",
  "confirmed",
  "orphaned",
  "paid",
  "underpaid",
  "expired",
  "cancelled",
  "webhook",
  "webhookFailed",
  "expiresAt",
];
function order(k: TimelineKind): number {
  return ORDER.indexOf(k);
}
