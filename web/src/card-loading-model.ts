import type { AggregationClause, QueryState } from "@pythia-software/query-table-core";
import { cardCoverQuery, type CardCover } from "./card-cover";
import type { QueryRecipe } from "./curiosity-data";

export type CardLoadingKind = "value" | "columns" | "list" | "scatter" | "line" | "box" | "pie" | "donut" | "table" | "text";
export type CardLoadingSpec = { kind: CardLoadingKind; count: number; headline?: boolean; belowLabels?: boolean; height?: number };

// Read the same cover definition as the preview, before any requests finish.
// Placeholder marks describe the display, never estimated data values.
export function cardLoadingPresentation(query: QueryState, options: { recipe?: QueryRecipe; cover?: CardCover; metricId?: string; days?: number } = {}): CardLoadingSpec {
  let projected = query, metricId = options.metricId;
  if (options.recipe && (query.grouping || options.cover)) {
    const cover = options.cover ?? (metricId ? { mode: query.grouping && !query.aggregations?.length ? "result" as const : "source" as const, metricId } : undefined);
    const result = cardCoverQuery(query, options.recipe, cover);
    projected = result.query; metricId = result.metricId;
  }
  const metrics = projected.aggregations ?? [];
  const metric = metrics.find(metric => metric.id === metricId) ?? metrics[0];
  if (!metric) return { kind: projected.grouping ? "table" : "text", count: 3 };
  const requested = options.cover?.maxGroups ?? metric.display?.maxGroups;
  const limit = typeof requested === "number" && Number.isSafeInteger(requested) && requested > 0 ? Math.min(100, requested) : undefined;
  const display = options.cover?.kind ?? metric.display?.kind ?? "auto";
  const timeline = metric.groupBy[0] === "day" && display === "auto" && metric.op === "sum" && !metric.expression && !metric.distribution;
  const kind = loadingKind(metric, display, timeline);
  const count = kind === "value" ? 1 : kind === "columns" && metric.distribution?.kind === "histogram" ? metric.distribution.bins ?? 10
    : timeline ? Math.min(100, (options.days ?? 7) + 1)
    : limit ?? (kind === "box" ? 3 : kind === "scatter" ? 12 : kind === "columns" || kind === "line" ? 8 : kind === "pie" || kind === "donut" ? 8 : 4);
  const placement = metric.display?.list?.barPosition;
  const belowLabels = kind === "list" && (placement === "below" || !placement && (metric.groupBy.length > 1
    || Boolean(options.recipe?.exploration?.labelFields?.length) || metric.groupBy.some(field => /title|signature|path|url/.test(field))));
  const headline = timeline;
  const height = limit && (kind === "list" || kind === "table" || kind === "box")
    ? Math.max(170, (kind === "box" ? 105 : 41) + count * (kind === "box" ? 52 : belowLabels ? 8 + 12 * (metric.display?.list?.maxLabelLines ?? 2) : 24))
    : kind === "box" ? 272 : headline ? 208 : undefined;
  return { kind, count: Math.max(1, Math.min(100, count)), headline, belowLabels, height };
}

function loadingKind(metric: AggregationClause, display: string, timeline: boolean): CardLoadingKind {
  if (metric.distribution) return metric.distribution.kind === "histogram" ? "columns" : "box";
  if (metric.expressionY) return "scatter";
  if (display === "value" || !metric.groupBy.length) return "value";
  if (timeline || display === "bar-vertical") return "columns";
  if (display === "line" || display === "scatter" || display === "pie" || display === "donut" || display === "box") return display;
  return "list";
}
