"use client";

import { InfoIcon } from "lucide-react";
import Link from "next/link";
import { Skeleton } from "@/components/ui/skeleton";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";

export interface Kpi {
  label: string;
  /** undefined = still loading. */
  value: string | undefined;
  hint?: string;
  href?: string;
  /** Highlight (e.g. failed webhooks > 0). */
  warn?: boolean;
}

/**
 * A row of headline numbers in one bordered grid (overview, merchant page).
 * `featureFirst` widens the first card on phones (an odd number of cards).
 */
export function KpiGrid({ cards, className, featureFirst = false }: { cards: Kpi[]; className?: string; featureFirst?: boolean }) {
  return (
    <div className={cn("grid grid-cols-2 gap-px overflow-hidden rounded-xl border bg-border md:grid-cols-3", className)}>
      {cards.map((c, i) => {
        const body = (
          <>
            <div className="flex items-center gap-1 text-xs text-muted-foreground">
              <span>{c.label}</span>
              {c.hint && (
                <Tooltip>
                  <TooltipTrigger asChild>
                    <button
                      type="button"
                      className="rounded-sm text-muted-foreground/70 outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50"
                      aria-label={c.hint}
                    >
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
              <div
                className={cn(
                  "mt-1.5 font-heading text-[1.6rem] leading-none font-semibold tracking-tight tabular",
                  c.warn && "text-warning",
                )}
              >
                {c.value}
              </div>
            )}
          </>
        );
        return (
          <div
            key={i}
            className={cn("bg-card px-4 py-3.5", featureFirst && i === 0 && "col-span-2 xl:col-span-1", c.warn && "bg-warning-surface/50")}
          >
            {c.href ? (
              <Link
                href={c.href}
                className="block rounded-md outline-none focus-visible:ring-2 focus-visible:ring-ring/50 [&:hover_div:last-child]:text-primary"
              >
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
