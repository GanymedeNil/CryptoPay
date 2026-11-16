import { SettingsPage } from "@/components/admin/settings/settings-page";
import { titleMetadata } from "@/lib/admin/metadata";

/** Client-authenticated: the page renders after `GET /auth/me` in the browser. */
export const instant = false;
export const generateMetadata = titleMetadata("admin.nav.settings");

export default function AdminSettingsRoute() {
  return <SettingsPage />;
}
