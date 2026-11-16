"use client";

import { ArrowRightIcon, LayersIcon, ReceiptTextIcon } from "lucide-react";
import Link from "next/link";
import { Guilloche } from "@/components/brand/guilloche";
import { SiteHeader } from "@/components/site-header";
import { Button } from "@/components/ui/button";
import { useT } from "@/lib/i18n";

/** The root page: what this service is, and the way to the admin console. */
export function Landing() {
  const t = useT();
  return (
    <div className="flex min-h-dvh flex-col">
      <SiteHeader className="max-w-3xl" />
      <main className="mx-auto flex w-full max-w-3xl flex-1 flex-col px-4 pt-6 pb-16 sm:pt-14">
        <section className="relative overflow-hidden rounded-2xl border bg-card shadow-[0_1px_0_0_var(--border),0_24px_48px_-32px_rgb(19_32_29/0.35)]">
          <div className="relative h-28 overflow-hidden bg-slip-band sm:h-36">
            <Guilloche className="absolute inset-0 h-full w-full opacity-60" />
          </div>
          <div className="space-y-5 px-5 pt-6 pb-7 sm:px-10 sm:pt-8 sm:pb-10">
            <h1 className="font-heading text-3xl font-semibold tracking-tight text-balance sm:text-4xl">
              {t("common.appName")}
            </h1>
            <p className="text-lg font-medium text-pretty">{t("landing.tagline")}</p>
            <p className="max-w-prose leading-relaxed text-pretty text-muted-foreground">
              {t("landing.intro")}
            </p>
            <div className="flex flex-col gap-2 pt-2 sm:flex-row sm:items-center sm:gap-4">
              <Button asChild size="lg" className="h-11 px-5 text-[15px]">
                <Link href="/admin" prefetch={false}>
                  {t("landing.adminCta")}
                  <ArrowRightIcon data-icon="inline-end" aria-hidden />
                </Link>
              </Button>
              <p className="text-sm text-muted-foreground">{t("landing.adminHint")}</p>
            </div>
          </div>
        </section>

        <dl className="mt-8 grid gap-6 px-1 sm:grid-cols-2 sm:gap-10">
          <div className="space-y-1.5">
            <dt className="flex items-center gap-2 text-sm font-semibold">
              <ReceiptTextIcon className="size-4 text-primary" aria-hidden />
              {t("landing.checkoutTitle")}
            </dt>
            <dd className="text-sm leading-relaxed text-muted-foreground">{t("landing.checkoutHint")}</dd>
          </div>
          <div className="space-y-1.5">
            <dt className="flex items-center gap-2 text-sm font-semibold">
              <LayersIcon className="size-4 text-primary" aria-hidden />
              {t("landing.networksTitle")}
            </dt>
            <dd className="text-sm leading-relaxed text-muted-foreground">{t("landing.networks")}</dd>
          </div>
        </dl>
        <p className="mt-10 px-1 text-xs text-muted-foreground">{t("landing.docsHint")}</p>
      </main>
    </div>
  );
}
