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
import { readFileSync } from "node:fs";
import { activateHeaderTool, open, skip } from "./harness.mjs";
import { layoutProblems, scrollProblems } from "./layout.mjs";
import { defaultApi, cloudScheduledStatus, rootConversation, workID } from "./fixtures.mjs";
import { EMPTY_QUERY, encodeQuery, withGroupingKeys } from "@pythia-software/query-table-core";

const work = `/work/${workID}`;
const exploreGroupedSearch = `/library?${new URLSearchParams({ q_library: encodeQuery(withGroupingKeys({ ...EMPTY_QUERY, select: [{ field: "repository_name" }], where: [{ field: "repository_name", op: "=", value: "repo-1" }] }, ["repository_name"])), search: "cache", text_plain: "1" })}`;
const analyzeFilteredQuery = { ...EMPTY_QUERY, select: [{ field: "tool_name" }, { field: "duration_ms" }], where: [{ field: "tool_name", op: "=", value: "Bash" }] };
const analyzeGroupedQuery = { ...withGroupingKeys(analyzeFilteredQuery, ["tool_name"]), where: [{ field: "tool_name", op: "!=", value: "Read" }] };
const recordKeyword = { field: "command", draft: {
  editor: "visual", match: "rules", text: "", mode: "advanced", fuzzy: false, caseSensitive: false, separators: false,
  root: { id: "group-record", kind: "group", join: "all", exclude: false, children: [{ id: "rule-record", kind: "words", text: "target", other: "", distance: "8", exclude: false }] },
  scope: { surface: "prose", roles: [], kinds: [], repository: "", provider: "", source: "", from: "", to: "", conversation_id: "", workspace_id: "" },
} };
const askSelection = {provider:"Codex",model:"test-model",effort:"Low"};
const askProviders = [{id:"Codex",models:[{id:"test-model",label:"Test Model",efforts:["Default","Low","High"],default:true}]}];
const askSession = {id:"saved-chat",createdAt:Date.now()-60000,updatedAt:Date.now(),selection:askSelection,draft:"",conversation:[{id:"question-1",prompt:"Where am I wasting time?",cli:"Codex",model:"test-model",modelLabel:"Test Model",effort:"Low",status:"multiple",message:"",elapsedMs:6000,reply:"Here are two useful views to explore.",cards:[{id:"tools",title:"Which tools keep me waiting?",kind:"Analyze",description:"Inspect recorded tool time and the original calls.",href:"/analyze?analyze_topic=tools"},{id:"findings",title:"Opportunities to save time",kind:"Findings",description:"Review recurring failures and repeated instructions.",href:"/findings"}]}]};
const askAPI = {
 "GET /api/ask/status":{settings:{enabled:true,showInvitation:true,selection:askSelection},providers:askProviders},
 "GET /api/ask/models":askProviders[0],
 "GET /api/ask/sessions":{sessions:[askSession]},
 "GET /api/ask/session":askSession,
 "POST /api/ask/visits":{ok:true},
 "POST /api/ask/session":({body})=>({...askSession,draft:body.draft}),
};
const askClarificationSession={...askSession,conversation:[{...askSession.conversation[0],status:'clarification',cards:[],reply:'',clarification:{question:'By “use”, do you mean model tokens, API cost equivalent, or something else?',options:['Model tokens','API cost equivalent']}}]};
const ROUTES = [
  ...["dark","light"].flatMap(theme => [
  {name:"Ask Pharos removed CLI",url:"/",ready:"#pharos-ask .home-search-trigger",api:{"GET /api/ask/status":{settings:{enabled:true,showInvitation:true,selection:askSelection},providers:[]}}},
  {name:"Ask Pharos opted out",url:"/",ready:"#pharos-ask .home-search-trigger",api:{"GET /api/ask/status":{settings:{enabled:false,showInvitation:false},providers:askProviders}}},
  {name:"Ask Pharos ready",url:"/",ready:"#pharos-ask .pharos-ai:not([hidden])",api:askAPI},
  {name:"Ask Pharos model picker",url:"/",ready:"#pharos-ask .pharos-ai:not([hidden])",api:askAPI,open:page=>page.locator("#pharos-ask .pharos-model-trigger").click(),shown:"#pharos-model-menu:not([hidden])"},
  {name:"Ask Pharos history",url:"/",ready:"#pharos-ask .pharos-ai:not([hidden])",api:askAPI,open:page=>page.locator("#pharos-ask .pharos-history-trigger").click(),shown:"#pharos-chat-history:not([hidden])"},
  {name:"Ask Pharos conversation",url:"/?ask_chat=saved-chat",ready:"#pharos-ask .pharos-turn",api:askAPI},
  {name:"Ask Pharos clarification",url:"/?ask_chat=saved-chat",ready:"#pharos-ask .pharos-clarification:not([hidden])",api:{...askAPI,"GET /api/ask/session":askClarificationSession}},
  {name:"Ask Pharos opt-in",url:"/",ready:"#pharos-ask .pharos-invitation:not([hidden])",api:{"GET /api/ask/status":{settings:{enabled:false,showInvitation:true},providers:askProviders}},open:page=>page.locator("#pharos-ask .pharos-try").click(),shown:"#pharos-ask .pharos-consent[open]"},
  {name:"Ask Pharos settings",url:"/settings/ask",ready:"#askSettingsPage .ask-settings select",api:askAPI},

  ].map(route=>({...route,name:`${route.name} (${theme})`,preferences:{"pharos-theme":theme}}))),
  { name: "Data guide", url: "/analyze?analyze_mode=query&analyze_data=tool_calls&analyze_topic=tools", ready: ".usage-workspace-table .qt-row", open: page => page.locator(".usage-workspace-table .data-source-link").click(), shown: ".data-guide[open]" },
  ...["usage", "writing", "writing_messages", "conversation_activity", "tool_calls", "tools", "skill_usages", "mcp_calls", "parallelism"].flatMap(dataset => [false, true].map(explore => ({
    name: `Query ideas ${explore ? "Explore " : ""}${dataset}`,
    url: `/analyze?analyze_mode=query&analyze_data=${dataset}`,
    ready: ".usage-workspace-table .qt-row",
    open: async page => {
      const chip = explore ? page.getByRole("group", { name: "Explore query ideas", exact: true }).getByRole("button").last() : page.locator(".usage-query-ideas .query-chip").first();
      await chip.click();
      await page.getByRole("link", { name: "Matching records", exact: true }).first().waitFor();
    },
    shown: ".usage-workspace-table .qt-row",
  }))),
  ...['provisional', 'partial'].flatMap(state => [false, true].map(expanded => ({
    name: `Indexing forecast (${state}${expanded ? ', details' : ''})`, url: '/library', ready: '#queryTableLibrary .qt-row',
    api: {'GET /api/library/status': {...defaultApi['GET /api/library/status'], idle: false, activities: [{id: 'index-forecast', kind: 'sync', label: 'Manual sync in progress', started_at: new Date(Date.now() - 120_000).toISOString(),
      stoppable: true, stop_path: '/api/index/cancel', detail: 'Codex · 0 of 4 sources finished',
      work: {phase: state === 'provisional' ? 'indexing' : 'discovering inputs', source: 'Codex', unit: 'conversation group', done: state === 'provisional' ? 42 : 0, total: state === 'provisional' ? 100 : 0, next: 'Tools, authorship and findings'},
      estimate: state === 'provisional' ? {state: 'estimating', low_seconds: 60, high_seconds: 120, basis: 'Recent pace of the current step'} : {state: 'unknown', basis: 'Discovering inputs before the stage count is available'},
      forecast: {state, current_step: 4, total_steps: 8, progress: state === 'provisional' ? .63 : undefined, low_seconds: state === 'provisional' ? 180 : 0, high_seconds: state === 'provisional' ? 600 : 0, predicted_steps: state === 'provisional' ? 4 : 3, remaining_steps: 4, basis: 'Remaining sources and analyses on this Mac', unknown_steps: state === 'partial' ? ['Classify authorship for retained conversations'] : []}}]}},
    open: async page => {
      await page.evaluate(() => { window.webkit = {messageHandlers: {pharosLibrary: {postMessage: async () => {}}}}; });
      await activateHeaderTool(page, 'pharosDrive');
      if (expanded) await page.locator('#pharosDrivePanel summary').click();
    }, shown: '#pharosDrivePanel .pharos-activity',
  }))),
  ...[false, true].map(expanded => ({
    name: `Background sync${expanded ? ', details' : ''}`, url: '/library', ready: '#queryTableLibrary .qt-row',
    api: {'GET /api/library/status': {...defaultApi['GET /api/library/status'], idle: false, activities: [
      {kind: 'maintenance', label: 'Indexing the Library view', detail: '2 workspaces to index', stoppable: true, stop_path: '/api/library/refresh/stop'},
      {kind: 'maintenance', label: 'Updating findings', detail: 'Measuring the findings you are watching'},
    ]}},
    open: async page => {
      await activateHeaderTool(page, 'pharosDrive');
      if (expanded) await page.locator('#pharosDrivePanel summary').click();
    }, shown: '#pharosDrivePanel .pharos-activity',
  })),
  { name: 'Idle drive overview', url: '/library', ready: '#queryTableLibrary .qt-row',
    open: page => activateHeaderTool(page, 'pharosDrive'), shown: '#pharosDrivePanel',
  },
  { name: "Home", url: "/", ready: ".home-belt-row .query-card" },
  { name: "Home search composer", url: "/", ready: ".home-search-trigger", open: page => page.getByRole("button", { name: "Search past work", exact: true }).click(), shown: ".home-search-disclosure.is-expanded" },
  { name: "Home search before date", url: "/", ready: ".home-search-trigger", open: async page => { await page.getByRole("button", { name: "Search past work", exact: true }).click(); await page.getByRole("combobox", { name: "Date comparison", exact: true }).selectOption("to"); }, shown: ".home-search-disclosure.is-expanded" },
  ...["start", "end"].map(bound => ({ name: `Home search date range adding ${bound}`, url: "/", ready: ".home-search-trigger", open: async page => {
    await page.getByRole("button", { name: "Search past work", exact: true }).click();
    if (bound === "start") await page.getByRole("combobox", { name: "Date comparison", exact: true }).selectOption("to");
    await page.getByRole("button", { name: `Add ${bound} date`, exact: true }).click();
    await page.getByLabel("Date on or after", { exact: true }).fill("2026-10-01");
    await page.getByLabel("Date on or before", { exact: true }).fill("2026-10-22");
  }, shown: ".home-search-disclosure.is-expanded" })),
  { name: "Card Library", url: "/cards", ready: ".card-library-entry .query-card" },
  { name: "All-time writing hours", url: "/cards?cards=library&card_search=writing-hours", ready: '[data-card-id="writing-hours"] .human-study-hour-bars' },
  { name: "Daily repository writing", url: "/cards?cards=library&card_search=human-repository-words-trend", ready: '[data-card-id="human-repository-words-trend"] .query-cover-columns' },
  { name: "Weekday distributions", url: "/cards?cards=library&card_search=day%20of%20the%20week", ready: '[data-card-id="human-words-weekday"] .query-shared-box' },
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
  { name: "Analyze parallelism", url: "/analyze?analyze_view=tokens&machine_view=parallelism", ready: ".concurrency-chart svg" },
  { name: "Analyze parallelism prototype", concurrency: true, url: "/analyze?analyze_view=tokens&machine_view=parallelism", ready: ".concurrency-chart svg" },
  { name: "Human Words", url: "/analyze?analyze_view=writing", ready: ".writing-panel .usage-chart" },
  { name: "Human Words messages", url: "/analyze?analyze_view=writing&writing=messages", ready: "#queryTableUsage .qt-row" },
  { name: "Carbon Impact", url: "/analyze?analyze_view=carbon", ready: "#carbonSummary" },
  { name: "Analyze conversations", url: "/analyze?analyze_view=conversations", ready: "#queryTableUsage .qt-row" },
  { name: "Analyze workspace dashboards", url: "/analyze", ready: ".usage-dashboard .usage-chart" },
  ...['tokens', 'human', 'skills', 'time', 'carbon'].map(topic => ({
    name: `Dashboard pinned key ${topic}`, url: `/analyze?analyze_topic=${topic}`, ready: '.usage-chart-bars:not(.loading) > button',
    open: async page => {
      const chart = page.locator('.usage-dashboard .usage-chart').first();
      await chart.locator('.usage-chart-bars > button').filter({ has: page.locator('i') }).first().focus();
      await page.keyboard.press('Enter');
      await chart.getByRole('group', { name: /^Details for / }).getByRole('link').first().waitFor();
    }, shown: '.usage-chart-tip.pinned',
  })),
  { name: "Analyze relative query previews", url: `/analyze?${new URLSearchParams({ analyze_topic: "tokens", analyze_filters: encodeQuery({ ...EMPTY_QUERY, where: [{ field: "first_usage_at", op: ">=", value: "-30d" }, { field: "first_usage_at", op: "<", value: "+0s" }, { field: "provider", op: "=", value: "codex" }] }) })}`, ready: ".usage-query-destinations" },
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
  { name: "Analyze workspace parallelism dashboard", url: "/analyze?analyze_workspace=1&analyze_topic=parallelism", ready: ".usage-dashboard .concurrency-chart svg" },
  { name: "Analyze workspace carbon dashboard", url: "/analyze?analyze_workspace=1&analyze_topic=carbon", ready: ".usage-dashboard #carbonSummary" },
  ...["usage", "conversation_activity", "writing", "writing_messages", "tool_calls", "tools", "mcp_calls", "skill_usages", "parallelism"].map(dataset => ({
    name: `Analyze workspace query ${dataset}`, url: `/analyze?analyze_mode=query&analyze_data=${dataset}`, ready: ".usage-workspace-table .qt-row", fitTable: true,
  })),
  { name: "Analyze record keyword editor", url: "/analyze?analyze_mode=query&analyze_data=tool_calls", ready: ".usage-workspace-table .qt-qb", open: page => page.getByRole("button", { name: /^Add keyword filter for / }).click(), shown: ".explore-search-modal" },
  { name: "Analyze record keyword condition", url: `/analyze?${new URLSearchParams({ analyze_mode:"query", analyze_data:"tool_calls", record_tool_calls:JSON.stringify(recordKeyword) })}`, ready: ".usage-workspace-table .qt-qb" },
  { name: "Analyze workspace legacy search link", url: "/analyze?analyze_workspace=1&analyze_mode=search&analyze_data=tool_calls", ready: ".usage-workspace-table .qt-row" },
  ...[["filtered", analyzeFilteredQuery], ["grouped", analyzeGroupedQuery]].map(([label, query]) => ({
    name: `Analyze workspace ${label} query`, url: `/analyze?${new URLSearchParams({ analyze_mode: "query", analyze_data: "tool_calls", q_tool_calls: encodeQuery(query) })}`, ready: ".usage-workspace-table .qt-qb",
    open: page => page.getByRole("button", { name: "Run Now", exact: true }).click(), shown: ".usage-workspace-table .qt-row",
  })),
  { name: "Analyze fresh", url: "/analyze?fresh=1&analyze_view=tokens&machine_view=tokens&activity_days=0&activity_repo=&activity_provider=&activity_model=", preferences: { "pharos-usage-view": "writing", "pharos-machine-view": "time", "pharos-machine-scope": { days: 7, repository: "missing", provider: "missing", model: "missing" } }, ready: "#queryTableUsage .qt-row" },
  { name: "Analyze query recovery", url: "/analyze?analyze_view=tokens&machine_view=tokens&q_usage=invalid", ready: ".query-recovery", open: page => page.getByRole("button", { name: "Reset table", exact: true }).click(), shown: "#queryTableUsage .qt-row" },
  { name: "Tool calls", url: "/tools", ready: "#toolsPage .qt-row" },
  { name: "Tool summary", url: "/tools?tools_view=summary", ready: "#toolsPage .qt-row" },
  { name: "Skills", url: "/tools?tools_view=skills", ready: ".skill-usages .qt-row" },
  { name: "Findings", url: "/findings", ready: ".findings-card" },
  { name: "Findings table", url: "/findings?view=table", ready: ".findings-table .qt-row" },
  { name: "Findings settings", url: "/findings?view=settings", ready: ".findings-settings .findings-panel" },
  { name: "Finding detail", url: "/findings?finding=drift%3Arepo%3Arepo_tl1%3Aghi", ready: ".findings-detail svg" },
  { name: "MCP dashboard", url: "/analyze?analyze_topic=mcp", ready: ".usage-mcp-dashboard .qt-metric-bar-row" },
  { name: "MCP trends", url: `/analyze?analyze_topic=mcp&analyze_chart=${encodeURIComponent(JSON.stringify({ metric: "failure_rate", split: "tool_name", view: "trend", period: "week", rollingPeriods: 37 }))}`, ready: ".usage-mcp-dashboard .qt-metric-mark" },
  { name: "MCP query", url: "/analyze?analyze_mode=query&analyze_data=mcp_calls&analyze_topic=mcp&analyze_search=1", ready: ".usage-workspace-table .qt-row" },
  { name: "MCP legacy link", url: "/mcp", ready: ".usage-mcp-dashboard .qt-metric" },
  { name: "MCP settings", url: "/settings/mcp", ready: "#mcpSettingsPage .mcp-setup-grid" },
  { name: "Library health", url: "/settings", ready: "#healthCards .panel:not([aria-busy])" },
  { name: "Cloud backup entry", url: "/settings/backups", ready: "#cloud-explore" },
  { name: "Cloud backup ongoing status", url: "/settings/backups", ready: ".cloud-ongoing article", api:{'GET /api/cloud-backup':cloudScheduledStatus} },
  { name: "Cloud backup schedule settings", url: "/settings/backups", ready: ".cloud-ongoing article", api:{'GET /api/cloud-backup':cloudScheduledStatus},open:page=>page.locator('.cloud-schedule-settings summary').click(),shown:'.cloud-schedule-settings .cloud-fields' },
  { name: "Cloud backup previous revisions", url: "/settings/backups", ready: ".cloud-ongoing article", api:{'GET /api/cloud-backup':cloudScheduledStatus},open:page=>page.getByRole('button',{name:'View backups and test restore',exact:true}).click(),shown:'.cloud-history-select' },
  { name: "Cloud backup calculator", url: "/settings/backups", ready: "#cloud-explore", open: page => page.getByRole('button', {name: 'Explore cloud backup options', exact: true}).click(), shown: ".cloud-results svg" },
  { name: "Cloud backup minimum storage breakdown", url: "/settings/backups", ready: "#cloud-explore", open: async page => {
    await page.getByRole('button', {name: 'Explore cloud backup options', exact: true}).click();
    await page.getByText('Detailed options', {exact: true}).click();
    await page.getByLabel('Storage class', {exact: true}).selectOption('gcp-archive');
    await page.getByText('Detailed options', {exact: true}).click();
  }, shown: '.cloud-results svg' },
  { name: "Cloud backup detailed options", url: "/settings/backups", ready: "#cloud-explore", open: async page => {
    await page.getByRole('button', {name: 'Explore cloud backup options', exact: true}).click();
    await page.getByText('Detailed options', {exact: true}).click();
  }, shown: "#cloud-region" },
  { name: "Cloud backup configuration", url: "/settings/backups", ready: "#cloud-explore", open: async page => {
    await page.getByRole('button', {name: 'Explore cloud backup options', exact: true}).click();
    await page.getByRole('button', {name: 'Configure this backup', exact: true}).click();
  }, shown: "#cloud-step-1" },
  { name: "Cloud backup commands", url: "/settings/backups", ready: "#cloud-explore", open: async page => {
    await page.getByRole('button', {name: 'Explore cloud backup options', exact: true}).click();
    await page.getByRole('button', {name: 'Configure this backup', exact: true}).click();
    await page.getByLabel('Google Cloud project ID', {exact: true}).fill('pharos-example');
    await page.getByLabel('Bucket location', {exact: true}).selectOption('europe-west2');
    await page.getByLabel('Google Cloud Storage · Archive', {exact: true}).check();
    await page.getByRole('button', {name: 'Review configuration & commands', exact: true}).click();
  }, shown: "#cloud-step-2 pre" },
  { name: "Cloud backup new GCP project", url: "/settings/backups", ready: "#cloud-explore", open: async page => {
    await page.getByRole('button', {name: 'Explore cloud backup options', exact: true}).click();
    await page.getByRole('button', {name: 'Configure this backup', exact: true}).click();
    await page.getByLabel('Google Cloud project', {exact: true}).selectOption('new');
    await page.getByLabel('Billing account ID', {exact: true}).fill('012ABC-345DEF-678ABC');
    await page.getByText('Schedule and advanced settings', {exact: true}).click();
  }, shown: '#cloud-projectParent' },
  { name: "Cloud backup password commands", url: "/settings/backups", ready: "#cloud-explore", open: async page => {
    await page.getByRole('button', {name: 'Explore cloud backup options', exact: true}).click();
    await page.getByRole('button', {name: 'Configure this backup', exact: true}).click();
    await page.getByLabel('Google Cloud project ID', {exact: true}).fill('pharos-example');
    await page.getByLabel('Require a password to restore', {exact: true}).selectOption('true');
    await page.getByRole('button', {name: 'Review configuration & commands', exact: true}).click();
  }, shown: '.cloud-protected-commands' },
  { name: "Cloud backup Azure commands", url: "/settings/backups", ready: "#cloud-explore", open: async page => {
    await page.getByRole('button', {name: 'Explore cloud backup options', exact: true}).click();
    await page.getByLabel('Cloud provider', {exact: true}).selectOption('azure');
    await page.getByRole('button', {name: 'Configure this backup', exact: true}).click();
    await page.getByLabel('Azure subscription', {exact: true}).fill('Backup subscription');
    await page.getByLabel('Azure Blob Storage · Cold', {exact: true}).check();
    await page.getByRole('button', {name: 'Review configuration & commands', exact: true}).click();
  }, shown: "#cloud-step-2 pre" },
  ...[[4,"Test"],[5,"First backup"],[6,"Test restore"]].map(([step,label])=>({
    name:`Cloud backup ${label.toLowerCase()}`,url:"/settings/backups",ready:"#cloud-explore",
    open:async page=>{
      await page.getByRole('button',{name:'Explore cloud backup options',exact:true}).click();
      await page.getByRole('button',{name:'Configure this backup',exact:true}).click();
      await page.getByLabel('Google Cloud project ID',{exact:true}).fill('pharos-example');
      await page.getByRole('button',{name:`Step ${step}: ${label}`,exact:true}).click();
    },shown:`#cloud-step-${step-1}`
  })),
  { name: "Sources", url: "/settings/sources", ready: "#sourceGrid .source-card" },
  { name: "Capture & index", url: "/settings/sync", ready: "#syncHistoryTables .qt-row" },
  { name: "Capture & index diagnostics", url: "/settings/sync", ready: "#syncHistoryTables .qt-row", open: page => page.getByText("Performance & verification details", { exact: true }).click(), shown: "#syncDiagnosticTables .qt-row" },
  { name: "Capture & index run details", url: "/settings/sync", ready: "#syncHistoryTables .qt-row", open: page => page.getByRole("region", { name: "Recent run history", exact: true }).getByRole("button", { name: "Details", exact: true }).first().click(), shown: ".sync-run-dialog .qt-row" },
  { name: "Capture & index empty", url: "/settings/sync", ready: "#syncHistoryTables .qt-empty", api: { "GET /api/sync/status": { ...defaultApi["GET /api/sync/status"], history: [], source_states: [] } } },
  { name: "Legacy Findings settings", url: "/settings/optimization", ready: ".findings-settings .findings-panel" },
  { name: "Header settings", url: "/settings/header", ready: "#headerActive .header-tool" },
  { name: "App preferences", url: "/settings/preferences", ready: "#optionalBehaviors .optional-behavior" },
  { name: "Mac menu syncing", url: "/settings/preferences", ready: "#menuBarActive .menu-bar-detail", api: {
    'GET /api/menu-bar/status': {...defaultApi['GET /api/menu-bar/status'], library: {activities: [{label: 'Manual sync: evaluating findings while checking every captured conversation and its retained tool results', progress: .22, estimated_completion_at: new Date(Date.now() + 60000).toISOString()}]}},
  } },
  { name: "Mac menu with all items", url: "/settings/preferences", ready: "#menuBarActive button", preferences: { 'pharos-menu-bar': { cost: true, backup: true, space: true, index: true, eject: true, search: true, analyze: true, optimize: true, capture: true, health: true, sources: true, finder: true } } },
  { name: "Workspace overview", url: work, ready: ".delegation-table tbody tr" },
  { name: "Workspace parallel activity", url: `${work}?explorer=parallel`, ready: ".delegation-parallel" },
  { name: "Long transcript", url: `${work}/conversation/${rootConversation}`, ready: ".conversation-panel .turn" },
  { name: "Child transcript", url: `${work}/conversation/conv-nested`, ready: ".conversation-panel .turn" },
  { name: "Quick search", url: "/library", ready: "#queryTableLibrary .qt-row", open: page => page.keyboard.press("ControlOrMeta+k"), shown: "#searchDialog[open]" },
  { name: "Analyze summary", url: "/library", ready: "#queryTableLibrary .qt-row", open: page => activateHeaderTool(page, "usageSummaryToggle"), shown: "#usageSummary" },
  { name: "Clickable Analyze summary", url: "/library", preferences: { "pharos-usage-summary-links": true }, ready: "#queryTableLibrary .qt-row", open: page => activateHeaderTool(page, "usageSummaryToggle"), shown: "#usageSummary" },
  { name: "App navigation", url: "/analyze", ready: ".usage-dashboard .usage-chart", open: async page => {
    if (await page.locator('#appNavigationToggle').isVisible()) await page.locator('#appNavigationToggle').click();
    else {
      await page.getByRole('button', { name: 'Collapse sidebar', exact: true }).click();
      await page.getByRole('button', { name: 'Expand sidebar', exact: true }).click();
    }
  }, shown: '#appNavigationRail:not([hidden]), #appNavigationDialog[open]' },
  { name: "Bookmarks drawer", url: "/library", ready: "#queryTableLibrary .qt-row", preferences: { "pharos-show-nav-button": true }, open: async page => { await activateHeaderTool(page, "navigatorToggle"); await page.locator("#bookmarksToggle").click(); }, shown: "#bookmarksDrawer.open" },
  { name: "History drawer", url: "/library", ready: "#queryTableLibrary .qt-row", preferences: { "pharos-show-nav-button": true }, open: async page => { await activateHeaderTool(page, "navigatorToggle"); await page.locator("#historyToggle").click(); }, shown: "#historyDrawer.open" },
];
const WIDTHS = [{ width: 1440, height: 900 }, { width: 1280, height: 720 }, { width: 1024, height: 768 }, { width: 390, height: 844 }, { width: 320, height: 640 }];

// Layout bugs found by this sweep and not fixed yet. A case whose only
// problems match here is marked todo with the bug, so the suite stays honest;
// any other problem still fails it.
const KNOWN = [{
  bug: "Query Table 0.9.5 fit-to-screen can clip native Detail and JSON buttons at phone widths; docs/handoffs/query-table-fit-screen-controls.md",
  when: (route, viewport) => route.fitTable && viewport.width < 641,
  match: /a control is cut off where nothing scrolls: button\.qt-(?:link-btn\.qt-item-detail-button|json-preview)/,
}];

for (const width of [1440, 390, 320]) test(`Search filter suggestions stay readable and keep input focus @ ${width}`, { skip }, async t => {
  const values = ["exploration", "exploration-service", "exploration-service-v2", "gbw-utilities", "general-purpose-repository",
    "go-query-table-integration-with-a-long-unbroken-suffix-abcdefghijklmnopqrstuvwxyz"];
  const query = { ...EMPTY_QUERY, where: [{ field: "repository_name", op: "=", value: "" }] };
  const app = await open(t, {
    url: `/library?${new URLSearchParams({ q_library: encodeQuery(query) })}`,
    viewport: { width, height: 844 }, ready: "#queryTableLibrary .qt-qb",
    api: { "GET /api/query/library/distinct": ({ query }) => ({ values: values.filter(value => value.includes(query.get("q") ?? "")), hasMore: false }) },
  });
  const { page } = app;
  const edit = page.getByRole("button", { name: /^Edit filter: Repository/ });
  if (width < 641) await edit.click();
  const input = page.getByRole("combobox", { name: "Value for Repository", exact: true });
  await input.focus();
  const suggestions = page.getByRole("listbox", { name: "Suggestions for Repository", exact: true });
  const longest = suggestions.getByRole("option", { name: values.at(-1), exact: true });
  await longest.waitFor();
  await app.settled();
  assert.ok((await suggestions.boundingBox()).width > (await input.boundingBox()).width, "suggestions are squeezed to the input width");
  assert.deepEqual(await layoutProblems(page), []);
  for (const option of await suggestions.getByRole("option").all()) {
    assert.ok(await option.evaluate(element => {
      const box = element.getBoundingClientRect();
      return box.left >= 0 && box.right <= innerWidth && box.top >= 0 && box.bottom <= innerHeight &&
        element.scrollWidth <= element.clientWidth && element.scrollHeight <= element.clientHeight;
    }), `suggestion is clipped: ${await option.textContent()}`);
  }
  await app.shot("menu");
  await input.press("Escape");
  await suggestions.waitFor({ state: "hidden" });
  assert.ok(await input.evaluate(element => document.activeElement === element));
  await input.press("ArrowDown");
  await suggestions.getByRole("option", { name: values[2], exact: true }).click();
  assert.equal(await input.inputValue(), values[2]);
  assert.ok(await input.evaluate(element => document.activeElement === element));
});

test('header centers navigation and collects actions before adding a dropdown row', { skip }, async t => {
  const { page, settled, shot } = await open(t, { url: '/library', ready: '#queryTableLibrary .qt-row' });
  const desktopButton = await page.locator('#themeToggle').boundingBox();
  const more = page.getByRole('button', { name: 'More tools', exact: true });
  for (const width of [1440, 1350, 1280, 1180, 1050, 1024, 1001, 1000, 900, 760, 700, 620, 560, 520, 480, 390, 320, 1280]) {
    await page.setViewportSize({ width, height: 900 });
    await settled();
    if (width === 1280 || width === 1350) assert.equal(await more.isVisible(), false, `tools still fit at ${width}px`);
    const bounds = await page.evaluate(() => {
      const tabs = document.querySelector('body > header .tabs');
      const bar = document.querySelector('body > header .bar'), style = getComputedStyle(bar), box = bar.getBoundingClientRect();
      return {
        left: box.left + parseFloat(style.paddingLeft), right: box.right - parseFloat(style.paddingRight), gap: parseFloat(style.columnGap),
        switcher: document.querySelector('#appNavigationToggle')?.checkVisibility() ? document.querySelector('#appNavigationToggle').getBoundingClientRect().toJSON() : null,
        tabs: tabs.getClientRects().length ? tabs.getBoundingClientRect().toJSON() : null,
        brand: document.querySelector('body > header .brand').getBoundingClientRect().toJSON(),
        tools: document.querySelector('body > header .header-icons').getBoundingClientRect().toJSON(),
      };
    });
    const { tabs, brand, tools, switcher, left, right, gap } = bounds;
    assert.ok(tabs ?? switcher, `navigation stays reachable at ${width}px`);
    assert.ok(brand.right <= tools.left, `brand and actions stay apart at ${width}px`);
    const center = rect => (rect.top + rect.bottom) / 2;
    assert.ok(Math.abs(center(brand) - center(tools)) < 1, `brand and actions share the first row at ${width}px`);
    if (tabs) {
      assert.ok(brand.right <= tabs.left && tabs.right <= tools.left, `navigation fits between the edges at ${width}px`);
      assert.ok(Math.abs(center(brand) - center(tabs)) < 1, `tabs stay on the first row at ${width}px`);
      const side = (right - left - tabs.width) / 2 - gap;
      if (brand.width <= side && tools.width <= side) assert.ok(Math.abs((tabs.left + tabs.right - left - right) / 2) < 1, `navigation is centered on wide screens at ${width}px`);
    } else {
      assert.ok(switcher.top >= Math.max(brand.bottom, tools.bottom), `dropdown gets a second row at ${width}px`);
      assert.ok(switcher.left >= left && switcher.right <= right, `dropdown stays within the header at ${width}px`);
    }
    assert.equal(await page.locator('body > header .brand > .brand-words .brand-tagline').isVisible(), true, 'the motto remains visible');
    const button = await page.locator('#themeToggle').boundingBox();
    if (button) {
      assert.equal(button.width, desktopButton.width, 'actions keep their desktop width');
      assert.equal(button.height, desktopButton.height, 'actions keep their desktop height');
    }
    if (await more.isVisible()) {
      const moreBox = await more.boundingBox();
      assert.ok(moreBox.x < (button?.x ?? Infinity), 'More sits to the left of retained actions');
    }
    assert.deepEqual(await layoutProblems(page), []);
    await shot(`header-${width}`);
  }
});

test('header fit responds to tool choices and label changes without resizing', { skip }, async t => {
  const { page, settled } = await open(t, {
    url: '/settings/header', viewport: { width: 1440, height: 900 }, ready: '#headerActive .header-tool',
    preferences: { 'pharos-header-tools-v1': ['library', 'search', 'summary', 'settings', 'theme'] },
  });
  const width = await page.evaluate(() => {
    const bar = document.querySelector('body > header .bar'), style = getComputedStyle(bar);
    return Math.ceil([...bar.querySelectorAll(':scope > .brand, :scope > .tabs, :scope > .header-icons')].reduce((total, element) => total + element.getBoundingClientRect().width, 0)
      + 2 * parseFloat(style.columnGap) + parseFloat(style.paddingLeft) + parseFloat(style.paddingRight) + document.querySelector('#themeToggle').getBoundingClientRect().width / 2);
  });
  await page.setViewportSize({ width, height: 900 });
  await settled();
  const more = page.getByRole('button', { name: 'More tools', exact: true });
  assert.equal(await more.isVisible(), false, 'a smaller tool selection fits');
  const name = page.locator('#headerLibrary .pharos-drive-name');
  const original = await name.textContent();
  await name.evaluate(element => { element.textContent = 'A library with a much longer drive label'; });
  await more.waitFor();
  assert.deepEqual(await layoutProblems(page), []);
  await name.evaluate((element, label) => { element.textContent = label; }, original);
  await more.waitFor({ state: 'hidden' });
  await page.getByRole('button', { name: 'Add Back / forward', exact: true }).click();
  await more.waitFor();
  await page.getByRole('button', { name: 'Arrange Back / forward', exact: true }).focus();
  await page.keyboard.press('Delete');
  await more.waitFor({ state: 'hidden' });
  const navigation = page.getByRole('navigation', { name: 'Primary navigation', exact: true });
  const tab = page.locator('body > header .tabs button[data-view="library"]'), label = await tab.textContent();
  await tab.evaluate(button => { button.textContent = button.textContent.repeat(10); });
  await page.locator('#appNavigationToggle').waitFor();
  assert.deepEqual(await layoutProblems(page), [], 'renamed navigation can switch rows without resizing');
  await tab.evaluate((button, label) => { button.textContent = label; }, label);
  await navigation.waitFor();
  await settled();
});

test("macOS window controls stay clear of the lighthouse when resizing", { skip }, async t => {
  const source = readFileSync(new URL("../../../macos/PharosApp.swift", import.meta.url), "utf8");
  const chromeCSS = source.match(/static let windowChromeScript = """[\s\S]*?style.textContent = `([\s\S]*?)`;/)[1];
  const { page, settled, shot } = await open(t, { url: '/library', ready: '#queryTableLibrary .qt-row' });
  await page.addStyleTag({ content: chromeCSS });
  await page.evaluate(() => {
    document.documentElement.classList.add('pharos-window');
    // AppKit places three 14 pt buttons at x=19, 39 and 59 in a 52 pt bar.
    const controls = document.createElement('div');
    controls.id = 'nativeWindowControls';
    controls.style.cssText = 'position:fixed;left:19px;top:19px;display:flex;gap:6px;z-index:100';
    for (const color of ['#ff5f57', '#febc2e', '#28c840']) {
      const button = document.createElement('span');
      button.style.cssText = `width:14px;height:14px;border-radius:50%;background:${color}`;
      controls.append(button);
    }
    document.body.append(controls);
  });
  for (const width of [1180, 1440, 2133, 800, 620, 390, 320, 1180]) {
    await page.setViewportSize({ width, height: 900 });
    await settled();
    const brand = page.locator('body > header .brand');
    assert.equal(await brand.locator('.brand-mark').isVisible(), true, 'lighthouse stays visible');
    assert.equal(await brand.locator(':scope > .brand-words .brand-tagline').isVisible(), true, 'motto stays visible in the native app');
    const controls = await page.locator('#nativeWindowControls').boundingBox();
    const windowed = await brand.boundingBox();
    assert.ok(windowed.x >= controls.x + controls.width, `lighthouse clears window controls at ${width}px`);
    assert.deepEqual(await layoutProblems(page), []);
    await shot(`windowed-${width}`);

    await page.evaluate(() => {
      document.documentElement.classList.add('pharos-fullscreen');
      document.querySelector('#nativeWindowControls').hidden = true;
    });
    await settled();
    assert.ok((await brand.boundingBox()).x < windowed.x, 'fullscreen reclaims the hidden controls’ space');
    assert.deepEqual(await layoutProblems(page), []);
    await page.evaluate(() => {
      document.documentElement.classList.remove('pharos-fullscreen');
      document.querySelector('#nativeWindowControls').hidden = false;
    });
    await settled();
    assert.ok((await brand.boundingBox()).x >= controls.x + controls.width, 'leaving fullscreen restores clearance');
  }
});

test("dashboard collection wraps after four bookmarked cards", { skip }, async t => {
  const bookmarks = Array.from({ length: 9 }, (_, index) => ({ id: `card-${index}`, title: `Saved view ${index + 1}`, url: `/settings/preferences?card=${index}` }));
  const { page, settled, shot } = await open(t, { url: '/', ready: '.home-collection .query-card', preferences: { 'pharos-bookmarks-v1': bookmarks } });
  const collection = page.getByRole('region', { name: 'Your collection', exact: true });
  for (const width of [2133, 3000, 1180, 800, 390, 320]) {
    await page.setViewportSize({ width, height: 900 });
    await settled();
    const cards = await collection.locator('.query-card').evaluateAll(cards => cards.map(card => {
      const { top, left, right } = card.getBoundingClientRect();
      return { top, left, right };
    }));
    assert.equal(cards.length, bookmarks.length, 'all bookmarked cards remain visible');
    const rows = Map.groupBy(cards, card => Math.round(card.top));
    assert.ok([...rows.values()].every(row => row.length <= 4), `at most four cards per row at ${width}px`);
    if (width >= 1180) assert.equal(rows.values().next().value.length, 4, 'wide windows fit four cards before wrapping');
    assert.ok(cards.every(card => card.left >= 0 && card.right <= width), 'cards stay inside narrow windows');
    assert.deepEqual(await layoutProblems(page), []);
    await shot(`collection-${width}`);
  }
});

test("dashboard catalog stays readable while a preview is pending", { skip }, async t => {
  const { page, settled, shot } = await open(t, { url: '/', ready: '.home-belt-row .query-card', viewport: { width: 1875, height: 1075 } });
  let release, requested;
  const response = new Promise(resolve => { release = resolve; });
  const pending = new Promise(resolve => { requested = resolve; });
  const hold = async route => {
    if (route.request().method() === 'POST') { requested(); await response; }
    await route.continue();
  };
  await page.route('**/api/query/tl1_attempts/*', hold);
  t.after(release);
  try {
    await page.getByRole('button', { name: 'Refresh suggestions', exact: true }).click();
    await pending;
    await page.mouse.move(5, 5);
    const next = page.getByRole('button', { name: 'Next three suggestions', exact: true });
    await page.waitForFunction(() => {
      const button = document.querySelector('button[aria-label="Next three suggestions"]');
      return button && !button.disabled;
    });
    const readable = async () => {
      await page.waitForFunction(() => {
        const titles = [...document.querySelectorAll('.home-belt-row .query-card:not([data-xray]) .query-title')];
        return titles.length && titles.every(title => {
          const box = title.getBoundingClientRect();
          return title.checkVisibility({ opacityProperty: true, visibilityProperty: true }) && box.left >= 0 && box.right <= innerWidth;
        });
      });
      assert.deepEqual(await layoutProblems(page), []);
    };
    await readable();
    await page.emulateMedia({ reducedMotion: 'no-preference' });
    await next.click();
    await page.waitForFunction(() => document.querySelector('.home-belt-row').getAttribute('aria-busy') === 'false');
    await readable();
    // A stale animation value must not move or hide the resting reading area.
    await page.locator('.home-belt-row').evaluate(row => row.style.setProperty('--belt-shift', '-20'));
    await readable();
    await shot('pending-preview');
  } finally {
    release();
    await settled();
    await page.unroute('**/api/query/tl1_attempts/*', hold);
  }
});

for (const width of [1649, 2133]) test(`pages without section menus use the full window at ${width}px`, { skip }, async t => {
  const { page } = await open(t, { url: '/analyze', viewport: { width, height: 1075 }, ready: '.usage-dashboard .usage-chart' });
  const primary = page.getByRole('navigation', { name: 'Primary navigation', exact: true });
  const fillsWindow = async () => {
    await page.waitForFunction(() => {
      const main = document.querySelector('main'), view = main.querySelector('.view.active');
      const box = main.getBoundingClientRect(), content = view.getBoundingClientRect();
      return Math.abs(box.left) < 1 && Math.abs(box.right - innerWidth) < 1 && Math.abs(content.left - box.left) < 1 && Math.abs(content.width - main.clientWidth) < 1;
    });
    assert.deepEqual(await layoutProblems(page), []);
  };
  await primary.getByRole('button', { name: 'Home', exact: true }).click();
  await page.locator('.home-belt-row .query-card').first().waitFor();
  await fillsWindow();
  await page.getByRole('button', { name: 'All Cards', exact: true }).click();
  await page.locator('.card-library-entry .query-card').first().waitFor();
  await fillsWindow();
  await primary.getByRole('button', { name: 'Search', exact: true }).click();
  await page.locator('#queryTableLibrary .qt-row').first().waitFor();
  await fillsWindow();
  await page.setViewportSize({ width: width + 400, height: 1075 });
  await fillsWindow();
});

for (const dashboard of [
  { name: "Human Words", topic: "human", card: "human-words", dataset: "writing_messages", field: "sent_at", first: "first_message_at", last: "last_message_at", summary: "Human Words summary", chart: { period: "day" } },
  { name: "Tokens and Cost", topic: "tokens", card: "cost-repos", dataset: "usage", field: "first_usage_at", first: "first_usage_at", last: "first_usage_at", summary: "Machine token summary", chart: { period: "day", metric: "cost", split: "repository_name" } },
  { name: "Agent Time", topic: "time", card: "machine-time", dataset: "tool_calls", field: "started_at", first: "started_at", last: "started_at", summary: "Agent Time summary", chart: { period: "day", metric: 0, split: "activity" } },
  { name: "Skills", topic: "skills", card: "skill-evidence", dataset: "skill_usages", field: "created_at", first: "created_at", last: "created_at", chart: { period: "day", metric: 0, split: "skill_name" } },
  { name: "Tools", topic: "tools", card: "tool-failure-rates", dataset: "tool_calls", field: "started_at", first: "day", last: "day", graph: ".usage-tools-dashboard > .qt-metric", chart: { period: "day", metric: "calls", split: "tool_name", view: "compare" } },
  { name: "Pharos MCP", topic: "mcp", card: "mcp-response-size", dataset: "mcp_calls", field: "called_at", first: "called_at", last: "called_at", graph: ".usage-mcp-dashboard > .qt-metric", chart: { period: "day", metric: "calls", split: "tool_name", view: "compare" } },
  { name: "Carbon Impact", topic: "carbon", card: "carbon-trend", dataset: "usage", field: "first_usage_at", first: "first_usage_at", last: "first_usage_at", summary: "Filtered carbon estimate", chart: { period: "day", metric: "co2", split: "type" } },
]) for (const width of [1504, 390, 320]) test(`${dashboard.name} card exploration and inline ranges fit at ${width}px`, { skip }, async t => {
  const where = [{ field: dashboard.last, op: ">=", value: "-7d" }, { field: dashboard.first, op: "<", value: "+0s" }];
  const sourceWhere = where.map(term => ({ ...term, field: dashboard.field }));
  const params = new URLSearchParams({ analyze_mode: "dashboards", analyze_topic: dashboard.topic, card_id: dashboard.card, card_query: dashboard.dataset,
    [`q_${dashboard.dataset}`]: encodeQuery({ ...EMPTY_QUERY, where: sourceWhere }), window_mode: "relative", window_days: "7", window_field: dashboard.field, window_dataset: dashboard.dataset,
    analyze_filters: encodeQuery({ ...EMPTY_QUERY, where }), analyze_chart: JSON.stringify(dashboard.chart) });
  const app = await open(t, { url: `/analyze?${params}`, viewport: { width, height: 933 }, ready: dashboard.graph ?? ".usage-dashboard .usage-chart" });
  const { page } = app;
  const chart = await page.locator(dashboard.graph ?? '.usage-dashboard .usage-chart').boundingBox();
  if (dashboard.summary) {
    const stats = await page.getByLabel(dashboard.summary, { exact: true }).boundingBox();
    assert.ok(stats.y >= chart.y + chart.height, 'summary statistics follow the graph');
  }
  if (dashboard.topic === 'carbon') {
    const assumptions = await page.getByRole('region', { name: 'Estimate assumptions', exact: true }).boundingBox();
    assert.ok(assumptions.y >= chart.y + chart.height, 'carbon assumptions follow the graph');
  }
  assert.deepEqual(await layoutProblems(page), []);
  await app.shot('relative-card-range');
  if (dashboard.topic === 'tools' || dashboard.topic === 'mcp') await page.getByRole('combobox', { name: 'Display', exact: true }).selectOption('interval');
  await page.getByRole('combobox', { name: 'Time Bucket', exact: true }).selectOption(['time', 'skills', 'tools', 'mcp'].includes(dashboard.topic) ? 'week' : 'hour');
  const range = page.getByRole('combobox', { name: 'Range', exact: true });
  for (const [mode, label] of [['custom', 'From date'], ['custom-times', 'From date and time']]) {
    await range.selectOption(mode);
    const from = page.getByLabel(label, { exact: true });
    await from.waitFor();
    await app.settled();
    assert.deepEqual(await layoutProblems(page), []);
    if (width === 1504) {
      const picker = await range.boundingBox(), dates = await from.boundingBox();
      assert.ok(dates.x >= picker.x + picker.width && dates.y < picker.y + picker.height && picker.y < dates.y + dates.height, 'custom inputs sit beside the range selector');
    }
    await app.shot(mode);
  }
});

for (const width of [1504, 390, 320]) test(`Parallelism card exploration and inline ranges fit at ${width}px`, { skip }, async t => {
  const where = [{ field: 'started_at', op: '>=', value: '-7d' }, { field: 'started_at', op: '<', value: '+0s' }];
  const params = new URLSearchParams({ analyze_mode: 'dashboards', analyze_topic: 'parallelism', card_id: 'parallel-days', card_query: 'parallelism',
    q_parallelism: encodeQuery({ ...EMPTY_QUERY, where }), window_mode: 'relative', window_days: '7', window_field: 'started_at', window_dataset: 'parallelism',
    analyze_parallelism: JSON.stringify({ range: '7d' }) });
  const app = await open(t, { url: `/analyze?${params}`, viewport: { width, height: 933 }, concurrency: true, ready: '.usage-dashboard .concurrency-chart svg' });
  const { page } = app;
  const chart = await page.locator('.concurrency-chart').boundingBox();
  const stats = await page.getByRole('group', { name: 'Parallelism summary', exact: true }).boundingBox();
  assert.ok(stats.y >= chart.y + chart.height, 'summary statistics follow the graph');
  assert.deepEqual(await layoutProblems(page), []);
  await app.shot('relative-card-range');
  const range = page.getByRole('combobox', { name: 'Range', exact: true });
  await range.selectOption('custom');
  const from = page.getByLabel('From date', { exact: true });
  await from.waitFor();
  await app.settled();
  assert.deepEqual(await layoutProblems(page), []);
  if (width === 1504) {
    const picker = await range.boundingBox(), dates = await from.boundingBox();
    assert.ok(dates.x >= picker.x + picker.width && dates.y < picker.y + picker.height && picker.y < dates.y + dates.height, 'custom inputs sit beside the range selector');
  }
  await app.shot('custom-dates');
});

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
        if (route.fitTable && viewport.width >= 1024) {
          await app.page.waitForFunction(() => {
            const wrap = document.querySelector('.usage-workspace-table .qt-table-wrap');
            return wrap && wrap.scrollWidth <= wrap.clientWidth + 1;
          });
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
        const known = KNOWN.filter(bug => (!bug.when || bug.when(route, viewport)) && problems.some(problem => bug.match.test(problem)));
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


// IndexedDB and painting saved charts during navigation require a real browser.
test("Card Library restores saved covers while fresh queries are pending", { skip }, async t => {
  let hold = false, release;
  const gate = new Promise(resolve => { release = resolve; });
  t.after(() => release());
  const app = await open(t, {
    url: "/cards?cards=library&card_search=tool-call-share",
    ready: ".card-library-entry .query-card",
    api: { "* /api/query/:dataset/:operation": async call => {
      if (hold && ["metrics", "query-v3", "rows-v2"].includes(call.params.operation)) await gate;
      return defaultApi["* /api/query/:dataset/:operation"](call);
    } },
  });
  // Confirm the browser has committed the snapshot before reloading its page.
  await app.page.waitForFunction(async () => {
    const db = await new Promise((resolve, reject) => {
      const request = indexedDB.open("pharos-card-previews-v1", 1);
      request.onsuccess = () => resolve(request.result); request.onerror = () => reject(request.error);
    });
    const entries = await new Promise((resolve, reject) => {
      const request = db.transaction("libraries").objectStore("libraries").get("browser-fixture");
      request.onsuccess = () => resolve(request.result); request.onerror = () => reject(request.error);
    });
    db.close(); return entries?.length > 0;
  });
  hold = true;
  await app.page.reload();
  await app.page.getByRole("status", { name: "Refreshing preview", exact: true }).first().waitFor();
  const catalog = app.page.locator("#card-library");
  await catalog.scrollIntoViewIfNeeded();
  assert.deepEqual(await layoutProblems(app.page), []);
  await app.shot("saved-covers-refreshing");
  release(); await app.settled();
  await app.page.getByRole("status", { name: "Refreshing preview", exact: true }).first().waitFor({ state: "hidden" });
  assert.deepEqual(app.problems(), []);
});
