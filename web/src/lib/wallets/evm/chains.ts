import { arbitrum, base, bsc, mainnet, polygon, sepolia } from "viem/chains";
import type { Chain } from "viem";

/**
 * viem definitions of the EVM chains in `backend/internal/catalog`. Used by
 * wagmi to switch the wallet and, when the wallet does not know the chain,
 * to add it (`wallet_addEthereumChain`).
 */
export const EVM_CHAINS = [mainnet, bsc, polygon, arbitrum, base, sepolia] as const;

export function evmChainById(id: number): Chain | undefined {
  return EVM_CHAINS.find((c) => c.id === id);
}
