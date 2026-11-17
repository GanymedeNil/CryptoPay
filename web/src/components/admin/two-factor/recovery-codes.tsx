"use client";

import { CheckIcon, CopyIcon, DownloadIcon, TriangleAlertIcon } from "lucide-react";
import { useId, useState } from "react";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Label } from "@/components/ui/label";
import { useCopy } from "@/hooks/use-copy";
import { recoveryCodesFileName, recoveryCodesText } from "@/lib/admin/two-factor";
import { useT } from "@/lib/i18n";

/** Offers `text` as a file download (no server round trip; nothing is stored). */
function downloadText(fileName: string, text: string) {
  const blob = new Blob([text], { type: "text/plain;charset=utf-8" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = fileName;
  a.rel = "noopener";
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 0);
}

/**
 * Freshly generated recovery codes, shown exactly once: copy all, download as
 * .txt, and "Done" only after "I stored them" is ticked. The codes live only
 * in the caller's state; `onDone` should drop them.
 */
export function RecoveryCodesPanel({ codes, email, onDone }: { codes: string[]; email: string; onDone: () => void }) {
  const t = useT();
  const { copied, copy } = useCopy(2500);
  const [ack, setAck] = useState(false);
  const ackId = useId();
  const listId = useId();
  const [generatedAt] = useState(() => new Date());
  const text = recoveryCodesText(t, { email, codes, generatedAt });
  return (
    <div className="space-y-4">
      <p className="text-sm text-muted-foreground">{t("admin.twoFactor.recovery.description")}</p>
      <div className="flex gap-2.5 rounded-lg border border-warning-border bg-warning-surface p-3 text-sm text-warning" role="note">
        <TriangleAlertIcon className="mt-0.5 size-4 shrink-0" aria-hidden />
        <p>{t("admin.twoFactor.recovery.warning")}</p>
      </div>
      <div className="space-y-2">
        <p id={listId} className="text-xs text-muted-foreground">
          {t("admin.twoFactor.recovery.listLabel")}
        </p>
        <ol
          aria-labelledby={listId}
          data-testid="recovery-codes"
          className="grid grid-cols-2 gap-x-4 gap-y-1.5 rounded-lg border bg-muted/50 px-4 py-3 font-mono text-sm tabular select-all"
        >
          {codes.map((c) => (
            <li key={c}>{c}</li>
          ))}
        </ol>
        <div className="flex flex-wrap gap-2">
          <Button type="button" variant="outline" size="sm" onClick={() => void copy(codes.join("\n"))}>
            {copied ? <CheckIcon className="text-success" data-icon="inline-start" aria-hidden /> : <CopyIcon data-icon="inline-start" aria-hidden />}
            {copied ? t("admin.twoFactor.recovery.copied") : t("admin.twoFactor.recovery.copyAll")}
          </Button>
          <Button type="button" variant="outline" size="sm" onClick={() => downloadText(recoveryCodesFileName(email), text)}>
            <DownloadIcon data-icon="inline-start" aria-hidden />
            {t("admin.twoFactor.recovery.download")}
          </Button>
        </div>
      </div>
      <div className="flex items-start gap-2.5">
        <Checkbox id={ackId} checked={ack} onCheckedChange={(v) => setAck(v === true)} className="mt-0.5" />
        <Label htmlFor={ackId} className="text-sm leading-snug font-normal">
          {t("admin.twoFactor.recovery.ack")}
        </Label>
      </div>
      <div className="flex justify-end">
        <Button type="button" disabled={!ack} onClick={onDone} title={ack ? undefined : t("admin.twoFactor.recovery.mustAcknowledge")}>
          {t("admin.twoFactor.recovery.done")}
        </Button>
      </div>
    </div>
  );
}
