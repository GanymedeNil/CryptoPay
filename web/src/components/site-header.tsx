"use client";

import { BrandMark } from "@/components/brand/brand-mark";
import { LocaleSwitcher } from "@/components/locale-switcher";
import { ThemeToggle } from "@/components/theme-toggle";
import { cn } from "@/lib/utils";

/** Thin top bar: brand on the left, language + theme on the right. */
export function SiteHeader({ className, children }: { className?: string; children?: React.ReactNode }) {
  return (
    <header
      className={cn(
        "mx-auto flex w-full items-center justify-between gap-3 px-4 pt-[max(0.75rem,env(safe-area-inset-top))] pb-3",
        className,
      )}
    >
      <div className="min-w-0">{children ?? <BrandMark />}</div>
      <div className="flex shrink-0 items-center gap-0.5">
        <LocaleSwitcher />
        <ThemeToggle />
      </div>
    </header>
  );
}
