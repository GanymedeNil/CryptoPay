import { act, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { notifyUnauthorized } from "@/lib/admin/query-client";
import { ApiError } from "@/lib/api/client";
import { adminUser, renderAdmin } from "@/test/admin";
import { AuthGate, GatedButton, loginUrl, safeNextPath, useAuth } from "./auth";
import { UsersPage } from "./users/users-page";

const nav = vi.hoisted(() => ({ replace: vi.fn(), push: vi.fn() }));
vi.mock("next/navigation", () => ({
  useRouter: () => ({ replace: nav.replace, push: nav.push, refresh: vi.fn(), back: vi.fn(), prefetch: vi.fn() }),
  usePathname: () => "/admin/invoices",
  useSearchParams: () => new URLSearchParams(),
  useParams: () => ({}),
}));

const api = vi.hoisted(() => ({ me: vi.fn(), listUsers: vi.fn() }));
vi.mock("@/lib/api/admin", () => ({ adminApi: api }));

function Who() {
  const { user, canWrite } = useAuth();
  return (
    <p>
      {user.email} · {canWrite ? "can write" : "read only"}
    </p>
  );
}

beforeEach(() => {
  window.history.replaceState(null, "", "/admin/invoices?status=paid");
});

describe("login redirect helpers", () => {
  it("only returns to console paths", () => {
    expect(safeNextPath("/admin/invoices?status=paid")).toBe("/admin/invoices?status=paid");
    expect(safeNextPath("https://evil.example")).toBe("/admin");
    expect(safeNextPath("//evil.example/admin")).toBe("/admin");
    expect(safeNextPath("/admin/login?next=/admin")).toBe("/admin");
    expect(safeNextPath(null)).toBe("/admin");
    expect(loginUrl("/admin/chains", "expired")).toBe("/admin/login?next=%2Fadmin%2Fchains&reason=expired");
    expect(loginUrl("/admin")).toBe("/admin/login");
  });
});

describe("AuthGate", () => {
  it("renders the console for a signed-in user and exposes the role", async () => {
    api.me.mockResolvedValue(adminUser("viewer"));
    renderAdmin(
      <AuthGate fallback={<p>checking</p>}>
        <Who />
      </AuthGate>,
      { user: null },
    );
    expect(screen.getByText("checking")).toBeInTheDocument();
    expect(await screen.findByText("viewer@cryptopay.test · read only")).toBeInTheDocument();
  });

  it("sends a signed-out visitor to the login page, keeping the current URL", async () => {
    api.me.mockRejectedValue(new ApiError({ status: 401, code: "unauthorized", message: "not signed in" }));
    renderAdmin(
      <AuthGate fallback={<p>checking</p>}>
        <Who />
      </AuthGate>,
      { user: null },
    );
    await waitFor(() => expect(nav.replace).toHaveBeenCalledWith("/admin/login?next=%2Fadmin%2Finvoices%3Fstatus%3Dpaid"));
    expect(screen.queryByText(/cryptopay\.test/)).not.toBeInTheDocument();
  });

  it("returns to login with 'session expired' when any later request gets a 401", async () => {
    api.me.mockResolvedValue(adminUser("admin"));
    const { client } = renderAdmin(
      <AuthGate fallback={<p>checking</p>}>
        <Who />
      </AuthGate>,
      { user: null },
    );
    await screen.findByText("admin@cryptopay.test · can write");
    expect(client.getQueryData(["admin", "me"])).toBeTruthy();
    act(() => notifyUnauthorized());
    expect(nav.replace).toHaveBeenCalledWith("/admin/login?next=%2Fadmin%2Finvoices%3Fstatus%3Dpaid&reason=expired");
  });

  it("offers a retry when the session check fails for another reason", async () => {
    api.me.mockRejectedValueOnce(new ApiError({ status: 503, code: "unavailable", message: "down" }));
    api.me.mockResolvedValueOnce(adminUser("owner"));
    renderAdmin(
      <AuthGate fallback={<p>checking</p>}>
        <Who />
      </AuthGate>,
      { user: null },
    );
    await userEvent.click(await screen.findByRole("button", { name: "Try again" }));
    expect(await screen.findByText("owner@cryptopay.test · can write")).toBeInTheDocument();
    expect(nav.replace).not.toHaveBeenCalled();
  });
});

describe("role gating", () => {
  it("disables mutating buttons for viewers and explains why", async () => {
    const onClick = vi.fn();
    renderAdmin(<GatedButton onClick={onClick}>Create invoice</GatedButton>, { role: "viewer" });
    const button = screen.getByRole("button", { name: "Create invoice" });
    expect(button).toBeDisabled();
    const wrapper = screen.getByLabelText("The Viewer role can't do this");
    await userEvent.hover(wrapper);
    expect((await screen.findAllByText("The Viewer role can't do this")).length).toBeGreaterThan(0);
  });

  it("keeps them enabled for admins; owner-only actions need an owner", () => {
    renderAdmin(
      <>
        <GatedButton>Create invoice</GatedButton>
        <GatedButton ownerOnly>Add admin</GatedButton>
      </>,
      { role: "admin" },
    );
    expect(screen.getByRole("button", { name: "Create invoice" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "Add admin" })).toBeDisabled();
  });

  it("shows other reasons a control is unavailable", () => {
    renderAdmin(<GatedButton disabledReason="Only pending invoices can be cancelled">Cancel invoice</GatedButton>, { role: "owner" });
    expect(screen.getByRole("button", { name: "Cancel invoice" })).toBeDisabled();
    expect(screen.getByLabelText("Only pending invoices can be cancelled")).toBeInTheDocument();
  });

  it("does not load the admin list for non-owners", () => {
    renderAdmin(<UsersPage />, { role: "admin" });
    expect(screen.getByText("Owners only")).toBeInTheDocument();
    expect(api.listUsers).not.toHaveBeenCalled();
  });
});
