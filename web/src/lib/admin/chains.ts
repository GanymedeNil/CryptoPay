import type { Chain, Family, Wallet } from "@/lib/api/types";

export type ChainHealth = "disabled" | "error" | "lagging" | "idle" | "ok";

/** Scanner state at a glance. */
export function chainHealth(c: Chain): ChainHealth {
  if (!c.enabled) return "disabled";
  if (c.last_error) return "error";
  if (!c.last_scan_at) return "idle";
  if (c.lag > Math.max(c.confirmations, 3)) return "lagging";
  return "ok";
}

export type NotPayableReason = "disabled" | "noWallet" | "noRpc";

/**
 * Why `payable` is false (payable = enabled && default wallet assigned && rpc
 * configured). Merchants with their own wallet on the chain (`merchant_wallets`)
 * can be paid even without a default wallet.
 */
export function notPayableReasons(c: Chain): NotPayableReason[] {
  if (c.payable) return [];
  const out: NotPayableReason[] = [];
  if (!c.enabled) out.push("disabled");
  if (!c.wallet_id) out.push("noWallet");
  if (c.rpc_urls.length === 0) out.push("noRpc");
  return out;
}

/**
 * Whether the chain receives payments for at least some merchants: payable
 * through its default wallet, or enabled with merchant wallets (and RPC).
 */
export function receivesPayments(c: Chain): boolean {
  return c.payable || (c.enabled && c.merchant_wallets > 0 && c.rpc_urls.length > 0);
}

/** Mainnets first, then testnets; catalog order is preserved inside each group. */
export function splitByNetwork<T extends { is_testnet: boolean }>(items: readonly T[]): { mainnet: T[]; testnet: T[] } {
  return { mainnet: items.filter((c) => !c.is_testnet), testnet: items.filter((c) => c.is_testnet) };
}

export function walletsForFamily(wallets: readonly Wallet[] | undefined, family: Family): Wallet[] {
  return (wallets ?? []).filter((w) => w.family === family);
}

/** True for masked values the API returns ("https://host/…"): never resubmit them. */
export function isMaskedRpcUrl(url: string): boolean {
  return url.includes("…") || url.includes("/...");
}

/** http(s) / ws(s) URL check for RPC endpoints. */
export function isRpcUrl(url: string): boolean {
  try {
    const u = new URL(url.trim());
    return ["http:", "https:", "ws:", "wss:"].includes(u.protocol) && Boolean(u.hostname);
  } catch {
    return false;
  }
}

/** `RPC_<CHAIN_ID>` env var name (upper-case, "-" → "_"). */
export function rpcEnvName(chainId: string): string {
  return chainId.toUpperCase().replace(/-/g, "_");
}
