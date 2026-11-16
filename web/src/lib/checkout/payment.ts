/** Pure helpers about what the customer has to pay for a quote. */
import type { AssetInfo, InvoiceStatus, Quote } from "@/lib/api/types";
import { baseToDecimal, isZeroBase } from "@/lib/format/amount";

/** Statuses in which the customer can still pay (the page keeps polling). */
export function isOpenStatus(status: InvoiceStatus): boolean {
  return status === "pending" || status === "partial" || status === "confirming";
}

/** True once something has been seen on chain for this quote. */
export function hasReceivedFunds(quote: Quote): boolean {
  return !isZeroBase(quote.amount_received_base);
}

/**
 * Base units the customer still has to send: the remainder after a partial
 * payment, otherwise the full amount due.
 */
export function amountToPayBase(quote: Quote): string {
  if (hasReceivedFunds(quote) && !isZeroBase(quote.amount_remaining_base)) {
    return quote.amount_remaining_base;
  }
  return quote.amount_base;
}

/** Whole units the customer still has to send (decimal string). */
export function amountToPay(quote: Quote): string {
  if (hasReceivedFunds(quote) && !isZeroBase(quote.amount_remaining_base)) {
    return quote.amount_remaining;
  }
  return quote.amount;
}

export function uriSchemeForChain(chainId: string): string {
  return chainId.startsWith("litecoin") ? "litecoin" : "bitcoin";
}

/**
 * Builds a payment request URI for `amountBase` (used for the remainder of a
 * partial payment, since the backend's `payment_uri` carries the full amount):
 * EIP-681 for EVM, BIP-21 for Bitcoin/Litecoin, Solana Pay for Solana.
 * Tron has no standard URI → "".
 */
export function buildPaymentUri(quote: Quote, amountBase: string): string {
  switch (quote.family) {
    case "evm": {
      const chain = quote.network_id ? `@${quote.network_id}` : "";
      if (quote.contract_address) {
        return `ethereum:${quote.contract_address}${chain}/transfer?address=${quote.address}&uint256=${amountBase}`;
      }
      return `ethereum:${quote.address}${chain}?value=${amountBase}`;
    }
    case "utxo":
      return `${uriSchemeForChain(quote.chain_id)}:${quote.address}?amount=${baseToDecimal(amountBase, quote.decimals)}`;
    case "solana": {
      const amount = baseToDecimal(amountBase, quote.decimals);
      const token = quote.contract_address ? `&spl-token=${quote.contract_address}` : "";
      return `solana:${quote.address}?amount=${amount}${token}`;
    }
    case "tron":
      return "";
  }
}

/**
 * The payment request to show / open: the backend's `payment_uri` while
 * nothing was received, a rebuilt one for the remainder afterwards. "" when
 * the family has no payment URI (Tron) — callers then use the plain address.
 */
export function paymentUriFor(quote: Quote): string {
  if (!quote.payment_uri) return "";
  if (!hasReceivedFunds(quote)) return quote.payment_uri;
  return buildPaymentUri(quote, amountToPayBase(quote)) || quote.payment_uri;
}

/** Token standard label shown next to the network ("TRC-20", "BEP-20", …). */
export function tokenStandard(asset: Pick<AssetInfo, "family" | "chain_id" | "contract_address">): string | null {
  if (!asset.contract_address) return null;
  switch (asset.family) {
    case "tron":
      return "TRC-20";
    case "solana":
      return "SPL";
    case "evm":
      return asset.chain_id === "bsc" ? "BEP-20" : "ERC-20";
    default:
      return null;
  }
}
