import test from "node:test";
import assert from "node:assert/strict";
import { EMPTY_QUERY, applyQuery, createGroupingModel, decodeQueryStrict, encodeQuery, groupLinkQuery, resolveGroupLink, exitGrouping, prepareGrouping, toQueryRequestV3, readFieldValue, normalizeQueryState } from "@pythia-software/query-table-core";
import { schemas, capabilities, execution } from "../helpers/recipes.mjs";
import { applyQueryIdea, usageQueryIdeas, queryIdeaMatches, queryIdeasOpen, queryIdeasPreferenceKey } from "../../../src/usage-query-ideas-model";
import { linkTargets } from "../../../src/query-link-targets";

const rows = (schema) => Array.from({ length: 4 }, (_, index) => ({
  ...Object.fromEntries(schema.fields.map(field => [field.name, field.type === "number" ? 2 + index : field.type === "bool" ? false : field.type === "datetime" ? `2026-10-${10 + index}T12:00:00Z` : field.type === "textarray" ? [] : "alpha"])),
  id: schema.fields.find(field => field.name === "id")?.type === "number" ? index + 1 : `record ${index} / &`, repository_name: index === 2 ? null : index === 3 ? "other" : "répo & one", conversation_id: index === 1 ? "conversation two" : "conversation / one", workspace_id: "work one", work_id: "work one",
  duration_ms: index === 0 ? null : index === 1 ? 0 : 600, duration_source: index === 0 ? null : "timestamps", error_count: index === 0 ? 1 : 0,
  typed_words: index === 0 ? 0 : index * 10, typed_turns: index, typed_message: index > 0,
  user_words: 100, words: 100, cost_usd: index === 2 ? null : 2, cost_today_usd: index === 2 ? null : 3,
  agent_session_id: `session ${index % 2}`, call_count: index === 1 ? 9 : 1, timed_count: index === 0 ? 0 : 1, total_duration_ms: index === 0 ? 0 : 100,
  call_message_id: `call message ${index}`, call_id: `call ${index}`, evidence_message_id: `evidence ${index}`,
}));

for (const [dataset, ideas] of Object.entries(usageQueryIdeas)) test(`${dataset}: every suggested grouping validates, round-trips and drills into its exact source population`, () => {
  const schema = schemas[dataset], data = rows(schema);
  const filterField = schema.fields.some(field => field.name === "repository_name") ? "repository_name" : "tool_name";
  const source = { ...EMPTY_QUERY, fitToScreen: false, where: [{ field: filterField, op: "!=", value: "other" }], limit: 1, offset: 100 };
  for (const idea of ideas) {
    const query = applyQueryIdea(source, idea);
    assert.equal(query.fitToScreen, true, `${idea.id}: suggested tables auto-fit`);
    assert.equal(query.grouping.source.fitToScreen, false, `${idea.id}: the original source preference stays intact`);
    createGroupingModel(schema).validate(query.grouping);
    assert.deepEqual(decodeQueryStrict(encodeQuery(query)), normalizeQueryState(query), idea.id);
    const result = applyQuery(data, { ...query, limit: 100 }, schema);
    assert.ok(result.rows.length, idea.id);
    const selected = query.select.map(column => column.field), links = query.grouping.summaries.filter(column => column.op === "link");
    assert.deepEqual(selected.slice(-links.length), links.map(column => column.id), `${idea.id}: links end the result columns`);
    const request = toQueryRequestV3(query, schema, capabilities, execution);
    assert.deepEqual(request.diagnostics ?? [], [], `${idea.id}: every metric is executable`);
    assert.ok(request.grouping.summaries.every(column => column.op !== "link"), `${idea.id}: links execute only on the client`);
    assert.equal(queryIdeaMatches(query, idea), true);
    const prepared = prepareGrouping(data, schema, query.grouping);
    const groupRows = prepared.rows;
    for (const row of groupRows) {
      const link = readFieldValue(prepared.schema.fields.find(field => field.name === "idea_records"), row);
      const records = groupLinkQuery(query, link);
      assert.ok(records, `${idea.id}: valid records query`);
      assert.ok(records.where.some(term => term.field === filterField && term.value === "other"), `${idea.id}: source filter retained`);
      assert.equal(records.offset, 0);
      const destination = resolveGroupLink(query, link, linkTargets);
      const url = new URL(destination.href, "https://pharos.test");
      const restored = decodeQueryStrict(url.searchParams.get(`q_${dataset}`));
      assert.equal(restored.fitToScreen, true, `${idea.id}: drill-in links auto-fit after URL restoration`);
      assert.deepEqual(restored.where, records.where);
      const expected = data.filter(record => record[filterField] !== "other" && query.grouping.keys.every(key => record[key] === row[key]));
      assert.deepEqual(applyQuery(data, { ...records, limit: 100 }, schema).rows.map(record => record.id).sort(), expected.map(record => record.id).sort(), idea.id);
    }
  }
});

test("switching suggestions preserves source filters and resets HAVING and result metrics", () => {
  const [first, second] = usageQueryIdeas.tool_calls;
  const source = { ...EMPTY_QUERY, where: [{ field: "provider", op: "=", value: "codex" }], aggregations: [{ id: "original", op: "count", groupBy: [] }] };
  const grouped = { ...applyQueryIdea(source, first), where: [{ field: "idea_calls", op: ">", value: 100 }] };
  const next = applyQueryIdea(grouped, second);
  assert.deepEqual(next.grouping.source.where, source.where);
  assert.deepEqual(next.where, []);
  assert.deepEqual(next.aggregations, second.metrics);
  assert.deepEqual(exitGrouping(next), source);
});

test("means and rates use the whole denominator including zero durations and absent timing", () => {
  const data = rows(schemas.tool_calls).slice(0, 2).map(row => ({ ...row, tool_name: "same" }));
  const idea = usageQueryIdeas.tool_calls.find(idea => idea.id === "tool_calls:tools");
  const result = applyQuery(data, applyQueryIdea(EMPTY_QUERY, idea), schemas.tool_calls).rows[0];
  assert.equal(result.idea_calls, 2);
  assert.equal(result.idea_timed, 1);
  assert.equal(result.idea_mean_duration, 0);
  assert.equal(result.idea_failure_rate, .5);
  const rollups = rows(schemas.tools).slice(0, 2).map(row => ({ ...row, tool_name: "same" }));
  const summary = applyQuery(rollups, applyQueryIdea(EMPTY_QUERY, usageQueryIdeas.tools[0]), schemas.tools).rows[0];
  assert.equal(summary.idea_failure_rate, .1, "one failure among ten calls, not the mean rollup rate");
  assert.equal(summary.idea_mean_duration, 100, "only one timed call supplies the duration denominator");
});

test("message-date writing averages exclude messages without typed words from the denominator", () => {
  const data = rows(schemas.writing_messages).slice(0, 2).map(row => ({ ...row, repository_name: "same" }));
  const result = applyQuery(data, applyQueryIdea(EMPTY_QUERY, usageQueryIdeas.writing_messages[0]), schemas.writing_messages).rows[0];
  assert.equal(result.idea_written_messages, 1);
  assert.equal(result.idea_words_per_message, 10);
  assert.equal(result.idea_authored_share, .05);
});

test("representative message links use coherent row IDs and encode special characters", () => {
  const data = rows(schemas.writing_messages).slice(0, 2).map(row => ({ ...row, conversation_id: "conv / &" }));
  const idea = usageQueryIdeas.writing_messages.find(idea => idea.id === "writing_messages:threads");
  const query = applyQueryIdea(EMPTY_QUERY, idea);
  const prepared = prepareGrouping(data, schemas.writing_messages, query.grouping);
  const row = prepared.rows[0];
  const link = resolveGroupLink(query, readFieldValue(prepared.schema.fields.find(field => field.name === "idea_example"), row), {});
  const url = new URL(link.href, "https://pharos.test");
  assert.equal(decodeURIComponent(url.pathname.slice("/conversation/".length)), "conv / &");
  assert.equal(url.searchParams.get("message"), data[1].id);
});

test("query ideas start open and remember independent boolean preferences for each dataset", () => {
  for (const value of [undefined, null, {}, "false", 0]) assert.equal(queryIdeasOpen(value), true);
  assert.equal(queryIdeasOpen(false), false);
  assert.equal(queryIdeasOpen(true), true);
  const keys = Object.keys(usageQueryIdeas).map(queryIdeasPreferenceKey);
  assert.equal(new Set(keys).size, keys.length);
});
