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
  text_builder: JSON.stringify({ editor: "visual", match: "rules", text: "", mode: "advanced", root: { id: "group-3", kind: "group", join: "all", exclude: false, children: [{ id: "rule-5", kind: "phrase", text: "lock this to another object", other: "", distance: "8", exclude: false }] }, scope: { surface: "prose", roles: [], kinds: [], repository: "", provider: "", source: "", from: "", to: "", conversation_id: "", workspace_id: "" }, fuzzy: false, caseSensitive: false, separators: false }),
});

test("MCP connection actions stay usable while the history summary waits", { skip }, async t => {
  const app = await open(t, { url: `/tools?${carried}`, ready: "#toolsPage .qt-row" });
  const { page } = app;
  let waiting;
  await page.route("**/api/mcp/calls?limit=1", route => { waiting = route; });
  await page.locator('button[data-view="mcp"]').click();
  await page.locator(".mcp-history .qt-row").first().waitFor();
  const copy = page.getByRole("button", { name: "Copy JSON", exact: true });
  // Trial checks actionability without touching the clipboard.
  await copy.click({ trial: true });
  await copy.focus();
  assert.equal(await copy.evaluate(element => document.activeElement === element), true);
  await app.shot("summary-pending");
  await page.locator('button[data-view="tools"]').click();
  await page.locator("#tools.active").waitFor();
  await page.unroute("**/api/mcp/calls?limit=1");
  await waiting?.abort("aborted").catch(() => {});
  await page.locator('button[data-view="mcp"]').click();
  await page.locator(".mcp-history .qt-row").first().waitFor();
  await app.settled();
});

test("MCP opens and reopens after Tools and a Library text search", { skip }, async t => {
  const app = await open(t, { url: `/library?${carried}`, ready: "#queryTableLibrary .qt-table" });
  for (const view of ["tools", "mcp", "library", "mcp"]) {
    await app.page.locator(`button[data-view="${view}"]`).click();
    await app.page.locator(`#${view}.active .qt-row`).first().waitFor();
  }
  await app.page.getByRole("button", { name: "Copy JSON", exact: true }).click({ trial: true });
  await app.settled();
  await app.shot("returned-to-mcp");
});


for (const minimal of [false, true]) {
  test(`MCP call details render and return focus with ${minimal ? "minimal" : "default"} columns`, { skip }, async t => {
    const query = minimal ? `?q_mcp_calls=${encodeURIComponent(encodeQuery({ ...EMPTY_QUERY, select: [{ field: "tool_name" }] }))}` : "";
    const app = await open(t, { url: `/mcp${query}`, ready: ".mcp-history .qt-row", viewport: { width: 390, height: 844 } });
    const show = app.page.locator(".mcp-history .qt-row").first().getByRole("button", { name: "Details", exact: true });
    await show.focus();
    await app.page.keyboard.press("Enter");
    const dialog = app.page.getByRole("dialog", { name: "MCP call details", exact: true });
    await dialog.locator(".mcp-code").first().waitFor();
    assert.equal(await dialog.evaluate(element => element.contains(document.activeElement)), true);
    await app.shot("call-details");
    await app.page.keyboard.press("Escape");
    await dialog.waitFor({ state: "detached" });
    assert.equal(await show.evaluate(element => element === document.activeElement), true);
    await app.page.locator(".mcp-history .qt-row").first().waitFor();
    await app.settled();
  });
}

test("unavailable MCP call details remain dismissible without losing the page", { skip }, async t => {
  const app = await open(t, { url: "/mcp", ready: ".mcp-history .qt-row", api: { "GET /api/query/mcp_calls/item": { row: null } } });
  await app.page.locator(".mcp-history .qt-row").first().getByRole("button", { name: "Details", exact: true }).click();
  const dialog = app.page.getByRole("dialog", { name: "MCP call details", exact: true });
  await dialog.getByRole("alert").waitFor();
  await dialog.getByRole("button", { name: "Close", exact: true }).click();
  await dialog.waitFor({ state: "detached" });
  await app.page.locator(".mcp-history .qt-row").first().waitFor();
});
