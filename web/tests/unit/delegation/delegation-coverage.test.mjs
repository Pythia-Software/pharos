import test from "node:test";
import assert from "node:assert/strict";

const { parentNote, coverageSummary, sumMetric, qualityMark, occurrenceNote, omittedSegmentsNote, sessionCountText, timelineEventsPending, timelineEventsPendingNote } = await import("../../../src/delegation-coverage");
const { readerSelection, unresolvedAliases } = await import("../../../src/delegation-aliases");

const row = (extra) => ({ id: "s", start: 0, end: 1, ...extra });

test("only a recorded or verified parent is presented without qualification", () => {
  assert.equal(parentNote(row({})), "");
  assert.equal(parentNote(row({ parent_id: "p", ancestry: { state: "resolved", depth: 1 }, parent_certainty: "native" })), "");
  assert.equal(parentNote(row({ parent_id: "p", ancestry: { state: "resolved", depth: 1 }, parent_certainty: "derived", parent_evidence: "exact-delegation-handle" })), "");
  // A stored link of unknown provenance is not verified ancestry.
  assert.equal(parentNote(row({ parent_id: "p", ancestry: { state: "resolved" }, parent_certainty: "legacy" })), "Parent not verified");
  assert.equal(parentNote(row({ parent_id: "p", ancestry: { state: "resolved" }, parent_certainty: "derived", parent_evidence: "inferred-command-launch" })), "Parent inferred from a command");
  // Unresolved ancestry wins over any certainty; a missing parent is not a root.
  for (const state of ["missing", "ambiguous", "cycle", "conflict"]) assert.equal(parentNote(row({ ancestry: { state } })), "Parent unresolved");
});

test("coverage counts what the snapshot could not establish and explains missing capabilities", () => {
  assert.deepEqual(coverageSummary(undefined), { counts: "", notes: [] });
  const summary = coverageSummary({ missing: ["typed-execution-evidence", "future-capability"], unresolved_parents: 2, unverified_parents: 1, conflicts: 0, unqueried: 1 });
  assert.match(summary.counts, /^2 sessions have unresolved parents · 1 parent link is not verified · 1 session has activity in another workspace/);
  assert.equal(summary.notes.length, 2);
  assert.match(summary.notes[1], /future-capability/, "an unfamiliar capability is still reported");
});

test("a sum over sessions stays partial or unknown rather than counting unknown usage as zero", () => {
  const m = (value, quality = "reported") => ({ value, quality });
  assert.deepEqual(sumMetric([m(null, "unknown"), m(null, "unknown")]), { value: null, partial: false });
  assert.deepEqual(sumMetric([m(10), m(null, "unknown")]), { value: 10, partial: true });
  assert.deepEqual(sumMetric([m(10), m(5, "partial")]), { value: 15, partial: true });
  assert.deepEqual(sumMetric([m(10), m(5)]), { value: 15, partial: false });
});

test("a saved selection the server could not resolve is never replaced by another session", () => {
  const current = [{ id: "a" }, { id: "b" }];
  const unresolved = new Set(unresolvedAliases({ old: null, moved: "b" }));
  assert.deepEqual([...unresolved], ["old"]);
  assert.deepEqual(readerSelection(current, "b", unresolved), { row: current[1], unresolved: false });
  assert.deepEqual(readerSelection(current, "old", unresolved), { row: undefined, unresolved: true });
  // A transcript with one session still shows it, marked unresolved.
  assert.deepEqual(readerSelection([current[0]], "old", unresolved), { row: current[0], unresolved: true });
  // Without a saved selection, or with one for another transcript, the transcript's first session.
  assert.deepEqual(readerSelection(current, "", unresolved), { row: current[0], unresolved: false });
  assert.deepEqual(readerSelection(current, "elsewhere", unresolved), { row: current[0], unresolved: false });
});

test("metric marks keep partial and unverified quality apart from observed values", () => {
  assert.equal(qualityMark("observed"), "");
  assert.equal(qualityMark("partial"), "+");
  assert.equal(qualityMark("unverified"), "?", "a depth through an unverified link is not shown as observed");
  assert.equal(qualityMark("inferred"), "?");
});

test("conversation-level copy uncertainty and omitted segments are explained", () => {
  assert.equal(occurrenceNote(), "");
  for (const reason of ["untyped-ancestry/v1", "retained-history-undeclared/v1", "declared-copy-source-missing/v1", "unresolved-observations"]) assert.match(occurrenceNote(reason), /^Copied history may be counted: \S/);
  assert.match(occurrenceNote("future-reason/v9"), /^Copied history may be counted/, "an unknown reason still says the count may include copies");
  assert.equal(omittedSegmentsNote([]), "");
  assert.match(omittedSegmentsNote([{ workspace_id: "w" }]), /^1 segment /);
  assert.match(omittedSegmentsNote([{ workspace_id: "w" }, { workspace_id: "v" }]), /^2 segments /);
});

test("a session count with unresolved identities is never shown as exact", () => {
  assert.equal(sessionCountText(3), "3 sessions");
  assert.equal(sessionCountText(1, 0), "1 session");
  assert.equal(sessionCountText(4, 2), "≈4 sessions (2 with unresolved identity)");
});

test("the Timeline counts pending events for every loaded session, including ones with no lane drawn", () => {
  const rows = [row({ id: "root" }), row({ id: "child", events_pending: true }), row({ id: "collapsed", events_pending: true }), row({ id: "quiet" })];
  // A lane can report pending before the row does; each session counts once.
  const lanes = new Map([["root", { events_pending: true }], ["child", { events_pending: true }], ["quiet", undefined]]);
  const shown = [{ id: "root" }, { id: "child" }, { id: "quiet" }];
  assert.deepEqual(timelineEventsPending(rows, lanes, shown), { sessions: 3, notShown: 1 });
  assert.deepEqual(timelineEventsPending(rows, new Map(), []), { sessions: 2, notShown: 2 }, "lanes not yet loaded leave every pending row unshown");
  assert.deepEqual(timelineEventsPending([row({ id: "a" })], new Map([["a", { events_pending: false }]]), [{ id: "a" }]), { sessions: 0, notShown: 0 });
  // Rows a collapse or page leaves out reach the Timeline only as the server's count.
  assert.deepEqual(timelineEventsPending(rows, lanes, shown, 5), { sessions: 5, notShown: 3 });
  assert.deepEqual(timelineEventsPending(rows, lanes, shown, 1), { sessions: 3, notShown: 1 }, "a stale smaller count never hides loaded pending rows");
  assert.equal(timelineEventsPendingNote({ sessions: 0, notShown: 0 }), "", "nothing pending says nothing");
  const some = timelineEventsPendingNote({ sessions: 3, notShown: 1 }), drawn = timelineEventsPendingNote({ sessions: 2, notShown: 0 }), none = timelineEventsPendingNote({ sessions: 2, notShown: 2 });
  assert.match(some, /3/);assert.match(some, /1/);
  assert.notEqual(drawn, none, "sessions without a lane are called out");
  assert.ok(none.startsWith(drawn), "the unshown part only adds to the lead");
});

test('cards qualify their summary only when a qualifier column counts inexact rows', async () => {
  const { cardQualification } = await import('../../../src/card-qualifier');
  const qualifier = { field: 'q', label: 'sessions are not exact' };
  assert.equal(cardQualification([{ q: 0 }, { q: 0 }], qualifier, true), null);
  assert.deepEqual(cardQualification([{ q: 1 }, { q: 2 }, { q: 0 }], qualifier, true), { mark: '≈', note: '2 groups include sessions are not exact' });
  assert.match(cardQualification([{ q: 1 }], qualifier, false).note, /^At least 1 group includes/);
  assert.equal(cardQualification([{ q: 1 }], undefined, true), null);
});
