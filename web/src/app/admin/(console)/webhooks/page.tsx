import { Suspense } from "react";
import { PageSkeleton } from "@/components/admin/states";
import { WebhooksPage } from "@/components/admin/webhooks/webhooks-page";
import { titleMetadata } from "@/lib/admin/metadata";

/** Client-authenticated: the page renders after `GET /auth/me` in the browser. */
export const instant = false;
export const generateMetadata = titleMetadata("admin.nav.webhooks");

export default function AdminWebhooksRoute() {
  return (
    <Suspense fallback={<PageSkeleton />}>
      <WebhooksPage />
    </Suspense>
  );
}
