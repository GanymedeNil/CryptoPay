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
});
