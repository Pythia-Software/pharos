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
const notEqual = (field: string, value: string): WhereTerm => ({ field, op: "!=", value });
const coverage = {
  tool: "Only sessions the tool ledger covers count.",
  authored: "Only sessions whose prompts are all classified count.",
  both: "Only sessions the tool ledger covers and whose prompts are all classified count.",
  window: "Only sessions whose model's context window is known count.",
};

// Sessions per day by a band, as shares of each trailing seven days.
function mix(id: string, title: string, description: string, split: string, note: string, extra: Partial<QueryRecipe> = {}): QueryRecipe {
  const metric: AggregationClause = { id: `ss_${split}_daily`, op: "count", groupBy: ["day", split], groupLimit: 10000, label: "Sessions per day" };
  return session({ id, title, description, action: "Explore session mix over time", note, measures: [metric],
    cover: { mode: "source", metricId: metric.id, kind: "auto", maxGroups: 15, showOther: true, showLegend: true, showHeadline: false, height: 300, timeline: { rollingPeriods: 7, percent: true } }, ...extra });
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
    cover: { mode: "result", metricId: value.id, kind: "bar-horizontal", maxGroups: 6, showOther: false }, ...extra });
}

const lengthNote = "Length counts every message of the session's own stream; a delegated run is its own session.";
const archetypeNote = "Automated sessions have no typed human message. The others are Quick (5 or fewer typed messages), Standard (15 or fewer), Deep (50 or fewer) and Marathon (more).";
const failureNote = `Failed calls divided by tool calls, pooled over each group's sessions. Rejections, interrupts and hook blocks are not failures. ${coverage.tool}`;
const failureRate = (id: string, title: string, description: string, note: string, where: WhereTerm[] = []) => {
  const recipe = rate(id, title, description, "failure_rate", "model_family", "model failure rates", ["Failed calls", "Tool calls"], note, 10, { idea: "tools-01" });
  return { ...recipe, where: [...recipe.where ?? [], ...where] };
};

const activeTime = distribution("ss_active_time", { metric: "active_time", split: "none", scale: "logarithmic" });
const turnCycle = distribution("ss_turn_cycle", { metric: "turn_cycle", split: "provider", scale: "logarithmic", minSamples: 3 });
const autonomy = distribution("ss_autonomy", { metric: "autonomy", split: "provider", minSamples: 3 });
const contextPeak = distribution("ss_context_peak", { metric: "context_peak", split: "model_family", minSamples: 10 });
const box = (metricId: string, minSamples: number) => ({ mode: "source" as const, metricId, kind: "box" as const, maxGroups: 6, minSamples, density: "compact" as const });

// One point per session, grouped by session as the dashboard's session unit
// is: a session per group, which the service counts within its group budget.
const activeSeconds: SummaryColumn = { id: "ss_active", op: "formula", expression: measureExpression(catalog.measures.active_time)!, label: "Active time" };
const sessionCalls: SummaryColumn = { id: "ss_tool_calls", op: "sum", field: "tool_calls", label: "Tool calls" };
const scatter: AggregationClause = { id: activeSeconds.id, op: "max", field: activeSeconds.id, expression: `MAX([${activeSeconds.id}])`, expressionY: `MAX([${sessionCalls.id}])`, groupBy: ["conversation_id"], groupLimit: 2000, sort: [{ key: "value", dir: "desc" }], label: "Sessions",
  display: { kind: "scatter", maxGroups: 2000, xLabel: "Active time (log)", yLabel: "Tool calls (log)", xScale: { mode: "log" }, yScale: { mode: "log" }, xFormat: catalog.measures.active_time.format, yFormat: catalog.measures.tool_calls.format, legendPosition: "none" } };

export const sessionShapeCardRecipes: QueryRecipe[] = [
  mix("session-length-mix", "Session length mix", "Each day's share of sessions by length band, over each trailing seven days in the last {days} days.", "length_band", `${lengthNote} Shares divide each band's sessions by all sessions in the same seven days.`),
  mix("session-archetype-mix", "Session archetypes over time", "Each day's share of sessions by archetype, over each trailing seven days in the last {days} days.", "archetype", `${archetypeNote} Shares divide each archetype's sessions by all sessions in the same seven days.`),
  session({ id: "session-archetypes", title: "Session archetypes", description: "Sessions started in the last {days} days, by archetype.", action: "Compare archetypes", note: archetypeNote,
    exploration: { keys: ["archetype"], summaries: [sessions], valueField: sessions.id, unit: "sessions", subjects: "archetypes", action: "Compare archetypes", labelFields: ["archetype"],
      coverMetric: { id: sessions.id, op: "sum", field: sessions.id, groupBy: ["archetype"], groupLimit: 10000, label: "Sessions by archetype", display: { kind: "pie" } } },
    cover: { mode: "result", metricId: sessions.id, kind: "pie", maxGroups: 6, showOther: true, showLegend: true, height: 300 } }),
  session({ id: "session-active-time", title: "How long sessions run", description: "Active time per session over the last {days} days, on a logarithmic axis.", idea: "rhythm-05", unit: "seconds", action: "Explore session active time", ...activeTime,
    note: "Active time adds the gaps between consecutive messages of 300 seconds or less; a longer gap counts as zero. Only sessions with timestamps count.",
    cover: { mode: "source", metricId: "ss_active_time", kind: "histogram", height: 240 } }),
  session({ id: "session-turn-cycle", title: "Turn cycle by provider", description: "Each session's median wait from your message to the agent's next one, over the last {days} days.", idea: "rhythm-07", unit: "seconds", action: "Compare turn cycles", ...turnCycle,
    note: "A session's turn cycle is the median gap from a typed human message to the agent's next message, leaving out gaps over 1,800 seconds. Only sessions with a turn cycle count; providers with fewer than three are left out.",
    cover: box("ss_turn_cycle", 3) }),
  session({ id: "session-autonomy", title: "Autonomy by provider", description: "Tool calls per typed message in each session over the last {days} days.", idea: "models-02", unit: "calls per message", action: "Compare autonomy", ...autonomy,
    note: `A session with no typed message counts one. ${coverage.both} Providers with fewer than three sessions are left out.`,
    cover: box("ss_autonomy", 3) }),
  failureRate("session-model-failure-rates", "Failure rate by model", "Failed tool calls divided by all tool calls in sessions over the last {days} days, by model.", failureNote),
  failureRate("session-steered-failure-rates", "Failure rate by model, steered sessions", "Failed tool calls divided by all tool calls in sessions you typed in over the last {days} days, by model.",
    `${failureNote} Automated sessions, with no typed human message, are left out.`, [notEqual("archetype", "automated")]),
  rate("session-repository-steering", "Steering load by repository", "Typed messages with a correction cue divided by typed messages over the last {days} days, by repository.", "steering_load", "repository_name", "steering load", ["Corrections", "Typed messages"],
    `Correction cues are read in the text you typed; pasted text is never a correction. ${coverage.authored}`, 10, { idea: "conversation-02" }),
  rate("session-model-one-shot", "One-shot edits by model", "Your turns with an edit and no edit–verify–edit retry, divided by turns with an edit, over the last {days} days.", "one_shot_rate", "model_family", "one-shot edits", ["One-shot edit turns", "Edit turns"],
    `A retry is an edit to a file, then a test or build command, then another edit to that file within one human turn. ${coverage.both}`, 10, { idea: "verification-05" }),
  rate("session-repository-verification", "Verified handbacks by repository", "Handbacks after an edit with a test or build after the last edit, over the last {days} days.", "verification", "repository_name", "verified handbacks", ["Verified handbacks", "Handbacks after an edit"],
    `Verified handbacks divided by handbacks after an edit, pooled. Read-only commands are not verification. ${coverage.both}`, 10, { idea: "verification-03" }),
  session({ id: "session-model-context", title: "Peak context by model", description: "Each session's largest request as a share of its model's context window, over the last {days} days.", idea: "context-02", unit: "percent", action: "Compare peak context", ...contextPeak,
    note: `${coverage.window} Models with fewer than ten sessions are left out.`, cover: box("ss_context_peak", 10) }),
  session({ id: "session-failure-trend", title: "Failure rate over time", description: "Failed tool calls divided by all tool calls over each trailing seven complete days, across the last 30 days.", idea: "tools-06", unit: "percent", days: [30], action: "Explore failure rate over time", where: [...toolData],
    note: `Each point pools seven complete local days: their failed calls over their tool calls. Today is left out until it is complete. Rejections, interrupts and hook blocks are not failures. ${coverage.tool}`,
    measures: [{ id: "ss_daily_failure_rate", op: "sum", field: "failed_calls", expression: "SUM([failed_calls])/NULLIF(SUM([tool_calls]),0)", groupBy: ["day"], groupLimit: 10000, label: "Failure rate by day" }], cover: trendCover("ss_daily_failure_rate"),
    trend: { value: { op: "sum", field: "failed_calls" }, per: { op: "sum", field: "tool_calls" }, count: { op: "sum", field: "tool_calls" }, lookback: 7, completeDays: true, format: "percent", valueLabel: "failure rate · 7 days", pointValueLabel: "failure rate", trendLabel: "7d", sample: "tool calls",
      detail: "Each point: failed calls ÷ tool calls over its trailing seven complete local days. The norm is the mean of the prior 30 endpoints." } }),
  weekday("session-weekday", "Sessions by day of the week", "Sessions started each day, by weekday, over the last {days} days, including idle days.", "sessions / day", { id: "ss_weekday_sessions", op: "count", groupBy: [], label: "Sessions per day" },
    { dataset: "session_shape", field: "started_at", category: "sessions", idea: "rhythm-01", where: [], action: "Explore when sessions start",
      note: "Each box compares complete local days of the same weekday, Monday through Sunday; days without a session count as zero. The first partial day and today are left out. Uses the device timezone." }),
  session({ id: "session-active-tools", title: "Active time and tool calls", description: "Each session's active time against its tool calls over the last {days} days; log–log axes.", idea: "learning-08", unit: "seconds", days: [7, 30], action: "Explore session outliers", where: [...toolData, { field: "conversation_id", op: "is_not_null", value: "" }],
    note: `One point is one session. ${coverage.tool} Points with no active time or no tool calls cannot be drawn on log axes. Shows up to 2,000 sessions with the most active time.`,
    exploration: { keys: ["conversation_id"], summaries: [activeSeconds, sessionCalls], valueField: activeSeconds.id, unit: "seconds", subjects: "sessions", action: "Explore session outliers", labelFields: ["conversation_id"], coverMetric: scatter },
    cover: { mode: "result", metricId: activeSeconds.id, kind: "scatter", height: 300 } }),
  session({ id: "session-length-without-batch", title: "Session length, Antigravity CLI excluded", description: "Sessions started in the last {days} days by length band, leaving out the Antigravity CLI harness.", action: "Compare session lengths", where: [notEqual("harness", "antigravity-cli")],
    note: `${lengthNote} Sessions run by the Antigravity CLI harness are left out; the dashboard opens with that filter.`,
    exploration: { keys: ["length_band"], summaries: [sessions], valueField: sessions.id, unit: "sessions", subjects: "length bands", action: "Compare session lengths", labelFields: ["length_band"],
      coverMetric: { id: sessions.id, op: "sum", field: sessions.id, groupBy: ["length_band"], groupLimit: 10000, label: "Sessions by length", display: { kind: "pie" } } },
    cover: { mode: "result", metricId: sessions.id, kind: "pie", maxGroups: 6, showOther: true, showLegend: true, height: 300 } }),
];
