"use client";

import { ClockIcon } from "lucide-react";
import type { CheckoutInvoice } from "@/lib/api/types";
import { formatMoney, splitAmount } from "@/lib/format/amount";
import { formatCountdown, remainingFraction, remainingMs } from "@/lib/format/countdown";
import { htmlLang, useLocale, useT } from "@/lib/i18n";
import { cn } from "@/lib/utils";

/** Who is paid, for what, how much — and the time left while open. */
export function InvoiceStub({
  invoice,
  now,
  clockOffsetMs,
  showCountdown,
}: {
  invoice: CheckoutInvoice;
  now: number;
  clockOffsetMs: number;
  showCountdown: boolean;
}) {
  const t = useT();
  const { locale } = useLocale();
  const price = formatMoney(invoice.amount, invoice.currency, htmlLang(locale));
  const { int, frac } = splitAmount(price);
  const left = remainingMs(invoice.expires_at, now, clockOffsetMs);
  const fraction = remainingFraction(invoice.created_at, invoice.expires_at, now, clockOffsetMs);
  const urgent = left > 0 && left < 5 * 60_000;

  return (
    <div className="space-y-4">
      <div className="flex items-start justify-between gap-4">
        <div className="min-w-0 space-y-0.5">
          <p className="text-[11px] font-semibold tracking-[0.14em] text-muted-foreground uppercase">
            {t("checkout.merchantLabel")}
          </p>
          <p className="truncate font-heading text-base font-semibold">{invoice.merchant_name}</p>
        </div>
        {showCountdown && (
          <div
            className={cn(
              "flex shrink-0 items-center gap-1.5 rounded-full border bg-card/80 px-2.5 py-1 text-sm font-medium tabular backdrop-blur-sm",
              urgent && "border-warning-border bg-warning-surface text-warning",
              left === 0 && "border-destructive/30 text-destructive",
            )}
          >
            <ClockIcon className="size-3.5" aria-hidden />
            <span className="sr-only">{t("checkout.timeLeft")}</span>
            <time dateTime={invoice.expires_at}>
              {left > 0 ? formatCountdown(left) : t("checkout.expiredShort")}
            </time>
          </div>
        )}
      </div>

      <div className="space-y-1">
        <p className="text-sm text-pretty text-foreground/80">
          {invoice.title || t("checkout.paymentFallbackTitle")}
        </p>
        {invoice.description && (
          <p className="line-clamp-2 text-xs text-pretty text-muted-foreground">{invoice.description}</p>
        )}
        <p className="font-heading text-[2.15rem] leading-tight font-semibold tracking-tight tabular">
          <span className="sr-only">{t("checkout.amountDue")} </span>
          {int}
          {frac && <span className="text-foreground/55">.{frac}</span>}
        </p>
      </div>

      {showCountdown && (
        <div
          className="h-1 overflow-hidden rounded-full bg-foreground/10"
          aria-hidden
        >
          <div
            className={cn(
              "h-full rounded-full bg-primary transition-[width] duration-1000 ease-linear",
              urgent && "bg-warning",
            )}
            style={{ width: `${(fraction * 100).toFixed(2)}%` }}
          />
        </div>
      )}
    </div>
  );
}
