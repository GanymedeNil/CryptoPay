"use client";

import { Fragment, useMemo } from "react";
import { useT } from "@/lib/i18n";
import { cn } from "@/lib/utils";
import { CopyButton } from "./copy-button";

type Token = { kind: "key" | "string" | "number" | "literal" | "punct" | "space"; text: string };

const TOKEN_RE = /("(?:\\.|[^"\\])*")(\s*:)?|(-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?)|\b(true|false|null)\b|([{}[\],:])|(\s+)/g;

/** Splits pretty-printed JSON into coloured tokens (no HTML injection: React escapes text). */
export function tokenizeJson(json: string): Token[] {
  const out: Token[] = [];
  let m: RegExpExecArray | null;
  let last = 0;
  TOKEN_RE.lastIndex = 0;
  while ((m = TOKEN_RE.exec(json))) {
    if (m.index > last) out.push({ kind: "punct", text: json.slice(last, m.index) });
    if (m[1]) {
      out.push({ kind: m[2] ? "key" : "string", text: m[1] });
      if (m[2]) out.push({ kind: "punct", text: m[2] });
    } else if (m[3]) out.push({ kind: "number", text: m[3] });
    else if (m[4]) out.push({ kind: "literal", text: m[4] });
    else if (m[5]) out.push({ kind: "punct", text: m[5] });
    else out.push({ kind: "space", text: m[6] ?? m[0] });
    last = TOKEN_RE.lastIndex;
  }
  if (last < json.length) out.push({ kind: "punct", text: json.slice(last) });
  return out;
}

export function stringifyJson(value: unknown): string {
  if (typeof value === "string") {
    // Payloads / responses can arrive as JSON text: pretty-print when they parse.
    try {
      return JSON.stringify(JSON.parse(value), null, 2);
    } catch {
      return value;
    }
  }
  try {
    return JSON.stringify(value, null, 2) ?? String(value);
  } catch {
    return String(value);
  }
}

const KIND_CLASS: Record<Token["kind"], string> = {
  key: "text-primary",
  string: "text-[color-mix(in_oklch,var(--chart-4),var(--foreground)_30%)]",
  number: "text-[color-mix(in_oklch,var(--chart-5),var(--foreground)_20%)]",
  literal: "text-destructive/90",
  punct: "text-muted-foreground",
  space: "",
};

/** Read-only, syntax-coloured JSON with a copy button. */
export function JsonViewer({
  value,
  className,
  maxHeight = "20rem",
  label,
}: {
  value: unknown;
  className?: string;
  maxHeight?: string;
  /** Accessible name of the region and the copy button label. */
  label?: string;
}) {
  const t = useT();
  const text = useMemo(() => stringifyJson(value), [value]);
  const tokens = useMemo(() => tokenizeJson(text), [text]);
  const empty = value === null || value === undefined || text === "{}" || text === "";
  return (
    <div className={cn("group/json relative min-w-0 rounded-lg border bg-muted/40", className)}>
      {!empty && (
        <div className="absolute top-1.5 right-1.5 z-10 rounded-md bg-card/80 backdrop-blur">
          <CopyButton value={text} label={label ?? "JSON"} size="icon-sm" />
        </div>
      )}
      <pre
        tabIndex={0}
        aria-label={label}
        className="overflow-auto p-3 pr-10 font-mono text-xs leading-relaxed outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
        style={{ maxHeight }}
      >
        {empty ? (
          <span className="text-muted-foreground">{text === "{}" ? "{}" : t("admin.json.empty")}</span>
        ) : (
          tokens.map((tok, i) => (
            <Fragment key={i}>
              {tok.kind === "space" ? tok.text : <span className={KIND_CLASS[tok.kind]}>{tok.text}</span>}
            </Fragment>
          ))
        )}
      </pre>
    </div>
  );
}
