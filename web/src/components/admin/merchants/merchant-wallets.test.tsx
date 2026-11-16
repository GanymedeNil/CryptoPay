import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "@/lib/api/client";
import type { MerchantWallet } from "@/lib/api/types";
import { chainFixture, renderAdmin, walletFixture } from "@/test/admin";
import { effectiveWallet, MerchantWallets, merchantWalletChains } from "./merchant-wallets";

const api = vi.hoisted(() => ({
  listChains: vi.fn(),
  listWallets: vi.fn(),
  setMerchantWallet: vi.fn(),
  unsetMerchantWallet: vi.fn(),
  listAuditLogs: vi.fn(),
  getMerchant: vi.fn(),
}));
vi.mock("@/lib/api/admin", () => ({ adminApi: api }));

const DEFAULT = walletFixture({ id: "w-1", name: "Ledger · EVM main account", chain_ids: ["ethereum", "polygon", "sepolia"] });
const OWN = walletFixture({ id: "w-2", name: "Shop treasury", xpub: "xpub6Shop", chain_ids: [] });
const LTC = walletFixture({ id: "w-ltc", name: "Shop LTC", family: "utxo", kind: "static", xpub: "", address: "ltc1qshop", chain_ids: [] });
const CHAINS = [
  chainFixture({ id: "ethereum", name: "Ethereum", wallet_id: "w-1" }),
  chainFixture({ id: "polygon", name: "Polygon", wallet_id: "w-1", merchant_wallets: 1 }),
  chainFixture({ id: "litecoin", name: "Litecoin", family: "utxo", wallet_id: null, payable: false }),
  chainFixture({ id: "base", name: "Base", enabled: false, wallet_id: null }),
  chainFixture({ id: "arbitrum", name: "Arbitrum One", enabled: false, wallet_id: null, merchant_wallets: 1 }),
  chainFixture({ id: "sepolia", name: "Ethereum Sepolia", is_testnet: true, wallet_id: "w-1" }),
];
const BINDINGS: MerchantWallet[] = [
  { chain_id: "polygon", wallet_id: "w-2", wallet_name: "Shop treasury", kind: "xpub" },
  { chain_id: "arbitrum", wallet_id: "w-2", wallet_name: "Shop treasury", kind: "xpub" },
];

beforeEach(() => {
  vi.clearAllMocks();
  api.listChains.mockResolvedValue({ items: CHAINS });
  api.listWallets.mockResolvedValue({ items: [DEFAULT, OWN, LTC] });
  api.setMerchantWallet.mockImplementation((_m: string, chain: string, body: { wallet_id: string }) =>
    Promise.resolve({ chain_id: chain, wallet_id: body.wallet_id, wallet_name: "Shop treasury", kind: "xpub" }),
  );
  api.unsetMerchantWallet.mockResolvedValue(undefined);
  api.listAuditLogs.mockResolvedValue({ items: [], total: 0, page: 1, page_size: 20 });
});

function row(chainId: string) {
  return screen.getByTestId(`merchant-wallet-${chainId}`);
}

describe("merchant wallet helpers", () => {
  it("resolve the effective wallet: own, else the chain's default, else none", () => {
    expect(effectiveWallet(CHAINS[1], BINDINGS[0], [DEFAULT])).toEqual({ source: "own", name: "Shop treasury" });
    expect(effectiveWallet(CHAINS[0], undefined, [DEFAULT])).toEqual({ source: "default", name: "Ledger · EVM main account" });
    expect(effectiveWallet(CHAINS[2], undefined, [DEFAULT])).toEqual({ source: "none", name: null });
  });

  it("list enabled chains plus disabled ones the merchant still has a wallet on", () => {
    expect(merchantWalletChains(CHAINS, BINDINGS).map((c) => c.id)).toEqual(["ethereum", "polygon", "litecoin", "arbitrum", "sepolia"]);
    expect(merchantWalletChains(CHAINS, []).map((c) => c.id)).toEqual(["ethereum", "polygon", "litecoin", "sepolia"]);
  });
});

describe("MerchantWallets", () => {
  it("shows the effective wallet of every chain, mainnets first and testnets grouped", async () => {
    renderAdmin(<MerchantWallets merchantId="m-1" bindings={BINDINGS} />);
    expect(screen.getByRole("heading", { name: "Receiving wallets" })).toBeInTheDocument();
    expect(screen.getByText(/applies to this merchant only/)).toBeInTheDocument();
    expect(screen.getByText(/unpaid invoices whose customers already see an address keep that address/)).toBeInTheDocument();

    await screen.findByTestId("merchant-wallet-ethereum");
    expect(within(row("ethereum")).getByText("Default")).toBeInTheDocument();
    await waitFor(() => expect(within(row("ethereum")).getByText("Ledger · EVM main account")).toBeInTheDocument());
    expect(within(row("ethereum")).getByRole("combobox", { name: "Receiving wallet on Ethereum" })).toHaveTextContent(
      "Use the default wallet (Ledger · EVM main account)",
    );
    expect(within(row("polygon")).getByText("Merchant wallet")).toBeInTheDocument();
    expect(within(row("polygon")).getByRole("combobox")).toHaveTextContent("Shop treasury");
    expect(within(row("litecoin")).getByText("Not configured — this chain can't receive payments for this merchant")).toBeInTheDocument();
    expect(within(row("litecoin")).getByRole("combobox")).toHaveTextContent("Use the default wallet (not configured)");
    expect(within(row("arbitrum")).getByText("chain disabled")).toBeInTheDocument();
    expect(screen.queryByTestId("merchant-wallet-base")).not.toBeInTheDocument();

    const mainnet = screen.getByRole("region", { name: "Mainnets" });
    const testnet = screen.getByRole("region", { name: "Testnets" });
    expect(within(mainnet).queryByTestId("merchant-wallet-sepolia")).not.toBeInTheDocument();
    expect(within(testnet).getByTestId("merchant-wallet-sepolia")).toBeInTheDocument();
  });

  it("sets a merchant wallet and switches back to the default", async () => {
    renderAdmin(<MerchantWallets merchantId="m-1" bindings={BINDINGS} />);
    await waitFor(() => expect(within(row("ethereum")).getByRole("combobox")).toBeEnabled());

    await userEvent.click(within(row("ethereum")).getByRole("combobox"));
    const options = screen.getAllByRole("option").map((o) => o.textContent);
    expect(options).toEqual([
      "Use the default wallet (Ledger · EVM main account)",
      "Ledger · EVM main account · Extended public key",
      "Shop treasury · Extended public key",
    ]);
    await userEvent.click(screen.getByRole("option", { name: "Shop treasury · Extended public key" }));
    await waitFor(() => expect(api.setMerchantWallet).toHaveBeenCalledWith("m-1", "ethereum", { wallet_id: "w-2" }));

    await userEvent.click(within(row("polygon")).getByRole("combobox"));
    await userEvent.click(screen.getByRole("option", { name: "Use the default wallet (Ledger · EVM main account)" }));
    await waitFor(() => expect(api.unsetMerchantWallet).toHaveBeenCalledWith("m-1", "polygon"));
    expect(api.setMerchantWallet).toHaveBeenCalledTimes(1);
  });

  it("offers only wallets of the chain's family and shows the server's reason inline", async () => {
    api.setMerchantWallet.mockRejectedValue(
      new ApiError({ status: 400, code: "invalid_request", message: "some fields are invalid", fields: { wallet_id: "the wallet's address is not valid on Litecoin" } }),
    );
    renderAdmin(<MerchantWallets merchantId="m-1" bindings={[]} />);
    await waitFor(() => expect(within(row("litecoin")).getByRole("combobox")).toBeEnabled());
    await userEvent.click(within(row("litecoin")).getByRole("combobox"));
    expect(screen.getAllByRole("option").map((o) => o.textContent)).toEqual(["Use the default wallet (not configured)", "Shop LTC · Fixed address"]);
    await userEvent.click(screen.getByRole("option", { name: "Shop LTC · Fixed address" }));
    expect(await within(row("litecoin")).findByRole("alert")).toHaveTextContent("the wallet's address is not valid on Litecoin");
    expect(within(row("litecoin")).getByRole("combobox")).toHaveAttribute("aria-invalid", "true");
  });

  it("is read-only for viewers", async () => {
    renderAdmin(<MerchantWallets merchantId="m-1" bindings={BINDINGS} />, { role: "viewer" });
    await screen.findByTestId("merchant-wallet-ethereum");
    await waitFor(() => expect(within(row("polygon")).getByRole("combobox")).toHaveTextContent("Shop treasury"));
    for (const id of ["ethereum", "polygon", "litecoin", "sepolia"]) {
      expect(within(row(id)).getByRole("combobox")).toBeDisabled();
    }
    expect(api.setMerchantWallet).not.toHaveBeenCalled();
    expect(api.unsetMerchantWallet).not.toHaveBeenCalled();
  });
});
