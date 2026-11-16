import { decodeFunctionData, erc20Abi } from "viem";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { quote } from "@/test/fixtures";
import { WalletError } from "../errors";
import type { WalletOption } from "../types";
import { payWithEvm, resetEvmConfigsForTests } from "./adapter";
import type { Eip1193Provider } from "./eip6963";
import { buildEvmPaymentRequest, parseEvmChainId } from "./tx";

const RECIPIENT = "0x742d35Cc6634C0532925a3b844Bc454e4438f44e";
const PAYER = "0x8ba1f109551bD432803012645Ac136ddd64DBA72";
const USDT = "0xdAC17F958D2ee523a2206206994597C13D831ec7";
const HASH = `0x${"ab".repeat(32)}`;

const usdtQuote = quote({
  family: "evm",
  chain_id: "bsc",
  chain_name: "BNB Smart Chain",
  network_id: "56",
  contract_address: "0x55d398326f99059fF775485246999027B3197955",
  decimals: 18,
  address: RECIPIENT,
  amount: "99.22",
  amount_base: "99220000000000000000",
});

describe("buildEvmPaymentRequest", () => {
  it("encodes ERC-20 transfer(recipient, amount) on the token contract", () => {
    const req = buildEvmPaymentRequest({ network_id: "1", address: RECIPIENT, contract_address: USDT }, "99220000");
    expect(req.chainId).toBe(1);
    expect(req.to).toBe(USDT);
    expect(req.value).toBe(0n);
    // selector a9059cbb + left-padded address + left-padded amount
    expect(req.data).toBe(
      "0xa9059cbb" +
        "000000000000000000000000742d35cc6634c0532925a3b844bc454e4438f44e" +
        (99_220_000).toString(16).padStart(64, "0"),
    );
    const decoded = decodeFunctionData({ abi: erc20Abi, data: req.data! });
    expect(decoded.functionName).toBe("transfer");
    expect(decoded.args).toEqual([RECIPIENT, 99_220_000n]);
  });

  it("keeps 18-decimal amounts exact (beyond Number precision)", () => {
    const req = buildEvmPaymentRequest(usdtQuote, "99220000000000000001");
    const decoded = decodeFunctionData({ abi: erc20Abi, data: req.data! });
    expect(decoded.args[1]).toBe(99_220_000_000_000_000_001n);
  });

  it("sends value to the recipient for the native coin", () => {
    const req = buildEvmPaymentRequest(
      { network_id: "8453", address: RECIPIENT.toLowerCase(), contract_address: "" },
      "29009000000000000",
    );
    expect(req).toEqual({ chainId: 8453, to: RECIPIENT, value: 29_009_000_000_000_000n });
  });

  it("rejects bad input", () => {
    expect(() => parseEvmChainId("")).toThrow(WalletError);
    expect(() => parseEvmChainId("0x1")).toThrow(WalletError);
    expect(() => buildEvmPaymentRequest({ network_id: "1", address: RECIPIENT, contract_address: "" }, "0")).toThrow(
      WalletError,
    );
  });
});

/** A minimal EIP-1193 wallet: one account, switchable chain, records calls. */
function mockWallet(opts: { chainId?: number; fail?: Partial<Record<string, unknown>> } = {}) {
  let chainId = opts.chainId ?? 1;
  const listeners: Record<string, ((...a: unknown[]) => void)[]> = {};
  const calls: { method: string; params?: unknown }[] = [];
  const provider: Eip1193Provider = {
    async request({ method, params }) {
      calls.push({ method, params });
      if (opts.fail?.[method]) throw opts.fail[method];
      switch (method) {
        case "eth_requestAccounts":
        case "eth_accounts":
          return [PAYER];
        case "eth_chainId":
          return `0x${chainId.toString(16)}`;
        case "wallet_requestPermissions":
          return [{ parentCapability: "eth_accounts" }];
        case "wallet_switchEthereumChain": {
          chainId = Number.parseInt((params as [{ chainId: string }])[0].chainId, 16);
          setTimeout(() => listeners.chainChanged?.forEach((l) => l(`0x${chainId.toString(16)}`)), 0);
          return null;
        }
        case "wallet_addEthereumChain": {
          chainId = Number.parseInt((params as [{ chainId: string }])[0].chainId, 16);
          return null;
        }
        case "eth_sendTransaction":
          return HASH;
        default:
          throw { code: -32601, message: `The method "${method}" does not exist / is not available.` };
      }
    },
    on(event, l) {
      (listeners[event] ??= []).push(l);
    },
    removeListener() {},
  };
  return { provider, calls };
}

const option: WalletOption = { id: "io.metamask", family: "evm", name: "MetaMask", kind: "injected" };

describe("payWithEvm (wagmi core + mocked EIP-1193 provider)", () => {
  beforeEach(() => {
    resetEvmConfigsForTests();
  });

  it("connects, switches to the quote's chain and sends the ERC-20 transfer", async () => {
    const wallet = mockWallet({ chainId: 1 });
    const steps: string[] = [];
    const result = await payWithEvm(
      { option, quote: usdtQuote, amountBase: usdtQuote.amount_base, onStep: (s) => steps.push(s) },
      { providerFor: () => wallet.provider },
    );
    expect(result.txHash).toBe(HASH);
    expect(steps).toEqual(["connecting", "switching", "confirm"]);

    const methods = wallet.calls.map((c) => c.method);
    expect(methods).toContain("wallet_switchEthereumChain");
    expect(wallet.calls.find((c) => c.method === "wallet_switchEthereumChain")?.params).toEqual([{ chainId: "0x38" }]);

    const sent = wallet.calls.find((c) => c.method === "eth_sendTransaction")!.params as [Record<string, string>];
    expect(sent[0].from.toLowerCase()).toBe(PAYER.toLowerCase());
    expect(sent[0].to.toLowerCase()).toBe(usdtQuote.contract_address.toLowerCase());
    expect(sent[0].value).toBe("0x0");
    const decoded = decodeFunctionData({ abi: erc20Abi, data: sent[0].data as `0x${string}` });
    expect(decoded.args).toEqual([RECIPIENT, 99_220_000_000_000_000_000n]);
  });

  it("sends native value without switching when already on the right chain", async () => {
    const wallet = mockWallet({ chainId: 8453 });
    const q = quote({
      family: "evm",
      chain_id: "base",
      network_id: "8453",
      contract_address: "",
      decimals: 18,
      address: RECIPIENT,
    });
    await payWithEvm({ option, quote: q, amountBase: "29009000000000000" }, { providerFor: () => wallet.provider });
    expect(wallet.calls.some((c) => c.method === "wallet_switchEthereumChain")).toBe(false);
    const sent = wallet.calls.find((c) => c.method === "eth_sendTransaction")!.params as [Record<string, string>];
    expect(sent[0].to.toLowerCase()).toBe(RECIPIENT.toLowerCase());
    expect(BigInt(sent[0].value)).toBe(29_009_000_000_000_000n);
    expect(sent[0].data).toBeUndefined();
  });

  it("maps a user rejection (4001)", async () => {
    const wallet = mockWallet({
      chainId: 56,
      fail: { eth_sendTransaction: { code: 4001, message: "User rejected the request." } },
    });
    const err = await payWithEvm(
      { option, quote: usdtQuote, amountBase: usdtQuote.amount_base },
      { providerFor: () => wallet.provider },
    ).catch((e) => e);
    expect(err).toBeInstanceOf(WalletError);
    expect(err.code).toBe("rejected");
  });

  it("maps insufficient funds", async () => {
    const wallet = mockWallet({
      chainId: 56,
      fail: { eth_sendTransaction: { code: -32000, message: "insufficient funds for gas * price + value" } },
    });
    const err = await payWithEvm(
      { option, quote: usdtQuote, amountBase: usdtQuote.amount_base },
      { providerFor: () => wallet.provider },
    ).catch((e) => e);
    expect(err.code).toBe("insufficient_funds");
  });

  it("adds the chain from its viem definition when the wallet doesn't know it (4902)", async () => {
    const wallet = mockWallet({
      chainId: 1,
      fail: { wallet_switchEthereumChain: { code: 4902, message: "Unrecognized chain ID" } },
    });
    const res = await payWithEvm(
      { option, quote: usdtQuote, amountBase: usdtQuote.amount_base },
      { providerFor: () => wallet.provider },
    );
    expect(res.txHash).toBe(HASH);
    const add = wallet.calls.find((c) => c.method === "wallet_addEthereumChain")!.params as [
      { chainId: string; chainName: string; rpcUrls: string[]; nativeCurrency: { symbol: string } },
    ];
    expect(add[0]).toMatchObject({ chainId: "0x38", chainName: "BNB Smart Chain", nativeCurrency: { symbol: "BNB" } });
    expect(add[0].rpcUrls.length).toBeGreaterThan(0);
  });

  it("maps a failed network switch to wrong_network, a declined chain add to rejected", async () => {
    const broken = mockWallet({
      chainId: 1,
      fail: { wallet_switchEthereumChain: { code: -32603, message: "Internal error" } },
    });
    const err = await payWithEvm(
      { option, quote: usdtQuote, amountBase: usdtQuote.amount_base },
      { providerFor: () => broken.provider },
    ).catch((e) => e);
    expect(err.code).toBe("wrong_network");
    expect(broken.calls.some((c) => c.method === "eth_sendTransaction")).toBe(false);

    resetEvmConfigsForTests();
    const declined = mockWallet({
      chainId: 1,
      fail: {
        wallet_switchEthereumChain: { code: 4902, message: "Unrecognized chain ID" },
        wallet_addEthereumChain: { code: 4001, message: "User rejected the request." },
      },
    });
    const err2 = await payWithEvm(
      { option, quote: usdtQuote, amountBase: usdtQuote.amount_base },
      { providerFor: () => declined.provider },
    ).catch((e) => e);
    expect(err2.code).toBe("rejected");
  });

  it("reports a missing provider and unsupported chains", async () => {
    await expect(
      payWithEvm({ option, quote: usdtQuote, amountBase: "1" }, { providerFor: () => undefined }),
    ).rejects.toMatchObject({ code: "not_installed" });
    await expect(
      payWithEvm(
        { option, quote: { ...usdtQuote, network_id: "999999" }, amountBase: "1" },
        { providerFor: () => mockWallet().provider },
      ),
    ).rejects.toMatchObject({ code: "unsupported" });
    expect(vi.isMockFunction(payWithEvm)).toBe(false);
  });
});
