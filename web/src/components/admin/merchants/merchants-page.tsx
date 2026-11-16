"use client";

import { KeyRoundIcon, PlusIcon, StoreIcon } from "lucide-react";
import Link from "next/link";
import { useState } from "react";
import { useMerchants } from "@/lib/admin/queries";
import { useUrlState } from "@/lib/admin/url-state";
import type { Merchant } from "@/lib/api/types";
import { useT } from "@/lib/i18n";
import { GatedButton } from "../auth";
import { DataTable, type Column } from "../data-table";
import { FilterBar, SearchFilter } from "../filter-bar";
import { PageHeader } from "../page-header";
import { EmptyState } from "../states";
import { MerchantStatusBadge, StatusBadge } from "../status-badge";
import { RelativeTime } from "../time";
import { CreateMerchantDialog } from "./create-merchant-dialog";

export function MerchantsPage() {
  const t = useT();
  const url = useUrlState();
  const [createOpen, setCreateOpen] = useState(false);
  const q = url.get("q");
  const list = useMerchants({ page: url.page, page_size: url.pageSize, q: q || undefined });

  const columns: Column<Merchant>[] = [
    {
      id: "name",
      header: t("admin.merchants.columns.name"),
      cell: (m) => (
        <Link href={`/admin/merchants/${m.id}`} className="font-medium text-primary hover:underline">
          {m.name}
        </Link>
      ),
    },
    { id: "status", header: t("admin.merchants.columns.status"), cell: (m) => <MerchantStatusBadge status={m.status} /> },
    {
      id: "webhook",
      header: t("admin.merchants.columns.webhook"),
      className: "max-w-80",
      cell: (m) =>
        m.webhook_url ? (
          <span className="block truncate font-mono text-xs" title={m.webhook_url}>
            {m.webhook_url}
          </span>
        ) : (
          <span className="text-muted-foreground">{t("admin.merchants.noWebhook")}</span>
        ),
    },
    {
      id: "secret",
      header: t("admin.merchants.columns.secret"),
      cell: (m) =>
        !m.webhook_url && !m.has_webhook_secret ? (
          <span className="text-muted-foreground">—</span>
        ) : m.has_webhook_secret ? (
          <StatusBadge tone="success" dot={false}>
            <KeyRoundIcon className="size-3" aria-hidden />
            {t("admin.merchants.secretSet")}
          </StatusBadge>
        ) : (
          <StatusBadge tone="warning" dot={false}>
            {t("admin.merchants.secretMissing")}
          </StatusBadge>
        ),
    },
    { id: "created", header: t("admin.merchants.columns.created"), cell: (m) => <RelativeTime iso={m.created_at} className="text-muted-foreground" /> },
  ];

  const createButton = (variant?: "outline") => (
    <GatedButton variant={variant} onClick={() => setCreateOpen(true)}>
      <PlusIcon data-icon="inline-start" aria-hidden />
      {t("admin.merchants.create")}
    </GatedButton>
  );

  return (
    <>
      <PageHeader title={t("admin.merchants.title")} description={t("admin.merchants.description")} actions={createButton()} />
      <FilterBar>
        <SearchFilter label={t("admin.merchants.searchLabel")} placeholder={t("admin.merchants.searchPlaceholder")} value={q} onCommit={(v) => url.set({ q: v })} />
      </FilterBar>
      <DataTable
        caption={t("admin.merchants.title")}
        columns={columns}
        rows={list.data?.items}
        getRowId={(m) => m.id}
        isLoading={list.isPending}
        isFetching={list.isFetching && list.isPlaceholderData}
        error={list.error}
        onRetry={() => void list.refetch()}
        rowHref={(m) => `/admin/merchants/${m.id}`}
        empty={
          q ? (
            <EmptyState icon={StoreIcon} title={t("admin.merchants.filteredEmptyTitle")} description={t("admin.merchants.filteredEmptyBody")} />
          ) : (
            <EmptyState icon={StoreIcon} title={t("admin.merchants.emptyTitle")} description={t("admin.merchants.emptyBody")} action={createButton("outline")} />
          )
        }
        mobileCard={(m) => (
          <div className="space-y-1">
            <div className="flex items-center justify-between gap-2">
              <Link href={`/admin/merchants/${m.id}`} className="font-medium text-primary">
                {m.name}
              </Link>
              <MerchantStatusBadge status={m.status} />
            </div>
            <p className="truncate font-mono text-xs text-muted-foreground">{m.webhook_url || t("admin.merchants.noWebhook")}</p>
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
      <CreateMerchantDialog open={createOpen} onOpenChange={setCreateOpen} />
    </>
  );
}
