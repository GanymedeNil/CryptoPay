"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { toast } from "sonner";
import { copyText } from "@/lib/clipboard";
import { useT } from "@/lib/i18n";

/** Copy with a transient "copied" state and a toast on failure. */
export function useCopy(resetMs = 1800) {
  const t = useT();
  const [copied, setCopied] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => () => {
    if (timer.current) clearTimeout(timer.current);
  }, []);

  const copy = useCallback(
    async (text: string, label?: string) => {
      const ok = await copyText(text);
      if (!ok) {
        toast.error(t("common.copyFailed"));
        return false;
      }
      setCopied(true);
      if (label) toast.success(t("common.copiedValue", { label }), { duration: 1600 });
      if (timer.current) clearTimeout(timer.current);
      timer.current = setTimeout(() => setCopied(false), resetMs);
      return true;
    },
    [resetMs, t],
  );

  return { copied, copy };
}
