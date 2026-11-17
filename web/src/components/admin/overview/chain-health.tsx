"use client";

import { ArrowRightIcon, GlobeIcon } from "lucide-react";
import Link from "next/link";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { useNow } from "@/hooks/use-now";
import { chainHealth, receivesPayments, type ChainHealth } from "@/lib/admin/chains";
import { useChains } from "@/lib/admin/queries";
import { formatRelativeTime } from "@/lib/format/relative";
import { formatCount } from "@/lib/format/units";
import type { Chain } from "@/lib/api/types";
import { useLocale, useT } from "@/lib/i18n";
import { cn } from "@/lib/utils";
import { Section } from "../page-header";
import { ErrorState } from "../states";
import { StatusBadge } from "../status-badge";

const HEALTH_DOT: Record<ChainHealth, string> = {
  ok: "bg-success",
  idle: "bg-muted-foreground/50",
  lagging: "bg-warning",
  error: "bg-destructive",
  disabled: "bg-muted-foreground/30",
};

/**
 * One tile per enabled chain: scanner lag as a meter, last scan, last error.
 * The meter is the scanner's distance to the chain head relative to the
 * confirmation depth (full = at least that many blocks behind).
 * `systemWide` labels it as not affected by the overview's merchant filter.
 */
export function ChainHealthStrip({ systemWide = false }: { systemWide?: boolean }) {
  const t = useT();
  const chains = useChains();
  const enabled = chains.data?.filter((c) => c.enabled) ?? [];
  return (
    <Section
      title={
        systemWide ? (
          <span className="flex flex-wrap items-center gap-2">
            {t("admin.overview.chainHealth.title")}
            <StatusBadge tone="neutral" dot={false}>
              <GlobeIcon className="size-3" aria-hidden />
              {t("admin.overview.chainHealth.systemWide")}
            </StatusBadge>
          </span>
        ) : (
          t("admin.overview.chainHealth.title")
        )
      }
      description={systemWide ? t("admin.overview.chainHealth.systemWideHint") : t("admin.overview.chainHealth.description")}
      className={cn(systemWide && "border-dashed bg-card/60")}
      actions={
        <Button asChild variant="ghost" size="sm">
          <Link href="/admin/chains">
            {t("admin.overview.chainHealth.manage")}
            <ArrowRightIcon data-icon="inline-end" aria-hidden />
          </Link>
        </Button>
      }
      bodyClassName="p-3"
    >
      {chains.error && !chains.data ? (
        <ErrorState compact error={chains.error} onRetry={() => void chains.refetch()} />
      ) : !chains.data ? (
        <div className="grid grid-cols-2 gap-2 sm:grid-cols-3 lg:grid-cols-4 xl:grid-cols-6">
          {Array.from({ length: 6 }, (_, i) => (
            <Skeleton key={i} className="h-20" />
          ))}
        </div>
      ) : enabled.length === 0 ? (
        <p className="px-1 py-2 text-sm text-muted-foreground">{t("admin.overview.chainHealth.noneEnabled")}</p>
      ) : (
        <ul className="grid grid-cols-1 gap-2 min-[420px]:grid-cols-2 sm:grid-cols-3 lg:grid-cols-4 xl:grid-cols-6">
          {enabled.map((c) => (
            <ChainTile key={c.id} chain={c} />
          ))}
        </ul>
      )}
    </Section>
  );
}

function ChainTile({ chain: c }: { chain: Chain }) {
  const t = useT();
  const { locale } = useLocale();
  const now = useNow(30_000);
  const health = chainHealth(c);
  const depth = Math.max(c.confirmations, 1);
  const fill = Math.min(1, Math.max(0, c.lag) / depth);
  const status =
    health === "error"
      ? t("admin.overview.chainHealth.error")
      : c.lag > 0
        ? t("admin.overview.chainHealth.lagBlocks", { n: formatCount(c.lag, locale) })
        : t("admin.overview.chainHealth.synced");
  return (
    <li className="min-w-0 rounded-lg border bg-background/60 p-2.5">
      <div className="flex items-center gap-1.5">
        <span className={cn("size-2 shrink-0 rounded-full", HEALTH_DOT[health])} aria-hidden />
        <span className="min-w-0 truncate text-[13px] font-medium">{c.name}</span>
        {!receivesPayments(c) && (
          <span className="ml-auto shrink-0 rounded border border-warning-border bg-warning-surface px-1 text-[10px] font-medium text-warning">
            {t("admin.overview.chainHealth.notPayable")}
          </span>
        )}
      </div>
      <div
        className="mt-2 h-1 overflow-hidden rounded-full bg-muted"
        role="meter"
        aria-label={status}
        aria-valuemin={0}
        aria-valuemax={depth}
        aria-valuenow={Math.min(Math.max(0, c.lag), depth)}
      >
        <div
          className={cn("h-full rounded-full", health === "error" ? "bg-destructive" : fill >= 1 ? "bg-warning" : "bg-primary")}
          style={{ width: `${Math.max(fill * 100, c.lag > 0 ? 6 : 0)}%` }}
        />
      </div>
      <div className="mt-1.5 flex items-center justify-between gap-2 text-[11px] text-muted-foreground">
        {health === "error" ? (
          <Tooltip>
            <TooltipTrigger asChild>
              <span tabIndex={0} className="min-w-0 truncate text-destructive outline-none focus-visible:underline">
                {c.last_error}
              </span>
            </TooltipTrigger>
            <TooltipContent className="max-w-xs break-words">{c.last_error}</TooltipContent>
          </Tooltip>
        ) : (
          <span className="tabular">{status}</span>
        )}
        <span className="shrink-0 whitespace-nowrap">
          {c.last_scan_at ? formatRelativeTime(c.last_scan_at, now, locale) : t("admin.overview.chainHealth.neverScanned")}
        </span>
      </div>
    </li>
  );
}
