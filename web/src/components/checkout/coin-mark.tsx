import { cn } from "@/lib/utils";

/**
 * Monogram for a coin (no third-party logos): the currency glyph on the
 * coin's recognisable colour.
 */
const COINS: Record<string, { glyph: string; bg: string; fg: string }> = {
  USDT: { glyph: "₮", bg: "#26a17b", fg: "#ffffff" },
  USDC: { glyph: "$", bg: "#2775ca", fg: "#ffffff" },
  BTC: { glyph: "₿", bg: "#f7931a", fg: "#ffffff" },
  ETH: { glyph: "Ξ", bg: "#627eea", fg: "#ffffff" },
  TRX: { glyph: "T", bg: "#eb0029", fg: "#ffffff" },
  SOL: { glyph: "S", bg: "#14151a", fg: "#14f195" },
  BNB: { glyph: "B", bg: "#f0b90b", fg: "#1e2026" },
  LTC: { glyph: "Ł", bg: "#345d9d", fg: "#ffffff" },
  POL: { glyph: "P", bg: "#8247e5", fg: "#ffffff" },
};

export function CoinMark({ symbol, className }: { symbol: string; className?: string }) {
  const c = COINS[symbol.toUpperCase()] ?? { glyph: symbol.slice(0, 1).toUpperCase(), bg: "var(--primary)", fg: "var(--primary-foreground)" };
  return (
    <span
      aria-hidden
      className={cn(
        "inline-flex size-9 shrink-0 select-none items-center justify-center rounded-full font-heading text-[17px] font-semibold leading-none shadow-[inset_0_0_0_1px_rgb(0_0_0/0.08)]",
        className,
      )}
      style={{ backgroundColor: c.bg, color: c.fg }}
    >
      {c.glyph}
    </span>
  );
}
