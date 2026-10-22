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
import { defaultMCPDashboard, validMCPDashboard, cumulativeMCPTrend, mcpDashboardMeasures, mcpDashboardBreakdowns, mcpDashboardQuery, mcpTrendRanking, withMCPTrendGroups, mcpTrendGroups } from "../../../src/mcp-dashboard-model.ts";
import { toolSummaryCallFilters, defaultToolDashboard, validToolDashboard, cumulativeToolTrend, toolDashboardMeasures, toolDashboardBreakdowns, toolDashboardQuery, toolTrendRanking, withToolTrendGroups, toolTrendGroups } from "../../../src/tools-dashboard-model.ts";
import { readDashboardQuery, inspectDashboardBucket, recordDashboardMetric, recordDashboards, groupedDashboardQuery, parallelismSelectionQuery, toolDashboardWhere } from "../../../src/usage-dashboard-model.ts";
import { timeBounds, usageTime, withTimeRange } from "../../../src/usage-chart.tsx";
import { dashboardQueryDetails, dashboardQueryLinks, dashboardQuerySentence } from "../../../src/dashboard-query-links.ts";

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
    ["skills", "skill_usages", recordDashboardMetric("skills", 0, "day", "skill_name")],
    ["time", "tool_calls", recordDashboardMetric("time", 0, "day", "activity")],
    ["parallelism", "parallelism", { op: "sum", field: "unit_hours", groupBy: ["repository_name"], label: "Active unit hours" }],
    ["tools", "tools", toolDashboardQuery(defaultToolDashboard, []).aggregations[0]],
    ["mcp", "mcp_calls", mcpDashboardQuery(defaultMCPDashboard, []).aggregations[0]],
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
  const skillLinks = dashboardQueryLinks("skill_usages", { ...EMPTY_QUERY, aggregations: [recordDashboardMetric("skills", 0, "day", "skill_name")] }, loadSchema(skillUsages), new URLSearchParams());
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

  const toolLinks = dashboardQueryLinks("tools", toolDashboardQuery(defaultToolDashboard, []), loadSchema(tools), new URLSearchParams());
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

const mcpRows = [
  { id: 1, called_at: "2026-10-01T12:00:00Z", day: "2026-10-01", week: "2026-09-28", month: "2026-10", tool_name: "search", status: "ok", query_source: "codex", call_count: 1, error_count: 0, estimated_output_tokens: 200, response_bytes: 600, duration_ms: 100, truncated_count: 0 },
  { id: 2, called_at: "2026-10-02T12:00:00Z", day: "2026-10-02", week: "2026-09-28", month: "2026-10", tool_name: "search", status: "error", query_source: null, call_count: 1, error_count: 1, estimated_output_tokens: 100, response_bytes: 300, duration_ms: 300, truncated_count: 1 },
  { id: 3, called_at: "2026-10-02T12:00:00Z", day: "2026-10-02", week: "2026-09-28", month: "2026-10", tool_name: "read", status: "ok", query_source: "claude", call_count: 1, error_count: 0, estimated_output_tokens: 30, response_bytes: 90, duration_ms: 50, truncated_count: 0 },
];

test("MCP dashboard measures and grouped rows agree across breakdowns and time buckets", () => {
  const schema = loadSchema(mcpCalls);
  assert.equal(validMCPDashboard(defaultMCPDashboard), true);
  assert.equal(validMCPDashboard({ ...defaultMCPDashboard, metric: "result_tokens" }), false);
  const where = [{ field: "called_at", op: ">=", value: "2026-10-01T00:00:00Z" }];
  for (const metric of Object.keys(mcpDashboardMeasures)) for (const split of Object.keys(mcpDashboardBreakdowns)) for (const view of ["compare", "trend"]) for (const period of ["day", "week", "month"]) {
    const query = mcpDashboardQuery({ metric, split, view, period }, where), grouped = groupedDashboardQuery(query);
    assert.deepEqual(grouped.grouping.source.where, where);
    assert.deepEqual(toMetricQuery(query, schema).diagnostics, []);
    const chart = applyAggregations(mcpRows, query, schema).metrics[0];
    assert.equal(chart.error, undefined);
    const rows = applyQuery(mcpRows, grouped, schema).rows;
    assert.equal(rows.length, chart.buckets.length, `${metric}/${split}/${view}/${period}`);
    for (const bucket of chart.buckets) {
      const row = rows.find(row => grouped.grouping.keys.every((key, i) => row[key] === bucket.keys[i]));
      assert.ok(row);
      assert.equal(row.dashboard_1, bucket.value);
    }
  }
  const rate = applyQuery(mcpRows, groupedDashboardQuery(mcpDashboardQuery({ ...defaultMCPDashboard, metric: "failure_rate" }, [])), schema).rows.find(row => row.tool_name === "search");
  assert.equal(rate.dashboard_1, 0.5);
  assert.equal(rate.dashboard_2, 2);
  assert.equal(rate.dashboard_3, 1);
  const mean = groupedDashboardQuery(mcpDashboardQuery({ ...defaultMCPDashboard, metric: "mean_time" }, []));
  assert.equal(mean.grouping.summaries[0].display.kind, "duration");
  assert.equal(applyQuery(mcpRows, mean, schema).rows.find(row => row.tool_name === "search").dashboard_1, 200);
});

test("MCP trend drilldowns retain selected and unknown groups, including an empty population", () => {
  const schema = loadSchema(mcpCalls), base = mcpDashboardQuery({ ...defaultMCPDashboard, split: "query_source", view: "trend" }, []);
  const ranking = mcpTrendRanking("query_source");
  assert.equal(ranking.groupLimit, mcpTrendGroups);
  const selected = withMCPTrendGroups(base, "query_source", [null, "codex"]);
  assert.deepEqual(applyQuery(mcpRows, usageSearchQuery(selected), schema).rows.map(row => row.id).sort(), [1, 2]);
  assert.deepEqual(groupedDashboardQuery(selected).grouping.source.where, selected.where);
  assert.equal(applyQuery(mcpRows, usageSearchQuery(withMCPTrendGroups(base, "query_source", [])), schema).rows.length, 0);
});

test("Query links retain complete grouped queries and Search explicitly opens source records", () => {
  const source = { ...EMPTY_QUERY, offset: 50, where: [{ field: "provider", op: "=", value: "codex" }], aggregations: [{ id: "tokens", op: "sum", field: "total_tokens", groupBy: [] }] };
  const query = { ...EMPTY_QUERY, grouping: { source, keys: ["model"], summaries: [], representative: null }, where: [{ field: "sum_tokens", op: ">", value: "1000" }] };
  const params = new URL(usageWorkspaceURL({ mode: "query", dataset: "usage", query }), "http://localhost").searchParams;
  assert.deepEqual(decodeQueryStrict(params.get("q_usage")), query);
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

test("guided graphs execute native metrics over their own datasets", () => {
  const documents = { tool_calls: toolCalls, skill_usages: skillUsages, parallelism };
  for (const [topic, spec] of Object.entries(recordDashboards)) {
    const schema = loadSchema(documents[spec.dataset]);
    for (const [split] of spec.splits) for (let metric = 0; metric < spec.measures.length; metric++) {
      const clause = recordDashboardMetric(topic, metric, "day", split);
      for (const field of [...clause.groupBy, ...clause.field ? [clause.field] : []]) assert.ok(schema.fields.some(item => item.name === field), `${topic}: ${field}`);
      const result = applyAggregations([{ id: "one", day: "2026-10-20", [split]: "example", duration_ms: 1000, result_tokens: 10, carried_tokens: 20, content_bytes: 100, error_count: 1, unit_hours: 2, elapsed_hours: 1, parallel_elapsed_hours: 0.5 }], { ...EMPTY_QUERY, aggregations: [clause] }, schema);
      assert.equal(result.metrics[0].error, undefined, `${topic}: ${metric}`);
      assert.ok(result.metrics[0].buckets[0].value > 0, `${topic}: ${metric} returns a measurement`);
    }
  }
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
    const query = toolDashboardQuery({ ...defaultToolDashboard, metric, split: "tool_name" }, [{ field: "provider", op: "=", value: "codex" }]);
    const measured = applyAggregations(rows, query, schema).metrics[0];
    assert.equal(measured.error, undefined);
    assert.equal(measured.buckets.find(bucket => bucket.keys[0] === "shell").value, expected);
    const grouped = decodeQueryStrict(encodeQuery(groupedDashboardQuery(query)));
    assert.equal(grouped.grouping.summaries[0].display.kind, display);
    assert.deepEqual(grouped.orderBy[0], { field: "dashboard_1", dir: "desc" }, "The grouped table retains the graph’s ranking");
    const result = applyQuery(rows, grouped, schema).rows;
    const shell = result.find(row => row.tool_name === "shell");
    assert.equal(shell.dashboard_1, expected);
    assert.equal(shell.dashboard_2, 1001, "Grouped rows include the total call population");
    assert.equal(shell.dashboard_3, metric === "failure_rate" ? 11 : 101, "The numerator or timed-call population remains inspectable");
    if (metric === "mean_time") assert.equal(result.find(row => row.tool_name === "read").dashboard_1, null, "Missing timing must not look instantaneous");
  }
  const commands = toolDashboardQuery({ ...defaultToolDashboard, split: "command_name" }, []);
  assert.equal(applyQuery(rows, commands, schema).rows.length, 3, "Command breakdowns exclude dedicated read tools");
});

test("Tools controls all produce native executable metrics and grouped queries", () => {
  const schema = loadSchema(tools);
  const row = { id: "one", day: "2026-10-01", week: "2026-09-28", month: "2026-10", tool_name: "shell", tool_category: "command", program: "go", command_name: "go test", command_category: "test", mcp_server: "server", repository_name: "pharos", model_family: "model", session_kind: "root", call_count: 10, error_count: 2, timed_count: 8, total_duration_ms: 1000, result_tokens: 50, carried_tokens: 200 };
  assert.equal(validToolDashboard({ ...defaultToolDashboard, metric: "toString" }), false);
  for (const metric of Object.keys(toolDashboardMeasures)) for (const split of Object.keys(toolDashboardBreakdowns)) for (const view of ["compare", "trend"]) for (const period of ["day", "week", "month"]) {
    const controls = { metric, split, view, period };
    assert.equal(validToolDashboard(controls), true);
    const query = toolDashboardQuery(controls, []);
    assert.deepEqual(toMetricQuery(query, schema).diagnostics, [], JSON.stringify(controls));
    const result = applyAggregations([row], query, schema);
    assert.equal(result.metrics[0].error, undefined, JSON.stringify(controls));
    assert.ok(result.metrics[0].buckets[0].value > 0, JSON.stringify(controls));
    const grouped = decodeQueryStrict(encodeQuery(groupedDashboardQuery(query)));
    assert.equal(applyQuery([row], grouped, schema).rows.length, 1);
  }
});


test("Tools trends choose busiest groups across the full range and preserve that population in links", () => {
  const schema = loadSchema(tools);
  const rows = Array.from({ length: toolTrendGroups + 1 }, (_, index) => ({ id: String(index), tool_name: `tool-${index}`, provider: "codex", day: "2026-10-01", call_count: index + 1, error_count: 1 }));
  rows.push({ id: "other-provider", tool_name: "another", provider: "claude", day: "2026-10-01", call_count: 1000, error_count: 1 });
  const query = toolDashboardQuery({ ...defaultToolDashboard, metric: "failure_rate", split: "tool_name", view: "trend" }, [{ field: "provider", op: "=", value: "codex" }]);
  const ranking = toolTrendRanking("tool_name");
  assert.deepEqual(toMetricQuery({ ...query, aggregations: [ranking] }, schema).diagnostics, []);
  const values = applyAggregations(rows, { ...query, aggregations: [ranking] }, schema).metrics[0].buckets.map(bucket => bucket.keys[0]);
  const selected = withToolTrendGroups(query, "tool_name", values);
  const result = applyQuery(rows, groupedDashboardQuery(selected), schema).rows;
  assert.equal(result.length, toolTrendGroups);
  assert.ok(result.every(row => row.tool_name !== "tool-0" && row.tool_name !== "another"), "Volume selects series before computing rates, within the current filters");
  assert.equal(query.where.length, 1, "Selecting series does not mutate the dashboard’s source filters");
  assert.equal(applyQuery(rows, withToolTrendGroups(query, "tool_name", []), schema).rows.length, 0, "An empty ranking cannot broaden a drilldown");
  const nullRows = [{ id: "missing", tool_name: null, provider: "codex" }, { id: "known", tool_name: "known", provider: "codex" }];
  assert.deepEqual(applyQuery(nullRows, withToolTrendGroups(query, "tool_name", [null]), schema).rows.map(row => row.id), ["missing"]);
});


test("Tool summary call links keep every dimension of the selected row", () => {
  const base = { day: "2026-10-01", tool_name: "read_mcp_resource", tool_category: "mcp", provider: "codex", model: "gpt", effort: "high", repository_name: "test", session_kind: "root", source_kind: "codex", program: null, subcommand: null, command_category: null, mcp_server: "alpha" };
  const rows = [{ ...base, id: "exact" }, { ...base, id: "server", mcp_server: "beta" }, { ...base, id: "category", command_category: "read" }, { ...base, id: "tool-category", tool_category: "read" }];
  const query = { ...EMPTY_QUERY, where: toolSummaryCallFilters(base) };
  assert.deepEqual(applyQuery(rows, query, loadSchema(toolCalls)).rows.map(row => row.id), ["exact"]);
});

// Row presentation is host-owned; native properties and row links must keep
// their behaviour independently of the compact collection projection.
import { createPropertyView, selectedFields } from "@pythia-software/query-table-core";
import { conversationRowURL, primaryUsageSource, usageDetailsSchema, usageRowLinks } from "../../../src/usage-row-model.ts";
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

test("cumulative Tools and MCP points match native aggregation of their entire contributing range", () => {
  const toolsRows = [
    { id: "a", day: "2026-10-01", tool_name: "shell", provider: "codex", call_count: 1000, error_count: 10, timed_count: 100, total_duration_ms: 10000, result_tokens: 100, carried_tokens: 200 },
    { id: "b", day: "2026-10-03", tool_name: "shell", provider: "codex", call_count: 1, error_count: 1, timed_count: 1, total_duration_ms: 1000, result_tokens: 50, carried_tokens: 400 },
    { id: "c", day: "2026-10-09", tool_name: null, provider: "codex", call_count: 50, error_count: 0, timed_count: 0, total_duration_ms: 0, result_tokens: 0, carried_tokens: 0 },
    { id: "d", day: "2026-10-02", tool_name: "shell", provider: "claude", call_count: 9, error_count: 9, timed_count: 9, total_duration_ms: 9999 },
    { id: "e", day: "2026-09-01", tool_name: "shell", provider: "codex", call_count: 900, error_count: 900, timed_count: 0, total_duration_ms: 0 },
  ].map(row=>({...row,week:row.day<"2026-10-05"?"2026-09-28":"2026-10-05",month:row.day.slice(0,7)}));
  const start=new Date(2026,9,1), end=new Date(2026,9,12);
  const configurations = [
    {document:tools,rows:toolsRows,defaults:defaultToolDashboard,measures:toolDashboardMeasures,build:toolDashboardQuery,accumulate:cumulativeToolTrend,valid:validToolDashboard,where:[{field:"provider",op:"=",value:"codex"},{field:"day",op:">=",value:"2026-10-01"},{field:"day",op:"<",value:"2026-10-13"}]},
    {document:mcpCalls,rows:[...mcpRows,{...mcpRows[0],id:4,day:"2026-10-09",week:"2026-10-05",called_at:"2026-10-09T12:00:00Z",duration_ms:null,estimated_output_tokens:null}],defaults:defaultMCPDashboard,measures:mcpDashboardMeasures,build:mcpDashboardQuery,accumulate:cumulativeMCPTrend,valid:validMCPDashboard,where:[{field:"day",op:">=",value:"2026-10-01"},{field:"day",op:"<",value:"2026-10-13"}]},
  ];
  for(const config of configurations)for(const metric of Object.keys(config.measures))for(const period of ["day","week","month"]){
    const schema=loadSchema(config.document),view={...config.defaults,split:"tool_name",view:"trend",period,metric,display:"cumulative",rollingPeriods:20};
    assert.equal(config.valid(view),true);
    assert.equal(config.valid({...view,display:"invalid"}),false);
    const query=config.build(view,config.where),before=structuredClone(query);
    assert.deepEqual(toMetricQuery(query,schema).diagnostics,[]);
    const results=applyAggregations(config.rows,query,schema).metrics;
    assert.ok(results.every(result=>!result.error));
    const trend=config.accumulate(view,results,start,end);
    assert.equal(trend.error,undefined,metric);
    for(const bucket of trend.buckets){
      const records=inspectDashboardBucket(query,bucket,query.aggregations[0],schema,true);
      assert.ok(records,metric);
      const actualRows=applyQuery(config.rows,records,schema).rows;
      assert.ok(actualRows.every(row=>row[period]<=bucket.keys[0]&&row.tool_name===bucket.keys[1]));
      const expectedQuery=config.build({...view,view:"compare"},records.where);
      const measured=applyAggregations(config.rows,expectedQuery,schema).metrics[0];
      assert.equal(measured.error,undefined);
      const expected=measured.buckets.find(item=>item.keys[0]===bucket.keys[1])?.value;
      assert.equal(bucket.value,expected??(query.aggregations[0].op==="sum"&&!query.aggregations[0].expression?0:null),`${config.document.name}/${metric}/${period}/${bucket.keys}`);
      const current=new URLSearchParams({analyze_chart:JSON.stringify(view),analyze_topic:config.document.name==="pharos_tools"?"tools":"mcp"});
      const url=new URL(usageWorkspaceURL({mode:"query",dataset:config.document.name==="pharos_tools"?"tools":"mcp_calls",query:records},current),"http://localhost");
      assert.equal(JSON.parse(url.searchParams.get("analyze_chart")).display,"cumulative");
    }
    assert.deepEqual(query,before,"Cumulative presentation and inspection preserve the source query");
  }
});
