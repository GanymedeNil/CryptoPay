import type { Family, WalletKind } from "@/lib/api/types";

/** Solana has no xpub derivation in the gateway: static only. */
export function kindsFor(family: Family): WalletKind[] {
  return family === "solana" ? ["static"] : ["xpub", "static"];
}

/** Accepted extended-public-key prefixes per family (see docs/architecture.md). */
export const XPUB_PREFIXES: Record<Exclude<Family, "solana">, string[]> = {
  evm: ["xpub"],
  tron: ["xpub"],
  utxo: ["xpub", "zpub", "tpub", "vpub"],
};

export function xpubPrefixes(family: Family): string[] {
  return family === "solana" ? [] : XPUB_PREFIXES[family];
}

const B58 = "[1-9A-HJ-NP-Za-km-z]";

/** Shape check only (the backend validates the checksum and derives). */
export function looksLikeXpub(family: Family, value: string): boolean {
  const v = value.trim();
  return xpubPrefixes(family).some((p) => v.startsWith(p)) && new RegExp(`^${B58}{100,120}$`).test(v);
}

/**
 * Detects input that must never be pasted into the gateway: extended private
 * keys, WIF / raw hex private keys, Solana secret keys (base58 or JSON
 * arrays) and BIP-39 mnemonics. Errs on the side of refusing.
 */
export function looksLikeSecret(value: string): boolean {
  const v = value.trim();
  if (!v) return false;
  // xprv / yprv / zprv / tprv / uprv / vprv (and multisig Yprv/Zprv/Uprv/Vprv)
  if (/^[xyztuvXYZUV]prv[1-9A-HJ-NP-Za-km-z]{20,}/.test(v)) return true;
  // 32-byte hex private key (EVM / Tron), with or without 0x — addresses are 20 bytes
  if (/^(0x)?[0-9a-fA-F]{64}$/.test(v)) return true;
  // WIF (Bitcoin / Litecoin private key)
  if (/^[5KLc9T][1-9A-HJ-NP-Za-km-z]{50,51}$/.test(v) && !/^T[1-9A-HJ-NP-Za-km-z]{33}$/.test(v)) return true;
  // Solana secret key: 64 bytes base58 (~87–88 chars) or a JSON byte array
  if (new RegExp(`^${B58}{80,90}$`).test(v)) return true;
  if (/^\[\s*\d{1,3}(\s*,\s*\d{1,3}){31,}\s*\]$/.test(v)) return true;
  // Mnemonic: 12–24 lower-case words
  const words = v.split(/\s+/);
  if (words.length >= 12 && words.length <= 24 && words.every((w) => /^[a-z]{3,8}$/.test(w))) return true;
  return false;
}

/** Light per-family address shape check for static wallets (backend normalises). */
export function looksLikeAddress(family: Family, value: string, chainId?: string): boolean {
  const v = value.trim();
  switch (family) {
    case "evm":
      return /^0x[0-9a-fA-F]{40}$/.test(v);
    case "tron":
      return /^T[1-9A-HJ-NP-Za-km-z]{33}$/.test(v);
    case "solana":
      return new RegExp(`^${B58}{32,44}$`).test(v);
    case "utxo":
      if (chainId === "litecoin") return /^(ltc1[02-9ac-hj-np-z]{8,87}|[LM3][1-9A-HJ-NP-Za-km-z]{25,34})$/.test(v);
      if (chainId?.includes("testnet")) return /^(tb1[02-9ac-hj-np-z]{8,87}|[mn2][1-9A-HJ-NP-Za-km-z]{25,34})$/.test(v);
      return /^(bc1[02-9ac-hj-np-z]{8,87}|[13][1-9A-HJ-NP-Za-km-z]{25,34}|ltc1[02-9ac-hj-np-z]{8,87}|tb1[02-9ac-hj-np-z]{8,87}|[LMmn2][1-9A-HJ-NP-Za-km-z]{25,34})$/.test(v);
  }
}
