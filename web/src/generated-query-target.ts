import { predicatesOf, type AggregationClause, type GroupKey, type QueryState, type WhereTerm } from "@pythia-software/query-table-core";

// Generated links to the classified user-message source can open the all-message
// page with the same population. Keep the two predicates in the source of a
// grouped query: putting them in HAVING would change its groups.
const authoredUser: WhereTerm[] = [
  { field: "role", op: "=", value: "user" },
  { field: "has_authorship", op: "=", value: "true" },
];
const writingMessageFields: Record<string, string> = {
  sent_at: "authorship_sent_at", day: "authorship_day", week: "authorship_week", month: "authorship_month",
  title: "authorship_title", repository_name: "authorship_repository_name", source_kind: "authorship_source_kind",
};
const sourceFields = Object.fromEntries(Object.entries(writingMessageFields).map(([source, target]) => [target, source]));
export const generatedMessageField = (field: string) => writingMessageFields[field] ?? field;
export const writingMessageSourceField = (field: string) => sourceFields[field] ?? field;

type FieldMap = (field: string) => string;

// Formula field tokens are bracketed. Quoted text is literal, even when it
// contains brackets; a blind replacement changes the user's formula.
function mapFormula(expression: string, field: FieldMap): string {
  let mapped = "", quote = "";
  for (let i = 0; i < expression.length; i++) {
    const char = expression[i];
    if (quote) {
      mapped += char;
      if (char === "\\" && i + 1 < expression.length) mapped += expression[++i];
      else if (char === quote && expression[i + 1] === quote) mapped += expression[++i];
      else if (char === quote) quote = "";
    } else if (char === "'" || char === '"') {
      quote = char; mapped += char;
    } else if (char === "[" && expression.indexOf("]", i + 1) !== -1) {
      const end = expression.indexOf("]", i + 1);
      mapped += `[${field(expression.slice(i + 1, end))}]`;
      i = end;
    } else mapped += char;
  }
  return mapped;
}

function mapWhere(where: WhereTerm[], field: FieldMap): WhereTerm[] {
  return where.map(term => "field" in term ? { ...term, field: field(term.field) }
    : { ...term, any: term.any.map(branch => "all" in branch
      ? { ...branch, all: branch.all.map(clause => ({ ...clause, field: field(clause.field) })) }
      : { ...branch, field: field(branch.field) }) });
}

function mapMetrics(metrics: AggregationClause[] | undefined, field: FieldMap): AggregationClause[] | undefined {
  return metrics?.map(metric => ({ ...metric,
    ...(metric.field ? { field: field(metric.field) } : {}),
    groupBy: metric.groupBy.map(field),
    ...(metric.where ? { where: mapWhere(metric.where, field) } : {}),
    ...(metric.dateGrouping ? { dateGrouping: { ...metric.dateGrouping, field: field(metric.dateGrouping.field) } } : {}),
    ...(metric.minCount && typeof metric.minCount === "object" ? { minCount: "distinct" in metric.minCount
      ? { min: metric.minCount.min, distinct: field(metric.minCount.distinct!) }
      : { min: metric.minCount.min, sum: field(metric.minCount.sum!) } } : {}),
    ...(metric.expression ? { expression: mapFormula(metric.expression, field) } : {}),
    ...(metric.expressionY ? { expressionY: mapFormula(metric.expressionY, field) } : {}),
    ...(metric.distribution ? { distribution: { ...metric.distribution, input: mapFormula(metric.distribution.input, field) } } : {}),
  }));
}

function mapQuery(query: QueryState, field: FieldMap): QueryState {
  return { ...query,
    select: query.select.map(column => ({ ...column, field: field(column.field) })),
    where: mapWhere(query.where, field),
    orderBy: query.orderBy.map(order => ({ ...order, field: field(order.field) })),
    ...(query.aggregations ? { aggregations: mapMetrics(query.aggregations, field) } : {}),
  };
}

const groupKeyField = (key: GroupKey) => typeof key === "string" ? key : key.field;

function mapGroupedQuery(query: QueryState): QueryState | undefined {
  const grouping = query.grouping!;
  const summaryIds = new Set(grouping.summaries.map(summary => summary.id));
  const keys = new Set(grouping.keys.map(groupKeyField));
  // A renamed key cannot share a result column name with a summary.
  if ([...keys].some(key => summaryIds.has(generatedMessageField(key)))) return;
  const resultField: FieldMap = field => summaryIds.has(field) ? field : keys.has(field) ? generatedMessageField(field) : field;
  const summaries = [];
  for (const summary of grouping.summaries) {
    if (summary.link?.to.kind === "query") return; // A query patch may contain its own field references.
    summaries.push({ ...summary,
      ...(summary.field ? { field: generatedMessageField(summary.field) } : {}),
      ...(summary.expression ? { expression: mapFormula(summary.expression, generatedMessageField) } : {}),
      ...(summary.link ? { link: { ...summary.link,
        label: typeof summary.link.label === "string" ? summary.link.label : { column: resultField(summary.link.label.column) },
        to: { ...summary.link.to, template: summary.link.to.template.replace(/\{field:([^{}]+)\}/g, (_match, name: string) => `{field:${resultField(name)}}`) },
      } } : {}),
    });
  }
  return { ...mapQuery(query, resultField), grouping: {
    ...grouping,
    source: mapQuery({ ...grouping.source, where: [...authoredUser, ...grouping.source.where] }, generatedMessageField),
    keys: grouping.keys.map(key => typeof key === "string" ? generatedMessageField(key) : { ...key, field: generatedMessageField(key.field) }),
    summaries,
    representative: grouping.representative && { ...grouping.representative,
      orderBy: grouping.representative.orderBy.map(order => ({ ...order, field: generatedMessageField(order.field) })) },
  } };
}

export const toolCallDimensions = new Set([
  "day", "week", "month", "repository_name", "source_kind", "provider", "channel",
  "machine_id", "execution_machine_id", "model_family", "model", "model_effort",
  "effort", "session_kind", "tool_name", "tool_category", "mcp_server", "mcp_method",
  "program", "subcommand", "command_name", "command_category", "activity",
  "operation_name", "operation_key",
]);
export const toolWhereCanUseCalls = (where: WhereTerm[]) => where.every(term => predicatesOf(term).every(clause => toolCallDimensions.has(clause.field)));

export function generatedQueryTarget(dataset: string, query: QueryState): { dataset: string; query: QueryState } {
  if (dataset !== "writing_messages" || Object.values(query.hostState?.params ?? {}).some(values => values.length)) return { dataset, query };
  const details = query.itemDetails;
  if (details?.view || details?.views || details?.page || details?.jsonInspections?.length) return { dataset, query };
  // Computed definitions are registered per dataset and cannot be transferred.
  if (JSON.stringify(query).includes("@computed/")) return { dataset, query };
  const mapped = query.grouping ? mapGroupedQuery(query)
    : mapQuery({ ...query, where: [...authoredUser, ...query.where] }, generatedMessageField);
  return mapped ? { dataset: "messages", query: mapped } : { dataset, query };
}
