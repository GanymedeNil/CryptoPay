import { htmlLang, type Locale } from "@/lib/i18n/config";

const UNITS: [Intl.RelativeTimeFormatUnit, number][] = [
  ["year", 365 * 24 * 3600],
  ["month", 30 * 24 * 3600],
  ["week", 7 * 24 * 3600],
  ["day", 24 * 3600],
  ["hour", 3600],
  ["minute", 60],
  ["second", 1],
];

/**
 * "3 minutes ago" / "3 分钟前" / "in 2 hours". `now` is injected so callers
 * (and tests) control the clock. Within ±45 s it says "now" / "现在".
 */
export function formatRelativeTime(
  iso: string | null | undefined,
  now: number,
  locale: Locale,
): string {
  if (!iso) return "";
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return "";
  const diffSec = Math.round((t - now) / 1000);
  const rtf = new Intl.RelativeTimeFormat(htmlLang(locale), { numeric: "auto" });
  if (Math.abs(diffSec) < 45) return rtf.format(0, "second");
  for (const [unit, size] of UNITS) {
    if (Math.abs(diffSec) >= size || unit === "second") {
      return rtf.format(Math.round(diffSec / size), unit);
    }
  }
  return "";
}

/** "2026-11-16" for `<input type="date">` from an ISO timestamp (local date). */
export function toDateInputValue(iso: string | null | undefined): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

/**
 * "2026-11-16" (a local calendar day from a date input) → RFC 3339 instant at
 * the start (or end, for an inclusive "to") of that local day.
 */
export function dateInputToIso(value: string, edge: "start" | "end"): string | undefined {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value);
  if (!m) return undefined;
  const d =
    edge === "start"
      ? new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3]), 0, 0, 0, 0)
      : new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3]), 23, 59, 59, 999);
  return Number.isNaN(d.getTime()) ? undefined : d.toISOString();
}

/** "2026-11-16" → "Nov 16, 2026" / "2026年11月16日" (a calendar day, no time zone shift). */
export function formatDay(day: string, locale: Locale): string {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(day);
  if (!m) return day;
  const d = new Date(Date.UTC(Number(m[1]), Number(m[2]) - 1, Number(m[3])));
  return new Intl.DateTimeFormat(htmlLang(locale), { dateStyle: "medium", timeZone: "UTC" }).format(d);
}

/** "2026-11-16" → "Nov 16" / "11月16日" for chart axes. */
export function formatShortDate(day: string, locale: Locale): string {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(day);
  if (!m) return day;
  const d = new Date(Date.UTC(Number(m[1]), Number(m[2]) - 1, Number(m[3])));
  return new Intl.DateTimeFormat(htmlLang(locale), {
    month: "short",
    day: "numeric",
    timeZone: "UTC",
  }).format(d);
}
