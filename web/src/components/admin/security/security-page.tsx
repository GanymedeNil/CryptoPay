"use client";

import { useQueryClient } from "@tanstack/react-query";
import { KeyRoundIcon, LaptopIcon, LogOutIcon, MonitorSmartphoneIcon, RefreshCwIcon, ShieldCheckIcon, ShieldOffIcon } from "lucide-react";
import { useRouter } from "next/navigation";
import { useId, useState } from "react";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { FieldGroup } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Spinner } from "@/components/ui/spinner";
import { describeError } from "@/lib/admin/errors";
import { adminKeys, useAdminMutation, useSessions } from "@/lib/admin/queries";
import { describeUserAgent, normalizeOtp, normalizeRecoveryCode } from "@/lib/admin/two-factor";
import { adminApi } from "@/lib/api/admin";
import { isApiError } from "@/lib/api/client";
import type { AdminMe, AdminSession } from "@/lib/api/types";
import { useT } from "@/lib/i18n";
import { loginUrl, useAuth } from "../auth";
import { ConfirmDialog } from "../confirm-dialog";
import { DataTable, type Column } from "../data-table";
import { describedBy, FormError, FormField } from "../form-field";
import { PageHeader, Section } from "../page-header";
import { ChangePasswordDialog } from "../shell/change-password-dialog";
import { EmptyState } from "../states";
import { StatusBadge } from "../status-badge";
import { RelativeTime } from "../time";
import { RecoveryCodesPanel } from "../two-factor/recovery-codes";
import { TwoFactorSetupDialog } from "../two-factor/setup-wizard";

/** `/admin/security`: the signed-in admin's own 2FA, sessions and password (every role). */
export function SecurityPage() {
  const t = useT();
  return (
    <>
      <PageHeader title={t("admin.security.title")} description={t("admin.security.description")} />
      <div className="space-y-4">
        <TwoFactorCard />
        <SessionsCard />
        <PasswordCard />
      </div>
    </>
  );
}

function useSetTwoFactorEnabled() {
  const qc = useQueryClient();
  return (enabled: boolean) => {
    qc.setQueryData<AdminMe>(adminKeys.me, (u) => u && { ...u, two_factor_enabled: enabled, two_factor_setup_required: false });
    void qc.invalidateQueries({ queryKey: adminKeys.me });
  };
}

export function TwoFactorCard() {
  const t = useT();
  const { user } = useAuth();
  const setEnabled = useSetTwoFactorEnabled();
  const [setupOpen, setSetupOpen] = useState(false);
  const [disableOpen, setDisableOpen] = useState(false);
  const [regenOpen, setRegenOpen] = useState(false);
  const on = user.two_factor_enabled;
  return (
    <Section
      id="two-factor"
      title={
        <span className="flex items-center gap-2">
          {t("admin.twoFactor.title")}
          <StatusBadge tone={on ? "success" : "muted"}>{on ? t("admin.twoFactor.enabled") : t("admin.twoFactor.disabled")}</StatusBadge>
        </span>
      }
      description={t("admin.twoFactor.description")}
    >
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <p className="flex items-start gap-2 text-sm text-pretty">
          {on ? <ShieldCheckIcon className="mt-0.5 size-4 shrink-0 text-success" aria-hidden /> : <ShieldOffIcon className="mt-0.5 size-4 shrink-0 text-warning" aria-hidden />}
          {on ? t("admin.twoFactor.enabledBody") : t("admin.twoFactor.disabledBody")}
        </p>
        <div className="flex shrink-0 flex-wrap gap-2">
          {on ? (
            <>
              <Button variant="outline" size="sm" onClick={() => setRegenOpen(true)}>
                <RefreshCwIcon data-icon="inline-start" aria-hidden />
                {t("admin.twoFactor.regenerate.action")}
              </Button>
              <Button variant="outline" size="sm" className="text-destructive" onClick={() => setDisableOpen(true)}>
                <ShieldOffIcon data-icon="inline-start" aria-hidden />
                {t("admin.twoFactor.disable.action")}
              </Button>
            </>
          ) : (
            <Button size="sm" onClick={() => setSetupOpen(true)}>
              <ShieldCheckIcon data-icon="inline-start" aria-hidden />
              {t("admin.twoFactor.enable")}
            </Button>
          )}
        </div>
      </div>
      <TwoFactorSetupDialog open={setupOpen} onOpenChange={setSetupOpen} email={user.email} onComplete={() => setEnabled(true)} />
      <DisableTwoFactorDialog open={disableOpen} onOpenChange={setDisableOpen} onDone={() => setEnabled(false)} />
      <RegenerateCodesDialog open={regenOpen} onOpenChange={setRegenOpen} email={user.email} />
    </Section>
  );
}

/** A typed code: 6 digits stay a TOTP code, anything else is treated as a recovery code. */
function normalizeAnyCode(raw: string): string {
  const digits = normalizeOtp(raw);
  return /^[\d\s-]+$/.test(raw.trim()) && digits.length > 0 ? digits : normalizeRecoveryCode(raw);
}

/**
 * Current password + code: the confirmation of disable (a code or a recovery
 * code) / regenerate (a code from the authenticator app only).
 */
function PasswordAndCodeForm({
  submitLabel,
  submittingLabel,
  destructive = false,
  pending,
  errors,
  onSubmit,
  onCancel,
  codeLabel,
  codeHint,
  allowRecovery,
}: {
  submitLabel: string;
  submittingLabel: string;
  destructive?: boolean;
  pending: boolean;
  errors: { password?: string; code?: string; form?: string };
  onSubmit: (v: { current_password: string; code: string }) => void;
  onCancel: () => void;
  codeLabel: string;
  codeHint: string;
  allowRecovery: boolean;
}) {
  const t = useT();
  const base = useId();
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [local, setLocal] = useState<{ password?: string; code?: string }>({});
  const pwErr = local.password ?? errors.password;
  const codeErr = local.code ?? errors.code;
  return (
    <form
      noValidate
      className="grid gap-4"
      onSubmit={(e) => {
        e.preventDefault();
        const next = {
          password: password ? undefined : t("admin.password.required"),
          code: code.trim() ? undefined : t("admin.password.required"),
        };
        setLocal(next);
        if (next.password || next.code) return;
        onSubmit({ current_password: password, code: allowRecovery ? normalizeAnyCode(code) : normalizeOtp(code) });
      }}
    >
      <FieldGroup className="gap-4">
        <FormField label={t("admin.password.current")} htmlFor={`${base}-pw`} error={pwErr}>
          <Input
            id={`${base}-pw`}
            type="password"
            autoComplete="current-password"
            autoFocus
            value={password}
            onChange={(e) => {
              setPassword(e.target.value);
              setLocal((l) => ({ ...l, password: undefined }));
            }}
            aria-invalid={!!pwErr}
            aria-describedby={describedBy(`${base}-pw`, { error: pwErr })}
          />
        </FormField>
        <FormField label={codeLabel} htmlFor={`${base}-code`} description={codeHint} error={codeErr}>
          <Input
            id={`${base}-code`}
            value={code}
            onChange={(e) => {
              setCode(e.target.value);
              setLocal((l) => ({ ...l, code: undefined }));
            }}
            autoComplete="one-time-code"
            autoCapitalize="none"
            spellCheck={false}
            className="font-mono tracking-wide"
            aria-invalid={!!codeErr}
            aria-describedby={describedBy(`${base}-code`, { description: true, error: codeErr })}
          />
        </FormField>
      </FieldGroup>
      <FormError message={errors.form} />
      <DialogFooter>
        <Button type="button" variant="outline" onClick={onCancel}>
          {t("admin.common.cancel")}
        </Button>
        <Button type="submit" variant={destructive ? "destructive" : "default"} disabled={pending}>
          {pending && <Spinner data-icon="inline-start" />}
          {pending ? submittingLabel : submitLabel}
        </Button>
      </DialogFooter>
    </form>
  );
}

type FormErrors = { password?: string; code?: string; form?: string };

function codeErrors(t: ReturnType<typeof useT>, e: unknown, wrongCode: string, conflict?: string): FormErrors {
  if (isApiError(e) && e.fields?.current_password) return { password: t("admin.password.wrongCurrent") };
  if (isApiError(e) && e.fields?.code) return { code: wrongCode };
  if (isApiError(e) && e.isConflict && conflict) return { form: conflict };
  return { form: describeError(t, e) };
}

function DisableTwoFactorDialog({ open, onOpenChange, onDone }: { open: boolean; onOpenChange: (o: boolean) => void; onDone: () => void }) {
  const t = useT();
  const [errors, setErrors] = useState<FormErrors>({});
  const disable = useAdminMutation({
    mutationFn: (v: { current_password: string; code: string }) => adminApi.disableTwoFactor(v),
    successMessage: t("admin.twoFactor.disable.done"),
    toastError: false,
    onSuccess: () => {
      onDone();
      onOpenChange(false);
    },
    onError: (e) => setErrors(codeErrors(t, e, t("admin.twoFactor.disable.wrongCode"), t("admin.twoFactor.disable.requiredBySettings"))),
  });
  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        if (!o) setErrors({});
        onOpenChange(o);
      }}
    >
      {open && (
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>{t("admin.twoFactor.disable.title")}</DialogTitle>
            <DialogDescription>{t("admin.twoFactor.disable.description")}</DialogDescription>
          </DialogHeader>
          <PasswordAndCodeForm
            submitLabel={t("admin.twoFactor.disable.submit")}
            submittingLabel={t("admin.twoFactor.disable.submitting")}
            destructive
            pending={disable.isPending}
            errors={errors}
            codeLabel={t("admin.twoFactor.disable.code")}
            codeHint={t("admin.twoFactor.disable.codeHint")}
            allowRecovery
            onSubmit={(v) => {
              setErrors({});
              disable.mutate(v);
            }}
            onCancel={() => onOpenChange(false)}
          />
        </DialogContent>
      )}
    </Dialog>
  );
}

function RegenerateCodesDialog({ open, onOpenChange, email }: { open: boolean; onOpenChange: (o: boolean) => void; email: string }) {
  const t = useT();
  const [errors, setErrors] = useState<FormErrors>({});
  const [codes, setCodes] = useState<string[] | null>(null);
  const regen = useAdminMutation({
    mutationFn: (v: { current_password: string; code: string }) => adminApi.regenerateRecoveryCodes(v),
    successMessage: t("admin.twoFactor.regenerate.done"),
    toastError: false,
    onSuccess: (r) => setCodes(r.recovery_codes),
    onError: (e) => setErrors(codeErrors(t, e, t("admin.twoFactor.regenerate.wrongCode"))),
  });
  const locked = codes !== null;
  const close = () => {
    setCodes(null);
    setErrors({});
    onOpenChange(false);
  };
  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        if (o) return onOpenChange(o);
        if (!locked) close();
      }}
    >
      {open && (
        <DialogContent
          className="sm:max-w-lg"
          showCloseButton={!locked}
          onEscapeKeyDown={(e) => locked && e.preventDefault()}
          onPointerDownOutside={(e) => e.preventDefault()}
          onInteractOutside={(e) => e.preventDefault()}
        >
          <DialogHeader>
            <DialogTitle>{locked ? t("admin.twoFactor.recovery.title") : t("admin.twoFactor.regenerate.title")}</DialogTitle>
            {!locked && <DialogDescription>{t("admin.twoFactor.regenerate.description")}</DialogDescription>}
          </DialogHeader>
          {codes ? (
            <RecoveryCodesPanel codes={codes} email={email} onDone={close} />
          ) : (
            <PasswordAndCodeForm
              submitLabel={t("admin.twoFactor.regenerate.submit")}
              submittingLabel={t("admin.twoFactor.regenerate.submitting")}
              pending={regen.isPending}
              errors={errors}
              codeLabel={t("admin.twoFactor.regenerate.code")}
              codeHint={t("admin.twoFactor.regenerate.codeHint")}
              allowRecovery={false}
              onSubmit={(v) => {
                setErrors({});
                regen.mutate(v);
              }}
              onCancel={close}
            />
          )}
        </DialogContent>
      )}
    </Dialog>
  );
}

// ---- sessions ---------------------------------------------------------------

export function SessionsCard() {
  const t = useT();
  const router = useRouter();
  const qc = useQueryClient();
  const sessions = useSessions();
  const [revoking, setRevoking] = useState<AdminSession | null>(null);
  const [othersOpen, setOthersOpen] = useState(false);

  const revoke = useAdminMutation({
    mutationFn: (s: AdminSession) => adminApi.revokeSession(s.id),
    // The current session is gone after this: refetching would only 401.
    invalidate: (_r, s) => (s.current ? [] : [adminKeys.sessions]),
    successMessage: (_r, s) => (s.current ? "" : t("admin.sessions.revoked")),
    onSuccess: (_r, s) => {
      if (!s.current) return;
      // Signing out this device is a logout.
      qc.clear();
      router.replace(loginUrl("/admin", "signedOut"));
    },
  });
  const revokeOthers = useAdminMutation({
    mutationFn: () => adminApi.revokeOtherSessions(),
    invalidate: [adminKeys.sessions],
    successMessage: (r) => t("admin.sessions.revokedOthers", { n: r.revoked }),
  });
  const others = (sessions.data ?? []).filter((s) => !s.current).length;

  const columns: Column<AdminSession>[] = [
    {
      id: "device",
      header: t("admin.sessions.columns.device"),
      cell: (s) => <SessionDevice session={s} />,
    },
    { id: "ip", header: t("admin.sessions.columns.ip"), cell: (s) => <span className="font-mono text-xs">{s.ip || "—"}</span> },
    { id: "created", header: t("admin.sessions.columns.signedIn"), cell: (s) => <RelativeTime iso={s.created_at} /> },
    { id: "expires", header: t("admin.sessions.columns.expires"), cell: (s) => <RelativeTime iso={s.expires_at} className="text-muted-foreground" /> },
    {
      id: "actions",
      header: <span className="sr-only">{t("admin.common.actions")}</span>,
      align: "right",
      cell: (s) => (
        <Button variant="outline" size="xs" onClick={() => setRevoking(s)} aria-label={`${s.current ? t("admin.sessions.revokeCurrent") : t("admin.sessions.revoke")}: ${s.ip}`}>
          <LogOutIcon data-icon="inline-start" aria-hidden />
          {s.current ? t("admin.sessions.revokeCurrent") : t("admin.sessions.revoke")}
        </Button>
      ),
    },
  ];

  return (
    <Section
      id="sessions"
      title={t("admin.sessions.title")}
      description={t("admin.sessions.description")}
      actions={
        <Button variant="outline" size="sm" disabled={others === 0 || revokeOthers.isPending} onClick={() => setOthersOpen(true)} title={others === 0 ? t("admin.sessions.noOthers") : undefined}>
          <LogOutIcon data-icon="inline-start" aria-hidden />
          {t("admin.sessions.revokeOthers")}
        </Button>
      }
      bodyClassName="p-0"
    >
      <DataTable
        caption={t("admin.sessions.title")}
        columns={columns}
        rows={sessions.data}
        getRowId={(s) => s.id}
        isLoading={sessions.isPending}
        error={sessions.error}
        onRetry={() => void sessions.refetch()}
        className="[&>div]:rounded-none [&>div]:border-0"
        empty={<EmptyState icon={MonitorSmartphoneIcon} title={t("admin.sessions.emptyTitle")} description={t("admin.sessions.emptyBody")} />}
        mobileCard={(s) => (
          <div className="flex items-start justify-between gap-3">
            <div className="min-w-0 space-y-1">
              <SessionDevice session={s} />
              <p className="text-xs text-muted-foreground">
                <span className="font-mono">{s.ip || "—"}</span> · <RelativeTime iso={s.created_at} />
              </p>
            </div>
            <Button variant="outline" size="xs" onClick={() => setRevoking(s)}>
              {s.current ? t("admin.sessions.revokeCurrent") : t("admin.sessions.revoke")}
            </Button>
          </div>
        )}
      />
      <ConfirmDialog
        open={revoking !== null}
        onOpenChange={(o) => !o && setRevoking(null)}
        title={revoking?.current ? t("admin.sessions.revokeCurrentTitle") : t("admin.sessions.revokeTitle")}
        description={revoking?.current ? t("admin.sessions.revokeCurrentBody") : t("admin.sessions.revokeBody")}
        confirmLabel={revoking?.current ? t("admin.sessions.revokeCurrent") : t("admin.sessions.revoke")}
        destructive
        onConfirm={() => revoking && revoke.mutateAsync(revoking)}
      />
      <ConfirmDialog
        open={othersOpen}
        onOpenChange={setOthersOpen}
        title={t("admin.sessions.revokeOthersTitle")}
        description={t("admin.sessions.revokeOthersBody")}
        confirmLabel={t("admin.sessions.revokeOthers")}
        destructive
        onConfirm={() => revokeOthers.mutateAsync()}
      />
    </Section>
  );
}

function SessionDevice({ session }: { session: AdminSession }) {
  const t = useT();
  const ua = describeUserAgent(session.user_agent);
  const label = ua ? [ua.browser, ua.os].filter(Boolean).join(" · ") : session.user_agent || t("admin.sessions.unknownDevice");
  return (
    <span className="flex min-w-0 items-center gap-2">
      <LaptopIcon className="size-4 shrink-0 text-muted-foreground" aria-hidden />
      <span className="truncate" title={session.user_agent || undefined}>
        {label}
      </span>
      {session.current && <StatusBadge tone="info">{t("admin.sessions.current")}</StatusBadge>}
    </span>
  );
}

function PasswordCard() {
  const t = useT();
  const [open, setOpen] = useState(false);
  return (
    <Section id="password" title={t("admin.security.password.title")} description={t("admin.security.password.description")}>
      <Button variant="outline" size="sm" onClick={() => setOpen(true)}>
        <KeyRoundIcon data-icon="inline-start" aria-hidden />
        {t("admin.security.password.change")}
      </Button>
      <ChangePasswordDialog open={open} onOpenChange={setOpen} />
    </Section>
  );
}
