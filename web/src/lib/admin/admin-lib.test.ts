import { describe, expect, it, vi } from "vitest";
import { ApiError } from "@/lib/api/client";
import type { InvoiceDetail } from "@/lib/api/types";
import { createTranslator } from "@/lib/i18n/translate";
import { quote, transfer } from "@/test/fixtures";
import { chainFixture, walletFixture } from "@/test/admin";
import { chainHealth, isMaskedRpcUrl, isRpcUrl, notPayableReasons, receivesPayments, rpcEnvName, splitByNetwork } from "./chains";
import { applyFieldErrors, describeError, errorKind } from "./errors";
import { addressLink, fillExplorerTemplate, safeHttpUrl, txLink } from "./explorer";
import { formToSettings, recipientProblem, settingsSchema, settingsToForm } from "./settings-form";
import {
  EMAIL_KINDS,
  emailKindLabel,
  groupSecret,
  isLoginChallenge,
  normalizeOtp,
  normalizeRecoveryCode,
  recoveryCodesFileName,
  recoveryCodesText,
} from "./two-factor";
import { canWrite, isOwner } from "./roles";
import { buildTimeline } from "./timeline";
import { intParam, patchSearchParams } from "./url-state";
import { findDuplicateWallet, kindsFor, looksLikeAddress, looksLikeSecret, looksLikeXpub } from "./wallet-input";
import { SETTINGS } from "@/test/admin";

const t = createTranslator("en");

describe("roles", () => {
  it("lets admins and owners write, owners manage users", () => {
    expect(canWrite("viewer")).toBe(false);
    expect(canWrite("admin")).toBe(true);
    expect(canWrite("owner")).toBe(true);
    expect(isOwner("admin")).toBe(false);
    expect(isOwner("owner")).toBe(true);
  });
});

describe("errors", () => {
  it("classifies and describes API errors", () => {
    expect(errorKind(new ApiError({ status: 0, code: "network_error", message: "x" }))).toBe("network");
    expect(errorKind(new ApiError({ status: 403, code: "forbidden", message: "x" }))).toBe("forbidden");
    expect(describeError(t, new ApiError({ status: 409, code: "conflict", message: "invoice is paid" }))).toBe(
      "Couldn't do that: invoice is paid",
    );
    expect(describeError(t, new ApiError({ status: 400, code: "invalid_request", message: "bad", fields: { name: "required" } }))).toBe(
      "Fix the highlighted fields.",
    );
    expect(describeError(t, new ApiError({ status: 503, code: "unavailable", message: "x" }))).toBe(
      "The server ran into an error. Try again later.",
    );
  });

  it("maps field errors onto form fields and returns the rest", () => {
    const setError = vi.fn();
    const err = new ApiError({
      status: 400,
      code: "invalid_request",
      message: "invalid",
      fields: { name: "required", expires_in: "too short", unknown_field: "?" },
    });
    const rest = applyFieldErrors(err, setError, ["name", "expires_minutes"], { expires_in: "expires_minutes" });
    expect(setError).toHaveBeenCalledWith("name", { type: "server", message: "required" }, { shouldFocus: true });
    expect(setError).toHaveBeenCalledWith("expires_minutes", { type: "server", message: "too short" }, { shouldFocus: false });
    expect(rest).toEqual({ unknown_field: "?" });
    expect(applyFieldErrors(new Error("x"), setError, [])).toBeNull();
  });
});

describe("explorer links", () => {
  it("fills the chain's templates, falling back to the built-in table", () => {
    expect(fillExplorerTemplate("https://x.io/tx/{tx}", "0xab")).toBe("https://x.io/tx/0xab");
    expect(fillExplorerTemplate("https://x.io/tx/%s", "0xab")).toBe("https://x.io/tx/0xab");
    expect(fillExplorerTemplate("https://x.io/tx/", "0xab")).toBe("https://x.io/tx/0xab");
    expect(txLink("ethereum", "0xab", [chainFixture({ explorer_tx_url: "https://scan.example/t/{hash}" })])).toBe(
      "https://scan.example/t/0xab",
    );
    expect(txLink("tron", "abc")).toBe("https://tronscan.org/#/transaction/abc");
    expect(addressLink("ethereum", "0x1", [chainFixture()])).toBe("https://etherscan.io/address/0x1");
    expect(safeHttpUrl("javascript:alert(1)")).toBeNull();
    expect(safeHttpUrl("https://ok.example/a")).toBe("https://ok.example/a");
  });
});

describe("chains", () => {
  it("derives scanner health and why a chain is not payable", () => {
    expect(chainHealth(chainFixture())).toBe("ok");
    expect(chainHealth(chainFixture({ enabled: false }))).toBe("disabled");
    expect(chainHealth(chainFixture({ last_error: "429" }))).toBe("error");
    expect(chainHealth(chainFixture({ lag: 340 }))).toBe("lagging");
    expect(chainHealth(chainFixture({ last_scan_at: null }))).toBe("idle");
    expect(notPayableReasons(chainFixture({ payable: false, enabled: false, wallet_id: null, rpc_urls: [] }))).toEqual([
      "disabled",
      "noWallet",
      "noRpc",
    ]);
    expect(notPayableReasons(chainFixture())).toEqual([]);
    // No default wallet, but merchants with their own wallet are still paid.
    const merchantOnly = chainFixture({ payable: false, wallet_id: null, merchant_wallets: 2 });
    expect(notPayableReasons(merchantOnly)).toEqual(["noWallet"]);
    expect(receivesPayments(merchantOnly)).toBe(true);
    expect(receivesPayments(chainFixture({ payable: false, wallet_id: null }))).toBe(false);
    expect(receivesPayments(chainFixture({ payable: false, enabled: false, wallet_id: null, merchant_wallets: 2 }))).toBe(false);
    expect(receivesPayments(chainFixture())).toBe(true);
    const { mainnet, testnet } = splitByNetwork([chainFixture(), chainFixture({ id: "sepolia", is_testnet: true })]);
    expect(mainnet.map((c) => c.id)).toEqual(["ethereum"]);
    expect(testnet.map((c) => c.id)).toEqual(["sepolia"]);
  });

  it("validates RPC URLs and recognises masked values", () => {
    expect(isRpcUrl("https://rpc.example/v2/key")).toBe(true);
    expect(isRpcUrl("wss://rpc.example")).toBe(true);
    expect(isRpcUrl("ftp://rpc.example")).toBe(false);
    expect(isRpcUrl("rpc.example")).toBe(false);
    expect(isMaskedRpcUrl("https://eth-mainnet.g.alchemy.com/…")).toBe(true);
    expect(rpcEnvName("tron-nile")).toBe("TRON_NILE");
  });
});

describe("wallet input", () => {
  it("refuses private keys and seed phrases", () => {
    expect(looksLikeSecret("xprv9s21ZrQH143K3QTDL4LXw2F7HEK3wJUD2nW2nRk4stbPy6cq3jPPqjiChkVvvNKmPGJxWUtg6LnF5kejMRNNU3TGtRBeJgk33yuGBxrMPHi")).toBe(true);
    expect(looksLikeSecret("zprvAWgYBBk7JR8Gjrh4UJQ2uJdG1r3WNRRfURiABBE3RvMXYSrRJL62XuezvGdPvG6GFBZduosCc1YP5wixPox7zhZLfiUm8aunE96BBa4Kei5R")).toBe(true);
    expect(looksLikeSecret(`0x${"a".repeat(64)}`)).toBe(true);
    expect(looksLikeSecret("abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about")).toBe(true);
    expect(looksLikeSecret("5HueCGU8rMjxEXxiPuD5BDku4MkFqeZyd4dZ1jvhTVqvbTLvyTJ")).toBe(true); // WIF
    // public data passes
    expect(looksLikeSecret("xpub661MyMwAqRbcFtXgS5sYJABqqG9YLmC4Q1Rdap9gSE8NqtwybGhePY2gZ29ESFjqJoCu1Rupje8YtGqsefD265TMg7usUDFdp6W1EGMcet8")).toBe(false);
    expect(looksLikeSecret("0x742d35Cc6634C0532925a3b844Bc454e4438f44e")).toBe(false);
    expect(looksLikeSecret("TNPeeaaFB7K9cmo4uQpcU32zGK8G1NYqeL")).toBe(false);
    expect(looksLikeSecret("9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM")).toBe(false);
  });

  it("checks xpub prefixes and address shapes per family", () => {
    const xpub = "xpub661MyMwAqRbcFtXgS5sYJABqqG9YLmC4Q1Rdap9gSE8NqtwybGhePY2gZ29ESFjqJoCu1Rupje8YtGqsefD265TMg7usUDFdp6W1EGMcet8";
    expect(looksLikeXpub("evm", xpub)).toBe(true);
    expect(looksLikeXpub("utxo", `z${xpub.slice(1)}`)).toBe(true);
    expect(looksLikeXpub("evm", `z${xpub.slice(1)}`)).toBe(false);
    expect(kindsFor("solana")).toEqual(["static"]);
    expect(kindsFor("evm")).toEqual(["xpub", "static"]);
    expect(looksLikeAddress("evm", "0x742d35Cc6634C0532925a3b844Bc454e4438f44e")).toBe(true);
    expect(looksLikeAddress("tron", "TNPeeaaFB7K9cmo4uQpcU32zGK8G1NYqeL")).toBe(true);
    expect(looksLikeAddress("utxo", "ltc1qg82tzq6kp7fpmt9s7ad8r3yyysk7qwn8xr6aen", "litecoin")).toBe(true);
    expect(looksLikeAddress("utxo", "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq", "litecoin")).toBe(false);
  });

  it("finds wallets that already hold the key material (the server refuses duplicates)", () => {
    const xpub = walletFixture({ id: "x", xpub: "xpubAAA" });
    const evm = walletFixture({ id: "e", kind: "static", xpub: "", address: "0x742d35Cc6634C0532925a3b844Bc454e4438f44e" });
    const tron = walletFixture({ id: "t", family: "tron", kind: "static", xpub: "", address: "TNPeeaaFB7K9cmo4uQpcU32zGK8G1NYqeL" });
    const all = [xpub, evm, tron];
    expect(findDuplicateWallet(all, "evm", "xpub", " xpubAAA ")?.id).toBe("x");
    expect(findDuplicateWallet(all, "tron", "xpub", "xpubAAA")?.id).toBe("x"); // one key, any family
    expect(findDuplicateWallet(all, "evm", "xpub", "xpubBBB")).toBeUndefined();
    expect(findDuplicateWallet(all, "evm", "static", "0x742d35cc6634c0532925a3b844bc454e4438f44e")?.id).toBe("e"); // EIP-55 case
    expect(findDuplicateWallet(all, "tron", "static", "TNPeeaaFB7K9cmo4uQpcU32zGK8G1NYqeL")?.id).toBe("t");
    expect(findDuplicateWallet(all, "solana", "static", "TNPeeaaFB7K9cmo4uQpcU32zGK8G1NYqeL")).toBeUndefined(); // per family
    expect(findDuplicateWallet(all, "evm", "static", "")).toBeUndefined();
    expect(findDuplicateWallet(undefined, "evm", "xpub", "xpubAAA")).toBeUndefined();
  });
});

describe("settings form conversions", () => {
  it("shows API units in operator units and back", () => {
    const form = settingsToForm(SETTINGS);
    expect(form.invoice_ttl).toBe("15");
    expect(form.invoice_ttl_unit).toBe("minutes");
    expect(form.grace_period).toBe("30");
    expect(form.underpay_tolerance_pct).toBe("0.5");
    expect(form.rate_markup_pct).toBe("1");
    expect(formToSettings(form)).toEqual(SETTINGS);
    expect(settingsToForm({ ...SETTINGS, invoice_ttl_seconds: 7200, grace_period_seconds: 45 })).toMatchObject({
      invoice_ttl: "2",
      invoice_ttl_unit: "hours",
      grace_period: "45",
      grace_period_unit: "seconds",
    });
    expect(
      formToSettings({ ...form, invoice_ttl: "1.5", invoice_ttl_unit: "hours", underpay_tolerance_pct: "0.25", checkout_base_url: "https://pay.example.com/" }),
    ).toMatchObject({ invoice_ttl_seconds: 5400, underpay_tolerance_bps: 25, checkout_base_url: "https://pay.example.com" });
  });

  it("validates ranges in the units the operator typed", () => {
    const schema = settingsSchema(t);
    const ok = schema.safeParse(settingsToForm(SETTINGS));
    expect(ok.success).toBe(true);
    const bad = schema.safeParse({
      ...settingsToForm(SETTINGS),
      invoice_ttl: "25",
      invoice_ttl_unit: "hours",
      underpay_tolerance_pct: "0.555",
      webhook_max_attempts: "0",
      checkout_base_url: "pay.example.com",
    });
    expect(bad.success).toBe(false);
    const issues = Object.fromEntries((bad.error?.issues ?? []).map((i) => [i.path.join("."), i.message]));
    expect(issues.invoice_ttl).toBe("From 1m to 1d");
    expect(issues.underpay_tolerance_pct).toBe("At most two decimals, e.g. 0.5");
    expect(issues.webhook_max_attempts).toBe("From 1 to 50");
    expect(issues.checkout_base_url).toBe("Enter an http(s) URL without a trailing /");
  });
});

describe("url state helpers", () => {
  it("patches search params and resets the page on filter changes", () => {
    const cur = new URLSearchParams("status=paid&page=3&q=abc");
    expect(patchSearchParams(cur, { status: "expired" }, true).toString()).toBe("status=expired&q=abc");
    expect(patchSearchParams(cur, { page: 4 }, false).toString()).toBe("status=paid&page=4&q=abc");
    expect(patchSearchParams(cur, { q: "" }, true).toString()).toBe("status=paid");
    expect(intParam("3", 1)).toBe(3);
    expect(intParam("0", 1)).toBe(1);
    expect(intParam("abc", 1)).toBe(1);
    expect(intParam(null, 1)).toBe(1);
  });
});

describe("invoice timeline", () => {
  it("orders every known timestamp and marks the deadline of open invoices", () => {
    const inv = {
      id: "i-1",
      status: "partial",
      created_at: "2026-11-16T07:00:00Z",
      updated_at: "2026-11-16T07:10:00Z",
      expires_at: "2026-11-16T07:15:00Z",
      paid_at: null,
      quotes: [
        quote({ id: "q-old", status: "superseded", created_at: "2026-11-16T07:01:00Z" }),
        quote({ id: "q-new", symbol: "BTC", chain_name: "Bitcoin", created_at: "2026-11-16T07:02:00Z" }),
      ],
      transfers: [transfer({ amount: "0.001", symbol: "BTC", detected_at: "2026-11-16T07:05:00Z", status: "pending" })],
      webhook_events: [
        {
          id: "e1",
          merchant_id: "m",
          invoice_id: "i-1",
          event_type: "invoice.payment_detected",
          url: "",
          status: "failed",
          attempts: 3,
          next_attempt_at: "",
          last_status_code: 502,
          last_error: "",
          last_response: "",
          delivered_at: null,
          created_at: "2026-11-16T07:05:01Z",
          payload: null,
        },
      ],
    } as unknown as InvoiceDetail;
    const entries = buildTimeline(inv);
    expect(entries.map((e) => e.kind)).toEqual(["created", "quoteSuperseded", "quote", "detected", "webhook", "expiresAt"]);
    expect(entries.map((e) => e.tone)).toEqual(["step", "step", "step", "progress", "danger", "future"]);
    expect(entries.at(-1)?.future).toBe(true);
    expect(entries.find((e) => e.kind === "webhook")?.note).toEqual({ key: "webhookFailed", vars: { attempts: "3" } });
    expect(entries.find((e) => e.kind === "detected")?.note?.key).toBe("awaitingConfirmations");
    const paid = buildTimeline({ ...inv, status: "paid", paid_at: "2026-11-16T07:20:00Z" }).at(-1);
    expect(paid?.kind).toBe("paid");
    expect(paid?.tone).toBe("success");
  });

  it("gives every outcome an unambiguous tone", () => {
    const base = {
      id: "i-2",
      status: "paid",
      created_at: "2026-11-16T07:00:00Z",
      updated_at: "2026-11-16T07:10:00Z",
      expires_at: "2026-11-16T07:30:00Z",
      paid_at: "2026-11-16T07:06:00Z",
      quotes: [quote({ id: "q", created_at: "2026-11-16T07:01:00Z" })],
      transfers: [
        transfer({ id: "t1", status: "confirmed", detected_at: "2026-11-16T07:02:00Z", confirmed_at: "2026-11-16T07:05:00Z" }),
        transfer({ id: "t2", status: "orphaned", detected_at: "2026-11-16T07:03:00Z" }),
      ],
      webhook_events: [
        { id: "w1", event_type: "invoice.confirming", status: "delivered", attempts: 1, created_at: "2026-11-16T07:02:01Z" },
        { id: "w2", event_type: "invoice.paid", status: "pending", attempts: 0, created_at: "2026-11-16T07:06:01Z" },
        { id: "w3", event_type: "invoice.paid", status: "pending", attempts: 2, created_at: "2026-11-16T07:06:02Z" },
      ],
    } as unknown as InvoiceDetail;
    const byKey = Object.fromEntries(buildTimeline(base).map((e) => [e.key, e]));
    expect(byKey["t-t1"].tone).toBe("step");
    expect(byKey["t-t1"].note).toBeUndefined();
    expect(byKey["tc-t1"].tone).toBe("success");
    expect(byKey["to-t2"].tone).toBe("danger");
    expect(byKey["w-w1"]).toMatchObject({ tone: "success", note: { key: "webhookDelivered" } });
    expect(byKey["w-w2"]).toMatchObject({ tone: "progress", note: { key: "webhookQueued" } });
    expect(byKey["w-w3"]).toMatchObject({ tone: "progress", note: { key: "webhookRetrying", vars: { attempts: "2" } } });
    expect(byKey.paid.tone).toBe("success");
    expect(buildTimeline({ ...base, status: "expired", paid_at: null }).find((e) => e.kind === "expired")?.tone).toBe("ended");
    expect(buildTimeline({ ...base, status: "underpaid", paid_at: null }).find((e) => e.kind === "underpaid")?.tone).toBe("danger");
    expect(buildTimeline({ ...base, status: "cancelled", paid_at: null }).find((e) => e.kind === "cancelled")?.tone).toBe("ended");
  });
});

describe("two-factor helpers", () => {
  it("normalises typed / pasted codes", () => {
    expect(normalizeOtp("123 456")).toBe("123456");
    expect(normalizeOtp("Code: 654-321!")).toBe("654321");
    expect(normalizeOtp("1234567890")).toBe("123456");
    expect(normalizeRecoveryCode("  ABCDE-12345 ")).toBe("abcde-12345");
    expect(groupSecret("JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP")).toBe("JBSW Y3DP EHPK 3PXP JBSW Y3DP EHPK 3PXP");
    expect(groupSecret("ABCDEF", 4)).toBe("ABCD EF");
  });

  it("recognises the 202 login challenge", () => {
    expect(isLoginChallenge({ two_factor_required: true, challenge_token: "x", expires_at: "2026-01-01T00:00:00Z" })).toBe(true);
    expect(
      isLoginChallenge({ id: "u", email: "a@b.c", role: "owner", active: true, last_login_at: null, created_at: "", two_factor_enabled: true }),
    ).toBe(false);
    expect(isLoginChallenge(null)).toBe(false);
  });

  it("builds the recovery-codes file", () => {
    const text = recoveryCodesText(t, { email: "owner@cryptopay.test", codes: ["aaaaa-bbbbb", "ccccc-ddddd"], generatedAt: new Date("2026-11-17T08:00:00.123Z") });
    expect(text).toContain("CryptoPay recovery codes");
    expect(text).toContain("Account: owner@cryptopay.test");
    expect(text).toContain("Generated: 2026-11-17 08:00:00 UTC");
    expect(text).toContain(" 1. aaaaa-bbbbb\n 2. ccccc-ddddd");
    expect(recoveryCodesFileName("Owner+Ops@CryptoPay.test")).toBe("cryptopay-recovery-codes-owner-ops-cryptopay.test.txt");
  });

  it("labels every documented e-mail kind", () => {
    expect(EMAIL_KINDS).toHaveLength(16);
    expect(new Set(EMAIL_KINDS.map((k) => k.kind)).size).toBe(16);
    expect(emailKindLabel(t, "alert.scanner_failing")).toBe("Scanner failing");
    expect(emailKindLabel(t, "security.unknown_future_kind")).toBe("security.unknown_future_kind");
    for (const k of EMAIL_KINDS) expect(emailKindLabel(t, k.kind)).not.toContain("admin.");
  });
});

describe("settings form · security and e-mail", () => {
  it("round-trips the new settings and validates recipients", () => {
    const s = { ...SETTINGS, require_2fa: true, alert_emails: true, alert_recipients: ["ops@example.com"], email_locale: "en" as const };
    const form = settingsToForm(s);
    expect(form).toMatchObject({ require_2fa: true, alert_recipients: ["ops@example.com"], email_locale: "en" });
    expect(formToSettings(form)).toEqual(s);
    expect(formToSettings({ ...form, alert_recipients: [" cfo@example.com "] }).alert_recipients).toEqual(["cfo@example.com"]);

    const schema = settingsSchema(t);
    expect(schema.safeParse(form).success).toBe(true);
    const dup = schema.safeParse({ ...form, alert_recipients: ["a@b.co", "A@b.co"] });
    expect(dup.error?.issues[0].message).toBe("This address is already in the list");
    const many = schema.safeParse({ ...form, alert_recipients: Array.from({ length: 11 }, (_, i) => `u${i}@example.com`) });
    expect(many.error?.issues[0].message).toBe("At most 10 recipients");
    const invalid = schema.safeParse({ ...form, alert_recipients: ["nope"] });
    expect(invalid.error?.issues[0].message).toBe("Enter a valid email address");

    expect(recipientProblem(t, [], "ops@example.com")).toBeNull();
    expect(recipientProblem(t, ["ops@example.com"], "OPS@example.com")).toBe("This address is already in the list");
    expect(recipientProblem(t, [], "ops")).toBe("Enter a valid email address");
    expect(recipientProblem(t, Array.from({ length: 10 }, (_, i) => `u${i}@example.com`), "x@example.com")).toBe("At most 10 recipients");
  });

  it("describes the two_factor_setup_required error", () => {
    const e = new ApiError({ status: 403, code: "two_factor_setup_required", message: "setup first" });
    expect(describeError(t, e)).toBe("Two-step verification must be enabled for your account first.");
    expect(errorKind(e)).toBe("forbidden");
  });
});
