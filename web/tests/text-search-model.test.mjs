import assert from "node:assert/strict";
import test from "node:test";
import { build } from "esbuild";
const built = await build({ entryPoints: ["src/text-search-model.ts"], bundle: true, write: false, platform: "node", format: "esm" });
const m = await import(`data:text/javascript;base64,${Buffer.from(built.outputFiles[0].text).toString("base64")}`);
const term = text => ({ op: "term", text });

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
