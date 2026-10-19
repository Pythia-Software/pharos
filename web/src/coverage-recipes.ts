import type { AggregationClause, SummaryColumn, WhereClause, WhereTerm } from "@pythia-software/query-table-core";
import type { CardExploration } from "./card-recipes";
import { trendCover } from "./card-trend";
import type { QueryRecipe } from "./curiosity-data";

// Machine time, parallel agents, carbon, findings and pricing: the areas whose
// Home cards were drafts with bespoke loaders. Each is now an ordinary Query
// Table recipe over tool_calls, parallelism, usage or findings rows.
const sum = (id: string, field: string, label: string): SummaryColumn => ({ id, op: "sum", field, label });
const count = (id: string, label: string): SummaryColumn => ({ id, op: "count", label });
const positive = (field: string): WhereTerm => ({ field, op: ">", value: "0" });
const equal = (field: string, value: string): WhereClause => ({ field, op: "=", value });
const daily = (id: string, op: AggregationClause["op"], field: string, label: string, extra: Partial<AggregationClause> = {}): AggregationClause => ({ id, op, ...(field ? { field } : {}), groupBy: ["day"], groupLimit: 10000, label, ...extra });
function compare(keys: string[], summaries: SummaryColumn[], unit: string, subjects: string, action: string, extra: Partial<CardExploration> = {}): CardExploration {
  return { keys, summaries, valueField: summaries[0].id, unit, subjects, action, labelFields: keys, ...extra };
}
// A part-of-whole pie over grouped rows with an exact Other remainder.
const share = (view: CardExploration, label: string): CardExploration => ({ ...view, coverMetric: { id: view.valueField, op: "sum", field: view.valueField, groupBy: view.keys, groupLimit: 10000, label, display: { kind: "pie" } } });

// ----- Machine time (tool_calls) ---------------------------------------------

const machineNote = "Sums recorded tool-call durations, so overlapping calls add up: this is machine work, not wall-clock time. Calls without a recorded duration count toward calls but not time. Activities follow the Machine Time classification of commands and tools.";
const machine = (recipe: Omit<QueryRecipe, "category" | "idea" | "dataset" | "field" | "unit"> & Partial<QueryRecipe>): QueryRecipe => ({ category: "machines", idea: "tools-05", dataset: "tool_calls", field: "started_at", unit: "ms", note: machineNote, ...recipe });
const machineTime = sum("mt_time", "duration_ms", "Recorded machine time"), machineCalls = count("mt_calls", "Calls");
const perTimedCall: SummaryColumn = { id: "mt_per_call", op: "avg", field: "duration_ms", label: "Per timed call" };
const machineContext = [{ field: "mt_calls", label: "calls" }, { field: "mt_per_call", label: "per timed call", unit: "ms" }];

const machineRecipes: QueryRecipe[] = [
  machine({ id: "machine-time", title: "Recorded machine time", description: "Tool and command time over the last {days} days, by the kind of work.", days: [7, 30], action: "Explore machine time",
    where: [positive("duration_ms")], measures: [{ ...daily("mt_daily", "sum", "duration_ms", "Recorded machine time by day"), groupBy: ["day", "activity"] }],
    cover: { mode: "source", metricId: "mt_daily", kind: "auto", maxGroups: 6, showOther: true, showLegend: true, height: 240 } }),
  machine({ id: "machine-activities", title: "Where machine time goes", description: "Share of recorded tool time by kind of work over the last {days} days.", days: [7, 30], action: "Compare activities",
    where: [positive("duration_ms")], measures: [],
    exploration: compare(["activity"], [machineTime, machineCalls, perTimedCall], "ms", "activities", "Compare activities", { context: machineContext }),
    cover: { mode: "result", metricId: "mt_time", kind: "bar-horizontal", maxGroups: 6, showOther: true } }),
  machine({ id: "machine-operations", title: "Longest-running operations", description: "Commands and tools with the most recorded time over the last {days} days.", days: [7, 30], action: "Inspect slow operations",
    note: `${machineNote} Operations group a program and subcommand (for example “go test”) or a named tool.`, where: [positive("duration_ms")], measures: [],
    exploration: compare(["operation_name", "activity"], [machineTime, machineCalls, perTimedCall], "ms", "operations", "Inspect slow operations", { labelFields: ["operation_name"], context: machineContext }),
    cover: { mode: "result", metricId: "mt_time", kind: "bar-horizontal", maxGroups: 6, showOther: false } }),
  machine({ id: "machine-call-trend", title: "Tool calls per day", description: "Daily tool calls averaged over each trailing seven complete days, across the last 30 days.", days: [30], unit: "calls", action: "Explore daily tool calls",
    note: "Counts every recorded tool call, including errors and calls without a duration, by its local start day. Each point divides seven days' calls by seven, including idle days; today is excluded until it is complete.",
    measures: [daily("mt_daily_calls", "count", "", "Tool calls per day")], cover: trendCover("mt_daily_calls"),
    trend: { dataset: "tools", value: { op: "sum", field: "call_count" }, count: { op: "sum", field: "call_count" }, perDay: true, lookback: 7, completeDays: true, valueLabel: "calls a day · 7-day average", pointValueLabel: "calls a day", trendLabel: "7d avg", sample: "calls",
      detail: "Each point: tool calls in its trailing seven complete local days ÷ 7. The norm is the mean of the prior 30 endpoints." } }),
  machine({ id: "machine-latency-trend", title: "Average tool call time", description: "Mean recorded duration per tool call over each trailing seven complete days, across the last 30 days.", days: [30], action: "Inspect daily call times",
    note: "Total recorded duration ÷ calls with a recorded duration, pooled over each seven-day window (not an average of daily averages). Long-running calls such as waits and watches pull the mean up; Longest-running operations names them. Windows without timed calls are gaps.",
    measures: [daily("mt_daily_mean", "avg", "duration_ms", "Mean recorded call time by day")], cover: trendCover("mt_daily_mean"),
    trend: { dataset: "tools", value: { op: "sum", field: "total_duration_ms" }, per: { op: "sum", field: "timed_count" }, count: { op: "sum", field: "timed_count" }, scale: 1 / 1000, lookback: 7, completeDays: true, valueLabel: "seconds per call · 7-day mean", pointValueLabel: "seconds per timed call", trendLabel: "7d mean", sample: "timed calls",
      detail: "Each point: recorded duration ÷ timed calls over its trailing seven complete local days. The norm is the mean of the prior 30 endpoints." } }),
];

// ----- Parallel agents (parallelism) -----------------------------------------

const parallelNote = "Counts workspaces or agents (your Parallelism counting setting) with an active conversation: observed prompt-to-response turns joined across gaps up to your Parallelism gap setting. Delegated agents run inside their root's workspace. Timing is estimated from transcript timestamps.";
const parallel = (recipe: Omit<QueryRecipe, "category" | "idea" | "dataset" | "field" | "unit"> & Partial<QueryRecipe>): QueryRecipe => ({ category: "machines", idea: "rhythm-03", dataset: "parallelism", field: "started_at", unit: "hours", note: parallelNote, ...recipe });

const parallelRecipes: QueryRecipe[] = [
  parallel({ id: "parallel-days", title: "Parallel hours by day", description: "Hours with one, two, or three or more agents active, for each day of the last {days} days.", days: [7, 30], action: "See each day's timeline",
    note: `${parallelNote} Each hour of wall time is counted once, at the number of agents active then.`,
    measures: [{ ...daily("pl_daily", "sum", "elapsed_hours", "Active hours by agents at once"), groupBy: ["day", "level_band"] }],
    cover: { mode: "source", metricId: "pl_daily", kind: "auto", maxGroups: 3, showOther: false, showLegend: true, height: 228 } }),
  parallel({ id: "parallel-trend", title: "Agents at once", description: "Average number of agents running together while any is active, over each trailing seven days.", days: [30], unit: "agents", action: "Open the parallelism view",
    note: `${parallelNote} Each point weights concurrency by time: agent-hours ÷ hours with any agent active.`,
    measures: [daily("pl_daily_concurrency", "sum", "unit_ms", "Agents at once while active", { expression: "SUM([unit_ms])/NULLIF(SUM([elapsed_ms]),0)" })], cover: trendCover("pl_daily_concurrency"),
    trend: { value: { op: "sum", field: "unit_ms" }, per: { op: "sum", field: "elapsed_ms" }, lookback: 7, completeDays: true, valueLabel: "agents at once while active · 7-day average", pointValueLabel: "agents at once", trendLabel: "7d avg", sample: "agent-hour records",
      detail: "Each point: agent time ÷ wall time with any agent active, over its trailing seven complete local days. Windows with no activity are gaps." } }),
  // Agent hours, not an average: a per-day average would bake the lookback
  // into the formula, and a collected card's window can change later.
  parallel({ id: "parallel-hours", title: "When agents overlap", description: "Agent hours at each hour of the day over the last {days} days, counting parallel agents separately.", days: [7, 30], action: "Find your parallel hours",
    note: `${parallelNote} Taller hours had more agents running at once, more often. Hours with no activity are omitted.`,
    measures: [{ id: "pl_hours", op: "sum", field: "unit_hours", groupBy: ["hour_of_day"], groupLimit: 24, sort: [{ key: "group0", dir: "asc" }], label: "Agent hours by local hour",
      display: { kind: "bar-vertical", xLabel: "Local hour", yLabel: "Agent hours", format: { kind: "number", decimals: 1 } } }],
    cover: { mode: "source", metricId: "pl_hours", kind: "bar-vertical", maxGroups: 24, height: 170 } }),
  parallel({ id: "parallel-repositories", title: "Agent hours by repository", description: "Active agent hours per repository over the last {days} days, counting parallel agents separately.", days: [7, 30], action: "Compare repositories", measures: [],
    exploration: compare(["repository_name"], [sum("pl_agent_hours", "unit_hours", "Agent hours"), sum("pl_elapsed", "repo_elapsed_hours", "Hours with any active"), { id: "pl_together", op: "formula", expression: "SUM([unit_hours])/NULLIF(SUM([repo_elapsed_hours]),0)", label: "At once while active" }, { id: "pl_units", op: "count_distinct", field: "unit_id", label: "Agents" }],
      "hours", "repositories", "Compare repositories", { context: [{ field: "pl_elapsed", label: "h with any active" }, { field: "pl_units", label: "agents" }] }),
    cover: { mode: "result", metricId: "pl_agent_hours", kind: "bar-horizontal", maxGroups: 6, showOther: true } }),
];

// ----- Carbon (usage) --------------------------------------------------------

const carbonNote = "Uses your Carbon Impact settings (estimate range, facility overhead, grid). Token counts are reported; energy and CO₂e are modeled estimates. Models are assigned to size tiers by name; unmatched models use the default tier.";
const carbonWhere = [positive("total_tokens")];
const co2 = sum("cb_co2", "co2e_kg", "Estimated CO₂e (kg)"), energy = sum("cb_energy", "energy_kwh", "Estimated energy (kWh)"), tokens = sum("cb_tokens", "total_tokens", "Recorded tokens");
const carbonContext = [{ field: "cb_tokens", label: "tokens" }, { field: "cb_energy", label: "kWh" }];
const carbon = (id: string, title: string, description: string, keys: string[], action: string, pie: boolean, extra: Partial<QueryRecipe> = {}): QueryRecipe => {
  const view = compare(keys, [co2, tokens, energy], "kg CO₂e", "groups", action, { context: carbonContext });
  return { id, title, description, category: "carbon", idea: "cost-01", dataset: "usage", field: "first_usage_at", days: [7, 30], unit: "kg CO₂e", where: carbonWhere, measures: [], action, note: carbonNote,
    exploration: pie ? share(view, title) : view, cover: pie ? { mode: "result", metricId: co2.id, kind: "pie", maxGroups: 5, showOther: true, showLegend: true, legendColumns: 2, height: 300 } : { mode: "result", metricId: co2.id, kind: "bar-horizontal", maxGroups: 5, showOther: true }, ...extra };
};

const carbonRecipes: QueryRecipe[] = [
  { id: "carbon-trend", title: "Estimated CO₂e", description: "Modeled emissions from recorded tokens over each trailing {days}-day period, across the last 30 days.", category: "carbon", idea: "cost-01", dataset: "usage", field: "first_usage_at", days: [7, 30], unit: "kg CO₂e",
    where: carbonWhere, action: "Explore carbon impact", note: `${carbonNote} Daily endpoints use local calendar days, so the latest point includes today so far.`,
    measures: [daily("cb_daily", "sum", "co2e_kg", "Estimated CO₂e by day (kg)")], cover: trendCover("cb_daily"),
    trend: { value: { op: "sum", field: "co2e_kg" }, valueLabel: "kg CO₂e · past {days} days", pointValueLabel: "kg CO₂e", sample: "usage records",
      detail: "Each point: estimated CO₂e over its trailing {days} local days, including today so far. The norm is the mean of the prior 30 endpoints." } },
  carbon("carbon-tiers", "CO₂e by model size", "Share of estimated emissions from small, mid-size and large models over the last {days} days.", ["carbon_tier"], "Compare model tiers", true, { cover: { mode: "result", metricId: co2.id, kind: "pie", maxGroups: 3, showOther: false, showLegend: true, legendColumns: 2, height: 230 } }),
  carbon("carbon-repositories", "CO₂e by repository", "Repositories with the largest estimated emissions from recorded tokens over the last {days} days.", ["repository_name"], "Compare repositories", false),
  carbon("carbon-models", "CO₂e by model", "Models with the largest estimated emissions from recorded tokens over the last {days} days.", ["model_family"], "Compare models", false),
  carbon("carbon-providers", "CO₂e by provider", "Share of estimated emissions by agent provider over the last {days} days.", ["provider"], "Compare providers", true),
  carbon("carbon-effort", "CO₂e by effort", "Share of estimated emissions by recorded reasoning effort over the last {days} days.", ["effort"], "Compare effort levels", true,
    { note: `${carbonNote} Effort is the level recorded on each usage fact; unspecified effort stays its own slice.` }),
];

// ----- Findings and pricing (current state) ----------------------------------

const likely = sum("fd_likely", "likely_usd", "Likely saving / month (USD)");
const findingsNote = "Likely saving = what the pattern cost × the share a change can remove × how often a change takes × how often the pattern persists untreated. Monthly estimates in USD from the latest Findings analysis.";
const finding = (recipe: Omit<QueryRecipe, "category" | "idea" | "dataset" | "field" | "unit" | "period"> & Partial<QueryRecipe>): QueryRecipe => ({ category: "findings", idea: "learning-03", dataset: "findings", field: "first_seen_at", unit: "USD", period: "current", note: findingsNote, ...recipe });
const openFindings = [equal("state", "open"), positive("likely_usd")];

const findingRecipes: QueryRecipe[] = [
  finding({ id: "findings-open", title: "Biggest open findings", description: "Optimization findings with the largest likely monthly saving, from the latest Findings analysis.", action: "Review your findings", where: openFindings, measures: [],
    exploration: compare(["id"], [likely, { id: "fd_title", op: "row", field: "title", label: "Finding" }, sum("fd_at_stake", "at_stake_usd", "At stake (USD)")], "USD", "open findings", "Review your findings",
      { labelFields: ["fd_title"], representative: { orderBy: [{ field: "first_seen_at", dir: "desc", nulls: "last" }] }, context: [{ field: "fd_at_stake", label: "at stake", unit: "USD" }] }),
    cover: { mode: "result", metricId: likely.id, kind: "bar-horizontal", maxGroups: 3, showOther: false } }),
  finding({ id: "findings-fixes", title: "Savings by kind of fix", description: "Likely monthly savings across open findings, by where the suggested change is made.", action: "Compare kinds of fix", where: openFindings, measures: [],
    note: `${findingsNote} Each open finding counts once, under the kind of change its prompt makes: repository instructions, repository tools and skills, harness settings, automation prompts or your global instructions.`,
    exploration: share(compare(["lever_label"], [likely, count("fd_count", "Open findings")], "USD", "kinds of fix", "Compare kinds of fix", { context: [{ field: "fd_count", label: "open findings" }] }), "Likely saving by kind of fix"),
    cover: { mode: "result", metricId: likely.id, kind: "pie", maxGroups: 5, showOther: true, showLegend: true, height: 300 } }),
  finding({ id: "findings-results", title: "Net savings from findings", description: "What copied finding prompts have saved after their added context cost, across verified results.", action: "See your wins", emptyLabel: "No verified results yet",
    note: "Copying a prompt starts its measurement. Savings come from each finding's fixed before/after plan, net of the added context cost.", where: [positive("wins")],
    measures: [{ id: "fd_net", op: "sum", field: "saved_net_usd", groupBy: [], label: "Saved, net of added context (USD)" }, { id: "fd_saved", op: "sum", field: "saved_usd", groupBy: [], label: "Saved before added context (USD)" }],
    cover: { mode: "source", metricId: "fd_net", kind: "value", height: 170 } }),
];

const pricingRecipes: QueryRecipe[] = [
  { id: "models-needing-prices", title: "Models needing prices", description: "Models whose tokens are unpriced or priced by assumption, by all-time recorded token volume.", category: "library", idea: "cost-01", dataset: "usage", field: "first_usage_at", unit: "tokens", period: "all", action: "Review model pricing",
    note: "All-time token volume. Assumed models borrow a sibling model's price; unpriced models have no retained price on their usage day. Placeholder model names are excluded.",
    where: [positive("total_tokens"), { any: [equal("price_status", "unpriced"), equal("price_status", "assumed")] }, { field: "model", op: "!=", value: "Unknown model" }, { field: "model", op: "starts_with", value: "<", negated: true }], measures: [],
    exploration: compare(["model", "price_status"], [sum("pr_tokens", "total_tokens", "Recorded tokens"), { id: "pr_last", op: "max", field: "day", label: "Last used" }, { id: "pr_providers", op: "count_distinct", field: "provider", label: "Providers" }], "tokens", "models", "Review model pricing"),
    cover: { mode: "result", metricId: "pr_tokens", kind: "bar-horizontal", maxGroups: 5, showOther: false } },
];

export const coverageRecipes: QueryRecipe[] = [...machineRecipes, ...parallelRecipes, ...carbonRecipes, ...findingRecipes, ...pricingRecipes];
