import { defaultChartScale } from "./chart-scale";
import type { AnalysisState, LegacyView } from "./dashboard-analysis-model";
import type { TokenChartView } from "./usage-presets";
import type { engineUsageTopics } from "./usage-workspace-model";

// The chart each Analyze dashboard opens with, and merges a link's
// analyze_chart over (useUsageDashboardState). The page and the site map
// (site-map.ts) both read these.
// The legacy Tokens page's saved chart starts from defaultTokenChart too.
export const defaultTokenChart: TokenChartView = { metric: "cost", split: "model_family", period: "week", hours: 6, days: 3, rollingPeriods: 0, scale: defaultChartScale, display: "interval" };
// Specialized dashboards own their own state contract. Keep the engine catalog
// exhaustive without inventing engine metrics for those pages.
// These are data settings only: how each chart is drawn by default is its
// catalog's (viewDefaults), so a link holds a view setting only when the
// reader chose it.
const daily = { period: "day" } as const;
export const dashboardDefaults: Record<string, AnalysisState> = {
  scatter: { view: "scatter", unit: "conversation", metric: "human_typed", metricY: "tokens_cost", split: "none" },
  compaction: { metric: "compactions", split: "trigger", ...daily },
  labels:{metric:"messages",split:"family",period:"day",view:"trend_line"},
  calls: { metric: "calls", split: "tool_name", view: "compare" },
  activity: { metric: "conversations", split: "provider_model", ...daily },
  tokens: { metric: defaultTokenChart.metric, split: defaultTokenChart.split, hours: 6, days: 3, ...daily },
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
  session_shape: { metric: "sessions", split: "length_band", period: "day" },
  // Spend by outcome over time: sessions are sparser than usage, so weeks.
  outcomes: { metric: "cost", split: "outcome_label", period: "week" },
  // The branches that took the most spend, compared, each within its
  // project: main in ten repositories is ten branches.
  branches: { view: "compare", metric: "cost", split: "project_branch" },
} satisfies Record<keyof typeof engineUsageTopics, AnalysisState>;

// The view settings each dashboard drew without a value before version 3,
// when its defaults held them and were written into every link it opened.
// readAnalysisLink leaves a version 2 link's value out where it equals one,
// so it is drawn by default. Frozen: do not change with today's defaults.
const legacyDaily = { values: { nonzero: true } };
export const legacyViewDefaults: Record<keyof typeof engineUsageTopics, LegacyView> = {
  scatter: { values: { scale: "linear", scaleY: "linear" } },
  compaction: legacyDaily, labels: { values: { share: true } }, calls: { values: {} }, activity: legacyDaily,
  tokens: { values: { nonzero: true, rollingPeriods: 0, scale: "half", display: "interval" } },
  human: legacyDaily, parallelism: legacyDaily, work_hours: legacyDaily, tools: { values: {} }, mcp: { values: {} }, carbon: legacyDaily,
  time: legacyDaily, skills: legacyDaily, agent_experience: legacyDaily, agent_event_rates: { values: {} },
  session_shape: { values: { rollingPeriods: 7 }, chose: true },
  outcomes: legacyDaily, branches: { values: {} },
};
