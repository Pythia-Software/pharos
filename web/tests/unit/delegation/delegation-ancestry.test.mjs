import test from "node:test";
import assert from "node:assert/strict";

const { activeLaneRows, laneCoverage, launchAttemptNote, segmentPosition, sessionAncestors } = await import("../../../src/delegation-ancestry");
const { timelineSessionBranches } = await import("../../../src/timeline-label-model");
const { delegationAliasRequests, normalizeDelegationView, normalizeDisclosureAliases, resolveSessionAlias } = await import("../../../src/delegation-aliases");

// Rows carry no numbering: parentage is parent_id and the explicit chain.
const row = (id, parent_id, start, end, extra = {}) => ({ id, parent_id, start, end, depth: 0, child_count: 0, ancestry: { state: parent_id ? "resolved" : "root" }, ...extra });

test("an inactive parent stays while a descendant is active, by ID rather than position", () => {
  const rows = [row("root", undefined, 0, 10, { child_count: 1 }), row("mid", "root", 0, 10, { child_count: 1 }), row("leaf", "mid", 50, 60), row("other", undefined, 0, 10)];
  assert.deepEqual(activeLaneRows(rows, rows, [40, 70]).map(n => n.id), ["root", "mid", "leaf"]);
  assert.deepEqual(activeLaneRows(rows, rows, [0, 5]).map(n => n.id), ["root", "mid", "other"]);
  // Siblings whose display labels would share a prefix ("1" and "1.1" vs "11") cannot leak into each other.
  const lookalike = [row("a", undefined, 0, 1, { child_count: 1 }), row("a-child", "a", 50, 60), row("aa", undefined, 0, 1)];
  assert.deepEqual(activeLaneRows(lookalike, lookalike, [40, 70]).map(n => n.id), ["a", "a-child"]);
});

test("an explicit ancestor chain covers parents beyond the page, and lane order is kept", () => {
  const rows = [row("grand", undefined, 0, 1), row("leaf", "parent", 50, 60, { ancestor_ids: ["parent", "grand"] })];
  assert.deepEqual(sessionAncestors(rows[1], new Map()), ["parent", "grand"]);
  const shown = activeLaneRows(rows, rows, [40, 70]);
  assert.deepEqual(shown.map(n => n.id), ["grand", "leaf"]);
  assert.deepEqual(laneCoverage(rows, shown), { omitted: ["parent"], unresolved: [] });
  // Lanes are capped separately from the rows that decide activity.
  assert.deepEqual(activeLaneRows(rows, [rows[0]], [40, 70]).map(n => n.id), ["grand"]);
});

test("a folded or paginated subtree keeps its parent through the subtree span", () => {
  const folded = row("parent", undefined, 0, 1, { child_count: 2, subtree_start: 0, subtree_end: 60 });
  assert.deepEqual(activeLaneRows([folded], [folded], [40, 70]).map(n => n.id), ["parent"]);
  // With every child loaded, the children decide.
  const children = [row("c1", "parent", 0, 1), row("c2", "parent", 2, 3)];
  assert.deepEqual(activeLaneRows([folded, ...children], [folded, ...children], [40, 70]), []);
});

test("unresolved ancestry is reported and never borrows a parent", () => {
  const rows = [row("root", undefined, 0, 1, { child_count: 1 }), row("orphan", undefined, 50, 60, { ancestry: { state: "missing" } }), row("tied", "root", 50, 60, { ancestry: { state: "ambiguous" } }), row("loop", "loop-b", 50, 60, { ancestry: { state: "cycle" } }), row("loop-b", "loop", 50, 60, { ancestry: { state: "cycle" } })];
  const shown = activeLaneRows(rows, rows, [40, 70]);
  assert.deepEqual(shown.map(n => n.id), ["root", "orphan", "tied", "loop", "loop-b"], "a cycle terminates");
  assert.deepEqual(laneCoverage(rows, shown).unresolved, ["orphan", "tied", "loop", "loop-b"]);
  assert.equal(timelineSessionBranches(rows).get("orphan").parentDepth, undefined);
});

test("session tree branches join only resolved parent IDs", () => {
  const rows = [{ id: "root", depth: 0 }, { id: "a", depth: 1, parent_id: "root" }, { id: "b", depth: 1 }];
  const branches = timelineSessionBranches(rows);
  assert.equal(branches.get("a").parentDepth, 0);
  assert.equal(branches.get("b").parentDepth, undefined, "an indented row without a parent ID has no inferred parent");
  assert.equal(branches.get("root").children, true);
});

const aliases = { "old-launch": "child", "old-copy": "child", "split-id": null };
const params = values => new URLSearchParams(Object.entries(values).map(([key, value]) => [key, typeof value === "string" ? value : JSON.stringify(value)]));

test("URL state names every session it references for resolution", () => {
  const url = params({ delegation_session: "old-launch", delegation_collapsed: "a,b", delegation_expanded: ["c"], delegation_relatives: ["d"], disclosures: { "delegation-labels:ancestor-conversation:x:session": true, "work:w:files": true } });
  assert.deepEqual(delegationAliasRequests(url), ["a", "ancestor-conversation", "ancestor-conversation:x", "b", "c", "d", "old-launch"]);
  assert.deepEqual(delegationAliasRequests(params({ delegation_expanded: "not json" })), [], "malformed state is not requested");
});

test("legacy selection, folded and expanded sessions restore on their execution", () => {
  assert.equal(resolveSessionAlias("old-launch", aliases), "child");
  assert.equal(resolveSessionAlias("split-id", aliases), "split-id", "a split alias is never applied to a chosen execution");
  assert.equal(resolveSessionAlias("unknown", aliases), "unknown");
  const changes = normalizeDelegationView(params({ delegation_session: "old-launch", delegation_collapsed: "old-copy,old-launch,split-id,z", delegation_expanded: ["old-copy", "child", "unrelated"], delegation_relatives: ["old-launch", "split-id"] }), aliases);
  assert.deepEqual(changes, { delegation_session: "child", delegation_collapsed: "child,split-id,z", delegation_expanded: JSON.stringify(["child", "unrelated"]), delegation_relatives: JSON.stringify(["child", "split-id"]) });
  assert.equal(normalizeDelegationView(params({ delegation_session: "child", delegation_collapsed: "z" }), aliases), null, "canonical state is unchanged");
  assert.equal(normalizeDelegationView(params({ delegation_session: "split-id" }), aliases), null, "unresolved selection stays in the URL");
});

test("session disclosures prefer the canonical key, let collapse win, and keep unrelated keys", () => {
  const key = id => `delegation-labels:${id}:session`;
  assert.deepEqual(normalizeDisclosureAliases({ [key("old-launch")]: true, [key("old-copy")]: false, "work:w:files": true }, aliases), { "work:w:files": true, [key("child")]: false });
  assert.deepEqual(normalizeDisclosureAliases({ [key("old-launch")]: false, [key("child")]: true }, aliases), { [key("child")]: true });
  assert.deepEqual(normalizeDisclosureAliases({ [key("old-launch")]: true, "delegation-labels:old-copy:mode:1": true }, aliases), { [key("child")]: true, "delegation-labels:child:mode:1": true });
  const unresolved = { [key("split-id")]: true, [key("unknown")]: false };
  assert.deepEqual(normalizeDisclosureAliases(unresolved, aliases), unresolved);
  const colon = { "delegation-labels:ancestor-conversation:x:session": true };
  assert.deepEqual(normalizeDisclosureAliases(colon, { "ancestor-conversation:x": "parent" }), { [key("parent")]: true });
  // A moved short ID never captures a current session whose ID extends it.
  assert.deepEqual(normalizeDisclosureAliases(colon, { "ancestor-conversation": "parent", "ancestor-conversation:x": "ancestor-conversation:x" }), colon);
  assert.deepEqual(normalizeDisclosureAliases(colon, { "ancestor-conversation": "parent", "ancestor-conversation:x": null }), { "delegation-labels:parent:x:session": true }, "an unresolved longer prefix is not a session");
  const changes = normalizeDelegationView(params({ disclosures: { [key("old-launch")]: true } }), aliases);
  assert.deepEqual(JSON.parse(changes.disclosures), { [key("child")]: true });
});

test("launch attempts are summarized by outcome, never as child sessions", () => {
  assert.equal(launchAttemptNote(undefined), "");
  assert.equal(launchAttemptNote([{ id: "a", outcome: "unresolved" }]), "1 launch without an outcome");
  assert.equal(launchAttemptNote([{ id: "a", outcome: "rejected" }, { id: "b", outcome: "rejected" }, { id: "c", outcome: "unresolved" }]), "2 launches rejected · 1 launch without an outcome");
});

test("each continuation segment knows its neighbours, wherever they are retained", () => {
  const segments = [{ conversation_id: "a", workspace_id: "w1" }, { conversation_id: "b", workspace_id: "w2" }, { conversation_id: "c", workspace_id: "w1" }];
  assert.deepEqual(segmentPosition(segments, "b"), { index: 1, total: 3, previous: segments[0], next: segments[2] });
  assert.deepEqual(segmentPosition(segments, "a"), { index: 0, total: 3, previous: undefined, next: segments[1] });
  assert.equal(segmentPosition(segments, "elsewhere"), null);
  assert.equal(segmentPosition([segments[0]], "a"), null, "a single transcript has no segments to navigate");
  assert.equal(segmentPosition(undefined, "a"), null);
});
