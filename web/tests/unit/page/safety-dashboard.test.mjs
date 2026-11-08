import test from "node:test";
import assert from "node:assert/strict";
import { EMPTY_QUERY, decodeQueryStrict, loadSchema } from "@pythia-software/query-table-core";
import { safetyResourceName, safetyShortName, safetyCommonDirectory, safetyDashboardPlaces, safetyDashboardChecks, safetySelectionPlan, safetyCartChecks } from "../../../src/safety-dashboard-model";
import { safetyCheckTableURL, safetyTableFocus } from "../../../src/safety-route";
import fixesDocument from "../../../../schemas/fixes.schema.json";
import safetyDocument from "../../../../schemas/safety.schema.json";
import { optimizeTableTitle, optimizeTableRemainder, optimizeTableSchemas } from "../../../src/optimize-table-title";

const notice = (id, patch = {}) => ({ id, state: "open", target: "repo:a", target_label: "Alpha", calls: 1, material_change: false, last_seen_at: "2026-11-01T12:00:00Z", ...patch });
const cases = [
  notice("old-evidence", { last_seen_at: "2026-10-31T12:00:00Z", material_change: true }),
  notice("frequent", { target: "machine:b", target_label: "Mac", calls: 8 }),
  notice("recent", { last_seen_at: "2026-11-01T10:00:00-03:00", calls: 3 }),
  notice("handed", { state: "handed_off", calls: 99 }),
];

// Three checks, one notice each unless noted: credentials is dangerous but
// old, process is routine but recent and busy, deletion has new evidence.
const check = (id, rule, danger, patch = {}) => notice(id, { rule, title: rule, danger, ...patch });
const checked = [
  check("c1", "credentials", "high", { last_seen_at: "2026-10-20T00:00:00Z" }),
  check("p1", "process", "low", { last_seen_at: "2026-11-02T00:00:00Z", calls: 9 }),
  check("p2", "process", "low", { state: "dismissed" }),
  check("d1", "boundary-delete", "medium", { material_change: true, target: "machine:b" }),
];

test("check cards follow the shown list and place without changing the notices", () => {
  const before = structuredClone(checked);
  assert.deepEqual(safetyDashboardChecks(checked, "open", "repo:a").map(item => item.rule), ["credentials", "process"]);
  assert.deepEqual(safetyDashboardChecks(checked, "dismissed", "").map(item => [item.rule, item.ids]), [["process", ["p2"]]]);
  assert.deepEqual(safetyDashboardChecks(checked, "handed_off", ""), []);
  assert.deepEqual(checked, before);
});

test("check cards rank by danger, recent activity, calls, or new evidence", () => {
  const order = rank => safetyDashboardChecks(checked, "open", "", rank).map(item => item.rule);
  assert.deepEqual(order("danger"), ["credentials", "boundary-delete", "process"]);
  assert.deepEqual(order("recent"), ["process", "boundary-delete", "credentials"]);
  assert.deepEqual(order("calls"), ["process", "credentials", "boundary-delete"], "equal calls fall back to danger");
  assert.deepEqual(order("evidence"), ["boundary-delete", "credentials", "process"]);
});

test("a check is Recent when a notice was seen in the last three days", () => {
  const now = Date.parse("2026-11-03T00:00:00Z");
  const recent = Object.fromEntries(safetyDashboardChecks(checked, "open", "", "danger", now).map(item => [item.rule, item.recent]));
  assert.deepEqual(recent, { credentials: false, process: true, "boundary-delete": true });
});

test("danger ranks high checks first", () => {
  const checks = safetyDashboardChecks([
    notice("p1", { rule: "process", title: "Every instance of a program stopped", danger: "low" }), notice("p2", { rule: "process", title: "Every instance of a program stopped", danger: "low" }), notice("p3", { rule: "process", title: "Every instance of a program stopped", danger: "low" }),
    notice("c1", { rule: "credentials", title: "Credentials copied to another location", danger: "high" }),
    notice("d1", { rule: "boundary-delete", title: "Deletion outside the checkout", danger: "medium" }),
  ], "open", "");
  assert.deepEqual(checks.map(check => [check.rule, check.danger]), [["credentials", "high"], ["boundary-delete", "medium"], ["process", "low"]], "a rare dangerous check outranks a common routine one");
});

test("place filter counts notices across lifecycle lists like Efficiency", () => {
  assert.deepEqual(safetyDashboardPlaces(cases), [{ key: "repo:a", label: "Alpha", count: 3 }, { key: "machine:b", label: "Mac", count: 1 }]);
  assert.deepEqual(safetyDashboardPlaces([]), []);
});

test("checks summarize the shown list and place, and link to that check's resources sorted by path", () => {
  const checked = [
    notice("a", { rule: "permissions", title: "Permissions broadened" }),
    notice("b", { rule: "boundary-delete", title: "Deletion outside the checkout" }),
    notice("c", { rule: "boundary-delete", title: "Deletion outside the checkout", target: "machine:b" }),
    notice("d", { rule: "boundary-delete", title: "Deletion outside the checkout", state: "dismissed" }),
  ];
  assert.deepEqual(safetyDashboardChecks(checked, "open", "").map(({ rule, count, locations }) => ({ rule, count, locations })), [{ rule: "boundary-delete", count: 2, locations: 2 }, { rule: "permissions", count: 1, locations: 1 }]);
  assert.deepEqual(safetyDashboardChecks(checked, "open", "repo:a").map(check => [check.rule, check.count]), [["boundary-delete", 1], ["permissions", 1]]);
  const url = new URL(safetyCheckTableURL("boundary-delete", { state: "open", target: "repo:a" }), "http://localhost");
  assert.equal(url.pathname, "/findings");
  assert.equal(url.searchParams.get("optimize_area"), "safety");
  assert.equal(url.searchParams.get("view"), "table");
  const query = decodeQueryStrict(url.searchParams.get("q_safety"));
  assert.deepEqual(query.where, [{ field: "rule", op: "=", value: "boundary-delete" }, { field: "state", op: "=", value: "open" }, { field: "target", op: "=", value: "repo:a" }]);
  assert.deepEqual(query.orderBy, [{ field: "subject", dir: "asc" }]);
  assert.deepEqual(query.select.slice(0, 2).map(column => column.field), ["subject", "command"]);
  assert.ok(!query.select.some(column => column.field === "state" || column.field === "target_label"), "a fixed state or location is stated once, not repeated per row");
  assert.deepEqual(JSON.parse(url.searchParams.get("query_layout")), { safety: true });
  assert.deepEqual(safetyTableFocus(query), { rule: "boundary-delete", state: "open", target: "repo:a" });
  const everywhere = decodeQueryStrict(new URL(safetyCheckTableURL("permissions"), "http://localhost").searchParams.get("q_safety"));
  assert.ok(["state", "target_label"].every(field => everywhere.select.some(column => column.field === field)));
  assert.deepEqual(safetyTableFocus(everywhere), { rule: "permissions" });
  assert.deepEqual(decodeQueryStrict(new URL(safetyCheckTableURL("permissions"), "http://localhost").searchParams.get("q_safety")).where, [{ field: "rule", op: "=", value: "permissions" }]);
});

test("a table is focused on a check only by one plain equality on it", () => {
  const query = where => ({ where, orderBy: [], select: [], limit: 50, offset: 0 });
  assert.equal(safetyTableFocus(query([])), null);
  assert.equal(safetyTableFocus(query([{ field: "rule", op: "=", value: "delete", negated: true }])), null);
  assert.equal(safetyTableFocus(query([{ field: "rule", op: "=", value: "delete" }, { field: "rule", op: "=", value: "permissions" }])), null);
  assert.equal(safetyTableFocus(query([{ any: [{ field: "rule", op: "=", value: "delete" }, { field: "rule", op: "=", value: "permissions" }] }])), null);
  assert.deepEqual(safetyTableFocus(query([{ field: "rule", op: "=", value: "delete" }, { field: "state", op: "!=", value: "open" }])), { rule: "delete" });
  assert.deepEqual(safetyTableFocus(query([{ field: "rule", op: "=", value: "delete" }, { field: "target_label", op: "=", value: "cosy-couch" }])), { rule: "delete", place: "cosy-couch" });
  assert.deepEqual(safetyTableFocus({ ...query([]), grouping: { source: query([{ field: "rule", op: "=", value: "delete" }]) } }), { rule: "delete" });
});

test("a selection becomes one prompt per location, largest first, and says why prompts are unavailable", () => {
  const notices = [notice("a1"), notice("b1", { target: "machine:b", target_label: "Mac" }), notice("a2"), notice("gone", { state: "dismissed" })];
  const mixed = safetySelectionPlan(notices, ["b1", "a1", "a2"]);
  assert.equal(mixed.blocked, "");
  assert.deepEqual(mixed.groups, [{ target: "repo:a", label: "Alpha", ids: ["a1", "a2"] }, { target: "machine:b", label: "Mac", ids: ["b1"] }]);
  const closed = safetySelectionPlan(notices, ["a1", "gone"]);
  assert.equal(closed.closed, 1);
  assert.match(closed.blocked, /1 of the selected notices is not open/);
  assert.match(safetySelectionPlan(notices, ["a1", "elsewhere"]).blocked, /Loading/, "a notice the overview has not loaded yet is never guessed at");
});

test("check cards lead with what their notices share", () => {
  assert.equal(safetyResourceName("/Users/someone/repo/.build/gcloud/"), "~/repo/.build/gcloud");
  assert.equal(safetyResourceName("/Users/someone/repo · push --force origin main"), "push --force origin main");
  assert.equal(safetyResourceName(" · orphan"), "orphan", "a missing repository leaves no separator behind");
  assert.equal(safetyResourceName("$PWD/.build/gcloud-config"), ".build/gcloud-config");
  assert.equal(safetyShortName("/Volumes/drive/some/very/long/path/to/catalog.sqlite3", 30).length, 30);
  assert.match(safetyShortName("/Volumes/drive/some/very/long/path/to/catalog.sqlite3", 30), /^\/Volumes.*….*catalog\.sqlite3$/);
  assert.equal(safetyCommonDirectory(["~/work/a/x", "~/work/a/y/z"]), "~/work/a");
  assert.equal(safetyCommonDirectory(["~/a", "~/b"]), "", "the home directory alone says nothing");
  assert.equal(safetyCommonDirectory(["/Volumes/drive/a", "/Volumes/drive/b"]), "/Volumes/drive");
  assert.equal(safetyCommonDirectory(["/Volumes/drive/a"]), "", "one resource is shown whole");
  assert.equal(safetyCommonDirectory(["push origin main", "push origin dev"]), "");
  const copy = (id, subject, patch = {}) => notice(id, { rule: "credentials", title: "Credentials copied to another location", danger: "high", subject, ...patch });
  const [credentials] = safetyDashboardChecks([
    copy("a", "/Users/me/explo/.build/gcloud/", { calls: 2, first_seen_at: "2026-10-08T00:00:00Z", last_seen_at: "2026-10-20T00:00:00Z" }),
    copy("b", "/Users/me/explo/.build/gcloud", { material_change: true }),
    copy("c", "/Users/me/explo/.build/gcloud-config", { cart: {} }),
  ], "open", "");
  assert.deepEqual(credentials.ids, ["a", "b", "c"]);
  assert.deepEqual(credentials.places, [{ label: "Alpha", count: 3 }], "one location is said once");
  assert.equal(credentials.under, "~/explo/.build");
  assert.deepEqual(credentials.resources, [{ name: "gcloud", count: 2 }, { name: "gcloud-config", count: 1 }], "a trailing slash names the same resource");
  assert.deepEqual([credentials.first_seen_at, credentials.last_seen_at], ["2026-10-08T00:00:00Z", "2026-11-01T12:00:00Z"]);
  assert.deepEqual([credentials.calls, credentials.fresh, credentials.inPrompt], [4, 1, 1]);
  const spread = safetyDashboardChecks(["a", "b", "c", "d"].map((id, i) => notice(id, { rule: "process", title: "Every instance of a program stopped", subject: `x · ${id}`, target: `repo:${i}`, target_label: ["pharos", "pharos", "explo", "tl1"][i] })), "open", "")[0];
  assert.deepEqual([spread.places, spread.morePlaces, spread.locations], [[{ label: "pharos", count: 2 }, { label: "explo", count: 1 }], 1, 4]);
  assert.equal(spread.moreResources, 1);
});

test("a dashboard prompt lists each check once with its notices", () => {
  const item = (id, rule, ticked) => ({ id, rule, title: rule, danger: "high", ticked });
  assert.deepEqual(safetyCartChecks({ items: [item("a", "credentials", true), item("b", "process", true), item("c", "credentials", false)] }),
    [{ rule: "credentials", title: "credentials", danger: "high", ids: ["a", "c"], ticked: 1 }, { rule: "process", title: "process", danger: "high", ids: ["b"], ticked: 1 }]);
  assert.deepEqual(safetyCartChecks({ items: [] }), []);
});


test("Optimize table headings describe the active source and group filters", () => {
  const schema = { fields: [
    { name: "state", label: "State", type: "text", filter: { values: { source: "static", options: [{ value: "handed_off", label: "Handed off" }] } } },
    { name: "target", label: "Location", type: "text" },
  ] };
  for (const noun of ["Risky action notices", "Efficiency findings", "Fixes"]) {
    assert.equal(optimizeTableTitle(noun, schema, EMPTY_QUERY), noun);
    const source = { ...EMPTY_QUERY, where: [{ field: "state", op: "=", value: "handed_off" }] };
    assert.equal(optimizeTableTitle(noun, schema, source), `${noun} where State is Handed off`);
    const grouped = { ...EMPTY_QUERY, grouping: { source, keys: ["target"], summaries: [{ id: "count", op: "count", label: "Notices" }], representative: null }, where: [{ field: "count", op: ">", value: "2" }] };
    assert.equal(optimizeTableTitle(noun, schema, grouped), `${noun} where State is Handed off grouped by Location, keeping groups where Notices is greater than 2`);
    assert.equal(optimizeTableTitle(noun, schema, { ...grouped, grouping: { ...grouped.grouping, source: EMPTY_QUERY } }), `${noun} grouped by Location, keeping groups where Notices is greater than 2`);
  }
});


test("focused table headings retain additional source and group restrictions", () => {
  const schema = { fields: [{ name: "rule", label: "Check", type: "text" }, { name: "state", label: "State", type: "text" }, { name: "target", label: "Location", type: "text" }] };
  const named = [{ field: "rule", op: "=", value: "credentials" }, { field: "state", op: "=", value: "open" }];
  const source = { ...EMPTY_QUERY, where: [...named, { any: [{ field: "target", op: "=", value: "a" }, { field: "target", op: "=", value: "b" }] }] };
  assert.equal(optimizeTableRemainder(schema, source, named), "where (Location is a or b)");
  const grouped = { ...EMPTY_QUERY, grouping: { source, keys: ["target"], summaries: [{ id: "n", op: "count", label: "Notices" }], representative: null }, where: [{ field: "n", op: ">", value: "2" }] };
  assert.equal(optimizeTableRemainder(schema, grouped, named), "where (Location is a or b) grouped by Location, keeping groups where Notices is greater than 2");
  assert.equal(source.where.length, 3, "the table query is unchanged");
});


test("grouped Optimize headings use source and result catalogues for their filters", () => {
  const fixes = loadSchema(fixesDocument);
  const place = fixes.fields.find(field => field.name === "target_label");
  const resultFields = [place, { name: "recent", label: "Latest seen", type: "datetime" }];
  const source = { ...EMPTY_QUERY, where: [
    { field: "shown", op: "=", value: "stale" },
    { field: "category", op: "=", value: "env" },
    { field: "last_seen_at", op: ">=", value: "2026-01-01" },
  ] };
  const grouped = { ...EMPTY_QUERY, grouping: { source, keys: ["target_label"], summaries: [{ id: "recent", op: "max", field: "last_seen_at", label: "Latest seen" }], representative: null }, where: [{ field: "recent", op: ">=", value: "2026-02-01" }] };
  // This is the runtime shape: grouped result fields omit source-only columns.
  const api = { query: grouped, computed: { catalogue: resultFields }, grouping: { source: { schema: fixes } } };
  const schemas = optimizeTableSchemas(api);
  const title = optimizeTableTitle("Fixes", schemas.source, grouped, schemas.result);
  assert.match(title, /On the menu is Hidden: quiet too long/);
  assert.match(title, /Kind of fix is Environment or setup/);
  assert.match(title, /Last seen date is on or after 2026-01-01/);
  assert.match(title, /keeping groups where Latest seen is on or after 2026-02-01/);

  const hidden = { ...grouped, grouping: { ...grouped.grouping, source: { ...source, where: [{ field: "state", op: "=", value: "open" }, { field: "shown", op: "!=", value: "shown" }, { field: "category", op: "=", value: "env" }] } } };
  const named = [{ field: "state", op: "=", value: "open" }, { field: "shown", op: "!=", value: "shown" }];
  assert.match(optimizeTableRemainder(schemas.source, hidden, named, schemas.result), /Kind of fix is Environment or setup/);

  const safety = loadSchema(safetyDocument);
  const safetyQuery = { ...grouped, grouping: { ...grouped.grouping, source: { ...EMPTY_QUERY, where: [{ field: "rule", op: "=", value: "credentials" }, { field: "danger", op: "=", value: "high" }] } } };
  const safetySchemas = optimizeTableSchemas({ ...api, query: safetyQuery, grouping: { source: { schema: safety } } });
  assert.match(optimizeTableRemainder(safetySchemas.source, safetyQuery, [{ field: "rule", op: "=", value: "credentials" }], safetySchemas.result), /Danger is Dangerous/);
  const plain = optimizeTableSchemas({ ...api, query: source });
  assert.equal(plain.source, plain.result, "ungrouped tables use their computed catalogue");
});
