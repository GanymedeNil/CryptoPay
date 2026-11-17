"use client";

import { ArrowRightIcon, CircleCheckIcon, InfoIcon, StoreIcon, TriangleAlertIcon, WebhookIcon, XIcon } from "lucide-react";
import Link from "next/link";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { failedWebhooksHref, invoicesHref, MERCHANT_PARAM, rangeParam } from "@/lib/admin/merchant-overview";
import { useAllMerchants, useMerchant, useOverview } from "@/lib/admin/queries";
import { useUrlState } from "@/lib/admin/url-state";
import { shortenAddress } from "@/lib/format/address";
import { compareDecimal, isDecimalString } from "@/lib/format/amount";
import { formatCount, formatPercent, plotValue } from "@/lib/format/units";
import type { InvoiceStatus, Merchant, Overview } from "@/lib/api/types";
import { useLocale, useT, type MessageKey } from "@/lib/i18n";
import { cn } from "@/lib/utils";
import { formatUsd } from "../amount";
import { PageHeader, Section } from "../page-header";
import { ErrorState } from "../states";
import { MerchantStatusBadge } from "../status-badge";
import { ChainHealthStrip } from "./chain-health";
import { KpiGrid } from "./kpi-grid";
import { MerchantComparison } from "./merchant-comparison";
import { MerchantPicker } from "./merchant-picker";
import { RangeToggle } from "./range-toggle";
import { VolumeChart } from "./volume-chart";

/**
 * The dashboard. `?days=` picks the range and `?merchant=<id>` restricts
 * every figure to one merchant (chain monitoring stays system-wide); without
 * a merchant the "by merchant" comparison is shown.
 */
export function OverviewPage() {
  const t = useT();
  const url = useUrlState();
  const days = rangeParam(url.params.get("days"));
  const merchantId = url.get(MERCHANT_PARAM) || null;
  const q = useOverview(days, merchantId);
  const data = q.data;
  const merchants = useAllMerchants();
  const listed = merchantId ? merchants.data?.items.find((m) => m.id === merchantId) : undefined;
  // Only for a merchant beyond the first page of the merchant list.
  const detail = useMerchant(merchantId ?? "", Boolean(merchantId) && merchants.isSuccess && !listed);
  const merchant = listed ?? detail.data?.merchant;
  const setMerchant = (id: string) => url.set({ [MERCHANT_PARAM]: id || null });

  return (
    <>
      <PageHeader
        title={t("admin.overview.title")}
        description={t("admin.overview.description")}
        actions={
          <>
            <MerchantPicker value={merchantId ?? ""} onChange={setMerchant} merchants={merchants.data?.items} label={merchant?.name} />
            <RangeToggle value={days} onChange={(d) => url.set({ days: d === 30 ? null : d })} />
          </>
        }
      />

      {merchantId && <MerchantChip id={merchantId} merchant={merchant} onClear={() => setMerchant("")} />}

      {q.error && !data ? (
        <ErrorState error={q.error} onRetry={() => void q.refetch()} />
      ) : (
        <div className={cn("space-y-4 transition-opacity", q.isPlaceholderData && "opacity-60")} aria-busy={q.isFetching}>
          <Kpis data={data} merchantId={merchantId} />
          <div className="grid gap-4 xl:grid-cols-3">
            <Section
              title={t("admin.overview.chart.title")}
              description={t("admin.overview.chart.description")}
              className="xl:col-span-2"
              bodyClassName="px-2 pt-4 pb-2 sm:px-4"
            >
              {data ? <VolumeChart daily={data.daily} /> : <Skeleton className="h-64 w-full" />}
            </Section>
            <Attention data={data} merchantId={merchantId} />
          </div>
          {!merchantId && <ChainHealthStrip />}
          <div className="grid gap-4 lg:grid-cols-2">
            <ByAsset data={data} />
            <ByStatus data={data} merchantId={merchantId} />
          </div>
          {merchantId ? (
            <div className="pt-2">
              <ChainHealthStrip systemWide />
            </div>
          ) : (
            <MerchantComparison days={days} />
          )}
        </div>
      )}
    </>
  );
}

/** Which merchant is in view, with a one-click way back to every merchant. */
function MerchantChip({ id, merchant, onClear }: { id: string; merchant: Merchant | undefined; onClear: () => void }) {
  const t = useT();
  return (
    <div
      className="mb-4 flex flex-wrap items-center gap-x-2 gap-y-1 rounded-xl border border-primary/30 bg-primary/5 py-1.5 pr-1.5 pl-3 text-sm"
      aria-live="polite"
    >
      <StoreIcon className="size-4 text-primary" aria-hidden />
      <span className="text-muted-foreground">{t("admin.overview.merchantFilter.viewing")}</span>
      <Link href={`/admin/merchants/${id}`} className="min-w-0 truncate font-medium hover:underline" title={t("admin.overview.merchantFilter.details")}>
        {merchant?.name ?? shortenAddress(id, 8, 4)}
      </Link>
      {merchant && <MerchantStatusBadge status={merchant.status} />}
      <Button variant="ghost" size="sm" className="ml-auto text-muted-foreground" onClick={onClear}>
        <XIcon data-icon="inline-start" aria-hidden />
        {t("admin.overview.merchantFilter.clear")}
      </Button>
    </div>
  );
}

function Kpis({ data, merchantId }: { data: Overview | undefined; merchantId: string | null }) {
  const t = useT();
  const { locale } = useLocale();
  const tot = data?.totals;
  return (
    <KpiGrid
      featureFirst
      className="xl:grid-cols-5"
      cards={[
        { label: t("admin.overview.kpi.volume"), value: tot && formatUsd(tot.volume_usd, locale), hint: t("admin.overview.kpi.volumeHint") },
        { label: t("admin.overview.kpi.invoices"), value: tot && formatCount(tot.invoices, locale), href: invoicesHref(merchantId) },
        { label: t("admin.overview.kpi.paid"), value: tot && formatCount(tot.paid_invoices, locale), href: invoicesHref(merchantId, "paid") },
        {
          label: t("admin.overview.kpi.conversion"),
          value: tot && formatPercent(tot.conversion_rate, locale),
          hint: t("admin.overview.kpi.conversionHint"),
        },
        { label: t("admin.overview.kpi.open"), value: tot && formatCount(tot.open_invoices, locale), hint: t("admin.overview.kpi.openHint") },
      ]}
    />
  );
}

function Attention({ data, merchantId }: { data: Overview | undefined; merchantId: string | null }) {
  const t = useT();
  const { locale } = useLocale();
  const tot = data?.totals;
  const items = [
    // Unmatched transfers belong to no merchant: not shown for one merchant.
    ...(merchantId
      ? []
      : [
          {
            count: tot?.unmatched_transfers,
            label: t("admin.overview.attention.unmatched"),
            hint: t("admin.overview.attention.unmatchedHint"),
            href: "/admin/transfers?matched=false",
            icon: TriangleAlertIcon,
          },
        ]),
    {
      count: tot?.failed_webhooks,
      label: t("admin.overview.attention.failedWebhooks"),
      hint: t("admin.overview.attention.failedHint"),
      href: failedWebhooksHref(merchantId),
      icon: WebhookIcon,
    },
  ];
  const allClear = tot && tot.failed_webhooks === 0 && (merchantId || tot.unmatched_transfers === 0);
  return (
    <Section title={t("admin.overview.attention.title")} bodyClassName="p-2">
      {!tot ? (
        <div className="space-y-2 p-2">
          <Skeleton className="h-16 w-full" />
          <Skeleton className="h-16 w-full" />
        </div>
      ) : (
        <ul className="space-y-1">
          {items.map((it) => {
            const hot = (it.count ?? 0) > 0;
            return (
              <li key={it.href}>
                <Link
                  href={it.href}
                  className={cn(
                    "group flex items-start gap-3 rounded-lg p-3 outline-none transition-colors hover:bg-muted/60 focus-visible:ring-2 focus-visible:ring-ring/50",
                    hot && "bg-warning-surface/60 hover:bg-warning-surface",
                  )}
                >
                  <span
                    className={cn(
                      "mt-0.5 flex size-8 shrink-0 items-center justify-center rounded-md",
                      hot ? "bg-warning-surface text-warning ring-1 ring-warning-border" : "bg-muted text-muted-foreground",
                    )}
                  >
                    <it.icon className="size-4" aria-hidden />
                  </span>
                  <span className="min-w-0 flex-1">
                    <span className="flex items-baseline justify-between gap-2">
                      <span className="text-sm font-medium">{it.label}</span>
                      <span className={cn("font-heading text-xl font-semibold tabular", hot ? "text-warning" : "text-muted-foreground")}>
                        {formatCount(it.count ?? 0, locale)}
                      </span>
                    </span>
                    <span className="mt-0.5 block text-xs text-pretty text-muted-foreground">{it.hint}</span>
                    {hot && (
                      <span className="mt-1.5 inline-flex items-center gap-1 text-xs font-medium text-primary">
                        {t("admin.overview.attention.review")}
                        <ArrowRightIcon className="size-3 transition-transform group-hover:translate-x-0.5" aria-hidden />
                      </span>
                    )}
                  </span>
                </Link>
              </li>
            );
          })}
          {allClear && (
            <li className="flex items-center gap-2 px-3 py-2 text-xs text-success">
              <CircleCheckIcon className="size-3.5" aria-hidden />
              {t("admin.overview.attention.allClear")}
            </li>
          )}
          {merchantId && (
            <li className="flex items-start gap-2 px-3 py-2 text-xs text-pretty text-muted-foreground">
              <InfoIcon className="mt-px size-3.5 shrink-0" aria-hidden />
              {t("admin.overview.merchantFilter.unmatchedHidden")}
            </li>
          )}
        </ul>
      )}
    </Section>
  );
}

function ByAsset({ data }: { data: Overview | undefined }) {
  const t = useT();
  const { locale } = useLocale();
  const rows = data
    ? [...data.by_asset].sort((a, b) =>
        isDecimalString(a.volume_usd) && isDecimalString(b.volume_usd) ? compareDecimal(b.volume_usd, a.volume_usd) : 0,
      )
    : undefined;
  // Bar widths only: plot numbers, never displayed.
  const max = rows ? Math.max(0, ...rows.map((r) => plotValue(r.volume_usd))) : 0;
  return (
    <Section title={t("admin.overview.byAsset.title")} bodyClassName="p-0">
      {!rows ? (
        <div className="space-y-2 p-4">
          {Array.from({ length: 4 }, (_, i) => (
            <Skeleton key={i} className="h-8 w-full" />
          ))}
        </div>
      ) : rows.length === 0 ? (
        <p className="p-4 text-sm text-muted-foreground">{t("admin.overview.byAsset.empty")}</p>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-[13px]">
            <thead>
              <tr className="border-b text-xs text-muted-foreground">
                <th scope="col" className="px-4 py-2 text-left font-medium">{t("admin.overview.byAsset.asset")}</th>
                <th scope="col" className="px-2 py-2 text-right font-medium">{t("admin.overview.byAsset.paid")}</th>
                <th scope="col" className="px-4 py-2 text-right font-medium">{t("admin.overview.byAsset.volume")}</th>
                <th scope="col" className="hidden w-32 px-4 py-2 font-medium sm:table-cell">
                  <span className="sr-only">{t("admin.overview.byAsset.share")}</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <tr key={r.asset_id} className="border-b last:border-0">
                  <td className="px-4 py-2">
                    <span className="font-medium">{r.symbol}</span>
                    <span className="ml-1.5 text-muted-foreground">{r.chain_name}</span>
                  </td>
                  <td className="px-2 py-2 text-right font-mono tabular">{formatCount(r.paid, locale)}</td>
                  <td className="px-4 py-2 text-right font-mono tabular">{formatUsd(r.volume_usd, locale)}</td>
                  <td className="hidden px-4 py-2 sm:table-cell" aria-hidden>
                    <div className="h-1.5 rounded-full bg-muted">
                      <div
                        className="h-full rounded-full bg-[var(--chart-2)]"
                        style={{ width: `${max > 0 ? Math.max(2, (plotValue(r.volume_usd) / max) * 100) : 0}%` }}
                      />
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Section>
  );
}

const STATUS_ORDER: InvoiceStatus[] = ["paid", "confirming", "partial", "pending", "underpaid", "expired", "cancelled"];
const STATUS_COLOR: Record<InvoiceStatus, string> = {
  paid: "var(--success)",
  confirming: "var(--chart-5)",
  partial: "var(--chart-4)",
  pending: "var(--chart-3)",
  underpaid: "var(--destructive)",
  expired: "color-mix(in oklch, var(--muted-foreground) 45%, transparent)",
  cancelled: "color-mix(in oklch, var(--muted-foreground) 25%, transparent)",
};

function ByStatus({ data, merchantId }: { data: Overview | undefined; merchantId: string | null }) {
  const t = useT();
  const { locale } = useLocale();
  const counts = new Map(data?.by_status.map((s) => [s.status, s.count]));
  const total = data ? data.by_status.reduce((n, s) => n + s.count, 0) : 0;
  const rows = STATUS_ORDER.map((s) => ({ status: s, count: counts.get(s) ?? 0 }));
  return (
    <Section title={t("admin.overview.byStatus.title")}>
      {!data ? (
        <Skeleton className="h-32 w-full" />
      ) : total === 0 ? (
        <p className="text-sm text-muted-foreground">{t("admin.overview.byStatus.empty")}</p>
      ) : (
        <div className="space-y-4">
          <div className="flex h-3 overflow-hidden rounded-full bg-muted" aria-hidden>
            {rows
              .filter((r) => r.count > 0)
              .map((r) => (
                <div key={r.status} style={{ width: `${(r.count / total) * 100}%`, background: STATUS_COLOR[r.status] }} />
              ))}
          </div>
          <ul className="grid grid-cols-1 gap-x-6 gap-y-1 min-[420px]:grid-cols-2">
            {rows.map((r) => {
              const label = t(`admin.status.invoice.${r.status}` as MessageKey);
              return (
                <li key={r.status}>
                  <Link
                    href={invoicesHref(merchantId, r.status)}
                    className="flex items-center gap-2 rounded-md px-1.5 py-1 text-sm outline-none hover:bg-muted/60 focus-visible:ring-2 focus-visible:ring-ring/50"
                    aria-label={`${t("admin.overview.byStatus.viewList", { status: label })}: ${r.count}`}
                  >
                    <span className="size-2.5 shrink-0 rounded-[3px]" style={{ background: STATUS_COLOR[r.status] }} aria-hidden />
                    <span className="flex-1">{label}</span>
                    <span className="font-mono tabular">{formatCount(r.count, locale)}</span>
                    <span className="w-12 text-right text-xs text-muted-foreground tabular">{formatPercent(r.count / total, locale, 0)}</span>
                  </Link>
                </li>
              );
            })}
          </ul>
        </div>
      )}
    </Section>
  );
}
