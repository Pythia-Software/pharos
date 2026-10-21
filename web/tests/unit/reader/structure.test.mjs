// The Show filter's categories and Find in the conversation reader
// (reader-model.js). The fixture test is the JS half of
// internal/archive/transcript_find_test.go; both read
// testdata/transcript-find-fixtures.json.
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
