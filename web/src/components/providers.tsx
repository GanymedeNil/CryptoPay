"use client";

import { ThemeProvider } from "next-themes";
import { Toaster } from "@/components/ui/sonner";
import { TooltipProvider } from "@/components/ui/tooltip";

/** Theme (light / dark / system via the `class` attribute) — no request data. */
export function ThemeProviders({ children }: { children: React.ReactNode }) {
  return (
    <ThemeProvider attribute="class" defaultTheme="system" enableSystem disableTransitionOnChange>
      {children}
    </ThemeProvider>
  );
}

/** UI-wide providers that live below the i18n provider. */
export function UiProviders({ children }: { children: React.ReactNode }) {
  return (
    <TooltipProvider delayDuration={300}>
      {children}
      <Toaster position="top-center" closeButton={false} />
    </TooltipProvider>
  );
}
