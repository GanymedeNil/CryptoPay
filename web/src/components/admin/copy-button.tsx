"use client";

import { CheckIcon, CopyIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { useCopy } from "@/hooks/use-copy";
import { useT } from "@/lib/i18n";
import { cn } from "@/lib/utils";

/**
 * Icon button that copies `value`. `label` names what is copied ("address",
 * "API key") for the accessible name and the confirmation toast.
 */
export function CopyButton({
  value,
  label,
  className,
  size = "icon-xs",
  toast = false,
}: {
  value: string;
  label: string;
  className?: string;
  size?: "icon-xs" | "icon-sm" | "icon";
  /** Also toast "<label> copied" (default: only the icon changes). */
  toast?: boolean;
}) {
  const t = useT();
  const { copied, copy } = useCopy();
  const name = t("admin.common.copyValue", { label });
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Button
          type="button"
          variant="ghost"
          size={size}
          className={cn("shrink-0 text-muted-foreground hover:text-foreground", className)}
          aria-label={name}
          onClick={(e) => {
            e.stopPropagation();
            void copy(value, toast ? label : undefined);
          }}
        >
          {copied ? <CheckIcon className="text-success" aria-hidden /> : <CopyIcon aria-hidden />}
        </Button>
      </TooltipTrigger>
      <TooltipContent>{copied ? t("common.copied") : name}</TooltipContent>
    </Tooltip>
  );
}
