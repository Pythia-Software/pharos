// Idle gaps and the compressed timeline clock (timeline-idle-model.ts).
import test from "node:test";
import assert from "node:assert/strict";
import * as idle from "../../../src/timeline-idle-model";
const { idleGaps, timeScale, laneBusy, toVirtual, fromVirtual, IDLE_GAP_MS } = idle;

const min = 60_000;
const at = m => 1_760_000_000_000 + m * min;

test("a gap needs the threshold of quiet and the caller speaking first after it", () => {
  const busy = [{ start: at(0), end: at(5) }, { start: at(40), end: at(45) }];
  assert.deepEqual(idleGaps({ busy, callers: [at(40)] }), [{ start: at(5), end: at(40) }]);
  // Agent work resuming on its own (the caller speaks only later) is not a gap.
  assert.deepEqual(idleGaps({ busy, callers: [at(45)] }), []);
  // A harness reply seconds before the person's message still ends a gap the person ended.
  assert.deepEqual(idleGaps({ busy: [{ start: at(0), end: at(5) }, { start: at(40), end: at(40) }], callers: [at(40) + 16_000] }), [{ start: at(5), end: at(40) }]);
  // Just under the threshold is not a gap; the threshold itself is.
  assert.deepEqual(idleGaps({ busy: [{ start: at(0), end: at(5) }], callers: [at(5) + IDLE_GAP_MS - 1] }), []);
  assert.equal(idleGaps({ busy: [{ start: at(0), end: at(5) }], callers: [at(5) + IDLE_GAP_MS] }).length, 1);
  // Caller messages are activity too: a quiet stretch before the first one is a gap only after activity.
  assert.deepEqual(idleGaps({ busy: [], callers: [at(0), at(30)] }), [{ start: at(0), end: at(30) }]);
});

test("anything in flight in any row keeps the clock running", () => {
  const callers = [at(0), at(60)];
  // A subagent's long tool call spans the parent's quiet stretch.
  const subagent = [{ start: at(10), end: at(55) }];
  assert.deepEqual(idleGaps({ busy: subagent, callers }), []);
  // Overlapping sessions merge; only the stretch after both finish is idle.
  const rows = [{ start: at(1), end: at(20) }, { start: at(15), end: at(30) }];
  assert.deepEqual(idleGaps({ busy: rows, callers }), [{ start: at(30), end: at(60) }]);
});

test("a truncated row stays busy to its session's end", () => {
  const lane = { marks: [{ start: at(0), end: at(1), end_known: true }], truncated: true };
  assert.deepEqual(laneBusy(lane, { start: at(0), end: at(90) }).at(-1), { start: at(1), end: at(90) });
  // A call without a recorded end is an instant, not open-ended.
  assert.deepEqual(laneBusy({ marks: [{ start: at(2), end: at(50), end_known: false }] }), [{ start: at(2), end: at(2) }]);
});

test("the compressed clock round-trips and keeps a sliver for each cut", () => {
  const range = [at(0), at(100)], gaps = [{ start: at(10), end: at(70) }];
  const scale = timeScale(range, gaps);
  assert.equal(scale.compressed, true);
  for (const t of [at(0), at(5), at(10), at(30), at(70), at(85), at(100)]) assert.ok(Math.abs(scale.time(scale.fraction(t)) - t) < 1e-3, `round trip at ${t}`);
  assert.equal(scale.fraction(at(0)), 0);
  assert.ok(Math.abs(scale.fraction(at(100)) - 1) < 1e-12);
  // 40 active minutes share the width; the 60-minute gap is a sliver.
  const sliver = scale.fraction(at(70)) - scale.fraction(at(10));
  assert.ok(sliver > 0 && sliver < 0.02, `sliver ${sliver}`);
  assert.equal(scale.cuts.length, 1);
  assert.deepEqual([scale.cuts[0].start, scale.cuts[0].end], [at(10), at(70)]);
  // Virtual drags use the compressed width.
  assert.ok(Math.abs(fromVirtual(scale, toVirtual(scale, at(85))) - at(85)) < 1e-3);
  assert.ok(scale.span < 100 * min);
});

test("gaps clip to the visible range; a window inside a gap is not compressed", () => {
  const gaps = [{ start: at(10), end: at(70) }];
  const edge = timeScale([at(50), at(100)], gaps);
  assert.equal(edge.cuts.length, 1);
  assert.deepEqual([edge.cuts[0].start, edge.cuts[0].end], [at(10), at(70)], "the cut names the whole gap");
  const inside = timeScale([at(20), at(60)], gaps);
  assert.equal(inside.compressed, false);
  assert.equal(inside.fraction(at(40)), 0.5);
  assert.equal(timeScale([at(0), at(100)]).fraction(at(25)), 0.25);
});

test("cut labels hide the ticks beneath them and skip crowded cuts", () => {
  const { cutAxisLabels } = idle;
  const { labelled, hiddenTicks } = cutAxisLabels([0, 0.25, 0.5, 0.75, 1], [{ fraction: 0.26 }, { fraction: 0.3 }, { fraction: 0.8 }], 1000);
  assert.deepEqual([...labelled], [0, 2]);
  assert.deepEqual([...hiddenTicks].sort(), [1, 3]);
});

test("a harness wait is busy for the idle rule and skippable only on request", () => {
  const { laneWaits, waitGaps, chosenGaps, markWait } = idle;
  // A background command launched at 0 (acknowledged at 0:03) that the harness reports done at 43.
  const wait = { start: at(0), end: at(0) + 3000, end_known: true, wait_kind: "background_command", wait_end: at(43) };
  const lane = { marks: [wait, { start: at(43), end: at(44), end_known: true }] };
  assert.deepEqual(laneWaits(lane), [{ start: at(0), end: at(43) }]);
  // An end that was not recorded is no span; nor is an ordinary call.
  assert.equal(markWait({ ...wait, wait_end: null }), null);
  assert.equal(markWait({ start: at(0), end: at(1) }), null);
  // Busy by default: the agent resuming on its own after the wait leaves no idle gap.
  assert.deepEqual(idleGaps({ busy: laneBusy(lane), callers: [at(90)] }), [{ start: at(44), end: at(90) }]);
  // Without waits, the stretch only the wait fills is a wait gap.
  const others = laneBusy(lane, undefined, { waits: false });
  assert.deepEqual(waitGaps({ busy: others, waits: laneWaits(lane) }), [{ start: at(0) + 3000, end: at(43), kind: "wait" }]);
  // Other work during the wait splits it; pieces under the threshold stay.
  assert.deepEqual(waitGaps({ busy: [...others, { start: at(20), end: at(21) }], waits: laneWaits(lane) }), [{ start: at(0) + 3000, end: at(20), kind: "wait" }, { start: at(21), end: at(43), kind: "wait" }]);
  assert.deepEqual(waitGaps({ busy: [...others, { start: at(10), end: at(35) }], waits: laneWaits(lane) }), []);
  // Overlapping waits count once.
  assert.deepEqual(waitGaps({ busy: [], waits: [{ start: at(0), end: at(20) }, { start: at(10), end: at(30) }] }), [{ start: at(0), end: at(30), kind: "wait" }]);
  // The checkboxes choose which gaps a timeline cuts; touching cuts merge.
  const gaps = { idle: [{ start: at(30), end: at(60) }], waits: [{ start: at(5), end: at(30), kind: "wait" }] };
  assert.deepEqual(chosenGaps(gaps, { skipIdle: true, skipWaits: false }), [{ start: at(30), end: at(60), kind: "idle" }]);
  assert.deepEqual(chosenGaps(gaps, { skipIdle: false, skipWaits: true }), [{ start: at(5), end: at(30), kind: "wait" }]);
  assert.deepEqual(chosenGaps(gaps, { skipIdle: true, skipWaits: true }), [{ start: at(5), end: at(60), kind: "mixed" }]);
  assert.deepEqual(chosenGaps(gaps, { skipIdle: false, skipWaits: false }), []);
  // A cut names what filled it.
  assert.equal(timeScale([at(0), at(100)], [{ start: at(5), end: at(30), kind: "wait" }]).cuts[0].kind, "wait");
});
