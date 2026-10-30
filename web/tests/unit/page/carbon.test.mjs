// The Carbon Impact arithmetic in carbon.js (window.pharosCarbon.estimate,
// with mass and energy for the headline figures). carbon.go is a separate
// implementation with its own test, TestCarbonEnergyArithmetic.
import { test } from "node:test";
import assert from "node:assert/strict";
import factors from "../../../../carbon/co2_factors.json";
import { loadClassicScript } from "../helpers/classic-script.mjs";
import shared from "../../../../testdata/carbon-estimates.json";
import { deriveCarbon } from "../../../src/carbon-analysis-catalog";

// carbon.js reads its assumption settings (grid, PUE, energy scenario, input
// sensitivity) from pharosPrefs once, when it loads.
function carbon(settings = {}) {
  const pharosPrefs = { get: (key, fallback) => key === "pharos-carbon-v1" ? settings : fallback, set() {} };
  return loadClassicScript("carbon.js", { pharosPrefs }).window.pharosCarbon;
}

test("linked Carbon assumptions override saved defaults without writing preferences", () => {
  const saved = { scenario: "low", grid: "world" }, linked = { scenario: "high", pueScenario: "high", inputSensitivity: "third", grid: "custom", customGrid: 100, vehicle: "car_hybrid", flightFrom: "lax", flightTo: "jfk" };
  const writes = [], location = new URL("http://pharos.test/analyze?analyze_topic=carbon");
  location.searchParams.set("analyze_carbon", JSON.stringify(linked));
  const loaded = loadClassicScript("carbon.js", { location, pharosPrefs: { get: (_key, fallback) => saved ?? fallback, set: (...args) => writes.push(args) } });
  const result = loaded.window.pharosCarbon.estimate(factors, counts("medium", "cache_read", 1e6));
  close(result.co2, 20.8, "URL assumptions applied");
  assert.deepEqual(structuredClone(loaded.window.pharosCarbon.settingsSnapshot(factors)), linked);
  location.searchParams.set("analyze_carbon", JSON.stringify({ ...linked, scenario: "central" }));
  loaded.window.dispatchEvent(new loaded.window.Event("pharos:route"));
  close(loaded.window.pharosCarbon.estimate(factors, counts("medium", "cache_read", 1e6)).co2, 400 * .02 * 5 / 3 * 1.3 * .1, "route assumptions applied");
  assert.equal(writes.length, 0);
});

function close(actual, expected, message) {
  assert.ok(Math.abs(actual - expected) <= 1e-9 * Math.max(1, Math.abs(expected)), `${message}: ${actual} is not ${expected}`);
}

const counts = (tier, category, tokens) => ({ tokens: { [tier]: { uncached_input: 0, cache_write: 0, cache_read: 0, output: 0, [category]: tokens } } });
const custom100 = { grid: "custom", customGrid: 100 };

test("one million mid-size tokens follow each grid, PUE, scenario and input sensitivity", () => {
  const output = counts("medium", "output", 1e6), cacheReads = counts("medium", "cache_read", 1e6);
  const cases = [
    // 1M output × 400 Wh per 1M × PUE 1.15 × 350 g/kWh (US average).
    { settings: {}, period: output, wh: 400, co2: 161, shown: "161 g" },
    // Settings left by an older version fall back to the defaults.
    { settings: { grid: "retired", scenario: "extreme", pueScenario: "none", inputSensitivity: "other" }, period: output, wh: 400, co2: 161, shown: "161 g" },
    { settings: { grid: "world" }, period: output, wh: 400, co2: 210.68, shown: "211 g" },
    { settings: custom100, period: output, wh: 400, co2: 46, shown: "46 g" },
    { settings: { ...custom100, pueScenario: "high" }, period: output, wh: 400, co2: 52, shown: "52 g" },
    // The estimate switch changes tier energy (1,200 Wh) while PUE stays high.
    { settings: { ...custom100, pueScenario: "high", scenario: "high" }, period: output, wh: 1200, co2: 156, shown: "156 g" },
    // Input sensitivity leaves output tokens alone.
    { settings: { ...custom100, pueScenario: "high", scenario: "high", inputSensitivity: "third" }, period: output, wh: 1200, co2: 156, shown: "156 g" },
    // 1M cache reads × 1,200 Wh × 0.08 × 5/3 × 1.3 × 100 g/kWh.
    { settings: { ...custom100, pueScenario: "high", scenario: "high", inputSensitivity: "third" }, period: cacheReads, wh: 160, co2: 20.8, shown: "20.8 g" },
  ];
  for (const { settings, period, wh, co2, shown } of cases) {
    const label = JSON.stringify(settings), pharosCarbon = carbon(settings);
    const result = pharosCarbon.estimate(factors, period);
    close(result.wh, wh, `${label} server Wh`);
    close(result.co2, co2, `${label} CO₂e`);
    close(result.grams(result.wh), result.co2, `${label} grams()`);
    assert.equal(pharosCarbon.mass(result.co2), shown, label);
  }

  const central = carbon().estimate(factors, output);
  close(central.facilityWh, 460, "facility Wh");
  assert.equal(carbon().energy(central.facilityWh), "460 Wh");
  assert.equal(central.byCategory.output.tokens, 1e6);
  close(central.byCategory.output.outputEquivalent, 1e6, "output is its own reference");
  close(central.byTier.medium.wh, 400, "mid-size tier Wh");

  const reads = carbon({ ...custom100, pueScenario: "high", scenario: "high", inputSensitivity: "third" }).estimate(factors, cacheReads).byCategory.cache_read;
  assert.equal(reads.tokens, 1e6);
  close(reads.outputEquivalent / reads.tokens, 0.08 * 5 / 3, "Watershed cache-read ratio");
  close(reads.wh, 160, "cache-read Wh");
});

test("mixed tiers and token types sum per-tier contributions", () => {
  const period = { tokens: { small: { output: 1e6 }, medium: { uncached_input: 1e6, cache_write: 2e6, cache_read: 20e6, output: 5e5 }, large: { output: 2.5e5 } } };
  const watershed = { inputSensitivity: "third" };
  const cases = [
    // Central: 100 + (80 + 160 + 160 + 200) + 225 Wh.
    { settings: {}, wh: 925, outputEquivalent: 2.75e6, co2: 372.3125, shown: "372 g", cacheReadWh: 160, cacheReadGrams: 64.4 },
    // Watershed scales every non-output ratio by 5/3.
    { settings: watershed, wh: 3575 / 3, outputEquivalent: 10.25e6 / 3, co2: 3575 / 3 * 1.15 * 0.35, shown: "480 g", cacheReadWh: 800 / 3 },
    { settings: { ...watershed, pueScenario: "high" }, wh: 3575 / 3, co2: 3575 / 3 * 1.3 * 0.35, shown: "542 g" },
    { settings: { ...watershed, pueScenario: "high", ...custom100 }, wh: 3575 / 3, co2: 3575 / 3 * 1.3 * 0.1, shown: "155 g" },
    // High: 300 + (800 + 1,600 + 3,200 + 600) + 625 Wh.
    { settings: { ...watershed, pueScenario: "high", ...custom100, scenario: "high" }, wh: 7125, co2: 926.25, shown: "926 g", cacheReadWh: 3200, cacheReadGrams: 416 },
  ];
  for (const { settings, wh, outputEquivalent, co2, shown, cacheReadWh, cacheReadGrams } of cases) {
    const label = JSON.stringify(settings), pharosCarbon = carbon(settings);
    const result = pharosCarbon.estimate(factors, period);
    const tokens = Object.values(result.byCategory).reduce((sum, category) => sum + category.tokens, 0);
    assert.equal(tokens, 24.75e6, `${label} reported tokens`);
    close(result.wh, wh, `${label} server Wh`);
    if (outputEquivalent !== undefined) close(result.outputEquivalent, outputEquivalent, `${label} output-equivalent tokens`);
    close(result.co2, co2, `${label} CO₂e`);
    assert.equal(pharosCarbon.mass(result.co2), shown, label);
    if (cacheReadWh !== undefined) close(result.byCategory.cache_read.wh, cacheReadWh, `${label} cache-read Wh`);
    if (cacheReadGrams !== undefined) close(result.grams(result.byCategory.cache_read.wh), cacheReadGrams, `${label} cache-read CO₂e`);
  }

  const central = carbon().estimate(factors, period);
  assert.equal(carbon().energy(central.facilityWh), "1.06 kWh");
  close(central.byCategory.cache_read.outputEquivalent, 400e3, "cache-read output-equivalent tokens");
  close(central.byCategory.cache_read.wh * central.pue, 184, "cache-read facility Wh");
  assert.deepEqual(Object.fromEntries(Object.entries(central.byTier).map(([tier, row]) => [tier, [row.tokens, Math.round(row.wh * 1e9) / 1e9]])),
    { small: [1e6, 100], medium: [23.5e6, 600], large: [2.5e5, 225] });

  // A real library's count of cache reads keeps its precise token count.
  const large = { tokens: { ...period.tokens, medium: { ...period.tokens.medium, cache_read: 174650085702 } } };
  const result = carbon().estimate(factors, large);
  assert.equal(result.byCategory.cache_read.tokens, 174650085702);
  assert.equal(carbon().mass(result.co2), "563 kg");
});

test("mass and energy step their unit and keep three significant figures", () => {
  const { mass, energy } = carbon();
  assert.deepEqual([0, 0.0123, 20.84, 999.4, 1234, 562681.19, 1.5e6].map(mass), ["0 g", "0.012 g", "20.8 g", "999 g", "1.23 kg", "563 kg", "1.5 t"]);
  assert.deepEqual([0, 460, 1063.75, 2.5e6].map(energy), ["0 Wh", "460 Wh", "1.06 kWh", "2.5 MWh"]);
});

// The estimates the service must reproduce too (TestCarbonSharedEstimates),
// and the Carbon dashboard's token-type split built on this calculator.
test("the calculator and the dashboard's token-type split reproduce the shared estimates", () => {
  for (const { name, settings, facility_wh, co2e_g, co2e_g_by_category } of shared.cases) {
    const pharosCarbon = carbon(settings), result = pharosCarbon.estimate(factors, { tokens: shared.tokens });
    close(result.facilityWh, facility_wh, `${name} facility Wh`);
    close(result.co2, co2e_g, `${name} CO₂e`);
    const totals = Object.entries(shared.tokens).map(([tier, counts]) => ({ keys: [tier], values: { uncached_input_tokens: counts.uncached_input, cache_read_input_tokens: counts.cache_read, cache_creation_input_tokens: counts.cache_write, output_tokens: counts.output } }));
    const parts = deriveCarbon({ settings: pharosCarbon.settingsSnapshot(factors), factors, estimate: pharosCarbon.estimate }, totals, "co2");
    for (const [category, grams] of Object.entries(co2e_g_by_category)) close(parts[category], grams, `${name} ${category}`);
  }
});

test("each dashboard card estimates its own Carbon assumptions without changing the calculator", () => {
  const calculator = carbon({scenario:"low",grid:"world"});
  const period = counts("medium","cache_read",1e6);
  const before = calculator.estimate(factors,period);
  const settings = {scenario:"high",pueScenario:"high",inputSensitivity:"third",grid:"custom",customGrid:100};
  close(calculator.estimate(factors,period,settings).co2,20.8,"card assumptions");
  close(calculator.estimate(factors,period).co2,before.co2,"calculator assumptions stay intact");
  assert.equal(calculator.settingsSnapshot(factors).scenario,"low");
});
