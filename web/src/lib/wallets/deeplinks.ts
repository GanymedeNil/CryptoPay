/**
 * "Open this page in the wallet app" links: they open the checkout URL inside
 * a mobile wallet's built-in dapp browser, where the wallet injects its
 * provider and the customer can pay with one tap.
 *
 * Formats verified against the wallets' documentation (Nov 2026):
 * - MetaMask   https://link.metamask.io/dapp/{url without scheme}
 *              docs.metamask.io → MetaMask Connect → "Use deeplinks"
 * - Trust      https://link.trustwallet.com/open_url?coin_id={slip44}&url={enc}
 *              developer.trustwallet.com deep linking (also trust://)
 * - OKX        https://web3.okx.com/download?deeplink={enc("okx://wallet/dapp/url?dappUrl=" + enc(url))}
 *              OKX "App universal link" docs (archived 2025-12; page since moved)
 * - TokenPocket tpdapp://open?params={enc(JSON {url, chain, source})}
 *              help.tokenpocket.pro "Pull up wallet with deeplink"; `chain`
 *              values other than the documented "EOS" are not listed there
 * - Coinbase   https://go.cb-w.com/dapp?cb_url={enc}
 *              Coinbase Wallet "Mobile dapp integration" (archived 2025-11)
 * - imToken    https://connect.token.im/link/navigate/DappView?url={enc}
 *              imtoken.gitbook.io/developers → Universal links
 *              (custom scheme equivalent: imtokenv2://navigate/DappView?url=…)
 * - TronLink   tronlinkoutside://pull.activity?param={enc(JSON {url, action:"open", protocol:"TronLink", version:"1.0"})}
 *              docs.tronlink.org/mobile/deeplink → "Open DApp"
 * - Phantom    https://phantom.com/ul/browse/{enc(url)}?ref={enc(origin)}
 *              docs.phantom.com → Deeplinks → Other methods → Browse
 * - Solflare   https://solflare.com/ul/v1/browse/{enc(url)}?ref={enc(origin)}
 *              docs.solflare.com → Deeplinks → Other methods → Browse
 */
import type { Family } from "@/lib/api/types";

export interface DeepLinkContext {
  /** Absolute https URL of the page to open (the checkout page). */
  url: string;
  family: Family;
}

export interface DeepLinkWallet {
  id: string;
  name: string;
  /** Families, in display order, for which this wallet is offered. */
  families: readonly Family[];
  /** Custom-scheme links do nothing when the app is not installed. */
  scheme: "universal" | "custom";
  build(ctx: DeepLinkContext): string;
}

export interface DeepLink {
  id: string;
  name: string;
  href: string;
  scheme: DeepLinkWallet["scheme"];
}

const enc = encodeURIComponent;

export function stripScheme(url: string): string {
  return url.replace(/^https?:\/\//i, "");
}

function originOf(url: string): string {
  try {
    return new URL(url).origin;
  } catch {
    return url;
  }
}

function hostOf(url: string): string {
  try {
    return new URL(url).host;
  } catch {
    return "";
  }
}

/** SLIP-44 coin ids Trust Wallet uses to pick the dapp browser's network. */
const TRUST_COIN_ID: Partial<Record<Family, number>> = { evm: 60, tron: 195, solana: 501 };

/** TokenPocket `chain` values (only "EOS" is documented; these follow its SDK naming). */
const TOKENPOCKET_CHAIN: Partial<Record<Family, string>> = { evm: "ETH", tron: "TRX", solana: "SOL" };

export const DEEP_LINK_WALLETS: readonly DeepLinkWallet[] = [
  {
    id: "metamask",
    name: "MetaMask",
    families: ["evm"],
    scheme: "universal",
    build: ({ url }) => `https://link.metamask.io/dapp/${stripScheme(url)}`,
  },
  {
    id: "tronlink",
    name: "TronLink",
    families: ["tron"],
    scheme: "custom",
    build: ({ url }) =>
      `tronlinkoutside://pull.activity?param=${enc(
        JSON.stringify({ url, action: "open", protocol: "TronLink", version: "1.0" }),
      )}`,
  },
  {
    id: "phantom",
    name: "Phantom",
    families: ["solana"],
    scheme: "universal",
    build: ({ url }) => `https://phantom.com/ul/browse/${enc(url)}?ref=${enc(originOf(url))}`,
  },
  {
    id: "solflare",
    name: "Solflare",
    families: ["solana"],
    scheme: "universal",
    build: ({ url }) => `https://solflare.com/ul/v1/browse/${enc(url)}?ref=${enc(originOf(url))}`,
  },
  {
    id: "trust",
    name: "Trust Wallet",
    families: ["evm"],
    scheme: "universal",
    build: ({ url, family }) =>
      `https://link.trustwallet.com/open_url?coin_id=${TRUST_COIN_ID[family] ?? 60}&url=${enc(url)}`,
  },
  {
    id: "okx",
    name: "OKX Wallet",
    families: ["evm", "tron"],
    scheme: "universal",
    build: ({ url }) =>
      `https://web3.okx.com/download?deeplink=${enc(`okx://wallet/dapp/url?dappUrl=${enc(url)}`)}`,
  },
  {
    id: "tokenpocket",
    name: "TokenPocket",
    families: ["evm", "tron"],
    scheme: "custom",
    build: ({ url, family }) =>
      `tpdapp://open?params=${enc(
        JSON.stringify({ url, chain: TOKENPOCKET_CHAIN[family] ?? "ETH", source: hostOf(url) || "cryptopay" }),
      )}`,
  },
  {
    id: "coinbase",
    name: "Coinbase Wallet",
    families: ["evm"],
    scheme: "universal",
    build: ({ url }) => `https://go.cb-w.com/dapp?cb_url=${enc(url)}`,
  },
  {
    id: "imtoken",
    name: "imToken",
    families: ["evm", "tron"],
    scheme: "universal",
    build: ({ url }) => `https://connect.token.im/link/navigate/DappView?url=${enc(url)}`,
  },
];

/** Deep links relevant to one family, in display order. */
export function deepLinksFor(family: Family, url: string): DeepLink[] {
  return DEEP_LINK_WALLETS.filter((w) => w.families.includes(family)).map((w) => ({
    id: w.id,
    name: w.name,
    href: w.build({ url, family }),
    scheme: w.scheme,
  }));
}
