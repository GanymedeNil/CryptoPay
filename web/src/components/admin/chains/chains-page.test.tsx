import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { chainFixture, renderAdmin, walletFixture } from "@/test/admin";
import { ChainsPage } from "./chains-page";

const api = vi.hoisted(() => ({ listChains: vi.fn(), listWallets: vi.fn(), updateChain: vi.fn(), listAuditLogs: vi.fn() }));
vi.mock("@/lib/api/admin", () => ({ adminApi: api }));

beforeEach(() => {
  vi.clearAllMocks();
  api.listChains.mockResolvedValue({
    items: [
      chainFixture({ id: "ethereum", name: "Ethereum", wallet_id: "w-1", merchant_wallets: 1 }),
      chainFixture({ id: "litecoin", name: "Litecoin", family: "utxo", wallet_id: null, payable: false, merchant_wallets: 2 }),
      chainFixture({ id: "base", name: "Base", wallet_id: null, payable: false }),
    ],
  });
  api.listWallets.mockResolvedValue({ items: [walletFixture({ id: "w-1", name: "Ledger main" })] });
  api.listAuditLogs.mockResolvedValue({ items: [], total: 0, page: 1, page_size: 20 });
});

function chainRow(name: string) {
  return screen.getByRole("heading", { name, level: 3 }).closest("li")!;
}

describe("ChainsPage", () => {
  it("explains that merchants with their own wallet are paid without a default wallet", async () => {
    renderAdmin(<ChainsPage />);
    await screen.findByRole("heading", { name: "Litecoin", level: 3 });

    const ltc = chainRow("Litecoin");
    expect(within(ltc).getByText(/Not payable/)).toHaveTextContent("no default wallet");
    expect(within(ltc).getByTestId("merchant-wallets")).toHaveTextContent("merchants with their own wallet can still be paid (2)");
    expect(within(chainRow("Ethereum")).getByTestId("merchant-wallets")).toHaveTextContent("Merchant wallets: 1");
    expect(within(chainRow("Base")).queryByTestId("merchant-wallets")).not.toBeInTheDocument();
  });

  it("labels the wallet select as the default wallet", async () => {
    renderAdmin(<ChainsPage />);
    await screen.findByRole("heading", { name: "Ethereum", level: 3 });
    const eth = chainRow("Ethereum");
    await userEvent.click(within(eth).getByRole("button", { name: /Configure/ }));
    expect(within(eth).getByRole("heading", { name: "Default receiving wallet" })).toBeInTheDocument();
    await waitFor(() => expect(within(eth).getByRole("combobox", { name: "Default receiving wallet" })).toHaveTextContent("Ledger main"));
    expect(within(eth).getByText(/Merchants without their own wallet are paid here/)).toBeInTheDocument();
    expect(within(eth).getByText(/Merchants with their own wallet on this chain \(1, set on the merchant page\)/)).toBeInTheDocument();
  });
});
