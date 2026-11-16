"use client";

import { LanguagesIcon } from "lucide-react";
import { useRouter } from "next/navigation";
import { startTransition } from "react";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { isLocale, LOCALE_LABELS, LOCALES, useLocale, useT } from "@/lib/i18n";

export function LocaleSwitcher() {
  const t = useT();
  const { locale, setLocale } = useLocale();
  const router = useRouter();
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="sm" aria-label={t("common.language")} className="gap-1.5">
          <LanguagesIcon aria-hidden />
          <span className="text-xs font-medium">{locale === "zh" ? "中文" : "EN"}</span>
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="min-w-32">
        <DropdownMenuLabel>{t("common.language")}</DropdownMenuLabel>
        <DropdownMenuRadioGroup
          value={locale}
          onValueChange={(v) => {
            if (!isLocale(v) || v === locale) return;
            setLocale(v);
            // Server components render with the cookie: re-render them too.
            startTransition(() => router.refresh());
          }}
        >
          {LOCALES.map((l) => (
            <DropdownMenuRadioItem key={l} value={l} lang={l === "zh" ? "zh-CN" : "en"}>
              {LOCALE_LABELS[l]}
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
