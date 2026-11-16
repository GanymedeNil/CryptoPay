#!/usr/bin/env node
// Dependency-free mock of the CryptoPay checkout API (docs/api.md §2) for
// developing the checkout page without the Go backend.
//
//   node mock/server.mjs                       # listens on :8787
//   BACKEND_URL=http://localhost:8787 pnpm dev # in another terminal
//   open http://localhost:8787/mock            # scenario index + controls
//
// Env: MOCK_PORT (8787), MOCK_CHECKOUT_BASE (http://localhost:3000),
//      MOCK_BLOCK_MS (2000, one confirmation per interval while confirming),
//      MOCK_CLOCK_SKEW_MS (0, added to server_time to test drift correction),
//      MOCK_WC_PROJECT_ID ("", WalletConnect project id to expose).

import { randomBytes, randomUUID } from "node:crypto";
import { createServer } from "node:http";

const PORT = Number(process.env.MOCK_PORT || 8787);
const CHECKOUT_BASE = (process.env.MOCK_CHECKOUT_BASE || "http://localhost:3000").replace(/\/+$/, "");
const BLOCK_MS = Number(process.env.MOCK_BLOCK_MS || 2000);
const CLOCK_SKEW_MS = Number(process.env.MOCK_CLOCK_SKEW_MS || 0);
const WC_PROJECT_ID = process.env.MOCK_WC_PROJECT_ID || "";

// ---------------------------------------------------------------------------
// Catalog (mirrors backend/internal/catalog)
// ---------------------------------------------------------------------------

const CHAINS = {
  ethereum: { name: "Ethereum", family: "evm", network_id: "1", conf: 12, testnet: false, tx: "https://etherscan.io/tx/{tx}" },
  bsc: { name: "BNB Smart Chain", family: "evm", network_id: "56", conf: 15, testnet: false, tx: "https://bscscan.com/tx/{tx}" },
  polygon: { name: "Polygon", family: "evm", network_id: "137", conf: 64, testnet: false, tx: "https://polygonscan.com/tx/{tx}" },
  arbitrum: { name: "Arbitrum One", family: "evm", network_id: "42161", conf: 40, testnet: false, tx: "https://arbiscan.io/tx/{tx}" },
  base: { name: "Base", family: "evm", network_id: "8453", conf: 20, testnet: false, tx: "https://basescan.org/tx/{tx}" },
  tron: { name: "Tron", family: "tron", network_id: "", conf: 19, testnet: false, tx: "https://tronscan.org/#/transaction/{tx}" },
  bitcoin: { name: "Bitcoin", family: "utxo", network_id: "", conf: 2, testnet: false, tx: "https://mempool.space/tx/{tx}", scheme: "bitcoin" },
  litecoin: { name: "Litecoin", family: "utxo", network_id: "", conf: 6, testnet: false, tx: "https://litecoinspace.org/tx/{tx}", scheme: "litecoin" },
  solana: { name: "Solana", family: "solana", network_id: "", conf: 1, testnet: false, tx: "https://solscan.io/tx/{tx}" },
  sepolia: { name: "Ethereum Sepolia", family: "evm", network_id: "11155111", conf: 3, testnet: true, tx: "https://sepolia.etherscan.io/tx/{tx}" },
  "tron-nile": { name: "Tron Nile Testnet", family: "tron", network_id: "", conf: 19, testnet: true, tx: "https://nile.tronscan.org/#/transaction/{tx}" },
  "bitcoin-testnet": { name: "Bitcoin Testnet", family: "utxo", network_id: "", conf: 1, testnet: true, tx: "https://mempool.space/testnet/tx/{tx}", scheme: "bitcoin" },
  "solana-devnet": { name: "Solana Devnet", family: "solana", network_id: "", conf: 1, testnet: true, tx: "https://solscan.io/tx/{tx}?cluster=devnet" },
};

// [id, chain, symbol, name, contract, decimals, quote_precision, stable]
const ASSETS = [
  ["usdt-tron", "tron", "USDT", "Tether USD", "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t", 6, 2, true],
  ["usdt-ethereum", "ethereum", "USDT", "Tether USD", "0xdAC17F958D2ee523a2206206994597C13D831ec7", 6, 2, true],
  ["usdt-bsc", "bsc", "USDT", "Tether USD", "0x55d398326f99059fF775485246999027B3197955", 18, 2, true],
  ["usdt-polygon", "polygon", "USDT", "Tether USD", "0xc2132D05D31c914a87C6611C10748AEb04B58e8F", 6, 2, true],
  ["usdt-arbitrum", "arbitrum", "USDT", "Tether USD", "0xFd086bC7CD5C481DCC9C85ebE478A1C0b69FCbb9", 6, 2, true],
  ["usdt-solana", "solana", "USDT", "Tether USD", "Es9vMFrzaCERmJfrF4H2FYD4KCoNkY11McCe8BenwNYB", 6, 2, true],
  ["usdc-ethereum", "ethereum", "USDC", "USD Coin", "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48", 6, 2, true],
  ["usdc-base", "base", "USDC", "USD Coin", "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913", 6, 2, true],
  ["usdc-solana", "solana", "USDC", "USD Coin", "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v", 6, 2, true],
  ["usdc-polygon", "polygon", "USDC", "USD Coin", "0x3c499c542cEF5E3811e1192ce70d8cC03d5c3359", 6, 2, true],
  ["usdc-arbitrum", "arbitrum", "USDC", "USD Coin", "0xaf88d065e77c8cC2239327C5EDb3A432268e5831", 6, 2, true],
  ["usdc-bsc", "bsc", "USDC", "USD Coin", "0x8AC76a51cc950d9822D68b83fE1Ad97B32Cd580d", 18, 2, true],
  ["btc-bitcoin", "bitcoin", "BTC", "Bitcoin", "", 8, 8, false],
  ["ltc-litecoin", "litecoin", "LTC", "Litecoin", "", 8, 6, false],
  ["eth-ethereum", "ethereum", "ETH", "Ether", "", 18, 6, false],
  ["trx-tron", "tron", "TRX", "TRON", "", 6, 2, false],
  ["bnb-bsc", "bsc", "BNB", "BNB", "", 18, 5, false],
  ["sol-solana", "solana", "SOL", "Solana", "", 9, 4, false],
  ["eth-base", "base", "ETH", "Ether", "", 18, 6, false],
  ["eth-arbitrum", "arbitrum", "ETH", "Ether", "", 18, 6, false],
  ["eth-sepolia", "sepolia", "ETH", "Sepolia Ether", "", 18, 6, false],
  ["usdc-sepolia", "sepolia", "USDC", "USD Coin (Sepolia)", "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238", 6, 2, true],
  ["trx-tron-nile", "tron-nile", "TRX", "TRON (Nile)", "", 6, 2, false],
  ["usdt-tron-nile", "tron-nile", "USDT", "Tether USD (Nile)", "TXYZopYRdj2D9XRtbG411XZZ3kM5VkAeBf", 6, 2, true],
  ["btc-bitcoin-testnet", "bitcoin-testnet", "BTC", "Bitcoin (Testnet)", "", 8, 8, false],
  ["sol-solana-devnet", "solana-devnet", "SOL", "Solana (Devnet)", "", 9, 4, false],
  ["usdc-solana-devnet", "solana-devnet", "USDC", "USD Coin (Devnet)", "4zMMC9srt5Ri5X14GAgXhaHii3GnPAEERYPJgZJDncDU", 6, 2, true],
].map(([id, chain_id, symbol, name, contract_address, decimals, precision, is_stablecoin]) => ({
  id,
  symbol,
  name,
  chain_id,
  chain_name: CHAINS[chain_id].name,
  family: CHAINS[chain_id].family,
  network_id: CHAINS[chain_id].network_id,
  is_testnet: CHAINS[chain_id].testnet,
  contract_address,
  decimals,
  is_stablecoin,
  precision,
}));

/** USD price per coin (mock rates). */
const USD_RATE = { USDT: "1", USDC: "1", BTC: "97250.5", ETH: "3412.8", TRX: "0.2471", SOL: "182.35", BNB: "652.1", LTC: "96.4" };
/** Units of fiat per USD. */
const FIAT_PER_USD = { USD: "1", EUR: "0.92", CNY: "7.21" };

/** Receiving addresses per family (static ones are shared, xpub ones are "derived"). */
const ADDRESSES = {
  evm: "0x742d35Cc6634C0532925a3b844Bc454e4438f44e",
  tron: "TNPeeaaFB7K9cmo4uQpcU32zGK8G1NYqeL",
  bitcoin: "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq",
  "bitcoin-testnet": "tb1qw508d6qejxtdg4y5r3zarvary0c5xw7kxpjzsx",
  litecoin: "ltc1qg82tzq6kp7fpmt9s7ad8r3yyysk7qwn8xr6aen",
  solana: "9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM",
};

// ---------------------------------------------------------------------------
// Exact decimal helpers (BigInt; no floats for money)
// ---------------------------------------------------------------------------

function toScaled(s, scale) {
  const [i, f = ""] = String(s).split(".");
  return BigInt(i + f.padEnd(scale, "0").slice(0, scale));
}
function fromScaled(n, scale) {
  const neg = n < 0n;
  const s = (neg ? -n : n).toString().padStart(scale + 1, "0");
  const int = scale ? s.slice(0, -scale) : s;
  const frac = scale ? s.slice(-scale) : "";
  return (neg ? "-" : "") + int + (frac ? "." + frac : "");
}
/** ceil(a / b) at `precision` decimals. */
function divCeil(a, b, precision) {
  const S = 18;
  const num = toScaled(a, S) * 10n ** BigInt(precision);
  const den = toScaled(b, S);
  return fromScaled((num + den - 1n) / den, precision);
}
function baseOf(amount, decimals) {
  return toScaled(amount, decimals).toString();
}
function wholeOf(base, decimals) {
  return fromScaled(BigInt(base), decimals);
}

// ---------------------------------------------------------------------------
// Invoices
// ---------------------------------------------------------------------------

/** @type {Map<string, any>} */
const invoices = new Map();

const iso = (ms) => new Date(ms).toISOString();
const now = () => Date.now();

function priceInUsd(inv) {
  // amount / fiat-per-usd
  return divCeil(inv.amount, FIAT_PER_USD[inv.currency] || "1", 8);
}

function estimate(inv, asset) {
  const rate = USD_RATE[asset.symbol];
  if (!rate) return null;
  return divCeil(priceInUsd(inv), rate, asset.precision);
}

function randomHex(bytes) {
  return randomBytes(bytes).toString("hex");
}
const B58 = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz";
function randomB58(len) {
  return Array.from(randomBytes(len), (b) => B58[b % 58]).join("");
}
function txHashFor(family) {
  if (family === "evm") return "0x" + randomHex(32);
  if (family === "solana") return randomB58(88);
  return randomHex(32);
}

function paymentUri(q) {
  const chain = CHAINS[q.chain_id];
  switch (q.family) {
    case "evm":
      return q.contract_address
        ? `ethereum:${q.contract_address}@${q.network_id}/transfer?address=${q.address}&uint256=${q.amount_base}`
        : `ethereum:${q.address}@${q.network_id}?value=${q.amount_base}`;
    case "utxo":
      return `${chain.scheme}:${q.address}?amount=${q.amount}`;
    case "solana":
      return `solana:${q.address}?amount=${q.amount}${q.contract_address ? `&spl-token=${q.contract_address}` : ""}`;
    default:
      return "";
  }
}

function addressFor(asset) {
  if (asset.family === "utxo") return ADDRESSES[asset.chain_id] || ADDRESSES.bitcoin;
  return ADDRESSES[asset.family];
}

function makeQuote(inv, asset) {
  let amount = estimate(inv, asset);
  if (inv.wallet_kind === "static") {
    // Unique-amount bump in the last digits (what the backend does for static wallets).
    const bump = BigInt(1 + (randomBytes(1)[0] % 89)); // 1..89 units of the last quoted digit
    amount = fromScaled(toScaled(amount, asset.precision) + bump, asset.precision);
  }
  const amount_base = baseOf(amount, asset.decimals);
  const q = {
    id: randomUUID(),
    asset_id: asset.id,
    symbol: asset.symbol,
    chain_id: asset.chain_id,
    chain_name: asset.chain_name,
    family: asset.family,
    network_id: asset.network_id,
    is_testnet: asset.is_testnet,
    contract_address: asset.contract_address,
    decimals: asset.decimals,
    wallet_kind: inv.wallet_kind,
    address: addressFor(asset),
    rate: divCeil(USD_RATE[asset.symbol], "1", 8),
    amount,
    amount_base,
    received_base: 0n,
    confirmed_base: 0n,
    payment_uri: "",
    required_confirmations: CHAINS[asset.chain_id].conf,
    status: "active",
    created_at: iso(now()),
  };
  q.payment_uri = paymentUri({ ...q });
  return q;
}

function quoteView(q) {
  const due = BigInt(q.amount_base);
  const remaining = due > q.received_base ? due - q.received_base : 0n;
  const { received_base, confirmed_base, ...rest } = q;
  return {
    ...rest,
    amount_received: wholeOf(received_base, q.decimals),
    amount_received_base: received_base.toString(),
    amount_confirmed: wholeOf(confirmed_base, q.decimals),
    amount_confirmed_base: confirmed_base.toString(),
    amount_remaining: wholeOf(remaining, q.decimals),
    amount_remaining_base: remaining.toString(),
  };
}

function addTransfer(inv, base, status, confirmations) {
  const q = inv.quote;
  const chain = CHAINS[q.chain_id];
  const tx = txHashFor(q.family);
  const t = {
    id: randomUUID(),
    chain_id: q.chain_id,
    asset_id: q.asset_id,
    symbol: q.symbol,
    tx_hash: tx,
    index: 0,
    block_number: 0,
    from_address: q.family === "evm" ? "0x8ba1f109551bD432803012645Ac136ddd64DBA72" : addressFor({ family: q.family, chain_id: q.chain_id }),
    to_address: q.address,
    amount: wholeOf(base, q.decimals),
    amount_base: base.toString(),
    status,
    confirmations,
    required_confirmations: q.required_confirmations,
    explorer_url: chain.tx.replace("{tx}", tx),
    invoice_id: inv.id,
    quote_id: q.id,
    detected_at: iso(now()),
    confirmed_at: status === "confirmed" ? iso(now()) : null,
    _since: now(),
  };
  inv.transfers.unshift(t);
  q.received_base += base;
  if (status === "confirmed") q.confirmed_base += base;
  return t;
}

/** Moves an invoice to a target state (mock control). */
function advance(inv, to) {
  const ensureQuote = () => {
    if (!inv.quote) inv.quote = makeQuote(inv, ASSETS.find((a) => a.id === (inv.default_asset || "usdt-tron")));
    return inv.quote;
  };
  const due = () => BigInt(ensureQuote().amount_base);
  switch (to) {
    case "pending":
      inv.status = "pending";
      inv.transfers = [];
      if (inv.quote) {
        inv.quote.received_base = 0n;
        inv.quote.confirmed_base = 0n;
        inv.quote.status = "active";
      }
      inv.paid_at = null;
      inv.overpaid = false;
      inv.expires_at = iso(now() + inv.ttl_ms);
      break;
    case "partial": {
      ensureQuote();
      const half = due() / 2n;
      addTransfer(inv, half > 0n ? half : 1n, "pending", 0);
      inv.status = "partial";
      break;
    }
    case "confirming": {
      ensureQuote();
      const rest = due() - inv.quote.received_base;
      if (rest > 0n) addTransfer(inv, rest, "pending", 0);
      inv.status = "confirming";
      inv.confirming_since = now();
      break;
    }
    case "paid":
    case "overpaid": {
      ensureQuote();
      const rest = due() - inv.quote.received_base;
      const extra = to === "overpaid" ? due() / 10n : 0n;
      if (rest + extra > 0n) addTransfer(inv, rest + extra, "confirmed", inv.quote.required_confirmations);
      for (const t of inv.transfers) {
        if (t.status === "pending") {
          t.status = "confirmed";
          t.confirmations = t.required_confirmations;
          t.confirmed_at = iso(now());
          inv.quote.confirmed_base += BigInt(t.amount_base);
        }
      }
      inv.quote.status = "paid";
      inv.status = "paid";
      inv.overpaid = to === "overpaid";
      inv.paid_at = iso(now());
      break;
    }
    case "expired":
      inv.status = "expired";
      inv.expires_at = iso(now() - 1000);
      if (inv.quote) inv.quote.status = "closed";
      break;
    case "underpaid": {
      ensureQuote();
      if (inv.quote.received_base === 0n) addTransfer(inv, due() / 2n, "confirmed", inv.quote.required_confirmations);
      inv.status = "underpaid";
      inv.expires_at = iso(now() - 1000);
      break;
    }
    case "cancelled":
      inv.status = "cancelled";
      break;
    case "next": {
      const order = ["pending", "partial", "confirming", "paid"];
      const i = order.indexOf(inv.status);
      return advance(inv, order[Math.min(order.length - 1, i + 1)] || "paid");
    }
    default:
      throw new Error(`unknown state "${to}"`);
  }
  inv.updated_at = iso(now());
}

/** Time-driven changes applied on every read: confirmations, expiry. */
function tick(inv) {
  const t = now();
  if (inv.status === "confirming" || inv.status === "partial") {
    for (const tr of inv.transfers) {
      if (tr.status !== "pending") continue;
      const blocks = Math.floor((t - tr._since) / BLOCK_MS);
      tr.confirmations = Math.min(tr.required_confirmations, blocks);
      if (tr.confirmations >= tr.required_confirmations) {
        tr.status = "confirmed";
        tr.confirmed_at = iso(t);
        inv.quote.confirmed_base += BigInt(tr.amount_base);
      }
    }
    if (inv.status === "confirming" && inv.transfers.every((x) => x.status === "confirmed")) {
      inv.status = "paid";
      inv.quote.status = "paid";
      inv.paid_at = iso(t);
    }
  }
  if ((inv.status === "pending" || inv.status === "partial") && Date.parse(inv.expires_at) <= t) {
    const confirmed = inv.quote ? inv.quote.confirmed_base : 0n;
    inv.status = confirmed > 0n ? "underpaid" : "expired";
  }
}

function checkoutView(inv) {
  tick(inv);
  const assets = inv.no_assets
    ? []
    : ASSETS.filter((a) => inv.show_testnets || !a.is_testnet).map(({ precision, ...a }) => ({
        ...a,
        estimated_amount: estimate(inv, { ...a, precision }),
      }));
  return {
    invoice: {
      id: inv.id,
      merchant_name: inv.merchant_name,
      title: inv.title,
      description: inv.description,
      amount: inv.amount,
      currency: inv.currency,
      status: inv.status,
      overpaid: inv.overpaid,
      paid_late: inv.paid_late,
      success_url: inv.success_url,
      cancel_url: inv.cancel_url,
      expires_at: inv.expires_at,
      paid_at: inv.paid_at,
      created_at: inv.created_at,
    },
    assets,
    quote: inv.quote ? quoteView(inv.quote) : null,
    // eslint-disable-next-line @typescript-eslint/no-unused-vars -- strip internal field
    transfers: inv.transfers.map(({ _since, ...t }) => t),
    walletconnect_project_id: WC_PROJECT_ID,
    server_time: iso(now() + CLOCK_SKEW_MS),
  };
}

function createInvoice(opts = {}) {
  const t = now();
  const ttl = Number(opts.ttl_seconds ?? 15 * 60) * 1000;
  const inv = {
    id: opts.id || randomUUID(),
    merchant_name: opts.merchant_name ?? "Acme Coffee Roasters",
    title: opts.title ?? "Order #10482 · Ethiopia Guji, 2 × 250 g",
    description: opts.description ?? "",
    amount: opts.amount ?? "99.00",
    currency: opts.currency ?? "USD",
    status: "pending",
    overpaid: false,
    paid_late: false,
    success_url: opts.success_url ?? "https://example.com/orders/10482/thanks",
    cancel_url: opts.cancel_url ?? "https://example.com/cart",
    expires_at: iso(t + ttl),
    paid_at: null,
    created_at: iso(t - (opts.age_seconds ?? 0) * 1000),
    ttl_ms: ttl,
    wallet_kind: opts.wallet_kind ?? "xpub",
    default_asset: opts.asset_id,
    show_testnets: opts.testnets ?? true,
    no_assets: Boolean(opts.no_assets),
    quote: null,
    transfers: [],
    updated_at: iso(t),
  };
  invoices.set(inv.id, inv);
  if (opts.asset_id) inv.quote = makeQuote(inv, ASSETS.find((a) => a.id === opts.asset_id));
  if (opts.state && opts.state !== "pending") advance(inv, opts.state);
  return inv;
}

/** Stable, memorable scenario ids. */
const SCENARIOS = [
  { key: "choose", label: "Choose asset (no quote yet)", id: "11111111-1111-4111-8111-111111111111", opts: {} },
  { key: "pay-tron", label: "Pay · USDT on Tron (xpub)", id: "22222222-2222-4222-8222-222222222222", opts: { asset_id: "usdt-tron" } },
  { key: "pay-static", label: "Pay · USDT on Ethereum, static wallet (exact amount)", id: "33333333-3333-4333-8333-333333333333", opts: { asset_id: "usdt-ethereum", wallet_kind: "static" } },
  { key: "partial", label: "Partial · BTC", id: "44444444-4444-4444-8444-444444444444", opts: { asset_id: "btc-bitcoin", state: "partial" } },
  { key: "confirming", label: "Confirming · ETH on Base → paid", id: "55555555-5555-4555-8555-555555555555", opts: { asset_id: "eth-base", state: "confirming" } },
  { key: "paid", label: "Paid (overpaid) with redirect", id: "66666666-6666-4666-8666-666666666666", opts: { asset_id: "usdc-polygon", state: "overpaid" } },
  { key: "expired", label: "Expired", id: "77777777-7777-4777-8777-777777777777", opts: { asset_id: "usdt-tron", state: "expired" } },
  { key: "underpaid", label: "Underpaid", id: "88888888-8888-4888-8888-888888888888", opts: { asset_id: "usdt-bsc", state: "underpaid" } },
  { key: "cancelled", label: "Cancelled", id: "99999999-9999-4999-8999-999999999999", opts: { state: "cancelled" } },
  { key: "solana-devnet", label: "Pay · USDC on Solana Devnet (testnet, static)", id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", opts: { asset_id: "usdc-solana-devnet", wallet_kind: "static" } },
  { key: "litecoin", label: "Pay · LTC (no browser wallet)", id: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", opts: { asset_id: "ltc-litecoin", currency: "CNY", amount: "688.00" } },
  { key: "expiring", label: "Pay · expires in 90 s", id: "cccccccc-cccc-4ccc-8ccc-cccccccccccc", opts: { asset_id: "trx-tron", ttl_seconds: 90, age_seconds: 810 } },
  { key: "sepolia", label: "Pay · ETH on Sepolia (testnet)", id: "dddddddd-dddd-4ddd-8ddd-dddddddddddd", opts: { asset_id: "eth-sepolia", currency: "EUR", amount: "25.00" } },
  { key: "no-assets", label: "No payable assets", id: "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee", opts: { no_assets: true } },
];

function seed() {
  invoices.clear();
  for (const s of SCENARIOS) createInvoice({ ...s.opts, id: s.id });
}
seed();

// ---------------------------------------------------------------------------
// HTTP
// ---------------------------------------------------------------------------

let outageUntil = 0;

function send(res, status, body, headers = {}) {
  const payload = body === undefined ? "" : typeof body === "string" ? body : JSON.stringify(body);
  res.writeHead(status, {
    "Content-Type": typeof body === "string" ? "text/html; charset=utf-8" : "application/json",
    "Cache-Control": "no-store",
    ...headers,
  });
  res.end(payload);
}

const apiError = (res, status, code, message, fields) =>
  send(res, status, { error: { code, message, ...(fields ? { fields } : {}) } });

async function readJson(req) {
  const chunks = [];
  for await (const c of req) chunks.push(c);
  if (!chunks.length) return {};
  try {
    return JSON.parse(Buffer.concat(chunks).toString("utf8"));
  } catch {
    return null;
  }
}

function indexPage() {
  const rows = SCENARIOS.map((s) => {
    const inv = invoices.get(s.id);
    const ctl = ["next", "pending", "partial", "confirming", "paid", "overpaid", "expired", "underpaid", "cancelled"]
      .map((to) => `<a href="/mock/invoices/${s.id}/advance?to=${to}">${to}</a>`)
      .join(" · ");
    return `<tr><td><a href="${CHECKOUT_BASE}/pay/${s.id}" target="_blank">${s.label}</a><br><code>${s.id}</code></td><td><b>${inv?.status ?? "-"}</b></td><td>${ctl}</td></tr>`;
  }).join("");
  return `<!doctype html><meta charset="utf-8"><title>CryptoPay mock</title>
<style>body{font:14px system-ui;margin:2rem;max-width:1100px}td{padding:.5rem;border-top:1px solid #ddd;vertical-align:top}code{color:#666}a{color:#1d5c4c}</style>
<h1>CryptoPay checkout mock</h1>
<p>Checkout pages: <code>${CHECKOUT_BASE}/pay/&lt;id&gt;</code>. Any other UUID → 404. Confirmations advance every ${BLOCK_MS} ms while confirming.</p>
<p><a href="/mock/reset">Reset all</a> · <a href="/mock/outage?seconds=20">Simulate 20 s outage (503)</a> · <code>POST /mock/invoices</code> {amount, currency, asset_id, wallet_kind, state, ttl_seconds}</p>
<table>${rows}</table>`;
}

const server = createServer(async (req, res) => {
  const url = new URL(req.url || "/", `http://${req.headers.host || "localhost"}`);
  const parts = url.pathname.split("/").filter(Boolean);
  const method = req.method || "GET";
  try {
    if (url.pathname === "/healthz") return send(res, 200, { status: "ok" });

    // ---- mock controls ----------------------------------------------------
    if (parts[0] === "mock") {
      if (parts.length === 1) return send(res, 200, indexPage());
      if (parts[1] === "reset") {
        seed();
        outageUntil = 0;
        return url.searchParams.has("json") ? send(res, 200, { ok: true }) : send(res, 302, "", { Location: "/mock" });
      }
      if (parts[1] === "outage") {
        outageUntil = now() + Number(url.searchParams.get("seconds") || 20) * 1000;
        return send(res, 200, { outage_until: iso(outageUntil) });
      }
      if (parts[1] === "invoices" && parts.length === 2 && method === "POST") {
        const body = (await readJson(req)) || {};
        const inv = createInvoice(body);
        return send(res, 201, { id: inv.id, checkout_url: `${CHECKOUT_BASE}/pay/${inv.id}` });
      }
      if (parts[1] === "invoices" && parts[3] === "advance") {
        const inv = invoices.get(parts[2]);
        if (!inv) return apiError(res, 404, "not_found", "invoice not found");
        advance(inv, url.searchParams.get("to") || "next");
        const wantsJson = (req.headers.accept || "").includes("application/json") || method === "POST";
        return wantsJson ? send(res, 200, checkoutView(inv)) : send(res, 302, "", { Location: "/mock" });
      }
      return apiError(res, 404, "not_found", "unknown mock endpoint");
    }

    // ---- checkout API -----------------------------------------------------
    if (parts[0] === "api" && parts[1] === "checkout" && parts[2]) {
      if (now() < outageUntil) return apiError(res, 503, "unavailable", "mock outage");
      const inv = invoices.get(parts[2]);
      if (!inv) return apiError(res, 404, "not_found", "invoice not found");

      if (parts.length === 3 && method === "GET") return send(res, 200, checkoutView(inv));

      if (parts.length === 4 && parts[3] === "quote" && method === "POST") {
        const body = await readJson(req);
        if (!body || typeof body.asset_id !== "string" || !body.asset_id) {
          return apiError(res, 400, "invalid_request", "asset_id is required", { asset_id: "required" });
        }
        tick(inv);
        if (inv.status !== "pending") return apiError(res, 409, "conflict", "invoice is not open");
        if (inv.quote && inv.quote.received_base > 0n) {
          return apiError(res, 409, "conflict", "funds already detected on another quote");
        }
        const asset = ASSETS.find((a) => a.id === body.asset_id);
        if (!asset || inv.no_assets || (!inv.show_testnets && asset.is_testnet)) {
          return apiError(res, 422, "unprocessable", "asset unavailable");
        }
        if (inv.quote?.asset_id !== asset.id) {
          if (inv.quote) inv.quote.status = "superseded";
          inv.quote = makeQuote(inv, asset);
        }
        return send(res, 200, checkoutView(inv));
      }
      return apiError(res, 405, "invalid_request", "method not allowed");
    }

    return apiError(res, 404, "not_found", "not found");
  } catch (e) {
    console.error(e);
    return apiError(res, 500, "internal", e instanceof Error ? e.message : "internal error");
  }
});

server.listen(PORT, () => {
  console.log(`CryptoPay mock API on http://localhost:${PORT}  (scenarios: http://localhost:${PORT}/mock)`);
  for (const s of SCENARIOS) console.log(`  ${s.key.padEnd(14)} ${CHECKOUT_BASE}/pay/${s.id}`);
});

for (const sig of ["SIGINT", "SIGTERM"]) process.on(sig, () => server.close(() => process.exit(0)));
