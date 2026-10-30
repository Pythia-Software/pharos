// Deterministic, schema-aware cases for tools/audit-usage.mjs. Runtime samples
// stay in the private run directory; this file contains no catalog data.
import { EMPTY_QUERY, encodeQuery, loadSchema, type QueryState, type WhereTerm } from "../web/node_modules/@pythia-software/query-table-core/dist/index.js";
import { usageDatasets, usageTopics, usageDatasetTopics, usageTopicDatasets, usageWorkspaceURL } from "../web/src/usage-workspace-model";
import { defaultConcurrencySettings } from "../web/src/concurrency-settings";
export { usageDatasets, usageTopics, usageWorkspaceURL, encodeQuery, EMPTY_QUERY, loadSchema };
export type AuditCase = { id: string; page: string; label: string; url: string; dataset?: string; query?: QueryState; where?: WhereTerm[]; interaction?: string; expectedEmpty?: boolean };
const day = 86400000;
export function fieldsFor(document: any) {
  const names = document.fields.map((field: any) => field.name);
  const choose = (candidates: string[]) => candidates.find(field => names.includes(field));
  return {
    time: choose(["first_usage_at", "started_at", "called_at", "first_message_at", "sent_at", "created_at", "event_at"]),
    group: choose(["provider", "root_provider", "tool_name", "repository_name", "root_model_family", "skill_name", "model"]),
    text: choose(["command", "text", "title", "arguments_json", "tool_name", "skill_name", "model", "root_model_family"]),
    number: choose(["total_tokens", "typed_words", "user_words", "duration_ms", "call_count", "estimated_output_tokens", "content_bytes", "unit_hours", "output_tokens", "words", "human_input_tokens"]),
    nullable: choose(["repository_name", "model", "root_model_family", "program", "error_text", "skill_name", "title"]),
  };
}
export function sampleSelect(document: any) {
  const selected = [document.idField ?? "id", ...Object.values(fieldsFor(document)), "day", "week", "month"].filter(field => document.fields.some((candidate: any) => candidate.name === field));
  return [...new Set(selected)].map(field => ({ field }));
}
export function queryCases(dataset: keyof typeof usageDatasets, document: any, samples: any[]): AuditCase[] {
  const f = fieldsFor(document), at = f.time && samples.find(row => row[f.time!])?.[f.time] || new Date().toISOString();
  const upper = new Date(Date.parse(at) + day), lower = new Date(Date.parse(at) - 7 * day);
  const calendarDays = [...new Set(samples.map(row => row.day).filter(Boolean))].slice(0, 7);
  const calendarRange: WhereTerm[] = !f.time && document.fields.some((field: any) => field.name === "day") ? [{ any: (calendarDays.length ? calendarDays : ["2026-10-01"]).map(value => ({ field: "day", op: "=" as const, value: String(value) })) }] : [];
  const range: WhereTerm[] = f.time ? [{ field: f.time, op: ">=", value: lower.toISOString() }, { field: f.time, op: "<", value: upper.toISOString() }] : calendarRange;
  const category = f.group!, numeric = f.number ?? document.fields.find((field: any) => field.type === "number" && field.name !== "id")?.name;
  const value = String(samples.find(row => row[category] != null)?.[category] ?? "codex");
  const text = f.text!, nullable = f.nullable ?? text;
  const predicate = (field: string, op: string, value = "") => ({ field, op: op === "not_contains" ? "contains" : op, value, ...(op === "not_contains" ? { negated: true } : {}) }) as WhereTerm;
  const metric = { id: "audit_count", op: "count" as const, groupBy: [category], label: "Matching records" };
  const base = { ...EMPTY_QUERY, limit: 25 };
  const group = (summaries: any[], where: WhereTerm[] = [], sourceWhere = range): QueryState => ({ ...base, grouping: { source: { ...base, where: sourceWhere }, keys: [category], summaries, representative: null }, select: [{ field: category }, ...summaries.map(summary => ({ field: summary.id }))], where, orderBy: [{ field: category, dir: "asc" }] });
  const cases: Array<[string, QueryState, boolean?]> = [
    ["Default records", base],
    ["Snapshot week", { ...base, where: range }],
    [f.time ? "Relative 90 days" : "Snapshot month", { ...base, where: f.time ? [predicate(f.time, ">=", "-90d")] : [predicate("month", "=", String(samples[0]?.month ?? "2026-10"))] }],
    ["Minimal columns and descending measure", { ...base, select: [...new Set([text, numeric].filter(Boolean))].map(field => ({ field })), orderBy: numeric ? [{ field: numeric, dir: "desc" }] : [] }],
    ["Category equality", { ...base, where: [predicate(category, "=", value)] }],
    ["Category exclusion", { ...base, where: [predicate(category, "!=", value)] }],
    ["Keyword contains", { ...base, where: [predicate(text, "contains", "test")] }],
    ["Keyword excludes", { ...base, where: [predicate(text, "not_contains", "test")] }],
    ["Positive measure", { ...base, where: numeric ? [predicate(numeric, ">", "0")] : [] }],
    ["Measure interval and keyword", { ...base, where: [...(numeric ? [predicate(numeric, ">=", "0"), predicate(numeric, "<", "10000")] : []), predicate(text, "not_contains", "audit_no_such_word")] }],
    ["Missing value", { ...base, where: [predicate(nullable, "is_null")] }],
    ["Known value", { ...base, where: [predicate(nullable, "is_not_null")] }],
    ["OR categories", { ...base, where: [{ any: [predicate(category, "=", value), predicate(category, "is_null")] } as WhereTerm] }],
    ["Time category and keyword", { ...base, where: [...range, predicate(category, "=", value), predicate(text, "not_contains", "audit_no_such_word")] }],
    ["Empty keyword population", { ...base, where: [predicate(text, "contains", "audit_no_such_word_82f14c")] }, true],
    ["Quoted keyword", { ...base, where: [predicate(text, "contains", '"test"')] }],
    ["Negated OR", { ...base, where: [predicate(category, "!=", "audit_no_such_category"), predicate(nullable, "is_not_null")] }],
    ["Groups and counts", group([{ id: "audit_rows", op: "count", label: "Records" }])],
    ["Groups with sum and mean", group(numeric ? [{ id: "audit_sum", op: "sum", field: numeric }, { id: "audit_mean", op: "avg", field: numeric }] : [{ id: "audit_rows", op: "count" }])],
    ["Grouped HAVING", group([{ id: "audit_rows", op: "count" }], [predicate("audit_rows", ">", "0")])],
    ["Metric and filtered records", { ...base, where: range, aggregations: [metric] }],
    ["Grouped formula", group(numeric ? [{ id: "audit_average", op: "formula", expression: `SUM([${numeric}])/NULLIF(COUNT(),0)` }] : [{ id: "audit_rows", op: "count" }])],
    ["Groups across all dates", group([{ id: "audit_rows", op: "count" }], [], [])],
    ["Second result page", { ...base, where: range, offset: 25 }],
    ["Relative dates and search panel", { ...base, where: [...(f.time ? [predicate(f.time, ">=", "-30d"), predicate(f.time, "<", "+0s")] : []), predicate(text, "not_contains", "audit_no_such_word")] }],
  ];
  return cases.map(([label, query, expectedEmpty], i) => ({ id: `query-${dataset}-${String(i + 1).padStart(2, "0")}`, page: `query:${dataset}`, label, dataset, query, expectedEmpty, interaction: i === 19 ? "inspect-group" : undefined,
    url: usageWorkspaceURL({ mode: "query", dataset, topic: usageDatasetTopics[dataset], query, search: i % 3 === 0 || i === 24 }) }));
}
export function dashboardCases(topic: keyof typeof usageTopics, documents: Record<string, any>, samples: Record<string, any[]>, concurrency: any): AuditCase[] {
  const dataset = usageTopicDatasets[topic];
  const f = fieldsFor(documents[dataset]), sample = samples[dataset] ?? [], category = f.group!, value = String(sample.find(row => row[category] != null)?.[category] ?? "codex");
  const date = new Date(); date.setHours(0, 0, 0, 0); date.setDate(date.getDate() - 29);
  const time = topic === "tools" ? "day" : f.time!;
  const baseWhere: WhereTerm[] = [{ field: time, op: ">=", value: date.toISOString() }];
  const conditions: WhereTerm[][] = [
    [], baseWhere, [...baseWhere, { field: category, op: "=", value }],
    [{ field: category, op: "!=", value }],
    [{ field: f.text!, op: "contains", value: "test" }],
    [{ field: f.text!, op: "contains", negated: true, value: "audit_no_such_word" }],
    [{ field: f.nullable ?? f.text!, op: "is_null", value: "" }],
    [{ field: f.nullable ?? f.text!, op: "is_not_null", value: "" }],
    [{ any: [{ field: category, op: "=", value }, { field: category, op: "is_null", value: "" }] }],
    [...baseWhere, ...(f.number ? [{ field: f.number, op: ">" as const, value: "0" }] : [])],
    [{ field: f.text!, op: "contains", value: "audit_no_such_word_82f14c" }],
    [...baseWhere, { field: category, op: "=", value }, { field: f.text!, op: "contains", negated: true, value: "audit_no_such_word" }],
  ];
  const periods = ["day", "week", "month", "hours", "days"], scales = ["half", "linear", "logarithmic"];
  const tokenSplits = ["type", "provider", "model_family", "model_effort", "effort", "repository_name", "session_kind"];
  const humanSplits = ["kind", "providers", "repository_name", "source_kind"];
  return Array.from({ length: 25 }, (_, i) => {
    const where = conditions[i % conditions.length];
    const params = new URLSearchParams({ analyze_mode: "dashboards", analyze_topic: topic, analyze_data: dataset, analyze_filters: encodeQuery({ ...EMPTY_QUERY, where }) });
    let view: any;
    if (topic === "tokens") view = { metric: ["tokens", "cost", "percent", "percent_cost"][i % 4], split: tokenSplits[i % tokenSplits.length], period: periods[i % periods.length], hours: [1, 6, 12][i % 3], days: [1, 3, 7][i % 3], rollingPeriods: [0, 3, 7][i % 3], scale: scales[i % 3] };
    if (topic === "human") view = { scope: i % 2 ? "all" : "typed", split: humanSplits[i % humanSplits.length], period: periods[i % periods.length], hours: [1, 6, 12][i % 3], days: [1, 3, 7][i % 3], rollingPeriods: [0, 3, 7][i % 3], scale: scales[i % 3] };
    if (topic === "time" || topic === "skills") view = { metric: i % 2, split: (topic === "time" ? ["activity", "tool_category", "provider", "repository_name"] : ["skill_name", "evidence_type", "provider", "repository_name", "status"])[i % (topic === "time" ? 4 : 5)], period: ["day", "week", "month"][(i + (topic === "time" ? Math.floor(i / conditions.length) : 0)) % 3], scale: scales[i % 3] };
    if (topic === "activity") view = { metric: i % 2 ? "output" : "human_input", split: ["provider", "model_family", "repository_name"][i % 3], view: "compare", period: ["day", "week", "month"][Math.floor(i / conditions.length)] };
    if (topic === "calls") view = { metric: i % 2 ? "calls" : "failures", split: ["tool_name", "repository_name", "error_signature"][i % 3], view: i % 3 ? "compare" : "trend", period: ["day", "week", "month"][Math.floor(i / conditions.length)] };
    if (topic === "tools") view = { rollingPeriods: 0, metric: ["calls", "failures", "failure_rate", "time", "mean_time", "context_added", "context_carried"][i % 7], split: ["tool_category", "tool_name", "command_category", "program", "command_name", "mcp_server", "repository_name", "model_family", "session_kind"][i % 9], view: i % 2 ? "trend" : "compare", period: ["day", "week", "month"][i % 3] };
    if (topic === "mcp") view = { rollingPeriods: 0, metric: ["calls", "failures", "failure_rate", "output", "mean_output", "largest_output", "bytes", "truncated", "time", "mean_time"][i % 10], split: ["tool_name", "status", "query_source"][i % 3], view: i % 2 ? "trend" : "compare", period: ["day", "week", "month"][i % 3] };
    if (topic === "carbon") view = { metric: i % 2 ? "energy" : "co2", split: ["type", "provider", "repository_name", "tier"][i % 4], period: periods[i % 5], hours: [1, 6, 12][i % 3], days: [1, 3, 7][i % 3] };
    if (view) params.set("analyze_chart", JSON.stringify({ ...view, display: i % 4 >= 2 ? "cumulative" : "interval" }));
    if (topic === "parallelism") {
      params.delete("analyze_filters");
      const day = concurrency.period_end;
      const repo = concurrency.threads?.find((thread: any) => thread.repository)?.repository ?? "";
      const settings = { ...defaultConcurrencySettings, mode: i % 2 ? "model" : "user", gap: [0, 15, 30, 60][i % 4], counting: i % 3 ? "workspace" : "agent", repository: i % 4 === 0 ? repo : "", range: ["day", "3d", "7d", "all", "custom"][i % 5], day, startDay: concurrency.period_start, endDay: day, showConversations: i % 3 === 0, smoothingMinutes: [0, 30, 60, 180, 360][Math.floor(i / 5)] };
      params.set("analyze_parallelism", JSON.stringify(settings));
    }
    const interaction = i < 5 ? "select" : i === 5 || i === 12 || i === 18 ? "legend" : i === 8 || i === 21 ? "expand" : topic === "carbon" && i >= 22 ? "carbon-assumption" : i === 15 ? "range" : undefined;
    return { id: `dashboard-${topic}-${String(i + 1).padStart(2, "0")}`, page: `dashboard:${topic}`, dataset, label: `Combination ${i + 1}${interaction ? ` + ${interaction}` : ""}`, url: `/analyze?${params}`, where: topic === "parallelism" ? undefined : where, interaction };
  });
}

// Target controls that the regular pairwise sample may not reach: chart-only
// legend choices, folded series, and Parallelism's pointer-driven drilldowns.
export function dashboardControlCases(documents: Record<string, any>, samples: Record<string, any[]>, concurrency: any): AuditCase[] {
  const extra = (topic: keyof typeof usageTopics, number: number, interaction: string, chart: Record<string, any> = {}): AuditCase => {
    const base = dashboardCases(topic, documents, samples, concurrency)[0];
    const url = new URL(base.url, "http://localhost");
    if (topic === "parallelism") {
      url.searchParams.set("analyze_parallelism", JSON.stringify({ ...defaultConcurrencySettings, range: "all", mode: "user", gap: 30, showConversations: true }));
    } else {
      url.searchParams.set("analyze_chart", JSON.stringify({ ...JSON.parse(url.searchParams.get("analyze_chart")!), ...chart }));
    }
    return { ...base, id: `dashboard-${topic}-${number}`, label: `Targeted ${interaction}`, interaction, url: url.pathname + url.search };
  };
  return [
    extra("tokens", 26, "legend", { metric: "tokens", split: "type" }),
    extra("tokens", 27, "expand", { metric: "tokens", split: "repository_name" }),
    extra("human", 26, "legend", { scope: "all", split: "kind" }),
    extra("human", 27, "expand", { scope: "all", split: "repository_name" }),
    extra("skills", 26, "expand", { split: "skill_name" }),
    extra("parallelism", 26, "select-conversation"),
    extra("parallelism", 27, "zoom"),
  ];
}
