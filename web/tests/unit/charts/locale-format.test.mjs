// Cached locale formatting must read exactly like the built-ins it replaces.
import test from "node:test";
import assert from "node:assert/strict";
import { localeDate, localeDateTime, localeNumber, localeTime } from "../../../src/locale-format";

test("cached formatters match toLocaleString and its Date variants", () => {
  for (const value of [0, -0.004, 1234.5678, 0.256, 1e9, NaN]) {
    for (const options of [undefined, { maximumFractionDigits: 3 }, { style: "percent", maximumFractionDigits: 1 }, { notation: "compact", maximumFractionDigits: 1 }]) {
      assert.equal(localeNumber(value, options), value.toLocaleString(undefined, options));
      assert.equal(localeNumber(value, options, "de-DE"), value.toLocaleString("de-DE", options));
    }
  }
  const date = new Date("2026-03-09T17:05:09Z");
  // Each Date method adds its own default fields when the options name none.
  for (const options of [undefined, {}, { timeZone: "UTC" }, { month: "short", day: "numeric" }, { hour: "numeric", minute: "2-digit" }, { weekday: "short", month: "short", day: "numeric", year: "numeric", timeZone: "UTC" }, { month: "short", timeZone: "UTC" }]) {
    assert.equal(localeDateTime(date, options, "en-US"), date.toLocaleString("en-US", options));
    assert.equal(localeDate(date, options, "en-US"), date.toLocaleDateString("en-US", options));
    assert.equal(localeTime(date, options, "en-US"), date.toLocaleTimeString("en-US", options));
    assert.equal(localeDateTime(date, options), date.toLocaleString(undefined, options));
  }
  assert.equal(localeDate(date.getTime(), { year: "numeric" }, "en-US"), "2026");
  assert.equal(localeDateTime(new Date(NaN), { month: "short" }), new Date(NaN).toLocaleString(undefined, { month: "short" }));
  // Repeated calls reuse a formatter and still format each value.
  assert.notEqual(localeNumber(1, { maximumFractionDigits: 3 }), localeNumber(2, { maximumFractionDigits: 3 }));
});
