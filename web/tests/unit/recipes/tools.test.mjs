import test from "node:test";
import assert from "node:assert/strict";
import { cardCoverQuery, core, instantiateRecipe, recipeHarness, schemas } from "../helpers/recipes.mjs";
import { filterBoxSamples, boxDetails, sharedBoxScale } from "../../../src/compact-box-model";
import { compactPieSlices } from "../../../src/compact-pie-model";
const now = Date.parse("2026-10-15T12:00:00Z"), { card, table: destination, metric } = recipeHarness(now);
const call = (id, extra = {}) => ({ id, started_at: now - 1000, ended_at: now - 500, tool_name: "Bash", tool_category: "command", command_name: "npm test", repository_name: "repo", conversation_id: "one", duration_ms: 1000, duration_source: "reported", provider: "codex", call_count: 1, error_count: 0, status: "ok", ...extra });

test("Pharos request sources count captured requests by caller origin, including failures and no-result calls", () => {
  const rows = [
    call("claude",{mcp_server:"pharos",mcp_method:"search_messages",source_kind:"conductor",provider:"claude",status:"error"}),
    call("codex",{mcp_server:"pharos",mcp_method:"search_messages",source_kind:"conductor",provider:"codex",status:"no_result"}),
    call("standalone",{mcp_server:"pharos",mcp_method:"get_work_detail",source_kind:"claude",provider:"claude"}),
    call("other",{mcp_server:"other",mcp_method:"search_messages",source_kind:"conductor",provider:"codex"}),
    call("blank",{mcp_server:"pharos",mcp_method:""}),
  ];
  const table=destination("pharos-mcp-sources",rows);
  assert.equal(table.length,3);
  assert.equal(table.reduce((n,row)=>n+row.tf_calls,0),3);
  assert.deepEqual(table.map(row=>`${row.source_kind}/${row.provider}`).sort(),["claude/claude","conductor/claude","conductor/codex"]);
});

test("MCP duration boxes use positive completed server calls and retain sparse tool cohorts in the destination", () => {
  const rows=[0,1,100,10000,null,-1].map((duration_ms,i)=>({id:i,called_at:now-1000,tool_name:"search_messages",duration_ms,status:i%2?"error":"ok"}));
  rows.push({id:7,called_at:now-1000,tool_name:"sparse",duration_ms:1},{id:8,called_at:now-1000,tool_name:"sparse",duration_ms:2});
  const table=destination("pharos-mcp-durations",rows), result=filterBoxSamples(metric("pharos-mcp-durations",rows),3);
  assert.equal(table.length,2);
  assert.equal(result.buckets.length,1);
  assert.deepEqual(result.buckets[0].keys,["search_messages"]);
  const summary=result.buckets[0].distribution.summary;
  assert.deepEqual([summary.n,summary.min,summary.median,summary.max],[3,1,100,10000]);
  assert.equal(card("pharos-mcp-durations").recipe.cover.mode,"source");
});

test("queried-source counts use server request filters and retain unspecified requests in the denominator", () => {
  const rows=["claude","codex",null,null].map((query_source,i)=>({id:i,called_at:now-1000,tool_name:"search_messages",query_source,status:i%2?"error":"ok"}));
  const table=destination("pharos-mcp-query-sources",rows);
  assert.equal(table.length,3);
  assert.equal(table.reduce((sum,row)=>sum+row.tf_calls,0),4);
  assert.equal(table.find(row=>row.query_source===null).tf_calls,2);
  assert.equal(card("pharos-mcp-query-sources").recipe.dataset,"mcp_calls");
  assert.equal(card("pharos-mcp-sources").recipe.dataset,"tool_calls");
});

test("MCP payload windows include older retained data only when the selected rolling window includes it", () => {
  const rows=[{id:1,called_at:now-8*86400000,tool_name:"search_messages",response_bytes:900,call_count:1,error_count:0,truncated_count:0}];
  const {recipe}=card("mcp-response-size");
  assert.deepEqual(recipe.days,[30,90]);
  assert.equal(destination("mcp-response-size",rows).length,0);
  const query=instantiateRecipe(recipe,30,new Date(now)).query;
  assert.equal(core.applyQuery(rows,query,schemas.mcp_calls,{now}).rows[0].card_response_bytes,900);
  const cover=cardCoverQuery(query,recipe);
  const result=core.evaluateMetrics(rows,cover.query,schemas.mcp_calls,{now}).metrics.find(m=>m.id===cover.metricId);
  assert.equal(result.buckets[0].distribution.summary.median,900);
  assert.equal(result.buckets[0].distribution.summary.n,1);
});

test("payload boxes use individual response sizes, retain zeros and errors, and keep sparse cohorts inspectable", () => {
  const rows=[0,1024,1048576,null,-1].map((response_bytes,i)=>({id:i,called_at:now-1000,tool_name:"search_work",response_bytes,error_count:i===1?1:0,truncated_count:0}));
  rows.push({id:8,called_at:now-1000,tool_name:"sparse",response_bytes:2000000},{id:9,called_at:now-1000,tool_name:"sparse",response_bytes:3000000});
  const table=destination("mcp-response-size",rows), result=filterBoxSamples(metric("mcp-response-size",rows),3), summary=result.buckets[0].distribution.summary;
  assert.equal(table.length,2);
  assert.equal(result.buckets.length,1);
  assert.deepEqual([summary.n,summary.min,summary.median,summary.max],[3,0,1024,1048576]);
  const search=table.find(row=>row.tool_name==="search_work");
  assert.equal(search.card_payload_median,1024);
  assert.equal(search.card_response_bytes,1049600);
  assert.equal(search.card_count,3);
  assert.equal(search.card_error_count,1);
  assert.equal(sharedBoxScale(result.buckets,6,"log").log,false,"zero-byte responses stay visible");
  const {recipe,query}=card("mcp-response-size"), cover=cardCoverQuery(query,recipe);
  assert.equal(recipe.cover.mode,"source");
  assert.equal(cover.query.aggregations.find(m=>m.id===cover.metricId).groupLimit,10000);
  const saved=cardCoverQuery(query,recipe,{mode:"source",metricId:"mcp-bytes",kind:"bar-horizontal"});
  assert.equal(saved.metricId,"mcp-bytes");
  assert.equal(saved.query.aggregations.find(m=>m.id==="mcp-bytes").op,"sum");
  assert.match(boxDetails("search_work",summary,"bytes"),/Median: 1 KiB \(1,024 bytes\)/);
  assert.match(boxDetails("search_work",summary,"bytes"),/Max: 1 MiB \(1,048,576 bytes\)/);
});

test("tool mix includes error and no-result calls and preserves exact Other beyond twelve tools", () => {
  const rows = Array.from({length:20},(_,i)=>call(String(i),{tool_name:`tool-${i}`,status:i%2?"no_result":"error",error_count:i%2?0:1}));
  rows.push(call("old",{started_at:now-8*86400000}));
  const pie=compactPieSlices(metric("tool-call-share",rows),5);
  assert.equal(pie.total,20);
  assert.equal(pie.slices.length,6);
  assert.equal(pie.slices.at(-1).value,15);
  assert.equal(pie.slices.reduce((n,s)=>n+s.value,0),20);
});

test("calls-per-conversation boxes aggregate before sampling and retain sparse repositories in the destination", () => {
  const rows=[1,10,100].flatMap((n,c)=>Array.from({length:n},(_,i)=>call(`${c}-${i}`,{conversation_id:`c-${c}`,status:i%2?"error":"no_result"})));
  rows.push(call("sparse-a",{repository_name:"sparse",conversation_id:"c-0"}),call("sparse-b",{repository_name:"sparse",conversation_id:"c-1"}),call("missing",{conversation_id:null}),call("blank",{conversation_id:""}));
  const table=destination("tool-calls-per-conversation",rows), result=filterBoxSamples(metric("tool-calls-per-conversation",rows),3);
  assert.equal(table.length,5);
  assert.equal(result.buckets.length,1);
  const summary=result.buckets[0].distribution.summary;
  assert.deepEqual([summary.n,summary.min,summary.median,summary.max],[3,1,10,100]);
  assert.equal(table.filter(row=>row.repository_name==="repo").reduce((sum,row)=>sum+row.tf_calls,0),111);
  assert.equal(card("tool-calls-per-conversation").recipe.exploration.coverMetric.groupLimit,10000);
});

test("command duration boxes separate timing sources and apply cutoffs after positive-duration selection", () => {
  const rows=[100,1000,10000,0,null,-1].map((duration_ms,i)=>call(String(i),{duration_ms}));
  rows.push(call("derived-a",{duration_source:"timestamps",duration_ms:100}),call("derived-b",{duration_source:"timestamps",duration_ms:200}),call("blank",{command_name:""}),call("missing",{command_name:null}));
  const table=destination("duration-distribution",rows), result=filterBoxSamples(metric("duration-distribution",rows),3);
  assert.equal(table.length,2);
  assert.equal(result.buckets.length,1);
  assert.deepEqual(result.buckets[0].keys,["npm test","reported"]);
  assert.equal(result.buckets[0].distribution.summary.n,3);
  assert.equal(result.buckets[0].distribution.summary.median,1000);
  const {recipe,query}=card("duration-distribution"), cover=cardCoverQuery(query,recipe), selected=cover.query.aggregations.find(m=>m.id===cover.metricId);
  assert.equal(selected.groupLimit,10000);
  assert.equal(selected.display.xScale.mode,"log");
  assert.equal(selected.display.format.sourceUnit,"milliseconds");
  assert.equal(recipe.cover.minSamples,3);
});

test("agent-side MCP keys separate servers while Pharos payloads keep their own measured-size population", () => {
  const rows=[call("a",{mcp_server:"one",mcp_method:"search"}),call("b",{mcp_server:"two",mcp_method:"search"}),call("missing",{mcp_server:"one",mcp_method:null}),call("blank",{mcp_method:""})];
  const methods=destination("mcp-methods",rows);
  assert.equal(methods.length,2);
  assert.deepEqual(methods.map(row=>row.mcp_server).sort(),["one","two"]);
  const facts=[100,200,0,null,-1].map((response_bytes,i)=>({id:String(i),called_at:now-1000,tool_name:"search_work",response_bytes,call_count:1,error_count:i===1?1:0,truncated_count:0}));
  const payload=destination("mcp-response-size",facts)[0];
  assert.equal(payload.card_response_bytes,300);
  assert.equal(payload.card_count,3);
  assert.equal(payload.card_error_count,1);
});
