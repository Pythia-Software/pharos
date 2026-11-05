import test from "node:test";
import assert from "node:assert/strict";
import { fixesBoundsPreview, fixesDashboard, fixesFacts, fixesHiddenNote, fixesHiddenReason, fixesKindsPresent, fixesLeadQuote, fixesPlaces, fixesRanked } from "../../../src/fixes-model";
import { optimizeSelectionPlan } from "../../../src/optimize-model";
import { decodeQueryStrict } from "@pythia-software/query-table-core";
import { fixDetailURL, fixesHiddenTableURL, fixesTableFocus } from "../../../src/fixes-route";
import { safetySelectionPlan } from "../../../src/safety-dashboard-model";

const status = (patch = {}) => ({ state: "recurring", label: "Still recurring", detail: "", first: "2026-10-20", last: "2026-11-01", days_quiet: 1, ...patch });
const item = (id, patch = {}) => ({
  id, title: id, category: "env", category_label: "Environment or setup", target: "repo:a", target_label: "Alpha", scope: "repo",
  work_items: 4, events: 6, repositories: [{ key: "Alpha", count: 6 }], status: status(), hours: 0, priority: 4,
  tentative: false, state: "open", hidden: "", deprioritized: false, ...patch,
});
const bounds = { quiet_days: 7, hide_days: 30, min_work_items: 3 };
const items = [
  item("busy", { work_items: 9, priority: 9 }),
  item("costly", { hours: 3, priority: 8, status: status({ last: "2026-10-25", days_quiet: 7 }), target: "global", target_label: "Global setup", scope: "global", repositories: [{ key: "Alpha", count: 3 }, { key: "Beta", count: 2 }, { key: "No repository", count: 1 }] }),
  item("stale", { hidden: "stale", status: status({ state: "quiet", days_quiet: 40 }) }),
  item("rare", { hidden: "rare", work_items: 2, priority: 2 }),
  item("fact", { category: "knowledge", tentative: true, priority: 3 }),
  item("handed", { state: "handed_off", hidden: "rare", work_items: 2 }),
];

test("Open leaves out what the bounds hide, counts it, and shows it on request", () => {
  const before = structuredClone(items);
  const open = fixesDashboard(items, { list: "open", place: "", kind: "", rank: "priority", showHidden: false });
  assert.deepEqual(open.main.map(entry => entry.id), ["busy", "costly"]);
  assert.deepEqual(open.maybe.map(entry => entry.id), ["fact"]);
  assert.deepEqual(open.hidden, { stale: 1, rare: 1, intended: 0, total: 2 });
  // Guidance an agent cites on purpose is left out whatever the bounds.
  const ruled = fixesDashboard([...items, item("rule", { hidden: "intended", intended: true, work_items: 9, priority: 9 })], { list: "open", place: "", kind: "", rank: "priority", showHidden: false });
  assert.deepEqual(ruled.main.map(entry => entry.id), ["busy", "costly"]);
  assert.deepEqual(ruled.hidden, { stale: 1, rare: 1, intended: 1, total: 3 });
  assert.match(fixesHiddenNote(ruled.hidden, { quiet_days: 7, hide_days: 30, min_work_items: 3 }), /Also left out: 1 problem the guidance causes on purpose\.$/);
  assert.deepEqual(open.counts, { open: 3, handed_off: 1, dismissed: 0 });
  const all = fixesDashboard(items, { list: "open", place: "", kind: "", rank: "priority", showHidden: true });
  assert.deepEqual(all.main.map(entry => entry.id), ["busy", "costly", "stale", "rare"]);
  // Bounds never hide what you handed off or closed.
  const handed = fixesDashboard(items, { list: "handed_off", place: "", kind: "", rank: "priority", showHidden: false });
  assert.deepEqual(handed.main.map(entry => entry.id), ["handed"]);
  assert.equal(handed.hidden.total, 0);
  assert.deepEqual(items, before);
});

test("place and kind filters narrow the lists and their counts", () => {
  const global = fixesDashboard(items, { list: "open", place: "global", kind: "", rank: "priority", showHidden: false });
  assert.deepEqual(global.main.map(entry => entry.id), ["costly"]);
  assert.deepEqual(global.counts, { open: 1, handed_off: 0, dismissed: 0 });
  const knowledge = fixesDashboard(items, { list: "open", place: "", kind: "knowledge", rank: "priority", showHidden: false });
  assert.deepEqual([...knowledge.main, ...knowledge.maybe].map(entry => entry.id), ["fact"]);
  assert.deepEqual(fixesPlaces(items).map(place => place.key), ["global", "repo:a"], "the shared setup comes first");
  assert.deepEqual(fixesKindsPresent(items).map(kind => [kind.key, kind.count]), [["env", 5], ["knowledge", 1]]);
});

test("ranks order by their measure, then by priority", () => {
  assert.deepEqual(fixesRanked(items.slice(0, 2), "time").map(entry => entry.id), ["costly", "busy"]);
  assert.deepEqual(fixesRanked(items.slice(0, 2), "recent").map(entry => entry.id), ["busy", "costly"]);
  assert.deepEqual(fixesRanked(items.slice(0, 2), "spread").map(entry => entry.id), ["costly", "busy"], "No repository does not count as one");
  assert.deepEqual(fixesRanked(items.slice(0, 2), "work").map(entry => entry.id), ["busy", "costly"]);
});

test("cards, notes and the settings preview describe the bounds", () => {
  assert.match(fixesFacts(items[1]).join(" · "), /^6 events · 2 repositories · .+ · 3 h of agent time$/);
  assert.equal(fixesFacts(items[0])[1], "Alpha");
  assert.equal(fixesHiddenNote({ stale: 1, rare: 0, total: 1 }, bounds), "Your settings hide 1 problem not seen in the last 30 days.");
  assert.equal(fixesHiddenNote({ stale: 2, rare: 3, total: 5 }, bounds), "Your settings hide 2 problems not seen in the last 30 days and 3 problems seen in fewer than 3 work items.");
  assert.equal(fixesHiddenNote({ stale: 0, rare: 0, total: 0 }, bounds), "");
  assert.match(fixesHiddenReason(items[2], bounds), /40 days.*30 days/);
  assert.match(fixesHiddenReason(items[3], bounds), /2 work items.*fewer than 3/);
  // The preview mirrors the server's rule: stale before rare; Open only.
  assert.deepEqual(fixesBoundsPreview(items, bounds), { stale: 1, rare: 1, shown: 3 });
  assert.deepEqual(fixesBoundsPreview(items, { ...bounds, hide_days: 0 }), { stale: 0, rare: 1, shown: 4 });
  assert.deepEqual(fixesBoundsPreview(items, { ...bounds, min_work_items: 5 }), { stale: 1, rare: 3, shown: 1 });
  assert.deepEqual(fixesBoundsPreview([item("unknown", { status: status({ state: "unknown", days_quiet: 99 }) })], bounds), { stale: 0, rare: 0, shown: 1 }, "an undated problem is never stale");
});

test("Risky actions and Fixes share one selection plan", () => {
  const plan = optimizeSelectionPlan(items, ["busy", "costly", "handed"], { one: "fix", many: "fixes" });
  assert.deepEqual(plan.groups.map(group => [group.target, group.ids]), [["repo:a", ["busy", "handed"]], ["global", ["costly"]]]);
  assert.match(plan.blocked, /^1 of the selected fixes is not open/);
  assert.deepEqual(safetySelectionPlan(items, ["busy", "costly"]).groups, optimizeSelectionPlan(items, ["busy", "costly"]).groups);
});

test("the table lists what the bounds hide, and a fix opens from it with the query kept", () => {
  const url = new URL(fixesHiddenTableURL(), "https://pharos.test");
  assert.equal(url.searchParams.get("optimize_area"), "fixes");
  assert.equal(url.searchParams.get("view"), "table");
  const query = decodeQueryStrict(url.searchParams.get("q_fixes"));
  assert.deepEqual(fixesTableFocus(query), { shown: "hidden", state: "open" });
  assert.deepEqual(fixesTableFocus({ ...query, where: [] }), { shown: undefined, state: undefined });
  const detail = new URL(fixDetailURL("fix_a", url), "https://pharos.test");
  assert.equal(detail.searchParams.get("fixes_item"), "fix_a");
  assert.equal(detail.searchParams.get("q_fixes"), url.searchParams.get("q_fixes"));
});

test("a lead is quoted only when it says more than the title", () => {
  assert.equal(fixesLeadQuote({ title: "I can't measure the live catalog", lead: "I can't measure the live catalog." }), "");
  assert.equal(fixesLeadQuote({ title: "`uv` missing from app's environment", lead: "`uv` is not installed here." }), "`uv` is not installed here.");
  assert.equal(fixesLeadQuote({ title: "Something" }), "");
  assert.equal(fixesLeadQuote({ title: "`npm test` could not run", lead: "Test status: `npm test` could not run." }), "");
});
