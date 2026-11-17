import type { EmailKind, LoginChallenge, LoginResponse } from "@/lib/api/types";
import type { MessageKey, TranslateFn } from "@/lib/i18n/types";

/** `POST /auth/login` answered 202: a second factor is needed. */
export function isLoginChallenge(r: LoginResponse | null | undefined): r is LoginChallenge {
  return typeof r === "object" && r !== null && (r as Partial<LoginChallenge>).two_factor_required === true;
}

/** A sign-in challenge is deleted by the server after this many wrong codes (docs/api.md). */
export const MAX_CHALLENGE_ATTEMPTS = 5;

/** TOTP codes have 6 digits (RFC 6238 as configured by the gateway). */
export const OTP_LENGTH = 6;

/**
 * Digits of a typed or pasted one-time code ("123 456", "123-456", "Code:
 * 123456"), at most 6.
 */
export function normalizeOtp(input: string): string {
  return input.replace(/\D+/g, "").slice(0, OTP_LENGTH);
}

/** Recovery codes are compared case-insensitively, surrounding blanks ignored. */
export function normalizeRecoveryCode(input: string): string {
  return input.trim().toLowerCase().replace(/\s+/g, "");
}

/** "JBSWY3DPEHPK3PXP" → "JBSW Y3DP EHPK 3PXP" (manual entry in an authenticator app). */
export function groupSecret(secret: string, size = 4): string {
  const clean = secret.replace(/\s+/g, "");
  const groups: string[] = [];
  for (let i = 0; i < clean.length; i += size) groups.push(clean.slice(i, i + size));
  return groups.join(" ");
}

/** Plain-text file offered as "download .txt" next to freshly generated recovery codes. */
export function recoveryCodesText(
  t: TranslateFn,
  { email, codes, generatedAt }: { email: string; codes: readonly string[]; generatedAt: Date },
): string {
  const lines = [
    t("admin.twoFactor.recovery.fileTitle"),
    t("admin.twoFactor.recovery.fileAccount", { email }),
    t("admin.twoFactor.recovery.fileCreated", { date: generatedAt.toISOString().replace("T", " ").replace(/\.\d+Z$/, " UTC") }),
    "",
    ...codes.map((c, i) => `${String(i + 1).padStart(2, " ")}. ${c}`),
    "",
    t("admin.twoFactor.recovery.fileNote"),
    "",
  ];
  return lines.join("\n");
}

/** File name of the recovery-codes download, e.g. "cryptopay-recovery-codes-owner-cryptopay.test.txt". */
export function recoveryCodesFileName(email: string): string {
  const safe = email.toLowerCase().replace(/[^a-z0-9.-]+/g, "-").replace(/^-+|-+$/g, "");
  return `cryptopay-recovery-codes-${safe || "account"}.txt`;
}

// ---- e-mail kinds -----------------------------------------------------------

export interface EmailKindInfo {
  kind: EmailKind;
  group: "security" | "alert";
  label: MessageKey;
}

/** Every documented e-mail kind with its label, in display order. */
export const EMAIL_KINDS: readonly EmailKindInfo[] = [
  { kind: "security.new_login", group: "security", label: "admin.emails.kinds.newLogin" },
  { kind: "security.login_blocked", group: "security", label: "admin.emails.kinds.loginBlocked" },
  { kind: "security.password_changed", group: "security", label: "admin.emails.kinds.passwordChanged" },
  { kind: "security.2fa_enabled", group: "security", label: "admin.emails.kinds.twoFactorEnabled" },
  { kind: "security.2fa_disabled", group: "security", label: "admin.emails.kinds.twoFactorDisabled" },
  { kind: "security.2fa_reset", group: "security", label: "admin.emails.kinds.twoFactorReset" },
  { kind: "security.recovery_codes", group: "security", label: "admin.emails.kinds.recoveryCodes" },
  { kind: "security.recovery_code_used", group: "security", label: "admin.emails.kinds.recoveryCodeUsed" },
  { kind: "security.account_created", group: "security", label: "admin.emails.kinds.accountCreated" },
  { kind: "security.role_changed", group: "security", label: "admin.emails.kinds.roleChanged" },
  { kind: "security.account_deactivated", group: "security", label: "admin.emails.kinds.accountDeactivated" },
  { kind: "security.account_reactivated", group: "security", label: "admin.emails.kinds.accountReactivated" },
  { kind: "alert.webhook_failed", group: "alert", label: "admin.emails.kinds.webhookFailed" },
  { kind: "alert.transfer_unmatched", group: "alert", label: "admin.emails.kinds.transferUnmatched" },
  { kind: "alert.invoice_underpaid", group: "alert", label: "admin.emails.kinds.invoiceUnderpaid" },
  { kind: "alert.scanner_failing", group: "alert", label: "admin.emails.kinds.scannerFailing" },
];

/** Readable label of an e-mail kind; unknown kinds are shown as sent by the server. */
export function emailKindLabel(t: TranslateFn, kind: string): string {
  const info = EMAIL_KINDS.find((k) => k.kind === kind);
  return info ? t(info.label) : kind;
}

// ---- user agents ------------------------------------------------------------

/**
 * A short "Browser · OS" description of a session's user agent, or null when
 * it can't be recognised (the raw string is then shown).
 */
export function describeUserAgent(ua: string): { browser: string | null; os: string | null } | null {
  if (!ua) return null;
  const browser = /Edg(e|A|iOS)?\//.test(ua)
    ? "Edge"
    : /OPR\/|Opera/.test(ua)
      ? "Opera"
      : /Firefox\/|FxiOS\//.test(ua)
        ? "Firefox"
        : /Chrome\/|CriOS\//.test(ua)
          ? "Chrome"
          : /Safari\//.test(ua) && /Version\//.test(ua)
            ? "Safari"
            : /^curl\//.test(ua)
              ? "curl"
              : null;
  const os = /iPhone|iPad|iPod/.test(ua)
    ? "iOS"
    : /Android/.test(ua)
      ? "Android"
      : /Windows/.test(ua)
        ? "Windows"
        : /Mac OS X|Macintosh/.test(ua)
          ? "macOS"
          : /CrOS/.test(ua)
            ? "ChromeOS"
            : /Linux/.test(ua)
              ? "Linux"
              : null;
  if (!browser && !os) return null;
  return { browser, os };
}
