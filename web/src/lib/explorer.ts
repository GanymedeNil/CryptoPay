/**
 * Client-side block explorer links for the built-in chains (mirrors
 * `backend/internal/catalog`). Prefer `Transfer.explorer_url` from the API
 * when a transfer exists; these templates cover a tx hash returned by a
 * wallet before the backend has seen it.
 */

interface ExplorerTemplates {
  tx: string;
  address: string;
}

const etherscanLike = (base: string): ExplorerTemplates => ({
  tx: `${base}/tx/{tx}`,
  address: `${base}/address/{address}`,
});

export const EXPLORERS: Readonly<Record<string, ExplorerTemplates>> = {
  ethereum: etherscanLike("https://etherscan.io"),
  bsc: etherscanLike("https://bscscan.com"),
  polygon: etherscanLike("https://polygonscan.com"),
  arbitrum: etherscanLike("https://arbiscan.io"),
  base: etherscanLike("https://basescan.org"),
  sepolia: etherscanLike("https://sepolia.etherscan.io"),
  tron: {
    tx: "https://tronscan.org/#/transaction/{tx}",
    address: "https://tronscan.org/#/address/{address}",
  },
  "tron-nile": {
    tx: "https://nile.tronscan.org/#/transaction/{tx}",
    address: "https://nile.tronscan.org/#/address/{address}",
  },
  bitcoin: {
    tx: "https://mempool.space/tx/{tx}",
    address: "https://mempool.space/address/{address}",
  },
  "bitcoin-testnet": {
    tx: "https://mempool.space/testnet/tx/{tx}",
    address: "https://mempool.space/testnet/address/{address}",
  },
  litecoin: {
    tx: "https://litecoinspace.org/tx/{tx}",
    address: "https://litecoinspace.org/address/{address}",
  },
  solana: {
    tx: "https://solscan.io/tx/{tx}",
    address: "https://solscan.io/account/{address}",
  },
  "solana-devnet": {
    tx: "https://solscan.io/tx/{tx}?cluster=devnet",
    address: "https://solscan.io/account/{address}?cluster=devnet",
  },
};

export function explorerTxUrl(chainId: string, txHash: string): string | null {
  const t = EXPLORERS[chainId];
  if (!t || !txHash) return null;
  return t.tx.replace("{tx}", encodeURIComponent(txHash));
}

export function explorerAddressUrl(chainId: string, address: string): string | null {
  const t = EXPLORERS[chainId];
  if (!t || !address) return null;
  return t.address.replace("{address}", encodeURIComponent(address));
}
