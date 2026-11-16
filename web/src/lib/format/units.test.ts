import { describe, expect, it } from "vitest";
import { dateInputToIso, formatDay, formatRelativeTime, formatShortDate } from "./relative";
import {
  bpsToPercent,
  formatCount,
  formatDuration,
  formatMillis,
  formatPercent,
  joinDuration,
  percentToBps,
  plotValue,
  splitDuration,
} from "./units";

describe("basis points ↔ percent", () => {
  it("converts exactly in both directions", () => {
    expect(bpsToPercent(50)).toBe("0.5");
    expect(bpsToPercent(1)).toBe("0.01");
    expect(bpsToPercent(1000)).toBe("10");
    expect(bpsToPercent(0)).toBe("0");
    expect(bpsToPercent(-25)).toBe("-0.25");
    expect(percentToBps("0.5")).toBe(50);
    expect(percentToBps("10")).toBe(1000);
    expect(percentToBps("0.01")).toBe(1);
    expect(percentToBps(" 1.25 ")).toBe(125);
    for (const bps of [0, 1, 7, 50, 99, 100, 125, 2000]) expect(percentToBps(bpsToPercent(bps))).toBe(bps);
  });

  it("rejects values finer than 1 bp or not numbers", () => {
    expect(percentToBps("0.555")).toBeNull();
    expect(percentToBps("abc")).toBeNull();
    expect(percentToBps("")).toBeNull();
    expect(percentToBps("1e2")).toBeNull();
  });
});

describe("durations", () => {
  it("picks the largest exact unit", () => {
    expect(splitDuration(900)).toEqual({ value: 15, unit: "minutes" });
    expect(splitDuration(7200)).toEqual({ value: 2, unit: "hours" });
    expect(splitDuration(45)).toEqual({ value: 45, unit: "seconds" });
    expect(splitDuration(5400, ["minutes", "hours"])).toEqual({ value: 90, unit: "minutes" });
    expect(splitDuration(0, ["minutes", "hours"])).toEqual({ value: 0, unit: "minutes" });
  });

  it("joins value + unit into whole seconds", () => {
    expect(joinDuration("15", "minutes")).toBe(900);
    expect(joinDuration("1.5", "hours")).toBe(5400);
    expect(joinDuration("0.5", "minutes")).toBe(30);
    expect(joinDuration(2, "hours")).toBe(7200);
    expect(joinDuration("0.001", "minutes")).toBeNull(); // 0.06 s
    expect(joinDuration("-1", "minutes")).toBeNull();
    expect(joinDuration("x", "minutes")).toBeNull();
  });

  it("formats compact durations per locale", () => {
    expect(formatDuration(90, "en")).toBe("1m 30s");
    expect(formatDuration(7200, "en")).toBe("2h");
    expect(formatDuration(0, "en")).toBe("0s");
    expect(formatDuration(90061, "zh")).toBe("1 天 1 小时 1 分 1 秒");
    expect(formatMillis(400, "en")).toBe("400 ms");
    expect(formatMillis(1500, "en")).toBe("1.5 s");
    expect(formatMillis(120_000, "zh")).toBe("2 分钟");
  });
});

describe("ratios, counts and plot values", () => {
  it("formats percentages and counts", () => {
    expect(formatPercent(0.4567, "en")).toBe("45.7%");
    expect(formatPercent(1, "en", 0)).toBe("100%");
    expect(formatPercent(Number.NaN, "en")).toBe("–");
    expect(formatCount(12345, "en")).toBe("12,345");
  });

  it("converts decimal strings to numbers for charts only", () => {
    expect(plotValue("1234.5")).toBe(1234.5);
    expect(plotValue(null)).toBe(0);
    expect(plotValue("not a number")).toBe(0);
  });
});

describe("relative and calendar dates", () => {
  const now = Date.parse("2026-11-16T08:00:00Z");
  it("says how long ago", () => {
    expect(formatRelativeTime("2026-11-16T07:57:00Z", now, "en")).toBe("3 minutes ago");
    expect(formatRelativeTime("2026-11-16T07:59:50Z", now, "en")).toBe("now");
    expect(formatRelativeTime("2026-11-16T10:00:00Z", now, "en")).toBe("in 2 hours");
    expect(formatRelativeTime("2026-11-15T08:00:00Z", now, "en")).toBe("yesterday");
    expect(formatRelativeTime(null, now, "en")).toBe("");
    expect(formatRelativeTime("2026-11-16T07:57:00Z", now, "zh")).toBe("3分钟前");
  });

  it("maps date inputs to the edges of a local day", () => {
    const start = dateInputToIso("2026-11-16", "start")!;
    const end = dateInputToIso("2026-11-16", "end")!;
    expect(new Date(start).getDate()).toBe(16);
    expect(new Date(start).getHours()).toBe(0);
    expect(new Date(end).getHours()).toBe(23);
    expect(Date.parse(end) - Date.parse(start)).toBe(24 * 3600 * 1000 - 1);
    expect(dateInputToIso("", "start")).toBeUndefined();
    expect(dateInputToIso("16/11/2026", "start")).toBeUndefined();
  });

  it("formats chart days without a time-zone shift", () => {
    expect(formatShortDate("2026-11-16", "en")).toBe("Nov 16");
    expect(formatDay("2026-01-01", "en")).toBe("Jan 1, 2026");
  });
});
