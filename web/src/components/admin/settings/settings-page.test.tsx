import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ApiError } from "@/lib/api/client";
import { renderAdmin, SETTINGS } from "@/test/admin";
import { SettingsForm } from "./settings-page";

const api = vi.hoisted(() => ({ updateSettings: vi.fn(), listAuditLogs: vi.fn() }));
vi.mock("@/lib/api/admin", () => ({ adminApi: api }));

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
