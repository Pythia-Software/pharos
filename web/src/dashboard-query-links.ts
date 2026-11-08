import { fitQueryToScreen } from "./query-fit";
import {messageLabelConversationQuery} from "./message-label-results-model";
import { EMPTY_QUERY, NULLARY_OPS, branchesOf, groupKeyField, isCalendarGroupKey, isOrGroup, loadSchema, type FieldSchema, type GroupKey, type QueryState, type SummaryColumn, type WhereClause, type WhereTerm } from "@pythia-software/query-table-core";
import { groupedDashboardQuery } from "./usage-dashboard-model";
import { usageDatasets, usageSearchQuery, usageWorkspaceURL, type UsageDataset } from "./usage-workspace-model";
import { optionLabels } from "./option-labels";
import { dashboardExplorations } from "./dashboard-query-explorations";
import { orderedUsageQuery } from "./usage-query-order";
import { generatedQueryTarget, toolCallDimensions, toolWhereCanUseCalls } from "./generated-query-target";
import { schemaDocuments } from "./schema-documents";

type LinkSchema = Pick<FieldSchema, "fields">;
export type QueryLinkDetail = { label: string; values: string[] };
type QuerySentencePart = { prefix: string; keyword: string; text: string };

// A daily tools row represents calls with these same dimensions. Its numeric
// totals are not predicates on individual calls, so leave any query using one
// of them on the stored source rather than weakening its population.
const toolCallMeasures = new Set([
  "call_count", "error_count", "input_bytes", "result_bytes", "result_tokens",
  "carried_tokens", "output_tokens", "lines_added", "lines_removed",
]);
const toolSum = (id: string, field: string, label: string): SummaryColumn => ({ id, op: "sum", field, label });

function toolChartOnCalls(chart: QueryState | null, where: WhereTerm[]): QueryState | undefined {
  if (!chart?.grouping || chart.grouping.representative) return;
  if (!chart.grouping.keys.every(key => toolCallDimensions.has(groupKeyField(key)))) return;
  const summaries = chart.grouping.summaries.map(summary => {
    if (summary.op !== "sum" || !summary.field || !toolCallMeasures.has(summary.field) || summary.expression) return;
    return summary;
  });
  if (summaries.some(summary => !summary)) return;
  return { ...chart, grouping: { ...chart.grouping, source: { ...EMPTY_QUERY, where }, summaries: summaries as SummaryColumn[] } };
}

function toolCallDestinations(chart: QueryState | null, where: WhereTerm[]) {
  if (!toolWhereCanUseCalls(where)) return;
  const source = { ...EMPTY_QUERY, where };
  const grouped = (title: string, keys: string[], summaries: SummaryColumn[]) => ({ title, query: {
    ...EMPTY_QUERY, grouping: { source, keys, summaries, representative: null },
    select: [...keys, ...summaries.map(summary => summary.id)].map(field => ({ field })),
    orderBy: [{ field: summaries[0].id, dir: "desc" as const }],
  } as QueryState });
  const exactChart = toolChartOnCalls(chart, where);
  return [
    ...(exactChart ? [{ title: "Chart groups", query: exactChart, dataset: "tool_calls" }] : []),
    { title: "Matching tool calls", query: { ...source, orderBy: [{ field: "started_at", dir: "desc" as const }] }, dataset: "tool_calls" },
    { ...grouped("Calls and errors by tool and provider", ["tool_name", "provider"], [toolSum("explore_calls", "call_count", "Calls"), toolSum("explore_errors", "error_count", "Errors")]), dataset: "tool_calls" },
    { ...grouped("Context added and carried by tool", ["tool_name"], [toolSum("explore_added", "result_tokens", "Context added"), toolSum("explore_carried", "carried_tokens", "Context carried")]), dataset: "tool_calls" },
    ...(!exactChart ? [{ ...grouped("Calls by day", ["day"], [toolSum("explore_calls", "call_count", "Calls"), toolSum("explore_errors", "error_count", "Errors")]), dataset: "tool_calls" }] : []),
  ];
}

const operators: Record<string, string> = {
  "=": "is", "!=": "is not", ">=": "is at least", "<=": "is at most", ">": "is greater than", "<": "is less than",
  contains: "contains", not_contains: "does not contain", starts_with: "starts with", ends_with: "ends with",
  has_word: "has word", has_words: "has all words", matches_regex: "matches pattern", not_matches_regex: "does not match pattern",
  is_null: "is empty", is_not_null: "is not empty", includes: "includes",
};
const dateOperators: Record<string, string> = { ">=": "is on or after", "<=": "is on or before", ">": "is after", "<": "is before" };

function fieldLabel(schema: LinkSchema, query: QueryState, name: string): string {
  return query.grouping?.summaries.find(column => column.id === name)?.label
    ?? schema.fields.find(field => field.name === name)?.label ?? name.replaceAll("_", " ");
}

// A calendar key names its interval: "Started by day".
function groupKeyLabel(schema: LinkSchema, query: QueryState, key: GroupKey): string {
  const label = fieldLabel(schema, query, groupKeyField(key));
  return isCalendarGroupKey(key) ? `${label} by ${key.interval}` : label;
}

export function dashboardFilterDescriptions(schema: LinkSchema, query: QueryState, where: WhereTerm[]): string[] {
  const options = optionLabels(schema.fields);
  const value = (clause: WhereClause) => {
    const labelled = options.get(clause.field)?.get(clause.value);
    if (labelled) return labelled;
    if (schema.fields.find(field => field.name === clause.field)?.type === "datetime") {
      if (/^[+-]?0(?:s|m|h|d|w)$/.test(clause.value)) return "now";
      const relative = /^([+-]?)(\d+)(s|m|h|d|w)$/.exec(clause.value);
      if (relative) {
        const unit = { s: "second", m: "minute", h: "hour", d: "day", w: "week" }[relative[3]];
        return `${relative[1] === "-" ? "-" : "+"}${relative[2]} ${unit}${relative[2] === "1" ? "" : "s"}`;
      }
      return clause.value.replace("T", " ").replace(/\.000(?=Z$|[+-]\d{2}:\d{2}$)/, "")
        .replace(/(\d{2}:\d{2}):00(?=Z$|[+-]\d{2}:\d{2}$)/, "$1").replace(/(Z|[+-]\d{2}:\d{2})$/, zone => ` ${zone === "Z" ? "UTC" : zone}`);
    }
    return clause.value || "“”";
  };
  const describe = (clause: WhereClause) => {
    const date = schema.fields.find(field => field.name === clause.field)?.type === "datetime";
    const op = (date ? dateOperators[clause.op] : undefined) ?? operators[clause.op] ?? clause.op.replaceAll("_", " ");
    return `${clause.negated ? "not (" : ""}${fieldLabel(schema, query, clause.field)} ${op}${NULLARY_OPS.has(clause.op) ? "" : ` ${value(clause)}`}${clause.negated ? ")" : ""}`;
  };
  return where.map(term => {
    if (!("any" in term)) return describe(term);
    if (!term.any.length) return "No matching records";
    if (!isOrGroup(term)) return `(${branchesOf(term).map(branch => branch.length > 1 ? `(${branch.map(describe).join(" and ")})` : describe(branch[0])).join(" or ")})`;
    const first = term.any[0];
    if (term.any.every(clause => clause.field === first.field && clause.op === "=" && !clause.negated)) {
      const days = [...new Set(term.any.map(clause => clause.value))].sort();
      // Tool summaries express their date window as an OR of calendar days.
      // Only collapse a truly contiguous range; gaps remain explicit.
      if (days.length > 1 && days.every((day, i) => /^\d{4}-\d{2}-\d{2}$/.test(day) && (!i || Date.parse(day) - Date.parse(days[i - 1]) === 86_400_000))) {
        return `${fieldLabel(schema, query, first.field)} from ${days[0]} to ${days.at(-1)}`;
      }
      const alternatives = `${fieldLabel(schema, query, first.field)} is ${term.any.map(value).join(" or ")}`;
      return term.any.length > 1 ? `(${alternatives})` : alternatives;
    }
    return `(${term.any.map(describe).join(" or ")})`;
  });
}

// Describe the very same query encoded in the destination, including source
// WHERE vs grouped HAVING. Empty sections are omitted.
export function dashboardQueryDetails(schema: LinkSchema, query: QueryState, resultSchema: LinkSchema = schema): QueryLinkDetail[] {
  const source = query.grouping?.source ?? query;
  const filters = dashboardFilterDescriptions(schema, source, source.where);
  const sorts = query.orderBy;
  return [
    { label: "Filters", values: filters },
    ...query.grouping && query.where.length ? [{ label: "Group filters", values: dashboardFilterDescriptions(resultSchema, query, query.where) }] : [],
    ...query.grouping ? [{ label: "Measures", values: query.grouping.summaries.filter(column => column.op !== "row" && column.op !== "link").map(column => fieldLabel(resultSchema, query, column.id)) }] : [],
    { label: "Group by", values: query.grouping?.keys.map(key => groupKeyLabel(schema, source, key)) ?? [] },
    { label: "Sort", values: sorts.map(sort => `${fieldLabel(resultSchema, query, sort.field)}${sort.extract ? ` matching /${sort.extract.regex}/` : ""} ${sort.dir === "desc" ? "descending" : "ascending"}${sort.nulls === "first" ? " (empty first)" : ""}`) },
  ].filter(detail => detail.values.length);
}

// Keep the summary as one reading beneath the name, with only its grammatical
// cues emphasized. The native predicates still own AND/OR and source/group scope.
export function dashboardQuerySentence(details: QueryLinkDetail[]): QuerySentencePart[] {
  if (!details.length) return [];
  const values = (label: string) => details.find(detail => detail.label === label)?.values ?? [];
  const list = (items: string[]) => items.length < 3 ? items.join(" and ") : `${items.slice(0, -1).join(", ")}, and ${items.at(-1)}`;
  const measures = values("Measures");
  const parts: QuerySentencePart[] = [{ prefix: "", keyword: "Show", text: measures.length ? list(measures) : "records" }];
  for (const [label, keyword, prefix, join] of [
    ["Filters", "where", " ", " and "],
    ["Group by", "grouped by", ", ", null],
    ["Group filters", "keeping groups where", ", ", " and "],
    ["Sort", "sorted by", ", ", ", then "],
  ] as const) {
    const items = values(label);
    if (items.length) parts.push({ prefix, keyword, text: join ? items.join(join) : list(items) });
  }
  return parts;
}

export function dashboardQueryLinks(dataset: UsageDataset, query: QueryState, schema: LinkSchema, params: URLSearchParams) {
  const records = usageSearchQuery(query);
  const chart = query.aggregations?.length || query.grouping ? groupedDashboardQuery(query) : null;
  const measure = chart?.grouping?.summaries[0]?.label?.replace(" per conversation group", "") ?? "Recorded totals";
  const keys = chart?.grouping?.keys.map(key => groupKeyLabel(schema, records, key).toLowerCase()) ?? [];
  const explorations = dashboardExplorations(dataset, records, params.get("analyze_topic") === "carbon");
  const toolCalls = dataset === "tools" ? toolCallDestinations(chart, records.where) : undefined;
  const destinations = toolCalls ?? (dataset==="message_labels"?[
    {title:"Matching conversations",query:messageLabelConversationQuery(records)},
    {title:"Conversations with human input",query:messageLabelConversationQuery({...records,where:[...records.where,{field:"source",op:"=",value:"human"}]})},
    {title:"Conversations with uncategorized messages",query:messageLabelConversationQuery({...records,where:[...records.where,{field:"label_status",op:"!=",value:"matched"}]})},
    {title:"Matching label observations",query:{...records,select:[{field:"fingerprint"},{field:"title"},{field:"label"},{field:"text"},{field:"occurred_at"}],hostState:{version:1,params:{label_results:["table"]}}}},
  ]:[
    ...(chart ? [{ title: `${measure}${keys.length ? ` by ${keys.join(" & ")}` : " totals"}`, query: chart }] : []),
    ...(explorations.length ? explorations : dataset === "agent_events" && params.get("analyze_topic") === "agent_experience" ? [{
      title: "Events to read",
      query: { ...records,
        select: ["family", "sentence", "subject", "response", "source", "occurred_at", "title", "probability"].map(field => ({ field })),
        orderBy: [{ field: "probability", dir: "desc" as const }, { field: "occurred_at", dir: "desc" as const }, { field: "id", dir: "asc" as const }],
      },
    }] : [{ title: `${usageDatasets[dataset].label} records`, query: records }]),
  ]);
  return destinations.map(destination => {
    const original = ("dataset" in destination ? destination.dataset : dataset) as UsageDataset;
    const target = generatedQueryTarget(original, orderedUsageQuery(original, destination.query));
    const targetDataset = target.dataset as UsageDataset;
    const query = fitQueryToScreen(target.query);
    const details = dashboardQueryDetails(targetDataset === dataset ? schema : loadSchema(schemaDocuments[targetDataset]), query);
    return { ...destination, query, dataset: usageDatasets[targetDataset].label,
      href: usageWorkspaceURL({ mode: "query", search: true, dataset: targetDataset, query }, params),
      details, sentence: dashboardQuerySentence(details),
    };
  });
}
