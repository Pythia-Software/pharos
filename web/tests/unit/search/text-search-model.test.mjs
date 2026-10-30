import assert from "node:assert/strict";
import test from "node:test";
import * as m from "../../../src/text-search-model";
const term = text => ({ op: "term", text });

test("new keyword searches cover the last 30 elapsed days and keep that bound across batches and links", () => {
  const clock = new Date("2026-11-15T12:34:56.789Z");
  const draft = m.newKeywordSearch(clock);
  assert.equal(draft.scope.from, "2026-10-16T12:34:56.789Z");
  assert.equal(draft.scope.to, "");
  assert.equal(m.readTextSearch(new URLSearchParams(), clock).draft.scope.from, draft.scope.from);
  draft.root.children[0].text = "migration";
  assert.equal(m.searchRequest(draft).request.from, draft.scope.from);
  assert.match(m.sentenceText(m.searchSentence(draft).parts), /in the last 30d/);
  for (const cursor of ["", "next-batch"]) {
    assert.equal(new URLSearchParams(m.textFindParameters(draft, cursor)).get("find_from"), draft.scope.from);
  }
  const restored = m.readTextSearch(new URLSearchParams({ text_builder: JSON.stringify(draft) }), new Date("2027-01-01"));
  assert.equal(restored.error, "");
  assert.equal(restored.draft.scope.from, draft.scope.from);
  assert.deepEqual(m.searchDateWindow(restored.draft), m.searchDateWindow(draft));
  assert.match(m.sentenceText(m.searchSentence(restored.draft).parts), /in the last 30d/);
});

test("collapsed readings omit only the default date and retain custom conditions", () => {
  const draft = m.newKeywordSearch(new Date("2026-11-15T12:34:56.789Z"));
  const collapsed = draft => m.searchSentence(draft, { hideDefaultDate: true });
  assert.equal(collapsed(draft).empty, true);
  assert.equal(m.searchSentence(draft).empty, false, "expanded readings show the default");
  draft.root.children[0].text = "migration";
  draft.scope.provider = "claude";
  assert.equal(m.sentenceText(collapsed(draft).parts), "Find conversation messages from Claude, with the word “migration”");
  assert.equal(m.searchRequest(draft).request.from, "2026-10-16T12:34:56.789Z", "hiding the date preserves its restriction");

  const custom = m.withSearchDateWindow(draft, "7d");
  assert.match(m.sentenceText(collapsed(custom).parts), /in the last 7d/);
  draft.scope.from = "2026-10-01";
  assert.match(m.sentenceText(collapsed(draft).parts), /on or after Oct 1, 2026/);
  assert.equal(m.searchDateWindow(draft), undefined, "a manually edited date is explicit");
  draft.scope.from = "2026-10-16T12:34:56.789Z"; draft.scope.to = "2026-11-01";
  assert.match(m.sentenceText(collapsed(draft).parts), /between .* and Nov 1, 2026/);
});

test("relative date edits use duration syntax and preserve their resolved scope", () => {
  const clock = new Date("2026-11-15T12:34:56.789Z");
  let draft = m.newKeywordSearch(clock);
  draft.root.children[0].text = "migration";
  draft.scope.to = "2026-11-01";
  for (const [duration, from] of [["7d", "2026-11-08T12:34:56.789Z"], ["24h", "2026-11-14T12:34:56.789Z"], ["1d2h", "2026-11-14T10:34:56.789Z"]]) {
    const edited = m.withSearchDateWindow(draft, duration, clock);
    assert.equal(edited.scope.from, from);
    assert.equal(edited.scope.to, "");
    const restored = m.readTextSearch(new URLSearchParams({ text_builder: JSON.stringify(edited) }), new Date("2027-01-01"));
    assert.equal(restored.error, "");
    assert.equal(m.searchRequest(restored.draft).request.from, from);
    assert.equal(m.searchDateWindow(restored.draft).duration, duration);
  }
  for (const duration of ["", "garbage", "0d", "-7d", "+7d", "999999999999999999d"]) {
    const edited = m.withSearchDateWindow(draft, duration, clock);
    assert.equal(m.searchRequest(edited).request, undefined, duration);
    assert.equal(m.searchRequest(edited).issues[0].id, "dates", duration);
  }
  draft.dateWindow = { duration: 30, from: draft.scope.from };
  assert.ok(m.readTextSearch(new URLSearchParams({ text_builder: JSON.stringify(draft) })).error);
});

test("date comparisons switch between last, on or after, and on or before using calendar dates", () => {
  const clock = new Date("2026-11-15T12:34:56.789Z");
  const draft = m.newKeywordSearch(clock);
  draft.root.children[0].text = "migration";
  const before = m.withSearchDateComparison(draft, "from", "to", clock);
  assert.equal(before.scope.from, "");
  assert.equal(before.scope.to, "2026-10-16");
  assert.equal(m.searchDateWindow(before), undefined);
  const request = m.searchRequest(before).request;
  assert.equal(request.from, ""); assert.equal(request.to, "2026-10-16");
  assert.match(m.sentenceText(m.searchSentence(before, { hideDefaultDate: true }).parts), /on or before Oct 16, 2026/);
  const params = new URLSearchParams(m.textFindParameters(before));
  assert.equal(params.has("find_from"), false); assert.equal(params.get("find_to"), "2026-10-16");
  const restored = m.readTextSearch(new URLSearchParams({ text_builder: JSON.stringify(before) }));
  assert.equal(restored.error, ""); assert.equal(restored.draft.scope.to, "2026-10-16");

  const after = m.withSearchDateComparison(before, "to", "from", clock);
  assert.equal(after.scope.from, "2026-10-16"); assert.equal(after.scope.to, "");
  assert.equal(m.searchDateWindow(after), undefined, "explicit dates stay visible in collapsed summaries");
  const absolute = m.withSearchDateComparison(draft, "from", "from", clock);
  assert.equal(absolute.scope.from, "2026-10-16"); assert.equal(m.searchDateWindow(absolute), undefined);
  const relative = m.withSearchDateComparison(before, "to", "last", clock);
  assert.equal(relative.scope.from, draft.scope.from); assert.equal(relative.scope.to, "");
  assert.equal(m.searchDateWindow(relative).duration, "30d");
});

test("date comparison edits preserve both bounds of an explicit range", () => {
  const draft = m.emptyTextSearch();
  draft.root.children[0].text = "migration";
  draft.scope.from = "2026-10-01"; draft.scope.to = "2026-10-15";
  for (const field of ["from", "to"]) {
    for (const comparison of ["last", "from", "to"]) {
      assert.deepEqual(m.withSearchDateComparison(draft, field, comparison).scope, draft.scope);
    }
  }
  assert.equal(m.searchRequest(draft).request.from, "2026-10-01");
  assert.equal(m.searchRequest(draft).request.to, "2026-10-15");
});

test("adding either date bound keeps the other and produces a saved calendar range", () => {
  const draft = m.newKeywordSearch(new Date("2026-11-15T12:34:56.789Z"));
  draft.root.children[0].text = "migration";
  draft.scope.provider = "claude";
  const range = m.withSearchDateRange(draft);
  assert.equal(range.scope.from, "2026-10-16");
  assert.equal(m.searchDateWindow(range), undefined);
  range.scope.to = "2026-11-01";
  assert.equal(m.searchRequest(range).request.from, "2026-10-16");
  assert.equal(m.searchRequest(range).request.to, "2026-11-01");
  assert.equal(range.scope.provider, "claude");
  const params = new URLSearchParams(m.textFindParameters(range));
  assert.equal(params.get("find_from"), "2026-10-16"); assert.equal(params.get("find_to"), "2026-11-01");
  const restored = m.readTextSearch(new URLSearchParams({ text_builder: JSON.stringify(range) }));
  assert.equal(restored.error, ""); assert.deepEqual(restored.draft.scope, range.scope);
  assert.match(m.sentenceText(m.searchSentence(range, { hideDefaultDate: true }).parts), /between Oct 16, 2026 and Nov 1, 2026/);

  const before = m.withSearchDateComparison(draft, "from", "to");
  const beforeRange = m.withSearchDateRange(before);
  assert.equal(beforeRange.scope.from, ""); assert.equal(beforeRange.scope.to, "2026-10-16");
  beforeRange.scope.from = "2026-10-01";
  assert.equal(m.searchRequest(beforeRange).request.from, "2026-10-01");
  assert.equal(m.searchRequest(beforeRange).request.to, "2026-10-16");
  beforeRange.scope.from = "2026-10-17";
  assert.equal(m.searchRequest(beforeRange).request, undefined, "reversed ranges cannot be applied");
});

test("date inputs show UTC calendar dates while saved precise bounds retain their scope until edited", () => {
  for (const [value, date] of [["", ""], ["2026-10-16", "2026-10-16"], ["2026-10-16T12:34:56.789Z", "2026-10-16"], ["2026-10-15T23:59:59.999-07:00", "2026-10-16"]]) {
    assert.equal(m.searchDateValue(value), date);
  }
  const draft = m.emptyTextSearch(); draft.root.children[0].text = "migration";
  draft.scope.from = "2026-10-16T12:34:56.789Z";
  const restored = m.readTextSearch(new URLSearchParams({ text_builder: JSON.stringify(draft) }));
  assert.equal(m.searchDateValue(restored.draft.scope.from), "2026-10-16");
  assert.equal(m.searchRequest(restored.draft).request.from, draft.scope.from);
});

test("clearing the keyword date searches all time and preserves that choice in saved searches", () => {
  const draft = m.newKeywordSearch();
  draft.root.children[0].text = "migration";
  draft.scope.from = "";
  const restored = m.readTextSearch(new URLSearchParams({ text_builder: JSON.stringify(draft) }));
  assert.equal(restored.error, "");
  assert.equal(restored.draft.scope.from, "");
  assert.equal(m.searchDateWindow(restored.draft), undefined);
  assert.equal(new URLSearchParams(m.textFindParameters(restored.draft)).has("find_from"), false);
  for (const params of ["search=migration", "search=migration&text_from=2026-01-01", "text_from=&text_to="]) {
    const saved = m.readTextSearch(new URLSearchParams(params));
    assert.equal(saved.draft.scope.from, new URLSearchParams(params).get("text_from") ?? "");
  }
  assert.equal(m.emptyTextSearch().scope.from, "", "conversation find remains unrestricted");
});

test("legacy searches keep their fuzzy default through restoration and request serialization", () => {
  for (const [suffix, fuzzy] of [["", true], ["&fuzzy=0", false], ["&fuzzy=1", true]]) {
    const saved = m.readTextSearch(new URLSearchParams(`search=migration${suffix}`));
    assert.equal(saved.active, true); assert.equal(saved.error, "");
    assert.equal(saved.draft.fuzzy, fuzzy);
    assert.equal(new URLSearchParams(m.textFindParameters(saved.draft)).get("find_fuzzy"), fuzzy ? "1" : "0");
    const migrated = m.readTextSearch(new URLSearchParams({ text_builder: JSON.stringify(saved.draft) }));
    assert.equal(migrated.draft.fuzzy, fuzzy);
  }
  assert.equal(m.readTextSearch(new URLSearchParams()).draft.fuzzy, false);
  const current = m.emptyTextSearch(); current.root.children = [m.newRule("migration")];
  assert.equal(m.readTextSearch(new URLSearchParams({ text_builder: JSON.stringify(current), fuzzy: "1" })).draft.fuzzy, false);
});

test("rule text cannot inject Boolean or group syntax", () => {
  const d = m.emptyTextSearch(); d.root.children = [m.newRule("cache OR (buffer)")];
  assert.deepEqual(m.searchRequest(d).request.where, { op: "and", children: [term("cache"), term("OR"), term("(buffer)")] });
  const params = new URLSearchParams(m.textFindParameters(d));
  assert.equal(params.has("find"), false);
  assert.deepEqual(JSON.parse(params.get("find_where")), m.searchRequest(d).request.where);
});
test("nested any rules and exclusions have an explicit positive scope", () => {
  const d = m.emptyTextSearch();
  d.root.children = [{ ...m.newGroup(), join: "any", children: [m.newRule("cache"), m.newRule("buffer")] }, { ...m.newRule("race condition"), kind: "phrase" }, { ...m.newRule("redis"), exclude: true }];
  assert.deepEqual(m.searchRequest(d).request.where, { op: "not", children: [{ op: "and", children: [{ op: "or", children: [term("cache"), term("buffer")] }, { op: "phrase", text: "race condition" }] }, term("redis")] });
  d.root.children = [{ ...m.newRule("redis"), exclude: true }];
  assert.equal(m.searchRequest(d).request, undefined);
  assert.match(m.searchRequest(d).issues[0].message, /Include at least one/);
});
test("unfinished rules cannot silently weaken an applied search", () => {
  const d = m.emptyTextSearch(); d.root.children = [m.newRule("cache"), m.newRule()];
  assert.equal(m.searchRequest(d).request, undefined);
  d.root.children = [{ ...m.newRule("timeout"), kind: "pre", other: "retry", distance: "" }];
  assert.equal(m.searchRequest(d).request, undefined);
  d.root.children[0].distance = "0";
  assert.deepEqual(m.searchRequest(d).request.where, { op: "pre", distance: 0, children: [{ op: "phrase", text: "timeout" }, { op: "phrase", text: "retry" }] });
  d.scope.from = "2026-10-12"; d.scope.to = "2026-10-01";
  assert.equal(m.searchRequest(d).request, undefined);
});
test("named roles select compatible surfaces instead of silently intersecting prose", () => {
  let s = m.chooseScope(m.emptyScope(), "system");
  assert.equal(s.surface, "all"); assert.deepEqual(s.roles, ["system"]);
  s = m.chooseScope(s, "user"); assert.equal(s.surface, "user"); assert.deepEqual(s.roles, []);
});
test("saved visual searches round trip with filters and stable semantics", () => {
  const d = m.emptyTextSearch(); d.root.children = [{ ...m.newRule("cache buffer"), kind: "anyWords" }];
  d.scope.provider = "claude"; d.scope.from = "2026-10-01"; d.caseSensitive = true;
  const restored = m.readTextSearch(new URLSearchParams({ text_builder: JSON.stringify(d) }));
  assert.equal(restored.error, ""); assert.equal(restored.active, true);
  assert.deepEqual(m.searchRequest(restored.draft), m.searchRequest(d));
  assert.ok(m.readTextSearch(new URLSearchParams({ text_builder: "broken" })).error);
  d.scope.where = term("hidden");
  const sanitized = m.readTextSearch(new URLSearchParams({ text_builder: JSON.stringify(d) }));
  assert.deepEqual(m.searchRequest(sanitized.draft).request.where, { op: "or", children: [term("cache"), term("buffer")] });
});
test("plain home searches treat operator words literally, old syntax links retain syntax", () => {
  const plain = m.readTextSearch(new URLSearchParams({ search: "cache OR buffer", text_plain: "1" }));
  assert.equal(plain.draft.editor, "visual"); assert.equal(m.searchRequest(plain.draft).request.where.children[1].text, "OR");
  const phrase = m.readTextSearch(new URLSearchParams({ search: "race condition", text_plain: "phrase" }));
  assert.deepEqual(m.searchRequest(phrase.draft).request.where, { op: "phrase", text: "race condition" });
  const old = m.readTextSearch(new URLSearchParams({ search: "cache OR buffer" }));
  assert.equal(old.draft.editor, "syntax"); assert.equal(m.searchRequest(old.draft).request.query, "cache OR buffer");
});
test("syntax import preserves nested exclusion and quoted prefix export", () => {
  const q = { op: "not", children: [{ op: "or", children: [term("cache"), term("buffer")] }, { op: "prefix", text: "migrat" }] };
  const d = { ...m.emptyTextSearch(), ...m.visualFromQuery(q) };
  assert.deepEqual(m.searchRequest(d).request.where, q);
  assert.match(m.querySyntax(q), /"migrat"\*/);
  assert.equal(m.querySyntax(term("cache-timeout"), true), "cache-timeout");
  assert.equal(m.querySyntax(term("OR"), true), '"OR"');
  assert.throws(() => m.querySyntax(term("(cache)"), true), /preserve its meaning/);
  assert.throws(() => m.visualFromQuery({ op: "near", distance: 2, children: [{ op: "prefix", text: "foo" }, term("bar")] }), /preserve its meaning/);
});

test("the English reading names every condition, filter, and option in plain words", () => {
  const read = (draft, context) => m.sentenceText(m.searchSentence(draft, context).parts);
  const d = m.emptyTextSearch();
  assert.equal(read(d), "Find conversation messages"); assert.equal(m.searchSentence(d).empty, true);
  d.root.children[0].text = "parser migration";
  d.root.children.push({ ...m.newRule("race condition"), kind: "phrase" }, { ...m.newRule("flaky test"), exclude: true });
  assert.equal(read(d), "Find conversation messages with all of the words “parser migration” and the phrase “race condition”, but not the words “flaky test”");
  d.scope.provider = "claude"; d.scope.from = "2026-10-01"; d.caseSensitive = true;
  assert.equal(read(d, { workspace: ["Repository contains pharos"] }), "Find conversation messages from Claude, on or after Oct 1, 2026, in workspaces where repository contains pharos, with all of the words “parser migration” and the phrase “race condition”, but not the words “flaky test”, matching letter case");
  const any = m.emptyTextSearch(); any.root.join = "any"; any.root.children[0].text = "cache";
  any.root.children.push(m.newRule("buffer"), { ...m.newGroup(), children: [{ ...m.newRule("deadlock"), kind: "near", other: "mutex", distance: "5" }, m.newRule("lock")] });
  assert.equal(read(any), "Find conversation messages with the word “cache”, the word “buffer”, or (“deadlock” within 5 words of “mutex” and the word “lock”)");
  const syntax = m.emptyTextSearch(); syntax.editor = "syntax"; syntax.text = "cache OR buffer";
  assert.equal(read(syntax), "Find conversation messages matching cache OR buffer");
  assert.equal(read(m.emptyTextSearch(), { kind: "file", file: "parser.go", fileCase: true }), "Find modified files with paths containing “parser.go”, matching letter case");
  const fixed = m.emptyTextSearch(); fixed.scope.conversation_id = "c1"; fixed.root.children[0].text = "x";
  assert.equal(read(fixed, { fixedContext: true }), "Find conversation messages with the word “x”");
});

test("exact text and patterns read exactly as typed, and blank text is not a search", () => {
  const read = draft => m.searchSentence(draft);
  const literal = { ...m.emptyTextSearch(), match: "literal", text: " foo::bar " };
  assert.equal(m.sentenceText(read(literal).parts), "Find conversation messages containing the exact text “ foo::bar ”");
  assert.equal(m.searchRequest(literal).request.where.text, " foo::bar ");
  const pattern = { ...m.emptyTextSearch(), match: "regex", text: " error[0-9]+" };
  assert.equal(m.sentenceText(read(pattern).parts), "Find conversation messages matching the pattern  error[0-9]+");
  for (const blank of [{ match: "literal", text: "   " }, { match: "regex", text: " " }, { editor: "syntax", text: "  " }]) {
    const draft = { ...m.emptyTextSearch(), ...blank };
    assert.equal(read(draft).empty, true, JSON.stringify(blank));
    assert.equal(m.searchRequest(draft).request, undefined); assert.equal(m.searchRequest(draft).issues[0].id, "text");
  }
});

test("matching options are read, counted, and sent only where they change the search", () => {
  const options = draft => m.sentenceText(m.searchSentence(draft).parts).split(", ").slice(1);
  const on = { separators: true, fuzzy: true, caseSensitive: true, text: '"race condition" cache' };
  for (const [editor, mode, separators, fuzzy] of [["syntax", "advanced", true, true], ["syntax", "terms", true, true], ["syntax", "phrase", true, false], ["syntax", "literal", false, false], ["syntax", "regex", false, false]]) {
    const draft = { ...m.emptyTextSearch(), ...on, editor, mode };
    assert.deepEqual(m.optionScope(draft), { separators, fuzzy }, mode);
    const request = m.searchRequest(draft).request;
    assert.deepEqual([request.separators, request.fuzzy, request.case_sensitive], [separators, fuzzy, true], mode);
    assert.equal(options(draft).includes("keeping phrase punctuation"), separators, mode);
    assert.equal(options(draft).includes("including word variations"), fuzzy, mode);
  }
  const literal = { ...m.emptyTextSearch(), ...on, match: "literal" };
  assert.deepEqual(m.optionsInEffect(literal), { caseSensitive: true, separators: false, fuzzy: false });
  assert.deepEqual(options(literal), ["matching letter case"]);
});

test("only a withheld batch offers a retry of the same request", () => {
  assert.equal(m.withheldBatch({ stop_reason: "consistency_check_timeout" }), true);
  for (const stop_reason of ["complete", "time_budget", "time_budget_no_progress", "page_limit"]) assert.equal(m.withheldBatch({ stop_reason }), false);
  assert.equal(m.withheldBatch(undefined), false);
});

test("the scope note says when a word search leaves tool output out, and when a literal search needs a scope", () => {
  const draft = (change = {}, scope = {}) => ({ ...m.emptyTextSearch(), ...change, scope: { ...m.emptyScope(), ...scope } });
  assert.equal(m.textScopeNote(draft({}, { surface: "tools" })), "tool-output");
  assert.equal(m.textScopeNote(draft({}, { surface: "all", roles: ["tool"] })), "tool-output");
  assert.equal(m.textScopeNote(draft({}, { surface: "all", workspace_id: "w" })), "tool-output");
  assert.equal(m.textScopeNote(draft({}, { surface: "all", roles: ["system"] })), null);
  assert.equal(m.textScopeNote(draft({}, { surface: "tools", conversation_id: "c" })), null);
  assert.equal(m.textScopeNote(draft({}, { surface: "prose" })), null);
  // Literal and regex searches scan stored text, tool output included.
  for (const literal of [{ match: "literal" }, { match: "regex" }, { editor: "syntax", mode: "regex" }]) {
    assert.equal(m.textScopeNote(draft(literal, { surface: "tools" })), "scan");
    assert.equal(m.textScopeNote(draft(literal, { surface: "tools", conversation_id: "c" })), null);
  }
  assert.equal(m.textScopeNote(draft({ editor: "syntax", mode: "advanced" }, { surface: "all" })), "tool-output");
});


test("Today search saves calendar intent and retains execution bounds across cursor batches", () => {
  const clock = new Date(2026, 10, 1, 9, 30);
  const draft = m.withSearchDateComparison(m.newKeywordSearch(clock), "from", "today", clock);
  draft.root.children[0].text = "migration";
  const midnight = new Date(clock); midnight.setHours(0, 0, 0, 0);
  assert.equal(draft.scope.from, midnight.toISOString());
  assert.equal(draft.scope.to, clock.toISOString());
  assert.match(m.sentenceText(m.searchSentence(draft).parts), /today.*local time/);
  const tomorrow = new Date(2026, 10, 2, 9);
  const restored = m.readTextSearch(new URLSearchParams({ text_builder: JSON.stringify(draft) }), tomorrow);
  assert.equal(restored.error, "");
  for (const cursor of ["", "next"]) {
    const params = new URLSearchParams(m.textFindParameters(restored.draft, cursor));
    assert.equal(params.get("find_from"), draft.scope.from);
    assert.equal(params.get("find_to"), draft.scope.to);
  }
  const renewed = m.refreshSearchToday(restored.draft, tomorrow);
  assert.notEqual(renewed.scope.from, draft.scope.from);
  assert.equal(renewed.scope.to, tomorrow.toISOString());
  assert.equal(m.refreshSearchToday(draft, new Date(clock.getTime()+1000), true), draft);
  const manual = { ...draft, scope: { ...draft.scope, from: "2026-01-01" } };
  assert.equal(m.refreshSearchToday(manual, tomorrow), manual);
  assert.equal(m.searchDateWindow(m.withSearchDateComparison(draft, "from", "last", clock)).duration, "30d");
});
