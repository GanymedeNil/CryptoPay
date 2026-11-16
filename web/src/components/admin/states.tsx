"use client";

import { CircleAlertIcon, LockIcon, RefreshCwIcon, SearchXIcon, WifiOffIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Empty, EmptyContent, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";
import { Skeleton } from "@/components/ui/skeleton";
import { describeError, errorKind } from "@/lib/admin/errors";
import { useT } from "@/lib/i18n";
import { cn } from "@/lib/utils";

/**
 * A failed load: what happened in plain words and a retry. 403 explains the
 * role restriction (no retry), 404 says the record is gone.
 */
export function ErrorState({
  error,
  onRetry,
  className,
  compact = false,
}: {
  error: unknown;
  onRetry?: () => void;
  className?: string;
  compact?: boolean;
}) {
  const t = useT();
  const kind = errorKind(error);
  const Icon = kind === "network" ? WifiOffIcon : kind === "forbidden" ? LockIcon : kind === "notFound" ? SearchXIcon : CircleAlertIcon;
  const title =
    kind === "forbidden"
      ? t("admin.access.forbiddenTitle")
      : kind === "notFound"
        ? t("admin.errors.notFoundTitle")
        : t("admin.errors.loadFailed");
  const body = kind === "forbidden" ? t("admin.access.forbiddenBody") : describeError(t, error);
  const canRetry = onRetry && kind !== "forbidden" && kind !== "notFound";
  if (compact) {
    return (
      <div role="alert" className={cn("flex flex-wrap items-center gap-x-3 gap-y-2 rounded-lg border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm", className)}>
        <Icon className="size-4 shrink-0 text-destructive" aria-hidden />
        <span className="min-w-0 flex-1">{body}</span>
        {canRetry && (
          <Button size="sm" variant="outline" onClick={onRetry}>
            <RefreshCwIcon data-icon="inline-start" aria-hidden />
            {t("common.retry")}
          </Button>
        )}
      </div>
    );
  }
  return (
    <Empty role="alert" className={cn("border border-dashed bg-card/50", className)}>
      <EmptyHeader>
        <EmptyMedia variant="icon" className={kind === "forbidden" ? "" : "bg-destructive/10 text-destructive"}>
          <Icon aria-hidden />
        </EmptyMedia>
        <EmptyTitle>{title}</EmptyTitle>
        <EmptyDescription>{body}</EmptyDescription>
      </EmptyHeader>
      {canRetry && (
        <EmptyContent>
          <Button variant="outline" onClick={onRetry}>
            <RefreshCwIcon data-icon="inline-start" aria-hidden />
            {t("common.retry")}
          </Button>
        </EmptyContent>
      )}
    </Empty>
  );
}

/** Empty list: says what will appear here and offers the next action. */
export function EmptyState({
  icon: Icon,
  title,
  description,
  action,
  className,
}: {
  icon?: React.ComponentType<{ className?: string; "aria-hidden"?: boolean }>;
  title: string;
  description?: string;
  action?: React.ReactNode;
  className?: string;
}) {
  return (
    <Empty className={cn("py-10", className)}>
      <EmptyHeader>
        {Icon && (
          <EmptyMedia variant="icon">
            <Icon aria-hidden />
          </EmptyMedia>
        )}
        <EmptyTitle>{title}</EmptyTitle>
        {description && <EmptyDescription>{description}</EmptyDescription>}
      </EmptyHeader>
      {action && <EmptyContent>{action}</EmptyContent>}
    </Empty>
  );
}

/** Placeholder rows while a table loads. */
export function SkeletonRows({ rows = 6, className }: { rows?: number; className?: string }) {
  const t = useT();
  return (
    <div className={cn("space-y-2 p-3", className)} role="status" aria-label={t("admin.table.loadingRows")}>
      {Array.from({ length: rows }, (_, i) => (
        <Skeleton key={i} className="h-9 w-full" style={{ opacity: 1 - i * 0.1 }} />
      ))}
    </div>
  );
}

/** Generic page placeholder (route-level Suspense fallback). */
export function PageSkeleton() {
  return (
    <div className="space-y-6" aria-busy="true">
      <div className="space-y-2">
        <Skeleton className="h-7 w-48" />
        <Skeleton className="h-4 w-80 max-w-full" />
      </div>
      <Skeleton className="h-10 w-full" />
      <div className="space-y-2 rounded-xl border bg-card p-3">
        {Array.from({ length: 6 }, (_, i) => (
          <Skeleton key={i} className="h-9 w-full" />
        ))}
      </div>
    </div>
  );
}
