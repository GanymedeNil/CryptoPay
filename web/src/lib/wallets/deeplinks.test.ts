import { describe, expect, it } from "vitest";
import { DEEP_LINK_WALLETS, deepLinksFor, stripScheme } from "./deeplinks";

const URL_ = "https://pay.example.com/pay/3f1c2b7a-9d4e-4c1a-8b2f-6e5d4c3b2a10";
const ENC = encodeURIComponent(URL_);

const hrefs = (family: Parameters<typeof deepLinksFor>[0]) =>
  Object.fromEntries(deepLinksFor(family, URL_).map((l) => [l.id, l.href]));

describe("deep links", () => {
  it("offers only the wallets relevant to each family, in order", () => {
    expect(deepLinksFor("evm", URL_).map((l) => l.id)).toEqual([
      "metamask",
      "trust",
      "okx",
      "tokenpocket",
      "coinbase",
      "imtoken",
    ]);
    expect(deepLinksFor("tron", URL_).map((l) => l.id)).toEqual(["tronlink", "okx", "tokenpocket", "imtoken"]);
    expect(deepLinksFor("solana", URL_).map((l) => l.id)).toEqual(["phantom", "solflare"]);
    expect(deepLinksFor("utxo", URL_)).toEqual([]);
  });

  it("builds the documented EVM formats", () => {
    const l = hrefs("evm");
    expect(l.metamask).toBe("https://link.metamask.io/dapp/pay.example.com/pay/3f1c2b7a-9d4e-4c1a-8b2f-6e5d4c3b2a10");
    expect(l.trust).toBe(`https://link.trustwallet.com/open_url?coin_id=60&url=${ENC}`);
    expect(l.coinbase).toBe(`https://go.cb-w.com/dapp?cb_url=${ENC}`);
    expect(l.imtoken).toBe(`https://connect.token.im/link/navigate/DappView?url=${ENC}`);
  });

  it("double-encodes the OKX deep link inside its universal link", () => {
    const href = hrefs("evm").okx;
    expect(href.startsWith("https://web3.okx.com/download?deeplink=")).toBe(true);
    const inner = decodeURIComponent(new URL(href).search.replace("?deeplink=", ""));
    expect(inner).toBe(`okx://wallet/dapp/url?dappUrl=${ENC}`);
    expect(decodeURIComponent(inner.split("dappUrl=")[1])).toBe(URL_);
  });

  it("encodes TokenPocket and TronLink JSON params", () => {
    const tp = new URL(hrefs("tron").tokenpocket.replace("tpdapp://", "https://x/"));
    expect(JSON.parse(tp.searchParams.get("params")!)).toEqual({ url: URL_, chain: "TRX", source: "pay.example.com" });
    const tpEvm = new URL(hrefs("evm").tokenpocket.replace("tpdapp://", "https://x/"));
    expect(JSON.parse(tpEvm.searchParams.get("params")!).chain).toBe("ETH");

    const tl = hrefs("tron").tronlink;
    expect(tl.startsWith("tronlinkoutside://pull.activity?param=")).toBe(true);
    const param = JSON.parse(decodeURIComponent(tl.split("param=")[1]));
    expect(param).toEqual({ url: URL_, action: "open", protocol: "TronLink", version: "1.0" });
  });

  it("builds the Solana browse links with a ref", () => {
    const l = hrefs("solana");
    const ref = encodeURIComponent("https://pay.example.com");
    expect(l.phantom).toBe(`https://phantom.com/ul/browse/${ENC}?ref=${ref}`);
    expect(l.solflare).toBe(`https://solflare.com/ul/v1/browse/${ENC}?ref=${ref}`);
  });

  it("marks custom-scheme links", () => {
    const custom = DEEP_LINK_WALLETS.filter((w) => w.scheme === "custom").map((w) => w.id);
    expect(custom.sort()).toEqual(["tokenpocket", "tronlink"]);
    expect(stripScheme("http://a.b/c")).toBe("a.b/c");
  });
});
