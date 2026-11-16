/**
 * Solana payments with injected providers (Phantom, Solflare, OKX, Backpack,
 * or a generic window.solana): build the transfer with @solana/web3.js
 * (+ spl-token), fetch a blockhash from our RPC, `signAndSendTransaction`.
 */
import { TOKEN_2022_PROGRAM_ID, TOKEN_PROGRAM_ID } from "@solana/spl-token";
import { Connection, PublicKey, type Transaction } from "@solana/web3.js";
import type { Quote } from "@/lib/api/types";
import { publicEnv } from "@/lib/env";
import { normalizeWalletError, WalletError } from "../errors";
import { solanaProviderObject } from "../injected";
import type { PayRequest, PayResult, WalletAdapter } from "../types";
import { buildSolanaPaymentTransaction } from "./tx";

interface PublicKeyLike {
  toString(): string;
}

export interface SolanaProviderLike {
  publicKey?: PublicKeyLike | null;
  connect(options?: Record<string, unknown>): Promise<unknown>;
  signAndSendTransaction(tx: Transaction, options?: Record<string, unknown>): Promise<{ signature: string } | string>;
}

export interface SolanaConnectionLike {
  getLatestBlockhash(commitment?: "confirmed" | "finalized"): Promise<{ blockhash: string; lastValidBlockHeight: number }>;
  getAccountInfo(key: PublicKey): Promise<{ owner: PublicKey } | null>;
}

export function solanaRpcUrlFor(quote: Pick<Quote, "chain_id" | "is_testnet">): string {
  return quote.chain_id === "solana-devnet" || quote.is_testnet
    ? publicEnv.solanaDevnetRpcUrl
    : publicEnv.solanaRpcUrl;
}

export interface SolanaAdapterDeps {
  getProvider?: (optionId: string) => SolanaProviderLike | undefined;
  getConnection?: (rpcUrl: string) => SolanaConnectionLike;
}

function defaultProvider(optionId: string): SolanaProviderLike | undefined {
  const p = solanaProviderObject(optionId) as Partial<SolanaProviderLike> | undefined;
  return p && typeof p.signAndSendTransaction === "function" && typeof p.connect === "function"
    ? (p as SolanaProviderLike)
    : undefined;
}

function publicKeyFrom(connectResult: unknown, provider: SolanaProviderLike): PublicKeyLike | undefined {
  if (typeof connectResult === "object" && connectResult !== null && "publicKey" in connectResult) {
    const pk = (connectResult as { publicKey?: PublicKeyLike | null }).publicKey;
    if (pk) return pk;
  }
  return provider.publicKey ?? undefined;
}

async function tokenProgramFor(connection: SolanaConnectionLike, mint: PublicKey): Promise<PublicKey> {
  try {
    const info = await connection.getAccountInfo(mint);
    if (info?.owner.equals(TOKEN_2022_PROGRAM_ID)) return TOKEN_2022_PROGRAM_ID;
  } catch {
    // fall back to the classic token program (USDT / USDC)
  }
  return TOKEN_PROGRAM_ID;
}

export async function payWithSolana(req: PayRequest, deps: SolanaAdapterDeps = {}): Promise<PayResult> {
  const { option, quote, amountBase, onStep } = req;
  try {
    const provider = (deps.getProvider ?? defaultProvider)(option.id);
    if (!provider) throw new WalletError("not_installed", "Solana wallet not found");

    onStep?.("connecting");
    const connected = await provider.connect();
    const pk = publicKeyFrom(connected, provider);
    if (!pk) throw new WalletError("not_installed", "No Solana account connected");
    const payer = new PublicKey(pk.toString());

    onStep?.("preparing");
    const rpcUrl = solanaRpcUrlFor(quote);
    const connection = deps.getConnection
      ? deps.getConnection(rpcUrl)
      : (new Connection(rpcUrl, "confirmed") as unknown as SolanaConnectionLike);
    const { blockhash, lastValidBlockHeight } = await connection.getLatestBlockhash("confirmed");
    const tokenProgramId = quote.contract_address
      ? await tokenProgramFor(connection, new PublicKey(quote.contract_address))
      : undefined;
    const tx = buildSolanaPaymentTransaction({
      quote,
      payer,
      amountBase,
      recentBlockhash: blockhash,
      lastValidBlockHeight,
      tokenProgramId,
    });

    onStep?.("confirm");
    const out = await provider.signAndSendTransaction(tx);
    const signature = typeof out === "string" ? out : out?.signature;
    if (!signature) throw new WalletError("unknown", "The wallet returned no signature");
    return { txHash: signature };
  } catch (e) {
    throw normalizeWalletError(e);
  }
}

export const solanaAdapter: WalletAdapter = { pay: (req) => payWithSolana(req) };
