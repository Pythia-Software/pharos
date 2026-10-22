import { NULLARY_OPS, type FieldSchema, type QueryState, type WhereClause, type WhereTerm } from "@pythia-software/query-table-core";
import { groupedDashboardQuery } from "./usage-dashboard-model";
import { usageDatasets, usageSearchQuery, usageWorkspaceURL, type UsageDataset } from "./usage-workspace-model";
import { optionLabels } from "./option-labels";
import { dashboardExplorations } from "./dashboard-query-explorations";
import { orderedUsageQuery } from "./usage-query-order";

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

function filterDescriptions(schema: LinkSchema, query: QueryState, where: WhereTerm[]): string[] {
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
export function dashboardQueryDetails(schema: LinkSchema, query: QueryState): QueryLinkDetail[] {
  const source = query.grouping?.source ?? query;
  const filters = filterDescriptions(schema, source, source.where);
  const sorts = query.orderBy;
  return [
    { label: "Filters", values: filters },
    ...query.grouping && query.where.length ? [{ label: "Group filters", values: filterDescriptions(schema, query, query.where) }] : [],
    ...query.grouping ? [{ label: "Measures", values: query.grouping.summaries.filter(column => column.op !== "row" && column.op !== "link").map(column => fieldLabel(schema, query, column.id)) }] : [],
    { label: "Group by", values: query.grouping?.keys.map(key => fieldLabel(schema, source, key)) ?? [] },
    { label: "Sort", values: sorts.map(sort => `${fieldLabel(schema, query, sort.field)}${sort.extract ? ` matching /${sort.extract.regex}/` : ""} ${sort.dir === "desc" ? "descending" : "ascending"}${sort.nulls === "first" ? " (empty first)" : ""}`) },
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
  const keys = chart?.grouping?.keys.map(key => fieldLabel(schema, records, key).toLowerCase()) ?? [];
  const explorations = dashboardExplorations(dataset, records, params.get("analyze_topic") === "carbon");
  const destinations = [
    ...(chart ? [{ title: `${measure}${keys.length ? ` by ${keys.join(" & ")}` : " totals"}`, query: chart }] : []),
    ...(explorations.length ? explorations : [{ title: `${usageDatasets[dataset].label} records`, query: records }]),
  ];
  return destinations.map(destination => {
    const query = { ...orderedUsageQuery(dataset, destination.query), fitToScreen: true };
    const details = dashboardQueryDetails(schema, query);
    return { ...destination, query, dataset: usageDatasets[dataset].label,
      href: usageWorkspaceURL({ mode: "query", search: true, dataset, query }, params),
      details, sentence: dashboardQuerySentence(details),
    };
  });
}
