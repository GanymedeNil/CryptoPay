import { Suspense } from "react";
import { InvoicesPage } from "@/components/admin/invoices/invoices-page";
import { PageSkeleton } from "@/components/admin/states";
import { titleMetadata } from "@/lib/admin/metadata";

/** Client-authenticated: the page renders after `GET /auth/me` in the browser. */
export const instant = false;
export const generateMetadata = titleMetadata("admin.nav.invoices");

export default function AdminInvoicesRoute() {
  return (
    <Suspense fallback={<PageSkeleton />}>
      <InvoicesPage />
    </Suspense>
  );
}
