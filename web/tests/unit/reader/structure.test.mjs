// The Show filter's categories and Find in the conversation reader
// (reader-model.js). The fixture test is the JS half of
// internal/archive/transcript_find_test.go; both read
// testdata/transcript-find-fixtures.json.
import { sidebarWidth } from "../../../src/workspace-inbox-model";
import { test } from "node:test";
import assert from "node:assert/strict";
import { plain } from "../helpers/classic-script.mjs";
import { event, human, reader } from "../helpers/transcript.mjs";
import findCases from "../../../../testdata/transcript-find-fixtures.json";

const { TRANSCRIPT_CATEGORIES, conversationMessages, transcriptFind, transcriptFindPattern, transcriptShown, transcriptStructure } = reader;
const allKinds = () => [...TRANSCRIPT_CATEGORIES].map(([key]) => key);
// The filter's events and their category paths, as renderConversation passes
// them to the Show control: every event but results paired to a call.
function filterPaths(messages) {
  const structure = transcriptStructure(messages);
  return [...messages].filter(message => !structure.consumed.has(message)).map(message => [...structure.pathOf(message)]);
}
const visible = (paths, shown) => paths.filter(path => path.every(key => shown.has(key))).length;

test("reader classification and find counts match the shared fixtures the service checks", () => {
  assert.ok(findCases.length > 0);
  for (const item of findCases) {
    const messages = conversationMessages({ messages: item.messages.map(message => ({ ...message, text: typeof message.text === "string" ? message.text : JSON.stringify(message.text) })) }), structure = transcriptStructure(messages);
    assert.deepEqual(plain([...messages].map(message => ({ kind: message.kind, path: structure.pathOf(message) }))), item.events, item.name);
    const queries = item.queries.map(query => plain(transcriptFind(messages, { pattern: transcriptFindPattern(query.term, !!query.regex, !!query.case), shown: new Set(query.show || allKinds()), output: !!query.output })));
    assert.deepEqual(queries, item.queries.map(query => query.want), item.name);
  }
});

test("events inside a hidden sub-agent count as hidden", () => {
  const messages = conversationMessages({ messages: [human(),
    event("delegate", "delegation", { name: "Task", input: { description: "Audit configuration" } }, 1, { call_id: "agent-1" }),
    event("child", "message", "Configuration looks fine.", 2, { parent_native_id: "agent-1" }),
    event("delegate-result", "delegation_result", { content: "Audit complete" }, 3, { call_id: "agent-1" }),
    event("final", "message", "Done.", 4)] });
  const paths = filterPaths(messages);
  assert.deepEqual(paths, [["prompts"], ["agents"], ["replies", "agents"], ["response"]]);
  assert.equal(visible(paths, transcriptShown({ level: "thought" })), 2);
});

test("filter levels choose categories and Find counts what they hide", () => {
  const messages = conversationMessages({ messages: [human("Audit the rules."),
    event("think", "reasoning", "Weighing which rule file to open first", 1),
    event("interim", "message", "Reading the rules now.", 2),
    event("run", "tool_call", { name: "Bash", input: { command: "grep -r quorum rules/" } }, 3, { call_id: "run" }),
    event("run-result", "tool_result", { content: "rules/a.md: quorum is three" }, 4, { call_id: "run" }),
    event("final", "message", "Quorum is three.", 5)] });
  const paths = filterPaths(messages);
  assert.deepEqual(paths.map(([category]) => category), ["prompts", "thinking", "replies", "commands", "response"]);
  const levels = Object.fromEntries(["all", "all-minus-usage", "thought", "primary"].map(level => [level, transcriptShown({ level })]));
  assert.deepEqual([...levels.all], allKinds());
  assert.deepEqual([...levels["all-minus-usage"]], allKinds().filter(key => key !== "usage"));
  assert.equal(visible(paths, levels.primary), 2);
  assert.equal(visible(paths, levels.thought), 4);
  assert.deepEqual([...transcriptShown({ level: "custom", custom: ["prompts", "commands"] })], ["prompts", "commands"]);
  assert.deepEqual([...transcriptShown({ level: "custom", custom: null })], []);
  // Find skips hidden events and names the kinds hiding them; output counts only when searched.
  const find = (term, shown, output = false) => plain(transcriptFind(messages, { pattern: transcriptFindPattern(term, false, false), shown, output }));
  assert.deepEqual(find("grep -r", levels.primary), { shown: 0, hidden: 1, hidden_kinds: ["commands"] });
  assert.deepEqual(find("grep -r", levels.all), { shown: 1, hidden: 0, hidden_kinds: [] });
  assert.deepEqual(find("quorum is three", levels.all), { shown: 1, hidden: 0, hidden_kinds: [] });
  assert.deepEqual(find("quorum is three", levels.all, true), { shown: 2, hidden: 0, hidden_kinds: [] });
});

test("find patterns escape plain terms and respect case", () => {
  assert.equal(transcriptFindPattern("a.b(c)", false, false).test("A.B(C)"), true);
  assert.equal(transcriptFindPattern("a.b(c)", false, false).test("axb(c)"), false);
  assert.equal(transcriptFindPattern("a.b", true, false).test("axb"), true);
  assert.equal(transcriptFindPattern("Quorum", false, true).test("quorum"), false);
  assert.throws(() => transcriptFindPattern("(", true, false));
});

test("Source anchors choose exact identity or nearest timestamp without inventing an event", () => {
  const messages = [
    { id: "late", created_at: "2026-10-21T11:00:00Z" },
    { id: "early", created_at: "2026-10-21T10:00:00Z" },
    { id: "invalid", created_at: "not a date" },
  ];
  assert.equal(reader.transcriptAnchor(messages, { message: "early", at: "2026-10-21T11:00:00Z" }), "early");
  assert.equal(reader.transcriptAnchor(messages, { message: "missing", at: "2026-10-21T11:00:00Z" }), null);
  assert.equal(reader.transcriptAnchor(messages, { at: "2026-10-21T10:30:00Z" }), "early");
  assert.equal(reader.transcriptAnchor(messages, { at: "2026-10-21T10:45:00Z" }), "late");
  assert.equal(reader.transcriptAnchor(messages, { at: "invalid" }), null);
  assert.equal(reader.transcriptAnchor([{id: "multi", call_id: "one"}, {id: "multi", call_id: "two"}], {message: "multi", call: "two"}), "multi");
  assert.equal(reader.transcriptAnchor([{id: "multi", call_id: "one"}], {message: "multi", call: "two"}), null);
  assert.equal(reader.transcriptAnchor([], { at: "2026-10-21T10:30:00Z" }), null);
});


test("linked transcript presets stand alone and category lists only define Custom",()=>{
  const saved={level:"all",custom:["commands"]};
  for(const level of ["all","all-minus-usage","thought","primary"]){
    for(const list of [undefined,'["commands"]','invalid']){
      const params=new URLSearchParams({reader_filter_level:level,...list===undefined?{}:{reader_filter:list}});
      const state=reader.transcriptFilterState(params,saved);
      assert.equal(state.level,level);assert.deepEqual([...transcriptShown(state)],[...transcriptShown({level})]);
      assert.deepEqual(plain(state.custom),saved.custom,'presets retain the saved Custom selection');
    }
  }
  const custom=reader.transcriptFilterState(new URLSearchParams({reader_filter_level:"custom",reader_filter:'["prompts","commands"]'}),saved);
  assert.equal(custom.level,"custom");assert.deepEqual(plain([...transcriptShown(custom)]),["prompts","commands"]);
  assert.equal(reader.transcriptFilterState(new URLSearchParams({reader_filter_level:"custom"}),{level:"thought"}).level,"thought");
  assert.equal(reader.transcriptFilterState(new URLSearchParams({reader_filter_level:"unknown"}),saved).level,"all");
  assert.equal(reader.transcriptFilterState(new URLSearchParams({reader_filter:'["commands"]'}),saved).level,"custom");
});


test("sidebar widths normalize legacy preferences and fractional drag limits for URLs", () => {
  assert.equal(sidebarWidth(465.7210937499999), 466);
  assert.equal(sidebarWidth(500, 465.7210937499999), 465);
  for (const width of [NaN, Infinity, -50, 0, 300.2, 465.7210937499999, 6000]) {
    const normalized = sidebarWidth(width);
    assert.ok(Number.isInteger(normalized) && normalized >= 300 && normalized <= 5000);
    assert.equal(sidebarWidth(normalized), normalized);
  }
});

test("result outlines keep turns, setup and nested inline agents under their owning turn", () => {
 const conversation = { messages: [
  event("setup", "context", "Session context"), human("Inspect the rules."),
  event("delegate", "delegation", { name: "Task", input: { subagent_type: "Reviewer", prompt: "Audit configuration" } }, 1, { call_id: "agent-1" }),
  event("child", "message", "Reviewing.", 2, { parent_native_id: "agent-1" }),
  event("nested", "delegation", { name: "Task", input: { description: "Check timing" } }, 3, { call_id: "agent-2", parent_native_id: "agent-1" }),
  event("nested-reply", "message", "Timing checked.", 4, { parent_native_id: "agent-2" }),
  event("result", "delegation_result", { content: "Audit complete." }, 5, { call_id: "agent-1" }),
  event("final", "message", "Done.", 6),
  event("second", "message", "Explain the fix.", 7, { role: "user" }),
 ] };
 // Result cards can load the reader even if another page-shell initializer fails.
 assert.equal(typeof reader.window.pharosConversationOutline, "function");
 const outline = reader.window.pharosConversationOutline(conversation);
 assert.equal(outline.length, 3); assert.equal(outline[0].label, "Session setup");
 assert.equal(outline[1].label, "Turn 1"); assert.equal(outline[2].label, "Turn 2");
 assert.equal(outline[1].human.text, "Inspect the rules."); assert.equal(outline[1].response.text, "Done.");
 const agent = outline[1].children[0];
 assert.equal(agent.label, "Reviewer"); assert.equal(agent.human.text, "Audit configuration");
 assert.equal(agent.response.text, "Reviewing.");
 assert.equal(agent.children[0].prompt, "Check timing"); assert.equal(agent.children[0].response.text, "Timing checked.");
 assert.equal(outline[2].human.text, "Explain the fix."); assert.equal(outline[2].response, null);
 // Two inline tool blocks share a stored message ID but remain separate agents.
 const envelope = { id: "stored-envelope", role: "assistant", kind: "message", text: JSON.stringify({ content: [
  { type: "tool_use", id: "audit-one", name: "Task", input: { prompt: "Audit configuration" } },
  { type: "tool_use", id: "audit-two", name: "Agent", input: { prompt: "Audit timing" } },
 ] }) };
 const messages = [human(), envelope,
  event("one-result", "delegation_result", { content: "Configuration checked." }, 1, { call_id: "audit-one" }),
  event("two-result", "delegation_result", { content: "Timing checked." }, 2, { call_id: "audit-two" }),
 ];
 const agents = reader.conversationOutline({ messages })[0].children;
 assert.equal(new Set(agents.map(agent => agent.id)).size, 2, "each normalized block must be selectable independently");
 assert.equal(agents.find(agent => agent.id === agents[1].id).human.text, "Audit timing");
 assert.equal(agents[1].response.text, "Timing checked.");
 const restored = reader.conversationOutline({ messages: [event("context", "context", "Earlier metadata"), ...structuredClone(messages)] })[1].children;
 assert.deepEqual(plain(restored.map(agent => agent.id)), plain(agents.map(agent => agent.id)), "stored identities survive reload and unrelated event insertions");
});


test("result outlines unwrap a delegation result when no child reply is retained", () => {
 const outline = reader.conversationOutline({ messages: [human(),
  event("delegate", "delegation", { name: "Task", input: { description: "Audit" } }, 1, { call_id: "agent" }),
  event("result", "delegation_result", { content: "Audit complete." }, 2, { call_id: "agent" }),
 ] });
 assert.equal(outline[0].children[0].response.role, "assistant");
 assert.equal(outline[0].children[0].response.text, "Audit complete.");
});
