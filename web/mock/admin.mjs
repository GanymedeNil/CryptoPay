// In-memory mock of the Admin API (docs/api.md §3) for developing /admin
// without the Go backend. Dependency free; imported by mock/server.mjs, which
// shares its invoices / catalog / quote helpers with it.
//
// Behaviour mirrors the contract: cookie session (`cp_session`, HttpOnly),
// CSRF header on every mutation, roles (viewer → 403 on mutations, users →
// owner only), validation errors with `fields`, 404 / 409 / 422 / 429,
// paging, filters, and an audit entry for every mutation. Two-factor
// authentication is real RFC 6238 TOTP (the seeded `mfa@` account uses the
// fixed DEMO_TOTP_SECRET printed at startup), with recovery codes, sign-in
// challenges, `require_2fa` enforcement and an e-mail outbox.

import { createHash, createHmac, randomBytes, randomUUID, timingSafeEqual } from "node:crypto";

export const DEFAULT_MERCHANT_ID = "6f1d2c3b-4a59-4e6f-8a7b-1c2d3e4f5a60";

/** Seeded admin accounts (also documented in web/README.md). */
export const MOCK_USERS = [
  { email: "owner@cryptopay.test", password: "owner-pass-123", role: "owner" },
  { email: "admin@cryptopay.test", password: "admin-pass-123", role: "admin" },
  { email: "viewer@cryptopay.test", password: "viewer-pass-123", role: "viewer" },
];

/** Seeded account with two-factor authentication on (also documented in web/README.md). */
export const MOCK_2FA_USER = { email: "mfa@cryptopay.test", password: "mfa-pass-123", role: "admin" };

/**
 * Fixed TOTP secret of MOCK_2FA_USER: add it to any authenticator app
 * (time based, SHA-1, 6 digits, 30 s), or compute codes with `totp()`.
 */
export const DEMO_TOTP_SECRET = "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP";

/** Recovery codes of MOCK_2FA_USER after a reset (each works once). */
export const DEMO_RECOVERY_CODES = [
  "demo0-aaaa1", "demo0-bbbb2", "demo0-cccc3", "demo0-dddd4", "demo0-eeee5",
  "demo0-ffff6", "demo0-gggg7", "demo0-hhhh8", "demo0-jjjj9", "demo0-kkkk0",
];

const DELAY_MS = Number(process.env.MOCK_ADMIN_DELAY_MS ?? 120);
const CHALLENGE_TTL_MS = 5 * 60 * 1000;
const CHALLENGE_MAX_ATTEMPTS = 5;
const TWO_FA_MAX_FAILURES = 10;
const ISSUER = "CryptoPay";
const SESSION_MAX_AGE = 7 * 24 * 3600;
const LOGIN_WINDOW_MS = 10 * 60 * 1000;
const LOGIN_MAX_FAILURES = 5;

// ---------------------------------------------------------------------------
// RFC 6238 TOTP (HMAC-SHA1, 6 digits, 30 s step, ±1 step) and recovery codes
// ---------------------------------------------------------------------------

const B32 = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";

export function base32Encode(buf) {
  let bits = 0;
  let value = 0;
  let out = "";
  for (const byte of buf) {
    value = (value << 8) | byte;
    bits += 8;
    while (bits >= 5) {
      out += B32[(value >>> (bits - 5)) & 31];
      bits -= 5;
    }
  }
  if (bits > 0) out += B32[(value << (5 - bits)) & 31];
  return out;
}

export function base32Decode(s) {
  const clean = String(s).toUpperCase().replace(/[\s=-]+/g, "");
  let bits = 0;
  let value = 0;
  const out = [];
  for (const ch of clean) {
    const v = B32.indexOf(ch);
    if (v < 0) throw new Error("invalid base32");
    value = (value << 5) | v;
    bits += 5;
    if (bits >= 8) {
      out.push((value >>> (bits - 8)) & 255);
      bits -= 8;
    }
  }
  return Buffer.from(out);
}

function hotp(key, counter) {
  const msg = Buffer.alloc(8);
  msg.writeBigUInt64BE(BigInt(counter));
  const mac = createHmac("sha1", key).update(msg).digest();
  const off = mac[mac.length - 1] & 15;
  const bin = ((mac[off] & 0x7f) << 24) | (mac[off + 1] << 16) | (mac[off + 2] << 8) | mac[off + 3];
  return String(bin % 1_000_000).padStart(6, "0");
}

/** The current 6-digit code of a base32 secret (tests and the startup banner use it). */
export function totp(secret, atMs = Date.now()) {
  return hotp(base32Decode(secret), Math.floor(atMs / 30_000));
}

/** Matching time step of `code` within ±1 step, newer than `lastStep`; null when wrong or replayed. */
function verifyTotp(secret, code, lastStep, atMs = Date.now()) {
  if (!/^\d{6}$/.test(String(code))) return null;
  const key = base32Decode(secret);
  const step = Math.floor(atMs / 30_000);
  for (const s of [step - 1, step, step + 1]) {
    if (s <= lastStep) continue;
    if (timingSafeEqual(Buffer.from(hotp(key, s)), Buffer.from(String(code)))) return s;
  }
  return null;
}

const RC_ALPHABET = "0123456789abcdefghjkmnpqrstvwxyz";
const sha256 = (s) => createHash("sha256").update(s).digest("hex");
const normRecovery = (s) => String(s).trim().toLowerCase().replace(/\s+/g, "");

function newRecoveryCodes() {
  return Array.from({ length: 10 }, () => {
    const b = randomBytes(10);
    const chars = Array.from(b, (x) => RC_ALPHABET[x & 31]).join("");
    return `${chars.slice(0, 5)}-${chars.slice(5)}`;
  });
}

const iso = (ms) => new Date(ms).toISOString();
const now = () => Date.now();
const DAY = 24 * 3600 * 1000;

// ---------------------------------------------------------------------------
// Deterministic randomness for seeds (stable data across restarts)
// ---------------------------------------------------------------------------

function mulberry32(seed) {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

function hex(rng, n) {
  let s = "";
  for (let i = 0; i < n; i++) s += Math.floor(rng() * 16).toString(16);
  return s;
}
function uuidFrom(rng) {
  const h = hex(rng, 32);
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-4${h.slice(13, 16)}-${"89ab"[Math.floor(rng() * 4)]}${h.slice(17, 20)}-${h.slice(20, 32)}`;
}
const B58 = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz";
const BECH32 = "qpzry9x8gf2tvdw0s3jn54khce6mua7l";
function fromDigest(seed, alphabet, len) {
  let out = "";
  let i = 0;
  while (out.length < len) {
    const d = createHash("sha256").update(`${seed}:${i++}`).digest();
    for (const b of d) {
      if (out.length >= len) break;
      out += alphabet[b % alphabet.length];
    }
  }
  return out;
}
const pick = (rng, list) => list[Math.floor(rng() * list.length)];

// ---------------------------------------------------------------------------
// Exact decimals (BigInt; never floats for money)
// ---------------------------------------------------------------------------

const SCALE = 8;
function toScaled(s, scale = SCALE) {
  const neg = String(s).startsWith("-");
  const [i, f = ""] = String(s).replace("-", "").split(".");
  const v = BigInt(i + f.padEnd(scale, "0").slice(0, scale));
  return neg ? -v : v;
}
function fromScaled(n, scale = SCALE) {
  const neg = n < 0n;
  const s = (neg ? -n : n).toString().padStart(scale + 1, "0");
  const int = s.slice(0, -scale);
  const frac = s.slice(-scale).replace(/0+$/, "");
  return (neg ? "-" : "") + int + (frac ? "." + frac : "");
}
const isDecimal = (v) => typeof v === "string" && /^\d+(\.\d+)?$/.test(v.trim());

// ---------------------------------------------------------------------------
// Validation helpers
// ---------------------------------------------------------------------------

const isHttpUrl = (v) => {
  try {
    const u = new URL(v);
    return u.protocol === "http:" || u.protocol === "https:";
  } catch {
    return false;
  }
};
const isRpcUrl = (v) => {
  try {
    const u = new URL(v);
    return ["http:", "https:", "ws:", "wss:"].includes(u.protocol) && Boolean(u.hostname);
  } catch {
    return false;
  }
};
const isEmail = (v) => typeof v === "string" && /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(v);
const isInt = (v, min, max) => Number.isInteger(v) && v >= min && v <= max;

function maskRpc(url) {
  try {
    const u = new URL(url);
    const hasMore = (u.pathname && u.pathname !== "/") || u.search;
    return `${u.protocol}//${u.host}${hasMore ? "/…" : ""}`;
  } catch {
    return "…";
  }
}

function looksLikeSecret(v) {
  const s = String(v || "").trim();
  if (/^[xyztuvXYZUV]prv/.test(s)) return true;
  if (/^(0x)?[0-9a-fA-F]{64}$/.test(s)) return true;
  const words = s.split(/\s+/);
  return words.length >= 12 && words.length <= 24 && words.every((w) => /^[a-z]{3,8}$/.test(w));
}

class HttpError extends Error {
  constructor(status, code, message, fields) {
    super(message);
    this.status = status;
    this.code = code;
    this.fields = fields;
  }
}
const bad = (message, fields) => new HttpError(400, "invalid_request", message, fields);
const notFound = (what) => new HttpError(404, "not_found", `${what} not found`);
const conflict = (message, fields) => new HttpError(409, "conflict", message, fields);
const unprocessable = (message, fields) => new HttpError(422, "unprocessable", message, fields);

function paginate(items, url) {
  const page = Math.max(1, Number.parseInt(url.searchParams.get("page") || "1", 10) || 1);
  const size = Math.min(100, Math.max(1, Number.parseInt(url.searchParams.get("page_size") || "20", 10) || 20));
  return { items: items.slice((page - 1) * size, page * size), total: items.length, page, page_size: size };
}

// ---------------------------------------------------------------------------

export function createAdminMock(ctx) {
  const { invoices, CHAINS, ASSETS, USD_RATE, FIAT_PER_USD, CHECKOUT_BASE } = ctx;

  /** @type {any} */
  let S;

  // ---- serializers ---------------------------------------------------------

  const publicUser = (u) => ({
    id: u.id,
    email: u.email,
    role: u.role,
    active: u.active,
    last_login_at: u.last_login_at,
    created_at: u.created_at,
    two_factor_enabled: Boolean(u.two_factor_enabled),
  });

  const publicEmail = (m) => ({
    id: m.id,
    kind: m.kind,
    to: m.to,
    subject: m.subject,
    status: m.status,
    attempts: m.attempts,
    last_error: m.last_error,
    created_at: m.created_at,
    sent_at: m.sent_at,
  });

  const publicMerchant = (m) => ({
    id: m.id,
    name: m.name,
    status: m.status,
    webhook_url: m.webhook_url,
    has_webhook_secret: Boolean(m.webhook_secret),
    created_at: m.created_at,
    updated_at: m.updated_at,
  });

  const publicKey = (k) => ({
    id: k.id,
    merchant_id: k.merchant_id,
    name: k.name,
    prefix: k.prefix,
    last_used_at: k.last_used_at,
    revoked_at: k.revoked_at,
    created_at: k.created_at,
  });

  function chainView(c) {
    simulateScanner(c);
    const urls = c.rpc_db.length ? c.rpc_db : c.rpc_env.length ? c.rpc_env : c.rpc_default;
    const source = c.rpc_db.length ? "database" : c.rpc_env.length ? "env" : "default";
    const meta = CHAINS[c.id];
    return {
      id: c.id,
      family: meta.family,
      name: meta.name,
      network_id: meta.network_id,
      native_symbol: c.native_symbol,
      is_testnet: meta.testnet,
      enabled: c.enabled,
      rpc_urls: urls.map(maskRpc),
      rpc_source: source,
      confirmations: c.confirmations,
      poll_interval_ms: c.poll_interval_ms,
      scan_batch: c.scan_batch,
      explorer_tx_url: meta.tx,
      explorer_addr_url: c.explorer_addr_url,
      wallet_id: c.wallet_id,
      scan_cursor: c.scan_cursor,
      last_head: c.last_head,
      lag: c.last_head - c.scan_cursor,
      last_scan_at: c.last_scan_at,
      last_error: c.last_error,
      payable: c.enabled && Boolean(c.wallet_id) && urls.length > 0,
      merchant_wallets: S.merchantWallets.filter((b) => b.chain_id === c.id).length,
    };
  }

  function simulateScanner(c) {
    if (!c.enabled) return;
    const t = now();
    const blocks = Math.floor((t - c.sim_since) / c.block_ms);
    c.last_head = c.sim_head + blocks;
    if (c.last_error) {
      c.scan_cursor = c.sim_head - 1200; // stuck while erroring
      c.last_scan_at = iso(c.sim_since - 4 * 60 * 1000);
    } else {
      c.scan_cursor = c.last_head - c.sim_lag;
      c.last_scan_at = iso(t - Math.min(c.poll_interval_ms, 3000));
    }
  }

  function assetView(a) {
    let rate = USD_RATE[a.symbol] ?? null;
    if (a.custom) rate = a.is_stablecoin && S.settings.stablecoin_peg ? "1" : null;
    if (a.no_rate) rate = null;
    return {
      id: a.id,
      symbol: a.symbol,
      name: a.name,
      chain_id: a.chain_id,
      chain_name: CHAINS[a.chain_id].name,
      family: CHAINS[a.chain_id].family,
      network_id: CHAINS[a.chain_id].network_id,
      is_testnet: CHAINS[a.chain_id].testnet,
      contract_address: a.contract_address,
      decimals: a.decimals,
      is_stablecoin: a.is_stablecoin,
      quote_precision: a.quote_precision,
      rate_key: a.rate_key,
      enabled: a.enabled,
      sort_order: a.sort_order,
      rate_usd: rate,
    };
  }

  function walletView(w) {
    return {
      id: w.id,
      name: w.name,
      family: w.family,
      kind: w.kind,
      xpub: w.xpub,
      address: w.address,
      next_index: w.next_index,
      chain_ids: S.chains.filter((c) => c.wallet_id === w.id).map((c) => c.id),
      merchant_bindings: S.merchantWallets
        .filter((b) => b.wallet_id === w.id)
        .map((b) => ({ merchant_id: b.merchant_id, merchant_name: S.merchants.find((m) => m.id === b.merchant_id)?.name ?? "", chain_id: b.chain_id }))
        .sort((a, b) => a.merchant_name.localeCompare(b.merchant_name) || a.chain_id.localeCompare(b.chain_id)),
      created_at: w.created_at,
    };
  }

  /** `MerchantWallet` (docs/api.md): a merchant's own wallet on a chain. */
  function merchantWalletView(b) {
    const w = S.wallets.find((x) => x.id === b.wallet_id);
    return { chain_id: b.chain_id, wallet_id: b.wallet_id, wallet_name: w?.name ?? "", kind: w?.kind ?? "xpub" };
  }

  /** The merchant's own wallet on the chain, else the chain's default wallet, else null. */
  function resolveWallet(merchantId, chainId) {
    const b = merchantId ? S.merchantWallets.find((x) => x.merchant_id === merchantId && x.chain_id === chainId) : null;
    const c = S.chains.find((x) => x.id === chainId);
    const id = b?.wallet_id ?? c?.wallet_id ?? null;
    return id ? (S.wallets.find((w) => w.id === id) ?? null) : null;
  }

  function amountUsd(inv) {
    const cur = inv.currency.toUpperCase();
    if (FIAT_PER_USD[cur]) return ctx.divCeil(inv.amount, FIAT_PER_USD[cur], 2);
    if (USD_RATE[cur]) return fromScaled((toScaled(inv.amount) * toScaled(USD_RATE[cur])) / 10n ** BigInt(SCALE)).replace(/(\.\d{2})\d+$/, "$1");
    return null;
  }

  const stripTransfer = (t) => {
    // eslint-disable-next-line @typescript-eslint/no-unused-vars -- strip internal field
    const { _since, ...rest } = t;
    return rest;
  };

  function invoiceView(inv) {
    ctx.tick(inv);
    syncWebhooks(inv);
    const m = S.merchants.find((x) => x.id === inv.merchant_id);
    return {
      id: inv.id,
      merchant_id: inv.merchant_id,
      merchant_name: m?.name ?? inv.merchant_name,
      order_id: inv.order_id ?? "",
      title: inv.title,
      description: inv.description,
      amount: inv.amount,
      currency: inv.currency,
      amount_usd: amountUsd(inv),
      status: inv.status,
      overpaid: inv.overpaid,
      paid_late: inv.paid_late,
      success_url: inv.success_url,
      cancel_url: inv.cancel_url,
      customer_email: inv.customer_email ?? "",
      metadata: inv.metadata ?? {},
      checkout_url: `${S.settings.checkout_base_url}/pay/${inv.id}`,
      expires_at: inv.expires_at,
      paid_at: inv.paid_at,
      created_at: inv.created_at,
      updated_at: inv.updated_at,
      quote: inv.quote ? ctx.quoteView(inv.quote) : null,
    };
  }

  function invoiceDetail(inv) {
    const base = invoiceView(inv);
    return {
      ...base,
      quotes: [...(inv.old_quotes ?? []), ...(inv.quote ? [inv.quote] : [])].map(ctx.quoteView),
      transfers: inv.transfers.map(stripTransfer),
      webhook_events: S.webhookEvents.filter((e) => e.invoice_id === inv.id).map(eventView),
    };
  }

  // ---- webhooks --------------------------------------------------------------

  const EVENT_FOR = {
    partial: "invoice.payment_detected",
    confirming: "invoice.confirming",
    paid: "invoice.paid",
    underpaid: "invoice.underpaid",
    expired: "invoice.expired",
    cancelled: "invoice.cancelled",
  };

  function enqueueEvent(inv, type, at = now(), forced) {
    const m = S.merchants.find((x) => x.id === inv.merchant_id);
    if (!m || !m.webhook_url) return;
    const id = forced?.id ?? `evt_${randomBytes(8).toString("hex")}`;
    const ev = {
      id,
      merchant_id: m.id,
      invoice_id: inv.id,
      event_type: type,
      url: m.webhook_url,
      status: "pending",
      attempts: 0,
      next_attempt_at: iso(at),
      last_status_code: 0,
      last_error: "",
      last_response: "",
      delivered_at: null,
      created_at: iso(at),
      payload: null,
      _deliverAt: at + 2500,
      ...forced,
    };
    S.webhookEvents.unshift(ev);
    ev.payload = { id: ev.id, type, created_at: ev.created_at, data: { ...invoiceViewNoSync(inv) } };
    return ev;
  }

  /** invoiceView without webhook sync (avoids recursion while building payloads). */
  function invoiceViewNoSync(inv) {
    return {
      id: inv.id,
      merchant_id: inv.merchant_id,
      order_id: inv.order_id ?? "",
      title: inv.title,
      description: inv.description,
      amount: inv.amount,
      currency: inv.currency,
      amount_usd: amountUsd(inv),
      status: inv.status,
      overpaid: inv.overpaid,
      paid_late: inv.paid_late,
      success_url: inv.success_url,
      cancel_url: inv.cancel_url,
      customer_email: inv.customer_email ?? "",
      metadata: inv.metadata ?? {},
      checkout_url: `${S.settings.checkout_base_url}/pay/${inv.id}`,
      expires_at: inv.expires_at,
      paid_at: inv.paid_at,
      created_at: inv.created_at,
      updated_at: inv.updated_at,
      quote: inv.quote ? ctx.quoteView(inv.quote) : null,
    };
  }

  /** Every status change enqueues an event (what the backend does in the same tx). */
  function syncWebhooks(inv) {
    if (inv._lastStatus === undefined) inv._lastStatus = inv.status;
    if (inv._lastStatus !== inv.status) {
      inv._lastStatus = inv.status;
      const type = EVENT_FOR[inv.status];
      if (type) enqueueEvent(inv, type);
    }
  }

  /** Delivery simulation: "broken"/"down" hosts fail, everything else gets a 200. */
  function deliver(ev) {
    if (ev.status !== "pending" || !ev._deliverAt || ev._deliverAt > now()) return;
    ev.attempts += 1;
    const m = S.merchants.find((x) => x.id === ev.merchant_id);
    const failing = !m || /broken|down|invalid/.test(ev.url);
    if (failing) {
      ev.last_status_code = /down/.test(ev.url) ? 0 : 502;
      ev.last_error = ev.last_status_code ? "unexpected status 502 Bad Gateway" : "dial tcp 203.0.113.9:443: connect: connection refused";
      ev.last_response = ev.last_status_code ? "<html><body><h1>502 Bad Gateway</h1></body></html>" : "";
      if (ev.attempts >= S.settings.webhook_max_attempts) {
        ev.status = "failed";
        ev._deliverAt = null;
      } else {
        const backoff = Math.min(2 ** ev.attempts * 15_000, 6 * 3600 * 1000);
        ev.status = ev.attempts >= 3 ? "failed" : "pending";
        ev.next_attempt_at = iso(now() + backoff);
        ev._deliverAt = ev.status === "pending" ? now() + backoff : null;
      }
    } else {
      ev.status = "delivered";
      ev.last_status_code = 200;
      ev.last_error = "";
      ev.last_response = '{"ok":true}';
      ev.delivered_at = iso(now());
      ev._deliverAt = null;
    }
  }

  function eventView(ev) {
    deliver(ev);
    // eslint-disable-next-line @typescript-eslint/no-unused-vars -- strip internal field
    const { _deliverAt, ...rest } = ev;
    return rest;
  }

  // ---- audit -----------------------------------------------------------------

  function audit(actor, action, targetType, targetId, detail, ip, at = now()) {
    S.audit.unshift({
      id: S.auditSeq++,
      actor_email: actor?.email ?? "",
      action,
      target_type: targetType,
      target_id: targetId ?? "",
      detail: detail ?? null,
      ip: ip ?? "127.0.0.1",
      created_at: iso(at),
    });
  }

  // ---- seed ------------------------------------------------------------------

  function seed() {
    const rng = mulberry32(20261116);
    const t0 = now();
    S = {
      users: [],
      sessions: new Map(),
      challenges: new Map(),
      loginFailures: new Map(),
      twoFactorFailures: new Map(),
      outbox: [],
      email:
        process.env.MOCK_SMTP === "off"
          ? { configured: false, from: "", host: "" }
          : { configured: true, from: "CryptoPay <no-reply@cryptopay.local>", host: "mailpit" },
      merchants: [],
      chains: [],
      assets: [],
      wallets: [],
      merchantWallets: [],
      unmatched: [],
      webhookEvents: [],
      audit: [],
      auditSeq: 1,
      settings: {
        invoice_ttl_seconds: 900,
        grace_period_seconds: 1800,
        underpay_tolerance_bps: 50,
        stablecoin_peg: true,
        rate_markup_bps: 100,
        webhook_max_attempts: 10,
        webhook_timeout_seconds: 10,
        checkout_base_url: CHECKOUT_BASE,
        walletconnect_project_id: "",
        require_2fa: false,
        security_emails: true,
        alert_emails: true,
        alert_recipients: ["ops@cryptopay.test"],
        email_locale: "zh",
      },
    };

    // users
    const roleAge = { owner: 120, admin: 60, viewer: 20 };
    for (const u of MOCK_USERS) {
      S.users.push({
        id: uuidFrom(rng),
        email: u.email,
        password: u.password,
        role: u.role,
        active: true,
        last_login_at: u.role === "viewer" ? null : iso(t0 - (u.role === "owner" ? 2 : 26) * 3600 * 1000),
        created_at: iso(t0 - roleAge[u.role] * DAY),
      });
    }
    S.users.push({
      id: uuidFrom(rng),
      email: "former.ops@cryptopay.test",
      password: "former-pass-123",
      role: "admin",
      active: false,
      last_login_at: iso(t0 - 45 * DAY),
      created_at: iso(t0 - 100 * DAY),
    });
    // Two-factor authentication on, with the fixed demo secret and recovery codes.
    S.users.push({
      id: uuidFrom(rng),
      email: MOCK_2FA_USER.email,
      password: MOCK_2FA_USER.password,
      role: MOCK_2FA_USER.role,
      active: true,
      last_login_at: iso(t0 - 3 * DAY),
      created_at: iso(t0 - 40 * DAY),
      two_factor_enabled: true,
      totpSecret: DEMO_TOTP_SECRET,
      lastStep: 0,
      recoveryCodes: DEMO_RECOVERY_CODES.map((c) => ({ hash: sha256(c), used: false })),
    });

    // merchants
    const merchantDefs = [
      { id: DEFAULT_MERCHANT_ID, name: "Acme Coffee Roasters", webhook_url: "https://shop.acme-coffee.example/webhooks/cryptopay", age: 110 },
      { name: "Nimbus VPN", webhook_url: "https://api.nimbus-vpn.example/broken/cryptopay-hook", age: 80 },
      { name: "Pixel Forge Games", webhook_url: "https://billing.pixelforge.example/hooks/crypto", age: 55 },
      { name: "Lumen Books", webhook_url: "", age: 30 },
      { name: "Old Storefront (closed)", webhook_url: "https://old.example/down/hook", age: 160, status: "disabled" },
    ];
    for (const d of merchantDefs) {
      const id = d.id ?? uuidFrom(rng);
      const created = t0 - d.age * DAY;
      S.merchants.push({
        id,
        name: d.name,
        status: d.status ?? "active",
        webhook_url: d.webhook_url,
        webhook_secret: d.webhook_url ? `whsec_${hex(rng, 40)}` : "",
        created_at: iso(created),
        updated_at: iso(created + 3 * DAY),
        api_keys: [],
      });
    }
    for (const m of S.merchants) {
      const keys = m.status === "disabled" ? ["Legacy"] : m.name === "Lumen Books" ? [] : ["Production", "Staging"];
      keys.forEach((name, i) => {
        const key = `sk_live_${fromDigest(`${m.id}${name}`, B58, 32)}`;
        m.api_keys.push({
          id: uuidFrom(rng),
          merchant_id: m.id,
          name,
          key,
          prefix: key.slice(0, 12),
          last_used_at: name === "Staging" ? null : iso(t0 - Math.floor(rng() * 48) * 3600 * 1000),
          revoked_at: null,
          created_at: iso(Date.parse(m.created_at) + (i + 1) * DAY),
        });
      });
      if (m.name === "Acme Coffee Roasters") {
        const key = `sk_live_${fromDigest("revoked", B58, 32)}`;
        m.api_keys.push({ id: uuidFrom(rng), merchant_id: m.id, name: "Old laptop", key, prefix: key.slice(0, 12), last_used_at: iso(t0 - 40 * DAY), revoked_at: iso(t0 - 38 * DAY), created_at: iso(t0 - 90 * DAY) });
      }
    }

    // wallets
    const W = (id, name, family, kind, xpub, address, next_index, used, age) => ({
      id,
      name,
      family,
      kind,
      xpub,
      address,
      next_index,
      used,
      created_at: iso(t0 - age * DAY),
    });
    S.wallets.push(
      W(uuidFrom(rng), "Ledger · EVM main account", "evm", "xpub", `xpub6C${fromDigest("evm", B58, 105)}`, "", 128, true, 105),
      W(uuidFrom(rng), "TronLink receiving", "tron", "xpub", `xpub6D${fromDigest("tron", B58, 105)}`, "", 57, true, 100),
      W(uuidFrom(rng), "Sparrow BTC vault", "utxo", "xpub", `zpub6r${fromDigest("btc", B58, 105)}`, "", 34, true, 98),
      W(uuidFrom(rng), "Phantom receiving address", "solana", "static", "", "9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM", 0, true, 90),
      W(uuidFrom(rng), "Old LTC address", "utxo", "static", "", "ltc1qg82tzq6kp7fpmt9s7ad8r3yyysk7qwn8xr6aen", 0, false, 70),
      // Merchants with funds of their own (merchant wallets, docs/architecture.md "Receiving wallets").
      W(uuidFrom(rng), "Pixel Forge · EVM treasury", "evm", "xpub", `xpub6C${fromDigest("pixelforge", B58, 105)}`, "", 41, true, 50),
      W(uuidFrom(rng), "Nimbus VPN · Tron hot wallet", "tron", "static", "", `T${fromDigest("nimbus-tron", B58, 33)}`, 0, true, 45),
      W(uuidFrom(rng), "Acme · Litecoin", "utxo", "static", "", `ltc1q${fromDigest("acme-ltc", BECH32, 38)}`, 0, false, 20),
    );
    const [wEvm, wTron, wBtc, wSol, , wPixel, wNimbus, wAcmeLtc] = S.wallets;

    // chains
    const RPC_DEFAULT = {
      ethereum: ["https://ethereum-rpc.publicnode.com"],
      bsc: ["https://bsc-dataseed.bnbchain.org"],
      polygon: ["https://polygon-rpc.com"],
      arbitrum: ["https://arb1.arbitrum.io/rpc"],
      base: ["https://mainnet.base.org"],
      tron: ["https://api.trongrid.io"],
      bitcoin: ["https://mempool.space/api"],
      litecoin: ["https://litecoinspace.org/api"],
      solana: ["https://api.mainnet-beta.solana.com"],
      sepolia: ["https://ethereum-sepolia-rpc.publicnode.com"],
      "tron-nile": ["https://nile.trongrid.io"],
      "bitcoin-testnet": ["https://mempool.space/testnet/api"],
      "solana-devnet": ["https://api.devnet.solana.com"],
    };
    const NATIVE = { ethereum: "ETH", bsc: "BNB", polygon: "POL", arbitrum: "ETH", base: "ETH", tron: "TRX", bitcoin: "BTC", litecoin: "LTC", solana: "SOL", sepolia: "ETH", "tron-nile": "TRX", "bitcoin-testnet": "BTC", "solana-devnet": "SOL" };
    const ADDR_URL = {
      ethereum: "https://etherscan.io/address/{address}",
      bsc: "https://bscscan.com/address/{address}",
      polygon: "https://polygonscan.com/address/{address}",
      arbitrum: "https://arbiscan.io/address/{address}",
      base: "https://basescan.org/address/{address}",
      tron: "https://tronscan.org/#/address/{address}",
      bitcoin: "https://mempool.space/address/{address}",
      litecoin: "https://litecoinspace.org/address/{address}",
      solana: "https://solscan.io/account/{address}",
      sepolia: "https://sepolia.etherscan.io/address/{address}",
      "tron-nile": "https://nile.tronscan.org/#/address/{address}",
      "bitcoin-testnet": "https://mempool.space/testnet/address/{address}",
      "solana-devnet": "https://solscan.io/account/{address}?cluster=devnet",
    };
    const HEAD = { ethereum: 23_812_440, bsc: 66_201_118, polygon: 79_440_210, arbitrum: 401_220_877, base: 38_120_455, tron: 77_120_334, bitcoin: 924_118, litecoin: 2_988_402, solana: 381_220_118, sepolia: 9_620_118, "tron-nile": 61_220_009, "bitcoin-testnet": 4_701_220, "solana-devnet": 421_884_201 };
    const BLOCK_MS = { ethereum: 12_000, bsc: 3_000, polygon: 2_000, arbitrum: 250, base: 2_000, tron: 3_000, bitcoin: 600_000, litecoin: 150_000, solana: 400, sepolia: 12_000, "tron-nile": 3_000, "bitcoin-testnet": 600_000, "solana-devnet": 400 };
    const plan = {
      ethereum: { wallet: wEvm, db: ["https://eth-mainnet.g.alchemy.com/v2/demo-key-9f2c", "https://ethereum-rpc.publicnode.com"], lag: 2 },
      bsc: { wallet: wEvm, env: ["https://bsc-mainnet.nodereal.io/v1/demo-key"], lag: 1 },
      polygon: { wallet: wEvm, lag: 340 },
      arbitrum: { wallet: wEvm, db: ["https://arb-mainnet.g.alchemy.com/v2/demo-key-11aa"], lag: 0, error: "eth_getLogs: 429 Too Many Requests (https://arb-mainnet.g.alchemy.com/…)" },
      base: { wallet: wEvm, env: ["https://base-mainnet.g.alchemy.com/v2/demo"], lag: 3 },
      tron: { wallet: wTron, db: ["https://api.trongrid.io?apikey=demo"], lag: 1 },
      bitcoin: { wallet: wBtc, db: ["https://btc.example-node.net:8332/wallet"], lag: 0 },
      litecoin: { wallet: null, lag: 0 },
      solana: { wallet: wSol, lag: 12 },
      sepolia: { wallet: wEvm, lag: 0 },
      "tron-nile": { wallet: wTron, lag: 0 },
      "bitcoin-testnet": { wallet: null, enabled: false, lag: 0 },
      "solana-devnet": { wallet: wSol, lag: 4 },
    };
    for (const id of Object.keys(CHAINS)) {
      const p = plan[id] ?? { lag: 0 };
      S.chains.push({
        id,
        native_symbol: NATIVE[id] ?? CHAINS[id].family.toUpperCase(),
        enabled: p.enabled ?? true,
        rpc_db: p.db ?? [],
        rpc_env: p.env ?? [],
        rpc_env_orig: p.env ?? [],
        rpc_default: RPC_DEFAULT[id] ?? [],
        confirmations: CHAINS[id].conf,
        poll_interval_ms: Math.max(1000, Math.min(15_000, BLOCK_MS[id] ?? 3000)),
        scan_batch: CHAINS[id].family === "utxo" ? 10 : CHAINS[id].family === "solana" ? 50 : 500,
        explorer_addr_url: ADDR_URL[id] ?? "",
        wallet_id: p.wallet ? p.wallet.id : null,
        sim_head: HEAD[id] ?? 1_000_000,
        sim_since: t0,
        sim_lag: p.lag,
        block_ms: BLOCK_MS[id] ?? 3000,
        scan_cursor: 0,
        last_head: 0,
        last_scan_at: p.enabled === false ? iso(t0 - 12 * DAY) : null,
        last_error: p.error ?? "",
      });
    }
    // Disabled chain keeps a frozen state.
    const btcTest = S.chains.find((c) => c.id === "bitcoin-testnet");
    if (btcTest) {
      btcTest.last_head = btcTest.sim_head;
      btcTest.scan_cursor = btcTest.sim_head - 15;
    }

    // merchant wallets: Pixel Forge keeps its EVM funds apart, Nimbus VPN its
    // Tron funds; Litecoin has no default wallet, only Acme receives there.
    const merchantId = (name) => S.merchants.find((m) => m.name === name).id;
    for (const [name, wallet, chains, age] of [
      ["Pixel Forge Games", wPixel, ["ethereum", "polygon", "base"], 50],
      ["Nimbus VPN", wNimbus, ["tron"], 45],
      ["Acme Coffee Roasters", wAcmeLtc, ["litecoin"], 20],
    ]) {
      for (const chain_id of chains) {
        S.merchantWallets.push({ merchant_id: merchantId(name), chain_id, wallet_id: wallet.id, created_at: iso(t0 - age * DAY), updated_at: iso(t0 - age * DAY) });
      }
    }

    // assets
    ASSETS.forEach((a, i) => {
      S.assets.push({
        id: a.id,
        symbol: a.symbol,
        name: a.name,
        chain_id: a.chain_id,
        contract_address: a.contract_address,
        decimals: a.decimals,
        is_stablecoin: a.is_stablecoin,
        quote_precision: a.precision,
        rate_key: { USDT: "tether", USDC: "usd-coin", PYUSD: "paypal-usd", BTC: "bitcoin", ETH: "ethereum", TRX: "tron", BNB: "binancecoin", SOL: "solana", LTC: "litecoin" }[a.symbol] ?? a.symbol.toLowerCase(),
        enabled: !(a.id === "usdc-bsc" || a.id === "eth-arbitrum"),
        sort_order: (i + 1) * 10,
        custom: false,
      });
    });
    S.assets.push({
      id: "link-ethereum",
      symbol: "LINK",
      name: "Chainlink",
      chain_id: "ethereum",
      contract_address: "0x514910771AF9Ca656af840dff83E8264EcF986CA",
      decimals: 18,
      is_stablecoin: false,
      quote_precision: 4,
      rate_key: "chainlink",
      enabled: false,
      sort_order: 990,
      custom: true,
      no_rate: true,
    });

    // invoices: scenario invoices keep their ids; add ~140 historical ones
    const merchantsActive = S.merchants.filter((m) => m.status === "active");
    const payableAssets = ASSETS.filter((a) => !a.is_testnet && a.chain_id !== "litecoin");
    const statusPool = ["paid", "paid", "paid", "paid", "paid", "paid", "expired", "expired", "expired", "cancelled", "underpaid", "paid"];
    const titles = ["Order #", "Subscription — 1 month", "Annual plan", "Gift card", "Top-up", "Invoice #"];
    for (let i = 0; i < 140; i++) {
      const m = merchantsActive[Math.floor(rng() * merchantsActive.length)];
      const ageDays = Math.floor(rng() * rng() * 88) + 1;
      const created = t0 - ageDays * DAY - Math.floor(rng() * DAY);
      let status = pick(rng, statusPool);
      const asset = pick(rng, payableAssets);
      const currency = pick(rng, ["USD", "USD", "USD", "EUR", "CNY", "USDT"]);
      const amount = currency === "CNY" ? `${Math.floor(50 + rng() * 2000)}.00` : `${Math.floor(5 + rng() * 480)}.${rng() < 0.5 ? "00" : "99"}`;
      if (status === "cancelled" && rng() < 0.5) status = "expired";
      const noQuote = status === "expired" && rng() < 0.5;
      const title = pick(rng, titles);
      const inv = ctx.createInvoice({
        id: uuidFrom(rng),
        merchant_id: m.id,
        merchant_name: m.name,
        order_id: rng() < 0.85 ? `${m.name.slice(0, 3).toUpperCase()}-${10000 + i * 7}` : "",
        title: title.endsWith("#") ? `${title}${10000 + i * 7}` : title,
        amount,
        currency,
        customer_email: rng() < 0.4 ? `customer${i}@mail.example` : "",
        metadata: rng() < 0.5 ? { cart_id: `c_${hex(rng, 8)}`, items: 1 + Math.floor(rng() * 4) } : {},
        asset_id: noQuote || status === "cancelled" ? undefined : asset.id,
        wallet_kind: asset.family === "solana" ? "static" : "xpub",
        ttl_seconds: 900,
        testnets: false,
      });
      if (status !== "pending") ctx.advance(inv, status === "paid" && rng() < 0.08 ? "overpaid" : status);
      backdate(inv, created, rng);
    }
    // a few live ones in the last hours
    const live = [
      { status: "pending", asset: "usdt-tron", age: 4 },
      { status: "partial", asset: "usdt-ethereum", age: 7 },
      { status: "confirming", asset: "usdc-polygon", age: 3 },
      { status: "pending", asset: undefined, age: 1 },
    ];
    for (const l of live) {
      const m = merchantsActive[Math.floor(rng() * merchantsActive.length)];
      const inv = ctx.createInvoice({
        id: uuidFrom(rng),
        merchant_id: m.id,
        merchant_name: m.name,
        order_id: `${m.name.slice(0, 3).toUpperCase()}-${20000 + Math.floor(rng() * 999)}`,
        title: "Order #" + (20000 + Math.floor(rng() * 999)),
        amount: "149.00",
        currency: "USD",
        asset_id: l.asset,
        ttl_seconds: 3600,
        testnets: false,
        age_seconds: l.age * 60,
      });
      if (l.status !== "pending") ctx.advance(inv, l.status);
    }

    // superseded quote on one scenario invoice (choose → changed coin)
    const partialScenario = invoices.get("44444444-4444-4444-8444-444444444444");
    if (partialScenario?.quote) {
      const old = ctx.makeQuote(partialScenario, ASSETS.find((a) => a.id === "usdt-tron"));
      old.status = "superseded";
      old.created_at = partialScenario.created_at;
      partialScenario.quote.created_at = iso(Date.parse(partialScenario.created_at) + 30_000);
      partialScenario.old_quotes.push(old);
    }

    // invoices settle their current status without emitting events for the seed
    for (const inv of invoices.values()) {
      ctx.tick(inv);
      inv._lastStatus = inv.status;
    }

    // webhook history for seeded invoices
    const sorted = [...invoices.values()].sort((a, b) => Date.parse(a.created_at) - Date.parse(b.created_at));
    for (const inv of sorted) {
      const m = S.merchants.find((x) => x.id === inv.merchant_id);
      if (!m?.webhook_url) continue;
      const created = Date.parse(inv.created_at);
      const types = [];
      if (inv.transfers.length) types.push(["invoice.payment_detected", created + 5 * 60_000]);
      if (inv.status === "paid") types.push(["invoice.confirming", created + 6 * 60_000], ["invoice.paid", Date.parse(inv.paid_at ?? inv.updated_at)]);
      if (inv.status === "expired") types.push(["invoice.expired", Date.parse(inv.expires_at)]);
      if (inv.status === "underpaid") types.push(["invoice.underpaid", Date.parse(inv.expires_at)]);
      if (inv.status === "cancelled") types.push(["invoice.cancelled", Date.parse(inv.updated_at)]);
      for (const [type, atRaw] of types) {
        const at = Math.max(created + 1000, Math.min(atRaw, t0 - 1000)); // history: after creation, not in the future
        // Nimbus VPN's endpoint started failing three days ago.
        const broken = /broken|down/.test(m.webhook_url) && t0 - at < 3 * DAY;
        const recent = t0 - at < 2 * 3600 * 1000;
        enqueueEvent(inv, type, at, {
          id: `evt_${hex(rng, 16)}`,
          status: broken ? (recent ? "pending" : "failed") : "delivered",
          attempts: broken ? (recent ? 2 : 10) : 1,
          next_attempt_at: iso(broken && recent ? t0 + 4 * 60_000 : at),
          last_status_code: broken ? 502 : 200,
          last_error: broken ? "unexpected status 502 Bad Gateway" : "",
          last_response: broken ? "<html><body><h1>502 Bad Gateway</h1></body></html>" : '{"received":true}',
          delivered_at: broken ? null : iso(at + 800),
          _deliverAt: broken && recent ? t0 + 4 * 60_000 : null,
        });
      }
    }
    S.webhookEvents.sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at));

    // unmatched transfers (static-wallet amount mismatches, late payments, unknown tokens)
    const unmatchedDefs = [
      { chain: "ethereum", asset: "usdt-ethereum", symbol: "USDT", amount: "98.97", to: "0x742d35Cc6634C0532925a3b844Bc454e4438f44e", age: 0.2, status: "confirmed" },
      { chain: "solana", asset: "usdc-solana", symbol: "USDC", amount: "250.004", to: "9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM", age: 1.5, status: "confirmed" },
      { chain: "tron", asset: "usdt-tron", symbol: "USDT", amount: "49.5", to: "TNPeeaaFB7K9cmo4uQpcU32zGK8G1NYqeL", age: 3, status: "confirmed" },
      { chain: "bsc", asset: "", symbol: "", amount: "1000", to: "0x742d35Cc6634C0532925a3b844Bc454e4438f44e", age: 6, status: "confirmed" },
      { chain: "polygon", asset: "usdc-polygon", symbol: "USDC", amount: "19.99", to: "0x742d35Cc6634C0532925a3b844Bc454e4438f44e", age: 0.05, status: "pending" },
      { chain: "bitcoin", asset: "btc-bitcoin", symbol: "BTC", amount: "0.00051", to: "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq", age: 12, status: "orphaned" },
    ];
    for (const d of unmatchedDefs) {
      const chain = CHAINS[d.chain];
      const tx = ctx.txHashFor(chain.family);
      const decimals = ASSETS.find((a) => a.id === d.asset)?.decimals ?? 18;
      const detected = t0 - d.age * DAY;
      S.unmatched.push({
        id: uuidFrom(rng),
        chain_id: d.chain,
        asset_id: d.asset,
        symbol: d.symbol,
        tx_hash: tx,
        index: d.asset ? 0 : 3,
        block_number: (HEAD[d.chain] ?? 1_000_000) - Math.floor((d.age * DAY) / (BLOCK_MS[d.chain] ?? 3000)),
        from_address: chain.family === "evm" ? `0x${fromDigest(tx, "0123456789abcdef", 40)}` : chain.family === "tron" ? `T${fromDigest(tx, B58, 33)}` : chain.family === "solana" ? fromDigest(tx, B58, 44) : `bc1q${fromDigest(tx, BECH32, 38)}`,
        to_address: d.to,
        amount: d.amount,
        amount_base: ctx.baseOf(d.amount, decimals),
        status: d.status,
        confirmations: d.status === "pending" ? 9 : chain.conf,
        required_confirmations: chain.conf,
        explorer_url: chain.tx.replace("{tx}", tx),
        invoice_id: null,
        quote_id: null,
        detected_at: iso(detected),
        confirmed_at: d.status === "confirmed" ? iso(detected + 10 * 60_000) : null,
      });
    }

    // audit history
    const owner = S.users[0];
    const adminUser = S.users[1];
    const hist = [
      [owner, "user.create", "user", S.users[1].id, { email: S.users[1].email, role: "admin" }, 60],
      [owner, "user.create", "user", S.users[2].id, { email: S.users[2].email, role: "viewer" }, 20],
      [owner, "settings.update", "settings", "", { rate_markup_bps: { from: 0, to: 100 } }, 50],
      [adminUser, "wallet.create", "wallet", wEvm.id, { family: "evm", kind: "xpub" }, 105],
      [adminUser, "chain.update", "chain", "ethereum", { wallet_id: wEvm.id, rpc_urls: "[2 urls]" }, 104],
      [adminUser, "merchant.create", "merchant", S.merchants[1].id, { name: S.merchants[1].name }, 80],
      [adminUser, "api_key.create", "merchant", S.merchants[1].id, { name: "Production" }, 79],
      [owner, "user.update", "user", S.users[3].id, { active: { from: true, to: false } }, 45],
      [adminUser, "asset.update", "asset", "usdc-bsc", { enabled: { from: true, to: false } }, 12],
      [adminUser, "invoice.mark_paid", "invoice", sorted[5]?.id ?? "", { note: "Customer underpaid by 0.3 USDT, settled off-chain (ticket #4521)" }, 9],
      [adminUser, "invoice.cancel", "invoice", sorted[9]?.id ?? "", null, 6],
      [owner, "merchant.update", "merchant", S.merchants[4].id, { status: { from: "active", to: "disabled" } }, 5],
      [adminUser, "webhook_event.retry", "webhook_event", S.webhookEvents[0]?.id ?? "", null, 1],
    ];
    hist.sort((a, b) => b[5] - a[5]);
    for (const [actor, action, type, id, detail, ageDays] of hist) {
      audit(actor, action, type, id, detail, actor === owner ? "203.0.113.24" : "198.51.100.7", t0 - ageDays * DAY);
    }
    for (let i = 0; i < 25; i++) {
      const u = i % 3 === 0 ? owner : adminUser;
      audit(u, "auth.login", "user", u.id, null, i % 2 ? "203.0.113.24" : "198.51.100.7", t0 - (i * 3.3 + 0.5) * DAY);
    }
    S.audit.sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at));
    S.audit.forEach((a, i) => (a.id = S.audit.length - i));
    S.auditSeq = S.audit.length + 1;

    // e-mail outbox history
    const mfa = S.users.find((u) => u.email === MOCK_2FA_USER.email);
    const hook = S.webhookEvents.find((e) => e.status === "failed");
    const failedMerchant = S.merchants.find((m) => m.id === hook?.merchant_id) ?? S.merchants[1];
    const outboxSeed = [
      ["security.new_login", owner.email, { ip: "203.0.113.24", ua: "Chrome · macOS" }, 0.4, "sent"],
      ["alert.webhook_failed", "ops@cryptopay.test", { merchant: failedMerchant.name, event: hook?.event_type ?? "invoice.paid" }, 0.9, "sent"],
      ["alert.transfer_unmatched", "ops@cryptopay.test", { chain: "Tron", amount: "12.5 USDT" }, 1.2, "sent"],
      ["alert.invoice_underpaid", "ops@cryptopay.test", { invoice: sorted[3]?.id ?? "" }, 1.6, "failed", "dial tcp: lookup smtp.example.com: no such host"],
      ["security.2fa_enabled", mfa.email, {}, 2.5, "sent"],
      ["security.recovery_code_used", mfa.email, { remaining: 9, ip: "198.51.100.7" }, 3.1, "sent"],
      ["alert.scanner_failing", "ops@cryptopay.test", { chain: "BSC", minutes: 14 }, 4.2, "sent"],
      ["security.login_blocked", adminUser.email, { ip: "192.0.2.77" }, 5.5, "sent"],
      ["security.password_changed", adminUser.email, { by: owner.email }, 6, "sent"],
      ["security.account_deactivated", "former.ops@cryptopay.test", { by: owner.email }, 45, "failed", "550 5.1.1 <former.ops@cryptopay.test>: Recipient address rejected"],
      ["security.account_created", S.users[2].email, { by: owner.email, role: "viewer" }, 20, "sent"],
      ["security.role_changed", adminUser.email, { by: owner.email, role: "admin" }, 60, "sent"],
    ];
    for (const [kind, to, vars, ageDays, status, err] of outboxSeed) {
      const m = composeMail(kind, to, vars, t0 - ageDays * DAY);
      m.status = status;
      m.attempts = status === "failed" ? 8 : 1;
      m.last_error = err ?? "";
      m.sent_at = status === "sent" ? iso(t0 - ageDays * DAY + 4000) : null;
      S.outbox.push(m);
    }
    S.outbox.sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at));
  }

  // ---- e-mail ----------------------------------------------------------------

  const MAIL_TITLES = {
    "security.new_login": { zh: "新设备登录了你的账号", en: "New sign-in to your account" },
    "security.login_blocked": { zh: "你的账号登录已被暂时锁定", en: "Sign-in to your account was blocked" },
    "security.password_changed": { zh: "你的密码已修改", en: "Your password was changed" },
    "security.2fa_enabled": { zh: "已启用两步验证", en: "Two-step verification turned on" },
    "security.2fa_disabled": { zh: "已停用两步验证", en: "Two-step verification turned off" },
    "security.2fa_reset": { zh: "你的两步验证已被重置", en: "Your two-step verification was reset" },
    "security.recovery_codes": { zh: "已生成新的恢复码", en: "New recovery codes generated" },
    "security.recovery_code_used": { zh: "有人使用恢复码登录了你的账号", en: "A recovery code was used to sign in" },
    "security.account_created": { zh: "已为你创建管理员账号", en: "An admin account was created for you" },
    "security.role_changed": { zh: "你的角色已变更", en: "Your role was changed" },
    "security.account_deactivated": { zh: "你的账号已被停用", en: "Your account was deactivated" },
    "security.account_reactivated": { zh: "你的账号已重新启用", en: "Your account was reactivated" },
    "alert.webhook_failed": { zh: "Webhook 投递最终失败", en: "Webhook delivery failed" },
    "alert.transfer_unmatched": { zh: "收到未匹配的转账", en: "Unmatched transfer received" },
    "alert.invoice_underpaid": { zh: "订单少付", en: "Invoice underpaid" },
    "alert.scanner_failing": { zh: "扫链持续失败", en: "Chain scanner keeps failing" },
  };

  /** A plain, calm notification in the configured language (no secrets, codes or tokens). */
  function composeMail(kind, to, vars, atMs = now()) {
    const lang = S.settings.email_locale === "en" ? "en" : "zh";
    const title = MAIL_TITLES[kind]?.[lang] ?? kind;
    const when = new Date(atMs).toISOString().replace("T", " ").replace(/\.\d+Z$/, " UTC");
    const facts = Object.entries(vars).map(([k, v]) => `  ${k}: ${v}`);
    const notYou =
      lang === "zh"
        ? "如果这不是你本人或你的同事的操作，请立即登录管理后台修改密码、检查「账户安全」，并联系所有者。"
        : "If this wasn't you or a colleague, sign in now, change your password, check Account security and contact an Owner.";
    const body = [
      lang === "zh" ? "你好，" : "Hello,",
      "",
      title + (lang === "zh" ? "。" : "."),
      "",
      `${lang === "zh" ? "时间" : "When"}: ${when}`,
      ...facts,
      "",
      kind.startsWith("security.") ? notYou : lang === "zh" ? "请在管理后台查看详情。" : "See the admin console for details.",
      "",
      "— CryptoPay",
    ].join("\n");
    return {
      id: randomUUID(),
      kind,
      to,
      subject: `[CryptoPay] ${title}`,
      body_text: body,
      status: "pending",
      attempts: 0,
      last_error: "",
      created_at: iso(atMs),
      sent_at: null,
    };
  }

  /** "Delivers" a queued message after a moment (addresses containing bounce / fail are rejected). */
  function deliverLater(m) {
    const timer = setTimeout(() => {
      if (m.status !== "pending") return;
      m.attempts += 1;
      if (/bounce|fail/i.test(m.to)) {
        m.status = "failed";
        m.last_error = `550 5.1.1 <${m.to}>: Recipient address rejected`;
      } else {
        m.status = "sent";
        m.sent_at = iso(now());
        m.last_error = "";
      }
    }, 1500);
    timer.unref?.();
  }

  /**
   * Queues a mail like the gateway does: only when e-mail is configured and
   * the matching toggle is on; `throttleMs` drops it when one of the same
   * kind went to the same address within that time.
   */
  function enqueueMail(kind, to, vars, throttleMs = 0) {
    if (!S.email.configured) return;
    if (kind.startsWith("security.") && !S.settings.security_emails) return;
    if (kind.startsWith("alert.") && !S.settings.alert_emails) return;
    if (throttleMs && S.outbox.some((m) => m.kind === kind && m.to === to && Date.parse(m.created_at) > now() - throttleMs)) return;
    const m = composeMail(kind, to, vars);
    S.outbox.unshift(m);
    deliverLater(m);
  }

  /** Rewrites the timestamps of a seeded invoice so history spreads over days. */
  function backdate(inv, created, rng) {
    const shift = (ms) => created + ms;
    inv.created_at = iso(created);
    inv.expires_at = iso(shift(15 * 60_000));
    if (inv.quote) inv.quote.created_at = iso(shift(60_000));
    inv.transfers.forEach((t, i) => {
      t.detected_at = iso(shift((4 + i) * 60_000));
      if (t.confirmed_at) t.confirmed_at = iso(shift((9 + i) * 60_000 + Math.floor(rng() * 300_000)));
      t.block_number = 10_000_000 + Math.floor(rng() * 9_000_000);
      t._since = 0;
    });
    if (inv.paid_at) inv.paid_at = iso(shift(12 * 60_000 + Math.floor(rng() * 300_000)));
    inv.updated_at = inv.paid_at ?? (inv.status === "cancelled" ? iso(shift(3 * 60_000)) : inv.expires_at);
    if (inv.status === "paid" && rng() < 0.06) inv.paid_late = true;
  }

  // ---- auth --------------------------------------------------------------------

  function parseCookies(req) {
    const out = {};
    for (const part of String(req.headers.cookie || "").split(";")) {
      const [k, ...v] = part.trim().split("=");
      if (k) out[k] = decodeURIComponent(v.join("="));
    }
    return out;
  }

  /** The request's session and user, or null. */
  function currentSession(req) {
    const token = parseCookies(req).cp_session;
    if (!token) return null;
    const s = S.sessions.get(token);
    if (!s || s.expires < now()) return null;
    const u = S.users.find((x) => x.id === s.userId);
    return u && u.active ? { token, session: s, user: u } : null;
  }

  function createSession(u, ip, ua) {
    // Security mail for a sign-in from an IP this account never signed in from (not on its first sign-in).
    const logins = S.audit.filter((l) => l.action === "auth.login" && l.target_id === u.id);
    if (logins.length > 0 && !logins.some((l) => l.ip === ip)) enqueueMail("security.new_login", u.email, { ip, user_agent: ua || "-" });
    const token = randomBytes(24).toString("base64url");
    S.sessions.set(token, { userId: u.id, expires: now() + SESSION_MAX_AGE * 1000 });
    u.last_login_at = iso(now());
    audit(u, "auth.login", "user", u.id, null, ip);
    return {
      body: publicUser(u),
      headers: { "Set-Cookie": `cp_session=${token}; Path=/; HttpOnly; SameSite=Lax; Max-Age=${SESSION_MAX_AGE}` },
    };
  }

  function signOutUser(userId, exceptToken) {
    let n = 0;
    for (const [tok, s] of S.sessions) {
      if (s.userId === userId && tok !== exceptToken) {
        S.sessions.delete(tok);
        n++;
      }
    }
    for (const [h, c] of S.challenges) if (c.userId === userId) S.challenges.delete(h);
    return n;
  }

  const CLEAR_COOKIE = { "Set-Cookie": "cp_session=; Path=/; HttpOnly; SameSite=Lax; Max-Age=0" };

  const clientIp = (req) => String(req.headers["x-forwarded-for"] || req.socket.remoteAddress || "").split(",")[0].trim().replace(/^::ffff:/, "");

  // ---- routes ------------------------------------------------------------------

  async function handle(req, res, url, parts, method) {
    if (DELAY_MS > 0) await new Promise((r) => setTimeout(r, DELAY_MS));
    try {
      const out = await route(req, res, url, parts, method);
      if (out === undefined) return;
      if (out.status === 204) return ctx.send(res, 204, undefined, out.headers);
      return ctx.send(res, out.status ?? 200, out.body, out.headers);
    } catch (e) {
      if (e instanceof HttpError) return ctx.apiError(res, e.status, e.code, e.message, e.fields);
      console.error(e);
      return ctx.apiError(res, 500, "internal", e instanceof Error ? e.message : "internal error");
    }
  }

  async function route(req, res, url, parts, method) {
    const ip = clientIp(req);
    const mutating = method !== "GET" && method !== "HEAD";
    if (mutating && String(req.headers["x-requested-with"] || "").toLowerCase() !== "cryptopay") {
      throw new HttpError(403, "forbidden", "missing X-Requested-With: cryptopay header (CSRF guard)");
    }
    const [a, b, c, d] = parts;
    const body = mutating ? await ctx.readJson(req) : null;
    if (mutating && body === null) throw bad("request body is not valid JSON");

    const ua = String(req.headers["user-agent"] || "");
    // -- auth (no session needed for login) --
    if (a === "auth" && b === "login" && !c && method === "POST") return login(body, ip, ua);
    if (a === "auth" && b === "login" && c === "2fa" && method === "POST") return loginTwoFactor(body, ip, ua);

    const cur = currentSession(req);
    if (!cur) throw new HttpError(401, "unauthorized", "not signed in");
    const { user, token } = cur;

    // require_2fa: only the setup flow (and a few account endpoints) until it is done.
    if (S.settings.require_2fa && !user.two_factor_enabled) {
      const allowed =
        a === "auth" &&
        ((b === "me" && method === "GET") ||
          (b === "logout" && method === "POST") ||
          (b === "password" && method === "POST") ||
          (b === "2fa" && (c === "setup" || c === "enable") && method === "POST"));
      if (!allowed) throw new HttpError(403, "two_factor_setup_required", "two-factor authentication must be enabled for this account first");
    }

    if (a === "auth") {
      if (b === "logout" && method === "POST") {
        S.sessions.delete(token);
        audit(user, "auth.logout", "user", user.id, null, ip);
        return { status: 204, headers: CLEAR_COOKIE };
      }
      if (b === "me" && method === "GET") {
        return { body: { ...publicUser(user), two_factor_setup_required: Boolean(S.settings.require_2fa && !user.two_factor_enabled) } };
      }
      if (b === "password" && method === "POST") {
        const fields = {};
        if (typeof body.current_password !== "string" || body.current_password !== user.password) fields.current_password = "is incorrect";
        if (typeof body.new_password !== "string" || body.new_password.length < 8) fields.new_password = "must be at least 8 characters";
        if (Object.keys(fields).length) throw bad("invalid password change", fields);
        user.password = body.new_password;
        const n = signOutUser(user.id, token);
        audit(user, "auth.password_change", "admin_user", user.id, { other_sessions_revoked: n }, ip);
        enqueueMail("security.password_changed", user.email, { by: user.email, ip });
        return { status: 204 };
      }
      if (b === "2fa") return twoFactorRoute(user, token, ip, method, c, body);
      throw notFound("endpoint");
    }

    // viewers: read only
    if (mutating && user.role === "viewer") throw new HttpError(403, "forbidden", "viewers cannot make changes");

    switch (a) {
      case "overview":
        if (method === "GET") return { body: overview(url) };
        break;
      case "invoices":
        return invoicesRoute(user, ip, method, url, b, c, body);
      case "transfers":
        return transfersRoute(user, ip, method, url, b, c, body);
      case "merchants":
        return merchantsRoute(user, ip, method, url, b, c, d, body);
      case "chains":
        return chainsRoute(user, ip, method, b, c, body);
      case "assets":
        return assetsRoute(user, ip, method, b, body);
      case "wallets":
        return walletsRoute(user, ip, method, b, body);
      case "webhook-events":
        return webhooksRoute(user, ip, method, url, b, c);
      case "settings":
        return settingsRoute(user, ip, method, body);
      case "users":
        if (user.role !== "owner") throw new HttpError(403, "forbidden", "user management is owner-only");
        return usersRoute(user, ip, method, b, c, body);
      case "email":
        return emailRoute(user, ip, method, url, b, c, d, body);
      case "audit-logs":
        if (method === "GET") return { body: auditList(url) };
        break;
    }
    throw notFound("endpoint");
  }

  function login(body, ip, ua) {
    const email = String(body?.email ?? "").trim().toLowerCase();
    const password = String(body?.password ?? "");
    if (!email || !password) throw bad("email and password are required", { ...(email ? {} : { email: "required" }), ...(password ? {} : { password: "required" }) });
    const key = `${email}|${ip}`;
    const fails = (S.loginFailures.get(key) ?? []).filter((t) => t > now() - LOGIN_WINDOW_MS);
    if (fails.length >= LOGIN_MAX_FAILURES) throw new HttpError(429, "rate_limited", "too many failed sign-in attempts; try again later");
    const u = S.users.find((x) => x.email === email);
    if (!u || u.password !== password || !u.active) {
      fails.push(now());
      S.loginFailures.set(key, fails);
      if (u && u.active && fails.length >= LOGIN_MAX_FAILURES) enqueueMail("security.login_blocked", u.email, { ip }, 3600_000);
      throw new HttpError(401, "unauthorized", "invalid email or password");
    }
    S.loginFailures.delete(key);
    if (u.two_factor_enabled) {
      const challenge = randomBytes(32).toString("base64url");
      const expires = now() + CHALLENGE_TTL_MS;
      S.challenges.set(sha256(challenge), { userId: u.id, expires, attempts: 0 });
      return { status: 202, body: { two_factor_required: true, challenge_token: challenge, expires_at: iso(expires) } };
    }
    return createSession(u, ip, ua);
  }

  /** `POST /auth/login/2fa`: every failure is the same 401 (docs/api.md). */
  function loginTwoFactor(body, ip, ua) {
    const tok = typeof body?.challenge_token === "string" ? body.challenge_token : "";
    const hasCode = typeof body?.code === "string" && body.code !== "";
    const hasRc = typeof body?.recovery_code === "string" && body.recovery_code !== "";
    if (!tok || hasCode === hasRc) {
      throw bad("send challenge_token and exactly one of code / recovery_code", {
        ...(tok ? {} : { challenge_token: "required" }),
        ...(hasCode === hasRc ? { code: "send exactly one of code / recovery_code" } : {}),
      });
    }
    const failed = () => new HttpError(401, "unauthorized", "the verification failed; check the code or sign in again");
    const h = sha256(tok);
    const ch = S.challenges.get(h);
    if (!ch || ch.expires <= now()) {
      S.challenges.delete(h);
      throw failed();
    }
    const u = S.users.find((x) => x.id === ch.userId);
    if (!u || !u.active || !u.two_factor_enabled) {
      S.challenges.delete(h);
      throw failed();
    }
    const fkey = u.id;
    const fails = (S.twoFactorFailures.get(fkey) ?? []).filter((t) => t > now() - LOGIN_WINDOW_MS);
    if (fails.length >= TWO_FA_MAX_FAILURES) throw new HttpError(429, "rate_limited", "too many wrong codes; try again later");
    let usedRecovery = false;
    if (hasCode) {
      const step = verifyTotp(u.totpSecret, body.code.trim(), u.lastStep ?? 0);
      if (step !== null) u.lastStep = step;
      else return wrongCode();
    } else {
      const rc = u.recoveryCodes.find((r) => !r.used && r.hash === sha256(normRecovery(body.recovery_code)));
      if (!rc) return wrongCode();
      rc.used = true;
      usedRecovery = true;
    }
    function wrongCode() {
      ch.attempts += 1;
      if (ch.attempts >= CHALLENGE_MAX_ATTEMPTS) S.challenges.delete(h);
      fails.push(now());
      S.twoFactorFailures.set(fkey, fails);
      if (fails.length === TWO_FA_MAX_FAILURES) enqueueMail("security.login_blocked", u.email, { ip }, 3600_000);
      throw failed();
    }
    S.challenges.delete(h);
    S.twoFactorFailures.delete(fkey);
    const out = createSession(u, ip, ua);
    if (usedRecovery) {
      enqueueMail("security.recovery_code_used", u.email, { remaining: u.recoveryCodes.filter((r) => !r.used).length, ip });
    }
    return out;
  }

  // ---- own two-factor authentication ------------------------------------------

  function checkPassword(user, body) {
    if (typeof body?.current_password !== "string" || body.current_password === "") throw bad("current_password is required", { current_password: "is required" });
    if (body.current_password !== user.password) throw bad("invalid request", { current_password: "is incorrect" });
  }

  /** A TOTP code or (allowRecovery) an unused recovery code; consumes it. */
  function checkCode(user, body, allowRecovery) {
    const code = String(body?.code ?? "").trim();
    if (!code) throw bad("code is required", { code: "is required" });
    if (/^\d{6}$/.test(code)) {
      const step = verifyTotp(user.totpSecret, code, user.lastStep ?? 0);
      if (step !== null) {
        user.lastStep = step;
        return;
      }
    } else if (allowRecovery) {
      const rc = user.recoveryCodes.find((r) => !r.used && r.hash === sha256(normRecovery(code)));
      if (rc) {
        rc.used = true;
        return;
      }
    }
    throw bad("invalid request", { code: "is incorrect" });
  }

  function issueRecoveryCodes(user) {
    const codes = newRecoveryCodes();
    user.recoveryCodes = codes.map((c) => ({ hash: sha256(c), used: false }));
    return codes;
  }

  function twoFactorRoute(user, token, ip, method, action, body) {
    if (method !== "POST") throw notFound("endpoint");
    if (action === "setup") {
      checkPassword(user, body);
      if (user.two_factor_enabled) throw conflict("two-factor authentication is already enabled");
      user.pendingSecret = base32Encode(randomBytes(20));
      const label = encodeURIComponent(`${ISSUER}:${user.email}`);
      const otpauth = `otpauth://totp/${label}?secret=${user.pendingSecret}&issuer=${ISSUER}&algorithm=SHA1&digits=6&period=30`;
      audit(user, "auth.2fa_setup", "admin_user", user.id, null, ip);
      return { body: { secret: user.pendingSecret, otpauth_uri: otpauth } };
    }
    if (action === "enable") {
      if (user.two_factor_enabled) throw conflict("two-factor authentication is already enabled");
      if (!user.pendingSecret) throw conflict("start the setup first (POST /auth/2fa/setup)");
      const code = String(body?.code ?? "").trim();
      if (!code) throw bad("code is required", { code: "is required" });
      const step = verifyTotp(user.pendingSecret, code, 0);
      if (step === null) throw bad("invalid request", { code: "is incorrect" });
      user.totpSecret = user.pendingSecret;
      user.pendingSecret = null;
      user.lastStep = step;
      user.two_factor_enabled = true;
      const codes = issueRecoveryCodes(user);
      const n = signOutUser(user.id, token);
      audit(user, "auth.2fa_enable", "admin_user", user.id, { other_sessions_revoked: n }, ip);
      enqueueMail("security.2fa_enabled", user.email, { ip });
      return { body: { recovery_codes: codes } };
    }
    if (action === "disable") {
      if (S.settings.require_2fa) throw conflict("two-factor authentication is required for every admin (setting require_2fa)");
      checkPassword(user, body);
      if (!user.two_factor_enabled) throw conflict("two-factor authentication is not enabled");
      checkCode(user, body, true);
      user.two_factor_enabled = false;
      user.totpSecret = null;
      user.recoveryCodes = [];
      user.lastStep = 0;
      audit(user, "auth.2fa_disable", "admin_user", user.id, null, ip);
      enqueueMail("security.2fa_disabled", user.email, { ip });
      return { status: 204 };
    }
    if (action === "recovery-codes") {
      checkPassword(user, body);
      if (!user.two_factor_enabled) throw conflict("two-factor authentication is not enabled");
      checkCode(user, body, false); // a code from the app only, like the gateway
      const codes = issueRecoveryCodes(user);
      audit(user, "auth.recovery_codes", "admin_user", user.id, null, ip);
      enqueueMail("security.recovery_codes", user.email, { ip });
      return { body: { recovery_codes: codes } };
    }
    throw notFound("endpoint");
  }

  // ---- overview ----------------------------------------------------------------

  function overview(url) {
    const raw = url.searchParams.get("days");
    const days = raw === null ? 30 : Number(raw);
    if (!Number.isInteger(days) || days < 1 || days > 365) throw bad("days must be 1..365", { days: "must be an integer from 1 to 365" });
    const end = new Date();
    const startDay = Date.UTC(end.getUTCFullYear(), end.getUTCMonth(), end.getUTCDate()) - (days - 1) * DAY;
    const inRange = [...invoices.values()].filter((inv) => {
      ctx.tick(inv);
      syncWebhooks(inv);
      return Date.parse(inv.created_at) >= startDay;
    });
    const daily = new Map();
    for (let i = 0; i < days; i++) daily.set(new Date(startDay + i * DAY).toISOString().slice(0, 10), { invoices: 0, paid: 0, vol: 0n });
    const byAsset = new Map();
    const byStatus = new Map();
    let volume = 0n;
    let paid = 0;
    let open = 0;
    for (const inv of inRange) {
      const day = inv.created_at.slice(0, 10);
      const row = daily.get(day);
      if (row) row.invoices += 1;
      byStatus.set(inv.status, (byStatus.get(inv.status) ?? 0) + 1);
      if (["pending", "partial", "confirming"].includes(inv.status)) open += 1;
      if (inv.status === "paid") {
        paid += 1;
        const usd = amountUsd(inv) ?? "0";
        const v = toScaled(usd);
        volume += v;
        const paidDay = (inv.paid_at ?? inv.created_at).slice(0, 10);
        const prow = daily.get(paidDay) ?? row;
        if (prow) {
          prow.paid += 1;
          prow.vol += v;
        }
        if (inv.quote) {
          const k = inv.quote.asset_id;
          const cur = byAsset.get(k) ?? { asset_id: k, symbol: inv.quote.symbol, chain_id: inv.quote.chain_id, chain_name: inv.quote.chain_name, paid: 0, vol: 0n };
          cur.paid += 1;
          cur.vol += v;
          byAsset.set(k, cur);
        }
      }
    }
    return {
      range_days: days,
      totals: {
        invoices: inRange.length,
        paid_invoices: paid,
        open_invoices: open,
        volume_usd: fromScaled(volume),
        conversion_rate: inRange.length ? Math.round((paid / inRange.length) * 10000) / 10000 : 0,
        unmatched_transfers: S.unmatched.filter((t) => t.invoice_id === null && t.status !== "orphaned").length,
        failed_webhooks: S.webhookEvents.filter((e) => (deliver(e), e.status === "failed")).length,
      },
      daily: [...daily.entries()].map(([date, r]) => ({ date, invoices: r.invoices, paid: r.paid, volume_usd: fromScaled(r.vol) })),
      by_asset: [...byAsset.values()].map(({ vol, ...r }) => ({ ...r, volume_usd: fromScaled(vol) })),
      by_status: [...byStatus.entries()].map(([status, count]) => ({ status, count })),
    };
  }

  // ---- invoices ----------------------------------------------------------------

  function findInvoice(id) {
    const inv = invoices.get(id);
    if (!inv) throw notFound("invoice");
    return inv;
  }

  function invoicesRoute(user, ip, method, url, id, action, body) {
    if (!id && method === "GET") {
      const q = (url.searchParams.get("q") || "").trim().toLowerCase();
      const status = url.searchParams.get("status");
      const merchantId = url.searchParams.get("merchant_id");
      const from = url.searchParams.get("from");
      const to = url.searchParams.get("to");
      const STATUSES = ["pending", "partial", "confirming", "paid", "underpaid", "expired", "cancelled"];
      if (status && !STATUSES.includes(status)) throw bad("unknown status", { status: "unknown status" });
      for (const [k, v] of [["from", from], ["to", to]]) if (v && Number.isNaN(Date.parse(v))) throw bad(`${k} must be RFC 3339`, { [k]: "must be an RFC 3339 timestamp" });
      let list = [...invoices.values()].map((inv) => {
        ctx.tick(inv);
        syncWebhooks(inv);
        return inv;
      });
      if (status) list = list.filter((i) => i.status === status);
      if (merchantId) list = list.filter((i) => i.merchant_id === merchantId);
      if (from) list = list.filter((i) => Date.parse(i.created_at) >= Date.parse(from));
      if (to) list = list.filter((i) => Date.parse(i.created_at) <= Date.parse(to));
      if (q) {
        list = list.filter(
          (i) =>
            i.id.toLowerCase().includes(q) ||
            (i.order_id || "").toLowerCase().includes(q) ||
            [i.quote, ...(i.old_quotes ?? [])].some((qq) => qq && qq.address.toLowerCase() === q) ||
            i.transfers.some((t) => t.tx_hash.toLowerCase() === q),
        );
      }
      list.sort((x, y) => Date.parse(y.created_at) - Date.parse(x.created_at));
      const page = paginate(list, url);
      return { body: { ...page, items: page.items.map((inv) => ({ ...invoiceView(inv) })) } };
    }
    if (!id && method === "POST") return createInvoice(user, ip, body);
    if (id && !action && method === "GET") return { body: invoiceDetail(findInvoice(id)) };
    if (id && action === "cancel" && method === "POST") {
      const inv = findInvoice(id);
      ctx.tick(inv);
      if (inv.status !== "pending") throw conflict(`only pending invoices can be cancelled (status is ${inv.status})`);
      if (inv.quote && inv.quote.received_base > 0n) throw conflict("funds were already detected for this invoice");
      ctx.advance(inv, "cancelled");
      if (inv.quote) inv.quote.status = "closed";
      syncWebhooks(inv);
      audit(user, "invoice.cancel", "invoice", inv.id, null, ip);
      return { body: invoiceView(inv) };
    }
    if (id && action === "mark-paid" && method === "POST") {
      const inv = findInvoice(id);
      ctx.tick(inv);
      const note = typeof body.note === "string" ? body.note.trim() : "";
      if (!note) throw bad("note is required", { note: "required" });
      if (inv.status === "paid" || inv.status === "cancelled") throw conflict(`invoice is already ${inv.status}`);
      inv.status = "paid";
      inv.paid_at = inv.paid_at ?? iso(now());
      inv.updated_at = iso(now());
      if (inv.quote) inv.quote.status = "paid";
      syncWebhooks(inv);
      audit(user, "invoice.mark_paid", "invoice", inv.id, { note }, ip);
      return { body: invoiceView(inv) };
    }
    throw notFound("endpoint");
  }

  function createInvoice(user, ip, body) {
    const fields = {};
    const m = S.merchants.find((x) => x.id === body.merchant_id);
    if (!body.merchant_id) fields.merchant_id = "required";
    else if (!m) fields.merchant_id = "unknown merchant";
    if (!isDecimal(body.amount) || /^0+(\.0+)?$/.test(String(body.amount).trim())) fields.amount = "must be a decimal string > 0";
    if (typeof body.currency !== "string" || !/^[A-Za-z0-9]{3,10}$/.test(body.currency)) fields.currency = "must be a currency code";
    if (body.order_id !== undefined && (typeof body.order_id !== "string" || body.order_id.length > 128)) fields.order_id = "max 128 characters";
    if (body.title !== undefined && (typeof body.title !== "string" || body.title.length > 200)) fields.title = "max 200 characters";
    if (body.description !== undefined && (typeof body.description !== "string" || body.description.length > 2000)) fields.description = "max 2000 characters";
    for (const k of ["success_url", "cancel_url"]) if (body[k] !== undefined && body[k] !== "" && !isHttpUrl(body[k])) fields[k] = "must be an http(s) URL";
    if (body.customer_email !== undefined && body.customer_email !== "" && !isEmail(body.customer_email)) fields.customer_email = "invalid email";
    if (body.expires_in !== undefined && !isInt(body.expires_in, 60, 86400)) fields.expires_in = "must be 60..86400 seconds";
    if (body.metadata !== undefined && (typeof body.metadata !== "object" || body.metadata === null || JSON.stringify(body.metadata).length > 4096)) fields.metadata = "must be an object up to 4 KB";
    if (Object.keys(fields).length) throw bad("invalid invoice", fields);
    if (m.status !== "active") throw unprocessable("merchant is disabled", { merchant_id: "merchant is disabled" });
    let asset = null;
    if (body.asset_id) {
      asset = ASSETS.find((x) => x.id === body.asset_id);
      if (!asset || !isAssetPayable(asset, m.id)) throw unprocessable("asset is not payable right now", { asset_id: "not payable right now" });
    }
    const currency = body.currency.toUpperCase();
    if (body.order_id) {
      const existing = [...invoices.values()].find((i) => i.merchant_id === m.id && i.order_id === body.order_id);
      if (existing) {
        if (existing.amount === body.amount.trim() && existing.currency === currency) return { status: 200, body: invoiceView(existing) };
        throw conflict("order_id was already used with a different amount or currency", { order_id: "already used with a different amount or currency" });
      }
    }
    const wallet = asset ? resolveWallet(m.id, asset.chain_id) : null;
    const inv = ctx.createInvoice({
      merchant_id: m.id,
      merchant_name: m.name,
      order_id: body.order_id ?? "",
      title: body.title ?? "",
      description: body.description ?? "",
      amount: body.amount.trim(),
      currency,
      success_url: body.success_url ?? "",
      cancel_url: body.cancel_url ?? "",
      customer_email: body.customer_email ?? "",
      metadata: body.metadata ?? {},
      asset_id: asset?.id,
      wallet_kind: wallet?.kind ?? "xpub",
      ttl_seconds: body.expires_in ?? S.settings.invoice_ttl_seconds,
      testnets: true,
    });
    inv._lastStatus = inv.status;
    audit(user, "invoice.create", "invoice", inv.id, { merchant_id: m.id, amount: inv.amount, currency }, ip);
    return { status: 201, body: invoiceView(inv) };
  }

  // ---- transfers -----------------------------------------------------------------

  function allTransfers() {
    const out = [];
    for (const inv of invoices.values()) {
      ctx.tick(inv);
      for (const t of inv.transfers) out.push(stripTransfer(t));
    }
    for (const t of S.unmatched) out.push(t);
    return out;
  }

  function transfersRoute(user, ip, method, url, id, action, body) {
    if (!id && method === "GET") {
      const chainId = url.searchParams.get("chain_id");
      const status = url.searchParams.get("status");
      const matched = url.searchParams.get("matched");
      const q = (url.searchParams.get("q") || "").trim().toLowerCase();
      if (matched && matched !== "true" && matched !== "false") throw bad("matched must be true or false", { matched: "must be true or false" });
      let list = allTransfers();
      if (chainId) list = list.filter((t) => t.chain_id === chainId);
      if (status) list = list.filter((t) => t.status === status);
      if (matched === "true") list = list.filter((t) => t.invoice_id !== null);
      if (matched === "false") list = list.filter((t) => t.invoice_id === null);
      if (q) list = list.filter((t) => [t.tx_hash, t.from_address, t.to_address].some((v) => v.toLowerCase().includes(q)));
      list.sort((a, b) => Date.parse(b.detected_at) - Date.parse(a.detected_at));
      return { body: paginate(list, url) };
    }
    if (id && action === "assign" && method === "POST") {
      const tr = S.unmatched.find((t) => t.id === id);
      if (!tr) {
        if (allTransfers().some((t) => t.id === id)) throw conflict("transfer is already matched to an invoice");
        throw notFound("transfer");
      }
      if (tr.status === "orphaned") throw conflict("orphaned transfers cannot be assigned");
      if (typeof body.invoice_id !== "string" || !body.invoice_id) throw bad("invoice_id is required", { invoice_id: "required" });
      const inv = invoices.get(body.invoice_id);
      if (!inv) throw unprocessable("invoice not found", { invoice_id: "invoice not found" });
      const quote = [inv.quote, ...(inv.old_quotes ?? [])].find((q) => q && q.asset_id === tr.asset_id && q.chain_id === tr.chain_id);
      if (!tr.asset_id || !quote) throw unprocessable(`invoice has no quote for ${tr.symbol || "this token"} on ${tr.chain_id}`, { invoice_id: "no quote for this asset" });
      S.unmatched = S.unmatched.filter((t) => t.id !== tr.id);
      const assigned = { ...tr, invoice_id: inv.id, quote_id: quote.id, _since: 0 };
      inv.transfers.unshift(assigned);
      const base = BigInt(tr.amount_base);
      quote.received_base += base;
      if (tr.status === "confirmed") quote.confirmed_base += base;
      const due = BigInt(quote.amount_base);
      const wasClosed = ["expired", "underpaid"].includes(inv.status);
      if (quote.confirmed_base >= due) {
        inv.status = "paid";
        inv.paid_at = iso(now());
        inv.paid_late = wasClosed;
        inv.overpaid = quote.confirmed_base > due;
        quote.status = "paid";
      } else if (quote.received_base >= due) {
        inv.status = "confirming";
      } else if (!wasClosed && inv.status !== "paid") {
        inv.status = "partial";
      }
      inv.updated_at = iso(now());
      syncWebhooks(inv);
      audit(user, "transfer.assign", "transfer", tr.id, { invoice_id: inv.id, amount: tr.amount, symbol: tr.symbol }, ip);
      return { body: stripTransfer(assigned) };
    }
    throw notFound("endpoint");
  }

  // ---- merchants -----------------------------------------------------------------

  function findMerchant(id) {
    const m = S.merchants.find((x) => x.id === id);
    if (!m) throw notFound("merchant");
    return m;
  }

  function merchantsRoute(user, ip, method, url, id, sub, keyId, body) {
    if (!id && method === "GET") {
      const q = (url.searchParams.get("q") || "").trim().toLowerCase();
      let list = [...S.merchants];
      if (q) list = list.filter((m) => m.name.toLowerCase().includes(q) || m.id === q);
      list.sort((a, b) => a.name.localeCompare(b.name));
      const page = paginate(list, url);
      return { body: { ...page, items: page.items.map(publicMerchant) } };
    }
    if (!id && method === "POST") {
      const fields = {};
      const name = typeof body.name === "string" ? body.name.trim() : "";
      if (!name) fields.name = "required";
      else if (name.length > 200) fields.name = "max 200 characters";
      const webhookUrl = typeof body.webhook_url === "string" ? body.webhook_url.trim() : "";
      if (webhookUrl && !isHttpUrl(webhookUrl)) fields.webhook_url = "must be an http(s) URL";
      if (Object.keys(fields).length) throw bad("invalid merchant", fields);
      if (S.merchants.some((m) => m.name.toLowerCase() === name.toLowerCase())) throw conflict("a merchant with this name already exists", { name: "already exists" });
      const t = iso(now());
      const secret = `whsec_${randomBytes(20).toString("hex")}`;
      const m = { id: randomUUID(), name, status: "active", webhook_url: webhookUrl, webhook_secret: secret, created_at: t, updated_at: t, api_keys: [] };
      S.merchants.push(m);
      audit(user, "merchant.create", "merchant", m.id, { name }, ip);
      return { status: 201, body: { merchant: publicMerchant(m), webhook_secret: secret } };
    }
    const m = findMerchant(id);
    if (!sub && method === "GET") {
      const order = (b) => {
        const c = CHAINS[b.chain_id];
        return `${c.testnet ? 1 : 0}${c.name}`;
      };
      const wallets = S.merchantWallets.filter((b) => b.merchant_id === m.id).sort((a, b) => order(a).localeCompare(order(b)));
      return { body: { merchant: publicMerchant(m), api_keys: m.api_keys.map(publicKey), wallets: wallets.map(merchantWalletView) } };
    }
    if (sub === "wallets" && keyId && (method === "PUT" || method === "DELETE")) {
      const c = findChain(keyId);
      const family = CHAINS[c.id].family;
      const existing = S.merchantWallets.find((b) => b.merchant_id === m.id && b.chain_id === c.id);
      if (method === "DELETE") {
        if (existing) {
          S.merchantWallets = S.merchantWallets.filter((b) => b !== existing);
          audit(user, "merchant.wallet.unset", "merchant", m.id, { chain_id: c.id, wallet_id: existing.wallet_id }, ip);
        }
        return { status: 204 };
      }
      const walletId = typeof body?.wallet_id === "string" ? body.wallet_id.trim() : "";
      const w = S.wallets.find((x) => x.id === walletId);
      if (!walletId) throw bad("invalid merchant wallet", { wallet_id: "is required" });
      if (!w) throw bad("invalid merchant wallet", { wallet_id: "unknown wallet" });
      if (w.family !== family) throw bad("invalid merchant wallet", { wallet_id: `the wallet is a ${w.family} wallet, the chain needs a ${family} wallet` });
      if (w.kind === "static" && !staticAddressValidOn(w.address, c.id)) throw bad("invalid merchant wallet", { wallet_id: `the wallet's address is not valid on ${CHAINS[c.id].name}` });
      const t = iso(now());
      const previous = existing ? existing.wallet_id : null;
      if (existing) {
        existing.wallet_id = w.id;
        existing.updated_at = t;
      } else {
        S.merchantWallets.push({ merchant_id: m.id, chain_id: c.id, wallet_id: w.id, created_at: t, updated_at: t });
      }
      audit(user, "merchant.wallet.set", "merchant", m.id, { chain_id: c.id, wallet_id: w.id, wallet_name: w.name, previous_wallet_id: previous }, ip);
      return { body: merchantWalletView(S.merchantWallets.find((b) => b.merchant_id === m.id && b.chain_id === c.id)) };
    }
    if (!sub && method === "PATCH") {
      const fields = {};
      const changes = {};
      if (body.name !== undefined) {
        if (typeof body.name !== "string" || !body.name.trim()) fields.name = "required";
        else if (body.name.trim() !== m.name) changes.name = { from: m.name, to: body.name.trim() };
      }
      if (body.status !== undefined && !["active", "disabled"].includes(body.status)) fields.status = "must be active or disabled";
      if (body.webhook_url !== undefined && body.webhook_url !== "" && !isHttpUrl(body.webhook_url)) fields.webhook_url = "must be an http(s) URL";
      if (Object.keys(fields).length) throw bad("invalid merchant", fields);
      if (changes.name) m.name = changes.name.to;
      if (body.status !== undefined && body.status !== m.status) {
        changes.status = { from: m.status, to: body.status };
        m.status = body.status;
      }
      if (body.webhook_url !== undefined && body.webhook_url !== m.webhook_url) {
        changes.webhook_url = { from: m.webhook_url, to: body.webhook_url };
        m.webhook_url = body.webhook_url.trim();
        if (m.webhook_url && !m.webhook_secret) m.webhook_secret = `whsec_${randomBytes(20).toString("hex")}`;
      }
      m.updated_at = iso(now());
      audit(user, "merchant.update", "merchant", m.id, changes, ip);
      return { body: publicMerchant(m) };
    }
    if (sub === "api-keys" && !keyId && method === "POST") {
      const name = typeof body.name === "string" ? body.name.trim() : "";
      if (!name) throw bad("name is required", { name: "required" });
      const key = `sk_live_${Array.from(randomBytes(32), (x) => B58[x % 58]).join("")}`;
      const k = { id: randomUUID(), merchant_id: m.id, name, key, prefix: key.slice(0, 12), last_used_at: null, revoked_at: null, created_at: iso(now()) };
      m.api_keys.unshift(k);
      audit(user, "api_key.create", "merchant", m.id, { name, prefix: k.prefix }, ip);
      return { status: 201, body: { api_key: publicKey(k), key } };
    }
    if (sub === "api-keys" && keyId && method === "DELETE") {
      const k = m.api_keys.find((x) => x.id === keyId);
      if (!k) throw notFound("api key");
      if (!k.revoked_at) k.revoked_at = iso(now());
      audit(user, "api_key.revoke", "merchant", m.id, { name: k.name, prefix: k.prefix }, ip);
      return { status: 204 };
    }
    if (sub === "webhook-secret" && method === "POST") {
      m.webhook_secret = `whsec_${randomBytes(20).toString("hex")}`;
      m.updated_at = iso(now());
      audit(user, "merchant.rotate_webhook_secret", "merchant", m.id, null, ip);
      return { body: { webhook_secret: m.webhook_secret } };
    }
    throw notFound("endpoint");
  }

  // ---- chains --------------------------------------------------------------------

  /** UTXO addresses belong to one network (bc1… / ltc1… / tb1…); other families share one format. */
  function staticAddressValidOn(address, chainId) {
    if (CHAINS[chainId].family !== "utxo") return true;
    if (chainId === "litecoin") return /^(ltc1|[LM])/.test(address);
    if (CHAINS[chainId].testnet) return /^(tb1|[mn2])/.test(address);
    return /^(bc1|[13])/.test(address);
  }

  function findChain(id) {
    const c = S.chains.find((x) => x.id === id);
    if (!c) throw notFound("chain");
    return c;
  }

  function chainsRoute(user, ip, method, id, action, body) {
    if (!id && method === "GET") return { body: { items: S.chains.map(chainView) } };
    const c = findChain(id);
    if (!action && method === "PATCH") {
      const fields = {};
      const family = CHAINS[c.id].family;
      if (body.enabled !== undefined && typeof body.enabled !== "boolean") fields.enabled = "must be a boolean";
      if (body.rpc_urls !== undefined) {
        if (!Array.isArray(body.rpc_urls) || body.rpc_urls.some((u) => typeof u !== "string" || !isRpcUrl(u) || u.includes("…"))) fields.rpc_urls = "every entry must be an http(s) or ws(s) URL";
        else if (body.rpc_urls.length > 8) fields.rpc_urls = "at most 8 endpoints";
      }
      if (body.confirmations !== undefined && !isInt(body.confirmations, 1, 1000)) fields.confirmations = "must be 1..1000";
      if (body.poll_interval_ms !== undefined && !isInt(body.poll_interval_ms, 200, 600000)) fields.poll_interval_ms = "must be 200..600000";
      if (body.scan_batch !== undefined && !isInt(body.scan_batch, 1, 10000)) fields.scan_batch = "must be 1..10000";
      if (body.wallet_id !== undefined && body.wallet_id !== null) {
        const w = S.wallets.find((x) => x.id === body.wallet_id);
        if (!w) fields.wallet_id = "unknown wallet";
        else if (w.family !== family) throw unprocessable(`wallet family ${w.family} does not match chain family ${family}`, { wallet_id: "wrong wallet family" });
      }
      if (Object.keys(fields).length) throw bad("invalid chain update", fields);
      const detail = {};
      for (const k of ["enabled", "confirmations", "poll_interval_ms", "scan_batch", "wallet_id"]) {
        if (body[k] !== undefined && body[k] !== c[k]) {
          detail[k] = { from: c[k], to: body[k] };
          c[k] = body[k];
        }
      }
      if (body.rpc_urls !== undefined) {
        c.rpc_db = body.rpc_urls.map((u) => u.trim());
        detail.rpc_urls = body.rpc_urls.length ? `[${body.rpc_urls.length} urls]` : "reset to env/default";
        if (c.last_error && body.rpc_urls.length) {
          c.last_error = "";
          c.sim_since = now();
          c.sim_head = c.last_head || c.sim_head;
        }
      }
      if (body.enabled === true) {
        c.sim_since = now();
        c.sim_head = c.last_head || c.sim_head;
      }
      audit(user, "chain.update", "chain", c.id, detail, ip);
      return { body: chainView(c) };
    }
    if (action === "test-rpc" && method === "POST") {
      const target = typeof body.rpc_url === "string" && body.rpc_url ? body.rpc_url : null;
      if (target && !isRpcUrl(target)) throw bad("rpc_url must be an http(s) or ws(s) URL", { rpc_url: "invalid URL" });
      const view = chainView(c);
      const failing = target ? /bad|invalid|down|localhost:1\b/.test(target) : Boolean(c.last_error);
      if (!target && view.rpc_urls.length === 0) return { body: { ok: false, head: 0, latency_ms: 0, error: "no RPC endpoint configured" } };
      if (failing) {
        return {
          body: {
            ok: false,
            head: 0,
            latency_ms: 0,
            error: target ? `dial tcp: lookup ${new URL(target).hostname}: no such host` : "429 Too Many Requests",
          },
        };
      }
      return { body: { ok: true, head: view.last_head + Math.floor(Math.random() * 3), latency_ms: 40 + Math.floor(Math.random() * 260), error: "" } };
    }
    throw notFound("endpoint");
  }

  /**
   * Checkout hides assets that are disabled here, or whose chain is disabled /
   * has no receiving wallet for the invoice's merchant (its own wallet, else
   * the chain's default wallet).
   */
  function isAssetPayable(asset, merchantId) {
    if (!S) return true;
    const a = S.assets.find((x) => x.id === asset.id);
    if (a && !a.enabled) return false;
    const c = S.chains.find((x) => x.id === asset.chain_id);
    if (!c) return true;
    const urls = c.rpc_db.length || c.rpc_env.length || c.rpc_default.length;
    return c.enabled && Boolean(resolveWallet(merchantId, c.id)) && Boolean(urls);
  }

  // ---- assets ----------------------------------------------------------------------

  function assetsRoute(user, ip, method, id, body) {
    if (!id && method === "GET") return { body: { items: S.assets.map(assetView) } };
    if (!id && method === "POST") {
      const fields = {};
      const chain = S.chains.find((c) => c.id === body.chain_id);
      if (!chain) fields.chain_id = "unknown chain";
      else if (CHAINS[chain.id].family === "utxo") fields.chain_id = "UTXO chains have no tokens";
      if (typeof body.symbol !== "string" || !/^[A-Za-z0-9]{2,10}$/.test(body.symbol)) fields.symbol = "2-10 letters or digits";
      if (typeof body.name !== "string" || !body.name.trim()) fields.name = "required";
      const family = chain ? CHAINS[chain.id].family : null;
      const contract = typeof body.contract_address === "string" ? body.contract_address.trim() : "";
      const contractOk =
        family === "evm" ? /^0x[0-9a-fA-F]{40}$/.test(contract) : family === "tron" ? /^T[1-9A-HJ-NP-Za-km-z]{33}$/.test(contract) : family === "solana" ? /^[1-9A-HJ-NP-Za-km-z]{32,44}$/.test(contract) : false;
      if (!contractOk) fields.contract_address = "invalid contract address for this chain";
      if (!isInt(body.decimals, 0, 36)) fields.decimals = "must be 0..36";
      if (!isInt(body.quote_precision, 0, 18)) fields.quote_precision = "must be 0..18";
      else if (isInt(body.decimals, 0, 36) && body.quote_precision > body.decimals) fields.quote_precision = "cannot exceed decimals";
      if (typeof body.rate_key !== "string" || !body.rate_key.trim()) fields.rate_key = "required";
      if (typeof body.is_stablecoin !== "boolean") fields.is_stablecoin = "must be a boolean";
      if (Object.keys(fields).length) throw bad("invalid asset", fields);
      const id2 = `${body.symbol.toLowerCase()}-${chain.id}`;
      if (S.assets.some((a) => a.id === id2)) throw conflict(`asset ${id2} already exists`, { symbol: "already exists on this chain" });
      if (S.assets.some((a) => a.chain_id === chain.id && a.contract_address.toLowerCase() === contract.toLowerCase())) throw conflict("this contract is already listed", { contract_address: "already listed" });
      const a = {
        id: id2,
        symbol: body.symbol.toUpperCase(),
        name: body.name.trim(),
        chain_id: chain.id,
        contract_address: contract,
        decimals: body.decimals,
        is_stablecoin: body.is_stablecoin,
        quote_precision: body.quote_precision,
        rate_key: body.rate_key.trim(),
        enabled: true,
        sort_order: 1000 + S.assets.length,
        custom: true,
      };
      S.assets.push(a);
      audit(user, "asset.create", "asset", a.id, { chain_id: a.chain_id, symbol: a.symbol, contract_address: a.contract_address }, ip);
      return { status: 201, body: assetView(a) };
    }
    const a = S.assets.find((x) => x.id === id);
    if (!a) throw notFound("asset");
    if (method === "PATCH") {
      const fields = {};
      if (body.enabled !== undefined && typeof body.enabled !== "boolean") fields.enabled = "must be a boolean";
      if (body.quote_precision !== undefined && !isInt(body.quote_precision, 0, a.decimals)) fields.quote_precision = `must be 0..${a.decimals}`;
      if (body.sort_order !== undefined && !isInt(body.sort_order, 0, 100000)) fields.sort_order = "must be 0..100000";
      if (body.rate_key !== undefined && (typeof body.rate_key !== "string" || !body.rate_key.trim())) fields.rate_key = "required";
      if (body.name !== undefined && (typeof body.name !== "string" || !body.name.trim())) fields.name = "required";
      if (Object.keys(fields).length) throw bad("invalid asset update", fields);
      const detail = {};
      for (const k of ["enabled", "quote_precision", "sort_order", "rate_key", "name"]) {
        if (body[k] !== undefined && body[k] !== a[k]) {
          detail[k] = { from: a[k], to: body[k] };
          a[k] = typeof body[k] === "string" ? body[k].trim() : body[k];
        }
      }
      audit(user, "asset.update", "asset", a.id, detail, ip);
      return { body: assetView(a) };
    }
    throw notFound("endpoint");
  }

  // ---- wallets --------------------------------------------------------------------

  const XPUB_PREFIX = { evm: ["xpub"], tron: ["xpub"], utxo: ["xpub", "zpub", "tpub", "vpub"] };

  function validateWalletInput(body, { requireName }) {
    const fields = {};
    if (requireName && (typeof body.name !== "string" || !body.name.trim())) fields.name = "required";
    if (!["evm", "tron", "utxo", "solana"].includes(body.family)) fields.family = "unknown family";
    if (!["xpub", "static"].includes(body.kind)) fields.kind = "must be xpub or static";
    else if (body.family === "solana" && body.kind === "xpub") fields.kind = "solana supports static wallets only";
    const secretField = body.kind === "xpub" ? "xpub" : "address";
    const value = String((body.kind === "xpub" ? body.xpub : body.address) ?? "").trim();
    if (looksLikeSecret(value)) {
      throw bad("this looks like a private key or seed phrase — never paste secrets here", { [secretField]: "looks like a private key or seed phrase" });
    }
    if (body.kind === "xpub") {
      if (!value) fields.xpub = "required";
      else if (!(XPUB_PREFIX[body.family] ?? []).some((p) => value.startsWith(p)) || !/^[1-9A-HJ-NP-Za-km-z]{100,120}$/.test(value)) fields.xpub = `must be an account-level ${(XPUB_PREFIX[body.family] ?? ["xpub"]).join("/")} key`;
    } else if (body.kind === "static") {
      const ok =
        body.family === "evm"
          ? /^0x[0-9a-fA-F]{40}$/.test(value)
          : body.family === "tron"
            ? /^T[1-9A-HJ-NP-Za-km-z]{33}$/.test(value)
            : body.family === "solana"
              ? /^[1-9A-HJ-NP-Za-km-z]{32,44}$/.test(value)
              : /^(bc1|tb1|ltc1)[02-9ac-hj-np-z]{8,87}$|^[13LMmn2][1-9A-HJ-NP-Za-km-z]{25,34}$/.test(value);
      if (!value) fields.address = "required";
      else if (!ok) fields.address = `not a valid ${body.family} address`;
    }
    if (Object.keys(fields).length) throw bad("invalid wallet", fields);
    return value;
  }

  function deriveAddresses(family, chainId, xpub) {
    return [0, 1, 2].map((i) => {
      const seed = `${xpub}:${chainId}:${i}`;
      switch (family) {
        case "evm":
          return `0x${fromDigest(seed, "0123456789abcdef", 40)}`;
        case "tron":
          return `T${fromDigest(seed, B58, 33)}`;
        default: {
          const hrp = chainId === "litecoin" ? "ltc1q" : chainId.includes("testnet") ? "tb1q" : "bc1q";
          return `${hrp}${fromDigest(seed, BECH32, 38)}`;
        }
      }
    });
  }

  function walletsRoute(user, ip, method, id, body) {
    if (!id && method === "GET") return { body: { items: S.wallets.map(walletView) } };
    if (id === "preview" && method === "POST") {
      const value = validateWalletInput(body, { requireName: false });
      const chain = S.chains.find((c) => c.id === body.chain_id);
      if (!chain) throw bad("unknown chain", { chain_id: "unknown chain" });
      if (CHAINS[chain.id].family !== body.family) throw bad("chain does not belong to this family", { chain_id: "wrong family" });
      if (body.kind === "static") {
        const normalised = body.family === "evm" ? checksumish(value) : value;
        return { body: { addresses: [normalised] } };
      }
      return { body: { addresses: deriveAddresses(body.family, chain.id, value) } };
    }
    if (!id && method === "POST") {
      const value = validateWalletInput(body, { requireName: true });
      const dup = S.wallets.find((w) => (body.kind === "xpub" ? w.kind === "xpub" && w.xpub === value : w.kind === "static" && w.family === body.family && w.address.toLowerCase() === value.toLowerCase()));
      if (dup) throw conflict("this key or address is already registered", { [body.kind === "xpub" ? "xpub" : "address"]: `is already registered as wallet "${dup.name}"` });
      const w = {
        id: randomUUID(),
        name: body.name.trim(),
        family: body.family,
        kind: body.kind,
        xpub: body.kind === "xpub" ? value : "",
        address: body.kind === "static" ? (body.family === "evm" ? checksumish(value) : value) : "",
        next_index: 0,
        used: false,
        created_at: iso(now()),
      };
      S.wallets.push(w);
      audit(user, "wallet.create", "wallet", w.id, { name: w.name, family: w.family, kind: w.kind }, ip);
      return { status: 201, body: walletView(w) };
    }
    const w = S.wallets.find((x) => x.id === id);
    if (!w) throw notFound("wallet");
    if (method === "PATCH") {
      if (typeof body.name !== "string" || !body.name.trim()) throw bad("name is required", { name: "required" });
      audit(user, "wallet.update", "wallet", w.id, { name: { from: w.name, to: body.name.trim() } }, ip);
      w.name = body.name.trim();
      return { body: walletView(w) };
    }
    if (method === "DELETE") {
      const bound = S.merchantWallets.filter((b) => b.wallet_id === w.id);
      if (bound.length) {
        const names = bound.map((b) => `"${S.merchants.find((m) => m.id === b.merchant_id)?.name}" on ${b.chain_id}`).join(", ");
        throw conflict(`the wallet is the receiving wallet of merchant ${names}; switch those merchants back to the default wallet (or to another wallet) first`);
      }
      if (w.used) throw conflict("wallet is referenced by quotes and cannot be deleted");
      for (const c of S.chains) if (c.wallet_id === w.id) c.wallet_id = null;
      S.wallets = S.wallets.filter((x) => x.id !== w.id);
      audit(user, "wallet.delete", "wallet", w.id, { name: w.name }, ip);
      return { status: 204 };
    }
    throw notFound("endpoint");
  }

  /** Mock "EIP-55": mixed case from a hash (format only, not real checksums). */
  function checksumish(addr) {
    const h = createHash("sha256").update(addr.toLowerCase()).digest("hex");
    return "0x" + [...addr.slice(2).toLowerCase()].map((ch, i) => (/[a-f]/.test(ch) && Number.parseInt(h[i], 16) >= 8 ? ch.toUpperCase() : ch)).join("");
  }

  // ---- webhook events ---------------------------------------------------------------

  function webhooksRoute(user, ip, method, url, id, action) {
    if (!id && method === "GET") {
      const status = url.searchParams.get("status");
      const merchantId = url.searchParams.get("merchant_id");
      const invoiceId = (url.searchParams.get("invoice_id") || "").trim();
      for (const inv of invoices.values()) {
        ctx.tick(inv);
        syncWebhooks(inv);
      }
      S.webhookEvents.forEach(deliver);
      let list = S.webhookEvents;
      if (status) list = list.filter((e) => e.status === status);
      if (merchantId) list = list.filter((e) => e.merchant_id === merchantId);
      if (invoiceId) list = list.filter((e) => e.invoice_id === invoiceId || e.invoice_id.startsWith(invoiceId));
      const page = paginate(list, url);
      return { body: { ...page, items: page.items.map(eventView) } };
    }
    if (id && action === "retry" && method === "POST") {
      const ev = S.webhookEvents.find((e) => e.id === id);
      if (!ev) throw notFound("webhook event");
      const m = S.merchants.find((x) => x.id === ev.merchant_id);
      if (m?.webhook_url) ev.url = m.webhook_url;
      ev.status = "pending";
      ev.next_attempt_at = iso(now());
      ev._deliverAt = now() + 2500;
      if (ev.attempts >= S.settings.webhook_max_attempts) ev.attempts = S.settings.webhook_max_attempts - 1;
      audit(user, "webhook_event.retry", "webhook_event", ev.id, { event_type: ev.event_type }, ip);
      return { body: eventView(ev) };
    }
    throw notFound("endpoint");
  }

  // ---- settings ------------------------------------------------------------------------

  function settingsRoute(user, ip, method, body) {
    if (method === "GET") return { body: S.settings };
    if (method === "PUT") {
      const f = {};
      const s = body;
      if (!isInt(s.invoice_ttl_seconds, 60, 86400)) f.invoice_ttl_seconds = "must be 60..86400";
      if (!isInt(s.grace_period_seconds, 0, 86400)) f.grace_period_seconds = "must be 0..86400";
      if (!isInt(s.underpay_tolerance_bps, 0, 1000)) f.underpay_tolerance_bps = "must be 0..1000";
      if (typeof s.stablecoin_peg !== "boolean") f.stablecoin_peg = "must be a boolean";
      if (!isInt(s.rate_markup_bps, 0, 2000)) f.rate_markup_bps = "must be 0..2000";
      if (!isInt(s.webhook_max_attempts, 1, 50)) f.webhook_max_attempts = "must be 1..50";
      if (!isInt(s.webhook_timeout_seconds, 1, 120)) f.webhook_timeout_seconds = "must be 1..120";
      if (typeof s.checkout_base_url !== "string" || !isHttpUrl(s.checkout_base_url) || s.checkout_base_url.endsWith("/")) f.checkout_base_url = "must be an http(s) URL without a trailing slash";
      if (typeof s.walletconnect_project_id !== "string" || s.walletconnect_project_id.length > 128) f.walletconnect_project_id = "max 128 characters";
      for (const k of ["require_2fa", "security_emails", "alert_emails"]) if (typeof s[k] !== "boolean") f[k] = "must be a boolean";
      if (!Array.isArray(s.alert_recipients)) f.alert_recipients = "is required ([] when not used)";
      else if (s.alert_recipients.length > 10) f.alert_recipients = "at most 10 addresses";
      else if (s.alert_recipients.some((r) => typeof r !== "string" || !isEmail(r.trim()))) f.alert_recipients = "must be valid e-mail addresses";
      else if (new Set(s.alert_recipients.map((r) => r.trim().toLowerCase())).size !== s.alert_recipients.length) f.alert_recipients = "contains duplicates";
      if (!["zh", "en"].includes(s.email_locale)) f.email_locale = "must be zh or en";
      if (Object.keys(f).length) throw bad("invalid settings", f);
      if (s.require_2fa && !S.settings.require_2fa && !user.two_factor_enabled) {
        throw conflict("enable two-factor authentication for your own account before requiring it for everyone");
      }
      const detail = {};
      for (const k of Object.keys(S.settings)) {
        if (JSON.stringify(s[k]) !== JSON.stringify(S.settings[k])) detail[k] = { from: S.settings[k], to: s[k] };
      }
      S.settings = {
        invoice_ttl_seconds: s.invoice_ttl_seconds,
        grace_period_seconds: s.grace_period_seconds,
        underpay_tolerance_bps: s.underpay_tolerance_bps,
        stablecoin_peg: s.stablecoin_peg,
        rate_markup_bps: s.rate_markup_bps,
        webhook_max_attempts: s.webhook_max_attempts,
        webhook_timeout_seconds: s.webhook_timeout_seconds,
        checkout_base_url: s.checkout_base_url,
        walletconnect_project_id: s.walletconnect_project_id,
        require_2fa: s.require_2fa,
        security_emails: s.security_emails,
        alert_emails: s.alert_emails,
        alert_recipients: s.alert_recipients.map((r) => r.trim()),
        email_locale: s.email_locale,
      };
      audit(user, "settings.update", "settings", "", detail, ip);
      return { body: S.settings };
    }
    throw notFound("endpoint");
  }

  // ---- users -----------------------------------------------------------------------------

  const activeOwners = () => S.users.filter((u) => u.role === "owner" && u.active);

  function usersRoute(user, ip, method, id, sub, body) {
    if (!id && method === "GET") return { body: { items: S.users.map(publicUser) } };
    if (!id && method === "POST") {
      const fields = {};
      const email = typeof body.email === "string" ? body.email.trim().toLowerCase() : "";
      if (!isEmail(email)) fields.email = "invalid email";
      if (typeof body.password !== "string" || body.password.length < 8) fields.password = "must be at least 8 characters";
      if (!["owner", "admin", "viewer"].includes(body.role)) fields.role = "must be owner, admin or viewer";
      if (Object.keys(fields).length) throw bad("invalid user", fields);
      if (S.users.some((u) => u.email === email)) throw conflict("an admin with this email already exists", { email: "already exists" });
      const u = { id: randomUUID(), email, password: body.password, role: body.role, active: true, last_login_at: null, created_at: iso(now()) };
      S.users.push(u);
      audit(user, "user.create", "user", u.id, { email, role: u.role }, ip);
      enqueueMail("security.account_created", u.email, { by: user.email, role: u.role });
      return { status: 201, body: publicUser(u) };
    }
    const u = S.users.find((x) => x.id === id);
    if (!u) throw notFound("user");
    if (sub === "2fa" && method === "DELETE") {
      if (u.id === user.id) throw conflict("use POST /auth/2fa/disable for your own account");
      if (!u.two_factor_enabled && !u.pendingSecret) throw conflict("this user has no two-factor authentication");
      u.two_factor_enabled = false;
      u.totpSecret = null;
      u.pendingSecret = null;
      u.recoveryCodes = [];
      u.lastStep = 0;
      const n = signOutUser(u.id);
      audit(user, "user.2fa_reset", "admin_user", u.id, { email: u.email, sessions_revoked: n }, ip);
      enqueueMail("security.2fa_reset", u.email, { by: user.email });
      return { status: 204 };
    }
    if (sub) throw notFound("endpoint");
    if (method === "PATCH") {
      const fields = {};
      if (body.role !== undefined && !["owner", "admin", "viewer"].includes(body.role)) fields.role = "must be owner, admin or viewer";
      if (body.active !== undefined && typeof body.active !== "boolean") fields.active = "must be a boolean";
      if (body.password !== undefined && (typeof body.password !== "string" || body.password.length < 8)) fields.password = "must be at least 8 characters";
      if (Object.keys(fields).length) throw bad("invalid user update", fields);
      const losesOwner = u.role === "owner" && u.active && ((body.role !== undefined && body.role !== "owner") || body.active === false);
      if (losesOwner && activeOwners().length <= 1) throw conflict("cannot demote or deactivate the last owner");
      const detail = {};
      if (body.role !== undefined && body.role !== u.role) {
        detail.role = { from: u.role, to: body.role };
        u.role = body.role;
        enqueueMail("security.role_changed", u.email, { by: user.email, role: u.role });
      }
      if (body.active !== undefined && body.active !== u.active) {
        detail.active = { from: u.active, to: body.active };
        u.active = body.active;
        if (!u.active) signOutUser(u.id);
        enqueueMail(u.active ? "security.account_reactivated" : "security.account_deactivated", u.email, { by: user.email });
      }
      if (body.password !== undefined) {
        u.password = body.password;
        detail.password = "reset";
        signOutUser(u.id);
        enqueueMail("security.password_changed", u.email, { by: user.email });
      }
      audit(user, "user.update", "user", u.id, detail, ip);
      return { body: publicUser(u) };
    }
    if (method === "DELETE") {
      if (u.id === user.id) throw conflict("you cannot delete yourself");
      if (u.role === "owner" && u.active && activeOwners().length <= 1) throw conflict("cannot delete the last owner");
      S.users = S.users.filter((x) => x.id !== u.id);
      signOutUser(u.id);
      audit(user, "user.delete", "user", u.id, { email: u.email }, ip);
      return { status: 204 };
    }
    throw notFound("endpoint");
  }

  // ---- e-mail -------------------------------------------------------------------------------

  function emailRoute(user, ip, method, url, a, id, action, body) {
    if (a === "status" && method === "GET") return { body: { ...S.email } };
    if (a === "test" && method === "POST") {
      if (!S.email.configured) throw conflict("e-mail is not configured (set SMTP_HOST and SMTP_FROM)");
      const to = body?.to === undefined || body.to === "" ? user.email : String(body.to).trim();
      if (!isEmail(to)) throw bad("invalid request", { to: "must be a valid e-mail address" });
      audit(user, "email.test", "email", "", { to }, ip);
      if (/bounce|fail/i.test(to)) return { body: { ok: false, error: `550 5.1.1 <${to}>: Recipient address rejected` } };
      return { body: { ok: true, error: "" } };
    }
    if (a !== "outbox") throw notFound("endpoint");
    // Mail to other admins: owners and admins only.
    if (user.role === "viewer") throw new HttpError(403, "forbidden", "the e-mail outbox is visible to owners and admins only");
    if (!id && method === "GET") {
      const status = url.searchParams.get("status") || "";
      const kind = url.searchParams.get("kind") || "";
      if (status && !["pending", "sent", "failed"].includes(status)) throw bad("invalid status", { status: "must be pending, sent or failed" });
      let list = S.outbox;
      if (status) list = list.filter((m) => m.status === status);
      if (kind) list = list.filter((m) => m.kind === kind);
      const page = paginate(list, url);
      return { body: { ...page, items: page.items.map(publicEmail) } };
    }
    const m = S.outbox.find((x) => x.id === id);
    if (!m) throw notFound("e-mail");
    if (!action && method === "GET") return { body: { ...publicEmail(m), body_text: m.body_text } };
    if (action === "retry" && method === "POST") {
      if (m.status === "sent") throw conflict("the message was already sent");
      m.status = "pending";
      audit(user, "email.retry", "email", m.id, { kind: m.kind }, ip);
      deliverLater(m);
      return { body: publicEmail(m) };
    }
    throw notFound("endpoint");
  }

  // ---- audit logs --------------------------------------------------------------------------

  function auditList(url) {
    const action = (url.searchParams.get("action") || "").trim().toLowerCase();
    const actor = (url.searchParams.get("actor") || "").trim().toLowerCase();
    let list = S.audit;
    if (action) list = list.filter((l) => l.action.toLowerCase().includes(action));
    if (actor) list = list.filter((l) => l.actor_email.toLowerCase().includes(actor));
    return paginate(list, url);
  }

  function printCredentials(base) {
    console.log(`\n  Admin console: ${base}/admin  (mock admin API under /api/admin)`);
    for (const u of MOCK_USERS) console.log(`    ${u.role.padEnd(7)} ${u.email.padEnd(24)} ${u.password}`);
    const m = MOCK_2FA_USER;
    console.log(`    ${m.role.padEnd(7)} ${m.email.padEnd(24)} ${m.password}   + two-step verification:`);
    console.log(`            TOTP secret ${DEMO_TOTP_SECRET} (add it to an authenticator app; code now: ${totp(DEMO_TOTP_SECRET)})`);
    console.log(`            recovery codes: ${DEMO_RECOVERY_CODES.join(" ")}`);
    console.log(`    e-mail: ${S?.email?.configured === false ? "not configured (MOCK_SMTP=off)" : "configured (mailpit), outbox at /admin/emails"}`);
    console.log("");
  }

  return { handle, seed, isAssetPayable, printCredentials, _state: () => S };
}
