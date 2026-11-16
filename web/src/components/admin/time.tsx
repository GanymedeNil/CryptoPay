"use client";

import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { useNow } from "@/hooks/use-now";
import { formatDateTime } from "@/lib/format/date";
import { formatRelativeTime } from "@/lib/format/relative";
import { useLocale, useT } from "@/lib/i18n";
import { cn } from "@/lib/utils";

/** "3 minutes ago" with the absolute time in a tooltip (and `<time dateTime>`). */
export function RelativeTime({
  iso,
  className,
  fallback,
}: {
  iso: string | null | undefined;
  className?: string;
  /** Shown when `iso` is null (default "—"). */
  fallback?: React.ReactNode;
}) {
  const t = useT();
  const { locale } = useLocale();
  const now = useNow(30_000);
  if (!iso) return <span className={cn("text-muted-foreground", className)}>{fallback ?? t("admin.common.empty")}</span>;
  const absolute = formatDateTime(iso, locale);
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <time
          dateTime={iso}
          tabIndex={0}
          className={cn("cursor-default rounded-sm whitespace-nowrap outline-none focus-visible:ring-2 focus-visible:ring-ring/50", className)}
        >
          {formatRelativeTime(iso, now, locale)}
        </time>
      </TooltipTrigger>
      <TooltipContent>{absolute}</TooltipContent>
    </Tooltip>
  );
}

/** Absolute time with the relative one underneath (detail views). */
export function DateTimeStack({ iso, className }: { iso: string | null | undefined; className?: string }) {
  const t = useT();
  const { locale } = useLocale();
  const now = useNow(30_000);
  if (!iso) return <span className={cn("text-muted-foreground", className)}>{t("admin.common.empty")}</span>;
  return (
    <span className={cn("inline-flex flex-col", className)}>
      <time dateTime={iso} className="tabular">
        {formatDateTime(iso, locale)}
      </time>
      <span className="text-xs text-muted-foreground">{formatRelativeTime(iso, now, locale)}</span>
    </span>
  );
}
