import { Suspense } from "react";
import { MerchantDetailPage } from "@/components/admin/merchants/merchant-detail-page";
import { PageSkeleton } from "@/components/admin/states";
import { titleMetadata } from "@/lib/admin/metadata";

/** Client-authenticated: the page renders after `GET /auth/me` in the browser. */
export const instant = false;
export const generateMetadata = titleMetadata("admin.nav.merchants");

async function MerchantRoute({ params }: { params: PageProps<"/admin/merchants/[id]">["params"] }) {
  const { id } = await params;
  return <MerchantDetailPage id={id} />;
}

export default function AdminMerchantDetailRoute({ params }: PageProps<"/admin/merchants/[id]">) {
  return (
    <Suspense fallback={<PageSkeleton />}>
      <MerchantRoute params={params} />
    </Suspense>
  );
}
