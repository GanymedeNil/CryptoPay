"use client";

import { SearchIcon, XIcon } from "lucide-react";
import { useEffect, useId, useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { useT } from "@/lib/i18n";
import { cn } from "@/lib/utils";

/** Row of filters above a list; wraps on small screens. */
export function FilterBar({
  children,
  onClear,
  active,
  className,
}: {
  children: React.ReactNode;
  /** Shows "Clear filters" while `active`. */
  onClear?: () => void;
  active?: boolean;
  className?: string;
}) {
  const t = useT();
  return (
    <div
      role="search"
      className={cn("mb-3 flex flex-col gap-2 sm:flex-row sm:flex-wrap sm:items-end", className)}
    >
      {children}
      {onClear && active && (
        <Button variant="ghost" size="sm" onClick={onClear} className="h-8 self-start text-muted-foreground sm:self-auto">
          <XIcon data-icon="inline-start" aria-hidden />
          {t("admin.common.clearFilters")}
        </Button>
      )}
    </div>
  );
}

/** Labelled filter control (label visible above on desktop, sr-only on phones is not needed: keep it visible). */
export function FilterField({ label, htmlFor, children, className }: { label: string; htmlFor?: string; children: React.ReactNode; className?: string }) {
  return (
    <div className={cn("flex min-w-0 flex-col gap-1", className)}>
      <Label htmlFor={htmlFor} className="text-xs font-normal text-muted-foreground">
        {label}
      </Label>
      {children}
    </div>
  );
}

/**
 * Search box that commits after a pause (or on Enter). The committed value
 * lives in the URL; this keeps the in-progress text locally and follows
 * external changes (e.g. "clear filters").
 */
export function SearchFilter({
  value,
  onCommit,
  label,
  placeholder,
  className,
  delay = 350,
  mono = false,
}: {
  value: string;
  onCommit: (value: string) => void;
  label: string;
  placeholder?: string;
  className?: string;
  delay?: number;
  mono?: boolean;
}) {
  const t = useT();
  const id = useId();
  const [text, setText] = useState(value);
  const [external, setExternal] = useState(value);
  if (external !== value) {
    setExternal(value);
    if (text.trim() !== value) setText(value);
  }
  const commitRef = useRef(onCommit);
  useEffect(() => {
    commitRef.current = onCommit;
  });
  useEffect(() => {
    if (text.trim() === value) return;
    const h = setTimeout(() => commitRef.current(text.trim()), delay);
    return () => clearTimeout(h);
  }, [text, value, delay]);

  return (
    <FilterField label={label} htmlFor={id} className={cn("sm:w-72", className)}>
      <div className="relative">
        <SearchIcon className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" aria-hidden />
        <Input
          id={id}
          type="search"
          value={text}
          placeholder={placeholder}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") onCommit(text.trim());
          }}
          className={cn("bg-card pr-8 pl-8", mono && "font-mono text-[13px]")}
          autoComplete="off"
          spellCheck={false}
        />
        {text && (
          <Button
            type="button"
            variant="ghost"
            size="icon-xs"
            className="absolute top-1/2 right-1 -translate-y-1/2 text-muted-foreground"
            aria-label={t("admin.common.clearFilters")}
            onClick={() => {
              setText("");
              onCommit("");
            }}
          >
            <XIcon aria-hidden />
          </Button>
        )}
      </div>
    </FilterField>
  );
}

const ALL = "__all__";

/** Single-value select with an "all" option (empty string = no filter). */
export function SelectFilter({
  value,
  onChange,
  label,
  allLabel,
  options,
  className,
}: {
  value: string;
  onChange: (value: string) => void;
  label: string;
  allLabel: string;
  options: { value: string; label: React.ReactNode }[];
  className?: string;
}) {
  const id = useId();
  return (
    <FilterField label={label} htmlFor={id} className={cn("sm:w-44", className)}>
      <Select value={value || ALL} onValueChange={(v) => onChange(v === ALL ? "" : v)}>
        <SelectTrigger id={id} className="w-full bg-card">
          <SelectValue />
        </SelectTrigger>
        <SelectContent position="popper" align="start">
          <SelectItem value={ALL}>{allLabel}</SelectItem>
          {options.map((o) => (
            <SelectItem key={o.value} value={o.value}>
              {o.label}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </FilterField>
  );
}

/** Two native date inputs (local calendar days). */
export function DateRangeFilter({
  from,
  to,
  onChange,
  fromLabel,
  toLabel,
}: {
  from: string;
  to: string;
  onChange: (range: { from: string; to: string }) => void;
  fromLabel: string;
  toLabel: string;
}) {
  const fromId = useId();
  const toId = useId();
  return (
    <div className="flex items-end gap-2">
      <FilterField label={fromLabel} htmlFor={fromId} className="flex-1 sm:w-38 sm:flex-none">
        <Input
          id={fromId}
          type="date"
          value={from}
          max={to || undefined}
          onChange={(e) => onChange({ from: e.target.value, to })}
          className="bg-card tabular"
        />
      </FilterField>
      <FilterField label={toLabel} htmlFor={toId} className="flex-1 sm:w-38 sm:flex-none">
        <Input
          id={toId}
          type="date"
          value={to}
          min={from || undefined}
          onChange={(e) => onChange({ from, to: e.target.value })}
          className="bg-card tabular"
        />
      </FilterField>
    </div>
  );
}
