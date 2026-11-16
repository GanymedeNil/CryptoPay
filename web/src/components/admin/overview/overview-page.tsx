"use client";

import { ArrowRightIcon, CircleCheckIcon, InfoIcon, TriangleAlertIcon, WebhookIcon } from "lucide-react";
import Link from "next/link";
import { Skeleton } from "@/components/ui/skeleton";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { useOverview } from "@/lib/admin/queries";
import { useUrlState } from "@/lib/admin/url-state";
import { compareDecimal, isDecimalString } from "@/lib/format/amount";
import { formatCount, formatPercent, plotValue } from "@/lib/format/units";
import type { InvoiceStatus, Overview } from "@/lib/api/types";
import { useLocale, useT, type MessageKey } from "@/lib/i18n";
import { cn } from "@/lib/utils";
import { formatUsd } from "../amount";
import { PageHeader, Section } from "../page-header";
import { ErrorState } from "../states";
import { ChainHealthStrip } from "./chain-health";
import { VolumeChart } from "./volume-chart";

export const RANGES = [7, 30, 90] as const;

export function OverviewPage() {
  const t = useT();
  const url = useUrlState();
  const raw = Number(url.get("days"));
  const days = (RANGES as readonly number[]).includes(raw) ? raw : 30;
  const q = useOverview(days);
  const data = q.data;

  return (
    <>
      <PageHeader
        title={t("admin.overview.title")}
        description={t("admin.overview.description")}
        actions={
          <ToggleGroup
            type="single"
            variant="outline"
            size="sm"
            spacing={0}
            value={String(days)}
            onValueChange={(v) => v && url.set({ days: v === "30" ? null : v })}
            aria-label={t("admin.overview.range")}
          >
            {RANGES.map((d) => (
              <ToggleGroupItem key={d} value={String(d)} className="px-3 tabular" aria-label={t("admin.overview.lastDays", { days: d })}>
                {t("admin.overview.days", { days: d })}
              </ToggleGroupItem>
            ))}
          </ToggleGroup>
        }
      />

      {q.error && !data ? (
        <ErrorState error={q.error} onRetry={() => void q.refetch()} />
      ) : (
        <div className={cn("space-y-4 transition-opacity", q.isPlaceholderData && "opacity-60")} aria-busy={q.isFetching}>
          <Kpis data={data} />
          <div className="grid gap-4 xl:grid-cols-3">
            <Section
              title={t("admin.overview.chart.title")}
              description={t("admin.overview.chart.description")}
              className="xl:col-span-2"
              bodyClassName="px-2 pt-4 pb-2 sm:px-4"
            >
              {data ? <VolumeChart daily={data.daily} /> : <Skeleton className="h-64 w-full" />}
            </Section>
            <Attention data={data} />
          </div>
          <ChainHealthStrip />
          <div className="grid gap-4 lg:grid-cols-2">
            <ByAsset data={data} />
            <ByStatus data={data} />
          </div>
        </div>
      )}
    </>
  );
}

function Kpis({ data }: { data: Overview | undefined }) {
  const t = useT();
  const { locale } = useLocale();
  const tot = data?.totals;
  const cards: { label: string; value: string | undefined; hint?: string; sub?: string; href?: string }[] = [
    { label: t("admin.overview.kpi.volume"), value: tot && formatUsd(tot.volume_usd, locale), hint: t("admin.overview.kpi.volumeHint") },
    { label: t("admin.overview.kpi.invoices"), value: tot && formatCount(tot.invoices, locale), href: "/admin/invoices" },
    { label: t("admin.overview.kpi.paid"), value: tot && formatCount(tot.paid_invoices, locale), href: "/admin/invoices?status=paid" },
    {
      label: t("admin.overview.kpi.conversion"),
      value: tot && formatPercent(tot.conversion_rate, locale),
      hint: t("admin.overview.kpi.conversionHint"),
    },
    { label: t("admin.overview.kpi.open"), value: tot && formatCount(tot.open_invoices, locale), hint: t("admin.overview.kpi.openHint") },
  ];
  return (
    <div className="grid grid-cols-2 gap-px overflow-hidden rounded-xl border bg-border md:grid-cols-3 xl:grid-cols-5">
      {cards.map((c, i) => {
        const body = (
          <>
            <div className="flex items-center gap-1 text-xs text-muted-foreground">
              <span>{c.label}</span>
              {c.hint && (
                <Tooltip>
                  <TooltipTrigger asChild>
                    <button type="button" className="rounded-sm text-muted-foreground/70 outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50" aria-label={c.hint}>
                      <InfoIcon className="size-3.5" aria-hidden />
                    </button>
                  </TooltipTrigger>
                  <TooltipContent className="max-w-56">{c.hint}</TooltipContent>
                </Tooltip>
              )}
            </div>
            {c.value === undefined ? (
              <Skeleton className="mt-2 h-7 w-24" />
            ) : (
              <div className="mt-1.5 font-heading text-[1.6rem] leading-none font-semibold tracking-tight tabular">{c.value}</div>
            )}
          </>
        );
        return (
          <div key={i} className={cn("bg-card px-4 py-3.5", i === 0 && "col-span-2 xl:col-span-1")}>
            {c.href ? (
              <Link href={c.href} className="block rounded-md outline-none focus-visible:ring-2 focus-visible:ring-ring/50 [&:hover_div:last-child]:text-primary">
                {body}
              </Link>
            ) : (
              body
            )}
          </div>
        );
      })}
    </div>
  );
}

function Attention({ data }: { data: Overview | undefined }) {
  const t = useT();
  const { locale } = useLocale();
  const tot = data?.totals;
  const items = [
    {
      count: tot?.unmatched_transfers,
      label: t("admin.overview.attention.unmatched"),
      hint: t("admin.overview.attention.unmatchedHint"),
      href: "/admin/transfers?matched=false",
      icon: TriangleAlertIcon,
    },
    {
      count: tot?.failed_webhooks,
      label: t("admin.overview.attention.failedWebhooks"),
      hint: t("admin.overview.attention.failedHint"),
      href: "/admin/webhooks?status=failed",
      icon: WebhookIcon,
    },
  ];
  const allClear = tot && tot.unmatched_transfers === 0 && tot.failed_webhooks === 0;
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

function ByStatus({ data }: { data: Overview | undefined }) {
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
                    href={`/admin/invoices?status=${r.status}`}
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
