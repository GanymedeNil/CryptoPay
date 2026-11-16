import type { CheckoutAsset } from "@/lib/api/types";

export interface CoinGroup {
  /** Upper-case symbol ("USDT"). */
  symbol: string;
  /** Display name of the first asset ("Tether USD"). */
  name: string;
  isStablecoin: boolean;
  /** Every network of this coin, in the backend's order. */
  assets: CheckoutAsset[];
  /** True when every network of the group is a testnet. */
  allTestnet: boolean;
}

/** Strips "(Nile)"-style suffixes so "Tether USD (Nile)" groups as "Tether USD". */
function baseName(name: string): string {
  return name.replace(/\s*\([^)]*\)\s*$/, "");
}

/**
 * Groups the (already sorted) payable assets by coin symbol, keeping the
 * order in which each coin first appears.
 */
export function groupAssetsByCoin(assets: CheckoutAsset[]): CoinGroup[] {
  const groups = new Map<string, CoinGroup>();
  for (const asset of assets) {
    const symbol = asset.symbol.toUpperCase();
    let g = groups.get(symbol);
    if (!g) {
      g = {
        symbol,
        name: baseName(asset.name),
        isStablecoin: asset.is_stablecoin,
        assets: [],
        allTestnet: true,
      };
      groups.set(symbol, g);
    }
    // Prefer a mainnet asset's name for the group label.
    if (!asset.is_testnet && g.allTestnet) g.name = baseName(asset.name);
    g.assets.push(asset);
    g.allTestnet = g.allTestnet && asset.is_testnet;
  }
  return [...groups.values()];
}
