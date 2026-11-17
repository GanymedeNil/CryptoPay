import { z } from "zod";
import { bpsToPercent, formatDuration, joinDuration, percentToBps, splitDuration, type DurationUnit } from "@/lib/format/units";
import type { EmailLocale, Settings } from "@/lib/api/types";
import type { Locale } from "@/lib/i18n/config";
import type { TranslateFn } from "@/lib/i18n/types";

/**
 * The settings form edits values in operator units (minutes / hours,
 * percent) as strings; the API takes seconds and basis points. These pure
 * helpers convert both ways and are unit tested.
 */
export interface SettingsFormValues {
  invoice_ttl: string;
  invoice_ttl_unit: DurationUnit;
  grace_period: string;
  grace_period_unit: DurationUnit;
  underpay_tolerance_pct: string;
  stablecoin_peg: boolean;
  rate_markup_pct: string;
  webhook_max_attempts: string;
  webhook_timeout_seconds: string;
  checkout_base_url: string;
  walletconnect_project_id: string;
  security_emails: boolean;
  alert_emails: boolean;
  alert_recipients: string[];
  email_locale: EmailLocale;
}

/** `alert_recipients` holds at most this many addresses (docs/api.md). */
export const MAX_ALERT_RECIPIENTS = 10;

/** Same shape check as the rest of the console (the server validates strictly). */
export function isEmailAddress(v: string): boolean {
  return /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(v) && v.length <= 254;
}

export const EMAIL_LOCALES: readonly EmailLocale[] = ["zh", "en"];

export const LIMITS = {
  invoice_ttl_seconds: [60, 86_400],
  grace_period_seconds: [0, 86_400],
  underpay_tolerance_bps: [0, 1_000],
  rate_markup_bps: [0, 2_000],
  webhook_max_attempts: [1, 50],
  webhook_timeout_seconds: [1, 120],
} as const;

const DURATION_UNITS: DurationUnit[] = ["minutes", "hours"];

export function settingsToForm(s: Settings): SettingsFormValues {
  const ttl = splitDuration(s.invoice_ttl_seconds, DURATION_UNITS);
  const grace = splitDuration(s.grace_period_seconds, s.grace_period_seconds % 60 === 0 ? DURATION_UNITS : ["seconds"]);
  return {
    invoice_ttl: String(ttl.value),
    invoice_ttl_unit: ttl.unit,
    grace_period: String(grace.value),
    grace_period_unit: grace.unit,
    underpay_tolerance_pct: bpsToPercent(s.underpay_tolerance_bps),
    stablecoin_peg: s.stablecoin_peg,
    rate_markup_pct: bpsToPercent(s.rate_markup_bps),
    webhook_max_attempts: String(s.webhook_max_attempts),
    webhook_timeout_seconds: String(s.webhook_timeout_seconds),
    checkout_base_url: s.checkout_base_url,
    walletconnect_project_id: s.walletconnect_project_id,
    security_emails: s.security_emails,
    alert_emails: s.alert_emails,
    alert_recipients: [...(s.alert_recipients ?? [])],
    email_locale: s.email_locale,
  };
}

/** Assumes values passed `settingsSchema` (throws otherwise). */
export function formToSettings(v: SettingsFormValues): Settings {
  const req = (n: number | null, field: string) => {
    if (n === null) throw new RangeError(`invalid ${field}`);
    return n;
  };
  return {
    invoice_ttl_seconds: req(joinDuration(v.invoice_ttl, v.invoice_ttl_unit), "invoice_ttl"),
    grace_period_seconds: req(joinDuration(v.grace_period, v.grace_period_unit), "grace_period"),
    underpay_tolerance_bps: req(percentToBps(v.underpay_tolerance_pct), "underpay_tolerance"),
    stablecoin_peg: v.stablecoin_peg,
    rate_markup_bps: req(percentToBps(v.rate_markup_pct), "rate_markup"),
    webhook_max_attempts: Number(v.webhook_max_attempts),
    webhook_timeout_seconds: Number(v.webhook_timeout_seconds),
    checkout_base_url: v.checkout_base_url.trim().replace(/\/+$/, ""),
    walletconnect_project_id: v.walletconnect_project_id.trim(),
    security_emails: v.security_emails,
    alert_emails: v.alert_emails,
    alert_recipients: v.alert_recipients.map((r) => r.trim()).filter(Boolean),
    email_locale: v.email_locale,
  };
}

/** API field → form field (for `ApiError.fields`). */
export const SETTINGS_FIELD_MAP: Record<keyof Settings, keyof SettingsFormValues> = {
  invoice_ttl_seconds: "invoice_ttl",
  grace_period_seconds: "grace_period",
  underpay_tolerance_bps: "underpay_tolerance_pct",
  stablecoin_peg: "stablecoin_peg",
  rate_markup_bps: "rate_markup_pct",
  webhook_max_attempts: "webhook_max_attempts",
  webhook_timeout_seconds: "webhook_timeout_seconds",
  checkout_base_url: "checkout_base_url",
  walletconnect_project_id: "walletconnect_project_id",
  security_emails: "security_emails",
  alert_emails: "alert_emails",
  alert_recipients: "alert_recipients",
  email_locale: "email_locale",
};

/**
 * Why `address` can't be added to the recipient list (null = it can).
 * Duplicates are compared case-insensitively.
 */
export function recipientProblem(t: TranslateFn, list: readonly string[], address: string): string | null {
  const v = address.trim();
  if (!isEmailAddress(v)) return t("admin.settings.validation.email");
  if (list.some((r) => r.toLowerCase() === v.toLowerCase())) return t("admin.settings.validation.duplicateEmail");
  if (list.length >= MAX_ALERT_RECIPIENTS) return t("admin.settings.validation.tooManyRecipients", { max: MAX_ALERT_RECIPIENTS });
  return null;
}

export function settingsSchema(t: TranslateFn, locale: Locale = "en") {
  const unit = z.enum(["seconds", "minutes", "hours"]);
  const integer = (min: number, max: number) =>
    z
      .string()
      .trim()
      .refine((v) => /^\d+$/.test(v), t("admin.settings.validation.integer"))
      .refine((v) => !/^\d+$/.test(v) || (Number(v) >= min && Number(v) <= max), t("admin.settings.validation.range", { min, max }));
  const percent = (minBps: number, maxBps: number) =>
    z
      .string()
      .trim()
      .refine((v) => percentToBps(v) !== null, t("admin.settings.validation.percent"))
      .refine(
        (v) => {
          const b = percentToBps(v);
          // Only one message per problem: the format refine reports invalid input.
          return b === null || (b >= minBps && b <= maxBps);
        },
        t("admin.settings.validation.range", { min: `${bpsToPercent(minBps)}%`, max: `${bpsToPercent(maxBps)}%` }),
      );
  return z
    .object({
      invoice_ttl: z.string().trim(),
      invoice_ttl_unit: unit,
      grace_period: z.string().trim(),
      grace_period_unit: unit,
      underpay_tolerance_pct: percent(...LIMITS.underpay_tolerance_bps),
      stablecoin_peg: z.boolean(),
      rate_markup_pct: percent(...LIMITS.rate_markup_bps),
      webhook_max_attempts: integer(...LIMITS.webhook_max_attempts),
      webhook_timeout_seconds: integer(...LIMITS.webhook_timeout_seconds),
      checkout_base_url: z
        .string()
        .trim()
        .refine((v) => {
          try {
            const u = new URL(v);
            return (u.protocol === "http:" || u.protocol === "https:") && !u.search && !u.hash;
          } catch {
            return false;
          }
        }, t("admin.settings.validation.url")),
      walletconnect_project_id: z.string().trim().max(128),
      security_emails: z.boolean(),
      alert_emails: z.boolean(),
      alert_recipients: z
        .array(z.string())
        .max(MAX_ALERT_RECIPIENTS, t("admin.settings.validation.tooManyRecipients", { max: MAX_ALERT_RECIPIENTS }))
        .superRefine((list, ctx) => {
          const seen = new Set<string>();
          for (const r of list) {
            const v = r.trim();
            if (!isEmailAddress(v)) {
              ctx.addIssue({ code: "custom", message: t("admin.settings.validation.email") });
              return;
            }
            if (seen.has(v.toLowerCase())) {
              ctx.addIssue({ code: "custom", message: t("admin.settings.validation.duplicateEmail") });
              return;
            }
            seen.add(v.toLowerCase());
          }
        }),
      email_locale: z.enum(["zh", "en"]),
    })
    .superRefine((v, ctx) => {
      const check = (field: "invoice_ttl" | "grace_period", u: DurationUnit, [min, max]: readonly [number, number]) => {
        const raw = v[field];
        if (!raw) return ctx.addIssue({ code: "custom", path: [field], message: t("admin.settings.validation.required") });
        if (!/^\d+(\.\d+)?$/.test(raw)) return ctx.addIssue({ code: "custom", path: [field], message: t("admin.settings.validation.number") });
        const s = joinDuration(raw, u);
        if (s === null) return ctx.addIssue({ code: "custom", path: [field], message: t("admin.settings.validation.wholeSeconds") });
        if (s < min || s > max) {
          ctx.addIssue({
            code: "custom",
            path: [field],
            message: t("admin.settings.validation.range", { min: formatDuration(min, locale), max: formatDuration(max, locale) }),
          });
        }
      };
      check("invoice_ttl", v.invoice_ttl_unit, LIMITS.invoice_ttl_seconds);
      check("grace_period", v.grace_period_unit, LIMITS.grace_period_seconds);
    });
}
