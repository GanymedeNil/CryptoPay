"use client";

import { CheckIcon, CopyIcon, TriangleAlertIcon } from "lucide-react";
import { useId, useState } from "react";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Label } from "@/components/ui/label";
import { useCopy } from "@/hooks/use-copy";
import { useT } from "@/lib/i18n";

/**
 * Shows a secret exactly once (webhook secret, API key). The operator must
 * tick "I have stored it" before the dialog can be closed — Escape, outside
 * clicks and the close button are disabled until then. The secret is held
 * only in the parent's state; closing calls `onClose` which should drop it.
 */
export function SecretRevealDialog({
  secret,
  title,
  description,
  label,
  onClose,
  children,
}: {
  /** null = closed. */
  secret: string | null;
  title: string;
  description?: React.ReactNode;
  /** What the secret is ("API key"), for the copy button. */
  label: string;
  onClose: () => void;
  /** Extra content below the secret (e.g. usage hints). */
  children?: React.ReactNode;
}) {
  const open = secret !== null;
  return (
    <Dialog open={open}>
      {open && (
        <SecretBody secret={secret} title={title} description={description} label={label} onClose={onClose}>
          {children}
        </SecretBody>
      )}
    </Dialog>
  );
}

function SecretBody({
  secret,
  title,
  description,
  label,
  onClose,
  children,
}: {
  secret: string;
  title: string;
  description?: React.ReactNode;
  label: string;
  onClose: () => void;
  children?: React.ReactNode;
}) {
  const t = useT();
  const { copied, copy } = useCopy(2500);
  const [ack, setAck] = useState(false);
  const ackId = useId();
  const secretId = useId();
  return (
    <DialogContent
      showCloseButton={false}
      className="sm:max-w-lg"
      onEscapeKeyDown={(e) => {
        if (!ack) e.preventDefault();
        else onClose();
      }}
      onPointerDownOutside={(e) => e.preventDefault()}
      onInteractOutside={(e) => e.preventDefault()}
    >
      <DialogHeader>
        <DialogTitle>{title}</DialogTitle>
        {description && <DialogDescription>{description}</DialogDescription>}
      </DialogHeader>

      <div className="flex gap-2.5 rounded-lg border border-warning-border bg-warning-surface p-3 text-sm text-warning">
        <TriangleAlertIcon className="mt-0.5 size-4 shrink-0" aria-hidden />
        <p>{t("admin.secret.onceWarning")}</p>
      </div>

      <div className="space-y-1.5">
        <Label htmlFor={secretId} className="text-xs text-muted-foreground">
          {label}
        </Label>
        <div className="flex items-stretch gap-2">
          <output
            id={secretId}
            className="min-w-0 flex-1 rounded-lg border bg-muted/60 px-3 py-2 font-mono text-[13px] leading-relaxed break-all select-all"
            data-testid="secret-value"
          >
            {secret}
          </output>
          <Button
            type="button"
            variant="outline"
            className="h-auto shrink-0"
            onClick={() => void copy(secret, label)}
            aria-label={t("admin.common.copyValue", { label })}
          >
            {copied ? <CheckIcon className="text-success" aria-hidden /> : <CopyIcon aria-hidden />}
            <span className="hidden sm:inline">{copied ? t("admin.secret.copied") : t("admin.secret.copy")}</span>
          </Button>
        </div>
      </div>

      {children}

      <div className="flex items-start gap-2.5">
        <Checkbox id={ackId} checked={ack} onCheckedChange={(v) => setAck(v === true)} className="mt-0.5" />
        <Label htmlFor={ackId} className="text-sm leading-snug font-normal">
          {t("admin.secret.acknowledge")}
        </Label>
      </div>

      <DialogFooter>
        <Button type="button" disabled={!ack} onClick={onClose} title={ack ? undefined : t("admin.secret.mustAcknowledge")}>
          {t("admin.secret.done")}
        </Button>
      </DialogFooter>
    </DialogContent>
  );
}
