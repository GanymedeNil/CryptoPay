import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { emailFixture, renderAdmin } from "@/test/admin";
import { EmailsPage } from "./emails-page";

// Stateful URL: router.replace updates what useSearchParams returns.
const nav = vi.hoisted(() => {
  const state = { search: "" };
  return {
    state,
    replace: vi.fn((href: string) => {
      state.search = href.includes("?") ? href.slice(href.indexOf("?") + 1) : "";
    }),
  };
});
vi.mock("next/navigation", () => ({
  useRouter: () => ({ replace: nav.replace, push: vi.fn(), refresh: vi.fn(), back: vi.fn(), prefetch: vi.fn() }),
  usePathname: () => "/admin/emails",
  useSearchParams: () => new URLSearchParams(nav.state.search),
  useParams: () => ({}),
}));

const api = vi.hoisted(() => ({
  listEmailOutbox: vi.fn(),
  getEmailMessage: vi.fn(),
  retryEmailMessage: vi.fn(),
  getEmailStatus: vi.fn(),
  listAuditLogs: vi.fn(),
}));
vi.mock("@/lib/api/admin", () => ({ adminApi: api }));

const FAILED = emailFixture();
const SENT = emailFixture({
  id: "e-2",
  kind: "security.new_login",
  to: "owner@cryptopay.test",
  subject: "[CryptoPay] New sign-in to your account",
  status: "sent",
  attempts: 1,
  last_error: "",
  sent_at: "2026-11-16T07:00:05Z",
});

beforeEach(() => {
  nav.state.search = "";
  api.listEmailOutbox.mockResolvedValue({ items: [FAILED, SENT], total: 2, page: 1, page_size: 20 });
  api.getEmailStatus.mockResolvedValue({ configured: true, from: "CryptoPay <no-reply@cryptopay.local>", host: "mailpit" });
});

const rowOf = (subject: string) => screen.getByText(subject).closest("tr")!;

describe("EmailsPage", () => {
  it("lists the outbox with readable kinds and statuses", async () => {
    renderAdmin(<EmailsPage />);
    expect(await screen.findByText(FAILED.subject)).toBeInTheDocument();
    expect(within(rowOf(FAILED.subject)).getByText("Webhook failed")).toBeInTheDocument();
    expect(within(rowOf(FAILED.subject)).getByText("Failed")).toBeInTheDocument();
    expect(within(rowOf(SENT.subject)).getByText("New sign-in IP")).toBeInTheDocument();
    expect(within(rowOf(SENT.subject)).getByText("Sent")).toBeInTheDocument();
    expect(api.listEmailOutbox).toHaveBeenCalledWith({ page: 1, page_size: 20, status: undefined, kind: undefined }, expect.anything());
  });

  it("filters by status and kind through the URL", async () => {
    const { rerender } = renderAdmin(<EmailsPage />);
    await screen.findByText(FAILED.subject);
    await userEvent.click(screen.getByRole("combobox", { name: "Status" }));
    await userEvent.click(await screen.findByRole("option", { name: "Failed" }));
    expect(nav.replace).toHaveBeenLastCalledWith("/admin/emails?status=failed", { scroll: false });
    rerender(<EmailsPage />);
    await waitFor(() => expect(api.listEmailOutbox).toHaveBeenLastCalledWith(expect.objectContaining({ status: "failed" }), expect.anything()));

    await userEvent.click(screen.getByRole("combobox", { name: "Type" }));
    await userEvent.click(await screen.findByRole("option", { name: "Operational alerts · Invoice underpaid" }));
    expect(nav.replace).toHaveBeenLastCalledWith("/admin/emails?status=failed&kind=alert.invoice_underpaid", { scroll: false });
  });

  it("loads the body when a row is expanded and retries a failed message", async () => {
    api.getEmailMessage.mockResolvedValue({ ...FAILED, body_text: "Hello,\n\nWebhook delivery failed." });
    api.retryEmailMessage.mockResolvedValue({ ...FAILED, status: "pending" });
    renderAdmin(<EmailsPage />);
    await screen.findByText(FAILED.subject);
    expect(within(rowOf(SENT.subject)).queryByRole("button", { name: "Retry" })).not.toBeInTheDocument();

    await userEvent.click(within(rowOf(FAILED.subject)).getByRole("button", { name: "Show details" }));
    expect(await screen.findByTestId("email-body")).toHaveTextContent("Webhook delivery failed.");
    expect(api.getEmailMessage).toHaveBeenCalledWith("e-1", expect.anything());
    expect(screen.getAllByText(FAILED.last_error).length).toBeGreaterThan(0);

    await userEvent.click(within(rowOf(FAILED.subject)).getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(api.retryEmailMessage).toHaveBeenCalledWith("e-1"));
    await waitFor(() => expect(api.listEmailOutbox).toHaveBeenCalledTimes(2));
  });

  it("warns when email is not configured", async () => {
    api.getEmailStatus.mockResolvedValue({ configured: false, from: "", host: "" });
    renderAdmin(<EmailsPage />);
    expect(await screen.findByText("Email is not configured")).toBeInTheDocument();
  });

  it("is not available to viewers", () => {
    renderAdmin(<EmailsPage />, { role: "viewer" });
    expect(screen.getByText("Owners and Admins only")).toBeInTheDocument();
    expect(api.listEmailOutbox).not.toHaveBeenCalled();
  });
});
