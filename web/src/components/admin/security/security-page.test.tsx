import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ApiError } from "@/lib/api/client";
import { adminUser, renderAdmin } from "@/test/admin";
import { SecurityPage } from "./security-page";

const nav = vi.hoisted(() => ({ replace: vi.fn() }));
vi.mock("next/navigation", () => ({
  useRouter: () => ({ replace: nav.replace, push: vi.fn(), refresh: vi.fn(), back: vi.fn(), prefetch: vi.fn() }),
  usePathname: () => "/admin/security",
  useSearchParams: () => new URLSearchParams(),
  useParams: () => ({}),
}));

const api = vi.hoisted(() => ({
  disableTwoFactor: vi.fn(),
  regenerateRecoveryCodes: vi.fn(),
  setupTwoFactor: vi.fn(),
  enableTwoFactor: vi.fn(),
  listAuditLogs: vi.fn(),
}));
vi.mock("@/lib/api/admin", () => ({ adminApi: api }));

describe("SecurityPage · two-step verification", () => {
  it("offers setup when it is off", async () => {
    renderAdmin(<SecurityPage />, { user: adminUser("viewer") });
    const card = screen.getByRole("region", { name: /Two-step verification/ });
    expect(within(card).getByText("Off")).toBeInTheDocument();
    await userEvent.click(within(card).getByRole("button", { name: "Enable two-step verification" }));
    expect(await screen.findByRole("dialog", { name: "Enable two-step verification" })).toBeInTheDocument();
    expect(screen.getByLabelText("Current password")).toBeInTheDocument();
  });

  it("explains why it can't be turned off while required", async () => {
    api.disableTwoFactor.mockRejectedValue(new ApiError({ status: 409, code: "conflict", message: "two-factor authentication is required" }));
    renderAdmin(<SecurityPage />, { user: adminUser("admin", { two_factor_enabled: true }) });
    await userEvent.click(screen.getByRole("button", { name: "Turn off two-step verification" }));
    const dialog = await screen.findByRole("dialog", { name: "Turn off two-step verification" });
    await userEvent.type(within(dialog).getByLabelText("Current password"), "admin-pass-123");
    await userEvent.type(within(dialog).getByLabelText("Code or recovery code"), "123 456");
    await userEvent.click(within(dialog).getByRole("button", { name: "Turn off" }));
    await waitFor(() => expect(api.disableTwoFactor).toHaveBeenCalledWith({ current_password: "admin-pass-123", code: "123456" }));
    expect(await within(dialog).findByText("The settings require two-step verification for every admin, so it can't be turned off.")).toBeInTheDocument();
  });

  it("maps a wrong password or code onto the fields", async () => {
    api.disableTwoFactor
      .mockRejectedValueOnce(new ApiError({ status: 400, code: "invalid_request", message: "x", fields: { current_password: "is incorrect" } }))
      .mockRejectedValueOnce(new ApiError({ status: 400, code: "invalid_request", message: "x", fields: { code: "is incorrect" } }));
    renderAdmin(<SecurityPage />, { user: adminUser("admin", { two_factor_enabled: true }) });
    await userEvent.click(screen.getByRole("button", { name: "Turn off two-step verification" }));
    const dialog = await screen.findByRole("dialog");
    await userEvent.type(within(dialog).getByLabelText("Current password"), "nope");
    await userEvent.type(within(dialog).getByLabelText("Code or recovery code"), "ABCDE-12345");
    await userEvent.click(within(dialog).getByRole("button", { name: "Turn off" }));
    expect(await within(dialog).findByText("The current password is incorrect")).toBeInTheDocument();
    expect(api.disableTwoFactor).toHaveBeenLastCalledWith({ current_password: "nope", code: "abcde-12345" });
    await userEvent.click(within(dialog).getByRole("button", { name: "Turn off" }));
    expect(await within(dialog).findByText("The code or recovery code is not correct")).toBeInTheDocument();
  });

  it("regenerates recovery codes and shows them once", async () => {
    const codes = Array.from({ length: 10 }, (_, i) => `abcde-0000${i}`);
    api.regenerateRecoveryCodes.mockResolvedValue({ recovery_codes: codes });
    renderAdmin(<SecurityPage />, { user: adminUser("admin", { two_factor_enabled: true }) });
    await userEvent.click(screen.getByRole("button", { name: "New recovery codes" }));
    const dialog = await screen.findByRole("dialog", { name: "Generate new recovery codes" });
    await userEvent.type(within(dialog).getByLabelText("Current password"), "admin-pass-123");
    await userEvent.type(within(dialog).getByLabelText("Authenticator code"), "654 321");
    await userEvent.click(within(dialog).getByRole("button", { name: "Generate new codes" }));
    expect(api.regenerateRecoveryCodes).toHaveBeenLastCalledWith({ current_password: "admin-pass-123", code: "654321" });
    const shown = await screen.findByRole("dialog", { name: "Save your recovery codes" });
    expect(within(within(shown).getByTestId("recovery-codes")).getAllByRole("listitem")).toHaveLength(10);
    await userEvent.keyboard("{Escape}");
    expect(screen.getByRole("dialog", { name: "Save your recovery codes" })).toBeInTheDocument();
    await userEvent.click(within(shown).getByLabelText("I have stored these recovery codes safely"));
    await userEvent.click(within(shown).getByRole("button", { name: "Done" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });

  it("keeps changing the password reachable", async () => {
    renderAdmin(<SecurityPage />);
    await userEvent.click(screen.getByRole("button", { name: "Change password" }));
    expect(await screen.findByRole("dialog", { name: "Change password" })).toBeInTheDocument();
  });
});
