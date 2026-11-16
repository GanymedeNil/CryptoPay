"use client";

import { InfoIcon } from "lucide-react";
import Link from "next/link";
import { useId, useState } from "react";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Spinner } from "@/components/ui/spinner";
import { splitByNetwork, walletsForFamily } from "@/lib/admin/chains";
import { describeError } from "@/lib/admin/errors";
import { adminKeys, useAdminMutation, useChains, useWallets } from "@/lib/admin/queries";
import { adminApi } from "@/lib/api/admin";
import { isApiError } from "@/lib/api/client";
import type { Chain, MerchantWallet, Wallet } from "@/lib/api/types";
import { useT, type MessageKey } from "@/lib/i18n";
import { cn } from "@/lib/utils";
import { RoleTooltip, useAuth } from "../auth";
import { Section } from "../page-header";
import { ErrorState } from "../states";
import { NetworkBadge, StatusBadge, type Tone } from "../status-badge";

/** Select value meaning "no merchant wallet: use the chain's default". */
export const USE_DEFAULT = "__default__";

export type EffectiveSource = "own" | "default" | "none";

/**
 * The wallet the merchant is paid to on a chain (docs/architecture.md,
 * "Receiving wallets"): its own binding, else the chain's default wallet,
 * else none.
 */
export function effectiveWallet(
  chain: Chain,
  binding: MerchantWallet | undefined,
  wallets: readonly Wallet[] | undefined,
): { source: EffectiveSource; name: string | null } {
  if (binding) return { source: "own", name: binding.wallet_name };
  if (chain.wallet_id) return { source: "default", name: wallets?.find((w) => w.id === chain.wallet_id)?.name ?? null };
  return { source: "none", name: null };
}

/** Enabled chains plus disabled ones the merchant still has a wallet on (so it can be removed). */
export function merchantWalletChains(chains: readonly Chain[], bindings: readonly MerchantWallet[]): Chain[] {
  return chains.filter((c) => c.enabled || bindings.some((b) => b.chain_id === c.id));
}

const SOURCE_TONE: Record<EffectiveSource, Tone> = { own: "info", default: "neutral", none: "warning" };

export function MerchantWallets({ merchantId, bindings, className }: { merchantId: string; bindings: MerchantWallet[]; className?: string }) {
  const t = useT();
  const chains = useChains({ refetchInterval: false });
  const wallets = useWallets();
  const list = merchantWalletChains(chains.data ?? [], bindings);
  const { mainnet, testnet } = splitByNetwork(list);

  return (
    <Section className={className} title={t("admin.merchant.wallets.title")} description={t("admin.merchant.wallets.description")}>
      <div className="mb-4 flex gap-2.5 rounded-lg border bg-muted/40 p-3 text-xs leading-relaxed text-pretty text-muted-foreground">
        <InfoIcon className="mt-0.5 size-4 shrink-0 text-foreground/70" aria-hidden />
        <div className="space-y-1">
          <p>{t("admin.merchant.wallets.explainer")}</p>
          <p className="font-medium text-foreground/80">{t("admin.merchant.wallets.existingNote")}</p>
        </div>
      </div>
      {chains.error && !chains.data ? (
        <ErrorState error={chains.error} onRetry={() => void chains.refetch()} />
      ) : !chains.data ? (
        <div className="space-y-2" aria-busy="true">
          {Array.from({ length: 3 }, (_, i) => (
            <Skeleton key={i} className="h-14 w-full" />
          ))}
        </div>
      ) : list.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t("admin.merchant.wallets.empty")}</p>
      ) : (
        <div className="space-y-5">
          {[
            { key: "mainnet", label: t("admin.chains.mainnet"), items: mainnet },
            { key: "testnet", label: t("admin.chains.testnet"), items: testnet },
          ]
            .filter((g) => g.items.length > 0)
            .map((g) => (
              <section key={g.key} aria-labelledby={`merchant-wallets-${g.key}`}>
                <h3 id={`merchant-wallets-${g.key}`} className="mb-2 font-heading text-xs font-semibold tracking-wide text-muted-foreground uppercase">
                  {g.label}
                </h3>
                <ul className="divide-y rounded-lg border">
                  {g.items.map((c) => (
                    <MerchantWalletRow
                      key={c.id}
                      merchantId={merchantId}
                      chain={c}
                      binding={bindings.find((b) => b.chain_id === c.id)}
                      wallets={wallets.data}
                    />
                  ))}
                </ul>
              </section>
            ))}
        </div>
      )}
    </Section>
  );
}

function MerchantWalletRow({
  merchantId,
  chain: c,
  binding,
  wallets,
}: {
  merchantId: string;
  chain: Chain;
  binding: MerchantWallet | undefined;
  wallets: Wallet[] | undefined;
}) {
  const t = useT();
  const { canWrite } = useAuth();
  const selectId = useId();
  const [error, setError] = useState<string | null>(null);
  const effective = effectiveWallet(c, binding, wallets);
  const defaultName = c.wallet_id ? (wallets?.find((w) => w.id === c.wallet_id)?.name ?? "…") : t("admin.merchant.wallets.defaultMissing");
  const options = walletsForFamily(wallets, c.family);
  const family = t(`admin.families.${c.family}` as MessageKey);
  const save = useAdminMutation<MerchantWallet | void, string | null>({
    mutationFn: (walletId) =>
      walletId ? adminApi.setMerchantWallet(merchantId, c.id, { wallet_id: walletId }) : adminApi.unsetMerchantWallet(merchantId, c.id),
    invalidate: [adminKeys.merchants.detail(merchantId), adminKeys.wallets, adminKeys.chains],
    successMessage: (_, walletId) =>
      walletId ? t("admin.merchant.wallets.setToast", { chain: c.name }) : t("admin.merchant.wallets.unsetToast", { chain: c.name }),
    toastError: false,
    onError: (e) => setError(isApiError(e) && e.fields?.wallet_id ? e.fields.wallet_id : describeError(t, e)),
  });
  const badge =
    effective.source === "own"
      ? t("admin.merchant.wallets.badgeOwn")
      : effective.source === "default"
        ? t("admin.merchant.wallets.badgeDefault")
        : t("admin.merchant.wallets.badgeNone");

  return (
    <li className="grid gap-2 p-3 sm:grid-cols-[minmax(0,1fr)_18rem] sm:items-center sm:gap-4" data-testid={`merchant-wallet-${c.id}`}>
      <div className="min-w-0 space-y-1">
        <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
          <span className={cn("text-sm font-medium", !c.enabled && "text-muted-foreground")}>{c.name}</span>
          <span className="rounded border px-1 font-mono text-[10px] text-muted-foreground uppercase">{family}</span>
          <NetworkBadge testnet={c.is_testnet} />
          {!c.enabled && <span className="text-xs text-muted-foreground">{t("admin.merchant.wallets.chainDisabled")}</span>}
        </div>
        <div className="flex flex-wrap items-center gap-2 text-xs">
          <StatusBadge tone={SOURCE_TONE[effective.source]}>{badge}</StatusBadge>
          {effective.name && <span className="truncate text-muted-foreground">{effective.name}</span>}
        </div>
      </div>
      <div className="space-y-1">
        <RoleTooltip blocked={!canWrite} reason={t("admin.access.readOnly")} className="w-full">
          <Select
            value={binding?.wallet_id ?? USE_DEFAULT}
            onValueChange={(v) => {
              setError(null);
              save.mutate(v === USE_DEFAULT ? null : v);
            }}
            disabled={!canWrite || save.isPending || !wallets}
          >
            <SelectTrigger
              id={selectId}
              className="w-full"
              aria-label={t("admin.merchant.wallets.selectLabel", { chain: c.name })}
              aria-invalid={!!error}
              aria-describedby={error ? `${selectId}-error` : undefined}
            >
              {save.isPending && <Spinner data-icon="inline-start" />}
              <SelectValue />
            </SelectTrigger>
            <SelectContent position="popper">
              <SelectItem value={USE_DEFAULT}>{t("admin.merchant.wallets.useDefault", { name: defaultName })}</SelectItem>
              {binding && !options.some((w) => w.id === binding.wallet_id) && (
                <SelectItem value={binding.wallet_id}>{binding.wallet_name}</SelectItem>
              )}
              {options.map((w) => (
                <SelectItem key={w.id} value={w.id}>
                  {w.name} · {t(`admin.walletKinds.${w.kind}` as MessageKey)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </RoleTooltip>
        {error && (
          <p id={`${selectId}-error`} role="alert" className="text-xs text-destructive">
            {error}
          </p>
        )}
        {wallets && options.length === 0 && (
          <p className="text-xs text-muted-foreground">
            {t("admin.merchant.wallets.noWalletsForFamily", { family })}{" "}
            <Link href="/admin/wallets" className="text-primary underline-offset-4 hover:underline">
              {t("admin.merchant.wallets.createWallet")}
            </Link>
          </p>
        )}
      </div>
    </li>
  );
}
