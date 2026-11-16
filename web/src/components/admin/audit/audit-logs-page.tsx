"use client";

import { ScrollTextIcon } from "lucide-react";
import Link from "next/link";
import { useAuditLogs } from "@/lib/admin/queries";
import { useUrlState } from "@/lib/admin/url-state";
import { shortenAddress } from "@/lib/format/address";
import type { AuditLog } from "@/lib/api/types";
import { useT } from "@/lib/i18n";
import { DataTable, type Column } from "../data-table";
import { FilterBar, SearchFilter } from "../filter-bar";
import { JsonViewer } from "../json-viewer";
import { PageHeader } from "../page-header";
import { EmptyState } from "../states";
import { DateTimeStack } from "../time";

/** Where an audit target lives in the console, when it has a page. */
export function auditTargetHref(log: Pick<AuditLog, "target_type" | "target_id">): string | null {
  if (!log.target_id) return null;
  switch (log.target_type) {
    case "invoice":
      return `/admin/invoices/${log.target_id}`;
    case "merchant":
      return `/admin/merchants/${log.target_id}`;
    case "transfer":
      return `/admin/transfers?q=${encodeURIComponent(log.target_id)}`;
    case "webhook_event":
      return null;
    case "chain":
      return "/admin/chains";
    case "asset":
      return "/admin/assets";
    case "wallet":
      return "/admin/wallets";
    case "settings":
      return "/admin/settings";
    case "user":
      return "/admin/users";
    default:
      return null;
  }
}

export function AuditLogsPage() {
  const t = useT();
  const url = useUrlState();
  const action = url.get("action");
  const actor = url.get("actor");
  const list = useAuditLogs({ page: url.page, page_size: url.pageSize, action: action || undefined, actor: actor || undefined });
  const filtered = Boolean(action || actor);

  const columns: Column<AuditLog>[] = [
    { id: "time", header: t("admin.audit.columns.time"), cell: (l) => <DateTimeStack iso={l.created_at} className="text-xs" /> },
    { id: "actor", header: t("admin.audit.columns.actor"), cell: (l) => <span className="block max-w-56 truncate">{l.actor_email || "—"}</span> },
    {
      id: "action",
      header: t("admin.audit.columns.action"),
      cell: (l) => <span className="rounded-md bg-muted px-1.5 py-0.5 font-mono text-xs">{l.action}</span>,
    },
    {
      id: "target",
      header: t("admin.audit.columns.target"),
      cell: (l) => {
        const href = auditTargetHref(l);
        const label = (
          <>
            <span className="text-muted-foreground">{l.target_type}</span>
            {l.target_id && <span className="ml-1 font-mono">{shortenAddress(l.target_id, 8, 4)}</span>}
          </>
        );
        return href ? (
          <Link href={href} className="text-xs hover:underline">
            {label}
          </Link>
        ) : (
          <span className="text-xs">{label}</span>
        );
      },
    },
    { id: "ip", header: t("admin.audit.columns.ip"), cell: (l) => <span className="font-mono text-xs text-muted-foreground">{l.ip || "—"}</span> },
  ];

  return (
    <>
      <PageHeader title={t("admin.audit.title")} description={t("admin.audit.description")} />
      <FilterBar active={filtered} onClear={() => url.clear(["page_size"])}>
        <SearchFilter label={t("admin.audit.action")} placeholder={t("admin.audit.actionPlaceholder")} value={action} onCommit={(v) => url.set({ action: v })} mono className="sm:w-60" />
        <SearchFilter label={t("admin.audit.actor")} placeholder={t("admin.audit.actorPlaceholder")} value={actor} onCommit={(v) => url.set({ actor: v })} className="sm:w-60" />
      </FilterBar>
      <DataTable
        caption={t("admin.audit.title")}
        columns={columns}
        rows={list.data?.items}
        getRowId={(l) => String(l.id)}
        isLoading={list.isPending}
        isFetching={list.isFetching && list.isPlaceholderData}
        error={list.error}
        onRetry={() => void list.refetch()}
        renderExpanded={(l) =>
          l.detail === null || l.detail === undefined ? (
            <p className="text-xs text-muted-foreground">{t("admin.audit.noDetail")}</p>
          ) : (
            <div className="space-y-1">
              <p className="text-xs text-muted-foreground">{t("admin.audit.detail")}</p>
              <JsonViewer value={l.detail} label={t("admin.audit.detail")} />
            </div>
          )
        }
        empty={
          filtered ? (
            <EmptyState icon={ScrollTextIcon} title={t("admin.audit.filteredEmptyTitle")} description={t("admin.audit.filteredEmptyBody")} />
          ) : (
            <EmptyState icon={ScrollTextIcon} title={t("admin.audit.emptyTitle")} description={t("admin.audit.emptyBody")} />
          )
        }
        mobileCard={(l) => (
          <div className="space-y-1">
            <div className="flex items-center justify-between gap-2">
              <span className="rounded-md bg-muted px-1.5 py-0.5 font-mono text-xs">{l.action}</span>
              <DateTimeStack iso={l.created_at} className="text-right text-xs" />
            </div>
            <p className="truncate text-xs text-muted-foreground">
              {l.actor_email} · {l.target_type} {l.target_id && shortenAddress(l.target_id, 8, 4)}
            </p>
          </div>
        )}
        pagination={
          list.data && {
            page: list.data.page,
            pageSize: list.data.page_size,
            total: list.data.total,
            onPageChange: (p) => url.set({ page: p > 1 ? p : null }, { resetPage: false }),
            onPageSizeChange: (s) => url.set({ page_size: s === 20 ? null : s }),
          }
        }
      />
    </>
  );
}
