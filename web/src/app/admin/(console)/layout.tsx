import { Suspense } from "react";
import { AdminShell, ShellSkeleton } from "@/components/admin/shell/admin-shell";

/**
 * Authenticated console. Data is fetched in the browser (the session cookie
 * is HttpOnly and lives on the API origin behind the /api proxy), so the
 * server renders the shell skeleton and the client takes over.
 */
export default function ConsoleLayout({ children }: { children: React.ReactNode }) {
  return (
    <Suspense fallback={<ShellSkeleton />}>
      <AdminShell>{children}</AdminShell>
    </Suspense>
  );
}
