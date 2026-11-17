import { Suspense } from "react";
import { EmailsPage } from "@/components/admin/emails/emails-page";
import { PageSkeleton } from "@/components/admin/states";
import { titleMetadata } from "@/lib/admin/metadata";

/** Client-authenticated: the page renders after `GET /auth/me` in the browser. */
export const instant = false;
export const generateMetadata = titleMetadata("admin.nav.emails");

/** Filters live in the URL (`useSearchParams`), so the page renders inside Suspense. */
export default function AdminEmailsRoute() {
  return (
    <Suspense fallback={<PageSkeleton />}>
      <EmailsPage />
    </Suspense>
  );
}
