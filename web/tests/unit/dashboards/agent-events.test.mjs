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
import { agentEventFixLinks, agentEventRatesCatalog as eventRates } from "../../../src/agent-event-rates-catalog";
import { analysisQuery } from "../../../src/dashboard-analysis-model";
import { EMPTY_QUERY, evaluateMetrics } from "@pythia-software/query-table-core";
import { population } from "../../support/query-server.mjs";
import { schemas } from "../helpers/recipes.mjs";

test("Agent Experience is listed by its Settings switch or by the library's configuration", t => {
  const saved = globalThis.window;
  t.after(() => { globalThis.window = saved; });
  const page = (preference, configured) => ({ pharosPrefs: { get: (key, fallback) => key === AGENT_EVENTS_KEY && preference !== undefined ? preference : fallback }, pharosConfiguredFlags: configured });
  for (const [preference, configured, listed] of [[undefined, {}, true], [true, {}, true], [false, { [AGENT_EVENTS_KEY]: true }, true], [false, undefined, false]]) {
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

test("compare bars of a rate or share carry its confidence interval, and Query Table returns the bounds", () => {
  const clauseFor = state => analysisQuery(eventRates, { view: "compare", split: "family_group", ...state }, []).clause;
  assert.deepEqual(clauseFor({ metric: "turn_share" }).interval, { kind: "proportion" });
  assert.deepEqual(clauseFor({ metric: "per_thousand" }).interval, { kind: "rate" });
  assert.equal(clauseFor({ metric: "events" }).interval, undefined, "a count has no interval");
  // Conversations pool their parts into one ratio per bar; a mean of their ratios has none.
  assert.deepEqual(clauseFor({ metric: "turn_share", unit: "conversation", split: "repository_name", reduce: "total" }).interval, { kind: "proportion" });
  assert.equal(clauseFor({ metric: "turn_share", unit: "conversation", split: "repository_name", reduce: "mean" }).interval, undefined);
  for (const metric of ["turn_share", "per_thousand", "message_share", "conversation_share"]) {
    const clause = clauseFor({ metric });
    const [result] = evaluateMetrics(population(schemas.agent_event_rates), { ...EMPTY_QUERY, aggregations: [clause] }, schemas.agent_event_rates).metrics;
    assert.ok(!result.error, `${metric}: ${result.error}`);
    for (const bucket of result.buckets) {
      assert.ok(!bucket.error && !bucket.intervalError, `${metric}: ${bucket.error ?? bucket.intervalError}`);
      if (typeof bucket.value === "number") assert.ok(bucket.lower <= bucket.value && bucket.value <= bucket.upper, `${metric} ${bucket.keys}: ${bucket.lower} ≤ ${bucket.value} ≤ ${bucket.upper}`);
    }
  }
});
