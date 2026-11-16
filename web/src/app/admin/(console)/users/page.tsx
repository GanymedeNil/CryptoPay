import { UsersPage } from "@/components/admin/users/users-page";
import { titleMetadata } from "@/lib/admin/metadata";

/** Client-authenticated: the page renders after `GET /auth/me` in the browser. */
export const instant = false;
export const generateMetadata = titleMetadata("admin.nav.users");

export default function AdminUsersRoute() {
  return <UsersPage />;
}
