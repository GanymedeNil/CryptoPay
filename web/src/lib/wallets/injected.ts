/**
 * Lightweight lookup of wallet objects injected into `window` (no wallet
 * libraries are imported here, so this stays in the main checkout bundle).
 */
import type { Quote } from "@/lib/api/types";
import { findEip6963Provider, getEip6963Providers, type Eip1193Provider } from "./evm/eip6963";
import type { WalletOption } from "./types";

type Dict = Record<string, unknown>;

function isObj(v: unknown): v is Dict {
  return (typeof v === "object" || typeof v === "function") && v !== null;
}

/** Safe nested read: `injectedAt("okxwallet", "tronLink")`. */
export function injectedAt(...path: string[]): unknown {
  if (typeof window === "undefined") return undefined;
  let node: unknown = window;
  for (const key of path) {
    if (!isObj(node)) return undefined;
    try {
      node = node[key];
    } catch {
      return undefined;
    }
  }
  return node;
}

// ---- EVM ------------------------------------------------------------------

export function legacyEthereumProvider(): Eip1193Provider | undefined {
  const p = injectedAt("ethereum");
  return isObj(p) && typeof p.request === "function" ? (p as unknown as Eip1193Provider) : undefined;
}

/** EVM wallets: every EIP-6963 announcement, else the legacy window.ethereum. */
export function detectEvmWallets(walletConnectProjectId: string): WalletOption[] {
  const options: WalletOption[] = getEip6963Providers().map(({ info }) => ({
    id: info.rdns,
    family: "evm",
    name: info.name,
    icon: info.icon,
    kind: "injected",
  }));
  if (options.length === 0 && legacyEthereumProvider()) {
    options.push({ id: "injected", family: "evm", name: "", kind: "injected" });
  }
  if (walletConnectProjectId) {
    options.push({ id: "walletConnect", family: "evm", name: "WalletConnect", kind: "walletconnect" });
  }
  return options;
}

/** The EIP-1193 provider behind an injected EVM option. */
export function evmProviderFor(optionId: string): Eip1193Provider | undefined {
  if (optionId === "injected") return legacyEthereumProvider();
  return findEip6963Provider(optionId)?.provider;
}

// ---- Tron -----------------------------------------------------------------

/** Tron wallets: TronLink, OKX's TronLink-compatible provider, any tronWeb. */
export function detectTronWallets(): WalletOption[] {
  const out: WalletOption[] = [];
  const tronLink = injectedAt("tronLink");
  const okx = injectedAt("okxwallet", "tronLink");
  // OKX may also occupy window.tronLink: list it once, as OKX.
  if (isObj(tronLink) && tronLink !== okx) {
    out.push({ id: "tronlink", family: "tron", name: "TronLink", kind: "injected" });
  }
  if (isObj(okx)) out.push({ id: "okx", family: "tron", name: "OKX Wallet", kind: "injected" });
  if (out.length === 0 && (isObj(tronLink) || isObj(injectedAt("tronWeb")))) {
    out.push({ id: "injected", family: "tron", name: "", kind: "injected" });
  }
  return out;
}

// ---- Solana ---------------------------------------------------------------

const SOLANA_WALLETS: { id: string; name: string; path: string[]; flag?: string }[] = [
  { id: "phantom", name: "Phantom", path: ["phantom", "solana"] },
  { id: "solflare", name: "Solflare", path: ["solflare"] },
  { id: "okx", name: "OKX Wallet", path: ["okxwallet", "solana"] },
  { id: "backpack", name: "Backpack", path: ["backpack"] },
];

export function solanaProviderObject(id: string): unknown {
  if (id === "injected") return injectedAt("solana");
  const w = SOLANA_WALLETS.find((x) => x.id === id);
  if (!w) return undefined;
  const p = injectedAt(...w.path);
  if (id === "phantom" && !isObj(p)) {
    const legacy = injectedAt("solana");
    return isObj(legacy) && legacy.isPhantom ? legacy : undefined;
  }
  if (id === "backpack" && isObj(p) && isObj(p.solana)) return p.solana;
  return p;
}

export function detectSolanaWallets(): WalletOption[] {
  const out: WalletOption[] = [];
  for (const w of SOLANA_WALLETS) {
    const p = solanaProviderObject(w.id);
    if (isObj(p) && typeof p.signAndSendTransaction === "function") {
      out.push({ id: w.id, family: "solana", name: w.name, kind: "injected" });
    }
  }
  if (out.length === 0) {
    const generic = injectedAt("solana");
    if (isObj(generic) && typeof generic.signAndSendTransaction === "function") {
      out.push({ id: "injected", family: "solana", name: "", kind: "injected" });
    }
  }
  return out;
}

// ---- Bitcoin --------------------------------------------------------------

export function okxBitcoinProvider(testnet: boolean): unknown {
  return injectedAt("okxwallet", testnet ? "bitcoinTestnet" : "bitcoin");
}

/** Bitcoin wallets (Litecoin has none: it is paid by QR / `litecoin:` link). */
export function detectBitcoinWallets(quote: Pick<Quote, "chain_id" | "is_testnet">): WalletOption[] {
  if (!quote.chain_id.startsWith("bitcoin")) return [];
  const out: WalletOption[] = [];
  const unisat = injectedAt("unisat");
  if (isObj(unisat) && typeof unisat.sendBitcoin === "function") {
    out.push({ id: "unisat", family: "utxo", name: "UniSat", kind: "injected" });
  }
  const okx = okxBitcoinProvider(quote.is_testnet);
  if (isObj(okx) && typeof okx.sendBitcoin === "function") {
    out.push({ id: "okx", family: "utxo", name: "OKX Wallet", kind: "injected" });
  }
  return out;
}

/** Injected wallets usable for this quote (WalletConnect included for EVM). */
export function detectWallets(quote: Quote, walletConnectProjectId: string): WalletOption[] {
  switch (quote.family) {
    case "evm":
      return detectEvmWallets(walletConnectProjectId);
    case "tron":
      return detectTronWallets();
    case "solana":
      return detectSolanaWallets();
    case "utxo":
      return detectBitcoinWallets(quote);
  }
}
