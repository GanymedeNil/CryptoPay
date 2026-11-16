import { describe, expect, it } from "vitest";
import { ApiError } from "@/lib/api/client";
import { ASSETS, asset, quote, view } from "@/test/fixtures";
import { groupAssetsByCoin } from "./assets";
import {
  amountToPay,
  amountToPayBase,
  buildPaymentUri,
  isOpenStatus,
  paymentUriFor,
  tokenStandard,
} from "./payment";
import { deriveScreen, isTerminalScreen } from "./view-state";

describe("payment helpers", () => {
  it("pays the full amount until something arrives, then the remainder", () => {
    const q = quote();
    expect(amountToPayBase(q)).toBe("99000000");
    expect(amountToPay(q)).toBe("99.00");
    const partial = quote({
      amount_received: "40",
      amount_received_base: "40000000",
      amount_remaining: "59",
      amount_remaining_base: "59000000",
    });
    expect(amountToPayBase(partial)).toBe("59000000");
    expect(amountToPay(partial)).toBe("59");
  });

  it("builds EIP-681 / BIP-21 / Solana Pay URIs", () => {
    const erc20 = quote({
      family: "evm",
      chain_id: "ethereum",
      network_id: "1",
      contract_address: "0xdAC17F958D2ee523a2206206994597C13D831ec7",
      address: "0x742d35Cc6634C0532925a3b844Bc454e4438f44e",
    });
    expect(buildPaymentUri(erc20, "59000000")).toBe(
      "ethereum:0xdAC17F958D2ee523a2206206994597C13D831ec7@1/transfer?address=0x742d35Cc6634C0532925a3b844Bc454e4438f44e&uint256=59000000",
    );
    const eth = quote({ ...erc20, contract_address: "", network_id: "8453", decimals: 18 });
    expect(buildPaymentUri(eth, "1000000000000000")).toBe(
      "ethereum:0x742d35Cc6634C0532925a3b844Bc454e4438f44e@8453?value=1000000000000000",
    );
    const btc = quote({ family: "utxo", chain_id: "bitcoin", address: "bc1qxyz", contract_address: "", decimals: 8 });
    expect(buildPaymentUri(btc, "50900")).toBe("bitcoin:bc1qxyz?amount=0.000509");
    expect(buildPaymentUri({ ...btc, chain_id: "litecoin" }, "100000000")).toBe("litecoin:bc1qxyz?amount=1");
    const usdcSol = quote({ family: "solana", chain_id: "solana", address: "Recip", contract_address: "Mint", decimals: 6 });
    expect(buildPaymentUri(usdcSol, "1500000")).toBe("solana:Recip?amount=1.5&spl-token=Mint");
    expect(buildPaymentUri({ ...usdcSol, contract_address: "", decimals: 9 }, "543000000")).toBe("solana:Recip?amount=0.543");
    expect(buildPaymentUri(quote(), "1")).toBe("");
  });

  it("uses the backend URI until a partial payment, then a remainder URI", () => {
    const btc = quote({
      family: "utxo",
      chain_id: "bitcoin",
      address: "bc1qxyz",
      contract_address: "",
      decimals: 8,
      payment_uri: "bitcoin:bc1qxyz?amount=0.00101799",
    });
    expect(paymentUriFor(btc)).toBe("bitcoin:bc1qxyz?amount=0.00101799");
    expect(
      paymentUriFor({ ...btc, amount_received_base: "50899", amount_remaining_base: "50900", amount_remaining: "0.000509" }),
    ).toBe("bitcoin:bc1qxyz?amount=0.000509");
    expect(paymentUriFor(quote())).toBe(""); // Tron: address only
  });

  it("labels token standards", () => {
    expect(tokenStandard(asset())).toBe("TRC-20");
    expect(tokenStandard(asset({ family: "evm", chain_id: "bsc" }))).toBe("BEP-20");
    expect(tokenStandard(asset({ family: "evm", chain_id: "polygon" }))).toBe("ERC-20");
    expect(tokenStandard(asset({ family: "solana", chain_id: "solana" }))).toBe("SPL");
    expect(tokenStandard(asset({ contract_address: "" }))).toBeNull();
  });

  it("knows which statuses are open", () => {
    expect(["pending", "partial", "confirming"].every((s) => isOpenStatus(s as never))).toBe(true);
    expect(["paid", "underpaid", "expired", "cancelled"].some((s) => isOpenStatus(s as never))).toBe(false);
  });
});

describe("asset grouping", () => {
  it("groups by coin, keeps order and flags testnets", () => {
    const groups = groupAssetsByCoin(ASSETS);
    expect(groups.map((g) => g.symbol)).toEqual(["USDT", "BTC"]);
    expect(groups[0].assets.map((a) => a.id)).toEqual(["usdt-tron", "usdt-ethereum", "usdt-tron-nile"]);
    expect(groups[0].name).toBe("Tether USD");
    expect(groups[0].allTestnet).toBe(false);
    const onlyTestnet = groupAssetsByCoin([asset({ id: "x", name: "Tether USD (Nile)", is_testnet: true })]);
    expect(onlyTestnet[0]).toMatchObject({ name: "Tether USD", allTestnet: true });
  });
});

describe("checkout state machine", () => {
  const notFound = new ApiError({ status: 404, code: "not_found", message: "nope" });
  const down = new ApiError({ status: 0, code: "network_error", message: "offline" });

  it.each([
    [{ data: null, error: null, changingAsset: false }, "loading"],
    [{ data: null, error: notFound, changingAsset: false }, "not_found"],
    [{ data: null, error: down, changingAsset: false }, "error"],
    [{ data: view("pending"), error: null, changingAsset: false }, "select_asset"],
    [{ data: view("pending", { quote: quote() }), error: null, changingAsset: false }, "pay"],
    [{ data: view("pending", { quote: quote() }), error: null, changingAsset: true }, "select_asset"],
    [{ data: view("pending", { quote: quote({ status: "superseded" }) }), error: null, changingAsset: false }, "select_asset"],
    [{ data: view("partial", { quote: quote() }), error: null, changingAsset: true }, "partial"],
    [{ data: view("confirming", { quote: quote() }), error: null, changingAsset: false }, "confirming"],
    [{ data: view("paid", { quote: quote() }), error: down, changingAsset: false }, "paid"],
    [{ data: view("expired"), error: null, changingAsset: false }, "expired"],
    [{ data: view("underpaid"), error: null, changingAsset: false }, "underpaid"],
    [{ data: view("cancelled"), error: null, changingAsset: false }, "cancelled"],
  ] as const)("%# → %s", (input, expected) => {
    expect(deriveScreen(input)).toBe(expected);
  });

  it("marks terminal screens", () => {
    expect(isTerminalScreen("paid")).toBe(true);
    expect(isTerminalScreen("not_found")).toBe(true);
    expect(isTerminalScreen("confirming")).toBe(false);
    expect(isTerminalScreen("error")).toBe(false);
  });
});
