"use client";

import { ArrowLeftIcon, ChevronDownIcon, ChevronRightIcon } from "lucide-react";
import { useId, useState } from "react";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Spinner } from "@/components/ui/spinner";
import type { CheckoutAsset } from "@/lib/api/types";
import { groupAssetsByCoin, type CoinGroup } from "@/lib/checkout/assets";
import { formatAmount } from "@/lib/format/amount";
import { useT } from "@/lib/i18n";
import { cn } from "@/lib/utils";
import { CoinMark } from "./coin-mark";
import { networkLabel, NetworkName, TestnetBadge } from "./network-badge";

function Estimate({ asset }: { asset: CheckoutAsset }) {
  const t = useT();
  if (asset.estimated_amount === null) {
    return <span className="text-xs text-muted-foreground">{t("checkout.select.noRate")}</span>;
  }
  return (
    <span className="font-mono text-sm tabular text-foreground/85">
      {t("checkout.select.estimated", { amount: formatAmount(asset.estimated_amount, { keepScale: true }) })}{" "}
      <span className="text-xs text-muted-foreground">{asset.symbol}</span>
    </span>
  );
}

export function AssetPicker({
  assets,
  onSelect,
  pendingAssetId,
  error,
  onBack,
}: {
  assets: CheckoutAsset[];
  onSelect: (asset: CheckoutAsset) => void;
  /** Asset whose quote is being created. */
  pendingAssetId: string | null;
  error: string | null;
  /** Shown when a quote already exists ("back to current payment"). */
  onBack?: () => void;
}) {
  const t = useT();
  const groups = groupAssetsByCoin(assets);
  const firstMulti = groups.find((g) => g.assets.length > 1)?.symbol ?? null;
  const [open, setOpen] = useState<string | null>(firstMulti);
  const busy = pendingAssetId !== null;

  return (
    <section aria-labelledby="choose-asset-title" className="space-y-4">
      <div className="space-y-1">
        {onBack && (
          <Button variant="ghost" size="sm" className="-ml-2 mb-1 text-muted-foreground" onClick={onBack}>
            <ArrowLeftIcon data-icon="inline-start" aria-hidden />
            {t("checkout.select.backToPayment")}
          </Button>
        )}
        <h2 id="choose-asset-title" className="font-heading text-lg font-semibold">
          {t("checkout.select.title")}
        </h2>
        <p className="text-sm text-muted-foreground">{t("checkout.select.description")}</p>
      </div>

      {error && (
        <Alert variant="destructive" role="alert">
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}

      {groups.length === 0 ? (
        <div className="rounded-xl border border-dashed px-4 py-8 text-center">
          <p className="font-medium">{t("checkout.select.empty")}</p>
          <p className="mt-1 text-sm text-muted-foreground">{t("checkout.select.emptyHint")}</p>
        </div>
      ) : (
        <ul className="divide-y overflow-hidden rounded-xl border">
          {groups.map((g) => (
            <CoinRow
              key={g.symbol}
              group={g}
              open={open === g.symbol}
              onToggle={() => setOpen((cur) => (cur === g.symbol ? null : g.symbol))}
              onSelect={onSelect}
              pendingAssetId={pendingAssetId}
              disabled={busy}
            />
          ))}
        </ul>
      )}
      {busy && (
        <p role="status" className="flex items-center gap-2 text-sm text-muted-foreground">
          <Spinner className="size-4" /> {t("checkout.select.preparing")}
        </p>
      )}
    </section>
  );
}

function CoinRow({
  group,
  open,
  onToggle,
  onSelect,
  pendingAssetId,
  disabled,
}: {
  group: CoinGroup;
  open: boolean;
  onToggle: () => void;
  onSelect: (asset: CheckoutAsset) => void;
  pendingAssetId: string | null;
  disabled: boolean;
}) {
  const t = useT();
  const panelId = useId();
  const single = group.assets.length === 1 ? group.assets[0] : null;
  const head = (
    <>
      <CoinMark symbol={group.symbol} />
      <span className="min-w-0 flex-1">
        <span className="flex items-center gap-2">
          <span className="font-semibold">{group.symbol}</span>
          {group.allTestnet && <TestnetBadge />}
        </span>{" "}
        <span className="block truncate text-xs text-muted-foreground">
          {single ? networkLabel(single) : `${group.name} · ${t("checkout.select.networks", { count: group.assets.length })}`}
        </span>
      </span>
    </>
  );

  if (single) {
    const pending = pendingAssetId === single.id;
    return (
      <li>
        <button
          type="button"
          disabled={disabled}
          onClick={() => onSelect(single)}
          className="flex w-full items-center gap-3 px-4 py-3.5 text-left transition-colors outline-none hover:bg-muted/60 focus-visible:bg-muted/60 focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:ring-inset disabled:cursor-not-allowed disabled:opacity-60"
        >
          {head} <Estimate asset={single} />
          {pending ? <Spinner className="size-4" /> : <ChevronRightIcon className="size-4 text-muted-foreground" aria-hidden />}
        </button>
      </li>
    );
  }

  return (
    <li>
      <button
        type="button"
        aria-expanded={open}
        aria-controls={panelId}
        onClick={onToggle}
        className="flex w-full items-center gap-3 px-4 py-3.5 text-left transition-colors outline-none hover:bg-muted/60 focus-visible:bg-muted/60 focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:ring-inset"
      >
        {head}
        <ChevronDownIcon
          className={cn("size-4 text-muted-foreground transition-transform", open && "rotate-180")}
          aria-hidden
        />
      </button>
      <div id={panelId} hidden={!open}>
        <p className="sr-only">{t("checkout.select.chooseNetwork", { symbol: group.symbol })}</p>
        <ul className="space-y-1 bg-muted/35 px-2 pt-1 pb-2">
          {group.assets.map((a) => {
            const pending = pendingAssetId === a.id;
            return (
              <li key={a.id}>
                <button
                  type="button"
                  disabled={disabled}
                  onClick={() => onSelect(a)}
                  className="flex w-full items-center gap-3 rounded-lg px-3 py-2.5 text-left transition-colors outline-none hover:bg-card focus-visible:bg-card focus-visible:ring-3 focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-60"
                >
                  <span className="min-w-0 flex-1">
                    <span className="flex flex-wrap items-center gap-x-1.5 gap-y-1 text-sm font-medium">
                      <NetworkName asset={a} />
                      {a.is_testnet && !group.allTestnet && (
                        <>
                          {" "}
                          <TestnetBadge />
                        </>
                      )}
                    </span>
                  </span>{" "}
                  <Estimate asset={a} />
                  {pending ? (
                    <Spinner className="size-4" />
                  ) : (
                    <ChevronRightIcon className="size-4 text-muted-foreground" aria-hidden />
                  )}
                </button>
              </li>
            );
          })}
        </ul>
      </div>
    </li>
  );
}
