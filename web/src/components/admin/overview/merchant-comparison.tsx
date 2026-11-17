"use client";

import { ArrowDownIcon, ArrowUpDownIcon, ArrowUpIcon, StoreIcon } from "lucide-react";
import Link from "next/link";
import { useId } from "react";
import { Button } from "@/components/ui/button";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import {
  defaultOrder,
  failedWebhooksHref,
  isIdle,
  MERCHANT_SORTS,
  nextSort,
  overviewHref,
  sortFromParams,
  sortPatch,
  type MerchantSortState,
} from "@/lib/admin/merchant-overview";
import { useMerchantOverviews } from "@/lib/admin/queries";
import { useUrlState } from "@/lib/admin/url-state";
import { formatCount, formatPercent } from "@/lib/format/units";
import type { MerchantOverview, MerchantOverviewQuery, MerchantOverviewSort } from "@/lib/api/types";
import { useLocale, useT, type MessageKey } from "@/lib/i18n";
import { cn } from "@/lib/utils";
import { formatUsd } from "../amount";
import { DataTable, type Column } from "../data-table";
import { EmptyState } from "../states";
import { MerchantStatusBadge, StatusBadge } from "../status-badge";
import { RelativeTime } from "../time";

const COLUMN_LABEL: Record<MerchantOverviewSort, MessageKey> = {
  name: "admin.overview.byMerchant.columns.merchant",
  invoices: "admin.overview.byMerchant.columns.invoices",
  paid_invoices: "admin.overview.byMerchant.columns.paid",
  volume_usd: "admin.overview.byMerchant.columns.volume",
  conversion_rate: "admin.overview.byMerchant.columns.conversion",
  open_invoices: "admin.overview.byMerchant.columns.open",
  failed_webhooks: "admin.overview.byMerchant.columns.failedWebhooks",
};

/**
 * "By merchant": one row per merchant over the overview's range, sorted and
 * paged by the server (`sort`, `order`, `page`, `page_size` in the URL).
 * Merchants without activity in the range are muted; the name opens the
 * overview filtered to that merchant.
 */
export function MerchantComparison({ days }: { days: number }) {
  const t = useT();
  const { locale } = useLocale();
  const headingId = useId();
  const url = useUrlState();
  const sort = sortFromParams(url.params.get("sort"), url.params.get("order"));
  const query: MerchantOverviewQuery = { days, sort: sort.sort, order: sort.order, page: url.page, page_size: url.pageSize };
  const list = useMerchantOverviews(query);
  const setSort = (s: MerchantSortState) => url.set(sortPatch(s));
  const onSort = (column: MerchantOverviewSort) => setSort(nextSort(sort, column));

  const header = (column: MerchantOverviewSort, align: "left" | "right" = "right") => {
    const active = sort.sort === column;
    const Icon = !active ? ArrowUpDownIcon : sort.order === "asc" ? ArrowUpIcon : ArrowDownIcon;
    const label = t(COLUMN_LABEL[column]);
    return (
      <button
        type="button"
        onClick={() => onSort(column)}
        title={t("admin.overview.byMerchant.sortBy", { column: label })}
        className={cn(
          "-mx-1 inline-flex items-center gap-1 rounded-sm px-1 py-0.5 outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50",
          align === "right" && "flex-row-reverse",
          active && "text-foreground",
        )}
      >
        {label}
        <Icon className={cn("size-3.5", !active && "opacity-50")} aria-hidden />
      </button>
    );
  };
  const ariaSort = (column: MerchantOverviewSort) =>
    sort.sort === column ? (sort.order === "asc" ? "ascending" : "descending") : "none";
  const count = (n: number) => <span className="font-mono tabular">{formatCount(n, locale)}</span>;

  const columns: Column<MerchantOverview>[] = [
    {
      id: "name",
      header: header("name", "left"),
      ariaSort: ariaSort("name"),
      className: "max-w-64",
      cell: (r) => <MerchantName row={r} days={days} />,
    },
    { id: "status", header: t("admin.overview.byMerchant.columns.status"), cell: (r) => <MerchantStatusBadge status={r.status} /> },
    { id: "invoices", header: header("invoices"), ariaSort: ariaSort("invoices"), align: "right", cell: (r) => count(r.invoices) },
    { id: "paid", header: header("paid_invoices"), ariaSort: ariaSort("paid_invoices"), align: "right", cell: (r) => count(r.paid_invoices) },
    {
      id: "volume",
      header: header("volume_usd"),
      ariaSort: ariaSort("volume_usd"),
      align: "right",
      cell: (r) => <span className="font-mono font-medium tabular whitespace-nowrap">{formatUsd(r.volume_usd, locale)}</span>,
    },
    {
      id: "conversion",
      header: header("conversion_rate"),
      ariaSort: ariaSort("conversion_rate"),
      align: "right",
      cell: (r) => <span className="font-mono tabular">{r.invoices > 0 ? formatPercent(r.conversion_rate, locale) : t("admin.common.empty")}</span>,
    },
    { id: "open", header: header("open_invoices"), ariaSort: ariaSort("open_invoices"), align: "right", cell: (r) => count(r.open_invoices) },
    {
      id: "failed",
      header: header("failed_webhooks"),
      ariaSort: ariaSort("failed_webhooks"),
      align: "right",
      cell: (r) => <FailedWebhooks row={r} />,
    },
    {
      id: "lastPaid",
      header: t("admin.overview.byMerchant.columns.lastPaid"),
      cell: (r) => <RelativeTime iso={r.last_paid_at} fallback={t("admin.common.never")} className="text-muted-foreground" />,
    },
  ];

  return (
    <section aria-labelledby={headingId} className="space-y-3">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div className="min-w-0 space-y-0.5">
          <h2 id={headingId} className="font-heading text-[15px] font-semibold">
            {t("admin.overview.byMerchant.title")}
          </h2>
          <p className="text-xs text-pretty text-muted-foreground">{t("admin.overview.byMerchant.description")}</p>
        </div>
        <PhoneSort sort={sort} onChange={setSort} />
      </div>
      <DataTable
        caption={t("admin.overview.byMerchant.title")}
        columns={columns}
        rows={list.data?.items}
        getRowId={(r) => r.merchant_id}
        isLoading={list.isPending}
        isFetching={list.isFetching && list.isPlaceholderData}
        error={list.error}
        onRetry={() => void list.refetch()}
        rowClassName={(r) => (isIdle(r) ? "text-muted-foreground opacity-70" : undefined)}
        empty={
          <EmptyState
            icon={StoreIcon}
            title={t("admin.overview.byMerchant.emptyTitle")}
            description={t("admin.overview.byMerchant.emptyBody")}
            action={
              <Button asChild variant="outline">
                <Link href="/admin/merchants">{t("admin.overview.byMerchant.manageMerchants")}</Link>
              </Button>
            }
          />
        }
        mobileCard={(r) => (
          <div className="space-y-1.5">
            <div className="flex items-start justify-between gap-2">
              <MerchantName row={r} days={days} />
              <MerchantStatusBadge status={r.status} />
            </div>
            <div className="flex items-baseline justify-between gap-2">
              <span className="font-mono text-sm font-medium tabular">{formatUsd(r.volume_usd, locale)}</span>
              <span className="text-xs text-muted-foreground tabular">
                {t("admin.overview.byMerchant.columns.invoices")} {formatCount(r.invoices, locale)} ·{" "}
                {t("admin.overview.byMerchant.columns.paid")} {formatCount(r.paid_invoices, locale)}
                {r.invoices > 0 && ` · ${formatPercent(r.conversion_rate, locale)}`}
              </span>
            </div>
            <div className="flex items-center justify-between gap-2 text-xs text-muted-foreground">
              <span className="flex items-center gap-2 tabular">
                {t("admin.overview.byMerchant.columns.open")} {formatCount(r.open_invoices, locale)}
                {r.failed_webhooks > 0 && <FailedWebhooks row={r} withLabel />}
              </span>
              <RelativeTime iso={r.last_paid_at} fallback={t("admin.common.never")} />
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
    </section>
  );
}

function MerchantName({ row: r, days }: { row: MerchantOverview; days: number }) {
  const t = useT();
  return (
    <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5">
      <Link href={overviewHref(r.merchant_id, days)} className="truncate font-medium text-primary hover:underline">
        {r.merchant_name}
      </Link>
      {isIdle(r) && (
        <StatusBadge tone="muted" dot={false}>
          {t("admin.overview.byMerchant.idle")}
        </StatusBadge>
      )}
    </div>
  );
}

function FailedWebhooks({ row: r, withLabel = false }: { row: MerchantOverview; withLabel?: boolean }) {
  const t = useT();
  const { locale } = useLocale();
  if (r.failed_webhooks === 0) return <span className="font-mono text-muted-foreground tabular">0</span>;
  return (
    <Link
      href={failedWebhooksHref(r.merchant_id)}
      aria-label={t("admin.overview.byMerchant.viewFailed", { name: r.merchant_name, count: formatCount(r.failed_webhooks, locale) })}
      className="inline-flex items-center gap-1 rounded-md border border-warning-border bg-warning-surface px-1.5 font-mono font-medium text-warning tabular hover:underline"
    >
      {withLabel && <span className="font-sans">{t("admin.overview.byMerchant.columns.failedWebhooks")}</span>}
      {formatCount(r.failed_webhooks, locale)}
    </Link>
  );
}

/** Column headers are hidden in the phone card layout: sort with a select there. */
function PhoneSort({ sort, onChange }: { sort: MerchantSortState; onChange: (s: MerchantSortState) => void }) {
  const t = useT();
  const id = useId();
  return (
    <div className="flex items-center gap-1.5 md:hidden">
      <label htmlFor={id} className="sr-only">
        {t("admin.overview.byMerchant.sort")}
      </label>
      <Select value={sort.sort} onValueChange={(v) => onChange({ sort: v as MerchantOverviewSort, order: defaultOrder(v as MerchantOverviewSort) })}>
        <SelectTrigger id={id} size="sm" className="w-40 bg-card">
          <SelectValue />
        </SelectTrigger>
        <SelectContent position="popper" align="end">
          {MERCHANT_SORTS.map((s) => (
            <SelectItem key={s} value={s}>
              {t(COLUMN_LABEL[s])}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <Button
        variant="outline"
        size="icon-sm"
        aria-label={t("admin.overview.byMerchant.ascending")}
        aria-pressed={sort.order === "asc"}
        onClick={() => onChange(nextSort(sort, sort.sort))}
      >
        {sort.order === "asc" ? <ArrowUpIcon aria-hidden /> : <ArrowDownIcon aria-hidden />}
      </Button>
    </div>
  );
}
