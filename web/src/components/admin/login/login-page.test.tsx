import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "@/lib/api/client";
import { adminUser, renderAdmin } from "@/test/admin";
import { LoginPage } from "./login-page";

const nav = vi.hoisted(() => ({ replace: vi.fn(), search: "next=%2Fadmin%2Fchains" }));
vi.mock("next/navigation", () => ({
  useRouter: () => ({ replace: nav.replace, push: vi.fn(), refresh: vi.fn(), back: vi.fn(), prefetch: vi.fn() }),
  usePathname: () => "/admin/login",
  useSearchParams: () => new URLSearchParams(nav.search),
  useParams: () => ({}),
}));

const api = vi.hoisted(() => ({ me: vi.fn(), login: vi.fn() }));
vi.mock("@/lib/api/admin", () => ({ adminApi: api }));

const unauthorized = () => new ApiError({ status: 401, code: "unauthorized", message: "not signed in" });

beforeEach(() => {
  nav.search = "next=%2Fadmin%2Fchains";
  api.me.mockRejectedValue(unauthorized());
});

async function fill(email: string, password: string) {
  await userEvent.type(screen.getByLabelText("Email"), email);
  await userEvent.type(screen.getByLabelText("Password"), password);
  await userEvent.click(screen.getByRole("button", { name: "Sign in" }));
}

describe("LoginPage", () => {
  it("validates before calling the API", async () => {
    renderAdmin(<LoginPage />, { user: null });
    await userEvent.click(screen.getByRole("button", { name: "Sign in" }));
    expect(await screen.findByText("Enter your email")).toBeInTheDocument();
    expect(screen.getByText("Enter your password")).toBeInTheDocument();
    expect(api.login).not.toHaveBeenCalled();
  });

  it("explains wrong credentials (401) and clears the password", async () => {
    api.login.mockRejectedValue(new ApiError({ status: 401, code: "unauthorized", message: "invalid" }));
    renderAdmin(<LoginPage />, { user: null });
    await fill("owner@cryptopay.test", "wrong");
    expect(await screen.findByRole("alert")).toHaveTextContent("Email or password is incorrect.");
    expect(screen.getByLabelText("Password")).toHaveValue("");
    expect(nav.replace).not.toHaveBeenCalled();
  });

  it("explains rate limiting (429)", async () => {
    api.login.mockRejectedValue(new ApiError({ status: 429, code: "rate_limited", message: "slow down" }));
    renderAdmin(<LoginPage />, { user: null });
    await fill("owner@cryptopay.test", "wrong");
    expect(await screen.findByRole("alert")).toHaveTextContent("Too many failed attempts");
  });

  it("signs in and returns to the requested page", async () => {
    api.login.mockResolvedValue(adminUser("owner"));
    const { client } = renderAdmin(<LoginPage />, { user: null });
    await fill(" owner@cryptopay.test ", "owner-pass-123");
    expect(api.login).toHaveBeenCalledWith({ email: "owner@cryptopay.test", password: "owner-pass-123" });
    await waitFor(() => expect(nav.replace).toHaveBeenCalledWith("/admin/chains"));
    expect(client.getQueryData(["admin", "me"])).toMatchObject({ role: "owner" });
  });

  it("never redirects off-site", async () => {
    nav.search = "next=https%3A%2F%2Fevil.example";
    api.login.mockResolvedValue(adminUser("owner"));
    renderAdmin(<LoginPage />, { user: null });
    await fill("owner@cryptopay.test", "owner-pass-123");
    await waitFor(() => expect(nav.replace).toHaveBeenCalledWith("/admin"));
  });

  it("tells a returning user why they are here", () => {
    nav.search = "reason=expired";
    renderAdmin(<LoginPage />, { user: null });
    expect(screen.getByText("Your session expired. Sign in again.")).toBeInTheDocument();
  });
});
