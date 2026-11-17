import type { InvoiceDetail, WebhookEvent } from "@/lib/api/types";

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
  | "webhook";

/**
 * How an entry turned out, so the UI never has to guess from the kind:
 * - `step`     a completed intermediate step (neutral)
 * - `progress` still waiting on something (confirmations, a webhook retry)
 * - `success`  a good outcome (transfer confirmed, invoice paid, webhook delivered)
 * - `danger`   a failure (transfer orphaned, invoice underpaid, webhook gave up)
 * - `ended`    closed without payment (expired, cancelled)
 * - `future`   has not happened yet (the deadline of an open invoice)
 */
export type TimelineTone = "step" | "progress" | "success" | "danger" | "ended" | "future";

/** Short status text shown next to an entry (i18n key under admin.invoice.timeline.notes). */
export type TimelineNote =
  | "awaitingConfirmations"
  | "webhookDelivered"
  | "webhookQueued"
  | "webhookRetrying"
  | "webhookFailed";

export interface TimelineEntry {
  key: string;
  at: string;
  kind: TimelineKind;
  tone: TimelineTone;
  /** Placeholder values for the label (asset, amount, event). */
  vars?: Record<string, string>;
  note?: { key: TimelineNote; vars?: Record<string, string> };
  /** Not happened yet (e.g. the expiry of an open invoice). */
  future?: boolean;
}

const OPEN = new Set(["pending", "partial", "confirming"]);

function webhookEntry(w: WebhookEvent): TimelineEntry {
  const base = { key: `w-${w.id}`, at: w.created_at, kind: "webhook" as const, vars: { event: w.event_type } };
  const attempts = String(w.attempts);
  switch (w.status) {
    case "delivered":
      return { ...base, tone: "success", note: { key: "webhookDelivered" } };
    case "failed":
      return { ...base, tone: "danger", note: { key: "webhookFailed", vars: { attempts } } };
    default:
      return w.attempts > 0
        ? { ...base, tone: "progress", note: { key: "webhookRetrying", vars: { attempts } } }
        : { ...base, tone: "progress", note: { key: "webhookQueued" } };
  }
}

/**
 * Status history reconstructed from the invoice detail: there is no status
 * log in the API, so every timestamp the invoice, its quotes, transfers and
 * webhook events carry becomes one entry, oldest first.
 */
export function buildTimeline(inv: InvoiceDetail): TimelineEntry[] {
  const out: TimelineEntry[] = [{ key: "created", at: inv.created_at, kind: "created", tone: "step" }];

  for (const q of inv.quotes) {
    out.push({
      key: `q-${q.id}`,
      at: q.created_at,
      kind: q.status === "superseded" ? "quoteSuperseded" : "quote",
      tone: "step",
      vars: { asset: `${q.symbol} · ${q.chain_name}` },
    });
  }
  for (const tr of inv.transfers) {
    const amount = `${tr.amount} ${tr.symbol}`.trim();
    const waiting = tr.status === "pending";
    out.push({
      key: `t-${tr.id}`,
      at: tr.detected_at,
      kind: "detected",
      tone: waiting ? "progress" : "step",
      vars: { amount },
      note: waiting
        ? {
            key: "awaitingConfirmations",
            vars: { n: String(tr.confirmations), m: String(tr.required_confirmations) },
          }
        : undefined,
    });
    if (tr.status === "confirmed" && tr.confirmed_at) {
      out.push({ key: `tc-${tr.id}`, at: tr.confirmed_at, kind: "confirmed", tone: "success", vars: { amount } });
    }
    if (tr.status === "orphaned") {
      out.push({
        key: `to-${tr.id}`,
        at: tr.confirmed_at ?? tr.detected_at,
        kind: "orphaned",
        tone: "danger",
        vars: { amount },
      });
    }
  }
  for (const w of inv.webhook_events) out.push(webhookEntry(w));

  switch (inv.status) {
    case "paid":
      if (inv.paid_at) out.push({ key: "paid", at: inv.paid_at, kind: "paid", tone: "success" });
      break;
    case "expired":
      out.push({ key: "expired", at: inv.expires_at, kind: "expired", tone: "ended" });
      break;
    case "underpaid":
      out.push({ key: "underpaid", at: inv.expires_at, kind: "underpaid", tone: "danger" });
      break;
    case "cancelled":
      out.push({ key: "cancelled", at: inv.updated_at, kind: "cancelled", tone: "ended" });
      break;
  }
  if (OPEN.has(inv.status)) {
    out.push({ key: "expires", at: inv.expires_at, kind: "expiresAt", tone: "future", future: true });
  }

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
  "expiresAt",
];
function order(k: TimelineKind): number {
  return ORDER.indexOf(k);
}
