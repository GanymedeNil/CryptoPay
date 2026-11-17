"use client";

import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { OVERVIEW_RANGES } from "@/lib/admin/merchant-overview";
import { useT } from "@/lib/i18n";

/** 7 / 30 / 90 days. */
export function RangeToggle({ value, onChange }: { value: number; onChange: (days: number) => void }) {
  const t = useT();
  return (
    <ToggleGroup
      type="single"
      variant="outline"
      size="sm"
      spacing={0}
      value={String(value)}
      onValueChange={(v) => v && onChange(Number(v))}
      aria-label={t("admin.overview.range")}
    >
      {OVERVIEW_RANGES.map((d) => (
        <ToggleGroupItem key={d} value={String(d)} className="px-3 tabular" aria-label={t("admin.overview.lastDays", { days: d })}>
          {t("admin.overview.days", { days: d })}
        </ToggleGroupItem>
      ))}
    </ToggleGroup>
  );
}
