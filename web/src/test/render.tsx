import { render, type RenderOptions } from "@testing-library/react";
import { TooltipProvider } from "@/components/ui/tooltip";
import type { Locale } from "@/lib/i18n/config";
import { I18nProvider } from "@/lib/i18n/provider";

/** Renders inside the same providers as the app (i18n + tooltip). */
export function renderWithProviders(
  ui: React.ReactElement,
  { locale = "en", ...options }: RenderOptions & { locale?: Locale } = {},
) {
  return render(
    <I18nProvider initialLocale={locale}>
      <TooltipProvider>{ui}</TooltipProvider>
    </I18nProvider>,
    options,
  );
}
