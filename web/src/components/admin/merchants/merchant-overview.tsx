"use client";

import { ArrowRightIcon, ReceiptTextIcon } from "lucide-react";
import Link from "next/link";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { failedWebhooksHref, invoicesHref, overviewHref, rangeParam } from "@/lib/admin/merchant-overview";
import { useOverview } from "@/lib/admin/queries";
import { useUrlState } from "@/lib/admin/url-state";
import { formatCount, formatPercent } from "@/lib/format/units";
import { useLocale, useT } from "@/lib/i18n";
import { cn } from "@/lib/utils";
import { formatUsd } from "../amount";
import { KpiGrid } from "../overview/kpi-grid";
import { RangeToggle } from "../overview/range-toggle";
import { VolumeChart } from "../overview/volume-chart";
import { Section } from "../page-header";
import { ErrorState } from "../states";

const TOP_ASSETS = 3;

/**
 * "Activity" on the merchant page: `GET /overview?merchant_id=` for the
 * range in `?days=` (7 / 30 / 90) — KPIs, a small daily chart, top assets
 * and links to the merchant's invoices and to the filtered overview.
 */
export function MerchantOverviewSection({ merchantId, className }: { merchantId: string; className?: string }) {
  const t = useT();
  const { locale } = useLocale();
  const url = useUrlState();
  const days = rangeParam(url.params.get("days"));
  const q = useOverview(days, merchantId);
  const data = q.data;
  const tot = data?.totals;
  const top = data?.by_asset.slice(0, TOP_ASSETS);

  return (
    <Section
      className={className}
      title={t("admin.merchant.overview.title")}
      description={t("admin.merchant.overview.description")}
      actions={<RangeToggle value={days} onChange={(d) => url.set({ days: d === 30 ? null : d }, { resetPage: false })} />}
    >
      {q.error && !data ? (
        <ErrorState compact error={q.error} onRetry={() => void q.refetch()} />
      ) : (
        <div className={cn("space-y-4 transition-opacity", q.isPlaceholderData && "opacity-60")} aria-busy={q.isFetching}>
          <KpiGrid
            className="xl:grid-cols-6"
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
              {
                label: t("admin.merchant.overview.failedWebhooks"),
                value: tot && formatCount(tot.failed_webhooks, locale),
                href: failedWebhooksHref(merchantId),
                warn: (tot?.failed_webhooks ?? 0) > 0,
              },
            ]}
          />
          <div className="grid gap-4 lg:grid-cols-3">
            <div className="min-w-0 lg:col-span-2">
              <h3 className="mb-1 text-xs font-medium text-muted-foreground">{t("admin.merchant.overview.dailyVolume")}</h3>
              {data ? <VolumeChart daily={data.daily} compact /> : <Skeleton className="h-36 w-full" />}
            </div>
            <div className="min-w-0">
              <h3 className="mb-1 text-xs font-medium text-muted-foreground">{t("admin.merchant.overview.topAssets")}</h3>
              {!top ? (
                <div className="space-y-2">
                  {Array.from({ length: TOP_ASSETS }, (_, i) => (
                    <Skeleton key={i} className="h-7 w-full" />
                  ))}
                </div>
              ) : top.length === 0 ? (
                <p className="py-2 text-sm text-muted-foreground">{t("admin.merchant.overview.noAssets")}</p>
              ) : (
                <ul className="divide-y text-[13px]">
                  {top.map((a) => (
                    <li key={a.asset_id} className="flex items-baseline justify-between gap-3 py-1.5">
                      <span className="min-w-0 truncate">
                        <span className="font-medium">{a.symbol}</span>
                        <span className="ml-1.5 text-muted-foreground">{a.chain_name}</span>
                      </span>
                      <span className="shrink-0 text-right">
                        <span className="font-mono tabular">{formatUsd(a.volume_usd, locale)}</span>
                        <span className="ml-2 text-xs text-muted-foreground tabular">×{formatCount(a.paid, locale)}</span>
                      </span>
                    </li>
                  ))}
                </ul>
              )}
            </div>
          </div>
          <div className="flex flex-wrap gap-2">
            <Button asChild variant="outline" size="sm">
              <Link href={invoicesHref(merchantId)}>
                <ReceiptTextIcon data-icon="inline-start" aria-hidden />
                {t("admin.merchant.overview.viewAllInvoices")}
              </Link>
            </Button>
            <Button asChild variant="ghost" size="sm">
              <Link href={overviewHref(merchantId, days)}>
                {t("admin.merchant.overview.openInOverview")}
                <ArrowRightIcon data-icon="inline-end" aria-hidden />
              </Link>
            </Button>
          </div>
        </div>
      )}
    </Section>
  );
}
