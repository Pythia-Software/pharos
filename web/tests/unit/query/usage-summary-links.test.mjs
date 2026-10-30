import test from "node:test";
import assert from "node:assert/strict";
import { EMPTY_QUERY, decodeQueryStrict, loadSchema, toQueryRequestV3 } from "@pythia-software/query-table-core";
import { usageSummaryURL } from "../../../src/usage-summary-links";
import { readUsageWorkspace } from "../../../src/usage-workspace-model";
import { readDashboardQuery } from "../../../src/usage-dashboard-model";
import { timeBounds, usageTime, writingTime } from "../../../src/usage-chart";
import { defaultConcurrencySettings, normalizeConcurrencySettings } from "../../../src/concurrency-settings";
import usage from "../../../../schemas/usage.schema.json";
import writingMessages from "../../../../schemas/writing_messages.schema.json";

const generatedAt = "2026-11-01T08:30:12.345Z"; // Crosses the US daylight saving change.
const windows = [["1h", 1], ["6h", 6], ["24h", 24], ["7d", 168], ["30d", 720], ["all", 0]];
const urlFor = (...args) => new URL(usageSummaryURL(...args), "http://pharos.test");

test("summary values open their dashboard with the snapshot's exact elapsed duration", () => {
  for (const [metric, topic] of [["human_words", "human"], ["tokens", "tokens"], ["cost_usd", "tokens"], ["co2e_g", "carbon"]]) {
    for (const [window, hours] of windows) {
      const url = urlFor(metric, window, generatedAt), params = url.searchParams;
      const state = readUsageWorkspace(params), human = metric === "human_words";
      assert.equal(url.pathname, "/analyze");
      assert.equal(state.topic, topic);
      assert.equal(state.mode, "dashboards");
      assert.equal(state.dataset, human ? "writing_messages" : "usage");
      const restored = readDashboardQuery(params, { ...EMPTY_QUERY, where: [{ field: human ? "sent_at" : "last_usage_at", op: ">=", value: "-30d" }] });
      assert.equal(restored.error, "");
      assert.deepEqual(restored.query, decodeQueryStrict(params.get("analyze_filters")));
      const bounds = timeBounds(restored.query.where, human ? { first: "sent_at", last: "sent_at" } : usageTime);
      if (hours) {
        assert.equal(bounds.to.toISOString(), generatedAt);
        assert.equal(bounds.from.getTime(), Date.parse(generatedAt) - hours * 3600000);
      } else {
        assert.deepEqual(restored.query.where, [], "All time explicitly clears saved/default dates");
        assert.equal(bounds.from, undefined);
        assert.equal(bounds.to, undefined);
      }
      const chart = JSON.parse(params.get("analyze_chart"));
      assert.equal(chart.period, hours && hours <= 24 ? "hour" : "day");
      if (metric === "cost_usd") assert.equal(chart.metric, "cost");
      if (metric === "tokens") assert.equal(chart.metric, "tokens");
      const schema = loadSchema(human ? writingMessages : usage);
      assert.deepEqual(toQueryRequestV3(restored.query, schema, { version: 3, profile: "qt-sqlite-v1" }).diagnostics ?? [], []);
    }
  }
});

test("parallelism summary links retain counting assumptions and replace saved selections and dates", () => {
  const saved = { ...defaultConcurrencySettings, counting: "agent", gap: 15, repository: "pharos", mode: "model", range: "custom", startDay: "2026-01-01", endDay: "2026-01-03", workspace: "selected", agent: "selected", zoom: [1, 2] };
  const before = structuredClone(saved);
  for (const [window, hours] of windows) {
    const params = urlFor("parallelism", window, generatedAt, saved).searchParams;
    assert.equal(readUsageWorkspace(params).topic, "parallelism");
    const settings = normalizeConcurrencySettings(JSON.parse(params.get("analyze_parallelism")));
    assert.equal(settings.counting, "agent");
    assert.equal(settings.gap, 15);
    assert.equal(settings.repository, "pharos");
    assert.equal(settings.mode, "user", "Header summary measures threaded activity");
    assert.equal(settings.range, "all");
    assert.equal(settings.workspace, "");
    assert.equal(settings.agent, "");
    assert.equal(settings.startDay, "");
    assert.equal(settings.endDay, "");
    assert.deepEqual(settings.zoom, hours ? [Date.parse(generatedAt) - hours * 3600000, Date.parse(generatedAt)] : null);
  }
  assert.deepEqual(saved, before);
});

test("summary links reject unknown durations and invalid snapshot times", () => {
  for (const window of ["", "2h", "constructor"]) assert.equal(usageSummaryURL("tokens", window, generatedAt), undefined);
  assert.equal(usageSummaryURL("tokens", "1h", "invalid"), undefined);
  assert.ok(usageSummaryURL("tokens", "all", "invalid"));
});


test("Today summary links use the service's midnight and freeze the displayed snapshot", () => {
  const from = "2026-11-01T06:00:00.000Z";
  for (const metric of ["human_words", "tokens", "cost_usd", "co2e_g", "parallelism"]) {
    const params = urlFor(metric, "today", generatedAt, defaultConcurrencySettings, from).searchParams;
    assert.equal(params.has("analyze_window"), false);
    if (metric === "parallelism") assert.deepEqual(JSON.parse(params.get("analyze_parallelism")).zoom, [Date.parse(from), Date.parse(generatedAt)]);
    else {
      const where = decodeQueryStrict(params.get("analyze_filters")).where;
      assert.equal(where.find(term => term.op === ">=").value, from);
      assert.equal(where.find(term => term.op === "<").value, generatedAt);
    }
  }
  assert.equal(usageSummaryURL("tokens", "today", "invalid"), undefined);
});
