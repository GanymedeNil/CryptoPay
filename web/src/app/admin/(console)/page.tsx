import { Suspense } from "react";
import { OverviewPage } from "@/components/admin/overview/overview-page";
import { PageSkeleton } from "@/components/admin/states";
import { titleMetadata } from "@/lib/admin/metadata";

/** Client-authenticated: the page renders after `GET /auth/me` in the browser. */
export const instant = false;
export const generateMetadata = titleMetadata("admin.nav.overview");

export default function AdminOverviewRoute() {
  return (
    <Suspense fallback={<PageSkeleton />}>
      <OverviewPage />
    </Suspense>
  );
}
