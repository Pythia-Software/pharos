// Normalized events, tool calls and their results, edits, todos, commands and
// file paths in the conversation reader (reader-model.js).
import { test } from "node:test";
import assert from "node:assert/strict";
import { plain } from "../helpers/classic-script.mjs";
import { event, human, read, reader, render } from "../helpers/transcript.mjs";

const { commandKind, conversationMessages, fileChanges, filePathParts, messageFailed, messageJSON, normalizeMessages, readableValue, todoItems, toolCategory, toolFailed, toolPayload } = reader;
const ids = messages => plain([...messages].map(message => message.native_id));
const calls = events => [...events].filter(message => message.kind === "tool_call");

test("session and turn ages accept epoch and ISO timestamps and keep two useful units", () => {
  const now = Date.parse("2026-10-20T15:20:00Z");
  for (const [elapsed, expected] of [
    [0, "just now"], [59000, "just now"], [60000, "1m ago"],
    [3599999, "59m ago"], [3600000, "1h ago"], [8100000, "2h 15m ago"],
    [86400000, "1d ago"], [176400000, "2d 1h ago"],
  ]) {
    const time = now - elapsed;
    assert.equal(reader.relativeAge(time, now), expected);
    assert.equal(reader.relativeAge(new Date(time).toISOString(), now), expected);
  }
  assert.equal(reader.relativeAge(now + 8100000, now), "in 2h 15m", "future source timestamps do not show negative ages");
  assert.equal(reader.relativeAge(0, 86400000), "1d ago", "epoch zero is a valid timestamp");
  assert.equal(reader.relativeAge(now - 60000, now + 60000), "2m ago", "refreshing uses the current clock");
});

test("missing or invalid timestamps omit the age instead of inventing one", () => {
  const now = Date.parse("2026-10-20T15:20:00Z");
  for (const value of [undefined, null, "", " ", "unknown", false, NaN, Infinity, 1e20]) {
    assert.equal(reader.relativeAge(value, now), "", String(value));
  }
  assert.equal(reader.relativeAge(now, NaN), "");
});

test("JSON-string command arguments show the command and associate failures with it", () => {
  const { events, graph } = render([human(),
    event("cmd", "tool_call", { name: "exec_command", arguments: JSON.stringify({ cmd: "npm test", workdir: "/repo" }) }, 10, { call_id: "command-1" }),
    event("result", "tool_result", { content: "Tests failed: assertion mismatch", is_error: true }, 12, { call_id: "command-1" }),
  ]);
  const [call] = calls(events), results = graph.toolResultsByCall.get("command-1");
  assert.equal(toolPayload(call).input.cmd, "npm test");
  assert.equal(toolCategory(call), "command");
  // The result pairs with its call, so the turn shows one event and it failed.
  assert.deepEqual(ids(results), ["result"]);
  assert.deepEqual(ids(graph.turns[0].events), ["cmd"]);
  assert.equal(toolFailed(call, results), true);
  assert.match(readableValue(toolPayload(results[0]).content), /assertion mismatch/);
});

test("mixed text and multiple tool blocks split into events and pair wrapped results", () => {
  const mixed = event("mixed", "message", { message: { content: [
    { type: "text", text: "I will inspect both configuration files." },
    { type: "tool_use", id: "r1", name: "Read", input: { file_path: "/repo/a.conf" } },
    { type: "tool_use", id: "r2", name: "Read", input: { file_path: "/repo/b.conf" } },
  ] } }, 2);
  const result = event("results", "message", { message: { role: "user", content: [
    { type: "tool_result", tool_use_id: "r1", content: "alpha result text" },
    { type: "tool_result", tool_use_id: "r2", content: "beta result text" },
  ] } }, 3, { role: "user" });
  const { events, graph } = render([human(), mixed, result]);
  assert.deepEqual(plain([...events].map(message => [message.kind, message.call_id ?? null])), [["message", null], ["message", null], ["tool_call", "r1"], ["tool_call", "r2"], ["tool_result", "r1"], ["tool_result", "r2"]]);
  assert.equal(graph.turns.length, 1, "results from a user envelope do not open a turn");
  assert.deepEqual(plain(calls(graph.turns[0].events).map(call => toolPayload(call).input.file_path)), ["/repo/a.conf", "/repo/b.conf"]);
  assert.deepEqual(["r1", "r2"].map(id => [...graph.toolResultsByCall.get(id)].map(item => readableValue(toolPayload(item).content))), [["alpha result text"], ["beta result text"]]);
});

test("every result of a call stays paired to it", () => {
  const { events, graph } = render([human(),
    event("call", "tool_call", { name: "Bash", input: { command: "make test" } }, 2, { call_id: "c" }),
    event("part-one", "tool_result", { content: "First output chunk" }, 3, { call_id: "c" }),
    event("part-two", "tool_result", { content: "Second output chunk" }, 4, { call_id: "c" }),
  ]);
  assert.deepEqual(ids(graph.turns[0].events), ["call"]);
  assert.deepEqual(ids(graph.toolResultsByCall.get("c")), ["part-one", "part-two"]);
  assert.deepEqual(plain([...graph.consumed]), ["part-one", "part-two"]);
  assert.equal(toolFailed(calls(events)[0], graph.toolResultsByCall.get("c")), false);
});

test("a delegation owns its child events, their results and its own result", () => {
  const { events, graph, context } = render([human(),
    event("delegate", "delegation", { name: "Task", input: { description: "Audit configuration" } }, 5, { call_id: "agent-1" }),
    event("child", "tool_call", { name: "Read", input: { file_path: "/repo/nested.toml" } }, 6, { call_id: "nested-read", parent_native_id: "agent-1" }),
    event("child-result", "tool_result", { content: "nested configuration contents" }, 7, { call_id: "nested-read", parent_native_id: "agent-1" }),
    event("delegate-result", "delegation_result", { content: "Audit complete" }, 8, { call_id: "agent-1" }),
  ]);
  assert.deepEqual(ids(graph.turns[0].events), ["delegate"]);
  assert.deepEqual(ids(graph.childrenByParent.get("agent-1")), ["child", "child-result"]);
  assert.deepEqual(ids(graph.toolResultsByCall.get("nested-read")), ["child-result"]);
  assert.deepEqual(ids(graph.delegationResultsByCall.get("agent-1")), ["delegate-result"]);
  const delegate = [...events].find(message => message.kind === "delegation");
  assert.deepEqual(ids(reader.ownedMessages([delegate], context)).sort(), ["child", "child-result", "delegate", "delegate-result"]);
});

test("orphaned child events stay in their turn and unknown envelopes keep their raw fields", () => {
  const { events, graph } = render([human(),
    event("orphan", "tool_call", { name: "Read", input: { file_path: "/repo/orphan.txt" } }, 4, { parent_native_id: "missing-parent" }),
    event("unknown", "metadata", { type: "future_provider_event", status: "paused", private_future_field: "forensic-value" }, 5),
  ]);
  assert.deepEqual(ids(graph.turns[0].events), ["orphan", "unknown"]);
  const unknown = [...events].find(message => message.native_id === "unknown");
  assert.match(reader.displayText(unknown), /future_provider_event/);
  assert.doesNotMatch(reader.displayText(unknown), /forensic-value/);
  assert.equal(messageJSON(unknown, true).private_future_field, "forensic-value");
});

test("Write without prior contents does not invent line deltas", () => {
  const { events } = render([human(), event("write", "tool_call", { name: "Write", input: { file_path: "/repo/overwritten.txt", content: "Replacement line one\nReplacement line two" } }, 4)]);
  const [file, ...more] = fileChanges(calls(events)[0]);
  assert.equal(more.length, 0);
  assert.equal(file.path, "/repo/overwritten.txt");
  assert.equal(file.unknown, true);
  assert.equal(file.added, undefined);
  assert.equal(file.removed, undefined);
  assert.equal(file.contentOnly, true, "the written content is not a diff");
  assert.match(file.diff, /Replacement line one/);
});

test("normalized user-role text-block results remain paired and retain error status", () => {
  const { events, graph } = render([human(),
    event("call", "tool_call", { tool: "Read", input: { file_path: "/repo/missing.txt" } }, 1, { call_id: "missing" }),
    event("result", "tool_result", { tool: "Read", content: [{ type: "text", text: "ENOENT missing file" }], is_error: true }, 2, { call_id: "missing", role: "user" }),
  ]);
  assert.equal(graph.turns.length, 1, "a user-role result does not open a turn");
  assert.deepEqual(ids(graph.turns[0].events), ["call"]);
  const results = graph.toolResultsByCall.get("missing");
  assert.deepEqual(ids(results), ["result"]);
  assert.equal(toolFailed(calls(events)[0], results), true);
  assert.equal(readableValue(toolPayload(results[0]).content), "ENOENT missing file");
});

test("null blocks are tolerated and file text mentioning exit code does not signal failure", () => {
  const { events, graph } = render([human(),
    event("wrapped", "message", { message: { content: [null, { type: "text", text: "Read the troubleshooting guide." }] } }, 1),
    event("call", "tool_call", { name: "Read", input: { file_path: "/repo/guide.md" } }, 2, { call_id: "guide" }),
    event("result", "tool_result", { content: "The guide says exit code 1 means an error." }, 3, { call_id: "guide" }),
  ]);
  assert.deepEqual(plain([...events].filter(message => message.kind === "message" && message.role !== "user").map(message => message.text)), ["Read the troubleshooting guide."]);
  assert.equal(toolFailed(calls(events)[0], graph.toolResultsByCall.get("guide")), false);
  assert.equal([...events].some(messageFailed), false);
});

test("trailing aborted-by-user noise is omitted only after a successful result", () => {
  const abort = event("late-abort", "error", { type: "error", content: "aborted by user" }, 910);
  const cases = [
    ["after a successful result", [human(), event("reply", "message", "The requested work is complete.", 5), event("success", "result", { type: "result", subtype: "success" }, 10), abort], ["human", "reply", "success"]],
    ["without a successful result", [human(), event("reply", "message", "Waiting for the operation.", 5), abort], ["human", "reply", "late-abort"]],
    ["after a failed result", [human(), event("failure", "result", { type: "result", subtype: "error_during_execution" }, 10), abort], ["human", "failure", "late-abort"]],
  ];
  for (const [name, messages, kept] of cases) {
    const events = conversationMessages({ messages });
    assert.deepEqual(ids(events), kept, name);
    assert.equal([...events].filter(messageFailed).length, kept.includes("late-abort") ? 1 : 0, name);
  }
});

test("failed Edit keeps the requested diff and reports the failure", () => {
  const { events, graph } = render([human(),
    event("edit", "tool_call", { name: "Edit", input: { file_path: "/repo/app.js", old_string: "old()", new_string: "new()" } }, 2, { call_id: "failed-edit" }),
    event("error", "tool_result", { content: "Could not locate old text", is_error: true }, 3, { call_id: "failed-edit" }),
  ]);
  const [call] = calls(events), results = graph.toolResultsByCall.get("failed-edit");
  assert.equal(toolFailed(call, results), true);
  const [file] = fileChanges(call, results[0]);
  assert.deepEqual([file.path, file.added, file.removed, file.unknown], ["/repo/app.js", 1, 1, false]);
  assert.match(file.diff, /^-old\(\)$/m);
  assert.match(file.diff, /^\+new\(\)$/m);
  assert.match(readableValue(toolPayload(results[0]).content), /Could not locate old text/);
});

test("replace_all and deletion without retained lines use unknown delta counts", () => {
  const { events } = render([human(),
    event("replace", "tool_call", { name: "Edit", input: { file_path: "/repo/all.txt", old_string: "old", new_string: "new", replace_all: true } }, 1),
    event("delete", "tool_call", { name: "apply_patch", input: "*** Begin Patch\n*** Delete File: /repo/deleted.txt\n*** End Patch" }, 2),
  ]);
  assert.deepEqual(plain(calls(events).flatMap(call => [...fileChanges(call)].map(file => [file.path, file.unknown]))), [["/repo/all.txt", true], ["/repo/deleted.txt", true]]);
});

test("edits count the lines they replace and add", () => {
  const { events } = render([human(), event("edit", "tool_call", { name: "Edit", input: { file_path: "/repo/src/app.js", old_string: "const old = 1;", new_string: "const current = 2;\nrun(current);" } }, 14, { call_id: "edit-1" })]);
  assert.deepEqual(plain([...fileChanges(calls(events)[0])].map(file => [file.path, file.added, file.removed, file.unknown])), [["/repo/src/app.js", 2, 1, false]]);
});

test("patches count their lines per file", () => {
  const { events } = render([human(), event("patch", "tool_call", { name: "apply_patch", input: "*** Begin Patch\n*** Update File: /workspace/src/a.js\n-oldA\n+newA\n+moreA\n*** Add File: /workspace/src/b.js\n+newB\n*** End Patch" }, 2)]);
  assert.deepEqual(plain([...fileChanges(calls(events)[0])].map(file => [file.path, file.added, file.removed, file.unknown])), [["/workspace/src/a.js", 2, 1, false], ["/workspace/src/b.js", 1, 0, false]]);
});

test("todo management is distinct from file writes and exposes statuses", () => {
  const { events } = render([human(),
    event("todos", "tool_call", { name: "TodoWrite", input: { todos: [{ content: "Inspect parser", status: "completed" }, { content: "Add tests", status: "in_progress" }] } }, 1),
    event("create", "tool_call", { name: "TaskCreate", input: { subject: "Review changes" } }, 2),
    event("delete", "tool_call", { name: "TaskUpdate", input: { taskId: "42", status: "deleted" } }, 3),
  ]);
  const [todos, create, remove] = calls(events);
  assert.deepEqual([todos, create, remove].map(toolCategory), ["todo", "todo", "todo"]);
  assert.deepEqual([todos, create, remove].map(call => fileChanges(call)), [null, null, null], "todos are not file changes");
  assert.deepEqual(plain(todoItems(todos).map(item => [item.content, item.status])), [["Inspect parser", "completed"], ["Add tests", "in_progress"]]);
  assert.match(todos.todoSummary, /\b2\b/);
  assert.match(create.todoSummary, /Review changes/);
  assert.match(remove.todoSummary, /Task 42/);
  assert.match(remove.todoSummary, /delet/i);
});

test("todo summaries describe only changes and preserve complete lists", () => {
  const todos = status => [{ content: "Inspect parser", status }, { content: "Write tests", status: "pending" }];
  const { events } = render([human(),
    event("initial", "tool_call", { name: "TodoWrite", input: { todos: todos("pending") } }, 1),
    event("update", "tool_call", { name: "TodoWrite", input: { todos: todos("completed") } }, 2),
    event("same", "tool_call", { name: "TodoWrite", input: { todos: todos("completed") } }, 3),
  ]);
  const [initial, update, same] = calls(events);
  assert.match(initial.todoSummary, /\b2\b/, "the first list is summarized by its size");
  assert.match(update.todoSummary, /Completed/);
  assert.match(update.todoSummary, /Inspect parser/);
  assert.doesNotMatch(update.todoSummary, /Write tests/, "unchanged items stay out of the summary");
  assert.doesNotMatch(same.todoSummary, /Inspect parser|Write tests/);
  assert.notEqual(same.todoSummary, update.todoSummary);
  assert.deepEqual(plain(todoItems(update).map(item => item.content)), ["Inspect parser", "Write tests"]);
});

test("command kinds distinguish git, python, typechecking, tests and lint", () => {
  const commands = [["git diff --stat", "git"], ["python3 tools/check.py", "python"], ["npm run typecheck", "typecheck"], ["npm test", "test"],
    ["tsc --noEmit -p web", "typecheck"], ["go test ./internal/archive", "test"], ["cd web && npx vitest run", "test"], ["ruff check tools", "lint"], ["ls -la", "command"]];
  const { events } = render([human(), ...commands.map(([command], index) => event("cmd" + index, "tool_call", { name: "Bash", input: { command } }, index + 1))]);
  assert.deepEqual(calls(events).map(call => commandKind(call)), commands.map(([, kind]) => kind));
  assert.equal(commandKind(calls(render([human(), read()]).events)[0]), "read", "other tools keep their category");
});

test("repository, worktree and recorded cwd paths collapse while external paths stay explicit", () => {
  const { events, context } = render([human(),
    event("cwd", "metadata", { type: "session_meta", cwd: "/checkout/task" }, 1),
    read("worktree", "/checkout/task/src/main.py", 2),
    read("repo", "/source/project/docs/guide.md", 3),
    read("outside", "/source/project-other/secret.txt", 4),
  ], { repo_root: null }, ["/source/project"]);
  assert.deepEqual(plain(context.repoRoot), ["/source/project", "/checkout/task"]);
  const parts = calls(events).map(call => filePathParts(toolPayload(call).input.file_path, context.repoRoot));
  assert.deepEqual(parts.map(part => [part.directory, part.filename]), [["src/", "main.py"], ["docs/", "guide.md"], ["/source/project-other/", "secret.txt"]]);
  // A chip collapses the directory of a path inside a root; an absolute path elsewhere shows it.
  assert.deepEqual(parts.map(part => part.absolute && !part.inside), [false, false, true]);
});

test("parsed message bodies are reused and update when an original replaces a preview", () => {
  const message = { text: JSON.stringify({ tool: "read", input: { path: "before" } }), raw_text: JSON.stringify({ type: "token_count" }) };
  const first = messageJSON(message), raw = messageJSON(message, true), same = messageJSON(message) === first && messageJSON(message, true) === raw;
  const normalized = normalizeMessages([message])[0], shared = messageJSON(normalized) === first;
  normalized.text = JSON.stringify({ tool: "read", input: { path: "after" } }); normalized.raw_text = normalized.text;
  assert.deepEqual({ same, shared, path: toolPayload(normalized).input.path, raw: messageJSON(normalized, true).input.path, source: messageJSON(message).input.path, missing: messageJSON({}) },
    { same: true, shared: true, path: "after", raw: "after", source: "before", missing: null });
});
