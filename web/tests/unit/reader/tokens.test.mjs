// Token accounting and context samples in the conversation reader
// (reader-model.js). The fixture test is the JS half of Go's
// TestTokenUsageFixtures; both read testdata/token-usage-fixtures.json.
import { test } from "node:test";
import assert from "node:assert/strict";
import { plain } from "../helpers/classic-script.mjs";
import { event, human, reader, render, spend } from "../helpers/transcript.mjs";
import tokenCases from "../../../../testdata/token-usage-fixtures.json";

const { conversationTokenReports, contextTokenReports, contextWindow, contextDelta, contextScale, contextProfile, tokenTotal } = reader;
const raw = events => events.map(value => ({ text: JSON.stringify(value) }));
const samples = events => [...events].map(message => message.tokenReport?.contextSample).filter(Boolean);

test("usage sensitivity diagnostics scope to owned messages; canonical estimates use token reports", () => {
  const report = {range_kind:"sensitivity_not_confidence",manifest_revision:"fixture.1",responses:[
    {message_id:"a",output_tokens:{expected:1200,lower:400,upper:3000},local_weight:0},
    {message_id:"b",model:"claude-opus-fixture",effort:"high",output_tokens:{expected:800,lower:200,upper:2000},local_weight:.5},
  ]};
  const conversation = {usage_estimates:report};
  const all = reader.usageEstimatePresentation(conversation);
  assert.equal(all.expected,2000);
  assert.equal(all.lower,600);
  assert.equal(all.upper,5000);
  assert.match(all.detail,/Local coding calibration/);
  assert.match(all.detail,/unreported/);
  assert.equal(reader.usageEstimatePresentation(conversation,[{id:"a"}]).expected,1200);
  assert.equal(reader.usageEstimatePresentation(conversation,[{id:"different"}]),null);
  assert.equal(reader.usageEstimatePresentation({}),null);
  assert.equal(reader.usageEstimatePresentation({usage_estimates:{...report,range_kind:"confidence"}}),null);
  const messages = [event("answer","message","Hello",0,{id:"a",duration_estimate:{status:"estimated",seconds:{expected:3,lower:1,upper:10}}})];
  assert.equal(spend(render(messages).events),null,"estimates must not enter token accounting");
  const estimatedMessages = [{...messages[0],usage_is_estimate:true,usage:{output_tokens:1200,reasoning_output_tokens:200,total_tokens:1200}}];
  const estimates = conversationTokenReports(estimatedMessages);
  assert.equal(estimates[0].total,1200);
  assert.equal(estimates[0].isEstimate,true);
  assert.equal(contextTokenReports(estimatedMessages,estimates)[0].contextSample,undefined,"estimated output is not measured input context");
  assert.match(reader.toolDurationEstimatePresentation(messages[0]).label,/Estimated/);
  assert.match(reader.toolDurationEstimatePresentation({duration_estimate:{status:"observed",seconds:{expected:0,lower:0,upper:0}}}).label,/Recorded/);
  assert.equal(reader.toolDurationEstimatePresentation({duration_estimate:{status:"unavailable"}}),null);
  const split = reader.toolDurationEstimatePresentation({duration_estimate:{status:"estimated",seconds:{expected:6,lower:5,upper:10},argument_generation_seconds:{expected:5,lower:5,upper:5},argument_generation_status:"observed",execution_seconds:{expected:1,lower:0,upper:5},execution_status:"estimated"}});
  assert.match(split.label,/Estimated execution/);
  assert.match(split.detail,/Argument generation: recorded 5 s/);
  assert.match(split.detail,/Full call-to-result span: 6 s/);
  const zero = reader.toolDurationEstimatePresentation({duration_estimate:{status:"observed",seconds:{expected:5,lower:5,upper:5},execution_seconds:{expected:0,lower:0,upper:0},execution_status:"observed"}});
  assert.match(zero.label,/Recorded execution: 0 s/);
});

test("all source accounting formats reconcile against shared fixtures", () => {
  assert.ok(tokenCases.length > 0);
  for (const item of tokenCases) {
    // Fixture events are protocol accounting envelopes, stored as metadata.
    const reports = conversationTokenReports(item.events.map((value, i) => ({ native_id: String(i), kind: "metadata", role: "system", text: JSON.stringify(value) })));
    assert.equal(reports.reduce((sum, report) => sum + (report?.total || 0), 0), item.total, item.name);
  }
});

test("thread checkpoints count increments once per turn", () => {
  const usage = (id, total) => event(id, "metadata", { type: "token_count", info: { total_token_usage: { total_tokens: total }, last_token_usage: { input_tokens: total, output_tokens: 0 } } });
  const { events, graph } = render([human(), usage("u1", 12000), usage("duplicate", 12000),
    event("h2", "message", "Continue", 1, { role: "user" }), usage("u2", 150000),
    event("compact", "metadata", { type: "compacted", payload: { summary: "Context reduced" } })]);
  assert.deepEqual([...graph.turns].map(turn => spend(turn.owned)), [12000, 138000]);
  assert.deepEqual(samples(events).map(sample => sample.compact ? "compaction" : sample.delta), [null, 0, 138000, "compaction"]);
});

test("turn and sub-agent spend include nested usage across many turns", () => {
  const messages = [];
  for (let i = 0; i < 45; i++) messages.push(event("h" + i, "message", "Turn " + i, i, { role: "user" }), event("u" + i, "message", { message: { id: "r" + i, usage: { input_tokens: 100 + i, output_tokens: 10 }, content: [{ type: "text", text: "Reply" }] } }, i));
  messages.push(event("agent", "delegation", { tool: "Task", input: { description: "Review" } }, 46, { call_id: "child" }),
    event("child-request", "message", { message: { id: "child-r", usage: { input_tokens: 5000, output_tokens: 100 }, content: [{ type: "text", text: "Child reply" }] } }, 47, { parent_native_id: "child" }),
    event("compact", "compaction", { type: "compacted" }, 48));
  const { events, graph, context } = render(messages), turns = [...graph.turns];
  assert.equal(turns.length, 45);
  assert.deepEqual(turns.slice(0, 2).map(turn => spend(turn.owned)), [110, 111]);
  assert.equal(spend(turns.at(-1).owned), 5254, "the last turn includes its sub-agent");
  // A sub-agent's badge counts what it owns, without its own call and results.
  const agent = [...events].find(message => message.kind === "delegation");
  assert.equal(spend([...reader.ownedMessages([agent], context)].filter(message => message !== agent)), 5100);
  const sampled = samples(events);
  assert.deepEqual([sampled.filter(sample => !sample.compact).length, sampled.filter(sample => sample.compact).length], [46, 1]);
  assert.deepEqual([...new Set(sampled.map(sample => sample.stream))], ["main", "child"]);
});

test("an advisor's request leaves the conversation's context alone", () => {
  const reply = (id, input) => event(id, "metadata", { type: "assistant", message: { id, usage: { input_tokens: input, output_tokens: 10 } } });
  const advisor = event("advisor", "metadata", { type: "assistant", advisor_of: "r1", message: { id: "r1:advisor:0", usage: { input_tokens: 50, output_tokens: 5 } } });
  const { events } = render([human(), reply("r1", 1000), advisor, reply("r2", 1020)]);
  assert.deepEqual(plain(samples(events).map(sample => sample.size)), [1000, 1020]);
});

test("split message usage includes cache once and reconciles result totals", () => {
  const message = { id: "request-1", usage: { input_tokens: 100, output_tokens: 20, cache_read_input_tokens: 1000, cache_creation_input_tokens: 200 }, content: [{ type: "text", text: "Reply" }, { type: "thinking", thinking: "Thought" }] };
  const { events, graph } = render([human(), event("reply", "message", { message }), event("retry", "message", { message: { ...message, usage: { ...message.usage, output_tokens: 30 } } }),
    event("result", "metadata", { type: "result", usage: { total_tokens: 1330 } })]);
  assert.equal(graph.turns.length, 1);
  assert.equal(spend(graph.turns[0].owned), 1330);
  // The retry supersedes the first copy of the request: one context sample.
  assert.deepEqual(plain(samples(events).map(sample => sample.size)), [1300]);
  assert.equal(tokenTotal({ input_tokens: 100, output_tokens: 20, cached_input_tokens: 80, reasoning_output_tokens: 10 }), 120);
});

test("context profiles isolate streams, reset at compaction, and never use spend totals", () => {
  const events = raw([
    { type: "assistant", message: { id: "a", usage: { input_tokens: 100, cache_read_input_tokens: 900, output_tokens: 30 } } },
    { type: "assistant", message: { id: "b", usage: { input_tokens: 100, cache_read_input_tokens: 1000, output_tokens: 40 } } },
    { type: "assistant", parent_tool_use_id: "child", message: { id: "c", usage: { input_tokens: 50, output_tokens: 20 } } },
    { type: "assistant", message: { id: "d", usage: { input_tokens: 100, cache_read_input_tokens: 1200, output_tokens: 50 } } },
    { type: "compacted" },
    { type: "assistant", message: { id: "e", usage: { input_tokens: 300, output_tokens: 10 } } },
    { type: "result", usage: { total_tokens: 90000 } },
    { type: "assistant", parent_tool_use_id: "child", message: { id: "f", usage: { input_tokens: 150, output_tokens: 20 } } },
  ]);
  const reports = contextTokenReports(events, conversationTokenReports(events)), sampled = [...reports].map(report => report?.contextSample || null);
  assert.deepEqual(sampled.map(sample => sample?.size ?? null), [1000, 1100, 50, 1300, null, 300, null, 150]);
  assert.deepEqual(sampled.map(sample => sample?.delta ?? null), [null, 100, null, 200, null, null, null, 100]);
  // One entry holding the second and fourth requests shows their net change (+300), not their sizes.
  assert.equal(contextDelta([sampled[1], sampled[3]]), 300);
  assert.equal(contextDelta([sampled[0], sampled[1]]), null, "a stream's first sample has no known addition");
  assert.equal(contextDelta([sampled[1], sampled[7]]), null, "samples of two streams have no single change");
});

test("context rail widths are linear shares of the largest context when the window is unknown", () => {
  const request = (id, input) => event(id, "message", { message: { id, usage: { input_tokens: input, output_tokens: 10 }, content: [{ type: "text", text: id }] } });
  const { events } = render([human(), request("first", 1000), request("second", 2000), event("compact", "compaction", { type: "compacted" }), request("after", 500)]);
  const sampled = samples(events), { windows, scale } = contextScale(sampled);
  assert.equal(windows.get("main"), null);
  assert.deepEqual(plain(sampled.filter(sample => !sample.compact).map(sample => [sample.size, contextProfile(sample, scale).width])), [[1000, 50], [2000, 100], [500, 25]]);
  assert.deepEqual(plain(contextProfile(sampled.find(sample => sample.compact), scale)), { from: 100, width: 100 });
});

test("context rail widths are shares of the context window and compactions keep their trigger", () => {
  const claude = (id, input, seconds) => event(id, "message", { message: { id, model: "claude-opus-5-5", usage: { input_tokens: input, output_tokens: 10 }, content: [{ type: "text", text: id }] } }, seconds);
  const { events } = render([human(), claude("first", 50000, 1), claude("second", 100000, 2),
    event("manual", "metadata", { type: "system", subtype: "compact_boundary", compactMetadata: { trigger: "manual", preTokens: 100000 } }, 3),
    claude("third", 20000, 4), event("auto", "compaction", { type: "compacted", compaction_trigger: "auto" }, 5)]);
  const sampled = samples(events), { windows, scale } = contextScale(sampled);
  assert.equal(windows.get("main"), 200000);
  // Each profile runs from the context before the request to the context after it; compaction resets it.
  assert.deepEqual(plain(sampled.filter(sample => !sample.compact).map(sample => contextProfile(sample, scale))), [{ from: 25, width: 25 }, { from: 25, width: 50 }, { from: 10, width: 10 }]);
  assert.deepEqual(plain(sampled.filter(sample => sample.compact).map(sample => sample.trigger)), ["manual", "auto"]);
});

test("context windows come from Codex checkpoints or grow to 1M once Claude exceeds 200K", () => {
  const events = raw([
    { type: "event_msg", payload: { type: "token_count", info: { total_token_usage: { input_tokens: 10 }, model_context_window: 258400 } } },
    { type: "token_usage_record", payload: { response_id: "r", usage: { input_tokens: 1000, output_tokens: 5 }, thread_token_usage: { input_tokens: 1000, output_tokens: 5 } } },
  ]);
  const codex = [...contextTokenReports(events, conversationTokenReports(events))].map(report => report?.contextSample).filter(Boolean);
  assert.deepEqual({
    codex: contextWindow(codex),
    claude: contextWindow([{ model: "claude-sonnet-5", size: 150000 }]),
    large: contextWindow([{ model: "claude-sonnet-5", size: 150000 }, { model: "claude-sonnet-5", size: 250000 }]),
    other: contextWindow([{ model: "local", size: 5 }]),
  }, { codex: 258400, claude: 200000, large: 1000000, other: null });
});

test("nested subagent checkpoints count once and remain nested sessions", () => {
  const { graph } = render([human(),
    event("outer-call", "delegation", { tool: "spawn_agent", input: { task: "outer" } }, 1, { call_id: "outer" }),
    event("outer-request", "metadata", { type: "assistant", parent_tool_use_id: "outer", message: { id: "outer-request", usage: { total_tokens: 120 } } }, 2, { parent_native_id: "outer" }),
    event("inner-call", "delegation", { tool: "spawn_agent", input: { task: "inner" } }, 3, { call_id: "inner", parent_native_id: "outer" }),
    event("inner-request", "metadata", { type: "assistant", parent_tool_use_id: "inner", message: { id: "inner-request", usage: { total_tokens: 60 } } }, 4, { parent_native_id: "inner" }),
    event("inner-total", "metadata", { type: "system", subtype: "task_notification", tool_use_id: "inner", usage: { total_tokens: 80 } }, 5, { parent_native_id: "inner" }),
    event("outer-total", "metadata", { type: "system", subtype: "task_notification", tool_use_id: "outer", usage: { total_tokens: 250 } }, 6, { parent_native_id: "outer" }),
    event("result", "result", { type: "result", usage: { total_tokens: 300 } }, 7),
  ]);
  assert.equal(graph.turns.length, 1);
  assert.equal(spend(graph.turns[0].owned), 300);
  // The outer agent is a top-level event; the inner one is nested under it.
  assert.deepEqual(plain(graph.turns[0].events.map(message => message.native_id)), ["outer-call", "result"]);
  assert.deepEqual(plain(graph.childrenByParent.get("outer").map(message => message.native_id)), ["outer-request", "inner-call", "outer-total"]);
  assert.deepEqual(plain(graph.childrenByParent.get("inner").map(message => message.native_id)), ["inner-request", "inner-total"]);

  const launch=event("launch", "delegation", {tool:"Agent",input:{model:"sonnet"}}, 1, {call_id:"child"});
  const details={status:"completed",totalToolUseCount:0,totalTokens:110,usage:{input_tokens:5,cache_read_input_tokens:90,cache_creation_input_tokens:5,output_tokens:10}};
  const handback=changed=>event("handback", "delegation_result", {content:"Finished",details:{...details,...changed}}, 3, {call_id:"child"});
  const request=event("request", "metadata", {type:"assistant",message:{id:"request",usage:{input_tokens:200,output_tokens:20}}}, 2, {parent_native_id:"child"});
  for(const [name,messages,want] of [
    ["result only",[launch,handback()],110],
    ["result owned by retained child",[launch,{...handback(),usage_in_child_conversation:true}],0],
    ["child ownership preserves parent launch request",[event("parent","metadata",{type:"assistant",message:{id:"parent",usage:{input_tokens:100,output_tokens:10}}}),launch,{...handback(),usage_in_child_conversation:true}],110],
    ["duplicate result",[launch,handback(),handback()],110],
    ["request already retained",[launch,request,handback()],220],
    ["SDK aggregate includes child",[launch,handback(),event("sdk","result",{type:"result",usage:{total_tokens:150}},4)],150],
    ["multi-step result is only a last request",[launch,handback({totalToolUseCount:12})],0],
    ["incomplete result",[launch,handback({status:"async_launched"})],0],
    ["inconsistent counters",[launch,handback({totalTokens:999})],0],
    ["unproved child identity",[handback()],0],
    ["ordinary tool output",[launch,{...handback(),kind:"tool_result"}],0],
    ["quoted usage",[launch,event("quote","delegation_result",{content:JSON.stringify({details})},3,{call_id:"child"})],0],
  ]){
    const reports=conversationTokenReports(messages);
    assert.equal(reports.reduce((sum,report)=>sum+(report?.total||0),0),want,name);
  }
  const reports=conversationTokenReports([launch,handback()]);
  assert.equal(reports[1].stream,"child");
  assert.equal(reports[1].breakdown.cacheRead,90);
});

test("only protocol metadata marks a continuation", () => {
  const count = (total, last) => ({ kind: "metadata", role: "system", text: JSON.stringify({ type: "event_msg", payload: { type: "token_count", info: { total_token_usage: { total_tokens: total }, last_token_usage: { total_tokens: last } } } }) });
  const marker = JSON.stringify({ type: "session_meta", payload: { forked_from_id: "parent" } });
  // Conductor stores pasted JSON as metadata under the sender's role.
  for (const kind of ["message", "metadata"]) {
    const reports = conversationTokenReports([{ kind, role: "user", text: marker }, count(1100, 100), count(1300, 200)]);
    assert.equal(reports.reduce((sum, report) => sum + (report?.total || 0), 0), 1300, kind);
  }
});


test("harness footer stays separate from consumption and context", () => {
  const footer="agentId: agent-one (use SendMessage with to: 'agent-one', summary: '<5-10 word recap>' to continue this agent)\n<usage>subagent_tokens: 116837\ntool_uses: 83\nduration_ms: 835048</usage>";
  const launch=event("launch","delegation",{tool:"Agent",input:{}},1,{call_id:"child"});
  const result=event("result","delegation_result",{content:[{type:"text",text:"Report"},{type:"text",text:footer}],is_error:false},2,{call_id:"child"});
  const evidence=reader.conversationTokenEvidence;
  for(const [name,call,body,want] of [
    ["harness footer",launch,result,1],
    ["ordinary output",launch,{...result,kind:"tool_result"},0],
    ["not delegated",{...launch,call_id:"other"},result,0],
    ["ordinary tool",{...launch,text:JSON.stringify({tool:"Read"})},result,0],
    ["wrong resume",{...launch,text:JSON.stringify({tool:"Agent",input:{resume:"other"}})},result,0],
    ["prose prefix",launch,{...result,text:JSON.stringify({content:[{type:"text",text:"Example: "+footer}]})},0],
    ["duplicate trailer",launch,{...result,text:JSON.stringify({content:[{type:"text",text:footer},{type:"text",text:footer}]})},0],
    ["identity mismatch",launch,{...result,text:JSON.stringify({content:[{type:"text",text:footer}],details:{agentId:"other"}})},0],
    ["overflow",launch,{...result,text:result.text.replace('116837','999999999999999999999')},0],
  ]) assert.equal(evidence([call,body]).length,want,name);
  const reports=contextTokenReports([launch,result],conversationTokenReports([launch,result]));
  assert.equal(reports[1].observedMinimum,116837);
  assert.equal(reports.reduce((n,r)=>n+(r?.total||0),0),0,"observation never enters the spending ledger");
  assert.ok(!reports.some(r=>r?.contextSample),"observation is not input context");
  const aggregate=event("sdk","result",{type:"result",usage:{total_tokens:200000}},3);
  assert.equal(conversationTokenReports([launch,result,aggregate]).reduce((n,r)=>n+(r?.total||0),0),200000);
});
