"use client";

import { useState } from "react";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import type { Quote } from "@/lib/api/types";
import { paymentUriFor } from "@/lib/checkout/payment";
import { useT } from "@/lib/i18n";
import { QrCode } from "./qr-code";

type Mode = "request" | "address";

/**
 * QR code of the payment request (EIP-681 / BIP-21 / Solana Pay) or of the
 * plain address — some wallets only read plain addresses, so the customer can
 * switch. Tron has no payment URI: address only.
 */
export function QrPanel({ quote }: { quote: Quote }) {
  const t = useT();
  const uri = paymentUriFor(quote);
  const [mode, setMode] = useState<Mode>("request");
  const effective: Mode = uri ? mode : "address";
  const value = effective === "request" ? uri : quote.address;

  return (
    <div className="flex flex-col items-center gap-4">
      {uri && (
        <ToggleGroup
          type="single"
          variant="outline"
          size="sm"
          value={effective}
          onValueChange={(v) => {
            if (v === "request" || v === "address") setMode(v);
          }}
          aria-label={t("checkout.pay.qrModeLabel")}
        >
          <ToggleGroupItem value="request" className="px-3">
            {t("checkout.pay.qrModeRequest")}
          </ToggleGroupItem>
          <ToggleGroupItem value="address" className="px-3">
            {t("checkout.pay.qrModeAddress")}
          </ToggleGroupItem>
        </ToggleGroup>
      )}
      <div className="rounded-2xl border bg-white p-3 shadow-[0_12px_32px_-24px_rgb(0_0_0/0.5)]">
        <QrCode
          value={value}
          label={t("checkout.pay.qrLabel", { symbol: quote.symbol })}
          className="size-[212px] sm:size-[232px]"
        />
      </div>
      <p className="max-w-xs text-center text-xs leading-relaxed text-muted-foreground" aria-live="polite">
        {effective === "request" ? t("checkout.pay.qrHintRequest") : t("checkout.pay.qrHintAddress")}
      </p>
    </div>
  );
}
