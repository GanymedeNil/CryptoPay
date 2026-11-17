"use client";

import { zodResolver } from "@hookform/resolvers/zod";
import { KeyRoundIcon, PlusIcon, ReceiptTextIcon, RefreshCwIcon, WebhookIcon } from "lucide-react";
import Link from "next/link";
import { useId, useState } from "react";
import { Controller, useForm } from "react-hook-form";
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
import { Skeleton } from "@/components/ui/skeleton";
import { Spinner } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";
import { applyFieldErrors, describeError } from "@/lib/admin/errors";
import { safeHttpUrl } from "@/lib/admin/explorer";
import { adminKeys, useAdminMutation, useMerchant } from "@/lib/admin/queries";
import { adminApi } from "@/lib/api/admin";
import type { ApiKey, CreateApiKeyResponse, Merchant, UpdateMerchantRequest } from "@/lib/api/types";
import { useT } from "@/lib/i18n";
import type { TranslateFn } from "@/lib/i18n/types";
import { GatedButton, useCanWrite } from "../auth";
import { ConfirmDialog } from "../confirm-dialog";
import { DataTable, type Column } from "../data-table";
import { describedBy, FormError, FormField } from "../form-field";
import { MonoValue } from "../mono-value";
import { PageHeader, Section } from "../page-header";
import { SecretRevealDialog } from "../secret-dialog";
import { useBreadcrumbDetail } from "../shell/admin-shell";
import { ErrorState } from "../states";
import { MerchantStatusBadge, StatusBadge } from "../status-badge";
import { RelativeTime } from "../time";
import { IntegrationSnippet } from "./integration-snippet";
import { MerchantOverviewSection } from "./merchant-overview";
import { MerchantWallets } from "./merchant-wallets";

export function MerchantDetailPage({ id }: { id: string }) {
  const t = useT();
  const q = useMerchant(id);
  const m = q.data?.merchant;
  useBreadcrumbDetail(m?.name ?? null);

  if (q.error && !q.data) {
    return (
      <>
        <PageHeader title={t("admin.nav.merchants")} back={{ href: "/admin/merchants", label: t("admin.merchant.back") }} />
        <ErrorState error={q.error} onRetry={() => void q.refetch()} />
      </>
    );
  }
  if (!q.data || !m) {
    return (
      <div className="space-y-4" aria-busy="true">
        <Skeleton className="h-8 w-64" />
        <Skeleton className="h-48 w-full" />
        <Skeleton className="h-48 w-full" />
      </div>
    );
  }
  return (
    <>
      <PageHeader
        back={{ href: "/admin/merchants", label: t("admin.merchant.back") }}
        title={m.name}
        meta={
          <>
            <MerchantStatusBadge status={m.status} />
            <MonoValue value={m.id} label={t("admin.common.id")} className="text-muted-foreground" />
          </>
        }
        actions={
          <>
            <Button asChild variant="outline" size="sm">
              <Link href={`/admin/invoices?merchant_id=${m.id}`}>
                <ReceiptTextIcon data-icon="inline-start" aria-hidden />
                {t("admin.merchant.viewInvoices")}
              </Link>
            </Button>
            <Button asChild variant="outline" size="sm">
              <Link href={`/admin/webhooks?merchant_id=${m.id}`}>
                <WebhookIcon data-icon="inline-start" aria-hidden />
                {t("admin.merchant.viewWebhooks")}
              </Link>
            </Button>
          </>
        }
      />
      <MerchantOverviewSection merchantId={m.id} className="mb-4" />
      <div className="grid gap-4 xl:grid-cols-2">
        <ProfileForm key={m.updated_at} merchant={m} />
        <WebhookSecret merchant={m} />
        <ApiKeys merchantId={m.id} keys={q.data.api_keys} className="xl:col-span-2" />
        <MerchantWallets merchantId={m.id} bindings={q.data.wallets ?? []} className="xl:col-span-2" />
        <div className="xl:col-span-2">
          <IntegrationSnippet />
        </div>
      </div>
    </>
  );
}

function profileSchema(t: TranslateFn) {
  return z.object({
    name: z.string().trim().min(1, t("admin.merchants.createDialog.nameRequired")).max(200),
    webhook_url: z
      .string()
      .trim()
      .refine((v) => v === "" || safeHttpUrl(v) !== null, t("admin.invoices.createDialog.urlInvalid")),
    active: z.boolean(),
  });
}
type ProfileValues = z.infer<ReturnType<typeof profileSchema>>;

/** Only the fields that changed (PATCH semantics). */
export function profilePatch(m: Merchant, v: ProfileValues): UpdateMerchantRequest {
  const patch: UpdateMerchantRequest = {};
  if (v.name.trim() !== m.name) patch.name = v.name.trim();
  if (v.webhook_url.trim() !== m.webhook_url) patch.webhook_url = v.webhook_url.trim();
  const status = v.active ? "active" : "disabled";
  if (status !== m.status) patch.status = status;
  return patch;
}

function ProfileForm({ merchant: m }: { merchant: Merchant }) {
  const t = useT();
  const base = useId();
  const canWrite = useCanWrite();
  const [formError, setFormError] = useState<string | null>(null);
  const form = useForm<ProfileValues>({
    resolver: zodResolver(profileSchema(t)),
    defaultValues: { name: m.name, webhook_url: m.webhook_url, active: m.status === "active" },
  });
  const errors = form.formState.errors;
  const save = useAdminMutation({
    mutationFn: (patch: UpdateMerchantRequest) => adminApi.updateMerchant(m.id, patch),
    // Name and status also show in the overview's merchant comparison.
    invalidate: [adminKeys.merchants.all, adminKeys.overview.all],
    successMessage: t("admin.merchant.saved"),
    toastError: false,
    onError: (e) => {
      const rest = applyFieldErrors(e, form.setError, ["name", "webhook_url"], { status: "active" });
      if (rest === null) setFormError(describeError(t, e));
      else if (Object.keys(rest).length) setFormError(Object.values(rest).join("; "));
    },
  });
  const dirty = form.formState.isDirty;
  return (
    <Section title={t("admin.merchant.profile")} description={t("admin.merchant.profileDescription")}>
      <form
        noValidate
        onSubmit={form.handleSubmit((v) => {
          setFormError(null);
          const patch = profilePatch(m, v);
          if (Object.keys(patch).length) save.mutate(patch);
        })}
      >
        <fieldset disabled={!canWrite} className="min-w-0">
          <FieldGroup className="gap-4">
            <FormField label={t("admin.merchant.name")} htmlFor={`${base}-name`} error={errors.name?.message}>
              <Input id={`${base}-name`} aria-invalid={!!errors.name} {...form.register("name")} />
            </FormField>
            <FormField
              label={t("admin.merchant.webhookUrl")}
              htmlFor={`${base}-url`}
              description={t("admin.merchant.webhookHint")}
              error={errors.webhook_url?.message}
            >
              <Input
                id={`${base}-url`}
                type="url"
                className="font-mono text-[13px]"
                placeholder="https://"
                aria-invalid={!!errors.webhook_url}
                aria-describedby={describedBy(`${base}-url`, { description: true, error: errors.webhook_url })}
                {...form.register("webhook_url")}
              />
            </FormField>
            <Controller
              control={form.control}
              name="active"
              render={({ field }) => (
                <div className="flex items-start justify-between gap-4 rounded-lg border p-3">
                  <div className="space-y-0.5">
                    <label htmlFor={`${base}-active`} className="text-sm font-medium">
                      {t("admin.merchant.statusActive")}
                    </label>
                    <p id={`${base}-active-desc`} className="text-xs text-muted-foreground">
                      {t("admin.merchant.statusHint")}
                    </p>
                  </div>
                  <Switch
                    id={`${base}-active`}
                    checked={field.value}
                    onCheckedChange={field.onChange}
                    aria-describedby={`${base}-active-desc`}
                  />
                </div>
              )}
            />
          </FieldGroup>
          <FormError message={formError} />
          {canWrite && (
            <div className="mt-4 flex justify-end gap-2">
              {dirty && (
                <Button type="button" variant="ghost" onClick={() => form.reset()}>
                  {t("admin.common.discard")}
                </Button>
              )}
              <Button type="submit" disabled={!dirty || save.isPending}>
                {save.isPending && <Spinner data-icon="inline-start" />}
                {save.isPending ? t("admin.common.saving") : t("admin.common.saveChanges")}
              </Button>
            </div>
          )}
        </fieldset>
      </form>
    </Section>
  );
}

function WebhookSecret({ merchant: m }: { merchant: Merchant }) {
  const t = useT();
  const [confirm, setConfirm] = useState(false);
  const [secret, setSecret] = useState<string | null>(null);
  const rotate = useAdminMutation({
    mutationFn: () => adminApi.rotateWebhookSecret(m.id),
    invalidate: [adminKeys.merchants.all],
    successMessage: t("admin.merchant.secret.rotatedToast"),
    onSuccess: (r) => setSecret(r.webhook_secret),
  });
  return (
    <Section title={t("admin.merchant.secret.title")} description={t("admin.merchant.secret.description")} className="h-fit">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <p className="flex items-center gap-2 text-sm">
          <KeyRoundIcon className={m.has_webhook_secret ? "size-4 text-success" : "size-4 text-warning"} aria-hidden />
          {m.has_webhook_secret ? t("admin.merchant.secret.configured") : t("admin.merchant.secret.missing")}
        </p>
        <GatedButton variant="outline" onClick={() => setConfirm(true)}>
          <RefreshCwIcon data-icon="inline-start" aria-hidden />
          {t("admin.merchant.secret.rotate")}
        </GatedButton>
      </div>
      <ConfirmDialog
        open={confirm}
        onOpenChange={setConfirm}
        title={t("admin.merchant.secret.rotateTitle")}
        description={t("admin.merchant.secret.rotateBody")}
        confirmLabel={t("admin.merchant.secret.rotateConfirm")}
        destructive
        onConfirm={() => rotate.mutateAsync()}
      />
      <SecretRevealDialog
        secret={secret}
        title={t("admin.merchant.secret.rotatedTitle")}
        description={t("admin.merchants.secretDialog.body")}
        label={t("admin.merchants.secretDialog.label")}
        onClose={() => setSecret(null)}
      />
    </Section>
  );
}

function ApiKeys({ merchantId, keys, className }: { merchantId: string; keys: ApiKey[]; className?: string }) {
  const t = useT();
  const [createOpen, setCreateOpen] = useState(false);
  const [created, setCreated] = useState<CreateApiKeyResponse | null>(null);
  const [revoking, setRevoking] = useState<ApiKey | null>(null);
  const [showRevoked, setShowRevoked] = useState(false);
  const revoke = useAdminMutation({
    mutationFn: (k: ApiKey) => adminApi.revokeApiKey(merchantId, k.id),
    invalidate: [adminKeys.merchants.detail(merchantId)],
    successMessage: t("admin.merchant.apiKeys.revokedToast"),
  });
  const revoked = keys.filter((k) => k.revoked_at);
  const rows = showRevoked ? keys : keys.filter((k) => !k.revoked_at);

  const columns: Column<ApiKey>[] = [
    { id: "name", header: t("admin.merchant.apiKeys.columns.name"), cell: (k) => <span className="font-medium">{k.name}</span> },
    {
      id: "prefix",
      header: t("admin.merchant.apiKeys.columns.prefix"),
      cell: (k) => <span className="font-mono text-xs">{k.prefix}…</span>,
    },
    { id: "created", header: t("admin.merchant.apiKeys.columns.created"), cell: (k) => <RelativeTime iso={k.created_at} /> },
    {
      id: "used",
      header: t("admin.merchant.apiKeys.columns.lastUsed"),
      cell: (k) => <RelativeTime iso={k.last_used_at} fallback={t("admin.common.never")} />,
    },
    {
      id: "status",
      header: t("admin.merchant.apiKeys.columns.status"),
      cell: (k) =>
        k.revoked_at ? (
          <StatusBadge tone="muted">{t("admin.merchant.apiKeys.revoked")}</StatusBadge>
        ) : (
          <StatusBadge tone="success">{t("admin.merchant.apiKeys.active")}</StatusBadge>
        ),
    },
    {
      id: "actions",
      header: <span className="sr-only">{t("admin.common.actions")}</span>,
      align: "right",
      cell: (k) =>
        k.revoked_at ? null : (
          <GatedButton variant="ghost" size="sm" className="text-destructive hover:text-destructive" onClick={() => setRevoking(k)}>
            {t("admin.merchant.apiKeys.revoke")}
          </GatedButton>
        ),
    },
  ];

  return (
    <Section
      className={className}
      title={t("admin.merchant.apiKeys.title")}
      description={t("admin.merchant.apiKeys.description")}
      actions={
        <GatedButton size="sm" onClick={() => setCreateOpen(true)}>
          <PlusIcon data-icon="inline-start" aria-hidden />
          {t("admin.merchant.apiKeys.create")}
        </GatedButton>
      }
      bodyClassName="p-0"
    >
      <DataTable
        caption={t("admin.merchant.apiKeys.title")}
        className="[&>div]:rounded-none [&>div]:border-0"
        columns={columns}
        rows={rows}
        getRowId={(k) => k.id}
        isLoading={false}
        rowClassName={(k) => (k.revoked_at ? "opacity-60" : undefined)}
        empty={<p className="p-4 text-sm text-muted-foreground">{t("admin.merchant.apiKeys.empty")}</p>}
      />
      {revoked.length > 0 && (
        <div className="border-t px-3 py-2">
          <Button variant="ghost" size="sm" className="text-muted-foreground" onClick={() => setShowRevoked((s) => !s)} aria-pressed={showRevoked}>
            {showRevoked ? t("admin.merchant.apiKeys.hideRevoked") : t("admin.merchant.apiKeys.showRevoked", { count: revoked.length })}
          </Button>
        </div>
      )}
      <CreateApiKeyDialog
        merchantId={merchantId}
        open={createOpen}
        onOpenChange={setCreateOpen}
        onCreated={(r) => {
          setCreateOpen(false);
          setCreated(r);
        }}
      />
      <SecretRevealDialog
        secret={created?.key ?? null}
        title={t("admin.merchant.apiKeys.createdTitle")}
        description={t("admin.merchant.apiKeys.createdBody")}
        label={t("admin.merchant.apiKeys.keyLabel")}
        onClose={() => setCreated(null)}
      />
      <ConfirmDialog
        open={revoking !== null}
        onOpenChange={(o) => !o && setRevoking(null)}
        title={t("admin.merchant.apiKeys.revokeTitle", { name: revoking?.name ?? "" })}
        description={t("admin.merchant.apiKeys.revokeBody")}
        confirmLabel={t("admin.merchant.apiKeys.revokeConfirm")}
        destructive
        onConfirm={() => (revoking ? revoke.mutateAsync(revoking) : undefined)}
      />
    </Section>
  );
}

function CreateApiKeyDialog({
  merchantId,
  open,
  onOpenChange,
  onCreated,
}: {
  merchantId: string;
  open: boolean;
  onOpenChange: (o: boolean) => void;
  onCreated: (r: CreateApiKeyResponse) => void;
}) {
  const t = useT();
  const inputId = useId();
  const [name, setName] = useState("");
  const [error, setError] = useState<string | null>(null);
  const create = useAdminMutation({
    mutationFn: (n: string) => adminApi.createApiKey(merchantId, { name: n }),
    invalidate: [adminKeys.merchants.detail(merchantId)],
    successMessage: t("admin.merchant.apiKeys.createdToast"),
    toastError: false,
    onSuccess: (r) => {
      setName("");
      onCreated(r);
    },
    onError: (e) => setError(describeError(t, e)),
  });
  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        onOpenChange(o);
        if (!o) {
          setName("");
          setError(null);
        }
      }}
    >
      <DialogContent className="sm:max-w-md">
        <form
          noValidate
          className="grid gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            if (!name.trim()) {
              setError(t("admin.merchant.apiKeys.nameRequired"));
              return;
            }
            setError(null);
            create.mutate(name.trim());
          }}
        >
          <DialogHeader>
            <DialogTitle>{t("admin.merchant.apiKeys.createTitle")}</DialogTitle>
            <DialogDescription>{t("admin.merchant.apiKeys.createDescription")}</DialogDescription>
          </DialogHeader>
          <FormField label={t("admin.merchant.apiKeys.name")} htmlFor={inputId} error={error ?? undefined}>
            <Input
              id={inputId}
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder={t("admin.merchant.apiKeys.namePlaceholder")}
              maxLength={100}
              aria-invalid={!!error}
              aria-describedby={describedBy(inputId, { error })}
              autoFocus
            />
          </FormField>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              {t("admin.common.cancel")}
            </Button>
            <Button type="submit" disabled={create.isPending}>
              {create.isPending && <Spinner data-icon="inline-start" />}
              {t("admin.merchant.apiKeys.create")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
