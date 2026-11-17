"use client";

import { useMemo } from "react";
import { Bar, CartesianGrid, ComposedChart, Line, XAxis, YAxis } from "recharts";
import { ChartContainer, ChartLegend, ChartLegendContent, ChartTooltip, type ChartConfig } from "@/components/ui/chart";
import { formatDay, formatShortDate } from "@/lib/format/relative";
import { formatCount, plotValue } from "@/lib/format/units";
import type { Overview } from "@/lib/api/types";
import { useLocale, useT } from "@/lib/i18n";
import { cn } from "@/lib/utils";
import { formatUsd } from "../amount";

interface Row {
  date: string;
  label: string;
  /** Plot-only number; display uses `volume_usd`. */
  volume: number;
  volume_usd: string;
  invoices: number;
  paid: number;
}

/**
 * Bars: paid volume (USD); lines: invoices created and paid per day.
 * `compact`: a short chart without legend and count axis (merchant page).
 */
export function VolumeChart({ daily, compact = false }: { daily: Overview["daily"]; compact?: boolean }) {
  const t = useT();
  const { locale } = useLocale();
  const config = useMemo<ChartConfig>(
    () => ({
      volume: { label: t("admin.overview.chart.volume"), color: "var(--chart-2)" },
      invoices: { label: t("admin.overview.chart.invoices"), color: "var(--chart-4)" },
      paid: { label: t("admin.overview.chart.paid"), color: "var(--chart-1)" },
    }),
    [t],
  );
  const rows = useMemo<Row[]>(
    () =>
      daily.map((d) => ({
        date: d.date,
        label: formatShortDate(d.date, locale),
        volume: plotValue(d.volume_usd),
        volume_usd: d.volume_usd,
        invoices: d.invoices,
        paid: d.paid,
      })),
    [daily, locale],
  );
  const empty = rows.every((r) => r.invoices === 0 && r.volume === 0);

  return (
    <div>
      {empty ? (
        <p className={cn("flex items-center justify-center text-sm text-muted-foreground", compact ? "h-36" : "h-56")}>
          {t("admin.overview.chart.empty")}
        </p>
      ) : (
        <ChartContainer config={config} className={cn("aspect-auto w-full", compact ? "h-36" : "h-64 sm:h-72")} aria-hidden>
          <ComposedChart data={rows} margin={{ top: 8, right: 4, bottom: 0, left: 0 }}>
            <CartesianGrid vertical={false} strokeDasharray="3 3" />
            <XAxis dataKey="label" tickLine={false} axisLine={false} tickMargin={8} minTickGap={24} />
            <YAxis
              yAxisId="usd"
              tickLine={false}
              axisLine={false}
              width={compact ? 48 : 56}
              tickFormatter={(v: number) => compactUsd(v, locale)}
            />
            <YAxis yAxisId="count" orientation="right" tickLine={false} axisLine={false} width={32} allowDecimals={false} hide={compact} />
            <ChartTooltip
              cursor={{ fill: "var(--muted)", opacity: 0.6 }}
              content={({ active, payload }) => {
                const row = payload?.[0]?.payload as Row | undefined;
                if (!active || !row) return null;
                return (
                  <div className="grid min-w-40 gap-1.5 rounded-lg border bg-popover px-3 py-2 text-xs text-popover-foreground shadow-lg">
                    <div className="font-medium">{formatDay(row.date, locale)}</div>
                    <TooltipRow color="var(--color-volume)" label={t("admin.overview.chart.volume")} value={formatUsd(row.volume_usd, locale)} />
                    <TooltipRow color="var(--color-invoices)" label={t("admin.overview.chart.invoices")} value={formatCount(row.invoices, locale)} />
                    <TooltipRow color="var(--color-paid)" label={t("admin.overview.chart.paid")} value={formatCount(row.paid, locale)} />
                  </div>
                );
              }}
            />
            {!compact && <ChartLegend content={<ChartLegendContent />} />}
            <Bar yAxisId="usd" dataKey="volume" fill="var(--color-volume)" radius={[3, 3, 0, 0]} maxBarSize={28} />
            <Line yAxisId="count" dataKey="invoices" type="monotone" stroke="var(--color-invoices)" strokeWidth={2} dot={false} />
            <Line yAxisId="count" dataKey="paid" type="monotone" stroke="var(--color-paid)" strokeWidth={2} dot={false} />
          </ComposedChart>
        </ChartContainer>
      )}
      {/* The same data for screen readers. */}
      <table className="sr-only">
        <caption>{t("admin.overview.chart.tableCaption")}</caption>
        <thead>
          <tr>
            <th scope="col">{t("admin.overview.chart.date")}</th>
            <th scope="col">{t("admin.overview.chart.volume")}</th>
            <th scope="col">{t("admin.overview.chart.invoices")}</th>
            <th scope="col">{t("admin.overview.chart.paid")}</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => (
            <tr key={r.date}>
              <th scope="row">{r.date}</th>
              <td>{formatUsd(r.volume_usd, locale)}</td>
              <td>{r.invoices}</td>
              <td>{r.paid}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function TooltipRow({ color, label, value }: { color: string; label: string; value: string }) {
  return (
    <div className="flex items-center gap-2">
      <span className="size-2.5 shrink-0 rounded-[2px]" style={{ background: color }} aria-hidden />
      <span className="text-muted-foreground">{label}</span>
      <span className="ml-auto pl-3 font-mono font-medium tabular">{value}</span>
    </div>
  );
}

/** Axis ticks only (plot numbers): "$12k". */
function compactUsd(v: number, locale: "zh" | "en"): string {
  return new Intl.NumberFormat(locale === "zh" ? "zh-CN" : "en", {
    style: "currency",
    currency: "USD",
    currencyDisplay: "narrowSymbol",
    notation: "compact",
    maximumFractionDigits: 1,
  }).format(v);
}
