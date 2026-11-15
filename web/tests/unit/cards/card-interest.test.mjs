import assert from "node:assert/strict";
import test from "node:test";
import { execution, schemas } from "../helpers/recipes.mjs";
import { queryCapabilities, serverQuery } from "../../support/query-server.mjs";
import { queryRecipes, instantiateRecipe, loadQueryPreview, clearPreviewCache } from "../../../src/curiosity-data";
import { cardInterest, cardInterestOptions, catalogSparseNote, showsCard, MIN_COVER_RECORDS } from "../../../src/card-interest";

const bucket = (value, count = 10, keys = []) => ({ keys, value, count });
function preview(clause, buckets, extra = {}, result = {}) {
  return { state: "ready", total: 0, summary: "", coverMetricId: clause.id, metrics: [{ clause: { groupBy: [], ...clause }, result: { id: clause.id, buckets, ...result } }], ...extra };
}
const level = (...args) => cardInterest(...args).level;

test("covers with nothing to show are empty, and unjudged covers stay", () => {
  assert.equal(level({ state: "empty", total: 0, summary: "", emptyLabel: "No subagents" }), "empty");
  assert.equal(cardInterest({ state: "empty", total: 0, summary: "", emptyLabel: "No subagents" }).reason, "No subagents");
  assert.equal(level(preview({ id: "m", op: "sum", field: "x" }, [bucket(0), bucket(0)])), "empty", "a row of zeros says nothing");
  assert.equal(level(preview({ id: "m", op: "avg", field: "x", groupBy: ["model"] }, [bucket(0, 5, ["a"]), bucket(0, 5, ["b"])])), "empty");
  // Loading, failing, rebuilding and cover-less previews are never hidden.
  assert.equal(level(undefined), "interesting");
  assert.equal(level({ state: "empty", total: 0, summary: "" }, { error: "boom" }), "interesting");
  assert.equal(level({ state: "empty", total: 0, summary: "", incompleteReason: "Session statistics are updating" }), "interesting");
  assert.equal(level({ state: "ready", total: 4, summary: "", study: {} }), "interesting");
  assert.equal(level(preview({ id: "m", op: "sum", field: "x" }, [], {}, { error: "bad" })), "interesting");
});

test("a handful of records is sparse; a distribution needs a spread", () => {
  assert.equal(level(preview({ id: "m", op: "count" }, [bucket(MIN_COVER_RECORDS - 1, MIN_COVER_RECORDS - 1)])), "sparse");
  assert.match(cardInterest(preview({ id: "m", op: "count" }, [bucket(1, 1)])).reason, /Only 1 record/);
  assert.equal(level(preview({ id: "m", op: "count" }, [bucket(MIN_COVER_RECORDS, MIN_COVER_RECORDS)])), "interesting");
  const box = { id: "m", op: "avg", field: "x", groupBy: [], distribution: { kind: "box" } };
  const spread = (min, max, n = 40) => ({ kind: "box", summary: { n, min, q1: min, median: min, q3: max, max, mean: min, low: min, high: max, outliers: [], outlierCount: 0, whiskers: "tukey", method: "exact-linear" } });
  assert.equal(level(preview(box, [{ ...bucket(3, 40), distribution: spread(1, 90) }])), "interesting");
  assert.equal(level(preview(box, [{ ...bucket(0, 40), distribution: spread(0, 0) }])), "sparse", "every conversation delegated nothing");
  assert.equal(level(preview({ ...box, distribution: { kind: "histogram" } }, [{ ...bucket(40, 40), distribution: { kind: "histogram", edges: [0, 1, 2], counts: [40, 0], n: 40 } }])), "sparse");
  assert.equal(level(preview({ ...box, distribution: { kind: "histogram" } }, [{ ...bucket(40, 40), distribution: { kind: "histogram", edges: [0, 1, 2], counts: [30, 10], n: 40 } }])), "interesting");
  assert.equal(level(preview(box, [{ ...bucket(5, 2), distribution: spread(1, 9, 2) }])), "sparse");
  // Five cards splitting the same box by project, model or effort are one box
  // in a library without them.
  assert.equal(level(preview({ ...box, groupBy: ["root_effort"] }, [{ ...bucket(3, 96, [null]), distribution: spread(1, 90) }])), "sparse");
  assert.equal(level(preview({ ...box, groupBy: ["root_effort"] }, [{ ...bucket(3, 50, ["high"]), distribution: spread(1, 90) }, { ...bucket(3, 46, ["low"]), distribution: spread(1, 9) }])), "interesting");
  // A grouped card's counts are groups: two tools with many calls each.
  assert.equal(level(preview({ id: "m", op: "sum", field: "calls", groupBy: ["tool_name"] }, [bucket(40, 1, ["Bash"]), bucket(30, 1, ["Edit"])]), { grouped: true }), "interesting");
  assert.equal(level(preview({ id: "m", op: "max", field: "share", groupBy: ["project_name"] }, [bucket(0.8, 1, [null])]), { grouped: true }), "sparse", "a library without projects");
});

test("a breakdown where one group holds everything is one bar; a timeline is not", () => {
  const byModel = { id: "m", op: "sum", field: "cost", groupBy: ["effort"] };
  assert.equal(level(preview(byModel, [bucket(120, 300, [null])])), "sparse");
  assert.equal(level(preview(byModel, [bucket(995, 300, ["none"]), bucket(5, 3, ["high"])])), "sparse");
  assert.equal(level(preview(byModel, [bucket(90, 300, ["none"]), bucket(10, 30, ["high"])])), "interesting");
  // Rates across groups and partial or minCount-trimmed breakdowns are not one bar.
  assert.equal(level(preview({ ...byModel, op: "avg" }, [bucket(4, 300, ["none"]), bucket(0.1, 3, ["high"])])), "interesting");
  assert.equal(level(preview({ ...byModel, op: "avg" }, [bucket(4, 300, ["none"])])), "sparse");
  assert.equal(level(preview(byModel, [bucket(120, 300, ["none"])], {}, { coverage: "partial" })), "interesting");
  assert.equal(level(preview(byModel, [bucket(120, 300, ["none"])], {}, { hiddenGroups: 2 })), "interesting");
  assert.equal(level(preview({ id: "m", op: "count", groupBy: ["day"] }, [bucket(40, 40, ["2026-11-01"]), bucket(0, 0, ["2026-11-02"])])), "interesting");
  assert.equal(level(preview({ id: "m", op: "sum", field: "tokens", groupBy: ["day", "model"] }, [bucket(40, 40, ["2026-11-01", "a"]), bucket(30, 9, ["2026-11-02", "a"])])), "interesting", "one model's timeline");
  assert.equal(level(preview({ id: "m", op: "count", groupBy: ["started_at"], dateGrouping: { interval: "day" } }, [bucket(40, 40, [1])])), "interesting");
  assert.equal(showsCard(preview(byModel, [bucket(120, 300, [null])])), false);
});

test("a card whose empty cover says how to turn its data on stays", () => {
  const setup = queryRecipes.filter(recipe => !recipe.retired && recipe.emptyIsSetup);
  assert.ok(setup.some(recipe => recipe.id === "agent-events-repository-friction") && setup.some(recipe => recipe.id === "digest-friction-weeks"));
  for (const recipe of queryRecipes.filter(recipe => /Turn on .* in Settings/.test(recipe.emptyLabel ?? ""))) assert.equal(recipe.emptyIsSetup, true, `${recipe.id} names a setting to turn on`);
  const empty = { state: "empty", total: 0, summary: "", emptyLabel: setup[0].emptyLabel };
  assert.equal(showsCard(empty, cardInterestOptions(setup[0])), true);
  assert.equal(showsCard(empty, cardInterestOptions({})), false);
  // Once it has data, it is judged like any other card.
  assert.equal(showsCard(preview({ id: "m", op: "sum", field: "x" }, [bucket(0)]), cardInterestOptions(setup[0])), false);
});

test("the library notes left-out cards only when some, not all, of those in view are", () => {
  assert.equal(catalogSparseNote(0, 12, false), undefined);
  assert.equal(catalogSparseNote(0, 12, true), undefined, "showing all with nothing to leave out says nothing");
  assert.equal(catalogSparseNote(4, 0, false), undefined, "an all-hidden tab has its own message");
  assert.match(catalogSparseNote(4, 8, false), /^4 cards have little or no data/);
  assert.match(catalogSparseNote(1, 12, true), /^1 card has .* and is shown anyway\.$/);
  assert.match(catalogSparseNote(4, 12, true), /and are shown anyway\.$/);
});

// A real catalog card, through the preview pipeline, over three libraries.
function server(t, rows) {
  clearPreviewCache();
  const priorFetch = global.fetch, priorLocation = global.location;
  global.location = { origin: "http://interest.test" };
  global.fetch = async (url, init = {}) => {
    const path = String(url), request = init.body ? JSON.parse(init.body) : undefined;
    const json = body => new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } });
    if (path.includes("computed-columns")) return json([]);
    if (path.endsWith("capabilities")) return json({ metricCapabilities: { version: 2, expressions: true, distributions: true }, computedExecution: execution, queryCapabilities });
    return json(serverQuery(request, rows, schemas.tool_calls));
  };
  t.after(() => { global.fetch = priorFetch; global.location = priorLocation; clearPreviewCache(); });
}
const call = (i, tool, errors) => ({ id: String(i), call_count: 1, error_count: errors, tool_name: tool, command_name: "", duration_ms: 100, result_tokens: 0, result_tokens_source: "measured", project_name: "p", conversation_id: String(i), started_at: Date.now() - 1000 });
async function failures(t, rows) {
  server(t, rows);
  const recipe = queryRecipes.find(recipe => recipe.id === "tool-failures"), item = instantiateRecipe(recipe, 7);
  return cardInterest(await loadQueryPreview(item.dataset, item.query, schemas.tool_calls, { recipe: item, window: item.window }), cardInterestOptions(item)).level;
}

test("the tool failures card fits only a library with varied failures", async t => {
  await t.test("a chat library has no tool calls", async t => assert.equal(await failures(t, []), "empty"));
  await t.test("one failing tool is one bar", async t => assert.equal(await failures(t, [call(1, "Bash", 1), call(2, "Bash", 1), call(3, "Read", 0)]), "sparse"));
  await t.test("one tool holding nearly every failure is one bar", async t => assert.equal(await failures(t, [...Array.from({ length: 200 }, (_, i) => call(i, "Bash", 1)), call(200, "Edit", 1)]), "sparse"));
  await t.test("failures across tools are interesting", async t => assert.equal(await failures(t, Array.from({ length: 8 }, (_, i) => call(i, i % 2 ? "Bash" : "Edit", 1))), "interesting"));
});
