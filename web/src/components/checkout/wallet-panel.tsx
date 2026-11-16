"use client";

import {
  ArrowUpRightIcon,
  CircleCheckIcon,
  ExternalLinkIcon,
  QrCodeIcon,
  SmartphoneIcon,
  WalletIcon,
} from "lucide-react";
import { useEffect, useMemo, useState, useSyncExternalStore } from "react";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Spinner } from "@/components/ui/spinner";
import { useIsMobile } from "@/hooks/use-is-mobile";
import type { Quote } from "@/lib/api/types";
import { amountToPayBase, paymentUriFor } from "@/lib/checkout/payment";
import { explorerTxUrl } from "@/lib/explorer";
import { shortenHash } from "@/lib/format/address";
import { useT } from "@/lib/i18n";
import type { TranslateFn } from "@/lib/i18n/types";
import {
  deepLinksFor,
  detectWallets,
  loadWalletAdapter,
  normalizeWalletError,
  subscribeEip6963,
  type PayStep,
  type WalletError,
  type WalletOption,
} from "@/lib/wallets";
import { cn } from "@/lib/utils";
import { CopyButton } from "./copy-button";

type PayState =
  | { status: "idle" }
  | { status: "working"; optionId: string; step: PayStep }
  | { status: "error"; optionId: string; error: WalletError }
  | { status: "submitted"; optionId: string; txHash: string };

// ---- injected wallet detection (re-checked as late wallets inject) --------

function subscribeWallets(onChange: () => void): () => void {
  const unsubscribe = subscribeEip6963(onChange);
  const timers = [250, 800, 2000].map((ms) => setTimeout(onChange, ms));
  const events = ["load", "tronLink#initialized", "focus"];
  events.forEach((e) => window.addEventListener(e, onChange));
  return () => {
    unsubscribe();
    timers.forEach(clearTimeout);
    events.forEach((e) => window.removeEventListener(e, onChange));
  };
}

function useInjectedWallets(quote: Quote, projectId: string): WalletOption[] {
  const key = useSyncExternalStore(
    subscribeWallets,
    () => detectWallets(quote, projectId).map((o) => `${o.id}|${o.name}`).join(","),
    () => "",
  );
  return useMemo(() => (key ? detectWallets(quote, projectId) : []), [key, quote, projectId]);
}

const noopSubscribe = () => () => {};
function useCurrentUrl(): string {
  return useSyncExternalStore(noopSubscribe, () => window.location.href, () => "");
}

// ---- messages ---------------------------------------------------------------

function errorMessage(t: TranslateFn, e: WalletError, quote: Quote): string {
  switch (e.code) {
    case "rejected":
      return t("wallet.errors.rejected");
    case "wrong_network":
      return t("wallet.errors.wrongNetwork", { network: quote.chain_name });
    case "insufficient_funds":
      return t("wallet.errors.insufficientFunds");
    case "not_installed":
      return t("wallet.errors.notInstalled");
    case "unsupported":
      return t("wallet.errors.unsupported");
    case "pending":
      return t("wallet.errors.pending");
    default:
      return t("wallet.errors.unknown", { message: e.message });
  }
}

function stepMessage(t: TranslateFn, step: PayStep, wallet: string, quote: Quote): string {
  switch (step) {
    case "connecting":
      return t("wallet.steps.connecting", { wallet });
    case "switching":
      return t("wallet.steps.switching", { network: quote.chain_name });
    case "preparing":
      return t("wallet.steps.preparing");
    case "confirm":
      return t("wallet.steps.confirm");
  }
}

function testnetHint(t: TranslateFn, quote: Quote): string | null {
  if (!quote.is_testnet) return null;
  if (quote.family === "solana") return t("wallet.hints.solanaDevnet");
  if (quote.family === "tron") return t("wallet.hints.tronNile");
  if (quote.family === "utxo") return t("wallet.hints.bitcoinTestnet");
  return null;
}

// ---- component --------------------------------------------------------------

export function WalletPanel({
  quote,
  walletConnectProjectId,
  disabled,
  onSubmitted,
}: {
  quote: Quote;
  walletConnectProjectId: string;
  /** The deadline passed locally: no new wallet payments. */
  disabled: boolean;
  /** Called once the wallet returned a transaction hash. */
  onSubmitted?: (txHash: string) => void;
}) {
  const t = useT();
  const isMobile = useIsMobile();
  const pageUrl = useCurrentUrl();
  const options = useInjectedWallets(quote, walletConnectProjectId);
  const [state, setState] = useState<PayState>({ status: "idle" });

  const injectedCount = options.filter((o) => o.kind === "injected").length;
  const uri = paymentUriFor(quote);
  const isBitcoinLike = quote.family === "utxo";
  const hasBrowserWallets = !(isBitcoinLike && !quote.chain_id.startsWith("bitcoin"));
  const deepLinks = isMobile && injectedCount === 0 && pageUrl ? deepLinksFor(quote.family, pageUrl) : [];
  const showUriButton = Boolean(uri) && (isMobile || isBitcoinLike);
  const hint = testnetHint(t, quote);

  // Warm the adapter chunk while the customer reads the page.
  useEffect(() => {
    if (options.length > 0) void loadWalletAdapter(quote.family).catch(() => undefined);
  }, [options.length, quote.family]);

  const busy = state.status === "working";

  async function pay(option: WalletOption) {
    setState({ status: "working", optionId: option.id, step: "connecting" });
    try {
      const adapter = await loadWalletAdapter(quote.family);
      const { txHash } = await adapter.pay({
        option,
        quote,
        amountBase: amountToPayBase(quote),
        walletConnectProjectId,
        onStep: (step) => setState({ status: "working", optionId: option.id, step }),
      });
      setState({ status: "submitted", optionId: option.id, txHash });
      onSubmitted?.(txHash);
    } catch (e) {
      setState({ status: "error", optionId: option.id, error: normalizeWalletError(e) });
    }
  }

  const nameOf = (o: WalletOption) => o.name || t("wallet.browserWallet");

  if (state.status === "submitted") {
    const url = explorerTxUrl(quote.chain_id, state.txHash);
    return (
      <div role="status" className="space-y-3 rounded-xl border border-success/30 bg-success-surface p-4">
        <p className="flex items-center gap-2 font-semibold text-success">
          <CircleCheckIcon className="size-5" aria-hidden />
          {t("wallet.submittedTitle")}
        </p>
        <p className="text-sm text-foreground/85">{t("wallet.submittedBody")}</p>
        <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg bg-card/70 px-3 py-2">
          <span className="font-mono text-xs" translate="no" title={state.txHash}>
            <span className="sr-only">{t("wallet.txHash")}: </span>
            {shortenHash(state.txHash, 10, 8)}
          </span>
          <span className="flex items-center gap-1">
            <CopyButton
              value={state.txHash}
              label={t("wallet.txHash")}
              copyLabel={t("common.copy")}
              size="icon-sm"
              variant="ghost"
            />
            {url && (
              <Button asChild variant="ghost" size="sm">
                <a href={url} target="_blank" rel="noopener noreferrer">
                  {t("checkout.transfers.viewOnExplorer")}
                  <ArrowUpRightIcon data-icon="inline-end" aria-hidden />
                  <span className="sr-only">{t("common.externalLink")}</span>
                </a>
              </Button>
            )}
          </span>
        </div>
      </div>
    );
  }

  return (
    <div className="space-y-4">
      {hint && options.length > 0 && <p className="text-xs leading-relaxed text-warning">{hint}</p>}

      {disabled && (
        <p className="rounded-lg bg-muted px-3 py-2 text-sm text-muted-foreground">{t("wallet.expiredDisabled")}</p>
      )}

      {options.length > 0 && (
        <div className="space-y-2">
          <p className="text-xs font-medium text-muted-foreground">{t("wallet.detected")}</p>
          <ul className="space-y-2">
            {options.map((o) => {
              const active = (state.status === "working" || state.status === "error") && state.optionId === o.id;
              return (
                <li key={`${o.kind}:${o.id}`}>
                  <Button
                    type="button"
                    variant={o.kind === "walletconnect" ? "outline" : "default"}
                    disabled={disabled || busy}
                    onClick={() => void pay(o)}
                    className={cn("h-12 w-full justify-start gap-3 rounded-xl px-3.5 text-[15px]")}
                  >
                    <WalletGlyph option={o} />
                    <span className="flex-1 truncate text-left">
                      {o.kind === "walletconnect" ? t("wallet.walletConnect") : t("wallet.payWith", { wallet: nameOf(o) })}
                    </span>
                    {active && state.status === "working" && <Spinner className="size-4" />}
                  </Button>
                  {o.kind === "walletconnect" && (
                    <p className="mt-1 px-1 text-xs text-muted-foreground">{t("wallet.walletConnectHint")}</p>
                  )}
                </li>
              );
            })}
          </ul>
        </div>
      )}

      <div aria-live="polite" className="empty:hidden">
        {state.status === "working" && (
          <p className="flex items-center gap-2 text-sm text-muted-foreground">
            <Spinner className="size-4" />
            {stepMessage(
              t,
              state.step,
              nameOf(options.find((o) => o.id === state.optionId) ?? { id: "", family: quote.family, name: "", kind: "injected" }),
              quote,
            )}
          </p>
        )}
        {state.status === "error" && (
          <Alert variant="destructive">
            <AlertDescription>{errorMessage(t, state.error, quote)}</AlertDescription>
          </Alert>
        )}
      </div>

      {options.length === 0 && (
        <p className="text-sm leading-relaxed text-muted-foreground">
          {!hasBrowserWallets
            ? t("wallet.noBrowserWalletForChain", { chain: quote.chain_name })
            : !isMobile
              ? t("wallet.noWallet")
              : deepLinks.length > 0
                ? t("wallet.noWalletMobile")
                : t("wallet.noWalletMobileUri")}
        </p>
      )}

      {showUriButton && !disabled && (
        <div className="space-y-1">
          <Button asChild variant={options.length === 0 ? "default" : "outline"} className="h-12 w-full rounded-xl text-[15px]">
            <a href={uri}>
              <SmartphoneIcon data-icon="inline-start" aria-hidden />
              {t("wallet.openWalletApp")}
            </a>
          </Button>
          <p className="px-1 text-xs text-muted-foreground">{t("wallet.openWalletAppHint")}</p>
        </div>
      )}

      {deepLinks.length > 0 && (
        <div className="space-y-2.5 pt-1">
          <div>
            <p className="text-sm font-semibold">{t("wallet.openInApp")}</p>
            <p className="text-xs text-muted-foreground">{t("wallet.openInAppHint")}</p>
          </div>
          <ul className="grid grid-cols-2 gap-2">
            {deepLinks.map((l) => (
              <li key={l.id}>
                <Button asChild variant="outline" className="h-11 w-full justify-between gap-1 rounded-xl px-3 text-[13px]">
                  <a href={l.href} rel="noopener noreferrer">
                    <span className="truncate">{l.name}</span>
                    <ExternalLinkIcon className="size-3.5! text-muted-foreground" aria-hidden />
                  </a>
                </Button>
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}

function WalletGlyph({ option }: { option: WalletOption }) {
  if (option.kind === "walletconnect") return <QrCodeIcon className="size-5" aria-hidden />;
  if (option.icon && /^(data:image\/|https:\/\/)/.test(option.icon)) {
    return (
      // eslint-disable-next-line @next/next/no-img-element -- EIP-6963 data: URI icons
      <img src={option.icon} alt="" className="size-6 shrink-0 rounded-md bg-white/90 p-0.5" />
    );
  }
  return <WalletIcon className="size-5" aria-hidden />;
}
