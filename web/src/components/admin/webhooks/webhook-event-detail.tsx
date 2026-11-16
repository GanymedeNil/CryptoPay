"use client";

import { RotateCwIcon } from "lucide-react";
import { adminKeys, useAdminMutation } from "@/lib/admin/queries";
import { adminApi } from "@/lib/api/admin";
import type { WebhookEvent } from "@/lib/api/types";
import { useT } from "@/lib/i18n";
import { GatedButton } from "../auth";
import { JsonViewer, stringifyJson } from "../json-viewer";
import { MonoValue } from "../mono-value";
import { DateTimeStack } from "../time";

/** `POST /webhook-events/{id}/retry` (re-queued for immediate delivery). */
export function RetryWebhookButton({ event, size = "sm" }: { event: WebhookEvent; size?: "sm" | "xs" }) {
  const t = useT();
  const retry = useAdminMutation({
    mutationFn: () => adminApi.retryWebhookEvent(event.id),
    invalidate: [adminKeys.webhooks.all, adminKeys.invoices.detail(event.invoice_id), adminKeys.overview.all],
    successMessage: t("admin.webhooks.retried"),
  });
  if (event.status === "delivered") return null;
  return (
    <GatedButton
      variant="outline"
      size={size}
      disabled={retry.isPending}
      onClick={(e) => {
        e.stopPropagation();
        retry.mutate();
      }}
    >
      <RotateCwIcon data-icon="inline-start" className={retry.isPending ? "animate-spin" : undefined} aria-hidden />
      {retry.isPending ? t("admin.webhooks.retrying") : t("admin.webhooks.retry")}
    </GatedButton>
  );
}

/** Expanded view: target URL, timings, last error / response and the payload. */
export function WebhookEventDetail({ event }: { event: WebhookEvent }) {
  const t = useT();
  return (
    <div className="grid min-w-0 gap-4 lg:grid-cols-2">
      <div className="min-w-0 space-y-3 text-sm">
        <div className="space-y-0.5">
          <p className="text-xs text-muted-foreground">{t("admin.webhooks.url")}</p>
          <MonoValue value={event.url} label={t("admin.webhooks.url")} wrap />
        </div>
        <div className="grid grid-cols-2 gap-3">
          <div className="space-y-0.5">
            <p className="text-xs text-muted-foreground">{t("admin.webhooks.columns.created")}</p>
            <DateTimeStack iso={event.created_at} />
          </div>
          <div className="space-y-0.5">
            <p className="text-xs text-muted-foreground">{t("admin.webhooks.columns.timing")}</p>
            <DateTimeStack iso={event.delivered_at ?? (event.status === "pending" || event.status === "failed" ? event.next_attempt_at : null)} />
          </div>
        </div>
        {event.last_error && (
          <div className="space-y-1">
            <p className="text-xs text-muted-foreground">{t("admin.webhooks.lastError")}</p>
            <p className="rounded-md border border-destructive/30 bg-destructive/5 px-2.5 py-1.5 font-mono text-xs break-all text-destructive">
              {event.last_error}
            </p>
          </div>
        )}
        <div className="space-y-1">
          <p className="text-xs text-muted-foreground">
            {t("admin.webhooks.lastResponse")}
            {event.last_status_code > 0 && <span className="ml-1.5 font-mono">HTTP {event.last_status_code}</span>}
          </p>
          {event.last_response ? (
            <pre className="max-h-40 overflow-auto rounded-md border bg-muted/40 px-2.5 py-1.5 font-mono text-xs break-all whitespace-pre-wrap">
              {stringifyJson(event.last_response)}
            </pre>
          ) : (
            <p className="text-xs text-muted-foreground">{t("admin.webhooks.noResponse")}</p>
          )}
        </div>
        <RetryWebhookButton event={event} />
      </div>
      <div className="min-w-0 space-y-1">
        <p className="text-xs text-muted-foreground">{t("admin.webhooks.payload")}</p>
        <JsonViewer value={event.payload} label={t("admin.webhooks.payload")} maxHeight="22rem" />
      </div>
    </div>
  );
}
