"use client";

import { useId, useState } from "react";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Spinner } from "@/components/ui/spinner";
import { QrCode } from "@/components/checkout/qr-code";
import { describeError } from "@/lib/admin/errors";
import { adminKeys, useAdminMutation } from "@/lib/admin/queries";
import { groupSecret, OTP_LENGTH } from "@/lib/admin/two-factor";
import { adminApi } from "@/lib/api/admin";
import { isApiError } from "@/lib/api/client";
import type { TwoFactorSetupResponse } from "@/lib/api/types";
import { useT } from "@/lib/i18n";
import { cn } from "@/lib/utils";
import { CopyButton } from "../copy-button";
import { describedBy, FormError, FormField } from "../form-field";
import { OtpInput } from "./otp-input";
import { RecoveryCodesPanel } from "./recovery-codes";

export type SetupStep = "password" | "scan" | "codes";
const STEPS: SetupStep[] = ["password", "scan", "codes"];

/**
 * Enabling two-factor authentication: confirm the password
 * (`POST /auth/2fa/setup` → pending secret) → scan the QR code / type the key
 * and confirm a code (`POST /auth/2fa/enable`) → recovery codes, shown once.
 * `onComplete` fires after the operator confirmed they stored the codes.
 */
export function TwoFactorSetupWizard({
  email,
  onComplete,
  onCancel,
  onStepChange,
}: {
  email: string;
  onComplete: () => void;
  onCancel?: () => void;
  onStepChange?: (step: SetupStep) => void;
}) {
  const t = useT();
  const [step, setStepState] = useState<SetupStep>("password");
  const [setup, setSetup] = useState<TwoFactorSetupResponse | null>(null);
  const [codes, setCodes] = useState<string[] | null>(null);
  const setStep = (s: SetupStep) => {
    setStepState(s);
    onStepChange?.(s);
  };
  const n = STEPS.indexOf(step) + 1;
  const labels: Record<SetupStep, string> = {
    password: t("admin.twoFactor.setup.stepPassword"),
    scan: t("admin.twoFactor.setup.stepScan"),
    codes: t("admin.twoFactor.setup.stepCodes"),
  };
  return (
    <div className="space-y-5">
      <ol className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs" aria-label={t("admin.twoFactor.setup.stepOf", { n, total: STEPS.length })}>
        {STEPS.map((s, i) => (
          <li
            key={s}
            aria-current={s === step ? "step" : undefined}
            className={cn("flex items-center gap-1.5", s === step ? "font-medium text-foreground" : "text-muted-foreground")}
          >
            <span
              className={cn(
                "flex size-5 items-center justify-center rounded-full border text-[11px] tabular",
                i < n - 1 && "border-transparent bg-primary/15 text-primary",
                s === step && "border-transparent bg-primary text-primary-foreground",
              )}
              aria-hidden
            >
              {i + 1}
            </span>
            {labels[s]}
          </li>
        ))}
      </ol>
      {step === "password" && (
        <PasswordStep
          onCancel={onCancel}
          onDone={(r) => {
            setSetup(r);
            setStep("scan");
          }}
        />
      )}
      {step === "scan" && setup && (
        <ScanStep
          setup={setup}
          onCancel={onCancel}
          onDone={(c) => {
            setCodes(c);
            setSetup(null); // the secret is not needed (or shown) any more
            setStep("codes");
          }}
        />
      )}
      {step === "codes" && codes && (
        <RecoveryCodesPanel
          codes={codes}
          email={email}
          onDone={() => {
            setCodes(null);
            onComplete();
          }}
        />
      )}
    </div>
  );
}

function PasswordStep({ onDone, onCancel }: { onDone: (r: TwoFactorSetupResponse) => void; onCancel?: () => void }) {
  const t = useT();
  const id = useId();
  const [password, setPassword] = useState("");
  const [fieldError, setFieldError] = useState<string | null>(null);
  const [formError, setFormError] = useState<string | null>(null);
  const setup = useAdminMutation({
    mutationFn: (current_password: string) => adminApi.setupTwoFactor({ current_password }),
    toastError: false,
    onSuccess: (r) => onDone(r),
    onError: (e) => {
      setPassword("");
      if (isApiError(e) && e.fields?.current_password) setFieldError(t("admin.twoFactor.setup.wrongPassword"));
      else if (isApiError(e) && e.isConflict) setFormError(t("admin.twoFactor.setup.alreadyEnabled"));
      else setFormError(describeError(t, e));
    },
  });
  return (
    <form
      noValidate
      className="space-y-4"
      onSubmit={(e) => {
        e.preventDefault();
        setFormError(null);
        if (!password) return setFieldError(t("admin.password.required"));
        setFieldError(null);
        setup.mutate(password);
      }}
    >
      <FormField label={t("admin.twoFactor.setup.password")} htmlFor={id} description={t("admin.twoFactor.setup.passwordHint")} error={fieldError ?? undefined}>
        <Input
          id={id}
          type="password"
          autoComplete="current-password"
          autoFocus
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          aria-invalid={!!fieldError}
          aria-describedby={describedBy(id, { description: true, error: fieldError })}
        />
      </FormField>
      <FormError message={formError} />
      <div className="flex justify-end gap-2">
        {onCancel && (
          <Button type="button" variant="outline" onClick={onCancel}>
            {t("admin.common.cancel")}
          </Button>
        )}
        <Button type="submit" disabled={setup.isPending}>
          {setup.isPending && <Spinner data-icon="inline-start" />}
          {setup.isPending ? t("admin.twoFactor.setup.continuing") : t("admin.twoFactor.setup.continue")}
        </Button>
      </div>
    </form>
  );
}

function ScanStep({
  setup,
  onDone,
  onCancel,
}: {
  setup: TwoFactorSetupResponse;
  onDone: (codes: string[]) => void;
  onCancel?: () => void;
}) {
  const t = useT();
  const id = useId();
  const [code, setCode] = useState("");
  const [fieldError, setFieldError] = useState<string | null>(null);
  const [formError, setFormError] = useState<string | null>(null);
  const enable = useAdminMutation({
    mutationFn: (c: string) => adminApi.enableTwoFactor({ code: c }),
    // Enabling signs out the user's other sessions.
    invalidate: [adminKeys.sessions],
    successMessage: t("admin.twoFactor.setup.enabledToast"),
    toastError: false,
    onSuccess: (r) => onDone(r.recovery_codes),
    onError: (e) => {
      setCode("");
      if (isApiError(e) && e.fields?.code) setFieldError(t("admin.twoFactor.setup.wrongCode"));
      else setFormError(describeError(t, e));
    },
  });
  const submit = (c: string) => {
    setFormError(null);
    if (c.length !== OTP_LENGTH) return setFieldError(t("admin.login.codeRequired"));
    setFieldError(null);
    enable.mutate(c);
  };
  const grouped = groupSecret(setup.secret);
  return (
    <form
      noValidate
      className="space-y-4"
      onSubmit={(e) => {
        e.preventDefault();
        submit(code);
      }}
    >
      <div className="space-y-1">
        <p className="text-sm font-medium">{t("admin.twoFactor.setup.scanTitle")}</p>
        <p className="text-xs text-muted-foreground">{t("admin.twoFactor.setup.scanHint")}</p>
      </div>
      <div className="flex flex-col items-center gap-4 sm:flex-row sm:items-start">
        <QrCode value={setup.otpauth_uri} label={t("admin.twoFactor.setup.qrLabel")} className="size-44 shrink-0 border p-1" />
        <div className="min-w-0 space-y-1.5">
          <p className="text-xs text-pretty text-muted-foreground">{t("admin.twoFactor.setup.manualEntry")}</p>
          <div className="flex items-center gap-1">
            <code className="rounded-md border bg-muted/60 px-2 py-1 font-mono text-sm tracking-wide break-all" data-testid="totp-secret">
              {grouped}
            </code>
            <CopyButton value={setup.secret} label={t("admin.twoFactor.setup.secret")} size="icon-sm" toast />
          </div>
        </div>
      </div>
      <FormField label={t("admin.twoFactor.setup.code")} htmlFor={id} description={t("admin.twoFactor.setup.codeHint")} error={fieldError ?? undefined}>
        <OtpInput
          id={id}
          value={code}
          onChange={(v) => {
            setCode(v);
            if (fieldError) setFieldError(null);
          }}
          onComplete={submit}
          disabled={enable.isPending}
          invalid={!!fieldError}
          describedBy={describedBy(id, { description: true, error: fieldError })}
          className="max-w-56"
        />
      </FormField>
      <FormError message={formError} />
      <div className="flex justify-end gap-2">
        {onCancel && (
          <Button type="button" variant="outline" onClick={onCancel}>
            {t("admin.common.cancel")}
          </Button>
        )}
        <Button type="submit" disabled={enable.isPending}>
          {enable.isPending && <Spinner data-icon="inline-start" />}
          {enable.isPending ? t("admin.twoFactor.setup.enabling") : t("admin.twoFactor.setup.enable")}
        </Button>
      </div>
    </form>
  );
}

/**
 * The wizard in a dialog (account security page). While the recovery codes
 * are on screen the dialog can't be dismissed except through "Done".
 */
export function TwoFactorSetupDialog({
  open,
  onOpenChange,
  email,
  onComplete,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  email: string;
  onComplete: () => void;
}) {
  const t = useT();
  const [step, setStep] = useState<SetupStep>("password");
  const locked = step === "codes";
  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        if (!o && locked) return;
        if (!o) setStep("password");
        onOpenChange(o);
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
            <DialogTitle>{step === "codes" ? t("admin.twoFactor.recovery.title") : t("admin.twoFactor.setup.title")}</DialogTitle>
            <DialogDescription>{t("admin.twoFactor.description")}</DialogDescription>
          </DialogHeader>
          <TwoFactorSetupWizard
            email={email}
            onStepChange={setStep}
            onCancel={() => onOpenChange(false)}
            onComplete={() => {
              setStep("password");
              onComplete();
              onOpenChange(false);
            }}
          />
        </DialogContent>
      )}
    </Dialog>
  );
}
