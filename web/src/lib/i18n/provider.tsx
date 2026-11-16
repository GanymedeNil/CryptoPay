"use client";

import { createContext, useCallback, useContext, useEffect, useMemo, useState } from "react";
import {
  htmlLang,
  isLocale,
  LOCALE_COOKIE,
  LOCALE_STORAGE_KEY,
  type Locale,
} from "./config";
import { createTranslator } from "./translate";
import type { TranslateFn } from "./types";

interface I18nContextValue {
  locale: Locale;
  setLocale: (locale: Locale) => void;
  t: TranslateFn;
}

const I18nContext = createContext<I18nContextValue | null>(null);

const ONE_YEAR = 60 * 60 * 24 * 365;

function readCookie(name: string): string | undefined {
  if (typeof document === "undefined") return undefined;
  return document.cookie
    .split("; ")
    .find((c) => c.startsWith(`${name}=`))
    ?.slice(name.length + 1);
}

/** Persists the choice in a cookie (read by the server) and localStorage. */
export function persistLocale(locale: Locale): void {
  if (typeof document === "undefined") return;
  document.cookie = `${LOCALE_COOKIE}=${locale}; Path=/; Max-Age=${ONE_YEAR}; SameSite=Lax`;
  try {
    window.localStorage.setItem(LOCALE_STORAGE_KEY, locale);
  } catch {
    // storage may be unavailable (private mode, in-app browsers)
  }
}

function readStoredLocale(): Locale | undefined {
  try {
    const v = window.localStorage.getItem(LOCALE_STORAGE_KEY);
    return isLocale(v) ? v : undefined;
  } catch {
    return undefined;
  }
}

export function I18nProvider({
  initialLocale,
  children,
}: {
  /** Resolved on the server from the cookie / Accept-Language. */
  initialLocale: Locale;
  children: React.ReactNode;
}) {
  const [locale, setLocaleState] = useState<Locale>(initialLocale);

  const setLocale = useCallback((next: Locale) => {
    persistLocale(next);
    setLocaleState(next);
  }, []);

  // The cookie can be cleared while localStorage survives: restore from it.
  useEffect(() => {
    if (isLocale(readCookie(LOCALE_COOKIE))) return;
    const stored = readStoredLocale();
    if (stored && stored !== initialLocale) {
      persistLocale(stored);
      queueMicrotask(() => setLocaleState(stored));
    }
  }, [initialLocale]);

  useEffect(() => {
    document.documentElement.lang = htmlLang(locale);
  }, [locale]);

  const value = useMemo<I18nContextValue>(
    () => ({ locale, setLocale, t: createTranslator(locale) }),
    [locale, setLocale],
  );

  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>;
}

function useI18n(): I18nContextValue {
  const ctx = useContext(I18nContext);
  if (!ctx) throw new Error("useT/useLocale must be used inside <I18nProvider>");
  return ctx;
}

/** `const t = useT(); t("checkout.pay.sendExactly")` */
export function useT(): TranslateFn {
  return useI18n().t;
}

export function useLocale(): { locale: Locale; setLocale: (locale: Locale) => void } {
  const { locale, setLocale } = useI18n();
  return { locale, setLocale };
}
