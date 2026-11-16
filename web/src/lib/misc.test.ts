import { afterEach, describe, expect, it, vi } from "vitest";
import { copyText } from "./clipboard";
import { isMobileUserAgent } from "./device";
import { explorerAddressUrl, explorerTxUrl } from "./explorer";

describe("explorer links", () => {
  it("builds tx and address links per chain", () => {
    expect(explorerTxUrl("ethereum", "0xabc")).toBe("https://etherscan.io/tx/0xabc");
    expect(explorerTxUrl("tron", "ff")).toBe("https://tronscan.org/#/transaction/ff");
    expect(explorerTxUrl("solana-devnet", "sig")).toBe("https://solscan.io/tx/sig?cluster=devnet");
    expect(explorerTxUrl("bitcoin-testnet", "id")).toBe("https://mempool.space/testnet/tx/id");
    expect(explorerAddressUrl("base", "0x1")).toBe("https://basescan.org/address/0x1");
    expect(explorerAddressUrl("litecoin", "ltc1q")).toBe("https://litecoinspace.org/address/ltc1q");
  });

  it("returns null for unknown chains or empty hashes", () => {
    expect(explorerTxUrl("dogecoin", "x")).toBeNull();
    expect(explorerTxUrl("ethereum", "")).toBeNull();
  });
});

describe("clipboard", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("uses the async clipboard API when available", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText } });
    await expect(copyText("99.84")).resolves.toBe(true);
    expect(writeText).toHaveBeenCalledWith("99.84");
  });

  it("falls back to execCommand when the API is missing or denied", async () => {
    vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText: vi.fn().mockRejectedValue(new Error("denied")) } });
    const exec = vi.fn().mockReturnValue(true);
    document.execCommand = exec;
    await expect(copyText("addr")).resolves.toBe(true);
    expect(exec).toHaveBeenCalledWith("copy");
    expect(document.querySelector("textarea")).toBeNull();
  });

  it("reports failure", async () => {
    vi.stubGlobal("navigator", { ...navigator, clipboard: undefined });
    document.execCommand = vi.fn().mockReturnValue(false);
    await expect(copyText("x")).resolves.toBe(false);
  });
});

describe("device", () => {
  it("detects phones and iPadOS", () => {
    expect(isMobileUserAgent("Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X)")).toBe(true);
    expect(isMobileUserAgent("Mozilla/5.0 (Linux; Android 15) Mobile Safari")).toBe(true);
    expect(isMobileUserAgent("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)", 5)).toBe(true);
    expect(isMobileUserAgent("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)", 0)).toBe(false);
    expect(isMobileUserAgent("Mozilla/5.0 (Windows NT 10.0; Win64; x64)")).toBe(false);
  });
});
