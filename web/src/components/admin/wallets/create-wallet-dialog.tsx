"use client";

import { useMutation } from "@tanstack/react-query";
import { CheckIcon, ShieldAlertIcon } from "lucide-react";
import { useId, useState } from "react";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Spinner } from "@/components/ui/spinner";
import { Textarea } from "@/components/ui/textarea";
import { describeError } from "@/lib/admin/errors";
import { adminKeys, useAdminMutation, useChains } from "@/lib/admin/queries";
import { kindsFor, looksLikeAddress, looksLikeSecret, looksLikeXpub, xpubPrefixes } from "@/lib/admin/wallet-input";
import { adminApi } from "@/lib/api/admin";
import { isApiError } from "@/lib/api/client";
import type { Chain, Family, WalletKind, WalletPreviewRequest } from "@/lib/api/types";
import { useT, type MessageKey } from "@/lib/i18n";
import { cn } from "@/lib/utils";
import { CopyButton } from "../copy-button";
import { describedBy, FormError, FormField } from "../form-field";

const FAMILIES: Family[] = ["evm", "tron", "utxo", "solana"];

export function CreateWalletDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      {open && <Wizard onDone={() => onOpenChange(false)} />}
    </Dialog>
  );
}

/** The chain used to preview addresses: the first enabled mainnet of the family, else any. */
export function defaultPreviewChain(chains: Chain[], family: Family): string {
  const ofFamily = chains.filter((c) => c.family === family);
  return (ofFamily.find((c) => c.enabled && !c.is_testnet) ?? ofFamily.find((c) => !c.is_testnet) ?? ofFamily[0])?.id ?? "";
}

function Wizard({ onDone }: { onDone: () => void }) {
  const t = useT();
  const base = useId();
  const chains = useChains({ refetchInterval: false });
  const [step, setStep] = useState<1 | 2 | 3>(1);
  const [family, setFamily] = useState<Family>("evm");
  const [kind, setKind] = useState<WalletKind>("xpub");
  const [key, setKey] = useState("");
  const [touched, setTouched] = useState(false);
  const [chainId, setChainId] = useState("");
  const [verified, setVerified] = useState(false);
  const [name, setName] = useState("");
  const [nameError, setNameError] = useState<string | null>(null);
  const [formError, setFormError] = useState<string | null>(null);

  const familyChains = (chains.data ?? []).filter((c) => c.family === family);
  const previewChain = chainId || defaultPreviewChain(chains.data ?? [], family);
  const trimmed = key.trim();
  const secret = looksLikeSecret(trimmed);
  const shapeOk = kind === "xpub" ? looksLikeXpub(family, trimmed) : looksLikeAddress(family, trimmed, previewChain);
  const keyError = secret
    ? t("admin.wallets.wizard.secretDetected")
    : touched && !trimmed
      ? kind === "xpub"
        ? t("admin.wallets.wizard.xpubRequired")
        : t("admin.wallets.wizard.addressRequired")
      : touched && kind === "xpub" && trimmed && !shapeOk
        ? t("admin.wallets.wizard.xpubInvalid", { prefixes: xpubPrefixes(family).join(" / ") })
        : null;

  const preview = useMutation({
    mutationFn: (body: WalletPreviewRequest) => adminApi.previewWallet(body),
    onSuccess: () => setVerified(false),
  });
  const create = useAdminMutation({
    mutationFn: () =>
      adminApi.createWallet({
        name: name.trim(),
        family,
        kind,
        ...(kind === "xpub" ? { xpub: trimmed } : { address: trimmed }),
      }),
    invalidate: [adminKeys.wallets, adminKeys.chains],
    successMessage: t("admin.wallets.wizard.created"),
    toastError: false,
    onSuccess: () => onDone(),
    onError: (e) => {
      if (isApiError(e) && e.fields?.name) setNameError(e.fields.name);
      else setFormError(describeError(t, e));
    },
  });

  function chooseFamily(f: Family) {
    setFamily(f);
    if (!kindsFor(f).includes(kind)) setKind("static");
    resetKey();
  }
  function resetKey() {
    setKey("");
    setTouched(false);
    setChainId("");
    preview.reset();
    setVerified(false);
  }
  function runPreview() {
    setTouched(true);
    if (!trimmed || secret || (kind === "xpub" && !shapeOk) || !previewChain) return;
    preview.mutate({ family, kind, chain_id: previewChain, ...(kind === "xpub" ? { xpub: trimmed } : { address: trimmed }) });
  }

  const steps: { n: 1 | 2 | 3; label: string }[] = [
    { n: 1, label: t("admin.wallets.wizard.stepType") },
    { n: 2, label: t("admin.wallets.wizard.stepKey") },
    { n: 3, label: t("admin.wallets.wizard.stepName") },
  ];
  const familyLabel = t(`admin.families.${family}` as MessageKey);
  const kindLabel = t(`admin.walletKinds.${kind}` as MessageKey);
  const previewOk = preview.data && preview.variables && preview.variables.chain_id === previewChain;

  return (
    <DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-xl">
      <DialogHeader>
        <DialogTitle>{t("admin.wallets.wizard.title")}</DialogTitle>
        <DialogDescription>{t("admin.wallets.wizard.step", { n: step, total: 3 })}</DialogDescription>
      </DialogHeader>
      <ol className="flex items-center gap-2 text-xs" aria-label={t("admin.wallets.wizard.title")}>
        {steps.map((s, i) => (
          <li key={s.n} className="flex items-center gap-2" aria-current={s.n === step ? "step" : undefined}>
            <span
              className={cn(
                "flex size-5 items-center justify-center rounded-full border text-[11px] font-semibold",
                s.n < step && "border-primary bg-primary text-primary-foreground",
                s.n === step && "border-primary text-primary",
                s.n > step && "text-muted-foreground",
              )}
            >
              {s.n < step ? <CheckIcon className="size-3" aria-hidden /> : s.n}
            </span>
            <span className={s.n === step ? "font-medium" : "text-muted-foreground"}>{s.label}</span>
            {i < steps.length - 1 && <span className="h-px w-4 bg-border sm:w-8" aria-hidden />}
          </li>
        ))}
      </ol>

      {step === 1 && (
        <div className="space-y-5">
          <fieldset className="space-y-2">
            <legend className="mb-2 text-sm font-medium">{t("admin.wallets.wizard.family")}</legend>
            <div className="grid grid-cols-2 gap-2">
              {FAMILIES.map((f) => (
                <RadioCard key={f} name={`${base}-family`} checked={family === f} onChange={() => chooseFamily(f)} title={t(`admin.families.${f}` as MessageKey)}>
                  {t(`admin.families.${f}Long` as MessageKey)}
                </RadioCard>
              ))}
            </div>
          </fieldset>
          <fieldset className="space-y-2">
            <legend className="mb-2 text-sm font-medium">{t("admin.wallets.wizard.kind")}</legend>
            <div className="grid gap-2 sm:grid-cols-2">
              <RadioCard
                name={`${base}-kind`}
                checked={kind === "xpub"}
                onChange={() => {
                  setKind("xpub");
                  resetKey();
                }}
                disabled={!kindsFor(family).includes("xpub")}
                title={t("admin.wallets.wizard.xpubTitle")}
              >
                {t("admin.wallets.wizard.xpubBody")}
              </RadioCard>
              <RadioCard
                name={`${base}-kind`}
                checked={kind === "static"}
                onChange={() => {
                  setKind("static");
                  resetKey();
                }}
                title={t("admin.wallets.wizard.staticTitle")}
              >
                {t("admin.wallets.wizard.staticBody")}
              </RadioCard>
            </div>
            {family === "solana" && <p className="text-xs text-muted-foreground">{t("admin.wallets.wizard.solanaStaticOnly")}</p>}
          </fieldset>
        </div>
      )}

      {step === 2 && (
        <div className="space-y-4">
          <NeverPasteNotice />
          <FormField
            label={kind === "xpub" ? t("admin.wallets.wizard.xpub") : t("admin.wallets.wizard.address")}
            htmlFor={`${base}-key`}
            description={
              kind === "xpub"
                ? t(family === "evm" ? "admin.wallets.wizard.xpubHintEvm" : family === "tron" ? "admin.wallets.wizard.xpubHintTron" : "admin.wallets.wizard.xpubHintUtxo")
                : t("admin.wallets.wizard.addressHint")
            }
            error={keyError ?? undefined}
          >
            {kind === "xpub" ? (
              <Textarea
                id={`${base}-key`}
                value={key}
                onChange={(e) => {
                  setKey(e.target.value);
                  preview.reset();
                }}
                onBlur={() => setTouched(true)}
                rows={3}
                className="font-mono text-xs break-all"
                autoComplete="off"
                spellCheck={false}
                placeholder={`${xpubPrefixes(family)[0]}6C…`}
                aria-invalid={!!keyError}
                aria-describedby={describedBy(`${base}-key`, { description: true, error: keyError })}
              />
            ) : (
              <Input
                id={`${base}-key`}
                value={key}
                onChange={(e) => {
                  setKey(e.target.value);
                  preview.reset();
                }}
                onBlur={() => setTouched(true)}
                className="font-mono text-[13px]"
                autoComplete="off"
                spellCheck={false}
                aria-invalid={!!keyError}
                aria-describedby={describedBy(`${base}-key`, { description: true, error: keyError })}
              />
            )}
          </FormField>

          <div className="flex flex-col gap-2 sm:flex-row sm:items-end">
            <FormField label={t("admin.wallets.wizard.previewChain")} htmlFor={`${base}-chain`} className="sm:flex-1">
              <Select value={previewChain} onValueChange={(v) => { setChainId(v); preview.reset(); }} disabled={familyChains.length === 0}>
                <SelectTrigger id={`${base}-chain`} className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent position="popper">
                  {familyChains.map((c) => (
                    <SelectItem key={c.id} value={c.id}>
                      {c.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </FormField>
            <Button type="button" variant="outline" onClick={runPreview} disabled={preview.isPending || secret || !previewChain}>
              {preview.isPending && <Spinner data-icon="inline-start" />}
              {preview.isPending ? t("admin.wallets.wizard.previewing") : t("admin.wallets.wizard.preview")}
            </Button>
          </div>
          {familyChains.length === 0 && chains.data && <p className="text-xs text-warning">{t("admin.wallets.wizard.noChains")}</p>}

          {preview.error && <FormError message={describeError(t, preview.error)} />}
          {previewOk && preview.data && (
            <div className="space-y-2 rounded-lg border bg-muted/30 p-3" aria-live="polite">
              <p className="text-sm font-medium">{t("admin.wallets.wizard.previewTitle")}</p>
              <p className="text-xs text-muted-foreground">
                {kind === "xpub" ? t("admin.wallets.wizard.previewXpubHint") : t("admin.wallets.wizard.previewStaticHint")}
              </p>
              <ol className="space-y-1" data-testid="preview-addresses">
                {preview.data.addresses.map((a, i) => (
                  <li key={a} className="flex items-center gap-2 rounded-md border bg-card px-2 py-1.5">
                    {kind === "xpub" && <span className="w-6 shrink-0 font-mono text-xs text-muted-foreground">{t("admin.wallets.wizard.previewIndex", { n: i })}</span>}
                    <span className="min-w-0 flex-1 font-mono text-xs break-all">{a}</span>
                    <CopyButton value={a} label={t("admin.wallets.wizard.address")} />
                  </li>
                ))}
              </ol>
              <div className="flex items-start gap-2.5 pt-1">
                <Checkbox id={`${base}-verified`} checked={verified} onCheckedChange={(v) => setVerified(v === true)} className="mt-0.5" />
                <Label htmlFor={`${base}-verified`} className="text-sm leading-snug font-normal">
                  {t("admin.wallets.wizard.verified")}
                </Label>
              </div>
            </div>
          )}
        </div>
      )}

      {step === 3 && (
        <div className="space-y-4">
          <p className="rounded-lg border bg-muted/40 px-3 py-2 text-sm">
            <span className="font-medium">{t("admin.wallets.wizard.summary", { family: familyLabel, kind: kindLabel })}</span>
            <span className="mt-1 block font-mono text-xs break-all text-muted-foreground">{trimmed}</span>
          </p>
          <FormField label={t("admin.wallets.wizard.name")} htmlFor={`${base}-name`} error={nameError ?? undefined}>
            <Input
              id={`${base}-name`}
              value={name}
              onChange={(e) => {
                setName(e.target.value);
                setNameError(null);
              }}
              placeholder={t("admin.wallets.wizard.namePlaceholder")}
              maxLength={100}
              autoFocus
              aria-invalid={!!nameError}
              aria-describedby={describedBy(`${base}-name`, { error: nameError })}
            />
          </FormField>
          <FormError message={formError} />
        </div>
      )}

      <DialogFooter className="sm:justify-between">
        <Button type="button" variant="ghost" onClick={() => (step === 1 ? onDone() : setStep((s) => (s - 1) as 1 | 2))}>
          {step === 1 ? t("admin.common.cancel") : t("admin.common.back")}
        </Button>
        {step < 3 ? (
          <Button
            type="button"
            onClick={() => setStep((s) => (s + 1) as 2 | 3)}
            disabled={step === 2 && (!previewOk || !verified || secret)}
            title={step === 2 && !verified ? t("admin.wallets.wizard.verifyFirst") : undefined}
          >
            {t("admin.common.next")}
          </Button>
        ) : (
          <Button
            type="button"
            disabled={create.isPending}
            onClick={() => {
              if (!name.trim()) {
                setNameError(t("admin.wallets.nameRequired"));
                return;
              }
              setFormError(null);
              create.mutate();
            }}
          >
            {create.isPending && <Spinner data-icon="inline-start" />}
            {t("admin.wallets.wizard.submit")}
          </Button>
        )}
      </DialogFooter>
    </DialogContent>
  );
}

/** The one warning that must not be missed. */
export function NeverPasteNotice() {
  const t = useT();
  return (
    <div className="flex gap-3 rounded-lg border-2 border-destructive/40 bg-destructive/5 p-3" role="note">
      <ShieldAlertIcon className="mt-0.5 size-5 shrink-0 text-destructive" aria-hidden />
      <div className="space-y-1">
        <p className="text-sm font-semibold text-destructive">{t("admin.wallets.wizard.neverTitle")}</p>
        <p className="text-xs leading-relaxed text-pretty text-foreground/80">{t("admin.wallets.wizard.neverBody")}</p>
      </div>
    </div>
  );
}

function RadioCard({
  name,
  checked,
  onChange,
  title,
  children,
  disabled,
}: {
  name: string;
  checked: boolean;
  onChange: () => void;
  title: string;
  children: React.ReactNode;
  disabled?: boolean;
}) {
  return (
    <label
      className={cn(
        "relative flex cursor-pointer flex-col gap-1 rounded-lg border p-3 transition-colors has-[:focus-visible]:ring-2 has-[:focus-visible]:ring-ring/50",
        checked ? "border-primary bg-primary/5" : "hover:bg-muted/50",
        disabled && "cursor-not-allowed opacity-50",
      )}
    >
      <input type="radio" name={name} checked={checked} onChange={onChange} disabled={disabled} className="sr-only" />
      <span className="flex items-center justify-between gap-2 text-sm font-medium">
        {title}
        <span className={cn("flex size-4 items-center justify-center rounded-full border", checked && "border-primary bg-primary")} aria-hidden>
          {checked && <span className="size-1.5 rounded-full bg-primary-foreground" />}
        </span>
      </span>
      <span className="text-xs leading-relaxed text-pretty text-muted-foreground">{children}</span>
    </label>
  );
}
