"use client";

import { zodResolver } from "@hookform/resolvers/zod";
import { KeyRoundIcon, LockIcon, MoreHorizontalIcon, PlusIcon, PowerIcon, ShieldOffIcon, TrashIcon, UsersIcon, WandSparklesIcon } from "lucide-react";
import { useId, useState } from "react";
import { Controller, useForm, useWatch } from "react-hook-form";
import { toast } from "sonner";
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
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { FieldGroup } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Spinner } from "@/components/ui/spinner";
import { applyFieldErrors, describeError } from "@/lib/admin/errors";
import { adminKeys, useAdminMutation, useUsers } from "@/lib/admin/queries";
import { ROLES } from "@/lib/admin/roles";
import { adminApi } from "@/lib/api/admin";
import { isApiError } from "@/lib/api/client";
import type { AdminRole, AdminUser, UpdateUserRequest } from "@/lib/api/types";
import { useT, type MessageKey } from "@/lib/i18n";
import type { TranslateFn } from "@/lib/i18n/types";
import { RoleTooltip, useAuth } from "../auth";
import { ConfirmDialog } from "../confirm-dialog";
import { CopyButton } from "../copy-button";
import { DataTable, type Column } from "../data-table";
import { describedBy, FormError, FormField } from "../form-field";
import { PageHeader } from "../page-header";
import { EmptyState } from "../states";
import { ActiveBadge, RoleBadge, StatusBadge } from "../status-badge";
import { RelativeTime } from "../time";

/** 16 random characters from an unambiguous alphabet (crypto RNG). */
export function generatePassword(length = 16): string {
  const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789";
  const bytes = new Uint32Array(length);
  crypto.getRandomValues(bytes);
  return Array.from(bytes, (b) => alphabet[b % alphabet.length]).join("");
}

/** "Last owner" and other conflicts get a specific explanation. */
function userErrorMessage(t: TranslateFn, e: unknown): string {
  if (isApiError(e) && e.isConflict && /owner/i.test(e.message)) return t("admin.users.lastOwner");
  return describeError(t, e);
}

export function UsersPage() {
  const t = useT();
  const { isOwner } = useAuth();
  if (!isOwner) {
    return (
      <>
        <PageHeader title={t("admin.users.title")} />
        <EmptyState icon={LockIcon} title={t("admin.access.ownerOnlyPageTitle")} description={t("admin.access.ownerOnlyPageBody")} className="rounded-xl border bg-card" />
      </>
    );
  }
  return <OwnerUsersPage />;
}

function OwnerUsersPage() {
  const t = useT();
  const { user: me } = useAuth();
  const users = useUsers();
  const [createOpen, setCreateOpen] = useState(false);
  const [resetting, setResetting] = useState<AdminUser | null>(null);
  const [deactivating, setDeactivating] = useState<AdminUser | null>(null);
  const [deleting, setDeleting] = useState<AdminUser | null>(null);
  const [resetting2fa, setResetting2fa] = useState<AdminUser | null>(null);

  const update = useAdminMutation({
    mutationFn: ({ id, body }: { id: string; body: UpdateUserRequest }) => adminApi.updateUser(id, body),
    invalidate: [adminKeys.users],
    successMessage: (_u, v) =>
      v.body.role ? t("admin.users.roleChanged") : v.body.active === false ? t("admin.users.deactivated") : t("admin.users.activated"),
    // Toast with the specific "last owner" wording instead of the generic one.
    toastError: false,
    onError: (e) => toast.error(userErrorMessage(t, e)),
  });
  const reset2fa = useAdminMutation({
    mutationFn: (u: AdminUser) => adminApi.resetUserTwoFactor(u.id),
    invalidate: [adminKeys.users],
    successMessage: t("admin.users.resetTwoFactorDone"),
  });
  const remove = useAdminMutation({
    mutationFn: (u: AdminUser) => adminApi.deleteUser(u.id),
    invalidate: [adminKeys.users],
    successMessage: t("admin.users.deleted"),
    toastError: false,
    onError: (e) => toast.error(userErrorMessage(t, e)),
  });

  const columns: Column<AdminUser>[] = [
    {
      id: "email",
      header: t("admin.users.columns.email"),
      cell: (u) => (
        <span className="font-medium">
          {u.email}
          {u.id === me.id && <span className="ml-1.5 rounded bg-muted px-1 text-[10px] font-semibold text-muted-foreground uppercase">{t("admin.users.you")}</span>}
        </span>
      ),
    },
    {
      id: "role",
      header: t("admin.users.columns.role"),
      cell: (u) =>
        u.id === me.id ? (
          <RoleBadge role={u.role} />
        ) : (
          <Select value={u.role} onValueChange={(role) => update.mutate({ id: u.id, body: { role: role as AdminRole } })} disabled={update.isPending}>
            <SelectTrigger size="sm" className="w-32" aria-label={t("admin.users.roleLabel", { email: u.email })}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {ROLES.map((r) => (
                <SelectItem key={r} value={r}>
                  {t(`admin.roles.${r}` as MessageKey)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        ),
    },
    { id: "status", header: t("admin.users.columns.status"), cell: (u) => <ActiveBadge active={u.active} /> },
    {
      id: "2fa",
      header: t("admin.users.columns.twoFactor"),
      cell: (u) => (
        <StatusBadge tone={u.two_factor_enabled ? "success" : "muted"}>
          {u.two_factor_enabled ? t("admin.users.twoFactorOn") : t("admin.users.twoFactorOff")}
        </StatusBadge>
      ),
    },
    { id: "login", header: t("admin.users.columns.lastLogin"), cell: (u) => <RelativeTime iso={u.last_login_at} fallback={t("admin.common.never")} /> },
    { id: "created", header: t("admin.users.columns.created"), cell: (u) => <RelativeTime iso={u.created_at} className="text-muted-foreground" /> },
    {
      id: "actions",
      header: <span className="sr-only">{t("admin.common.actions")}</span>,
      align: "right",
      cell: (u) => {
        const self = u.id === me.id;
        return (
          <RoleTooltip blocked={self} reason={t("admin.users.cannotChangeSelf")}>
            <DropdownMenu modal={false}>
              <DropdownMenuTrigger asChild>
                <Button variant="ghost" size="icon-sm" disabled={self} aria-label={`${t("admin.common.more")}: ${u.email}`}>
                  <MoreHorizontalIcon aria-hidden />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                <DropdownMenuItem onSelect={() => setResetting(u)}>
                  <KeyRoundIcon aria-hidden />
                  {t("admin.users.resetPassword")}
                </DropdownMenuItem>
                {u.two_factor_enabled && (
                  <DropdownMenuItem onSelect={() => setResetting2fa(u)}>
                    <ShieldOffIcon aria-hidden />
                    {t("admin.users.resetTwoFactor")}
                  </DropdownMenuItem>
                )}
                {u.active ? (
                  <DropdownMenuItem onSelect={() => setDeactivating(u)}>
                    <PowerIcon aria-hidden />
                    {t("admin.users.deactivate")}
                  </DropdownMenuItem>
                ) : (
                  <DropdownMenuItem onSelect={() => update.mutate({ id: u.id, body: { active: true } })}>
                    <PowerIcon aria-hidden />
                    {t("admin.users.activate")}
                  </DropdownMenuItem>
                )}
                <DropdownMenuSeparator />
                <DropdownMenuItem variant="destructive" onSelect={() => setDeleting(u)}>
                  <TrashIcon aria-hidden />
                  {t("admin.users.delete")}
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </RoleTooltip>
        );
      },
    },
  ];

  return (
    <>
      <PageHeader
        title={t("admin.users.title")}
        description={t("admin.users.description")}
        actions={
          <Button onClick={() => setCreateOpen(true)}>
            <PlusIcon data-icon="inline-start" aria-hidden />
            {t("admin.users.create")}
          </Button>
        }
      />
      <DataTable
        caption={t("admin.users.title")}
        columns={columns}
        rows={users.data}
        getRowId={(u) => u.id}
        isLoading={users.isPending}
        error={users.error}
        onRetry={() => void users.refetch()}
        rowClassName={(u) => (u.active ? undefined : "text-muted-foreground")}
        empty={<EmptyState icon={UsersIcon} title={t("admin.users.emptyTitle")} description={t("admin.users.emptyBody")} />}
      />
      {users.data && users.data.length === 1 && (
        <EmptyState icon={UsersIcon} title={t("admin.users.emptyTitle")} description={t("admin.users.emptyBody")} className="mt-3 rounded-xl border border-dashed" />
      )}
      <ul className="mt-6 grid gap-3 text-sm sm:grid-cols-3">
        {ROLES.map((r) => (
          <li key={r} className="space-y-1.5 rounded-lg border bg-card p-3">
            <RoleBadge role={r} />
            <p className="text-xs text-pretty text-muted-foreground">{t(`admin.roles.${r}Hint` as MessageKey)}</p>
          </li>
        ))}
      </ul>
      <CreateUserDialog open={createOpen} onOpenChange={setCreateOpen} />
      <ResetPasswordDialog user={resetting} onOpenChange={(o) => !o && setResetting(null)} />
      <ConfirmDialog
        open={deactivating !== null}
        onOpenChange={(o) => !o && setDeactivating(null)}
        title={t("admin.users.deactivateTitle", { email: deactivating?.email ?? "" })}
        description={t("admin.users.deactivateBody")}
        confirmLabel={t("admin.users.deactivate")}
        destructive
        onConfirm={() => deactivating && update.mutateAsync({ id: deactivating.id, body: { active: false } })}
      />
      <ConfirmDialog
        open={resetting2fa !== null}
        onOpenChange={(o) => !o && setResetting2fa(null)}
        title={t("admin.users.resetTwoFactorTitle", { email: resetting2fa?.email ?? "" })}
        description={t("admin.users.resetTwoFactorBody")}
        confirmLabel={t("admin.users.resetTwoFactorConfirm")}
        destructive
        onConfirm={() => resetting2fa && reset2fa.mutateAsync(resetting2fa)}
      />
      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(o) => !o && setDeleting(null)}
        title={t("admin.users.deleteTitle", { email: deleting?.email ?? "" })}
        description={t("admin.users.deleteBody")}
        confirmLabel={t("admin.users.deleteConfirm")}
        destructive
        onConfirm={() => deleting && remove.mutateAsync(deleting)}
      />
    </>
  );
}

function createSchema(t: TranslateFn) {
  return z.object({
    email: z.string().trim().regex(/^[^\s@]+@[^\s@]+\.[^\s@]+$/, t("admin.users.createDialog.emailInvalid")),
    password: z.string().min(8, t("admin.users.createDialog.passwordShort")),
    role: z.enum(["owner", "admin", "viewer"]),
  });
}
type CreateValues = z.infer<ReturnType<typeof createSchema>>;

function CreateUserDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      {open && <CreateUserForm onDone={() => onOpenChange(false)} />}
    </Dialog>
  );
}

function CreateUserForm({ onDone }: { onDone: () => void }) {
  const t = useT();
  const base = useId();
  const [formError, setFormError] = useState<string | null>(null);
  const form = useForm<CreateValues>({
    resolver: zodResolver(createSchema(t)),
    defaultValues: { email: "", password: "", role: "viewer" },
  });
  const errors = form.formState.errors;
  const password = useWatch({ control: form.control, name: "password" });
  const role = useWatch({ control: form.control, name: "role" });
  const create = useAdminMutation({
    mutationFn: (v: CreateValues) => adminApi.createUser({ email: v.email.trim(), password: v.password, role: v.role }),
    invalidate: [adminKeys.users],
    successMessage: t("admin.users.createDialog.created"),
    toastError: false,
    onSuccess: () => onDone(),
    onError: (e) => {
      const rest = applyFieldErrors(e, form.setError, ["email", "password", "role"]);
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
          <DialogTitle>{t("admin.users.createDialog.title")}</DialogTitle>
          <DialogDescription>{t("admin.users.createDialog.description")}</DialogDescription>
        </DialogHeader>
        <FieldGroup className="gap-4">
          <FormField label={t("admin.users.createDialog.email")} htmlFor={`${base}-email`} error={errors.email?.message}>
            <Input
              id={`${base}-email`}
              type="email"
              autoComplete="off"
              aria-invalid={!!errors.email}
              aria-describedby={describedBy(`${base}-email`, { error: errors.email })}
              {...form.register("email")}
            />
          </FormField>
          <FormField
            label={t("admin.users.createDialog.password")}
            htmlFor={`${base}-pw`}
            description={t("admin.users.createDialog.passwordHint")}
            error={errors.password?.message}
          >
            <div className="flex gap-1.5">
              <Input
                id={`${base}-pw`}
                type="text"
                autoComplete="new-password"
                className="font-mono"
                aria-invalid={!!errors.password}
                aria-describedby={describedBy(`${base}-pw`, { description: true, error: errors.password })}
                {...form.register("password")}
              />
              {password && <CopyButton value={password} label={t("admin.users.createDialog.password")} size="icon" />}
              <Button
                type="button"
                variant="outline"
                size="icon"
                aria-label={t("admin.users.createDialog.generate")}
                title={t("admin.users.createDialog.generate")}
                onClick={() => form.setValue("password", generatePassword(), { shouldValidate: true, shouldDirty: true })}
              >
                <WandSparklesIcon aria-hidden />
              </Button>
            </div>
          </FormField>
          <FormField label={t("admin.users.createDialog.role")} htmlFor={`${base}-role`}>
            <Controller
              control={form.control}
              name="role"
              render={({ field }) => (
                <Select value={field.value} onValueChange={field.onChange}>
                  <SelectTrigger id={`${base}-role`} className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent position="popper">
                    {ROLES.map((r) => (
                      <SelectItem key={r} value={r}>
                        {t(`admin.roles.${r}` as MessageKey)}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              )}
            />
            <p className="text-xs text-muted-foreground">{t(`admin.roles.${role}Hint` as MessageKey)}</p>
          </FormField>
        </FieldGroup>
        <FormError message={formError} />
        <DialogFooter>
          <Button type="button" variant="outline" onClick={onDone}>
            {t("admin.common.cancel")}
          </Button>
          <Button type="submit" disabled={create.isPending}>
            {create.isPending && <Spinner data-icon="inline-start" />}
            {t("admin.users.createDialog.submit")}
          </Button>
        </DialogFooter>
      </form>
    </DialogContent>
  );
}

function ResetPasswordDialog({ user, onOpenChange }: { user: AdminUser | null; onOpenChange: (o: boolean) => void }) {
  return (
    <Dialog open={user !== null} onOpenChange={onOpenChange}>
      {user && <ResetForm key={user.id} user={user} onDone={() => onOpenChange(false)} />}
    </Dialog>
  );
}

function ResetForm({ user, onDone }: { user: AdminUser; onDone: () => void }) {
  const t = useT();
  const id = useId();
  const [password, setPassword] = useState(() => generatePassword());
  const [error, setError] = useState<string | null>(null);
  const reset = useAdminMutation({
    mutationFn: (pw: string) => adminApi.updateUser(user.id, { password: pw }),
    invalidate: [adminKeys.users],
    successMessage: t("admin.users.resetDone"),
    toastError: false,
    onSuccess: () => onDone(),
    onError: (e) => setError(isApiError(e) && e.fields?.password ? e.fields.password : userErrorMessage(t, e)),
  });
  return (
    <DialogContent className="sm:max-w-md">
      <form
        noValidate
        className="grid gap-4"
        onSubmit={(e) => {
          e.preventDefault();
          if (password.length < 8) return setError(t("admin.users.createDialog.passwordShort"));
          setError(null);
          reset.mutate(password);
        }}
      >
        <DialogHeader>
          <DialogTitle>{t("admin.users.resetTitle", { email: user.email })}</DialogTitle>
          <DialogDescription>{t("admin.users.resetDescription")}</DialogDescription>
        </DialogHeader>
        <FormField label={t("admin.password.next")} htmlFor={id} error={error ?? undefined}>
          <div className="flex gap-1.5">
            <Input
              id={id}
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              className="font-mono"
              autoComplete="new-password"
              aria-invalid={!!error}
              aria-describedby={describedBy(id, { error })}
            />
            <CopyButton value={password} label={t("admin.password.next")} size="icon" />
            <Button type="button" variant="outline" size="icon" aria-label={t("admin.users.createDialog.generate")} onClick={() => setPassword(generatePassword())}>
              <WandSparklesIcon aria-hidden />
            </Button>
          </div>
        </FormField>
        <DialogFooter>
          <Button type="button" variant="outline" onClick={onDone}>
            {t("admin.common.cancel")}
          </Button>
          <Button type="submit" disabled={reset.isPending}>
            {reset.isPending && <Spinner data-icon="inline-start" />}
            {t("admin.users.resetPassword")}
          </Button>
        </DialogFooter>
      </form>
    </DialogContent>
  );
}
