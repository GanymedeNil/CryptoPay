// Client-safe i18n entry point. Server components use "@/lib/i18n/server".
export * from "./config";
export * from "./types";
export { createTranslator, interpolate, dictionaries } from "./translate";
export { I18nProvider, useT, useLocale, persistLocale } from "./provider";
