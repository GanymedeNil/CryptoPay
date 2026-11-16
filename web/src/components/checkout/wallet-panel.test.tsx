import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { quote } from "@/test/fixtures";
import { renderWithProviders } from "@/test/render";
import { resetEip6963ForTests, startEip6963Discovery } from "@/lib/wallets/evm/eip6963";
import { loadWalletAdapter, WalletError } from "@/lib/wallets";
import { WalletPanel } from "./wallet-panel";

vi.mock("@/lib/wallets", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/wallets")>()),
  loadWalletAdapter: vi.fn(),
}));

const IPHONE = "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 Mobile/15E148";

const evmQuote = quote({
  family: "evm",
  chain_id: "ethereum",
  chain_name: "Ethereum",
  network_id: "1",
  contract_address: "0xdAC17F958D2ee523a2206206994597C13D831ec7",
  address: "0x742d35Cc6634C0532925a3b844Bc454e4438f44e",
  payment_uri: "ethereum:0xdAC17F958D2ee523a2206206994597C13D831ec7@1/transfer?address=0x742d35Cc6634C0532925a3b844Bc454e4438f44e&uint256=99000000",
});

function announceMetaMask() {
  window.dispatchEvent(
    new CustomEvent("eip6963:announceProvider", {
      detail: Object.freeze({
        info: { uuid: "1", name: "MetaMask", icon: "data:image/svg+xml,<svg/>", rdns: "io.metamask" },
        provider: { request: async () => null },
      }),
    }),
  );
}

describe("WalletPanel", () => {
  beforeEach(() => {
    resetEip6963ForTests();
    vi.mocked(loadWalletAdapter).mockReset();
    vi.mocked(loadWalletAdapter).mockResolvedValue({ pay: vi.fn() });
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    resetEip6963ForTests();
  });

  it("offers deep links for the quote's family and the payment URI on a phone without a wallet", () => {
    vi.stubGlobal("navigator", { ...navigator, userAgent: IPHONE, maxTouchPoints: 5 });
    renderWithProviders(<WalletPanel quote={evmQuote} walletConnectProjectId="" disabled={false} />);
    expect(screen.getByText("Open this page in a wallet app")).toBeInTheDocument();
    const names = ["MetaMask", "Trust Wallet", "OKX Wallet", "TokenPocket", "Coinbase Wallet", "imToken"];
    for (const n of names) expect(screen.getByRole("link", { name: new RegExp(n) })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /TronLink|Phantom/ })).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Open wallet app to pay/ })).toHaveAttribute("href", evmQuote.payment_uri);
  });

  it("shows Tron wallets' links for a Tron quote, and no URI button (Tron has no URI)", () => {
    vi.stubGlobal("navigator", { ...navigator, userAgent: IPHONE, maxTouchPoints: 5 });
    renderWithProviders(<WalletPanel quote={quote()} walletConnectProjectId="" disabled={false} />);
    expect(screen.getByRole("link", { name: /TronLink/ }).getAttribute("href")).toMatch(/^tronlinkoutside:\/\/pull\.activity\?param=/);
    expect(screen.queryByRole("link", { name: /Open wallet app to pay/ })).not.toBeInTheDocument();
  });

  it("explains Litecoin has no browser wallet", () => {
    const ltc = quote({ family: "utxo", chain_id: "litecoin", chain_name: "Litecoin", contract_address: "", payment_uri: "litecoin:ltc1q?amount=1" });
    renderWithProviders(<WalletPanel quote={ltc} walletConnectProjectId="" disabled={false} />);
    expect(screen.getByText(/Litecoin has no browser wallet/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Open wallet app to pay/ })).toHaveAttribute("href", "litecoin:ltc1q?amount=1");
  });

  it("pays with a detected wallet and shows the tx hash with an explorer link", async () => {
    startEip6963Discovery();
    announceMetaMask();
    const pay = vi.fn(async ({ onStep }: { onStep?: (s: string) => void }) => {
      onStep?.("confirm");
      return { txHash: `0x${"ab".repeat(32)}` };
    });
    vi.mocked(loadWalletAdapter).mockResolvedValue({ pay } as never);
    const onSubmitted = vi.fn();
    renderWithProviders(
      <WalletPanel quote={evmQuote} walletConnectProjectId="wc-id" disabled={false} onSubmitted={onSubmitted} />,
    );
    expect(screen.getByRole("button", { name: "WalletConnect" })).toBeInTheDocument();
    await userEvent.click(await screen.findByRole("button", { name: "Pay with MetaMask" }));
    expect(await screen.findByText("Transaction sent")).toBeInTheDocument();
    expect(pay).toHaveBeenCalledWith(
      expect.objectContaining({
        option: expect.objectContaining({ id: "io.metamask" }),
        amountBase: "99000000",
        walletConnectProjectId: "wc-id",
      }),
    );
    expect(screen.getByRole("link", { name: /View on explorer/ })).toHaveAttribute(
      "href",
      `https://etherscan.io/tx/0x${"ab".repeat(32)}`,
    );
    expect(onSubmitted).toHaveBeenCalled();
  });

  it("shows friendly errors and lets the customer retry", async () => {
    startEip6963Discovery();
    announceMetaMask();
    const pay = vi.fn().mockRejectedValue(new WalletError("wrong_network"));
    vi.mocked(loadWalletAdapter).mockResolvedValue({ pay } as never);
    renderWithProviders(<WalletPanel quote={evmQuote} walletConnectProjectId="" disabled={false} />);
    const button = await screen.findByRole("button", { name: "Pay with MetaMask" });
    await userEvent.click(button);
    expect(await screen.findByText("Your wallet isn't on Ethereum. Switch networks in the wallet and try again.")).toBeInTheDocument();
    pay.mockRejectedValueOnce(new WalletError("rejected"));
    await userEvent.click(button);
    expect(await screen.findByText("You cancelled the request in your wallet.")).toBeInTheDocument();
  });

  it("disables wallet payments after the deadline", async () => {
    startEip6963Discovery();
    announceMetaMask();
    renderWithProviders(<WalletPanel quote={evmQuote} walletConnectProjectId="" disabled />);
    expect(await screen.findByRole("button", { name: "Pay with MetaMask" })).toBeDisabled();
    expect(screen.getByText(/deadline has passed/)).toBeInTheDocument();
  });
});
