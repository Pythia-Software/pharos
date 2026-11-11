// The structural sweep: every page the shell serves, at a desktop, a phone
// and a narrow phone width. Each must render its main content without page,
// console or request errors, keep everything inside the viewport, main and
// its cards, and keep the header in place while main scrolls.
//
// Adding a page is one line in ROUTES: a URL and a selector that appears once
// its main content has rendered. `open` runs before the checks, for surfaces
// reached by a click rather than a URL. A route without `open` must also
// leave initial focus to the user and keep its title unfocusable, unless it
// sets `movesFocus` (a modal that focuses its own title, say).
import { describe, test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { activateHeaderTool, open, skip } from "./harness.mjs";
import { layoutProblems, scrollProblems } from "./layout.mjs";
import { claudeImportFixture, defaultApi, projectColorFixture, cloudScheduledStatus, rootConversation, workID, peerWorkID, relatedWork, eventStream, idleStatus, busyStatus, managedStatus, temporaryBackgroundDocument, workBackgroundDocument, workInspection, pendingEvidenceApi } from "./fixtures.mjs";
import { EMPTY_QUERY, encodeQuery, withGroupingKeys } from "@pythia-software/query-table-core";

const missingPrices = { ...defaultApi['GET /api/pricing'], unpriced_tokens: 5000,
  unpriced_models: [{ model: 'unpriced-preview-model-with-a-long-reported-name', tokens: 5000 }],
  prompt: 'Research unpriced-preview-model-with-a-long-reported-name.\nEdit pricing/cost_changes.json with sourced prices.' };

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
    if(href==="internal/archive/api.go:12")return {status:"file",detail:"File available on this Mac",href:"/api/markdown-link?open=1&href=internal/archive/api.go%3A12",path:"/workspace/internal/archive/api.go",launchable:false};
    if(href==="http://localhost:5173/")return {status:"server",detail:"Local server returned HTTP 200"};
    return {status:"unavailable",detail:href.startsWith('http')?"Local server unavailable on this Mac":"File unavailable on this Mac"};
  },
  "POST /api/markdown-link": ({query}) => ({opened:"/workspace/"+query.get("href").split(":")[0],revealed:false}),
};
const work = `/work/${workID}`;
const tokenEvidenceAPI = {
  "GET /api/work/:id/delegation": request => {
    const data = defaultApi["GET /api/work/:id/delegation"](request);
    const patch = row => row.id !== "s-nested" ? row : {...row,
      self: {...row.self, tokens: {value:116837,quality:"reported-minimum",known:1,total:1}, cost:{value:null,quality:"unknown",known:0,total:1}},
      all: {...row.all, tokens: {value:116837,quality:"reported-minimum",known:1,total:1}},
      token_attribution: {accounted_tokens:0,direct_tokens:0,unallocated_tokens:0,observed_minimum_tokens:116837,
        evidence:[{scope:"last-request",total_tokens:116837,message_id:"conv-child-reply",conversation_id:"conv-child",workspace_id:workID}]}};
    return {...data, rows:data.rows.map(patch), focus:data.focus.map(patch)};
  },
};
// Related work: chips (one uncertain) and a certain review's session among
// the workspace's own.
const relatedWorkAPI = {
  "GET /api/work/:id": request => ({ ...defaultApi["GET /api/work/:id"](request), ...relatedWork }),
  "GET /api/work/:id/delegation": request => {
    const data = defaultApi["GET /api/work/:id/delegation"](request), [review] = relatedWork.peers;
    const peer = { ...data.rows[0], id: "s-peer", workspace_id: peerWorkID, conversation_id: "conv-review", label: review.title, outline: "2", order: data.rows.length, child_count: 0,
      peer: { workspace_id: peerWorkID, title: review.title, role: review.role } };
    return { ...data, rows: [...data.rows, peer], focus: [...data.focus, peer], matched: { ...data.matched, [peer.id]: true }, total: data.total + 1, session_count: data.session_count + 1, group_totals: data.totals };
  },
  "GET /api/work/:id/conversations/:conversation": request => request.params.id === peerWorkID ? { ...relatedWork.peer_conversations[0],
    messages: [{ id: "conv-review-ask", role: "user", kind: "message", text: "Review the changes" }, { id: "conv-review-reply", role: "assistant", kind: "message", text: "P1: the sort ignores ties." }] }
    : defaultApi["GET /api/work/:id/conversations/:conversation"](request),
};
// The root session's last reply, which the card previews as its latest response.
const latestResponseID = defaultApi["GET /api/work/:id/conversations/:conversation"]({ params: { id: workID, conversation: rootConversation } }).messages
  .filter(message => message.role === "assistant" && message.kind === "message" && !message.parent_native_id).at(-1).id;
const conversationResultAPI = {
  "POST /api/query/library/:operation": request => {
    const result = defaultApi["* /api/query/:dataset/:operation"]({ ...request, params: { dataset: "library", operation: request.params.operation } });
    if (!result?.rows) return result;
    return { ...result, total: 1, rows: result.rows.length ? [{ ...result.rows[0], id: workID, title: "Investigate the incremental indexer", repository_name: "pharos", models: "claude-opus-5-5", efforts: "high", token_count: 190000, turn_count: 12, subagent_count: 2, preview_elapsed_ms: 4800000, cost_usd: 3.3, pr_numbers: "", first_input: `<system_instruction>Initialize the workspace</system_instruction>\n\nReview **the incremental indexer** and its nested agents. ${"Check the recovery path after sleep and preserve the existing index. ".repeat(12)}`, last_response: `The review is **complete**. ${"The index resumes after sleep without replaying retained captures. ".repeat(20)}`, last_response_id: latestResponseID }] : [] };
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
  return {id,native_id:id,role:"user",kind:"message",text:parts.map(([text])=>text).join(""),authorship:{typed_words:48,provisional:true,spans:parts.map(([text,category])=>{const start=end;end+=Buffer.byteLength(text);return {start,end,category,words:text.split(/\s+/).length};})}};
}
const authoredPrompt = classifiedPrompt("human-mixed",humanPromptParts);
const authoredTurnMessages = [authoredPrompt,
  ...Array.from({length:5},(_,index)=>[
    {id:index===0?"m-15":`review-${index}`,native_id:index===0?"m-15":`review-${index}`,role:"assistant",kind:"delegation",call_id:`reviewer-${index}`,text:JSON.stringify({name:"Task",input:{subagent_type:`Reviewer ${index+1}`,prompt:["Review the parent turn structure","Check nested review groups","Verify human-written text highlighting","Check keyboard navigation","Check the narrow screen layout"][index]}})},
    {id:`review-reply-${index}`,native_id:`review-reply-${index}`,role:"assistant",kind:"message",parent_native_id:`reviewer-${index}`,text:`Review ${index+1} complete. The assigned check passed.`},
  ]).flat(),
  {id:"nested-review",native_id:"nested-review",role:"assistant",kind:"delegation",call_id:"nested-reviewer",parent_native_id:"reviewer-0",text:JSON.stringify({name:"Task",input:{subagent_type:"Nested reviewer",prompt:"Check the child branch under Reviewer 1"}})},
  {id:"nested-review-reply",native_id:"nested-review-reply",role:"assistant",kind:"message",parent_native_id:"nested-reviewer",text:"The nested review stays under Reviewer 1."},
  {id:"parent-reply",native_id:"parent-reply",role:"assistant",kind:"message",text:"All five reviews are complete. The outline keeps each review inside the turn that requested it, including the nested review under Reviewer 1.",
    labels:{segment:"AR",ids:["C","C.report","C.report.completion"],probs:{"C.report.completion":0.93}},
    agent_events:[{id:"e-review",kind:"workaround",label:"Workaround",group:"Choices you may not see",probability:0.81,sentence:[31,141],subject:[],response:[],
      target:{method:"overlap",message_id:"human-mixed",role:"user",kind:"message",start:authoredPrompt.text.indexOf("keep my written"),end:authoredPrompt.text.indexOf("keep my written")+"keep my written instructions easy to spot".length,text:"keep my written instructions easy to spot",shared:["written","instructions"],score:5.1}}]},
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
  "GET /api/work/:id/conversations/:conversation": {id:rootConversation,workspace_id:workID,provider:"codex",messages:authoredTurnMessages,
    labels:{labeled:true,pending:false,taxonomy:[{id:"C.report.completion",name:"Completion",definition:"Reports finished work."}]},agent_events:{messages_read:8}},
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
// The caller comes back after most of an hour with nothing running: the
// Timeline skips the gap (a cut on every row) and Agent events is expanded.
const fixtureSpan = defaultApi["GET /api/work/:id/delegation"]({ query: new URLSearchParams() });
const resumedAt = fixtureSpan.start + 3600000;
const idleTimelineAPI = {
  "GET /api/work/:id/delegation": request => { const value = defaultApi["GET /api/work/:id/delegation"](request); return { ...value, end: resumedAt + 60000, rows: value.rows.map(row => row.depth === 0 ? { ...row, end: resumedAt + 60000 } : row) }; },
  "GET /api/work/:id/parallel": request => { const value = defaultApi["GET /api/work/:id/parallel"](request); return { ...value, lanes: value.lanes.map(lane => lane.session !== "s-root" || !lane.messages || resumedAt < value.start || resumedAt > value.end ? lane : { ...lane, messages: [...lane.messages, { id: "m-back", role: "user", kind: "message", created_at: new Date(resumedAt).toISOString(), text: "Back again: carry on.", authorship: [{ category: "typed", bytes: 21 }] }] }) }; },
};
const idleTimelineDisclosures = encodeURIComponent(JSON.stringify({ "delegation-labels:s-root:session": true, "delegation-labels:s-root:events": true }));

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
const backgroundTaskStatus = { ...busyStatus,
  operations: [{ ...busyStatus.operations[0], kind: 'background.update', variant: 'frequent', label: 'Frequent update', priority: 'background',
    step: { index: 2, count: 3, name: 'Your writing', unit: null, done: 0, total: null },
    children: [{id: 'chain', kind: 'analysis.chain', label: 'Update analyses', state: 'running', children: [
      {id: 'authorship', kind: 'analysis.authorship', label: 'Classify your writing', state: 'running', children: [
        {id: 'rebuild', kind: 'analysis.authorship', label: 'Classify your writing', state: 'running',
          step: {name: 'classifying authorship', unit: 'message', done: 601, total: 42918, fraction: 601 / 42918}, children: []},
      ]},
    ]}] }, {id: 'label-index', kind: 'maintenance.labels.query', label: 'Index message labels', state: 'running', priority: 'background',
      step: {name: 'Indexing message labels', unit: 'conversation', done: 1200, total: null}, children: []},
    {id: 'agent-events', kind: 'analysis.agent_events', label: 'Find earlier agent events', state: 'running', priority: 'background',
      step: {name: 'finding agent events', unit: 'conversation', done: 310, total: 21123, fraction: 310 / 21123,
        detail: 'Scoring on GPU · 21,887 messages saved · 68,992 candidates scored · 36 candidates/s overall'}, children: []}],
  background: { ...idleStatus.background,
    items: idleStatus.background.items.map(item => item.id === 'writing' ? { ...item, state: 'running', state_label: 'Updating now' } : item),
    cadences: idleStatus.background.cadences.map(cadence => cadence.id === 'frequent' ? { ...cadence, state: 'running', state_label: 'Updating now' } : cadence),
  },
  attention: [],
};
const askFeedbackAPI = {...askAPI,"GET /api/ask/session":{...askSession,conversation:[{...askSession.conversation[0],status:"feedback",cards:[],feedback:{kind:"feature",comment:"Add Markdown export of selected conversations so I can share a review.\nPreserve message order and tool-call context."}}]}};
const ROUTES = [
  {name:"Scatter Plots",url:"/analyze?analyze_topic=scatter&analyze_data=scatter",ready:".usage-scatter-dashboard"},
  {name:"Compaction comparison",url:"/analyze?"+new URLSearchParams({analyze_topic:"compaction",analyze_data:"compactions",analyze_chart:JSON.stringify({view:"compare",metric:"compactions",split:"trigger_provider"})}),ready:analysisReady},
  {name:"Compaction",url:"/analyze?analyze_topic=compaction&analyze_data=compactions",ready: `.usage-compaction-dashboard ${overTimeReady}`},
  {name:"Waiting",url:"/analyze?analyze_topic=thinking-overview&analyze_data=model_requests",ready:'.thinking-dashboard[aria-busy="false"] .thinking-overview-stats'},
  ...['time','heatmap','histogram','reasoning','wait-box','calendar'].map(view=>({name:'Thinking '+view,url:'/analyze?'+new URLSearchParams({analyze_topic:'thinking-detail',analyze_data:'model_requests',thinking:JSON.stringify({view,groupBy:'model_effort'})}),ready:'.thinking-dashboard[aria-busy="false"] .thinking-population'})),
  {name:'Library settings expanded',url:'/settings/libraries',ready:'#library-settings-current h3',managedLibrary:{id:'local',name:'Pending history',config:'/Users/test/Libraries/Pending/library.toml'},open:async page=>{
    await page.locator('#library-settings-transfer-panel summary').click();
    await page.locator('#library-settings-delivery-toggle').click();
    await page.locator('#library-settings-list [data-rename-id]').first().click();
  },shown:'.library-settings-rename'},
  ...['overview','open','create','restore'].map(kind=>({name:'Library settings '+kind,url:'/settings/libraries',ready:'#library-settings-current h3',managedLibrary:{id:'local',name:'Pending history with a long library name',config:'/Users/test/Libraries/Pending/library.toml'},...(kind==='overview'?{}:{open:page=>page.locator({open:'#library-settings-open',create:'#library-settings-new',restore:'#library-settings-restore'}[kind]).click(),shown:'#library-settings-'+kind+'-form:not([hidden])'})})),
  // The setup window is a modal dialog that focuses its title on open
  // (application.html), so its routes declare movesFocus.
  {name:"Application first launch",url:"/app",ready:"#application-setup[open]",movesFocus:true,api:{"GET /api/app/state":{application:"/Applications/Pharos.app",selected:"",mcp:"",busy:false,libraries:[]}}},
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
  {name:"Application library chooser",url:"/app",ready:"#library-list .card",movesFocus:true,check:async page=>{
    const widths=await page.locator('#application-intro').evaluate(intro=>({
      available:intro.getBoundingClientRect().width,
      copy:intro.querySelector('.setup-copy').getBoundingClientRect().width,
    }));
    assert.ok(widths.copy>widths.available*.8,'the library introduction leaves an unused artwork column');
  }},
  // One route per screen of source setup: how to find, what the usual
  // places hold (or that they hold nothing), the folders named by hand, the
  // review (with and without anything to add) and importing an export.
  ...['start','found','nothing','manual','review','empty','import'].map(step=>({name:`Application sources ${step}`,url:'/app',ready:'#application-setup[open]',api:{
    'GET /api/app/state':{application:'/Applications/Pharos.app',selected:'',mcp:'',busy:false,libraries:[]},
    'POST /api/app/create':{saved:true},
    ...(step==='nothing'?{'GET /api/probe/tools':{tools:[]}}:{}),
  },open:async page=>{
    await page.locator('#meet-pharos').click();await page.locator('#start-setup').click();
    for(let i=0;i<4;i++)await page.locator('#submit').click();
    await page.locator('#configure-sources').click();
    const guide=page.locator('#pharosOnboarding'),next=guide.getByRole('button',{name:'Continue',exact:true});
    await guide.locator('.pharos-places>summary').waitFor();
    if(step==='start'){await guide.locator('.pharos-places>summary').click();return}
    if(step==='import'){await guide.getByRole('button',{name:'Import an export',exact:true}).click();return}
    if(step==='manual'){
      await guide.getByRole('radio',{name:/tell Pharos where to look/}).check();await next.click();
      await guide.getByRole('checkbox',{name:/Cursor/}).check();return;
    }
    await next.click();
    if(step==='nothing')return guide.getByRole('button',{name:'Choose tools and folders',exact:true}).waitFor();
    await guide.locator('.pharos-candidate').first().waitFor();
    if(step==='empty')for(const box of await guide.locator('.pharos-candidate input[type=checkbox]').all())await box.uncheck();
    if(step!=='found')await next.click();
  },shown:'#pharosOnboarding'})),
  {name:'Claude Downloads watch',url:'/settings/sources',ready:'#claudeDownloadsWatchSection .claude-watch-export',api:{
    'GET /api/claude-imports':{jobs:[],run:{running:false},watch:{enabled:true,available:true,browser:'',browsers:[],exports:[
      {id:'queued-export',state:'fetching',phase:'queued',exported_at:'2026-11-10T20:15:05Z',collected:0,expected:6,message:'Waiting for another capture or index to finish. Collection resumes automatically when the library is free.'},
      {id:'indexing-export',state:'fetching',phase:'indexing',exported_at:'2026-11-10T19:15:05Z',collected:6,expected:6,ingestion:{stage:'indexing',processed:25,workspaces:20,conversations:20,messages:150,skipped_current:5}},
    ],library:{workspaces:25,conversations:25,recent:[{id:workID,title:'Plan a project with a long workspace title that wraps at narrow window widths',url:`/work/${workID}`}]}}},
  },check:async(page,app)=>{
    await page.locator('#claudeDownloadsWatchSection .claude-watch-exports').scrollIntoViewIfNeeded();
    await app.shot('ingestion');
    const workspace=page.locator(`#claudeDownloadsWatchSection .claude-watch-library a[href="${work}"]`);
    await workspace.focus();
    const next=structuredClone(ROUTES.find(route=>route.name==='Claude Downloads watch').api['GET /api/claude-imports']);
    next.watch.library.workspaces++;
    next.watch.library.recent.unshift({id:peerWorkID,title:'Another imported conversation',url:`/work/${peerWorkID}`});
    next.watch.exports[1].ingestion.messages++;
    await page.route('**/api/claude-imports',route=>route.fulfill({json:next}),{times:1});
    const refreshed=page.waitForResponse(response=>new URL(response.url()).pathname==='/api/claude-imports');
    await page.evaluate(()=>window.pharosClaudeDownloads.render(document.getElementById('claudeDownloadsWatchSection')));
    await refreshed;await app.settled();
    assert.ok(await workspace.evaluate(element=>element===document.activeElement),'ingestion progress refresh lost the focused workspace link');
    const stop=page.locator('#claudeDownloadsWatchSection [data-id="indexing-export"]').getByRole('button',{name:'Stop',exact:true});
    await stop.focus();
    // Let the next automatic poll restore the original fixture: both the
    // recent workspace list and the focused export's progress change.
    await page.waitForResponse(response=>new URL(response.url()).pathname==='/api/claude-imports',{timeout:10000});
    await app.settled();
    assert.ok(await stop.evaluate(element=>element===document.activeElement),'automatic ingestion polling lost the focused Stop button');
    await workspace.click();
    await page.waitForURL(url=>url.pathname===work);
    await page.locator('.delegation-table tbody tr').first().waitFor();
    await page.goBack();
    await page.locator('#claudeDownloadsWatchSection .claude-watch-export').first().waitFor();
    await page.getByRole('link',{name:'Browse all Claude.ai workspaces',exact:true}).click();
    await page.waitForURL('**/library?*');
    await page.locator('#queryTableLibrary').waitFor();
    await page.goBack();
    await page.locator('#claudeDownloadsWatchSection .claude-watch-export').first().waitFor();
  }},
  {name:'Claude export collection',url:'/settings/sources',ready:'#pharosAddSource',api:{
    'GET /api/claude-imports':({query})=>query.get('id')?claudeImportFixture:{jobs:[{...claudeImportFixture,collected:1,expected:2}],run:{running:false}},
    'POST /api/claude-imports/links':{links:{'part-two':'https://claude.ai/export/download/synthetic-token'}},
  },open:async page=>{
    await page.evaluate(()=>window.pharosOnboarding.open({mode:'import'}));
    const guide=page.locator('#pharosOnboarding');
    await guide.getByRole('radio',{name:'Claude.ai',exact:true}).check();
    await guide.getByRole('button',{name:/Resume export/}).click();
    await guide.getByRole('button',{name:'Show missing download links',exact:true}).click();
    await guide.getByRole('link',{name:/Download projects/}).waitFor();
  },shown:'#pharosOnboarding'},
  {name:'Source settings guide',url:'/settings/sources',ready:'#pharosFindSources',open:async page=>{
    const trigger=page.locator('#sourceToolbar').getByRole('button',{name:'Data Guide',exact:true});
    await trigger.click();
    const guide=page.getByRole('dialog',{name:'Data Guide',exact:true});
    await guide.waitFor();
    await guide.getByRole('button',{name:'Explain sources from Cursor',exact:true}).waitFor();
    await page.keyboard.press('Escape');
    assert.ok(await trigger.evaluate(element=>element===document.activeElement),'Sources guide lost its invoking control');
    await page.locator('#pharosFindSources').click();
  },shown:'#pharosOnboarding',check:async(page,app)=>{
    const trigger=page.locator('#pharosOnboarding').getByRole('button',{name:'Data Guide',exact:true});
    await trigger.click();
    const guide=page.getByRole('dialog',{name:'Data Guide',exact:true});
    await guide.waitFor();
    assert.deepEqual(await layoutProblems(page),[],'source setup guide layout');
    await app.shot('source-setup-data-guide');
    await page.keyboard.press('Escape');
    assert.ok(await trigger.evaluate(element=>element===document.activeElement),'nested source guide lost focus');
  }},
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
  // One empty state per page covers all three states across the three pages.
  ...[["Search", "/library", "unconfigured", false, false], ["Analyze", "/analyze", "idle", true, false], ["Optimize", "/findings", "syncing", true, true]].map(([name, url, state, sources, busy]) => ({
    name: `${name} empty library ${state}`, url, ready: ".view.active .library-empty-card",
    api: { "GET /api/sync/status": { ...defaultApi["GET /api/sync/status"], library_empty: true, has_sources: sources, busy, sources: sources ? [{ name: "codex" }] : [], available_sources: [] } },
  })),
  // The theme changes colours, not metrics: Ask Pharos runs dark, and its
  // ready state once in light so the light theme still renders.
  {name:"Ask Pharos ready (light)",url:"/",ready:"#pharos-ask .pharos-ai:not([hidden])",api:askAPI,preferences:{"pharos-theme":"light"}},
  ...[
  {name:"Ask Pharos removed CLI",url:"/",ready:"#pharos-ask .home-search-trigger",api:{"GET /api/ask/status":{settings:{enabled:true,showInvitation:true,selection:askSelection},providers:[]}}},
  {name:"Ask Pharos opted out",url:"/",ready:"#pharos-ask .home-search-trigger",api:{"GET /api/ask/status":{settings:{enabled:false,showInvitation:false},providers:askProviders}}},
  {name:"Ask Pharos ready",url:"/",ready:"#pharos-ask .pharos-ai:not([hidden])",api:askAPI},
  {name:"Ask Pharos full screen",url:"/ask",ready:"#askPage #pharos-ask .pharos-ai:not([hidden])",api:askAPI},
  {name:"Ask Pharos model picker",url:"/",ready:"#pharos-ask .pharos-ai:not([hidden])",api:askAPI,open:page=>page.locator("#pharos-ask .pharos-model-trigger").click(),shown:"#pharos-model-menu:not([hidden])"},
  {name:"Ask Pharos history",url:"/",ready:"#pharos-ask .pharos-ai:not([hidden])",api:askAPI,open:page=>page.locator("#pharos-ask .pharos-history-trigger").click(),shown:"#pharos-chat-history[open]"},
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
  {name:"Ask Pharos feedback draft",url:"/?ask_chat=saved-chat",ready:"#pharos-ask .pharos-feedback:not([hidden])",api:askFeedbackAPI},
  // In the Mac app, where it can transcribe, the composer and the feedback text box offer a microphone.
  {name:"Ask Pharos dictation",url:"/?ask_chat=saved-chat",ready:"#pharos-ask .pharos-feedback .dictation-button:not([hidden])",api:askFeedbackAPI,
    nativeHost:"window.webkit={messageHandlers:{pharosDictation:{postMessage:async body=>body.action==='status'?{available:true}:true}}};"},
  {name:"Ask Pharos clarification",url:"/?ask_chat=saved-chat",ready:"#pharos-ask .pharos-clarification:not([hidden])",api:{...askAPI,"GET /api/ask/session":askClarificationSession}},
  {name:"Ask Pharos opt-in",url:"/",ready:"#pharos-ask .pharos-invitation:not([hidden])",api:{"GET /api/ask/status":{settings:{enabled:false,showInvitation:true},providers:askProviders}},open:page=>page.locator("#pharos-ask .pharos-try").click(),shown:"#pharos-ask .pharos-consent[open]"},
  {name:"Ask Pharos settings",url:"/settings/ask",ready:"#askSettingsPage .ask-settings select",api:askAPI},

  ].map(route=>({...route,preferences:{"pharos-theme":"dark"}})),
  { name: "Messages dataset query", url: "/analyze?analyze_mode=query&analyze_data=messages", ready: ".usage-workspace-table .qt-row" },
  { name: "TL1 attempts query", url: "/analyze?analyze_mode=query&analyze_data=tl1_attempts", ready: ".usage-workspace-table .qt-row" },
  { name: "Data guide", url: "/analyze?analyze_mode=query&analyze_data=tool_calls&analyze_topic=tools", ready: ".usage-workspace-table .qt-row", open: page => page.locator(".usage-workspace-table .data-source-link").click(), shown: ".data-guide[open]",
    check: async (page, app) => {
      const guide = page.getByRole('dialog', { name: 'Data Guide', exact: true });
      const picker = guide.getByRole('combobox', { name: 'Guide term', exact: true });
      for (const [term, label] of [['conversations','Conversations'],['agents','Agent sessions'],['turns','Turns'],['messages','Messages'],['tool_calls','Tool calls'],['skills','Skills and instructions'],['compactions','Context compactions'],['tokens','Tokens and API cost'],['repositories','Repositories'],['workspaces','Workspaces'],['work_items','Work items'],['agent_apps','Agent apps'],['providers','Providers'],['models','Models and effort'],['channels','Channels'],['machines','Machines'],['libraries','Libraries'],['sources','Sources']]) {
        if (await picker.isVisible()) await picker.selectOption(term);
        else await guide.getByRole('navigation', { name: 'Data Guide terms' }).getByRole('button', { name: label, exact: true }).click();
        await page.waitForFunction(value => new URLSearchParams(location.search).get('guide_entity') === value, term);
        assert.deepEqual(await layoutProblems(page), [], term + ' illustration layout');
        await app.shot('illustration-' + term);
      }
    },
  },
  ...[["usage", false], ["tool_calls", true]].map(([dataset, explore]) => ({
    name: `Query ideas ${explore ? "Explore " : ""}${dataset}`,
    url: `/analyze?analyze_mode=query&analyze_data=${dataset}`,
    ready: ".usage-workspace-table .qt-row",
    open: async page => {
      const chip = explore ? page.getByRole("button", { name: /^Command families/ }) : page.locator(".usage-query-ideas .usage-idea").first();
      await chip.click();
      await page.getByRole("link", { name: "Matching records", exact: true }).first().waitFor();
    },
    shown: ".usage-workspace-table .qt-row",
  })),
  // The unified activity panel, idle and busy, in the browser and in the app
  // (other libraries).
  ...[['idle', idleStatus, false], ['busy', busyStatus, false], ['busy in the app', busyStatus, true],
    ['partial rates', {...busyStatus, resources: {...busyStatus.resources, cpu_partial: true, disk_partial: true, gpu: {...busyStatus.resources.gpu, partial: true}}}, false],
  ].map(([state, snapshot, managed]) => ({
    name: `Unified activity panel ${state}`, url: '/library?view=table', ready: '#queryTableLibrary .qt-row',
    managedLibrary: managed ? {id: 'local', name: 'Pending history', config: '/Users/test/Libraries/Pending/library.toml'} : null,
    api: managed ? {'GET /api/app/status/stream': eventStream(managedStatus(snapshot)), 'GET /api/app/status': managedStatus(snapshot)}
      : {'GET /api/status/stream': eventStream(snapshot), 'GET /api/status': snapshot},
    open: async page => {
      await page.evaluate(() => { window.webkit = {messageHandlers: {pharosLibrary: {postMessage: async () => {}}}}; });
      await activateHeaderTool(page, 'pharosDrive');
      // Every cadence row open, its items listed.
      const closed = page.locator('#pharosStatusPanel .pharos-status-cadences details:not([open]) summary');
      while (await closed.count()) await closed.first().click();
      const oneLine = await page.locator('.pharos-status-load').evaluate(row => {
        const cells = [...row.children].map(cell => cell.getBoundingClientRect());
        return cells.every(cell => Math.abs(cell.top - cells[0].top) < 1) && row.scrollWidth <= row.clientWidth;
      });
      assert.ok(oneLine, 'the resource metrics fit on one line');
      if (state === 'partial rates') {
        for (const id of ['cpu', 'gpu', 'io']) {
          const cell = page.locator(`#pharosStatusPanel [data-key="load:${id}"]`);
          assert.match(await cell.innerText(), /≥/, 'the undercount qualifier is visible');
          assert.match(await cell.getAttribute('title'), /may undercount/, 'hover explains partial measurements');
        }
      }
    }, shown: '#pharosStatusPanel .pharos-status-items li',
  })),
  { name: "Unified activity panel background task", url: '/library?view=table', ready: '#queryTableLibrary .qt-row',
    api: {'GET /api/status/stream': eventStream(backgroundTaskStatus), 'GET /api/status': backgroundTaskStatus},
    open: page => activateHeaderTool(page, 'pharosDrive'), shown: '#pharosStatusPanel .pharos-status-position-track',
    check: async page => {
      const measured = page.getByRole('progressbar', {name: 'Classify your writing progress', exact: true});
      const unmeasured = page.getByRole('progressbar', {name: 'Index message labels progress', exact: true});
      await measured.waitFor();
      await unmeasured.waitFor();
      assert.notEqual(await measured.getAttribute('aria-valuenow'), null, 'measured progress is accessible');
      assert.equal(await unmeasured.getAttribute('aria-valuenow'), null, 'unknown work is accessible without a fabricated percentage');
    },
  },
  { name: "Home", url: "/", ready: ".home-recent-grid .home-recent-card" },
  { name: "Home without recent conversations", url: "/", ready: ".home-recent-empty", api: { "GET /api/library/recent-conversations": { items: [] } } },
  { name: "Home search composer", url: "/", ready: ".home-search-trigger", open: async page => { await page.getByRole("button", { name: "Search past work", exact: true }).focus(); await page.keyboard.press("Enter"); }, shown: ".home-search-disclosure.is-expanded",
    check: page => page.waitForFunction(() => document.activeElement?.matches(".home-search-content textarea, .home-search-content input")) },
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
  { name: "Search table", url: "/library?view=table", ready: "#queryTableLibrary .qt-row" },
  { name: "Markdown links in transcript", url: `${work}/conversation/${rootConversation}`, api: markdownLinkAPI, ready: ".conversation-panel .markdown-link" },
  { name: "Markdown links in Search", url: "/library", api: {...conversationResultAPI,...markdownLinkAPI}, ready: ".conversation-result-card", open: async page => {await page.getByRole("button",{name:/^Browse turns:/}).first().click();await page.getByRole("link",{name:"Running dev server",exact:true}).waitFor();}, shown: ".result-turn-content .markdown-link" },
  { name: "Search nested reviews and human writing", url: "/library?reader_writing=0&reader_events=1&reader_labels=1", api: authoredTurnAPI, ready: ".conversation-result-card", open: async page => {await page.getByRole("button",{name:/^Browse turns:/}).first().click();await page.getByRole("button",{name:/Sub-agent · Reviewer 5/}).waitFor();}, shown: ".result-turn-content .human-written",
    check: async page => {
      const painted = await page.locator(".result-turn-content .human-written").first().evaluate(element => getComputedStyle(element).backgroundColor);
      assert.notEqual(painted, "rgba(0, 0, 0, 0)", "human writing remains highlighted when the reader preference is off");
    } },
  { name: "Transcript human writing", url: `${work}/conversation/${rootConversation}`, api: authoredTurnAPI, ready: ".conversation-panel .human-written" },
  { name: "Transcript expanded sub-agent", url: `${work}/conversation/${rootConversation}`, api: authoredTurnAPI, ready: ".subagent-run", open: async page => { await page.locator(".subagent-run > summary").first().click(); }, shown: ".subagent-body" },
  { name: "Transcript agent event link", url: `${work}/conversation/${rootConversation}?reader_events=1&reader_event_link=e-review`, api: authoredTurnAPI, ready: ".conversation-panel.show-events mark.event-target.link-selected" },
  { name: "Search conversations", url: "/library", ready: ".conversation-result-card" },
  ...["turn", "sub-agent", "accordion", "summary", "preview"].map(selection => ({
    name: `Search conversation outline ${selection}`, url: "/library", api: conversationResultAPI, ready: ".conversation-result-card",
    open: async page => {
      if (selection === "preview") {
        // Clicking a preview message opens Browse at its turn and reveals it.
        await page.locator(".conversation-result-preview .result-message.assistant").click({ position: { x: 40, y: 30 } });
        const current = page.getByRole("navigation", { name: "Conversation outline", exact: true }).locator('[aria-current="true"]');
        await current.waitFor();
        assert.match(await current.innerText(), /^Turn 12/);
        const highlighted = page.locator(".result-turn-content .result-message.assistant.pharos-highlight");
        await highlighted.waitFor();
        const clipped = await highlighted.evaluate(element => {
          const target = element.getBoundingClientRect();
          for (let ancestor = element.parentElement; ancestor && !ancestor.matches("main"); ancestor = ancestor.parentElement) {
            const style = getComputedStyle(ancestor), rect = ancestor.getBoundingClientRect();
            if (style.overflowX !== "visible" && (target.left - 8 < rect.left || target.right + 8 > rect.right)) return true;
            if (style.overflowY !== "visible" && (target.top - 8 < rect.top || target.bottom + 8 > rect.bottom)) return true;
          }
          return false;
        });
        assert.equal(clipped, false, "the selected message's highlight has room outside its edges");
        return;
      }
      await page.getByRole("button", selection === "summary" ? { name: /^Browse turns: / } : { name: "Browse", exact: true }).click();
      if (selection === "summary") assert.ok(await page.getByRole("button", { name: "Collapse turns", exact: true }).evaluate(button => button === document.activeElement), "opening from the compact summary keeps keyboard focus on the collapse control");
      const outline = page.getByRole("navigation", { name: "Conversation outline", exact: true });
      await outline.getByRole("button", { name: /^Turn 1/ }).first().waitFor();
      if (selection === "sub-agent") {
        await outline.locator("summary").filter({ hasText: "Sub-agent conversation · codex" }).click();
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
  { name: "Search compact conversation previews", url: "/library", api: conversationResultAPI, ready: ".conversation-result-preview" },
  { name: "Search workspace fingerprints", url: "/library?view=table&q_library="+encodeQuery({...EMPTY_QUERY,select:[{field:'title',width:300},{field:'fingerprint',width:180}],limit:5}), ready: '.message-label-fingerprint img' },
  { name: "Analyze conversation fingerprints", url: "/analyze?analyze_mode=query&analyze_data=usage&q_usage="+encodeQuery({...EMPTY_QUERY,select:[{field:'fingerprint',width:180}],limit:5}), ready: '.message-label-fingerprint img' },
  { name: "Search display switch", url: "/library?view=table", ready: "#queryTableLibrary .qt-row", open: page => page.getByRole("group", { name: "Display results as" }).getByRole("button", { name: "Conversations", exact: true }).click(), shown: ".conversation-result-card" },
  { name: "Search content editor", url: "/library?view=table", ready: "#queryTableLibrary .qt-row", open: page => page.getByRole("button", { name: "Messages", exact: true }).click(), shown: ".explore-search-modal" },
  { name: "Search grouped search", url: exploreGroupedSearch, ready: "#queryTableLibrary .qt-row" },
  { name: "Search grouped conversation display", url: exploreGroupedSearch, ready: "#queryTableLibrary .qt-row", open: page => page.getByRole("group", { name: "Display results as" }).getByRole("button", { name: "Conversations", exact: true }).click(), shown: ".conversation-result-card" },
  // Older Analyze and Tools links open what replaced their pages.
  { name: "Analyze legacy link", url: '/analyze?analyze_view=carbon&analyze_chart={"metric":"energy"}', ready: ".usage-dashboard #carbonSummary" },
  { name: "Tools legacy link", url: "/tools?tools_view=summary", ready: ".usage-workspace-table .qt-row" },
  { name: "Writing estimates awaiting refinement", url: "/analyze?analyze_topic=human", ready: '[aria-label="Human Words summary"][aria-busy="false"]' },
  { name: "Conversations dashboard", url: "/analyze?analyze_topic=activity", ready: '.dashboard-stats[aria-label="Conversations summary"][aria-busy="false"]' },
  {name:'Message Labels dashboard',url:'/analyze?analyze_mode=dashboards&analyze_topic=labels&analyze_data=message_labels',ready:'.usage-labels-dashboard .usage-chart'},
  {name:'Message Labels specific hierarchy',url:'/analyze?'+new URLSearchParams({analyze_topic:'labels',analyze_chart:JSON.stringify({view:'trend',metric:'messages',split:'label'})}),ready:'.usage-labels-dashboard .usage-chart'},
  {name:'Message Labels bars',url:'/analyze?'+new URLSearchParams({analyze_topic:'labels',analyze_chart:JSON.stringify({view:'trend_bar',metric:'messages',split:'family'})}),ready:'.usage-labels-dashboard .usage-chart'},
  {name:'Message Labels comparison',url:'/analyze?'+new URLSearchParams({analyze_mode:'dashboards',analyze_topic:'labels',analyze_data:'message_labels',analyze_chart:JSON.stringify({view:'compare',metric:'messages',split:'family'})}),ready:'.usage-labels-dashboard'},
  {name:'Message label conversation cards',url:'/analyze?'+new URLSearchParams({analyze_mode:'query',analyze_data:'message_labels',analyze_topic:'labels',label_results:'cards',q_message_labels:encodeQuery({...EMPTY_QUERY,limit:40,grouping:{source:EMPTY_QUERY,keys:['fingerprint'],summaries:[{id:'conversation_title',op:'row',field:'title'},{id:'conversation_workspace',op:'row',field:'workspace_id'},{id:'conversation_repository',op:'row',field:'repository_name'},{id:'matching_messages',op:'count_distinct',field:'message_id'},{id:'last_match',op:'max',field:'occurred_at'},{id:'source_message',op:'row',field:'message_id'}],representative:{orderBy:[{field:'occurred_at',dir:'desc'}]}}})}),ready:'.message-label-result-card'},
  {name:'Message label conversation cards with events pending',url:'/analyze?'+new URLSearchParams({analyze_mode:'query',analyze_data:'message_labels',analyze_topic:'labels',label_results:'cards',q_message_labels:encodeQuery({...EMPTY_QUERY,limit:40,grouping:{source:EMPTY_QUERY,keys:['fingerprint'],summaries:[{id:'conversation_title',op:'row',field:'title'},{id:'conversation_workspace',op:'row',field:'workspace_id'},{id:'conversation_repository',op:'row',field:'repository_name'},{id:'matching_messages',op:'count_distinct',field:'message_id'},{id:'last_match',op:'max',field:'occurred_at'},{id:'source_message',op:'row',field:'message_id'}],representative:{orderBy:[{field:'occurred_at',dir:'desc'}]}}})}),api:pendingEvidenceApi,ready:'.message-label-result-card .fingerprint-sampled'},
  {name:'Message label conversation rows',url:'/analyze?analyze_mode=query&analyze_data=message_labels&analyze_topic=labels&label_results=table',ready:'.qt-row'},
  { name: "Analyze workspace dashboards", url: "/analyze", ready: ".usage-dashboard .usage-chart" },
  { name: "Human Words component shares", url: `/analyze?analyze_topic=human&analyze_chart=${encodeURIComponent(JSON.stringify({ view: "compare", metric: "words", split: "category", compareAs: "share" }))}`, ready: '.usage-analysis-dashboard[aria-busy="false"] .usage-query-destinations' },
  // One route per chart kind and dashboard component; the numbers each
  // topic draws are covered by the dashboard parity and analysis unit tests.
  {
    name: "Dashboard pinned key", url: "/analyze?analyze_topic=tokens", ready: '.usage-chart-bars:not(.loading) > button',
    open: async page => {
      const chart = page.locator('.usage-dashboard .usage-chart').first();
      await chart.locator('.usage-chart-bars > button').filter({ has: page.locator('i') }).first().focus();
      await page.keyboard.press('Enter');
      await chart.getByRole('group', { name: /^Details for / }).getByRole('link').first().waitFor();
    }, shown: '.usage-chart-tip.pinned',
  },
  { name: "Analyze relative query previews", url: `/analyze?${new URLSearchParams({ analyze_topic: "tokens", analyze_filters: encodeQuery({ ...EMPTY_QUERY, where: [{ field: "first_usage_at", op: ">=", value: "-30d" }, { field: "first_usage_at", op: "<", value: "+0s" }, { field: "provider", op: "=", value: "codex" }] }) })}`, ready: ".usage-query-destinations" },
  { name: "Analyze workspace cumulative", url: `/analyze?analyze_topic=tokens&analyze_chart=${encodeURIComponent(JSON.stringify({ display: "cumulative", period: "days", days: 3, split: "model_family", metric: "percent_cost" }))}`, ready: ".usage-dashboard .usage-chart-lines" },
  ...Object.entries({
    compare: { view: "compare", metric: "cost", split: "provider_model", compareAs: "share" },
    distribution: { view: "distribution", unit: "conversation", metric: "cost", split: "model_family", minSamples: 2 },
    cycle: { view: "cycle", cycle: "hour", metric: "tokens", split: "none" },
  }).map(([view, chart]) => ({ name: `Tokens and Cost ${view} view`, url: `/analyze?analyze_topic=tokens&analyze_chart=${encodeURIComponent(JSON.stringify(chart))}`, ready: '.usage-analysis-dashboard[aria-busy="false"] .usage-query-destinations',
    check: async page => assert.equal(await page.locator(".usage-analysis-dashboard [role=alert]").count(), 0, "the analysis reports an error") })),
  { name: "Tokens and Cost trend ratio", url: `/analyze?analyze_topic=tokens&analyze_chart=${encodeURIComponent(JSON.stringify({ period: "day", metric: "cache_read_share", split: "provider_model", rollingPeriods: 7 }))}&disclosures=${encodeURIComponent(JSON.stringify({ "dashboard:view": true }))}`,
    api: { 'GET /api/pricing': missingPrices },
    ready: '.usage-dashboard section[aria-label="Over time"][aria-busy="false"] .usage-query-destinations',
    check: async page => assert.equal(await page.locator('section[aria-label="Over time"] .query-table-error').count(), 0, "the trend reports an error") },
  { name: "Analyze workspace custom intervals", url: `/analyze?analyze_topic=tokens&analyze_chart=${encodeURIComponent(JSON.stringify({ period: "hours", hours: 6, rollingPeriods: 37 }))}`, ready: ".usage-dashboard .usage-period-custom input" },
  { name: "Analyze workspace tools dashboard", url: "/analyze?analyze_workspace=1&analyze_topic=tools", ready: `.usage-tools-dashboard ${analysisReady}` },
  // Seven weeks keeps the history request within the stub's equality-filter
  // capability; wider day regex filters are exercised against the Go service.
  { name: "Analyze workspace tools trends", url: `/analyze?analyze_workspace=1&analyze_topic=tools&analyze_chart=${encodeURIComponent(JSON.stringify({ metric: "failure_rate", split: "tool_category", view: "trend", period: "week", rollingPeriods: 7 }))}`, ready: `.usage-tools-dashboard ${overTimeReady}` },
  // A record dashboard's error reporting, on the one chart kind (weekday
  // cycle) not drawn above.
  { name: "MCP dashboard weekday cycle", url: `/analyze?analyze_topic=mcp&analyze_chart=${encodeURIComponent(JSON.stringify({ view: "cycle", cycle: "weekday", metric: "time", split: "none", reduce: "mean" }))}`, ready: `.usage-mcp-dashboard ${analysisReady}`,
    check: async page => assert.deepEqual(await page.locator(".usage-mcp-dashboard [role=alert], .usage-mcp-dashboard .query-table-error").allTextContents(), [], "the dashboard reports an error") },
  { name: "Analyze Work Hours dashboard", url: "/analyze?analyze_topic=work_hours", ready: ".usage-work-hours-dashboard .usage-chart" },
  { name: "Analyze habits punchcard", url: "/analyze?analyze_topic=habits&habits_view=punchcard", ready: '[data-pharos-target="section:habits-punchcard"]' },
  { name: "Analyze habits daily schedule", url: "/analyze?analyze_topic=habits&habits_view=days", ready: ".habits-days-chart" },
  { name: "Analyze habits dashboard", url: "/analyze?analyze_topic=habits&habits_view=calendar&habits_day=" + new Date(Date.now() - 86400000).toISOString().slice(0, 10), ready: ".habits-panel .habits-day" },
  { name: "Analyze workspace parallelism dashboard", url: "/analyze?analyze_workspace=1&analyze_topic=parallelism", ready: ".usage-dashboard .concurrency-chart svg" },
  { name: "Analyze workspace carbon dashboard", url: "/analyze?analyze_workspace=1&analyze_topic=carbon", ready: ".usage-dashboard #carbonSummary" },
  { name: "Analyze workspace agent experience dashboard", url: "/analyze?analyze_workspace=1&analyze_topic=agent_experience", ready: `.usage-agent-experience-dashboard ${overTimeReady}` },
  { name: "Agent experience dashboard with events pending", url: "/analyze?analyze_workspace=1&analyze_topic=agent_experience", api: pendingEvidenceApi, ready: ".usage-agent-experience-dashboard .dashboard-stats[aria-busy=false]", check: page => page.locator(".usage-agent-experience-dashboard .usage-dashboard-note[role=status]").first().waitFor() },
  { name: "Agent events query with events pending", url: "/analyze?analyze_mode=query&analyze_data=agent_events", api: pendingEvidenceApi, ready: ".usage-workspace-table .usage-dashboard-note[role=status]" },
  // Agent Events opens on Compare; its trend is the stacked kind chart.
  { name: "Agent Events dashboard", url: "/analyze?analyze_topic=agent_event_rates", ready: `.usage-agent-events-dashboard ${analysisReady}`,
    check: async page => assert.deepEqual(await page.locator(".usage-agent-events-dashboard [role=alert], .usage-agent-events-dashboard .query-table-error").allTextContents(), [], "the dashboard reports an error") },
  { name: "Agent Events trend by kind", url: `/analyze?analyze_topic=agent_event_rates&analyze_chart=${encodeURIComponent(JSON.stringify({ view: "trend", metric: "per_thousand", split: "family", period: "day" }))}`, ready: `.usage-agent-events-dashboard ${overTimeReady}` },
  // With a partial population, so its chips share the control row at 320 px.
  { name: "Analyze workspace session shape dashboard", url: `/analyze?${new URLSearchParams({ analyze_workspace: "1", analyze_topic: "session_shape",
    analyze_filters: encodeQuery({ ...EMPTY_QUERY, where: [{ field: "started_at", op: ">=", value: "-30d" }, { field: "started_at", op: "<", value: "+0s" }, { any: [{ field: "harness", op: "!=", value: "antigravity-cli" }, { field: "harness", op: "is_null", value: "" }] }] }) })}`,
    ready: `.usage-session-shape-dashboard ${overTimeReady}`,
    check: async page => assert.equal(await page.locator(".usage-session-shape-dashboard .dashboard-population-chip").count(), 1, "the population is not shown beside the controls") },
  { name: "Session shape dashboard with statistics pending", url: "/analyze?analyze_topic=session_shape", api: {
    "* /api/query/:dataset/:operation": async call => {
      const answer = await defaultApi["* /api/query/:dataset/:operation"](call);
      return call.params.dataset === "session_shape" && call.params.operation === "query-v3"
        ? { ...answer, coverage: { state: "pending", pending_sessions: 5, stale_sessions: 2, missing_sessions: 3 } } : answer;
    },
  }, ready: `.usage-session-shape-dashboard ${overTimeReady}`,
    check: page => page.locator(".usage-session-shape-dashboard .usage-dashboard-note[role=status]").waitFor() },
  { name: "Session shape query with statistics pending", url: "/analyze?analyze_mode=query&analyze_data=session_shape", api: {
    "* /api/query/:dataset/:operation": async call => {
      const answer = await defaultApi["* /api/query/:dataset/:operation"](call);
      return call.params.dataset === "session_shape" && call.params.operation === "query-v3"
        ? { ...answer, coverage: { state: "pending", pending_sessions: 5, stale_sessions: 2, missing_sessions: 3 } } : answer;
    },
  }, ready: ".usage-workspace-table .usage-dashboard-note[role=status]" },
  { name: "Analyze binned distribution", url: `/analyze?analyze_topic=session_shape&analyze_chart=${encodeURIComponent(JSON.stringify({ v: 2, view: "distribution", metric: "active_time", split: "none" }))}`, ready: '.usage-analysis-dashboard[aria-busy="false"]' },
  { name: "Analyze trend heatmap", url: `/analyze?analyze_topic=session_shape&analyze_chart=${encodeURIComponent(JSON.stringify({ v: 2, view: "trend", metric: "sessions", split: "autonomy_band", display: "heatmap", period: "day" }))}`, ready: '.usage-session-shape-dashboard section[aria-label="Over time"][aria-busy="false"] .dashboard-heatmap' },
  { name: "Human Words conversation heatmap", url: `/analyze?analyze_topic=human&analyze_days=30&analyze_chart=${encodeURIComponent(JSON.stringify({ v: 2, view: "trend", metric: "typed", split: "none", display: "heatmap", period: "day" }))}`, ready: 'section[aria-label="Over time"][aria-busy="false"] .dashboard-heatmap' },
  // Spend by branch: the coverage split above the compared branches.
  { name: "Analyze spend by branch dashboard", url: "/analyze?analyze_topic=branches", ready: `.usage-branches-dashboard ${analysisReady}`,
    check: async page => {
      await page.locator(".usage-branches-dashboard").getByRole("combobox", { name: "Breakdown", exact: true }).selectOption("channel");
      await page.locator(`.usage-branches-dashboard ${analysisReady}`).waitFor();
      assert.deepEqual(await page.locator(".usage-branches-dashboard [role=alert], .usage-branches-dashboard .query-table-error").allTextContents(), [], "the Channel breakdown reports an error");
    } },
  // Outcomes: the chart and its panels, each read from the outcomes dataset.
  { name: "Analyze outcomes dashboard", url: "/analyze?analyze_topic=outcomes", ready: `.usage-outcomes-dashboard ${overTimeReady}`,
    check: async page => {
      await page.locator(".usage-outcomes-dashboard .outcome-panel[aria-busy=false]").nth(2).waitFor();
      assert.deepEqual(await page.locator(".usage-outcomes-dashboard [role=alert], .usage-outcomes-dashboard .query-table-error").allTextContents(), [], "the dashboard reports an error");
    } },
  // A card's relative range, replaced by custom dates and times in the
  // dashboard's inline range picker.
  { name: "Dashboard custom date and time range", url: `/analyze?${new URLSearchParams({ analyze_mode: "dashboards", analyze_topic: "tokens",
    q_usage: encodeQuery({ ...EMPTY_QUERY, where: [{ field: "first_usage_at", op: ">=", value: "-7d" }, { field: "first_usage_at", op: "<", value: "+0s" }] }), window_mode: "relative", window_days: "7", window_field: "first_usage_at", window_dataset: "usage",
    analyze_filters: encodeQuery({ ...EMPTY_QUERY, where: [{ field: "first_usage_at", op: ">=", value: "-7d" }, { field: "first_usage_at", op: "<", value: "+0s" }] }), analyze_chart: JSON.stringify({ period: "day", metric: "cost", split: "repository_name" }) })}`,
    ready: ".usage-dashboard .usage-chart", open: async page => {
      await page.getByRole("combobox", { name: "Time Bucket", exact: true }).selectOption("hour");
      await page.getByRole("combobox", { name: "Range", exact: true }).selectOption("custom-times");
      await page.getByLabel("From date and time", { exact: true }).waitFor();
    }, shown: ".usage-dashboard .usage-chart" },
  { name: "Parallelism custom date range", concurrency: true, url: `/analyze?${new URLSearchParams({ analyze_mode: "dashboards", analyze_topic: "parallelism",
    q_parallelism: encodeQuery({ ...EMPTY_QUERY, where: [{ field: "started_at", op: ">=", value: "-7d" }, { field: "started_at", op: "<", value: "+0s" }] }), window_mode: "relative", window_days: "7", window_field: "started_at", window_dataset: "parallelism",
    analyze_parallelism: JSON.stringify({ range: "7d" }) })}`,
    ready: ".usage-dashboard .concurrency-chart svg", open: async page => {
      // The ribbon's Range; the engine views below have their own.
      await page.getByRole("region", { name: "Parallelism chart", exact: true }).getByRole("combobox", { name: "Range", exact: true }).selectOption("custom");
      await page.getByLabel("From date", { exact: true }).waitFor();
    }, shown: ".usage-dashboard .concurrency-chart svg" },
  // Fitted tables with wrapping host links, and one without.
  ...["usage", "writing_messages", "agent_events", "mcp_calls"].map(dataset => ({
    name: `Analyze workspace query ${dataset}`, url: `/analyze?analyze_mode=query&analyze_data=${dataset}`, ready: ".usage-workspace-table .qt-row", fitTable: true,
  })),
  { name: "Analyze record keyword editor", url: "/analyze?analyze_mode=query&analyze_data=tool_calls", ready: ".usage-workspace-table .qt-qb", open: page => page.getByRole("button", { name: /^Add keyword filter for / }).click(), shown: ".explore-search-modal" },
  { name: "Analyze record keyword condition", url: `/analyze?${new URLSearchParams({ analyze_mode:"query", analyze_data:"tool_calls", record_tool_calls:JSON.stringify(recordKeyword) })}`, ready: ".usage-workspace-table .qt-qb" },
  { name: "Analyze workspace legacy search link", url: "/analyze?analyze_workspace=1&analyze_mode=search&analyze_data=tool_calls", ready: ".usage-workspace-table .qt-row" },
  ...[["filtered", analyzeFilteredQuery], ["grouped", analyzeGroupedQuery]].map(([label, query]) => ({
    name: `Analyze workspace ${label} query`, url: `/analyze?${new URLSearchParams({ analyze_mode: "query", analyze_data: "tool_calls", q_tool_calls: encodeQuery(query) })}`, ready: ".usage-workspace-table .qt-qb",
    open: page => page.getByRole("button", { name: "Run Now", exact: true }).click(), shown: ".usage-workspace-table .qt-row",
  })),
  { name: "Analyze query recovery", url: "/analyze?analyze_mode=query&analyze_data=usage&q_usage=invalid", ready: ".query-recovery", open: page => page.getByRole("button", { name: "Reset table", exact: true }).click(), shown: ".usage-workspace-table .qt-row" },
  { name: "Findings", url: "/findings", ready: ".findings-card" },
  { name: "Safety notices", url: "/findings?optimize_area=safety", ready: ".safety-check-card", open: page => page.locator(".safety-check-card input[type=checkbox]").first().check(), shown: ".safety-check-cards .safety-selection-toolbar" },
  { name: "Safety table", url: "/findings?optimize_area=safety&view=table", ready: ".safety-table .findings-cart li", open: page => page.locator(".safety-table .qt-row input[type=checkbox]").first().check(), shown: ".safety-selection-toolbar" },
  { name: "Safety check table", url: `/findings?${new URLSearchParams({ optimize_area: "safety", view: "table", q_safety: encodeQuery({ ...EMPTY_QUERY, where: [{ field: "rule", op: "=", value: "credentials" }, { field: "state", op: "=", value: "open" }] }) })}`, ready: ".optimize-table-questions", open: page => page.locator(".optimize-table-questions summary").click(), shown: ".optimize-table-questions[open]" },
  { name: "Safety grouped check table", url: `/findings?${new URLSearchParams({ optimize_area: "safety", view: "table", q_safety: encodeQuery({ ...EMPTY_QUERY, grouping: { source: { ...EMPTY_QUERY, where: [{ field: "rule", op: "=", value: "credentials" }, { field: "danger", op: "=", value: "high" }] }, keys: ["target_label"], summaries: [{ id: "notice_count", op: "count", label: "Notices" }], representative: null } }) })}`, ready: ".optimize-table-questions" },
  { name: "Safety settings", url: "/findings?optimize_area=safety&view=settings", ready: ".safety-check" },
  { name: "Safety evidence", url: "/findings?optimize_area=safety&safety_case=safety_sample", ready: ".safety-detail", open: async page => { await page.locator('.safety-evidence summary').click(); }, shown: '.safety-evidence[open] pre' },
  { name: "Safety prompt", url: "/findings?optimize_area=safety", ready: ".safety-check-card", open: async page => { await page.getByRole('button', { name: 'Review prompt (1 notice)' }).click(); }, shown: '.findings-dialog' },
  { name: "Fixes", url: "/findings?optimize_area=fixes&fixes_hidden=1&disclosures=%7B%22fixes%3Amaybe%22%3Atrue%7D", ready: ".fixes-card", open: page => page.locator(".fixes-card input[type=checkbox]").first().check(), shown: ".fixes-cards .safety-selection-toolbar" },
  { name: "Fixes prompt", url: "/findings?optimize_area=fixes", ready: ".fixes-card", open: async page => { await page.getByRole("button", { name: "Review prompt (1 fix)" }).click(); }, shown: ".findings-dialog input[name=fixesMode]" },
  { name: "Fix detail", url: "/findings?optimize_area=fixes&fixes_item=fix_sample", ready: ".fixes-detail .fixes-quotes", open: async page => { await page.locator(".fixes-members summary").click(); }, shown: ".fixes-members[open] .fixes-quotes" },
  { name: "Fixes settings", url: "/findings?optimize_area=fixes&view=settings", ready: ".fixes-bounds select" },
  { name: "Fixes with events pending", url: "/findings?optimize_area=fixes", api: pendingEvidenceApi, ready: ".fixes-card", check: page => page.locator("[role=status].findings-muted").first().waitFor() },
  { name: "Safety with events pending", url: "/findings?optimize_area=safety", api: pendingEvidenceApi, ready: ".safety-check-card", check: page => page.locator("[role=status].findings-muted").first().waitFor() },
  { name: "Fixes table", url: "/findings?optimize_area=fixes&view=table", ready: ".safety-table .findings-cart li", open: page => page.locator(".safety-table .qt-row input[type=checkbox]").first().check(), shown: ".safety-selection-toolbar" },
  { name: "Fixes hidden table", url: `/findings?${new URLSearchParams({ optimize_area: "fixes", view: "table", q_fixes: encodeQuery({ ...EMPTY_QUERY, where: [{ field: "state", op: "=", value: "open" }, { field: "shown", op: "!=", value: "shown" }] }) })}`, ready: '[data-pharos-target="section:fixes-hidden"]' },
  { name: "Fixes grouped table", url: `/findings?${new URLSearchParams({ optimize_area: "fixes", view: "table", q_fixes: encodeQuery({ ...EMPTY_QUERY, grouping: { source: { ...EMPTY_QUERY, where: [{ field: "shown", op: "=", value: "stale" }, { field: "category", op: "=", value: "env" }] }, keys: ["target_label"], summaries: [{ id: "fix_count", op: "count", label: "Fixes" }], representative: null } }) })}`, ready: ".optimize-table-title" },
  { name: "Fixes grouped hidden table", url: `/findings?${new URLSearchParams({ optimize_area: "fixes", view: "table", q_fixes: encodeQuery({ ...EMPTY_QUERY, grouping: { source: { ...EMPTY_QUERY, where: [{ field: "state", op: "=", value: "open" }, { field: "shown", op: "!=", value: "shown" }, { field: "category", op: "=", value: "env" }] }, keys: ["target_label"], summaries: [{ id: "fix_count", op: "count", label: "Fixes" }], representative: null } }) })}`, ready: '[data-pharos-target="section:fixes-hidden"]' },
  { name: "Findings table", url: "/findings?view=table", ready: ".findings-table .qt-row" },
  { name: "Findings settings", url: "/findings?view=settings", ready: ".findings-settings .findings-panel" },
  { name: "Finding detail", url: "/findings?finding=drift%3Arepo%3Arepo_tl1%3Aghi", ready: ".findings-detail svg" },
  { name: "MCP dashboard", url: "/analyze?analyze_topic=mcp", ready: `.usage-mcp-dashboard ${analysisReady}` },
  { name: "MCP trends", url: `/analyze?analyze_topic=mcp&analyze_chart=${encodeURIComponent(JSON.stringify({ metric: "failure_rate", split: "tool_name", view: "trend", period: "week", rollingPeriods: 37 }))}`, ready: `.usage-mcp-dashboard ${overTimeReady}` },
  { name: "MCP query", url: "/analyze?analyze_mode=query&analyze_data=mcp_calls&analyze_topic=mcp&analyze_search=1", ready: ".usage-workspace-table .qt-row" },
  { name: "MCP legacy link", url: "/mcp", ready: `.usage-mcp-dashboard ${analysisReady}` },
  { name: "MCP settings", url: "/settings/mcp", ready: "#mcpSettingsPage .mcp-setup-grid" },
  // Files: a file's tabs (Git's code, blame and history, its sessions'
  // edits as diffs, the sessions), a directory with Go to file, a commit's
  // diffs, the checkout chooser, the tree hidden, and the projects.
  { name: "File code", url: "/files?file=internal/archive/api.go&file_project=pharos", ready: "#fileHistoryPage .files-code .files-lines" },
  { name: "File blame, tree hidden", url: "/files?file=internal/archive/api.go&file_project=pharos&file_view=blame&files_tree_collapsed=1", ready: "#fileHistoryPage .files-blame-session" },
  { name: "File history", url: "/files?file=internal/archive/api.go&file_project=pharos&file_view=history", ready: "#fileHistoryPage .files-commits .files-commit-subject" },
  { name: "File changes", url: "/files?file=internal/archive/api.go&file_project=pharos&file_view=changes", ready: "#fileHistoryPage .files-change .files-diff-line.add" },
  { name: "File sessions", url: "/files?file=internal/archive/api.go&file_project=pharos&file_view=sessions", ready: "#fileHistoryPage .file-sessions .file-place" },
  { name: "File checkout chooser", url: "/files?file=internal/archive/api.go&file_project=pharos", ready: "#fileHistoryPage .files-checkout .files-checkout-candidates button",
    api: { "GET /api/repositories/:id/checkout": ({ params }) => ({ repository_id: params.id, name: "pharos", path: null, status: "unset", reason: "Choose this repository's checkout on this Mac.", candidates: ["/Users/me/pharos", "/Users/me/conductor/workspaces/pharos/a-workspace-with-a-long-name"] }) } },
  { name: "Files directory", url: "/files?file_dir=internal&file_project=pharos", ready: "#fileHistoryPage .files-directory .files-table tbody tr",
    open: page => page.getByRole("searchbox", { name: "Go to file" }).fill("api"), shown: "#fileHistoryPage .files-find-results a" },
  { name: "Files commit", url: "/files?file_commit=0123456&file=internal/archive/api.go&file_project=pharos", ready: "#fileHistoryPage .files-commit .files-commit-diff[open] .files-diff-line.add" },
  { name: "Files projects", url: "/files", ready: "#fileHistoryPage .files-projects li" },
  { name: "Logs overview", url: "/settings/logs", ready: "#logsPage .logs-cards" },
  { name: "Logs service output", url: "/settings/logs?log_source=service", ready: "#logsPage .logs-run pre" },
  { name: "Logs empty source", url: "/settings/logs?log_source=background", ready: "#logsOperations" },
  { name: "Library health", url: "/settings", ready: "#healthCards .panel:not([aria-busy])" },
  // In the app, a catalog with much free space offers to compact it.
  { name: "Library health compact catalog", url: "/settings", ready: ".storage-compact button", managedLibrary: { id: "local", name: "Pending history", config: "/Users/test/Libraries/Pending/library.toml" },
    api: { "GET /api/health/storage/breakdown": compactableBreakdown }, open: page => page.getByRole("button", { name: "Compact catalog…", exact: true }).click(), shown: ".storage-compact button.primary" },
  { name: "Cloud backup entry", url: "/settings/backups", ready: "#cloud-explore" },
  { name: "Cloud backup ongoing status", url: "/settings/backups", ready: ".cloud-ongoing article", api:{'GET /api/cloud-backup':cloudScheduledStatus} },
  { name: "Cloud backup schedule settings", url: "/settings/backups", ready: ".cloud-ongoing article", api:{'GET /api/cloud-backup':cloudScheduledStatus},open:page=>page.locator('.cloud-schedule-settings summary').click(),shown:'.cloud-schedule-settings .cloud-fields' },
  { name: "Cloud backup previous revisions", url: "/settings/backups", ready: ".cloud-ongoing article", api:{'GET /api/cloud-backup':cloudScheduledStatus},open:page=>page.getByRole('button',{name:'View backups and test restore',exact:true}).click(),shown:'.cloud-history-select' },
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
  // Step 4 stands for the later setup steps (first backup, test restore).
  ...[[4,"Test"]].map(([step,label])=>({
    name:`Cloud backup ${label.toLowerCase()}`,url:"/settings/backups",ready:"#cloud-explore",
    open:async page=>{
      await page.getByRole('button',{name:'Explore cloud backup options',exact:true}).click();
      await page.getByRole('button',{name:'Configure this backup',exact:true}).click();
      await page.getByLabel('Google Cloud project ID',{exact:true}).fill('pharos-example');
      await page.getByRole('button',{name:`Step ${step}: ${label}`,exact:true}).click();
    },shown:`#cloud-step-${step-1}`
  })),
  { name: "Sources", url: "/settings/sources", ready: "#sourceGrid .source-card" },
  { name: "Background Processing", url: "/settings/sync", ready: "#syncHistoryTables .qt-row" },
  { name: "Background Processing integrity warning", url: "/settings/sync", ready: ".sync-warning", api: {
    "GET /api/sync/status": { ...defaultApi["GET /api/sync/status"], issues: [{ id: "mismatch", source_name: "codex", unit: `/sessions/${"long-workspace-name/".repeat(12)}rollout.jsonl` }] },
    "GET /api/activity": { runs: [{ id: "verified", kind: "verification", state: "complete", verification: { checked: 12, passed: 10, reindexed: 1, mismatch: 1, deferred: 1, errors: 0 } }] },
  } },
  ...[false, true].map(details => ({ name: `Background Processing deferred verification${details ? ' details' : ''}`, url: "/settings/sync", ready: "#syncHistoryTables .qt-row", api: {
    "GET /api/sync/status": { ...defaultApi["GET /api/sync/status"], history: [{ ...defaultApi["GET /api/sync/status"].history[0], audit: { outcome: "deferred", discovered_units: 10, deferred_units: 1, detail: "large group queued for manual verification" } }] },
  }, ...(details ? { open: page => page.getByRole("region", { name: "Recent run history", exact: true }).getByRole("button", { name: "Details", exact: true }).first().click(), shown: ".sync-run-dialog .qt-row" } : {}) })),
  { name: "Background Processing schedule", url: "/settings/sync", ready: "#backgroundSchedule .bg-col", open: page => page.locator("#backgroundSchedule .bg-item-head", { hasText: "Update writing estimates" }).click(), shown: "#backgroundSchedule .bg-detail:not([hidden])" },
  { name: "Background Processing message labels waiting", url: "/settings/sync", ready: "#backgroundSchedule .bg-col", api: {
    "GET /api/background": { ...defaultApi["GET /api/background"], labels: { enabled: true, can_change: true,
      status: "On, waiting for the labeling model: partly downloaded (64 of 142 MB, resumed on the next attempt). The labeling worker is missing; install a Pharos build that includes it, or configure a local worker.",
      notice: `The configured labeling worker /Applications/${'LongApplicationName'.repeat(12)}/Contents/MacOS/pharos-labeler-ort is missing. Reinstall Pharos.`,
      execution: {device:'unavailable', status:'GPU unavailable', detail:`Cannot load /Applications/${'LongApplicationName'.repeat(12)}/Contents/MacOS/mlx/libpharos_mlx.dylib`, confirmed:false} },
      agent_events: { enabled: true, can_change: false, status: "On in this library's configuration (agent_events_enabled).", notice:"Enabled in this library's configuration." } },
  }, open: async page => {
    await page.locator('[data-view-key="background:labels"]').click();
    await page.locator('[data-view-key="background:agent_events"]').click();
  }, shown: '#background-detail-labels:not([hidden])' },
  { name: "Background Processing temporary schedule", url: "/settings/sync", ready: "#backgroundSchedule .bg-col", api: { "GET /api/background": temporaryBackgroundDocument }, open: page => page.locator("#backgroundSchedule .bg-item-head", { hasText: "Update writing estimates" }).click(), shown: "#backgroundSchedule .bg-detail:not([hidden])" },
  { name: "Background Processing work", url: "/settings/sync", ready: "#backgroundSchedule [data-card=work] .bg-work-row", api: { "GET /api/background": workBackgroundDocument, "GET /api/work": workInspection },
    open: page => page.locator('#backgroundSchedule [data-view-key="background:work:details"] summary').click(), shown: '#backgroundSchedule [data-view-key="background:work:details"][open] .bg-work-row' },
  { name: "Background Processing sources", url: "/settings/sync", ready: "#backgroundSchedule .bg-col", open: page => page.locator("#backgroundSchedule .bg-item-head", { hasText: "New conversations" }).click(), shown: "#backgroundSchedule .bg-sources" },
  { name: "Background Processing queued verification", url: "/settings/sync", ready: "#backgroundSchedule .bg-col", open: page => page.locator("#backgroundSchedule .bg-item-head", { hasText: "Verify indexed data" }).click(), shown: "#backgroundSchedule .bg-detail:not([hidden])" },
  { name: "Background Processing diagnostics", url: "/settings/sync", ready: "#syncHistoryTables .qt-row", open: page => page.getByText("Performance & verification details", { exact: true }).click(), shown: "#syncDiagnosticTables .qt-row" },
  { name: "Background Processing run details", url: "/settings/sync", ready: "#syncHistoryTables .qt-row", open: page => page.getByRole("region", { name: "Recent run history", exact: true }).getByRole("button", { name: "Details", exact: true }).first().click(), shown: ".sync-run-dialog .qt-row" },
  { name: "Background Processing empty", url: "/settings/sync", ready: "#syncHistoryTables .qt-empty", api: { "GET /api/sync/status": { ...defaultApi["GET /api/sync/status"], history: [], source_states: [] } } },
  { name: "Legacy Findings settings", url: "/settings/optimization", ready: ".findings-settings .findings-panel" },
  { name: "Layout Preferences", url: "/settings/layout", ready: "#headerExpand" },
  { name: "Header settings", url: "/settings/layout", ready: "#headerExpand", open: page => page.locator('#headerExpand').click(), shown: '#headerActive .header-tool' },
  { name: "Quick Stats settings", url: "/settings/layout", ready: "#quickStatsExpand", open: page => page.locator('#quickStatsExpand').click(), shown: '#quickStatsEditor button' },
  { name: "Quick Stats toolbox", url: "/settings/layout", ready: "#quickStatsExpand", preferences: { "pharos-quick-stats-v1": { metrics: ['human_words', 'tokens'], windows: ['today', '24h'] } }, open: page => page.locator('#quickStatsExpand').click(), shown: '#quickStatsPreview td' },
  { name: "App preferences", url: "/settings/preferences", ready: "#lighthouseSpinOnClick" },
  { name: "Model colors", url: "/settings/models", ready: ".model-provider-card", open: async page => { await page.locator(".model-provider-card summary").first().click(); }, shown: ".model-provider-card details[open] .model-color-model" },
  { name: "Project colors", url: "/settings/projects", ready: ".project-color-row" },
  { name: "Mac menu disabled", url: "/settings/layout", ready: "#showMacMenuBar", preferences: { 'pharos-menu-bar': { enabled: false } } },
  { name: "Mac menu expanded", url: "/settings/layout", ready: "#showMacMenuBar", open: page => page.locator('#menuBarExpand').click(), shown: '#menuBarActive button' },
  { name: "Mac menu syncing", url: "/settings/layout", ready: "#showMacMenuBar", open: page => page.locator('#menuBarExpand').click(), shown: '#menuBarActive .menu-bar-detail', api: {
    'GET /api/menu-bar/status': {...defaultApi['GET /api/menu-bar/status'], library: {activities: [{label: 'Manual sync: evaluating findings while checking every captured conversation and its retained tool results', progress: .22, estimated_completion_at: new Date(Date.now() + 60000).toISOString()}]}},
  } },
  { name: "Mac menu with all items", url: "/settings/layout", ready: "#showMacMenuBar", open: page => page.locator('#menuBarExpand').click(), shown: '#menuBarActive button', preferences: { 'pharos-menu-bar': { cost: true, backup: true, space: true, index: true, eject: true, search: true, analyze: true, optimize: true, capture: true, health: true, sources: true, finder: true } } },
  { name: "Workspace overview", url: work, api: tokenEvidenceAPI, ready: ".delegation-table tbody tr" },
  { name: "Workspace files and commits", url: `${work}?disclosures=${encodeURIComponent(JSON.stringify({ [`work:${workID}:files`]: true }))}`, ready: ".work-files .file-place",
    api: { "GET /api/work/:id": request => ({ ...defaultApi["GET /api/work/:id"](request), files: [{ conversation_id: rootConversation, path: "internal/archive/api.go" }] }) } },
  { name: "Workspace Timeline", url: `${work}?explorer=timeline`, ready: ".lt-session-root" },
  { name: "Workspace Timeline compact key", url: `${work}?explorer=timeline&disclosures=${encodeURIComponent(JSON.stringify({'timeline-key':false}))}`, ready: ".lt-session-root", api: {
    "GET /api/work/:id/parallel": request => {
      const data=defaultApi["GET /api/work/:id/parallel"](request);
      if(request.query.get('labels')!=='1')return data;
      // A dense mode family must wrap individual icons at narrow widths.
      const ids=Array.from({length:40},(_,index)=>`C.report.fixture${index}`);
      return {...data,taxonomy:[...data.taxonomy,...ids.map((id,index)=>({id,name:`Fixture mode ${index}`,definition:'Synthetic layout fixture.'}))],lanes:data.lanes.map(lane=>({...lane,messages:lane.messages.map(message=>message.role==='assistant'?{...message,labels:{...message.labels,ids:[...message.labels.ids,...ids]}}:message)}))};
    },
  } },
  { name: "Workspace with related work", url: work, api: relatedWorkAPI, ready: ".peer-strip .peer-chip.uncertain", open: page => page.locator(".peer-reasons summary").click(), shown: ".peer-reasons[open] li",
    check: async page => {
      await page.locator('[data-session-id="s-peer"]').first().waitFor();
      // Following a hand-off opens the related conversation and reveals the
      // message without taking keyboard focus.
      await page.goto(`${page.url().split("/work/")[0]}${work}/conversation/${rootConversation}`);
      await page.locator(".handoff-marker").first().click();
      await page.locator(".peer-reader-note").waitFor();
      await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(() => requestAnimationFrame(resolve)))));
      assert.ok(await page.evaluate(() => !document.activeElement?.closest(".conversation-panel")), "following a hand-off moved focus into the reader");
    } },
  { name: "Workspace Timeline skipping idle time, agent events expanded", url: `${work}?explorer=timeline&disclosures=${idleTimelineDisclosures}`, api: idleTimelineAPI, ready: ".lt-session-node .lt-session-row-label svg.event-link-glyph", open: page => page.locator(".lt-session-ribbon .lt-idle-cut-line").first().waitFor({ state: "attached" }), shown: ".lt-clock-heading .lt-idle-cut time" },
  // Unsettled evidence: an ≈ session count, "+N more" relatives and pending events.
  { name: "Workspace overview with pending evidence", url: work, api: pendingEvidenceApi, ready: ".delegation-table .delegation-relatives" },
  { name: "Workspace Timeline with pending events", url: `${work}?explorer=timeline`, api: pendingEvidenceApi, ready: ".lt-recursive-lane .lt-session-notice", check: page => page.locator("[role=status].delegation-truncation").first().waitFor() },
  { name: "Workspace sessions sidebar with pending evidence", url: `${work}/conversation/${rootConversation}`, api: pendingEvidenceApi, ready: ".delegation-inbox-heading" },
  { name: "Long transcript", url: `${work}/conversation/${rootConversation}`, ready: ".conversation-panel .turn" },
  { name: "Child transcript", url: `${work}/conversation/conv-nested`, api: tokenEvidenceAPI, ready: ".conversation-panel .turn" },
  { name: "Quick search", url: "/library?view=table", ready: "#queryTableLibrary .qt-row", open: page => page.keyboard.press("ControlOrMeta+k"), shown: "#searchDialog[open]" },
  { name: "Analyze summary", url: "/library?view=table", ready: "#queryTableLibrary .qt-row", open: page => activateHeaderTool(page, "usageSummaryToggle"), shown: "#usageSummary" },
  { name: "Extended Quick Stats", url: "/library?view=table", preferences: { "pharos-quick-stats-v1": {metrics:['active_conversations','message_count','user_messages','assistant_messages','tool_calls','failed_tool_calls','tool_failure_rate','delegated_runs','active_repositories','tool_time_ms','agent_time_ms','active_time_ms'], windows:['5m','15m','yesterday','week','month','all']}, "pharos-usage-summary-links":true }, ready: "#queryTableLibrary .qt-row", open: page => activateHeaderTool(page,"usageSummaryToggle"), shown: "#usageSummary" },
  { name: "Customized Quick Stats", url: "/library?view=table", preferences: { "pharos-quick-stats-v1": { metrics: ['cost_usd', 'tokens'], windows: ['today', '6h'] } }, ready: "#queryTableLibrary .qt-row", open: page => activateHeaderTool(page, "usageSummaryToggle"), shown: "#usageSummary" },
  { name: "Clickable Analyze summary", url: "/library?view=table", preferences: { "pharos-usage-summary-links": true }, ready: "#queryTableLibrary .qt-row", open: page => activateHeaderTool(page, "usageSummaryToggle"), shown: "#usageSummary" },
  { name: "App navigation", url: "/analyze", ready: ".usage-dashboard .usage-chart", open: async page => {
    if (await page.locator('#appNavigationToggle').isVisible()) await page.locator('#appNavigationToggle').click();
    else {
      await page.getByRole('button', { name: 'Collapse sidebar', exact: true }).click();
      await page.getByRole('button', { name: 'Expand sidebar', exact: true }).click();
    }
  }, shown: '#appNavigationRail:not([hidden]), #appNavigationDialog[open]' },
  { name: "Bookmarks drawer", url: "/library?view=table", ready: "#queryTableLibrary .qt-row", preferences: { "pharos-show-nav-button": true }, open: async page => { await activateHeaderTool(page, "navigatorToggle"); await page.locator("#bookmarksToggle").click(); }, shown: "#bookmarksDrawer.open" },
  { name: "History drawer", url: "/library?view=table", ready: "#queryTableLibrary .qt-row", preferences: { "pharos-show-nav-button": true }, open: async page => { await activateHeaderTool(page, "navigatorToggle"); await page.locator("#historyToggle").click(); }, shown: "#historyDrawer.open" },
];
// A desktop and the narrowest phone. The header and sidebar breakpoints in
// between have their own standalone tests below.
const WIDTHS = [{ width: 1440, height: 900 }, { width: 320, height: 640 }];

// Layout bugs found by this sweep and not fixed yet. A case whose only
// problems match here is marked todo with the bug, so the suite stays honest;
// any other problem still fails it.
const KNOWN = [];

for (const width of [1440, 320]) test(`Search filter suggestions stay readable and keep input focus @ ${width}`, { skip }, async t => {
  const values = ["exploration", "exploration-service", "exploration-service-v2", "gbw-utilities", "general-purpose-repository",
    "go-query-table-integration-with-a-long-unbroken-suffix-abcdefghijklmnopqrstuvwxyz"];
  const query = { ...EMPTY_QUERY, where: [{ field: "repository_name", op: "=", value: "" }] };
  const app = await open(t, {
    url: `/library?${new URLSearchParams({ q_library: encodeQuery(query) })}`,
    viewport: { width, height: 844 }, ready: "#queryTableLibrary .qt-qb",
    api: { "POST /api/query/library/query-v3": request => {
      const result = defaultApi["* /api/query/:dataset/:operation"]({ ...request, params: { dataset: "library", operation: "query-v3" } });
      return request.body.distinct ? { ...result, distinct: { values: values.filter(value => value.includes(request.body.distinct.search)), hasMore: false, hasNull: false } } : result;
    } },
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
  const { page, settled, shot } = await open(t, { url: '/library?view=table', ready: '#queryTableLibrary .qt-row' });
  const more = page.getByRole('button', { name: 'More tools', exact: true });
  // One width per regime: centered, collected actions, phone header, the
  // narrowest phone, and back to a width where every tool fits.
  for (const width of [1440, 1000, 700, 320, 1280]) {
    await page.setViewportSize({ width, height: 900 });
    await settled();
    if (width === 1280) assert.equal(await more.isVisible(), false, `tools still fit at ${width}px`);
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

// Inline at 1440; collected into More at 320.
for (const width of [1440, 320]) test(`Pharos release action fits through download and relaunch @ ${width}`, {skip}, async t => {
  const {page, settled, shot} = await open(t, {
    url: '/library?view=table', viewport: {width, height: width < 500 ? 844 : 900}, ready: '#queryTableLibrary .qt-row',
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
  const { page, settled, shot } = await open(t, { url: '/library?view=table', ready: '#queryTableLibrary .qt-row' });
  await page.addStyleTag({ content: chromeCSS });
  await page.evaluate(() => {
    document.documentElement.classList.add('pharos-window');
    // AppKit places three 14 pt buttons at x=19, 39 and 59 in a 52 pt bar.
    // Page zoom scales CSS pixels but not the window's points, so the
    // controls are sized in points divided by --pharos-zoom.
    const controls = document.createElement('div');
    controls.id = 'nativeWindowControls';
    controls.style.cssText = 'position:fixed;left:calc(19px / var(--pharos-zoom, 1));top:calc(19px / var(--pharos-zoom, 1));display:flex;gap:calc(6px / var(--pharos-zoom, 1));z-index:100';
    for (const color of ['#ff5f57', '#febc2e', '#28c840']) {
      const button = document.createElement('span');
      button.style.cssText = `width:calc(14px / var(--pharos-zoom, 1));height:calc(14px / var(--pharos-zoom, 1));border-radius:50%;background:${color}`;
      controls.append(button);
    }
    document.body.append(controls);
  });
  const setZoom = zoom => page.evaluate(zoom => document.documentElement.style.setProperty('--pharos-zoom', String(zoom)), zoom);
  for (const width of [2133, 1180, 320]) {
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
  // Zooming out must not slide the lighthouse under the controls.
  for (const zoom of [0.5, 1.5]) {
    await setZoom(zoom);
    await settled();
    const controls = await page.locator('#nativeWindowControls').boundingBox();
    assert.ok((await page.locator('body > header .brand').boundingBox()).x >= controls.x + controls.width, `lighthouse clears window controls at ${zoom * 100}% zoom`);
    assert.deepEqual(await layoutProblems(page), []);
  }
});

test('Ask Pharos suggestions stay readable, prefill the editor and remember being hidden', { skip }, async t => {
  const {page,settled,shot,calls}=await open(t,{url:'/',ready:'.pharos-idea-toggle',viewport:{width:2133,height:1070},api:{
    ...askAPI,
    'POST /api/ask/session':({body,state})=>state.suggestionDraft={...askSession,...body,conversation:[]},
    'GET /api/ask/session':({query,state})=>state.suggestionDraft||{...askSession,id:query.get('id'),conversation:[]},
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
  const distinctHint=async()=>{
    const {hint,visible,questions}=await page.evaluate(()=>({
      hint:document.getElementById('pharos-question').dataset.placeholder,
      visible:[...document.querySelectorAll('.pharos-suggestion[data-reading] .pharos-idea-text')].map(label=>label.textContent),
      questions:[...document.querySelectorAll('.pharos-idea-text')].map(label=>label.textContent),
    }));
    assert.ok(questions.includes(hint)&&!visible.includes(hint),'the placeholder uses a question outside the visible suggestion cards');
  };
  for(const width of [320,2133]){
    await page.setViewportSize({width,height:1070});await settled();
    assert.deepEqual(await layoutProblems(page),[]);
    await readable();
    await distinctHint();
    await shot(`expanded-${width}`);
  }
  for(let step=0;step<2;step++){
    await suggestions.getByRole('button',{name:'Next suggestions',exact:true}).click();
    await page.waitForFunction(()=>!document.querySelector('.pharos-idea-viewport').hasAttribute('data-moving'));
    await readable();
    await distinctHint();
  }
  await suggestions.getByRole('button',{name:'Refresh suggestions',exact:true}).click();
  await page.waitForFunction(()=>document.querySelector('.pharos-idea-announcement')?.textContent==='Prompt suggestions refreshed.');
  assert.ok(calls.some(call=>call.path==='/api/ask/suggestions'&&call.query.get('seed')==='1'),'Refresh asks the library for another slate');
  await readable();
  await distinctHint();
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
  // Full screen gives the row the window's width, so it reads more cards at once.
  // Its suggestions give way to a draft, so clear the one chosen above.
  await page.goto(new URL('/ask',page.url()).href);await editor.waitFor();await editor.fill('');
  await viewport.waitFor();await settled();
  assert.ok(await viewport.locator('[data-reading]').count()>3,'a wide full screen row shows more than three suggestions');
  assert.deepEqual(await layoutProblems(page),[]);
  await readable();
  await distinctHint();
  await shot('full-screen-2133');
  await suggestions.getByRole('button',{name:'Next suggestions',exact:true}).click();
  await page.waitForFunction(()=>!document.querySelector('.pharos-idea-viewport').hasAttribute('data-moving'));
  await readable();
  await distinctHint();
});

test("Background Processing items drag between all four columns for a week", { skip }, async t => {
  let document = structuredClone(defaultApi['GET /api/background']);
  let heldRead = null, heldSave = null;
  const { page, calls } = await open(t, { url: '/settings/sync', viewport: { width: 1800, height: 1800 }, ready: '#backgroundSchedule .bg-col', api: {
    'GET /api/background': () => {
      if (!heldRead) return document;
      heldRead.started.resolve();
      return heldRead.reply.promise;
    },
    'POST /api/background/settings': async ({ body }) => {
      if (heldSave) await heldSave.promise;
      for (const [id, cadence] of Object.entries(body.temporary_cadences)) {
        const item = document.view.items.find(item => item.id === id);
        item.cadence = cadence || item.usual_cadence;
        item.column = item.cadence;
        item.temporary_cadence = cadence ? { cadence, expires_at: new Date(Date.now() + 7 * 86400000).toISOString() } : null;
      }
      document.view.cadences = document.view.cadences.map(column => ({ ...column,
        items: document.view.items.filter(item => item.cadence === column.id).map(item => item.id) }));
      document.view.unscheduled = document.view.items.filter(item => item.cadence === 'manual').map(item => item.id);
      return document;
    },
  } });
  const schedule = page.locator('#backgroundSchedule');
  // A poll already in flight must not undo a move, even when it finishes
  // after the save. Hold responses by condition, without timing assumptions.
  const stale = structuredClone(document);
  heldRead = {started: Promise.withResolvers(), reply: Promise.withResolvers()};
  await page.evaluate(() => { window.scheduleRefresh = window.pharosBackground.refresh(true); });
  await heldRead.started.promise;
  heldSave = Promise.withResolvers();
  // Move the backup out first; the fourth column remains a destination for enabled work.
  const backup = schedule.locator('.bg-item[data-item="backup"]');
  await backup.locator('.bg-grip').dragTo(schedule.locator('.bg-col[data-cadence="daily"]'));
  await schedule.locator('.bg-col[data-cadence="daily"] .bg-item[data-item="backup"]').waitFor();
  assert.ok(document.view.unscheduled.includes('backup'), 'the task moves before the server answers');
  // Cadence runs resolve membership on the server, which still has the old
  // schedule. Task runs use an explicit ID and can remain available.
  const cadenceRuns = schedule.locator('.bg-col-head .bg-run');
  for (const run of await cadenceRuns.all()) assert.ok(await run.isDisabled());
  assert.ok(await schedule.getByRole('button', {name: 'Run Update writing estimates now', exact: true}).isEnabled());
  await cadenceRuns.evaluateAll(buttons => buttons.forEach(button => button.click()));
  assert.ok(!calls.some(call => call.method === 'POST' && call.path === '/api/background/run'), 'no cadence runs before the move is saved');
  const saved = page.waitForResponse(response => response.request().method() === 'POST' && response.url().endsWith('/api/background/settings'));
  heldSave.resolve();
  heldSave = null;
  await saved;
  await page.waitForFunction(() => !document.querySelector('#backgroundSchedule [data-item="backup"] .bg-state').textContent.includes('Saving'));
  assert.ok(await schedule.locator('.bg-col[data-cadence="daily"] .bg-col-head .bg-run').isEnabled(), 'cadence runs resume after saving');
  heldRead.reply.resolve(stale);
  await page.evaluate(() => window.scheduleRefresh);
  heldRead = null;
  await schedule.locator('.bg-col[data-cadence="daily"] .bg-item[data-item="backup"]').waitFor();
  for (const id of document.view.items.filter(item => item.state !== 'disabled').map(item => item.id)) {
    for (const cadence of ['manual', 'frequent', 'hourly', 'daily']) {
      const item = schedule.locator(`.bg-item[data-item="${id}"]`);
      await item.locator('.bg-grip').dragTo(schedule.locator(`.bg-col[data-cadence="${cadence}"]`));
      await schedule.locator(`.bg-col[data-cadence="${cadence}"] .bg-item[data-item="${id}"]`).waitFor();
    }
  }
  assert.ok(calls.some(call => call.method === 'POST' && call.path === '/api/background/settings'
    && call.body?.temporary_cadences?.conversations === 'manual'), 'fixed items accept the manual column');
  // A redraw keeps keyboard focus on the item's run button.
  await page.getByRole('button', { name: 'Run Findings now', exact: true }).focus();
  await page.evaluate(() => window.pharosBackground.render());
  assert.equal(await page.evaluate(() => document.activeElement?.getAttribute('aria-label')), 'Run Findings now');
});

test("Background Processing shares the busy switch across Hourly and Daily and shows work history", { skip }, async t => {
  let holdWhenBusy = true;
  const { page, calls, settled, shot } = await open(t, { url: '/settings/sync', ready: '#syncHistoryTables .qt-row', api: {
    'POST /api/background/settings': ({ body }) => {
      holdWhenBusy = body.hold_when_busy;
      return { ...defaultApi['GET /api/background'], settings: { ...defaultApi['GET /api/background'].settings, hold_when_busy: holdWhenBusy } };
    },
  } });
  const schedule = page.locator('#backgroundSchedule');
  assert.equal(await schedule.getByText('What it costs').count(), 0);
  assert.equal(await schedule.getByText('Be considerate').count(), 0);
  const hourly = schedule.locator('.bg-col[data-cadence="hourly"]').getByRole('switch', { name: 'Hold while busy' });
  const daily = schedule.locator('.bg-col[data-cadence="daily"]').getByRole('switch', { name: 'Hold while busy' });
  assert.ok(await hourly.isVisible());
  assert.ok(await daily.isVisible());
  assert.equal(await schedule.locator('.bg-mode').getByRole('switch').count(), 0);
  await hourly.uncheck();
  await settled();
  assert.equal(await daily.isChecked(), false);
  assert.ok(calls.some(call => call.method === 'POST' && call.path === '/api/background/settings' && call.body?.hold_when_busy === false));
  await daily.check();
  await settled();
  assert.equal(await hourly.isChecked(), true);
  assert.equal(calls.filter(call => call.method === 'POST' && call.path === '/api/background/settings' && 'hold_when_busy' in call.body).length, 2);
  const history = page.getByRole('region', { name: 'Recent run history', exact: true });
  for (const work of ['Index changes', 'Update findings', 'Back up']) assert.ok(await history.getByText(work, { exact: true }).count());
  await history.getByRole('button', { name: 'Add filter', exact: true }).click();
  await page.getByRole('button', { name: /^Operation(?:\s|$)/ }).click();
  await history.getByRole('combobox', { name: 'Value for Operation', exact: true }).fill('Back up');
  await page.waitForFunction(() => document.querySelectorAll('#syncHistoryTables .qt-row').length === 1);
  assert.ok(await history.getByText('Back up', { exact: true }).isVisible());
  assert.equal(await page.getByRole('region', { name: 'Source status', exact: true }).locator('.qt-row').count(), 2, 'history filters leave other tables alone');
  await history.getByRole('button', { name: 'Remove Operation filter', exact: true }).click();
  await page.waitForFunction(() => document.querySelectorAll('#syncHistoryTables .qt-row').length === 3);
  await history.getByRole('button', { name: 'Add filter', exact: true }).click();
  await page.getByRole('button', { name: 'Started started_at', exact: true }).click();
  await history.getByRole('combobox', { name: 'Value for Started', exact: true }).fill(defaultApi['GET /api/operations'].journal.recent[0].started_at);
  await page.waitForFunction(() => document.querySelectorAll('#syncHistoryTables .qt-row').length === 1);
  await history.getByRole('button', { name: 'Remove Started filter', exact: true }).click();
  await history.getByRole('button', { name: 'Add sort', exact: true }).click();
  await page.getByRole('button', { name: /^Elapsed(?:\s|$)/ }).click();
  await page.waitForFunction(() => document.querySelectorAll('#syncHistoryTables .qt-row').length === 3);
  assert.equal(await page.getByLabel('Last run cost').count(), 0);
  await shot('background-processing');
  await history.scrollIntoViewIfNeeded();
  await shot('work-history');
});

test("dashboard collection wraps after four bookmarked cards", { skip }, async t => {
  const bookmarks = Array.from({ length: 9 }, (_, index) => ({ id: `card-${index}`, title: `Saved view ${index + 1}`, url: `/settings/preferences?card=${index}` }));
  const { page, settled, shot } = await open(t, { url: '/', ready: '.home-collection .query-card', preferences: { 'pharos-bookmarks-v1': bookmarks } });
  const collection = page.getByRole('region', { name: 'Your collection', exact: true });
  for (const width of [3000, 1180, 320]) {
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

test("dashboard loading follows the chart and keeps controls usable", { skip }, async t => {
  const { page, settled } = await open(t, { url: '/analyze?analyze_topic=tokens', viewport: { width: 390, height: 850 }, ready: overTimeReady });
  let release;
  let response = new Promise(resolve => { release = resolve; });
  const hold = async route => {
    if (route.request().method() === 'POST') await response;
    await route.continue();
  };
  await page.route('**/api/query/usage/*', hold);
  t.after(() => release());
  try {
    const trend = page.locator('section[aria-label="Over time"]');
    const column = await trend.locator('.usage-chart-bars > button').first().elementHandle();
    const view = trend.getByLabel('Analysis', { exact: true });
    for (const event of ['dataset', 'focus', 'visibilitychange']) {
      await column.focus();
      await page.evaluate(event => {
        if (event === 'dataset') window.dispatchEvent(new CustomEvent('pharos:data-refresh', { detail: 'usage' }));
        else if (event === 'focus') window.dispatchEvent(new Event('focus'));
        else document.dispatchEvent(new Event('visibilitychange'));
      }, event);
      await trend.locator('[data-loading-kind="columns"]').waitFor();
      assert.ok(await column.evaluate(el => el.isConnected && document.activeElement === el), `${event} keeps the focused period during loading`);
      if (event === 'visibilitychange') await view.focus();
      release();
      await page.locator(overTimeReady).waitFor();
      await settled();
      if (event === 'visibilitychange') assert.ok(await view.evaluate(el => document.activeElement === el), 'loading does not take focus back from another control');
      else assert.ok(await column.evaluate(el => el.isConnected && document.activeElement === el), `${event} keeps the focused period after loading`);
      response = new Promise(resolve => { release = resolve; });
    }

    await page.getByRole('button', { name: 'Refresh', exact: true }).click();
    await trend.locator('[data-loading-kind="columns"]').waitFor();
    assert.deepEqual(await layoutProblems(page), []);
    assert.equal(await trend.locator('.query-preview-spinner').evaluate(el => getComputedStyle(el).animationName), 'none', 'reduced motion stops the spinner');
    release();
    await page.locator(overTimeReady).waitFor();

    response = new Promise(resolve => { release = resolve; });
    await trend.getByLabel('Analysis', { exact: true }).selectOption('compare');
    const analysis = page.locator('.usage-analysis-dashboard');
    await analysis.locator('[data-loading-kind="list"]').waitFor();
    assert.deepEqual(await layoutProblems(page), []);
    release();
    await page.locator(analysisReady).waitFor();
    await settled();

    const fail = route => route.fulfill({ status: 200, json: { metrics: [] } });
    await page.route('**/api/query/usage/query-v3', fail);
    await page.getByRole('button', { name: 'Refresh', exact: true }).click();
    await analysis.locator('[role="alert"]').waitFor();
    await page.unroute('**/api/query/usage/query-v3', fail);
    response = new Promise(resolve => { release = resolve; });
    await page.getByRole('button', { name: 'Refresh', exact: true }).click();
    await analysis.locator('[data-loading-kind="list"]').waitFor();
    assert.equal(await analysis.locator('[role="alert"]').count(), 0, 'a retry replaces the old error with loading');
    release();
    await page.locator(analysisReady).waitFor();
    await settled();
  } finally {
    release();
    await page.unroute('**/api/query/usage/*', hold);
  }
});

test('Recent conversation links open the selected conversation and search', { skip }, async t => {
  const { page, settled, calls } = await open(t, { url: '/', ready: '.home-recent-grid .home-recent-card' });
  assert.equal(calls.some(call => /\/api\/work\/.+\/conversations\//.test(call.path)), false, 'previews do not load transcripts');
  await page.locator('.home-recent-grid .home-recent-title').first().click();
  await page.waitForURL(url => url.pathname === `/work/${workID}/conversation/${rootConversation}`);
  await page.locator('#detail .conversation-panel:not(.conversation-loading)').first().waitFor();
  await settled();
  await page.goBack();
  await page.locator('.home-recent-search').click();
  await page.waitForURL(url => url.pathname === '/library' && url.searchParams.get('view') === 'conversation');
  await page.locator('#queryTableLibrary .conversation-result-card').first().waitFor();
});

test('Habits daily bars use the available width when the window and range change', { skip }, async t => {
  const app = await open(t, { url: '/analyze?analyze_topic=habits&habits_view=days', viewport: { width: 1920, height: 1050 }, ready: '.habits-days-chart .habits-span',
    api: {'GET /api/status/stream': eventStream(busyStatus), 'GET /api/status': busyStatus} });
  const { page } = app, chart = page.locator('.habits-days-chart'), bar = chart.locator('.habits-span').first();
  await page.waitForFunction(() => {
    const chart = document.querySelector('.habits-days-chart'), svg = chart?.querySelector('svg');
    return chart && svg && Number(svg.getAttribute('width')) === chart.clientWidth;
  });
  const wide = (await bar.boundingBox()).width;
  await chart.scrollIntoViewIfNeeded();
  await app.shot('wide');
  await page.setViewportSize({ width: 390, height: 900 });
  await page.waitForFunction(previous => document.querySelector('.habits-days-chart .habits-span')?.getBoundingClientRect().width < previous, wide);
  const narrow = (await bar.boundingBox()).width;
  assert.ok(narrow < wide, 'bars shrink with the available width');
  await page.getByRole('combobox', { name: 'Your days range', exact: true }).selectOption('7');
  await page.waitForFunction(previous => document.querySelector('.habits-days-chart .habits-span')?.getBoundingClientRect().width > previous, narrow);
  assert.ok((await bar.boundingBox()).width > narrow, 'fewer days get wider bars');
  await page.setViewportSize({ width: 1920, height: 1050 });
  await page.evaluate(() => { window.webkit = {messageHandlers: {pharosLibrary: {postMessage: async () => {}}}}; });
  await activateHeaderTool(page, 'pharosDrive');
  const panel = page.getByRole('dialog', { name: 'Library drive', exact: true });
  const settings = panel.getByRole('button', { name: 'Open Background Processing settings', exact: true });
  await settings.waitFor();
  await panel.getByRole('button', { name: 'Stop', exact: true }).waitFor();
  await panel.locator('#pharosStatusEject').waitFor();
  await app.shot('background-processing-panel');
  await settings.click();
  await page.waitForURL(url => url.pathname === '/settings/sync');
  await page.locator('#backgroundSchedule .bg-col').first().waitFor();
  assert.equal(await panel.count(), 0, 'opening settings closes the panel');
});

test('pages without section menus use the full window', { skip }, async t => {
  const width = 2133;
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
  await page.locator('.conversation-result-card').first().waitFor();
  await fillsWindow();
  await page.setViewportSize({ width: width + 400, height: 1075 });
  await fillsWindow();
});

for(const width of [725,390])test(`Timeline axis clocks fit zoomed windows at ${width}px`,{skip},async t=>{
  const start=defaultApi['GET /api/work/:id/delegation']({query:new URLSearchParams()}).start;
  const url=work+'?'+new URLSearchParams({explorer:'timeline',delegation_start:String(start),delegation_end:String(start+30000)});
  const app=await open(t,{url,viewport:{width,height:850},ready:'.lt-session-root'}),{page}=app;
  await page.waitForFunction(()=>{
    const labels=[...document.querySelectorAll('.delegation-axis time')].map(element=>element.getBoundingClientRect());
    return labels.length>1&&labels.every((rect,i)=>!i||rect.left>=labels[i-1].right);
  });
  assert.deepEqual(await layoutProblems(page),[]);
});

describe("structure", { concurrency: Number(process.env.PHAROS_SWEEP_CONCURRENCY || 3) }, () => {
  for (const route of ROUTES) {
    for (const viewport of WIDTHS) {
      const name = `${route.name} @ ${viewport.width}`;
      test(name, { skip }, async t => {
        const app = await open(t, { url: route.url, viewport, ready: route.ready, preferences: route.preferences, concurrency: route.concurrency, managedLibrary: route.managedLibrary, nativeHost: route.nativeHost, api: route.api });
        // Loading a page leaves initial focus to the user, and its title is
        // ordinary text. A dashboard's chart title stands in for its h1.
        const focus = route.open || route.movesFocus ? [] : await app.page.evaluate(() => {
          const found = [], name = el => `${el.tagName.toLowerCase()}${el.id ? `#${el.id}` : ""}${el.className && typeof el.className === "string" ? `.${el.className.trim().split(/\s+/)[0]}` : ""}`;
          const active = document.activeElement;
          if (active && active !== document.body) found.push(`loading the page focused ${name(active)}`);
          for (const title of document.querySelectorAll(".view.active :is(h1, .usage-chart-headline > h2)")) {
            if (!title.checkVisibility() || getComputedStyle(title).clipPath !== "none") continue;
            title.focus();
            if (document.activeElement === title) { found.push(`page title accepts focus: "${title.textContent.trim().slice(0, 40)}"`); title.blur(); }
          }
          return found;
        });
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
        if (route.check) await route.check(app.page, app);
        await app.shot("top");
        const problems = await layoutProblems(app.page);
        if (!route.open) {
          problems.push(...await scrollProblems(app.page));
          await app.settled();
          await app.shot("scrolled");
          problems.push(...(await layoutProblems(app.page)).filter(problem => !problems.includes(problem)));
        }
        problems.push(...focus, ...fitting, ...app.problems());
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


test("project color settings show indexed projects while mounted and retain editor focus", { skip }, async t => {
  let projects = [];
  const { page } = await open(t, { url: "/settings/projects", projectColors: [], ready: ".project-color-settings", api: {
    "GET /api/project-colors": () => projects,
  } });
  const refresh = () => page.evaluate(() => window.dispatchEvent(new Event("pharos:usage-refresh")));
  projects = projectColorFixture.slice(0, 1);
  await refresh();
  const first = page.getByLabel(`Color for ${projects[0].name}`, { exact: true });
  await first.waitFor({ state: "visible" });
  await first.focus();
  projects = [...projectColorFixture];
  await refresh();
  await page.getByLabel(`Color for ${projects[1].name}`, { exact: true }).waitFor({ state: "visible" });
  assert.equal(await first.evaluate(element => element === document.activeElement), true, "registry refresh leaves the existing editor focused");

  await page.evaluate(() => window.pharosNavigate("/settings/preferences"));
  projects = [...projects, { ...projectColorFixture[0], id: "repository:new", name: "New project", key: "c".repeat(64) }];
  await refresh();
  await page.getByLabel("Color for New project", { exact: true }).waitFor({ state: "attached" });
  await page.evaluate(() => window.pharosNavigate("/settings/projects"));
  assert.equal(await page.getByLabel("Color for New project", { exact: true }).isVisible(), true);
});

test("clipped labels reveal hidden text and stop repeating it when they fit", { skip }, async t => {
  const names = ["repository/shared-prefix-branch-name/feature-one", "repository/shared-prefix-branch-name/feature-two"];
  const {page} = await open(t, {url: "/cards?cards=library&card_search=human-repository-words-trend", ready: ".query-chart-legend", projectColors: [{ ...projectColorFixture[0], name: names[0] }],
    api: {"* /api/query/:dataset/:operation": call => {
      const response = defaultApi["* /api/query/:dataset/:operation"](call);
      if (!response?.metrics) return response;
      return {...response, metrics: response.metrics.map(metric => {
        const clause = call.body?.metrics?.find(clause => clause.id === metric.id);
        if (!clause?.groupBy?.includes("project_name") || !clause.groupBy.includes("day")) return metric;
        const days = [...new Set(metric.buckets.map(bucket => bucket.keys[clause.groupBy.indexOf("day")]))];
        const buckets = days.flatMap(day => names.map(name => ({keys: clause.groupBy.map(field => field === "day" ? day : name), value: 42, count: 1})));
        return {...metric, buckets, groupCount: buckets.length};
      })};
    }},
  });
  const legend = page.locator(".query-chart-legend").first();
  await legend.evaluate(element => {element.closest('.query-card').style.transform = 'rotate(2deg)';});
  await legend.evaluate(element => {element.style.width = "160px";});
  for (const name of names) {
    const item = legend.locator("[data-chart-tooltip-label]").filter({hasText: name});
    await item.hover();
    await page.waitForFunction(name => document.querySelector('[role="tooltip"].chart-tooltip:not([hidden])')?.textContent.includes(name), name);
    await page.locator('[role="tooltip"] .chart-tooltip-swatch').first().waitFor({ state: "visible" });
  }
  await page.locator(".query-cover-columns > [data-chart-tooltip]").first().hover();
  await page.locator('[role="tooltip"] .chart-tooltip-swatch').first().waitFor({ state: "visible" });
  await legend.evaluate(element => {element.style.width = "100%";element.style.gridTemplateColumns = "1fr";});
  await legend.locator("[data-chart-tooltip-label]").first().hover();
  await page.waitForFunction(() => !document.querySelector('[role="tooltip"].chart-tooltip:not([hidden])'));

  // Classic labels, session names and inline work-preview parts use the same
  // opt-in runtime; exercise single-line, ancestor and multiline clipping.
  await page.evaluate(() => {
    const host = document.createElement("div");host.id = "overflow-fixture";
    host.style.cssText = "position:fixed;top:120px;left:10px;width:90px;z-index:20000;background:white";
    host.innerHTML = '<div style="overflow:hidden;text-overflow:ellipsis;white-space:nowrap"><span data-overflow-title>Repository with a hidden branch suffix</span></div><div data-overflow-title style="display:-webkit-box;-webkit-line-clamp:2;-webkit-box-orient:vertical;overflow:hidden">A session title that wraps across several lines and has a hidden final suffix</div>';
    document.body.append(host);
  });
  const labels = page.locator("#overflow-fixture [data-overflow-title]");
  for (const label of await labels.all()) {
    await label.hover({position: {x: 4, y: 4}});
    assert.equal(await label.getAttribute("title"), await label.textContent(), "clipped text is available on hover");
  }
  // Resize while still hovered: the last label now fits, so help disappears.
  await page.locator("#overflow-fixture").evaluate(element => {element.style.width = "1200px";});
  await page.waitForFunction(() => !document.querySelector("#overflow-fixture > [data-overflow-title]").hasAttribute("title"));
  for (const label of await labels.all()) {
    await label.hover({position: {x: 4, y: 4}});
    assert.equal(await label.getAttribute("title"), null);
  }
});

test("Card Library timeline ticks fit full dollar amounts", { skip }, async t => {
  let suppliedLargeCosts = false;
  const app = await open(t, {
    url: "/cards?cards=library&card_search=cumulative-api-cost&card_window=30",
    ready: ".query-timeline-ticks > span",
    api: { "* /api/query/:dataset/:operation": call => {
      const response = defaultApi["* /api/query/:dataset/:operation"](call);
      if (call.params.dataset !== "usage" || !response?.metrics) return response;
      const cost = call.body?.metrics?.find(metric => metric.field === "cost_usd" && metric.groupBy?.includes("day"));
      if (!cost) return response;
      suppliedLargeCosts = response.metrics.some(metric => metric.id === cost.id && metric.buckets.length);
      return { ...response, metrics: response.metrics.map(metric => metric.id !== cost.id ? metric : {
        ...metric, buckets: metric.buckets.map(bucket => ({ ...bucket, value: 1_250_000 / metric.buckets.length })),
      }) };
    } },
  });
  assert.ok(suppliedLargeCosts, "the fixture supplied six- and seven-figure cost ticks");
  const chart = app.page.locator('[data-card-id="cumulative-api-cost"] .query-cover-timeline');
  await chart.waitFor();
  for (const width of [1440, 320]) {
    await app.page.setViewportSize({ width, height: 900 });
    await chart.scrollIntoViewIfNeeded();
    const problems = await chart.evaluate(chart => {
      const clip = chart.closest(".query-card-open").getBoundingClientRect();
      const plot = chart.querySelector(".query-cover-columns").getBoundingClientRect();
      const dates = chart.querySelector(".query-timeline-dates > span").getBoundingClientRect();
      const ticks = [...chart.querySelectorAll(".query-timeline-ticks > span")].map(tick => tick.getBoundingClientRect()).sort((a, b) => a.top - b.top);
      const problems = [];
      for (const tick of ticks) {
        if (tick.left < clip.left || tick.right > clip.right) problems.push("a dollar tick is clipped by its card");
        if (tick.right > plot.left) problems.push("a dollar tick overlaps the plot");
      }
      if (ticks.some((tick, index) => index && tick.top < ticks[index - 1].bottom)) problems.push("dollar ticks overlap each other");
      if (Math.abs(dates.left - plot.left) > 1) problems.push("dates do not align with the plot");
      if (plot.width <= 0) problems.push("the gutter leaves no room for the plot");
      return problems;
    });
    assert.deepEqual(problems, [], `timeline at ${width}px`);
    assert.deepEqual(await layoutProblems(app.page), []);
    await app.shot(`full-dollar-timeline-${width}`);
  }
});

// IndexedDB and painting saved charts during navigation require a real browser.
test("Card Library restores saved covers while fresh queries are pending", { skip }, async t => {
  let hold = false, release, requested;
  const pending = new Promise(resolve => { requested = resolve; });
  const gate = new Promise(resolve => { release = resolve; });
  t.after(() => release());
  const app = await open(t, {
    url: "/cards?cards=library&card_search=tool-call-share",
    ready: ".card-library-entry .query-card",
    api: { "* /api/query/:dataset/:operation": async call => {
      if (hold && ["metrics", "query-v3", "rows-v2"].includes(call.params.operation)) { requested(); await gate; }
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
  await pending;
  // The saved chart remains painted and usable while its request is held.
  // Freshness describes data age; it no longer doubles as a refresh spinner.
  await app.page.locator(".query-cover:not([aria-busy=true])").first().waitFor();
  const catalog = app.page.locator("#card-library");
  await catalog.scrollIntoViewIfNeeded();
  await app.shot("saved-covers-refreshing");
  release(); await app.settled();
  assert.deepEqual(app.problems(), []);
});


// Card links open full dashboards: one round trip into the supporting query
// and back, then a view change that survives reload.
test("Card dashboard opens from the Card Library, fits, and supports changing views and reloading", { skip }, async t => {
  const app = await open(t, { url: '/cards?card_search=error-signatures&card_topic=all&card_window=7', ready: '.query-card-open' });
  const { page } = app;
  await page.locator('.query-card-open').first().click();
  const view = page.getByRole('combobox', { name: 'Analysis', exact: true });
  await view.waitFor();
  assert.equal(new URL(page.url()).searchParams.get('analyze_topic'), 'calls');
  await page.locator('.usage-query-destinations a').first().waitFor();
  await app.settled();
  assert.equal(await page.getByRole('alert').count(), 0, 'the matching analysis is available');
  assert.deepEqual(await layoutProblems(page), []);
  for (const width of [1000, 390, 1440]) {
    await page.setViewportSize({ width, height: 900 });
    await app.settled();
    await page.getByRole("navigation", { name: "Explore this dashboard" }).scrollIntoViewIfNeeded();
    assert.deepEqual(await layoutProblems(page), []);
    await app.shot(`dashboard-query-cards-${width}`);
  }
  const contributor = page.getByRole("navigation", { name: "Explore this dashboard" }).getByRole("link").first();
  await contributor.focus();
  await page.keyboard.press("Enter");
  await page.locator('.usage-workspace-table').waitFor();
  await page.getByRole('link', { name: /^Back to dashboard/ }).click();
  await page.locator('.usage-dashboard .usage-query-destinations a').first().waitFor();
  await app.settled();
  assert.deepEqual(await layoutProblems(page), []);
  await view.selectOption('trend');
  await page.waitForFunction(() => JSON.parse(new URLSearchParams(location.search).get('analyze_chart')).view === 'trend');
  await page.reload();
  await page.getByRole('combobox', { name: 'Analysis', exact: true }).waitFor();
  assert.equal(await page.getByRole('combobox', { name: 'Analysis', exact: true }).inputValue(), 'trend');
  await app.settled();
  assert.deepEqual(app.problems(), []);
});


const oldActivitySource = { ...EMPTY_QUERY, where: [
  { field: "provider", op: "=", value: "codex" },
  { field: "last_at", op: ">=", value: "2026-10-01T15:00:00.000Z" },
  { field: "last_at", op: "<", value: "+0s" },
] };
function oldActivityCard(token) {
  return `/analyze?${new URLSearchParams({ analyze_mode: "dashboards", analyze_topic: "human", analyze_data: "writing_messages",
    analyze_chart: JSON.stringify({ view: "trend", metric: "words" }), analyze_filters: encodeQuery(EMPTY_QUERY),
    card_id: "session-human-output", card_query: "scatter", q_scatter: token,
    window_mode: "today", window_days: "1", window_dataset: "scatter", window_field: "last_at", window_timezone: "Asia/Tokyo",
  })}`;
}
// The durable source is the harder path; the inline mapping is covered by
// unit/cards/card-recipes.
test("Older card dashboard restores durable sources before rendering", { skip }, async t => {
  const code = "eeeeeeeeeeeeeeeeeeeeee";
  const app = await open(t, { url: oldActivityCard(`@${code}`), ready: '.usage-query-destinations a',
    api: { "GET /api/query-snapshots": { version: 1, code, token: encodeQuery(oldActivitySource), createdAt: 0 } },
  });
  const { page } = app;
  await page.getByRole('heading', { name: 'Scatter Plots', exact: true }).waitFor();
  assert.equal(await page.getByRole('combobox', { name: 'X axis', exact: true }).inputValue(), 'Human messages');
  const params = new URL(page.url()).searchParams;
  assert.equal(params.get('analyze_topic'), 'scatter');
  assert.equal(params.get('analyze_window'), 'today');
  assert.equal(params.get('analyze_timezone'), 'Asia/Tokyo');
  assert.ok(app.calls.filter(call => call.method === 'POST' && call.path.startsWith('/api/query/')).every(call => !call.path.startsWith('/api/query/writing_messages/')), 'the old dashboard never executes');
  await page.locator('.usage-query-destinations a').first().click();
  await page.locator('.usage-workspace-table').waitFor();
  assert.equal(new URL(page.url()).searchParams.get('window_timezone'), 'Asia/Tokyo');
  await page.getByRole('link', { name: /^Back to dashboard/ }).click();
  await page.locator('.usage-query-destinations a').first().waitFor();
  await page.reload();
  await page.getByRole('combobox', { name: 'X axis', exact: true }).waitFor();
  assert.equal(await page.getByRole('combobox', { name: 'X axis', exact: true }).inputValue(), 'Human messages');
  await app.settled();
});

test('Tokens and Cost keeps missing pricing actionable across refresh, chart views and reload', { skip }, async t => {
  let pricing = defaultApi['GET /api/pricing'];
  const app = await open(t, { url: '/analyze?analyze_topic=tokens', ready: '.usage-query-destinations a',
    api: { 'GET /api/pricing': () => pricing } });
  const { page, settled } = app;
  const notice = page.getByRole('region', { name: 'Missing model prices' });
  await settled();
  assert.equal(await notice.count(), 0);
  pricing = missingPrices;
  const refresh = page.getByRole('button', { name: 'Refresh', exact: true });
  await refresh.focus();
  await refresh.press('Enter');
  await notice.waitFor();
  assert.ok(await refresh.evaluate(element => document.activeElement === element), 'loading pricing moved focus');
  await page.evaluate(() => { window.pharosCopyText = async text => { window.copiedPricingPrompt = text; }; });
  await notice.getByRole('button', { name: 'Copy refresh prices prompt' }).click();
  await notice.getByRole('status').waitFor();
  assert.equal(await page.evaluate(() => window.copiedPricingPrompt), missingPrices.prompt);
  await notice.locator('summary').click();
  const prompt = page.getByRole('textbox', { name: 'Pricing refresh prompt' });
  assert.equal(await prompt.inputValue(), missingPrices.prompt);
  await page.waitForFunction(() => JSON.parse(new URLSearchParams(location.search).get('disclosures') || '{}')['tokens.pricing-prompt'] === true);
  await page.reload();
  await prompt.waitFor();
  assert.equal(await prompt.inputValue(), missingPrices.prompt, 'the expanded prompt restores after reload');
  await page.getByRole('combobox', { name: 'Analysis', exact: true }).selectOption('compare');
  await page.locator('.usage-analysis-dashboard[aria-busy="false"]').waitFor();
  assert.ok(await notice.isVisible(), 'comparison views keep the pricing notice');
  pricing = defaultApi['GET /api/pricing'];
  await refresh.click();
  await notice.waitFor({ state: 'hidden' });
  await settled();
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
  // The Mac app's context menu reads the resolved path for Open With.
  assert.equal(await source.getAttribute("data-file-path"),"/workspace/internal/archive/api.go");
  // A confirmed file opens in its Mac app instead of loading in the browser.
  await source.click();
  await page.waitForFunction(()=>document.querySelector('#feedbackToast.visible'));
  assert.ok(app.calls.some(call=>call.method==='POST'&&call.path==='/api/markdown-link'&&call.query.get('href')==='internal/archive/api.go:12'));
  assert.equal(page.context().pages().length,1);
  assert.equal(await page.getByRole("link",{name:"Running dev server",exact:true}).getAttribute("href"),"http://localhost:5173/");
  for(const name of ["Missing file","Stopped dev server"]){
    const link=page.getByRole("link",{name,exact:true});
    assert.equal(await link.getAttribute("aria-disabled"),"true");
    assert.equal(await link.getAttribute("href"),null);
    await link.focus();await page.keyboard.press("Enter");
    assert.equal(new URL(page.url()).pathname,`${work}/conversation/${rootConversation}`);
  }
  assert.ok(app.calls.some(call=>call.path==='/api/markdown-link'&&call.query.get('workspace')===workID),"checks resolve paths in the conversation's workspace");
  for(const [name,href] of [["Library view","/library?view=conversation"],["Filtered usage","/analyze?analyze_topic=tokens"]]){
    const link=page.getByRole("link",{name,exact:true});
    assert.equal(await link.getAttribute("href"),href);
    assert.equal(await link.getAttribute("aria-disabled"),null);
  }
  await app.shot("link-states");
  await page.getByRole("link",{name:"Library view",exact:true}).click();
  await page.waitForURL(url=>url.pathname==='/library');
  await page.locator('.conversation-result-card').first().waitFor();
});

test('Scatter card opens the standalone dashboard with its selected axes and source query',{skip},async t=>{
 const app=await open(t,{url:'/cards?card_search=human-words-api-cost&card_topic=all&card_window=7',ready:'.query-card-open'}),{page}=app;
 await page.locator('.query-card-open').first().click();
 await page.getByRole('combobox',{name:'X axis',exact:true}).waitFor();
 assert.equal(new URL(page.url()).searchParams.get('analyze_topic'),'scatter');
 assert.equal(await page.getByRole('combobox',{name:'X axis',exact:true}).inputValue(),'Typed words');
 assert.equal(await page.getByRole('combobox',{name:'Y axis',exact:true}).inputValue(),'API cost (USD)');
 await page.locator('.usage-query-destinations a').first().click();
 await page.locator('.usage-workspace-table').waitFor();
 assert.equal(new URL(page.url()).searchParams.get('analyze_data'),'scatter');
 await page.getByRole('link',{name:/^Back to dashboard/}).click();
 await page.getByRole('combobox',{name:'X axis',exact:true}).waitFor();
 await app.settled();
});


test("model color editors save inherited palettes, retain focus, and restore provider disclosures", { skip }, async t => {
  let inventoryReads = 0;
  const { page, shot } = await open(t, { url: "/settings/projects", ready: ".project-color-settings", api: {
    "GET /api/model-colors": call => { if (call.query.get("inventory") === "1") inventoryReads++; return defaultApi["GET /api/model-colors"](call); },
  } });
  assert.equal(inventoryReads, 0, "other settings pages leave model discovery idle");
  await page.evaluate(() => window.pharosNavigate("/settings/models"));
  await page.getByLabel("Color for gpt-local", { exact: true }).waitFor({ state: "attached" });
  const provider = page.getByLabel("Color for OpenAI", { exact: true });
  await provider.focus();
  await page.evaluate(() => window.dispatchEvent(new Event("pharos:usage-refresh")));
  assert.equal(await provider.evaluate(element => element === document.activeElement), true);
  await provider.fill("#7547b3");
  await page.getByRole("button", { name: "Save color for OpenAI", exact: true }).click();
  await page.waitForFunction(() => document.querySelector('[aria-label="Color for Codex"]')?.value === "#7547b3");
  const card = page.getByRole("region", { name: "OpenAI colors", exact: true });
  await card.locator("summary").first().click();
  const model = page.getByLabel("Color for gpt-6-sol", { exact: true });
  await model.waitFor({ state: "visible" });
  await model.focus();
  await model.fill("#ee1122");
  assert.equal(await model.evaluate(element => element === document.activeElement), true);
  await page.getByRole("button", { name: "Save color for gpt-6-sol", exact: true }).click();
  await page.waitForFunction(() => document.querySelector('[aria-label="Restore recommended shade for gpt-6-sol"]')?.disabled === false);
  await page.reload();
  await model.waitFor({ state: "visible" });
  await page.waitForFunction(() => document.querySelector('[aria-label="Color for gpt-6-sol"]')?.value === "#ee1122");
  assert.equal(await model.inputValue(), "#ee1122");
  await page.getByRole("button", { name: "Restore brand color for OpenAI", exact: true }).click();
  await page.waitForFunction(() => document.querySelector('[aria-label="Color for Codex"]')?.value === "#10a37f");
  assert.equal(await model.inputValue(), "#ee1122", "provider reset preserves the model choice");
  await page.getByRole("button", { name: "Restore recommended shade for gpt-6-sol", exact: true }).click();
  await page.waitForFunction(() => document.querySelector('[aria-label="Color for gpt-6-sol"]')?.value !== "#ee1122");
  await card.locator("summary").first().click();
  await page.evaluate(() => window.scrollTo({ top: 0 }));
  await shot("model-provider-colors");
});
