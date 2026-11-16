import { cookies, headers } from "next/headers";
import { isLocale, LOCALE_COOKIE, negotiateLocale, type Locale } from "./config";
import { createTranslator } from "./translate";

/**
 * Locale of the current request: the `cp_locale` cookie, else the browser's
 * Accept-Language, else zh. Server components only (dynamic: wrap callers
 * in <Suspense> — the root layout already does).
 */
export async function getRequestLocale(): Promise<Locale> {
  const fromCookie = (await cookies()).get(LOCALE_COOKIE)?.value;
  if (isLocale(fromCookie)) return fromCookie;
  return negotiateLocale((await headers()).get("accept-language"));
}

/** `const t = await getServerT()` inside a server component. */
export async function getServerT() {
  return createTranslator(await getRequestLocale());
}
