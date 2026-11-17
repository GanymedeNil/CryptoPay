"use client";

import { zodResolver } from "@hookform/resolvers/zod";
import { ChevronDownIcon, CircleCheckIcon, ExternalLinkIcon } from "lucide-react";
import Link from "next/link";
import { useId, useState } from "react";
import { Controller, useForm } from "react-hook-form";
import { z } from "zod";
import { Button } from "@/components/ui/button";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
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
import { Select, SelectContent, SelectGroup, SelectItem, SelectLabel, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Spinner } from "@/components/ui/spinner";
import { Textarea } from "@/components/ui/textarea";
import { applyFieldErrors, describeError } from "@/lib/admin/errors";
import { adminKeys, useAdminMutation, useAllMerchants, useAssets } from "@/lib/admin/queries";
import { safeHttpUrl } from "@/lib/admin/explorer";
import { compareDecimal } from "@/lib/format/amount";
import { adminApi } from "@/lib/api/admin";
import { isApiError } from "@/lib/api/client";
import type { AdminCreateInvoice, AdminInvoice, Asset } from "@/lib/api/types";
import { useT } from "@/lib/i18n";
import type { TranslateFn } from "@/lib/i18n/types";
import { cn } from "@/lib/utils";
import { CopyButton } from "../copy-button";
import { describedBy, FormError, FormField } from "../form-field";

const ANY = "__any__";
export const CURRENCY_SUGGESTIONS = ["USD", "EUR", "CNY", "GBP", "JPY", "HKD", "USDT", "USDC", "PYUSD", "BTC", "ETH"];

const optionalUrl = (t: TranslateFn) =>
  z
    .string()
    .trim()
    .refine((v) => v === "" || safeHttpUrl(v) !== null, t("admin.invoices.createDialog.urlInvalid"));

export function invoiceSchema(t: TranslateFn) {
  const tooLong = (max: number) => t("admin.invoices.createDialog.tooLong", { max });
  return z.object({
    merchant_id: z.string().min(1, t("admin.invoices.createDialog.merchantRequired")),
    amount: z
      .string()
      .trim()
      .refine((v) => /^\d+(\.\d+)?$/.test(v) && compareDecimal(v, "0") > 0, t("admin.invoices.createDialog.amountInvalid")),
    currency: z
      .string()
      .trim()
      .regex(/^[A-Za-z0-9]{3,10}$/, t("admin.invoices.createDialog.currencyInvalid")),
    title: z.string().max(200, tooLong(200)),
    description: z.string().max(2000, tooLong(2000)),
    asset_id: z.string(),
    order_id: z.string().trim().max(128, tooLong(128)),
    expires_minutes: z
      .string()
      .trim()
      .refine((v) => v === "" || (/^\d+$/.test(v) && Number(v) >= 1 && Number(v) <= 1440), t("admin.invoices.createDialog.expiresInvalid")),
    customer_email: z
      .string()
      .trim()
      .refine((v) => v === "" || /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(v), t("admin.invoices.createDialog.emailInvalid")),
    success_url: optionalUrl(t),
    cancel_url: optionalUrl(t),
  });
}

export type InvoiceFormValues = z.infer<ReturnType<typeof invoiceSchema>>;

const FIELDS = [
  "merchant_id",
  "amount",
  "currency",
  "title",
  "description",
  "asset_id",
  "order_id",
  "expires_minutes",
  "customer_email",
  "success_url",
  "cancel_url",
] as const;

/** Fields inside the collapsed "more options" section. */
const MORE_FIELDS = ["asset_id", "order_id", "expires_minutes", "customer_email", "success_url", "cancel_url"] as const;

/** Form values → `POST /invoices` body (empty optionals omitted, minutes → seconds). */
export function toCreateInvoice(v: InvoiceFormValues): AdminCreateInvoice {
  const body: AdminCreateInvoice = {
    merchant_id: v.merchant_id,
    amount: v.amount.trim(),
    currency: v.currency.trim().toUpperCase(),
  };
  if (v.title.trim()) body.title = v.title.trim();
  if (v.description.trim()) body.description = v.description.trim();
  if (v.asset_id && v.asset_id !== ANY) body.asset_id = v.asset_id;
  if (v.order_id.trim()) body.order_id = v.order_id.trim();
  if (v.expires_minutes.trim()) body.expires_in = Number(v.expires_minutes) * 60;
  if (v.customer_email.trim()) body.customer_email = v.customer_email.trim();
  if (v.success_url.trim()) body.success_url = v.success_url.trim();
  if (v.cancel_url.trim()) body.cancel_url = v.cancel_url.trim();
  return body;
}

export function CreateInvoiceDialog({
  open,
  onOpenChange,
  defaultMerchantId,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  defaultMerchantId?: string;
}) {
  const [created, setCreated] = useState<AdminInvoice | null>(null);
  const [round, setRound] = useState(0);
  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        onOpenChange(o);
        if (!o) setCreated(null);
      }}
    >
      {open && (
        <DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-xl">
          {created ? (
            <Created
              invoice={created}
              onAnother={() => {
                setCreated(null);
                setRound((r) => r + 1);
              }}
              onClose={() => {
                onOpenChange(false);
                setCreated(null);
              }}
            />
          ) : (
            <InvoiceForm key={round} defaultMerchantId={defaultMerchantId} onCreated={setCreated} onCancel={() => onOpenChange(false)} />
          )}
        </DialogContent>
      )}
    </Dialog>
  );
}

function InvoiceForm({
  defaultMerchantId,
  onCreated,
  onCancel,
}: {
  defaultMerchantId?: string;
  onCreated: (inv: AdminInvoice) => void;
  onCancel: () => void;
}) {
  const t = useT();
  const base = useId();
  const id = (f: string) => `${base}-${f}`;
  const merchants = useAllMerchants();
  const assets = useAssets();
  const [more, setMore] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);
  const active = (merchants.data?.items ?? []).filter((m) => m.status === "active");

  const form = useForm<InvoiceFormValues>({
    resolver: zodResolver(invoiceSchema(t)),
    defaultValues: {
      merchant_id: defaultMerchantId ?? "",
      amount: "",
      currency: "USD",
      title: "",
      description: "",
      asset_id: ANY,
      order_id: "",
      expires_minutes: "",
      customer_email: "",
      success_url: "",
      cancel_url: "",
    },
  });
  const errors = form.formState.errors;

  const mutation = useAdminMutation({
    mutationFn: (v: InvoiceFormValues) => adminApi.createInvoice(toCreateInvoice(v)),
    invalidate: [adminKeys.invoices.all, adminKeys.overview.all],
    successMessage: t("admin.invoices.createDialog.createdToast"),
    // Field errors are shown on the fields; anything else in the form footer.
    toastError: false,
    onSuccess: (inv) => onCreated(inv),
    onError: (e) => {
      const rest = applyFieldErrors(e, form.setError, FIELDS, { expires_in: "expires_minutes" });
      if (rest === null) {
        setFormError(describeError(t, e));
        return;
      }
      if (Object.keys(rest).length) setFormError(Object.entries(rest).map(([k, v]) => `${k}: ${v}`).join("; "));
      // Fields inside "more options" must be visible to be fixed.
      const apiFields = Object.keys(isApiError(e) ? (e.fields ?? {}) : {}).map((k) => (k === "expires_in" ? "expires_minutes" : k));
      if (apiFields.some((k) => (MORE_FIELDS as readonly string[]).includes(k))) setMore(true);
    },
  });

  const assetGroups = groupAssets(assets.data ?? []);

  return (
    <form
      noValidate
      onSubmit={form.handleSubmit(
        (v) => {
          setFormError(null);
          mutation.mutate(v);
        },
        (errs) => {
          if (Object.keys(errs).some((k) => (MORE_FIELDS as readonly string[]).includes(k))) setMore(true);
        },
      )}
      className="grid gap-4"
    >
      <DialogHeader>
        <DialogTitle>{t("admin.invoices.createDialog.title")}</DialogTitle>
        <DialogDescription>{t("admin.invoices.createDialog.description")}</DialogDescription>
      </DialogHeader>

      <FieldGroup className="gap-4">
        <FormField label={t("admin.invoices.createDialog.merchant")} htmlFor={id("merchant")} error={errors.merchant_id?.message}>
          <Controller
            control={form.control}
            name="merchant_id"
            render={({ field }) => (
              <Select value={field.value} onValueChange={field.onChange} disabled={merchants.isPending}>
                <SelectTrigger
                  id={id("merchant")}
                  className="w-full"
                  aria-invalid={!!errors.merchant_id}
                  aria-describedby={describedBy(id("merchant"), { error: errors.merchant_id })}
                >
                  <SelectValue placeholder={t("admin.invoices.createDialog.merchantPlaceholder")} />
                </SelectTrigger>
                <SelectContent position="popper">
                  {active.length === 0 ? (
                    <div className="px-2 py-3 text-sm text-muted-foreground">{t("admin.invoices.createDialog.noMerchants")}</div>
                  ) : (
                    active.map((m) => (
                      <SelectItem key={m.id} value={m.id}>
                        {m.name}
                      </SelectItem>
                    ))
                  )}
                </SelectContent>
              </Select>
            )}
          />
        </FormField>

        <div className="grid grid-cols-[1fr_8rem] gap-3">
          <FormField label={t("admin.invoices.createDialog.amount")} htmlFor={id("amount")} error={errors.amount?.message}>
            <Input
              id={id("amount")}
              inputMode="decimal"
              autoComplete="off"
              placeholder="99.00"
              className="font-mono tabular"
              aria-invalid={!!errors.amount}
              aria-describedby={describedBy(id("amount"), { error: errors.amount })}
              {...form.register("amount")}
            />
          </FormField>
          <FormField label={t("admin.invoices.createDialog.currency")} htmlFor={id("currency")} error={errors.currency?.message}>
            <Input
              id={id("currency")}
              list={id("currencies")}
              autoComplete="off"
              className="font-mono uppercase"
              aria-invalid={!!errors.currency}
              aria-describedby={describedBy(id("currency"), { error: errors.currency })}
              {...form.register("currency")}
            />
            <datalist id={id("currencies")}>
              {CURRENCY_SUGGESTIONS.map((c) => (
                <option key={c} value={c} />
              ))}
            </datalist>
          </FormField>
        </div>
        <p className="-mt-2 text-xs text-muted-foreground">{t("admin.invoices.createDialog.currencyHint")}</p>

        <FormField label={t("admin.invoices.createDialog.invoiceTitle")} htmlFor={id("title")} error={errors.title?.message} optional>
          <Input
            id={id("title")}
            placeholder={t("admin.invoices.createDialog.titlePlaceholder")}
            aria-invalid={!!errors.title}
            {...form.register("title")}
          />
        </FormField>
        <FormField label={t("admin.invoices.createDialog.descriptionField")} htmlFor={id("description")} error={errors.description?.message} optional>
          <Textarea id={id("description")} rows={2} aria-invalid={!!errors.description} {...form.register("description")} />
        </FormField>

        <Collapsible open={more} onOpenChange={setMore}>
          <CollapsibleTrigger asChild>
            <Button type="button" variant="ghost" size="sm" className="-ml-2 text-muted-foreground" aria-expanded={more}>
              <ChevronDownIcon className={cn("transition-transform", more && "rotate-180")} data-icon="inline-start" aria-hidden />
              {t("admin.invoices.createDialog.moreOptions")}
            </Button>
          </CollapsibleTrigger>
          <CollapsibleContent className="mt-3 grid gap-4">
            <FormField label={t("admin.invoices.createDialog.asset")} htmlFor={id("asset")} error={errors.asset_id?.message} optional>
              <Controller
                control={form.control}
                name="asset_id"
                render={({ field }) => (
                  <Select value={field.value} onValueChange={field.onChange}>
                    <SelectTrigger id={id("asset")} className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent position="popper" className="max-h-72">
                      <SelectItem value={ANY}>{t("admin.invoices.createDialog.assetAny")}</SelectItem>
                      {assetGroups.map(([chain, list]) => (
                        <SelectGroup key={chain}>
                          <SelectLabel>{chain}</SelectLabel>
                          {list.map((a) => (
                            <SelectItem key={a.id} value={a.id}>
                              {a.symbol} · {a.chain_name}
                            </SelectItem>
                          ))}
                        </SelectGroup>
                      ))}
                    </SelectContent>
                  </Select>
                )}
              />
            </FormField>
            <div className="grid gap-4 sm:grid-cols-2">
              <FormField
                label={t("admin.invoices.createDialog.orderId")}
                htmlFor={id("order")}
                description={t("admin.invoices.createDialog.orderIdHint")}
                error={errors.order_id?.message}
                optional
              >
                <Input
                  id={id("order")}
                  className="font-mono"
                  autoComplete="off"
                  aria-invalid={!!errors.order_id}
                  aria-describedby={describedBy(id("order"), { description: true, error: errors.order_id })}
                  {...form.register("order_id")}
                />
              </FormField>
              <FormField
                label={t("admin.invoices.createDialog.expiresIn")}
                htmlFor={id("expires")}
                description={t("admin.invoices.createDialog.expiresHint")}
                error={errors.expires_minutes?.message}
                optional
              >
                <Input
                  id={id("expires")}
                  inputMode="numeric"
                  placeholder="15"
                  className="tabular"
                  aria-invalid={!!errors.expires_minutes}
                  aria-describedby={describedBy(id("expires"), { description: true, error: errors.expires_minutes })}
                  {...form.register("expires_minutes")}
                />
              </FormField>
            </div>
            <FormField label={t("admin.invoices.createDialog.customerEmail")} htmlFor={id("email")} error={errors.customer_email?.message} optional>
              <Input id={id("email")} type="email" autoComplete="off" aria-invalid={!!errors.customer_email} {...form.register("customer_email")} />
            </FormField>
            <FormField label={t("admin.invoices.createDialog.successUrl")} htmlFor={id("success")} error={errors.success_url?.message} optional>
              <Input id={id("success")} type="url" placeholder="https://" aria-invalid={!!errors.success_url} {...form.register("success_url")} />
            </FormField>
            <FormField label={t("admin.invoices.createDialog.cancelUrl")} htmlFor={id("cancel")} error={errors.cancel_url?.message} optional>
              <Input id={id("cancel")} type="url" placeholder="https://" aria-invalid={!!errors.cancel_url} {...form.register("cancel_url")} />
            </FormField>
          </CollapsibleContent>
        </Collapsible>
      </FieldGroup>

      <FormError message={formError} />

      <DialogFooter>
        <Button type="button" variant="outline" onClick={onCancel}>
          {t("admin.common.cancel")}
        </Button>
        <Button type="submit" disabled={mutation.isPending}>
          {mutation.isPending && <Spinner data-icon="inline-start" />}
          {mutation.isPending ? t("admin.common.creating") : t("admin.invoices.createDialog.submit")}
        </Button>
      </DialogFooter>
    </form>
  );
}

function groupAssets(assets: Asset[]): [string, Asset[]][] {
  const map = new Map<string, Asset[]>();
  for (const a of assets) {
    if (!a.enabled) continue;
    const key = a.chain_name;
    map.set(key, [...(map.get(key) ?? []), a]);
  }
  return [...map.entries()];
}

function Created({ invoice, onAnother, onClose }: { invoice: AdminInvoice; onAnother: () => void; onClose: () => void }) {
  const t = useT();
  const href = safeHttpUrl(invoice.checkout_url);
  return (
    <div className="grid gap-4">
      <DialogHeader>
        <DialogTitle className="flex items-center gap-2">
          <CircleCheckIcon className="size-5 text-success" aria-hidden />
          {t("admin.invoices.createDialog.createdTitle")}
        </DialogTitle>
        <DialogDescription>{t("admin.invoices.createDialog.createdBody")}</DialogDescription>
      </DialogHeader>
      <div className="space-y-1.5">
        <p className="text-xs text-muted-foreground">{t("admin.invoices.createDialog.checkoutLink")}</p>
        <div className="flex items-center gap-1 rounded-lg border bg-muted/50 py-1 pr-1 pl-3">
          <span className="min-w-0 flex-1 font-mono text-[13px] break-all" data-testid="checkout-url">
            {invoice.checkout_url}
          </span>
          <CopyButton value={invoice.checkout_url} label={t("admin.invoices.createDialog.checkoutLink")} size="icon-sm" toast />
        </div>
      </div>
      <DialogFooter className="sm:justify-between">
        <Button type="button" variant="ghost" onClick={onAnother}>
          {t("admin.invoices.createDialog.createAnother")}
        </Button>
        <div className="flex flex-col-reverse gap-2 sm:flex-row">
          {href && (
            <Button asChild variant="outline">
              <a href={href} target="_blank" rel="noopener noreferrer" referrerPolicy="no-referrer">
                <ExternalLinkIcon data-icon="inline-start" aria-hidden />
                {t("admin.invoices.createDialog.openCheckout")}
              </a>
            </Button>
          )}
          <Button asChild>
            <Link href={`/admin/invoices/${invoice.id}`} onClick={onClose}>
              {t("admin.invoices.createDialog.viewInvoice")}
            </Link>
          </Button>
        </div>
      </DialogFooter>
    </div>
  );
}
