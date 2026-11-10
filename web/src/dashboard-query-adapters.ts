import { type WhereClause, type WhereTerm } from "@pythia-software/query-table-core";
import type { AnalysisCatalog, AnalysisMeasure } from "./dashboard-analysis-model";
import type { DashboardQueryContext } from "./dashboard-query-explorations";
import { thinkingWhere, type ThinkingState } from "./thinking-model";
import { habitsAnalysisCatalog } from "./habits-analysis-catalog";
import { shiftDay, type HabitsSummary, type PunchcardActor } from "./habits-model";
import type { HabitsCardView, HabitsView } from "./habits-card";
import { parseRangeInput } from "./chart-range";
import { withSlice } from "./chart-selection";
import { parallelismAnalysisCatalog } from "./parallelism-analysis-catalog";
import { parallelismSelectionQuery } from "./usage-dashboard-model";
import type { ConcurrencySelection } from "./concurrency";

const wait: AnalysisMeasure = { label: "Median observed wait (seconds)", unit: "seconds", format: { kind: "number", decimals: 1 }, op: "count", expression: "MEDIAN([gap_seconds])", value: "gap_seconds", additive: false, note: "Observed post-tool gaps, not pure inference time. Missing timing stays missing." };
const thinkingCatalog: AnalysisCatalog = {
  dataset: "model_requests", time: { first: "requested_at", last: "requested_at" }, timestamp: "requested_at",
  measures: { wait, reasoning: { label: "Reported reasoning tokens", unit: "tokens", format: { kind: "number", decimals: 0 }, op: "sum", field: "reasoning_output_tokens", value: "reasoning_output_tokens", additive: true, note: "Reported reasoning output; missing values are not zero." } },
  breakdowns: Object.fromEntries([
    ["model", "Model"], ["effort", "Effort"], ["model_effort", "Model + effort"], ["speed", "Speed"], ["model_speed", "Model + speed"], ["model_effort_speed", "Model + effort + speed"],
    ["provider", "Provider"], ["harness", "Agent app"], ["repository", "Repository", "repository_name"], ["machine", "Machine"], ["preceding_tool", "Preceding tool"], ["agent_depth", "Agent depth"],
  ].map(([id, label, field]) => [id, { label, fields: [field ?? id] }])),
  units: {}, records: { label: "Model request", plural: "Model requests", note: wait.note }, periods: { day: "day", week: "week", month: "month" },
  cycle: { hour: { kind: "timestamp", field: "requested_at" }, weekday: { kind: "timestamp", field: "requested_at" } },
  trend: { metric: "wait", split: "model" }, scatter: ["wait", "reasoning"],
};

export function thinkingQueryContext(state: ThinkingState, nativeWhere: WhereTerm[], overview = false, now = Date.now()): DashboardQueryContext {
  return { catalog: thinkingCatalog, now, zone: state.timezone || undefined, where: [...nativeWhere, ...thinkingWhere(state, now)],
    state: { view: overview || state.view === "time" ? "trend" : state.view === "calendar" ? "cycle" : "distribution", metric: state.view === "reasoning" && !overview ? "reasoning" : "wait",
      split: overview && state.groupBy === "none" ? "model" : state.groupBy, period: state.period, hours: state.hours, days: state.days, cycle: "hour", minSamples: 0 },
    note: `Recorded request inputs for ${overview ? "Waiting" : "Thinking"}. ${state.view === "calendar" && !overview ? `Hourly inputs before ${state.calendarRows} calendar rows and ${state.calendarMetric === "ratio" ? "relative" : "raw"} display; the comparison uses ${state.hourA}:00 and ${state.hourB}:00. ` : ""}Waits are observed post-tool gaps; missing timing remains missing.` };
}

// The ribbon and the lower analysis chart have different scopes. Only an
// explicitly selected or zoomed ribbon overrides the dashboard's one quartet.
export function ribbonQueryContext(selection: ConcurrencySelection): DashboardQueryContext {
  const query = parallelismSelectionQuery(selection);
  return { catalog: { ...parallelismAnalysisCatalog(selection.settings.counting), request: { params: { parallelism_settings: JSON.stringify({ counting: selection.settings.counting, gap: selection.settings.gap }) } } }, state: { view: "compare", metric: "agent_hours", split: "repository_name" },
    where: query.where, supporting: query, note: "Follows the selected Parallelism ribbon, including its range, counting method and gap. Occupancy records touch the selected range." };
}

export function habitsQueryContext(data: HabitsSummary, chart: HabitsView, view: HabitsCardView, selected = ""): DashboardQueryContext {
  const actor = chart === "calendar" ? view.calendar : chart === "days" ? "you" : view.punchcard;
  const actors: PunchcardActor[] = actor === "agents" ? ["with_you", "unattended"] : actor === "difference" ? ["you", "with_you", "unattended"] : [actor];
  const start = `${String(data.settings.dayStart).padStart(2, "0")}:00`;
  let from = chart === "punchcard" ? data.punchcard.from : chart === "days" ? shiftDay(data.today, 1 - view.shapeDays) : shiftDay(data.today, -((new Date(`${data.today}T12:00:00Z`).getUTCDay() + 6) % 7) - (view.weeks - 1) * 7);
  if (from < data.period_start) from = data.period_start;
  const to = chart === "punchcard" ? data.punchcard.to : data.today;
  let where = withSlice([], "actor", { values: actors, excluded: false });
  const zone = data.timezone === "Local" ? Intl.DateTimeFormat().resolvedOptions().timeZone : data.timezone;
  where.push({ field: "started_at", op: ">=", value: parseRangeInput(`${from}T${start}`, zone).toISOString() },
    { field: "started_at", op: "<", value: parseRangeInput(`${shiftDay(to, 1)}T${start}`, zone).toISOString() });
  // Calendar weekdays and selected habit days use the habit-day boundary,
  // rather than filtering the local-clock weekday of an after-midnight hour.
  if (chart === "calendar" && (!view.weekends || selected)) {
    const days: string[] = [];
    for (let day = from; day <= to; day = shiftDay(day, 1)) if ((!selected || selected === day) && (view.weekends || ![0, 6].includes(new Date(`${day}T12:00:00Z`).getUTCDay()))) days.push(day);
    const dayTerm = (values: string[]): WhereClause => ({ field: "day", op: "matches_regex", value: `^(${values.join("|") || "no-matching-days"})$` });
    const hours = (from: number, to: number): WhereClause => ({ field: "hour_of_day", op: "matches_regex", value: `^(${Array.from({ length: to - from }, (_, i) => String(i + from).padStart(2, "0")).join("|")})$` });
    where.push(data.settings.dayStart ? { any: [
      { all: [dayTerm(days), hours(data.settings.dayStart, 24)] },
      { all: [dayTerm(days.map(day => shiftDay(day, 1))), hours(0, data.settings.dayStart)] },
    ] } : dayTerm(days));
  }
  const catalog = { ...habitsAnalysisCatalog, request: { params: { habits_settings: JSON.stringify(data.settings) } } };
  return { catalog, zone, where, state: { metric: "wall_hours", split: chart === "punchcard" ? "actor" : "none", view: "trend", period: "day" },
    note: `Activity-hour inputs to ${chart === "punchcard" ? "the weekday/hour view" : chart === "days" ? "your daily schedule" : "the activity calendar"}; habit days start at ${start}. ${actor === "difference" ? "Shows you and agents separately before their difference is drawn. " : ""}Streak thresholds and vacation markings are calendar annotations.` };
}
