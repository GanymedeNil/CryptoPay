import { describe, expect, it, vi } from "vitest";
import { quote } from "@/test/fixtures";
import type { WalletOption } from "../types";
import { expectedBitcoinNetwork, payWithBitcoin, type OkxBitcoinLike, type UnisatLike } from "./adapter";

const btcQuote = quote({
  family: "utxo",
  chain_id: "bitcoin",
  chain_name: "Bitcoin",
  symbol: "BTC",
  contract_address: "",
  decimals: 8,
  address: "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq",
});
const unisatOption: WalletOption = { id: "unisat", family: "utxo", name: "UniSat", kind: "injected" };
const okxOption: WalletOption = { id: "okx", family: "utxo", name: "OKX Wallet", kind: "injected" };

function unisat(chain: string): UnisatLike & { chain: string } {
  const u = {
    chain,
    requestAccounts: vi.fn().mockResolvedValue(["bc1qpayer"]),
    getChain: vi.fn(async () => ({ enum: u.chain })),
    switchChain: vi.fn(async (c: string) => {
      u.chain = c;
    }),
    sendBitcoin: vi.fn().mockResolvedValue("txid-unisat"),
  };
  return u;
}

describe("payWithBitcoin", () => {
  it("sends satoshis with UniSat on the right network", async () => {
    const u = unisat("BITCOIN_MAINNET");
    const res = await payWithBitcoin({ option: unisatOption, quote: btcQuote, amountBase: "101799" }, { getUnisat: () => u });
    expect(res.txHash).toBe("txid-unisat");
    expect(u.sendBitcoin).toHaveBeenCalledWith(btcQuote.address, 101_799);
    expect(u.switchChain).not.toHaveBeenCalled();
  });

  it("asks UniSat to switch networks for a testnet quote", async () => {
    const u = unisat("BITCOIN_MAINNET");
    const q = { ...btcQuote, chain_id: "bitcoin-testnet", is_testnet: true };
    const steps: string[] = [];
    await payWithBitcoin({ option: unisatOption, quote: q, amountBase: "1000", onStep: (s) => steps.push(s) }, { getUnisat: () => u });
    expect(u.switchChain).toHaveBeenCalledWith("BITCOIN_TESTNET");
    expect(steps).toEqual(["connecting", "switching", "confirm"]);
  });

  it("fails with wrong_network when the switch is refused or impossible", async () => {
    const u = unisat("BITCOIN_TESTNET4");
    u.switchChain = vi.fn().mockResolvedValue(undefined); // stays on testnet4
    await expect(
      payWithBitcoin({ option: unisatOption, quote: btcQuote, amountBase: "1" }, { getUnisat: () => u }),
    ).rejects.toMatchObject({ code: "wrong_network" });
    expect(u.sendBitcoin).not.toHaveBeenCalled();
  });

  it("falls back to the legacy getNetwork API", async () => {
    const u: UnisatLike = {
      requestAccounts: vi.fn().mockResolvedValue([]),
      getNetwork: vi.fn().mockResolvedValue("livenet"),
      sendBitcoin: vi.fn().mockResolvedValue("tx"),
    };
    await expect(payWithBitcoin({ option: unisatOption, quote: btcQuote, amountBase: "5" }, { getUnisat: () => u })).resolves.toEqual({
      txHash: "tx",
    });
  });

  it("uses OKX's per-network provider", async () => {
    const okx: OkxBitcoinLike = {
      connect: vi.fn().mockResolvedValue({ address: "bc1q" }),
      sendBitcoin: vi.fn().mockResolvedValue("txid-okx"),
    };
    const getOkx = vi.fn(() => okx);
    const res = await payWithBitcoin({ option: okxOption, quote: btcQuote, amountBase: "2500" }, { getOkx });
    expect(getOkx).toHaveBeenCalledWith(false);
    expect(okx.sendBitcoin).toHaveBeenCalledWith(btcQuote.address, 2500);
    expect(res.txHash).toBe("txid-okx");
  });

  it("rejects Litecoin, unsafe amounts and user cancellation", async () => {
    await expect(
      payWithBitcoin({ option: unisatOption, quote: { ...btcQuote, chain_id: "litecoin", chain_name: "Litecoin" }, amountBase: "1" }),
    ).rejects.toMatchObject({ code: "unsupported" });
    await expect(
      payWithBitcoin({ option: unisatOption, quote: btcQuote, amountBase: "9007199254740993" }, { getUnisat: () => unisat("BITCOIN_MAINNET") }),
    ).rejects.toMatchObject({ code: "unknown" });
    const u = unisat("BITCOIN_MAINNET");
    u.sendBitcoin = vi.fn().mockRejectedValue({ code: 4001, message: "User rejected the request." });
    await expect(
      payWithBitcoin({ option: unisatOption, quote: btcQuote, amountBase: "1" }, { getUnisat: () => u }),
    ).rejects.toMatchObject({ code: "rejected" });
    expect(expectedBitcoinNetwork({ chain_id: "bitcoin", is_testnet: false })).toBe("mainnet");
  });
});
