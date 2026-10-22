import assert from "node:assert/strict";
import test from "node:test";
import * as m from "../../../src/search-composer-state";
import { withGroupingKeys } from "@pythia-software/query-table-core";
import { queryForSearchResultsView, searchResultsView } from "../../../src/search-results-view-model";
const schema = { name: "test", idField: "id", fields: [], defaultSort: [{ field: "activity_at", dir: "desc" }], defaultLimit: 50 };
test("fresh and restored unused composers start with a removable 30-day message restriction", () => {
 const earliest = Date.now() - 30 * 86400000;
 for (const state of [m.freshSearch(schema), m.restoreSearch(new URLSearchParams(), schema)]) {
  assert.ok(Date.parse(state.text.scope.from) >= earliest);
  assert.ok(Date.parse(state.text.scope.from) <= Date.now() - 30 * 86400000);
  assert.equal(state.text.dateWindow.duration, "30d");
  assert.deepEqual(state.query.where, [], "the date restricts messages rather than workspace activity");
  state.text.root.children[0].text = "cache";
  state.text.scope.from = "";
  const restored = m.restoreSearch(new URL(m.searchURL(state), "http://localhost").searchParams, schema);
  assert.equal(restored.text.scope.from, "");
 }
});
test("Table and Conversations display the same query without changing filters, columns or paging", () => {
 const query = { ...m.freshSearch(schema).query,
  where: [{ any: [{ field: "repository_name", op: "=", value: "pharos" }, { field: "repository_name", op: "=", value: "query-table" }] }],
  select: [{ field: "title" }, { field: "total_tokens" }], offset: 100,
 };
 for (const view of ["table", "conversation"]) {
  assert.equal(queryForSearchResultsView(query, view), query);
  assert.equal(searchResultsView(query, view), view);
 }
});
test("grouped results show a table and Conversations returns to the filtered source work", () => {
 const source = { ...m.freshSearch(schema).query,
  where: [{ field: "repository_name", op: "=", value: "pharos" }],
  select: [{ field: "title" }, { field: "total_tokens" }],
  orderBy: [{ field: "total_tokens", dir: "desc", nulls: "last" }],
 };
 const grouped = withGroupingKeys(source, ["repository_name"]);
 assert.equal(searchResultsView(grouped, "conversation"), "table");
 assert.equal(queryForSearchResultsView(grouped, "table"), grouped);
 const conversations = queryForSearchResultsView(grouped, "conversation");
 assert.equal(conversations.grouping, undefined);
 assert.deepEqual(conversations.where, source.where);
 assert.deepEqual(conversations.select, source.select);
 assert.deepEqual(conversations.orderBy, source.orderBy);
 assert.equal(searchResultsView(conversations, "conversation"), "conversation");
 assert.ok(grouped.grouping);
});
test("composer carries scope, exclusions, OR filters, advanced sort and metrics without cursor", () => {
 const d = m.freshSearch(schema);
 d.text.root.children[0].text = "parser";
 d.text.root.children.push({ ...d.text.root.children[0], id: "negative", text: "retry", exclude: true });
 d.text.scope.provider = "claude"; d.text.scope.workspace_id = "w";
 d.text.caseSensitive = true;
 d.query.where = [{ any: [{ field: "title", op: "contains", value: "test" }, { field: "title", op: "contains", value: "fix", negated: true }] }];
 d.query.orderBy = [{ field: "title", dir: "asc", nulls: "first", extract: { regex: "[A-Z]+" } }];
 d.query.aggregations = [{ id: "count", op: "count", groupBy: ["provider"] }];
 d.query.offset = 250;
 const url = new URL(m.searchURL(d), "http://localhost");
 assert.equal(url.pathname, "/library");
 assert.equal(url.searchParams.has("text_cursor"), false);
 const restored = m.restoreSearch(url.searchParams, schema);
 assert.deepEqual(restored.query, { ...d.query, offset: 0 });
 assert.deepEqual(restored.text.scope, d.text.scope);
 assert.deepEqual(restored.text.dateWindow, d.text.dateWindow);
 assert.equal(restored.text.root.children[1].exclude, true);
 assert.equal(restored.text.caseSensitive, true);
});
for (const kind of ["file", "url"]) {
 test(`${kind} target keeps case independent of text case, fuzzy and message scope`, () => {
  const d = m.freshSearch(schema); d.kind = kind; d.file = kind === "file" ? "parser.go" : "example.com/Parser";
  d.text.fuzzy = true; d.text.scope.provider = "claude";
  for (const textCase of [false, true]) for (const fileCase of [false, true]) {
   d.text.caseSensitive = textCase; d.fileCase = fileCase;
   const params = new URL(m.searchURL(d), "http://localhost").searchParams;
   assert.equal(params.get("kind"), kind); assert.equal(params.get("search"), d.file);
   assert.equal(params.get("case"), fileCase ? "1" : null);
   for (const key of ["text_builder", "fuzzy", "text_provider"]) assert.equal(params.has(key), false);
   const restored = m.restoreSearch(params, schema);
   assert.equal(restored.kind, kind); assert.equal(restored.fileCase, fileCase);
  }
 });
}
test("legacy plain search and file links still restore", () => {
 const old = m.restoreSearch(new URLSearchParams("search=cache+OR+buffer&text_plain=1&text_surface=user"), schema);
 assert.equal(old.text.editor, "visual"); assert.equal(old.text.root.children[0].text, "cache OR buffer");
 const file = m.restoreSearch(new URLSearchParams("search=README&kind=file&case=1"), schema);
 assert.equal(file.fileCase, true); assert.equal(file.kind, "file");
});
test("fresh searches never reuse another draft's workspace restrictions", () => {
 const a = m.freshSearch(schema); a.query.where.push({ field: "title", op: "=", value: "A" }); a.text.scope.workspace_id = "w";
 const b = m.freshSearch(schema); assert.deepEqual(b.query.where, []); assert.equal(b.text.scope.workspace_id, "");
});
test("corrupt workspace query is visible instead of silently broadening the search", () => {
 const state = m.restoreSearch(new URLSearchParams("q_library=not-a-query"), schema);
 assert.match(state.error, /workspace filters could not be read/);
});
test("rolling windows travel with the query until their date filters change", () => {
 const params = new URL(m.searchURL(m.freshSearch(schema)), "http://localhost").searchParams;
 params.set("window_dataset", "library"); params.set("window_field", "activity_at"); params.set("window_days", "7");
 const d = m.restoreSearch(params, schema);
 assert.equal(new URL(m.searchURL(d), "http://localhost").searchParams.get("window_days"), "7");
 d.query.where = [];
 assert.equal(new URL(m.searchURL(d), "http://localhost").searchParams.has("window_days"), false);
});

test("relative windows round trip through search URLs and disappear after a manual date edit", () => {
 const state = m.freshSearch(schema);
 state.window = { days: 7, field: "activity_at", mode: "relative" };
 state.query.where = [{ field: "activity_at", op: ">=", value: "-7d" }, { field: "activity_at", op: "<", value: "+0s" }];
 const params = new URL(m.searchURL(state), "http://localhost").searchParams;
 assert.equal(params.get("window_mode"), "relative");
 const restored = m.restoreSearch(params, schema);
 assert.deepEqual(restored.window, state.window); assert.deepEqual(restored.query.where, state.query.where);
 restored.query.where[0] = { ...restored.query.where[0], value: "2026-01-01T00:00:00Z" };
 const edited = new URL(m.searchURL(restored), "http://localhost").searchParams;
 for (const key of ["window_mode", "window_days", "window_field", "window_dataset"]) assert.equal(edited.has(key), false);
});
