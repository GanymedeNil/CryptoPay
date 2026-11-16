import { Suspense } from "react";
import { MerchantsPage } from "@/components/admin/merchants/merchants-page";
import { PageSkeleton } from "@/components/admin/states";
import { titleMetadata } from "@/lib/admin/metadata";

/** Client-authenticated: the page renders after `GET /auth/me` in the browser. */
export const instant = false;
export const generateMetadata = titleMetadata("admin.nav.merchants");

export default function AdminMerchantsRoute() {
  return (
    <Suspense fallback={<PageSkeleton />}>
      <MerchantsPage />
    </Suspense>
  );
}
