import type { CheckoutAsset, CheckoutView, InvoiceStatus, Quote, Transfer } from "@/lib/api/types";

export const INVOICE_ID = "3f1c2b7a-9d4e-4c1a-8b2f-6e5d4c3b2a10";
export const NOW = Date.parse("2026-11-16T08:00:00Z");

export function asset(overrides: Partial<CheckoutAsset> = {}): CheckoutAsset {
  return {
    id: "usdt-tron",
    symbol: "USDT",
    name: "Tether USD",
    chain_id: "tron",
    chain_name: "Tron",
    family: "tron",
    network_id: "",
    is_testnet: false,
    contract_address: "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t",
    decimals: 6,
    is_stablecoin: true,
    estimated_amount: "99.00",
    ...overrides,
  };
}

export const ASSETS: CheckoutAsset[] = [
  asset(),
  asset({
    id: "usdt-ethereum",
    chain_id: "ethereum",
    chain_name: "Ethereum",
    family: "evm",
    network_id: "1",
    contract_address: "0xdAC17F958D2ee523a2206206994597C13D831ec7",
  }),
  asset({
    id: "btc-bitcoin",
    symbol: "BTC",
    name: "Bitcoin",
    chain_id: "bitcoin",
    chain_name: "Bitcoin",
    family: "utxo",
    contract_address: "",
    decimals: 8,
    is_stablecoin: false,
    estimated_amount: "0.00101799",
  }),
  asset({
    id: "usdt-tron-nile",
    name: "Tether USD (Nile)",
    chain_id: "tron-nile",
    chain_name: "Tron Nile Testnet",
    is_testnet: true,
    contract_address: "TXYZopYRdj2D9XRtbG411XZZ3kM5VkAeBf",
  }),
];

export function quote(overrides: Partial<Quote> = {}): Quote {
  return {
    id: "q-1",
    asset_id: "usdt-tron",
    symbol: "USDT",
    chain_id: "tron",
    chain_name: "Tron",
    family: "tron",
    network_id: "",
    is_testnet: false,
    contract_address: "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t",
    decimals: 6,
    wallet_kind: "xpub",
    address: "TNPeeaaFB7K9cmo4uQpcU32zGK8G1NYqeL",
    rate: "1",
    amount: "99.00",
    amount_base: "99000000",
    amount_received: "0",
    amount_received_base: "0",
    amount_confirmed: "0",
    amount_confirmed_base: "0",
    amount_remaining: "99.00",
    amount_remaining_base: "99000000",
    payment_uri: "",
    required_confirmations: 19,
    status: "active",
    created_at: "2026-11-16T07:55:00Z",
    ...overrides,
  };
}

export function transfer(overrides: Partial<Transfer> = {}): Transfer {
  return {
    id: "t-1",
    chain_id: "tron",
    asset_id: "usdt-tron",
    symbol: "USDT",
    tx_hash: "a".repeat(64),
    index: 0,
    block_number: 100,
    from_address: "TLa2f6VPqDgRE67v1736s7bJ8Ray5wYjU7",
    to_address: "TNPeeaaFB7K9cmo4uQpcU32zGK8G1NYqeL",
    amount: "99",
    amount_base: "99000000",
    status: "pending",
    confirmations: 4,
    required_confirmations: 19,
    explorer_url: `https://tronscan.org/#/transaction/${"a".repeat(64)}`,
    invoice_id: INVOICE_ID,
    quote_id: "q-1",
    detected_at: "2026-11-16T07:58:00Z",
    confirmed_at: null,
    ...overrides,
  };
}

export function view(
  status: InvoiceStatus = "pending",
  overrides: Partial<Omit<CheckoutView, "invoice">> & { invoice?: Partial<CheckoutView["invoice"]> } = {},
): CheckoutView {
  const { invoice, ...rest } = overrides;
  return {
    invoice: {
      id: INVOICE_ID,
      merchant_name: "Acme Coffee Roasters",
      title: "Order #10482",
      description: "",
      amount: "99.00",
      currency: "USD",
      status,
      overpaid: false,
      paid_late: false,
      success_url: "https://shop.example/thanks",
      cancel_url: "https://shop.example/cart",
      expires_at: "2026-11-16T08:15:00Z",
      paid_at: null,
      created_at: "2026-11-16T07:45:00Z",
      ...invoice,
    },
    assets: ASSETS,
    quote: null,
    transfers: [],
    walletconnect_project_id: "",
    server_time: new Date(NOW).toISOString(),
    ...rest,
  };
}
