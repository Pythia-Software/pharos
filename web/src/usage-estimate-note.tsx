import { EstimateInfoButton } from "./estimate-info";
import React from "react";
import { durationEstimateExplanation, estimateExplanation, hasUsageEstimates } from "./usage-estimate-model";

export function UsageEstimateNote({ results, dataset }: { results: readonly unknown[]; dataset: string }) {
  if (!hasUsageEstimates(results)) return null;
  return <p className="usage-dashboard-note">{["tools", "tool_calls"].includes(dataset) ? durationEstimateExplanation : estimateExplanation}<EstimateInfoButton /></p>;
}
