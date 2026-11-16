"use client";

import { CoinsIcon, PencilIcon, PlusIcon, TriangleAlertIcon } from "lucide-react";
import { useState } from "react";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { addressLink } from "@/lib/admin/explorer";
import { adminKeys, useAssets, useChains, useOptimisticItemMutation } from "@/lib/admin/queries";
import { formatAmount } from "@/lib/format/amount";
import { adminApi } from "@/lib/api/admin";
import type { Asset, Chain } from "@/lib/api/types";
import { useT } from "@/lib/i18n";
import { cn } from "@/lib/utils";
import { GatedButton, RoleTooltip, useAuth } from "../auth";
import { DataTable, type Column } from "../data-table";
import { MonoValue } from "../mono-value";
import { PageHeader } from "../page-header";
import { EmptyState, ErrorState } from "../states";
import { NetworkBadge, StatusBadge } from "../status-badge";
import { AddAssetDialog, EditAssetDialog } from "./asset-dialogs";

/** Assets grouped by chain, in the chains' catalog order. */
export function groupAssetsByChain(assets: Asset[], chains: Chain[] | undefined): { chainId: string; chainName: string; testnet: boolean; enabled: boolean; items: Asset[] }[] {
  const order = new Map((chains ?? []).map((c, i) => [c.id, i]));
  const groups = new Map<string, Asset[]>();
  for (const a of assets) groups.set(a.chain_id, [...(groups.get(a.chain_id) ?? []), a]);
  return [...groups.entries()]
    .map(([chainId, items]) => {
      const chain = chains?.find((c) => c.id === chainId);
      return {
        chainId,
        chainName: items[0]?.chain_name ?? chainId,
        testnet: items[0]?.is_testnet ?? false,
        enabled: chain?.enabled ?? true,
        items: [...items].sort((x, y) => x.sort_order - y.sort_order || x.symbol.localeCompare(y.symbol)),
      };
    })
    .sort((a, b) => (order.get(a.chainId) ?? 999) - (order.get(b.chainId) ?? 999));
}

export function AssetsPage() {
  const t = useT();
  const assets = useAssets();
  const chains = useChains({ refetchInterval: false });
  const [addOpen, setAddOpen] = useState(false);
  const [editing, setEditing] = useState<Asset | null>(null);
  const groups = assets.data ? groupAssetsByChain(assets.data, chains.data) : undefined;

  return (
    <>
      <PageHeader
        title={t("admin.assets.title")}
        description={t("admin.assets.description")}
        actions={
          <GatedButton onClick={() => setAddOpen(true)} disabled={!chains.data}>
            <PlusIcon data-icon="inline-start" aria-hidden />
            {t("admin.assets.add")}
          </GatedButton>
        }
      />
      {assets.error && !assets.data ? (
        <ErrorState error={assets.error} onRetry={() => void assets.refetch()} />
      ) : !groups ? (
        <div className="space-y-3">
          {Array.from({ length: 4 }, (_, i) => (
            <Skeleton key={i} className="h-36 w-full" />
          ))}
        </div>
      ) : groups.length === 0 ? (
        <EmptyState icon={CoinsIcon} title={t("admin.assets.empty")} />
      ) : (
        <div className="space-y-5">
          {groups.map((g) => (
            <section key={g.chainId} aria-labelledby={`assets-${g.chainId}`}>
              <h2 id={`assets-${g.chainId}`} className="mb-2 flex items-center gap-2 font-heading text-sm font-semibold">
                {g.chainName}
                <NetworkBadge testnet={g.testnet} />
                {!g.enabled && <StatusBadge tone="muted">{t("admin.assets.chainDisabled")}</StatusBadge>}
              </h2>
              <AssetTable assets={g.items} chains={chains.data} onEdit={setEditing} />
            </section>
          ))}
        </div>
      )}
      {chains.data && <AddAssetDialog open={addOpen} onOpenChange={setAddOpen} chains={chains.data} />}
      <EditAssetDialog asset={editing} onOpenChange={(o) => !o && setEditing(null)} />
    </>
  );
}

function AssetTable({ assets, chains, onEdit }: { assets: Asset[]; chains?: Chain[]; onEdit: (a: Asset) => void }) {
  const t = useT();
  const { canWrite } = useAuth();
  const toggle = useOptimisticItemMutation<Asset, { id: string; enabled: boolean }>({
    queryKey: adminKeys.assets,
    id: (v) => v.id,
    apply: (a, v) => ({ ...a, enabled: v.enabled }),
    mutationFn: (v) => adminApi.updateAsset(v.id, { enabled: v.enabled }),
    successMessage: (a) => (a.enabled ? t("admin.assets.enabledToast", { symbol: a.symbol }) : t("admin.assets.disabledToast", { symbol: a.symbol })),
  });
  const columns: Column<Asset>[] = [
    {
      id: "asset",
      header: t("admin.assets.columns.asset"),
      cell: (a) => (
        <div className="min-w-0">
          <div className="flex items-center gap-1.5">
            <span className="font-medium">{a.symbol}</span>
            {a.is_stablecoin && <span className="rounded bg-muted px-1 text-[10px] font-medium text-muted-foreground">{t("admin.assets.stablecoin")}</span>}
          </div>
          <div className="truncate text-xs text-muted-foreground">{a.name}</div>
        </div>
      ),
    },
    {
      id: "contract",
      header: t("admin.assets.columns.contract"),
      cell: (a) =>
        a.contract_address ? (
          <MonoValue value={a.contract_address} label={t("admin.assets.columns.contract")} short={[6, 4]} href={addressLink(a.chain_id, a.contract_address, chains)} />
        ) : (
          <span className="text-xs text-muted-foreground">{t("admin.assets.native")}</span>
        ),
    },
    { id: "decimals", header: t("admin.assets.columns.decimals"), align: "right", cell: (a) => <span className="font-mono tabular">{a.decimals}</span> },
    { id: "precision", header: t("admin.assets.columns.precision"), align: "right", cell: (a) => <span className="font-mono tabular">{a.quote_precision}</span> },
    { id: "sort", header: t("admin.assets.columns.sortOrder"), align: "right", cell: (a) => <span className="font-mono tabular">{a.sort_order}</span> },
    { id: "rateKey", header: t("admin.assets.columns.rateKey"), cell: (a) => <span className="font-mono text-xs">{a.rate_key}</span> },
    {
      id: "rate",
      header: t("admin.assets.columns.rate"),
      align: "right",
      cell: (a) =>
        a.rate_usd ? (
          <span className="font-mono tabular">${formatAmount(a.rate_usd, { maxFractionDigits: 6 })}</span>
        ) : (
          <Tooltip>
            <TooltipTrigger asChild>
              <span tabIndex={0} className="inline-flex items-center gap-1 text-xs text-warning outline-none focus-visible:underline">
                <TriangleAlertIcon className="size-3.5" aria-hidden />
                {t("admin.assets.noRate")}
              </span>
            </TooltipTrigger>
            <TooltipContent className="max-w-56">{t("admin.assets.noRateHint")}</TooltipContent>
          </Tooltip>
        ),
    },
    {
      id: "enabled",
      header: t("admin.assets.columns.enabled"),
      cell: (a) => (
        <RoleTooltip blocked={!canWrite} reason={t("admin.access.readOnly")}>
          <Switch
            checked={a.enabled}
            disabled={!canWrite}
            onCheckedChange={(enabled) => toggle.mutate({ id: a.id, enabled })}
            aria-label={t("admin.assets.enableLabel", { symbol: a.symbol, chain: a.chain_name })}
          />
        </RoleTooltip>
      ),
    },
    {
      id: "actions",
      header: <span className="sr-only">{t("admin.common.actions")}</span>,
      align: "right",
      cell: (a) => (
        <GatedButton variant="ghost" size="icon-sm" onClick={() => onEdit(a)} aria-label={t("admin.assets.editTitle", { symbol: a.symbol, chain: a.chain_name })}>
          <PencilIcon aria-hidden />
        </GatedButton>
      ),
    },
  ];
  return (
    <DataTable
      caption={assets[0]?.chain_name ?? t("admin.assets.title")}
      columns={columns}
      rows={assets}
      getRowId={(a) => a.id}
      isLoading={false}
      rowClassName={(a) => cn(!a.enabled && "text-muted-foreground")}
      empty={null}
      mobileCard={(a) => (
        <div className="flex items-center justify-between gap-3">
          <div className="min-w-0">
            <div className="flex items-center gap-1.5">
              <span className="font-medium">{a.symbol}</span>
              <span className="truncate text-xs text-muted-foreground">{a.name}</span>
            </div>
            <div className="mt-0.5 text-xs text-muted-foreground">
              {a.rate_usd ? `$${formatAmount(a.rate_usd, { maxFractionDigits: 6 })}` : t("admin.assets.noRate")} · {t("admin.assets.columns.precision")} {a.quote_precision}
            </div>
          </div>
          <div className="flex items-center gap-1">
            <GatedButton variant="ghost" size="icon-sm" onClick={() => onEdit(a)} aria-label={t("admin.assets.editTitle", { symbol: a.symbol, chain: a.chain_name })}>
              <PencilIcon aria-hidden />
            </GatedButton>
            <RoleTooltip blocked={!canWrite} reason={t("admin.access.readOnly")}>
              <Switch
                checked={a.enabled}
                disabled={!canWrite}
                onCheckedChange={(enabled) => toggle.mutate({ id: a.id, enabled })}
                aria-label={t("admin.assets.enableLabel", { symbol: a.symbol, chain: a.chain_name })}
              />
            </RoleTooltip>
          </div>
        </div>
      )}
    />
  );
}
