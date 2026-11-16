"use client";

import { zodResolver } from "@hookform/resolvers/zod";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowLeftIcon, CircleAlertIcon, EyeIcon, EyeOffIcon, InfoIcon, ShieldCheckIcon } from "lucide-react";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { useEffect, useId, useState } from "react";
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
import { adminApi } from "@/lib/api/admin";
import { isApiError } from "@/lib/api/client";
import type { LoginRequest } from "@/lib/api/types";
import { useT } from "@/lib/i18n";
import type { TranslateFn } from "@/lib/i18n/types";
import { safeNextPath } from "../auth";

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

  const login = useMutation({
    mutationFn: (body: LoginRequest) => adminApi.login(body),
    meta: { skipAuthRedirect: true },
    onSuccess: (user) => {
      // A new session: nothing cached from a previous one may survive.
      qc.clear();
      qc.setQueryData(adminKeys.me, user);
      router.replace(next);
    },
    onError: () => {
      form.setValue("password", "");
      form.setFocus("password");
    },
  });

  const errors = form.formState.errors;
  const submitError = login.error ? loginErrorMessage(t, login.error) : null;

  return (
    <div className="flex min-h-dvh flex-col">
      <SiteHeader className="max-w-5xl" />
      <main className="mx-auto flex w-full max-w-5xl flex-1 items-start justify-center px-4 pt-4 pb-16 sm:items-center sm:pt-0">
        <div className="w-full max-w-sm">
          <section className="overflow-hidden rounded-2xl border bg-card shadow-[0_1px_0_0_var(--border),0_24px_48px_-32px_rgb(19_32_29/0.35)]">
            <div className="relative h-16 overflow-hidden border-b bg-slip-band">
              <Guilloche className="absolute inset-0 h-full w-full opacity-50" />
            </div>
            <div className="space-y-6 px-5 pt-5 pb-6 sm:px-7">
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
            </div>
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
