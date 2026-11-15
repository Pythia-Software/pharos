import { EMPTY_QUERY, type AggregationClause } from "@pythia-software/query-table-core";
import { cardLoadingPresentation, type CardLoadingSpec } from "./card-loading-model";
import type { ResolvedAnalysis } from "./dashboard-analysis-model";

// A cycle is folded after its request, and binned distributions use a
// separate renderer. Other analysis charts already describe their display.
export function analysisLoadingPresentation(clause: AggregationClause, view: ResolvedAnalysis, binned: boolean): CardLoadingSpec {
  if (view.view === "cycle") return { kind: view.reduce === "median" ? "box" : "columns", count: view.cycle === "hour" ? 24 : 7 };
  if (binned) return { kind: "columns", count: 16 };
  return cardLoadingPresentation({ ...EMPTY_QUERY, aggregations: [clause] });
}
