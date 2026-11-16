import type { Locale } from "./config";
import { en } from "./dictionaries/en";
import { zh } from "./dictionaries/zh";
import type { Dictionary, MessageKey, MessageVars, TranslateFn } from "./types";

export const dictionaries: Record<Locale, Dictionary> = { zh, en };

function lookup(dict: Dictionary, key: string): string | undefined {
  let node: unknown = dict;
  for (const part of key.split(".")) {
    if (typeof node !== "object" || node === null) return undefined;
    node = (node as Record<string, unknown>)[part];
  }
  return typeof node === "string" ? node : undefined;
}

/** Replaces `{name}` placeholders; unknown placeholders are left as is. */
export function interpolate(template: string, vars?: MessageVars): string {
  if (!vars) return template;
  return template.replace(/\{(\w+)\}/g, (match, name: string) =>
    Object.prototype.hasOwnProperty.call(vars, name) ? String(vars[name]) : match,
  );
}

/**
 * Returns `t(key, vars)` for a locale. Missing keys fall back to zh, then to
 * the key itself (visible in the UI, so it gets noticed).
 */
export function createTranslator(locale: Locale): TranslateFn {
  const dict = dictionaries[locale];
  return (key: MessageKey, vars?: MessageVars) =>
    interpolate(lookup(dict, key) ?? lookup(zh, key) ?? key, vars);
}
