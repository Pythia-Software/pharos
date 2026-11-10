import test from "node:test";
import assert from "node:assert/strict";
import { agentEventReadWhere } from "../../../src/agent-event-rates-model";
import messagesDocument from "../../../../schemas/agent_event_messages.schema.json";
import kindsFixture from "../../../src/agent-event-kinds.json";
import { agentEventGroups, agentEventKinds } from "../../../src/agent-events-analysis-catalog";
import { reader } from "../helpers/transcript.mjs";
import { plain } from "../helpers/classic-script.mjs";
import { primaryUsageSource } from "../../../src/usage-row-model";
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

test("an event opens the reader at its message with Events highlighted", () => {
  const url = new URL(primaryUsageSource("agent_events", { workspace_id: "w 1", conversation_id: "c1", message_id: "m1" }), "http://pharos.test");
  assert.equal(url.pathname, "/work/w%201");
  assert.deepEqual(Object.fromEntries(url.searchParams), { conversation: "c1", message: "m1", reader_events: "1" });
  assert.equal(primaryUsageSource("agent_events", { message_id: "m1" }), undefined);
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
