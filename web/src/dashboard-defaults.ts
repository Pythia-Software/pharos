import { defaultChartScale } from "./chart-scale";
import type { AnalysisState } from "./dashboard-analysis-model";
import type { TokenChartView } from "./usage-presets";
import type { engineUsageTopics } from "./usage-workspace-model";

// The chart each Analyze dashboard opens with, and merges a link's
// analyze_chart over (useUsageDashboardState). The page and the site map
// (site-map.ts) both read these.
// The legacy Tokens page's saved chart starts from defaultTokenChart too.
export const defaultTokenChart: TokenChartView = { metric: "cost", split: "model_family", period: "week", hours: 6, days: 3, rollingPeriods: 0, scale: defaultChartScale, display: "interval" };
// Specialized dashboards own their own state contract. Keep the engine catalog
// exhaustive without inventing engine metrics for those pages.
// Daily trends open without their zeroes: idle days (weekends, days away)
// would otherwise fill most of a sparse month with empty columns.
const daily = { period: "day", nonzero: true } as const;
export const dashboardDefaults: Record<string, AnalysisState> = {
  scatter: { view: "scatter", unit: "conversation", metric: "human_typed", metricY: "tokens_cost", split: "none", scale: "linear", scaleY: "linear" },
  compaction: { metric: "compactions", split: "trigger", ...daily },
  labels:{metric:"messages",split:"family",period:"day",view:"trend_line",share:true},
  calls: { metric: "calls", split: "tool_name", view: "compare" },
  activity: { metric: "conversations", split: "provider_model", ...daily },
  tokens: { ...defaultTokenChart, ...daily },
  human: { metric: "typed", split: "none", ...daily },
  parallelism: { metric: "agent_hours", split: "level_band", ...daily },
  work_hours: { metric: "wall_hours", split: "actor", ...daily },
  tools: { metric: "calls", split: "tool_category", view: "compare" },
  mcp: { metric: "calls", split: "tool_name", view: "compare" },
  carbon: { metric: "co2", split: "type", ...daily },
  time: { metric: "time", split: "activity", ...daily },
  skills: { metric: "records", split: "skill_name", ...daily },
  agent_experience: { metric: "events", split: "family_group", ...daily },
  agent_event_rates: { metric: "per_thousand", split: "friction_repository", view: "compare", minSamples: 200 },
  // Session Shape chooses each chart itself (its catalog's present); a
  // day with a trailing week's smoothing reads its bands best.
  session_shape: { metric: "sessions", split: "length_band", period: "day", rollingPeriods: 7 },
} satisfies Record<keyof typeof engineUsageTopics, AnalysisState>;
