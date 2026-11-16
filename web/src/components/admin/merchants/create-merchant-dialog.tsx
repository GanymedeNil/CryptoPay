"use client";

import { zodResolver } from "@hookform/resolvers/zod";
import { useRouter } from "next/navigation";
import { useId, useState } from "react";
import { useForm } from "react-hook-form";
import { z } from "zod";
import { Button } from "@/components/ui/button";
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
import { Spinner } from "@/components/ui/spinner";
import { applyFieldErrors, describeError } from "@/lib/admin/errors";
import { safeHttpUrl } from "@/lib/admin/explorer";
import { adminKeys, useAdminMutation } from "@/lib/admin/queries";
import { adminApi } from "@/lib/api/admin";
import type { CreateMerchantResponse } from "@/lib/api/types";
import { useT } from "@/lib/i18n";
import type { TranslateFn } from "@/lib/i18n/types";
import { describedBy, FormError, FormField } from "../form-field";
import { SecretRevealDialog } from "../secret-dialog";

export function merchantSchema(t: TranslateFn) {
  return z.object({
    name: z.string().trim().min(1, t("admin.merchants.createDialog.nameRequired")).max(200),
    webhook_url: z
      .string()
      .trim()
      .refine((v) => v === "" || safeHttpUrl(v) !== null, t("admin.invoices.createDialog.urlInvalid")),
  });
}

/**
 * Create a merchant, then show its webhook secret exactly once; closing the
 * secret dialog opens the new merchant.
 */
export function CreateMerchantDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const router = useRouter();
  const t = useT();
  const [created, setCreated] = useState<CreateMerchantResponse | null>(null);
  return (
    <>
      <Dialog open={open} onOpenChange={onOpenChange}>
        {open && (
          <MerchantForm
            onCancel={() => onOpenChange(false)}
            onCreated={(r) => {
              onOpenChange(false);
              setCreated(r);
            }}
          />
        )}
      </Dialog>
      <SecretRevealDialog
        secret={created?.webhook_secret ?? null}
        title={t("admin.merchants.secretDialog.title")}
        description={t("admin.merchants.secretDialog.body")}
        label={t("admin.merchants.secretDialog.label")}
        onClose={() => {
          const id = created?.merchant.id;
          setCreated(null);
          if (id) router.push(`/admin/merchants/${id}`);
        }}
      />
    </>
  );
}

function MerchantForm({ onCancel, onCreated }: { onCancel: () => void; onCreated: (r: CreateMerchantResponse) => void }) {
  const t = useT();
  const base = useId();
  const [formError, setFormError] = useState<string | null>(null);
  const form = useForm<z.infer<ReturnType<typeof merchantSchema>>>({
    resolver: zodResolver(merchantSchema(t)),
    defaultValues: { name: "", webhook_url: "" },
  });
  const errors = form.formState.errors;
  const create = useAdminMutation({
    mutationFn: (v: { name: string; webhook_url: string }) =>
      adminApi.createMerchant({ name: v.name.trim(), ...(v.webhook_url.trim() ? { webhook_url: v.webhook_url.trim() } : {}) }),
    invalidate: [adminKeys.merchants.all],
    successMessage: t("admin.merchants.createDialog.created"),
    toastError: false,
    onSuccess: (r) => onCreated(r),
    onError: (e) => {
      const rest = applyFieldErrors(e, form.setError, ["name", "webhook_url"]);
      if (rest === null) setFormError(describeError(t, e));
      else if (Object.keys(rest).length) setFormError(Object.values(rest).join("; "));
    },
  });
  return (
    <DialogContent className="sm:max-w-md">
      <form
        noValidate
        className="grid gap-4"
        onSubmit={form.handleSubmit((v) => {
          setFormError(null);
          create.mutate(v);
        })}
      >
        <DialogHeader>
          <DialogTitle>{t("admin.merchants.createDialog.title")}</DialogTitle>
          <DialogDescription>{t("admin.merchants.createDialog.description")}</DialogDescription>
        </DialogHeader>
        <FieldGroup className="gap-4">
          <FormField label={t("admin.merchants.createDialog.name")} htmlFor={`${base}-name`} error={errors.name?.message}>
            <Input
              id={`${base}-name`}
              placeholder={t("admin.merchants.createDialog.namePlaceholder")}
              aria-invalid={!!errors.name}
              aria-describedby={describedBy(`${base}-name`, { error: errors.name })}
              {...form.register("name")}
            />
          </FormField>
          <FormField
            label={t("admin.merchants.createDialog.webhookUrl")}
            htmlFor={`${base}-url`}
            description={t("admin.merchants.createDialog.webhookHint")}
            error={errors.webhook_url?.message}
            optional
          >
            <Input
              id={`${base}-url`}
              type="url"
              placeholder="https://shop.example.com/webhooks/cryptopay"
              className="font-mono text-[13px]"
              aria-invalid={!!errors.webhook_url}
              aria-describedby={describedBy(`${base}-url`, { description: true, error: errors.webhook_url })}
              {...form.register("webhook_url")}
            />
          </FormField>
        </FieldGroup>
        <FormError message={formError} />
        <DialogFooter>
          <Button type="button" variant="outline" onClick={onCancel}>
            {t("admin.common.cancel")}
          </Button>
          <Button type="submit" disabled={create.isPending}>
            {create.isPending && <Spinner data-icon="inline-start" />}
            {create.isPending ? t("admin.common.creating") : t("admin.merchants.createDialog.submit")}
          </Button>
        </DialogFooter>
      </form>
    </DialogContent>
  );
}
