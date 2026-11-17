import { SecurityPage } from "@/components/admin/security/security-page";
import { titleMetadata } from "@/lib/admin/metadata";

/** Client-authenticated: the page renders after `GET /auth/me` in the browser. */
export const instant = false;
export const generateMetadata = titleMetadata("admin.nav.security");

export default function AdminSecurityRoute() {
  return <SecurityPage />;
}
