import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Overview } from "@/lib/api/types";
import { renderAdmin } from "@/test/admin";
import { MerchantOverviewSection } from "./merchant-overview";

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
  usePathname: () => "/admin/merchants/m-1",
  useSearchParams: () => new URLSearchParams(nav.state.search),
  useParams: () => ({}),
}));

const api = vi.hoisted(() => ({ getOverview: vi.fn() }));
vi.mock("@/lib/api/admin", () => ({ adminApi: api }));

function overview(overrides: Partial<Overview> = {}): Overview {
  return {
    range_days: 30,
    merchant_id: "m-1",
    totals: {
      invoices: 40,
      paid_invoices: 30,
      open_invoices: 2,
      volume_usd: "9000.5",
      conversion_rate: 0.75,
      unmatched_transfers: 0,
      failed_webhooks: 3,
    },
    daily: [
      { date: "2026-11-15", invoices: 3, paid: 2, volume_usd: "150.5" },
      { date: "2026-11-16", invoices: 1, paid: 1, volume_usd: "20" },
    ],
    by_asset: [
      { asset_id: "usdt-tron", symbol: "USDT", chain_id: "tron", chain_name: "Tron", paid: 20, volume_usd: "6000" },
      { asset_id: "usdc-base", symbol: "USDC", chain_id: "base", chain_name: "Base", paid: 6, volume_usd: "2000.5" },
      { asset_id: "eth-ethereum", symbol: "ETH", chain_id: "ethereum", chain_name: "Ethereum", paid: 3, volume_usd: "900" },
      { asset_id: "btc-bitcoin", symbol: "BTC", chain_id: "bitcoin", chain_name: "Bitcoin", paid: 1, volume_usd: "100" },
    ],
    by_status: [{ status: "paid", count: 30 }],
    ...overrides,
  };
}

beforeEach(() => {
  nav.state.search = "";
  nav.replace.mockClear();
  api.getOverview.mockImplementation(async (q: { days?: number }) => overview({ range_days: q.days ?? 30 }));
});

describe("MerchantOverviewSection", () => {
  it("shows the merchant's KPIs, top assets and filtered links", async () => {
    renderAdmin(<MerchantOverviewSection merchantId="m-1" />);
    const section = (await screen.findByRole("heading", { name: "Activity" })).closest("section")!;
    expect(await within(section).findByText("$9,000.50")).toBeInTheDocument();
    expect(api.getOverview).toHaveBeenCalledWith({ days: 30, merchant_id: "m-1" }, expect.anything());
    expect(within(section).getByText("75%")).toBeInTheDocument();

    // Failed webhooks: highlighted, linked to the merchant's failed deliveries.
    const failed = within(section).getByText("Failed webhooks").closest("a")!;
    expect(failed).toHaveAttribute("href", "/admin/webhooks?status=failed&merchant_id=m-1");
    expect(within(failed).getByText("3")).toHaveClass("text-warning");

    // Top three assets by volume.
    expect(within(section).getByText("USDT")).toBeInTheDocument();
    expect(within(section).getByText("ETH")).toBeInTheDocument();
    expect(within(section).queryByText("BTC")).not.toBeInTheDocument();

    expect(within(section).getByRole("link", { name: "View all invoices" })).toHaveAttribute("href", "/admin/invoices?merchant_id=m-1");
    expect(within(section).getByRole("link", { name: "Open in overview" })).toHaveAttribute("href", "/admin?merchant=m-1");
    expect(within(section).getAllByRole("link").map((l) => l.getAttribute("href"))).toEqual(
      expect.arrayContaining(["/admin/invoices?merchant_id=m-1", "/admin/invoices?status=paid&merchant_id=m-1"]),
    );
    // The daily data is there for screen readers.
    expect(within(section).getByRole("table", { name: "Daily payment data" })).toBeInTheDocument();
  });

  it("switches the range through the URL and keeps it in the overview link", async () => {
    const { rerender } = renderAdmin(<MerchantOverviewSection merchantId="m-1" />);
    await screen.findByText("$9,000.50");
    await userEvent.click(screen.getByRole("radio", { name: "Last 7 days" }));
    expect(nav.replace).toHaveBeenLastCalledWith("/admin/merchants/m-1?days=7", { scroll: false });
    await act(async () => rerender(<MerchantOverviewSection merchantId="m-1" />));
    await waitFor(() => expect(api.getOverview).toHaveBeenLastCalledWith({ days: 7, merchant_id: "m-1" }, expect.anything()));
    expect(screen.getByRole("link", { name: "Open in overview" })).toHaveAttribute("href", "/admin?days=7&merchant=m-1");
  });

  it("says when nothing was paid and when loading failed", async () => {
    api.getOverview.mockResolvedValueOnce(
      overview({
        by_asset: [],
        totals: { ...overview().totals, failed_webhooks: 0, paid_invoices: 0, volume_usd: "0" },
        daily: [{ date: "2026-11-16", invoices: 0, paid: 0, volume_usd: "0" }],
      }),
    );
    renderAdmin(<MerchantOverviewSection merchantId="m-1" />);
    expect(await screen.findByText("No paid invoices in this period.")).toBeInTheDocument();
    expect(screen.getByText("No invoices in this period.")).toBeInTheDocument();
    expect(screen.getByText("Failed webhooks").closest("a")!.querySelector(".text-warning")).toBeNull();
  });

  it("shows an error with retry", async () => {
    const { ApiError } = await import("@/lib/api/client");
    api.getOverview.mockRejectedValueOnce(new ApiError({ status: 500, code: "internal", message: "boom" }));
    renderAdmin(<MerchantOverviewSection merchantId="m-1" />);
    await userEvent.click(await screen.findByRole("button", { name: "Try again" }));
    expect(await screen.findByText("$9,000.50")).toBeInTheDocument();
  });
});
