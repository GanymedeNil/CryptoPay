import { describe, expect, it } from "vitest";
import {
  defaultOrder,
  failedWebhooksHref,
  invoicesHref,
  isIdle,
  nextSort,
  overviewHref,
  rangeParam,
  sortFromParams,
  sortPatch,
} from "./merchant-overview";

describe("merchant overview helpers", () => {
  it("reads the range and the sort from the URL with the server's defaults", () => {
    expect(rangeParam("7")).toBe(7);
    expect(rangeParam("90")).toBe(90);
    expect(rangeParam("365")).toBe(30);
    expect(rangeParam(null)).toBe(30);
    expect(sortFromParams(null, null)).toEqual({ sort: "volume_usd", order: "desc" });
    expect(sortFromParams("name", null)).toEqual({ sort: "name", order: "asc" });
    expect(sortFromParams("invoices", "asc")).toEqual({ sort: "invoices", order: "asc" });
    expect(sortFromParams("created_at", "sideways")).toEqual({ sort: "volume_usd", order: "desc" });
    expect(defaultOrder("failed_webhooks")).toBe("desc");
  });

  it("flips the sorted column and starts another column at its default order", () => {
    const volume = { sort: "volume_usd", order: "desc" } as const;
    expect(nextSort(volume, "volume_usd")).toEqual({ sort: "volume_usd", order: "asc" });
    expect(nextSort({ sort: "volume_usd", order: "asc" }, "volume_usd")).toEqual({ sort: "volume_usd", order: "desc" });
    expect(nextSort(volume, "name")).toEqual({ sort: "name", order: "asc" });
    expect(nextSort({ sort: "name", order: "desc" }, "open_invoices")).toEqual({ sort: "open_invoices", order: "desc" });
  });

  it("keeps defaults out of the URL", () => {
    expect(sortPatch({ sort: "volume_usd", order: "desc" })).toEqual({ sort: null, order: null });
    expect(sortPatch({ sort: "volume_usd", order: "asc" })).toEqual({ sort: null, order: "asc" });
    expect(sortPatch({ sort: "name", order: "asc" })).toEqual({ sort: "name", order: null });
    expect(sortPatch({ sort: "name", order: "desc" })).toEqual({ sort: "name", order: "desc" });
  });

  it("builds filtered links", () => {
    expect(overviewHref("m-1")).toBe("/admin?merchant=m-1");
    expect(overviewHref("m-1", 7)).toBe("/admin?days=7&merchant=m-1");
    expect(invoicesHref(null)).toBe("/admin/invoices");
    expect(invoicesHref("m-1")).toBe("/admin/invoices?merchant_id=m-1");
    expect(invoicesHref("m-1", "paid")).toBe("/admin/invoices?status=paid&merchant_id=m-1");
    expect(failedWebhooksHref()).toBe("/admin/webhooks?status=failed");
    expect(failedWebhooksHref("m 1")).toBe("/admin/webhooks?status=failed&merchant_id=m+1");
  });

  it("calls a merchant idle without invoices created or paid in the range", () => {
    expect(isIdle({ invoices: 0, paid_invoices: 0 })).toBe(true);
    expect(isIdle({ invoices: 0, paid_invoices: 1 })).toBe(false);
    expect(isIdle({ invoices: 2, paid_invoices: 0 })).toBe(false);
  });
});
