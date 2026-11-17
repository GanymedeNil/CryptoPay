"use client";

import { zodResolver } from "@hookform/resolvers/zod";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowLeftIcon, CircleAlertIcon, EyeIcon, EyeOffIcon, InfoIcon, KeyRoundIcon, ShieldCheckIcon, SmartphoneIcon } from "lucide-react";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { useEffect, useId, useRef, useState } from "react";
import { useForm } from "react-hook-form";
import { z } from "zod";
import { BrandMark } from "@/components/brand/brand-mark";
import { Guilloche } from "@/components/brand/guilloche";
import { SiteHeader } from "@/components/site-header";
import { Button } from "@/components/ui/button";
import { Field, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Spinner } from "@/components/ui/spinner";
import { adminKeys } from "@/lib/admin/queries";
import { isLoginChallenge, MAX_CHALLENGE_ATTEMPTS, normalizeRecoveryCode, OTP_LENGTH } from "@/lib/admin/two-factor";
import { adminApi } from "@/lib/api/admin";
import { isApiError } from "@/lib/api/client";
import type { AdminMe, AdminUser, LoginChallenge, LoginRequest, LoginTwoFactorRequest } from "@/lib/api/types";
import { useT } from "@/lib/i18n";
import type { TranslateFn } from "@/lib/i18n/types";
import { safeNextPath } from "../auth";
import { OtpInput } from "../two-factor/otp-input";

function schema(t: TranslateFn) {
  return z.object({
    email: z.string().trim().min(1, t("admin.login.emailRequired")).email(t("admin.login.emailInvalid")),
    password: z.string().min(1, t("admin.login.passwordRequired")),
  });
}

function loginErrorMessage(t: TranslateFn, error: unknown): string {
  if (isApiError(error)) {
    if (error.isUnauthorized) return t("admin.login.invalidCredentials");
    if (error.isRateLimited) return t("admin.login.rateLimited");
    if (error.isNetworkError || error.status >= 500) return t("admin.login.network");
    return t("admin.login.failed", { message: error.message });
  }
  return t("admin.errors.generic");
}

export function LoginPage() {
  const t = useT();
  const router = useRouter();
  const params = useSearchParams();
  const qc = useQueryClient();
  const next = safeNextPath(params.get("next"));
  const reason = params.get("reason");
  const ids = { email: useId(), password: useId(), error: useId() };
  const [showPassword, setShowPassword] = useState(false);

  // Already signed in (e.g. opened /admin/login in a second tab): go straight in.
  const me = useQuery({
    queryKey: adminKeys.me,
    queryFn: ({ signal }) => adminApi.me({ signal }),
    retry: false,
    staleTime: 0,
    enabled: reason !== "signedOut",
  });
  useEffect(() => {
    if (me.data) router.replace(next);
  }, [me.data, next, router]);

  const form = useForm<z.infer<ReturnType<typeof schema>>>({
    resolver: zodResolver(schema(t)),
    defaultValues: { email: "", password: "" },
  });
  // Set when the account has two-factor authentication (202 from /auth/login).
  const [challenge, setChallenge] = useState<LoginChallenge | null>(null);

  const signedIn = (user: AdminUser) => {
    // A new session: nothing cached from a previous one may survive.
    qc.clear();
    qc.setQueryData<AdminMe>(adminKeys.me, { ...user, two_factor_setup_required: false });
    // /auth/me is the source of truth for "setup required": reload it in the console.
    void qc.invalidateQueries({ queryKey: adminKeys.me, refetchType: "none" });
    router.replace(next);
  };

  const login = useMutation({
    mutationFn: (body: LoginRequest) => adminApi.login(body),
    meta: { skipAuthRedirect: true },
    onSuccess: (res) => {
      form.setValue("password", "");
      if (isLoginChallenge(res)) {
        setChallenge(res);
        return;
      }
      signedIn(res);
    },
    onError: () => {
      form.setValue("password", "");
      form.setFocus("password");
    },
  });

  const errors = form.formState.errors;
  const submitError = login.error ? loginErrorMessage(t, login.error) : null;

  if (challenge) {
    return (
      <LoginFrame>
        <TwoFactorStep
          // A new challenge starts with a fresh attempt count.
          key={challenge.challenge_token}
          challenge={challenge}
          onSignedIn={signedIn}
          onBack={() => {
            setChallenge(null);
            login.reset();
            setTimeout(() => form.setFocus("password"), 0);
          }}
        />
      </LoginFrame>
    );
  }

  return (
    <LoginFrame>
      <div className="space-y-2">
        <BrandMark />
        <h1 className="font-heading text-xl font-semibold tracking-tight">{t("admin.login.title")}</h1>
        <p className="text-sm text-muted-foreground">{t("admin.login.subtitle")}</p>
      </div>

      {reason && !login.error && (
        <p className="flex items-start gap-2 rounded-lg border bg-muted/50 px-3 py-2 text-sm" role="status">
          <InfoIcon className="mt-0.5 size-4 shrink-0 text-muted-foreground" aria-hidden />
          {reason === "signedOut" ? t("admin.login.signedOut") : t("admin.login.sessionExpired")}
        </p>
      )}

      <form
        noValidate
        onSubmit={form.handleSubmit((v) => login.mutate({ email: v.email.trim(), password: v.password }))}
        aria-describedby={submitError ? ids.error : undefined}
      >
        <FieldGroup className="gap-4">
          <Field data-invalid={!!errors.email}>
            <FieldLabel htmlFor={ids.email}>{t("admin.login.email")}</FieldLabel>
            <Input
              id={ids.email}
              type="email"
              autoComplete="username"
              inputMode="email"
              autoFocus
              aria-invalid={!!errors.email}
              className="h-10"
              {...form.register("email")}
            />
            <FieldError errors={[errors.email]} />
          </Field>
          <Field data-invalid={!!errors.password}>
            <FieldLabel htmlFor={ids.password}>{t("admin.login.password")}</FieldLabel>
            <div className="relative">
              <Input
                id={ids.password}
                type={showPassword ? "text" : "password"}
                autoComplete="current-password"
                aria-invalid={!!errors.password}
                className="h-10 pr-10"
                {...form.register("password")}
              />
              <Button
                type="button"
                variant="ghost"
                size="icon-sm"
                className="absolute top-1/2 right-1 -translate-y-1/2 text-muted-foreground"
                aria-label={showPassword ? t("admin.login.hidePassword") : t("admin.login.showPassword")}
                aria-pressed={showPassword}
                onClick={() => setShowPassword((s) => !s)}
              >
                {showPassword ? <EyeOffIcon aria-hidden /> : <EyeIcon aria-hidden />}
              </Button>
            </div>
            <FieldError errors={[errors.password]} />
          </Field>

          {submitError && (
            <p
              id={ids.error}
              role="alert"
              className="flex items-start gap-2 rounded-lg border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm text-destructive"
            >
              <CircleAlertIcon className="mt-0.5 size-4 shrink-0" aria-hidden />
              {submitError}
            </p>
          )}

          <Button type="submit" size="lg" className="h-10 w-full" disabled={login.isPending || login.isSuccess}>
            {(login.isPending || login.isSuccess) && <Spinner data-icon="inline-start" />}
            {login.isPending || login.isSuccess ? t("admin.login.submitting") : t("admin.login.submit")}
          </Button>
        </FieldGroup>
      </form>
    </LoginFrame>
  );
}

/** Card, brand band and footer links shared by both sign-in steps. */
function LoginFrame({ children }: { children: React.ReactNode }) {
  const t = useT();
  return (
    <div className="flex min-h-dvh flex-col">
      <SiteHeader className="max-w-5xl" />
      <main className="mx-auto flex w-full max-w-5xl flex-1 items-start justify-center px-4 pt-4 pb-16 sm:items-center sm:pt-0">
        <div className="w-full max-w-sm">
          <section className="overflow-hidden rounded-2xl border bg-card shadow-[0_1px_0_0_var(--border),0_24px_48px_-32px_rgb(19_32_29/0.35)]">
            <div className="relative h-16 overflow-hidden border-b bg-slip-band">
              <Guilloche className="absolute inset-0 h-full w-full opacity-50" />
            </div>
            <div className="space-y-6 px-5 pt-5 pb-6 sm:px-7">{children}</div>
          </section>
          <p className="mt-4 flex items-start gap-2 px-1 text-xs text-muted-foreground">
            <ShieldCheckIcon className="mt-px size-3.5 shrink-0 text-primary" aria-hidden />
            {t("admin.login.nonCustodial")}
          </p>
          <Button asChild variant="link" size="sm" className="mt-2 px-1 text-muted-foreground">
            <Link href="/">
              <ArrowLeftIcon data-icon="inline-start" aria-hidden />
              {t("admin.login.backHome")}
            </Link>
          </Button>
        </div>
      </main>
    </div>
  );
}

type StepError =
  | { kind: "wrong"; left: number }
  | { kind: "expired" }
  | { kind: "usedUp" }
  | { kind: "rateLimited" }
  | { kind: "other"; message: string };

/**
 * Second sign-in step (`POST /auth/login/2fa`). The server answers every
 * failure with the same 401, so the page tells "wrong code" from "start over"
 * itself: the challenge expires at `expires_at` and dies after
 * `MAX_CHALLENGE_ATTEMPTS` wrong codes.
 */
function TwoFactorStep({
  challenge,
  onSignedIn,
  onBack,
}: {
  challenge: LoginChallenge;
  onSignedIn: (user: AdminUser) => void;
  onBack: () => void;
}) {
  const t = useT();
  const ids = { code: useId(), recovery: useId(), error: useId(), hint: useId() };
  const [mode, setMode] = useState<"code" | "recovery">("code");
  const [code, setCode] = useState("");
  const [recovery, setRecovery] = useState("");
  const [wrong, setWrong] = useState(0);
  const [error, setError] = useState<StepError | null>(null);
  const [inputError, setInputError] = useState<string | null>(null);
  const recoveryRef = useRef<HTMLInputElement>(null);
  const expiresAt = Date.parse(challenge.expires_at);

  // Say so as soon as the challenge times out, not only on the next submit.
  useEffect(() => {
    const ms = expiresAt - Date.now();
    if (!Number.isFinite(ms)) return;
    const timer = setTimeout(() => setError({ kind: "expired" }), Math.max(ms, 0));
    return () => clearTimeout(timer);
  }, [expiresAt]);

  const verify = useMutation({
    mutationFn: (body: LoginTwoFactorRequest) => adminApi.loginTwoFactor(body),
    meta: { skipAuthRedirect: true },
    onSuccess: (user) => onSignedIn(user),
    onError: (e) => {
      setCode("");
      setRecovery("");
      if (isApiError(e) && e.isUnauthorized) {
        const n = wrong + 1;
        setWrong(n);
        if (Date.now() >= expiresAt) setError({ kind: "expired" });
        else if (n >= MAX_CHALLENGE_ATTEMPTS) setError({ kind: "usedUp" });
        else setError({ kind: "wrong", left: MAX_CHALLENGE_ATTEMPTS - n });
      } else if (isApiError(e) && e.isRateLimited) {
        setError({ kind: "rateLimited" });
      } else {
        setError({ kind: "other", message: loginErrorMessage(t, e) });
      }
      if (mode === "recovery") setTimeout(() => recoveryRef.current?.focus(), 0);
    },
  });

  const dead = error?.kind === "expired" || error?.kind === "usedUp" || error?.kind === "rateLimited";
  const submit = (value: string) => {
    if (dead || verify.isPending) return;
    if (Date.now() >= expiresAt) return setError({ kind: "expired" });
    if (mode === "code") {
      if (value.length !== OTP_LENGTH) return setInputError(t("admin.login.codeRequired"));
      setInputError(null);
      verify.mutate({ challenge_token: challenge.challenge_token, code: value });
    } else {
      const rc = normalizeRecoveryCode(value);
      if (!rc) return setInputError(t("admin.login.recoveryRequired"));
      setInputError(null);
      verify.mutate({ challenge_token: challenge.challenge_token, recovery_code: rc });
    }
  };

  let message: string | null = null;
  switch (error?.kind) {
    case "wrong":
      message = t(mode === "code" ? "admin.login.wrongCode" : "admin.login.wrongRecoveryCode", { n: error.left });
      break;
    case "expired":
      message = t("admin.login.challengeExpired");
      break;
    case "usedUp":
      message = t("admin.login.challengeUsedUp");
      break;
    case "rateLimited":
      message = t("admin.login.twoFactorRateLimited");
      break;
    case "other":
      message = error.message;
      break;
  }

  const busy = verify.isPending || verify.isSuccess;
  return (
    <>
      <div className="space-y-2">
        <BrandMark />
        <h1 className="flex items-center gap-2 font-heading text-xl font-semibold tracking-tight">{t("admin.login.twoFactorTitle")}</h1>
        <p className="text-sm text-muted-foreground">
          {mode === "code" ? t("admin.login.twoFactorSubtitle") : t("admin.login.recoverySubtitle")}
        </p>
      </div>
      <form
        noValidate
        onSubmit={(e) => {
          e.preventDefault();
          submit(mode === "code" ? code : recovery);
        }}
        aria-describedby={message ? ids.error : undefined}
      >
        <FieldGroup className="gap-4">
          {mode === "code" ? (
            <Field data-invalid={!!inputError}>
              <FieldLabel htmlFor={ids.code}>{t("admin.login.code")}</FieldLabel>
              <OtpInput
                id={ids.code}
                value={code}
                onChange={(v) => {
                  setCode(v);
                  if (inputError) setInputError(null);
                }}
                onComplete={submit}
                autoFocus
                disabled={busy || dead}
                invalid={!!inputError}
                describedBy={ids.hint}
              />
              <p id={ids.hint} className="text-xs text-muted-foreground">
                {t("admin.login.codeHint")}
              </p>
              {inputError && <FieldError>{inputError}</FieldError>}
            </Field>
          ) : (
            <Field data-invalid={!!inputError}>
              <FieldLabel htmlFor={ids.recovery}>{t("admin.login.recoveryCode")}</FieldLabel>
              <Input
                id={ids.recovery}
                ref={recoveryRef}
                value={recovery}
                onChange={(e) => {
                  setRecovery(e.target.value);
                  if (inputError) setInputError(null);
                }}
                autoComplete="off"
                autoCapitalize="none"
                spellCheck={false}
                autoFocus
                placeholder="xxxxx-xxxxx"
                disabled={busy || dead}
                aria-invalid={!!inputError}
                className="h-10 font-mono tracking-wide"
              />
              {inputError && <FieldError>{inputError}</FieldError>}
            </Field>
          )}

          {message && (
            <div
              id={ids.error}
              role="alert"
              className="space-y-2 rounded-lg border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm text-destructive"
            >
              <p className="flex items-start gap-2">
                <CircleAlertIcon className="mt-0.5 size-4 shrink-0" aria-hidden />
                {message}
              </p>
            </div>
          )}

          {dead ? (
            <Button type="button" size="lg" className="h-10 w-full" onClick={onBack}>
              {t("admin.login.startOver")}
            </Button>
          ) : (
            <Button type="submit" size="lg" className="h-10 w-full" disabled={busy}>
              {busy && <Spinner data-icon="inline-start" />}
              {busy ? t("admin.login.verifying") : t("admin.login.verify")}
            </Button>
          )}
          {!dead && (
            <Button
              type="button"
              variant="link"
              size="sm"
              className="h-auto self-start px-0"
              onClick={() => {
                setMode((m) => (m === "code" ? "recovery" : "code"));
                setInputError(null);
                if (error?.kind === "wrong" || error?.kind === "other") setError(null);
              }}
            >
              {mode === "code" ? <KeyRoundIcon data-icon="inline-start" aria-hidden /> : <SmartphoneIcon data-icon="inline-start" aria-hidden />}
              {mode === "code" ? t("admin.login.useRecoveryCode") : t("admin.login.useAuthenticator")}
            </Button>
          )}
        </FieldGroup>
      </form>
      <Button type="button" variant="ghost" size="sm" className="-ml-2 text-muted-foreground" onClick={onBack}>
        <ArrowLeftIcon data-icon="inline-start" aria-hidden />
        {t("admin.login.backToPassword")}
      </Button>
    </>
  );
}
