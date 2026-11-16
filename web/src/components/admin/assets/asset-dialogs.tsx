"use client";

import { zodResolver } from "@hookform/resolvers/zod";
import { useId, useState } from "react";
import { Controller, useForm } from "react-hook-form";
import { z } from "zod";
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
import { FieldGroup } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Spinner } from "@/components/ui/spinner";
import { applyFieldErrors, describeError } from "@/lib/admin/errors";
import { adminKeys, useAdminMutation } from "@/lib/admin/queries";
import { adminApi } from "@/lib/api/admin";
import type { Asset, Chain, CreateAssetRequest, Family, UpdateAssetRequest } from "@/lib/api/types";
import { useT } from "@/lib/i18n";
import type { TranslateFn } from "@/lib/i18n/types";
import { describedBy, FormError, FormField } from "../form-field";

/** Token contract formats per family (UTXO chains have no tokens). */
export function isValidContract(family: Family, value: string): boolean {
  const v = value.trim();
  switch (family) {
    case "evm":
      return /^0x[0-9a-fA-F]{40}$/.test(v);
    case "tron":
      return /^T[1-9A-HJ-NP-Za-km-z]{33}$/.test(v);
    case "solana":
      return /^[1-9A-HJ-NP-Za-km-z]{32,44}$/.test(v);
    default:
      return false;
  }
}

const intIn = (t: TranslateFn, min: number, max: number) =>
  z
    .string()
    .trim()
    .refine((v) => /^\d+$/.test(v), t("admin.assets.validation.integer"))
    .refine((v) => !/^\d+$/.test(v) || (Number(v) >= min && Number(v) <= max), t("admin.assets.validation.range", { min, max }));

function addSchema(t: TranslateFn, chains: Chain[]) {
  return z
    .object({
      chain_id: z.string().min(1, t("admin.assets.validation.required")),
      symbol: z.string().trim().regex(/^[A-Za-z0-9]{2,10}$/, t("admin.assets.validation.symbol")),
      name: z.string().trim().min(1, t("admin.assets.validation.required")).max(100),
      contract_address: z.string().trim().min(1, t("admin.assets.validation.required")),
      decimals: intIn(t, 0, 36),
      quote_precision: intIn(t, 0, 18),
      rate_key: z.string().trim().min(1, t("admin.assets.validation.required")).max(100),
      is_stablecoin: z.boolean(),
    })
    .superRefine((v, ctx) => {
      const chain = chains.find((c) => c.id === v.chain_id);
      if (chain && v.contract_address && !isValidContract(chain.family, v.contract_address)) {
        ctx.addIssue({ code: "custom", path: ["contract_address"], message: t("admin.assets.validation.contract") });
      }
      if (/^\d+$/.test(v.decimals) && /^\d+$/.test(v.quote_precision) && Number(v.quote_precision) > Number(v.decimals)) {
        ctx.addIssue({ code: "custom", path: ["quote_precision"], message: t("admin.assets.validation.precisionTooLarge") });
      }
    });
}
type AddValues = z.infer<ReturnType<typeof addSchema>>;

const ADD_FIELDS = ["chain_id", "symbol", "name", "contract_address", "decimals", "quote_precision", "rate_key", "is_stablecoin"] as const;

export function toCreateAsset(v: AddValues): CreateAssetRequest {
  return {
    chain_id: v.chain_id,
    symbol: v.symbol.trim().toUpperCase(),
    name: v.name.trim(),
    contract_address: v.contract_address.trim(),
    decimals: Number(v.decimals),
    quote_precision: Number(v.quote_precision),
    rate_key: v.rate_key.trim(),
    is_stablecoin: v.is_stablecoin,
  };
}

/** `POST /assets`: a token on an account-based chain (EVM / Tron / Solana). */
export function AddAssetDialog({ open, onOpenChange, chains }: { open: boolean; onOpenChange: (o: boolean) => void; chains: Chain[] }) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      {open && <AddAssetForm chains={chains} onDone={() => onOpenChange(false)} />}
    </Dialog>
  );
}

function AddAssetForm({ chains, onDone }: { chains: Chain[]; onDone: () => void }) {
  const t = useT();
  const base = useId();
  const id = (f: string) => `${base}-${f}`;
  const tokenChains = chains.filter((c) => c.family !== "utxo");
  const [formError, setFormError] = useState<string | null>(null);
  const form = useForm<AddValues>({
    resolver: zodResolver(addSchema(t, chains)),
    defaultValues: {
      chain_id: "",
      symbol: "",
      name: "",
      contract_address: "",
      decimals: "6",
      quote_precision: "2",
      rate_key: "",
      is_stablecoin: false,
    },
  });
  const errors = form.formState.errors;
  const create = useAdminMutation({
    mutationFn: (v: AddValues) => adminApi.createAsset(toCreateAsset(v)),
    invalidate: [adminKeys.assets],
    successMessage: t("admin.assets.addDialog.created"),
    toastError: false,
    onSuccess: () => onDone(),
    onError: (e) => {
      const rest = applyFieldErrors(e, form.setError, ADD_FIELDS);
      if (rest === null) setFormError(describeError(t, e));
      else if (Object.keys(rest).length) setFormError(Object.entries(rest).map(([k, v]) => `${k}: ${v}`).join("; "));
    },
  });
  const text = (name: Exclude<keyof AddValues, "chain_id" | "is_stablecoin">, label: string, opts: { hint?: string; mono?: boolean; numeric?: boolean; placeholder?: string } = {}) => {
    const err = errors[name]?.message;
    return (
      <FormField label={label} htmlFor={id(name)} description={opts.hint} error={err}>
        <Input
          id={id(name)}
          inputMode={opts.numeric ? "numeric" : undefined}
          placeholder={opts.placeholder}
          className={opts.mono ? "font-mono text-[13px]" : undefined}
          autoComplete="off"
          spellCheck={false}
          aria-invalid={!!err}
          aria-describedby={describedBy(id(name), { description: !!opts.hint, error: err })}
          {...form.register(name)}
        />
      </FormField>
    );
  };
  return (
    <DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-lg">
      <form
        noValidate
        className="grid gap-4"
        onSubmit={form.handleSubmit((v) => {
          setFormError(null);
          create.mutate(v);
        })}
      >
        <DialogHeader>
          <DialogTitle>{t("admin.assets.addDialog.title")}</DialogTitle>
          <DialogDescription>{t("admin.assets.addDialog.description")}</DialogDescription>
        </DialogHeader>
        <FieldGroup className="gap-4">
          <FormField label={t("admin.assets.field.chain")} htmlFor={id("chain")} error={errors.chain_id?.message} description={t("admin.assets.addDialog.utxoUnsupported")}>
            <Controller
              control={form.control}
              name="chain_id"
              render={({ field }) => (
                <Select value={field.value} onValueChange={field.onChange}>
                  <SelectTrigger id={id("chain")} className="w-full" aria-invalid={!!errors.chain_id}>
                    <SelectValue placeholder={t("admin.assets.field.chainPlaceholder")} />
                  </SelectTrigger>
                  <SelectContent position="popper" className="max-h-72">
                    {tokenChains.map((c) => (
                      <SelectItem key={c.id} value={c.id}>
                        {c.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              )}
            />
          </FormField>
          <div className="grid gap-4 sm:grid-cols-2">
            {text("symbol", t("admin.assets.field.symbol"), { hint: t("admin.assets.field.symbolHint"), mono: true, placeholder: "USDT" })}
            {text("name", t("admin.assets.field.name"), { placeholder: "Tether USD" })}
          </div>
          {text("contract_address", t("admin.assets.field.contract"), { hint: t("admin.assets.field.contractHint"), mono: true })}
          <div className="grid gap-4 sm:grid-cols-2">
            {text("decimals", t("admin.assets.field.decimals"), { numeric: true, mono: true })}
            {text("quote_precision", t("admin.assets.field.precision"), { hint: t("admin.assets.field.precisionHint"), numeric: true, mono: true })}
          </div>
          {text("rate_key", t("admin.assets.field.rateKey"), { hint: t("admin.assets.field.rateKeyHint"), mono: true, placeholder: "tether" })}
          <Controller
            control={form.control}
            name="is_stablecoin"
            render={({ field }) => (
              <div className="flex items-start gap-2.5">
                <Checkbox id={id("stable")} checked={field.value} onCheckedChange={(v) => field.onChange(v === true)} className="mt-0.5" />
                <div className="space-y-0.5">
                  <Label htmlFor={id("stable")}>{t("admin.assets.field.stablecoin")}</Label>
                  <p className="text-xs text-muted-foreground">{t("admin.assets.field.stablecoinHint")}</p>
                </div>
              </div>
            )}
          />
        </FieldGroup>
        <FormError message={formError} />
        <DialogFooter>
          <Button type="button" variant="outline" onClick={onDone}>
            {t("admin.common.cancel")}
          </Button>
          <Button type="submit" disabled={create.isPending}>
            {create.isPending && <Spinner data-icon="inline-start" />}
            {t("admin.assets.addDialog.submit")}
          </Button>
        </DialogFooter>
      </form>
    </DialogContent>
  );
}

function editSchema(t: TranslateFn, decimals: number) {
  return z.object({
    name: z.string().trim().min(1, t("admin.assets.validation.required")).max(100),
    quote_precision: intIn(t, 0, decimals),
    sort_order: intIn(t, 0, 100_000),
    rate_key: z.string().trim().min(1, t("admin.assets.validation.required")).max(100),
  });
}
type EditValues = z.infer<ReturnType<typeof editSchema>>;

/** `PATCH /assets/{id}`: name, quote precision, sort order, rate key. */
export function EditAssetDialog({ asset, onOpenChange }: { asset: Asset | null; onOpenChange: (o: boolean) => void }) {
  return (
    <Dialog open={asset !== null} onOpenChange={onOpenChange}>
      {asset && <EditAssetForm asset={asset} onDone={() => onOpenChange(false)} />}
    </Dialog>
  );
}

function EditAssetForm({ asset: a, onDone }: { asset: Asset; onDone: () => void }) {
  const t = useT();
  const base = useId();
  const [formError, setFormError] = useState<string | null>(null);
  const form = useForm<EditValues>({
    resolver: zodResolver(editSchema(t, a.decimals)),
    defaultValues: { name: a.name, quote_precision: String(a.quote_precision), sort_order: String(a.sort_order), rate_key: a.rate_key },
  });
  const errors = form.formState.errors;
  const save = useAdminMutation({
    mutationFn: (body: UpdateAssetRequest) => adminApi.updateAsset(a.id, body),
    invalidate: [adminKeys.assets],
    successMessage: t("admin.assets.saved"),
    toastError: false,
    onSuccess: () => onDone(),
    onError: (e) => {
      const rest = applyFieldErrors(e, form.setError, ["name", "quote_precision", "sort_order", "rate_key"]);
      if (rest === null) setFormError(describeError(t, e));
    },
  });
  const field = (name: keyof EditValues, label: string, hint?: string, mono = false) => {
    const id = `${base}-${name}`;
    const err = errors[name]?.message;
    return (
      <FormField label={label} htmlFor={id} description={hint} error={err}>
        <Input
          id={id}
          className={mono ? "font-mono text-[13px]" : undefined}
          aria-invalid={!!err}
          aria-describedby={describedBy(id, { description: !!hint, error: err })}
          {...form.register(name)}
        />
      </FormField>
    );
  };
  return (
    <DialogContent className="sm:max-w-md">
      <form
        noValidate
        className="grid gap-4"
        onSubmit={form.handleSubmit((v) => {
          setFormError(null);
          const body: UpdateAssetRequest = {};
          if (v.name.trim() !== a.name) body.name = v.name.trim();
          if (Number(v.quote_precision) !== a.quote_precision) body.quote_precision = Number(v.quote_precision);
          if (Number(v.sort_order) !== a.sort_order) body.sort_order = Number(v.sort_order);
          if (v.rate_key.trim() !== a.rate_key) body.rate_key = v.rate_key.trim();
          if (Object.keys(body).length === 0) return onDone();
          save.mutate(body);
        })}
      >
        <DialogHeader>
          <DialogTitle>{t("admin.assets.editTitle", { symbol: a.symbol, chain: a.chain_name })}</DialogTitle>
          <DialogDescription className="font-mono text-xs">{a.id}</DialogDescription>
        </DialogHeader>
        <FieldGroup className="gap-4">
          {field("name", t("admin.assets.field.name"))}
          <div className="grid gap-4 sm:grid-cols-2">
            {field("quote_precision", t("admin.assets.field.precision"), t("admin.assets.field.precisionHint"), true)}
            {field("sort_order", t("admin.assets.field.sortOrder"), t("admin.assets.field.sortOrderHint"), true)}
          </div>
          {field("rate_key", t("admin.assets.field.rateKey"), t("admin.assets.field.rateKeyHint"), true)}
        </FieldGroup>
        <FormError message={formError} />
        <DialogFooter>
          <Button type="button" variant="outline" onClick={onDone}>
            {t("admin.common.cancel")}
          </Button>
          <Button type="submit" disabled={save.isPending}>
            {save.isPending && <Spinner data-icon="inline-start" />}
            {t("admin.common.saveChanges")}
          </Button>
        </DialogFooter>
      </form>
    </DialogContent>
  );
}
