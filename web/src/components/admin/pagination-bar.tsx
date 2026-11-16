"use client";

import { ChevronLeftIcon, ChevronRightIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { PAGE_SIZES } from "@/lib/admin/url-state";
import { formatCount } from "@/lib/format/units";
import { useLocale, useT } from "@/lib/i18n";
import { cn } from "@/lib/utils";

export interface PaginationState {
  page: number;
  pageSize: number;
  total: number;
  onPageChange: (page: number) => void;
  onPageSizeChange?: (size: number) => void;
}

export function pageCount(total: number, pageSize: number): number {
  return Math.max(1, Math.ceil(total / pageSize));
}

/** "21–40 of 345" + page size + previous / next. */
export function PaginationBar({ page, pageSize, total, onPageChange, onPageSizeChange, className }: PaginationState & { className?: string }) {
  const t = useT();
  const { locale } = useLocale();
  const pages = pageCount(total, pageSize);
  const from = total === 0 ? 0 : (page - 1) * pageSize + 1;
  const to = Math.min(total, page * pageSize);
  return (
    <nav
      aria-label={t("admin.table.pagination")}
      className={cn("flex flex-wrap items-center justify-between gap-x-4 gap-y-2 px-1 text-sm text-muted-foreground", className)}
    >
      <p aria-live="polite" className="tabular">
        {t("admin.table.range", {
          from: formatCount(from, locale),
          to: formatCount(to, locale),
          total: formatCount(total, locale),
        })}
      </p>
      <div className="flex items-center gap-2">
        {onPageSizeChange && (
          <div className="flex items-center gap-1.5">
            <span className="hidden sm:inline" aria-hidden>
              {t("admin.table.perPage")}
            </span>
            <Select value={String(pageSize)} onValueChange={(v) => onPageSizeChange(Number(v))}>
              <SelectTrigger size="sm" aria-label={t("admin.table.perPage")} className="w-18">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {PAGE_SIZES.map((s) => (
                  <SelectItem key={s} value={String(s)}>
                    {s}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        )}
        <span className="tabular whitespace-nowrap">{t("admin.table.pageOf", { page, pages })}</span>
        <Button
          variant="outline"
          size="icon-sm"
          onClick={() => onPageChange(page - 1)}
          disabled={page <= 1}
          aria-label={t("admin.table.previous")}
        >
          <ChevronLeftIcon aria-hidden />
        </Button>
        <Button
          variant="outline"
          size="icon-sm"
          onClick={() => onPageChange(page + 1)}
          disabled={page >= pages}
          aria-label={t("admin.table.next")}
        >
          <ChevronRightIcon aria-hidden />
        </Button>
      </div>
    </nav>
  );
}
