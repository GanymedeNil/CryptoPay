"use client";

import { zodResolver } from "@hookform/resolvers/zod";
import { ChevronDownIcon, CircleCheckIcon, CircleSlashIcon, TriangleAlertIcon, UsersIcon } from "lucide-react";
import Link from "next/link";
import { useId, useState } from "react";
import { useForm, useWatch } from "react-hook-form";
import { z } from "zod";
import { Button } from "@/components/ui/button";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Spinner } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";
import { chainHealth, notPayableReasons, splitByNetwork, walletsForFamily } from "@/lib/admin/chains";
import { applyFieldErrors } from "@/lib/admin/errors";
import { adminKeys, useAdminMutation, useChains, useOptimisticItemMutation, useWallets } from "@/lib/admin/queries";
import { formatCount, formatMillis } from "@/lib/format/units";
import { adminApi } from "@/lib/api/admin";
import type { Chain, UpdateChainRequest, Wallet } from "@/lib/api/types";
import { useLocale, useT, type MessageKey } from "@/lib/i18n";
import type { TranslateFn } from "@/lib/i18n/types";
import { cn } from "@/lib/utils";
import { RoleTooltip, useAuth } from "../auth";
import { describedBy, FormField } from "../form-field";
import { PageHeader } from "../page-header";
import { ErrorState } from "../states";
import { NetworkBadge, StatusBadge } from "../status-badge";
import { RelativeTime } from "../time";
import { RpcEditor, RpcSourceBadge } from "./rpc-editor";

export function ChainsPage() {
  const t = useT();
  const chains = useChains({ refetchInterval: 15_000 });
  const wallets = useWallets();
  return (
    <>
      <PageHeader title={t("admin.chains.title")} description={t("admin.chains.description")} />
      {chains.error && !chains.data ? (
        <ErrorState error={chains.error} onRetry={() => void chains.refetch()} />
      ) : !chains.data ? (
        <div className="space-y-2">
          {Array.from({ length: 6 }, (_, i) => (
            <Skeleton key={i} className="h-20 w-full" />
          ))}
        </div>
      ) : (
        <ChainGroups chains={chains.data} wallets={wallets.data} />
      )}
    </>
  );
}

function ChainGroups({ chains, wallets }: { chains: Chain[]; wallets: Wallet[] | undefined }) {
  const t = useT();
  const { mainnet, testnet } = splitByNetwork(chains);
  return (
    <div className="space-y-8">
      {[
        { key: "mainnet", label: t("admin.chains.mainnet"), items: mainnet },
        { key: "testnet", label: t("admin.chains.testnet"), items: testnet },
      ]
        .filter((g) => g.items.length > 0)
        .map((g) => (
          <section key={g.key} aria-labelledby={`chains-${g.key}`}>
            <h2 id={`chains-${g.key}`} className="mb-2 flex items-baseline gap-2 font-heading text-sm font-semibold tracking-wide text-muted-foreground uppercase">
              {g.label}
              <span className="font-mono text-xs font-normal tabular">{g.items.length}</span>
            </h2>
            <ul className="space-y-2">
              {g.items.map((c) => (
                <ChainRow key={c.id} chain={c} wallets={wallets} />
              ))}
            </ul>
          </section>
        ))}
    </div>
  );
}

const HEALTH_TONE = { ok: "success", idle: "muted", lagging: "warning", error: "danger", disabled: "muted" } as const;

function ChainRow({ chain: c, wallets }: { chain: Chain; wallets: Wallet[] | undefined }) {
  const t = useT();
  const { locale } = useLocale();
  const { canWrite } = useAuth();
  const [open, setOpen] = useState(false);
  const switchId = useId();
  const health = chainHealth(c);
  const reasons = notPayableReasons(c);
  const toggle = useOptimisticItemMutation<Chain, boolean>({
    queryKey: adminKeys.chains,
    id: () => c.id,
    apply: (x, enabled) => ({ ...x, enabled }),
    mutationFn: (enabled) => adminApi.updateChain(c.id, { enabled }),
    successMessage: (x) => (x.enabled ? t("admin.chains.enabledToast", { name: x.name }) : t("admin.chains.disabledToast", { name: x.name })),
    invalidate: [adminKeys.overview.all],
  });
  const defaultRpcWarning = !c.is_testnet && c.rpc_source === "default" && c.enabled;

  return (
    <li className={cn("rounded-xl border bg-card", !c.enabled && "bg-card/60")}>
      <Collapsible open={open} onOpenChange={setOpen}>
        <div className="flex flex-col gap-3 p-3 sm:p-4 lg:flex-row lg:items-center">
          <div className="flex min-w-0 flex-1 items-start gap-3">
            <div className="min-w-0 flex-1">
              <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                <h3 className={cn("font-heading text-[15px] font-semibold", !c.enabled && "text-muted-foreground")}>{c.name}</h3>
                <span className="rounded border px-1 font-mono text-[10px] text-muted-foreground uppercase">{t(`admin.families.${c.family}` as MessageKey)}</span>
                <NetworkBadge testnet={c.is_testnet} />
                {c.network_id && <span className="font-mono text-[11px] text-muted-foreground">{t("admin.chains.networkId", { id: c.network_id })}</span>}
              </div>
              <div className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1.5 text-xs text-muted-foreground">
                {c.payable ? (
                  <span className="inline-flex items-center gap-1 text-success">
                    <CircleCheckIcon className="size-3.5" aria-hidden />
                    {t("admin.chains.payable")}
                  </span>
                ) : (
                  <span className="inline-flex items-center gap-1 text-warning">
                    <CircleSlashIcon className="size-3.5" aria-hidden />
                    {t("admin.chains.notPayable")}
                    {reasons.length > 0 && (
                      <span className="-ml-1">
                        {locale === "zh" ? "：" : ": "}
                        {reasons.map((r) => t(`admin.chains.reason${r[0].toUpperCase()}${r.slice(1)}` as MessageKey)).join(locale === "zh" ? "、" : ", ")}
                      </span>
                    )}
                  </span>
                )}
                {c.merchant_wallets > 0 && (
                  <span className="inline-flex items-center gap-1 text-foreground/80" data-testid="merchant-wallets">
                    <UsersIcon className="size-3.5" aria-hidden />
                    {!c.payable && c.enabled
                      ? t("admin.chains.merchantWalletsPayable", { n: formatCount(c.merchant_wallets, locale) })
                      : t("admin.chains.merchantWallets", { n: formatCount(c.merchant_wallets, locale) })}
                  </span>
                )}
                {c.enabled && (
                  <StatusBadge tone={HEALTH_TONE[health]}>
                    {health === "error"
                      ? t("admin.overview.chainHealth.error")
                      : c.lag > 0
                        ? t("admin.overview.chainHealth.lagBlocks", { n: formatCount(c.lag, locale) })
                        : t("admin.overview.chainHealth.synced")}
                  </StatusBadge>
                )}
                <RpcSourceBadge chain={c} />
              </div>
            </div>
          </div>
          <div className="flex items-center justify-between gap-3 lg:justify-end">
            <div className="flex items-center gap-2">
              <RoleTooltip blocked={!canWrite} reason={t("admin.access.readOnly")}>
                <Switch
                  id={switchId}
                  checked={c.enabled}
                  disabled={!canWrite || toggle.isPending}
                  onCheckedChange={(v) => toggle.mutate(v)}
                  aria-label={t("admin.chains.enableLabel", { name: c.name })}
                />
              </RoleTooltip>
              <label htmlFor={switchId} className="text-sm" aria-hidden>
                {t("admin.chains.enabled")}
              </label>
            </div>
            <CollapsibleTrigger asChild>
              <Button variant="outline" size="sm" aria-expanded={open}>
                {open ? t("admin.chains.hideConfig") : t("admin.chains.configure")}
                <ChevronDownIcon className={cn("transition-transform", open && "rotate-180")} data-icon="inline-end" aria-hidden />
              </Button>
            </CollapsibleTrigger>
          </div>
        </div>
        {defaultRpcWarning && !open && (
          <p className="mx-3 mb-3 flex items-center gap-1.5 rounded-md border border-warning-border bg-warning-surface px-2.5 py-1.5 text-xs text-warning sm:mx-4">
            <TriangleAlertIcon className="size-3.5 shrink-0" aria-hidden />
            {t("admin.chains.rpc.defaultWarningTitle")}
          </p>
        )}
        <CollapsibleContent>
          <div className="grid gap-5 border-t p-3 sm:p-4 lg:grid-cols-3">
            <ScannerStatus chain={c} />
            <RpcEditor chain={c} />
            <div className="space-y-5">
              <WalletAssignment chain={c} wallets={wallets} />
              <ScanParams key={`${c.confirmations}-${c.poll_interval_ms}-${c.scan_batch}`} chain={c} />
            </div>
          </div>
        </CollapsibleContent>
      </Collapsible>
    </li>
  );
}

function ScannerStatus({ chain: c }: { chain: Chain }) {
  const t = useT();
  const { locale } = useLocale();
  const rows: [string, React.ReactNode][] = [
    [t("admin.chains.scanner.cursor"), <span key="c" className="font-mono tabular">{formatCount(c.scan_cursor, locale)}</span>],
    [t("admin.chains.scanner.head"), <span key="h" className="font-mono tabular">{formatCount(c.last_head, locale)}</span>],
    [
      t("admin.chains.scanner.lag"),
      <span key="l" className={cn("font-mono tabular", c.lag > c.confirmations && "text-warning")}>
        {t("admin.chains.scanner.lagBlocks", { n: formatCount(c.lag, locale) })}
      </span>,
    ],
    [t("admin.chains.scanner.lastScan"), <RelativeTime key="s" iso={c.last_scan_at} fallback={t("admin.chains.scanner.never")} />],
  ];
  return (
    <div className="space-y-3">
      <h4 className="text-sm font-medium">{t("admin.chains.scanner.title")}</h4>
      <dl className="grid grid-cols-2 gap-x-4 gap-y-2 text-sm">
        {rows.map(([k, v]) => (
          <div key={k} className="space-y-0.5">
            <dt className="text-xs text-muted-foreground">{k}</dt>
            <dd>{v}</dd>
          </div>
        ))}
      </dl>
      <div className="space-y-1">
        <p className="text-xs text-muted-foreground">{t("admin.chains.scanner.lastError")}</p>
        {c.last_error ? (
          <p className="rounded-md border border-destructive/30 bg-destructive/5 px-2.5 py-1.5 font-mono text-xs break-all text-destructive">{c.last_error}</p>
        ) : (
          <p className="text-xs text-success">{t("admin.chains.scanner.healthy")}</p>
        )}
      </div>
    </div>
  );
}

const NONE = "__none__";

function WalletAssignment({ chain: c, wallets }: { chain: Chain; wallets: Wallet[] | undefined }) {
  const t = useT();
  const { locale } = useLocale();
  const { canWrite } = useAuth();
  const selectId = useId();
  const options = walletsForFamily(wallets, c.family);
  const family = t(`admin.families.${c.family}` as MessageKey);
  const save = useAdminMutation({
    mutationFn: (walletId: string | null) => adminApi.updateChain(c.id, { wallet_id: walletId }),
    invalidate: [adminKeys.chains, adminKeys.wallets, adminKeys.overview.all],
    successMessage: t("admin.chains.wallet.saved"),
  });
  return (
    <div className="space-y-2">
      <h4 className="text-sm font-medium">{t("admin.chains.wallet.title")}</h4>
      <RoleTooltip blocked={!canWrite} reason={t("admin.access.readOnly")} className="w-full">
        <Select
          value={c.wallet_id ?? NONE}
          onValueChange={(v) => save.mutate(v === NONE ? null : v)}
          disabled={!canWrite || save.isPending || !wallets}
        >
          <SelectTrigger id={selectId} className="w-full" aria-label={t("admin.chains.wallet.label")} aria-describedby={`${selectId}-hint`}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent position="popper">
            <SelectItem value={NONE}>{t("admin.chains.wallet.none")}</SelectItem>
            {options.map((w) => (
              <SelectItem key={w.id} value={w.id}>
                {w.name} · {t(`admin.walletKinds.${w.kind}` as MessageKey)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </RoleTooltip>
      <p id={`${selectId}-hint`} className="text-xs text-muted-foreground">
        {options.length === 0 && wallets ? (
          <>
            {t("admin.chains.wallet.noneForFamily", { family })}{" "}
            <Link href="/admin/wallets" className="text-primary underline-offset-4 hover:underline">
              {t("admin.chains.wallet.createWallet")}
            </Link>
          </>
        ) : (
          t("admin.chains.wallet.hint", { family })
        )}
      </p>
      {c.merchant_wallets > 0 && (
        <p className="text-xs text-muted-foreground">{t("admin.chains.wallet.merchantHint", { n: formatCount(c.merchant_wallets, locale) })}</p>
      )}
    </div>
  );
}

const LIMITS = {
  confirmations: [1, 1000],
  poll_interval_ms: [200, 600_000],
  scan_batch: [1, 10_000],
} as const;

function paramsSchema(t: TranslateFn) {
  const int = (key: keyof typeof LIMITS) =>
    z
      .string()
      .trim()
      .refine((v) => /^\d+$/.test(v), t("admin.chains.params.integer"))
      .refine(
        (v) => !/^\d+$/.test(v) || (Number(v) >= LIMITS[key][0] && Number(v) <= LIMITS[key][1]),
        t("admin.chains.params.range", { min: LIMITS[key][0], max: LIMITS[key][1] }),
      );
  return z.object({ confirmations: int("confirmations"), poll_interval_ms: int("poll_interval_ms"), scan_batch: int("scan_batch") });
}
type ParamValues = z.infer<ReturnType<typeof paramsSchema>>;

function ScanParams({ chain: c }: { chain: Chain }) {
  const t = useT();
  const { locale } = useLocale();
  const { canWrite } = useAuth();
  const base = useId();
  const form = useForm<ParamValues>({
    resolver: zodResolver(paramsSchema(t)),
    defaultValues: {
      confirmations: String(c.confirmations),
      poll_interval_ms: String(c.poll_interval_ms),
      scan_batch: String(c.scan_batch),
    },
  });
  const errors = form.formState.errors;
  const save = useAdminMutation({
    mutationFn: (body: UpdateChainRequest) => adminApi.updateChain(c.id, body),
    invalidate: [adminKeys.chains],
    successMessage: t("admin.chains.params.saved"),
    onError: (e) => applyFieldErrors(e, form.setError, ["confirmations", "poll_interval_ms", "scan_batch"]),
  });
  const poll = Number(useWatch({ control: form.control, name: "poll_interval_ms" }));
  const fields: { name: keyof ParamValues; label: string; hint: string; suffix?: string }[] = [
    { name: "confirmations", label: t("admin.chains.params.confirmations"), hint: t("admin.chains.params.confirmationsHint") },
    {
      name: "poll_interval_ms",
      label: t("admin.chains.params.pollInterval"),
      hint: t("admin.chains.params.pollIntervalHint"),
      suffix: Number.isFinite(poll) && poll > 0 ? `= ${formatMillis(poll, locale)}` : undefined,
    },
    { name: "scan_batch", label: t("admin.chains.params.scanBatch"), hint: t("admin.chains.params.scanBatchHint") },
  ];
  return (
    <form
      noValidate
      onSubmit={form.handleSubmit((v) => {
        const body: UpdateChainRequest = {};
        if (Number(v.confirmations) !== c.confirmations) body.confirmations = Number(v.confirmations);
        if (Number(v.poll_interval_ms) !== c.poll_interval_ms) body.poll_interval_ms = Number(v.poll_interval_ms);
        if (Number(v.scan_batch) !== c.scan_batch) body.scan_batch = Number(v.scan_batch);
        if (Object.keys(body).length) save.mutate(body);
      })}
      className="space-y-3"
    >
      <h4 className="text-sm font-medium">{t("admin.chains.params.title")}</h4>
      <fieldset disabled={!canWrite} className="space-y-3">
        {fields.map((f) => {
          const id = `${base}-${f.name}`;
          const err = errors[f.name]?.message;
          return (
            <FormField key={f.name} label={f.label} htmlFor={id} description={f.hint} error={err}>
              <div className="flex items-center gap-2">
                <Input
                  id={id}
                  inputMode="numeric"
                  className="w-32 font-mono tabular"
                  aria-invalid={!!err}
                  aria-describedby={describedBy(id, { description: true, error: err })}
                  {...form.register(f.name)}
                />
                {f.name === "poll_interval_ms" && <span className="text-xs text-muted-foreground">{t("admin.chains.params.ms")}</span>}
                {f.suffix && <span className="text-xs text-muted-foreground tabular">{f.suffix}</span>}
              </div>
            </FormField>
          );
        })}
        {canWrite && (
          <Button type="submit" size="sm" disabled={!form.formState.isDirty || save.isPending}>
            {save.isPending && <Spinner data-icon="inline-start" />}
            {t("admin.common.saveChanges")}
          </Button>
        )}
      </fieldset>
    </form>
  );
}
