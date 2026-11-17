import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { notifyTwoFactorRequired } from "@/lib/admin/query-client";
import { ApiError } from "@/lib/api/client";
import { adminMe, renderAdmin } from "@/test/admin";
import { AuthGate } from "../auth";
import { TwoFactorSetupWizard } from "./setup-wizard";

const nav = vi.hoisted(() => ({ replace: vi.fn() }));
vi.mock("next/navigation", () => ({
  useRouter: () => ({ replace: nav.replace, push: vi.fn(), refresh: vi.fn(), back: vi.fn(), prefetch: vi.fn() }),
  usePathname: () => "/admin/invoices",
  useSearchParams: () => new URLSearchParams(),
  useParams: () => ({}),
}));

const api = vi.hoisted(() => ({
  me: vi.fn(),
  logout: vi.fn(),
  setupTwoFactor: vi.fn(),
  enableTwoFactor: vi.fn(),
  listAuditLogs: vi.fn(),
}));
vi.mock("@/lib/api/admin", () => ({ adminApi: api }));

const SECRET = "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP";
const CODES = ["0a1b2-c3d4e", "f5g6h-7j8k9", "m0n1p-2q3r4", "s5t6v-7w8x9", "y0z1a-2b3c4", "d5e6f-7g8h9", "j0k1m-2n3p4", "q5r6s-7t8v9", "w0x1y-2z3a4", "b5c6d-7e8f9"];

beforeEach(() => {
  api.setupTwoFactor.mockResolvedValue({
    secret: SECRET,
    otpauth_uri: `otpauth://totp/CryptoPay%3Aowner%40cryptopay.test?secret=${SECRET}&issuer=CryptoPay&algorithm=SHA1&digits=6&period=30`,
  });
  api.enableTwoFactor.mockResolvedValue({ recovery_codes: CODES });
});
afterEach(() => vi.unstubAllGlobals());

async function passwordStep(password = "owner-pass-123") {
  await userEvent.type(screen.getByLabelText("Current password"), password);
  await userEvent.click(screen.getByRole("button", { name: "Continue" }));
}

describe("TwoFactorSetupWizard", () => {
  it("confirms the password, shows the QR code and key, and reports wrong inputs on their fields", async () => {
    api.setupTwoFactor.mockRejectedValueOnce(
      new ApiError({ status: 400, code: "invalid_request", message: "invalid", fields: { current_password: "is incorrect" } }),
    );
    api.enableTwoFactor.mockRejectedValueOnce(new ApiError({ status: 400, code: "invalid_request", message: "invalid", fields: { code: "is incorrect" } }));
    renderAdmin(<TwoFactorSetupWizard email="owner@cryptopay.test" onComplete={vi.fn()} />);
    expect(screen.getByText("Confirm password").closest("li")).toHaveAttribute("aria-current", "step");

    await passwordStep("wrong");
    expect(await screen.findByText("The current password is not correct")).toBeInTheDocument();
    expect(api.setupTwoFactor).toHaveBeenCalledWith({ current_password: "wrong" });

    await passwordStep();
    expect(await screen.findByRole("img", { name: "Two-step verification QR code" })).toBeInTheDocument();
    expect(screen.getByTestId("totp-secret")).toHaveTextContent("JBSW Y3DP EHPK 3PXP JBSW Y3DP EHPK 3PXP");
    expect(screen.getByText("Link your app").closest("li")).toHaveAttribute("aria-current", "step");

    await userEvent.type(screen.getByLabelText("6-digit code"), "000000");
    expect(await screen.findByText("That code is not correct. Check your phone's clock and try again")).toBeInTheDocument();
    expect(screen.getByLabelText("6-digit code")).toHaveValue("");
  });

  it("shows the recovery codes once and finishes only after they were stored", async () => {
    const createObjectURL = vi.fn(() => "blob:codes");
    vi.stubGlobal("URL", Object.assign(URL, { createObjectURL, revokeObjectURL: vi.fn() }));
    const onComplete = vi.fn();
    renderAdmin(<TwoFactorSetupWizard email="owner@cryptopay.test" onComplete={onComplete} />);
    await passwordStep();
    await userEvent.type(await screen.findByLabelText("6-digit code"), "123456");
    await waitFor(() => expect(api.enableTwoFactor).toHaveBeenCalledWith({ code: "123456" }));

    const list = await screen.findByTestId("recovery-codes");
    expect(within(list).getAllByRole("listitem").map((li) => li.textContent)).toEqual(CODES);
    // The secret is gone from the screen once the codes are shown.
    expect(screen.queryByTestId("totp-secret")).not.toBeInTheDocument();
    expect(screen.getByText(/shown only this once/)).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Download .txt" }));
    expect(createObjectURL).toHaveBeenCalledTimes(1);
    const blob = (createObjectURL.mock.calls[0] as unknown as [Blob])[0];
    const text = await blob.text();
    expect(text).toContain("CryptoPay recovery codes");
    expect(text).toContain("Account: owner@cryptopay.test");
    for (const c of CODES) expect(text).toContain(c);

    const done = screen.getByRole("button", { name: "Done" });
    expect(done).toBeDisabled();
    await userEvent.click(done);
    expect(onComplete).not.toHaveBeenCalled();
    await userEvent.click(screen.getByLabelText("I have stored these recovery codes safely"));
    await userEvent.click(screen.getByRole("button", { name: "Done" }));
    expect(onComplete).toHaveBeenCalledTimes(1);
  });
});

describe("AuthGate · forced two-factor setup", () => {
  function Console() {
    return <p>console content</p>;
  }

  it("shows only the setup flow while /auth/me says it is required, then the console", async () => {
    api.me.mockResolvedValue(adminMe("admin", { two_factor_setup_required: true }));
    renderAdmin(
      <AuthGate fallback={<p>checking</p>}>
        <Console />
      </AuthGate>,
      { user: null },
    );
    expect(await screen.findByRole("heading", { name: "Set up two-step verification first" })).toBeInTheDocument();
    expect(screen.getByText("Signed in as admin@cryptopay.test")).toBeInTheDocument();
    expect(screen.queryByText("console content")).not.toBeInTheDocument();

    api.me.mockResolvedValue(adminMe("admin", { two_factor_enabled: true }));
    await passwordStep("admin-pass-123");
    await userEvent.type(await screen.findByLabelText("6-digit code"), "123456");
    await screen.findByTestId("recovery-codes");
    // The codes stay on screen until confirmed, even though setup is done server-side.
    expect(screen.queryByText("console content")).not.toBeInTheDocument();
    await userEvent.click(screen.getByLabelText("I have stored these recovery codes safely"));
    await userEvent.click(screen.getByRole("button", { name: "Done" }));
    expect(await screen.findByText("console content")).toBeInTheDocument();
  });

  it("switches to the setup flow when any request answers 403 two_factor_setup_required", async () => {
    api.me.mockResolvedValue(adminMe("viewer"));
    renderAdmin(
      <AuthGate fallback={<p>checking</p>}>
        <Console />
      </AuthGate>,
      { user: null },
    );
    expect(await screen.findByText("console content")).toBeInTheDocument();
    api.me.mockResolvedValue(adminMe("viewer", { two_factor_setup_required: true }));
    act(() => notifyTwoFactorRequired());
    expect(await screen.findByRole("heading", { name: "Set up two-step verification first" })).toBeInTheDocument();
    expect(screen.queryByText("console content")).not.toBeInTheDocument();
    await waitFor(() => expect(api.me).toHaveBeenCalledTimes(2));
    expect(screen.getByRole("heading", { name: "Set up two-step verification first" })).toBeInTheDocument();
  });

  it("can sign out from the setup flow", async () => {
    api.me.mockResolvedValue(adminMe("admin", { two_factor_setup_required: true }));
    api.logout.mockResolvedValue(undefined);
    renderAdmin(
      <AuthGate fallback={<p>checking</p>}>
        <Console />
      </AuthGate>,
      { user: null },
    );
    await userEvent.click(await screen.findByRole("button", { name: "Sign out" }));
    await waitFor(() => expect(nav.replace).toHaveBeenCalledWith("/admin/login?reason=signedOut"));
  });
});
