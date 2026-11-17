"use client";

import { CheckIcon, ChevronsUpDownIcon, SearchIcon, StoreIcon } from "lucide-react";
import { useId, useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import type { Merchant } from "@/lib/api/types";
import { useT } from "@/lib/i18n";
import { cn } from "@/lib/utils";

/** Above this many merchants the list gets a search box. */
export const SEARCH_THRESHOLD = 8;

/**
 * "All merchants" + one option per merchant. `value` "" = all. `label` names
 * a selected merchant that is not in `merchants` (yet), e.g. beyond the first
 * page of the merchant list.
 */
export function MerchantPicker({
  value,
  onChange,
  merchants,
  label,
  className,
}: {
  value: string;
  onChange: (merchantId: string) => void;
  merchants: Merchant[] | undefined;
  label?: string;
  className?: string;
}) {
  const t = useT();
  const listId = useId();
  const labelId = useId();
  const valueId = useId();
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const all = merchants ?? [];
  const searchable = all.length > SEARCH_THRESHOLD;
  const needle = query.trim().toLowerCase();
  const shown = needle ? all.filter((m) => m.name.toLowerCase().includes(needle) || m.id === needle) : all;
  const current = value ? (all.find((m) => m.id === value)?.name ?? label ?? value) : t("admin.overview.merchantFilter.all");

  const choose = (id: string) => {
    setOpen(false);
    setQuery("");
    if (id !== value) onChange(id);
  };
  const focusOption = (from: HTMLElement, step: number) => {
    const list = document.getElementById(listId);
    const options = Array.from(list?.querySelectorAll<HTMLElement>("[role=option]") ?? []);
    if (!options.length) return;
    const i = options.indexOf(from);
    options[i < 0 ? (step > 0 ? 0 : options.length - 1) : Math.min(options.length - 1, Math.max(0, i + step))].focus();
  };

  return (
    <Popover
      open={open}
      onOpenChange={(o) => {
        setOpen(o);
        if (!o) setQuery("");
      }}
    >
      <PopoverTrigger asChild>
        <Button
          variant="outline"
          size="sm"
          role="combobox"
          aria-expanded={open}
          aria-controls={open ? listId : undefined}
          aria-labelledby={`${labelId} ${valueId}`}
          className={cn("max-w-64 min-w-0 justify-between gap-2 font-normal", className)}
        >
          <StoreIcon data-icon="inline-start" aria-hidden />
          <span id={labelId} className="sr-only">
            {t("admin.overview.merchantFilter.label")}:
          </span>
          <span id={valueId} className={cn("min-w-0 flex-1 truncate text-left", value && "font-medium")}>
            {current}
          </span>
          <ChevronsUpDownIcon className="text-muted-foreground" aria-hidden />
        </Button>
      </PopoverTrigger>
      <PopoverContent align="end" className="w-72 gap-0 p-0">
        {searchable && (
          <div className="relative border-b p-1.5">
            <SearchIcon className="pointer-events-none absolute top-1/2 left-4 size-4 -translate-y-1/2 text-muted-foreground" aria-hidden />
            <Input
              type="search"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "ArrowDown") {
                  e.preventDefault();
                  focusOption(e.currentTarget, 1);
                } else if (e.key === "Enter" && needle && shown.length > 0) {
                  e.preventDefault();
                  choose(shown[0].id);
                }
              }}
              placeholder={t("admin.overview.merchantFilter.search")}
              aria-label={t("admin.overview.merchantFilter.search")}
              aria-controls={listId}
              className="h-8 border-0 pl-8 shadow-none focus-visible:ring-0"
              autoComplete="off"
              spellCheck={false}
              autoFocus
            />
          </div>
        )}
        <div
          id={listId}
          role="listbox"
          aria-label={t("admin.overview.merchantFilter.label")}
          className="max-h-72 overflow-y-auto p-1"
          onKeyDown={(e) => {
            if (e.key !== "ArrowDown" && e.key !== "ArrowUp") return;
            e.preventDefault();
            focusOption(e.target as HTMLElement, e.key === "ArrowDown" ? 1 : -1);
          }}
        >
          {!needle && <Option selected={!value} onSelect={() => choose("")} label={t("admin.overview.merchantFilter.all")} />}
          {shown.map((m) => (
            <Option
              key={m.id}
              selected={m.id === value}
              onSelect={() => choose(m.id)}
              label={m.name}
              note={m.status === "disabled" ? t("admin.status.merchant.disabled") : undefined}
            />
          ))}
          {needle && shown.length === 0 && (
            <p className="px-2 py-3 text-center text-sm text-muted-foreground" role="status">
              {t("admin.overview.merchantFilter.noMatch")}
            </p>
          )}
        </div>
      </PopoverContent>
    </Popover>
  );
}

function Option({ selected, onSelect, label, note }: { selected: boolean; onSelect: () => void; label: string; note?: string }) {
  return (
    <button
      type="button"
      role="option"
      aria-selected={selected}
      onClick={onSelect}
      className={cn(
        "flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm outline-none hover:bg-muted focus-visible:bg-muted focus-visible:ring-2 focus-visible:ring-ring/50",
        selected && "font-medium",
      )}
    >
      <CheckIcon className={cn("size-4 shrink-0", selected ? "text-primary" : "invisible")} aria-hidden />
      <span className="min-w-0 flex-1 truncate">{label}</span>
      {note && <span className="shrink-0 text-xs text-muted-foreground">{note}</span>}
    </button>
  );
}
