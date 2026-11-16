import {
  ASSOCIATED_TOKEN_PROGRAM_ID,
  createAssociatedTokenAccountIdempotentInstruction,
  createTransferCheckedInstruction,
  getAssociatedTokenAddressSync,
  TOKEN_PROGRAM_ID,
} from "@solana/spl-token";
import { PublicKey, SystemProgram, Transaction } from "@solana/web3.js";
import type { Quote } from "@/lib/api/types";
import { WalletError } from "../errors";

export interface BuildSolanaPaymentInput {
  quote: Pick<Quote, "address" | "contract_address" | "decimals">;
  payer: PublicKey;
  /** Integer string in base units (lamports / token base units). */
  amountBase: string;
  recentBlockhash: string;
  lastValidBlockHeight: number;
  /** Token program owning the mint (classic Token or Token-2022). */
  tokenProgramId?: PublicKey;
}

/**
 * SOL: one `SystemProgram.transfer`. SPL token: create the recipient's
 * associated token account if needed (idempotent, paid by the customer) and
 * `transferChecked` from the customer's ATA.
 */
export function buildSolanaPaymentTransaction(input: BuildSolanaPaymentInput): Transaction {
  const { quote, payer, recentBlockhash, lastValidBlockHeight } = input;
  const amount = BigInt(input.amountBase);
  if (amount <= 0n) throw new WalletError("unsupported", "Nothing to pay");
  const recipient = new PublicKey(quote.address);
  const tx = new Transaction({ feePayer: payer, blockhash: recentBlockhash, lastValidBlockHeight });

  if (!quote.contract_address) {
    tx.add(SystemProgram.transfer({ fromPubkey: payer, toPubkey: recipient, lamports: amount }));
    return tx;
  }

  const programId = input.tokenProgramId ?? TOKEN_PROGRAM_ID;
  const mint = new PublicKey(quote.contract_address);
  const source = getAssociatedTokenAddressSync(mint, payer, false, programId, ASSOCIATED_TOKEN_PROGRAM_ID);
  const destination = getAssociatedTokenAddressSync(mint, recipient, true, programId, ASSOCIATED_TOKEN_PROGRAM_ID);
  tx.add(
    createAssociatedTokenAccountIdempotentInstruction(
      payer,
      destination,
      recipient,
      mint,
      programId,
      ASSOCIATED_TOKEN_PROGRAM_ID,
    ),
    createTransferCheckedInstruction(source, mint, destination, payer, amount, quote.decimals, [], programId),
  );
  return tx;
}
