import { describe, expect, it, vi } from "vitest";
import { quote } from "@/test/fixtures";
import type { WalletOption } from "../types";
import {
  expectedTronNetwork,
  payWithTron,
  TRC20_FEE_LIMIT_SUN,
  TRC20_TRANSFER_ABI,
  tronNetworkFromHost,
  tronNetworkMatches,
  type TronProviders,
  type TronWebLike,
} from "./adapter";

const option: WalletOption = { id: "tronlink", family: "tron", name: "TronLink", kind: "injected" };
const TXID = "c".repeat(64);

function mockTron(host = "https://api.trongrid.io", requestResult: unknown = { code: 200 }) {
  const send = vi.fn().mockResolvedValue(TXID);
  const transfer = vi.fn().mockReturnValue({ send });
  const tronWeb: TronWebLike = {
    defaultAddress: { base58: "TLa2f6VPqDgRE67v1736s7bJ8Ray5wYjU7" },
    fullNode: { host },
    trx: { sendTransaction: vi.fn().mockResolvedValue({ result: true, txid: TXID }) },
    contract: vi.fn().mockReturnValue({ transfer }),
  };
  const request = vi.fn().mockResolvedValue(requestResult);
  const providers: TronProviders = { link: { request, tronWeb }, getTronWeb: () => tronWeb };
  return { tronWeb, providers, request, transfer, send };
}

describe("tron network detection", () => {
  it("reads the network from the full node host", () => {
    expect(tronNetworkFromHost("https://api.trongrid.io")).toBe("mainnet");
    expect(tronNetworkFromHost("https://nile.trongrid.io")).toBe("nile");
    expect(tronNetworkFromHost("https://api.nileex.io")).toBe("nile");
    expect(tronNetworkFromHost("https://api.shasta.trongrid.io")).toBe("shasta");
    expect(tronNetworkFromHost("https://my-node.example")).toBe("unknown");
    expect(expectedTronNetwork({ chain_id: "tron-nile", is_testnet: true })).toBe("nile");
    expect(expectedTronNetwork({ chain_id: "tron", is_testnet: false })).toBe("mainnet");
    expect(tronNetworkMatches("unknown", "mainnet")).toBe(true);
    expect(tronNetworkMatches("unknown", "nile")).toBe(false);
  });
});

describe("payWithTron", () => {
  it("sends TRC-20 transfer(to, amount) on the token contract", async () => {
    const m = mockTron();
    const q = quote();
    const res = await payWithTron({ option, quote: q, amountBase: "99000000" }, () => m.providers);
    expect(res.txHash).toBe(TXID);
    expect(m.request).toHaveBeenCalledWith({ method: "tron_requestAccounts" });
    expect(m.tronWeb.contract).toHaveBeenCalledWith(TRC20_TRANSFER_ABI, q.contract_address);
    expect(m.transfer).toHaveBeenCalledWith(q.address, "99000000");
    expect(m.send).toHaveBeenCalledWith({ feeLimit: TRC20_FEE_LIMIT_SUN });
  });

  it("sends TRX in sun with trx.sendTransaction", async () => {
    const m = mockTron();
    const q = quote({ asset_id: "trx-tron", symbol: "TRX", contract_address: "" });
    const res = await payWithTron({ option, quote: q, amountBase: "400650000" }, () => m.providers);
    expect(m.tronWeb.trx.sendTransaction).toHaveBeenCalledWith(q.address, 400_650_000);
    expect(res.txHash).toBe(TXID);
  });

  it("uses transaction.txID when txid is absent and surfaces failures", async () => {
    const m = mockTron();
    (m.tronWeb.trx.sendTransaction as ReturnType<typeof vi.fn>).mockResolvedValueOnce({
      result: true,
      transaction: { txID: "d".repeat(64) },
    });
    const q = quote({ contract_address: "" });
    await expect(payWithTron({ option, quote: q, amountBase: "1" }, () => m.providers)).resolves.toEqual({
      txHash: "d".repeat(64),
    });
    (m.tronWeb.trx.sendTransaction as ReturnType<typeof vi.fn>).mockResolvedValueOnce({
      result: false,
      code: "BANDWITH_ERROR",
      // hex of "Account resource insufficient error."
      message: Buffer.from("Account resource insufficient error.").toString("hex"),
    });
    await expect(payWithTron({ option, quote: q, amountBase: "1" }, () => m.providers)).rejects.toMatchObject({
      code: "insufficient_funds",
    });
  });

  it("refuses to pay a mainnet quote from a Nile wallet", async () => {
    const m = mockTron("https://nile.trongrid.io");
    await expect(payWithTron({ option, quote: quote(), amountBase: "1" }, () => m.providers)).rejects.toMatchObject({
      code: "wrong_network",
    });
    expect(m.transfer).not.toHaveBeenCalled();
    const nileQuote = quote({ chain_id: "tron-nile", is_testnet: true });
    await expect(payWithTron({ option, quote: nileQuote, amountBase: "1" }, () => m.providers)).resolves.toBeTruthy();
  });

  it("maps TronLink rejection codes and plain-string errors", async () => {
    const rejected = mockTron(undefined, { code: 4001, message: "User rejected the request." });
    await expect(payWithTron({ option, quote: quote(), amountBase: "1" }, () => rejected.providers)).rejects.toMatchObject({
      code: "rejected",
    });
    const declined = mockTron();
    declined.send.mockRejectedValueOnce("Confirmation declined by user");
    await expect(payWithTron({ option, quote: quote(), amountBase: "1" }, () => declined.providers)).rejects.toMatchObject({
      code: "rejected",
    });
  });

  it("treats a locked wallet (no default address) as not available", async () => {
    const m = mockTron();
    m.tronWeb.defaultAddress = { base58: false };
    await expect(payWithTron({ option, quote: quote(), amountBase: "1" }, () => m.providers)).rejects.toMatchObject({
      code: "not_installed",
    });
    await expect(payWithTron({ option, quote: quote(), amountBase: "1" }, () => undefined)).rejects.toMatchObject({
      code: "not_installed",
    });
  });
});
