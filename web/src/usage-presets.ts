import type { ChartPeriod } from "./chart-period";
import type { ChartScale } from "./chart-scale";
import type { ChartPresentation } from "./chart-selection";
import type { ChartDisplay } from "./chart-series";

export type TokenSplit = "type" | "channel" | "machine_id" | "execution_machine_id" | "provider" | "model_family" | "model_effort" | "effort" | "project_name" | "session_kind";
export type TokenChartView = ChartPresentation & { metric: "tokens" | "cost" | "percent" | "percent_cost"; split: TokenSplit; period: ChartPeriod; hours: number; days: number; rollingPeriods: number; scale: ChartScale; display?: ChartDisplay };
