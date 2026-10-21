import test from "node:test";
import assert from "node:assert/strict";
import { EMPTY_QUERY, ITEM_DETAILS_COLUMN, encodeQuery } from "@pythia-software/query-table-core";
import { open, skip } from "./harness.mjs";

// Navigation can carry previous surfaces' state, including the Library's
// visual text search. MCP must use its own controller after every visit.
const carried = new URLSearchParams({
  q_tool_calls: encodeQuery({ ...EMPTY_QUERY, select: [{ field: "tool_name" }], orderBy: [{ field: "started_at", dir: "desc" }] }),
  tools_view: "summary",
  q_tools: encodeQuery({ ...EMPTY_QUERY, select: [{ field: "tool_name" }, { field: "call_count" }] }),
  q_library: encodeQuery({ ...EMPTY_QUERY, select: [{ field: "title" }] }),
  text_builder: JSON.stringify({ editor: "visual", match: "rules", text: "", mode: "advanced", root: { id: "group-3", kind: "group", join: "all", exclude: false, children: [{ id: "rule-5", kind: "phrase", text: "lock this to another object", other: "", distance: "8", exclude: false }] }, scope: { surface: "prose", roles: [], kinds: [], repository: "", provider: "", source: "", from: "", to: "", conversation_id: "", workspace_id: "" }, fuzzy: false, caseSensitive: false, separators: false }),
});
const queryURL = "/analyze?analyze_mode=query&analyze_data=mcp_calls&analyze_topic=mcp";
const table = ".usage-workspace-table";
async function navigate(page, href) {
  await page.evaluate(href => window.pharosNavigate(href), href);
}

test("MCP connection actions stay usable independently of the history summary", { skip }, async t => {
  const app = await open(t, { url: `/tools?${carried}`, ready: "#toolsPage .qt-row" });
  const { page } = app;
  let waiting;
  await page.route("**/api/mcp/calls?limit=1", route => { waiting = route; });
  await navigate(page, "/settings/mcp");
  const copy = page.getByRole("button", { name: "Copy JSON", exact: true });
  await copy.click({ trial: true });
  await copy.focus();
  assert.equal(await copy.evaluate(element => document.activeElement === element), true);
  await app.shot("connection-settings");
  await page.getByRole("link", { name: "View MCP usage", exact: true }).click();
  await page.locator(".usage-mcp-dashboard .qt-metric").first().waitFor();
  await page.getByRole("link", { name: "MCP settings", exact: true }).click();
  await copy.click({ trial: true });
  await page.unroute("**/api/mcp/calls?limit=1");
  await waiting?.abort("aborted").catch(() => {});
  await app.settled();
});

for (const fallback of [false, true]) {
  test(`MCP opens and reopens after Tools and a Library text search${fallback ? " without AbortSignal.any" : ""}`, { skip }, async t => {
    const app = await open(t, { url: `/library?${carried}`, ready: "#queryTableLibrary .qt-table" });
    if (fallback) {
      await app.page.addInitScript(() => { AbortSignal.any = undefined; });
      await app.page.reload();
      await app.page.locator("#queryTableLibrary .qt-row").first().waitFor();
    }
    for (const [href, ready] of [[`/tools?${carried}`, "#toolsPage .qt-row"], [queryURL, `${table} .qt-row`], [`/library?${carried}`, "#queryTableLibrary .qt-row"], [queryURL, `${table} .qt-row`]]) {
      await navigate(app.page, href);
      await app.page.locator(ready).first().waitFor();
    }
    await navigate(app.page, "/settings/mcp");
    await app.page.getByRole("button", { name: "Copy JSON", exact: true }).click({ trial: true });
    await app.settled();
    await app.shot("returned-to-mcp");
  });
}

for (const columns of ["default", "minimal", "recovered default"]) {
  test(`MCP call details render and return focus with ${columns} columns`, { skip }, async t => {
    const minimal = columns === "minimal", recovery = columns === "recovered default";
    // Keep the native Detail action beside the single selected data column.
    const query = minimal ? `&q_mcp_calls=${encodeURIComponent(encodeQuery({ ...EMPTY_QUERY, select: [{ field: "tool_name" }, { field: ITEM_DETAILS_COLUMN }], itemDetails: { modes: ["modal", "expand"] } }))}` : "";
    const invalid = { ...EMPTY_QUERY, select: [{ field: "tool_name" }], where: [{ field: "removed_field", op: "=", value: "x" }] };
    const preferences = recovery ? {
      "query-table:default:pharos_mcp_calls": "bad-default",
      "pharos-bookmarks-v1": [{ id: "bad-default", savedQueryId: "bad-default", title: "Failing default", url: `${queryURL}&q_mcp_calls=${encodeURIComponent(encodeQuery(invalid))}`, dataset: "mcp_calls", query: invalid }],
    } : {};
    const app = await open(t, { url: `${queryURL}${query}`, preferences, ready: recovery ? `${table} .query-recovery` : `${table} .qt-row`, viewport: { width: 390, height: 844 } });
    if (recovery) {
      await app.page.getByRole("button", { name: "Reset table", exact: true }).click();
      await app.page.locator(`${table} .qt-row`).first().waitFor();
      await app.page.locator(`${table} .query-recovery`).waitFor({ state: "detached" });
      await app.settled();
    }
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
}

test("unavailable MCP call details remain dismissible without losing the page", { skip }, async t => {
  const app = await open(t, { url: queryURL, ready: `${table} .qt-row`, api: { "GET /api/query/mcp_calls/item": { row: null } } });
  await app.page.getByRole("button", { name: /^Open item .+ in modal$/ }).first().click();
  const dialog = app.page.getByRole("dialog");
  await dialog.waitFor();
  await app.page.keyboard.press("Escape");
  await dialog.waitFor({ state: "detached" });
  await app.page.locator(`${table} .qt-row`).first().waitFor();
});
