import test from "node:test";
import assert from "node:assert/strict";
import { eventRates, perThousand, ratePeriod, weekStart } from "../../../src/agent-event-rates-model";
import { interleaveByKind } from "../../../src/agent-event-examples";

test("event rates divide each period's events by the messages read in it", () => {
  const events = [
    { keys: ["2026-10-20", "Friction"], value: 30 }, { keys: ["2026-10-20", "Reactions and learning"], value: 10 },
    { keys: ["2026-10-21", "Friction"], value: 5 },
    // No messages were read that day, so no rate can be said for it.
    { keys: ["2026-10-22", "Friction"], value: 7 },
  ];
  const read = [{ keys: ["2026-10-20"], value: 2000 }, { keys: ["2026-10-21"], value: 500 }, { keys: ["2026-10-23"], value: 100 }];
  const days = eventRates(events, read, "day");
  assert.deepEqual(days.map(day => day.start), ["2026-10-20", "2026-10-21", "2026-10-23"]);
  assert.equal(perThousand(days[0].total, days[0].read), 20);
  assert.equal(perThousand(days[0].events.Friction, days[0].read), 15);
  assert.equal(days[2].total, 0, "a day with messages read and no events is a zero rate");
  // A week pools its days' events and messages before dividing, so the 22nd's
  // events count once the week has messages read.
  const weeks = eventRates(events, read, "week");
  assert.equal(weeks.length, 1);
  assert.equal(weeks[0].start, "2026-10-19");
  assert.equal(perThousand(weeks[0].total, weeks[0].read), 1000 * 52 / 2600);
});

test("weeks start on Monday, and long ranges switch to weeks", () => {
  assert.equal(weekStart("2026-10-25"), "2026-10-19");
  assert.equal(weekStart("2026-10-26"), "2026-10-26");
  assert.equal(ratePeriod(30), "day");
  assert.equal(ratePeriod(90), "week");
});

test("examples take kinds in turn, most confident first within each", () => {
  const rows = [
    { id: "a1", family: "risk_flagged" }, { id: "a2", family: "risk_flagged" }, { id: "a3", family: "risk_flagged" },
    { id: "b1", family: "confusion" }, { id: "c1", family: "discovery" }, { id: "c2", family: "discovery" },
  ];
  assert.deepEqual(interleaveByKind(rows, 5).map(row => row.id), ["a1", "b1", "c1", "a2", "c2"]);
  assert.deepEqual(interleaveByKind(rows, 10).map(row => row.id), ["a1", "b1", "c1", "a2", "c2", "a3"]);
});
