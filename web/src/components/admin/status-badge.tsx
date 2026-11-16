"use client";

import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import type {
  AdminRole,
  InvoiceStatus,
  MerchantStatus,
  QuoteStatus,
  TransferStatus,
  WebhookEventStatus,
} from "@/lib/api/types";
import { useT, type MessageKey } from "@/lib/i18n";
import { cn } from "@/lib/utils";

export type Tone = "neutral" | "info" | "progress" | "success" | "warning" | "danger" | "muted";

const TONE_CLASS: Record<Tone, string> = {
  neutral: "border-border bg-card text-foreground",
  info: "border-transparent bg-primary/10 text-primary",
  progress: "border-transparent bg-[color-mix(in_oklch,var(--chart-5)_16%,transparent)] text-[color-mix(in_oklch,var(--chart-5),var(--foreground)_25%)]",
  success: "border-transparent bg-success-surface text-success",
  warning: "border-warning-border bg-warning-surface text-warning",
  danger: "border-transparent bg-destructive/10 text-destructive dark:bg-destructive/20",
  muted: "border-transparent bg-muted text-muted-foreground",
};

const DOT_CLASS: Record<Tone, string> = {
  neutral: "bg-muted-foreground",
  info: "bg-primary",
  progress: "bg-[var(--chart-5)] animate-soft-pulse",
  success: "bg-success",
  warning: "bg-warning",
  danger: "bg-destructive",
  muted: "bg-muted-foreground/60",
};

/** Small pill with a status dot. Colour is never the only signal: the text says it. */
export function StatusBadge({
  tone,
  children,
  hint,
  className,
  dot = true,
}: {
  tone: Tone;
  children: React.ReactNode;
  /** Tooltip explaining what the status means. */
  hint?: string;
  className?: string;
  dot?: boolean;
}) {
  const badge = (
    <span
      className={cn(
        "inline-flex h-5 shrink-0 items-center gap-1.5 rounded-full border px-2 text-xs font-medium whitespace-nowrap",
        TONE_CLASS[tone],
        className,
      )}
    >
      {dot && <span className={cn("size-1.5 rounded-full", DOT_CLASS[tone])} aria-hidden />}
      {children}
    </span>
  );
  if (!hint) return badge;
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span tabIndex={0} className="inline-flex rounded-full outline-none focus-visible:ring-2 focus-visible:ring-ring/50">
          {badge}
        </span>
      </TooltipTrigger>
      <TooltipContent className="max-w-60">{hint}</TooltipContent>
    </Tooltip>
  );
}

export const INVOICE_TONE: Record<InvoiceStatus, Tone> = {
  pending: "neutral",
  partial: "warning",
  confirming: "progress",
  paid: "success",
  underpaid: "danger",
  expired: "muted",
  cancelled: "muted",
};

export function InvoiceStatusBadge({ status, withHint = false }: { status: InvoiceStatus; withHint?: boolean }) {
  const t = useT();
  return (
    <StatusBadge
      tone={INVOICE_TONE[status]}
      hint={withHint ? t(`admin.status.invoiceHint.${status}` as MessageKey) : undefined}
    >
      {t(`admin.status.invoice.${status}` as MessageKey)}
    </StatusBadge>
  );
}

const QUOTE_TONE: Record<QuoteStatus, Tone> = {
  active: "info",
  superseded: "muted",
  paid: "success",
  closed: "muted",
};

export function QuoteStatusBadge({ status }: { status: QuoteStatus }) {
  const t = useT();
  return <StatusBadge tone={QUOTE_TONE[status]}>{t(`admin.status.quote.${status}` as MessageKey)}</StatusBadge>;
}

const TRANSFER_TONE: Record<TransferStatus, Tone> = {
  pending: "progress",
  confirmed: "success",
  orphaned: "danger",
};

export function TransferStatusBadge({ status }: { status: TransferStatus }) {
  const t = useT();
  return <StatusBadge tone={TRANSFER_TONE[status]}>{t(`admin.status.transfer.${status}` as MessageKey)}</StatusBadge>;
}

const WEBHOOK_TONE: Record<WebhookEventStatus, Tone> = {
  pending: "progress",
  delivered: "success",
  failed: "danger",
};

export function WebhookStatusBadge({ status }: { status: WebhookEventStatus }) {
  const t = useT();
  return <StatusBadge tone={WEBHOOK_TONE[status]}>{t(`admin.status.webhook.${status}` as MessageKey)}</StatusBadge>;
}

export function MerchantStatusBadge({ status }: { status: MerchantStatus }) {
  const t = useT();
  return (
    <StatusBadge tone={status === "active" ? "success" : "muted"}>
      {t(`admin.status.merchant.${status}` as MessageKey)}
    </StatusBadge>
  );
}

export function ActiveBadge({ active }: { active: boolean }) {
  const t = useT();
  return (
    <StatusBadge tone={active ? "success" : "muted"}>
      {active ? t("admin.status.user.active") : t("admin.status.user.inactive")}
    </StatusBadge>
  );
}

const ROLE_CLASS: Record<AdminRole, string> = {
  owner: "border-transparent bg-primary text-primary-foreground",
  admin: "border-transparent bg-primary/10 text-primary",
  viewer: "border-border bg-card text-muted-foreground",
};

export function RoleBadge({ role, className }: { role: AdminRole; className?: string }) {
  const t = useT();
  return (
    <span
      className={cn(
        "inline-flex h-5 items-center rounded-md border px-1.5 text-[11px] font-semibold tracking-wide whitespace-nowrap uppercase",
        ROLE_CLASS[role],
        className,
      )}
    >
      {t(`admin.roles.${role}` as MessageKey)}
    </span>
  );
}

/** Mainnet / testnet chip. */
export function NetworkBadge({ testnet }: { testnet: boolean }) {
  const t = useT();
  if (!testnet) return null;
  return (
    <span className="inline-flex h-4.5 items-center rounded border border-warning-border bg-warning-surface px-1 text-[10px] font-semibold tracking-wide text-warning uppercase">
      {t("admin.status.testnet")}
    </span>
  );
}
