import { Suspense } from "react";
import { InvoiceDetailPage } from "@/components/admin/invoices/invoice-detail-page";
import { PageSkeleton } from "@/components/admin/states";
import { idTitleMetadata } from "@/lib/admin/metadata";

/** Client-authenticated: the page renders after `GET /auth/me` in the browser. */
export const instant = false;
export const generateMetadata = idTitleMetadata("admin.invoice.title");

async function InvoiceRoute({ params }: { params: PageProps<"/admin/invoices/[id]">["params"] }) {
  const { id } = await params;
  return <InvoiceDetailPage id={id} />;
}

export default function AdminInvoiceDetailRoute({ params }: PageProps<"/admin/invoices/[id]">) {
  return (
    <Suspense fallback={<PageSkeleton />}>
      <InvoiceRoute params={params} />
    </Suspense>
  );
}
