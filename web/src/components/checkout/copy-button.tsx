"use client";

import { CheckIcon, CopyIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { useCopy } from "@/hooks/use-copy";
import { useT } from "@/lib/i18n";
import { cn } from "@/lib/utils";

/**
 * Copy button with visible feedback. `label` names what is copied, for the
 * accessible name ("Copy amount") and the confirmation toast.
 */
export function CopyButton({
  value,
  label,
  copyLabel,
  showText = false,
  className,
  size = "sm",
  variant = "outline",
}: {
  value: string;
  /** What is copied, e.g. "Amount". */
  label: string;
  /** Accessible name / button text, e.g. "Copy amount". */
  copyLabel: string;
  showText?: boolean;
  className?: string;
  size?: "sm" | "default" | "icon" | "icon-sm";
  variant?: "outline" | "ghost" | "secondary" | "default";
}) {
  const t = useT();
  const { copied, copy } = useCopy();
  return (
    <Button
      type="button"
      variant={variant}
      size={size}
      className={cn("shrink-0", className)}
      aria-label={showText ? undefined : copyLabel}
      onClick={() => void copy(value, label)}
    >
      {copied ? <CheckIcon className="text-success" aria-hidden /> : <CopyIcon aria-hidden />}
      {showText && <span>{copied ? t("common.copied") : copyLabel}</span>}
    </Button>
  );
}
