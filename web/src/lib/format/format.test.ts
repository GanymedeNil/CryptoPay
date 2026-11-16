import { describe, expect, it } from "vitest";
import { chunk, shortenAddress, shortenHash, splitAddressForVerification } from "./address";
import {
  addDecimal,
  baseToDecimal,
  compareDecimal,
  decimalToBase,
  formatAmount,
  formatMoney,
  isZeroBase,
  isZeroDecimal,
  normalizeDecimal,
  plainAmount,
  roundDecimal,
  splitAmount,
  subtractDecimal,
  toSafeInteger,
} from "./amount";
import {
  computeClockOffset,
  countdownParts,
  formatCountdown,
  remainingFraction,
  remainingMs,
} from "./countdown";

describe("amount math (exact, no floats)", () => {
  it("converts base units ↔ decimals exactly", () => {
    expect(baseToDecimal("1500000", 6)).toBe("1.5");
    expect(baseToDecimal("1", 18)).toBe("0.000000000000000001");
    expect(baseToDecimal("0", 8)).toBe("0");
    expect(baseToDecimal("123456789012345678901234567890", 18)).toBe("123456789012.34567890123456789");
    expect(decimalToBase("1.5", 6)).toBe("1500000");
    expect(decimalToBase("0.1", 18)).toBe("100000000000000000");
    expect(decimalToBase("99.00", 6)).toBe("99000000");
  });

  it("refuses to round silently", () => {
    expect(() => decimalToBase("0.1234567", 6)).toThrow(RangeError);
    expect(() => baseToDecimal("1.5", 6)).toThrow(RangeError);
    expect(() => decimalToBase("abc", 6)).toThrow(RangeError);
  });

  it("does arithmetic that floats get wrong", () => {
    expect(addDecimal("0.1", "0.2")).toBe("0.3");
    expect(subtractDecimal("1", "0.9")).toBe("0.1");
    expect(subtractDecimal("0.00050899", "0.00101799")).toBe("-0.000509");
    expect(compareDecimal("1.10", "1.1")).toBe(0);
    expect(compareDecimal("2", "10")).toBe(-1);
    expect(compareDecimal("-1", "-2")).toBe(1);
  });

  it("normalises and detects zero", () => {
    expect(normalizeDecimal("0012.3400")).toBe("12.34");
    expect(normalizeDecimal("-0.000")).toBe("0");
    expect(plainAmount("99.840")).toBe("99.84");
    expect(isZeroDecimal("0.000000")).toBe(true);
    expect(isZeroDecimal("0.0001")).toBe(false);
    expect(isZeroBase("000")).toBe(true);
    expect(isZeroBase("10")).toBe(false);
  });

  it("rounds with explicit modes", () => {
    expect(roundDecimal("1.005", 2)).toBe("1.01"); // 1.005.toFixed(2) === "1.00"
    expect(roundDecimal("1.0049", 2)).toBe("1");
    expect(roundDecimal("1.001", 2, "ceil")).toBe("1.01");
    expect(roundDecimal("-1.001", 2, "ceil")).toBe("-1");
    expect(roundDecimal("-1.001", 2, "floor")).toBe("-1.01");
    expect(roundDecimal("9.999", 2)).toBe("10");
  });

  it("formats with grouping and padding", () => {
    expect(formatAmount("1234567.5")).toBe("1,234,567.5");
    expect(formatAmount("1234567.5", { minFractionDigits: 2 })).toBe("1,234,567.50");
    expect(formatAmount("0.123456789", { maxFractionDigits: 4 })).toBe("0.1235");
    expect(formatAmount("1000", { group: false })).toBe("1000");
    expect(formatAmount("-42.10")).toBe("-42.1");
    expect(formatAmount("99.00", { keepScale: true })).toBe("99.00");
    expect(formatAmount("1234.5430", { keepScale: true })).toBe("1,234.5430");
    expect(formatAmount("1.23456", { keepScale: true, maxFractionDigits: 3 })).toBe("1.235");
    expect(splitAmount("1,234.50")).toEqual({ int: "1,234", frac: "50" });
    expect(splitAmount("99")).toEqual({ int: "99", frac: "" });
  });

  it("formats fiat and crypto prices", () => {
    expect(formatMoney("99", "USD", "en")).toBe("$99.00");
    expect(formatMoney("1234.5", "EUR", "en")).toBe("€1,234.50");
    expect(formatMoney("688", "CNY", "zh-CN")).toBe("¥688.00");
    expect(formatMoney("1000", "JPY", "en")).toBe("¥1,000");
    expect(formatMoney("12.5", "USDT", "en")).toBe("12.5 USDT");
    expect(formatMoney("0.01", "BTC", "en")).toBe("0.01 BTC");
  });

  it("converts to a JS number only when exact", () => {
    expect(toSafeInteger("2100000000000000")).toBe(2_100_000_000_000_000);
    expect(() => toSafeInteger("9007199254740993")).toThrow(RangeError);
    expect(() => toSafeInteger("-1")).toThrow(RangeError);
  });
});

describe("address helpers", () => {
  it("shortens", () => {
    expect(shortenAddress("0x742d35Cc6634C0532925a3b844Bc454e4438f44e")).toBe("0x742d…f44e");
    expect(shortenAddress("short")).toBe("short");
    expect(shortenAddress("")).toBe("");
    expect(shortenHash("a".repeat(64))).toBe(`${"a".repeat(8)}…${"a".repeat(6)}`);
  });

  it("splits for verification without losing characters", () => {
    const addr = "0x742d35Cc6634C0532925a3b844Bc454e4438f44e";
    const { head, middle, tail } = splitAddressForVerification(addr, 6);
    expect(head).toBe("0x742d35");
    expect(tail).toBe("38f44e");
    expect(head + middle + tail).toBe(addr);
    expect(splitAddressForVerification("TShort", 6)).toEqual({ head: "TShort", middle: "", tail: "" });
    expect(chunk("abcdefghij", 4)).toEqual(["abcd", "efgh", "ij"]);
  });
});

describe("countdown", () => {
  const now = Date.parse("2026-11-16T08:00:00Z");

  it("corrects drift with the server clock", () => {
    const offset = computeClockOffset("2026-11-16T08:00:30Z", now);
    expect(offset).toBe(30_000);
    // The client clock is 30 s behind: 5 min on the server is 4:30 left.
    expect(remainingMs("2026-11-16T08:05:00Z", now, offset)).toBe(270_000);
    expect(computeClockOffset("not a date", now)).toBe(0);
  });

  it("never goes negative and formats", () => {
    expect(remainingMs("2026-11-16T07:00:00Z", now)).toBe(0);
    expect(formatCountdown(75_000)).toBe("01:15");
    expect(formatCountdown(3_726_000)).toBe("1:02:06");
    expect(formatCountdown(0)).toBe("00:00");
    expect(formatCountdown(500)).toBe("00:01"); // rounds up: never shows 00:00 early
    expect(countdownParts(61_000)).toMatchObject({ minutes: 1, seconds: 1, totalSeconds: 61 });
  });

  it("computes the remaining fraction of the window", () => {
    expect(remainingFraction("2026-11-16T07:50:00Z", "2026-11-16T08:10:00Z", now)).toBeCloseTo(0.5);
    expect(remainingFraction("2026-11-16T07:50:00Z", "2026-11-16T07:55:00Z", now)).toBe(0);
    expect(remainingFraction("bad", "2026-11-16T07:55:00Z", now)).toBe(0);
  });
});
