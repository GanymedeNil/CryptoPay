"use client";

import { LockIcon, MailIcon, RotateCwIcon, TriangleAlertIcon } from "lucide-react";
import { Skeleton } from "@/components/ui/skeleton";
import { adminKeys, useAdminMutation, useEmailMessage, useEmailOutbox, useEmailStatus } from "@/lib/admin/queries";
import { EMAIL_KINDS, emailKindLabel } from "@/lib/admin/two-factor";
import { enumParam, useUrlState } from "@/lib/admin/url-state";
import { adminApi } from "@/lib/api/admin";
import type { EmailMessage, EmailMessageStatus, EmailOutboxQuery } from "@/lib/api/types";
import { useT, type MessageKey } from "@/lib/i18n";
import { GatedButton, useAuth } from "../auth";
import { DataTable, type Column } from "../data-table";
import { FilterBar, SelectFilter } from "../filter-bar";
import { PageHeader } from "../page-header";
import { EmptyState, ErrorState } from "../states";
import { StatusBadge, type Tone } from "../status-badge";
import { DateTimeStack, RelativeTime } from "../time";

const STATUSES: EmailMessageStatus[] = ["pending", "sent", "failed"];
const KIND_VALUES = EMAIL_KINDS.map((k) => k.kind);
const EMAIL_TONE: Record<EmailMessageStatus, Tone> = { pending: "progress", sent: "success", failed: "danger" };

export function EmailStatusBadge({ status }: { status: EmailMessageStatus }) {
  const t = useT();
  return <StatusBadge tone={EMAIL_TONE[status]}>{t(`admin.status.email.${status}` as MessageKey)}</StatusBadge>;
}

/** `/admin/emails`: the e-mail outbox (owner / admin; viewers get 403 from the API). */
export function EmailsPage() {
  const t = useT();
  const { canWrite } = useAuth();
  if (!canWrite) {
    return (
      <>
        <PageHeader title={t("admin.emails.title")} />
        <EmptyState icon={LockIcon} title={t("admin.emails.adminOnlyTitle")} description={t("admin.emails.adminOnlyBody")} className="rounded-xl border bg-card" />
      </>
    );
  }
  return <Outbox />;
}

function Outbox() {
  const t = useT();
  const url = useUrlState();
  const status = enumParam(url.params.get("status"), STATUSES);
  const kind = enumParam(url.params.get("kind"), KIND_VALUES);
  const query: EmailOutboxQuery = { page: url.page, page_size: url.pageSize, status, kind };
  const list = useEmailOutbox(query);
  const emailStatus = useEmailStatus();
  const filtered = Boolean(status || kind);

  const columns: Column<EmailMessage>[] = [
    { id: "kind", header: t("admin.emails.columns.kind"), cell: (m) => <span className="whitespace-nowrap">{emailKindLabel(t, m.kind)}</span> },
    { id: "to", header: t("admin.emails.columns.to"), cell: (m) => <span className="block max-w-48 truncate font-mono text-xs">{m.to}</span> },
    { id: "subject", header: t("admin.emails.columns.subject"), cell: (m) => <span className="block max-w-80 truncate">{m.subject}</span> },
    { id: "status", header: t("admin.emails.columns.status"), cell: (m) => <EmailStatusBadge status={m.status} /> },
    { id: "attempts", header: t("admin.emails.columns.attempts"), align: "right", cell: (m) => <span className="font-mono tabular">{m.attempts}</span> },
    { id: "created", header: t("admin.emails.columns.created"), cell: (m) => <RelativeTime iso={m.created_at} className="text-muted-foreground" /> },
    { id: "sent", header: t("admin.emails.columns.sent"), cell: (m) => (m.sent_at ? <RelativeTime iso={m.sent_at} /> : "—") },
    { id: "actions", header: <span className="sr-only">{t("admin.common.actions")}</span>, cell: (m) => <RetryEmailButton message={m} size="xs" /> },
  ];

  return (
    <>
      <PageHeader title={t("admin.emails.title")} description={t("admin.emails.description")} />
      {emailStatus.data && !emailStatus.data.configured && (
        <div className="mb-3 flex gap-2.5 rounded-lg border border-warning-border bg-warning-surface p-3 text-sm text-warning" role="note">
          <TriangleAlertIcon className="mt-0.5 size-4 shrink-0" aria-hidden />
          <div className="space-y-0.5">
            <p className="font-medium">{t("admin.settings.emailStatus.notConfiguredTitle")}</p>
            <p className="text-pretty">{t("admin.settings.emailStatus.notConfiguredBody")}</p>
          </div>
        </div>
      )}
      <FilterBar active={filtered} onClear={() => url.clear(["page_size"])}>
        <SelectFilter
          label={t("admin.emails.status")}
          allLabel={t("admin.emails.allStatuses")}
          value={status ?? ""}
          onChange={(v) => url.set({ status: v })}
          options={STATUSES.map((s) => ({ value: s, label: t(`admin.status.email.${s}` as MessageKey) }))}
          className="sm:w-36"
        />
        <SelectFilter
          label={t("admin.emails.kind")}
          allLabel={t("admin.emails.allKinds")}
          value={kind ?? ""}
          onChange={(v) => url.set({ kind: v })}
          options={EMAIL_KINDS.map((k) => ({
            value: k.kind,
            label: `${t(k.group === "security" ? "admin.emails.groupSecurity" : "admin.emails.groupAlert")} · ${t(k.label)}`,
          }))}
          className="sm:w-64"
        />
      </FilterBar>
      <DataTable
        caption={t("admin.emails.title")}
        columns={columns}
        rows={list.data?.items}
        getRowId={(m) => m.id}
        isLoading={list.isPending}
        isFetching={list.isFetching && list.isPlaceholderData}
        error={list.error}
        onRetry={() => void list.refetch()}
        renderExpanded={(m) => <EmailMessageDetail message={m} />}
        empty={
          filtered ? (
            <EmptyState icon={MailIcon} title={t("admin.emails.filteredEmptyTitle")} description={t("admin.emails.filteredEmptyBody")} />
          ) : (
            <EmptyState icon={MailIcon} title={t("admin.emails.emptyTitle")} description={t("admin.emails.emptyBody")} />
          )
        }
        mobileCard={(m) => (
          <div className="space-y-1.5">
            <div className="flex items-center justify-between gap-2">
              <span className="truncate text-sm font-medium">{emailKindLabel(t, m.kind)}</span>
              <EmailStatusBadge status={m.status} />
            </div>
            <p className="truncate text-sm">{m.subject}</p>
            <div className="flex items-center justify-between gap-2 text-xs text-muted-foreground">
              <span className="truncate font-mono">{m.to}</span>
              <RelativeTime iso={m.created_at} />
            </div>
          </div>
        )}
        pagination={
          list.data && {
            page: list.data.page,
            pageSize: list.data.page_size,
            total: list.data.total,
            onPageChange: (p) => url.set({ page: p > 1 ? p : null }, { resetPage: false }),
            onPageSizeChange: (s) => url.set({ page_size: s === 20 ? null : s }),
          }
        }
      />
    </>
  );
}

/** `POST /email/outbox/{id}/retry` for a failed message. */
export function RetryEmailButton({ message, size = "sm" }: { message: EmailMessage; size?: "sm" | "xs" }) {
  const t = useT();
  const retry = useAdminMutation({
    mutationFn: () => adminApi.retryEmailMessage(message.id),
    invalidate: [adminKeys.email.all],
    successMessage: t("admin.emails.retried"),
  });
  if (message.status !== "failed") return null;
  return (
    <GatedButton
      variant="outline"
      size={size}
      disabled={retry.isPending}
      onClick={(e) => {
        e.stopPropagation();
        retry.mutate();
      }}
    >
      <RotateCwIcon data-icon="inline-start" className={retry.isPending ? "animate-spin" : undefined} aria-hidden />
      {retry.isPending ? t("admin.emails.retrying") : t("admin.emails.retry")}
    </GatedButton>
  );
}

/** Expanded row: recipient, timings, last error and the text body (loaded on demand). */
function EmailMessageDetail({ message }: { message: EmailMessage }) {
  const t = useT();
  const detail = useEmailMessage(message.id);
  return (
    <div className="grid min-w-0 gap-4 lg:grid-cols-[minmax(0,1fr)_minmax(0,2fr)]">
      <div className="min-w-0 space-y-3 text-sm">
        <div className="space-y-0.5">
          <p className="text-xs text-muted-foreground">{t("admin.emails.columns.to")}</p>
          <p className="font-mono text-xs break-all">{message.to}</p>
        </div>
        <div className="grid grid-cols-2 gap-3">
          <div className="space-y-0.5">
            <p className="text-xs text-muted-foreground">{t("admin.emails.columns.created")}</p>
            <DateTimeStack iso={message.created_at} />
          </div>
          <div className="space-y-0.5">
            <p className="text-xs text-muted-foreground">{t("admin.emails.columns.sent")}</p>
            <DateTimeStack iso={message.sent_at} />
          </div>
        </div>
        <p className="text-xs text-muted-foreground">{t("admin.emails.attempts", { n: message.attempts })}</p>
        {message.last_error && (
          <div className="space-y-1">
            <p className="text-xs text-muted-foreground">{t("admin.emails.lastError")}</p>
            <p className="rounded-md border border-destructive/30 bg-destructive/5 px-2.5 py-1.5 font-mono text-xs break-all text-destructive">
              {message.last_error}
            </p>
          </div>
        )}
        <RetryEmailButton message={message} />
      </div>
      <div className="min-w-0 space-y-1">
        <p className="text-xs text-muted-foreground">{t("admin.emails.body")}</p>
        {detail.error ? (
          <ErrorState compact error={detail.error} onRetry={() => void detail.refetch()} />
        ) : !detail.data ? (
          <Skeleton className="h-32 w-full" />
        ) : (
          <pre
            data-testid="email-body"
            className="max-h-96 overflow-auto rounded-md border bg-muted/40 px-3 py-2 font-sans text-[13px] leading-relaxed whitespace-pre-wrap"
          >
            {detail.data.body_text}
          </pre>
        )}
      </div>
    </div>
  );
}
