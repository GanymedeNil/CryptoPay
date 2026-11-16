import { htmlLang, type Locale } from "@/lib/i18n/config";

/** "2026-11-16T08:30:00Z" → "2026/11/16 16:30" (zh) / "Nov 16, 2026, 4:30 PM" (en). */
export function formatDateTime(iso: string | null | undefined, locale: Locale): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return new Intl.DateTimeFormat(htmlLang(locale), {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(d);
}
