"use client";

import { useQueryClient } from "@tanstack/react-query";
import { CircleAlertIcon, CircleCheckIcon, SearchIcon, TriangleAlertIcon } from "lucide-react";
import { useId, useState } from "react";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Spinner } from "@/components/ui/spinner";
import { describeError } from "@/lib/admin/errors";
import { adminKeys, useAdminMutation } from "@/lib/admin/queries";
import { adminApi } from "@/lib/api/admin";
import { isApiError } from "@/lib/api/client";
import type { InvoiceDetail, Transfer } from "@/lib/api/types";
import { useT, type MessageKey } from "@/lib/i18n";
import { CryptoAmount, Money } from "../amount";
import { describedBy, FormField } from "../form-field";
import { MonoValue } from "../mono-value";
import { InvoiceStatusBadge } from "../status-badge";

const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** Extracts an invoice UUID from a pasted id or checkout / admin URL. */
export function parseInvoiceId(input: string): string | null {
  const m = /[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/i.exec(input.trim());
  return m && UUID_RE.test(m[0]) ? m[0].toLowerCase() : null;
}

/** Same asset (and therefore chain) as the transfer: what the backend requires. */
export function invoiceAcceptsTransfer(inv: InvoiceDetail, tr: Transfer): boolean {
  return inv.quotes.some((q) => q.asset_id === tr.asset_id && q.chain_id === tr.chain_id);
}

/**
 * Assign an unmatched transfer to an invoice: paste the id, preview the
 * invoice (status, amount, quotes for this asset), then confirm.
 */
export function AssignTransferDialog({
  transfer,
  chainName,
  onOpenChange,
}: {
  transfer: Transfer | null;
  /** Display name of the transfer's chain (falls back to its id). */
  chainName?: string;
  onOpenChange: (open: boolean) => void;
}) {
  return (
    <Dialog open={transfer !== null} onOpenChange={onOpenChange}>
      {transfer && <AssignBody transfer={transfer} chainName={chainName ?? transfer.chain_id} onDone={() => onOpenChange(false)} />}
    </Dialog>
  );
}

function AssignBody({ transfer, chainName, onDone }: { transfer: Transfer; chainName: string; onDone: () => void }) {
  const t = useT();
  const qc = useQueryClient();
  const inputId = useId();
  const [text, setText] = useState("");
  const [lookupError, setLookupError] = useState<string | null>(null);
  const [looking, setLooking] = useState(false);
  const [invoice, setInvoice] = useState<InvoiceDetail | null>(null);

  const assign = useAdminMutation({
    mutationFn: (invoiceId: string) => adminApi.assignTransfer(transfer.id, { invoice_id: invoiceId }),
    invalidate: (_d, invoiceId) => [adminKeys.transfers.all, adminKeys.invoices.detail(invoiceId), adminKeys.invoices.all, adminKeys.overview.all],
    successMessage: t("admin.transfers.assignDialog.assigned"),
    onSuccess: () => onDone(),
  });

  async function lookup() {
    setInvoice(null);
    setLookupError(null);
    const id = parseInvoiceId(text);
    if (!id) {
      setLookupError(t("admin.transfers.assignDialog.invoiceIdInvalid"));
      return;
    }
    setLooking(true);
    try {
      const inv = await qc.fetchQuery({
        queryKey: adminKeys.invoices.detail(id),
        queryFn: ({ signal }) => adminApi.getInvoice(id, { signal }),
        staleTime: 0,
      });
      setInvoice(inv);
    } catch (e) {
      setLookupError(isApiError(e) && e.isNotFound ? t("admin.transfers.assignDialog.notFound") : describeError(t, e));
    } finally {
      setLooking(false);
    }
  }

  const asset = `${transfer.symbol || t("admin.transfers.unknownToken")} · ${chainName}`;
  const compatible = invoice ? invoiceAcceptsTransfer(invoice, transfer) : false;
  const terminal = invoice && ["paid", "cancelled", "expired", "underpaid"].includes(invoice.status);

  return (
    <DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-lg">
      <DialogHeader>
        <DialogTitle>{t("admin.transfers.assignDialog.title")}</DialogTitle>
        <DialogDescription>{t("admin.transfers.assignDialog.description")}</DialogDescription>
      </DialogHeader>

      <div className="space-y-1 rounded-lg border bg-muted/40 p-3 text-sm">
        <p className="text-xs text-muted-foreground">{t("admin.transfers.assignDialog.transfer")}</p>
        <div className="flex flex-wrap items-baseline justify-between gap-2">
          <CryptoAmount amount={transfer.amount} symbol={transfer.symbol} className="text-base font-medium" />
          <span className="text-xs text-muted-foreground">{chainName}</span>
        </div>
        <MonoValue value={transfer.tx_hash} label={t("admin.invoice.transfers.tx")} short={[12, 8]} />
      </div>

      <form
        className="space-y-2"
        onSubmit={(e) => {
          e.preventDefault();
          void lookup();
        }}
      >
        <FormField label={t("admin.transfers.assignDialog.invoiceId")} htmlFor={inputId} error={lookupError ?? undefined}>
          <div className="flex gap-2">
            <Input
              id={inputId}
              value={text}
              onChange={(e) => {
                setText(e.target.value);
                setInvoice(null);
              }}
              placeholder={t("admin.transfers.assignDialog.invoiceIdPlaceholder")}
              className="font-mono text-[13px]"
              autoComplete="off"
              spellCheck={false}
              aria-invalid={!!lookupError}
              aria-describedby={describedBy(inputId, { error: lookupError })}
            />
            <Button type="submit" variant="outline" disabled={!text.trim() || looking}>
              {looking ? <Spinner data-icon="inline-start" /> : <SearchIcon data-icon="inline-start" aria-hidden />}
              {looking ? t("admin.transfers.assignDialog.lookingUp") : t("admin.transfers.assignDialog.lookup")}
            </Button>
          </div>
        </FormField>
      </form>

      {invoice && (
        <div className="space-y-3 rounded-lg border p-3" aria-live="polite">
          <p className="text-xs font-medium text-muted-foreground">{t("admin.transfers.assignDialog.preview")}</p>
          <div className="flex flex-wrap items-start justify-between gap-2">
            <div className="min-w-0">
              <p className="truncate font-medium">{invoice.title || t("admin.invoice.untitled")}</p>
              <p className="text-xs text-muted-foreground">{invoice.merchant_name}</p>
            </div>
            <div className="text-right">
              <Money amount={invoice.amount} currency={invoice.currency} className="font-medium" />
              <div className="mt-1">
                <InvoiceStatusBadge status={invoice.status} />
              </div>
            </div>
          </div>
          {invoice.quotes.length > 0 && (
            <ul className="space-y-1 text-xs">
              {invoice.quotes.map((q) => (
                <li key={q.id} className="flex items-center justify-between gap-2">
                  <span>
                    {q.symbol} · {q.chain_name}
                  </span>
                  <span className="text-muted-foreground">
                    <CryptoAmount amount={q.amount_received} /> / <CryptoAmount amount={q.amount} symbol={q.symbol} />
                  </span>
                </li>
              ))}
            </ul>
          )}
          {compatible ? (
            <p className="flex items-start gap-2 text-sm text-success">
              <CircleCheckIcon className="mt-0.5 size-4 shrink-0" aria-hidden />
              {t("admin.transfers.assignDialog.compatible", { asset })}
            </p>
          ) : (
            <p className="flex items-start gap-2 text-sm text-destructive" role="alert">
              <CircleAlertIcon className="mt-0.5 size-4 shrink-0" aria-hidden />
              {t("admin.transfers.assignDialog.incompatible", { asset })}
            </p>
          )}
          {compatible && terminal && (
            <p className="flex items-start gap-2 text-sm text-warning">
              <TriangleAlertIcon className="mt-0.5 size-4 shrink-0" aria-hidden />
              {t("admin.transfers.assignDialog.finalStatus", { status: t(`admin.status.invoice.${invoice.status}` as MessageKey) })}
            </p>
          )}
          <p className="text-xs text-muted-foreground">{t("admin.transfers.assignDialog.warning")}</p>
        </div>
      )}

      <DialogFooter>
        <Button type="button" variant="outline" onClick={onDone}>
          {t("admin.common.cancel")}
        </Button>
        <Button type="button" disabled={!invoice || !compatible || assign.isPending} onClick={() => invoice && assign.mutate(invoice.id)}>
          {assign.isPending && <Spinner data-icon="inline-start" />}
          {assign.isPending ? t("admin.transfers.assignDialog.assigning") : t("admin.transfers.assignDialog.confirm")}
        </Button>
      </DialogFooter>
    </DialogContent>
  );
}
