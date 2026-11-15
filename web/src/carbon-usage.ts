import type { AggregationClause, WhereTerm } from "@pythia-software/query-table-core";
import { carbonCategories, type CarbonCounts } from "./carbon-analysis-catalog";
import type { MetricResult } from "./metric-request";

// Carbon Impact's estimate reads the usage it covers through query-v3 on the
// Usage dataset: token counts by model tier and category, before any energy
// assumption, so the calculator (carbon.js) can apply the reader's settings
// without asking again. Its filter choices are a second, unfiltered request.

// Every clause reads hour, which sends it to the hourly usage records
// (executeUsageQueryV3): a range that starts or ends inside a day counts only
// that range's hours, not the whole day's record. The records with no hour
// are the usage with no date; they count in the totals but have no place on
// a chart.
const dated: WhereTerm = { field: "hour", op: "is_not_null", value: "" };
const undated: WhereTerm = { field: "hour", op: "is_null", value: "" };
const tokenFields = [...new Set(carbonCategories.flatMap(category => category.fields))];
const sides = [["dated", dated], ["undated", undated]] as const;

export const carbonUsageMetrics: AggregationClause[] = [
  ...sides.flatMap(([side, term]) => tokenFields.map((field): AggregationClause => ({ id: `${side}:${field}`, op: "sum", field, groupBy: ["carbon_tier"], where: [term] }))),
  { id: "undated:total_tokens", op: "sum", field: "total_tokens", groupBy: [], where: [undated] },
  // Usage whose tier was not chosen from its model's name: reported models
  // the factors do not recognize, and usage that reported no model.
  ...sides.map(([side, term]): AggregationClause => ({ id: `${side}:default_models`, op: "sum", field: "total_tokens", groupBy: ["model"], where: [term, { field: "carbon_tier_basis", op: "=", value: "default" }] })),
  ...sides.map(([side, term]): AggregationClause => ({ id: `${side}:unreported`, op: "sum", field: "total_tokens", groupBy: [], where: [term, { field: "carbon_tier_basis", op: "=", value: "unreported" }] })),
];

// The filters offer every value in the retained usage, so one filter cannot
// hide another's options or strand a selection that now matches nothing.
export const carbonChoiceFields = ["repository_name", "provider", "channel", "machine_id", "execution_machine_id"] as const;
export const carbonChoiceMetrics: AggregationClause[] = carbonChoiceFields.map(field => ({ id: `choices:${field}`, op: "count", groupBy: [field] }));

export type CarbonTokens = { key?: string; tokens: CarbonCounts };
export type CarbonUsage = {
  windows: CarbonTokens[];
  undated_tokens: number;
  unclassified_tokens: number;
  default_tier_models: Array<{ model: string; tokens: number }>;
  default_tier_unknown_tokens: number;
};

// A list cut short would misstate what fell back to the default tier, or
// hide filter options: it fails the read instead.
function complete(metric: MetricResult | undefined, id: string): MetricResult {
  if (!metric) throw new Error(`The server omitted ${id}.`);
  if (metric.coverage === "partial" || metric.groupCount !== undefined && metric.groupCount > metric.buckets.length || metric.buckets.some(bucket => bucket.error))
    throw new Error("Carbon usage is incomplete.");
  return metric;
}
const amount = (value: unknown) => Number(value ?? 0);

export function carbonUsage(results: MetricResult[]): CarbonUsage {
  const byId = new Map(results.map(metric => [metric.id, metric]));
  const metric = (id: string) => complete(byId.get(id), id);
  const tokens: CarbonCounts = {};
  let unclassified = 0;
  for (const [side] of sides) for (const field of tokenFields) for (const bucket of metric(`${side}:${field}`).buckets) {
    const tier = (tokens[String(bucket.keys[0] ?? "")] ??= Object.fromEntries(carbonCategories.map(category => [category.key, 0])));
    for (const category of carbonCategories) if (category.fields.includes(field)) tier[category.key] += amount(bucket.value);
    if (field === "unclassified_tokens") unclassified += amount(bucket.value);
  }
  const models = new Map<string, number>();
  for (const [side] of sides) for (const bucket of metric(`${side}:default_models`).buckets) {
    const model = String(bucket.keys[0] ?? "");
    models.set(model, (models.get(model) ?? 0) + amount(bucket.value));
  }
  const total = (id: string) => amount(metric(id).buckets[0]?.value);
  return {
    windows: [{ key: "filtered", tokens }],
    undated_tokens: total("undated:total_tokens"),
    unclassified_tokens: unclassified,
    default_tier_models: [...models].map(([model, tokens]) => ({ model, tokens })).sort((a, b) => b.tokens - a.tokens || (a.model < b.model ? -1 : a.model > b.model ? 1 : 0)),
    default_tier_unknown_tokens: sides.reduce((sum, [side]) => sum + total(`${side}:unreported`), 0),
  };
}

export function carbonChoices(results: MetricResult[]): Record<string, string[]> {
  const byId = new Map(results.map(metric => [metric.id, metric]));
  return Object.fromEntries(carbonChoiceFields.map(field => {
    const values = complete(byId.get(`choices:${field}`), field).buckets.map(bucket => bucket.keys[0] == null ? "" : String(bucket.keys[0]));
    return [field, [...new Set(values)].sort()];
  }));
}
