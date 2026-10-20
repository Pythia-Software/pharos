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

test("all source accounting formats reconcile against shared fixtures", () => {
  assert.ok(tokenCases.length > 0);
  for (const item of tokenCases) {
    const reports = conversationTokenReports(item.events.map((value, i) => ({ native_id: String(i), text: JSON.stringify(value) })));
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
});
