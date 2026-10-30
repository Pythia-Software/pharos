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
import { defaultApi, cloudScheduledStatus, rootConversation, workID, eventStream, idleStatus, busyStatus, managedStatus, temporaryBackgroundDocument } from "./fixtures.mjs";
import { EMPTY_QUERY, encodeQuery, withGroupingKeys } from "@pythia-software/query-table-core";

const markdownLinkText = `## Implementation links

[GitHub pull request](https://github.com/gbdubs/pharos/pull/42) · [Project documentation](https://example.com/docs)

- [Source file](internal/archive/api.go:12)
- [Missing file](/Users/previous-mac/project/report.md)
- [Running dev server](http://localhost:5173/)
- [Stopped dev server](http://localhost:59999/)
- [Library view](/library?view=conversation)
- [Filtered usage](/analyze?analyze_topic=tokens)
`;
const markdownLinkAPI = {
  "GET /api/work/:id/conversations/:conversation": { id: rootConversation, workspace_id: workID, provider: "codex", messages: [
    {id:"link-question",native_id:"link-question",role:"user",kind:"message",text:"Review [the source file](internal/archive/api.go:12) and [the change](https://github.com/gbdubs/pharos/pull/42)."},
    {id:"link-response",native_id:"link-response",role:"assistant",kind:"message",text:markdownLinkText},
  ]},
  "GET /api/markdown-link": ({query}) => {
    const href=query.get("href");
    if(href==="internal/archive/api.go:12")return {status:"file",detail:"File available on this Mac",href:"/api/markdown-link?open=1&href=internal/archive/api.go%3A12"};
    if(href==="http://localhost:5173/")return {status:"server",detail:"Local server returned HTTP 200"};
    return {status:"unavailable",detail:href.startsWith('http')?"Local server unavailable on this Mac":"File unavailable on this Mac"};
  },
};
const work = `/work/${workID}`;
const conversationResultAPI = {
  "POST /api/query/library/:operation": request => {
    const result = defaultApi["* /api/query/:dataset/:operation"]({ ...request, params: { dataset: "library", operation: request.params.operation } });
    if (!result?.rows) return result;
    return { ...result, total: 1, rows: result.rows.length ? [{ ...result.rows[0], id: workID, title: "Investigate the incremental indexer", repository_name: "pharos", models: "claude-opus-5-5", efforts: "high", token_count: 190000, turn_count: 12, subagent_count: 2, preview_elapsed_ms: 4800000, cost_usd: 3.3, pr_numbers: "", first_input: `<system_instruction>Initialize the workspace</system_instruction>\n\nReview **the incremental indexer** and its nested agents. ${"Check the recovery path after sleep and preserve the existing index. ".repeat(12)}`, last_response: `The review is **complete**. ${"The index resumes after sleep without replaying retained captures. ".repeat(20)}` }] : [] };
  },
  "GET /api/work/:id": request => {
    const work = defaultApi["GET /api/work/:id"](request);
    return { ...work, conversations: work.conversations.map(conversation => ({ ...conversation,
      ...(conversation.id === rootConversation ? { first_prompt: `<system_instruction>${"Initialize the workspace and load the project settings. ".repeat(80)}</system_instruction>` } : {}),
      parent_id: conversation.id === "conv-child" ? rootConversation : conversation.id === "conv-nested" ? "conv-child" : null })) };
  },
  "GET /api/work/:id/conversations/:conversation": request => {
    const conversation = defaultApi["GET /api/work/:id/conversations/:conversation"](request);
    return request.params.conversation === rootConversation ? { ...conversation, messages: [{ id: "session-setup", native_id: "session-setup", role: "system", kind: "context", text: "Session context" }, ...conversation.messages] } : conversation;
  },
};

// Mixed provenance in the same prompt: only the classified typed spans glow.
const humanPromptParts = [
  ["<system_instruction>Workspace context supplied by Conductor. Keep generated setup separate from the user's own request.</system_instruction>\n\n", "harness"],
  ["Please **keep the sub-agents nested** inside the parent turn. I want to see which review belongs to which request.\n\n", "typed"],
  ["Here is the output from the previous run:\n\n```text\n5 review workers started\n1 worker is checking the conversation reader\n```\n\n", "pasted"],
  ["Make the relationship clear, and keep my written instructions easy to spot. The café example should still render correctly.", "typed"],
];
function classifiedPrompt(id, parts) {
  let end=0;
  return {id,native_id:id,role:"user",kind:"message",text:parts.map(([text])=>text).join(""),authorship:{typed_words:48,spans:parts.map(([text,category])=>{const start=end;end+=Buffer.byteLength(text);return {start,end,category,words:text.split(/\s+/).length};})}};
}
const authoredPrompt = classifiedPrompt("human-mixed",humanPromptParts);
const authoredTurnMessages = [authoredPrompt,
  ...Array.from({length:5},(_,index)=>[
    {id:`review-${index}`,native_id:`review-${index}`,role:"assistant",kind:"delegation",call_id:`reviewer-${index}`,text:JSON.stringify({name:"Task",input:{subagent_type:`Reviewer ${index+1}`,prompt:["Review the parent turn structure","Check nested review groups","Verify human-written text highlighting","Check keyboard navigation","Check the narrow screen layout"][index]}})},
    {id:`review-reply-${index}`,native_id:`review-reply-${index}`,role:"assistant",kind:"message",parent_native_id:`reviewer-${index}`,text:`Review ${index+1} complete. The assigned check passed.`},
  ]).flat(),
  {id:"nested-review",native_id:"nested-review",role:"assistant",kind:"delegation",call_id:"nested-reviewer",parent_native_id:"reviewer-0",text:JSON.stringify({name:"Task",input:{subagent_type:"Nested reviewer",prompt:"Check the child branch under Reviewer 1"}})},
  {id:"nested-review-reply",native_id:"nested-review-reply",role:"assistant",kind:"message",parent_native_id:"nested-reviewer",text:"The nested review stays under Reviewer 1."},
  {id:"parent-reply",native_id:"parent-reply",role:"assistant",kind:"message",text:"All five reviews are complete. The outline keeps each review inside the turn that requested it, including the nested review under Reviewer 1."},
  classifiedPrompt("human-next",[["Now show me the next turn separately.","typed"]]),
  {id:"next-reply",native_id:"next-reply",role:"assistant",kind:"message",text:"This turn has no sub-agents."},
];
const authoredTurnAPI = {
  ...conversationResultAPI,
  "POST /api/query/library/:operation": request => {
    const result=conversationResultAPI["POST /api/query/library/:operation"](request);
    return !result?.rows?result:{...result,rows:result.rows.map(row=>({...row,title:"Review the conversation reader",first_input:authoredPrompt.text,last_response:"All five reviews are complete.",turn_count:2,subagent_count:6}))};
  },
  "GET /api/work/:id": request => ({...defaultApi["GET /api/work/:id"](request),title:"Review the conversation reader",conversations:[{id:rootConversation,provider:"codex",title:"Keep reviews inside the parent turn"}]}),
  "GET /api/work/:id/conversations/:conversation": {id:rootConversation,workspace_id:workID,provider:"codex",messages:authoredTurnMessages},
};

async function publishAppUpdate(page, state) {
  await page.evaluate(state => {
    window.pharosUpdateFixture = state;
    window.webkit = {messageHandlers: {pharosUpdates: {postMessage: async ({action}) => {
      if (action === 'install') {
        window.pharosUpdateFixture = {...window.pharosUpdateFixture, phase: 'downloading'};
        dispatchEvent(new CustomEvent('pharos:app-update', {detail: window.pharosUpdateFixture}));
      }
      return window.pharosUpdateFixture;
    }}}};
    dispatchEvent(new CustomEvent('pharos:app-update', {detail: state}));
  }, state);
}
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
// Engine dashboards are ready when their view stops loading and links its
// supporting query.
const overTimeReady = 'section[aria-label="Over time"][aria-busy="false"] .usage-query-destinations', analysisReady = '.usage-analysis-dashboard[aria-busy="false"] .usage-query-destinations';
const storageBreakdown = defaultApi["GET /api/health/storage/breakdown"];
const compactableBreakdown = { ...storageBreakdown, catalog_free_bytes: 31e9, compact_needs_bytes: 70e9,
  parts: storageBreakdown.parts.flatMap(part => part.key === "catalog_database" ? [{ ...part, bytes: 63e9 }, { key: "catalog_free", label: "Free space in the catalog", bytes: 31e9 }] : [part]) };
const ROUTES = [
  {name:'Library settings expanded',url:'/settings/libraries',ready:'#library-settings-current h3',managedLibrary:{id:'local',name:'Pending history',config:'/Users/test/Libraries/Pending/library.toml'},open:async page=>{
    await page.locator('#library-settings-transfer-panel summary').click();
    await page.locator('#library-settings-delivery-toggle').click();
    await page.locator('#library-settings-list [data-rename-id]').first().click();
  },shown:'.library-settings-rename'},
  ...['overview','open','create','restore'].map(kind=>({name:'Library settings '+kind,url:'/settings/libraries',ready:'#library-settings-current h3',managedLibrary:{id:'local',name:'Pending history with a long library name',config:'/Users/test/Libraries/Pending/library.toml'},...(kind==='overview'?{}:{open:page=>page.locator({open:'#library-settings-open',create:'#library-settings-new',restore:'#library-settings-restore'}[kind]).click(),shown:'#library-settings-'+kind+'-form:not([hidden])'})})),
  {name:"Application first launch",url:"/app",ready:"#application-setup[open]",api:{"GET /api/app/state":{application:"/Applications/Pharos.app",selected:"",mcp:"",busy:false,libraries:[]}}},
  ...["about","machines","storage","details","review"].map(step=>({name:`Application introduction ${step}`,url:"/app",ready:"#application-setup[open]",api:{"GET /api/app/state":{application:"/Applications/Pharos.app",selected:"",mcp:"",busy:false,libraries:[]}},open:async page=>{
    await page.locator('#meet-pharos').click();
    if(step==='about')return;
    await page.locator('#start-setup').click();
    if(step==='machines')return;
    await page.locator('#submit').click();
    if(step==='storage')return;
    await page.locator('#submit').click();
    if(step==='review')await page.locator('#submit').click();
  },shown:step==='about'?'#about-pharos:not([hidden])':`#wizard-${step}:not([hidden])`})),
  {name:"Application library chooser",url:"/app",ready:"#library-list .card",check:async page=>{
    const widths=await page.locator('#application-intro').evaluate(intro=>({
      available:intro.getBoundingClientRect().width,
      copy:intro.querySelector('.setup-copy').getBoundingClientRect().width,
    }));
    assert.ok(widths.copy>widths.available*.8,'the library introduction leaves an unused artwork column');
  }},
  ...['tools','other','none','locations','custom','review','empty'].map(step=>({name:`Application sources ${step}`,url:'/app',ready:'#application-setup[open]',api:{
    'GET /api/app/state':{application:'/Applications/Pharos.app',selected:'',mcp:'',busy:false,libraries:[]},
    'POST /api/app/create':{saved:true},
    ...(step==='none'?{'GET /api/probe/tools':{tools:[]}}:{}),
  },open:async page=>{
    await page.locator('#meet-pharos').click();await page.locator('#start-setup').click();
    for(let i=0;i<4;i++)await page.locator('#submit').click();
    await page.locator('#configure-sources').click();
    const guide=page.locator('#pharosOnboarding');
    await guide.locator('.pharos-other-tools>summary').waitFor();
    if(['tools','none'].includes(step))return;
    if(step==='other'){await guide.locator('.pharos-other-tools>summary').click();return}
    if(step==='empty'){await guide.getByRole('checkbox',{name:/Claude Code/}).uncheck();await guide.locator('.pharos-other-tools>summary').click()}
    await guide.getByRole('checkbox',{name:step==='empty'?/Antigravity/:/Claude Code/}).check();
    await guide.getByRole('button',{name:'Continue',exact:true}).click();
    await guide.getByRole('button',{name:'Back',exact:true}).waitFor();
    if(step==='custom')await guide.locator('.pharos-custom-source summary').click();
    if(step==='review')await guide.getByRole('button',{name:'Continue',exact:true}).click();
  },shown:'#pharosOnboarding'})),
  {name:'Source settings guide',url:'/settings/sources',ready:'#pharosFindSources',open:page=>page.locator('#pharosFindSources').click(),shown:'#pharosOnboarding'},
  ...["create","open","recover"].map(flow=>({name:`Application library ${flow}`,url:"/app",ready:"#library-list .card",open:page=>page.locator(`#library-actions [data-flow="${flow}"]`).click(),shown:"#setup:not([hidden])"})),
  ...["storage","details","review"].map(step=>({name:`Application SSD ${step}`,url:"/app",ready:"#application-setup[open]",api:{"GET /api/app/state":{application:"/Applications/Pharos.app",selected:"",mcp:"",busy:false,libraries:[]}},open:async page=>{
    await page.locator('#meet-pharos').click();await page.locator('#start-setup').click();
    await page.locator('input[name=machines][value=many]').check();await page.locator('#submit').click();
    if(step==='storage')return;
    await page.locator('#submit').click();
    if(step==='review'){await page.getByRole('button',{name:'Edit library location',exact:true}).click();await page.locator('#path').fill('/Volumes/My SSD');await page.locator('#submit').click()}
  },shown:`#wizard-${step}:not([hidden])`})),
  ...[1,2].map(step=>({name:`Application restoration step ${step+1}`,url:"/app",ready:"#library-list .card",open:async page=>{
    await page.locator('#library-actions [data-flow=recover]').click();await page.locator('#backup').fill('/Backups/Library');await page.locator('#submit').click();
    if(step===2){await page.locator('#recover-path').fill('/Libraries/Restored');await page.locator('#submit').click()}
  },shown:step===1?'#wizard-recover-target:not([hidden])':'#wizard-recover-review:not([hidden])'})),
  ...[["Search", "/library"], ["Analyze", "/analyze"], ["Optimize", "/findings"]].flatMap(([name, url]) => [["unconfigured", false, false], ["idle", true, false], ["syncing", true, true]].map(([state, sources, busy]) => ({
    name: `${name} empty library ${state}`, url, ready: ".view.active .library-empty-card",
    api: { "GET /api/sync/status": { ...defaultApi["GET /api/sync/status"], library_empty: true, has_sources: sources, busy, sources: sources ? [{ name: "codex" }] : [], available_sources: [] } },
  }))),
  ...["dark","light"].flatMap(theme => [
  {name:"Ask Pharos removed CLI",url:"/",ready:"#pharos-ask .home-search-trigger",api:{"GET /api/ask/status":{settings:{enabled:true,showInvitation:true,selection:askSelection},providers:[]}}},
  {name:"Ask Pharos opted out",url:"/",ready:"#pharos-ask .home-search-trigger",api:{"GET /api/ask/status":{settings:{enabled:false,showInvitation:false},providers:askProviders}}},
  {name:"Ask Pharos ready",url:"/",ready:"#pharos-ask .pharos-ai:not([hidden])",api:askAPI},
  {name:"Ask Pharos model picker",url:"/",ready:"#pharos-ask .pharos-ai:not([hidden])",api:askAPI,open:page=>page.locator("#pharos-ask .pharos-model-trigger").click(),shown:"#pharos-model-menu:not([hidden])"},
  {name:"Ask Pharos history",url:"/",ready:"#pharos-ask .pharos-ai:not([hidden])",api:askAPI,open:page=>page.locator("#pharos-ask .pharos-history-trigger").click(),shown:"#pharos-chat-history:not([hidden])"},
  {name:"Ask Pharos conversation",url:"/?ask_chat=saved-chat",ready:"#pharos-ask .pharos-turn",api:askAPI,check:async page=>{
    const visible=await page.locator('.pharos-user-question').first().evaluate(message=>{
      const viewport=message.closest('.pharos-thread-viewport');
      if(viewport.scrollHeight>parseFloat(getComputedStyle(viewport).maxHeight)+1)return true;
      const box=message.getBoundingClientRect(),bounds=viewport.getBoundingClientRect();
      return box.top>=bounds.top&&box.bottom<=bounds.bottom;
    });
    assert.ok(visible,'the first message must fit completely when the conversation fits on screen');
  }},
  ...['',' \n '].map(reply=>({name:`Ask Pharos destinations with ${reply?'blank':'empty'} intro`,url:'/?ask_chat=saved-chat',ready:'#pharos-ask .pharos-destinations:not([hidden])',api:{...askAPI,'GET /api/ask/session':{...askSession,conversation:askSession.conversation.map(turn=>({...turn,reply}))}}})),
  {name:"Ask Pharos error activity",url:"/?ask_chat=saved-chat",ready:"#pharos-ask .pharos-activity:not([hidden])",api:{...askAPI,"GET /api/ask/session":{...askSession,conversation:askSession.conversation.map(turn=>({...turn,status:'error',cards:[],reply:'Could not finish checking the destination. Try again.',activity:[{message:"Browsing available views…",elapsedMs:1200},{message:"Browsing available views — done",elapsedMs:2500},{message:"Checking a destination link…",elapsedMs:3000}]}))}},open:page=>page.locator('#pharos-ask .pharos-activity summary').click(),shown:'#pharos-ask .pharos-activity[open]'},
  {name:"Ask Pharos clarification",url:"/?ask_chat=saved-chat",ready:"#pharos-ask .pharos-clarification:not([hidden])",api:{...askAPI,"GET /api/ask/session":askClarificationSession}},
  {name:"Ask Pharos opt-in",url:"/",ready:"#pharos-ask .pharos-invitation:not([hidden])",api:{"GET /api/ask/status":{settings:{enabled:false,showInvitation:true},providers:askProviders}},open:page=>page.locator("#pharos-ask .pharos-try").click(),shown:"#pharos-ask .pharos-consent[open]"},
  {name:"Ask Pharos settings",url:"/settings/ask",ready:"#askSettingsPage .ask-settings select",api:askAPI},

  ].map(route=>({...route,name:`${route.name} (${theme})`,preferences:{"pharos-theme":theme}}))),
  { name: "TL1 attempts query", url: "/analyze?analyze_mode=query&analyze_data=tl1_attempts", ready: ".usage-workspace-table .qt-row" },
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
  // The unified activity panel (flag on), idle and busy, in the browser and
  // in the app (other libraries).
  ...[['idle', idleStatus, false], ['busy', busyStatus, false], ['busy in the app', busyStatus, true]].map(([state, snapshot, managed]) => ({
    name: `Unified activity panel ${state}`, url: '/library', ready: '#queryTableLibrary .qt-row', preferences: {'pharos-unified-activity-v1': true},
    managedLibrary: managed ? {id: 'local', name: 'Pending history', config: '/Users/test/Libraries/Pending/library.toml'} : null,
    api: managed ? {'GET /api/app/status/stream': eventStream(managedStatus(snapshot)), 'GET /api/app/status': managedStatus(snapshot)}
      : {'GET /api/status/stream': eventStream(snapshot), 'GET /api/status': snapshot},
    open: async page => {
      await page.evaluate(() => { window.webkit = {messageHandlers: {pharosLibrary: {postMessage: async () => {}}}}; });
      await activateHeaderTool(page, 'pharosDrive');
      // Every cadence row open, its items listed.
      const closed = page.locator('#pharosStatusPanel .pharos-status-cadences details:not([open]) summary');
      while (await closed.count()) await closed.first().click();
    }, shown: '#pharosStatusPanel .pharos-status-items li',
  })),
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
  { name: "Markdown links in transcript", url: `${work}/conversation/${rootConversation}`, api: markdownLinkAPI, ready: ".conversation-panel .markdown-link" },
  { name: "Markdown links in Search", url: "/library?view=conversation", api: {...conversationResultAPI,...markdownLinkAPI}, ready: ".conversation-result-card", open: async page => {await page.getByRole("button",{name:/^Browse turns:/}).first().click();await page.getByRole("link",{name:"Running dev server",exact:true}).waitFor();}, shown: ".result-turn-content .markdown-link" },
  { name: "Search nested reviews and human writing", url: "/library?view=conversation", api: authoredTurnAPI, ready: ".conversation-result-card", open: async page => {await page.getByRole("button",{name:/^Browse turns:/}).first().click();await page.getByRole("button",{name:/Sub-agent · Reviewer 5/}).waitFor();}, shown: ".result-turn-content .human-written" },
  { name: "Transcript human writing", url: `${work}/conversation/${rootConversation}`, api: authoredTurnAPI, ready: ".conversation-panel .human-written" },
  { name: "Search conversations", url: "/library?view=conversation", ready: ".conversation-result-card" },
  ...["turn", "sub-agent", "accordion", "summary"].map(selection => ({
    name: `Search conversation outline ${selection}`, url: "/library?view=conversation", api: conversationResultAPI, ready: ".conversation-result-card",
    open: async page => {
      await page.getByRole("button", selection === "summary" ? { name: /^Browse turns: / } : { name: "Browse", exact: true }).click();
      if (selection === "summary") assert.ok(await page.getByRole("button", { name: "Collapse turns", exact: true }).evaluate(button => button === document.activeElement), "opening from the compact summary keeps keyboard focus on the collapse control");
      const outline = page.getByRole("navigation", { name: "Conversation outline", exact: true });
      await outline.getByRole("button", { name: /^Turn 1/ }).first().waitFor();
      if (selection === "sub-agent") {
        await outline.locator("summary").filter({ hasText: "Sub-agent session · codex" }).click();
        await outline.getByRole("button", { name: /Turn 1.*Review the change/ }).click();
      } else await outline.getByRole("button", { name: /^Turn 3/ }).first().click();
      await page.getByRole("region", { name: "Selected turn preview" }).locator(".result-message").first().waitFor();
      if (selection === "accordion") {
        const toggle = page.getByRole("button", { name: /^(Browse|Collapse turns)$/ });
        await toggle.focus();
        await page.keyboard.press("Enter");
        assert.equal(await toggle.getAttribute("aria-expanded"), "false");
        assert.ok(await toggle.evaluate(button => button === document.activeElement), "collapsing keeps focus on the disclosure control");
        await page.keyboard.press("Enter");
        assert.equal(await toggle.getAttribute("aria-expanded"), "true");
        await outline.getByRole("button", { name: /^Turn 3/ }).first().waitFor();
        assert.equal(await outline.getByRole("button", { name: /^Turn 3/ }).first().getAttribute("aria-current"), "true", "reopening preserves the selected turn");
        await page.reload();
        await page.getByRole("button", { name: "Collapse turns", exact: true }).waitFor();
        await outline.getByRole("button", { name: /^Turn 3/ }).first().waitFor();
        assert.equal(await outline.getByRole("button", { name: /^Turn 3/ }).first().getAttribute("aria-current"), "true", "the URL restores the selected turn after reload");
      }
    }, shown: ".conversation-outline",
  })),
  { name: "Search compact conversation previews", url: "/library?view=conversation", api: conversationResultAPI, ready: ".conversation-result-preview" },
  { name: "Search display switch", url: "/library", ready: "#queryTableLibrary .qt-row", open: page => page.getByRole("group", { name: "Display results as" }).getByRole("button", { name: "Conversations", exact: true }).click(), shown: ".conversation-result-card" },
  { name: "Search content editor", url: "/library", ready: "#queryTableLibrary .qt-row", open: page => page.getByRole("button", { name: "Messages", exact: true }).click(), shown: ".explore-search-modal" },
  { name: "Search grouped search", url: exploreGroupedSearch, ready: "#queryTableLibrary .qt-row" },
  { name: "Search grouped conversation display", url: exploreGroupedSearch, ready: "#queryTableLibrary .qt-row", open: page => page.getByRole("group", { name: "Display results as" }).getByRole("button", { name: "Conversations", exact: true }).click(), shown: ".conversation-result-card" },
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
  ...Object.entries({
    compare: { view: "compare", metric: "cost", split: "provider_model", compareAs: "share" },
    distribution: { view: "distribution", unit: "conversation", metric: "cost", split: "model_family", minSamples: 2 },
    scatter: { view: "scatter", unit: "conversation", metric: "tokens", metricY: "cost", split: "provider", scale: "logarithmic", scaleY: "logarithmic" },
    cycle: { view: "cycle", cycle: "hour", metric: "tokens", split: "none" },
  }).map(([view, chart]) => ({ name: `Tokens and Cost ${view} view`, url: `/analyze?analyze_topic=tokens&analyze_chart=${encodeURIComponent(JSON.stringify(chart))}`, ready: '.usage-analysis-dashboard[aria-busy="false"] .usage-query-destinations',
    check: async page => assert.equal(await page.locator(".usage-analysis-dashboard [role=alert]").count(), 0, "the analysis reports an error") })),
  // Over time draws every measure, population and breakdown kind.
  ...Object.entries({
    ratio: { metric: "cache_read_share", split: "provider_model", rollingPeriods: 7 },
    "distinct rolling sum": { metric: "conversations", split: "none", rollingPeriods: 7, rollingOp: "sum" },
    "whole periods and norm": { metric: "tokens", split: "none", rollingPeriods: 7, completePeriods: true, norm: true },
    "token type": { metric: "tokens", split: "type", share: true },
    "priced cost": { metric: "priced_cost", split: "provider", display: "cumulative" },
  }).map(([name, chart]) => ({ name: `Tokens and Cost trend ${name}`, url: `/analyze?analyze_topic=tokens&analyze_chart=${encodeURIComponent(JSON.stringify({ period: "day", ...chart }))}`,
    ready: '.usage-dashboard section[aria-label="Over time"][aria-busy="false"] .usage-query-destinations',
    check: async page => assert.equal(await page.locator('section[aria-label="Over time"] .query-table-error').count(), 0, "the trend reports an error") })),
  { name: "Analyze cumulative human", url: `/analyze?analyze_topic=human&analyze_chart=${encodeURIComponent(JSON.stringify({"display": "cumulative", "rollingPeriods": 20}))}`, ready: ".usage-dashboard .usage-chart-lines" },
  { name: "Analyze cumulative skills", url: `/analyze?analyze_topic=skills&analyze_chart=${encodeURIComponent(JSON.stringify({"display": "cumulative", "rollingPeriods": 20}))}`, ready: ".usage-dashboard .usage-chart-lines" },
  { name: "Analyze cumulative time", url: `/analyze?analyze_topic=time&analyze_chart=${encodeURIComponent(JSON.stringify({"display": "cumulative", "rollingPeriods": 20}))}`, ready: ".usage-dashboard .usage-chart-lines" },
  { name: "Analyze cumulative carbon", url: `/analyze?analyze_topic=carbon&analyze_chart=${encodeURIComponent(JSON.stringify({"display": "cumulative", "rollingPeriods": 20}))}`, ready: ".usage-dashboard .usage-chart-lines" },
  { name: "Analyze cumulative tools", url: `/analyze?analyze_topic=tools&analyze_chart=${encodeURIComponent(JSON.stringify({"display": "cumulative", "rollingPeriods": 20, "view": "trend", "metric": "mean_time", "period": "day"}))}`, ready: `.usage-tools-dashboard ${overTimeReady}` },
  { name: "Analyze cumulative mcp", url: `/analyze?analyze_topic=mcp&analyze_chart=${encodeURIComponent(JSON.stringify({"display": "cumulative", "rollingPeriods": 20, "view": "trend", "metric": "mean_time", "period": "day"}))}`, ready: `.usage-mcp-dashboard ${overTimeReady}` },
  { name: "Analyze workspace custom intervals", url: `/analyze?analyze_topic=tokens&analyze_chart=${encodeURIComponent(JSON.stringify({ period: "hours", hours: 6, rollingPeriods: 37 }))}`, ready: ".usage-dashboard .usage-period-custom input" },
  { name: "Analyze workspace human dashboard", url: "/analyze?analyze_workspace=1&analyze_topic=human", ready: `.usage-human-dashboard ${overTimeReady}` },
  { name: "Analyze workspace tools dashboard", url: "/analyze?analyze_workspace=1&analyze_topic=tools", ready: `.usage-tools-dashboard ${analysisReady}` },
  // Seven weeks keeps the history request within the stub's equality-filter
  // capability; wider day regex filters are exercised against the Go service.
  { name: "Analyze workspace tools trends", url: `/analyze?analyze_workspace=1&analyze_topic=tools&analyze_chart=${encodeURIComponent(JSON.stringify({ metric: "failure_rate", split: "tool_category", view: "trend", period: "week", rollingPeriods: 7 }))}`, ready: `.usage-tools-dashboard ${overTimeReady}` },
  // Every view each record dashboard offers, over its catalog (2b).
  ...Object.entries({
    tools: { "compare share": { view: "compare", metric: "calls", split: "tool_name", compareAs: "share" }, "compare minimum": { view: "compare", metric: "failure_rate", split: "tool_name", minSamples: 10 }, "over time": { view: "trend", metric: "truncation_rate", split: "mcp_method", rollingPeriods: 7, completePeriods: true } },
    mcp: { compare: { view: "compare", metric: "mean_output", split: "query_source" }, distribution: { view: "distribution", metric: "bytes", split: "tool_name", minSamples: 3, scale: "logarithmic" }, "hour cycle": { view: "cycle", cycle: "hour", metric: "calls", split: "none" }, "weekday cycle": { view: "cycle", cycle: "weekday", metric: "time", split: "none", reduce: "mean" }, "over time": { view: "trend", metric: "failure_rate", split: "status", rollingPeriods: 7 } },
    time: { compare: { view: "compare", metric: "time", split: "activity", compareAs: "share" }, "unit compare": { view: "compare", unit: "conversation", metric: "calls", split: "repository_name", reduce: "median" }, distribution: { view: "distribution", metric: "time", split: "command_source", minSamples: 3, scale: "logarithmic" }, "unit distribution": { view: "distribution", unit: "tool_conversation", metric: "calls", split: "tool_name" }, scatter: { view: "scatter", unit: "conversation", metric: "calls", metricY: "time", split: "provider" }, cycle: { view: "cycle", cycle: "hour", metric: "time", split: "none", reduce: "total" }, "over time": { v: 2, metric: "mean_time", split: "none", rollingPeriods: 7, completePeriods: true, norm: true } },
    // 2c: messages, usage under the Carbon assumptions, and parallelism records.
    human: { compare: { view: "compare", metric: "typed", split: "repository_name", compareAs: "share" }, "unit compare": { view: "compare", unit: "conversation", metric: "typed", split: "repository_name", reduce: "mean" }, distribution: { view: "distribution", unit: "conversation", metric: "typed", split: "none" }, "hour cycle": { view: "cycle", cycle: "hour", metric: "typed", split: "none", reduce: "total" }, "weekday cycle": { view: "cycle", cycle: "weekday", metric: "typed_messages", split: "none" }, "over time": { v: 2, metric: "words", split: "kind", rollingPeriods: 7 }, "cue share": { v: 2, metric: "correction_share", split: "none", period: "week" }, hourly: { v: 2, metric: "typed", split: "none", period: "hour" } },
    carbon: { compare: { view: "compare", metric: "co2", split: "carbon_tier", compareAs: "share" }, "over time": { v: 2, metric: "energy", split: "type", rollingPeriods: 7, rollingOp: "sum" }, cycle: { view: "cycle", cycle: "weekday", metric: "co2", split: "none" } },
    parallelism: { "over time": { v: 2, metric: "at_once", split: "none", rollingPeriods: 7, completePeriods: true, norm: true }, compare: { view: "compare", metric: "agent_hours", split: "repository_name" }, distribution: { view: "distribution", unit: "agent", metric: "agent_hours", split: "repository_name" }, cycle: { view: "cycle", cycle: "hour", metric: "agent_hours", split: "none", reduce: "total" } },
    skills: { compare: { view: "compare", metric: "conversations", split: "skill_name" }, distribution: { view: "distribution", metric: "body", split: "skill_evidence", minSamples: 3 }, "unit distribution": { view: "distribution", unit: "skill_path", metric: "loads", split: "skill_name" }, scatter: { view: "scatter", unit: "conversation", metric: "records", metricY: "body", split: "provider" }, cycle: { view: "cycle", cycle: "weekday", metric: "records", split: "none" }, "over time": { v: 2, metric: "records", split: "evidence_type", share: true, rollingPeriods: 7 } },
  }).flatMap(([topic, charts]) => Object.entries(charts).map(([name, chart]) => ({ name: `${topic} dashboard ${name}`, url: `/analyze?analyze_topic=${topic}&analyze_chart=${encodeURIComponent(JSON.stringify(chart))}`,
    ready: `.usage-${topic}-dashboard ${(chart.view ?? "trend") === "trend" ? overTimeReady : analysisReady}`,
    check: async page => assert.deepEqual(await page.locator(`.usage-${topic}-dashboard [role=alert], .usage-${topic}-dashboard .query-table-error`).allTextContents(), [], "the dashboard reports an error") }))),
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
  { name: "MCP dashboard", url: "/analyze?analyze_topic=mcp", ready: `.usage-mcp-dashboard ${analysisReady}` },
  { name: "MCP trends", url: `/analyze?analyze_topic=mcp&analyze_chart=${encodeURIComponent(JSON.stringify({ metric: "failure_rate", split: "tool_name", view: "trend", period: "week", rollingPeriods: 37 }))}`, ready: `.usage-mcp-dashboard ${overTimeReady}` },
  { name: "MCP query", url: "/analyze?analyze_mode=query&analyze_data=mcp_calls&analyze_topic=mcp&analyze_search=1", ready: ".usage-workspace-table .qt-row" },
  { name: "MCP legacy link", url: "/mcp", ready: `.usage-mcp-dashboard ${analysisReady}` },
  { name: "MCP settings", url: "/settings/mcp", ready: "#mcpSettingsPage .mcp-setup-grid" },
  { name: "Library health", url: "/settings", ready: "#healthCards .panel:not([aria-busy])" },
  // In the app, a catalog with much free space offers to compact it.
  { name: "Library health compact catalog", url: "/settings", ready: ".storage-compact button", managedLibrary: { id: "local", name: "Pending history", config: "/Users/test/Libraries/Pending/library.toml" },
    api: { "GET /api/health/storage/breakdown": compactableBreakdown }, open: page => page.getByRole("button", { name: "Compact catalog…", exact: true }).click(), shown: ".storage-compact button.primary" },
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
  { name: "Background updates", url: "/settings/sync", ready: "#syncHistoryTables .qt-row" },
  ...[false, true].map(details => ({ name: `Background updates deferred verification${details ? ' details' : ''}`, url: "/settings/sync", ready: "#syncHistoryTables .qt-row", api: {
    "GET /api/sync/status": { ...defaultApi["GET /api/sync/status"], history: [{ ...defaultApi["GET /api/sync/status"].history[0], audit: { outcome: "deferred", discovered_units: 10, deferred_units: 1, detail: "large group queued for manual verification" } }] },
  }, ...(details ? { open: page => page.getByRole("region", { name: "Recent run history", exact: true }).getByRole("button", { name: "Details", exact: true }).first().click(), shown: ".sync-run-dialog .qt-row" } : {}) })),
  { name: "Background updates schedule", url: "/settings/sync", ready: "#backgroundSchedule .bg-col", open: page => page.locator("#backgroundSchedule .bg-item-head", { hasText: "Your writing" }).click(), shown: "#backgroundSchedule .bg-detail" },
  { name: "Background updates temporary schedule", url: "/settings/sync", ready: "#backgroundSchedule .bg-col", api: { "GET /api/background": temporaryBackgroundDocument }, open: page => page.locator("#backgroundSchedule .bg-item-head", { hasText: "Your writing" }).click(), shown: "#backgroundSchedule .bg-detail" },
  { name: "Background updates sources", url: "/settings/sync", ready: "#backgroundSchedule .bg-col", open: page => page.locator("#backgroundSchedule .bg-item-head", { hasText: "New conversations" }).click(), shown: "#backgroundSchedule .bg-sources" },
  { name: "Background updates queued verification", url: "/settings/sync", ready: "#backgroundSchedule .bg-col", open: page => page.locator("#backgroundSchedule .bg-item-head", { hasText: "Verify indexed data" }).click(), shown: "#backgroundSchedule .bg-detail" },
  { name: "Background updates diagnostics", url: "/settings/sync", ready: "#syncHistoryTables .qt-row", open: page => page.getByText("Performance & verification details", { exact: true }).click(), shown: "#syncDiagnosticTables .qt-row" },
  { name: "Background updates run details", url: "/settings/sync", ready: "#syncHistoryTables .qt-row", open: page => page.getByRole("region", { name: "Recent run history", exact: true }).getByRole("button", { name: "Details", exact: true }).first().click(), shown: ".sync-run-dialog .qt-row" },
  { name: "Background updates empty", url: "/settings/sync", ready: "#syncHistoryTables .qt-empty", api: { "GET /api/sync/status": { ...defaultApi["GET /api/sync/status"], history: [], source_states: [] } } },
  { name: "Legacy Findings settings", url: "/settings/optimization", ready: ".findings-settings .findings-panel" },
  { name: "Layout Preferences", url: "/settings/layout", ready: "#headerExpand" },
  { name: "Header settings", url: "/settings/layout", ready: "#headerExpand", open: page => page.locator('#headerExpand').click(), shown: '#headerActive .header-tool' },
  { name: "Quick Stats settings", url: "/settings/layout", ready: "#quickStatsExpand", open: page => page.locator('#quickStatsExpand').click(), shown: '#quickStatsEditor button' },
  { name: "Quick Stats toolbox", url: "/settings/layout", ready: "#quickStatsExpand", preferences: { "pharos-quick-stats-v1": { metrics: ['human_words', 'tokens'], windows: ['today', '24h'] } }, open: page => page.locator('#quickStatsExpand').click(), shown: '#quickStatsPreview td' },
  { name: "App preferences", url: "/settings/preferences", ready: "#lighthouseSpinOnClick" },
  { name: "Mac menu disabled", url: "/settings/layout", ready: "#showMacMenuBar", preferences: { 'pharos-menu-bar': { enabled: false } } },
  { name: "Mac menu expanded", url: "/settings/layout", ready: "#showMacMenuBar", open: page => page.locator('#menuBarExpand').click(), shown: '#menuBarActive button' },
  { name: "Mac menu syncing", url: "/settings/layout", ready: "#showMacMenuBar", open: page => page.locator('#menuBarExpand').click(), shown: '#menuBarActive .menu-bar-detail', api: {
    'GET /api/menu-bar/status': {...defaultApi['GET /api/menu-bar/status'], library: {activities: [{label: 'Manual sync: evaluating findings while checking every captured conversation and its retained tool results', progress: .22, estimated_completion_at: new Date(Date.now() + 60000).toISOString()}]}},
  } },
  { name: "Mac menu with all items", url: "/settings/layout", ready: "#showMacMenuBar", open: page => page.locator('#menuBarExpand').click(), shown: '#menuBarActive button', preferences: { 'pharos-menu-bar': { cost: true, backup: true, space: true, index: true, eject: true, search: true, analyze: true, optimize: true, capture: true, health: true, sources: true, finder: true } } },
  { name: "Workspace overview", url: work, ready: ".delegation-table tbody tr" },
  { name: "Workspace parallel activity", url: `${work}?explorer=parallel`, ready: ".delegation-parallel" },
  { name: "Long transcript", url: `${work}/conversation/${rootConversation}`, ready: ".conversation-panel .turn" },
  { name: "Child transcript", url: `${work}/conversation/conv-nested`, ready: ".conversation-panel .turn" },
  { name: "Quick search", url: "/library", ready: "#queryTableLibrary .qt-row", open: page => page.keyboard.press("ControlOrMeta+k"), shown: "#searchDialog[open]" },
  { name: "Analyze summary", url: "/library", ready: "#queryTableLibrary .qt-row", open: page => activateHeaderTool(page, "usageSummaryToggle"), shown: "#usageSummary" },
  { name: "Extended Quick Stats", url: "/library", preferences: { "pharos-quick-stats-v1": {metrics:['active_conversations','message_count','user_messages','assistant_messages','tool_calls','failed_tool_calls','tool_failure_rate','delegated_runs','active_repositories','tool_time_ms','agent_time_ms','active_time_ms'], windows:['5m','15m','yesterday','week','month','all']}, "pharos-usage-summary-links":true }, ready: "#queryTableLibrary .qt-row", open: page => activateHeaderTool(page,"usageSummaryToggle"), shown: "#usageSummary" },
  { name: "Customized Quick Stats", url: "/library", preferences: { "pharos-quick-stats-v1": { metrics: ['cost_usd', 'tokens'], windows: ['today', '6h'] } }, ready: "#queryTableLibrary .qt-row", open: page => activateHeaderTool(page, "usageSummaryToggle"), shown: "#usageSummary" },
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
  bug: "Query Table 0.9.7 fit-to-screen floors a column at its wrapping host link measured on one line, so Analyze's fitted tables scroll at desktop widths; docs/handoffs/query-table-fit-wrapping-links.md",
  when: (route, viewport) => /^Analyze workspace query (usage|writing|writing_messages|tool_calls|skill_usages|conversation_activity)$/.test(route.name) && viewport.width >= 1024,
  match: /^fitted table still scrolls sideways: \d+px wide in \d+px$/,
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
  await publishAppUpdate(page, {version: '12.123.456-beta.123', phase: 'available'});
  await more.waitFor();
  assert.deepEqual(await layoutProblems(page), [], 'the release action reflows the measured combo without resizing');
  await publishAppUpdate(page, {});
  await more.waitFor({state: 'hidden'});
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

for (const width of [1440, 1024, 390, 320]) test(`Pharos release action fits through download and relaunch @ ${width}`, {skip}, async t => {
  const {page, settled, shot} = await open(t, {
    url: '/library', viewport: {width, height: width < 500 ? 844 : 900}, ready: '#queryTableLibrary .qt-row',
    api: {'GET /api/library/status': {...defaultApi['GET /api/library/status'],
      drive: {...defaultApi['GET /api/library/status'].drive, ejectable: true, name: 'My portable Pharos library with a long drive name'}}},
  });
  const more = page.getByRole('button', {name: 'More tools', exact: true});
  const reveal = async () => {
    await settled();
    if (!await page.locator('#pharosAppUpdate').isVisible()) await more.click();
    await page.locator('#pharosAppUpdate').waitFor();
    assert.deepEqual(await layoutProblems(page), []);
  };
  await shot('current');
  await publishAppUpdate(page, {version: '0.8.0', phase: 'available'});
  await reveal();
  const updateWidth = (await page.locator('#pharosAppUpdate').boundingBox()).width;
  await page.locator('#pharosAppUpdate').hover();
  await shot('release-available');
  await page.locator('#pharosAppUpdate').click();
  await reveal();
  await shot('download-started');
  for (const state of [
    {version: '0.8.0', phase: 'downloading', progress: 42},
    {version: '0.8.0', phase: 'preparing'},
    {version: '0.8.0', phase: 'installing'},
    {version: '12.123.456-beta.123', phase: 'available'},
  ]) {
    await publishAppUpdate(page, state);
    await reveal();
    assert.equal((await page.locator('#pharosAppUpdate').boundingBox()).width, updateWidth, 'progress and long version tooltips keep the same icon width');
    await page.locator('#pharosAppUpdate').hover();
    await shot(state.phase === 'available' ? 'long-version' : state.phase);
  }
  // Availability can change while the overflow popup is closed. It must
  // still use the new measured width when tools return to the header.
  if (await page.locator('#headerOverflow').evaluate(el => el.matches(':popover-open'))) await page.keyboard.press('Escape');
  await publishAppUpdate(page, {});
  await page.setViewportSize({width: 1440, height: 900});
  await settled();
  assert.deepEqual(await layoutProblems(page), []);
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

test('Ask Pharos suggestions stay readable, prefill the editor and remember being hidden', { skip }, async t => {
  const {page,settled,shot,calls}=await open(t,{url:'/',ready:'.pharos-idea-toggle',viewport:{width:2133,height:1070},api:{
    ...askAPI,
    'POST /api/ask/session':({body,state})=>state.suggestionDraft={...askSession,...body,conversation:[]},
    'GET /api/ask/session':({query,state})=>state.suggestionDraft||{...askSession,id:query.get('id'),conversation:[]},
    'GET /api/query/library/distinct':({query})=>query.get('field')==='repository_name'?{values:['pharos','query-table'],hasMore:false}:undefined,
  }});
  const suggestions=page.getByRole('region',{name:'Prompt suggestions',exact:true});
  const viewport=page.getByRole('region',{name:'Browse prompt suggestions',exact:true});
  const toggle=suggestions.getByRole('button',{name:'Hide suggestions',exact:true});
  const readable=async()=>{
    const clipped=await viewport.locator('[data-reading] .pharos-idea-text').evaluateAll(labels=>labels.some(label=>{
      const text=document.createRange();text.selectNodeContents(label);
      const rect=text.getBoundingClientRect(),box=label.closest('button').getBoundingClientRect();
      return rect.left<box.left||rect.right>box.right||rect.top<box.top||rect.bottom>box.bottom;
    }));
    assert.equal(clipped,false,'suggestion questions fit their cards');
  };
  for(const theme of ['dark','light']){
    await page.evaluate(theme=>window.pharosPrefs.set('pharos-theme',theme),theme);
    await page.evaluate(theme=>document.documentElement.dataset.theme=theme,theme);
    for(const width of [2133,390,320]){
      await page.setViewportSize({width,height:1070});await settled();
      assert.deepEqual(await layoutProblems(page),[]);
      await readable();
      await shot(`expanded-${theme}-${width}`);
    }
  }
  await page.setViewportSize({width:2133,height:1070});await settled();
  for(let step=0;step<7;step++){
    await suggestions.getByRole('button',{name:'Next suggestions',exact:true}).click();
    await readable();
  }
  await suggestions.getByRole('button',{name:'Refresh suggestions',exact:true}).click();
  await readable();
  const card=viewport.locator('[data-reading]').filter({hasText:'repository'}).first();
  const prompt=await card.locator('.pharos-idea-text').textContent();
  await card.click();
  const editor=page.getByRole('textbox',{name:'Ask a question',exact:true});
  assert.equal(await editor.evaluate(element=>element.value),prompt);
  assert.ok(await editor.evaluate(element=>element===document.activeElement));
  assert.ok(!calls.some(call=>call.path==='/api/ask/send'),'choosing an idea does not send a question');
  await toggle.focus();await page.keyboard.press('Enter');
  await viewport.waitFor({state:'hidden'});
  const show=suggestions.getByRole('button',{name:'Show suggestions',exact:true});
  assert.equal(await show.getAttribute('aria-expanded'),'false');
  assert.ok(await show.evaluate(element=>element===document.activeElement));
  assert.equal(await editor.evaluate(element=>element.value),prompt,'collapsing preserves the draft');
  await page.evaluate(()=>window.pharosPrefs.flush());await settled();
  await page.reload();await show.waitFor();await settled();
  assert.ok(await viewport.isHidden(),'the library preference survives reload');
  await shot('collapsed');
  await show.click();await viewport.waitFor();await settled();
  await page.evaluate(()=>window.pharosPrefs.flush());
  await page.reload();await toggle.waitFor();await viewport.waitFor();await settled();
  assert.equal(await toggle.getAttribute('aria-expanded'),'true');
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
  { name: "Tools", topic: "tools", card: "tool-failure-rates", dataset: "tool_calls", field: "started_at", first: "day", last: "day", graph: ".usage-tools-dashboard .usage-analysis-dashboard .qt-metric", chart: { period: "day", metric: "calls", split: "tool_name", view: "compare" } },
  { name: "Pharos MCP", topic: "mcp", card: "mcp-response-size", dataset: "mcp_calls", field: "called_at", first: "called_at", last: "called_at", graph: ".usage-mcp-dashboard .usage-analysis-dashboard .qt-metric", chart: { period: "day", metric: "calls", split: "tool_name", view: "compare" } },
  { name: "Carbon Impact", topic: "carbon", card: "carbon-trend", dataset: "usage", field: "first_usage_at", first: "first_usage_at", last: "first_usage_at", summary: "Filtered carbon estimate", chart: { period: "day", metric: "co2", split: "type" } },
]) for (const width of [1504, 390, 320]) test(`${dashboard.name} dashboard and inline ranges fit at ${width}px`, { skip }, async t => {
  const where = [{ field: dashboard.last, op: ">=", value: "-7d" }, { field: dashboard.first, op: "<", value: "+0s" }];
  const sourceWhere = where.map(term => ({ ...term, field: dashboard.field }));
  const params = new URLSearchParams({ analyze_mode: "dashboards", analyze_topic: dashboard.topic,
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
  if (dashboard.topic === 'tools' || dashboard.topic === 'mcp') await page.getByRole('combobox', { name: 'View', exact: true }).selectOption('trend');
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

for (const width of [1504, 390, 320]) test(`Parallelism dashboard and inline ranges fit at ${width}px`, { skip }, async t => {
  const where = [{ field: 'started_at', op: '>=', value: '-7d' }, { field: 'started_at', op: '<', value: '+0s' }];
  const params = new URLSearchParams({ analyze_mode: 'dashboards', analyze_topic: 'parallelism',
    q_parallelism: encodeQuery({ ...EMPTY_QUERY, where }), window_mode: 'relative', window_days: '7', window_field: 'started_at', window_dataset: 'parallelism',
    analyze_parallelism: JSON.stringify({ range: '7d' }) });
  const app = await open(t, { url: `/analyze?${params}`, viewport: { width, height: 933 }, concurrency: true, ready: '.usage-dashboard .concurrency-chart svg' });
  const { page } = app;
  const chart = await page.locator('.concurrency-chart').boundingBox();
  const stats = await page.getByRole('group', { name: 'Parallelism summary', exact: true }).boundingBox();
  assert.ok(stats.y >= chart.y + chart.height, 'summary statistics follow the graph');
  assert.deepEqual(await layoutProblems(page), []);
  await app.shot('relative-card-range');
  // The ribbon's Range; the engine views below have their own.
  const range = page.getByRole('region', { name: 'Parallelism chart', exact: true }).getByRole('combobox', { name: 'Range', exact: true });
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

describe("structure", { concurrency: Number(process.env.PHAROS_SWEEP_CONCURRENCY || 3) }, () => {
  for (const route of ROUTES) {
    for (const viewport of WIDTHS) {
      const name = `${route.name} @ ${viewport.width}`;
      test(name, { skip }, async t => {
        const app = await open(t, { url: route.url, viewport, ready: route.ready, preferences: route.preferences, concurrency: route.concurrency, managedLibrary: route.managedLibrary, api: route.api });
        if (route.open) {
          await route.open(app.page);
          await app.page.locator(route.shown).first().waitFor();
          await app.settled();
        }
        // A fitted table stops scrolling sideways at desktop widths. One that
        // still scrolls once the wait ends is a problem, so KNOWN can name it.
        const fitting = [];
        if (route.fitTable && viewport.width >= 1024) {
          await app.page.waitForFunction(() => {
            const wrap = document.querySelector('.usage-workspace-table .qt-table-wrap');
            return wrap && wrap.scrollWidth <= wrap.clientWidth + 1;
          }).catch(async error => {
            const size = await app.page.evaluate(() => {
              const wrap = document.querySelector('.usage-workspace-table .qt-table-wrap');
              return wrap ? { scroll: wrap.scrollWidth, client: wrap.clientWidth } : null;
            });
            if (!size) throw error;
            // Fitting may have finished as the wait ended.
            if (size.scroll > size.client + 1) fitting.push(`fitted table still scrolls sideways: ${size.scroll}px wide in ${size.client}px`);
          });
        }
        if (route.check) await route.check(app.page);
        await app.shot("top");
        const problems = await layoutProblems(app.page);
        if (!route.open) {
          problems.push(...await scrollProblems(app.page));
          await app.settled();
          await app.shot("scrolled");
          problems.push(...(await layoutProblems(app.page)).filter(problem => !problems.includes(problem)));
        }
        problems.push(...fitting, ...app.problems());
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

// Card links as the site map cases hold them: each card at its first default
// window, and some at every window. Open the 7-day one where there is one.
const cardLinks = JSON.parse(readFileSync(new URL('../../../testdata/site-map-cases.json', import.meta.url), 'utf8')).cards;

// Card links open full dashboards. Check navigation into supporting queries
// and back, and that each chart family fits the available viewport.
for (const id of ['human-words', 'cost-repos', 'machine-time', 'skill-evidence', 'tool-failure-rates', 'mcp-response-size', 'carbon-trend', 'parallel-days', 'machine-latency-trend', 'session-input-output', 'human-message-lengths', 'writing-hours', 'session-human-output', 'error-signatures']) {
  for (const width of [1504, 390, 320]) test(`Card dashboard ${id} fits at ${width}px`, { skip }, async t => {
    const item = cardLinks.find(card => card.id === id && card.days === 7) ?? cardLinks.find(card => card.id === id);
    const app = await open(t, { url: item.url, viewport: { width, height: 933 }, ready: '.usage-query-destinations a' });
    const { page } = app;
    await app.settled();
    assert.equal(await page.getByRole('alert').count(), 0, 'the matching analysis is available');
    assert.deepEqual(await layoutProblems(page), []);
    const details = page.locator('.usage-query-destinations a').first();
    await details.waitFor();
    await app.shot(`card-dashboard-${id}`);
    await details.click();
    await page.locator('.usage-workspace-table').waitFor();
    await page.getByRole('link', { name: /^Back to dashboard/ }).click();
    await page.locator('.usage-dashboard .usage-query-destinations a').first().waitFor();
    await app.settled();
    assert.deepEqual(await layoutProblems(page), []);
  });
}


for (const choice of ['today', '24h']) test(`Query time window picker keeps ${choice} selected after replacing a custom range`, { skip }, async t => {
  const params = new URLSearchParams({ analyze_mode: 'query', analyze_topic: 'human', analyze_data: 'writing_messages', q_writing_messages: encodeQuery({ ...EMPTY_QUERY, where: [{ field: 'sent_at', op: '<=', value: '2026-01-31' }] }) });
  const app = await open(t, { url: `/analyze?${params}`, ready: 'select[aria-label="Query time window"]' });
  const picker = app.page.getByRole('combobox', { name: 'Query time window', exact: true });
  const executed = app.page.waitForResponse(response => new URL(response.url()).pathname.startsWith('/api/query/writing_messages') && response.request().method() === 'POST');
  await picker.selectOption(choice);
  await executed;
  await app.settled();
  assert.equal(await picker.inputValue(), choice);
  assert.equal(new URL(app.page.url()).searchParams.get('window_mode'), choice === 'today' ? 'today' : 'relative');
});


test("Card dashboard opens from the Card Library and supports changing views and reloading", { skip }, async t => {
  const app = await open(t, { url: '/cards?card_search=error-signatures&card_topic=all&card_window=7', ready: '.query-card-open' });
  const { page } = app;
  await page.locator('.query-card-open').first().click();
  const view = page.getByRole('combobox', { name: 'View', exact: true });
  await view.waitFor();
  assert.equal(new URL(page.url()).searchParams.get('analyze_topic'), 'calls');
  await view.selectOption('trend');
  await page.waitForFunction(() => JSON.parse(new URLSearchParams(location.search).get('analyze_chart')).view === 'trend');
  await page.reload();
  await page.getByRole('combobox', { name: 'View', exact: true }).waitFor();
  assert.equal(await page.getByRole('combobox', { name: 'View', exact: true }).inputValue(), 'trend');
  await app.settled();
  assert.deepEqual(app.problems(), []);
});


const oldActivitySource = { ...EMPTY_QUERY, where: [
  { field: "root_effort", op: "=", value: "high" },
  { field: "event_at", op: ">=", value: "2026-10-01T15:00:00.000Z" },
  { field: "event_at", op: "<", value: "+0s" },
] };
function oldActivityCard(token = encodeQuery(oldActivitySource)) {
  return `/analyze?${new URLSearchParams({ analyze_mode: "dashboards", analyze_topic: "human", analyze_data: "writing_messages",
    analyze_chart: JSON.stringify({ view: "trend", metric: "words" }), analyze_filters: encodeQuery(EMPTY_QUERY),
    card_id: "session-human-output", card_query: "conversation_activity", q_conversation_activity: token,
    window_mode: "today", window_days: "1", window_dataset: "conversation_activity", window_field: "event_at", window_timezone: "Asia/Tokyo",
  })}`;
}
for (const durable of [false, true]) test(`Older card dashboard restores ${durable ? "durable" : "inline"} sources before rendering`, { skip }, async t => {
  const code = "eeeeeeeeeeeeeeeeeeeeee";
  const app = await open(t, { url: oldActivityCard(durable ? `@${code}` : undefined), ready: '.usage-query-destinations a',
    api: { "GET /api/query-snapshots": { version: 1, code, token: encodeQuery(oldActivitySource), createdAt: 0 } },
  });
  const { page } = app;
  await page.getByRole('heading', { name: 'Conversation Input and Output', exact: true }).waitFor();
  assert.equal(await page.getByRole('combobox', { name: 'View', exact: true }).inputValue(), 'scatter');
  const params = new URL(page.url()).searchParams;
  assert.equal(params.get('analyze_topic'), 'activity');
  assert.equal(params.get('analyze_window'), 'today');
  assert.equal(params.get('analyze_timezone'), 'Asia/Tokyo');
  assert.ok(app.calls.filter(call => call.method === 'POST' && call.path.startsWith('/api/query/')).every(call => !call.path.startsWith('/api/query/writing_messages/')), 'the old dashboard never executes');
  await page.locator('.usage-query-destinations a').first().click();
  await page.locator('.usage-workspace-table').waitFor();
  assert.equal(new URL(page.url()).searchParams.get('window_timezone'), 'Asia/Tokyo');
  await page.getByRole('link', { name: /^Back to dashboard/ }).click();
  await page.locator('.usage-query-destinations a').first().waitFor();
  await page.reload();
  await page.getByRole('combobox', { name: 'View', exact: true }).waitFor();
  assert.equal(await page.getByRole('combobox', { name: 'View', exact: true }).inputValue(), 'scatter');
  await app.settled();
});

test('Older card dashboard restoration cannot overwrite a later route', { skip }, async t => {
  let release, started;
  const gate = new Promise(resolve => { release = resolve; }), requested = new Promise(resolve => { started = resolve; });
  t.after(() => release());
  const code = 'ffffffffffffffffffffff';
  const app = await open(t, { url: '/analyze?analyze_topic=tokens', ready: '.usage-query-destinations a',
    api: { 'GET /api/query-snapshots': async () => { started(); await gate; return { version: 1, code, token: encodeQuery(oldActivitySource), createdAt: 0 }; } },
  });
  const { page } = app;
  await page.evaluate(url => window.pharosNavigate(url), oldActivityCard(`@${code}`));
  await requested;
  await page.getByRole('status').filter({ hasText: 'Opening dashboard' }).waitFor();
  await page.evaluate(() => window.pharosNavigate('/analyze?analyze_mode=dashboards&analyze_topic=calls&analyze_data=tool_calls'));
  await page.getByRole('heading', { name: 'Tool Calls', exact: true }).waitFor();
  release();
  await app.settled();
  assert.equal(new URL(page.url()).searchParams.get('analyze_topic'), 'calls');
  await page.getByRole('heading', { name: 'Tool Calls', exact: true }).waitFor();
});

test("Markdown local destinations become navigable only after availability is confirmed", { skip }, async t => {
  const app=await open(t,{url:`${work}/conversation/${rootConversation}`,api:markdownLinkAPI,ready:".markdown-link"});
  const {page}=app;
  await page.waitForFunction(()=>!document.querySelector('[data-link-status="checking"]'));
  const source=page.getByRole("link",{name:"Source file",exact:true});
  assert.ok(await source.getAttribute("href"));
  assert.equal(await source.getAttribute("aria-disabled"),null);
  assert.equal(await page.getByRole("link",{name:"Running dev server",exact:true}).getAttribute("href"),"http://localhost:5173/");
  for(const name of ["Missing file","Stopped dev server"]){
    const link=page.getByRole("link",{name,exact:true});
    assert.equal(await link.getAttribute("aria-disabled"),"true");
    assert.equal(await link.getAttribute("href"),null);
    await link.focus();await page.keyboard.press("Enter");
    assert.equal(new URL(page.url()).pathname,`${work}/conversation/${rootConversation}`);
  }
  const checks=app.calls.filter(call=>call.path==='/api/markdown-link'&&call.query.get('href')==='internal/archive/api.go:12');
  assert.equal(checks.length,1,"repeated file links share one check");
  assert.equal(checks[0].query.get('workspace'),workID);
  for(const [name,href] of [["Library view","/library?view=conversation"],["Filtered usage","/analyze?analyze_topic=tokens"]]){
    const link=page.getByRole("link",{name,exact:true});
    assert.equal(await link.getAttribute("href"),href);
    assert.equal(await link.getAttribute("aria-disabled"),null);
    assert.equal(app.calls.some(call=>call.path==='/api/markdown-link'&&call.query.get('href')===href),false,"Pharos routes must not be probed as files");
  }
  await app.shot("link-states");
  await page.getByRole("link",{name:"Library view",exact:true}).click();
  await page.waitForURL(url=>url.pathname==='/library'&&url.searchParams.get('view')==='conversation');
  await page.locator('.conversation-result-card').first().waitFor();
});
