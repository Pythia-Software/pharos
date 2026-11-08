import { dashboardQuerySettingsSnapshot } from "./dashboard-query-settings";
import { fitQueryToScreen } from "./query-fit";
import { NULLARY_OPS, branchesOf, groupKeyField, isCalendarGroupKey, isOrGroup, loadSchema, type FieldSchema, type GroupKey, type QueryState, type WhereClause, type WhereTerm } from "@pythia-software/query-table-core";
import { usageDatasets, usageDatasetTopics, usageWorkspaceURL, type UsageDataset } from "./usage-workspace-model";
import { optionLabels } from "./option-labels";
import { dashboardExplorations, type DashboardQueryContext } from "./dashboard-query-explorations";
import { orderedUsageQuery } from "./usage-query-order";
import { generatedQueryTarget, toolWhereCanUseCalls } from "./generated-query-target";
import { schemaDocuments } from "./schema-documents";
import { analysisCatalogs } from "./analysis-catalogs";
import { dashboardDefaults } from "./dashboard-defaults";

type LinkSchema = Pick<FieldSchema, "fields">;
export type QueryLinkDetail = { label: string; values: string[] };
type QuerySentencePart = { prefix: string; keyword: string; text: string };

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
    ...query.grouping ? [{ label: "Measures", values: [...new Set(query.grouping.summaries.filter(column => column.op !== "row" && column.op !== "link").map(column => fieldLabel(resultSchema, query, column.id)))] }]
      : query.aggregations?.length ? [{ label: "Measures", values: query.aggregations.map(clause => clause.label ?? clause.field ?? "Records") }] : [],
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

// Context is explicit in mounted dashboards, where settings can resolve a
// different catalog/source. URL-only callers use the same catalog contract.
export function dashboardQueryLinks(dataset: UsageDataset, query: QueryState, schema: FieldSchema, params: URLSearchParams, context?: DashboardQueryContext) {
  const topic = params.get("analyze_topic") ?? usageDatasetTopics[dataset];
  const catalog = context?.catalog ?? analysisCatalogs[topic];
  if (!catalog) throw new Error(`Dashboard ${topic} needs a query recommendation adapter.`);
  const state = context?.state ?? { ...dashboardDefaults[topic], ...JSON.parse(params.get("analyze_chart") ?? "{}") };
  const suggestions = dashboardExplorations(context ?? { catalog, state, where: (query.grouping?.source ?? query).where, supporting: query }, schema);
  const current = new URLSearchParams(params);
  const snapshot = dashboardQuerySettingsSnapshot(catalog.request?.params);
  if (snapshot) current.set("analyze_query_settings", snapshot);
  else current.delete("analyze_query_settings");
  const carbon = catalog.request?.params?.carbon_settings;
  if (carbon) current.set("analyze_carbon", carbon);
  return suggestions.map(destination => {
    // A rollup's numeric predicates cannot be reinterpreted as call filters.
    // Only record evidence with a dimension-only population can change grain.
    if (destination.dataset === "tools" && destination.purpose === "contributors" && !destination.query.grouping && toolWhereCanUseCalls(destination.query.where)) {
      const calls = loadSchema(schemaDocuments.tool_calls), names = new Set(calls.fields.map(field => field.name));
      const field = (name: string) => ({ total_duration_ms: "duration_ms", timed_count: "duration_ms", truncated_count: "truncated" }[name] ?? name);
      const orderBy = destination.query.orderBy.map(order => ({ ...order, field: field(order.field) }));
      // Retain the rollup if a future metric has no call-level ranking.
      if (orderBy.every(order => names.has(order.field))) {
        destination = { ...destination, dataset: "tool_calls", title: destination.title.replace("Daily tool summaries", "Tool calls"),
          note: "Individual tool calls behind the selected summary population.", query: { ...destination.query,
            select: [...new Set(["started_at", "tool_name", "operation_name", "status", ...destination.query.select.map(column => field(column.field))])].filter(name => names.has(name)).map(field => ({ field })), orderBy } };
      }
    }
    const target = generatedQueryTarget(destination.dataset, orderedUsageQuery(destination.dataset, destination.query));
    const targetDataset = target.dataset as UsageDataset;
    const query = fitQueryToScreen(target.query);
    const details = dashboardQueryDetails(targetDataset === dataset ? schema : loadSchema(schemaDocuments[targetDataset]), query);
    return { ...destination, query, dataset: usageDatasets[targetDataset].label,
      href: destination.blocked ? undefined : usageWorkspaceURL({ mode: "query", search: true, dataset: targetDataset, query }, current),
      details, sentence: dashboardQuerySentence(details),
    };
  });
}
