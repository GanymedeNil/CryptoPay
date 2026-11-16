/**
 * Bitcoin payments with UniSat (`window.unisat`) or OKX
 * (`window.okxwallet.bitcoin` / `.bitcoinTestnet`). Litecoin has no injected
 * wallet: it is paid by QR code / `litecoin:` link only.
 */
import type { Quote } from "@/lib/api/types";
import { toSafeInteger } from "@/lib/format/amount";
import { normalizeWalletError, WalletError } from "../errors";
import { injectedAt, okxBitcoinProvider } from "../injected";
import type { PayRequest, PayResult, WalletAdapter } from "../types";

export interface UnisatLike {
  requestAccounts(): Promise<string[]>;
  getNetwork?(): Promise<string>;
  getChain?(): Promise<{ enum: string } | null>;
  switchChain?(chain: string): Promise<unknown>;
  switchNetwork?(network: string): Promise<unknown>;
  sendBitcoin(to: string, satoshis: number, options?: Record<string, unknown>): Promise<string>;
}

export interface OkxBitcoinLike {
  connect(): Promise<{ address?: string } | unknown>;
  sendBitcoin?(to: string, satoshis: number, options?: Record<string, unknown>): Promise<string>;
}

export type BitcoinNetwork = "mainnet" | "testnet";

export function expectedBitcoinNetwork(quote: Pick<Quote, "chain_id" | "is_testnet">): BitcoinNetwork {
  return quote.chain_id === "bitcoin" && !quote.is_testnet ? "mainnet" : "testnet";
}

const UNISAT_CHAIN: Record<BitcoinNetwork, string> = {
  mainnet: "BITCOIN_MAINNET",
  // The backend's "bitcoin-testnet" is testnet3 (mempool.space/testnet).
  testnet: "BITCOIN_TESTNET",
};
const UNISAT_LEGACY_NETWORK: Record<BitcoinNetwork, string> = { mainnet: "livenet", testnet: "testnet" };

async function unisatOnNetwork(unisat: UnisatLike, expected: BitcoinNetwork): Promise<boolean | undefined> {
  if (unisat.getChain) {
    const chain = await unisat.getChain();
    if (chain?.enum) return chain.enum === UNISAT_CHAIN[expected];
  }
  if (unisat.getNetwork) return (await unisat.getNetwork()) === UNISAT_LEGACY_NETWORK[expected];
  return undefined;
}

export interface BitcoinAdapterDeps {
  getUnisat?: () => UnisatLike | undefined;
  getOkx?: (testnet: boolean) => OkxBitcoinLike | undefined;
}

function defaultUnisat(): UnisatLike | undefined {
  const u = injectedAt("unisat") as Partial<UnisatLike> | undefined;
  return u && typeof u.sendBitcoin === "function" ? (u as UnisatLike) : undefined;
}

function defaultOkx(testnet: boolean): OkxBitcoinLike | undefined {
  const o = okxBitcoinProvider(testnet) as Partial<OkxBitcoinLike> | undefined;
  return o && typeof o.connect === "function" ? (o as OkxBitcoinLike) : undefined;
}

export async function payWithBitcoin(req: PayRequest, deps: BitcoinAdapterDeps = {}): Promise<PayResult> {
  const { option, quote, amountBase, onStep } = req;
  try {
    if (!quote.chain_id.startsWith("bitcoin")) {
      throw new WalletError("unsupported", `${quote.chain_name} has no browser wallet`);
    }
    const expected = expectedBitcoinNetwork(quote);
    const sats = toSafeInteger(amountBase);

    if (option.id === "unisat") {
      const unisat = (deps.getUnisat ?? defaultUnisat)();
      if (!unisat) throw new WalletError("not_installed", "UniSat not found");
      onStep?.("connecting");
      await unisat.requestAccounts();
      if ((await unisatOnNetwork(unisat, expected)) === false) {
        onStep?.("switching");
        if (unisat.switchChain) await unisat.switchChain(UNISAT_CHAIN[expected]);
        else if (unisat.switchNetwork) await unisat.switchNetwork(UNISAT_LEGACY_NETWORK[expected]);
        if ((await unisatOnNetwork(unisat, expected)) === false) {
          throw new WalletError("wrong_network", `UniSat is not on ${expected}`);
        }
      }
      onStep?.("confirm");
      return { txHash: await unisat.sendBitcoin(quote.address, sats) };
    }

    if (option.id === "okx") {
      // OKX exposes one provider per network, so the network always matches.
      const okx = (deps.getOkx ?? defaultOkx)(expected === "testnet");
      if (!okx) throw new WalletError("not_installed", "OKX Wallet not found");
      if (typeof okx.sendBitcoin !== "function") throw new WalletError("unsupported", "sendBitcoin unavailable");
      onStep?.("connecting");
      await okx.connect();
      onStep?.("confirm");
      return { txHash: await okx.sendBitcoin(quote.address, sats) };
    }

    throw new WalletError("unsupported", `Unknown Bitcoin wallet "${option.id}"`);
  } catch (e) {
    throw normalizeWalletError(e);
  }
}

export const bitcoinAdapter: WalletAdapter = { pay: (req) => payWithBitcoin(req) };
