// The Show filter's categories and Find in the conversation reader
// (reader-model.js). The fixture test is the JS half of
// internal/archive/transcript_find_test.go; both read
// testdata/transcript-find-fixtures.json.
import { readerKeyState, readerLineOptionLabel, readerLineScopes, updateReaderKeys } from "../../../src/reader-key-model";
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

test("workspace heading recognizes chat import sources", () => {
  for (const kind of ["claude-ai", "chatgpt", "chatgpt-export"]) assert.equal(reader.isChatImportSource(kind), true);
  for (const kind of ["claude", "codex", "", null, undefined]) assert.equal(reader.isChatImportSource(kind), false);
});

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

test("Find counts tag-filtered events and paired outputs as hidden", () => {
  const messages = conversationMessages({ messages: [human(),
    event("read", "tool_call", { name: "Read", input: { path: "rules.txt" } }, 1, { call_id: "read" }),
    event("output", "tool_result", { content: "rules.txt contents" }, 2, { call_id: "read" })] });
  const structure = transcriptStructure(messages);
  const result = transcriptFind(messages, { pattern: /rules\.txt/, shown: new Set(allKinds()), output: true,
    hiddenTagOf: message => (structure.consumed.get(message) || message).call_id === "read" ? ["Read"] : [] });
  assert.deepEqual(plain(result), { shown: 0, hidden: 2, hidden_kinds: [], hidden_tags: ["Read"] });
});

test("activity tag identities separate categories and follow changed labels", () => {
  const messages = conversationMessages({ messages: [human(),
    event("write", "tool_call", {name:"Write",input:{file_path:"/repo/a"}}, 1),
    event("edit", "tool_call", {name:"Edit",input:{file_path:"/repo/a"}}, 2),
    event("poll", "tool_call", {name:"write_stdin",input:{session_id:1}}, 3)] });
  const structure = transcriptStructure(messages);
  const index = reader.transcriptTagIndex(messages, structure, message => message.kind === "tool_call"
    ? {category: reader.readerTimelineCategory(message), label: message.native_id === "edit" ? "Edit" : "Write"} : null);
  const write = messages.find(message => message.native_id === "write"), poll = messages.find(message => message.native_id === "poll");
  const writeID = index.tagOf(write), pollID = index.tagOf(poll);
  assert.notEqual(writeID, pollID, "identical labels in different categories have distinct identities");
  assert.deepEqual(plain(index.group("write")), [writeID, index.tagOf(messages.find(message => message.native_id === "edit"))]);
  assert.deepEqual(plain(index.group("poll")), [pollID]);
  let hidden = reader.transcriptTagSelection(new Set(), index.all(), [pollID]);
  assert.equal(reader.transcriptTagHidden(hidden, writeID), true);
  assert.equal(reader.transcriptTagHidden(hidden, pollID), false);
  // Existing URLs hid every matching label. Show one category without losing
  // the old hide for another category or an unrelated conversation.
  hidden = reader.transcriptTagSelection(new Set(["Write", "Absent"]), index.all(), [pollID]);
  assert.equal(reader.transcriptTagHidden(hidden, writeID), true);
  assert.equal(reader.transcriptTagHidden(hidden, pollID), false);
  assert.equal(hidden.has("Absent"), true);
  assert.equal(reader.transcriptTagHidden(new Set([...hidden]), pollID), false, "overrides survive URL round trips");
  hidden = reader.transcriptTagSelection(hidden, index.all(), index.all());
  assert.equal(reader.transcriptTagHidden(hidden, writeID), false);
  assert.equal(reader.transcriptTagHidden(hidden, pollID), false);

  const calls = conversationMessages({messages:[human(),
    event("pending", "tool_call", {name:"mcp__Claude_Docs__batch",input:{container:{create:{name:"Brief",doc:{markdown:"# Brief"}}},batch:[]}},1),
    event("complete", "tool_call", {name:"mcp__Claude_Docs__batch",input:{container:{create:{name:"Other",doc:{markdown:"# Other"}}},batch:[]}},2)]});
  const [pending, complete] = calls.filter(message => message.kind === "tool_call"), receipts = new Map([[pending, []], [complete, [event("receipt", "tool_result", {content:[{type:"text",text:'{"verdict":"allow"}'}]})]]]);
  const docs = reader.transcriptTagIndex(calls, transcriptStructure(calls), message => message.kind === "tool_call"
    ? {category: reader.readerTimelineCategory(message), label: reader.mcpPresentation(message, receipts.get(message)).label} : null);
  const oldID = docs.tagOf(pending), completedID = docs.tagOf(complete);
  assert.notEqual(oldID, completedID);
  receipts.set(pending, receipts.get(complete));
  assert.equal(docs.refresh(pending), true);
  assert.equal(docs.tagOf(pending), completedID, "updated label joins the matching activity");
  assert.equal(docs.info(completedID).count, 2);
  assert.equal(docs.info(oldID), null);
  assert.deepEqual(plain(docs.group(reader.readerTimelineCategory(pending))), [completedID]);
});

test("activity keys group recorded MCP services, context and built-in tools without changing saved tags", () => {
  const cases = [
    ["mcp__remote-devices__Claude_Browser__find", ["MCP", "Browser (remote devices)"], "web"],
    ["mcp__Claude_Browser__navigate", ["MCP", "Browser"], "web"],
    ["mcp__Gmail__search_threads", ["MCP", "Gmail"], "search"],
    ["mcp__remote-devices__device_bash", ["MCP", "Remote devices"], "command"],
    ["mcp__codex_apps__github.fetch_file", ["MCP", "GitHub"], "read"],
    ["mcp__codex_apps__github_fetch_file", ["MCP", "GitHub"], "read"],
    ["mcp__codex_apps__search_service.web_run", ["MCP", "Search service"], "search"],
    ["mcp__codex_apps__search_service_web_run", ["MCP", "Search service"], "search"],
    ["mcp__custom-server__unfamiliar", ["MCP", "custom-server"], "tool"],
    ["mcp__custom-server.read_item", ["MCP", "custom-server"], "read"],
    ["ToolSearch", ["Tool discovery"], "search"], ["Skill", ["Skills"], "read"],
    ["Read", ["Reads & searches"], "read"], ["Edit", ["File edits"], "edit"],
    ["Bash", ["Commands"], "command"], ["TodoWrite", ["Planning"], "todo"],
  ];
  for (const [name, groups, icon] of cases) {
    const style = reader.transcriptActivityPresentation(event(name, "tool_call", { name }));
    assert.deepEqual(plain(style.keyPath.map(part => part.label)), groups, name);
    assert.equal(style.icon, icon, name);
  }
  for (const [kind, payload, icon] of [["metadata", {type:"claude_ai_conversation"}, "chat"], ["context", {source:"date_note"}, "history"], ["context", {source:"agent_listing"}, "agent"], ["context", {source:"mcp_instructions_delta"}, "tool"]]) {
    const style = reader.transcriptActivityPresentation(event(kind, kind, payload));
    assert.deepEqual(plain(style.keyPath.map(part => part.label)), ["Context"]);
    assert.equal(style.icon, icon);
  }
  const calls = [event("a", "tool_call", {name:"mcp__Gmail__search_threads"}), event("b", "tool_call", {name:"mcp__custom-server__search"}), event("c", "tool_call", {name:"Skill"})];
  const describe = message => ({...reader.transcriptActivityPresentation(message), category:reader.readerTimelineCategory(message), label:message === calls[2] ? "Skill" : "Search"});
  const index = reader.transcriptTagIndex(calls, transcriptStructure(calls), describe), saved = index.tagOf(calls[0]);
  assert.equal(saved, reader.transcriptTagId(reader.readerTimelineCategory(calls[0]), "Search"));
  assert.equal(index.tagOf(calls[1]), saved, "equal labels retain their existing shared filter identity");
  const keys = reader.transcriptActivityKeys(index), mcp = keys.find(item => item.label === "MCP");
  assert.deepEqual(plain(mcp.values), [saved]);
  assert.equal(mcp.children[0].id, saved, "shared labels live once at their common MCP ancestor");
  const leaves = items => items.flatMap(item => item.children ? leaves(item.children) : item.values);
  assert.equal(leaves(keys).length, index.all().length, "shared tags are not duplicated between service groups");
  assert.deepEqual(new Set(leaves(keys)), new Set(index.all()), "every recorded activity has one leaf");
  calls[1].text = JSON.stringify({name:"Skill"});
  assert.equal(index.refresh(calls[1]), true);
  const refreshed = reader.transcriptActivityKeys(index).find(item => item.label === "MCP");
  assert.equal(refreshed.children[0].label, "Gmail", "removing a member restores the surviving service group");
  assert.deepEqual(plain(refreshed.values), [saved]);
  const commands = [event("git", "tool_call", {name:"Bash",input:{command:"git status"}}),event("tests", "tool_call", {name:"Bash",input:{command:"go test ./..."}})];
  assert.equal(reader.transcriptActivityPresentation(commands[0]).icon, "git", "a single command keeps its specialized icon");
  const commandIndex = reader.transcriptTagIndex(commands, transcriptStructure(commands), message => ({...reader.transcriptActivityPresentation(message),category:reader.readerTimelineCategory(message),label:"Run"}));
  const commandID = commandIndex.tagOf(commands[0]);
  assert.equal(reader.transcriptActivityIcon(commandIndex.info(commandID)), "command", "different commands sharing a label use the common command icon");
  assert.equal(reader.transcriptActivityKeys(commandIndex)[0].children[0].icon, reader.transcriptActivityIcon(commandIndex.info(commandID)));
});

test("content keys join categories and activities while preserving saved filter identities", () => {
  const messages=conversationMessages({messages:[human(),event("thought","reasoning","Consider the evidence"),
    event("read","tool_call",{name:"Read"}),event("search","tool_call",{name:"Grep"}),
    event("mcp","tool_call",{name:"mcp__custom__do_work"}),event("context","context",{source:"date_note"}),event("answer","message","Done")]});
  const structure=transcriptStructure(messages),index=reader.transcriptTagIndex(messages,structure,message=>message.kind==="message"?null:{...reader.transcriptActivityPresentation(message),category:reader.readerTimelineCategory(message),label:message.native_id});
  const model=reader.transcriptContentKeys(index,new Set(messages.map(message=>structure.pathOf(message)[0])));
  const reads=model.items.find(item=>item.kind==="reads"),thinking=model.items.find(item=>item.kind==="thinking"),tools=model.items.find(item=>item.kind==="tools"),events=model.items.find(item=>item.kind==="events");
  assert.deepEqual(plain(reads.children.map(item=>item.label)),["read","search"],"Read and Search refine the single content category");
  assert.deepEqual(plain(thinking.children.map(item=>item.label)),["thought"]);
  assert.deepEqual(plain(tools.children.map(item=>item.label)),["MCP"],"MCP services belong under Other tools");
  assert.deepEqual(plain(events.children.map(item=>item.label)),["Context"]);
  assert.equal(index.tagOf(messages[2]),reader.transcriptTagId("read","read"));
  const leaves=items=>items.flatMap(item=>item.children?leaves(item.children):item.values);
  assert.deepEqual(new Set(leaves(model.items)),new Set(model.all),"each selectable content entry has one leaf");

  const all=new Set(allKinds()),read=reads.children.find(item=>item.label==="read"),search=reads.children.find(item=>item.label==="search");
  const unrelated=reader.transcriptTagId("write","not in this conversation"),hidden=new Set(["search",unrelated]);
  const shown=(kinds,tags)=>new Set(reader.transcriptContentShown(model,new Set(kinds),new Set(tags)));
  assert.equal(shown(all,hidden).has(read.values[0]),true);
  assert.equal(shown(all,hidden).has(search.values[0]),false,"legacy label exclusions still apply");
  const hide=reader.transcriptContentSelection(model,all,hidden,reads,"hide");
  assert.equal(hide.categories.includes("reads"),false);
  assert.deepEqual(plain(hide.hidden),[...hidden],"hiding a category retains dormant child choices");
  assert.ok(reads.values.every(id=>!shown(hide.categories,hide.hidden).has(id)),"a disabled parent dims every child");
  const showChild=reader.transcriptContentSelection(model,new Set(hide.categories),new Set(hide.hidden),search,"show");
  assert.equal(showChild.categories.includes("reads"),true,"a child restores its category");
  assert.ok(showChild.hidden.includes("show:"+search.tags[0]),"showing a child overrides its legacy exclusion without removing that exclusion elsewhere");
  assert.ok(showChild.hidden.includes(unrelated));
  assert.equal(shown(showChild.categories,showChild.hidden).has(search.values[0]),true);
  const isolated=reader.transcriptContentSelection(model,all,new Set(),read,"only");
  assert.deepEqual(plain(isolated.categories),["reads"]);
  assert.deepEqual([...shown(isolated.categories,isolated.hidden)],[read.values[0]],"isolation keeps only the chosen leaf");
  const restored=reader.transcriptContentSelection(model,new Set(isolated.categories),new Set(isolated.hidden),reads,"show");
  assert.ok(reads.values.every(id=>shown(restored.categories,restored.hidden).has(id)),"showing a category restores all of its children");
  const clear=reader.transcriptContentSelection(model,new Set(isolated.categories),new Set([...isolated.hidden,unrelated]),read,"clear");
  assert.deepEqual(plain(clear.categories),allKinds());
  assert.ok(clear.hidden.includes(unrelated));
  assert.equal(reader.transcriptContentShown(model,new Set(clear.categories),new Set(clear.hidden)).length,model.all.length);
});

test("shared activity tags retain their content-category membership", () => {
  const messages=conversationMessages({messages:[human(),event("read","tool_call",{name:"Read"}),event("mcp","tool_call",{name:"mcp__custom__do_work"})]});
  const structure=transcriptStructure(messages),index=reader.transcriptTagIndex(messages,structure,message=>message.kind==="tool_call"?{...reader.transcriptActivityPresentation(message),category:"read",label:"Read"}:null);
  assert.equal(index.tagOf(messages[1]),index.tagOf(messages[2]));
  const model=reader.transcriptContentKeys(index,new Set(messages.map(message=>structure.pathOf(message)[0])));
  const read=model.items.find(item=>item.kind==="reads"),tools=model.items.find(item=>item.kind==="tools");
  assert.equal(read.children[0].label,"Read");assert.equal(tools.children[0].label,"MCP");
  assert.deepEqual(plain(read.tags),plain(tools.tags),"stored shared tags stay shared");
  const hidden=reader.transcriptContentSelection(model,new Set(allKinds()),new Set(),read,"hide");
  const shown=new Set(reader.transcriptContentShown(model,new Set(hidden.categories),new Set(hidden.hidden)));
  assert.ok(read.values.every(value=>!shown.has(value)));assert.ok(tools.values.every(value=>shown.has(value)),"a category hide does not hide the same recorded label in another category");
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
 assert.equal(outline.length, 3); assert.equal(outline[0].label, "Agent setup");
 const graph = reader.conversationGraph(reader.conversationMessages(conversation));
 assert.equal(graph.turns.length, 3, "the reader count includes the displayed setup and excludes nested agent messages");
 const agentOnly = reader.conversationGraph(reader.conversationMessages({messages:[event("reply", "message", "Working without a retained prompt.")]}));
 assert.equal(agentOnly.turns.length, 1, "an agent-only transcript still has one displayed turn");
 assert.equal(reader.conversationGraph([]).turns.length, 0);
 assert.equal(outline[1].label, "Turn 1"); assert.equal(outline[2].label, "Turn 2");
 assert.equal(outline[1].human.text, "Inspect the rules."); assert.equal(outline[1].response.text, "Done.");
 const agent = outline[1].children[0];
 assert.equal(agent.label, "Reviewer"); assert.equal(agent.human.text, "Audit configuration");
 assert.equal(agent.response.text, "Reviewing.");
 assert.equal(agent.children[0].prompt, "Check timing"); assert.equal(agent.children[0].response.text, "Timing checked.");
 assert.equal(outline[2].human.text, "Explain the fix."); assert.equal(outline[2].response, null);
 // Each entry owns its messages: a turn its events, a sub-agent its own.
 const owned = entry => plain(entry.messages.map(message => message.native_id)).sort();
 for (const id of ["delegate", "result", "final"]) assert.ok(owned(outline[1]).includes(id), id);
 assert.deepEqual(owned(agent), ["child", "delegate", "nested", "nested-reply", "result"]);
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

test("linked disclosure and content overrides change only the identities they name", () => {
 const { readerDisclosureOpen, readerDisclosureToggle, readerContentShown } = reader;
 // ui.py reads an absent parameter as [] or {}; both keep every native default.
 for (const initial of [true, false]) assert.equal(readerDisclosureOpen("message:a:turn", new Set(), new Set(), initial), initial);
 const expanded = new Set(["message:a:turn"]), collapsed = new Set(["call:b:tool"]);
 assert.equal(readerDisclosureOpen("message:a:turn", expanded, collapsed, false), true);
 assert.equal(readerDisclosureOpen("call:b:tool", expanded, collapsed, true), false);
 assert.equal(readerDisclosureOpen("section:other", expanded, collapsed, true), true);
 const closed = readerDisclosureToggle("message:a:turn", false, ["message:a:turn", "section:z"], []);
 assert.deepEqual(plain(closed), { expanded: ["section:z"], collapsed: ["message:a:turn"] }, "a toggle moves its identity between the lists");
 assert.deepEqual(plain(readerDisclosureToggle("message:c", true, ["section:z"], ["message:c"])), { expanded: ["message:c", "section:z"], collapsed: [] }, "lists stay sorted");
 for (const state of [undefined, null, {}]) for (const fallback of [true, false]) assert.equal(readerContentShown(state, "m-1", fallback), fallback);
 assert.equal(readerContentShown({ "m-1": false }, "m-1", true), false, "an explicit false overrides an open default");
 assert.equal(readerContentShown({ "m-1": true }, "m-1", false), true);
 assert.equal(readerContentShown({ "m-1": true }, "toString", false), false, "only the state's own keys count");
});


test("reader legend keys isolate values and categories, combine choices, and allow hiding everything", () => {
  const all = ['prompt', 'reply', 'read', 'edit'], tools = ['read', 'edit'];
  assert.deepEqual(updateReaderKeys(all, all, tools, 'only'), tools);
  assert.deepEqual(updateReaderKeys(all, ['read'], tools, 'toggle'), tools);
  assert.deepEqual(updateReaderKeys(all, ['prompt', 'read'], tools, 'toggle'), ['prompt', 'read', 'edit']);
  assert.deepEqual(updateReaderKeys(all, tools, tools, 'toggle'), []);
  assert.deepEqual(updateReaderKeys(all, [], ['reply'], 'toggle'), ['reply']);
  assert.deepEqual(updateReaderKeys(all, ['reply'], tools, 'exclude'), ['prompt', 'reply']);
  assert.deepEqual(updateReaderKeys(all, [], tools, 'clear'), all);
  assert.deepEqual(updateReaderKeys(all, ['prompt', 'read'], tools, 'show'), ['prompt', 'read', 'edit']);
  assert.deepEqual(updateReaderKeys(all, ['prompt', 'read'], tools, 'hide'), ['prompt']);
  assert.deepEqual(updateReaderKeys(all, all, tools, 'show'), all, 'Show keeps visible values visible');
  assert.deepEqual(updateReaderKeys(all, ['prompt'], tools, 'hide'), ['prompt'], 'Hide keeps hidden values hidden');
  assert.deepEqual(readerKeyState(all, ['read'], tools), {active:false,mixed:true,dimmed:false,selectionActive:true,soleSelection:false,visibleCount:1,hiddenCount:1});
  assert.equal(readerKeyState(all, [], tools).dimmed, true);
  assert.equal(readerKeyState(all, all, tools).selectionActive, false);
  assert.deepEqual(readerLineScopes(['read'], {label:'Read',values:['read']}), [{label:'Specific activity',values:['read']}]);
  assert.deepEqual(readerLineScopes(['read'], {label:'Read',values:['read','edit']}), [
    {label:'Specific activity',values:['read']}, {label:'Category: Read',values:['read','edit']},
  ]);
  assert.deepEqual(readerLineScopes(['read'], {label:'Read',values:[]}), [{label:'Specific activity',values:['read']}]);
  assert.equal(readerLineOptionLabel('hide', undefined, 1, 0), 'Hide this activity');
  assert.equal(readerLineOptionLabel('only', 'Read', 2, 0), 'Show only the Read category');
  assert.equal(readerLineOptionLabel('show', 'Read', 1, 2), 'Show 2 hidden labels in the Read category');
  assert.equal(readerLineOptionLabel('hide', 'Read', 1, 2), 'Hide 1 visible label in the Read category');
  const hidden = ['events:event-friction', 'labels:P.frame', 'writing'], available = ['writing', 'events:event-learning'];
  const exclusions = shown => plain(reader.readerHighlightExclusions(hidden, available, shown));
  assert.deepEqual(exclusions(available), ['events:event-friction', 'labels:P.frame'], 'adding writing preserves keys absent from this conversation');
  assert.deepEqual(exclusions([]), [...hidden, 'events:event-learning'], 'hiding available keys retains unrelated exclusions');
  assert.deepEqual(hidden, ['events:event-friction', 'labels:P.frame', 'writing']);
});


test("whole-key presets cover content, highlights, activity and timeline",()=>{
 const all=plain(reader.readerKeyPresetValues('all'));
 const content=plain(reader.readerKeyPresetValues('content'));
 const quiet=plain(reader.readerKeyPresetValues('quiet'));
 assert.deepEqual(all.reader_filter,allKinds());
 assert.deepEqual(quiet.reader_filter,allKinds().filter(id=>id!=='usage'));
 assert.deepEqual([all.reader_writing,all.reader_events,all.reader_labels],[true,true,true]);
 assert.deepEqual([content.reader_writing,content.reader_events,content.reader_labels],[false,false,false]);
 assert.deepEqual(content.timeline_visibility,{sections:{values:[],excluded:false,allHidden:true}});
 for(const values of [all,content,quiet]){
  assert.deepEqual(values.reader_hidden_tags,[]);assert.deepEqual(values.reader_highlight_hidden,[]);
  const state={level:values.reader_filter_level,custom:values.reader_filter};
  assert.equal(reader.readerKeyPresetID(state,values,[],[],values.timeline_visibility),values===all?'all':values===content?'content':'quiet');
  assert.equal(reader.readerKeyPresetID(state,values,[],['hidden'],values.timeline_visibility),null);
 }
 assert.equal(reader.readerKeyPresetID({level:'all'},all,[],[],content.timeline_visibility),null);
 assert.equal(reader.readerKeyPresetID({level:'all'},all,['labels:C.report'],[],{}),null);
 assert.match(reader.readerKeyLastNote(content),/All content shown.*Text highlighting shown: none.*Timeline elements hidden/);
 assert.match(reader.readerKeyLastNote(quiet),/Hidden: Usage & quota/);
 assert.equal(reader.readerKeyPresetValues('last'),null);
 assert.equal(reader.readerKeyPresetValues('unknown'),null);
});
