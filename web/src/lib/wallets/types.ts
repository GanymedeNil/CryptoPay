import type { Family, Quote } from "@/lib/api/types";

/** A wallet the customer can pay with, as listed in the UI. */
export interface WalletOption {
  /**
   * Unique within the family: an EIP-6963 rdns ("io.metamask"), "injected"
   * (legacy window.ethereum), "walletConnect", "tronlink", "okx", "phantom",
   * "solflare", "backpack", "unisat", …
   */
  id: string;
  family: Family;
  name: string;
  /** data: / https: icon announced by the wallet (EIP-6963), if any. */
  icon?: string;
  kind: "injected" | "walletconnect";
}

/** Progress reported while paying, shown to the customer. */
export type PayStep = "connecting" | "switching" | "preparing" | "confirm";

export interface PayRequest {
  option: WalletOption;
  quote: Quote;
  /** Integer string in base units: what to send (remainder for partials). */
  amountBase: string;
  /** From `CheckoutView.walletconnect_project_id` ("" = disabled). */
  walletConnectProjectId?: string;
  onStep?: (step: PayStep) => void;
}

export interface PayResult {
  /** Tx hash / Tron txid / Solana signature / Bitcoin txid. */
  txHash: string;
}

/** One implementation per family, loaded lazily (see `loadWalletAdapter`). */
export interface WalletAdapter {
  pay(request: PayRequest): Promise<PayResult>;
}
