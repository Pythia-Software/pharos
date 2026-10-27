import test from "node:test";
import assert from "node:assert/strict";
import { EMPTY_QUERY, applyQuery, applyAggregations, decodeQueryStrict, encodeQuery, loadSchema, normalizeQueryState, toMetricQuery, toQueryRequestV3 } from "@pythia-software/query-table-core";
import { queryCases, dashboardCases, dashboardControlCases } from "../../../../tools/usage-audit-cases.ts";
import usage from "../../../../schemas/usage.schema.json";
import conversationActivity from "../../../../schemas/conversation_activity.schema.json";
import writing from "../../../../schemas/writing.schema.json";
import writingMessages from "../../../../schemas/writing_messages.schema.json";
import tools from "../../../../schemas/tools.schema.json";
import toolCalls from "../../../../schemas/tool_calls.schema.json";
import skillUsages from "../../../../schemas/skill_usages.schema.json";
import parallelism from "../../../../schemas/parallelism.schema.json";
import mcpCalls from "../../../../schemas/mcp_calls.schema.json";
import { currentUsageAnalyses, isUsageWorkspace, readUsageWorkspace, usageAnalysisLink, usageDashboardReturn, usageDatasets, usageSearchQuery, usageTopics, usageWorkspaceURL } from "../../../src/usage-workspace-model.ts";
import { toolSummaryCallFilters } from "../../../src/tools-dashboard-model.ts";
import { readDashboardQuery, groupedDashboardQuery, parallelismSelectionQuery, toolDashboardWhere } from "../../../src/usage-dashboard-model.ts";
import { trendQuery } from "../../../src/dashboard-trend-model.ts";
import { analysisQuery } from "../../../src/dashboard-analysis-model.ts";
import { mcpAnalysisCatalog } from "../../../src/mcp-analysis-catalog.ts";
import { toolsAnalysisCatalog } from "../../../src/tools-analysis-catalog.ts";
import { agentTimeAnalysisCatalog, skillsAnalysisCatalog } from "../../../src/record-analysis-catalogs.ts";
// A dashboard's trend clause, as its supporting query carries it.
// A timestamp trend's supporting query opens records (Query Table request
// 6), so these dashboards' grouped links come from Compare.
const trendClause = (catalog, state) => trendQuery(catalog, state, [], { now: Date.parse("2026-10-20T12:00:00Z") }).supporting.aggregations?.[0] ?? analysisQuery(catalog, { ...state, view: "compare" }, []).clause;
import { timeBounds, usageTime, withTimeRange, smoothingWhere } from "../../../src/usage-chart.tsx";
import { dashboardQueryDetails, dashboardQueryLinks, dashboardQuerySentence } from "../../../src/dashboard-query-links.ts";
import { dashboardChartURL } from "../../../src/dashboard-chart-links.ts";

test('dashboard series links retain filters, resolve Other and null identities, and select contributing records', () => {
  const range = { from: new Date('2026-10-02T00:00:00Z'), to: new Date('2026-10-03T00:00:00Z'), rolling: false };
  const where = [{ field: 'provider', op: '=', value: 'codex' }, { field: 'model_family', op: '=', value: 'previous' }];
  const current = new URLSearchParams({ analyze_topic: 'tokens', analyze_chart: JSON.stringify({ period: 'day' }) });
  const series = { key: 'other', label: 'Other', color: 'gray', foldedValues: ['model-hidden', ''] };
  const href = dashboardChartURL({ dataset: 'usage', where, time: usageTime, range, series, split: 'model_family', fields: ['output_tokens'], current });
  const url = new URL(href, 'http://localhost'), query = decodeQueryStrict(url.searchParams.get('q_usage'));
  assert.equal(url.searchParams.get('analyze_mode'), 'query');
  assert.equal(url.searchParams.get('analyze_chart'), current.get('analyze_chart'));
  const rows = [
    { id: 'hidden', provider: 'codex', model_family: 'model-hidden', first_usage_at: '2026-10-02T12:00:00Z', last_usage_at: '2026-10-02T12:00:00Z', output_tokens: 1 },
    { id: 'missing', provider: 'codex', model_family: null, first_usage_at: '2026-10-02T12:00:00Z', last_usage_at: '2026-10-02T12:00:00Z', output_tokens: 2 },
  ];
  const candidates = [...rows,
    { ...rows[0], id: 'wrong-provider', provider: 'claude' },
    { ...rows[0], id: 'wrong-model', model_family: 'previous' },
    { ...rows[0], id: 'zero', output_tokens: 0 },
    { ...rows[0], id: 'outside', first_usage_at: '2026-10-03T00:00:00Z', last_usage_at: '2026-10-03T00:00:00Z' },
  ];
  assert.deepEqual(applyQuery(candidates, query, loadSchema(usage)).rows.map(row => row.id).sort(), ['hidden', 'missing']);
  assert.deepEqual(where, [{ field: 'provider', op: '=', value: 'codex' }, { field: 'model_family', op: '=', value: 'previous' }]);
});

test('dashboard links use each record dataset, raw dimension, contributing field, and time semantics', () => {
  const from = new Date('2026-10-02T00:00:00Z'), to = new Date('2026-10-03T00:00:00Z');
  const cases = [
    { dataset: 'usage', document: usage, time: usageTime, split: 'model_effort', value: 'gpt-6-sol / high', fields: ['total_tokens'] },
    { dataset: 'usage', document: usage, time: usageTime, split: 'carbon_tier', value: 'large', fields: ['energy_wh'] },
    { dataset: 'writing', document: writing, time: { first: 'first_message_at', last: 'last_message_at' }, split: 'providers', value: 'claude,codex', fields: ['typed_words'] },
    { dataset: 'writing', document: writing, time: { first: 'first_message_at', last: 'last_message_at' }, fields: ['quoted_words', 'resent_words'] },
    { dataset: 'tool_calls', document: toolCalls, time: { first: 'started_at', last: 'started_at' }, split: 'activity', value: 'shell', fields: ['duration_ms'] },
    { dataset: 'skill_usages', document: skillUsages, time: { first: 'created_at', last: 'created_at' }, split: 'skill_name', value: '/path/to/skill', fields: [] },
  ];
  for (const spec of cases) {
    const series = { key: 'series', label: 'Display label is not a filter', color: 'gray', value: spec.value };
    const href = dashboardChartURL({ ...spec, where: [], range: { from, to, rolling: false }, series });
    const params = new URL(href, 'http://localhost').searchParams;
    assert.equal(params.get('analyze_data'), spec.dataset);
    const query = decodeQueryStrict(params.get(`q_${spec.dataset}`));
    const row = { id: 'contributor', [spec.time.first]: '2026-10-02T12:00:00Z', [spec.time.last]: '2026-10-02T12:00:00Z', ...spec.split ? { [spec.split]: spec.value } : {}, ...spec.fields.length ? { [spec.fields.at(-1)]: 2 } : {} };
    assert.equal(applyQuery([row], query, loadSchema(spec.document)).rows.length, 1, spec.dataset);
    assert.ok(!query.where.some(term => 'field' in term && term.value === series.label));
  }
});

test('inspection clamps partial intervals but includes the full averaging window', () => {
  const where = [
    { field: 'first_usage_at', op: '>=', value: '2026-10-02T12:00:30.123Z' },
    { field: 'last_usage_at', op: '<', value: '2026-10-05T12:00:40.456Z' },
  ];
  const series = { key: 'tokens', label: 'Tokens', color: 'gray' };
  const range = { from: new Date('2026-10-01T00:00:00Z'), to: new Date('2026-10-06T00:00:00Z'), rolling: false };
  for (const rolling of [false, true]) {
    const source = rolling ? smoothingWhere(where, usageTime, 'day', 3) : where;
    const href = dashboardChartURL({ dataset: 'usage', where: source, time: usageTime, range: { ...range, rolling }, series });
    const query = decodeQueryStrict(new URL(href, 'http://localhost').searchParams.get('q_usage'));
    const bounds = timeBounds(query.where, usageTime);
    assert.equal(bounds.from.getTime(), rolling ? range.from.getTime() : Date.parse(where[0].value));
    assert.equal(bounds.to.getTime(), Date.parse(where[1].value));
    const overlapping = [
      { id: 'before-source-start', first_usage_at: '2026-10-01T00:00:00Z', last_usage_at: '2026-10-03T00:00:00Z' },
      { id: 'after-source-end', first_usage_at: '2026-10-03T00:00:00Z', last_usage_at: '2026-10-06T00:00:00Z' },
    ];
    assert.deepEqual(applyQuery(overlapping, query, loadSchema(usage)).rows.map(row => row.id), rolling ? ['before-source-start'] : []);
  }
});

test("dashboard destinations describe their encoded query and consolidate record search", () => {
  const schema = loadSchema(usage);
  const query = { ...EMPTY_QUERY, where: [
    { field: "provider", op: "=", value: "codex" },
    { field: "first_usage_at", op: ">=", value: "-30d" },
    { field: "first_usage_at", op: "<", value: "+0s" },
  ], aggregations: [{ id: "tokens", op: "sum", field: "total_tokens", groupBy: ["day", "model_family"], label: "Tokens", sort: [{ key: "value", dir: "desc" }] }] };
  const original = structuredClone(query);
  const links = dashboardQueryLinks("usage", query, schema, new URLSearchParams({ analyze_topic: "tokens" }));
  assert.equal(links.length, 4);
  for (const link of links) {
    const params = new URL(link.href, "http://localhost").searchParams;
    assert.deepEqual(decodeQueryStrict(params.get("q_usage")), normalizeQueryState(link.query));
    assert.equal(readUsageWorkspace(params).search, true);
    assert.deepEqual(link.details, dashboardQueryDetails(schema, decodeQueryStrict(params.get("q_usage"))));
    assert.deepEqual(link.details.find(detail => detail.label === "Filters").values, ["Provider is Codex", "First usage is on or after -30 days", "First usage is before now"]);
  }
  const grouped = links.find(link => link.query.grouping), records = links.find(link => !link.query.grouping);
  assert.deepEqual(grouped.details.find(detail => detail.label === "Group by").values, ["Day", "Model"]);
  assert.deepEqual(grouped.details.find(detail => detail.label === "Measures").values, ["Tokens"]);
  assert.deepEqual(grouped.details.find(detail => detail.label === "Sort").values, ["Tokens descending", "Day ascending", "Model ascending"]);
  assert.deepEqual(records.details.find(detail => detail.label === "Sort").values, ["Cost (API $) descending"]);
  assert.deepEqual(records.query.where, usageSearchQuery(query).where);
  assert.deepEqual(query, original);
});

test("dashboard filter descriptions preserve alternatives, negations, empties and date gaps", () => {
  const schema = loadSchema(usage);
  const query = { ...EMPTY_QUERY, where: [
    { any: [{ field: "provider", op: "=", value: "codex" }, { field: "provider", op: "=", value: "claude" }] },
    { any: [{ field: "repository_name", op: "contains", value: "pharos", negated: true }, { field: "provider", op: "is_null", value: "" }] },
    { any: ["2026-10-02", "2026-10-01", "2026-10-03"].map(value => ({ field: "day", op: "=", value })) },
    { any: ["2026-10-01", "2026-10-03"].map(value => ({ field: "day", op: "=", value })) },
    { field: "provider", op: "!=", value: "claude" },
    { field: "first_usage_at", op: "<", value: "2026-10-21T12:00:00.000Z" },
    { field: "last_usage_at", op: ">", value: "2026-10-21T12:34:56.123-06:00" },
    { any: [] },
  ] };
  assert.deepEqual(dashboardQueryDetails(schema, query)[0].values, [
    "(Provider is Codex or Claude)", "(not (Repository contains pharos) or Provider is empty)",
    "Day from 2026-10-01 to 2026-10-03", "(Day is 2026-10-01 or 2026-10-03)", "Provider is not Claude",
    "First usage is before 2026-10-21 12:00 UTC", "Last usage is after 2026-10-21 12:34:56.123 -06:00", "No matching records",
  ]);
});

test("dashboard previews distinguish source and group filters and retain custom sorts", () => {
  const schema = loadSchema(usage), source = { ...EMPTY_QUERY, orderBy: [{ field: "total_tokens", dir: "asc" }], where: [{ field: "repository_name", op: "=", value: "pharos" }] };
  const query = { ...EMPTY_QUERY, grouping: { source, keys: [], summaries: [{ id: "total", label: "Total tokens", op: "sum", field: "total_tokens" }], representative: null }, where: [{ field: "total", op: ">", value: "1000" }], orderBy: [{ field: "total", dir: "desc" }] };
  const links = dashboardQueryLinks("usage", query, schema, new URLSearchParams());
  const details = Object.fromEntries(links[0].details.map(detail => [detail.label, detail.values]));
  assert.deepEqual(details.Filters, ["Repository is pharos"]);
  assert.deepEqual(details["Group filters"], ["Total tokens is greater than 1000"]);
  assert.equal(details["Group by"], undefined);
  assert.deepEqual(details.Sort, ["Total tokens descending"]);
  const sentence = dashboardQuerySentence(links[0].details);
  assert.deepEqual(sentence.map(part => part.keyword), ["Show", "where", "keeping groups where", "sorted by"]);
  assert.equal(sentence.map(part => `${part.prefix}${part.keyword}${part.text ? ` ${part.text}` : ""}`).join(""), "Show Total tokens where Repository is pharos, keeping groups where Total tokens is greater than 1000, sorted by Total tokens descending");
  assert.deepEqual(links[1].details.find(detail => detail.label === "Sort").values, ["Cost (API $) descending"]);
  assert.deepEqual(links[1].query.where, source.where);
  const ungrouped = dashboardQueryLinks("usage", EMPTY_QUERY, schema, new URLSearchParams());
  assert.ok(ungrouped.every(link => !link.details.some(detail => detail.label === "Filters")));
  assert.deepEqual(dashboardQueryDetails(schema, EMPTY_QUERY), [], "empty filters, grouping and sorting have no row, including implicit schema defaults");
  assert.deepEqual(dashboardQueryDetails(schema, { ...EMPTY_QUERY, aggregations: [] }), []);
  assert.deepEqual(dashboardQuerySentence([]), []);
  assert.deepEqual(dashboardQuerySentence([{ label: "Sort", values: ["Cost descending", "Day ascending"] }]), [
    { prefix: "", keyword: "Show", text: "records" },
    { prefix: ", ", keyword: "sorted by", text: "Cost descending, then Day ascending" },
  ]);
  const extracted = { ...source, orderBy: [{ field: "model", dir: "asc", nulls: "first", extract: { regex: "([0-9]+)" } }] };
  assert.deepEqual(dashboardQueryDetails(schema, extracted).find(detail => detail.label === "Sort").values, ["Reported model matching /([0-9]+)/ ascending (empty first)"]);
});

test("all eight dashboards offer four distinct native queries that retain source scope", () => {
  const documents = { usage, writing, tools, tool_calls: toolCalls, skill_usages: skillUsages, parallelism, mcp_calls: mcpCalls };
  const configs = [
    ["tokens", "usage", { op: "sum", field: "total_tokens", groupBy: ["day", "model_family"], label: "Tokens" }],
    ["carbon", "usage", { op: "sum", field: "co2e_g", groupBy: ["day", "carbon_tier"], label: "Estimated CO₂e" }],
    ["human", "writing", { op: "sum", field: "typed_words", groupBy: ["repository_name"], label: "Typed words" }],
    ["skills", "skill_usages", trendClause(skillsAnalysisCatalog, { metric: "records", split: "skill_name" })],
    ["time", "tool_calls", trendClause(agentTimeAnalysisCatalog, { metric: "time", split: "activity" })],
    ["parallelism", "parallelism", { op: "sum", field: "unit_hours", groupBy: ["repository_name"], label: "Active unit hours" }],
    ["tools", "tools", analysisQuery(toolsAnalysisCatalog, { view: "compare", metric: "calls", split: "tool_category" }, []).clause],
    ["mcp", "mcp_calls", analysisQuery(mcpAnalysisCatalog, { view: "compare", metric: "calls", split: "tool_name" }, []).clause],
  ];
  for (const [topic, dataset, metric] of configs) {
    const schema = loadSchema(documents[dataset]);
    const where = [{ field: dataset === "mcp_calls" ? "query_source" : "repository_name", op: "=", value: "pharos" }];
    const query = { ...EMPTY_QUERY, fitToScreen: false, where, aggregations: [{ ...metric, id: "chart" }] }, original = structuredClone(query);
    const links = dashboardQueryLinks(dataset, query, schema, new URLSearchParams({ analyze_topic: topic, analyze_carbon: '{"scenario":"high"}' }));
    assert.equal(links.length, 4, topic);
    assert.equal(new Set(links.map(link => link.title)).size, 4, topic);
    assert.equal(new Set(links.map(link => encodeQuery(link.query))).size, 4, topic);
    for (const link of links) {
      const url = new URL(link.href, "http://localhost"), restored = decodeQueryStrict(url.searchParams.get(`q_${dataset}`));
      assert.deepEqual(restored, normalizeQueryState(link.query));
      assert.equal(restored.fitToScreen, true, `${topic}: ${link.title} fits its columns to the screen`);
      assert.deepEqual(toQueryRequestV3(restored, schema, { version: 3, profile: "qt-sqlite-v1", groupedRows: true, computedSourceFilters: true }).diagnostics ?? [], [], `${topic}: ${link.title}`);
      assert.deepEqual((restored.grouping?.source ?? restored).where.slice(0, where.length), where);
      assert.equal(readUsageWorkspace(url.searchParams).topic, topic);
      if (topic === "carbon") assert.equal(url.searchParams.get("analyze_carbon"), '{"scenario":"high"}');
      assert.ok(link.details.every(detail => detail.values.length));
    }
    assert.deepEqual(query, original);
  }
});

test("focused dashboard queries execute group thresholds and weighted measures", () => {
  const skillLinks = dashboardQueryLinks("skill_usages", { ...EMPTY_QUERY, aggregations: [trendClause(skillsAnalysisCatalog, { metric: "records", split: "skill_name" })] }, loadSchema(skillUsages), new URLSearchParams());
  const repeats = skillLinks.find(link => link.query.grouping?.summaries.some(column => column.id === "explore_body_bytes"));
  const evidence = [
    { id: "1", skill_name: "review", conversation_id: "a", title: "First", evidence_type: "file_load", content_bytes: 100, created_at: "2026-10-01T00:00:00Z" },
    { id: "2", skill_name: "review", conversation_id: "a", title: "Latest", evidence_type: "file_load", content_bytes: -1, created_at: "2026-10-02T00:00:00Z" },
    { id: "3", skill_name: "review", conversation_id: "b", evidence_type: "file_load", content_bytes: 200 },
    { id: "4", skill_name: "review", conversation_id: "b", evidence_type: "explicit_invocation", content_bytes: 200 },
  ];
  const rows = applyQuery(evidence, repeats.query, loadSchema(skillUsages)).rows;
  assert.equal(rows.length, 1);
  assert.equal(rows[0].conversation_id, "a");
  assert.equal(rows[0].explore_count, 2);
  assert.equal(rows[0].explore_body_bytes, 100);
  assert.equal(rows[0].explore_name, "Latest");

  const toolLinks = dashboardQueryLinks("tools", analysisQuery(toolsAnalysisCatalog, { view: "compare", metric: "calls", split: "tool_category" }, []).query, loadSchema(tools), new URLSearchParams());
  const failures = toolLinks.find(link => link.query.grouping?.summaries.some(column => column.id === "explore_error_rate"));
  const rates = applyQuery([{ id: "1", tool_name: "shell", provider: "codex", call_count: 10, error_count: 1 }, { id: "2", tool_name: "shell", provider: "codex", call_count: 90, error_count: 0 }], failures.query, loadSchema(tools)).rows;
  assert.equal(rates[0].explore_error_rate, 0.01);
});

test("the live audit provides 25 valid, distinct queries per dataset and dashboard", () => {
  const documents = { usage, conversation_activity: conversationActivity, writing, writing_messages: writingMessages, tools, tool_calls: toolCalls, mcp_calls: mcpCalls, skill_usages: skillUsages, parallelism };
  const capabilities = { version: 3, profile: "qt-sqlite-v1", groupedRows: true, computedSourceFilters: true };
  for (const [dataset, document] of Object.entries(documents)) {
    const schema = loadSchema(document), cases = queryCases(dataset, document, []);
    assert.equal(cases.length, 25);
    assert.equal(new Set(cases.map(item => item.url)).size, 25);
    for (const item of cases) {
      assert.ok(decodeQueryStrict(encodeQuery(item.query)), item.id);
      assert.deepEqual(toQueryRequestV3(item.query, schema, capabilities).diagnostics ?? [], [], item.id);
    }
  }
  for (const topic of Object.keys(usageTopics)) {
    const cases = dashboardCases(topic, documents, {}, { period_start: "2026-10-01", period_end: "2026-10-14", threads: [] });
    assert.equal(cases.length, 25);
    assert.equal(new Set(cases.map(item => item.url)).size, 25);
    for (const item of cases) assert.equal(readUsageWorkspace(new URL(item.url, "http://localhost").searchParams).topic, topic);
  }
  const controls = dashboardControlCases(documents, {}, { period_start: "2026-10-01", period_end: "2026-10-14", threads: [] });
  assert.equal(new Set(controls.map(item => JSON.stringify([item.url, item.interaction]))).size, controls.length);
  for (const item of controls) {
    assert.ok(Number(item.id.slice(-2)) > 25 && Number(item.id.slice(-2)) <= 30);
    assert.ok(item.interaction);
    assert.equal(readUsageWorkspace(new URL(item.url, "http://localhost").searchParams).topic, item.page.slice("dashboard:".length));
  }
});

test("every Analyze dataset and dashboard topic has a reloadable route", () => {
  const schemas = { usage, conversation_activity: conversationActivity, writing, writing_messages: writingMessages, tools, tool_calls: toolCalls, mcp_calls: mcpCalls, skill_usages: skillUsages, parallelism };
  for (const [dataset, target] of Object.entries(usageDatasets)) assert.ok(schemas[dataset].fields.some(field => field.name === target.field), `${dataset}'s search entry point belongs to its own schema`);
  for (const dataset of Object.keys(usageDatasets)) for (const mode of ["query", "search"]) {
    const url = new URL(usageWorkspaceURL({ mode, dataset }), "http://localhost");
    assert.equal(url.pathname, "/analyze");
    assert.equal(readUsageWorkspace(url.searchParams).dataset, dataset);
    assert.equal(readUsageWorkspace(url.searchParams).mode, "query");
    assert.equal(readUsageWorkspace(url.searchParams).search, mode === "search");
    const query = { ...EMPTY_QUERY, select: [{ field: "id", width: 175 }], where: [{ field: "id", op: "=", value: "example" }], orderBy: [{ field: "id", dir: "asc" }], limit: 25 };
    const linked = new URL(usageWorkspaceURL({ mode, dataset, query }), "http://localhost");
    assert.deepEqual(decodeQueryStrict(linked.searchParams.get(`q_${dataset}`)), { ...query, fitToScreen: true }, `${dataset} fits to screen without changing the analysis`);
    assert.equal(query.fitToScreen, undefined, "Link generation preserves the input query");
  }
  for (const topic of Object.keys(usageTopics)) assert.equal(readUsageWorkspace(new URL(usageWorkspaceURL({ topic }), "http://localhost").searchParams).topic, topic);
  assert.ok(currentUsageAnalyses.every(item => Object.hasOwn(usageTopics, item.topic)));
});

test("Analyze defaults to its sidebar layout while explicit older analyses remain reachable", () => {
  assert.equal(isUsageWorkspace(new URLSearchParams()), true);
  assert.equal(isUsageWorkspace(new URLSearchParams("analyze_view=tokens&machine_view=time")), false);
  assert.equal(isUsageWorkspace(new URLSearchParams("analyze_view=writing&writing=messages")), false);
  assert.equal(isUsageWorkspace(new URLSearchParams("analyze_workspace=1&analyze_view=tokens")), true);
  const url = new URL(usageWorkspaceURL({ topic: "mcp" }, new URLSearchParams("analyze_workspace=1&analyze_topic=tools&analyze_chart=old&analyze_filters=old")), "http://localhost");
  assert.equal(url.searchParams.has("analyze_workspace"), false);
  assert.equal(url.searchParams.has("analyze_chart"), false);
  assert.equal(url.searchParams.has("analyze_filters"), false);
  assert.equal(readUsageWorkspace(url.searchParams).topic, "mcp");
});




test("Query links retain complete grouped queries and Search explicitly opens source records", () => {
  const source = { ...EMPTY_QUERY, offset: 50, where: [{ field: "provider", op: "=", value: "codex" }], aggregations: [{ id: "tokens", op: "sum", field: "total_tokens", groupBy: [] }] };
  const query = { ...EMPTY_QUERY, grouping: { source, keys: ["model"], summaries: [], representative: null }, where: [{ field: "sum_tokens", op: ">", value: "1000" }] };
  const params = new URL(usageWorkspaceURL({ mode: "query", dataset: "usage", query }), "http://localhost").searchParams;
  assert.deepEqual(decodeQueryStrict(params.get("q_usage")), { ...query, fitToScreen: true });
  assert.deepEqual(usageSearchQuery(query), { ...source, offset: 0, aggregations: [] });
  assert.equal(query.grouping.source.offset, 50, "Link generation must not change the original query");
});

test("dashboard returns retain edited source filters and disclose incompatible resets", () => {
  const source = { ...EMPTY_QUERY, where: [{ field: "provider", op: "=", value: "codex" }] };
  const current = new URLSearchParams({ analyze_mode: "query", analyze_data: "usage", analyze_topic: "tokens", q_usage: encodeQuery(source), analyze_chart: '{"split":"model"}' });
  let back = usageDashboardReturn(current), params = new URL(back.href, "http://localhost").searchParams;
  assert.deepEqual(decodeQueryStrict(params.get("analyze_filters")).where, source.where);
  assert.equal(params.get("analyze_chart"), current.get("analyze_chart"));
  assert.equal(back.resetsFilters, false);
  const grouped = { ...EMPTY_QUERY, grouping: { source, keys: ["model"], summaries: [], representative: null }, where: [{ field: "model", op: "=", value: "gpt" }] };
  current.set("q_usage", encodeQuery(grouped));
  back = usageDashboardReturn(current);
  assert.match(back.label, /resets group filters/);
  assert.deepEqual(decodeQueryStrict(new URL(back.href, "http://localhost").searchParams.get("analyze_filters")).where, source.where);
  current.set("analyze_topic", "human");
  back = usageDashboardReturn(current);
  assert.match(back.label, /resets query filters/);
  assert.deepEqual(decodeQueryStrict(new URL(back.href, "http://localhost").searchParams.get("analyze_filters")).where, []);
});

test("legacy analyses carry the full query and linked chart assumptions or label a reset", () => {
  const query = { ...EMPTY_QUERY, where: [{ field: "provider", op: "=", value: "claude" }] };
  const current = new URLSearchParams({ analyze_chart: '{"split":"type"}', analyze_carbon: '{"scenario":"high"}' });
  const linked = usageAnalysisLink("usage", query, "/analyze?analyze_view=carbon", current);
  const params = new URL(linked.href, "http://localhost").searchParams;
  assert.equal(linked.resetsFilters, false);
  assert.deepEqual(decodeQueryStrict(params.get("q_carbon")), query);
  assert.equal(params.get("analyze_carbon"), current.get("analyze_carbon"));
  const reset = usageAnalysisLink("tool_calls", query, "/analyze?machine_view=time", current);
  assert.equal(reset.resetsFilters, true);
});

test("changing records drops stale table context while switching modes keeps the current query window", () => {
  const current = new URLSearchParams({ analyze_workspace: "1", analyze_data: "usage", q_usage: "saved", q_tools: "stale", window_dataset: "usage", window_days: "30", window_field: "first_usage_at", window_mode: "relative", machine_view: "time", activity_repo: "pharos", analyze_view: "writing" });
  const switched = new URL(usageWorkspaceURL({ mode: "search" }, current), "http://localhost").searchParams;
  assert.equal(switched.get("q_usage"), "saved");
  assert.equal(switched.get("window_mode"), "relative");
  assert.equal(switched.has("q_tools"), false);
  const different = new URL(usageWorkspaceURL({ mode: "query", dataset: "tool_calls" }, current), "http://localhost").searchParams;
  assert.equal(different.has("q_usage"), false);
  assert.equal(different.has("window_dataset"), false);
  for (const key of ["analyze_view", "machine_view", "activity_repo"]) assert.equal(different.has(key), false);
  const recipe = new URLSearchParams({ q_tool_calls: "saved", window_dataset: "tool_calls", window_days: "7", window_mode: "relative", window_field: "started_at" });
  const opened = new URL(usageWorkspaceURL({ mode: "query", dataset: "tool_calls", query: EMPTY_QUERY }, recipe), "http://localhost").searchParams;
  assert.equal(opened.get("window_dataset"), "tool_calls");
  assert.equal(opened.get("window_mode"), "relative");
  const dashboard = new URL(usageWorkspaceURL({ mode: "dashboards" }, current), "http://localhost").searchParams;
  assert.equal(dashboard.has("q_usage"), false, "A saved question must not claim to apply a custom query it cannot represent");
  assert.equal(dashboard.has("window_dataset"), false);
  const chart = new URLSearchParams({ analyze_topic: "tokens", analyze_chart: "tokens-view", analyze_filters: "token-filters" });
  const another = new URL(usageWorkspaceURL({ topic: "skills" }, chart), "http://localhost").searchParams;
  assert.equal(another.has("analyze_chart"), false, "Different graphs use different controls and fields");
  assert.equal(another.has("analyze_filters"), false);
});

test("invalid page selections fall back to visible, usable defaults", () => {
  assert.deepEqual(readUsageWorkspace(new URLSearchParams("analyze_mode=sql&analyze_data=constructor&analyze_topic=prototype&analyze_days=2")), { mode: "dashboards", search: false, dataset: "usage", topic: "tokens", report: "", days: 30 });
});


test("dashboard filter links round trip and malformed filters produce a visible error", () => {
  const query = { ...EMPTY_QUERY, where: [{ field: "repository_name", op: "=", value: "pharos" }] };
  assert.deepEqual(readDashboardQuery(new URLSearchParams({ analyze_filters: encodeQuery(query) }), EMPTY_QUERY), { query, error: "" });
  assert.ok(readDashboardQuery(new URLSearchParams({ analyze_filters: "broken" }), EMPTY_QUERY).error);
});


test("grouped dashboard links execute source filters and formulas as native result rows", () => {
  const schema = loadSchema(toolCalls);
  const records = [
    { id: "a", provider: "codex", tool_name: "shell", duration_ms: 100, result_tokens: 4 },
    { id: "b", provider: "codex", tool_name: "shell", duration_ms: 200, result_tokens: 6 },
    { id: "c", provider: "codex", tool_name: "read", duration_ms: 20, result_tokens: 3 },
    { id: "d", provider: "claude", tool_name: "shell", duration_ms: 5000, result_tokens: 50 },
  ];
  const query = { ...EMPTY_QUERY, where: [{ field: "provider", op: "=", value: "codex" }], aggregations: [
    { id: "calls", op: "count", groupBy: ["tool_name"], label: "Calls" },
    { id: "time", op: "sum", field: "duration_ms", groupBy: ["tool_name"], label: "Duration" },
    { id: "words", op: "sum", expression: "SUM([result_tokens])/COUNT()", groupBy: ["tool_name"], label: "Mean result tokens" },
  ] };
  const original = structuredClone(query);
  const grouped = groupedDashboardQuery(query);
  const restored = decodeQueryStrict(encodeQuery(grouped));
  assert.deepEqual(restored, grouped);
  assert.deepEqual(grouped.grouping.source.where, query.where);
  assert.deepEqual(grouped.grouping.summaries[1].display, { kind: "duration", sourceUnit: "milliseconds" });
  const result = applyQuery(records, restored, schema);
  assert.deepEqual(result.rows.map(row => [row.tool_name, row.dashboard_1, row.dashboard_2, row.dashboard_3]), [["read", 1, 20, 3], ["shell", 2, 300, 5]]);
  const filtered = applyQuery(records, { ...restored, where: [{ field: "dashboard_1", op: ">", value: "1" }] }, schema);
  assert.equal(filtered.rows.length, 1, "Optional structured search on groups uses HAVING without changing the source");
  assert.equal(filtered.rows[0].tool_name, "shell");
  assert.deepEqual(query, original);
  assert.strictEqual(groupedDashboardQuery(grouped), grouped);
  assert.throws(() => groupedDashboardQuery({ ...query, aggregations: [...query.aggregations, { id: "different", op: "count", groupBy: ["provider"] }] }), /same grouping/);
  assert.throws(() => groupedDashboardQuery({ ...query, aggregations: [{ ...query.aggregations[0], where: [{ field: "status", op: "=", value: "error" }] }] }), /own grouped-query/);
});

test("parallelism links preserve the selected unit, repository and hours overlapping a zoom", () => {
  const bounds = [Date.parse("2026-10-20T10:15:00Z"), Date.parse("2026-10-20T11:30:00Z")];
  const selected = { workspace_id: "workspace", conversation_id: "child", root_conversation_id: "root" };
  const settings = { counting: "agent", gap: 15, repository: "pharos", mode: "model" };
  const query = parallelismSelectionQuery({ settings, bounds, selected });
  const schema = loadSchema(parallelism);
  const common = { counting: "agent", gap_minutes: 15, repository_name: "pharos", unit_id: "root", unit_hours: 1 };
  const rows = ["09:00", "10:00", "11:00", "12:00"].map((hour, id) => ({ ...common, id, started_at: `2026-10-20T${hour}:00Z` }));
  rows.push({ ...rows[1], id: 9, unit_id: "another" });
  assert.deepEqual(applyQuery(rows, query, schema).rows.map(row => row.id).sort(), [1, 2]);
  assert.ok(parallelismSelectionQuery({ settings: { ...settings, counting: "workspace" }, bounds, selected }).where.some(term => term.field === "unit_id" && term.value === "workspace"));
  assert.deepEqual(groupedDashboardQuery(query).grouping.keys, ["repository_name"]);
});


test("Tools range filters select whole local calendar days through the native text schema", () => {
  const schema = loadSchema(tools);
  const bounds = { from: new Date(2024, 2, 8), to: new Date(2024, 2, 12) };
  const where = toolDashboardWhere([{ field: "day", op: ">=", value: bounds.from.toISOString() }, { field: "day", op: "<", value: bounds.to.toISOString() }, { field: "provider", op: "=", value: "codex" }], bounds);
  const rows = ["2024-03-07", "2024-03-08", "2024-03-09", "2024-03-10", "2024-03-11", "2024-03-12"].map((day, id) => ({ id, day, provider: "codex", tool_name: "shell", call_count: 1 }));
  const query = { ...EMPTY_QUERY, where, aggregations: [{ id: "calls", op: "sum", field: "call_count", groupBy: ["tool_name"] }] };
  assert.deepEqual(toMetricQuery(query, schema).diagnostics, []);
  assert.deepEqual(applyQuery(rows, query, schema).rows.map(row => row.day).sort(), ["2024-03-08", "2024-03-09", "2024-03-10", "2024-03-11"]);
  assert.deepEqual(toolDashboardWhere([], {}), []);
  const year = toolDashboardWhere([], { from: new Date(2023, 0, 1), to: new Date(2024, 0, 1) });
  assert.deepEqual(toMetricQuery({ ...query, where: year }, schema).diagnostics, []);
  const endpoints = [{ id: "before", day: "2022-12-31" }, { id: "start", day: "2023-01-01" }, { id: "end", day: "2023-12-31" }, { id: "after", day: "2024-01-01" }];
  assert.deepEqual(applyQuery(endpoints, { ...EMPTY_QUERY, where: year, orderBy: [{ field: "day", dir: "asc" }] }, schema).rows.map(row => row.id), ["start", "end"]);
  for (const [range, expected] of [
    [{ to: new Date(2023, 0, 2) }, ["before", "start"]],
    [{ from: new Date(2023, 0, 1), to: new Date(2028, 0, 1) }, ["start", "end", "after"]],
    [{ from: new Date(2024, 0, 2), to: new Date(2023, 0, 1) }, []],
  ]) {
    const query = { ...EMPTY_QUERY, where: toolDashboardWhere([], range), orderBy: [{ field: "day", dir: "asc" }] };
    assert.deepEqual(toMetricQuery({ ...query, aggregations: [{ id: 'count', op: 'count', groupBy: [] }] }, schema).diagnostics, []);
    assert.deepEqual(applyQuery(endpoints, query, schema).rows.map(row => row.id), expected);
  }
});

test("custom date bounds survive URL encoding, preserve other filters, and clear explicitly", () => {
  const provider = { field: 'provider', op: '=', value: 'codex' }, from = new Date(2026, 9, 1), to = new Date(2026, 9, 9);
  for (const bounds of [{ from, to }, { from }, { to }]) {
    const where = withTimeRange([provider], usageTime, bounds.from, bounds.to);
    const query = decodeQueryStrict(encodeQuery({ ...EMPTY_QUERY, where }));
    assert.deepEqual(timeBounds(query.where, usageTime), bounds);
    assert.ok(query.where.some(term => term.field === 'provider' && term.value === 'codex'));
    assert.deepEqual(withTimeRange(query.where, usageTime), [provider]);
  }
});


test("Tools comparisons weight failure rates and mean durations by their actual call populations", () => {
  const schema = loadSchema(tools);
  const rows = [
    { id: "a", provider: "codex", day: "2026-10-01", tool_name: "shell", tool_category: "command", call_count: 1000, error_count: 10, timed_count: 100, total_duration_ms: 10000 },
    { id: "b", provider: "codex", day: "2026-10-02", tool_name: "shell", tool_category: "command", call_count: 1, error_count: 1, timed_count: 1, total_duration_ms: 1000 },
    { id: "c", provider: "codex", day: "2026-10-02", tool_name: "read", tool_category: "read", call_count: 50, error_count: 0, timed_count: 0, total_duration_ms: 0 },
    { id: "d", provider: "claude", day: "2026-10-02", tool_name: "shell", tool_category: "command", call_count: 9, error_count: 9, timed_count: 9, total_duration_ms: 9999 },
  ];
  for (const [metric, expected, display] of [["failure_rate", 11 / 1001, "percent"], ["mean_time", 11000 / 101, "duration"]]) {
    const analysis = analysisQuery(toolsAnalysisCatalog, { view: "compare", metric, split: "tool_name" }, [{ field: "provider", op: "=", value: "codex" }]);
    const measured = applyAggregations(rows, analysis.query, schema).metrics[0];
    assert.equal(measured.error, undefined);
    assert.equal(measured.buckets.find(bucket => bucket.keys[0] === "shell").value, expected);
    const grouped = decodeQueryStrict(encodeQuery(analysis.supporting));
    assert.equal(grouped.grouping.summaries[0].display.kind, display);
    assert.equal(grouped.orderBy[0].field, "analysis_value", "The grouped table retains the graph’s ranking");
    const result = applyQuery(rows, grouped, schema).rows;
    const shell = result.find(row => row.tool_name === "shell");
    assert.equal(shell.analysis_value, expected);
    assert.equal(shell.analysis_denominator, metric === "failure_rate" ? 1001 : 101, "Grouped rows include the pooled population");
    assert.equal(shell.analysis_numerator, metric === "failure_rate" ? 11 : 11000, "The numerator remains inspectable");
    if (metric === "mean_time") assert.equal(result.find(row => row.tool_name === "read").analysis_value, null, "Missing timing must not look instantaneous");
  }
});





test("Tool summary call links keep every dimension of the selected row", () => {
  const base = { day: "2026-10-01", tool_name: "read_mcp_resource", tool_category: "mcp", provider: "codex", model: "gpt", effort: "high", repository_name: "test", session_kind: "root", source_kind: "codex", program: null, subcommand: null, command_category: null, mcp_server: "alpha" };
  const rows = [{ ...base, id: "exact" }, { ...base, id: "server", mcp_server: "beta" }, { ...base, id: "category", command_category: "read" }, { ...base, id: "tool-category", tool_category: "read" }];
  const query = { ...EMPTY_QUERY, where: toolSummaryCallFilters(base) };
  assert.deepEqual(applyQuery(rows, query, loadSchema(toolCalls)).rows.map(row => row.id), ["exact"]);
});

// Row presentation is host-owned; native properties and row links must keep
// their behaviour independently of the compact collection projection.
import { createPropertyView, ITEM_DETAILS_COLUMN, selectedFields, toServerQuery } from "@pythia-software/query-table-core";
import { conversationRowURL, primaryUsageSource, usageDetailsSchema, usageRowLinks } from "../../../src/usage-row-model.ts";
import { usageActionQuery, usageDrilldownColumns, usageDrilldownField, usageQuerySchema } from "../../../src/usage-query-columns.ts";
import { orderedUsageQuery } from "../../../src/usage-query-order.ts";
import { authoredRenderers } from "../../../src/authored.tsx";
import { renderToStaticMarkup } from "react-dom/server";

test("Analyze defaults and Details cover every dataset without changing explicit saved projections", () => {
  const documents = { usage, conversation_activity: conversationActivity, writing, writing_messages: writingMessages, tools, tool_calls: toolCalls, mcp_calls: mcpCalls, skill_usages: skillUsages, parallelism };
  for (const [dataset, document] of Object.entries(documents)) {
    const schema = loadSchema(document), details = usageDetailsSchema(schema, dataset);
    assert.ok(details);
    assert.ok(schema.defaultSelect.length <= 9, dataset);
    for (const column of schema.defaultSelect) assert.ok(schema.fields.some(field => field.name === column.field), `${dataset}: ${column.field}`);
    const properties = createPropertyView(details).properties;
    assert.ok(properties.length > schema.defaultSelect.length, dataset);
    assert.ok(properties.every(property => details.fields.some(field => field.name === property.field)), dataset);
    const saved = { ...EMPTY_QUERY, select: [{ field: "id", width: 175 }] };
    assert.deepEqual(selectedFields(schema, saved).map(field => field.name), ["id"]);
  }
  assert.equal(usageDetailsSchema(loadSchema(usage), "library"), undefined);
  assert.ok(createPropertyView(usageDetailsSchema(loadSchema(toolCalls), "tool_calls")).properties.some(p => p.field === "result_text"));
  assert.ok(createPropertyView(usageDetailsSchema(loadSchema(mcpCalls), "mcp_calls")).properties.some(p => p.field === "arguments_json"));
});

test("Analyze keeps every drilldown after the selected data and before native Detail", () => {
  const documents = { usage, conversation_activity: conversationActivity, writing, writing_messages: writingMessages, tools, tool_calls: toolCalls, mcp_calls: mcpCalls, skill_usages: skillUsages, parallelism };
  for (const [dataset, document] of Object.entries(documents)) {
    const original = loadSchema(document), schema = usageQuerySchema(original, dataset);
    const saved = { ...EMPTY_QUERY, select: [{ field: ITEM_DETAILS_COLUMN }, { field: "id", width: 175 }] };
    const query = usageActionQuery(orderedUsageQuery(dataset, saved), schema, dataset);
    assert.deepEqual(query.select[0], saved.select[1], `${dataset} keeps the user's data columns and widths`);
    assert.equal(query.select.at(-1).field, ITEM_DETAILS_COLUMN);
    const columns = usageDrilldownColumns[dataset];
    assert.deepEqual(query.select.slice(1, -1).map(column => column.field), columns.map(column => usageDrilldownField(column.kind)));
    assert.strictEqual(usageActionQuery(query, schema, dataset), query, "Canonicalization must settle");
    const server = toServerQuery(query, schema);
    assert.ok(server.select.every(name => original.fields.some(field => field.name === name)), `${dataset} only requests backend fields`);
    for (const column of columns) for (const dependency of column.dependencies) {
      assert.ok(original.fields.some(field => field.name === dependency), `${dataset}: declared dependency ${dependency} exists`);
      assert.ok(server.select.includes(dependency), `${dataset}: narrow saved projections still load ${dependency}`);
    }
    const request = toQueryRequestV3(query, schema, { version: 3, profile: "qt-sqlite-v1", groupedRows: true, computedSourceFilters: true });
    assert.deepEqual(request.diagnostics ?? [], [], dataset);
    const fresh = usageActionQuery(EMPTY_QUERY, schema, dataset);
    assert.deepEqual(fresh.select.slice(0, original.defaultSelect.length), original.defaultSelect);
  }
});

test("Drilldown ordering reads a conversation forward and browses populations newest first", () => {
  const schema = loadSchema(writingMessages), where = [{ field: "work_id", op: "=", value: "work" }];
  const rows = [
    { id: "late", work_id: "work", sent_at: "2026-10-21T12:00:00Z" },
    { id: "early", work_id: "work", sent_at: "2026-10-20T12:00:00Z" },
    { id: "other", work_id: "elsewhere", sent_at: "2026-10-22T12:00:00Z" },
  ];
  const human = usageRowLinks("writing", { id: "work" }).find(link => link.kind === "messages");
  const scoped = decodeQueryStrict(new URL(human.href, "http://localhost").searchParams.get("q_writing_messages"));
  assert.deepEqual(applyQuery(rows, scoped, schema).rows.map(row => row.id), ["early", "late"]);
  const [dashboard] = dashboardQueryLinks("writing_messages", { ...EMPTY_QUERY, where }, schema, new URLSearchParams());
  assert.deepEqual(applyQuery(rows, dashboard.query, schema).rows.map(row => row.id), ["early", "late"]);
  assert.equal(dashboard.query.fitToScreen, true);
  const across = orderedUsageQuery("writing_messages", EMPTY_QUERY);
  assert.deepEqual(applyQuery(rows, across, schema).rows.map(row => row.id), ["other", "late", "early"]);
  for (const clause of [{ any: where }, { ...where[0], negated: true }, { ...where[0], op: "contains" }]) {
    assert.equal(orderedUsageQuery("writing_messages", { ...EMPTY_QUERY, where: [clause] }).orderBy[0].dir, "desc");
  }
  const custom = { ...EMPTY_QUERY, orderBy: [{ field: "typed_words", dir: "desc" }] };
  assert.strictEqual(orderedUsageQuery("writing_messages", custom), custom);
  for (const dataset of Object.keys(usageDatasets)) {
    const query = orderedUsageQuery(dataset, EMPTY_QUERY);
    assert.ok(query.orderBy.length, dataset);
    assert.deepEqual(decodeQueryStrict(encodeQuery(query)), query, `${dataset} sort survives reload`);
  }
  const summary = { ...EMPTY_QUERY, grouping: { source: EMPTY_QUERY, keys: ["work_id"], summaries: [], representative: null } };
  const grouped = orderedUsageQuery("writing_messages", summary);
  assert.deepEqual(grouped.orderBy, [{ field: "work_id", dir: "asc" }]);
  assert.strictEqual(orderedUsageQuery("writing_messages", grouped), grouped);
});

test("Usage source links retain exact messages, encoded identity and distinct evidence/body destinations", () => {
  const row = { workspace_id: "work /#+", conversation_id: "conversation /#+", call_id: "native /#+", call_message_id: "call /#+", result_message_id: "result /#+", started_at: "2026-10-21T10:30:00Z" };
  const links = usageRowLinks("tool_calls", row);
  const call = new URL(links[0].href, "http://localhost"), result = new URL(links[1].href, "http://localhost");
  assert.equal(decodeURIComponent(call.pathname.slice(6)), row.workspace_id);
  assert.equal(call.searchParams.get("conversation"), row.conversation_id);
  assert.equal(call.searchParams.get("message"), row.call_message_id);
  assert.equal(result.searchParams.get("message"), row.result_message_id);
  assert.equal(call.searchParams.get("call"), row.call_id);
  assert.equal(result.searchParams.get("call"), row.call_id);
  assert.equal(call.searchParams.has("at"), false);
  const skill = usageRowLinks("skill_usages", { ...row, evidence_message_id: "evidence", body_message_id: "body", tool_call_id: "tool" });
  assert.equal(new URL(skill[0].href, "http://localhost").searchParams.get("message"), "evidence");
  assert.equal(new URL(skill[1].href, "http://localhost").searchParams.get("message"), "body");
  const tool = decodeQueryStrict(new URL(skill[2].href, "http://localhost").searchParams.get("q_tool_calls"));
  assert.deepEqual(tool.itemDetails.active, { id: "tool", mode: "modal" });
  assert.deepEqual(tool.where, [{ field: "id", op: "=", value: "tool" }]);
  assert.equal(primaryUsageSource("writing_messages", { ...row, id: "human" }).includes("message=human"), true);
  assert.equal(conversationRowURL({}, "missing"), undefined);
  assert.deepEqual(usageRowLinks("skill_usages", {}), []);
  assert.equal(usageRowLinks("parallelism", { unit_id: "/worktree", counting: "workspace" }).length, 0);
});

test("Aggregate time and cross-query links express their source scope", () => {
  const row = { id: "work", workspace_id: "work", conversation_id: "child", root_conversation_id: "root", first_usage_at: "2026-10-21T10:30:00Z" };
  const current = new URLSearchParams({ analyze_carbon: "custom-assumptions" });
  const links = usageRowLinks("usage", row, current);
  const source = new URL(links[0].href, "http://localhost");
  assert.equal(source.searchParams.get("at"), row.first_usage_at);
  assert.equal(source.searchParams.has("message"), false);
  assert.match(links[0].label, /near/);
  const calls = new URL(links.find(link => link.label.includes("Tool calls")).href, "http://localhost");
  assert.equal(calls.searchParams.get("analyze_carbon"), "custom-assumptions");
  assert.deepEqual(decodeQueryStrict(calls.searchParams.get("q_tool_calls")).where, [{ field: "conversation_id", op: "=", value: "child" }]);
  assert.equal(links.find(link => link.label === "Root conversation").href, "/conversation/root");
  const human = usageRowLinks("writing", row, current).find(link => link.label.startsWith("Messages"));
  assert.deepEqual(decodeQueryStrict(new URL(human.href, "http://localhost").searchParams.get("q_writing_messages")).where, [{ field: "work_id", op: "=", value: "work" }]);
});

test("Authored Details retains classified text beyond the cell preview and reports retention limits", () => {
  const row = { text_truncated: true, segments: [{ category: "harness", text: "Full harness instructions\n" }, { category: "typed", text: "Human words ".repeat(90) + "Retained end" }] };
  const context = { row, field: loadSchema(writingMessages).fields.find(f => f.name === "text"), query: EMPTY_QUERY };
  const cell = renderToStaticMarkup(authoredRenderers.authored_text({ ...context, presentation: "cell" }));
  const detail = renderToStaticMarkup(authoredRenderers.authored_text({ ...context, presentation: "property" }));
  assert.doesNotMatch(cell, /Retained end/);
  assert.match(detail, /Retained end/);
  assert.match(detail, /Full harness instructions/);
  assert.match(detail, /truncated/);
  assert.match(detail, /data-category="typed"/);
});


test("MCP Details links use explicit request targets while workspace units link to matching checkouts", () => {
  const row = { arguments_json: JSON.stringify({ conversation_id: "conversation", message_id: "message" }) };
  const [target] = usageRowLinks("mcp_calls", row);
  assert.equal(target.label, "Requested message");
  assert.equal(new URL(target.href, "http://localhost").searchParams.get("message"), "message");
  assert.deepEqual(usageRowLinks("mcp_calls", { arguments_json: "invalid" }), []);
  assert.deepEqual(usageRowLinks("mcp_calls", { arguments_json: '{"query":"search terms"}' }), []);
  const [checkout] = usageRowLinks("parallelism", { counting: "workspace", location: "/worktree /+" });
  assert.equal(checkout.label, "Works at this checkout");
  const url = new URL(checkout.href, "http://localhost");
  assert.equal(url.pathname, "/library");
  assert.deepEqual(decodeQueryStrict(url.searchParams.get("q_library")).where, [{ field: "location", op: "=", value: "/worktree /+" }]);
});


test('Analyze record keywords remain scoped to their dataset and dashboard return names the reset', () => {
  const current = new URLSearchParams({ analyze_mode:'query',analyze_data:'usage',analyze_topic:'tokens',q_usage:encodeQuery(EMPTY_QUERY),record_usage:'keyword-definition' });
  const same = new URL(usageWorkspaceURL({mode:'query'},current),'http://localhost');
  assert.equal(same.searchParams.get('record_usage'),'keyword-definition');
  const switched = new URL(usageWorkspaceURL({dataset:'tool_calls'},current),'http://localhost');
  assert.equal(switched.searchParams.has('record_usage'),false);
  const back=usageDashboardReturn(current);
  assert.equal(back.resetsFilters,true);
  assert.match(back.label,/keyword filters/);
  assert.equal(new URL(back.href,'http://localhost').searchParams.has('record_usage'),false);
});

test("a query link on the same dataset opens the query it names, with or without dashboard filters", () => {
  const saved = { ...EMPTY_QUERY, where: [{ field: "provider", op: "=", value: "claude" }] }, requested = { ...EMPTY_QUERY, where: [{ field: "provider", op: "=", value: "codex" }] };
  for (const filters of [false, true]) {
    const current = new URLSearchParams({ analyze_mode: "query", analyze_data: "usage", analyze_topic: "tokens", q_usage: encodeQuery(saved), ...filters ? { analyze_filters: encodeQuery(EMPTY_QUERY) } : {} });
    const opened = new URL(usageWorkspaceURL({ mode: "query", dataset: "usage", query: requested }, current), "http://localhost").searchParams;
    assert.deepEqual(decodeQueryStrict(opened.get("q_usage")).where, requested.where, `analyze_filters: ${filters}`);
    assert.equal(opened.has("analyze_opened"), filters);
  }
  const kept = new URL(usageWorkspaceURL({ mode: "query" }, new URLSearchParams({ analyze_mode: "query", analyze_data: "usage", q_usage: encodeQuery(saved) })), "http://localhost").searchParams;
  assert.deepEqual(decodeQueryStrict(kept.get("q_usage")).where, saved.where, "without a query, the current one stays");
});
