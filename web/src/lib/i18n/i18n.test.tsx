import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it } from "vitest";
import { isLocale, LOCALE_COOKIE, negotiateLocale } from "./config";
import { en } from "./dictionaries/en";
import { zh } from "./dictionaries/zh";
import { I18nProvider, useLocale, useT } from "./provider";
import { createTranslator, interpolate } from "./translate";

function leafKeys(obj: object, prefix = ""): string[] {
  return Object.entries(obj).flatMap(([k, v]) =>
    typeof v === "string" ? [`${prefix}${k}`] : leafKeys(v as object, `${prefix}${k}.`),
  );
}

function placeholders(s: string): string[] {
  return [...s.matchAll(/\{(\w+)\}/g)].map((m) => m[1]).sort();
}

describe("dictionaries", () => {
  it("have exactly the same keys and placeholders in zh and en", () => {
    const zk = leafKeys(zh).sort();
    const ek = leafKeys(en).sort();
    expect(ek).toEqual(zk);
    const t = { zh: createTranslator("zh"), en: createTranslator("en") };
    for (const key of zk) {
      // No vars → templates come back untouched, so placeholders can be compared.
      expect(placeholders(t.en(key as never)), key).toEqual(placeholders(t.zh(key as never)));
    }
  });

  it("have no empty strings", () => {
    const t = createTranslator("en");
    for (const key of leafKeys(en)) expect(t(key as never).trim(), key).not.toBe("");
  });
});

describe("translate", () => {
  it("interpolates named placeholders", () => {
    expect(interpolate("Pay {merchant} {n}", { merchant: "Acme", n: 3 })).toBe("Pay Acme 3");
    expect(interpolate("{missing} stays", {})).toBe("{missing} stays");
    expect(createTranslator("en")("checkout.documentTitle", { merchant: "Acme" })).toBe("Pay Acme");
    expect(createTranslator("zh")("checkout.documentTitle", { merchant: "Acme" })).toBe("付款给 Acme");
  });

  it("falls back to the key for unknown keys", () => {
    expect(createTranslator("en")("nope.missing" as never)).toBe("nope.missing");
  });
});

describe("locale negotiation", () => {
  it("parses Accept-Language with q values", () => {
    expect(negotiateLocale("en-US,en;q=0.9,zh;q=0.8")).toBe("en");
    expect(negotiateLocale("fr-FR,zh-CN;q=0.9,en;q=0.8")).toBe("zh");
    expect(negotiateLocale("zh;q=0.5,en;q=0.9")).toBe("en");
    expect(negotiateLocale("fr,de")).toBe("zh");
    expect(negotiateLocale(null)).toBe("zh");
    expect(isLocale("en")).toBe(true);
    expect(isLocale("fr")).toBe(false);
  });
});

function Probe() {
  const t = useT();
  const { locale, setLocale } = useLocale();
  return (
    <div>
      <p>{t("checkout.select.title")}</p>
      <button onClick={() => setLocale(locale === "zh" ? "en" : "zh")}>switch</button>
    </div>
  );
}

describe("I18nProvider", () => {
  beforeEach(() => {
    document.cookie = `${LOCALE_COOKIE}=; Max-Age=0; Path=/`;
    localStorage.clear();
  });

  it("renders the initial locale and switches, persisting cookie + storage + <html lang>", async () => {
    document.cookie = `${LOCALE_COOKIE}=zh; Path=/`;
    render(
      <I18nProvider initialLocale="zh">
        <Probe />
      </I18nProvider>,
    );
    expect(screen.getByText("选择付款方式")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "switch" }));
    expect(screen.getByText("Choose how to pay")).toBeInTheDocument();
    expect(document.cookie).toContain(`${LOCALE_COOKIE}=en`);
    expect(localStorage.getItem("cp_locale")).toBe("en");
    expect(document.documentElement.lang).toBe("en");
  });

  it("restores the locale from localStorage when the cookie is gone", async () => {
    localStorage.setItem("cp_locale", "en");
    render(
      <I18nProvider initialLocale="zh">
        <Probe />
      </I18nProvider>,
    );
    await act(async () => {
      await Promise.resolve();
    });
    expect(screen.getByText("Choose how to pay")).toBeInTheDocument();
    expect(document.cookie).toContain(`${LOCALE_COOKIE}=en`);
  });
});
