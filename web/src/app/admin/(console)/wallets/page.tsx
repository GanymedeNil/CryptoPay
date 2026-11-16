import { WalletsPage } from "@/components/admin/wallets/wallets-page";
import { titleMetadata } from "@/lib/admin/metadata";

/** Client-authenticated: the page renders after `GET /auth/me` in the browser. */
export const instant = false;
export const generateMetadata = titleMetadata("admin.nav.wallets");

export default function AdminWalletsRoute() {
  return <WalletsPage />;
}
