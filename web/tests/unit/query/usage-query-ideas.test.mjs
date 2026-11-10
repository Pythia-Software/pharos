import { usageActionQuery, usageQuerySchema } from "../../../src/usage-query-columns";
import test from "node:test";
import assert from "node:assert/strict";
import { EMPTY_QUERY, applyQuery, createGroupingModel, decodeQueryStrict, encodeQuery, filterValues, groupLinkQuery, resolveGroupLink, exitGrouping, prepareGrouping, toQueryRequestV3, readFieldValue, normalizeQueryState } from "@pythia-software/query-table-core";
import { schemas, capabilities, execution } from "../helpers/recipes.mjs";
import { applyQueryIdea, usageQueryIdeas, queryIdeaMatches, queryIdeasOpen, queryIdeasPreferenceKey } from "../../../src/usage-query-ideas-model";
import { analyzeQueryDatasets } from "../../../src/usage-workspace-model";
import { linkTargets } from "../../../src/query-link-targets";

const baseRow = (schema, index) => ({
  ...Object.fromEntries(schema.fields.map(field => [field.name, field.type === "number" ? 2 + index : field.type === "bool" ? false : field.type === "datetime" ? `2026-10-${10 + index}T12:00:00Z` : field.type === "textarray" ? [] : "alpha"])),
  id: schema.fields.find(field => field.name === "id")?.type === "number" ? index + 1 : `record ${index} / &`,
  repository_name: index === 2 ? null : "répo & one", project_name: index === 2 ? null : "répo & one", conversation_id: "conversation / one", work_id: "work one",
  duration_ms: index === 0 ? null : index === 1 ? 0 : 600, error_count: index === 0 ? 1 : 0,
  typed_words: index === 0 ? 0 : index * 10, typed_turns: index, words: 100, user_words: 100,
  cost_usd: index === 2 ? null : 2, cost_today_usd: index === 2 ? null : 3,
  call_count: index === 1 ? 9 : 1, timed_count: index === 0 ? 0 : 1,
  total_duration_ms: index === 0 ? 0 : 100, call_message_id: `call message ${index}`,
  call_id: `call ${index}`, evidence_message_id: `evidence ${index}`,
});

function satisfyingRow(schema, terms, index = 0) {
  const row = baseRow(schema, index);
  const satisfy = (term) => {
    if (term.all) return term.all.forEach(satisfy);
    if (term.any) return satisfy(term.any[0]);
    const type = schema.fields.find(field => field.name === term.field)?.type;
    if (term.op === "is_not_null") row[term.field] = type === "number" ? 4 : type === "bool" ? true : "known";
    else if (term.op === "is_null") row[term.field] = null;
    else if (term.op === ">" || term.op === ">=") row[term.field] = Number(term.value) + 1;
    else if (term.op === "=") row[term.field] = type === "bool" ? term.value === "true" : type === "number" ? Number(term.value) : term.value;
    else if (term.op === "!=") row[term.field] = "different";
  };
  terms.forEach(satisfy);
  return row;
}

const prior = { ...EMPTY_QUERY, fitToScreen: false, where: [{ field: "provider", op: "=", value: "stale-provider" }],
  aggregations: [{ id: "old_metric", op: "count", groupBy: [] }], hostState: { version: 1, params: { keyword: "stale" } },
  limit: 1, offset: 100 };

test("every canonical dataset has several record and grouped starting points", () => {
  for (const dataset of analyzeQueryDatasets) {
    const ideas = usageQueryIdeas[dataset];
    assert.ok(ideas.length >= 5, `${dataset}: several ideas`);
    assert.ok(ideas.some(idea => "query" in idea), `${dataset}: record idea`);
    assert.ok(ideas.some(idea => "grouping" in idea), `${dataset}: grouped idea`);
    assert.equal(new Set(ideas.map(idea => idea.id)).size, ideas.length, `${dataset}: unique IDs`);
  }
});

for (const [dataset, ideas] of Object.entries(usageQueryIdeas)) test(`${dataset}: ideas validate, round-trip and start independently`, () => {
  const schema = schemas[dataset];
  for (const idea of ideas) {
    const query = applyQueryIdea(prior, idea);
    assert.equal(query.fit, "screen", idea.id);
    assert.equal(query.offset, 0, idea.id);
    assert.deepEqual(query.hostState?.params ?? {}, dataset === "message_labels" ? { label_results: ["table"] } : {}, `${idea.id}: host keyword cleared`);
    assert.deepEqual(decodeQueryStrict(encodeQuery(query)), normalizeQueryState(query), idea.id);
    assert.equal(queryIdeaMatches(query, idea), true, idea.id);
    assert.deepEqual(query.aggregations ?? [], ("query" in idea ? idea.query.aggregations : idea.metrics) ?? [], `${idea.id}: own metrics`);
    const expectedWhere = "query" in idea ? idea.query.where : idea.where ?? [];
    assert.deepEqual((query.grouping?.source ?? query).where, expectedWhere, `${idea.id}: own record filters`);
    if (query.grouping) {
      createGroupingModel(schema).validate(query.grouping);
      assert.deepEqual(query.where, [], `${idea.id}: old HAVING cleared`);
      assert.deepEqual(exitGrouping(query).where, expectedWhere, `${idea.id}: exit returns own source`);
      const links = query.grouping.summaries.filter(column => column.op === "link");
      assert.deepEqual(query.select.map(column => column.field).slice(-links.length), links.map(column => column.id), `${idea.id}: links end columns`);
    } else {
      assert.ok(query.select.length > 0, `${idea.id}: record columns`);
      assert.deepEqual(query.where, expectedWhere);
      assert.equal(applyQuery([satisfyingRow(schema, expectedWhere)], query, schema).rows.length, 1, `${idea.id}: record population`);
    }
    const request = toQueryRequestV3(query, schema, capabilities, execution);
    assert.deepEqual(request.diagnostics ?? [], [], `${idea.id}: executable V3 query`);
    if (query.grouping) assert.ok(request.grouping.summaries.every(column => column.op !== "link"), `${idea.id}: links stay client-side`);
  }
});

for (const [dataset, ideas] of Object.entries(usageQueryIdeas)) test(`${dataset}: grouped ideas drill into exactly their own source rows`, () => {
  const schema = schemas[dataset];
  for (const idea of ideas.filter(idea => "grouping" in idea)) {
    const query = applyQueryIdea(prior, idea);
    const keys = query.grouping.keys;
    const inside = satisfyingRow(schema, idea.where ?? [], 0);
    const alsoInside = { ...inside, id: `another ${idea.id}` };
    const outside = { ...inside, id: `outside ${idea.id}`, [keys[0]]: keys[0] === "id" || inside[keys[0]] === null ? `outside ${idea.id}` : null };
    const filter = (idea.where ?? []).find(term => "field" in term && !keys.includes(term.field));
    const excluded = filter ? { ...inside, id: `excluded ${idea.id}` } : null;
    if (excluded) {
      if (filter.op === "is_not_null") excluded[filter.field] = null;
      else if (filter.op === ">" || filter.op === ">=") excluded[filter.field] = 0;
      else if (typeof inside[filter.field] === "boolean") excluded[filter.field] = !inside[filter.field];
      else excluded[filter.field] = "outside";
    }
    const data = [inside, alsoInside, outside, ...(excluded ? [excluded] : [])];
    const prepared = prepareGrouping(data, schema, query.grouping);
    assert.ok(prepared.rows.length, `${idea.id}: source population exists`);
    const sourceRows = applyQuery(data, { ...query.grouping.source, limit: 100 }, schema).rows;
    for (const row of prepared.rows) {
      const link = readFieldValue(prepared.schema.fields.find(field => field.name === "idea_records"), row);
      const records = groupLinkQuery(query, link);
      assert.ok(records, `${idea.id}: records query`);
      for (const filter of idea.where ?? []) assert.ok(records.where.some(term => JSON.stringify(term) === JSON.stringify(filter)), `${idea.id}: own filter retained`);
      assert.equal(records.offset, 0, idea.id);
      const destination = resolveGroupLink(query, link, linkTargets);
      const url = new URL(destination.href, "https://pharos.test");
      if (url.searchParams.has(`q_${dataset}`)) {
        const restored = decodeQueryStrict(url.searchParams.get(`q_${dataset}`));
        assert.deepEqual(restored.where, records.where, `${idea.id}: URL population`);
      } else {
        // Legacy internal sources are redirected to a canonical destination.
        assert.ok([...url.searchParams.keys()].some(key => key.startsWith("q_")), `${idea.id}: canonical destination has a query`);
      }
      const expected = sourceRows.filter(record => keys.every(key => record[key] === row[key]));
      assert.deepEqual(applyQuery(data, { ...records, limit: 100 }, schema).rows.map(record => record.id).sort(), expected.map(record => record.id).sort(), idea.id);
    }
  }
});

test("switching grouped and record ideas clears old filters, HAVING, metrics and host search", () => {
  const first = usageQueryIdeas.messages.find(idea => idea.id === "messages:workspace-writing");
  const second = usageQueryIdeas.messages.find(idea => idea.id === "messages:assistant-replies");
  const grouped = { ...applyQueryIdea(prior, first), where: [{ field: "idea_messages", op: ">", value: 100 }] };
  const record = applyQueryIdea(grouped, second);
  assert.equal(record.grouping, undefined);
  assert.deepEqual(record.where, second.query.where);
  assert.deepEqual(record.aggregations ?? [], []);
  assert.deepEqual(record.hostState?.params, {});
  const regrouped = applyQueryIdea(record, first);
  assert.deepEqual(regrouped.grouping.source.where, first.where);
  assert.deepEqual(regrouped.where, []);
  assert.deepEqual(exitGrouping(regrouped).where, first.where);
});

test("means and rates use the whole denominator including zero durations and absent timing", () => {
  const data = [baseRow(schemas.tool_calls, 0), baseRow(schemas.tool_calls, 1)].map(row => ({ ...row, tool_name: "same" }));
  const idea = usageQueryIdeas.tool_calls.find(idea => idea.id === "tool_calls:tools");
  const result = applyQuery(data, applyQueryIdea(EMPTY_QUERY, idea), schemas.tool_calls).rows[0];
  assert.equal(result.idea_calls, 2);
  assert.equal(result.idea_timed, 1);
  assert.equal(result.idea_mean_duration, 0);
  assert.equal(result.idea_failure_rate, .5);
  const rollups = [baseRow(schemas.tools, 0), baseRow(schemas.tools, 1)].map(row => ({ ...row, tool_name: "same" }));
  const summary = applyQuery(rollups, applyQueryIdea(EMPTY_QUERY, usageQueryIdeas.tools[0]), schemas.tools).rows[0];
  assert.equal(summary.idea_failure_rate, .1);
  assert.equal(summary.idea_mean_duration, 100);
});

test("nullable message authorship stays outside classified workspace writing", () => {
  const schema = schemas.messages;
  assert.equal(filterValues(schema.fields.find(field => field.name === "text")).source, "freeform", "full message text filters do not request unsupported distinct values");
  const data = [
    { ...baseRow(schema, 0), id: "written", role: "user", has_authorship: true, typed_words: 12, pasted_words: 3, words: 20, work_id: "work one" },
    { ...baseRow(schema, 1), id: "unknown", role: "user", has_authorship: false, typed_words: null, words: null, work_id: null },
    { ...baseRow(schema, 2), id: "reply", role: "assistant", kind: "message", has_authorship: false, typed_words: null, words: null, work_id: null },
  ];
  const writing = usageQueryIdeas.messages.find(idea => idea.id === "messages:workspace-writing");
  const result = applyQuery(data, applyQueryIdea(EMPTY_QUERY, writing), schema).rows;
  assert.equal(result.length, 1);
  assert.equal(result[0].idea_messages, 1);
  assert.equal(result[0].idea_typed_words, 12);
  assert.equal(result[0].idea_authored_share, .6);
  const models = usageQueryIdeas.messages.find(idea => idea.id === "messages:models");
  assert.equal(applyQuery(data, applyQueryIdea(EMPTY_QUERY, models), schema).rows.reduce((total, row) => total + row.idea_messages, 0), 1, "assistant model comparisons exclude user messages");
  const roles = usageQueryIdeas.messages.find(idea => idea.id === "messages:roles-kinds");
  assert.equal(applyQuery(data, applyQueryIdea(EMPTY_QUERY, roles), schema).rows.reduce((total, row) => total + row.idea_messages, 0), 3);
});

test("representative message links use coherent row IDs and encode special characters", () => {
  const data = [baseRow(schemas.writing_messages, 0), baseRow(schemas.writing_messages, 1)].map(row => ({ ...row, conversation_id: "conv / &" }));
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


test("idea selection survives the table's evidence and Detail columns and label presentation defaults", () => {
  for (const dataset of ["messages", "message_labels"]) {
    const schema = usageQuerySchema(schemas[dataset], dataset);
    for (const idea of usageQueryIdeas[dataset]) {
      const drawn = usageActionQuery(applyQueryIdea(EMPTY_QUERY, idea), schema, dataset);
      assert.equal(queryIdeaMatches(drawn, idea), true, idea.id);
    }
  }
});
