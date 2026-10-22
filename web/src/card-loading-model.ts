import type { AggregationClause, QueryState } from "@pythia-software/query-table-core";
import { cardCoverQuery, humanStudyCoverMatches, type CardCover } from "./card-cover";
import { boxCoverHeight } from "./compact-box-model";
import type { QueryRecipe } from "./curiosity-data";
import { usesWeekdayCover } from "./weekday-cover";

export type CardLoadingKind = "value" | "columns" | "list" | "scatter" | "line" | "box" | "pie" | "donut" | "table" | "text";
export type CardLoadingSpec = { kind: CardLoadingKind; count: number; headline?: boolean; belowLabels?: boolean; height?: number };

// Read the same cover definition as the preview, before any requests finish.
// Placeholder marks describe the display, never estimated data values.
export function cardLoadingPresentation(query: QueryState, options: { recipe?: QueryRecipe; cover?: CardCover; metricId?: string; days?: number } = {}): CardLoadingSpec {
  const study = options.recipe?.humanStudy;
  if (study && (!query.grouping || study === "conversation-lengths") && humanStudyCoverMatches(options.recipe!, options.cover, options.metricId)) {
    const spec: CardLoadingSpec = study === "conversation-lengths" ? { kind: "list", count: 6, height: 240 } : study === "hours" ? { kind: "columns", count: 24, height: 205 } : ["contribution-patterns", "examples"].includes(study) ? { kind: "list", count: study === "examples" ? 3 : 4, height: 210 } : { kind: "line", count: 31, headline: true, height: 260 };
    return { ...spec, height: (options.cover ?? options.recipe?.cover)?.height ?? spec.height };
  }
  let projected = query, metricId = options.metricId;
  if (options.recipe && (query.grouping || options.cover)) {
    const cover = options.cover ?? (metricId ? { mode: query.grouping && !query.aggregations?.length ? "result" as const : "source" as const, metricId } : undefined);
    const result = cardCoverQuery(query, options.recipe, cover);
    projected = result.query; metricId = result.metricId;
  }
  const metrics = projected.aggregations ?? [];
  const metric = metrics.find(metric => metric.id === metricId) ?? metrics[0];
  if (!metric) return { kind: projected.grouping ? "table" : "text", count: 3 };
  const cover = options.cover ?? options.recipe?.cover;
  if (usesWeekdayCover(options.recipe, metric, cover, projected)) {
    const count = Math.min(7, cover?.maxGroups ?? 7);
    return { kind: "box", count, height: cover?.height ?? boxCoverHeight(count, cover?.density === "compact") };
  }
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
  const compact = (options.cover ?? options.recipe?.cover)?.density === "compact";
  const height = limit && (kind === "list" || kind === "table" || kind === "box")
    ? kind === "box" ? boxCoverHeight(count, compact) : Math.max(170, 41 + count * (belowLabels ? 8 + 12 * (metric.display?.list?.maxLabelLines ?? 2) : count >= 7 ? 20 : 24))
    : kind === "box" ? 272 : ["pie", "donut"].includes(kind) ? 300 : kind === "scatter" ? 240 : headline ? 208 : undefined;
  return { kind, count: Math.max(1, Math.min(100, count)), headline, belowLabels, height: (options.cover ?? options.recipe?.cover)?.height ?? height };
}

function loadingKind(metric: AggregationClause, display: string, timeline: boolean): CardLoadingKind {
  if (metric.distribution) return metric.distribution.kind === "histogram" ? "columns" : "box";
  if (metric.expressionY) return "scatter";
  if (display === "value" || !metric.groupBy.length) return "value";
  if (timeline || display === "bar-vertical") return "columns";
  if (display === "line" || display === "scatter" || display === "pie" || display === "donut" || display === "box") return display;
  return "list";
}
