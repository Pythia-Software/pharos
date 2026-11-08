import test from "node:test";
import assert from "node:assert/strict";

const { sessionOutsideWindow, laneCoverage, launchAttemptNote, segmentPosition, sessionAncestors } = await import("../../../src/delegation-ancestry");
const { timelineSessionBranches } = await import("../../../src/timeline-label-model");
const { delegationAliasRequests, normalizeDelegationView, normalizeDisclosureAliases, resolveSessionAlias } = await import("../../../src/delegation-aliases");

// Rows carry no numbering: parentage is parent_id and the explicit chain.
const row = (id, parent_id, start, end, extra = {}) => ({ id, parent_id, start, end, depth: 0, child_count: 0, ancestry: { state: parent_id ? "resolved" : "root" }, ...extra });

test("the selected window blanks only sessions or branches known to be outside it", () => {
  const parent = row("parent", undefined, 0, 10, {child_count: 1, subtree_start: 0, subtree_end: 60});
  assert.equal(sessionOutsideWindow(parent, [40, 70]), true);
  assert.equal(sessionOutsideWindow(parent, [40, 70], true), false, "a child can overlap an inclusive branch");
  assert.equal(sessionOutsideWindow(parent, [70, 80], true), true);
  assert.equal(sessionOutsideWindow({...parent, subtree_end: null}, [70, 80], true), false, "unknown branch bounds remain visible");
  assert.equal(sessionOutsideWindow(row("unknown", undefined, null, null), [40, 70]), false);
  assert.equal(sessionOutsideWindow(row("edge", undefined, 10, 40), [40, 70]), false, "window boundaries are inclusive");
  assert.equal(sessionOutsideWindow(parent, null), false);
});

test("coverage reports missing and unresolved ancestors for retained lanes", () => {
  const rows = [row("grand", undefined, 0, 1), row("leaf", "parent", 50, 60, { ancestor_ids: ["parent", "grand"] }), row("orphan", undefined, null, null, { ancestry: { state: "missing" } })];
  assert.deepEqual(sessionAncestors(rows[1], new Map()), ["parent", "grand"]);
  assert.deepEqual(laneCoverage(rows, rows), { omitted: ["parent"], unresolved: ["orphan"] });
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
