"use client";

import { WebhookIcon } from "lucide-react";
import Link from "next/link";
import { useAllMerchants, useWebhookEvents } from "@/lib/admin/queries";
import { enumParam, useUrlState } from "@/lib/admin/url-state";
import { shortenAddress } from "@/lib/format/address";
import type { WebhookEvent, WebhookEventQuery, WebhookEventStatus } from "@/lib/api/types";
import { useT, type MessageKey } from "@/lib/i18n";
import { DataTable, type Column } from "../data-table";
import { FilterBar, SearchFilter, SelectFilter } from "../filter-bar";
import { PageHeader } from "../page-header";
import { EmptyState } from "../states";
import { WebhookStatusBadge } from "../status-badge";
import { RelativeTime } from "../time";
import { RetryWebhookButton, WebhookEventDetail } from "./webhook-event-detail";

const STATUSES: WebhookEventStatus[] = ["pending", "delivered", "failed"];

export function WebhooksPage() {
  const t = useT();
  const url = useUrlState();
  const merchants = useAllMerchants();
  const status = enumParam(url.params.get("status"), STATUSES);
  const merchantId = url.get("merchant_id");
  const invoiceId = url.get("invoice_id");
  const query: WebhookEventQuery = {
    page: url.page,
    page_size: url.pageSize,
    status,
    merchant_id: merchantId || undefined,
    invoice_id: invoiceId || undefined,
  };
  const list = useWebhookEvents(query);
  const filtered = Boolean(status || merchantId || invoiceId);
  const merchantName = (id: string) => merchants.data?.items.find((m) => m.id === id)?.name ?? shortenAddress(id, 8, 4);

  const columns: Column<WebhookEvent>[] = [
    { id: "event", header: t("admin.webhooks.columns.event"), cell: (w) => <span className="font-mono text-xs">{w.event_type}</span> },
    { id: "merchant", header: t("admin.webhooks.columns.merchant"), cell: (w) => <span className="block max-w-40 truncate">{merchantName(w.merchant_id)}</span> },
    {
      id: "invoice",
      header: t("admin.webhooks.columns.invoice"),
      cell: (w) => (
        <Link href={`/admin/invoices/${w.invoice_id}`} className="font-mono text-[13px] text-primary hover:underline">
          {shortenAddress(w.invoice_id, 8, 4)}
        </Link>
      ),
    },
    { id: "status", header: t("admin.webhooks.columns.status"), cell: (w) => <WebhookStatusBadge status={w.status} /> },
    {
      id: "attempts",
      header: t("admin.webhooks.columns.attempts"),
      align: "right",
      cell: (w) => <span className="font-mono tabular">{w.attempts}</span>,
    },
    {
      id: "code",
      header: t("admin.webhooks.columns.lastCode"),
      cell: (w) => (
        <span className={w.last_status_code >= 400 || (w.status === "failed" && !w.last_status_code) ? "font-mono text-destructive tabular" : "font-mono tabular"}>
          {w.last_status_code || "—"}
        </span>
      ),
    },
    {
      id: "timing",
      header: t("admin.webhooks.columns.timing"),
      cell: (w) =>
        w.delivered_at ? (
          <RelativeTime iso={w.delivered_at} className="text-muted-foreground" />
        ) : w.status !== "delivered" && w.next_attempt_at ? (
          <RelativeTime iso={w.next_attempt_at} />
        ) : (
          "—"
        ),
    },
    { id: "created", header: t("admin.webhooks.columns.created"), cell: (w) => <RelativeTime iso={w.created_at} className="text-muted-foreground" /> },
    { id: "actions", header: <span className="sr-only">{t("admin.common.actions")}</span>, cell: (w) => <RetryWebhookButton event={w} size="xs" /> },
  ];

  return (
    <>
      <PageHeader title={t("admin.webhooks.title")} description={t("admin.webhooks.description")} />
      <FilterBar active={filtered} onClear={() => url.clear(["page_size"])}>
        <SelectFilter
          label={t("admin.webhooks.status")}
          allLabel={t("admin.webhooks.allStatuses")}
          value={status ?? ""}
          onChange={(v) => url.set({ status: v })}
          options={STATUSES.map((s) => ({ value: s, label: t(`admin.status.webhook.${s}` as MessageKey) }))}
          className="sm:w-36"
        />
        <SelectFilter
          label={t("admin.webhooks.merchant")}
          allLabel={t("admin.webhooks.allMerchants")}
          value={merchantId}
          onChange={(v) => url.set({ merchant_id: v })}
          options={(merchants.data?.items ?? []).map((m) => ({ value: m.id, label: m.name }))}
          className="sm:w-52"
        />
        <SearchFilter
          label={t("admin.webhooks.invoiceId")}
          placeholder={t("admin.webhooks.invoiceIdPlaceholder")}
          value={invoiceId}
          onCommit={(v) => url.set({ invoice_id: v })}
          mono
        />
      </FilterBar>
      <DataTable
        caption={t("admin.webhooks.title")}
        columns={columns}
        rows={list.data?.items}
        getRowId={(w) => w.id}
        isLoading={list.isPending}
        isFetching={list.isFetching && list.isPlaceholderData}
        error={list.error}
        onRetry={() => void list.refetch()}
        renderExpanded={(w) => <WebhookEventDetail event={w} />}
        empty={
          filtered ? (
            <EmptyState icon={WebhookIcon} title={t("admin.webhooks.filteredEmptyTitle")} description={t("admin.webhooks.filteredEmptyBody")} />
          ) : (
            <EmptyState
              icon={WebhookIcon}
              title={t("admin.webhooks.emptyTitle")}
              description={t("admin.webhooks.emptyBody")}
              action={
                <Link href="/admin/merchants" className="text-sm font-medium text-primary hover:underline">
                  {t("admin.nav.merchants")}
                </Link>
              }
            />
          )
        }
        mobileCard={(w) => (
          <div className="space-y-1.5">
            <div className="flex items-center justify-between gap-2">
              <span className="font-mono text-xs">{w.event_type}</span>
              <WebhookStatusBadge status={w.status} />
            </div>
            <div className="flex items-center justify-between gap-2 text-xs text-muted-foreground">
              <span className="truncate">
                {merchantName(w.merchant_id)} · {t("admin.webhooks.attempts", { n: w.attempts })}
                {w.last_status_code ? ` · HTTP ${w.last_status_code}` : ""}
              </span>
              <RelativeTime iso={w.created_at} />
            </div>
          </div>
        )}
        pagination={
          list.data && {
            page: list.data.page,
            pageSize: list.data.page_size,
            total: list.data.total,
            onPageChange: (p) => url.set({ page: p > 1 ? p : null }, { resetPage: false }),
            onPageSizeChange: (s) => url.set({ page_size: s === 20 ? null : s }),
          }
        }
      />
    </>
  );
}
