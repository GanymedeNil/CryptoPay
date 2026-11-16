import type { Metadata } from "next";
import { AdminQueryProvider } from "@/lib/admin/query-client";
import { getServerT } from "@/lib/i18n/server";

export async function generateMetadata(): Promise<Metadata> {
  const t = await getServerT();
  const consoleName = t("admin.meta.title");
  return {
    title: { template: `%s · ${consoleName} · CryptoPay`, absolute: `${consoleName} · CryptoPay` },
    robots: { index: false, follow: false },
  };
}

/** Every /admin route shares one client-side query cache. */
export default function AdminLayout({ children }: { children: React.ReactNode }) {
  return <AdminQueryProvider>{children}</AdminQueryProvider>;
}
