"use client";

import { zodResolver } from "@hookform/resolvers/zod";
import { useId, useState } from "react";
import { Controller, useForm, useWatch } from "react-hook-form";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Spinner } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";
import { applyFieldErrors, describeError } from "@/lib/admin/errors";
import { adminKeys, useAdminMutation, useSettings } from "@/lib/admin/queries";
import {
  formToSettings,
  settingsSchema,
  settingsToForm,
  SETTINGS_FIELD_MAP,
  type SettingsFormValues,
} from "@/lib/admin/settings-form";
import { formatDuration, joinDuration, type DurationUnit } from "@/lib/format/units";
import { adminApi } from "@/lib/api/admin";
import type { Settings } from "@/lib/api/types";
import { useLocale, useT } from "@/lib/i18n";
import { useCanWrite } from "../auth";
import { describedBy, FormError, FormField } from "../form-field";
import { PageHeader, Section } from "../page-header";
import { ErrorState } from "../states";

export function SettingsPage() {
  const t = useT();
  const q = useSettings();
  return (
    <>
      <PageHeader title={t("admin.settings.title")} description={t("admin.settings.description")} />
      {q.error && !q.data ? (
        <ErrorState error={q.error} onRetry={() => void q.refetch()} />
      ) : !q.data ? (
        <div className="space-y-4">
          {Array.from({ length: 3 }, (_, i) => (
            <Skeleton key={i} className="h-48 w-full" />
          ))}
        </div>
      ) : (
        <SettingsForm key={JSON.stringify(q.data)} settings={q.data} />
      )}
    </>
  );
}

const FORM_FIELDS = Object.values(SETTINGS_FIELD_MAP);

export function SettingsForm({ settings }: { settings: Settings }) {
  const t = useT();
  const { locale } = useLocale();
  const canWrite = useCanWrite();
  const base = useId();
  const id = (f: string) => `${base}-${f}`;
  const [formError, setFormError] = useState<string | null>(null);
  const form = useForm<SettingsFormValues>({
    resolver: zodResolver(settingsSchema(t, locale)),
    defaultValues: settingsToForm(settings),
    mode: "onBlur",
  });
  const errors = form.formState.errors;
  const watched = useWatch({ control: form.control });
  const save = useAdminMutation({
    mutationFn: (s: Settings) => adminApi.updateSettings(s),
    invalidate: [adminKeys.settings],
    successMessage: t("admin.settings.saved"),
    toastError: false,
    onError: (e) => {
      const rest = applyFieldErrors(e, form.setError, FORM_FIELDS, SETTINGS_FIELD_MAP);
      if (rest === null) setFormError(describeError(t, e));
      else if (Object.keys(rest).length) setFormError(Object.entries(rest).map(([k, v]) => `${k}: ${v}`).join("; "));
      else setFormError(t("admin.errors.fixFields"));
    },
  });
  const dirty = form.formState.isDirty;

  const durationField = (
    field: "invoice_ttl" | "grace_period",
    unitField: "invoice_ttl_unit" | "grace_period_unit",
    label: string,
    hint: string,
  ) => {
    const value = watched[field] ?? "";
    const unit = watched[unitField] ?? "minutes";
    const seconds = joinDuration(value, unit);
    const err = errors[field]?.message;
    return (
      <FormField label={label} htmlFor={id(field)} description={hint} error={err}>
        <div className="flex items-center gap-2">
          <Input
            id={id(field)}
            inputMode="decimal"
            className="w-28 font-mono tabular"
            aria-invalid={!!err}
            aria-describedby={describedBy(id(field), { description: true, error: err })}
            {...form.register(field)}
          />
          <Controller
            control={form.control}
            name={unitField}
            render={({ field: f }) => (
              <Select value={f.value} onValueChange={(v) => f.onChange(v as DurationUnit)}>
                <SelectTrigger className="w-28" aria-label={`${label} · ${t("admin.settings.unitLabel")}`}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {(["seconds", "minutes", "hours"] as const).map((u) => (
                    <SelectItem key={u} value={u}>
                      {t(`admin.settings.units.${u}`)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            )}
          />
          {seconds !== null && unit !== "seconds" && (
            <span className="text-xs text-muted-foreground tabular">= {formatDuration(seconds, locale)}</span>
          )}
        </div>
      </FormField>
    );
  };

  const suffixField = (
    field: "underpay_tolerance_pct" | "rate_markup_pct" | "webhook_max_attempts" | "webhook_timeout_seconds",
    label: string,
    hint: string,
    suffix: string,
    opts: { width?: string } = {},
  ) => {
    const err = errors[field]?.message;
    return (
      <FormField label={label} htmlFor={id(field)} description={hint} error={err}>
        <div className="flex items-center gap-2">
          <Input
            id={id(field)}
            inputMode="decimal"
            className={`${opts.width ?? "w-28"} font-mono tabular`}
            aria-invalid={!!err}
            aria-describedby={describedBy(id(field), { description: true, error: err })}
            {...form.register(field)}
          />
          <span className="text-sm text-muted-foreground">{suffix}</span>
        </div>
      </FormField>
    );
  };

  return (
    <form
      noValidate
      onSubmit={form.handleSubmit((v) => {
        setFormError(null);
        save.mutate(formToSettings(v));
      })}
    >
      <fieldset disabled={!canWrite} className="min-w-0 space-y-4">
        <Section title={t("admin.settings.sections.invoices")} description={t("admin.settings.sections.invoicesDescription")}>
          <div className="grid gap-5 lg:grid-cols-2">
            {durationField("invoice_ttl", "invoice_ttl_unit", t("admin.settings.fields.invoiceTtl"), t("admin.settings.fields.invoiceTtlHint"))}
            {durationField("grace_period", "grace_period_unit", t("admin.settings.fields.gracePeriod"), t("admin.settings.fields.gracePeriodHint"))}
            {suffixField(
              "underpay_tolerance_pct",
              t("admin.settings.fields.underpayTolerance"),
              t("admin.settings.fields.underpayToleranceHint"),
              t("admin.settings.units.percent"),
            )}
          </div>
        </Section>

        <Section title={t("admin.settings.sections.rates")} description={t("admin.settings.sections.ratesDescription")}>
          <div className="grid gap-5 lg:grid-cols-2">
            <Controller
              control={form.control}
              name="stablecoin_peg"
              render={({ field }) => (
                <div className="flex items-start justify-between gap-4 rounded-lg border p-3">
                  <div className="space-y-0.5">
                    <label htmlFor={id("peg")} className="text-sm font-medium">
                      {t("admin.settings.fields.stablecoinPeg")}
                    </label>
                    <p id={`${id("peg")}-desc`} className="text-xs text-pretty text-muted-foreground">
                      {t("admin.settings.fields.stablecoinPegHint")}
                    </p>
                  </div>
                  <Switch id={id("peg")} checked={field.value} onCheckedChange={field.onChange} aria-describedby={`${id("peg")}-desc`} />
                </div>
              )}
            />
            {suffixField("rate_markup_pct", t("admin.settings.fields.rateMarkup"), t("admin.settings.fields.rateMarkupHint"), t("admin.settings.units.percent"))}
          </div>
        </Section>

        <Section title={t("admin.settings.sections.webhooks")} description={t("admin.settings.sections.webhooksDescription")}>
          <div className="grid gap-5 lg:grid-cols-2">
            {suffixField(
              "webhook_max_attempts",
              t("admin.settings.fields.webhookMaxAttempts"),
              t("admin.settings.fields.webhookMaxAttemptsHint"),
              t("admin.settings.units.times"),
            )}
            {suffixField(
              "webhook_timeout_seconds",
              t("admin.settings.fields.webhookTimeout"),
              t("admin.settings.fields.webhookTimeoutHint"),
              t("admin.settings.units.seconds"),
            )}
          </div>
        </Section>

        <Section title={t("admin.settings.sections.checkout")} description={t("admin.settings.sections.checkoutDescription")}>
          <div className="grid gap-5 lg:grid-cols-2">
            <FormField
              label={t("admin.settings.fields.checkoutBaseUrl")}
              htmlFor={id("base")}
              description={t("admin.settings.fields.checkoutBaseUrlHint")}
              error={errors.checkout_base_url?.message}
            >
              <Input
                id={id("base")}
                type="url"
                className="font-mono text-[13px]"
                placeholder="https://pay.example.com"
                aria-invalid={!!errors.checkout_base_url}
                aria-describedby={describedBy(id("base"), { description: true, error: errors.checkout_base_url })}
                {...form.register("checkout_base_url")}
              />
            </FormField>
            <FormField
              label={t("admin.settings.fields.walletconnectProjectId")}
              htmlFor={id("wc")}
              description={t("admin.settings.fields.walletconnectProjectIdHint")}
              error={errors.walletconnect_project_id?.message}
              optional
            >
              <Input
                id={id("wc")}
                className="font-mono text-[13px]"
                autoComplete="off"
                spellCheck={false}
                aria-invalid={!!errors.walletconnect_project_id}
                aria-describedby={describedBy(id("wc"), { description: true, error: errors.walletconnect_project_id })}
                {...form.register("walletconnect_project_id")}
              />
            </FormField>
          </div>
        </Section>

        <FormError message={formError} />

        {canWrite && (
          <div className="sticky bottom-0 z-10 -mx-4 flex items-center justify-end gap-2 border-t bg-background/90 px-4 py-3 backdrop-blur sm:-mx-6 sm:px-6 lg:-mx-8 lg:px-8">
            {dirty && <span className="mr-auto text-sm text-warning" aria-live="polite">{t("admin.common.unsavedChanges")}</span>}
            <Button type="button" variant="ghost" disabled={!dirty || save.isPending} onClick={() => form.reset()}>
              {t("admin.common.discard")}
            </Button>
            <Button type="submit" disabled={!dirty || save.isPending}>
              {save.isPending && <Spinner data-icon="inline-start" />}
              {save.isPending ? t("admin.common.saving") : t("admin.common.saveChanges")}
            </Button>
          </div>
        )}
      </fieldset>
    </form>
  );
}
