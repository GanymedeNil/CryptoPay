import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { quote } from "@/test/fixtures";
import { normalizeWalletError, WalletError } from "./errors";
import { getEip6963Providers, resetEip6963ForTests, startEip6963Discovery } from "./evm/eip6963";
import { detectWallets, evmProviderFor } from "./injected";

const w = window as unknown as Record<string, unknown>;
const INJECTED = ["ethereum", "tronLink", "tronWeb", "okxwallet", "phantom", "solflare", "backpack", "solana", "unisat"];

function announce(rdns: string, name: string) {
  const provider = { request: async () => null };
  window.dispatchEvent(
    new CustomEvent("eip6963:announceProvider", {
      detail: Object.freeze({ info: { uuid: crypto.randomUUID(), name, icon: "data:image/svg+xml,<svg/>", rdns }, provider }),
    }),
  );
  return provider;
}

describe("normalizeWalletError", () => {
  it.each([
    [{ code: 4001, message: "User rejected the request." }, "rejected"],
    ["Confirmation declined by user", "rejected"],
    [{ name: "UserRejectedRequestError", message: "x" }, "rejected"],
    [{ message: "outer", cause: { code: 4001 } }, "rejected"],
    [{ code: -32002, message: "Request of type 'wallet_requestPermissions' already pending" }, "pending"],
    [{ message: "insufficient funds for gas * price + value" }, "insufficient_funds"],
    [{ message: "execution reverted: BEP20: transfer amount exceeds balance" }, "insufficient_funds"],
    [{ message: "Transaction simulation failed: custom program error: 0x1" }, "insufficient_funds"],
    [{ code: 4902, message: "Unrecognized chain ID" }, "wrong_network"],
    [{ name: "ChainMismatchError", message: "x" }, "wrong_network"],
    [{ message: "Blockhash not found" }, "wrong_network"],
    [{ name: "ProviderNotFoundError", message: "Provider not found." }, "not_installed"],
    [new Error("boom"), "unknown"],
  ])("%# %o → %s", (input, code) => {
    expect(normalizeWalletError(input).code).toBe(code);
  });

  it("keeps WalletErrors and truncates long messages", () => {
    const e = new WalletError("pending");
    expect(normalizeWalletError(e)).toBe(e);
    expect(normalizeWalletError(new Error("x".repeat(500))).message.length).toBeLessThanOrEqual(180);
  });
});

describe("injected wallet detection", () => {
  beforeEach(() => {
    resetEip6963ForTests();
    for (const k of INJECTED) delete w[k];
  });
  afterEach(() => {
    resetEip6963ForTests();
    for (const k of INJECTED) delete w[k];
  });

  it("lists EIP-6963 wallets once each, plus WalletConnect when configured", () => {
    startEip6963Discovery();
    const mm = announce("io.metamask", "MetaMask");
    announce("io.metamask", "MetaMask"); // duplicate announcement
    announce("com.okex.wallet", "OKX Wallet");
    expect(getEip6963Providers()).toHaveLength(2);
    const evm = quote({ family: "evm", chain_id: "ethereum", network_id: "1" });
    expect(detectWallets(evm, "").map((o) => o.id)).toEqual(["io.metamask", "com.okex.wallet"]);
    expect(detectWallets(evm, "wc-project").map((o) => o.kind)).toEqual(["injected", "injected", "walletconnect"]);
    expect(evmProviderFor("io.metamask")).toBe(mm);
  });

  it("falls back to legacy window.ethereum", () => {
    const eth = { request: async () => null };
    w.ethereum = eth;
    const evm = quote({ family: "evm", chain_id: "ethereum", network_id: "1" });
    expect(detectWallets(evm, "")).toEqual([{ id: "injected", family: "evm", name: "", kind: "injected" }]);
    expect(evmProviderFor("injected")).toBe(eth);
  });

  it("finds Tron wallets without listing OKX twice", () => {
    const okxTron = { request: async () => null };
    w.okxwallet = { tronLink: okxTron };
    w.tronLink = okxTron; // OKX also occupies window.tronLink
    expect(detectWallets(quote(), "").map((o) => o.id)).toEqual(["okx"]);
    w.tronLink = { request: async () => null };
    expect(detectWallets(quote(), "").map((o) => o.id)).toEqual(["tronlink", "okx"]);
  });

  it("finds Solana and Bitcoin wallets by capability", () => {
    w.phantom = { solana: { signAndSendTransaction() {}, connect() {} } };
    w.backpack = { signAndSendTransaction() {} };
    const sol = quote({ family: "solana", chain_id: "solana" });
    expect(detectWallets(sol, "").map((o) => o.id)).toEqual(["phantom", "backpack"]);

    w.unisat = { sendBitcoin() {} };
    w.okxwallet = { bitcoin: { connect() {}, sendBitcoin() {} } };
    const btc = quote({ family: "utxo", chain_id: "bitcoin" });
    expect(detectWallets(btc, "").map((o) => o.id)).toEqual(["unisat", "okx"]);
    // OKX has no testnet provider here; Litecoin never has browser wallets.
    expect(detectWallets({ ...btc, chain_id: "bitcoin-testnet", is_testnet: true }, "").map((o) => o.id)).toEqual(["unisat"]);
    expect(detectWallets({ ...btc, chain_id: "litecoin" }, "")).toEqual([]);
  });
});
