import { Suspense } from "react";
import { LoginPage } from "@/components/admin/login/login-page";
import { titleMetadata } from "@/lib/admin/metadata";

/** Reads the session in the browser; not prerendered for instant navigation. */
export const instant = false;
export const generateMetadata = titleMetadata("admin.login.title");

/** Reads `?next=` / `?reason=` on the client, so it renders inside Suspense. */
export default function AdminLoginPage() {
  return (
    <Suspense fallback={null}>
      <LoginPage />
    </Suspense>
  );
}
