import type { AggregationClause, SummaryColumn, WhereTerm } from "@pythia-software/query-table-core";
import type { QueryRecipe } from "./curiosity-data";
import { trendCover } from "./card-trend";
import { weekday } from "./weekday-recipes";
import { analysisQuery, measureExpression, ratioPart, type AnalysisState } from "./dashboard-analysis-model";
import { sessionShapeAnalysisCatalog as catalog, toolData } from "./session-shape-analysis-catalog";
import { bandBins } from "./session-shape-bands";

// Session Shape cards (session_shape: one row per agent session). Each card
// opens the Session Shape dashboard on the state its cover draws
// (card-destinations.ts). A rate counts only the sessions with the evidence
// it needs, so its card's population is that measure's own coverage, which
// the dashboard shows as its filters.
type Recipe = Omit<QueryRecipe, "category" | "idea" | "dataset" | "field" | "unit" | "measures"> & Partial<QueryRecipe>;
const session = (recipe: Recipe): QueryRecipe => ({ category: "sessions", idea: "conversation-04", dataset: "session_shape", field: "started_at", unit: "sessions", days: [30, 90], measures: [], ...recipe });
const sessions: SummaryColumn = { id: "ss_sessions", op: "count", label: "Sessions" };
const hours: SummaryColumn = { id: "ss_active_hours", op: "sum", field: "active_hours", label: "Active hours" };
const notEqual = (field: string, value: string): WhereTerm => ({ field, op: "!=", value });
const coverage = {
  tool: "Only sessions the tool ledger covers count.",
  authored: "Only sessions whose prompts are all classified count.",
  both: "Only sessions the tool ledger covers and whose prompts are all classified count.",
  window: "Only sessions whose model's context window is known count.",
};

// Sessions per day by a band's value, as the dashboard's heatmap draws it:
// each trailing seven days' sessions in each fine bin of the value
// (session-shape-bands.ts), on its continuous axis. Only sessions with a
// value count.
const binned = "Each cell averages the day's sessions in one narrow range of the value over seven days.";
function daily(id: string, title: string, description: string, split: keyof typeof bandBins, note: string, extra: Partial<QueryRecipe> = {}): QueryRecipe {
  const bins = bandBins[split];
  const metric: AggregationClause = { id: `ss_${split}_daily`, op: "count", groupBy: ["day", bins.field], groupLimit: 10000, label: "Sessions per day", display: { kind: "auto", yLabel: bins.label } };
  return session({ id, title, description, action: "Explore sessions over time", note: `${note} ${binned}`, measures: [metric],
    cover: { mode: "source", metricId: metric.id, kind: "auto", maxGroups: 15, showOther: true, showLegend: false, showHeadline: false, timeline: { rollingPeriods: 7, display: "heatmap" } }, ...extra,
    where: [...extra.where ?? [], { field: bins.field, op: "is_not_null", value: "" }] });
}

// A per-session measure's distribution over time, as the dashboard draws
// the measure without a breakdown (heatmapSource).
function bands(id: string, title: string, description: string, metric: string, note: string): QueryRecipe {
  const measure = catalog.measures[metric];
  return daily(id, title, description, measure.band as keyof typeof bandBins, `${note} ${steeredNote}`,
    { action: `Explore ${measure.label.toLowerCase()} over time`, unit: "sessions", where: [steered, ...measure.where ?? []] });
}

// The dashboard's own distribution clause and population, so the cover is
// the chart the card opens. The cover's minSamples hides small groups, as
// the dashboard's group minimum does.
function distribution(id: string, state: AnalysisState) {
  const { query, clause: { minCount: _minimum, ...clause } } = analysisQuery(catalog, { ...state, view: "distribution" }, []);
  return { where: query.where, measures: [{ ...clause, id }] };
}

// A pooled rate by group: summed numerator over summed denominator among
// groups with at least `minimum` sessions, with both parts and the sessions
// beside it.
function rate(id: string, title: string, description: string, metric: string, split: string, subjects: string, partLabels: [string, string], note: string, minimum = 10, extra: Partial<QueryRecipe> = {}): QueryRecipe {
  const measure = catalog.measures[metric], [top, bottom] = measure.ratio!.map(ratioPart);
  const value: SummaryColumn = { id: `ss_${metric}`, op: "formula", expression: measureExpression(measure)!, label: measure.label };
  const numerator: SummaryColumn = { id: `ss_${top.field}`, op: "sum", field: top.field, label: partLabels[0] };
  const denominator: SummaryColumn = { id: `ss_${bottom.field}`, op: "sum", field: bottom.field, label: partLabels[1] };
  return session({ id, title, description, unit: "percent", action: `Compare ${subjects}`, where: [...measure.where ?? []], note: `${note} Groups with fewer than ${minimum} sessions are left out.`,
    exploration: { keys: [split], summaries: [value, numerator, denominator, sessions], having: [{ field: sessions.id, op: ">=", value: String(minimum) }], valueField: value.id, unit: "percent", subjects, action: `Compare ${subjects}`, labelFields: [split], tieBreakField: sessions.id,
      context: [{ field: denominator.id, label: partLabels[1].toLowerCase() }, { field: sessions.id, label: "sessions" }] },
    cover: { mode: "result", metricId: value.id, kind: "bar-horizontal", maxGroups: 10, showOther: false }, ...extra });
}

// A measure of your sessions as a sparkline: its value over each trailing
// seven complete days, pooled as the catalog pools it (summed parts, or a
// daily average of a total), at each of the last 31 days.
function sparkline(id: string, title: string, description: string, metric: string, labels: { value: string; point: string }, note: string, extra: Partial<QueryRecipe> = {}): QueryRecipe {
  const measure = catalog.measures[metric], [top, bottom] = measure.ratio?.map(ratioPart) ?? [];
  const part = (item: { op: string; field?: string }) => ({ op: item.op as "sum" | "count", ...item.field ? { field: item.field } : {} });
  const value = top ? part(top) : part({ op: measure.op, field: measure.field }), per = bottom ? part(bottom) : undefined;
  const daily: AggregationClause = { id: `ss_${metric}_trend`, op: value.op, ...value.field ? { field: value.field } : {}, ...per ? { expression: `SUM([${value.field}])/NULLIF(${per.op === "count" ? "COUNT" : "SUM"}([${per.field}]),0)` } : {},
    groupBy: ["day"], groupLimit: 10000, label: `${measure.label} by day` };
  return session({ id, title, description, unit: measure.unit, days: [30, 90], action: `Explore ${measure.label.toLowerCase()} over time`, where: [steered, ...measure.where ?? []],
    note: `Each point pools seven complete local days; today is left out until it is complete. ${note} ${steeredNote}`, measures: [daily], cover: trendCover(daily.id),
    trend: { value, ...per ? { per } : { perDay: true }, count: { op: "count" }, lookback: 7, completeDays: true, ...measure.format?.kind === "percent" ? { format: "percent" as const } : {},
      valueLabel: `${labels.value} · 7 days`, pointValueLabel: labels.point, trendLabel: "7d", sample: "sessions",
      detail: `Each point: ${per ? `${labels.point} pooled over` : `the daily average of`} its trailing seven complete local days. The norm is the mean of the prior 30 endpoints.` }, ...extra });
}

const lengthNote = "Length counts every message of the session's own stream; a delegated run is its own session.";
const archetypeNote = "Automated sessions have no typed human message. The others are Quick (5 or fewer typed messages), Standard (15 or fewer), Deep (50 or fewer) and Marathon (more).";
// Sessions a person typed in: most sessions are automated runs, which would
// drown out every shape of your own work.
const steered = notEqual("archetype", "automated");
const steeredNote = "Automated sessions, with no typed human message, are left out; the dashboard opens with that filter.";
const failureNote = `Failed calls divided by tool calls, pooled over each group's sessions. Rejections, interrupts and hook blocks are not failures. ${coverage.tool}`;

const activeTime = distribution("ss_active_time", { metric: "active_time", split: "archetype", scale: "logarithmic", minSamples: 3 });
const turnCycle = distribution("ss_turn_cycle", { metric: "turn_cycle", split: "provider", scale: "logarithmic", minSamples: 3 });
const autonomy = distribution("ss_autonomy", { metric: "autonomy", split: "provider", scale: "logarithmic", minSamples: 3 });
const contextPeak = distribution("ss_context_peak", { metric: "context_peak", split: "model_family", minSamples: 10 });
// A log axis cannot place zero, so log cards leave out sessions with none.
const positive = (field: string): WhereTerm => ({ field, op: ">", value: "0" });
const box = (metricId: string, minSamples: number, maxGroups = 6) => ({ mode: "source" as const, metricId, kind: "box" as const, maxGroups, minSamples, sampleUnit: "sessions", density: "compact" as const });

export const sessionShapeCardRecipes: QueryRecipe[] = [
  // Usage kinds: who starts sessions, and what kind your own are.
  session({ id: "session-sources", title: "Where sessions come from", description: "Sessions started in the last {days} days, by the app that ran them.", action: "Compare harnesses",
    note: "A harness is the app that ran the session: an interactive client, an SDK or a batch runner. Delegated runs count as their own sessions.",
    exploration: { keys: ["harness"], summaries: [sessions], valueField: sessions.id, unit: "sessions", subjects: "harnesses", action: "Compare harnesses", labelFields: ["harness"] },
    cover: { mode: "result", metricId: sessions.id, kind: "bar-horizontal", maxGroups: 10, showOther: true } }),
  daily("session-archetype-mix", "Your sessions by kind", "Your sessions each day by the messages you typed in them, over the last {days} days.", "archetype",
    `${archetypeNote} ${steeredNote}`, { where: [steered] }),
  daily("session-length-mix", "How long your sessions are", "Your sessions each day by their message count, over the last {days} days.", "length_band",
    `${lengthNote} ${steeredNote}`, { where: [steered] }),
  session({ id: "session-active-time", title: "Active time by session kind", description: "Each session's active time over the last {days} days, by archetype; log axis.", idea: "rhythm-05", unit: "seconds", action: "Compare session active time", ...activeTime, where: [...activeTime.where, positive("active_seconds")],
    note: "Active time adds the gaps between consecutive messages of 300 seconds or less; a longer gap counts as zero. Only sessions with some active time count; kinds with fewer than three are left out.",
    cover: box("ss_active_time", 3) }),
  session({ id: "session-turn-cycle", title: "Turn cycle by provider", description: "Each session's median wait from your message to the agent's next one, over the last {days} days; log axis.", idea: "rhythm-07", unit: "seconds", action: "Compare turn cycles", ...turnCycle,
    note: "A session's turn cycle is the median gap from a typed human message to the agent's next message, leaving out gaps over 1,800 seconds. Only sessions with a turn cycle count; providers with fewer than three are left out.",
    cover: box("ss_turn_cycle", 3) }),
  session({ id: "session-autonomy", title: "Tool calls per typed message, by provider", description: "Each session's tool calls divided by the messages you typed in it, over the last {days} days; log axis.", idea: "models-02", unit: "calls per typed message", action: "Compare tool calls per typed message",
    ...autonomy, where: [...autonomy.where, positive("autonomy"), steered], note: `${coverage.both} Sessions without a tool call are left out. ${steeredNote} Providers with fewer than three sessions are left out.`, cover: box("ss_autonomy", 3) }),
  // Failures: by model, by session kind, over time.
  rate("session-model-failure-rates", "Failure rate by model", "Failed tool calls divided by all tool calls, by model, over the last {days} days.", "failure_rate", "model_family", "model failure rates", ["Failed calls", "Tool calls"],
    `${failureNote} Synthetic placeholder turns are left out; the dashboard opens with that filter.`, 20, { idea: "tools-01", where: [...toolData, notEqual("model_family", "<synthetic>")] }),
  rate("session-kind-failure-rates", "Failure rate by session kind", "Failed tool calls divided by all tool calls, automated runs against the sessions you typed in, over the last {days} days.", "failure_rate", "archetype", "failure rates by session kind", ["Failed calls", "Tool calls"],
    `${archetypeNote} ${failureNote}`, 10, { idea: "tools-01" }),
  session({ id: "session-failure-trend", title: "Failure rate over time", description: "Failed tool calls divided by all tool calls over each trailing seven complete days, across the last 30 days.", idea: "tools-06", unit: "percent", days: [30], action: "Explore failure rate over time", where: [...toolData],
    note: `Each point pools seven complete local days: their failed calls over their tool calls. Today is left out until it is complete. Rejections, interrupts and hook blocks are not failures. ${coverage.tool}`,
    measures: [{ id: "ss_daily_failure_rate", op: "sum", field: "failed_calls", expression: "SUM([failed_calls])/NULLIF(SUM([tool_calls]),0)", groupBy: ["day"], groupLimit: 10000, label: "Failure rate by day" }], cover: trendCover("ss_daily_failure_rate"),
    trend: { value: { op: "sum", field: "failed_calls" }, per: { op: "sum", field: "tool_calls" }, count: { op: "sum", field: "tool_calls" }, lookback: 7, completeDays: true, format: "percent", valueLabel: "failure rate · 7 days", pointValueLabel: "failure rate", trendLabel: "7d", sample: "tool calls",
      detail: "Each point: failed calls ÷ tool calls over its trailing seven complete local days. The norm is the mean of the prior 30 endpoints." } }),
  // Where your time goes, and how often handbacks are verified.
  session({ id: "session-repository-hours", title: "Where your active hours go", description: "Active hours of the sessions you typed in over the last {days} days, by repository.", unit: "hours", action: "Compare repositories", where: [steered],
    note: `Active time adds the gaps between consecutive messages of 300 seconds or less; a longer gap counts as zero. ${steeredNote}`,
    exploration: { keys: ["repository_name"], summaries: [hours, sessions], valueField: hours.id, unit: "hours", subjects: "repositories", action: "Compare repositories", labelFields: ["repository_name"],
      context: [{ field: sessions.id, label: "sessions" }] },
    cover: { mode: "result", metricId: hours.id, kind: "bar-horizontal", maxGroups: 10, showOther: true } }),
  rate("session-repository-verification", "Verified handbacks by repository", "Handbacks after an edit with a test or build after the last edit, over the last {days} days.", "verification", "repository_name", "verified handbacks", ["Verified handbacks", "Handbacks after an edit"],
    `Verified handbacks divided by handbacks after an edit, pooled. Read-only commands are not verification. ${coverage.both}`, 20, { idea: "verification-03" }),
  // Your sessions over time, without a breakdown.
  sparkline("session-count-trend", "Your sessions a day", "Sessions you typed in, a day, averaged over each trailing seven complete days.", "sessions", { value: "sessions a day", point: "sessions a day" },
    "A session counts on the day it starts."),
  sparkline("session-hours-trend", "Your active hours a day", "Active hours of the sessions you typed in, a day, averaged over each trailing seven complete days.", "active_hours", { value: "active hours a day", point: "active hours a day" },
    "Active time adds the gaps between consecutive messages of 300 seconds or less, on the day the session starts."),
  // Per-session measures over time, as their distributions.
  bands("session-autonomy-trend", "Tool calls per typed message over time", "Your sessions each day by tool calls per typed message, over the last {days} days.", "autonomy",
    `Each session's tool calls divided by the messages you typed in it. ${coverage.both}`),
  bands("session-turn-cycle-trend", "Turn cycle over time", "Your sessions each day by their median turn cycle, over the last {days} days.", "turn_cycle",
    "A session's turn cycle is the median gap from a typed message to the agent's next one, leaving out gaps over 1,800 seconds; only sessions with one count."),
  bands("session-context-trend", "Peak context over time", "Your sessions each day by their largest request as a share of the model's context window, over the last {days} days.", "context_peak",
    coverage.window),
  // Context, and when you work.
  session({ id: "session-model-context", title: "Peak context by model", description: "Each session's largest request as a share of its model's context window, over the last {days} days.", idea: "context-02", unit: "percent", action: "Compare peak context", ...contextPeak,
    note: `${coverage.window} Models with fewer than ten sessions are left out.`, cover: box("ss_context_peak", 10, 8) }),
  weekday("session-weekday", "When you start sessions", "Sessions you typed in, started each day, by weekday, over the last {days} days, including idle days.", "sessions / day", { id: "ss_weekday_sessions", op: "count", groupBy: [], label: "Sessions per day" },
    { dataset: "session_shape", field: "started_at", category: "sessions", idea: "rhythm-01", where: [steered], action: "Explore when sessions start",
      note: `Each box compares complete local days of the same weekday, Monday through Sunday; days without a session count as zero. The first partial day and today are left out. Uses the device timezone. ${steeredNote}` }),
  // The range still picks sessions by their start; each one folds by the day
  // of its last message.
  { ...weekday("session-last-weekday", "When you last interact, by weekday", "Sessions you typed in, by the weekday of their last message, over the last {days} days, including idle days.", "sessions / day", { id: "ss_last_weekday_sessions", op: "count", groupBy: [], label: "Sessions per day" },
    { dataset: "session_shape", field: "last_message_at", category: "sessions", idea: "rhythm-01", where: [steered], action: "Explore when sessions end",
      note: `Each box compares complete local days of the same weekday, Monday through Sunday, placing each session by its last message; days without one count as zero. Sessions are those started in the range. Uses the device timezone. ${steeredNote}` }), field: "started_at" },
];
