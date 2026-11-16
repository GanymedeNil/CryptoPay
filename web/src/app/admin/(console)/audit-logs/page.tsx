import { Suspense } from "react";
import { AuditLogsPage } from "@/components/admin/audit/audit-logs-page";
import { PageSkeleton } from "@/components/admin/states";
import { titleMetadata } from "@/lib/admin/metadata";

/** Client-authenticated: the page renders after `GET /auth/me` in the browser. */
export const instant = false;
export const generateMetadata = titleMetadata("admin.nav.auditLogs");

export default function AdminAuditLogsRoute() {
  return (
    <Suspense fallback={<PageSkeleton />}>
      <AuditLogsPage />
    </Suspense>
  );
}
