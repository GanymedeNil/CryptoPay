"use client";

import { ExternalLinkIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { shortenAddress } from "@/lib/format/address";
import { useT } from "@/lib/i18n";
import { cn } from "@/lib/utils";
import { CopyButton } from "./copy-button";

/**
 * An address, hash or id in the data font: optionally shortened (full value
 * in the tooltip and the copy button), with a copy button and an explorer
 * link.
 */
export function MonoValue({
  value,
  label,
  short,
  href,
  copy = true,
  className,
  wrap = false,
}: {
  value: string;
  /** What it is ("address", "tx hash") — accessible names and toasts. */
  label: string;
  /** [head, tail] characters to keep, or true for the default 6…4. */
  short?: boolean | [number, number];
  /** Explorer (or other external) link. */
  href?: string | null;
  copy?: boolean;
  className?: string;
  /** Wrap long values instead of truncating (detail views). */
  wrap?: boolean;
}) {
  const t = useT();
  if (!value) return <span className="text-muted-foreground">{t("admin.common.empty")}</span>;
  const display = short
    ? Array.isArray(short)
      ? shortenAddress(value, short[0], short[1])
      : shortenAddress(value, 6, 4)
    : value;
  const text = (
    <span
      className={cn(
        "font-mono text-[0.8125rem] tabular",
        wrap ? "break-anywhere" : "truncate",
        display !== value && "cursor-default",
      )}
    >
      {display}
    </span>
  );
  return (
    <span className={cn("inline-flex max-w-full min-w-0 items-center gap-0.5 align-middle", className)}>
      {display !== value ? (
        <Tooltip>
          <TooltipTrigger asChild>
            <span tabIndex={0} className="min-w-0 truncate rounded-sm outline-none focus-visible:ring-2 focus-visible:ring-ring/50">
              {text}
            </span>
          </TooltipTrigger>
          <TooltipContent className="max-w-sm font-mono break-all">{value}</TooltipContent>
        </Tooltip>
      ) : (
        text
      )}
      {copy && <CopyButton value={value} label={label} />}
      {href && (
        <Tooltip>
          <TooltipTrigger asChild>
            <Button asChild variant="ghost" size="icon-xs" className="shrink-0 text-muted-foreground hover:text-foreground">
              <a
                href={href}
                target="_blank"
                rel="noopener noreferrer"
                referrerPolicy="no-referrer"
                aria-label={`${t("admin.common.viewOnExplorer")} ${t("common.externalLink")}`}
                onClick={(e) => e.stopPropagation()}
              >
                <ExternalLinkIcon aria-hidden />
              </a>
            </Button>
          </TooltipTrigger>
          <TooltipContent>{t("admin.common.viewOnExplorer")}</TooltipContent>
        </Tooltip>
      )}
    </span>
  );
}
