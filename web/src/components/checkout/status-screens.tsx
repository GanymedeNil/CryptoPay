"use client";

import {
  ArrowLeftIcon,
  CircleCheckBigIcon,
  CircleXIcon,
  HourglassIcon,
  RefreshCwIcon,
  SearchXIcon,
  TriangleAlertIcon,
  WifiOffIcon,
} from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import { Spinner } from "@/components/ui/spinner";
import type { CheckoutView } from "@/lib/api/types";
import { redirectTo } from "@/lib/browser";
import { formatAmount, isZeroDecimal } from "@/lib/format/amount";
import { formatDateTime } from "@/lib/format/date";
import { useLocale, useT } from "@/lib/i18n";
import { cn } from "@/lib/utils";
import { networkLabel } from "./network-badge";
import { TransfersList } from "./transfers-list";

/** Only http(s) URLs may be used for redirects / links back to the merchant. */
export function safeHttpUrl(url: string | null | undefined): string | null {
  if (!url) return null;
  try {
    const u = new URL(url);
    return u.protocol === "https:" || u.protocol === "http:" ? u.toString() : null;
  } catch {
    return null;
  }
}

export const SUCCESS_REDIRECT_SECONDS = 6;

function StatusHeader({
  icon: Icon,
  tone,
  title,
  body,
}: {
  icon: React.ComponentType<{ className?: string; "aria-hidden"?: boolean }>;
  tone: "success" | "warning" | "destructive" | "muted";
  title: string;
  body?: React.ReactNode;
}) {
  return (
    <div className="space-y-3">
      <span
        className={cn(
          "inline-flex size-11 items-center justify-center rounded-full",
          tone === "success" && "bg-success-surface text-success",
          tone === "warning" && "bg-warning-surface text-warning",
          tone === "destructive" && "bg-destructive/10 text-destructive",
          tone === "muted" && "bg-muted text-muted-foreground",
        )}
      >
        <Icon className="size-5" aria-hidden />
      </span>
      <h2 className="font-heading text-xl font-semibold tracking-tight text-balance">{title}</h2>
      {body && <div className="text-sm leading-relaxed text-pretty text-muted-foreground">{body}</div>}
    </div>
  );
}

function MerchantLink({ href, label }: { href: string | null; label: string }) {
  if (!href) return null;
  return (
    <Button asChild variant="outline" className="h-11 rounded-xl px-4">
      <a href={href} rel="noreferrer">
        <ArrowLeftIcon data-icon="inline-start" aria-hidden />
        {label}
      </a>
    </Button>
  );
}

// ---- paid -------------------------------------------------------------------

export function PaidScreen({ view }: { view: CheckoutView }) {
  const t = useT();
  const { locale } = useLocale();
  const { invoice, quote, transfers } = view;
  const successUrl = safeHttpUrl(invoice.success_url);
  const [seconds, setSeconds] = useState(SUCCESS_REDIRECT_SECONDS);
  const [cancelled, setCancelled] = useState(false);
  const redirected = useRef(false);

  useEffect(() => {
    if (!successUrl || cancelled) return;
    const id = setInterval(() => setSeconds((s) => Math.max(0, s - 1)), 1000);
    return () => clearInterval(id);
  }, [successUrl, cancelled]);

  useEffect(() => {
    if (successUrl && !cancelled && seconds === 0 && !redirected.current) {
      redirected.current = true;
      redirectTo(successUrl);
    }
  }, [seconds, successUrl, cancelled]);

  return (
    <div className="space-y-6">
      <StatusHeader
        icon={CircleCheckBigIcon}
        tone="success"
        title={t("checkout.paid.title")}
        body={t("checkout.paid.body", { merchant: invoice.merchant_name })}
      />

      {quote && (
        <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2.5 rounded-xl border bg-background/50 p-4 text-sm">
          <dt className="text-muted-foreground">{t("checkout.paid.amountPaid")}</dt>
          <dd className="text-right font-mono font-semibold tabular" translate="no">
            {formatAmount(isZeroDecimal(quote.amount_confirmed) ? quote.amount : quote.amount_confirmed)} {quote.symbol}
          </dd>
          <dt className="text-muted-foreground">{t("checkout.paid.network")}</dt>
          <dd className="text-right">{networkLabel(quote)}</dd>
          {invoice.paid_at && (
            <>
              <dt className="text-muted-foreground">{t("checkout.paid.paidAt")}</dt>
              <dd className="text-right tabular">{formatDateTime(invoice.paid_at, locale)}</dd>
            </>
          )}
        </dl>
      )}

      {(invoice.overpaid || invoice.paid_late) && (
        <ul className="space-y-2 text-sm">
          {invoice.overpaid && (
            <li className="flex gap-2.5 rounded-xl border border-warning-border bg-warning-surface p-3 text-foreground/90">
              <TriangleAlertIcon className="mt-0.5 size-4 shrink-0 text-warning" aria-hidden />
              {t("checkout.paid.overpaid")}
            </li>
          )}
          {invoice.paid_late && (
            <li className="flex gap-2.5 rounded-xl bg-muted p-3 text-foreground/90">
              <HourglassIcon className="mt-0.5 size-4 shrink-0 text-muted-foreground" aria-hidden />
              {t("checkout.paid.paidLate")}
            </li>
          )}
        </ul>
      )}

      <TransfersList transfers={transfers} />

      {successUrl ? (
        <div className="space-y-3 rounded-xl bg-muted/70 p-4">
          {!cancelled && (
            <p className="text-sm tabular" aria-live="polite">
              {t("checkout.paid.redirecting", { seconds })}
            </p>
          )}
          <div className="flex flex-wrap gap-2">
            <Button asChild className="h-11 rounded-xl px-4">
              <a href={successUrl} rel="noreferrer">
                {t("checkout.paid.returnNow")}
              </a>
            </Button>
            {!cancelled && (
              <Button variant="ghost" className="h-11 rounded-xl" onClick={() => setCancelled(true)}>
                {t("checkout.paid.stay")}
              </Button>
            )}
          </div>
        </div>
      ) : (
        <p className="text-sm text-muted-foreground">{t("checkout.paid.safeToClose")}</p>
      )}
    </div>
  );
}

// ---- expired / underpaid / cancelled ----------------------------------------

export function ExpiredScreen({
  view,
  onCheckAgain,
  checking,
}: {
  view: CheckoutView;
  onCheckAgain: () => void;
  checking: boolean;
}) {
  const t = useT();
  const back = safeHttpUrl(view.invoice.cancel_url);
  return (
    <div className="space-y-6">
      <StatusHeader icon={HourglassIcon} tone="muted" title={t("checkout.expired.title")} body={t("checkout.expired.body")} />
      <TransfersList transfers={view.transfers} />
      <div className="flex flex-wrap gap-2">
        <Button className="h-11 rounded-xl px-4" onClick={onCheckAgain} disabled={checking}>
          {checking ? <Spinner data-icon="inline-start" /> : <RefreshCwIcon data-icon="inline-start" aria-hidden />}
          {checking ? t("checkout.expired.checking") : t("checkout.expired.checkAgain")}
        </Button>
        <MerchantLink href={back} label={t("checkout.expired.backToMerchant")} />
      </div>
    </div>
  );
}

export function UnderpaidScreen({
  view,
  onCheckAgain,
  checking,
}: {
  view: CheckoutView;
  onCheckAgain: () => void;
  checking: boolean;
}) {
  const t = useT();
  const { quote } = view;
  const back = safeHttpUrl(view.invoice.cancel_url);
  return (
    <div className="space-y-6">
      <StatusHeader
        icon={TriangleAlertIcon}
        tone="warning"
        title={t("checkout.underpaid.title")}
        body={
          quote
            ? t("checkout.underpaid.body", {
                received: formatAmount(quote.amount_confirmed),
                due: formatAmount(quote.amount),
                symbol: quote.symbol,
              })
            : undefined
        }
      />
      <TransfersList transfers={view.transfers} />
      <div className="flex flex-wrap gap-2">
        <Button variant="outline" className="h-11 rounded-xl px-4" onClick={onCheckAgain} disabled={checking}>
          {checking ? <Spinner data-icon="inline-start" /> : <RefreshCwIcon data-icon="inline-start" aria-hidden />}
          {checking ? t("checkout.expired.checking") : t("checkout.expired.checkAgain")}
        </Button>
        <MerchantLink href={back} label={t("checkout.expired.backToMerchant")} />
      </div>
    </div>
  );
}

export function CancelledScreen({ view }: { view: CheckoutView }) {
  const t = useT();
  const back = safeHttpUrl(view.invoice.cancel_url);
  return (
    <div className="space-y-6">
      <StatusHeader icon={CircleXIcon} tone="destructive" title={t("checkout.cancelled.title")} body={t("checkout.cancelled.body")} />
      <MerchantLink href={back} label={t("checkout.cancelled.backToMerchant")} />
    </div>
  );
}

// ---- not found / network error ----------------------------------------------

export function NotFoundScreen() {
  const t = useT();
  return <StatusHeader icon={SearchXIcon} tone="muted" title={t("checkout.notFound.title")} body={t("checkout.notFound.body")} />;
}

export function ErrorScreen({
  rateLimited,
  onRetry,
  retrying,
}: {
  rateLimited: boolean;
  onRetry: () => void;
  retrying: boolean;
}) {
  const t = useT();
  return (
    <div className="space-y-6">
      <StatusHeader
        icon={WifiOffIcon}
        tone="destructive"
        title={t("checkout.error.title")}
        body={rateLimited ? t("checkout.error.rateLimited") : t("checkout.error.body")}
      />
      <Button className="h-11 rounded-xl px-4" onClick={onRetry} disabled={retrying}>
        {retrying ? <Spinner data-icon="inline-start" /> : <RefreshCwIcon data-icon="inline-start" aria-hidden />}
        {t("common.retry")}
      </Button>
    </div>
  );
}
