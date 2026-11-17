// @vitest-environment node
// Smoke test of the dependency-free admin mock (mock/admin.mjs via server.mjs).
import { spawn } from "node:child_process";
import { fileURLToPath } from "node:url";
import { afterAll, beforeAll, describe, expect, it } from "vitest";

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

describe("admin mock: e-mail", () => {
  const get = (path, cookie) => fetch(`${BASE}/api/admin${path}`, { headers: { cookie } });
  const send = (method, path, cookie, body) =>
    fetch(`${BASE}/api/admin${path}`, { method, headers: { ...json, cookie }, body: body === undefined ? undefined : JSON.stringify(body) });

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
    // Security events are mailed to the affected admin.
    expect(all.items.some((m) => m.kind === "security.new_login" && m.to === "owner@cryptopay.test")).toBe(true);

    const viewer = await login("viewer@cryptopay.test", "viewer-pass-123");
    expect((await get("/email/status", viewer.cookie)).status).toBe(200);
    expect((await get("/email/outbox", viewer.cookie)).status).toBe(403);
    expect((await send("POST", "/email/test", viewer.cookie, {})).status).toBe(403);
  });
});
