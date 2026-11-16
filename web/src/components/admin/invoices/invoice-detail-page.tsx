"use client";

import { BadgeCheckIcon, BanIcon, ExternalLinkIcon, RadioIcon } from "lucide-react";
import Link from "next/link";
import { useId, useState } from "react";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { Textarea } from "@/components/ui/textarea";
import { addressLink, safeHttpUrl, txLink } from "@/lib/admin/explorer";
import { adminKeys, useAdminMutation, useChains, useInvoice } from "@/lib/admin/queries";
import { buildTimeline, type TimelineEntry } from "@/lib/admin/timeline";
import { formatCount } from "@/lib/format/units";
import { adminApi } from "@/lib/api/admin";
import type { Chain, InvoiceDetail, Quote, Transfer } from "@/lib/api/types";
import { useLocale, useT, type MessageKey } from "@/lib/i18n";
import { cn } from "@/lib/utils";
import { CryptoAmount, Money } from "../amount";
import { GatedButton } from "../auth";
import { ConfirmDialog } from "../confirm-dialog";
import { DataTable, type Column } from "../data-table";
import { describedBy, FormField } from "../form-field";
import { JsonViewer } from "../json-viewer";
import { MonoValue } from "../mono-value";
import { DetailList, PageHeader, Section } from "../page-header";
import { useBreadcrumbDetail } from "../shell/admin-shell";
import { ErrorState } from "../states";
import { InvoiceStatusBadge, QuoteStatusBadge, StatusBadge, TransferStatusBadge, WebhookStatusBadge } from "../status-badge";
import { DateTimeStack, RelativeTime } from "../time";
import { WebhookEventDetail } from "../webhooks/webhook-event-detail";

export function InvoiceDetailPage({ id }: { id: string }) {
  const t = useT();
  const q = useInvoice(id);
  const chains = useChains({ refetchInterval: false });
  useBreadcrumbDetail(id.slice(0, 8));
  const inv = q.data;

  if (q.error && !inv) {
    return (
      <>
        <PageHeader title={t("admin.invoice.title")} back={{ href: "/admin/invoices", label: t("admin.invoice.back") }} />
        <ErrorState error={q.error} onRetry={() => void q.refetch()} />
      </>
    );
  }
  if (!inv) return <DetailSkeleton />;

  return (
    <>
      <PageHeader
        back={{ href: "/admin/invoices", label: t("admin.invoice.back") }}
        title={inv.title || t("admin.invoice.untitled")}
        meta={
          <>
            <InvoiceStatusBadge status={inv.status} withHint />
            {inv.overpaid && <StatusBadge tone="warning" dot={false}>{t("admin.status.overpaid")}</StatusBadge>}
            {inv.paid_late && <StatusBadge tone="warning" dot={false}>{t("admin.status.paidLate")}</StatusBadge>}
            <MonoValue value={inv.id} label={t("admin.common.id")} className="text-muted-foreground" />
          </>
        }
        actions={<InvoiceActions inv={inv} />}
      />
      {(inv.status === "pending" || inv.status === "partial" || inv.status === "confirming") && (
        <p className="-mt-2 mb-4 flex items-center gap-2 text-xs text-muted-foreground" aria-live="polite">
          <RadioIcon className="size-3.5 animate-soft-pulse text-primary" aria-hidden />
          {t("admin.invoice.live")}
        </p>
      )}
      <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_20rem] xl:grid-cols-[minmax(0,1fr)_24rem]">
        <Summary inv={inv} />
        <Timeline inv={inv} />
        <div className="min-w-0 space-y-4 lg:col-span-2">
          <Quotes quotes={inv.quotes} currency={inv.currency} chains={chains.data} />
          <Transfers transfers={inv.transfers} chains={chains.data} />
          <Section title={t("admin.invoice.webhooks.title")} bodyClassName="p-0">
            <DataTable
              caption={t("admin.invoice.webhooks.title")}
              className="[&>div]:rounded-none [&>div]:border-0"
              rows={inv.webhook_events}
              getRowId={(w) => w.id}
              isLoading={false}
              empty={<p className="p-4 text-sm text-muted-foreground">{t("admin.invoice.webhooks.empty")}</p>}
              renderExpanded={(w) => <WebhookEventDetail event={w} />}
              columns={[
                { id: "event", header: t("admin.webhooks.columns.event"), cell: (w) => <span className="font-mono text-xs">{w.event_type}</span> },
                { id: "status", header: t("admin.webhooks.columns.status"), cell: (w) => <WebhookStatusBadge status={w.status} /> },
                {
                  id: "attempts",
                  header: t("admin.webhooks.columns.attempts"),
                  align: "right",
                  cell: (w) => <span className="font-mono tabular">{w.attempts}</span>,
                },
                {
                  id: "code",
                  header: t("admin.webhooks.columns.lastCode"),
                  cell: (w) => <span className="font-mono tabular">{w.last_status_code || "—"}</span>,
                },
                { id: "created", header: t("admin.webhooks.columns.created"), cell: (w) => <RelativeTime iso={w.created_at} /> },
              ]}
            />
          </Section>
        </div>
      </div>
    </>
  );
}

function InvoiceActions({ inv }: { inv: InvoiceDetail }) {
  const t = useT();
  const [cancelOpen, setCancelOpen] = useState(false);
  const [markOpen, setMarkOpen] = useState(false);
  const checkout = safeHttpUrl(inv.checkout_url);
  const invalidate = [adminKeys.invoices.all, adminKeys.overview.all, adminKeys.webhooks.all];
  const cancel = useAdminMutation({
    mutationFn: () => adminApi.cancelInvoice(inv.id),
    invalidate,
    successMessage: t("admin.invoice.cancelled"),
  });
  const canCancel = inv.status === "pending";
  const canMark = inv.status !== "paid" && inv.status !== "cancelled";
  return (
    <>
      {checkout && (
        <Button asChild variant="outline">
          <a href={checkout} target="_blank" rel="noopener noreferrer" referrerPolicy="no-referrer">
            <ExternalLinkIcon data-icon="inline-start" aria-hidden />
            {t("admin.invoice.openCheckout")}
          </a>
        </Button>
      )}
      <GatedButton
        variant="outline"
        onClick={() => setMarkOpen(true)}
        disabledReason={canMark ? null : t("admin.invoice.markPaidUnavailable")}
      >
        <BadgeCheckIcon data-icon="inline-start" aria-hidden />
        {t("admin.invoice.markPaid")}
      </GatedButton>
      <GatedButton
        variant="destructive"
        onClick={() => setCancelOpen(true)}
        disabledReason={canCancel ? null : t("admin.invoice.cancelOnlyPending")}
      >
        <BanIcon data-icon="inline-start" aria-hidden />
        {t("admin.invoice.cancel")}
      </GatedButton>
      <ConfirmDialog
        open={cancelOpen}
        onOpenChange={setCancelOpen}
        title={t("admin.invoice.cancelTitle")}
        description={t("admin.invoice.cancelBody")}
        confirmLabel={t("admin.invoice.cancelConfirm")}
        destructive
        onConfirm={() => cancel.mutateAsync()}
      />
      <MarkPaidDialog invoiceId={inv.id} open={markOpen} onOpenChange={setMarkOpen} />
    </>
  );
}

/** Manual resolution: requires a note (kept in the audit log). */
export function MarkPaidDialog({ invoiceId, open, onOpenChange }: { invoiceId: string; open: boolean; onOpenChange: (o: boolean) => void }) {
  const t = useT();
  const noteId = useId();
  const [note, setNote] = useState("");
  const [touched, setTouched] = useState(false);
  const mark = useAdminMutation({
    mutationFn: (n: string) => adminApi.markInvoicePaid(invoiceId, { note: n }),
    invalidate: [adminKeys.invoices.all, adminKeys.overview.all, adminKeys.webhooks.all],
    successMessage: t("admin.invoice.markedPaid"),
  });
  const trimmed = note.trim();
  const error = touched && trimmed.length < 3 ? t("admin.invoice.markPaidNoteRequired") : undefined;
  return (
    <ConfirmDialog
      open={open}
      onOpenChange={(o) => {
        onOpenChange(o);
        if (!o) {
          setNote("");
          setTouched(false);
        }
      }}
      title={t("admin.invoice.markPaidTitle")}
      description={t("admin.invoice.markPaidBody")}
      confirmLabel={t("admin.invoice.markPaidConfirm")}
      confirmDisabled={trimmed.length < 3}
      onConfirm={() => mark.mutateAsync(trimmed)}
    >
      <FormField label={t("admin.invoice.markPaidNote")} htmlFor={noteId} error={error}>
        <Textarea
          id={noteId}
          value={note}
          rows={3}
          maxLength={500}
          placeholder={t("admin.invoice.markPaidNotePlaceholder")}
          onChange={(e) => setNote(e.target.value)}
          onBlur={() => setTouched(true)}
          aria-invalid={!!error}
          aria-describedby={describedBy(noteId, { error })}
          aria-required
        />
      </FormField>
    </ConfirmDialog>
  );
}

function Summary({ inv }: { inv: InvoiceDetail }) {
  const t = useT();
  const hasMeta = inv.metadata && Object.keys(inv.metadata).length > 0;
  return (
    <Section title={t("admin.invoice.summary")}>
      <DetailList
        items={[
          {
            label: t("admin.invoice.merchant"),
            value: (
              <Link href={`/admin/merchants/${inv.merchant_id}`} className="text-primary hover:underline">
                {inv.merchant_name}
              </Link>
            ),
          },
          {
            label: t("admin.invoice.orderId"),
            value: inv.order_id ? <MonoValue value={inv.order_id} label={t("admin.invoice.orderId")} /> : "—",
          },
          {
            label: t("admin.invoice.amount"),
            value: <Money amount={inv.amount} currency={inv.currency} className="text-base font-medium" />,
          },
          {
            label: t("admin.invoice.amountUsd"),
            value: <Money amount={inv.amount_usd} currency="USD" />,
            hidden: inv.currency === "USD",
          },
          { label: t("admin.invoice.created"), value: <DateTimeStack iso={inv.created_at} /> },
          { label: t("admin.invoice.expires"), value: <DateTimeStack iso={inv.expires_at} /> },
          { label: t("admin.invoice.paidAt"), value: <DateTimeStack iso={inv.paid_at} />, hidden: !inv.paid_at },
          { label: t("admin.invoice.updated"), value: <DateTimeStack iso={inv.updated_at} /> },
          { label: t("admin.invoice.customerEmail"), value: inv.customer_email || "—", hidden: !inv.customer_email },
          {
            label: t("admin.invoice.checkoutUrl"),
            value: <MonoValue value={inv.checkout_url} label={t("admin.invoice.checkoutUrl")} wrap />,
            wide: true,
          },
          { label: t("admin.invoice.successUrl"), value: <MonoValue value={inv.success_url} label={t("admin.invoice.successUrl")} wrap />, hidden: !inv.success_url, wide: true },
          { label: t("admin.invoice.cancelUrl"), value: <MonoValue value={inv.cancel_url} label={t("admin.invoice.cancelUrl")} wrap />, hidden: !inv.cancel_url, wide: true },
          { label: t("admin.invoice.description"), value: <p className="whitespace-pre-wrap">{inv.description}</p>, hidden: !inv.description, wide: true },
          { label: t("admin.invoice.metadata"), value: <JsonViewer value={inv.metadata} label={t("admin.invoice.metadata")} maxHeight="14rem" />, hidden: !hasMeta, wide: true },
        ]}
      />
      {(inv.overpaid || inv.paid_late) && (
        <ul className="mt-4 space-y-1 rounded-lg border border-warning-border bg-warning-surface px-3 py-2 text-sm text-warning">
          {inv.overpaid && <li>{t("admin.invoice.overpaidNote")}</li>}
          {inv.paid_late && <li>{t("admin.invoice.paidLateNote")}</li>}
        </ul>
      )}
    </Section>
  );
}

function Quotes({ quotes, currency, chains }: { quotes: Quote[]; currency: string; chains?: Chain[] }) {
  const t = useT();
  const columns: Column<Quote>[] = [
    {
      id: "asset",
      header: t("admin.invoice.quotes.asset"),
      cell: (q) => (
        <div>
          <div className="font-medium">
            {q.symbol}
            {q.is_testnet && <span className="ml-1.5 text-[10px] font-semibold text-warning uppercase">{t("admin.status.testnet")}</span>}
          </div>
          <div className="text-xs text-muted-foreground">
            {q.chain_name} · {t(`admin.walletKinds.${q.wallet_kind}` as MessageKey)}
          </div>
        </div>
      ),
    },
    {
      id: "address",
      header: t("admin.invoice.quotes.address"),
      cell: (q) => <MonoValue value={q.address} label={t("admin.invoice.quotes.address")} short={[8, 6]} href={addressLink(q.chain_id, q.address, chains)} />,
    },
    {
      id: "due",
      header: t("admin.invoice.quotes.due"),
      align: "right",
      cell: (q) => <CryptoAmount amount={q.amount} symbol={q.symbol} />,
    },
    {
      id: "received",
      header: `${t("admin.invoice.quotes.received")} / ${t("admin.invoice.quotes.confirmed")}`,
      align: "right",
      cell: (q) => (
        <div>
          <CryptoAmount amount={q.amount_received} />
          <div className="text-xs text-muted-foreground">
            <CryptoAmount amount={q.amount_confirmed} />
          </div>
        </div>
      ),
    },
    {
      id: "rate",
      header: t("admin.invoice.quotes.rate"),
      cell: (q) => (
        <span className="font-mono text-xs whitespace-nowrap tabular">
          {t("admin.invoice.quotes.ratePer", { symbol: q.symbol, rate: q.rate, currency })}
        </span>
      ),
    },
    {
      id: "status",
      header: t("admin.invoice.quotes.status"),
      cell: (q) => (
        <div className="space-y-0.5">
          <QuoteStatusBadge status={q.status} />
          <div className="text-xs text-muted-foreground">{t("admin.invoice.quotes.confirmations", { n: q.required_confirmations })}</div>
        </div>
      ),
    },
    { id: "created", header: t("admin.invoice.quotes.created"), cell: (q) => <RelativeTime iso={q.created_at} /> },
  ];
  return (
    <Section title={t("admin.invoice.quotes.title")} description={t("admin.invoice.quotes.description")} bodyClassName="p-0">
      <DataTable
        caption={t("admin.invoice.quotes.title")}
        className="[&>div]:rounded-none [&>div]:border-0"
        columns={columns}
        rows={[...quotes].sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at))}
        getRowId={(q) => q.id}
        isLoading={false}
        rowClassName={(q) => (q.status === "superseded" || q.status === "closed" ? "opacity-70" : undefined)}
        empty={<p className="p-4 text-sm text-muted-foreground">{t("admin.invoice.quotes.empty")}</p>}
      />
    </Section>
  );
}

export function TransferConfirmations({ tr }: { tr: Transfer }) {
  const { locale } = useLocale();
  const pct = tr.required_confirmations > 0 ? Math.min(1, tr.confirmations / tr.required_confirmations) : 1;
  return (
    <div className="min-w-20">
      <span className="font-mono text-xs tabular">
        {formatCount(tr.confirmations, locale)} / {formatCount(tr.required_confirmations, locale)}
      </span>
      <div className="mt-1 h-1 w-full overflow-hidden rounded-full bg-muted" aria-hidden>
        <div className={cn("h-full rounded-full", pct >= 1 ? "bg-success" : "bg-[var(--chart-5)]")} style={{ width: `${pct * 100}%` }} />
      </div>
    </div>
  );
}

function Transfers({ transfers, chains }: { transfers: Transfer[]; chains?: Chain[] }) {
  const t = useT();
  const { locale } = useLocale();
  const columns: Column<Transfer>[] = [
    {
      id: "tx",
      header: t("admin.invoice.transfers.tx"),
      cell: (tr) => (
        <MonoValue
          value={tr.tx_hash}
          label={t("admin.invoice.transfers.tx")}
          short={[10, 6]}
          href={tr.explorer_url ? safeHttpUrl(tr.explorer_url) : txLink(tr.chain_id, tr.tx_hash, chains)}
        />
      ),
    },
    {
      id: "from",
      header: t("admin.invoice.transfers.from"),
      cell: (tr) => <MonoValue value={tr.from_address} label={t("admin.invoice.transfers.from")} short href={addressLink(tr.chain_id, tr.from_address, chains)} />,
    },
    { id: "amount", header: t("admin.invoice.transfers.amount"), align: "right", cell: (tr) => <CryptoAmount amount={tr.amount} symbol={tr.symbol} /> },
    { id: "conf", header: t("admin.invoice.transfers.confirmations"), cell: (tr) => <TransferConfirmations tr={tr} /> },
    {
      id: "block",
      header: t("admin.invoice.transfers.block"),
      align: "right",
      cell: (tr) => <span className="font-mono text-xs tabular">{tr.block_number ? formatCount(tr.block_number, locale) : "—"}</span>,
    },
    { id: "status", header: t("admin.invoice.transfers.status"), cell: (tr) => <TransferStatusBadge status={tr.status} /> },
    { id: "detected", header: t("admin.invoice.transfers.detected"), cell: (tr) => <RelativeTime iso={tr.detected_at} /> },
  ];
  return (
    <Section title={t("admin.invoice.transfers.title")} bodyClassName="p-0">
      <DataTable
        caption={t("admin.invoice.transfers.title")}
        className="[&>div]:rounded-none [&>div]:border-0"
        columns={columns}
        rows={transfers}
        getRowId={(tr) => tr.id}
        isLoading={false}
        empty={<p className="p-4 text-sm text-muted-foreground">{t("admin.invoice.transfers.empty")}</p>}
      />
    </Section>
  );
}

const DOT: Partial<Record<TimelineEntry["kind"], string>> = {
  paid: "bg-success ring-success/20",
  confirmed: "bg-success ring-success/20",
  detected: "bg-[var(--chart-5)] ring-[var(--chart-5)]/20",
  orphaned: "bg-destructive ring-destructive/20",
  underpaid: "bg-destructive ring-destructive/20",
  webhookFailed: "bg-destructive ring-destructive/20",
  cancelled: "bg-muted-foreground ring-muted-foreground/20",
  expired: "bg-muted-foreground ring-muted-foreground/20",
  expiresAt: "bg-card ring-border border border-dashed border-muted-foreground",
  webhook: "bg-primary/50 ring-primary/10",
};

function Timeline({ inv }: { inv: InvoiceDetail }) {
  const t = useT();
  const entries = buildTimeline(inv);
  const label = (e: TimelineEntry) => t(`admin.invoice.timeline.${e.kind}` as MessageKey, e.vars);
  return (
    <Section title={t("admin.invoice.timeline.title")} className="h-fit" bodyClassName="max-h-[28rem] overflow-y-auto p-4">
      <ol className="relative space-y-4 pl-5 before:absolute before:top-1.5 before:bottom-1.5 before:left-[5px] before:w-px before:bg-border">
        {entries.map((e) => (
          <li key={e.key} className={cn("relative", e.future && "text-muted-foreground")}>
            <span
              className={cn("absolute top-1.5 -left-5 size-[11px] rounded-full ring-4", DOT[e.kind] ?? "bg-primary ring-primary/15")}
              aria-hidden
            />
            <p className={cn("text-sm", e.kind.startsWith("webhook") ? "font-mono text-xs leading-5" : "font-medium")}>{label(e)}</p>
            <RelativeTime iso={e.at} className="text-xs text-muted-foreground" />
          </li>
        ))}
      </ol>
    </Section>
  );
}

function DetailSkeleton() {
  return (
    <div className="space-y-4" aria-busy="true">
      <Skeleton className="h-6 w-32" />
      <Skeleton className="h-8 w-72" />
      <div className="grid gap-4 lg:grid-cols-[1fr_20rem] xl:grid-cols-[1fr_24rem]">
        <Skeleton className="h-64 w-full" />
        <Skeleton className="h-64 w-full" />
        <Skeleton className="h-40 w-full lg:col-span-2" />
      </div>
    </div>
  );
}
