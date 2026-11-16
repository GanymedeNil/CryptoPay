/**
 * Tron payments through the wallet's injected TronWeb (TronLink, OKX's
 * TronLink-compatible provider, or any in-app browser that injects tronWeb).
 * No library is bundled: everything goes through the wallet's own TronWeb.
 */
import type { Quote } from "@/lib/api/types";
import { toSafeInteger } from "@/lib/format/amount";
import { normalizeWalletError, WalletError } from "../errors";
import { injectedAt } from "../injected";
import type { PayRequest, PayResult, WalletAdapter } from "../types";

export interface TronSendResult {
  result?: boolean;
  txid?: string;
  transaction?: { txID?: string };
  code?: string;
  message?: string;
}

export interface TronContractLike {
  transfer(to: string, amount: string): { send(options?: Record<string, unknown>): Promise<string> };
}

export interface TronWebLike {
  ready?: boolean;
  defaultAddress?: { base58?: string | false; hex?: string | false };
  fullNode?: { host?: string };
  trx: {
    sendTransaction(to: string, amount: number, options?: Record<string, unknown>): Promise<TronSendResult>;
  };
  contract(abi?: unknown[], address?: string): TronContractLike | Promise<TronContractLike>;
}

export interface TronLinkLike {
  ready?: boolean;
  tronWeb?: TronWebLike | false;
  request?(args: { method: string; params?: unknown }): Promise<unknown>;
}

/** Minimal TRC-20 ABI: only `transfer` is called. */
export const TRC20_TRANSFER_ABI = [
  {
    name: "transfer",
    type: "function",
    stateMutability: "nonpayable",
    inputs: [
      { name: "_to", type: "address" },
      { name: "_value", type: "uint256" },
    ],
    outputs: [{ name: "", type: "bool" }],
  },
];

/** Max TRX burnt for energy on a TRC-20 transfer (a cap, not the fee): 100 TRX. */
export const TRC20_FEE_LIMIT_SUN = 100_000_000;

export type TronNetwork = "mainnet" | "nile" | "shasta" | "unknown";

/** Infers the wallet's network from its full node host. */
export function tronNetworkFromHost(host: string | undefined): TronNetwork {
  if (!host) return "unknown";
  if (/nile/i.test(host)) return "nile";
  if (/shasta/i.test(host)) return "shasta";
  if (/trongrid\.io|tronstack\.io|tron\.network|tronscan/i.test(host)) return "mainnet";
  return "unknown";
}

export function expectedTronNetwork(quote: Pick<Quote, "chain_id" | "is_testnet">): TronNetwork {
  if (quote.chain_id === "tron-nile") return "nile";
  return quote.is_testnet ? "nile" : "mainnet";
}

export function tronNetworkMatches(actual: TronNetwork, expected: TronNetwork): boolean {
  // An unknown (custom) node is accepted for mainnet only.
  return actual === expected || (actual === "unknown" && expected === "mainnet");
}

function decodeHexMessage(msg: string | undefined): string | undefined {
  if (!msg || !/^[0-9a-f]+$/i.test(msg) || msg.length % 2) return msg;
  try {
    const bytes = msg.match(/../g)!.map((h) => Number.parseInt(h, 16));
    return new TextDecoder().decode(new Uint8Array(bytes));
  } catch {
    return msg;
  }
}

export function tronTxIdFromResult(res: TronSendResult | string | undefined): string {
  if (typeof res === "string") return res;
  const txid = res?.txid ?? res?.transaction?.txID;
  if (res && res.result !== false && txid) return txid;
  throw normalizeWalletError(decodeHexMessage(res?.message) ?? res?.code ?? "Transaction failed");
}

export interface TronProviders {
  link?: TronLinkLike;
  getTronWeb: () => TronWebLike | undefined;
}

function asTronWeb(v: unknown): TronWebLike | undefined {
  return typeof v === "object" && v !== null && "trx" in v ? (v as TronWebLike) : undefined;
}

/** Resolves the wallet objects behind a Tron option id. */
export function tronProvidersFor(optionId: string): TronProviders | undefined {
  const linkObj = optionId === "okx" ? injectedAt("okxwallet", "tronLink") : injectedAt("tronLink");
  const link = typeof linkObj === "object" && linkObj !== null ? (linkObj as TronLinkLike) : undefined;
  const getTronWeb = () =>
    asTronWeb(link?.tronWeb) ??
    (optionId === "okx" ? asTronWeb(injectedAt("okxwallet", "tronWeb")) : asTronWeb(injectedAt("tronWeb")));
  if (!link && !getTronWeb()) return undefined;
  return { link, getTronWeb };
}

export async function payWithTron(
  req: PayRequest,
  resolve: (optionId: string) => TronProviders | undefined = tronProvidersFor,
): Promise<PayResult> {
  const { option, quote, amountBase, onStep } = req;
  try {
    const providers = resolve(option.id);
    if (!providers) throw new WalletError("not_installed", "Tron wallet not found");

    onStep?.("connecting");
    if (providers.link?.request) {
      const res = await providers.link.request({ method: "tron_requestAccounts" });
      const code = typeof res === "object" && res !== null ? (res as { code?: unknown }).code : undefined;
      if (code === 4001) throw new WalletError("rejected", "User rejected the request");
      if (code === 4000) throw new WalletError("pending", "Authorization already in progress");
    }
    const tronWeb = providers.getTronWeb();
    const from = tronWeb?.defaultAddress?.base58;
    if (!tronWeb || !from) throw new WalletError("not_installed", "Tron wallet is locked or not connected");

    const actual = tronNetworkFromHost(tronWeb.fullNode?.host);
    const expected = expectedTronNetwork(quote);
    if (!tronNetworkMatches(actual, expected)) {
      throw new WalletError("wrong_network", `Wallet is on ${actual}, expected ${expected}`);
    }

    onStep?.("confirm");
    if (!quote.contract_address) {
      // TronWeb requires a number of sun; refuses anything not exactly representable.
      const res = await tronWeb.trx.sendTransaction(quote.address, toSafeInteger(amountBase));
      return { txHash: tronTxIdFromResult(res) };
    }
    const contract = await tronWeb.contract(TRC20_TRANSFER_ABI, quote.contract_address);
    const txid = await contract
      .transfer(quote.address, BigInt(amountBase).toString())
      .send({ feeLimit: TRC20_FEE_LIMIT_SUN });
    return { txHash: tronTxIdFromResult(txid) };
  } catch (e) {
    throw normalizeWalletError(e);
  }
}

export const tronAdapter: WalletAdapter = { pay: (req) => payWithTron(req) };
