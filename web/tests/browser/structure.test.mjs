// The structural sweep: every page the shell serves, at a desktop, a phone
// and a narrow phone width. Each must render its main content without page,
// console or request errors, keep everything inside the viewport, main and
// its cards, and keep the header in place while main scrolls.
//
// Adding a page is one line in ROUTES: a URL and a selector that appears once
// its main content has rendered. `open` runs before the checks, for surfaces
// reached by a click rather than a URL.
import { describe, test } from "node:test";
import assert from "node:assert/strict";
import { EMPTY_QUERY, encodeQuery } from "@pythia-software/query-table-core";
import { open, skip } from "./harness.mjs";
import { layoutProblems, scrollProblems } from "./layout.mjs";
import { defaultApi, rootConversation, workID } from "./fixtures.mjs";
import { EMPTY_QUERY, encodeQuery, withGroupingKeys } from "@pythia-software/query-table-core";

const work = `/work/${workID}`;
const exploreGroupedSearch = `/library?${new URLSearchParams({ q_library: encodeQuery(withGroupingKeys({ ...EMPTY_QUERY, select: [{ field: "repository_name" }], where: [{ field: "repository_name", op: "=", value: "repo-1" }] }, ["repository_name"])), search: "cache", text_plain: "1" })}`;
const ROUTES = [
  ...['provisional', 'partial'].flatMap(state => [false, true].map(expanded => ({
    name: `Indexing forecast (${state}${expanded ? ', details' : ''})`, url: '/library', ready: '#pharosDrive',
    api: {'GET /api/library/status': {...defaultApi['GET /api/library/status'], idle: false, activities: [{id: 'index-forecast', kind: 'sync', label: 'Capture & index', started_at: new Date(Date.now() - 120_000).toISOString(),
      stoppable: true, stop_path: '/api/index/cancel', detail: 'Codex · 0 of 4 sources finished',
      work: {phase: 'discovering inputs', source: 'Codex', done: 0, total: 0, next: 'Tools, authorship and findings'},
      estimate: {state: 'unknown', basis: 'Discovering inputs before the stage count is available'},
      forecast: {state, low_seconds: state === 'provisional' ? 180 : 0, high_seconds: state === 'provisional' ? 600 : 0, predicted_steps: state === 'provisional' ? 4 : 3, remaining_steps: 4, basis: 'Remaining sources and analyses on this Mac', unknown_steps: state === 'partial' ? ['Classify authorship for retained conversations'] : []}}]}},
    open: async page => {
      await page.evaluate(() => { window.webkit = {messageHandlers: {pharosLibrary: {postMessage: async () => {}}}}; });
      await page.locator('#pharosDrive').click();
      if (expanded) await page.locator('#pharosDrivePanel summary').click();
    }, shown: '#pharosDrivePanel .pharos-activity',
  }))),
  { name: "Home", url: "/", ready: ".home-belt-row .query-card" },
  { name: "Dashboard Catalog", url: "/home?cards=library", ready: ".card-library-entry .query-card" },
  { name: "Search table", url: "/library", ready: "#queryTableLibrary .qt-row" },
  { name: "Search conversations", url: "/library?view=conversation", ready: ".conversation-result-card" },
  { name: "Search display switch", url: "/library", ready: "#queryTableLibrary .qt-row", open: page => page.getByRole("group", { name: "Display results as" }).getByRole("button", { name: "Conversations", exact: true }).click(), shown: ".conversation-result-card" },
  { name: "Search content editor", url: "/library", ready: "#queryTableLibrary .qt-row", open: page => page.getByRole("button", { name: "Messages", exact: true }).click(), shown: ".explore-search-modal" },
  { name: "Search grouped search", url: exploreGroupedSearch, ready: "#queryTableLibrary .qt-row" },
  { name: "Search grouped conversation display", url: exploreGroupedSearch, ready: "#queryTableLibrary .qt-row", open: page => page.getByRole("group", { name: "Display results as" }).getByRole("button", { name: "Conversations", exact: true }).click(), shown: ".conversation-result-card" },
  { name: "TL1 overview", url: "/tl1", ready: ".tl1-tiles" },
  { name: "TL1 configurations", url: "/tl1/configurations", ready: "#tl1-matrix tbody tr" },
  { name: "TL1 follow-ups", url: "/tl1/followups", ready: "#tl1-followup-next tbody tr" },
  { name: "TL1 quality", url: "/tl1/quality", ready: "#tl1-errors" },
  { name: "TL1 runs", url: "/tl1/runs", ready: "#tl1Page .qt-row" },
  { name: "Analyze tokens", url: "/analyze?analyze_view=tokens&machine_view=tokens", ready: "#queryTableUsage .usage-chart" },
  { name: "Analyze machine time", url: "/analyze?analyze_view=tokens&machine_view=time", ready: ".machine-call-surface .qt-row" },
  { name: "Analyze parallelism", concurrency: true, url: "/analyze?analyze_view=tokens&machine_view=parallelism", ready: ".concurrency-chart svg" },
  { name: "Human Words", url: "/analyze?analyze_view=writing", ready: ".writing-panel .usage-chart" },
  { name: "Human Words messages", url: "/analyze?analyze_view=writing&writing=messages", ready: "#queryTableUsage .qt-row" },
  { name: "Carbon Impact", url: "/analyze?analyze_view=carbon", ready: "#carbonSummary" },
  { name: "Analyze conversations", url: "/analyze?analyze_view=conversations", ready: "#queryTableUsage .qt-row" },
  { name: "Analyze workspace dashboards", url: "/analyze", ready: ".usage-dashboard .usage-chart" },
  { name: "Analyze workspace cumulative", url: `/analyze?analyze_topic=tokens&analyze_chart=${encodeURIComponent(JSON.stringify({ display: "cumulative", period: "days", days: 3, split: "model_family", metric: "percent_cost" }))}`, ready: ".usage-dashboard .usage-chart-lines" },
  { name: "Analyze cumulative human", url: `/analyze?analyze_topic=human&analyze_chart=${encodeURIComponent(JSON.stringify({"display": "cumulative", "rollingPeriods": 20}))}`, ready: ".usage-dashboard .usage-chart-lines" },
  { name: "Analyze cumulative skills", url: `/analyze?analyze_topic=skills&analyze_chart=${encodeURIComponent(JSON.stringify({"display": "cumulative", "rollingPeriods": 20}))}`, ready: ".usage-dashboard .usage-chart-lines" },
  { name: "Analyze cumulative time", url: `/analyze?analyze_topic=time&analyze_chart=${encodeURIComponent(JSON.stringify({"display": "cumulative", "rollingPeriods": 20}))}`, ready: ".usage-dashboard .usage-chart-lines" },
  { name: "Analyze cumulative carbon", url: `/analyze?analyze_topic=carbon&analyze_chart=${encodeURIComponent(JSON.stringify({"display": "cumulative", "rollingPeriods": 20}))}`, ready: ".usage-dashboard .usage-chart-lines" },
  { name: "Analyze cumulative tools", url: `/analyze?analyze_topic=tools&analyze_chart=${encodeURIComponent(JSON.stringify({"display": "cumulative", "rollingPeriods": 20, "view": "trend", "metric": "mean_time", "period": "day"}))}`, ready: ".usage-tools-dashboard .qt-metric-mark" },
  { name: "Analyze cumulative mcp", url: `/analyze?analyze_topic=mcp&analyze_chart=${encodeURIComponent(JSON.stringify({"display": "cumulative", "rollingPeriods": 20, "view": "trend", "metric": "mean_time", "period": "day"}))}`, ready: ".usage-mcp-dashboard .qt-metric-mark" },
  { name: "Analyze workspace custom intervals", url: `/analyze?analyze_topic=tokens&analyze_chart=${encodeURIComponent(JSON.stringify({ period: "hours", hours: 6, rollingPeriods: 37 }))}`, ready: ".usage-dashboard .usage-period-custom input" },
  { name: "Analyze workspace human dashboard", url: "/analyze?analyze_workspace=1&analyze_topic=human", ready: ".usage-dashboard .writing-panel .usage-chart" },
  { name: "Analyze workspace tools dashboard", url: "/analyze?analyze_workspace=1&analyze_topic=tools", ready: ".usage-tools-dashboard .qt-metric" },
  // Seven weeks keeps the history request within the stub's equality-filter
  // capability; wider day regex filters are exercised against the Go service.
  { name: "Analyze workspace tools trends", url: `/analyze?analyze_workspace=1&analyze_topic=tools&analyze_chart=${encodeURIComponent(JSON.stringify({ metric: "failure_rate", split: "tool_category", view: "trend", period: "week", rollingPeriods: 7 }))}`, ready: ".usage-tools-dashboard .qt-metric-mark" },
  { name: "Analyze workspace skills dashboard", url: "/analyze?analyze_workspace=1&analyze_topic=skills", ready: ".usage-dashboard .usage-chart" },
  { name: "Analyze workspace time dashboard", url: "/analyze?analyze_workspace=1&analyze_topic=time", ready: ".usage-dashboard .usage-chart" },
  { name: "Analyze workspace parallelism dashboard", concurrency: true, url: "/analyze?analyze_workspace=1&analyze_topic=parallelism", ready: ".usage-dashboard .concurrency-chart svg" },
  { name: "Analyze workspace carbon dashboard", url: "/analyze?analyze_workspace=1&analyze_topic=carbon", ready: ".usage-dashboard #carbonSummary" },
  { name: "Analyze workspace query", url: "/analyze?analyze_workspace=1&analyze_mode=query&analyze_data=tool_calls", ready: ".usage-workspace-table .qt-row" },
  { name: "Analyze workspace optional search", url: "/analyze?analyze_workspace=1&analyze_mode=search&analyze_data=tool_calls", ready: ".usage-workspace-table .qt-row" },
  { name: "Analyze fresh", url: "/analyze?fresh=1&analyze_view=tokens&machine_view=tokens&activity_days=0&activity_repo=&activity_provider=&activity_model=", preferences: { "pharos-usage-view": "writing", "pharos-machine-view": "time", "pharos-machine-scope": { days: 7, repository: "missing", provider: "missing", model: "missing" } }, ready: "#queryTableUsage .qt-row" },
  { name: "Analyze query recovery", url: "/analyze?analyze_view=tokens&machine_view=tokens&q_usage=invalid", ready: ".query-recovery", open: page => page.getByRole("button", { name: "Reset table", exact: true }).click(), shown: "#queryTableUsage .qt-row" },
  { name: "Tool calls", url: "/tools", ready: "#toolsPage .qt-row" },
  { name: "Tool summary", url: "/tools?tools_view=summary", ready: "#toolsPage .qt-row" },
  { name: "Skills", url: "/tools?tools_view=skills", ready: ".skill-usages .qt-row" },
  { name: "Findings", url: "/findings", ready: ".findings-card" },
  { name: "Finding detail", url: "/findings?finding=drift%3Arepo%3Arepo_tl1%3Aghi", ready: ".findings-detail svg" },
  { name: "MCP dashboard", url: "/analyze?analyze_topic=mcp", ready: ".usage-mcp-dashboard .qt-metric-bar-row" },
  { name: "MCP trends", url: `/analyze?analyze_topic=mcp&analyze_chart=${encodeURIComponent(JSON.stringify({ metric: "failure_rate", split: "tool_name", view: "trend", period: "week", rollingPeriods: 37 }))}`, ready: ".usage-mcp-dashboard .qt-metric-mark" },
  { name: "MCP query", url: "/analyze?analyze_mode=query&analyze_data=mcp_calls&analyze_topic=mcp&analyze_search=1", ready: ".usage-workspace-table .qt-row" },
  { name: "MCP legacy link", url: "/mcp", ready: ".usage-mcp-dashboard .qt-metric" },
  { name: "MCP settings", url: "/settings/mcp", ready: "#mcpSettingsPage .mcp-setup-grid" },
  { name: "Library health", url: "/settings", ready: "#healthCards .panel:not([aria-busy])" },
  { name: "Sources", url: "/settings/sources", ready: "#sourceGrid .source-card" },
  { name: "Capture & index", url: "/settings/sync", ready: "#syncDashboard .sync-panel table" },
  { name: "Optimization settings", url: "/settings/optimization", ready: "#optimizationSettingsPage .findings-panel" },
  { name: "Header settings", url: "/settings/header", ready: "#headerActive .header-tool" },
  { name: "App preferences", url: "/settings/preferences", ready: "#optionalBehaviors .optional-behavior" },
  { name: "Workspace overview", url: work, ready: ".delegation-table tbody tr" },
  { name: "Workspace parallel activity", url: `${work}?explorer=parallel`, ready: ".delegation-parallel" },
  { name: "Long transcript", url: `${work}/conversation/${rootConversation}`, ready: ".conversation-panel .turn" },
  { name: "Child transcript", url: `${work}/conversation/conv-nested`, ready: ".conversation-panel .turn" },
  { name: "Quick search", url: "/library", ready: "#queryTableLibrary .qt-row", open: page => page.keyboard.press("ControlOrMeta+k"), shown: "#searchDialog[open]" },
  { name: "Analyze summary", url: "/library", ready: "#queryTableLibrary .qt-row", open: page => page.locator("#usageSummaryToggle").click(), shown: "#usageSummary" },
  { name: "Bookmarks drawer", url: "/library", ready: "#queryTableLibrary .qt-row", preferences: { "pharos-show-nav-button": true }, open: async page => { await page.locator("#navigatorToggle").click(); await page.locator("#bookmarksToggle").click(); }, shown: "#bookmarksDrawer.open" },
  { name: "History drawer", url: "/library", ready: "#queryTableLibrary .qt-row", preferences: { "pharos-show-nav-button": true }, open: async page => { await page.locator("#navigatorToggle").click(); await page.locator("#historyToggle").click(); }, shown: "#historyDrawer.open" },
];
const WIDTHS = [{ width: 1440, height: 900 }, { width: 390, height: 844 }, { width: 320, height: 640 }];

// Layout bugs found by this sweep and not fixed yet. A case whose only
// problems match here is marked todo with the bug, so the suite stays honest;
// any other problem still fails it.
const KNOWN = [];

for (const [name, chart, requestCount] of [
  ["text sources", { split: "kind" }, 1],
  ["smoothed repositories", { split: "repository_name", rollingPeriods: 7 }, 2],
  ["hourly", { split: "kind", period: "hour" }, 1],
]) test(`Human Words relative dates settle across renders and renew on refresh (${name})`, { skip }, async t => {
  const query = days => ({ ...EMPTY_QUERY, where: [
    { field: "last_message_at", op: ">=", value: `-${days}d` },
    { field: "first_message_at", op: "<", value: "+0s" },
  ] });
  const params = new URLSearchParams({ analyze_mode: "dashboards", analyze_topic: "human", analyze_filters: encodeQuery(query(7)), analyze_chart: JSON.stringify({ period: "day", ...chart }) });
  const app = await open(t, { url: `/analyze?${params}`, ready: ".writing-panel .usage-chart" });
  const requests = () => app.calls.filter(call => call.path === "/api/query/writing/series");
  assert.equal(requests().length, requestCount, "loading and data renders complete without refetching");
  const initial = requests().map(call => call.body);
  await app.page.clock.setFixedTime(new Date(Date.now() + 86400000));
  await app.page.getByRole("combobox", { name: "Metric", exact: true }).selectOption("all");
  await app.page.setViewportSize({ width: 390, height: 844 });
  await app.settled();
  assert.equal(requests().length, requestCount, "presentation changes keep the resolved request bounds");

  const refreshed = app.page.waitForResponse(response => response.url().endsWith("/api/query/writing/series"));
  await app.page.evaluate(() => window.dispatchEvent(new Event("pharos:usage-refresh")));
  await refreshed;
  await app.settled();
  assert.equal(requests().length, requestCount * 2);
  assert.ok(requests().slice(requestCount).every(call => call.body.end > initial[0].end), "refresh renews the relative end");

  const filtered = app.page.waitForResponse(response => response.url().endsWith("/api/query/writing/series"));
  await app.page.evaluate(filters => {
    const url = new URL(location.href); url.searchParams.set("analyze_filters", filters);
    history.replaceState(history.state, "", url); window.dispatchEvent(new Event("pharos:route"));
  }, encodeQuery(query(30)));
  await filtered;
  await app.settled();
  assert.equal(requests().length, requestCount * 3);
  assert.ok(requests().slice(requestCount * 2).some(call => call.body.where.some(term => term.value === "-30d")), "filter changes issue the new request");
});

describe("structure", { concurrency: Number(process.env.PHAROS_SWEEP_CONCURRENCY || 3) }, () => {
  for (const route of ROUTES) {
    for (const viewport of WIDTHS) {
      const name = `${route.name} @ ${viewport.width}`;
      test(name, { skip }, async t => {
        const app = await open(t, { url: route.url, viewport, ready: route.ready, preferences: route.preferences, concurrency: route.concurrency, api: route.api });
        if (route.open) {
          await route.open(app.page);
          await app.page.locator(route.shown).first().waitFor();
          await app.settled();
        }
        await app.shot("top");
        const problems = await layoutProblems(app.page);
        if (!route.open) {
          problems.push(...await scrollProblems(app.page));
          await app.settled();
          await app.shot("scrolled");
          problems.push(...(await layoutProblems(app.page)).filter(problem => !problems.includes(problem)));
        }
        problems.push(...app.problems());
        const known = KNOWN.filter(bug => problems.some(problem => bug.match.test(problem)));
        if (problems.some(problem => !known.some(bug => bug.match.test(problem)))) assert.fail(`${name} (${route.url}):\n  ${problems.join("\n  ")}`);
        if (known.length) t.todo(known.map(bug => bug.bug).join("; "));
      });
    }
  }
});

// Source links must open lazy tool bodies and paired results, including a
// retained message containing several provider tool-use blocks.
test("Usage source links reveal and highlight the exact call, result, or nearest event", { skip }, async t => {
  const time = "2026-10-21T10:00:00Z";
  const messages = [
    { id: "prompt", native_id: "prompt", role: "user", kind: "message", text: "Inspect both files", created_at: time },
    { id: "multi", native_id: "multi", role: "assistant", kind: "message", created_at: "2026-10-21T10:00:10Z", text: JSON.stringify({ message: { content: [
      { type: "tool_use", id: "call-one", name: "Read", input: { file_path: "/one" } },
      { type: "tool_use", id: "call-two", name: "Read", input: { file_path: "/two" } },
    ] } }) },
    ...["one", "two"].map((name, index) => ({ id: `result-${name}`, native_id: `result-${name}`, role: "tool", kind: "tool_result", call_id: `call-${name}`, text: JSON.stringify({ content: `Content of ${name}` }), created_at: `2026-10-21T10:00:${20 + index}Z` })),
    { id: "next", native_id: "next", role: "user", kind: "message", text: "Continue", created_at: "2026-10-21T11:00:00Z" },
  ];
  messages.find(message => message.id === "result-two").text_clipped = true;
  const api = {
    "GET /api/work/:id/conversations/:conversation": { id: rootConversation, provider: "claude", messages },
    "GET /api/messages/:id": { text: JSON.stringify({ content: "Content of two · full retained output" }) },
  };
  const { page, settled } = await open(t, { url: `${work}/conversation/${rootConversation}?message=result-two&call=call-two`, api });
  const result = page.locator('.authorship-target[data-message-id="result-two"][data-call-id="call-two"]');
  await result.waitFor({ state: "visible" });
  await settled();
  await page.waitForFunction(() => document.querySelector('.authorship-target[data-message-id="result-two"]')?.textContent.includes("full retained output"));
  assert.match(await result.textContent(), /Content of two/);
  assert.equal(new URL(page.url()).searchParams.get("message"), "result-two");
  await page.evaluate(href => window.pharosNavigate(href), `${work}/conversation/${rootConversation}?message=multi&call=call-two`);
  const call = page.locator('.event-body.authorship-target[data-message-id="multi"][data-call-id="call-two"]');
  await call.waitFor({ state: "visible" });
  assert.match(await call.textContent(), /\/two/);
  await page.evaluate(href => window.pharosNavigate(href), `${work}/conversation/${rootConversation}?at=2026-10-21T10%3A00%3A20Z`);
  await page.locator('.authorship-target[data-message-id="result-one"]').waitFor({ state: "visible" });
  assert.equal(new URL(page.url()).searchParams.get("at"), "2026-10-21T10:00:20Z");
});
