"use client";

import { MoreHorizontalIcon, PencilIcon, PlusIcon, TrashIcon, WalletIcon } from "lucide-react";
import { useId, useState } from "react";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Spinner } from "@/components/ui/spinner";
import { describeError } from "@/lib/admin/errors";
import { adminKeys, useAdminMutation, useChains, useWallets } from "@/lib/admin/queries";
import { formatCount } from "@/lib/format/units";
import { adminApi } from "@/lib/api/admin";
import { isApiError } from "@/lib/api/client";
import type { Wallet } from "@/lib/api/types";
import { useLocale, useT, type MessageKey } from "@/lib/i18n";
import { GatedButton, RoleTooltip, useAuth } from "../auth";
import { ConfirmDialog } from "../confirm-dialog";
import { DataTable, type Column } from "../data-table";
import { describedBy, FormField } from "../form-field";
import { MonoValue } from "../mono-value";
import { PageHeader } from "../page-header";
import { EmptyState } from "../states";
import { StatusBadge } from "../status-badge";
import { RelativeTime } from "../time";
import { CreateWalletDialog } from "./create-wallet-dialog";

export function WalletsPage() {
  const t = useT();
  const { locale } = useLocale();
  const { canWrite } = useAuth();
  const wallets = useWallets();
  const chains = useChains({ refetchInterval: false });
  const [createOpen, setCreateOpen] = useState(false);
  const [renaming, setRenaming] = useState<Wallet | null>(null);
  const [deleting, setDeleting] = useState<Wallet | null>(null);
  const [inUse, setInUse] = useState<Wallet | null>(null);
  const chainName = (id: string) => chains.data?.find((c) => c.id === id)?.name ?? id;

  const remove = useAdminMutation({
    mutationFn: (w: Wallet) => adminApi.deleteWallet(w.id),
    invalidate: [adminKeys.wallets, adminKeys.chains],
    successMessage: t("admin.wallets.deleted"),
    toastError: (e) => !(isApiError(e) && e.isConflict),
    onError: (e, w) => {
      if (isApiError(e) && e.isConflict) setInUse(w);
    },
  });

  const columns: Column<Wallet>[] = [
    {
      id: "name",
      header: `${t("admin.wallets.columns.name")} / ${t("admin.wallets.columns.type")}`,
      cell: (w) => (
        <div className="space-y-1">
          <div className="font-medium">{w.name}</div>
          <div className="flex flex-wrap items-center gap-1">
            <span className="rounded border px-1 font-mono text-[10px] uppercase">{t(`admin.families.${w.family}` as MessageKey)}</span>
            <StatusBadge tone={w.kind === "xpub" ? "info" : "neutral"} dot={false}>
              {t(`admin.walletKinds.${w.kind}` as MessageKey)}
            </StatusBadge>
          </div>
        </div>
      ),
    },
    {
      id: "key",
      header: t("admin.wallets.columns.key"),
      cell: (w) =>
        w.kind === "xpub" ? (
          <MonoValue value={w.xpub} label={t("admin.wallets.wizard.xpub")} short={[10, 8]} />
        ) : (
          <MonoValue value={w.address} label={t("admin.wallets.wizard.address")} short={[8, 6]} />
        ),
    },
    {
      id: "next",
      header: t("admin.wallets.columns.nextIndex"),
      align: "right",
      cell: (w) => (w.kind === "xpub" ? <span className="font-mono text-xs tabular">{t("admin.wallets.nextIndex", { n: formatCount(w.next_index, locale) })}</span> : "—"),
    },
    {
      id: "chains",
      header: t("admin.wallets.columns.chains"),
      className: "min-w-44 whitespace-normal",
      cell: (w) =>
        w.chain_ids.length ? (
          <div className="flex max-w-72 flex-wrap gap-1">
            {w.chain_ids.map((c) => (
              <span key={c} className="rounded-md bg-muted px-1.5 py-0.5 text-[11px]">
                {chainName(c)}
              </span>
            ))}
          </div>
        ) : (
          <span className="text-xs text-muted-foreground">{t("admin.wallets.notAssigned")}</span>
        ),
    },
    { id: "created", header: t("admin.wallets.columns.created"), cell: (w) => <RelativeTime iso={w.created_at} className="text-muted-foreground" /> },
    {
      id: "actions",
      header: <span className="sr-only">{t("admin.common.actions")}</span>,
      align: "right",
      cell: (w) => <RowMenu wallet={w} canWrite={canWrite} onRename={setRenaming} onDelete={setDeleting} />,
    },
  ];

  const createButton = (variant?: "outline") => (
    <GatedButton variant={variant} onClick={() => setCreateOpen(true)}>
      <PlusIcon data-icon="inline-start" aria-hidden />
      {t("admin.wallets.create")}
    </GatedButton>
  );

  return (
    <>
      <PageHeader title={t("admin.wallets.title")} description={t("admin.wallets.description")} actions={createButton()} />
      <DataTable
        caption={t("admin.wallets.title")}
        columns={columns}
        rows={wallets.data}
        getRowId={(w) => w.id}
        isLoading={wallets.isPending}
        error={wallets.error}
        onRetry={() => void wallets.refetch()}
        empty={<EmptyState icon={WalletIcon} title={t("admin.wallets.emptyTitle")} description={t("admin.wallets.emptyBody")} action={createButton("outline")} />}
        mobileCard={(w) => (
          <div className="space-y-1.5">
            <div className="flex items-center justify-between gap-2">
              <span className="font-medium">{w.name}</span>
              <RowMenu wallet={w} canWrite={canWrite} onRename={setRenaming} onDelete={setDeleting} />
            </div>
            <div className="flex flex-wrap items-center gap-1 text-xs">
              <span className="rounded border px-1 font-mono text-[10px] uppercase">{t(`admin.families.${w.family}` as MessageKey)}</span>
              <span className="text-muted-foreground">{t(`admin.walletKinds.${w.kind}` as MessageKey)}</span>
            </div>
            <MonoValue value={w.kind === "xpub" ? w.xpub : w.address} label={t("admin.wallets.columns.key")} short={[10, 8]} />
            <p className="text-xs text-muted-foreground">{w.chain_ids.length ? w.chain_ids.map(chainName).join(" · ") : t("admin.wallets.notAssigned")}</p>
          </div>
        )}
      />
      <CreateWalletDialog open={createOpen} onOpenChange={setCreateOpen} />
      <RenameWalletDialog wallet={renaming} onOpenChange={(o) => !o && setRenaming(null)} />
      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(o) => !o && setDeleting(null)}
        title={t("admin.wallets.deleteTitle", { name: deleting?.name ?? "" })}
        description={t("admin.wallets.deleteBody")}
        confirmLabel={t("admin.wallets.deleteConfirm")}
        destructive
        onConfirm={async () => {
          if (!deleting) return;
          try {
            await remove.mutateAsync(deleting);
          } catch (e) {
            // In use: close this dialog, the explanation dialog takes over.
            if (isApiError(e) && e.isConflict) return;
            throw e;
          }
        }}
      />
      <Dialog open={inUse !== null} onOpenChange={(o) => !o && setInUse(null)}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>{t("admin.wallets.inUseTitle")}</DialogTitle>
          </DialogHeader>
          <p className="text-sm text-pretty text-muted-foreground">{t("admin.wallets.inUse")}</p>
          <DialogFooter>
            <Button onClick={() => setInUse(null)}>{t("admin.common.done")}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}

function RowMenu({
  wallet,
  canWrite,
  onRename,
  onDelete,
}: {
  wallet: Wallet;
  canWrite: boolean;
  onRename: (w: Wallet) => void;
  onDelete: (w: Wallet) => void;
}) {
  const t = useT();
  return (
    <RoleTooltip blocked={!canWrite} reason={t("admin.access.readOnly")}>
      {/* Non-modal: the items open dialogs, which must own focus and pointer events. */}
      <DropdownMenu modal={false}>
        <DropdownMenuTrigger asChild>
          <Button variant="ghost" size="icon-sm" disabled={!canWrite} aria-label={`${t("admin.common.more")}: ${wallet.name}`}>
            <MoreHorizontalIcon aria-hidden />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <DropdownMenuItem onSelect={() => onRename(wallet)}>
            <PencilIcon aria-hidden />
            {t("admin.common.rename")}
          </DropdownMenuItem>
          <DropdownMenuItem variant="destructive" onSelect={() => onDelete(wallet)}>
            <TrashIcon aria-hidden />
            {t("admin.common.delete")}
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </RoleTooltip>
  );
}

function RenameWalletDialog({ wallet, onOpenChange }: { wallet: Wallet | null; onOpenChange: (o: boolean) => void }) {
  return (
    <Dialog open={wallet !== null} onOpenChange={onOpenChange}>
      {wallet && <RenameForm key={wallet.id} wallet={wallet} onDone={() => onOpenChange(false)} />}
    </Dialog>
  );
}

function RenameForm({ wallet, onDone }: { wallet: Wallet; onDone: () => void }) {
  const t = useT();
  const id = useId();
  const [name, setName] = useState(wallet.name);
  const [error, setError] = useState<string | null>(null);
  const rename = useAdminMutation({
    mutationFn: (n: string) => adminApi.updateWallet(wallet.id, { name: n }),
    invalidate: [adminKeys.wallets],
    successMessage: t("admin.wallets.renamed"),
    toastError: false,
    onSuccess: () => onDone(),
    onError: (e) => setError(isApiError(e) && e.fields?.name ? e.fields.name : describeError(t, e)),
  });
  return (
    <DialogContent className="sm:max-w-sm">
      <form
        noValidate
        className="grid gap-4"
        onSubmit={(e) => {
          e.preventDefault();
          if (!name.trim()) return setError(t("admin.wallets.nameRequired"));
          if (name.trim() === wallet.name) return onDone();
          rename.mutate(name.trim());
        }}
      >
        <DialogHeader>
          <DialogTitle>{t("admin.wallets.renameTitle")}</DialogTitle>
        </DialogHeader>
        <FormField label={t("admin.wallets.name")} htmlFor={id} error={error ?? undefined}>
          <Input
            id={id}
            value={name}
            onChange={(e) => setName(e.target.value)}
            maxLength={100}
            autoFocus
            aria-invalid={!!error}
            aria-describedby={describedBy(id, { error })}
          />
        </FormField>
        <DialogFooter>
          <Button type="button" variant="outline" onClick={onDone}>
            {t("admin.common.cancel")}
          </Button>
          <Button type="submit" disabled={rename.isPending}>
            {rename.isPending && <Spinner data-icon="inline-start" />}
            {t("admin.common.save")}
          </Button>
        </DialogFooter>
      </form>
    </DialogContent>
  );
}
