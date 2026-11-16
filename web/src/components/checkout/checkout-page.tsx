"use client";

import { ArrowLeftRightIcon, QrCodeIcon, RadarIcon, WalletIcon, WifiOffIcon } from "lucide-react";
import { useEffect, useState } from "react";
import { BrandMark } from "@/components/brand/brand-mark";
import { SiteHeader } from "@/components/site-header";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useIsMobile } from "@/hooks/use-is-mobile";
import { useNow } from "@/hooks/use-now";
import type { CheckoutApi } from "@/lib/api/checkout";
import { ApiError } from "@/lib/api/client";
import type { CheckoutAsset, CheckoutView, Quote } from "@/lib/api/types";
import { amountToPay, isOpenStatus } from "@/lib/checkout/payment";
import { deriveScreen, type CheckoutScreen } from "@/lib/checkout/view-state";
import { formatAmount } from "@/lib/format/amount";
import { remainingMs } from "@/lib/format/countdown";
import { useT } from "@/lib/i18n";
import type { MessageKey, TranslateFn } from "@/lib/i18n/types";
import { AssetPicker } from "./asset-picker";
import { InvoiceStub } from "./invoice-stub";
import { PaymentDetails } from "./payment-details";
import { QrPanel } from "./qr-panel";
import { PaidStamp, Slip } from "./slip";
import {
  CancelledScreen,
  ErrorScreen,
  ExpiredScreen,
  NotFoundScreen,
  PaidScreen,
  UnderpaidScreen,
} from "./status-screens";
import { TransfersList } from "./transfers-list";
import { useCheckout } from "./use-checkout";
import { WalletPanel } from "./wallet-panel";

const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

const SCREEN_TITLE: Record<CheckoutScreen, MessageKey> = {
  loading: "common.loading",
  not_found: "checkout.notFound.title",
  error: "checkout.error.title",
  select_asset: "checkout.select.title",
  pay: "checkout.pay.waiting",
  partial: "checkout.partial.title",
  confirming: "checkout.confirming.title",
  paid: "checkout.paid.title",
  expired: "checkout.expired.title",
  underpaid: "checkout.underpaid.title",
  cancelled: "checkout.cancelled.title",
};

function quoteErrorMessage(t: TranslateFn, e: unknown): string {
  if (e instanceof ApiError) {
    if (e.isConflict) return t("checkout.select.conflict");
    if (e.status === 422) return t("checkout.select.unavailable");
    if (e.isRateLimited) return t("checkout.error.rateLimited");
    if (e.isTransient) return t("checkout.error.body");
    return t("checkout.select.failed", { message: e.message });
  }
  return t("checkout.select.failed", { message: e instanceof Error ? e.message : String(e) });
}

/** Share of the amount due already received, 0..100 (BigInt math). */
function receivedPercent(quote: Quote): number {
  const due = BigInt(quote.amount_base || "0");
  if (due <= 0n) return 0;
  const got = BigInt(quote.amount_received_base || "0");
  const pct = (got * 100n) / due;
  return Number(pct > 100n ? 100n : pct);
}

export function CheckoutPage({ invoiceId, api }: { invoiceId: string; api?: CheckoutApi }) {
  const t = useT();
  const valid = UUID_RE.test(invoiceId);
  const { data, error, clockOffsetMs, refreshing, refresh, selectAsset } = useCheckout(valid ? invoiceId : null, {
    api,
  });
  const [changingAsset, setChangingAsset] = useState(false);
  const [pendingAssetId, setPendingAssetId] = useState<string | null>(null);
  const [quoteError, setQuoteError] = useState<string | null>(null);

  const screen: CheckoutScreen = valid ? deriveScreen({ data, error, changingAsset }) : "not_found";
  const open = data ? isOpenStatus(data.invoice.status) : false;
  const now = useNow(1000, open);
  const left = data ? remainingMs(data.invoice.expires_at, now, clockOffsetMs) : Number.POSITIVE_INFINITY;
  const timeUp = open && left === 0;

  const merchant = data?.invoice.merchant_name ?? "";
  useEffect(() => {
    if (merchant) document.title = `${t("checkout.documentTitle", { merchant })} · CryptoPay`;
  }, [merchant, t]);

  async function handleSelect(asset: CheckoutAsset) {
    setQuoteError(null);
    setPendingAssetId(asset.id);
    try {
      await selectAsset(asset.id);
      setChangingAsset(false);
    } catch (e) {
      setQuoteError(quoteErrorMessage(t, e));
      if (e instanceof ApiError && e.isConflict) void refresh();
    } finally {
      setPendingAssetId(null);
    }
  }

  const timeWarning =
    open && left > 0 && left <= 60_000
      ? t("checkout.pay.lessThan1")
      : open && left > 0 && left <= 5 * 60_000
        ? t("checkout.pay.lessThan5")
        : "";

  return (
    <CheckoutFrame>
      <h1 className="sr-only">
        {merchant ? t("checkout.documentTitle", { merchant }) : t("checkout.paymentFallbackTitle")}
      </h1>

      {data && error?.isTransient && (
        <p
          role="status"
          className="mb-3 flex items-center justify-center gap-2 rounded-lg bg-warning-surface px-3 py-2 text-xs font-medium text-warning"
        >
          <WifiOffIcon className="size-3.5" aria-hidden />
          {t("checkout.error.reconnecting")}
        </p>
      )}

      {screen === "loading" ? (
        <LoadingSlip />
      ) : (
        <Slip
          stub={
            data ? (
              <InvoiceStub
                invoice={data.invoice}
                now={now}
                clockOffsetMs={clockOffsetMs}
                // Once the full amount is seen the deadline no longer matters to the customer.
                showCountdown={open && data.invoice.status !== "confirming"}
              />
            ) : undefined
          }
          stamp={screen === "paid" ? <PaidStamp label={t("checkout.paid.stamp")} /> : undefined}
        >
          <ScreenBody
            screen={screen}
            data={data}
            error={error}
            timeUp={timeUp}
            refreshing={refreshing}
            onRefresh={() => void refresh()}
            onSelect={(a) => void handleSelect(a)}
            pendingAssetId={pendingAssetId}
            quoteError={quoteError}
            onChangeCoin={() => {
              setQuoteError(null);
              setChangingAsset(true);
            }}
            onCancelChange={
              changingAsset
                ? () => {
                    setQuoteError(null);
                    setChangingAsset(false);
                  }
                : undefined
            }
          />
        </Slip>
      )}

      {/* Screen-reader announcements for status changes and the deadline. */}
      <div role="status" aria-live="polite" aria-atomic="true" className="sr-only">
        {t(SCREEN_TITLE[screen])}
      </div>
      <div aria-live="assertive" aria-atomic="true" className="sr-only">
        {timeWarning}
      </div>
    </CheckoutFrame>
  );
}

/** Page chrome shared by the checkout and its loading fallback. */
function CheckoutFrame({ children }: { children: React.ReactNode }) {
  const t = useT();
  return (
    <div className="flex min-h-dvh flex-col">
      <SiteHeader className="max-w-[480px]">
        <BrandMark />
      </SiteHeader>
      <main className="mx-auto w-full max-w-[480px] flex-1 px-3 pb-[max(2.5rem,env(safe-area-inset-bottom))] sm:px-4 sm:pt-4">
        {children}
        <p className="mt-6 px-4 text-center text-xs leading-relaxed text-muted-foreground">
          {t("checkout.nonCustodial")}
        </p>
      </main>
    </div>
  );
}

/** Route-level fallback while the page segment streams in. */
export function CheckoutShell() {
  return (
    <CheckoutFrame>
      <LoadingSlip />
    </CheckoutFrame>
  );
}

function ScreenBody({
  screen,
  data,
  error,
  timeUp,
  refreshing,
  onRefresh,
  onSelect,
  pendingAssetId,
  quoteError,
  onChangeCoin,
  onCancelChange,
}: {
  screen: CheckoutScreen;
  data: CheckoutView | null;
  error: ApiError | null;
  timeUp: boolean;
  refreshing: boolean;
  onRefresh: () => void;
  onSelect: (asset: CheckoutAsset) => void;
  pendingAssetId: string | null;
  quoteError: string | null;
  onChangeCoin: () => void;
  onCancelChange?: () => void;
}) {
  switch (screen) {
    case "not_found":
      return <NotFoundScreen />;
    case "error":
      return <ErrorScreen rateLimited={Boolean(error?.isRateLimited)} onRetry={onRefresh} retrying={refreshing} />;
    case "loading":
      return null;
  }
  if (!data) return null;
  switch (screen) {
    case "select_asset":
      return (
        <AssetPicker
          assets={data.assets}
          onSelect={onSelect}
          pendingAssetId={pendingAssetId}
          error={quoteError}
          onBack={data.quote?.status === "active" ? onCancelChange : undefined}
        />
      );
    case "pay":
    case "partial":
      return data.quote ? (
        <PayScreen
          view={data}
          quote={data.quote}
          partial={screen === "partial"}
          timeUp={timeUp}
          onChangeCoin={screen === "pay" ? onChangeCoin : undefined}
        />
      ) : null;
    case "confirming":
      return <ConfirmingScreen view={data} />;
    case "paid":
      return <PaidScreen view={data} />;
    case "expired":
      return <ExpiredScreen view={data} onCheckAgain={onRefresh} checking={refreshing} />;
    case "underpaid":
      return <UnderpaidScreen view={data} onCheckAgain={onRefresh} checking={refreshing} />;
    case "cancelled":
      return <CancelledScreen view={data} />;
  }
}

function PayScreen({
  view,
  quote,
  partial,
  timeUp,
  onChangeCoin,
}: {
  view: CheckoutView;
  quote: Quote;
  partial: boolean;
  timeUp: boolean;
  onChangeCoin?: () => void;
}) {
  const t = useT();
  const isMobile = useIsMobile();
  const [tab, setTab] = useState<string | null>(null);
  const activeTab = tab ?? (isMobile ? "wallet" : "qr");
  // Once a wallet sent the payment, switching coin would only cause confusion.
  const [sent, setSent] = useState(false);

  return (
    <div className="space-y-6">
      {partial && (
        <div className="space-y-2.5 rounded-xl border border-primary/20 bg-accent/70 p-4">
          <p className="font-semibold">{t("checkout.partial.title")}</p>
          <p className="text-sm leading-relaxed text-foreground/85">
            {t("checkout.partial.body", {
              received: formatAmount(quote.amount_received),
              remaining: formatAmount(amountToPay(quote)),
              symbol: quote.symbol,
            })}
          </p>
          <div className="h-1.5 overflow-hidden rounded-full bg-foreground/10" aria-hidden>
            <div className="h-full rounded-full bg-primary" style={{ width: `${receivedPercent(quote)}%` }} />
          </div>
        </div>
      )}

      {timeUp && (
        <Alert className="border-warning-border bg-warning-surface text-foreground">
          <AlertDescription className="text-foreground/90">{t("checkout.pay.expiredWaiting")}</AlertDescription>
        </Alert>
      )}

      <PaymentDetails invoice={view.invoice} quote={quote} />

      <Tabs value={activeTab} onValueChange={setTab} className="gap-4">
        <TabsList className="grid h-10! w-full grid-cols-2" aria-label={t("checkout.pay.methodLabel")}>
          <TabsTrigger value="wallet" className="gap-1.5">
            <WalletIcon aria-hidden /> {t("checkout.pay.tabWallet")}
          </TabsTrigger>
          <TabsTrigger value="qr" className="gap-1.5">
            <QrCodeIcon aria-hidden /> {t("checkout.pay.tabQr")}
          </TabsTrigger>
        </TabsList>
        <TabsContent value="wallet" forceMount className="data-[state=inactive]:hidden">
          <WalletPanel
            key={quote.id}
            quote={quote}
            walletConnectProjectId={view.walletconnect_project_id}
            disabled={timeUp}
            onSubmitted={() => setSent(true)}
          />
        </TabsContent>
        <TabsContent value="qr" forceMount className="data-[state=inactive]:hidden">
          <QrPanel key={quote.id} quote={quote} />
        </TabsContent>
      </Tabs>

      <TransfersList transfers={view.transfers} />

      {onChangeCoin && !sent && (
        <div className="flex justify-center border-t pt-4">
          <Button variant="ghost" className="text-muted-foreground" onClick={onChangeCoin}>
            <ArrowLeftRightIcon data-icon="inline-start" aria-hidden />
            {t("checkout.pay.changeCoin")}
          </Button>
        </div>
      )}
    </div>
  );
}

function ConfirmingScreen({ view }: { view: CheckoutView }) {
  const t = useT();
  return (
    <div className="space-y-6">
      <div className="space-y-3">
        <span className="relative inline-flex size-11 items-center justify-center rounded-full bg-success-surface text-success">
          <span className="absolute inset-0 animate-ping rounded-full bg-success/20 motion-reduce:hidden" aria-hidden />
          <RadarIcon className="relative size-5" aria-hidden />
        </span>
        <h2 className="font-heading text-xl font-semibold tracking-tight">{t("checkout.confirming.title")}</h2>
        <p className="text-sm leading-relaxed text-pretty text-muted-foreground">{t("checkout.confirming.body")}</p>
      </div>
      <TransfersList transfers={view.transfers} />
    </div>
  );
}

function LoadingSlip() {
  const t = useT();
  return (
    <div className="rounded-2xl border bg-card p-5 sm:p-7" aria-busy="true">
      <span className="sr-only">{t("common.loading")}</span>
      <div className="space-y-3">
        <Skeleton className="h-3 w-20" />
        <Skeleton className="h-5 w-40" />
        <Skeleton className="mt-4 h-9 w-36" />
      </div>
      <div className="my-6 border-t border-dashed" />
      <div className="space-y-3">
        <Skeleton className="h-5 w-44" />
        <Skeleton className="h-14 w-full" />
        <Skeleton className="h-14 w-full" />
        <Skeleton className="h-14 w-full" />
      </div>
    </div>
  );
}
