import test from "node:test";
import assert from "node:assert/strict";
import { EMPTY_QUERY, encodeQuery } from "@pythia-software/query-table-core";
import { open, skip } from "./harness.mjs";

// Navigation can carry previous surfaces' state, including the Library's
// visual text search. MCP must use its own controller after every visit.
const carried = new URLSearchParams({
  q_tool_calls: encodeQuery({ ...EMPTY_QUERY, select: [{ field: "tool_name" }], orderBy: [{ field: "started_at", dir: "desc" }] }),
  tools_view: "summary",
  q_tools: encodeQuery({ ...EMPTY_QUERY, select: [{ field: "tool_name" }, { field: "call_count" }] }),
  q_library: encodeQuery({ ...EMPTY_QUERY, select: [{ field: "title" }] }),
  view: "table",
  text_builder: JSON.stringify({ editor: "visual", match: "rules", text: "", mode: "advanced", root: { id: "group-3", kind: "group", join: "all", exclude: false, children: [{ id: "rule-5", kind: "phrase", text: "lock this to another object", other: "", distance: "8", exclude: false }] }, scope: { surface: "prose", roles: [], kinds: [], repository: "", provider: "", source: "", from: "", to: "", conversation_id: "", workspace_id: "" }, fuzzy: false, caseSensitive: false, separators: false }),
});
const queryURL = "/analyze?analyze_mode=query&analyze_data=mcp_calls&analyze_topic=mcp";
const table = ".usage-workspace-table";
async function navigate(page, href) {
  await page.evaluate(href => window.pharosNavigate(href), href);
}

// The fallback without AbortSignal.any is unit-tested (metric-request.test.mjs).
test("MCP opens and reopens after Tools and a Library text search", { skip }, async t => {
    const app = await open(t, { url: `/library?${carried}`, ready: "#queryTableLibrary .qt-table" });
    for (const [href, ready] of [[`/tools?${carried}`, "#toolsPage .qt-row"], [queryURL, `${table} .qt-row`], [`/library?${carried}`, "#queryTableLibrary .qt-row"], [queryURL, `${table} .qt-row`]]) {
      await navigate(app.page, href);
      await app.page.locator(ready).first().waitFor();
    }
    await navigate(app.page, "/settings/mcp");
    await app.page.getByRole("button", { name: "Copy JSON", exact: true }).click({ trial: true });
    await app.settled();
    await app.shot("returned-to-mcp");
});

// Details load by ID whatever the table projects (unit/page/mcp.test.mjs), so
// one column set suffices; this one also covers recovering from a bad default.
test("MCP call details render and return focus after recovering from a failing default", { skip }, async t => {
  const invalid = { ...EMPTY_QUERY, select: [{ field: "tool_name" }], where: [{ field: "removed_field", op: "=", value: "x" }] };
  const preferences = {
    "query-table:default:pharos_mcp_calls": "bad-default",
    "pharos-bookmarks-v1": [{ id: "bad-default", savedQueryId: "bad-default", title: "Failing default", url: `${queryURL}&q_mcp_calls=${encodeURIComponent(encodeQuery(invalid))}`, dataset: "mcp_calls", query: invalid }],
  };
  const app = await open(t, { url: queryURL, preferences, ready: `${table} .query-recovery`, viewport: { width: 390, height: 844 } });
  await app.page.getByRole("button", { name: "Reset table", exact: true }).click();
  await app.page.locator(`${table} .qt-row`).first().waitFor();
  await app.page.locator(`${table} .query-recovery`).waitFor({ state: "detached" });
  await app.settled();
  const show = app.page.getByRole("button", { name: /^Open item .+ in modal$/ }).first();
  await show.focus();
  await app.page.keyboard.press("Enter");
  const dialog = app.page.getByRole("dialog");
  await dialog.getByText("Arguments (sanitized)", { exact: true }).waitFor();
  assert.equal(await dialog.evaluate(element => element.contains(document.activeElement)), true);
  await app.shot("call-details");
  await app.page.keyboard.press("Escape");
  await dialog.waitFor({ state: "detached" });
  assert.equal(await show.evaluate(element => element === document.activeElement), true);
  await app.page.locator(`${table} .qt-row`).first().waitFor();
  await app.settled();
});

test("unavailable MCP call details remain dismissible without losing the page", { skip }, async t => {
  const app = await open(t, { url: queryURL, ready: `${table} .qt-row`, api: { "GET /api/query/mcp_calls/item": { row: null } } });
  await app.page.getByRole("button", { name: /^Open item .+ in modal$/ }).first().click();
  const dialog = app.page.getByRole("dialog");
  await dialog.waitFor();
  await app.page.keyboard.press("Escape");
  await dialog.waitFor({ state: "detached" });
  await app.page.locator(`${table} .qt-row`).first().waitFor();
});
