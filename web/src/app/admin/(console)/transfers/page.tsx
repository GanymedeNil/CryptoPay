import { Suspense } from "react";
import { PageSkeleton } from "@/components/admin/states";
import { TransfersPage } from "@/components/admin/transfers/transfers-page";
import { titleMetadata } from "@/lib/admin/metadata";

/** Client-authenticated: the page renders after `GET /auth/me` in the browser. */
export const instant = false;
export const generateMetadata = titleMetadata("admin.nav.transfers");

export default function AdminTransfersRoute() {
  return (
    <Suspense fallback={<PageSkeleton />}>
      <TransfersPage />
    </Suspense>
  );
}
