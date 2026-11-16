"use client";

import { ArrowLeftRightIcon, ArrowRightIcon, LinkIcon, TriangleAlertIcon } from "lucide-react";
import Link from "next/link";
import { useState } from "react";
import { addressLink, safeHttpUrl, txLink } from "@/lib/admin/explorer";
import { useChains, useTransfers } from "@/lib/admin/queries";
import { enumParam, useUrlState } from "@/lib/admin/url-state";
import { shortenAddress } from "@/lib/format/address";
import { formatCount } from "@/lib/format/units";
import type { Transfer, TransferQuery, TransferStatus } from "@/lib/api/types";
import { useLocale, useT, type MessageKey } from "@/lib/i18n";
import { CryptoAmount } from "../amount";
import { GatedButton } from "../auth";
import { DataTable, type Column } from "../data-table";
import { FilterBar, SearchFilter, SelectFilter } from "../filter-bar";
import { TransferConfirmations } from "../invoices/invoice-detail-page";
import { MonoValue } from "../mono-value";
import { PageHeader } from "../page-header";
import { EmptyState } from "../states";
import { StatusBadge, TransferStatusBadge } from "../status-badge";
import { RelativeTime } from "../time";
import { AssignTransferDialog } from "./assign-transfer-dialog";

const STATUSES: TransferStatus[] = ["pending", "confirmed", "orphaned"];
const MATCHED = ["true", "false"] as const;

export function TransfersPage() {
  const t = useT();
  const { locale } = useLocale();
  const url = useUrlState();
  const chains = useChains({ refetchInterval: false });
  const [assigning, setAssigning] = useState<Transfer | null>(null);
  const chainId = url.get("chain_id");
  const status = enumParam(url.params.get("status"), STATUSES);
  const matched = enumParam(url.params.get("matched"), MATCHED);
  const q = url.get("q");
  const query: TransferQuery = {
    page: url.page,
    page_size: url.pageSize,
    chain_id: chainId || undefined,
    status,
    matched: matched === undefined ? undefined : matched === "true",
    q: q || undefined,
  };
  const list = useTransfers(query);
  const filtered = Boolean(chainId || status || matched || q);
  const chainName = (id: string) => chains.data?.find((c) => c.id === id)?.name ?? id;

  const columns: Column<Transfer>[] = [
    {
      id: "tx",
      header: t("admin.transfers.columns.tx"),
      cell: (tr) => (
        <div className="space-y-0.5">
          <MonoValue
            value={tr.tx_hash}
            label={t("admin.invoice.transfers.tx")}
            short={[10, 6]}
            href={safeHttpUrl(tr.explorer_url) ?? txLink(tr.chain_id, tr.tx_hash, chains.data)}
          />
          <div className="text-xs text-muted-foreground">
            <RelativeTime iso={tr.detected_at} />
            {tr.index > 0 && <span className="ml-1.5 font-mono">#{tr.index}</span>}
          </div>
        </div>
      ),
    },
    {
      id: "asset",
      header: t("admin.transfers.columns.asset"),
      cell: (tr) => (
        <div>
          <div className="font-medium">{tr.symbol || t("admin.transfers.unknownToken")}</div>
          <div className="text-xs text-muted-foreground">{chainName(tr.chain_id)}</div>
        </div>
      ),
    },
    { id: "amount", header: t("admin.transfers.columns.amount"), align: "right", cell: (tr) => <CryptoAmount amount={tr.amount} /> },
    {
      id: "addresses",
      header: t("admin.transfers.columns.addresses"),
      cell: (tr) => (
        <div className="flex flex-col gap-0.5">
          <MonoValue value={tr.from_address} label={t("admin.invoice.transfers.from")} short={[6, 4]} copy={false} href={addressLink(tr.chain_id, tr.from_address, chains.data)} />
          <span className="flex items-center gap-1">
            <ArrowRightIcon className="size-3 shrink-0 text-muted-foreground" aria-label={t("admin.common.to")} />
            <MonoValue value={tr.to_address} label={t("admin.invoice.transfers.to")} short={[6, 4]} copy={false} href={addressLink(tr.chain_id, tr.to_address, chains.data)} />
          </span>
        </div>
      ),
    },
    {
      id: "status",
      header: t("admin.transfers.columns.status"),
      cell: (tr) => (
        <div className="space-y-1">
          <TransferStatusBadge status={tr.status} />
          <TransferConfirmations tr={tr} />
        </div>
      ),
    },
    {
      id: "invoice",
      header: t("admin.transfers.columns.invoice"),
      cell: (tr) =>
        tr.invoice_id ? (
          <Link href={`/admin/invoices/${tr.invoice_id}`} className="font-mono text-[13px] text-primary hover:underline">
            {shortenAddress(tr.invoice_id, 8, 4)}
          </Link>
        ) : (
          <div className="flex flex-col items-start gap-1">
            <StatusBadge tone="warning">{t("admin.transfers.unmatched")}</StatusBadge>
            <GatedButton size="xs" variant="outline" onClick={() => setAssigning(tr)} disabledReason={tr.status === "orphaned" ? t("admin.status.transfer.orphaned") : null}>
              <LinkIcon data-icon="inline-start" aria-hidden />
              {t("admin.transfers.assign")}
            </GatedButton>
          </div>
        ),
    },
  ];

  const unmatchedCount = matched === "false" && list.data ? list.data.total : 0;

  return (
    <>
      <PageHeader title={t("admin.transfers.title")} description={t("admin.transfers.description")} />
      <FilterBar active={filtered} onClear={() => url.clear(["page_size"])}>
        <SearchFilter label={t("admin.transfers.searchLabel")} placeholder={t("admin.transfers.searchPlaceholder")} value={q} onCommit={(v) => url.set({ q: v })} mono />
        <SelectFilter
          label={t("admin.transfers.chain")}
          allLabel={t("admin.transfers.allChains")}
          value={chainId}
          onChange={(v) => url.set({ chain_id: v })}
          options={(chains.data ?? []).map((c) => ({ value: c.id, label: c.name }))}
        />
        <SelectFilter
          label={t("admin.transfers.status")}
          allLabel={t("admin.transfers.allStatuses")}
          value={status ?? ""}
          onChange={(v) => url.set({ status: v })}
          options={STATUSES.map((s) => ({ value: s, label: t(`admin.status.transfer.${s}` as MessageKey) }))}
          className="sm:w-36"
        />
        <SelectFilter
          label={t("admin.transfers.matching")}
          allLabel={t("admin.transfers.matchedAll")}
          value={matched ?? ""}
          onChange={(v) => url.set({ matched: v })}
          options={[
            { value: "true", label: t("admin.transfers.matched") },
            { value: "false", label: t("admin.transfers.unmatched") },
          ]}
          className="sm:w-36"
        />
      </FilterBar>
      {unmatchedCount > 0 && (
        <div className="mb-3 flex gap-3 rounded-xl border border-warning-border bg-warning-surface p-3 text-sm text-warning" role="note">
          <TriangleAlertIcon className="mt-0.5 size-4 shrink-0" aria-hidden />
          <div>
            <p className="font-medium">{t("admin.transfers.unmatchedNoticeTitle", { count: formatCount(unmatchedCount, locale) })}</p>
            <p className="mt-0.5 text-pretty opacity-90">{t("admin.transfers.unmatchedNotice")}</p>
          </div>
        </div>
      )}
      <DataTable
        caption={t("admin.transfers.title")}
        columns={columns}
        rows={list.data?.items}
        getRowId={(tr) => tr.id}
        isLoading={list.isPending}
        isFetching={list.isFetching && list.isPlaceholderData}
        error={list.error}
        onRetry={() => void list.refetch()}
        rowClassName={(tr) => (tr.invoice_id ? undefined : "bg-warning-surface/30")}
        empty={
          filtered ? (
            <EmptyState icon={ArrowLeftRightIcon} title={t("admin.transfers.filteredEmptyTitle")} description={t("admin.transfers.filteredEmptyBody")} />
          ) : (
            <EmptyState
              icon={ArrowLeftRightIcon}
              title={t("admin.transfers.emptyTitle")}
              description={t("admin.transfers.emptyBody")}
              action={
                <Link href="/admin/chains" className="text-sm font-medium text-primary hover:underline">
                  {t("admin.nav.chains")}
                </Link>
              }
            />
          )
        }
        mobileCard={(tr) => (
          <div className="space-y-1.5">
            <div className="flex items-center justify-between gap-2">
              <CryptoAmount amount={tr.amount} symbol={tr.symbol || "?"} className="font-medium" />
              <TransferStatusBadge status={tr.status} />
            </div>
            <MonoValue value={tr.tx_hash} label={t("admin.invoice.transfers.tx")} short={[10, 6]} href={safeHttpUrl(tr.explorer_url) ?? txLink(tr.chain_id, tr.tx_hash, chains.data)} />
            <div className="flex items-center justify-between gap-2 text-xs text-muted-foreground">
              <span>
                {chainName(tr.chain_id)} · {tr.confirmations}/{tr.required_confirmations}
              </span>
              <RelativeTime iso={tr.detected_at} />
            </div>
            {tr.invoice_id ? (
              <Link href={`/admin/invoices/${tr.invoice_id}`} className="block font-mono text-xs text-primary">
                → {shortenAddress(tr.invoice_id, 8, 4)}
              </Link>
            ) : (
              <GatedButton size="sm" variant="outline" onClick={() => setAssigning(tr)}>
                <LinkIcon data-icon="inline-start" aria-hidden />
                {t("admin.transfers.assign")}
              </GatedButton>
            )}
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
      <AssignTransferDialog
        transfer={assigning}
        chainName={assigning ? chainName(assigning.chain_id) : undefined}
        onOpenChange={(o) => !o && setAssigning(null)}
      />
    </>
  );
}
