import { ChainsPage } from "@/components/admin/chains/chains-page";
import { titleMetadata } from "@/lib/admin/metadata";

/** Client-authenticated: the page renders after `GET /auth/me` in the browser. */
export const instant = false;
export const generateMetadata = titleMetadata("admin.nav.chains");

export default function AdminChainsRoute() {
  return <ChainsPage />;
}
