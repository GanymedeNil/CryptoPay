/**
 * Wallet integration entry point. Everything exported here is lightweight;
 * the per-family adapters (wagmi/viem, @solana/web3.js, …) are only loaded
 * through `loadWalletAdapter` (dynamic import) when the customer pays.
 */
import type { Family } from "@/lib/api/types";
import type { WalletAdapter } from "./types";

export type { PayRequest, PayResult, PayStep, WalletAdapter, WalletOption } from "./types";
export { WalletError, normalizeWalletError, type WalletErrorCode } from "./errors";
export { deepLinksFor, DEEP_LINK_WALLETS, type DeepLink } from "./deeplinks";
export { detectWallets } from "./injected";
export { startEip6963Discovery, subscribeEip6963, getEip6963Providers } from "./evm/eip6963";

export async function loadWalletAdapter(family: Family): Promise<WalletAdapter> {
  switch (family) {
    case "evm":
      return (await import("./evm/adapter")).evmAdapter;
    case "tron":
      return (await import("./tron/adapter")).tronAdapter;
    case "solana":
      return (await import("./solana/adapter")).solanaAdapter;
    case "utxo":
      return (await import("./bitcoin/adapter")).bitcoinAdapter;
  }
}
