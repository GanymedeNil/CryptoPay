import type { Metadata, Viewport } from "next";
import { Bricolage_Grotesque, IBM_Plex_Mono, Public_Sans } from "next/font/google";
import { Suspense } from "react";
import { ThemeProviders, UiProviders } from "@/components/providers";
import { I18nProvider } from "@/lib/i18n/provider";
import { getRequestLocale } from "@/lib/i18n/server";
import "./globals.css";

const display = Bricolage_Grotesque({
  variable: "--font-display",
  subsets: ["latin"],
  display: "swap",
});

const body = Public_Sans({
  variable: "--font-body",
  subsets: ["latin"],
  display: "swap",
});

const data = IBM_Plex_Mono({
  variable: "--font-data",
  subsets: ["latin"],
  weight: ["400", "500", "600"],
  display: "swap",
});

export const metadata: Metadata = {
  title: { default: "CryptoPay", template: "%s · CryptoPay" },
  description: "Self-hosted, non-custodial crypto payment gateway.",
  applicationName: "CryptoPay",
  formatDetection: { telephone: false, address: false, email: false },
};

export const viewport: Viewport = {
  width: "device-width",
  initialScale: 1,
  viewportFit: "cover",
  themeColor: [
    { media: "(prefers-color-scheme: light)", color: "#eef2ef" },
    { media: "(prefers-color-scheme: dark)", color: "#0a1210" },
  ],
};

/** Reads the locale cookie, so it renders inside the root <Suspense>. */
async function LocalizedProviders({ children }: { children: React.ReactNode }) {
  const locale = await getRequestLocale();
  return (
    <I18nProvider initialLocale={locale}>
      <UiProviders>{children}</UiProviders>
    </I18nProvider>
  );
}

export default function RootLayout({ children }: LayoutProps<"/">) {
  return (
    <html
      lang="zh-CN"
      suppressHydrationWarning
      className={`${display.variable} ${body.variable} ${data.variable} h-full antialiased`}
    >
      <body className="flex min-h-full flex-col">
        <ThemeProviders>
          <Suspense fallback={null}>
            <LocalizedProviders>{children}</LocalizedProviders>
          </Suspense>
        </ThemeProviders>
      </body>
    </html>
  );
}
