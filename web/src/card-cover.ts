import { type AggregationClause, type MetricDisplay, type QueryState } from "@pythia-software/query-table-core";
import type { QueryRecipe } from "./curiosity-data";

export type CardCover = { mode: "source" | "result"; metricId: string; kind?: MetricDisplay["kind"]; maxGroups?: number; showOther?: boolean; showLegend?: boolean; height?: number; showHeadline?: boolean; legendColumns?: 1 | 2; minSamples?: number; sampleUnit?: string; density?: "compact"; stackBy?: string };

function coverDisplay(cover: CardCover): Partial<MetricDisplay> {
  return { ...(cover.maxGroups !== undefined ? { maxGroups: cover.maxGroups } : {}),
    ...(cover.showOther !== undefined ? { showOther: cover.showOther } : {}),
    ...(cover.showLegend !== undefined ? { legendPosition: cover.showLegend ? "bottom" : "none" } : {}) };
}
export function defaultCardCover(recipe: QueryRecipe): CardCover | undefined {
  if (recipe.cover) return recipe.cover;
  if (!recipe.measures.length) return;
  const metric = recipe.measures[0];
  // Distributions and paired axes describe observations, not group medians.
  if (recipe.exploration && !metric.distribution && !metric.expressionY) return {
    mode: "result", metricId: recipe.exploration.valueField,
    kind: recipe.exploration.coverMetric ? "auto" : "bar-horizontal",
  };
  return { mode: "source", metricId: metric.id, kind: metric.display?.kind ?? "bar-horizontal" };
}

// Study identity comes from its source measure or grouped value, rather than
// an editable cover. Both catalog templates and saved cards can select another
// metric, which must use native execution instead of the original study.
export function humanStudyCoverMatches(recipe: QueryRecipe, cover = recipe.cover, metricId?: string): boolean {
  const studyMetricId = recipe.exploration?.valueField ?? recipe.measures[0]?.id;
  const mode = recipe.exploration ? "result" : "source";
  return (!metricId || metricId === studyMetricId) && (!cover || cover.mode === mode && cover.metricId === studyMetricId);
}

export function cardCoverQuery(query: QueryState, recipe: QueryRecipe, requested?: CardCover): { query: QueryState; metricId?: string } {
  const cover = requested ?? defaultCardCover(recipe);
  if (!cover) return { query };
  if (query.grouping && cover.mode === "result") {
    const column = query.grouping.summaries.find(column => column.id === cover.metricId && column.op !== "row");
    if (!column) return { query };
    const view = recipe.exploration;
    const unit = view?.valueField === column.id ? view.unit : column.field === "duration_ms" || column.expression === "MEDIAN([duration_ms])" ? "ms" : ["card_rate", "card_escalation"].includes(column.id) ? "percent" : "";
    const daily = view?.coverMetric?.field === column.id;
    const metric: AggregationClause = daily ? { ...view!.coverMetric!, id: column.id, display: { ...view!.coverMetric!.display, kind: cover.kind ?? "auto" } } : {
      id: column.id, op: ["sum", "count"].includes(column.op) ? "sum" : "max", field: column.id, groupBy: query.grouping.keys,
      label: column.label ?? column.id, groupLimit: 12,
      display: { kind: cover.kind ?? "bar-horizontal", ...(unit === "percent" ? { format: { kind: "percent", decimals: 1 } } : unit === "ms" ? { format: { kind: "duration", sourceUnit: "milliseconds" } } : {}) },
    };
    metric.display = { ...metric.display, ...coverDisplay(cover), kind: metric.display?.kind ?? "auto" };
    // Fetch complete cohorts before applying the cover's sample threshold;
    // otherwise small early groups could crowd out eligible later ones.
    if (metric.distribution?.kind === "box" && cover.minSamples) metric.groupLimit = 10000;
    if (!daily) metric.groupLimit = Math.max(12, cover.maxGroups ?? 12);
    const companions: AggregationClause[] = (view?.context ?? []).filter(item => item.field !== column.id).map(item => ({ id: item.field, op: "max", field: item.field, groupBy: query.grouping!.keys, label: item.label, groupLimit: Math.max(12, cover.maxGroups ?? 12) }));
    return { query: { ...query, aggregations: [metric, ...companions] }, metricId: column.id };
  }
  // A source chart shares the destination's source predicates. The destination
  // itself remains a grouped table and retains its HAVING and row layout.
  const source = query.grouping?.source ?? query;
  const metrics = source.aggregations?.length ? source.aggregations : recipe.measures;
  const selected = metrics.find(metric => metric.id === cover.metricId) ?? metrics[0];
  return { query: { ...source, aggregations: metrics.map(metric => metric === selected ? { ...metric,
    ...(cover.maxGroups && metric.groupLimit ? { groupLimit: Math.max(metric.groupLimit, cover.maxGroups) } : {}),
    display: { ...metric.display, ...coverDisplay(cover), kind: cover.kind ?? metric.display?.kind ?? "auto" } } : metric) }, metricId: selected?.id };
}

export function cardKind(recipe: QueryRecipe): string {
  if (recipe.humanStudy) return recipe.humanStudy === "conversation-lengths" ? "Distribution" : ["contribution-patterns", "examples"].includes(recipe.humanStudy) ? "Composition" : recipe.humanStudy === "specific-feedback" ? "Rate" : "Timeline";
  const metric = recipe.exploration?.coverMetric ?? recipe.measures[0];
  return !metric ? "Text" : ["pie", "donut"].includes(recipe.cover?.kind ?? metric.display?.kind ?? "") ? "Composition" : metric.distribution?.kind === "box" ? "Distribution" : metric.distribution?.kind === "histogram" ? "Histogram" : metric.expressionY ? "Relationship" : (metric.dateGrouping || metric.groupBy[0] === "day" || metric.groupBy[0] === "hour_of_day") ? "Timeline" : metric.display?.format?.kind === "percent" || recipe.exploration?.unit === "percent" ? "Rate" : "Ranking";
}
