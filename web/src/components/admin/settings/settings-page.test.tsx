import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "@/lib/api/client";
import { adminUser, renderAdmin, SETTINGS } from "@/test/admin";
import { SettingsForm } from "./settings-page";

const api = vi.hoisted(() => ({ updateSettings: vi.fn(), listAuditLogs: vi.fn(), getEmailStatus: vi.fn(), sendTestEmail: vi.fn() }));
vi.mock("@/lib/api/admin", () => ({ adminApi: api }));

beforeEach(() => {
  api.getEmailStatus.mockResolvedValue({ configured: true, from: "CryptoPay <no-reply@cryptopay.local>", host: "mailpit" });
});

describe("SettingsForm", () => {
  it("shows seconds as minutes and bps as percent, and saves in API units", async () => {
    api.updateSettings.mockImplementation(async (s) => s);
    renderAdmin(<SettingsForm settings={SETTINGS} />);
    const ttl = screen.getByLabelText("Invoice lifetime");
    expect(ttl).toHaveValue("15");
    expect(screen.getByLabelText("Underpayment tolerance")).toHaveValue("0.5");
    expect(screen.getByLabelText("Rate markup")).toHaveValue("1");
    expect(screen.getByRole("button", { name: "Save changes" })).toBeDisabled();

    await userEvent.clear(ttl);
    await userEvent.type(ttl, "45");
    const tolerance = screen.getByLabelText("Underpayment tolerance");
    await userEvent.clear(tolerance);
    await userEvent.type(tolerance, "0.25");
    expect(screen.getByText("Unsaved changes")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Save changes" }));
    await waitFor(() =>
      expect(api.updateSettings).toHaveBeenCalledWith({ ...SETTINGS, invoice_ttl_seconds: 2700, underpay_tolerance_bps: 25 }),
    );
  });

  it("validates in operator units before saving", async () => {
    renderAdmin(<SettingsForm settings={SETTINGS} />);
    const tolerance = screen.getByLabelText("Underpayment tolerance");
    await userEvent.clear(tolerance);
    await userEvent.type(tolerance, "0.555");
    const attempts = screen.getByLabelText("Maximum attempts");
    await userEvent.clear(attempts);
    await userEvent.type(attempts, "99");
    await userEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("At most two decimals, e.g. 0.5")).toBeInTheDocument();
    expect(screen.getByText("From 1 to 50")).toBeInTheDocument();
    expect(api.updateSettings).not.toHaveBeenCalled();
  });

  it("maps server field errors back to the operator's fields", async () => {
    api.updateSettings.mockRejectedValue(
      new ApiError({ status: 400, code: "invalid_request", message: "invalid", fields: { grace_period_seconds: "must be 0..86400" } }),
    );
    renderAdmin(<SettingsForm settings={SETTINGS} />);
    const markup = screen.getByLabelText("Rate markup");
    await userEvent.clear(markup);
    await userEvent.type(markup, "2");
    await userEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("must be 0..86400")).toBeInTheDocument();
    expect(screen.getByLabelText("Grace period")).toHaveAttribute("aria-invalid", "true");
  });

  it("is read-only for viewers", () => {
    renderAdmin(<SettingsForm settings={SETTINGS} />, { role: "viewer" });
    expect(screen.getByLabelText("Invoice lifetime")).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Save changes" })).not.toBeInTheDocument();
  });
});

describe("SettingsForm · security and email", () => {
  it("saves require 2FA, the email toggles, recipients and language", async () => {
    api.updateSettings.mockImplementation(async (s) => s);
    renderAdmin(<SettingsForm settings={SETTINGS} />, { user: adminUser("owner", { two_factor_enabled: true }) });
    expect(await screen.findByText("Configured: sent through mailpit from CryptoPay <no-reply@cryptopay.local>.")).toBeInTheDocument();
    expect(screen.queryByText(/you haven't enabled it yet/)).not.toBeInTheDocument();
    expect(screen.getByText("No recipients: alerts go to every active Owner.")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("switch", { name: "Require two-step verification for every admin" }));
    await userEvent.click(screen.getByRole("switch", { name: "Operational alert emails" }));
    await userEvent.click(screen.getByRole("switch", { name: "Security emails" }));
    const recipients = screen.getByLabelText("Alert recipients");
    await userEvent.type(recipients, "ops@example.com{Enter}");
    await userEvent.type(recipients, "OPS@example.com{Enter}");
    expect(screen.getByText("This address is already in the list")).toBeInTheDocument();
    await userEvent.clear(recipients);
    await userEvent.type(recipients, "not-an-email");
    await userEvent.click(screen.getByRole("button", { name: "Add" }));
    expect(screen.getByText("Enter a valid email address")).toBeInTheDocument();
    await userEvent.clear(recipients);
    await userEvent.type(recipients, "cfo@example.com, sre@example.com");
    await userEvent.click(screen.getByRole("button", { name: "Add" }));
    await userEvent.click(screen.getByRole("button", { name: "Remove sre@example.com" }));
    await userEvent.click(screen.getByRole("combobox", { name: "Email language" }));
    await userEvent.click(await screen.findByRole("option", { name: "English" }));

    await userEvent.click(screen.getByRole("button", { name: "Save changes" }));
    await waitFor(() =>
      expect(api.updateSettings).toHaveBeenCalledWith({
        ...SETTINGS,
        require_2fa: true,
        alert_emails: true,
        security_emails: false,
        alert_recipients: ["ops@example.com", "cfo@example.com"],
        email_locale: "en",
      }),
    );
  });

  it("explains the precondition and surfaces the 409 on the switch", async () => {
    api.updateSettings.mockRejectedValue(
      new ApiError({ status: 409, code: "conflict", message: "enable two-factor authentication for your own account before requiring it for everyone" }),
    );
    renderAdmin(<SettingsForm settings={SETTINGS} />, { user: adminUser("admin") });
    expect(screen.getByText(/you haven't enabled it yet/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Enable it under Account security" })).toHaveAttribute("href", "/admin/security");
    const toggle = screen.getByRole("switch", { name: "Require two-step verification for every admin" });
    await userEvent.click(toggle);
    await userEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(
      await screen.findByText("Couldn't turn it on: enable two-factor authentication for your own account before requiring it for everyone"),
    ).toBeInTheDocument();
    expect(toggle).toHaveAttribute("aria-invalid", "true");
  });

  it("says clearly when email is not configured", async () => {
    api.getEmailStatus.mockResolvedValue({ configured: false, from: "", host: "" });
    renderAdmin(<SettingsForm settings={SETTINGS} />);
    const note = await screen.findByTestId("email-not-configured");
    expect(note).toHaveTextContent("Email is not configured");
    expect(note).toHaveTextContent("SMTP_HOST and SMTP_FROM");
  });

  it("sends a test email and shows the outcome", async () => {
    api.sendTestEmail
      .mockResolvedValueOnce({ ok: true, error: "" })
      .mockResolvedValueOnce({ ok: false, error: "550 5.1.1 <bounce@example.com>: Recipient address rejected" })
      .mockRejectedValueOnce(new ApiError({ status: 409, code: "conflict", message: "e-mail is not configured" }));
    renderAdmin(<SettingsForm settings={SETTINGS} />);
    const send = screen.getByRole("button", { name: "Send test email" });
    await userEvent.click(send);
    await waitFor(() => expect(api.sendTestEmail).toHaveBeenCalledWith({}));
    expect(await screen.findByTestId("test-email-result")).toHaveTextContent("Test email sent to owner@cryptopay.test.");

    await userEvent.type(screen.getByLabelText("Test recipient"), "bounce@example.com{Enter}");
    await waitFor(() => expect(api.sendTestEmail).toHaveBeenLastCalledWith({ to: "bounce@example.com" }));
    expect(await screen.findByText("Sending failed: 550 5.1.1 <bounce@example.com>: Recipient address rejected")).toBeInTheDocument();
    // Enter in the test field does not submit the settings form.
    expect(api.updateSettings).not.toHaveBeenCalled();

    await userEvent.click(send);
    expect(await screen.findByText("Email is not configured, so no test email can be sent.")).toBeInTheDocument();
  });

  it("is read-only for viewers, including the test email", async () => {
    renderAdmin(<SettingsForm settings={SETTINGS} />, { role: "viewer" });
    expect(screen.getByRole("switch", { name: "Require two-step verification for every admin" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Send test email" })).toBeDisabled();
    expect(screen.getByLabelText("Alert recipients")).toBeDisabled();
  });
});
