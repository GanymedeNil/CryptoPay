"use client";

import { PlusIcon, ReceiptTextIcon } from "lucide-react";
import Link from "next/link";
import { useState } from "react";
import { useAllMerchants, useInvoices } from "@/lib/admin/queries";
import { enumParam, useUrlState } from "@/lib/admin/url-state";
import { dateInputToIso } from "@/lib/format/relative";
import { shortenAddress } from "@/lib/format/address";
import type { AdminInvoice, AdminInvoiceQuery, InvoiceStatus } from "@/lib/api/types";
import { useT, type MessageKey } from "@/lib/i18n";
import { CryptoAmount, Money } from "../amount";
import { GatedButton } from "../auth";
import { DataTable, type Column } from "../data-table";
import { DateRangeFilter, FilterBar, SearchFilter, SelectFilter } from "../filter-bar";
import { PageHeader } from "../page-header";
import { EmptyState } from "../states";
import { InvoiceStatusBadge, StatusBadge } from "../status-badge";
import { RelativeTime } from "../time";
import { CreateInvoiceDialog } from "./create-invoice-dialog";

export const INVOICE_STATUSES: InvoiceStatus[] = ["pending", "partial", "confirming", "paid", "underpaid", "expired", "cancelled"];

export function InvoicesPage() {
  const t = useT();
  const url = useUrlState();
  const [createOpen, setCreateOpen] = useState(false);
  const status = enumParam(url.params.get("status"), INVOICE_STATUSES);
  const merchantId = url.get("merchant_id");
  const q = url.get("q");
  const from = url.get("from");
  const to = url.get("to");
  const query: AdminInvoiceQuery = {
    page: url.page,
    page_size: url.pageSize,
    status,
    merchant_id: merchantId || undefined,
    q: q || undefined,
    from: dateInputToIso(from, "start"),
    to: dateInputToIso(to, "end"),
  };
  const list = useInvoices(query);
  const merchants = useAllMerchants();
  const filtered = Boolean(status || merchantId || q || from || to);

  const columns: Column<AdminInvoice>[] = [
    {
      id: "invoice",
      header: t("admin.invoices.columns.invoice"),
      className: "max-w-72",
      cell: (inv) => (
        <div className="min-w-0">
          <Link href={`/admin/invoices/${inv.id}`} className="font-mono text-[13px] font-medium text-primary hover:underline">
            {shortenAddress(inv.id, 8, 4)}
          </Link>
          <div className="truncate text-xs text-muted-foreground">
            {inv.title || t("admin.invoice.untitled")}
            {inv.order_id && <span className="font-mono"> · {inv.order_id}</span>}
          </div>
        </div>
      ),
    },
    {
      id: "merchant",
      header: t("admin.invoices.columns.merchant"),
      cell: (inv) => <span className="block max-w-40 truncate">{inv.merchant_name}</span>,
    },
    {
      id: "amount",
      header: t("admin.invoices.columns.amount"),
      align: "right",
      cell: (inv) => (
        <div>
          <Money amount={inv.amount} currency={inv.currency} />
          {inv.amount_usd && inv.currency !== "USD" && (
            <div className="text-xs text-muted-foreground">
              ≈ <Money amount={inv.amount_usd} currency="USD" />
            </div>
          )}
        </div>
      ),
    },
    {
      id: "asset",
      header: t("admin.invoices.columns.asset"),
      cell: (inv) =>
        inv.quote ? (
          <div>
            <CryptoAmount amount={inv.quote.amount} symbol={inv.quote.symbol} />
            <div className="text-xs text-muted-foreground">{inv.quote.chain_name}</div>
          </div>
        ) : (
          <span className="text-muted-foreground">{t("admin.invoices.noAsset")}</span>
        ),
    },
    {
      id: "status",
      header: t("admin.invoices.columns.status"),
      cell: (inv) => (
        <div className="flex flex-wrap items-center gap-1">
          <InvoiceStatusBadge status={inv.status} withHint />
          {inv.overpaid && <StatusBadge tone="warning" dot={false}>{t("admin.status.overpaid")}</StatusBadge>}
          {inv.paid_late && <StatusBadge tone="warning" dot={false}>{t("admin.status.paidLate")}</StatusBadge>}
        </div>
      ),
    },
    {
      id: "created",
      header: t("admin.invoices.columns.created"),
      cell: (inv) => <RelativeTime iso={inv.created_at} className="text-muted-foreground" />,
    },
  ];

  return (
    <>
      <PageHeader
        title={t("admin.invoices.title")}
        description={t("admin.invoices.description")}
        actions={
          <GatedButton onClick={() => setCreateOpen(true)}>
            <PlusIcon data-icon="inline-start" aria-hidden />
            {t("admin.invoices.create")}
          </GatedButton>
        }
      />
      <FilterBar active={filtered} onClear={() => url.clear(["page_size"])}>
        <SearchFilter
          label={t("admin.invoices.searchLabel")}
          placeholder={t("admin.invoices.searchPlaceholder")}
          value={q}
          onCommit={(v) => url.set({ q: v })}
          className="sm:w-80"
          mono
        />
        <SelectFilter
          label={t("admin.invoices.status")}
          allLabel={t("admin.invoices.allStatuses")}
          value={status ?? ""}
          onChange={(v) => url.set({ status: v })}
          options={INVOICE_STATUSES.map((s) => ({ value: s, label: t(`admin.status.invoice.${s}` as MessageKey) }))}
        />
        <SelectFilter
          label={t("admin.invoices.merchant")}
          allLabel={t("admin.invoices.allMerchants")}
          value={merchantId}
          onChange={(v) => url.set({ merchant_id: v })}
          options={(merchants.data?.items ?? []).map((m) => ({ value: m.id, label: m.name }))}
          className="sm:w-52"
        />
        <DateRangeFilter
          from={from}
          to={to}
          fromLabel={t("admin.invoices.dateFrom")}
          toLabel={t("admin.invoices.dateTo")}
          onChange={(r) => url.set({ from: r.from, to: r.to })}
        />
      </FilterBar>
      <DataTable
        caption={t("admin.invoices.title")}
        columns={columns}
        rows={list.data?.items}
        getRowId={(inv) => inv.id}
        isLoading={list.isPending}
        isFetching={list.isFetching && list.isPlaceholderData}
        error={list.error}
        onRetry={() => void list.refetch()}
        rowHref={(inv) => `/admin/invoices/${inv.id}`}
        empty={
          filtered ? (
            <EmptyState icon={ReceiptTextIcon} title={t("admin.invoices.filteredEmptyTitle")} description={t("admin.invoices.filteredEmptyBody")} />
          ) : (
            <EmptyState
              icon={ReceiptTextIcon}
              title={t("admin.invoices.emptyTitle")}
              description={t("admin.invoices.emptyBody")}
              action={
                <GatedButton variant="outline" onClick={() => setCreateOpen(true)}>
                  <PlusIcon data-icon="inline-start" aria-hidden />
                  {t("admin.invoices.create")}
                </GatedButton>
              }
            />
          )
        }
        mobileCard={(inv) => (
          <div className="space-y-1.5">
            <div className="flex items-center justify-between gap-2">
              <Link href={`/admin/invoices/${inv.id}`} className="font-mono text-[13px] font-medium text-primary">
                {shortenAddress(inv.id, 8, 4)}
              </Link>
              <InvoiceStatusBadge status={inv.status} />
            </div>
            <div className="flex items-baseline justify-between gap-2">
              <span className="truncate text-sm">{inv.title || t("admin.invoice.untitled")}</span>
              <Money amount={inv.amount} currency={inv.currency} className="text-sm font-medium" />
            </div>
            <div className="flex items-center justify-between gap-2 text-xs text-muted-foreground">
              <span className="truncate">
                {inv.merchant_name}
                {inv.quote && ` · ${inv.quote.symbol} ${inv.quote.chain_name}`}
              </span>
              <RelativeTime iso={inv.created_at} />
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
      <CreateInvoiceDialog open={createOpen} onOpenChange={setCreateOpen} />
    </>
  );
}
