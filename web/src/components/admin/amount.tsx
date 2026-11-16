"use client";

import { formatAmount, formatMoney, isDecimalString } from "@/lib/format/amount";
import { htmlLang, useLocale } from "@/lib/i18n";
import { cn } from "@/lib/utils";

/** Price in its currency: "$99.00", "¥688.00", "12.5 USDT". Never goes through a float. */
export function Money({
  amount,
  currency,
  className,
}: {
  amount: string | null | undefined;
  currency: string;
  className?: string;
}) {
  const { locale } = useLocale();
  if (!amount || !isDecimalString(amount)) return <span className={cn("text-muted-foreground", className)}>—</span>;
  return <span className={cn("font-mono tabular whitespace-nowrap", className)}>{formatMoney(amount, currency, htmlLang(locale))}</span>;
}

/** On-chain amount with its symbol, keeping the scale the backend sent ("99.00 USDT"). */
export function CryptoAmount({
  amount,
  symbol,
  className,
  symbolClassName,
}: {
  amount: string | null | undefined;
  symbol?: string;
  className?: string;
  symbolClassName?: string;
}) {
  if (!amount || !isDecimalString(amount)) return <span className={cn("text-muted-foreground", className)}>—</span>;
  return (
    <span className={cn("font-mono tabular whitespace-nowrap", className)}>
      {formatAmount(amount, { keepScale: true })}
      {symbol && <span className={cn("ml-1 text-muted-foreground", symbolClassName)}>{symbol}</span>}
    </span>
  );
}

/** USD volume string → "$12,345.67". */
export function formatUsd(amount: string | null | undefined, locale: "zh" | "en"): string {
  if (!amount || !isDecimalString(amount)) return "—";
  return formatMoney(formatAmount(amount, { group: false, maxFractionDigits: 2 }), "USD", htmlLang(locale));
}
