"use client";

import { useQueryClient } from "@tanstack/react-query";
import { LogOutIcon, ShieldAlertIcon } from "lucide-react";
import { useRouter } from "next/navigation";
import { useState } from "react";
import { toast } from "sonner";
import { BrandMark } from "@/components/brand/brand-mark";
import { LocaleSwitcher } from "@/components/locale-switcher";
import { ThemeToggle } from "@/components/theme-toggle";
import { Button } from "@/components/ui/button";
import { adminApi } from "@/lib/api/admin";
import { useT } from "@/lib/i18n";
import { TwoFactorSetupWizard } from "./setup-wizard";

/**
 * Shown instead of the console while `require_2fa` is on and the signed-in
 * account has no two-factor authentication: only the setup flow (and signing
 * out) is available until it is done.
 */
export function ForcedTwoFactorSetup({ email, onDone, signOutUrl }: { email: string; onDone: () => void; signOutUrl: string }) {
  const t = useT();
  const router = useRouter();
  const qc = useQueryClient();
  const [busy, setBusy] = useState(false);

  async function signOut() {
    setBusy(true);
    try {
      await adminApi.logout();
    } catch (e) {
      if ((e as { status?: number }).status !== 401) {
        toast.error(t("admin.shell.signOutFailed"));
        setBusy(false);
        return;
      }
    }
    qc.clear();
    router.replace(signOutUrl);
  }

  return (
    <div className="flex min-h-dvh flex-col">
      <header className="flex h-14 items-center gap-2 border-b px-4">
        <BrandMark />
        <div className="ml-auto flex items-center gap-0.5">
          <LocaleSwitcher />
          <ThemeToggle />
          <Button variant="ghost" size="sm" disabled={busy} onClick={() => void signOut()}>
            <LogOutIcon data-icon="inline-start" aria-hidden />
            {busy ? t("admin.shell.signingOut") : t("admin.shell.signOut")}
          </Button>
        </div>
      </header>
      <main id="admin-main" className="mx-auto w-full max-w-xl flex-1 px-4 pt-8 pb-16">
        <section className="rounded-2xl border bg-card p-5 shadow-sm sm:p-7" aria-labelledby="forced-2fa-title">
          <div className="mb-5 space-y-2">
            <div className="flex items-center gap-2 text-warning">
              <ShieldAlertIcon className="size-5" aria-hidden />
              <h1 id="forced-2fa-title" className="font-heading text-xl font-semibold tracking-tight text-foreground">
                {t("admin.twoFactor.forced.title")}
              </h1>
            </div>
            <p className="text-sm text-muted-foreground">{t("admin.twoFactor.forced.description")}</p>
            <p className="text-xs text-muted-foreground">{t("admin.twoFactor.forced.signedInAs", { email })}</p>
          </div>
          <TwoFactorSetupWizard email={email} onComplete={onDone} />
        </section>
      </main>
    </div>
  );
}
