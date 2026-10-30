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

test("conversation card facts follow selected columns, order, accessors and display formats", async () => {
 const React = await import("react"), { renderToStaticMarkup } = await import("react-dom/server");
 const { selectedFields } = await import("@pythia-software/query-table-core");
 const { defaultRenderers } = await import("@pythia-software/query-table-ui");
 const { ConversationFacts } = await import("../../../src/conversation-results");
 const fields = [
  { name: "title", label: "Work", type: "text", source: { kind: "backend" } },
  { name: "branch", label: "Branch", type: "text", source: { kind: "backend" } },
  { name: "tool_use_count", label: "Tool uses", type: "number", source: { kind: "backend" }, render: "number" },
  { name: "computed", label: "Computed total", type: "number", source: { kind: "derived", accessor: row => row.tool_use_count * 2 }, render: "number" },
  { name: "missing", label: "Missing value", type: "text", source: { kind: "backend" } },
 ];
 const row = { title: "A work", branch: "feature/search", tool_use_count: 12 };
 const render = select => {
  const query = { ...m.freshSearch(schema).query, select };
  return renderToStaticMarkup(React.createElement(ConversationFacts, { row, fields: selectedFields({ ...schema, fields }, query), query, renderers: defaultRenderers }));
 };
 const initial = render([{ field: "title" }, { field: "branch" }, { field: "tool_use_count" }]);
 assert.ok(initial.includes("feature/search")); assert.ok(initial.includes("12")); assert.ok(!initial.includes("A work"));
 const customized = render([{ field: "computed" }, { field: "missing" }, { field: "tool_use_count", display: { kind: "text" } }]);
 assert.ok(customized.includes("24")); assert.ok(!customized.includes("Missing value"));
 assert.ok(customized.indexOf("Computed total") < customized.indexOf("Tool uses"));
 assert.ok(!customized.includes("feature/search"));
 const customFormat = { ...m.freshSearch(schema).query, select: [{ field: "tool_use_count", display: { kind: "text" } }] };
 const formatted = renderToStaticMarkup(React.createElement(ConversationFacts, { row, fields: [fields[2]], query: customFormat, renderers: { ...defaultRenderers, number: () => "numeric renderer" } }));
 assert.ok(formatted.includes("12")); assert.ok(!formatted.includes("numeric renderer"), "column display overrides the field renderer");
 for (const value of [undefined, null, "", "   ", []]) {
  row.missing = value;
  const empty = render([{ field: "missing" }]);
  assert.ok(!empty.includes("Missing value"), `empty fact has no label: ${JSON.stringify(value)}`);
  assert.ok(!empty.includes("—"));
 }
 const noPresentation = renderToStaticMarkup(React.createElement(ConversationFacts, { row, fields: [fields[2]], query: { ...m.freshSearch(schema).query, select: [{ field: "tool_use_count" }] }, renderers: { ...defaultRenderers, number: () => null } }));
 assert.ok(!noPresentation.includes("Tool uses"));
 row.tool_use_count = 0; assert.ok(render([{ field: "tool_use_count" }]).includes(">0<"));
});

test("conversation outlines retain child conversations, orphaned sessions and malformed parent links", async () => {
 const { conversationForest, initialOutlineEntry } = await import("../../../src/conversation-results");
 const child = { id: "child", parent_id: "root" }, nested = { id: "nested", parent_id: "child" };
 const roots = conversationForest([nested, child, { id: "root" }, { id: "orphan", parent_id: "absent" }, { id: "cycle-a", parent_id: "cycle-b" }, { id: "cycle-b", parent_id: "cycle-a" }]);
 assert.deepEqual(roots.map(node => node.conversation.id), ["root", "orphan", "cycle-a", "cycle-b"]);
 assert.equal(roots[0].children[0].conversation, child);
 assert.equal(roots[0].children[0].children[0].conversation, nested);
 const setup = { id: "setup", kind: "turn", human: null, children: [{ id: "setup-agent", kind: "agent", human: { role: "user", text: "Initialize" }, children: [] }] };
 const firstTurn = { id: "first", kind: "turn", human: { role: "user", text: "Review the change" }, children: [] };
 assert.equal(initialOutlineEntry([setup, firstTurn]), firstTurn, "browsing starts at the first user turn, beyond session setup and its agents");
 assert.equal(initialOutlineEntry([firstTurn]), firstTurn);
 assert.equal(initialOutlineEntry([setup]), setup, "setup-only conversations remain inspectable");
 assert.equal(initialOutlineEntry([]), undefined);
});

test("compact conversation activity describes counts and elapsed time without inventing missing values", async () => {
 const { conversationActivitySummary } = await import("../../../src/conversation-results");
 assert.equal(conversationActivitySummary({ turn_count: 12, subagent_count: 2, preview_elapsed_ms: 4800000 }), "12 turns · 2 sub-agents · 1h 20m elapsed");
 assert.equal(conversationActivitySummary({ turn_count: 1, subagent_count: 1, preview_elapsed_ms: 1000 }), "1 turn · 1 sub-agent · 1s elapsed");
 assert.equal(conversationActivitySummary({ turn_count: 0, subagent_count: 0, preview_elapsed_ms: 0 }), "0 turns · 0 sub-agents · 0s elapsed");
 assert.equal(conversationActivitySummary({ turn_count: 3, subagent_count: 0 }), "3 turns · 0 sub-agents");
 assert.equal(conversationActivitySummary({ turn_count: null, subagent_count: -1, preview_elapsed_ms: NaN }), "Conversation details");
 assert.equal(conversationActivitySummary({ preview_elapsed_ms: null }), "Conversation details");
});
