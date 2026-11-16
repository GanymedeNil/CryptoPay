import type { Metadata } from "next";
import { getServerT } from "@/lib/i18n/server";
import type { MessageKey } from "@/lib/i18n/types";

/**
 * `export const generateMetadata = titleMetadata("admin.nav.invoices")` —
 * a localized `<title>` (the admin layout adds "· Admin · CryptoPay"). Reads
 * the locale cookie, so it streams with the rest of the request-time render;
 * the language switcher's `router.refresh()` re-renders it.
 */
export function titleMetadata(key: MessageKey) {
  return async function generateMetadata(): Promise<Metadata> {
    const t = await getServerT();
    return { title: t(key) };
  };
}

/** Detail pages: "<label> <first 8 chars of the id>". */
export function idTitleMetadata(key: MessageKey) {
  return async function generateMetadata({ params }: { params: Promise<{ id: string }> }): Promise<Metadata> {
    const [t, { id }] = await Promise.all([getServerT(), params]);
    return { title: `${t(key)} ${id.slice(0, 8)}` };
  };
}
