import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Merchant, MerchantOverview, MerchantOverviewPage, Overview } from "@/lib/api/types";
import { chainFixture, merchantFixture, renderAdmin } from "@/test/admin";
import { OverviewPage } from "./overview-page";

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
  usePathname: () => "/admin",
  useSearchParams: () => new URLSearchParams(nav.state.search),
  useParams: () => ({}),
}));

const api = vi.hoisted(() => ({
  getOverview: vi.fn(),
  listMerchantOverviews: vi.fn(),
  listMerchants: vi.fn(),
  getMerchant: vi.fn(),
  listChains: vi.fn(),
}));
vi.mock("@/lib/api/admin", () => ({ adminApi: api }));

function overview(overrides: Partial<Overview> = {}): Overview {
  return {
    range_days: 30,
    merchant_id: null,
    totals: {
      invoices: 120,
      paid_invoices: 80,
      open_invoices: 4,
      volume_usd: "12345.67",
      conversion_rate: 0.5,
      unmatched_transfers: 3,
      failed_webhooks: 2,
    },
    daily: [
      { date: "2026-11-15", invoices: 3, paid: 2, volume_usd: "150.5" },
      { date: "2026-11-16", invoices: 1, paid: 0, volume_usd: "0" },
    ],
    by_asset: [{ asset_id: "usdt-tron", symbol: "USDT", chain_id: "tron", chain_name: "Tron", paid: 2, volume_usd: "150.5" }],
    by_status: [
      { status: "paid", count: 80 },
      { status: "expired", count: 40 },
    ],
    ...overrides,
  };
}

function row(overrides: Partial<MerchantOverview> = {}): MerchantOverview {
  return {
    merchant_id: "m-1",
    merchant_name: "Acme Coffee Roasters",
    status: "active",
    invoices: 40,
    paid_invoices: 30,
    volume_usd: "9000.5",
    conversion_rate: 0.75,
    open_invoices: 2,
    failed_webhooks: 0,
    last_invoice_at: "2026-11-16T07:00:00Z",
    last_paid_at: "2026-11-16T07:10:00Z",
    ...overrides,
  };
}

const ROWS: MerchantOverview[] = [
  row(),
  row({ merchant_id: "m-2", merchant_name: "Nimbus VPN", volume_usd: "120", failed_webhooks: 12, conversion_rate: 0.1 }),
  row({
    merchant_id: "m-3",
    merchant_name: "Old Storefront (closed)",
    status: "disabled",
    invoices: 0,
    paid_invoices: 0,
    volume_usd: "0",
    conversion_rate: 0,
    open_invoices: 0,
    last_invoice_at: null,
    last_paid_at: null,
  }),
];

function rowsPage(items: MerchantOverview[] = ROWS, total = items.length): MerchantOverviewPage {
  return { items, total, page: 1, page_size: 20, range_days: 30 };
}

const MERCHANTS: Merchant[] = [
  merchantFixture(),
  merchantFixture({ id: "m-2", name: "Nimbus VPN" }),
  merchantFixture({ id: "m-3", name: "Old Storefront (closed)", status: "disabled" }),
];

function hrefs(): string[] {
  return screen.getAllByRole("link").map((a) => a.getAttribute("href") ?? "");
}

beforeEach(() => {
  nav.state.search = "";
  nav.replace.mockClear();
  api.getOverview.mockImplementation(async (q: { days?: number; merchant_id?: string }) =>
    overview({ range_days: q.days ?? 30, merchant_id: q.merchant_id ?? null }),
  );
  api.listMerchantOverviews.mockResolvedValue(rowsPage());
  api.listMerchants.mockResolvedValue({ items: MERCHANTS, page: 1, page_size: 100, total: MERCHANTS.length });
  api.listChains.mockResolvedValue({ items: [chainFixture()] });
  api.getMerchant.mockReset();
});

describe("OverviewPage merchant filter", () => {
  it("reads range and merchant from the URL and requests the filtered overview", async () => {
    nav.state.search = "days=7&merchant=m-1";
    renderAdmin(<OverviewPage />);
    expect(await screen.findByText("$12,345.67")).toBeInTheDocument();
    expect(api.getOverview).toHaveBeenCalledWith({ days: 7, merchant_id: "m-1" }, expect.anything());
    // The chip names the merchant in view; the picker shows it too.
    const chip = screen.getByText("Viewing").parentElement!;
    expect(await within(chip).findByRole("link", { name: "Acme Coffee Roasters" })).toHaveAttribute("href", "/admin/merchants/m-1");
    expect(screen.getByRole("combobox", { name: "Merchant: Acme Coffee Roasters" })).toBeInTheDocument();
    // No per-merchant comparison while one merchant is in view.
    expect(screen.queryByRole("heading", { name: "By merchant" })).not.toBeInTheDocument();
    expect(api.listMerchantOverviews).not.toHaveBeenCalled();
  });

  it("carries the merchant into every link and hides what is not attributable", async () => {
    nav.state.search = "merchant=m-2";
    renderAdmin(<OverviewPage />);
    await screen.findByText("$12,345.67");
    expect(hrefs()).toEqual(
      expect.arrayContaining([
        "/admin/invoices?merchant_id=m-2",
        "/admin/invoices?status=paid&merchant_id=m-2",
        "/admin/webhooks?status=failed&merchant_id=m-2",
        "/admin/invoices?status=expired&merchant_id=m-2",
      ]),
    );
    expect(hrefs()).not.toContain("/admin/transfers?matched=false");
    expect(screen.queryByText("Unmatched transfers")).not.toBeInTheDocument();
    expect(screen.getByText(/Unmatched transfers belong to no merchant/)).toBeInTheDocument();
    // Chain monitoring stays global and says so.
    const chains = screen.getByRole("heading", { name: /Chain monitoring/ });
    expect(within(chains).getByText("System-wide")).toBeInTheDocument();
    expect(await screen.findByText(/ignores the merchant filter/)).toBeInTheDocument();
  });

  it("shows unmatched transfers and unfiltered links without a merchant", async () => {
    renderAdmin(<OverviewPage />);
    await screen.findByText("$12,345.67");
    expect(api.getOverview).toHaveBeenCalledWith({ days: 30, merchant_id: undefined }, expect.anything());
    expect(screen.getByText("Unmatched transfers")).toBeInTheDocument();
    expect(hrefs()).toEqual(expect.arrayContaining(["/admin/invoices", "/admin/webhooks?status=failed", "/admin/transfers?matched=false"]));
    expect(screen.queryByText("System-wide")).not.toBeInTheDocument();
    expect(screen.queryByText("Viewing")).not.toBeInTheDocument();
  });

  it("writes the picked merchant to the URL and clears it in one click", async () => {
    nav.state.search = "days=7";
    const { rerender } = renderAdmin(<OverviewPage />);
    await screen.findByText("$12,345.67");
    await userEvent.click(screen.getByRole("combobox", { name: "Merchant: All merchants" }));
    await userEvent.click(await screen.findByRole("option", { name: /Nimbus VPN/ }));
    expect(nav.replace).toHaveBeenLastCalledWith("/admin?days=7&merchant=m-2", { scroll: false });

    await act(async () => rerender(<OverviewPage />));
    await waitFor(() => expect(api.getOverview).toHaveBeenLastCalledWith({ days: 7, merchant_id: "m-2" }, expect.anything()));
    await userEvent.click(await screen.findByRole("button", { name: "Clear merchant filter" }));
    expect(nav.replace).toHaveBeenLastCalledWith("/admin?days=7", { scroll: false });
  });

  it("offers a search box when there are many merchants", async () => {
    const many = Array.from({ length: 12 }, (_, i) => merchantFixture({ id: `m-${i + 10}`, name: `Shop ${String.fromCharCode(65 + i)}` }));
    api.listMerchants.mockResolvedValue({ items: [...MERCHANTS, ...many], page: 1, page_size: 100, total: 15 });
    renderAdmin(<OverviewPage />);
    await screen.findByText("$12,345.67");
    await userEvent.click(screen.getByRole("combobox", { name: /Merchant/ }));
    const search = await screen.findByRole("searchbox", { name: "Search merchants" });
    await userEvent.type(search, "nimb");
    expect(screen.getAllByRole("option").map((o) => o.textContent)).toEqual(["Nimbus VPN"]);
    await userEvent.type(search, "{Enter}");
    expect(nav.replace).toHaveBeenLastCalledWith("/admin?merchant=m-2", { scroll: false });

    await userEvent.click(screen.getByRole("combobox", { name: /Merchant/ }));
    await userEvent.type(await screen.findByRole("searchbox", { name: "Search merchants" }), "zzz");
    expect(screen.getByText("No matching merchant")).toBeInTheDocument();
  });

  it("names a merchant that is not in the first page of the merchant list", async () => {
    api.getMerchant.mockResolvedValue({ merchant: merchantFixture({ id: "m-99", name: "Far Away Shop" }), api_keys: [], wallets: [] });
    nav.state.search = "merchant=m-99";
    renderAdmin(<OverviewPage />);
    const chip = (await screen.findByText("Viewing")).parentElement!;
    expect(await within(chip).findByRole("link", { name: "Far Away Shop" })).toBeInTheDocument();
    expect(api.getMerchant).toHaveBeenCalledWith("m-99", expect.anything());
  });
});

describe("OverviewPage by-merchant comparison", () => {
  it("lists every merchant with server-side sort and paging", async () => {
    renderAdmin(<OverviewPage />);
    const table = await screen.findByRole("table", { name: "By merchant" });
    expect(api.listMerchantOverviews).toHaveBeenCalledWith(
      { days: 30, sort: "volume_usd", order: "desc", page: 1, page_size: 20 },
      expect.anything(),
    );
    const acme = within(table).getByRole("link", { name: "Acme Coffee Roasters" });
    expect(acme).toHaveAttribute("href", "/admin?merchant=m-1");
    const acmeRow = acme.closest("tr")!;
    expect(within(acmeRow).getByText("$9,000.50")).toBeInTheDocument();
    expect(within(acmeRow).getByText("75%")).toBeInTheDocument();
    expect(within(table).getByRole("columnheader", { name: /Volume/ })).toHaveAttribute("aria-sort", "descending");
    expect(within(table).getByRole("columnheader", { name: /Invoices/ })).toHaveAttribute("aria-sort", "none");

    // Failed webhooks stand out and open the merchant's failed deliveries.
    const failed = within(table).getByRole("link", { name: "View failed webhooks of Nimbus VPN (12)" });
    expect(failed).toHaveAttribute("href", "/admin/webhooks?status=failed&merchant_id=m-2");
    expect(failed).toHaveClass("text-warning");

    // Idle merchants are muted and say so.
    const idleRow = within(table).getByRole("link", { name: "Old Storefront (closed)" }).closest("tr")!;
    expect(within(idleRow).getByText("No activity")).toBeInTheDocument();
    expect(within(idleRow).getByText("Disabled")).toBeInTheDocument();
    expect(within(idleRow).getByText("Never")).toBeInTheDocument();
    expect(idleRow).toHaveClass("opacity-70");
    expect(acmeRow).not.toHaveClass("opacity-70");
  });

  it("sorts on the server when a column header is clicked", async () => {
    const { rerender } = renderAdmin(<OverviewPage />);
    const table = await screen.findByRole("table", { name: "By merchant" });

    await userEvent.click(within(table).getByRole("button", { name: "Invoices" }));
    expect(nav.replace).toHaveBeenLastCalledWith("/admin?sort=invoices", { scroll: false });
    await act(async () => rerender(<OverviewPage />));
    await waitFor(() =>
      expect(api.listMerchantOverviews).toHaveBeenLastCalledWith(
        expect.objectContaining({ sort: "invoices", order: "desc", page: 1 }),
        expect.anything(),
      ),
    );

    // Same column again: ascending.
    await userEvent.click(within(table).getByRole("button", { name: "Invoices" }));
    expect(nav.replace).toHaveBeenLastCalledWith("/admin?sort=invoices&order=asc", { scroll: false });
    await act(async () => rerender(<OverviewPage />));
    await waitFor(() =>
      expect(api.listMerchantOverviews).toHaveBeenLastCalledWith(expect.objectContaining({ sort: "invoices", order: "asc" }), expect.anything()),
    );
    expect(within(table).getByRole("columnheader", { name: /Invoices/ })).toHaveAttribute("aria-sort", "ascending");

    // Names start A→Z; a sort change drops the page number.
    nav.state.search = "page=3";
    await act(async () => rerender(<OverviewPage />));
    await userEvent.click(within(screen.getByRole("table", { name: "By merchant" })).getByRole("button", { name: "Merchant" }));
    expect(nav.replace).toHaveBeenLastCalledWith("/admin?sort=name", { scroll: false });
  });

  it("pages through merchants and keeps the range", async () => {
    nav.state.search = "days=90";
    api.listMerchantOverviews.mockResolvedValue(rowsPage(ROWS, 45));
    renderAdmin(<OverviewPage />);
    const table = await screen.findByRole("table", { name: "By merchant" });
    expect(within(table).getByRole("link", { name: "Acme Coffee Roasters" })).toHaveAttribute("href", "/admin?days=90&merchant=m-1");
    await userEvent.click(screen.getByRole("button", { name: "Next page" }));
    expect(nav.replace).toHaveBeenLastCalledWith("/admin?days=90&page=2", { scroll: false });
  });

  it("explains an empty list and a failed load", async () => {
    api.listMerchantOverviews.mockResolvedValue(rowsPage([]));
    renderAdmin(<OverviewPage />);
    expect(await screen.findByText("No merchants yet")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Manage merchants" })).toHaveAttribute("href", "/admin/merchants");
  });

  it("retries a failed load", async () => {
    const { ApiError } = await import("@/lib/api/client");
    api.listMerchantOverviews.mockRejectedValueOnce(new ApiError({ status: 500, code: "internal", message: "boom" }));
    api.listMerchantOverviews.mockResolvedValueOnce(rowsPage());
    renderAdmin(<OverviewPage />);
    await userEvent.click(await screen.findByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("link", { name: "Nimbus VPN" })).toBeInTheDocument();
  });
});
