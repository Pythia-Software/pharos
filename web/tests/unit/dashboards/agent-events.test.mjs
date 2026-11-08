import test from "node:test";
import assert from "node:assert/strict";
import { agentEventReadWhere, daySpan, eventRates, perThousand, ratePeriod, weekStart } from "../../../src/agent-event-rates-model";
import messagesDocument from "../../../../schemas/agent_event_messages.schema.json";
import kindsFixture from "../../../src/agent-event-kinds.json";
import { agentEventGroups, agentEventKinds } from "../../../src/agent-events-analysis-catalog";
import { reader } from "../helpers/transcript.mjs";
import { plain } from "../helpers/classic-script.mjs";
import { eventReaderURL, interleaveByKind } from "../../../src/agent-event-examples";
import { AGENT_EVENTS_KEY, agentEventsEnabled } from "../../../src/agent-events-flag";
import { agentEventFixLinks } from "../../../src/agent-event-rates-catalog";

test("Agent Experience is listed by its Settings switch or by the library's configuration", t => {
  const saved = globalThis.window;
  t.after(() => { globalThis.window = saved; });
  const page = (preference, configured) => ({ pharosPrefs: { get: (key, fallback) => key === AGENT_EVENTS_KEY && preference !== undefined ? preference : fallback }, pharosConfiguredFlags: configured });
  for (const [preference, configured, listed] of [[undefined, {}, false], [true, {}, true], [undefined, { [AGENT_EVENTS_KEY]: true }, true], [false, undefined, false]]) {
    globalThis.window = page(preference, configured);
    assert.equal(agentEventsEnabled(), listed, `preference ${preference}, configured ${JSON.stringify(configured)}`);
  }
});

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
  // Thirty sampled days over six months span six months: weeks, not thirty
  // touching daily columns.
  const sampled = Array.from({ length: 30 }, (_, index) => ({ keys: [new Date(Date.UTC(2026, 4, 1 + index * 6)).toISOString().slice(0, 10)], value: 10 }));
  assert.equal(daySpan(sampled), 175);
  assert.equal(ratePeriod(daySpan(sampled)), "week");
  assert.equal(daySpan([{ keys: ["2026-10-20"], value: 1 }]), 1);
  assert.equal(daySpan([]), 0);
});

test("examples take kinds in turn, most confident first within each", () => {
  const rows = [
    { id: "a1", family: "risk_flagged" }, { id: "a2", family: "risk_flagged" }, { id: "a3", family: "risk_flagged" },
    { id: "b1", family: "confusion" }, { id: "c1", family: "discovery" }, { id: "c2", family: "discovery" },
  ];
  assert.deepEqual(interleaveByKind(rows, 5).map(row => row.id), ["a1", "b1", "c1", "a2", "c2"]);
  assert.deepEqual(interleaveByKind(rows, 10).map(row => row.id), ["a1", "b1", "c1", "a2", "c2", "a3"]);
});

test("an example opens the reader at its message with Events highlighted", () => {
  const url = new URL(eventReaderURL({ workspace_id: "w 1", conversation_id: "c1", message_id: "m1" }), "http://pharos.test");
  assert.equal(url.pathname, "/work/w%201");
  assert.deepEqual(Object.fromEntries(url.searchParams), { conversation: "c1", message: "m1", reader_events: "1" });
  assert.equal(eventReaderURL({ message_id: "m1" }), undefined);
});

test("the messages read take the shared filters, model included, and never an event's own", () => {
  const fields = messagesDocument.fields.map(field => field.name);
  const where = [
    { field: "model", op: "=", value: "claude-opus-5-5" }, { field: "provider", op: "=", value: "claude" },
    { field: "family", op: "=", value: "workaround" }, { field: "id", op: "=", value: "e12" },
  ];
  assert.deepEqual(agentEventReadWhere(where, fields).map(term => term.field), ["model", "provider"]);
});

// Go's agentEventFamilies writes web/src/agent-event-kinds.json
// (TestAgentEventKindsAgreeEverywhere): the dashboard reads it, and the
// reader's group colours, which CSS needs one by one, follow it.
test("the dashboard's kinds and the reader's groups are Go's", () => {
  assert.deepEqual(agentEventKinds, kindsFixture.kinds);
  assert.deepEqual([...agentEventGroups], kindsFixture.groups);
  assert.deepEqual(plain(reader.EVENT_GROUPS.map(([group]) => group)), kindsFixture.groups);
});

test("Fixes links name the friction kinds the population keeps", () => {
  const kinds = where => agentEventFixLinks(where).map(link => new URLSearchParams(link.href.split("?")[1]).get("fixes_kind"));
  assert.deepEqual(kinds([]), ["stale", "flaky", "env", "tool", "obstacle"]);
  assert.deepEqual(kinds([{ field: "family", op: "=", value: "stale_guidance" }]), ["stale"]);
  // The first view: friction without plain obstacles.
  assert.deepEqual(kinds([{ field: "family_group", op: "=", value: "Friction" }, { field: "family", op: "!=", value: "obstacle" }]), ["stale", "flaky", "env", "tool"]);
  assert.deepEqual(kinds([{ field: "family_group", op: "=", value: "Choices you may not see" }]), []);
});
