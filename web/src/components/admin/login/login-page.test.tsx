import { screen, waitFor, within } from "@testing-library/react";
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

const api = vi.hoisted(() => ({ me: vi.fn(), login: vi.fn(), loginTwoFactor: vi.fn() }));
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

describe("LoginPage · two-step verification", () => {
  const challenge = (expiresInMs = 5 * 60_000) => ({
    two_factor_required: true as const,
    challenge_token: "ch-1",
    expires_at: new Date(Date.now() + expiresInMs).toISOString(),
  });
  const wrong = () => new ApiError({ status: 401, code: "unauthorized", message: "the verification failed" });

  async function toSecondStep(expiresInMs?: number) {
    api.login.mockResolvedValue(challenge(expiresInMs));
    const r = renderAdmin(<LoginPage />, { user: null });
    await fill("mfa@cryptopay.test", "mfa-pass-123");
    expect(await screen.findByRole("heading", { name: "Two-step verification" })).toBeInTheDocument();
    return r;
  }

  it("asks for the code after a 202 and signs in as soon as six digits are in", async () => {
    api.loginTwoFactor.mockResolvedValue(adminUser("admin", { two_factor_enabled: true }));
    const { client } = await toSecondStep();
    expect(client.getQueryData(["admin", "me"])).toBeUndefined();
    const input = screen.getByLabelText("Verification code");
    expect(input).toHaveAttribute("inputmode", "numeric");
    expect(input).toHaveAttribute("autocomplete", "one-time-code");
    expect(screen.queryByLabelText("Password")).not.toBeInTheDocument();
    await userEvent.type(input, "123456");
    await waitFor(() => expect(api.loginTwoFactor).toHaveBeenCalledWith({ challenge_token: "ch-1", code: "123456" }));
    await waitFor(() => expect(nav.replace).toHaveBeenCalledWith("/admin/chains"));
    expect(client.getQueryData(["admin", "me"])).toMatchObject({ role: "admin", two_factor_enabled: true });
    expect(api.loginTwoFactor).toHaveBeenCalledTimes(1);
  });

  it("accepts a pasted code with separators", async () => {
    api.loginTwoFactor.mockResolvedValue(adminUser("admin"));
    await toSecondStep();
    await userEvent.click(screen.getByLabelText("Verification code"));
    await userEvent.paste("Code: 654-321");
    await waitFor(() => expect(api.loginTwoFactor).toHaveBeenCalledWith({ challenge_token: "ch-1", code: "654321" }));
  });

  it("signs in with a recovery code instead", async () => {
    api.loginTwoFactor.mockResolvedValue(adminUser("admin"));
    await toSecondStep();
    await userEvent.click(screen.getByRole("button", { name: "Use a recovery code instead" }));
    expect(screen.getByText(/Each code works only once/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Verify" }));
    expect(await screen.findByText("Enter a recovery code")).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText("Recovery code"), " ABCDE-12345 ");
    await userEvent.click(screen.getByRole("button", { name: "Verify" }));
    await waitFor(() => expect(api.loginTwoFactor).toHaveBeenCalledWith({ challenge_token: "ch-1", recovery_code: "abcde-12345" }));
    await waitFor(() => expect(nav.replace).toHaveBeenCalledWith("/admin/chains"));
  });

  it("counts wrong codes and asks to start over once the challenge is used up", async () => {
    api.loginTwoFactor.mockRejectedValue(wrong());
    await toSecondStep();
    const input = screen.getByLabelText("Verification code");
    await userEvent.type(input, "111111");
    expect(await screen.findByRole("alert")).toHaveTextContent("That code is not correct. Check that your phone's clock is right and try again (4 attempts left).");
    expect(input).toHaveValue("");
    for (let i = 0; i < 4; i++) {
      await userEvent.type(screen.getByLabelText("Verification code"), "222222");
      await waitFor(() => expect(api.loginTwoFactor).toHaveBeenCalledTimes(i + 2));
    }
    expect(await screen.findByRole("alert")).toHaveTextContent("Too many wrong codes");
    expect(screen.getByLabelText("Verification code")).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "Sign in again" }));
    expect(await screen.findByLabelText("Password")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Sign in to the admin console" })).toBeInTheDocument();
  });

  it("says when the challenge expired", async () => {
    await toSecondStep(-1000);
    expect(await screen.findByRole("alert")).toHaveTextContent("The verification timed out (5 minutes). Sign in with your password again.");
    expect(screen.getByLabelText("Verification code")).toBeDisabled();
    expect(api.loginTwoFactor).not.toHaveBeenCalled();
  });

  it("explains throttling (429) and goes back to step one", async () => {
    api.loginTwoFactor.mockRejectedValue(new ApiError({ status: 429, code: "rate_limited", message: "slow down" }));
    await toSecondStep();
    await userEvent.type(screen.getByLabelText("Verification code"), "123456");
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Too many attempts. Wait a few minutes, then sign in again.");
    await userEvent.click(screen.getByRole("button", { name: "Back to email and password" }));
    const form = (await screen.findByLabelText("Email")).closest("form")!;
    expect(within(form).getByLabelText("Email")).toHaveValue("mfa@cryptopay.test");
    expect(within(form).getByLabelText("Password")).toHaveValue("");
  });
});
