import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { AdminInvoice, Page } from "@/lib/api/types";
import { merchantFixture, renderAdmin } from "@/test/admin";
import { quote } from "@/test/fixtures";
import { InvoicesPage } from "./invoices-page";

// Stateful URL: router.replace updates what useSearchParams returns.
const nav = vi.hoisted(() => {
  const state = { search: "" };
  return {
    state,
    replace: vi.fn((href: string) => {
      state.search = href.includes("?") ? href.slice(href.indexOf("?") + 1) : "";
    }),
    push: vi.fn(),
  };
});
vi.mock("next/navigation", () => ({
  useRouter: () => ({ replace: nav.replace, push: nav.push, refresh: vi.fn(), back: vi.fn(), prefetch: vi.fn() }),
  usePathname: () => "/admin/invoices",
  useSearchParams: () => new URLSearchParams(nav.state.search),
  useParams: () => ({}),
}));

const api = vi.hoisted(() => ({ listInvoices: vi.fn(), listMerchants: vi.fn(), listAssets: vi.fn() }));
vi.mock("@/lib/api/admin", () => ({ adminApi: api }));

function invoice(i: number, overrides: Partial<AdminInvoice> = {}): AdminInvoice {
  return {
    id: `${String(i).padStart(8, "0")}-0000-4000-8000-000000000000`,
    merchant_id: "m-1",
    merchant_name: "Acme Coffee Roasters",
    order_id: `ORD-${i}`,
    title: `Order #${i}`,
    description: "",
    amount: "99.00",
    currency: "USD",
    amount_usd: "99.00",
    status: "paid",
    overpaid: false,
    paid_late: false,
    success_url: "",
    cancel_url: "",
    customer_email: "",
    metadata: {},
    checkout_url: `https://pay.example/pay/${i}`,
    expires_at: "2026-11-16T08:15:00Z",
    paid_at: null,
    created_at: "2026-11-16T07:00:00Z",
    updated_at: "2026-11-16T07:00:00Z",
    quote: quote(),
    ...overrides,
  };
}

function page(items: AdminInvoice[], p: number, total: number): Page<AdminInvoice> {
  return { items, page: p, page_size: 20, total };
}

beforeEach(() => {
  nav.state.search = "";
  api.listMerchants.mockResolvedValue({ items: [merchantFixture()], page: 1, page_size: 100, total: 1 });
  api.listAssets.mockResolvedValue({ items: [] });
});

describe("InvoicesPage", () => {
  it("reads filters and page from the URL and passes them to the API", async () => {
    nav.state.search = "status=paid&merchant_id=m-1&page=2&from=2026-11-01";
    api.listInvoices.mockResolvedValue(page([invoice(21), invoice(22, { status: "partial", overpaid: true })], 2, 45));
    renderAdmin(<InvoicesPage />);
    const table = await screen.findByRole("table", { name: "Invoices" });
    expect(within(table).getByText("Order #21")).toBeInTheDocument();
    expect(within(table).getByText("Partially paid")).toBeInTheDocument();
    expect(within(table).getByText("Overpaid")).toBeInTheDocument();
    const query = api.listInvoices.mock.calls[0][0];
    expect(query).toMatchObject({ page: 2, page_size: 20, status: "paid", merchant_id: "m-1" });
    expect(new Date(query.from).getDate()).toBe(1);
    expect(query.q).toBeUndefined();
    expect(screen.getByText("21–40 of 45")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Clear filters" })).toBeInTheDocument();
  });

  it("keeps filters when paging and drops the page when a filter changes", async () => {
    nav.state.search = "status=paid&page=2";
    api.listInvoices.mockResolvedValue(page([invoice(21)], 2, 45));
    renderAdmin(<InvoicesPage />);
    await screen.findByText("Order #21");

    await userEvent.click(screen.getByRole("button", { name: "Next page" }));
    expect(nav.replace).toHaveBeenLastCalledWith("/admin/invoices?status=paid&page=3", { scroll: false });

    nav.state.search = "status=paid&page=3";
    const search = screen.getByRole("searchbox", { name: "Search invoices" });
    fireEvent.change(search, { target: { value: "ORD-21" } });
    fireEvent.keyDown(search, { key: "Enter" });
    expect(nav.replace).toHaveBeenLastCalledWith("/admin/invoices?status=paid&q=ORD-21", { scroll: false });
  });

  it("refetches when the URL changes", async () => {
    api.listInvoices.mockImplementation(async (q: { page?: number }) => page([invoice(q.page === 2 ? 21 : 1)], q.page ?? 1, 30));
    const { rerender } = renderAdmin(<InvoicesPage />);
    await screen.findByText("Order #1");
    await act(async () => {
      nav.state.search = "page=2";
      rerender(<InvoicesPage />);
    });
    expect(await screen.findByText("Order #21")).toBeInTheDocument();
    expect(api.listInvoices).toHaveBeenLastCalledWith(expect.objectContaining({ page: 2 }), expect.anything());
  });

  it("explains an empty result and offers the next step", async () => {
    api.listInvoices.mockResolvedValue(page([], 1, 0));
    renderAdmin(<InvoicesPage />);
    expect(await screen.findByText("No invoices yet")).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: "Create invoice" })).toHaveLength(2);

    nav.state.search = "status=expired";
    renderAdmin(<InvoicesPage />);
    expect(await screen.findByText("No invoices match these filters")).toBeInTheDocument();
  });

  it("hides creation from viewers", async () => {
    api.listInvoices.mockResolvedValue(page([invoice(1)], 1, 1));
    renderAdmin(<InvoicesPage />, { role: "viewer" });
    await screen.findByText("Order #1");
    expect(screen.getByRole("button", { name: "Create invoice" })).toBeDisabled();
  });

  it("shows an error with retry", async () => {
    const { ApiError } = await import("@/lib/api/client");
    api.listInvoices.mockRejectedValueOnce(new ApiError({ status: 500, code: "internal", message: "boom" }));
    api.listInvoices.mockResolvedValueOnce(page([invoice(7)], 1, 1));
    renderAdmin(<InvoicesPage />);
    await userEvent.click(await screen.findByRole("button", { name: "Try again" }));
    await waitFor(() => expect(screen.getByText("Order #7")).toBeInTheDocument());
  });
});
