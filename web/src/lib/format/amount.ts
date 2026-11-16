/**
 * Exact decimal helpers for money. Amounts travel as decimal strings
 * ("12.5") and base-unit integer strings ("12500000"); all arithmetic here is
 * BigInt based. Nothing in this module converts an amount to a JS `number`,
 * except `toSafeInteger` which exists only for third-party wallet APIs that
 * demand a number of base units and refuses anything not exactly
 * representable.
 */

const DECIMAL_RE = /^([+-])?(\d+)(?:\.(\d+))?$/;

export interface ParsedDecimal {
  negative: boolean;
  /** Integer digits without leading zeros ("0" for none). */
  int: string;
  /** Fraction digits without trailing zeros ("" for none). */
  frac: string;
}

export function isDecimalString(value: string): boolean {
  return DECIMAL_RE.test(value.trim());
}

export function parseDecimal(value: string): ParsedDecimal {
  const m = DECIMAL_RE.exec(value.trim());
  if (!m) throw new RangeError(`Invalid decimal amount: "${value}"`);
  const int = m[2].replace(/^0+(?=\d)/, "");
  const frac = (m[3] ?? "").replace(/0+$/, "");
  const isZero = /^0*$/.test(int) && frac === "";
  return { negative: m[1] === "-" && !isZero, int, frac };
}

/** Canonical form: no leading/trailing zeros, no "+", "-0" → "0". */
export function normalizeDecimal(value: string): string {
  const p = parseDecimal(value);
  return `${p.negative ? "-" : ""}${p.int}${p.frac ? `.${p.frac}` : ""}`;
}

/** value × 10^scale as BigInt; throws if value has more than `scale` decimals. */
export function toScaledBigInt(value: string, scale: number): bigint {
  const p = parseDecimal(value);
  if (p.frac.length > scale) {
    throw new RangeError(`"${value}" has more than ${scale} decimal places`);
  }
  const digits = p.int + p.frac.padEnd(scale, "0");
  const n = BigInt(digits);
  return p.negative ? -n : n;
}

/** Inverse of `toScaledBigInt`, normalised. */
export function fromScaledBigInt(n: bigint, scale: number): string {
  const negative = n < 0n;
  const abs = (negative ? -n : n).toString().padStart(scale + 1, "0");
  const int = scale === 0 ? abs : abs.slice(0, abs.length - scale);
  const frac = scale === 0 ? "" : abs.slice(abs.length - scale).replace(/0+$/, "");
  const out = `${int}${frac ? `.${frac}` : ""}`;
  return negative && out !== "0" ? `-${out}` : out;
}

/** "1500000", 6 → "1.5" */
export function baseToDecimal(base: string, decimals: number): string {
  if (!/^-?\d+$/.test(base.trim())) throw new RangeError(`Invalid base amount: "${base}"`);
  return fromScaledBigInt(BigInt(base.trim()), decimals);
}

/** "1.5", 6 → "1500000" (exact; throws when it would need rounding). */
export function decimalToBase(amount: string, decimals: number): string {
  return toScaledBigInt(amount, decimals).toString();
}

function scaleOf(...values: string[]): number {
  return Math.max(0, ...values.map((v) => parseDecimal(v).frac.length));
}

export function compareDecimal(a: string, b: string): -1 | 0 | 1 {
  const s = scaleOf(a, b);
  const x = toScaledBigInt(a, s);
  const y = toScaledBigInt(b, s);
  return x < y ? -1 : x > y ? 1 : 0;
}

export function addDecimal(a: string, b: string): string {
  const s = scaleOf(a, b);
  return fromScaledBigInt(toScaledBigInt(a, s) + toScaledBigInt(b, s), s);
}

export function subtractDecimal(a: string, b: string): string {
  const s = scaleOf(a, b);
  return fromScaledBigInt(toScaledBigInt(a, s) - toScaledBigInt(b, s), s);
}

export function isZeroDecimal(value: string): boolean {
  const p = parseDecimal(value);
  return p.int === "0" && p.frac === "";
}

export function isZeroBase(value: string): boolean {
  return /^[+-]?0*$/.test(value.trim());
}

export type RoundingMode = "halfExpand" | "ceil" | "floor" | "trunc";

/** Rounds a decimal string to `digits` fraction digits (exact). */
export function roundDecimal(value: string, digits: number, mode: RoundingMode = "halfExpand"): string {
  const p = parseDecimal(value);
  if (p.frac.length <= digits) return normalizeDecimal(value);
  const scaled = BigInt(p.int + p.frac.slice(0, digits).padEnd(digits, "0"));
  const rest = p.frac.slice(digits);
  const hasRest = /[1-9]/.test(rest);
  let up = false;
  switch (mode) {
    case "halfExpand":
      up = rest[0] >= "5";
      break;
    case "ceil":
      up = hasRest && !p.negative;
      break;
    case "floor":
      up = hasRest && p.negative;
      break;
    case "trunc":
      up = false;
  }
  const magnitude = up ? scaled + 1n : scaled;
  return fromScaledBigInt(p.negative ? -magnitude : magnitude, digits);
}

export interface FormatAmountOptions {
  /** Insert group separators in the integer part (default true). */
  group?: boolean;
  /** Pad the fraction with zeros up to this many digits. */
  minFractionDigits?: number;
  /** Round (half-expand) to at most this many digits. Omit to keep all. */
  maxFractionDigits?: number;
  /**
   * Keep the fraction digits of the input (e.g. "99.00" stays "99.00"): for
   * amounts the backend already rounded to the asset's quote precision.
   */
  keepScale?: boolean;
  /** Separator characters; defaults suit both zh and en ("," and "."). */
  groupSeparator?: string;
  decimalSeparator?: string;
}

/**
 * Formats a decimal string for display without ever going through a float:
 * `formatAmount("1234567.5", { minFractionDigits: 2 })` → "1,234,567.50".
 */
export function formatAmount(value: string, opts: FormatAmountOptions = {}): string {
  const {
    group = true,
    minFractionDigits = 0,
    maxFractionDigits,
    groupSeparator = ",",
    decimalSeparator = ".",
    keepScale = false,
  } = opts;
  const inputScale = keepScale ? (value.trim().split(".")[1] ?? "").length : 0;
  const rounded = maxFractionDigits === undefined ? value : roundDecimal(value, maxFractionDigits);
  const p = parseDecimal(rounded);
  const int = group ? p.int.replace(/\B(?=(\d{3})+(?!\d))/g, groupSeparator) : p.int;
  const minDigits = Math.max(
    minFractionDigits,
    maxFractionDigits === undefined ? inputScale : Math.min(inputScale, maxFractionDigits),
  );
  const frac = p.frac.padEnd(minDigits, "0");
  return `${p.negative ? "-" : ""}${int}${frac ? `${decimalSeparator}${frac}` : ""}`;
}

/** The amount as the user should type / paste it: canonical, no grouping. */
export function plainAmount(value: string): string {
  return normalizeDecimal(value);
}

interface CurrencyAffix {
  prefix: string;
  suffix: string;
  fractionDigits: number;
}

const affixCache = new Map<string, CurrencyAffix>();

/**
 * Currency symbol placement and minor-unit digits, read from Intl by
 * formatting the constant 1 (never the amount itself). Falls back to
 * "<amount> CODE" for crypto symbols / non ISO codes.
 */
export function currencyAffix(currency: string, locale: string): CurrencyAffix {
  const key = `${locale}|${currency}`;
  const cached = affixCache.get(key);
  if (cached) return cached;
  let affix: CurrencyAffix = { prefix: "", suffix: ` ${currency}`, fractionDigits: -1 };
  if (/^[A-Z]{3}$/.test(currency) && !CRYPTO_CODES.has(currency)) {
    try {
      const nf = new Intl.NumberFormat(locale, {
        style: "currency",
        currency,
        currencyDisplay: "narrowSymbol",
      });
      const parts = nf.formatToParts(1);
      const firstNum = parts.findIndex((x) => x.type === "integer");
      const lastNum = parts.findLastIndex((x) => x.type === "integer" || x.type === "fraction");
      const prefix = parts.slice(0, firstNum).map((x) => x.value).join("");
      const suffix = parts.slice(lastNum + 1).map((x) => x.value).join("");
      affix = {
        prefix,
        suffix,
        fractionDigits: nf.resolvedOptions().maximumFractionDigits ?? 2,
      };
    } catch {
      // keep the fallback
    }
  }
  affixCache.set(key, affix);
  return affix;
}

const CRYPTO_CODES = new Set(["BTC", "ETH", "LTC", "TRX", "SOL", "BNB", "POL", "XRP", "DOT", "XMR"]);

/**
 * Formats a price: fiat → "$99.00" / "¥99.00" / "€99.00"; crypto symbols →
 * "12.5 USDT". Fiat amounts are padded to the currency's minor units.
 */
export function formatMoney(amount: string, currency: string, locale = "en"): string {
  const affix = currencyAffix(currency.toUpperCase(), locale);
  const body = formatAmount(amount, {
    minFractionDigits: affix.fractionDigits > 0 ? affix.fractionDigits : 0,
  });
  if (body.startsWith("-")) return `-${affix.prefix}${body.slice(1)}${affix.suffix}`;
  return `${affix.prefix}${body}${affix.suffix}`;
}

/**
 * Converts an integer base-unit string to a JS number for wallet APIs that
 * only accept numbers (TronWeb `sendTransaction`, UniSat/OKX `sendBitcoin`).
 * Throws instead of silently losing precision.
 */
export function toSafeInteger(base: string): number {
  const v = BigInt(base.trim());
  if (v < 0n || v > BigInt(Number.MAX_SAFE_INTEGER)) {
    throw new RangeError(`Amount ${base} cannot be represented exactly`);
  }
  return Number(v);
}

/** Splits a formatted amount into integer and fraction parts for styling. */
export function splitAmount(formatted: string, decimalSeparator = "."): { int: string; frac: string } {
  const i = formatted.indexOf(decimalSeparator);
  return i < 0 ? { int: formatted, frac: "" } : { int: formatted.slice(0, i), frac: formatted.slice(i + 1) };
}
