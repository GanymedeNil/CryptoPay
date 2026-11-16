import { act, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { CheckoutApi } from "@/lib/api/checkout";
import { ApiError } from "@/lib/api/client";
import type { CheckoutView } from "@/lib/api/types";
import { redirectTo } from "@/lib/browser";
import { INVOICE_ID, NOW, quote, transfer, view } from "@/test/fixtures";
import { renderWithProviders } from "@/test/render";
import { CheckoutPage } from "./checkout-page";

vi.mock("@/lib/browser", () => ({ redirectTo: vi.fn() }));

type Step = CheckoutView | ApiError;

let serverSkewMs = 0;

/**
 * A fake checkout API answering GETs from a script (the last answer repeats).
 * `server_time` is stamped at answer time, `serverSkewMs` ahead of the client.
 */
function fakeApi(...script: Step[]) {
  let i = 0;
  const get = vi.fn(async () => {
    const step = script[Math.min(i++, script.length - 1)];
    if (step instanceof ApiError) throw step;
    const v = structuredClone(step);
    v.server_time = new Date(Date.now() + serverSkewMs).toISOString();
    return v;
  });
  const createQuote = vi.fn<CheckoutApi["createQuote"]>();
  return { get, createQuote } as unknown as CheckoutApi & {
    get: typeof get;
    createQuote: typeof createQuote;
  };
}

const notFound = () => new ApiError({ status: 404, code: "not_found", message: "invoice not found" });
const offline = () => new ApiError({ status: 0, code: "network_error", message: "Failed to fetch" });

const evmQuote = quote({
  id: "q-evm",
  asset_id: "usdt-ethereum",
  family: "evm",
  chain_id: "ethereum",
  chain_name: "Ethereum",
  network_id: "1",
  contract_address: "0xdAC17F958D2ee523a2206206994597C13D831ec7",
  address: "0x742d35Cc6634C0532925a3b844Bc454e4438f44e",
  payment_uri:
    "ethereum:0xdAC17F958D2ee523a2206206994597C13D831ec7@1/transfer?address=0x742d35Cc6634C0532925a3b844Bc454e4438f44e&uint256=99000000",
  required_confirmations: 12,
});

function renderPage(api: CheckoutApi, locale: "en" | "zh" = "en", id = INVOICE_ID) {
  return renderWithProviders(<CheckoutPage invoiceId={id} api={api} />, { locale });
}

/** Let pending promises (fetches) settle under fake timers. */
async function flush(ms = 0) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
}

function setVisibility(state: DocumentVisibilityState) {
  Object.defineProperty(document, "visibilityState", { configurable: true, get: () => state });
  document.dispatchEvent(new Event("visibilitychange"));
}

describe("CheckoutPage screens", () => {
  beforeEach(() => {
    vi.useFakeTimers({ shouldAdvanceTime: false });
    vi.setSystemTime(NOW);
    setVisibility("visible");
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("shows the asset picker grouped by coin with network badges and estimates", async () => {
    renderPage(fakeApi(view("pending")));
    await flush();
    expect(screen.getByRole("heading", { name: "Choose how to pay" })).toBeInTheDocument();
    expect(screen.getByText("Acme Coffee Roasters")).toBeInTheDocument();
    expect(screen.getByText("$99")).toBeInTheDocument();
    // USDT group is expanded by default: its networks are buttons with estimates.
    const tron = screen.getByRole("button", { name: "Tron · TRC-20 ≈ 99.00 USDT" });
    expect(tron).toBeEnabled();
    expect(screen.getByRole("button", { name: /Ethereum · ERC-20/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Tron Nile Testnet · TRC-20 Testnet ≈ 99.00 USDT" })).toBeInTheDocument();
    // BTC has a single network: one direct button.
    expect(screen.getByRole("button", { name: "BTC Bitcoin ≈ 0.00101799 BTC" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "USDT Tether USD · 3 networks" })).toHaveAttribute("aria-expanded", "true");
  });

  it("creates a quote when an asset is picked and shows payment details", async () => {
    const api = fakeApi(view("pending"));
    api.createQuote.mockResolvedValue(view("pending", { quote: evmQuote }));
    const user = userEvent.setup({ delay: null });
    renderPage(api);
    await flush();
    await user.click(screen.getByRole("button", { name: /Ethereum · ERC-20/ }));
    await flush();
    expect(api.createQuote).toHaveBeenCalledWith(INVOICE_ID, "usdt-ethereum");
    expect(screen.getByRole("heading", { name: "Amount to send" })).toBeInTheDocument();
    expect(screen.getByText("0x742d35")).toBeInTheDocument();
    expect(screen.getByText(/Only send USDT on the Ethereum network/)).toBeInTheDocument();
    expect(screen.queryByText("Send the exact amount")).not.toBeInTheDocument();
    // Desktop default: the QR tab, with a payment-request / address toggle.
    expect(screen.getByRole("img", { name: "QR code for the USDT payment" })).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "Payment request" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Change coin/ })).toBeInTheDocument();
  });

  it("explains a 409 when the coin can't be changed any more", async () => {
    const api = fakeApi(view("pending", { quote: evmQuote }));
    api.createQuote.mockRejectedValue(new ApiError({ status: 409, code: "conflict", message: "funds detected" }));
    const user = userEvent.setup({ delay: null });
    renderPage(api);
    await flush();
    await user.click(screen.getByRole("button", { name: /Change coin/ }));
    expect(screen.getByRole("button", { name: /Back to current payment/ })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /^Tron · TRC-20/ }));
    await flush();
    expect(screen.getByRole("alert")).toHaveTextContent("A payment in another coin was already detected");
  });

  it("warns that a static-wallet amount must be exact", async () => {
    renderPage(fakeApi(view("pending", { quote: quote({ wallet_kind: "static", amount: "99.07", amount_base: "99070000" }) })));
    await flush();
    expect(screen.getByRole("heading", { name: "Send exactly" })).toBeInTheDocument();
    expect(screen.getByText("Send the exact amount")).toBeInTheDocument();
    expect(screen.getByText(/The amount 99\.07 USDT identifies your payment/)).toBeInTheDocument();
  });

  it("shows received vs remaining for a partial payment and keeps the pay UI", async () => {
    const q = quote({
      amount_received: "40",
      amount_received_base: "40000000",
      amount_remaining: "59",
      amount_remaining_base: "59000000",
    });
    renderPage(fakeApi(view("partial", { quote: q, transfers: [transfer({ amount: "40", amount_base: "40000000" })] })));
    await flush();
    const slip = screen.getByRole("article");
    expect(within(slip).getByText("Partial payment received")).toBeInTheDocument();
    expect(screen.getByText(/We received 40 USDT\. Send the remaining 59 USDT/)).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Remaining to send" })).toBeInTheDocument();
    expect(screen.getByText("4 of 19 confirmations")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Change coin/ })).not.toBeInTheDocument();
  });

  it("shows confirmation progress and explorer links while confirming", async () => {
    renderPage(fakeApi(view("confirming", { quote: quote(), transfers: [transfer({ confirmations: 7 })] })));
    await flush();
    expect(screen.getByRole("heading", { name: "Payment detected" })).toBeInTheDocument();
    expect(screen.getByText("7 of 19 confirmations")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /View on explorer/ })).toHaveAttribute(
      "href",
      `https://tronscan.org/#/transaction/${"a".repeat(64)}`,
    );
    // No countdown once the full amount is seen.
    expect(screen.queryByText("Time left")).not.toBeInTheDocument();
  });

  it("shows success, the overpaid note, and redirects to success_url unless the customer stays", async () => {
    const paid = view("paid", {
      invoice: { overpaid: true, paid_at: "2026-11-16T07:59:00Z" },
      quote: quote({ status: "paid", amount_confirmed: "108.9", amount_confirmed_base: "108900000" }),
      transfers: [transfer({ status: "confirmed", confirmations: 19 })],
    });
    renderPage(fakeApi(paid));
    await flush();
    expect(screen.getByRole("heading", { name: "Payment complete" })).toBeInTheDocument();
    expect(screen.getByText("108.9 USDT")).toBeInTheDocument();
    expect(screen.getByText(/You sent more than the amount due/)).toBeInTheDocument();
    expect(screen.getByText("Returning to the merchant in 6 s")).toBeInTheDocument();
    await flush(6000);
    expect(redirectTo).toHaveBeenCalledWith("https://shop.example/thanks");
  });

  it("lets the customer cancel the redirect", async () => {
    vi.mocked(redirectTo).mockClear();
    const user = userEvent.setup({ delay: null });
    renderPage(fakeApi(view("paid", { quote: quote({ status: "paid" }) })));
    await flush();
    await user.click(screen.getByRole("button", { name: "Stay on this page" }));
    await flush(10_000);
    expect(redirectTo).not.toHaveBeenCalled();
    expect(screen.getByRole("link", { name: "Return to merchant" })).toHaveAttribute("href", "https://shop.example/thanks");
  });

  it("shows expired with a manual re-check", async () => {
    const api = fakeApi(view("expired"), view("paid", { quote: quote({ status: "paid" }) }));
    const user = userEvent.setup({ delay: null });
    renderPage(api);
    await flush();
    expect(screen.getByRole("heading", { name: "This payment has expired" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Return to merchant/ })).toHaveAttribute("href", "https://shop.example/cart");
    await flush(30_000);
    expect(api.get).toHaveBeenCalledTimes(1); // terminal: no polling
    await user.click(screen.getByRole("button", { name: "Check again" }));
    await flush();
    expect(api.get).toHaveBeenCalledTimes(2);
    expect(screen.getByRole("heading", { name: "Payment complete" })).toBeInTheDocument();
  });

  it("shows underpaid and cancelled", async () => {
    const { unmount } = renderPage(
      fakeApi(view("underpaid", { quote: quote({ amount_confirmed: "49.5", amount_confirmed_base: "49500000" }) })),
    );
    await flush();
    expect(screen.getByRole("heading", { name: "Payment incomplete" })).toBeInTheDocument();
    expect(screen.getByText(/We confirmed 49\.5 USDT, less than the 99 USDT due/)).toBeInTheDocument();
    unmount();

    renderPage(fakeApi(view("cancelled", { invoice: { cancel_url: "javascript:alert(1)" } })));
    await flush();
    expect(screen.getByRole("heading", { name: "Payment cancelled" })).toBeInTheDocument();
    // Unsafe merchant URLs are never rendered as links.
    expect(screen.queryByRole("link", { name: /Return to merchant/ })).not.toBeInTheDocument();
  });

  it("shows not found for a 404 and for malformed ids, without polling", async () => {
    const api = fakeApi(notFound());
    const { unmount } = renderPage(api);
    await flush();
    expect(screen.getByRole("heading", { name: "Payment not found" })).toBeInTheDocument();
    await flush(20_000);
    expect(api.get).toHaveBeenCalledTimes(1);
    unmount();

    const api2 = fakeApi(view());
    renderPage(api2, "en", "not-a-uuid");
    await flush();
    expect(screen.getByRole("heading", { name: "Payment not found" })).toBeInTheDocument();
    expect(api2.get).not.toHaveBeenCalled();
  });

  it("shows a network error with retry, then recovers", async () => {
    const api = fakeApi(offline(), view("pending"));
    const user = userEvent.setup({ delay: null });
    renderPage(api);
    await flush();
    expect(screen.getByRole("heading", { name: "Can't reach the payment server" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Try again" }));
    await flush();
    expect(screen.getByRole("heading", { name: "Choose how to pay" })).toBeInTheDocument();
  });

  it("renders in Chinese", async () => {
    renderPage(fakeApi(view("pending", { quote: quote({ wallet_kind: "static" }) })), "zh");
    await flush();
    expect(screen.getByRole("heading", { name: "请转账精确金额" })).toBeInTheDocument();
    expect(screen.getByText("务必转账精确金额")).toBeInTheDocument();
    expect(screen.getByText("收款方")).toBeInTheDocument();
  });

  it("announces the current status to screen readers", async () => {
    renderPage(fakeApi(view("confirming", { quote: quote(), transfers: [transfer()] })));
    await flush();
    const live = screen.getAllByRole("status").find((el) => el.getAttribute("aria-live") === "polite")!;
    expect(within(live).getByText("Payment detected")).toBeInTheDocument();
  });
});

describe("CheckoutPage polling", () => {
  beforeEach(() => {
    vi.useFakeTimers({ shouldAdvanceTime: false });
    vi.setSystemTime(NOW);
    setVisibility("visible");
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("polls every 3 s while open and stops once paid", async () => {
    const pending = view("pending", { quote: quote() });
    const confirming = view("confirming", { quote: quote(), transfers: [transfer()] });
    const paid = view("paid", { quote: quote({ status: "paid" }), invoice: { success_url: "" } });
    const api = fakeApi(pending, pending, confirming, paid);
    renderPage(api);
    await flush();
    expect(api.get).toHaveBeenCalledTimes(1);
    await flush(2999);
    expect(api.get).toHaveBeenCalledTimes(1);
    await flush(1);
    expect(api.get).toHaveBeenCalledTimes(2);
    await flush(3000);
    expect(api.get).toHaveBeenCalledTimes(3);
    expect(screen.getByRole("heading", { name: "Payment detected" })).toBeInTheDocument();
    await flush(3000);
    expect(api.get).toHaveBeenCalledTimes(4);
    expect(screen.getByRole("heading", { name: "Payment complete" })).toBeInTheDocument();
    expect(screen.getByText("You can close this page now.")).toBeInTheDocument();
    await flush(30_000);
    expect(api.get).toHaveBeenCalledTimes(4);
  });

  it("pauses while the tab is hidden and refreshes as soon as it is visible", async () => {
    const api = fakeApi(view("pending", { quote: quote() }));
    renderPage(api);
    await flush();
    expect(api.get).toHaveBeenCalledTimes(1);
    act(() => setVisibility("hidden"));
    await flush(30_000);
    expect(api.get).toHaveBeenCalledTimes(1);
    act(() => setVisibility("visible"));
    await flush();
    expect(api.get).toHaveBeenCalledTimes(2);
    await flush(3000);
    expect(api.get).toHaveBeenCalledTimes(3);
  });

  it("backs off on errors and shows a reconnecting notice while keeping the last data", async () => {
    const api = fakeApi(view("pending", { quote: quote() }), offline(), offline(), view("pending", { quote: quote() }));
    renderPage(api);
    await flush();
    await flush(3000); // 2nd call fails
    expect(api.get).toHaveBeenCalledTimes(2);
    expect(screen.getByText("Connection lost. Retrying…")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Amount to send" })).toBeInTheDocument();
    await flush(5999); // backoff: 3 s × 2¹
    expect(api.get).toHaveBeenCalledTimes(2);
    await flush(1);
    expect(api.get).toHaveBeenCalledTimes(3); // fails again → 12 s
    await flush(12_000);
    expect(api.get).toHaveBeenCalledTimes(4);
    expect(screen.queryByText("Connection lost. Retrying…")).not.toBeInTheDocument();
  });

  it("corrects the countdown with server_time", async () => {
    // The customer's clock is 2 minutes slow; the server says 5 minutes are left.
    vi.setSystemTime(NOW - 120_000);
    serverSkewMs = 120_000;
    const v = view("pending", {
      quote: quote(),
      invoice: { expires_at: new Date(NOW + 5 * 60_000).toISOString() },
    });
    try {
      renderPage(fakeApi(v));
      await flush();
      expect(screen.getByText("05:00")).toBeInTheDocument(); // not 07:00
      await flush(61_000);
      expect(screen.getByText("03:59")).toBeInTheDocument();
    } finally {
      serverSkewMs = 0;
    }
  });

  it("disables wallet payments locally once the deadline passes", async () => {
    const v = view("pending", {
      quote: quote(),
      invoice: { expires_at: new Date(NOW + 2000).toISOString() },
    });
    renderPage(fakeApi(v));
    await flush();
    await flush(3000);
    expect(screen.getByText("Time's up")).toBeInTheDocument();
    expect(screen.getByText(/Time's up\. If you already sent the payment/)).toBeInTheDocument();
  });
});
