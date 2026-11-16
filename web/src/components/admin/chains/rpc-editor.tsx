"use client";

import { useMutation } from "@tanstack/react-query";
import { ActivityIcon, CircleCheckIcon, CircleXIcon, PencilIcon, PlusIcon, RotateCcwIcon, TrashIcon, TriangleAlertIcon } from "lucide-react";
import { useId, useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Spinner } from "@/components/ui/spinner";
import { isMaskedRpcUrl, isRpcUrl, rpcEnvName } from "@/lib/admin/chains";
import { describeError } from "@/lib/admin/errors";
import { adminKeys, useAdminMutation } from "@/lib/admin/queries";
import { formatCount } from "@/lib/format/units";
import { adminApi } from "@/lib/api/admin";
import type { Chain, TestRpcResult } from "@/lib/api/types";
import { useLocale, useT, type MessageKey } from "@/lib/i18n";
import { GatedButton, useCanWrite } from "../auth";
import { ConfirmDialog } from "../confirm-dialog";
import { StatusBadge, type Tone } from "../status-badge";

const SOURCE_TONE: Record<Chain["rpc_source"], Tone> = { database: "info", env: "neutral", default: "warning" };
const SOURCE_LABEL: Record<Chain["rpc_source"], MessageKey> = {
  database: "admin.chains.rpc.sourceDatabase",
  env: "admin.chains.rpc.sourceEnv",
  default: "admin.chains.rpc.sourceDefault",
};

export function RpcSourceBadge({ chain }: { chain: Chain }) {
  const t = useT();
  const hint =
    chain.rpc_source === "env"
      ? t("admin.chains.rpc.sourceEnvHint", { env: rpcEnvName(chain.id) })
      : chain.rpc_source === "database"
        ? t("admin.chains.rpc.sourceDatabaseHint")
        : t("admin.chains.rpc.sourceDefaultHint");
  return (
    <StatusBadge tone={SOURCE_TONE[chain.rpc_source]} hint={hint} dot={false}>
      {t(SOURCE_LABEL[chain.rpc_source])}
    </StatusBadge>
  );
}

/** Result line of a "test RPC" call. */
export function TestResultLine({ result, error }: { result?: TestRpcResult; error?: unknown }) {
  const t = useT();
  const { locale } = useLocale();
  if (error) {
    return (
      <p className="flex items-start gap-1.5 text-xs text-destructive" role="status">
        <CircleXIcon className="mt-px size-3.5 shrink-0" aria-hidden />
        {t("admin.chains.rpc.testFailed", { error: describeError(t, error) })}
      </p>
    );
  }
  if (!result) return null;
  return result.ok ? (
    <p className="flex items-start gap-1.5 text-xs text-success" role="status">
      <CircleCheckIcon className="mt-px size-3.5 shrink-0" aria-hidden />
      <span className="tabular">
        {t("admin.chains.rpc.testOk", { head: formatCount(result.head, locale), latency: formatCount(result.latency_ms, locale) })}
      </span>
    </p>
  ) : (
    <p className="flex items-start gap-1.5 text-xs break-all text-destructive" role="status">
      <CircleXIcon className="mt-px size-3.5 shrink-0" aria-hidden />
      {t("admin.chains.rpc.testFailed", { error: result.error || "—" })}
    </p>
  );
}

function useTestRpc(chainId: string) {
  return useMutation({
    mutationFn: (rpcUrl?: string) => adminApi.testChainRpc(chainId, rpcUrl ? { rpc_url: rpcUrl } : {}),
  });
}

/** Validates the editor rows; returns per-row error keys and the clean list. */
export function validateRpcRows(rows: string[]): { errors: (MessageKey | null)[]; urls: string[] } {
  const seen = new Set<string>();
  const errors = rows.map((raw): MessageKey | null => {
    const v = raw.trim();
    if (!v) return null;
    if (isMaskedRpcUrl(v)) return "admin.chains.rpc.urlMasked";
    if (!isRpcUrl(v)) return "admin.chains.rpc.urlInvalid";
    if (seen.has(v)) return "admin.chains.rpc.duplicate";
    seen.add(v);
    return null;
  });
  return { errors, urls: rows.map((r) => r.trim()).filter(Boolean) };
}

/**
 * RPC endpoints of one chain. The API returns them masked (scheme + host),
 * so editing starts from an empty list and *replaces* the stored one; an
 * empty list ("reset to default") falls back to env / the free default.
 */
export function RpcEditor({ chain }: { chain: Chain }) {
  const t = useT();
  const canWrite = useCanWrite();
  const base = useId();
  const [editing, setEditing] = useState(false);
  const [rows, setRows] = useState<string[]>([""]);
  const [submitted, setSubmitted] = useState(false);
  const [resetOpen, setResetOpen] = useState(false);
  const [rowTests, setRowTests] = useState<Record<number, { result?: TestRpcResult; error?: unknown; pending?: boolean }>>({});
  const testCurrent = useTestRpc(chain.id);
  const testRow = useTestRpc(chain.id);

  const save = useAdminMutation({
    mutationFn: (urls: string[]) => adminApi.updateChain(chain.id, { rpc_urls: urls }),
    invalidate: [adminKeys.chains],
    successMessage: (_c, urls) => (urls.length ? t("admin.chains.rpc.saved") : t("admin.chains.rpc.resetDone")),
    onSuccess: () => {
      setEditing(false);
      setRows([""]);
      setSubmitted(false);
      setRowTests({});
      // The old result described the endpoints that were just replaced.
      testCurrent.reset();
    },
  });

  const { errors, urls } = validateRpcRows(rows);
  const hasErrors = errors.some(Boolean);
  const showDefaultWarning = !chain.is_testnet && chain.rpc_source === "default";

  async function runRowTest(i: number) {
    const url = rows[i]?.trim();
    if (!url || errors[i]) return;
    setRowTests((s) => ({ ...s, [i]: { pending: true } }));
    try {
      const result = await testRow.mutateAsync(url);
      setRowTests((s) => ({ ...s, [i]: { result } }));
    } catch (error) {
      setRowTests((s) => ({ ...s, [i]: { error } }));
    }
  }

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h4 className="text-sm font-medium">{t("admin.chains.rpc.title")}</h4>
        <RpcSourceBadge chain={chain} />
      </div>

      {showDefaultWarning && (
        <div className="flex gap-2 rounded-lg border border-warning-border bg-warning-surface p-2.5 text-xs text-warning" role="note">
          <TriangleAlertIcon className="mt-px size-4 shrink-0" aria-hidden />
          <div>
            <p className="font-semibold">{t("admin.chains.rpc.defaultWarningTitle")}</p>
            <p className="mt-0.5 text-pretty">{t("admin.chains.rpc.defaultWarning")}</p>
          </div>
        </div>
      )}

      {!editing && (
        <>
          <ul className="space-y-1" aria-label={t("admin.chains.rpc.title")}>
            {chain.rpc_urls.map((u, i) => (
              <li key={i} className="flex items-center gap-2 rounded-md border bg-muted/40 px-2 py-1.5 font-mono text-xs break-all">
                <span className="text-muted-foreground tabular">{i + 1}</span>
                {u}
              </li>
            ))}
            {chain.rpc_urls.length === 0 && <li className="text-xs text-muted-foreground">{t("admin.chains.reasonNoRpc")}</li>}
          </ul>
          <p className="text-[11px] text-muted-foreground">{t("admin.chains.rpc.masked")}</p>
          <div className="flex flex-wrap items-center gap-2">
            <GatedButton size="sm" variant="outline" onClick={() => testCurrent.mutate(undefined)} disabled={testCurrent.isPending || chain.rpc_urls.length === 0}>
              {testCurrent.isPending ? <Spinner data-icon="inline-start" /> : <ActivityIcon data-icon="inline-start" aria-hidden />}
              {testCurrent.isPending ? t("admin.chains.rpc.testing") : t("admin.chains.rpc.testCurrent")}
            </GatedButton>
            <GatedButton size="sm" variant="outline" onClick={() => setEditing(true)}>
              <PencilIcon data-icon="inline-start" aria-hidden />
              {t("admin.chains.rpc.edit")}
            </GatedButton>
            {chain.rpc_source === "database" && (
              <GatedButton size="sm" variant="ghost" onClick={() => setResetOpen(true)}>
                <RotateCcwIcon data-icon="inline-start" aria-hidden />
                {t("admin.chains.rpc.reset")}
              </GatedButton>
            )}
          </div>
          <div aria-live="polite">
            <TestResultLine result={testCurrent.data} error={testCurrent.error} />
          </div>
        </>
      )}

      {editing && canWrite && (
        <form
          noValidate
          className="space-y-2 rounded-lg border bg-background/60 p-3"
          onSubmit={(e) => {
            e.preventDefault();
            setSubmitted(true);
            if (hasErrors || urls.length === 0) return;
            save.mutate(urls);
          }}
        >
          <p className="text-xs text-muted-foreground">{t("admin.chains.rpc.editHint")}</p>
          {rows.map((value, i) => {
            const err = errors[i] && (submitted || value.trim()) ? t(errors[i]!) : null;
            const id = `${base}-rpc-${i}`;
            const test = rowTests[i];
            return (
              <div key={i} className="space-y-1">
                <label htmlFor={id} className="sr-only">
                  {t("admin.chains.rpc.urlLabel", { n: i + 1 })}
                </label>
                <div className="flex gap-1.5">
                  <Input
                    id={id}
                    value={value}
                    onChange={(e) => {
                      const next = [...rows];
                      next[i] = e.target.value;
                      setRows(next);
                      setRowTests((s) => ({ ...s, [i]: {} }));
                    }}
                    placeholder={t("admin.chains.rpc.urlPlaceholder")}
                    className="font-mono text-xs"
                    autoComplete="off"
                    spellCheck={false}
                    inputMode="url"
                    aria-invalid={!!err}
                    aria-describedby={err ? `${id}-err` : undefined}
                  />
                  <Button
                    type="button"
                    size="sm"
                    variant="outline"
                    className="h-8"
                    disabled={!value.trim() || !!errors[i] || test?.pending}
                    onClick={() => void runRowTest(i)}
                  >
                    {test?.pending ? <Spinner /> : t("admin.chains.rpc.test")}
                  </Button>
                  <Button
                    type="button"
                    size="icon"
                    variant="ghost"
                    aria-label={t("admin.chains.rpc.remove", { n: i + 1 })}
                    disabled={rows.length === 1}
                    onClick={() => {
                      setRows(rows.filter((_, j) => j !== i));
                      setRowTests({});
                    }}
                  >
                    <TrashIcon aria-hidden />
                  </Button>
                </div>
                {err && (
                  <p id={`${id}-err`} className="text-xs text-destructive">
                    {err}
                  </p>
                )}
                <div aria-live="polite">
                  <TestResultLine result={test?.result} error={test?.error} />
                </div>
              </div>
            );
          })}
          {submitted && urls.length === 0 && <p className="text-xs text-destructive">{t("admin.chains.rpc.atLeastOne")}</p>}
          <div className="flex flex-wrap items-center justify-between gap-2 pt-1">
            <Button type="button" variant="ghost" size="sm" onClick={() => setRows([...rows, ""])} disabled={rows.length >= 8}>
              <PlusIcon data-icon="inline-start" aria-hidden />
              {t("admin.chains.rpc.add")}
            </Button>
            <div className="flex gap-2">
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => {
                  setEditing(false);
                  setRows([""]);
                  setSubmitted(false);
                  setRowTests({});
                }}
              >
                {t("admin.common.cancel")}
              </Button>
              <Button type="submit" size="sm" disabled={save.isPending}>
                {save.isPending && <Spinner data-icon="inline-start" />}
                {t("admin.chains.rpc.save")}
              </Button>
            </div>
          </div>
        </form>
      )}

      <ConfirmDialog
        open={resetOpen}
        onOpenChange={setResetOpen}
        title={t("admin.chains.rpc.resetTitle")}
        description={t("admin.chains.rpc.resetBody")}
        confirmLabel={t("admin.chains.rpc.resetConfirm")}
        onConfirm={() => save.mutateAsync([])}
      />
    </div>
  );
}
