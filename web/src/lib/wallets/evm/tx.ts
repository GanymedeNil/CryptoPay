import { encodeFunctionData, erc20Abi, getAddress, type Address, type Hex } from "viem";
import type { Quote } from "@/lib/api/types";
import { WalletError } from "../errors";

export interface EvmPaymentRequest {
  chainId: number;
  /** Recipient (native) or token contract (ERC-20). */
  to: Address;
  /** Wei for native transfers, 0 for tokens. */
  value: bigint;
  /** ERC-20 `transfer(address,uint256)` calldata; undefined for native. */
  data?: Hex;
}

export function parseEvmChainId(networkId: string): number {
  const id = /^\d+$/.test(networkId) ? Number.parseInt(networkId, 10) : Number.NaN;
  if (!Number.isSafeInteger(id) || id <= 0) {
    throw new WalletError("unsupported", `Invalid EVM network id "${networkId}"`);
  }
  return id;
}

/**
 * The transaction the wallet must send: a plain value transfer for the native
 * coin, or `transfer(recipient, amount)` on the token contract.
 */
export function buildEvmPaymentRequest(
  quote: Pick<Quote, "network_id" | "address" | "contract_address">,
  amountBase: string,
): EvmPaymentRequest {
  const chainId = parseEvmChainId(quote.network_id);
  const amount = BigInt(amountBase);
  if (amount <= 0n) throw new WalletError("unsupported", "Nothing to pay");
  const recipient = getAddress(quote.address);
  if (!quote.contract_address) {
    return { chainId, to: recipient, value: amount };
  }
  return {
    chainId,
    to: getAddress(quote.contract_address),
    value: 0n,
    data: encodeFunctionData({
      abi: erc20Abi,
      functionName: "transfer",
      args: [recipient, amount],
    }),
  };
}
