import type { EmailKind } from "@/lib/api/types";
import type { MessageKey, TranslateFn } from "@/lib/i18n/types";

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
