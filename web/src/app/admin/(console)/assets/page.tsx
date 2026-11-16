import { AssetsPage } from "@/components/admin/assets/assets-page";
import { titleMetadata } from "@/lib/admin/metadata";

/** Client-authenticated: the page renders after `GET /auth/me` in the browser. */
export const instant = false;
export const generateMetadata = titleMetadata("admin.nav.assets");

export default function AdminAssetsRoute() {
  return <AssetsPage />;
}
