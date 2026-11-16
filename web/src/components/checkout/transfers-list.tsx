"use client";

import { ArrowUpRightIcon, CircleCheckIcon, CircleDashedIcon, CircleSlashIcon } from "lucide-react";
import type { Transfer } from "@/lib/api/types";
import { explorerTxUrl } from "@/lib/explorer";
import { formatAmount } from "@/lib/format/amount";
import { shortenHash } from "@/lib/format/address";
import { useT } from "@/lib/i18n";
import { cn } from "@/lib/utils";

/** Confirmation meter: discrete ticks for short requirements, a bar otherwise. */
function ConfirmationMeter({ n, required, status }: { n: number; required: number; status: Transfer["status"] }) {
  const done = status === "confirmed" ? required : Math.min(n, required);
  const pct = required > 0 ? (done / required) * 100 : 100;
  if (required > 0 && required <= 24) {
    return (
      <div className="flex gap-[3px]" aria-hidden>
        {Array.from({ length: required }, (_, i) => (
          <span
            key={i}
            className={cn("h-1.5 flex-1 rounded-full bg-foreground/10", i < done && "bg-primary")}
          />
        ))}
      </div>
    );
  }
  return (
    <div className="h-1.5 overflow-hidden rounded-full bg-foreground/10" aria-hidden>
      <div className="h-full rounded-full bg-primary transition-[width] duration-700" style={{ width: `${pct}%` }} />
    </div>
  );
}

export function TransfersList({ transfers, className }: { transfers: Transfer[]; className?: string }) {
  const t = useT();
  if (transfers.length === 0) return null;
  return (
    <section aria-labelledby="transfers-title" className={cn("space-y-3", className)}>
      <h3 id="transfers-title" className="text-xs font-semibold tracking-[0.12em] text-muted-foreground uppercase">
        {t("checkout.transfers.title")}
      </h3>
      <ul className="space-y-2.5">
        {transfers.map((tr) => {
          const url = tr.explorer_url || explorerTxUrl(tr.chain_id, tr.tx_hash);
          const confirmations = Math.min(tr.confirmations, tr.required_confirmations);
          const Icon =
            tr.status === "confirmed" ? CircleCheckIcon : tr.status === "orphaned" ? CircleSlashIcon : CircleDashedIcon;
          return (
            <li key={tr.id} className="space-y-2.5 rounded-xl border bg-background/50 p-3.5">
              <div className="flex items-center justify-between gap-3">
                <p className="font-mono text-sm font-semibold tabular" translate="no">
                  {formatAmount(tr.amount)} <span className="font-normal text-muted-foreground">{tr.symbol}</span>
                </p>
                <span
                  className={cn(
                    "inline-flex items-center gap-1 text-xs font-medium",
                    tr.status === "confirmed" && "text-success",
                    tr.status === "pending" && "text-warning",
                    tr.status === "orphaned" && "text-destructive",
                  )}
                >
                  <Icon className={cn("size-3.5", tr.status === "pending" && "animate-soft-pulse")} aria-hidden />
                  {t(`checkout.transfers.${tr.status}`)}
                </span>
              </div>
              {tr.status !== "orphaned" && (
                <div className="space-y-1.5">
                  <ConfirmationMeter n={tr.confirmations} required={tr.required_confirmations} status={tr.status} />
                  <p className="text-xs text-muted-foreground tabular">
                    {t("checkout.transfers.confirmations", {
                      n: tr.status === "confirmed" ? tr.required_confirmations : confirmations,
                      required: tr.required_confirmations,
                    })}
                  </p>
                </div>
              )}
              <div className="flex items-center justify-between gap-3 text-xs">
                <span className="font-mono text-muted-foreground" translate="no" title={tr.tx_hash}>
                  <span className="sr-only">{t("checkout.transfers.txHash")}: </span>
                  {shortenHash(tr.tx_hash)}
                </span>
                {url && (
                  <a
                    href={url}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="inline-flex items-center gap-0.5 font-medium text-primary underline-offset-4 hover:underline focus-visible:underline"
                  >
                    {t("checkout.transfers.viewOnExplorer")}
                    <ArrowUpRightIcon className="size-3.5" aria-hidden />
                    <span className="sr-only">{t("common.externalLink")}</span>
                  </a>
                )}
              </div>
            </li>
          );
        })}
      </ul>
    </section>
  );
}
