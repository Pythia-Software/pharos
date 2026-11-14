// Human events on the reader's timeline (web/src/human-events-model.ts): which
// markers a person's message gets, the room the clock makes for them, and
// where each sits.
import { test } from "node:test";
import assert from "node:assert/strict";
import { reader } from "../helpers/transcript.mjs";
import { plain } from "../helpers/classic-script.mjs";
import { timelineRowExpands, humanEventPointSlots, laneHumanEvents, humanEventDimensions, humanEventGroups, humanEventLayout, humanEventLetter, humanEventPlacement, humanEventTitle, humanEventX, HUMAN_EVENT_RADIUS, HUMAN_EVENT_SLOT_SYMBOLS, HUMAN_EVENT_SYMBOL_PX } from "../../../src/human-events-model";
import { timeScale } from "../../../src/timeline-idle-model";
import { overlapping } from "../../../src/ribbon-marks";
import { timelineSectionVisible, timelineValueVisible, updateTimelineVisibility, validTimelineVisibility } from "../../../src/timeline-visibility";

let serial = 0;
const event = (message, slot, position = 0, label = `d${slot}.label`) =>
  ({ id: `${message}:${serial++}`, message_id: message, slot, className: `eng-slot-${slot}`, dimension: `d${slot}`, name: `Dimension ${slot}`, label, labelName: label.split(".")[1],
    title: `Dimension ${slot} · ${label.split(".")[1]}`, score: 0.914, start: 0, end: 4, position, text: "do it" });
const times = new Map([["a", 0], ["b", 500], ["c", 1000]]);
const at = id => times.get(id);
const near = (actual, expected, message) => assert.ok(Math.abs(actual - expected) < 1e-9, `${message ?? ""} ${actual} vs ${expected}`);

test("markers group by message in time order and by place in the message, without dimensions the Timeline key hides", () => {
  const events = [event("b", 4, 0.5), event("b", 1, 0.2, "d1.zeta"), event("b", 1, 0.9, "d1.alpha"), event("a", 2), event("gone", 0)];
  const groups = humanEventGroups(events, at);
  assert.deepEqual(groups.map(group => [group.message_id, group.at, group.events.map(item => item.position)]), [["a", 0, [0]], ["b", 500, [0.2, 0.5, 0.9]]]);
  assert.deepEqual(humanEventGroups(events, at, dimension => dimension !== "d1").map(group => group.events.map(item => item.label)), [["d2.label"], ["d4.label"]]);
  assert.deepEqual(humanEventGroups(events, at, () => false), []);
  // A Timeline row left with nothing but Human events (every other section hidden in the Key) still folds out onto them.
  assert.equal(timelineRowExpands(0, humanEventGroups(events, at, dimension => dimension === "d4").length), true);
  assert.equal(timelineRowExpands(0, humanEventGroups(events, at, () => false).length), false);
  assert.equal(timelineRowExpands(2, 0), true);
  assert.equal(humanEventTitle(events[0]), "Human event · Dimension 4 · label 91%: do it");
  // The rows Human events folds out into, and the Timeline key's chips: a dimension each, in slot order.
  assert.deepEqual(humanEventDimensions(groups.flatMap(group => group.events)).map(dimension => [dimension.id, dimension.name, dimension.className, dimension.events.length]),
    [["d1", "Dimension 1", "eng-slot-1", 2], ["d2", "Dimension 2", "eng-slot-2", 1], ["d4", "Dimension 4", "eng-slot-4", 1]]);
  // A dimension's mark is its display name's first letter.
  assert.deepEqual(["Intent", "Grounding", "Bounds", "Verification", "Delegation", "Evaluation", "Questions", "Authorship", "Tone", " mood", ""].map(humanEventLetter).join(""), "IGBVDEQATM?");
});

test("each person's message gets a slot and the rest of the clock keeps its proportions", () => {
  const base = timeScale([0, 1000]), track = 550;
  const groups = humanEventGroups([event("a", 0), event("a", 1, 0.5), event("b", 0), event("b", 1, 0.4), event("b", 2, 0.8)], at);
  const { scale, slots } = humanEventLayout(base, groups, track);
  const a = slots.get("a"), b = slots.get("b");
  near(a.width, (2 * HUMAN_EVENT_SYMBOL_PX + 2) / track); near(b.width, (3 * HUMAN_EVENT_SYMBOL_PX + 2) / track);
  const rest = 1 - a.width - b.width;
  near(a.left, 0); near(b.left, a.width + rest / 2, "the second slot starts where its message does");
  // A message starts its slot; what follows starts after it.
  near(scale.fraction(0), 0); near(scale.fraction(500), b.left); near(scale.fraction(1000), 1);
  near(scale.fraction(250), a.width + rest / 4); near(scale.fraction(750), a.width + b.width + rest * 3 / 4);
  assert.ok(scale.fraction(1) >= a.left + a.width, "nothing later sits under the markers");
  // The clock runs both ways: inside a slot it is the message's time.
  for (const time of [1, 250, 499, 501, 750, 1000]) near(scale.time(scale.fraction(time)), time);
  near(scale.time(b.left + b.width / 2), 500); near(scale.time(a.width / 2), 0);
  let last = -1; for (let time = 0; time <= 1000; time += 50) { const x = scale.fraction(time); assert.ok(x >= last); last = x; }
  assert.equal(humanEventLayout(base, [], track).scale, base, "no markers: the clock is untouched");
  // A long message's slot stops growing; its markers stack instead.
  const many = humanEventGroups(Array.from({ length: 30 }, (_, index) => event("a", index % 9, index / 30)), at);
  near(humanEventLayout(base, many, 1000).slots.get("a").width, (HUMAN_EVENT_SLOT_SYMBOLS * HUMAN_EVENT_SYMBOL_PX + 2) / 1000);
  // On a narrow row all slots shrink alike to half of it, and a last message keeps its slot inside the row.
  const narrow = humanEventLayout(base, humanEventGroups([...Array.from({ length: 9 }, (_, slot) => event("a", slot)), ...Array.from({ length: 9 }, (_, slot) => event("b", slot)), event("c", 0)], at), 200);
  near([...narrow.slots.values()].reduce((sum, slot) => sum + slot.width, 0), 0.5);
  const c = narrow.slots.get("c"); near(narrow.scale.fraction(1000), c.left); near(c.left + c.width, 1); near(narrow.scale.time(narrow.scale.fraction(700)), 700);
  // Idle cuts move with the clock.
  const cut = timeScale([0, 10_000_000], [{ start: 2_000_000, end: 9_000_000 }]);
  const moved = humanEventLayout(cut, [{ message_id: "a", at: 1_000_000, events: [event("a", 0)] }], 400);
  assert.equal(moved.scale.cuts.length, 1); assert.ok(moved.scale.cuts[0].fraction > cut.cuts[0].fraction);
});

test("a marker sits where its annotation starts in the message, and markers that would touch stack in lanes", () => {
  const track = 600, height = 28, slots = new Map([["a", { left: 0.1, width: 0.1 }]]);
  const first = event("a", 0, 0), middle = event("a", 1, 0.5), last = event("a", 2, 1);
  near(humanEventX(first, slots.get("a"), track), 60 + HUMAN_EVENT_SYMBOL_PX / 2);
  near(humanEventX(last, slots.get("a"), track), 120 - HUMAN_EVENT_SYMBOL_PX / 2, "the last marker ends inside the slot");
  near(humanEventX(middle, slots.get("a"), track), 90);
  assert.equal(humanEventX(first, undefined, track), null);
  // Apart: all on the row's axis. The same x on the folded row and on a dimension's own row.
  const apart = humanEventPlacement([first, middle, last], slots, track, height);
  assert.deepEqual([first, middle, last].map(item => apart.get(item.id).y), [14, 14, 14]);
  near(humanEventPlacement([middle], slots, track, height).get(middle.id).x, apart.get(middle.id).x);
  // Bunched: three at one place take the axis, then the lanes above and below it, without touching.
  const bunch = [event("a", 0, 0.5), event("a", 1, 0.5), event("a", 2, 0.51)], stacked = humanEventPlacement(bunch, slots, track, height), placed = bunch.map(item => stacked.get(item.id));
  assert.equal(new Set(placed.map(mark => mark.y)).size, 3); assert.equal(placed[0].y, 14);
  assert.ok(placed.every(mark => mark.y - HUMAN_EVENT_RADIUS >= -0.5 && mark.y + HUMAN_EVENT_RADIUS <= height + 0.5), "lanes stay inside the row");
  assert.deepEqual(overlapping(placed), []);
  // More than the lanes hold: every marker is still placed, in a lane of the row.
  const crowd = Array.from({ length: 8 }, () => event("a", 3, 0.5)), crowded = humanEventPlacement(crowd, slots, track, height);
  assert.equal(crowded.size, 8); assert.ok([...crowded.values()].every(mark => mark.y >= HUMAN_EVENT_RADIUS - 0.5 && mark.y <= height - HUMAN_EVENT_RADIUS + 0.5));
  assert.equal(humanEventPlacement([event("gone", 0)], slots, track, height).size, 0);
});

test("the Timeline key hides Human events by dimension, apart from the Highlights key", () => {
  let visibility = updateTimelineVisibility({}, "human", ["register"], "hide");
  assert.ok(validTimelineVisibility(visibility));
  assert.equal(timelineValueVisible(visibility, "human", "register"), false); assert.equal(timelineValueVisible(visibility, "human", "intent"), true);
  assert.equal(timelineValueVisible(visibility, "events", "friction"), true, "agent events keep their own selection");
  visibility = updateTimelineVisibility(visibility, "sections", ["human"], "hide");
  assert.equal(timelineSectionVisible(visibility, "human"), false); assert.equal(timelineSectionVisible(visibility, "events"), true);
});

test("a workspace Timeline lane gets the reader's Human events from its spans and stored text", () => {
  // Typed "Please open it now. " (20 bytes), then a pasted block, as the lane sends authorship: parts in order.
  const text = "Please open it now. ERROR importer.go:42";
  const lane = [{ id: "m1", role: "user", kind: "message", text: "Please op", engagement_text: text, authorship: [{ category: "typed", bytes: 20 }, { category: "pasted", bytes: 20 }],
    engagement: [{ label: "intent.task", dimension: "intent", score: 0.9, start: 8, end: 13 }, { label: "rights.go_ahead", dimension: "rights", score: 0.9, start: 0, end: 6 },
      { label: "register.filler", dimension: "register", score: 0.99, start: 0, end: 6 }, { label: "bounds.scope", dimension: "bounds", score: 0.5, start: 7, end: 14 }, { label: "grounding.evidence", dimension: "grounding", score: 0.9, start: 20, end: 25 }] },
    { id: "m2", role: "user", kind: "message", text: "No stored text", engagement: [{ label: "intent.task", dimension: "intent", score: 0.9, start: 0, end: 2 }] },
    { id: "m3", role: "assistant", kind: "message", text: "Done.", engagement_text: "Done.", engagement: [{ label: "intent.task", dimension: "intent", score: 0.9, start: 0, end: 4 }] }];
  const events = plain(laneHumanEvents(lane, reader.engagementTimeline));
  // The reader's rules: whole words, the display threshold, typed text only, the label never shown, display names.
  assert.deepEqual(events.map(event => [event.message_id, event.label, event.name, event.text]), [["m1", "rights.go_ahead", "Delegation", "Please"], ["m1", "intent.task", "Intent", "open it"]]);
  assert.deepEqual(laneHumanEvents(lane, undefined), [], "a page without the reader's model shows none");
  assert.deepEqual(laneHumanEvents([], reader.engagementTimeline), []);
});

test("on a clock that cannot widen, a message's slot starts at its instant and stays inside the row", () => {
  const scale = timeScale([0, 1000]), groups = humanEventGroups([event("a", 0, 0), event("b", 1, 0), event("b", 2, 0.5), event("c", 3, 0)], at);
  const slots = humanEventPointSlots(scale, groups, 400);
  near(slots.get("b").left, 0.5 - HUMAN_EVENT_SYMBOL_PX / 2 / 400); near(slots.get("b").width, (2 * HUMAN_EVENT_SYMBOL_PX + 2) / 400);
  assert.equal(slots.get("a").left, 0); near(slots.get("c").left + slots.get("c").width, 1);
  // A marker's x follows from the slot as in the reader, and stacking is the same placement.
  const placed = humanEventPlacement(groups.flatMap(group => group.events), slots, 400, 24);
  assert.equal(placed.size, 4);
  assert.equal(humanEventPointSlots(scale, groups, 0).size, 0);
  assert.equal(humanEventPointSlots(timeScale([2000, 3000]), groups, 400).size, 0, "messages outside the window get no slot");
});
