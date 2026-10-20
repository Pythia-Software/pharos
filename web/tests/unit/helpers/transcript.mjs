// The conversation reader's model (internal/archive/assets/reader-model.js),
// loaded once per test file, and builders for the events tests feed it. The
// builders match tests/transcript-ui.test.cjs, so its cases port unchanged.
import { loadClassicScript } from "./classic-script.mjs";

export const reader = loadClassicScript("reader-model.js");

export function event(id, kind, text, seconds = 0, extra = {}) {
  return { native_id: id, role: kind === "tool_result" ? "tool" : "assistant", kind,
    text: typeof text === "string" ? text : JSON.stringify(text),
    created_at: new Date(Date.UTC(2026, 8, 20, 12, 0, seconds)).toISOString(), ...extra };
}
export const human = (text = "Inspect the rules and report the result.") => event("human", "message", text, 0, { role: "user" });
export const read = (id = "read", file = "/workspace/docs/RULES.md", seconds = 10) => event(id, "message", {
  message: { id: "provider-message-id", model: "claude-opus-4-5", content: [
    { id: `${id}-call`, type: "tool_use", name: "Read", input: { file_path: file } },
  ] }, context_management: null,
}, seconds);

// The model renderConversation (ui.py) builds before it renders anything:
// normalized events, their grouping and turns, todo summaries, file roots, and
// each turn's owned events. `fields` and `workspaceRoots` are what the
// transcript-ui fixture passes.
export function render(messages, fields = {}, workspaceRoots = []) {
  const conversation = { provider: "claude", native_id: "fixture", repo_root: "/workspace", ...fields, messages };
  const events = reader.conversationMessages(conversation), graph = reader.conversationGraph(events);
  const context = { ...graph, repoRoot: reader.conversationRoots(conversation, workspaceRoots) };
  reader.describeTodoChanges(events, context);
  const visited = new Set();
  for (const turn of graph.turns) turn.owned = reader.ownedMessages([turn.human, ...turn.events], context, visited);
  return { conversation, events, graph, context };
}

// A set of events' reported spend, as a turn's or sub-agent's badge shows it:
// null when none reported any.
export function spend(messages) {
  const known = messages.map(message => message.tokenReport).filter(report => report && report.total !== null);
  return known.length ? known.reduce((total, report) => total + report.total, 0) : null;
}
