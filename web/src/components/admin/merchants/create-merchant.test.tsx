import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ApiError } from "@/lib/api/client";
import { merchantFixture, renderAdmin } from "@/test/admin";
import { CreateMerchantDialog } from "./create-merchant-dialog";

const nav = vi.hoisted(() => ({ push: vi.fn(), replace: vi.fn() }));
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: nav.push, replace: nav.replace, refresh: vi.fn(), back: vi.fn(), prefetch: vi.fn() }),
  usePathname: () => "/admin/merchants",
  useSearchParams: () => new URLSearchParams(),
  useParams: () => ({}),
}));

const api = vi.hoisted(() => ({ createMerchant: vi.fn(), listAuditLogs: vi.fn() }));
vi.mock("@/lib/api/admin", () => ({ adminApi: api }));

describe("create merchant", () => {
  it("validates, shows the webhook secret exactly once, then opens the merchant", async () => {
    api.createMerchant.mockResolvedValue({
      merchant: merchantFixture({ id: "m-new", name: "Harbor Tea" }),
      webhook_secret: "whsec_0123456789abcdef",
    });
    const onOpenChange = vi.fn();
    const { rerender } = renderAdmin(<CreateMerchantDialog open onOpenChange={onOpenChange} />);
    const form = screen.getByRole("dialog", { name: "Create merchant" });

    await userEvent.click(within(form).getByRole("button", { name: "Create merchant" }));
    expect(await within(form).findByText("Enter a name")).toBeInTheDocument();
    expect(api.createMerchant).not.toHaveBeenCalled();

    await userEvent.type(within(form).getByLabelText("Name"), "Harbor Tea");
    await userEvent.type(within(form).getByLabelText(/Webhook URL/), "https://tea.example/hook");
    await userEvent.click(within(form).getByRole("button", { name: "Create merchant" }));
    expect(api.createMerchant).toHaveBeenCalledWith({ name: "Harbor Tea", webhook_url: "https://tea.example/hook" });
    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
    rerender(<CreateMerchantDialog open={false} onOpenChange={onOpenChange} />);

    const secret = await screen.findByRole("dialog", { name: "Store the webhook signing secret" });
    expect(within(secret).getByTestId("secret-value")).toHaveTextContent("whsec_0123456789abcdef");
    const done = within(secret).getByRole("button", { name: "Done" });
    expect(done).toBeDisabled();
    await userEvent.click(within(secret).getByRole("checkbox"));
    await userEvent.click(done);

    expect(nav.push).toHaveBeenCalledWith("/admin/merchants/m-new");
    await waitFor(() => expect(screen.queryByText("whsec_0123456789abcdef")).not.toBeInTheDocument());
  });

  it("puts server field errors on the fields", async () => {
    api.createMerchant.mockRejectedValue(
      new ApiError({ status: 409, code: "conflict", message: "exists", fields: { name: "already exists" } }),
    );
    renderAdmin(<CreateMerchantDialog open onOpenChange={() => {}} />);
    const form = screen.getByRole("dialog", { name: "Create merchant" });
    await userEvent.type(within(form).getByLabelText("Name"), "Acme Coffee Roasters");
    await userEvent.click(within(form).getByRole("button", { name: "Create merchant" }));
    expect(await within(form).findByText("already exists")).toBeInTheDocument();
    expect(within(form).getByLabelText("Name")).toHaveAttribute("aria-invalid", "true");
    expect(screen.queryByRole("dialog", { name: "Store the webhook signing secret" })).not.toBeInTheDocument();
  });
});
