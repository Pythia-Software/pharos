import { groupKeyFields, type AggregationClause, type FieldDef, type MetricDisplay, type QueryState } from "@pythia-software/query-table-core";
import type { QueryRecipe } from "./curiosity-data";
import type { ChartScale } from "./chart-scale";
import type { ChartDisplay } from "./chart-series";

export type CardCover = { mode: "source" | "result"; metricId: string; kind?: MetricDisplay["kind"]; maxGroups?: number; showOther?: boolean; showLegend?: boolean; height?: number; showHeadline?: boolean; legendColumns?: 1 | 2; minSamples?: number; sampleUnit?: string; density?: "compact"; stackBy?: string; timeline?: { rollingPeriods?: number; percent?: boolean; scale?: ChartScale; display?: ChartDisplay; nonzero?: boolean } };

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
      id: column.id, op: ["sum", "count"].includes(column.op) ? "sum" : "max", field: column.id, groupBy: groupKeyFields(query.grouping.keys),
      label: column.label ?? column.id, groupLimit: 12,
      display: { kind: cover.kind ?? "bar-horizontal", ...(unit === "percent" ? { format: { kind: "percent", decimals: 1 } } : unit === "ms" ? { format: { kind: "duration", sourceUnit: "milliseconds" } } : {}) },
    };
    metric.display = { ...metric.display, ...coverDisplay(cover), kind: metric.display?.kind ?? "auto" };
    // Fetch complete cohorts before applying the cover's sample threshold;
    // otherwise small early groups could crowd out eligible later ones.
    if (metric.distribution?.kind === "box" && cover.minSamples) metric.groupLimit = 10000;
    if (!daily) metric.groupLimit = Math.max(12, cover.maxGroups ?? 12);
    const companions: AggregationClause[] = (view?.context ?? []).filter(item => item.field !== column.id).map(item => ({ id: item.field, op: "max", field: item.field, groupBy: groupKeyFields(query.grouping!.keys), label: item.label, groupLimit: Math.max(12, cover.maxGroups ?? 12) }));
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

// The chart a card draws, as its preview and loading placeholder read it.
export function cardChart(query: QueryState, options: { recipe?: QueryRecipe; cover?: CardCover; metricId?: string } = {}): { query: QueryState; clause?: AggregationClause } {
  let projected = query, metricId = options.metricId;
  if (options.recipe && (query.grouping || options.cover)) {
    const cover = options.cover ?? (metricId ? { mode: query.grouping && !query.aggregations?.length ? "result" as const : "source" as const, metricId } : undefined);
    const result = cardCoverQuery(query, options.recipe, cover);
    projected = result.query; metricId = result.metricId;
  }
  const metrics = projected.aggregations ?? [];
  return { query: projected, clause: metrics.find(metric => metric.id === metricId) ?? metrics[0] };
}

// A card split by a key draws its leading groups, plus Other where that is
// exact. Time buckets and fixed sets, such as weekdays, are drawn whole.
// Paired axes and histograms have no groups to limit.
export type CardGroups = { noun: string; max: number; fallback?: number };
export const cardGroupsMax = 100;
const wholeKeys = new Set(["day", "hour", "week", "month", "hour_of_day", "weekday", "level_band"]);
export function cardGroups(clause: AggregationClause | undefined, fields: FieldDef[] = [], subjects?: string): CardGroups | undefined {
  if (!clause || clause.expressionY || clause.distribution?.kind === "histogram" || clause.display?.kind === "value") return;
  const keys = clause.groupBy.filter(field => !wholeKeys.has(field) && field !== clause.dateGrouping?.field);
  if (!keys.length) return;
  // Lowercase words but keep acronyms such as MCP and URL.
  const label = (field: string) => (fields.find(item => item.name === field)?.label ?? field.replace(/_/g, " ")).replace(/\S+/g, word => /^[A-Z0-9]{2,}s?$/.test(word) ? word : word.toLowerCase());
  const noun = subjects ?? (keys.length === 1 ? pluralNoun(label(keys[0])) : `${keys.map(label).join("/")} groups`);
  const fallback = clause.display?.maxGroups;
  return { noun, max: Math.max(cardGroupsMax, fallback ?? 0), ...(fallback !== undefined ? { fallback } : {}) };
}
export function pluralNoun(noun: string): string {
  return /[^aeiou]y$/.test(noun) ? `${noun.slice(0, -1)}ies` : /(s|x|ch|sh)$/.test(noun) ? `${noun}es` : `${noun}s`;
}
export function validCardGroups(value: unknown, max = cardGroupsMax): value is number {
  return Number.isInteger(value) && (value as number) >= 1 && (value as number) <= max;
}

// The group count a card's editors offer, if its chart has groups. Studies
// and Habits sections draw their own fixed charts.
export function cardGroupsFor(query: QueryState, options: { recipe?: QueryRecipe; cover?: CardCover; metricId?: string }, fields: FieldDef[] = []): CardGroups | undefined {
  const recipe = options.recipe;
  if (recipe?.habits || recipe?.trend || recipe?.humanStudy && humanStudyCoverMatches(recipe, options.cover, options.metricId)) return;
  // A destination's own name for its rows reads better than its fields,
  // when it names something other than plain groups of them.
  const { clause } = cardChart(query, options), view = recipe?.exploration;
  const named = view && !/\bgroups$/.test(view.subjects) && JSON.stringify(clause?.groupBy) === JSON.stringify(groupKeyFields(view.keys));
  return cardGroups(clause, fields, named ? view.subjects : undefined);
}

// A collected card can show more or fewer groups than its definition. With a
// recipe the count joins the cover; a saved query's own chart takes it directly.
export function withCardGroups(query: QueryState, options: { recipe?: QueryRecipe; cover?: CardCover; metricId?: string }, groups?: number): { query: QueryState; cover?: CardCover } {
  if (groups === undefined) return { query, cover: options.cover };
  if (options.recipe) {
    const base = options.cover ?? (options.metricId ? { mode: query.grouping && !query.aggregations?.length ? "result" as const : "source" as const, metricId: options.metricId } : defaultCardCover(options.recipe));
    return { query, cover: base && { ...base, maxGroups: groups } };
  }
  const metrics = query.aggregations ?? [], selected = metrics.find(metric => metric.id === options.metricId) ?? metrics[0];
  return { query: { ...query, aggregations: metrics.map(metric => metric === selected ? { ...metric, ...(metric.groupLimit ? { groupLimit: Math.max(metric.groupLimit, groups) } : {}), display: { ...metric.display, kind: metric.display?.kind ?? "auto", maxGroups: groups } } : metric) }, cover: options.cover };
}

export function cardKind(recipe: QueryRecipe): string {
  if (recipe.habits) return recipe.habits.kind;
  if (recipe.weekday && (!recipe.cover?.kind || recipe.cover.kind === "auto")) return "Distribution";
  if (recipe.humanStudy) return recipe.humanStudy === "conversation-lengths" ? "Distribution" : ["contribution-patterns", "examples"].includes(recipe.humanStudy) ? "Composition" : recipe.humanStudy === "specific-feedback" ? "Rate" : "Timeline";
  const metric = recipe.exploration?.coverMetric ?? recipe.measures[0];
  return !metric ? "Text" : ["pie", "donut"].includes(recipe.cover?.kind ?? metric.display?.kind ?? "") ? "Composition" : metric.distribution?.kind === "box" ? "Distribution" : metric.distribution?.kind === "histogram" ? "Histogram" : metric.expressionY ? "Relationship" : (metric.dateGrouping || metric.groupBy[0] === "day" || metric.groupBy[0] === "hour_of_day") ? "Timeline" : metric.display?.format?.kind === "percent" || recipe.exploration?.unit === "percent" ? "Rate" : "Ranking";
}
