// @vitest-environment node
// Smoke test of the dependency-free admin mock (mock/admin.mjs via server.mjs).
import { spawn } from "node:child_process";
import { fileURLToPath } from "node:url";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { DEMO_RECOVERY_CODES, DEMO_TOTP_SECRET, MOCK_2FA_USER, totp } from "./admin.mjs";

const PORT = 20000 + Math.floor(Math.random() * 20000);
const BASE = `http://127.0.0.1:${PORT}`;
let child;

beforeAll(async () => {
  child = spawn(process.execPath, [fileURLToPath(new URL("./server.mjs", import.meta.url))], {
    env: { ...process.env, MOCK_PORT: String(PORT), MOCK_ADMIN_DELAY_MS: "0" },
    stdio: "ignore",
  });
  for (let i = 0; i < 50; i++) {
    try {
      if ((await fetch(`${BASE}/healthz`)).ok) return;
    } catch {
      // not up yet
    }
    await new Promise((r) => setTimeout(r, 100));
  }
  throw new Error("mock did not start");
}, 15_000);

afterAll(() => child?.kill());

const json = { "Content-Type": "application/json", "X-Requested-With": "cryptopay" };

async function login(email, password) {
  const res = await fetch(`${BASE}/api/admin/auth/login`, { method: "POST", headers: json, body: JSON.stringify({ email, password }) });
  const cookie = (res.headers.get("set-cookie") ?? "").split(";")[0];
  return { res, cookie };
}

describe("admin mock", () => {
  it("authenticates with a cookie session and the CSRF header", async () => {
    expect((await fetch(`${BASE}/api/admin/auth/me`)).status).toBe(401);
    const noCsrf = await fetch(`${BASE}/api/admin/auth/login`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ email: "owner@cryptopay.test", password: "owner-pass-123" }),
    });
    expect(noCsrf.status).toBe(403);
    expect((await login("owner@cryptopay.test", "nope")).res.status).toBe(401);
    const { res, cookie } = await login("owner@cryptopay.test", "owner-pass-123");
    expect(res.status).toBe(200);
    expect(res.headers.get("set-cookie")).toMatch(/cp_session=.+HttpOnly/);
    const me = await (await fetch(`${BASE}/api/admin/auth/me`, { headers: { cookie } })).json();
    expect(me).toMatchObject({ email: "owner@cryptopay.test", role: "owner" });
  });

  it("enforces roles", async () => {
    const { cookie } = await login("viewer@cryptopay.test", "viewer-pass-123");
    expect((await fetch(`${BASE}/api/admin/invoices`, { headers: { cookie } })).status).toBe(200);
    const patch = await fetch(`${BASE}/api/admin/chains/ethereum`, { method: "PATCH", headers: { ...json, cookie }, body: '{"enabled":false}' });
    expect(patch.status).toBe(403);
    expect((await fetch(`${BASE}/api/admin/users`, { headers: { cookie } })).status).toBe(403);
  });

  it("filters and pages lists, validates bodies with field errors", async () => {
    const { cookie } = await login("admin@cryptopay.test", "admin-pass-123");
    const paid = await (await fetch(`${BASE}/api/admin/invoices?status=paid&page_size=5&page=2`, { headers: { cookie } })).json();
    expect(paid.page).toBe(2);
    expect(paid.items).toHaveLength(5);
    expect(paid.items.every((i) => i.status === "paid")).toBe(true);
    expect(paid.total).toBeGreaterThan(10);

    const unmatched = await (await fetch(`${BASE}/api/admin/transfers?matched=false`, { headers: { cookie } })).json();
    expect(unmatched.items.every((t) => t.invoice_id === null)).toBe(true);
    expect(unmatched.total).toBeGreaterThan(0);

    const bad = await fetch(`${BASE}/api/admin/merchants`, { method: "POST", headers: { ...json, cookie }, body: '{"name":"","webhook_url":"ftp://x"}' });
    expect(bad.status).toBe(400);
    expect((await bad.json()).error.fields).toEqual({ name: "required", webhook_url: "must be an http(s) URL" });

    const overview = await (await fetch(`${BASE}/api/admin/overview?days=7`, { headers: { cookie } })).json();
    expect(overview.daily).toHaveLength(7);
    expect(typeof overview.totals.volume_usd).toBe("string");
  });

  it("refuses to demote the last owner and shows secrets once", async () => {
    const { cookie } = await login("owner@cryptopay.test", "owner-pass-123");
    const users = await (await fetch(`${BASE}/api/admin/users`, { headers: { cookie } })).json();
    const owner = users.items.find((u) => u.role === "owner");
    const demote = await fetch(`${BASE}/api/admin/users/${owner.id}`, { method: "PATCH", headers: { ...json, cookie }, body: '{"role":"admin"}' });
    expect(demote.status).toBe(409);

    const created = await (
      await fetch(`${BASE}/api/admin/merchants`, { method: "POST", headers: { ...json, cookie }, body: '{"name":"Mock test shop"}' })
    ).json();
    expect(created.webhook_secret).toMatch(/^whsec_/);
    const detail = await (await fetch(`${BASE}/api/admin/merchants/${created.merchant.id}`, { headers: { cookie } })).json();
    expect(JSON.stringify(detail)).not.toContain(created.webhook_secret);
  });

  it("binds merchant wallets and scopes payability per merchant", async () => {
    const { cookie } = await login("admin@cryptopay.test", "admin-pass-123");
    const get = async (path) => (await fetch(`${BASE}/api/admin${path}`, { headers: { cookie } })).json();
    const send = (method, path, body) =>
      fetch(`${BASE}/api/admin${path}`, { method, headers: { ...json, cookie }, body: body === undefined ? undefined : JSON.stringify(body) });

    const merchants = (await get("/merchants?page_size=100")).items;
    const lumen = merchants.find((m) => m.name === "Lumen Books");
    const acme = merchants.find((m) => m.name === "Acme Coffee Roasters");
    expect((await get(`/merchants/${lumen.id}`)).wallets).toEqual([]);
    const acmeDetail = await get(`/merchants/${acme.id}`);
    expect(acmeDetail.wallets).toEqual([expect.objectContaining({ chain_id: "litecoin", kind: "static" })]);

    // Litecoin has no default wallet: only Acme (own wallet) sees LTC on the checkout.
    const chains = (await get("/chains")).items;
    const ltc = chains.find((c) => c.id === "litecoin");
    expect(ltc).toMatchObject({ wallet_id: null, payable: false, merchant_wallets: 1 });
    const checkoutAssets = async (merchantId) => {
      const inv = await (await send("POST", "/invoices", { merchant_id: merchantId, amount: "10", currency: "USD" })).json();
      const view = await (await fetch(`${BASE}/api/checkout/${inv.id}`)).json();
      return view.assets.map((a) => a.id);
    };
    expect(await checkoutAssets(acme.id)).toContain("ltc-litecoin");
    expect(await checkoutAssets(lumen.id)).not.toContain("ltc-litecoin");

    // Set → visible on the merchant, the wallet and the chain; validation; unset is idempotent.
    const wallets = (await get("/wallets")).items;
    const ltcWallet = wallets.find((w) => w.address.startsWith("ltc1") && w.merchant_bindings.length === 0);
    const tronWallet = wallets.find((w) => w.family === "tron" && w.kind === "static");
    expect((await send("PUT", `/merchants/${lumen.id}/wallets/litecoin`, { wallet_id: tronWallet.id })).status).toBe(400);
    expect((await send("PUT", `/merchants/${lumen.id}/wallets/litecoin`, {})).status).toBe(400);
    expect((await send("PUT", `/merchants/${lumen.id}/wallets/nope`, { wallet_id: ltcWallet.id })).status).toBe(404);
    const set = await send("PUT", `/merchants/${lumen.id}/wallets/litecoin`, { wallet_id: ltcWallet.id });
    expect(set.status).toBe(200);
    expect(await set.json()).toEqual({ chain_id: "litecoin", wallet_id: ltcWallet.id, wallet_name: ltcWallet.name, kind: "static" });
    expect((await get("/wallets")).items.find((w) => w.id === ltcWallet.id).merchant_bindings).toEqual([
      { merchant_id: lumen.id, merchant_name: "Lumen Books", chain_id: "litecoin" },
    ]);
    expect((await get("/chains")).items.find((c) => c.id === "litecoin").merchant_wallets).toBe(2);
    expect(await checkoutAssets(lumen.id)).toContain("ltc-litecoin");
    const del = await send("DELETE", `/wallets/${ltcWallet.id}`);
    expect(del.status).toBe(409);
    expect((await del.json()).error.message).toContain("Lumen Books");
    expect((await send("DELETE", `/merchants/${lumen.id}/wallets/litecoin`)).status).toBe(204);
    expect((await send("DELETE", `/merchants/${lumen.id}/wallets/litecoin`)).status).toBe(204);
    expect((await get(`/merchants/${lumen.id}`)).wallets).toEqual([]);

    // Duplicate key material → 409 on the field; viewers are read-only.
    const dup = await send("POST", "/wallets", { name: "dup", family: "tron", kind: "static", address: tronWallet.address });
    expect(dup.status).toBe(409);
    expect((await dup.json()).error.fields.address).toContain("already registered");
    const viewer = await login("viewer@cryptopay.test", "viewer-pass-123");
    const denied = await fetch(`${BASE}/api/admin/merchants/${lumen.id}/wallets/litecoin`, {
      method: "PUT",
      headers: { ...json, cookie: viewer.cookie },
      body: JSON.stringify({ wallet_id: ltcWallet.id }),
    });
    expect(denied.status).toBe(403);
  });
});

describe("admin mock: two-factor authentication, sessions, e-mail", () => {
  const get = (path, cookie) => fetch(`${BASE}/api/admin${path}`, { headers: { cookie } });
  const send = (method, path, cookie, body) =>
    fetch(`${BASE}/api/admin${path}`, { method, headers: { ...json, cookie }, body: body === undefined ? undefined : JSON.stringify(body) });
  const step2 = (body) => fetch(`${BASE}/api/admin/auth/login/2fa`, { method: "POST", headers: json, body: JSON.stringify(body) });
  const challenge = async () => {
    const { res, cookie } = await login(MOCK_2FA_USER.email, MOCK_2FA_USER.password);
    expect(res.status).toBe(202);
    expect(cookie).toBe("");
    return res.json();
  };
  /** A 6-digit code that is certainly not valid right now. */
  const wrongCode = () => {
    const ok = new Set([-30_000, 0, 30_000].map((d) => totp(DEMO_TOTP_SECRET, Date.now() + d)));
    for (let i = 0; ; i++) {
      const c = String(i).padStart(6, "0");
      if (!ok.has(c)) return c;
    }
  };

  it("signs in in two steps; codes are not replayable and recovery codes work once", async () => {
    const ch = await challenge();
    expect(ch).toMatchObject({ two_factor_required: true, challenge_token: expect.any(String) });
    expect(Date.parse(ch.expires_at) - Date.now()).toBeGreaterThan(4 * 60_000);

    expect((await step2({ challenge_token: ch.challenge_token })).status).toBe(400);
    expect((await step2({ challenge_token: ch.challenge_token, code: "123456", recovery_code: "x" })).status).toBe(400);
    const wrong = await step2({ challenge_token: ch.challenge_token, code: wrongCode() });
    expect(wrong.status).toBe(401);
    expect((await wrong.json()).error.code).toBe("unauthorized");

    const code = totp(DEMO_TOTP_SECRET);
    const ok = await step2({ challenge_token: ch.challenge_token, code });
    expect(ok.status).toBe(200);
    expect(ok.headers.get("set-cookie")).toMatch(/cp_session=.+HttpOnly/);
    expect(await ok.json()).toMatchObject({ email: MOCK_2FA_USER.email, two_factor_enabled: true });
    // The challenge is used up; the same code is not accepted twice.
    expect((await step2({ challenge_token: ch.challenge_token, code })).status).toBe(401);
    const again = await challenge();
    expect((await step2({ challenge_token: again.challenge_token, code })).status).toBe(401);

    const rc = await step2({ challenge_token: again.challenge_token, recovery_code: ` ${DEMO_RECOVERY_CODES[0].toUpperCase()} ` });
    expect(rc.status).toBe(200);
    const third = await challenge();
    expect((await step2({ challenge_token: third.challenge_token, recovery_code: DEMO_RECOVERY_CODES[0] })).status).toBe(401);
  });

  it("invalidates a challenge after 5 wrong codes", async () => {
    const ch = await challenge();
    for (let i = 0; i < 5; i++) expect((await step2({ challenge_token: ch.challenge_token, code: wrongCode() })).status).toBe(401);
    expect((await step2({ challenge_token: ch.challenge_token, code: totp(DEMO_TOTP_SECRET, Date.now() + 30_000) })).status).toBe(401);
    // A fresh challenge works (and resets the per-account failure count).
    const fresh = await challenge();
    expect((await step2({ challenge_token: fresh.challenge_token, recovery_code: DEMO_RECOVERY_CODES[1] })).status).toBe(200);
  });

  it("sets up 2FA, enforces require_2fa and lets an owner reset it", async () => {
    const owner = await login("owner@cryptopay.test", "owner-pass-123");
    const email = `enforce-${Date.now()}@cryptopay.test`;
    const created = await (await send("POST", "/users", owner.cookie, { email, password: "enforce-pass-1", role: "admin" })).json();
    const user = await login(email, "enforce-pass-1");
    expect(user.res.status).toBe(200);

    // Only an admin with 2FA can require it.
    const settings = await (await get("/settings", owner.cookie)).json();
    expect(settings).toMatchObject({ require_2fa: false, security_emails: true, email_locale: "zh" });
    expect((await send("PUT", "/settings", owner.cookie, { ...settings, require_2fa: true })).status).toBe(409);
    const bad = await send("PUT", "/settings", owner.cookie, { ...settings, alert_recipients: ["nope"] });
    expect((await bad.json()).error.fields.alert_recipients).toBeTruthy();
    const mfaCh = await challenge();
    const mfa = await step2({ challenge_token: mfaCh.challenge_token, recovery_code: DEMO_RECOVERY_CODES[2] });
    const mfaCookie = (mfa.headers.get("set-cookie") ?? "").split(";")[0];
    expect((await send("PUT", "/settings", mfaCookie, { ...settings, require_2fa: true })).status).toBe(200);
    expect((await send("POST", "/auth/2fa/disable", mfaCookie, { current_password: MOCK_2FA_USER.password, code: totp(DEMO_TOTP_SECRET) })).status).toBe(409);

    // Without 2FA only the setup flow is reachable.
    const me = await (await get("/auth/me", user.cookie)).json();
    expect(me).toMatchObject({ two_factor_enabled: false, two_factor_setup_required: true });
    const blocked = await get("/invoices", user.cookie);
    expect(blocked.status).toBe(403);
    expect((await blocked.json()).error.code).toBe("two_factor_setup_required");
    expect((await get("/auth/sessions", user.cookie)).status).toBe(403);

    const wrongPw = await send("POST", "/auth/2fa/setup", user.cookie, { current_password: "nope" });
    expect(wrongPw.status).toBe(400);
    expect((await wrongPw.json()).error.fields).toHaveProperty("current_password");
    const setup = await (await send("POST", "/auth/2fa/setup", user.cookie, { current_password: "enforce-pass-1" })).json();
    expect(setup.otpauth_uri).toMatch(/^otpauth:\/\/totp\/CryptoPay%3A.+\?secret=[A-Z2-7]+&issuer=CryptoPay&algorithm=SHA1&digits=6&period=30$/);
    const wrongEnable = await send("POST", "/auth/2fa/enable", user.cookie, { code: "abc" });
    expect((await wrongEnable.json()).error.fields).toHaveProperty("code");
    const enabled = await (await send("POST", "/auth/2fa/enable", user.cookie, { code: totp(setup.secret) })).json();
    expect(enabled.recovery_codes).toHaveLength(10);
    expect(enabled.recovery_codes[0]).toMatch(/^[0-9a-z]{5}-[0-9a-z]{5}$/);
    expect((await get("/invoices", user.cookie)).status).toBe(200);
    // The owner (no 2FA) is locked out too until require_2fa is off again.
    expect((await get("/users", owner.cookie)).status).toBe(403);
    expect((await send("PUT", "/settings", mfaCookie, { ...settings, require_2fa: false })).status).toBe(200);
    const users = await (await get("/users", owner.cookie)).json();
    expect(users.items.find((u) => u.id === created.id).two_factor_enabled).toBe(true);

    // Owner reset: not yourself; the user is signed out everywhere.
    expect((await send("DELETE", `/users/${users.items.find((u) => u.email === "owner@cryptopay.test").id}/2fa`, owner.cookie)).status).toBe(409);
    expect((await send("DELETE", `/users/${created.id}/2fa`, owner.cookie)).status).toBe(204);
    expect((await get("/auth/me", user.cookie)).status).toBe(401);
    expect((await send("DELETE", `/users/${created.id}/2fa`, owner.cookie)).status).toBe(409);
  });

  it("lists and revokes the caller's own sessions", async () => {
    const a = await login("admin@cryptopay.test", "admin-pass-123");
    const b = await login("admin@cryptopay.test", "admin-pass-123");
    const list = await (await get("/auth/sessions", a.cookie)).json();
    expect(list.items.filter((s) => s.current)).toHaveLength(1);
    expect(list.items.length).toBeGreaterThanOrEqual(2);
    expect(Object.keys(list.items[0]).sort()).toEqual(["created_at", "current", "expires_at", "id", "ip", "user_agent"]);
    const viewer = await login("viewer@cryptopay.test", "viewer-pass-123");
    const other = (await (await get("/auth/sessions", viewer.cookie)).json()).items[0];
    expect((await send("DELETE", `/auth/sessions/${other.id}`, a.cookie)).status).toBe(404);

    const revoked = await (await send("POST", "/auth/sessions/revoke-others", a.cookie)).json();
    expect(revoked.revoked).toBeGreaterThanOrEqual(1);
    expect((await get("/auth/me", b.cookie)).status).toBe(401);
    const mine = (await (await get("/auth/sessions", a.cookie)).json()).items;
    expect(mine).toHaveLength(1);
    const del = await send("DELETE", `/auth/sessions/${mine[0].id}`, a.cookie);
    expect(del.status).toBe(204);
    expect(del.headers.get("set-cookie")).toMatch(/Max-Age=0/);
    expect((await get("/auth/me", a.cookie)).status).toBe(401);
  });

  it("reports the e-mail status, sends test mails and keeps an outbox", async () => {
    const { cookie } = await login("admin@cryptopay.test", "admin-pass-123");
    expect(await (await get("/email/status", cookie)).json()).toEqual({
      configured: true,
      from: "CryptoPay <no-reply@cryptopay.local>",
      host: "mailpit",
    });
    expect(await (await send("POST", "/email/test", cookie, {})).json()).toEqual({ ok: true, error: "" });
    const bounced = await (await send("POST", "/email/test", cookie, { to: "bounce@example.com" })).json();
    expect(bounced.ok).toBe(false);
    expect(bounced.error).toContain("550");
    expect((await send("POST", "/email/test", cookie, { to: "nope" })).status).toBe(400);

    const all = await (await get("/email/outbox?page_size=100", cookie)).json();
    expect(all.total).toBeGreaterThan(5);
    expect(all.items[0]).not.toHaveProperty("body_text");
    const failed = await (await get("/email/outbox?status=failed", cookie)).json();
    expect(failed.items.length).toBeGreaterThan(0);
    expect(failed.items.every((m) => m.status === "failed")).toBe(true);
    const kind = await (await get("/email/outbox?kind=alert.webhook_failed", cookie)).json();
    expect(kind.items.every((m) => m.kind === "alert.webhook_failed")).toBe(true);
    const detail = await (await get(`/email/outbox/${failed.items[0].id}`, cookie)).json();
    expect(detail.body_text).toContain("CryptoPay");
    const retried = await (await send("POST", `/email/outbox/${failed.items[0].id}/retry`, cookie)).json();
    expect(retried.status).toBe("pending");
    const sent = all.items.find((m) => m.status === "sent");
    expect((await send("POST", `/email/outbox/${sent.id}/retry`, cookie)).status).toBe(409);
    // A security event queues a mail to the affected admin.
    expect(all.items.some((m) => m.kind === "security.recovery_code_used" && m.to === MOCK_2FA_USER.email)).toBe(true);

    const viewer = await login("viewer@cryptopay.test", "viewer-pass-123");
    expect((await get("/email/status", viewer.cookie)).status).toBe(200);
    expect((await get("/email/outbox", viewer.cookie)).status).toBe(403);
    expect((await send("POST", "/email/test", viewer.cookie, {})).status).toBe(403);
  });

  it("throttles repeated wrong codes per account (429)", async () => {
    let status = 0;
    for (let i = 0; i < 4 && status !== 429; i++) {
      const ch = await challenge();
      for (let j = 0; j < 5; j++) {
        status = (await step2({ challenge_token: ch.challenge_token, code: wrongCode() })).status;
        if (status === 429) break;
      }
    }
    expect(status).toBe(429);
  });

  it("filters the overview by merchant and compares merchants with the same definitions", async () => {
    const { cookie } = await login("viewer@cryptopay.test", "viewer-pass-123");
    const get = async (path) => {
      const res = await fetch(`${BASE}/api/admin${path}`, { headers: { cookie } });
      return { status: res.status, body: await res.json() };
    };
    const units = (s) => {
      const [i, f = ""] = s.split(".");
      return BigInt(i + f.padEnd(8, "0").slice(0, 8));
    };
    const merchants = (await get("/merchants?page_size=100")).body.items;

    // The mock's clock moves (confirmations, webhook deliveries): compare a
    // snapshot only when the rows did not change while it was taken.
    let checked = false;
    for (let attempt = 0; attempt < 5 && !checked; attempt++) {
      const before = (await get("/overview/merchants?days=90&page_size=100")).body;
      const all = (await get("/overview?days=90")).body;
      const filtered = [];
      for (const r of before.items) filtered.push((await get(`/overview?days=90&merchant_id=${r.merchant_id}`)).body);
      const after = (await get("/overview/merchants?days=90&page_size=100")).body;
      const again = (await get("/overview?days=90")).body;
      if (JSON.stringify(before) !== JSON.stringify(after) || JSON.stringify(all.totals) !== JSON.stringify(again.totals)) continue;

      expect(before.range_days).toBe(90);
      expect(before.total).toBe(merchants.length);
      expect(all.merchant_id).toBeNull();
      // Every merchant has a row; the closed store is idle.
      expect(before.items.find((r) => r.merchant_name === "Old Storefront (closed)")).toMatchObject({
        status: "disabled", invoices: 0, paid_invoices: 0, volume_usd: "0", conversion_rate: 0, last_invoice_at: null, last_paid_at: null,
      });
      // A row is exactly the merchant's filtered overview…
      before.items.forEach((r, i) => {
        expect(filtered[i].merchant_id).toBe(r.merchant_id);
        expect(filtered[i].totals).toEqual({
          invoices: r.invoices, paid_invoices: r.paid_invoices, open_invoices: r.open_invoices, volume_usd: r.volume_usd,
          conversion_rate: r.conversion_rate, unmatched_transfers: 0, failed_webhooks: r.failed_webhooks,
        });
      });
      // …and the rows add up to the unfiltered overview (except unmatched transfers).
      for (const k of ["invoices", "paid_invoices", "open_invoices", "failed_webhooks"]) {
        expect(before.items.reduce((n, r) => n + r[k], 0)).toBe(all.totals[k]);
      }
      expect(before.items.reduce((n, r) => n + units(r.volume_usd), 0n)).toBe(units(all.totals.volume_usd));
      expect(all.totals.unmatched_transfers).toBeGreaterThan(0);
      expect(filtered.reduce((n, o) => n + o.daily.reduce((m, d) => m + d.invoices, 0), 0)).toBe(all.totals.invoices);
      checked = true;
    }
    expect(checked).toBe(true);

    // Sorting (volume numerically, names A→Z by default) and paging.
    const asc = (await get("/overview/merchants?days=90&sort=volume_usd&order=asc&page_size=100")).body.items.map((r) => units(r.volume_usd));
    expect(asc).toEqual([...asc].sort((a, b) => (a < b ? -1 : a > b ? 1 : 0)));
    const names = (await get("/overview/merchants?sort=name&page_size=100")).body.items.map((r) => r.merchant_name);
    expect(names).toEqual([...names].sort((a, b) => a.localeCompare(b)));
    const page2 = (await get("/overview/merchants?sort=name&page=2&page_size=2")).body;
    expect(page2).toMatchObject({ page: 2, page_size: 2, total: merchants.length, range_days: 30 });
    expect(page2.items.map((r) => r.merchant_name)).toEqual(names.slice(2, 4));

    // Validation.
    const badId = await get("/overview?merchant_id=nope");
    expect(badId.status).toBe(400);
    expect(badId.body.error.fields).toHaveProperty("merchant_id");
    expect((await get("/overview?merchant_id=00000000-0000-4000-8000-000000000000")).status).toBe(404);
    expect((await get("/overview/merchants?sort=created_at")).body.error.fields).toHaveProperty("sort");
    expect((await get("/overview/merchants?order=up")).body.error.fields).toHaveProperty("order");
    expect((await get("/overview/merchants?days=0")).status).toBe(400);
  });
});
