"use client";

import { FlaskConicalIcon, ShieldAlertIcon, TriangleAlertIcon } from "lucide-react";
import type { CheckoutInvoice, Quote } from "@/lib/api/types";
import { amountToPay, hasReceivedFunds } from "@/lib/checkout/payment";
import { formatAmount, formatMoney, plainAmount, splitAmount } from "@/lib/format/amount";
import { chunk, splitAddressForVerification } from "@/lib/format/address";
import { htmlLang, useLocale, useT } from "@/lib/i18n";
import { cn } from "@/lib/utils";
import { CopyButton } from "./copy-button";
import { NetworkBadge, TestnetBadge } from "./network-badge";

/** The exact amount, address and network the customer must use. */
export function PaymentDetails({ invoice, quote }: { invoice: CheckoutInvoice; quote: Quote }) {
  const t = useT();
  const { locale } = useLocale();
  const isStatic = quote.wallet_kind === "static";
  const partial = hasReceivedFunds(quote);
  const amount = amountToPay(quote);
  // The full amount is quoted at the asset's precision: keep its digits ("99.00").
  const shown = formatAmount(amount, { keepScale: !partial });
  const { int, frac } = splitAmount(shown);
  const showFiat = !partial && invoice.currency.toUpperCase() !== quote.symbol.toUpperCase();

  const amountLabel = partial
    ? t("checkout.pay.remainingToSend")
    : isStatic
      ? t("checkout.pay.sendExactly")
      : t("checkout.pay.amountToSend");

  return (
    <div className="space-y-5">
      {/* Network first: choosing the wrong one is the costliest mistake. */}
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-xs font-medium text-muted-foreground">{t("checkout.pay.network")}</span>
        <NetworkBadge asset={quote} />
        {quote.is_testnet && <TestnetBadge />}
      </div>

      <div className="space-y-2">
        <div className="flex items-center justify-between gap-3">
          <h3 className="text-xs font-semibold tracking-[0.12em] text-muted-foreground uppercase">{amountLabel}</h3>
          <CopyButton
            value={plainAmount(amount)}
            label={t("checkout.pay.amountLabel")}
            copyLabel={t("checkout.pay.copyAmount")}
            showText
          />
        </div>
        <p className="flex flex-wrap items-baseline gap-x-2 font-mono leading-none tabular" translate="no">
          <span className="text-[1.9rem] font-semibold tracking-tight break-all sm:text-[2.1rem]">
            {int}
            {frac && (
              <>
                <span className="-mx-[0.14em] text-foreground/60">.</span>
                <span className={cn(isStatic ? "text-foreground" : "text-foreground/80")}>{frac}</span>
              </>
            )}
          </span>
          <span className="text-base font-medium text-muted-foreground">{quote.symbol}</span>
        </p>
        {showFiat && (
          <p className="text-sm text-muted-foreground tabular">
            {t("checkout.pay.approx", {
              amount: formatMoney(invoice.amount, invoice.currency, htmlLang(locale)),
            })}
          </p>
        )}
      </div>

      {isStatic && (
        <div
          role="note"
          className="flex gap-3 rounded-xl border border-warning-border bg-warning-surface p-3.5 text-warning"
        >
          <ShieldAlertIcon className="mt-0.5 size-5 shrink-0" aria-hidden />
          <div className="space-y-1">
            <p className="text-sm font-semibold">{t("checkout.pay.staticTitle")}</p>
            <p className="text-[13px] leading-relaxed text-foreground/85">
              {t("checkout.pay.staticBody", {
                amount: formatAmount(amount, { keepScale: true, group: false }),
                symbol: quote.symbol,
              })}
            </p>
          </div>
        </div>
      )}

      <AddressBlock address={quote.address} />

      <div role="note" className="flex gap-2.5 rounded-xl bg-muted/70 p-3.5 text-[13px] leading-relaxed">
        <TriangleAlertIcon className="mt-0.5 size-4 shrink-0 text-warning" aria-hidden />
        <p>{t("checkout.pay.networkWarning", { network: quote.chain_name, symbol: quote.symbol })}</p>
      </div>

      {quote.is_testnet && (
        <p className="flex items-center gap-2 text-xs text-muted-foreground">
          <FlaskConicalIcon className="size-3.5" aria-hidden />
          {t("checkout.pay.testnetNotice")}
        </p>
      )}
    </div>
  );
}

/** Full address, chunked, with the head and tail emphasised for checking. */
export function AddressBlock({ address }: { address: string }) {
  const t = useT();
  const { head, middle, tail } = splitAddressForVerification(address, 6);
  return (
    <div className="space-y-2">
      <div className="flex items-center justify-between gap-3">
        <h3 className="text-xs font-semibold tracking-[0.12em] text-muted-foreground uppercase">
          {t("checkout.pay.address")}
        </h3>
        <CopyButton
          value={address}
          label={t("checkout.pay.addressLabel")}
          copyLabel={t("checkout.pay.copyAddress")}
          showText
        />
      </div>
      <p
        className="rounded-xl border bg-background/60 px-3.5 py-3 font-mono text-[15px] leading-relaxed break-anywhere select-all"
        translate="no"
      >
        {/* Visual grouping uses padding only, so a manual copy stays exact. */}
        <span className="font-semibold text-primary">{head}</span>
        {chunk(middle, 4).map((c, i) => (
          <span key={i} className="pl-[0.22em] text-foreground/70">
            {c}
          </span>
        ))}
        {tail && <span className="pl-[0.22em] font-semibold text-primary">{tail}</span>}
      </p>
      <p className="text-xs text-muted-foreground">{t("checkout.pay.addressVerify")}</p>
    </div>
  );
}
