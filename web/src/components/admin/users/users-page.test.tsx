import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "@/lib/api/client";
import { adminUser, renderAdmin } from "@/test/admin";
import { UsersPage } from "./users-page";

const api = vi.hoisted(() => ({ listUsers: vi.fn(), resetUserTwoFactor: vi.fn(), updateUser: vi.fn(), deleteUser: vi.fn(), listAuditLogs: vi.fn() }));
vi.mock("@/lib/api/admin", () => ({ adminApi: api }));

const OWNER = adminUser("owner", { id: "u-owner" });
const MFA = adminUser("admin", { id: "u-mfa", email: "mfa@cryptopay.test", two_factor_enabled: true });
const PLAIN = adminUser("viewer", { id: "u-plain", email: "plain@cryptopay.test" });

const rowOf = (email: string) => screen.getByText(email).closest("tr")!;

beforeEach(() => {
  api.listUsers.mockResolvedValue({ items: [OWNER, MFA, PLAIN] });
});

describe("UsersPage · two-step verification", () => {
  it("shows who has two-step verification", async () => {
    renderAdmin(<UsersPage />, { user: OWNER });
    await screen.findByText("mfa@cryptopay.test");
    expect(within(rowOf("mfa@cryptopay.test")).getByText("On")).toBeInTheDocument();
    expect(within(rowOf("plain@cryptopay.test")).getByText("Off")).toBeInTheDocument();
  });

  it("lets an owner reset another user's two-step verification after confirming", async () => {
    api.resetUserTwoFactor.mockResolvedValue(undefined);
    renderAdmin(<UsersPage />, { user: OWNER });
    await screen.findByText("mfa@cryptopay.test");
    await userEvent.click(within(rowOf("mfa@cryptopay.test")).getByRole("button", { name: "More actions: mfa@cryptopay.test" }));
    await userEvent.click(await screen.findByRole("menuitem", { name: "Reset two-step verification" }));
    const confirm = await screen.findByRole("alertdialog", { name: "Reset two-step verification of mfa@cryptopay.test?" });
    expect(within(confirm).getByText(/every session is signed out/)).toBeInTheDocument();
    await userEvent.click(within(confirm).getByRole("button", { name: "Reset two-step verification" }));
    await waitFor(() => expect(api.resetUserTwoFactor).toHaveBeenCalledWith("u-mfa"));
    await waitFor(() => expect(api.listUsers).toHaveBeenCalledTimes(2));
  });

  it("offers the reset only for users who have it", async () => {
    renderAdmin(<UsersPage />, { user: OWNER });
    await screen.findByText("plain@cryptopay.test");
    await userEvent.click(within(rowOf("plain@cryptopay.test")).getByRole("button", { name: "More actions: plain@cryptopay.test" }));
    await screen.findByRole("menuitem", { name: "Reset password" });
    expect(screen.queryByRole("menuitem", { name: "Reset two-step verification" })).not.toBeInTheDocument();
  });

  it("keeps the dialog open when the reset is refused", async () => {
    api.resetUserTwoFactor.mockRejectedValue(new ApiError({ status: 409, code: "conflict", message: "this user has no two-factor authentication" }));
    renderAdmin(<UsersPage />, { user: OWNER });
    await screen.findByText("mfa@cryptopay.test");
    await userEvent.click(within(rowOf("mfa@cryptopay.test")).getByRole("button", { name: "More actions: mfa@cryptopay.test" }));
    await userEvent.click(await screen.findByRole("menuitem", { name: "Reset two-step verification" }));
    const confirm = await screen.findByRole("alertdialog");
    await userEvent.click(within(confirm).getByRole("button", { name: "Reset two-step verification" }));
    await waitFor(() => expect(api.resetUserTwoFactor).toHaveBeenCalled());
    expect(screen.getByRole("alertdialog")).toBeInTheDocument();
  });
});
