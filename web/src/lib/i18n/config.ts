export const LOCALES = ["zh", "en"] as const;
export type Locale = (typeof LOCALES)[number];

export const DEFAULT_LOCALE: Locale = "zh";

/** Cookie read by the server to render the right language on first paint. */
export const LOCALE_COOKIE = "cp_locale";
/** Mirror in localStorage (restores the cookie if it was cleared). */
export const LOCALE_STORAGE_KEY = "cp_locale";

export const LOCALE_LABELS: Record<Locale, string> = {
  zh: "中文",
  en: "English",
};

export function isLocale(value: unknown): value is Locale {
  return typeof value === "string" && (LOCALES as readonly string[]).includes(value);
}

/** BCP-47 tag for `<html lang>` and `Intl`. */
export function htmlLang(locale: Locale): string {
  return locale === "zh" ? "zh-CN" : "en";
}

/**
 * Picks a locale from an Accept-Language header ("en-US,en;q=0.9,zh;q=0.8").
 * Falls back to the default (zh).
 */
export function negotiateLocale(acceptLanguage: string | null | undefined): Locale {
  if (!acceptLanguage) return DEFAULT_LOCALE;
  const ranked = acceptLanguage
    .split(",")
    .map((part, i) => {
      const [tag, ...params] = part.trim().split(";");
      const q = params.map((p) => p.trim()).find((p) => p.startsWith("q="));
      const quality = q ? Number.parseFloat(q.slice(2)) : 1;
      return { tag: tag.toLowerCase(), q: Number.isFinite(quality) ? quality : 0, i };
    })
    .filter((x) => x.tag && x.q > 0)
    .sort((a, b) => b.q - a.q || a.i - b.i);
  for (const { tag } of ranked) {
    const base = tag.split("-")[0];
    if (isLocale(base)) return base;
  }
  return DEFAULT_LOCALE;
}
