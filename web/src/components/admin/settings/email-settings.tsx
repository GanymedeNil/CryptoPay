"use client";

import { CircleCheckIcon, CircleXIcon, MailIcon, PlusIcon, SendIcon, TriangleAlertIcon, XIcon } from "lucide-react";
import Link from "next/link";
import { useId, useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { Spinner } from "@/components/ui/spinner";
import { describeError } from "@/lib/admin/errors";
import { useAdminMutation, useEmailStatus } from "@/lib/admin/queries";
import { isEmailAddress, MAX_ALERT_RECIPIENTS, recipientProblem } from "@/lib/admin/settings-form";
import { adminApi } from "@/lib/api/admin";
import { isApiError } from "@/lib/api/client";
import type { TestEmailResult } from "@/lib/api/types";
import { useT } from "@/lib/i18n";
import { cn } from "@/lib/utils";
import { useAuth } from "../auth";
import { describedBy } from "../form-field";

/** `GET /email/status`: configured (host, from) or how to configure it. */
export function EmailStatusPanel() {
  const t = useT();
  const status = useEmailStatus();
  const { canWrite } = useAuth();
  if (status.isPending) {
    return (
      <div className="space-y-1" aria-busy="true">
        <span className="sr-only" role="status">
          {t("admin.settings.emailStatus.loading")}
        </span>
        <Skeleton className="h-10 w-full" />
      </div>
    );
  }
  if (status.error || !status.data) {
    return <p className="text-sm text-destructive">{t("admin.settings.emailStatus.failed")}</p>;
  }
  if (!status.data.configured) {
    return (
      <div className="flex gap-2.5 rounded-lg border border-warning-border bg-warning-surface p-3 text-sm text-warning" role="note" data-testid="email-not-configured">
        <TriangleAlertIcon className="mt-0.5 size-4 shrink-0" aria-hidden />
        <div className="space-y-0.5">
          <p className="font-medium">{t("admin.settings.emailStatus.notConfiguredTitle")}</p>
          <p className="text-pretty">{t("admin.settings.emailStatus.notConfiguredBody")}</p>
        </div>
      </div>
    );
  }
  return (
    <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border bg-muted/30 px-3 py-2 text-sm">
      <p className="flex items-center gap-2">
        <CircleCheckIcon className="size-4 shrink-0 text-success" aria-hidden />
        {t("admin.settings.emailStatus.configured", { host: status.data.host, from: status.data.from })}
      </p>
      {canWrite && (
        <Link href="/admin/emails" className="text-sm font-medium text-primary hover:underline">
          {t("admin.settings.emailStatus.viewOutbox")}
        </Link>
      )}
    </div>
  );
}

/** List of alert recipients: add (Enter or button, comma-separated paste works) and remove. */
export function RecipientsEditor({
  id,
  value,
  onChange,
  error,
  disabled,
}: {
  id: string;
  value: string[];
  onChange: (next: string[]) => void;
  error?: string;
  disabled?: boolean;
}) {
  const t = useT();
  const [text, setText] = useState("");
  const [problem, setProblem] = useState<string | null>(null);
  const add = () => {
    const parts = text.split(/[\s,;]+/).filter(Boolean);
    if (parts.length === 0) return;
    let next = value;
    for (const p of parts) {
      const why = recipientProblem(t, next, p);
      if (why) {
        setProblem(why);
        return;
      }
      next = [...next, p.trim()];
    }
    setProblem(null);
    setText("");
    onChange(next);
  };
  const shown = problem ?? error;
  return (
    <div className="space-y-2">
      {value.length === 0 ? (
        <p className="text-xs text-muted-foreground">{t("admin.settings.fields.recipientsEmpty")}</p>
      ) : (
        <ul className="flex flex-wrap gap-1.5" aria-label={t("admin.settings.fields.alertRecipients")}>
          {value.map((r) => (
            <li
              key={r}
              className={cn(
                "inline-flex h-7 items-center gap-1 rounded-md border bg-muted/40 pr-0.5 pl-2 font-mono text-xs",
                !isEmailAddress(r) && "border-destructive/50 text-destructive",
              )}
            >
              {r}
              <Button
                type="button"
                variant="ghost"
                size="icon-xs"
                disabled={disabled}
                aria-label={t("admin.settings.fields.removeRecipient", { email: r })}
                onClick={() => onChange(value.filter((x) => x !== r))}
              >
                <XIcon aria-hidden />
              </Button>
            </li>
          ))}
        </ul>
      )}
      <div className="flex max-w-md gap-1.5">
        <Input
          id={id}
          type="email"
          inputMode="email"
          autoComplete="off"
          value={text}
          disabled={disabled || value.length >= MAX_ALERT_RECIPIENTS}
          placeholder={t("admin.settings.fields.alertRecipientsPlaceholder")}
          aria-invalid={!!shown}
          aria-describedby={describedBy(id, { description: true, error: shown })}
          onChange={(e) => {
            setText(e.target.value);
            if (problem) setProblem(null);
          }}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              // Enter adds the address instead of submitting the settings form.
              e.preventDefault();
              add();
            }
          }}
        />
        <Button type="button" variant="outline" disabled={disabled || !text.trim()} onClick={add}>
          <PlusIcon data-icon="inline-start" aria-hidden />
          {t("admin.settings.fields.addRecipient")}
        </Button>
      </div>
      {shown && (
        <p id={`${id}-err`} role="alert" className="text-sm text-destructive">
          {shown}
        </p>
      )}
    </div>
  );
}

/** "Send test email" (`POST /email/test`): synchronous, shows the SMTP outcome. */
export function TestEmailPanel() {
  const t = useT();
  const { user } = useAuth();
  const id = useId();
  const [to, setTo] = useState("");
  const [result, setResult] = useState<{ tone: "ok" | "error"; message: string } | null>(null);
  const send = useAdminMutation({
    mutationFn: (addr: string) => adminApi.sendTestEmail(addr ? { to: addr } : {}),
    toastError: false,
    onSuccess: (r: TestEmailResult, addr) =>
      setResult(
        r.ok
          ? { tone: "ok", message: t("admin.settings.testEmail.ok", { to: addr || user.email }) }
          : { tone: "error", message: t("admin.settings.testEmail.failed", { error: r.error }) },
      ),
    onError: (e) =>
      setResult({
        tone: "error",
        message:
          isApiError(e) && e.isConflict
            ? t("admin.settings.testEmail.notConfigured")
            : isApiError(e) && e.fields?.to
              ? `${t("admin.settings.testEmail.to")}: ${e.fields.to}`
              : describeError(t, e),
      }),
  });
  const run = () => {
    const addr = to.trim();
    if (addr && !isEmailAddress(addr)) {
      setResult({ tone: "error", message: t("admin.settings.validation.email") });
      return;
    }
    setResult(null);
    send.mutate(addr);
  };
  return (
    <div className="space-y-2 rounded-lg border p-3">
      <p className="flex items-center gap-2 text-sm font-medium">
        <MailIcon className="size-4 text-muted-foreground" aria-hidden />
        {t("admin.settings.testEmail.title")}
      </p>
      <div className="flex flex-col gap-1.5 sm:flex-row sm:items-end">
        <div className="min-w-0 flex-1 space-y-1">
          <label htmlFor={id} className="text-xs text-muted-foreground">
            {t("admin.settings.testEmail.to")}
          </label>
          <Input
            id={id}
            type="email"
            inputMode="email"
            autoComplete="off"
            value={to}
            placeholder={user.email}
            onChange={(e) => setTo(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                run();
              }
            }}
            aria-describedby={`${id}-hint`}
          />
        </div>
        <Button type="button" variant="outline" disabled={send.isPending} onClick={run}>
          {send.isPending ? <Spinner data-icon="inline-start" /> : <SendIcon data-icon="inline-start" aria-hidden />}
          {send.isPending ? t("admin.settings.testEmail.sending") : t("admin.settings.testEmail.send")}
        </Button>
      </div>
      <p id={`${id}-hint`} className="text-xs text-muted-foreground">
        {t("admin.settings.testEmail.toHint", { email: user.email })}
      </p>
      <div aria-live="polite">
        {result && (
          <p
            data-testid="test-email-result"
            className={cn(
              "flex items-start gap-2 rounded-md px-2.5 py-1.5 text-sm",
              result.tone === "ok" ? "bg-success-surface text-success" : "bg-destructive/5 text-destructive",
            )}
          >
            {result.tone === "ok" ? <CircleCheckIcon className="mt-0.5 size-4 shrink-0" aria-hidden /> : <CircleXIcon className="mt-0.5 size-4 shrink-0" aria-hidden />}
            <span className="min-w-0 break-words">{result.message}</span>
          </p>
        )}
      </div>
    </div>
  );
}
