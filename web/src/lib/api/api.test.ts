import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createAdminApi } from "./admin";
import { createCheckoutApi } from "./checkout";
import { ApiError, buildQuery, codeForStatus, createApiClient, isApiError } from "./client";
import { createMerchantApi } from "./merchant";

function jsonResponse(status: number, body: unknown, statusText = "") {
  return new Response(body === undefined ? null : JSON.stringify(body), {
    status,
    statusText,
    headers: { "Content-Type": "application/json" },
  });
}

describe("api client", () => {
  const fetchMock = vi.fn<typeof fetch>();

  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal("fetch", fetchMock);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("GETs JSON same-origin without the CSRF header", async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { ok: 1 }));
    const client = createApiClient();
    await expect(client.get("/api/x", { page: 2, q: "", status: undefined })).resolves.toEqual({ ok: 1 });
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("/api/x?page=2");
    expect(init?.credentials).toBe("same-origin");
    expect(init?.method).toBe("GET");
    expect((init?.headers as Record<string, string>)["X-Requested-With"]).toBeUndefined();
  });

  it("sends JSON bodies and X-Requested-With on mutations", async () => {
    fetchMock.mockResolvedValue(jsonResponse(201, { id: "1" }));
    await createApiClient().post("/api/y", { a: "1.5" });
    const init = fetchMock.mock.calls[0][1]!;
    const headers = init.headers as Record<string, string>;
    expect(headers["X-Requested-With"]).toBe("cryptopay");
    expect(headers["Content-Type"]).toBe("application/json");
    expect(init.body).toBe('{"a":"1.5"}');
  });

  it("resolves 204 to undefined", async () => {
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }));
    await expect(createApiClient().delete("/api/z")).resolves.toBeUndefined();
  });

  it("maps the error envelope to ApiError", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse(422, { error: { code: "unprocessable", message: "bad amount", fields: { amount: "must be > 0" } } }),
    );
    const err = await createApiClient().post("/api/v1/invoices", {}).catch((e) => e);
    expect(isApiError(err)).toBe(true);
    expect(err).toMatchObject({ status: 422, code: "unprocessable", message: "bad amount" });
    expect((err as ApiError).fields).toEqual({ amount: "must be > 0" });
  });

  it("maps non-JSON / unexpected error bodies from the status", async () => {
    fetchMock.mockResolvedValue(new Response("<html>Bad gateway</html>", { status: 502, statusText: "Bad Gateway" }));
    const err = (await createApiClient().get("/api/x").catch((e) => e)) as ApiError;
    expect(err.code).toBe("unavailable");
    expect(err.message).toBe("Bad Gateway");
    expect(err.isTransient).toBe(true);

    fetchMock.mockResolvedValue(jsonResponse(404, { message: "nope" }));
    const nf = (await createApiClient().get("/api/x").catch((e) => e)) as ApiError;
    expect(nf.isNotFound).toBe(true);
    expect(nf.code).toBe("not_found");
  });

  it("wraps network failures with status 0 and re-throws aborts", async () => {
    fetchMock.mockRejectedValue(new TypeError("Failed to fetch"));
    const err = (await createApiClient().get("/api/x").catch((e) => e)) as ApiError;
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ status: 0, code: "network_error", isNetworkError: true, isTransient: true });

    const abort = new DOMException("aborted", "AbortError");
    fetchMock.mockRejectedValue(abort);
    await expect(createApiClient().get("/api/x")).rejects.toBe(abort);
  });

  it("rejects a 200 that is not JSON", async () => {
    fetchMock.mockResolvedValue(new Response("<html/>", { status: 200 }));
    await expect(createApiClient().get("/api/x")).rejects.toMatchObject({ code: "invalid_response" });
  });

  it("knows the documented codes", () => {
    expect([400, 401, 403, 404, 409, 422, 429, 500, 503, 418].map(codeForStatus)).toEqual([
      "invalid_request",
      "unauthorized",
      "forbidden",
      "not_found",
      "conflict",
      "unprocessable",
      "rate_limited",
      "internal",
      "unavailable",
      "http_error",
    ]);
    expect(buildQuery({ a: 1, b: false, c: null })).toBe("?a=1&b=false");
    expect(buildQuery({})).toBe("");
  });
});

describe("endpoint modules", () => {
  function recorder() {
    const calls: { method: string; url: string; body?: string; headers: Record<string, string> }[] = [];
    const fake: typeof fetch = async (input, init) => {
      calls.push({
        method: init?.method ?? "GET",
        url: String(input),
        body: init?.body as string | undefined,
        headers: init?.headers as Record<string, string>,
      });
      return jsonResponse(200, {});
    };
    return { calls, client: createApiClient({ fetch: fake }), fake };
  }

  it("checkout: GET view and POST quote", async () => {
    const { calls, client } = recorder();
    const api = createCheckoutApi(client);
    await api.get("abc/def");
    await api.createQuote("inv", "usdt-tron");
    expect(calls[0]).toMatchObject({ method: "GET", url: "/api/checkout/abc%2Fdef" });
    expect(calls[1]).toMatchObject({ method: "POST", url: "/api/checkout/inv/quote", body: '{"asset_id":"usdt-tron"}' });
  });

  it("admin: paths, methods and query params", async () => {
    const { calls, client } = recorder();
    const api = createAdminApi(client);
    await api.login({ email: "a@b.c", password: "secret12" });
    await api.listInvoices({ status: "paid", page: 2, q: "0xabc" });
    await api.revokeApiKey("m1", "k1");
    await api.updateChain("tron", { enabled: false, wallet_id: null });
    await api.updateSettings({
      invoice_ttl_seconds: 900,
      grace_period_seconds: 600,
      underpay_tolerance_bps: 50,
      stablecoin_peg: true,
      rate_markup_bps: 0,
      webhook_max_attempts: 10,
      webhook_timeout_seconds: 10,
      checkout_base_url: "https://pay.example",
      walletconnect_project_id: "",
      require_2fa: true,
      security_emails: true,
      alert_emails: true,
      alert_recipients: ["ops@example.com"],
      email_locale: "en",
    });
    await api.listTransfers({ matched: false });
    await api.setMerchantWallet("m1", "tron-nile", { wallet_id: "w1" });
    await api.unsetMerchantWallet("m1", "tron-nile");
    expect(calls.map((c) => `${c.method} ${c.url}`)).toEqual([
      "POST /api/admin/auth/login",
      "GET /api/admin/invoices?status=paid&page=2&q=0xabc",
      "DELETE /api/admin/merchants/m1/api-keys/k1",
      "PATCH /api/admin/chains/tron",
      "PUT /api/admin/settings",
      "GET /api/admin/transfers?matched=false",
      "PUT /api/admin/merchants/m1/wallets/tron-nile",
      "DELETE /api/admin/merchants/m1/wallets/tron-nile",
    ]);
    expect(calls[3].body).toBe('{"enabled":false,"wallet_id":null}');
    expect(calls[2].headers["X-Requested-With"]).toBe("cryptopay");
    expect(calls[6].body).toBe('{"wallet_id":"w1"}');
    expect(calls[7].headers["X-Requested-With"]).toBe("cryptopay");
  });

  it("admin: two-factor and e-mail endpoints", async () => {
    const { calls, client } = recorder();
    const api = createAdminApi(client);
    await api.loginTwoFactor({ challenge_token: "ch", code: "123456" });
    await api.setupTwoFactor({ current_password: "pw" });
    await api.enableTwoFactor({ code: "123456" });
    await api.disableTwoFactor({ current_password: "pw", code: "abcde-12345" });
    await api.regenerateRecoveryCodes({ current_password: "pw", code: "123456" });
    await api.resetUserTwoFactor("u1");
    await api.getEmailStatus();
    await api.sendTestEmail();
    await api.sendTestEmail({ to: "a@b.c" });
    await api.listEmailOutbox({ status: "failed", kind: "alert.webhook_failed", page: 2 });
    await api.getEmailMessage("e1");
    await api.retryEmailMessage("e1");
    expect(calls.map((c) => `${c.method} ${c.url}`)).toEqual([
      "POST /api/admin/auth/login/2fa",
      "POST /api/admin/auth/2fa/setup",
      "POST /api/admin/auth/2fa/enable",
      "POST /api/admin/auth/2fa/disable",
      "POST /api/admin/auth/2fa/recovery-codes",
      "DELETE /api/admin/users/u1/2fa",
      "GET /api/admin/email/status",
      "POST /api/admin/email/test",
      "POST /api/admin/email/test",
      "GET /api/admin/email/outbox?status=failed&kind=alert.webhook_failed&page=2",
      "GET /api/admin/email/outbox/e1",
      "POST /api/admin/email/outbox/e1/retry",
    ]);
    expect(calls[0].body).toBe('{"challenge_token":"ch","code":"123456"}');
    expect(calls[7].body).toBe("{}");
    expect(calls[8].body).toBe('{"to":"a@b.c"}');
    for (const c of calls.filter((x) => x.method !== "GET")) expect(c.headers["X-Requested-With"]).toBe("cryptopay");
  });

  it("admin: login answers either a user or a 202 challenge", async () => {
    const challenge = { two_factor_required: true, challenge_token: "ch", expires_at: "2026-11-17T08:05:00Z" };
    const fetch202: typeof fetch = async () => jsonResponse(202, challenge);
    const res = await createAdminApi(createApiClient({ fetch: fetch202 })).login({ email: "a@b.c", password: "pw" });
    expect(res).toEqual(challenge);
  });

  it("merchant: bearer auth on every request", async () => {
    const { calls, fake } = recorder();
    const api = createMerchantApi({ apiKey: "sk_live_x", baseUrl: "https://gw.example/", fetch: fake });
    await api.createInvoice({ amount: "10", currency: "USD" });
    await api.listInvoices({ order_id: "o-1" });
    expect(calls[0]).toMatchObject({ method: "POST", url: "https://gw.example/api/v1/invoices" });
    expect(calls[0].headers.Authorization).toBe("Bearer sk_live_x");
    expect(calls[1].url).toBe("https://gw.example/api/v1/invoices?order_id=o-1");
  });
});
