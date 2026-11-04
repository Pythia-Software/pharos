import type { AggregationClause, SummaryColumn, WhereTerm } from "@pythia-software/query-table-core";
import type { QueryRecipe } from "./curiosity-data";
import { trendCover } from "./card-trend";
import { weekday } from "./weekday-recipes";
import { analysisQuery, measureExpression, ratioPart, type AnalysisState } from "./dashboard-analysis-model";
import { sessionShapeAnalysisCatalog as catalog, toolData } from "./session-shape-analysis-catalog";

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

// Sessions per day by a breakdown: counts, or shares of each trailing seven
// days.
function daily(id: string, title: string, description: string, split: string, note: string, { shares = false, ...extra }: Partial<QueryRecipe> & { shares?: boolean } = {}): QueryRecipe {
  const metric: AggregationClause = { id: `ss_${split}_daily`, op: "count", groupBy: ["day", split], groupLimit: 10000, label: "Sessions per day", display: { kind: "auto", yLabel: shares ? "Share of sessions" : "Sessions" } };
  return session({ id, title, description, action: "Explore sessions over time", note, measures: [metric],
    cover: { mode: "source", metricId: metric.id, kind: "auto", maxGroups: 15, showOther: true, showLegend: true, showHeadline: false, height: 300, ...(shares ? { timeline: { rollingPeriods: 7, percent: true } } : {}) }, ...extra });
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
  daily("session-archetype-mix", "Your sessions by kind", "Sessions you typed in each day over the last {days} days, by archetype.", "archetype", `${archetypeNote} ${steeredNote}`, { where: [steered] }),
  daily("session-length-mix", "How long your sessions are", "Each day's share of your sessions by length band, over each trailing seven days in the last {days} days.", "length_band",
    `${lengthNote} Shares divide each band's sessions by all your sessions in the same seven days. ${steeredNote}`, { shares: true, where: [steered] }),
  session({ id: "session-active-time", title: "Active time by session kind", description: "Each session's active time over the last {days} days, by archetype; log axis.", idea: "rhythm-05", unit: "seconds", action: "Compare session active time", ...activeTime, where: [...activeTime.where, positive("active_seconds")],
    note: "Active time adds the gaps between consecutive messages of 300 seconds or less; a longer gap counts as zero. Only sessions with some active time count; kinds with fewer than three are left out.",
    cover: box("ss_active_time", 3) }),
  session({ id: "session-turn-cycle", title: "Turn cycle by provider", description: "Each session's median wait from your message to the agent's next one, over the last {days} days; log axis.", idea: "rhythm-07", unit: "seconds", action: "Compare turn cycles", ...turnCycle,
    note: "A session's turn cycle is the median gap from a typed human message to the agent's next message, leaving out gaps over 1,800 seconds. Only sessions with a turn cycle count; providers with fewer than three are left out.",
    cover: box("ss_turn_cycle", 3) }),
  session({ id: "session-autonomy", title: "Autonomy by provider", description: "Tool calls per message you typed, in each of your sessions over the last {days} days; log axis.", idea: "models-02", unit: "calls per message", action: "Compare autonomy",
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
  // Context, and when you work.
  session({ id: "session-model-context", title: "Peak context by model", description: "Each session's largest request as a share of its model's context window, over the last {days} days.", idea: "context-02", unit: "percent", action: "Compare peak context", ...contextPeak,
    note: `${coverage.window} Models with fewer than ten sessions are left out.`, cover: box("ss_context_peak", 10, 8) }),
  weekday("session-weekday", "When you start sessions", "Sessions you typed in, started each day, by weekday, over the last {days} days, including idle days.", "sessions / day", { id: "ss_weekday_sessions", op: "count", groupBy: [], label: "Sessions per day" },
    { dataset: "session_shape", field: "started_at", category: "sessions", idea: "rhythm-01", where: [steered], action: "Explore when sessions start",
      note: `Each box compares complete local days of the same weekday, Monday through Sunday; days without a session count as zero. The first partial day and today are left out. Uses the device timezone. ${steeredNote}` }),
];
