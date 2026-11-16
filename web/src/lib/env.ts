/**
 * Public (browser) configuration. `NEXT_PUBLIC_*` values are inlined at build
 * time, so they must be referenced literally here.
 */
export const publicEnv = {
  /** Solana mainnet RPC used to fetch a recent blockhash for wallet payments. */
  solanaRpcUrl: process.env.NEXT_PUBLIC_SOLANA_RPC_URL || "https://api.mainnet-beta.solana.com",
  /** Solana devnet RPC (testnet assets). */
  solanaDevnetRpcUrl: process.env.NEXT_PUBLIC_SOLANA_DEVNET_RPC_URL || "https://api.devnet.solana.com",
} as const;
