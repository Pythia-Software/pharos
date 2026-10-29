import { defaultChartScale } from "./chart-scale";
import type { AnalysisState } from "./dashboard-analysis-model";
import type { TokenChartView } from "./usage-presets";
import type { UsageTopic } from "./usage-workspace-model";

// The chart each Analyze dashboard opens with, and merges a link's
// analyze_chart over (useUsageDashboardState). The page and the site map
// (site-map.ts) both read these.
// The legacy Tokens page's saved chart starts from defaultTokenChart too.
export const defaultTokenChart: TokenChartView = { metric: "cost", split: "model_family", period: "week", hours: 6, days: 3, rollingPeriods: 0, scale: defaultChartScale, display: "interval" };
export const dashboardDefaults: Record<UsageTopic, AnalysisState> = {
  tokens: { ...defaultTokenChart, period: "day" },
  human: { metric: "typed", split: "none", period: "day" },
  parallelism: { metric: "agent_hours", split: "level_band", period: "day" },
  tools: { metric: "calls", split: "tool_category", view: "compare" },
  mcp: { metric: "calls", split: "tool_name", view: "compare" },
  carbon: { metric: "co2", split: "type", period: "day" },
  time: { metric: "time", split: "activity", period: "day" },
  skills: { metric: "records", split: "skill_name", period: "day" },
};
