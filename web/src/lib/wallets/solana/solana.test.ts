// @vitest-environment node
import {
  ASSOCIATED_TOKEN_PROGRAM_ID,
  getAssociatedTokenAddressSync,
  TOKEN_2022_PROGRAM_ID,
  TOKEN_PROGRAM_ID,
} from "@solana/spl-token";
import { PublicKey, SystemProgram, type Transaction } from "@solana/web3.js";
import { describe, expect, it, vi } from "vitest";
import { quote } from "@/test/fixtures";
import type { WalletOption } from "../types";
import { payWithSolana, solanaRpcUrlFor, type SolanaConnectionLike, type SolanaProviderLike } from "./adapter";
import { buildSolanaPaymentTransaction } from "./tx";

const PAYER = new PublicKey("7xKXtg2CW87d97TXJSDpbD5jBkheTqA83TZRuJosgAsU");
const RECIPIENT = "9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM";
const USDC = "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v";
const BLOCKHASH = "GHtXQBsoZHVnNFa9YevAzFr17DJjgHXk3ycTKD5xD3Zi";

const solQuote = quote({
  family: "solana",
  chain_id: "solana",
  symbol: "SOL",
  contract_address: "",
  decimals: 9,
  address: RECIPIENT,
});
const usdcQuote = quote({
  family: "solana",
  chain_id: "solana",
  symbol: "USDC",
  contract_address: USDC,
  decimals: 6,
  address: RECIPIENT,
});

describe("buildSolanaPaymentTransaction", () => {
  it("builds a SystemProgram transfer for SOL", () => {
    const tx = buildSolanaPaymentTransaction({
      quote: solQuote,
      payer: PAYER,
      amountBase: "543000000",
      recentBlockhash: BLOCKHASH,
      lastValidBlockHeight: 100,
    });
    expect(tx.feePayer?.equals(PAYER)).toBe(true);
    expect(tx.recentBlockhash).toBe(BLOCKHASH);
    expect(tx.instructions).toHaveLength(1);
    const ix = tx.instructions[0];
    expect(ix.programId.equals(SystemProgram.programId)).toBe(true);
    expect(ix.keys[0].pubkey.equals(PAYER)).toBe(true);
    expect(ix.keys[1].pubkey.toBase58()).toBe(RECIPIENT);
    // SystemInstruction::Transfer = u32 2, then u64 lamports (LE)
    expect(ix.data.readUInt32LE(0)).toBe(2);
    expect(ix.data.readBigUInt64LE(4)).toBe(543_000_000n);
  });

  it("creates the recipient ATA idempotently and uses transferChecked for tokens", () => {
    const tx = buildSolanaPaymentTransaction({
      quote: usdcQuote,
      payer: PAYER,
      amountBase: "99000000",
      recentBlockhash: BLOCKHASH,
      lastValidBlockHeight: 100,
    });
    const mint = new PublicKey(USDC);
    const source = getAssociatedTokenAddressSync(mint, PAYER);
    const dest = getAssociatedTokenAddressSync(mint, new PublicKey(RECIPIENT), true);
    expect(tx.instructions).toHaveLength(2);

    const [create, transfer] = tx.instructions;
    expect(create.programId.equals(ASSOCIATED_TOKEN_PROGRAM_ID)).toBe(true);
    expect([...create.data]).toEqual([1]); // CreateIdempotent
    expect(create.keys.map((k) => k.pubkey.toBase58())).toEqual([
      PAYER.toBase58(),
      dest.toBase58(),
      RECIPIENT,
      USDC,
      SystemProgram.programId.toBase58(),
      TOKEN_PROGRAM_ID.toBase58(),
    ]);

    expect(transfer.programId.equals(TOKEN_PROGRAM_ID)).toBe(true);
    expect(transfer.data[0]).toBe(12); // TransferChecked
    expect(transfer.data.readBigUInt64LE(1)).toBe(99_000_000n);
    expect(transfer.data[9]).toBe(6); // decimals
    expect(transfer.keys.map((k) => k.pubkey.toBase58())).toEqual([
      source.toBase58(),
      USDC,
      dest.toBase58(),
      PAYER.toBase58(),
    ]);
    expect(transfer.keys[3].isSigner).toBe(true);
  });

  it("supports Token-2022 mints", () => {
    const tx = buildSolanaPaymentTransaction({
      quote: usdcQuote,
      payer: PAYER,
      amountBase: "1",
      recentBlockhash: BLOCKHASH,
      lastValidBlockHeight: 1,
      tokenProgramId: TOKEN_2022_PROGRAM_ID,
    });
    expect(tx.instructions[1].programId.equals(TOKEN_2022_PROGRAM_ID)).toBe(true);
  });
});

describe("payWithSolana (mocked provider + RPC)", () => {
  const option: WalletOption = { id: "phantom", family: "solana", name: "Phantom", kind: "injected" };

  function deps(signResult: unknown = { signature: "5igSig" }, owner = TOKEN_PROGRAM_ID) {
    let signed: Transaction | undefined;
    const provider: SolanaProviderLike = {
      publicKey: null,
      connect: vi.fn().mockResolvedValue({ publicKey: PAYER }),
      signAndSendTransaction: vi.fn(async (tx: Transaction) => {
        signed = tx;
        return signResult as { signature: string };
      }),
    };
    const connection: SolanaConnectionLike = {
      getLatestBlockhash: vi.fn().mockResolvedValue({ blockhash: BLOCKHASH, lastValidBlockHeight: 42 }),
      getAccountInfo: vi.fn().mockResolvedValue({ owner }),
    };
    const getConnection = vi.fn(() => connection);
    return { provider, connection, getConnection, signed: () => signed };
  }

  it("connects, builds the SPL transfer and returns the signature", async () => {
    const d = deps();
    const steps: string[] = [];
    const res = await payWithSolana(
      { option, quote: usdcQuote, amountBase: "99000000", onStep: (s) => steps.push(s) },
      { getProvider: () => d.provider, getConnection: d.getConnection },
    );
    expect(res.txHash).toBe("5igSig");
    expect(steps).toEqual(["connecting", "preparing", "confirm"]);
    expect(d.getConnection).toHaveBeenCalledWith(solanaRpcUrlFor(usdcQuote));
    expect(d.signed()?.instructions).toHaveLength(2);
    expect(d.signed()?.recentBlockhash).toBe(BLOCKHASH);
  });

  it("accepts providers that return a bare signature string (Solflare/Backpack)", async () => {
    const d = deps("sigString");
    d.provider.connect = vi.fn().mockResolvedValue(true);
    d.provider.publicKey = PAYER;
    const res = await payWithSolana(
      { option: { ...option, id: "solflare" }, quote: solQuote, amountBase: "543000000" },
      { getProvider: () => d.provider, getConnection: d.getConnection },
    );
    expect(res.txHash).toBe("sigString");
    expect(d.connection.getAccountInfo).not.toHaveBeenCalled();
  });

  it("uses the devnet RPC for devnet quotes and maps rejections", async () => {
    expect(solanaRpcUrlFor({ chain_id: "solana-devnet", is_testnet: true })).toContain("devnet");
    expect(solanaRpcUrlFor({ chain_id: "solana", is_testnet: false })).toContain("mainnet");
    const d = deps();
    d.provider.signAndSendTransaction = vi.fn().mockRejectedValue({ code: 4001, message: "User rejected the request." });
    await expect(
      payWithSolana(
        { option, quote: solQuote, amountBase: "1" },
        { getProvider: () => d.provider, getConnection: d.getConnection },
      ),
    ).rejects.toMatchObject({ code: "rejected" });
  });

  it("reports a missing wallet", async () => {
    await expect(
      payWithSolana({ option, quote: solQuote, amountBase: "1" }, { getProvider: () => undefined }),
    ).rejects.toMatchObject({ code: "not_installed" });
  });
});
