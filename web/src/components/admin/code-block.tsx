"use client";

import { cn } from "@/lib/utils";
import { CopyButton } from "./copy-button";

/** Read-only code sample with a copy button (no highlighting: samples are short). */
export function CodeBlock({ code, label, className }: { code: string; label: string; className?: string }) {
  return (
    <div className={cn("relative min-w-0 rounded-lg border bg-muted/40", className)}>
      <div className="absolute top-1.5 right-1.5 rounded-md bg-card/80 backdrop-blur">
        <CopyButton value={code} label={label} size="icon-sm" toast />
      </div>
      <pre
        tabIndex={0}
        aria-label={label}
        className="overflow-x-auto p-3 pr-11 font-mono text-xs leading-relaxed outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
      >
        <code>{code}</code>
      </pre>
    </div>
  );
}
