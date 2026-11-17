"use client";

import { ChevronRightIcon } from "lucide-react";
import { useRouter } from "next/navigation";
import { Fragment, useState } from "react";
import { Button } from "@/components/ui/button";
import { Spinner } from "@/components/ui/spinner";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { useIsMobile } from "@/hooks/use-mobile";
import { useT } from "@/lib/i18n";
import { cn } from "@/lib/utils";
import { PaginationBar, type PaginationState } from "./pagination-bar";
import { ErrorState, SkeletonRows } from "./states";

export interface Column<T> {
  id: string;
  header: React.ReactNode;
  cell: (row: T) => React.ReactNode;
  /** Applied to both the header and the cells. */
  className?: string;
  align?: "left" | "right";
  /** `aria-sort` of a sortable column's header (put the sort button in `header`). */
  ariaSort?: "ascending" | "descending" | "none";
}

const INTERACTIVE = "a,button,input,select,textarea,label,[role=button],[role=switch],[role=checkbox],[role=combobox],[data-row-ignore]";

/**
 * The admin list table: loading skeleton, error with retry, empty state,
 * optional row navigation (click / Enter), expandable rows, pagination, and
 * on phones (< 768 px) a card per row when `mobileCard` is given (otherwise
 * the table scrolls horizontally).
 */
export function DataTable<T>({
  columns,
  rows,
  getRowId,
  isLoading,
  isFetching = false,
  error,
  onRetry,
  empty,
  rowHref,
  renderExpanded,
  mobileCard,
  pagination,
  caption,
  rowClassName,
  className,
}: {
  columns: Column<T>[];
  rows: T[] | undefined;
  getRowId: (row: T) => string;
  /** No data yet (first load). */
  isLoading: boolean;
  /** Background refetch (filters changed with previous data kept). */
  isFetching?: boolean;
  error?: unknown;
  onRetry?: () => void;
  empty: React.ReactNode;
  /** Whole-row navigation target. Put a real link in the first cell too. */
  rowHref?: (row: T) => string;
  renderExpanded?: (row: T) => React.ReactNode;
  mobileCard?: (row: T) => React.ReactNode;
  pagination?: PaginationState;
  /** Accessible table name. */
  caption: string;
  rowClassName?: (row: T) => string | undefined;
  className?: string;
}) {
  const t = useT();
  const router = useRouter();
  const isMobile = useIsMobile();
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set());

  const toggle = (id: string) =>
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });

  let body: React.ReactNode;
  if (error && !rows) {
    body = <ErrorState error={error} onRetry={onRetry} className="m-3 border-0" />;
  } else if (isLoading || !rows) {
    body = <SkeletonRows />;
  } else if (rows.length === 0) {
    body = empty;
  } else if (isMobile && mobileCard) {
    body = (
      <ul className="divide-y">
        {rows.map((row) => {
          const id = getRowId(row);
          const href = rowHref?.(row);
          const open = expanded.has(id);
          return (
            <li
              key={id}
              className={cn("relative px-3 py-3", href && "cursor-pointer active:bg-muted/60", rowClassName?.(row))}
              onClick={(e) => {
                if (!href || (e.target as HTMLElement).closest(INTERACTIVE)) return;
                router.push(href);
              }}
            >
              {mobileCard(row)}
              {renderExpanded && (
                <>
                  <Button
                    variant="ghost"
                    size="sm"
                    className="mt-2 -ml-2 text-muted-foreground"
                    aria-expanded={open}
                    onClick={() => toggle(id)}
                  >
                    <ChevronRightIcon className={cn("transition-transform", open && "rotate-90")} aria-hidden />
                    {open ? t("admin.table.collapse") : t("admin.table.expand")}
                  </Button>
                  {open && <div className="mt-2">{renderExpanded(row)}</div>}
                </>
              )}
            </li>
          );
        })}
      </ul>
    );
  } else {
    const colCount = columns.length + (renderExpanded ? 1 : 0);
    body = (
      <Table aria-label={caption} className="text-[13px]">
        <TableHeader className="bg-muted/40">
          <TableRow className="hover:bg-transparent">
            {renderExpanded && (
              <TableHead className="w-8 px-2">
                <span className="sr-only">{t("admin.table.expand")}</span>
              </TableHead>
            )}
            {columns.map((c) => (
              <TableHead
                key={c.id}
                aria-sort={c.ariaSort}
                className={cn(
                  "h-9 px-3 text-xs font-medium text-muted-foreground",
                  c.align === "right" && "text-right",
                  c.className,
                )}
              >
                {c.header}
              </TableHead>
            ))}
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.map((row) => {
            const id = getRowId(row);
            const href = rowHref?.(row);
            const open = expanded.has(id);
            return (
              <Fragment key={id}>
                <TableRow
                  data-state={open ? "selected" : undefined}
                  className={cn(href && "cursor-pointer", rowClassName?.(row))}
                  onClick={(e) => {
                    if ((e.target as HTMLElement).closest(INTERACTIVE)) return;
                    if (href) router.push(href);
                    else if (renderExpanded) toggle(id);
                  }}
                >
                  {renderExpanded && (
                    <TableCell className="w-8 px-2">
                      <Button
                        variant="ghost"
                        size="icon-xs"
                        aria-expanded={open}
                        aria-label={open ? t("admin.table.collapse") : t("admin.table.expand")}
                        onClick={() => toggle(id)}
                      >
                        <ChevronRightIcon className={cn("transition-transform", open && "rotate-90")} aria-hidden />
                      </Button>
                    </TableCell>
                  )}
                  {columns.map((c) => (
                    <TableCell
                      key={c.id}
                      className={cn("px-3 py-2.5", c.align === "right" && "text-right", c.className)}
                    >
                      {c.cell(row)}
                    </TableCell>
                  ))}
                </TableRow>
                {renderExpanded && open && (
                  <TableRow className="bg-muted/20 hover:bg-muted/20">
                    <TableCell colSpan={colCount} className="px-3 py-3 whitespace-normal">
                      {renderExpanded(row)}
                    </TableCell>
                  </TableRow>
                )}
              </Fragment>
            );
          })}
        </TableBody>
      </Table>
    );
  }

  return (
    <div className={cn("space-y-3", className)}>
      <div className="relative overflow-hidden rounded-xl border bg-card" aria-busy={isLoading || isFetching}>
        {isFetching && rows && (
          <div className="absolute top-2 right-2 z-10 flex items-center gap-1.5 rounded-md bg-card/90 px-2 py-1 text-xs text-muted-foreground shadow-sm">
            <Spinner className="size-3" aria-hidden />
            <span>{t("admin.table.updating")}</span>
          </div>
        )}
        {error && rows ? <ErrorState compact error={error} onRetry={onRetry} className="m-2" /> : null}
        {body}
      </div>
      {pagination && rows && rows.length > 0 && <PaginationBar {...pagination} />}
    </div>
  );
}
