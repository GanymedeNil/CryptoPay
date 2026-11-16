/**
 * Unit conversions used by the admin console. Settings travel in the API's
 * units (seconds, basis points, milliseconds); operators think in minutes,
 * hours and percent. Every conversion is exact (BigInt / integer maths) and
 * round-trips: `percentToBps(bpsToPercent(x)) === x`.
 */

import { fromScaledBigInt, isDecimalString, parseDecimal } from "./amount";

// ---------------------------------------------------------------------------
// Basis points ↔ percent
// ---------------------------------------------------------------------------

/** 50 → "0.5", 1 → "0.01", 1000 → "10", -25 → "-0.25". */
export function bpsToPercent(bps: number): string {
  if (!Number.isInteger(bps)) throw new RangeError(`bps must be an integer: ${bps}`);
  return fromScaledBigInt(BigInt(bps), 2);
}

/**
 * "0.5" → 50, "10" → 1000, "0.01" → 1. Returns `null` for anything that is
 * not a decimal with at most two fraction digits (1 bp = 0.01 %).
 */
export function percentToBps(percent: string): number | null {
  const v = percent.trim();
  if (!v || !isDecimalString(v)) return null;
  const p = parseDecimal(v);
  if (p.frac.length > 2) return null;
  const n = Number(p.int) * 100 + Number(p.frac.padEnd(2, "0") || "0");
  if (!Number.isSafeInteger(n)) return null;
  return p.negative ? -n : n;
}

// ---------------------------------------------------------------------------
// Durations
// ---------------------------------------------------------------------------

export type DurationUnit = "seconds" | "minutes" | "hours";

export const UNIT_SECONDS: Record<DurationUnit, number> = {
  seconds: 1,
  minutes: 60,
  hours: 3600,
};

/**
 * Picks the largest unit that represents `seconds` exactly:
 * 900 → { value: 15, unit: "minutes" }, 7200 → { 2, "hours" }, 45 → { 45, "seconds" }.
 * `allowed` restricts the candidates (largest first is tried first).
 */
export function splitDuration(
  seconds: number,
  allowed: readonly DurationUnit[] = ["hours", "minutes", "seconds"],
): { value: number; unit: DurationUnit } {
  const order: DurationUnit[] = (["hours", "minutes", "seconds"] as const).filter((u) =>
    allowed.includes(u),
  );
  for (const unit of order) {
    const size = UNIT_SECONDS[unit];
    if (seconds !== 0 && seconds % size === 0) return { value: seconds / size, unit };
  }
  const smallest = order[order.length - 1] ?? "seconds";
  return { value: seconds / UNIT_SECONDS[smallest], unit: smallest };
}

/** { 15, "minutes" } → 900. Returns `null` unless the result is a whole number of seconds. */
export function joinDuration(value: string | number, unit: DurationUnit): number | null {
  const raw = typeof value === "number" ? String(value) : value.trim();
  if (!raw || !isDecimalString(raw)) return null;
  const p = parseDecimal(raw);
  if (p.negative) return null;
  // value × unit, exact: scale the fraction by the unit size
  const scale = p.frac.length;
  const scaled = BigInt(p.int + p.frac) * BigInt(UNIT_SECONDS[unit]);
  const divisor = 10n ** BigInt(scale);
  if (scaled % divisor !== 0n) return null;
  const n = Number(scaled / divisor);
  return Number.isSafeInteger(n) ? n : null;
}

/** Compact human duration: 90 → "1m 30s" / "1 分 30 秒", 7200 → "2h". */
export function formatDuration(seconds: number, locale: "zh" | "en"): string {
  if (!Number.isFinite(seconds)) return "";
  const neg = seconds < 0;
  let s = Math.round(Math.abs(seconds));
  const d = Math.floor(s / 86400);
  s -= d * 86400;
  const h = Math.floor(s / 3600);
  s -= h * 3600;
  const m = Math.floor(s / 60);
  s -= m * 60;
  const zh = locale === "zh";
  const parts: string[] = [];
  if (d) parts.push(zh ? `${d} 天` : `${d}d`);
  if (h) parts.push(zh ? `${h} 小时` : `${h}h`);
  if (m) parts.push(zh ? `${m} 分` : `${m}m`);
  if (s || parts.length === 0) parts.push(zh ? `${s} 秒` : `${s}s`);
  return (neg ? "-" : "") + parts.join(" ");
}

/** 1500 → "1.5 s", 400 → "400 ms", 120000 → "2 min". */
export function formatMillis(ms: number, locale: "zh" | "en"): string {
  if (!Number.isFinite(ms)) return "";
  if (ms < 1000) return `${ms} ms`;
  if (ms < 60_000) return `${trimFloat(ms / 1000)} ${locale === "zh" ? "秒" : "s"}`;
  return `${trimFloat(ms / 60_000)} ${locale === "zh" ? "分钟" : "min"}`;
}

function trimFloat(n: number): string {
  return String(Math.round(n * 100) / 100);
}

// ---------------------------------------------------------------------------
// Ratios and counts (plain numbers — never money)
// ---------------------------------------------------------------------------

/** 0.4567 → "45.7%" (ratios are numbers in the API, e.g. `conversion_rate`). */
export function formatPercent(ratio: number, locale: "zh" | "en", maximumFractionDigits = 1): string {
  if (!Number.isFinite(ratio)) return "–";
  return new Intl.NumberFormat(locale === "zh" ? "zh-CN" : "en", {
    style: "percent",
    maximumFractionDigits,
  }).format(ratio);
}

/** 12345 → "12,345". */
export function formatCount(n: number, locale: "zh" | "en"): string {
  if (!Number.isFinite(n)) return "–";
  return new Intl.NumberFormat(locale === "zh" ? "zh-CN" : "en").format(n);
}

/**
 * Decimal string → number, **for plotting only** (chart axes need numbers).
 * Never use the result for display or arithmetic on money; display with the
 * string formatters instead. Invalid input plots as 0.
 */
export function plotValue(decimal: string | null | undefined): number {
  if (!decimal || !isDecimalString(decimal)) return 0;
  const n = Number(decimal);
  return Number.isFinite(n) ? n : 0;
}
