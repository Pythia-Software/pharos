import { predicatesOf, type WhereTerm } from "@pythia-software/query-table-core";

// The Session Shape dashboard's own panels, without a DOM: the signal table,
// which compares the last seven days with the eight weeks before them, and
// the signals Sessions to read ranks by (docs/session-shape.md).

// A duration in seconds, as people say it: 42 s, 12 min, 1 h 20 min.
export function formatSeconds(value: number): string {
  if (!Number.isFinite(value)) return "—";
  if (value < 60) return `${Math.round(value)} s`;
  if (value < 3600) return `${Math.round(value / 60)} min`;
  const hours = Math.floor(value / 3600), minutes = Math.round((value % 3600) / 60);
  return minutes ? `${hours} h ${minutes} min` : `${hours} h`;
}

// The signal table. Each signal is a pooled rate: a summed numerator over a
// summed denominator, both from sessions with the evidence the signal needs
// (a signal without evidence is empty in the dataset, so it adds nothing).
// Counts become rates per 100 calls or sessions, so a busy week and a quiet
// one compare.
export type SignalScale = "percent" | "per100";
export type SignalPart = { id: string; expression: string };
export type SignalDefinition = { id: string; label: string; numerator: string; denominator: string; scale: SignalScale; per?: string; evidence: string; note: string };
const sum = (field: string): SignalPart => ({ id: field, expression: `SUM([${field}])` });
const flagged = (id: string, ...flags: string[]): SignalPart => ({ id, expression: `SUM(${flags.reduceRight((inner, flag) => `IF([${flag}],${inner},0)`, "1")})` });
export const signalParts: SignalPart[] = [
  flagged("tool_sessions", "has_tool_data"), flagged("authored_sessions", "has_authorship"), flagged("both_sessions", "has_tool_data", "has_authorship"), flagged("window_sessions", "has_context_window"),
  sum("tool_calls"), sum("failed_calls"), sum("rejections"), sum("interrupts"), sum("hook_blocks"), sum("identical_retries"),
  sum("edit_turns"), sum("one_shot_edit_turns"), sum("churned_files"), sum("mid_task_compactions"), sum("context_pressure"),
  sum("typed_human_messages"), sum("corrections"), sum("edit_handbacks"), sum("verified_handbacks"), sum("long_unprompted_runs"), sum("ended_on_failure"),
];
export const signalDefinitions: SignalDefinition[] = [
  { id: "failed_calls", label: "Failure rate", numerator: "failed_calls", denominator: "tool_calls", scale: "percent", evidence: "tool_sessions", note: "Failed calls over tool calls. Rejections, interrupts and hook blocks are not failures." },
  { id: "rejections", label: "Rejections", numerator: "rejections", denominator: "tool_calls", scale: "per100", per: "calls", evidence: "tool_sessions", note: "Calls you rejected, per 100 tool calls." },
  { id: "interrupts", label: "Interrupts", numerator: "interrupts", denominator: "tool_calls", scale: "per100", per: "calls", evidence: "tool_sessions", note: "Calls interrupted, per 100 tool calls." },
  { id: "hook_blocks", label: "Hook blocks", numerator: "hook_blocks", denominator: "tool_calls", scale: "per100", per: "calls", evidence: "tool_sessions", note: "Calls a hook blocked, per 100 tool calls." },
  { id: "identical_retries", label: "Identical retries", numerator: "identical_retries", denominator: "tool_calls", scale: "per100", per: "calls", evidence: "tool_sessions", note: "Repeats of a byte-identical call in a run of three or more, per 100 tool calls." },
  { id: "thrash_retries", label: "One-shot edit turns", numerator: "one_shot_edit_turns", denominator: "edit_turns", scale: "percent", evidence: "both_sessions", note: "Turns with an edit and no edit–verify–edit retry on one file, over turns with an edit." },
  { id: "churned_files", label: "Churned files", numerator: "churned_files", denominator: "tool_sessions", scale: "per100", per: "sessions", evidence: "tool_sessions", note: "Files edited three or more times within fewer than 10 messages, per 100 sessions." },
  { id: "mid_task_compactions", label: "Mid-task compactions", numerator: "mid_task_compactions", denominator: "tool_sessions", scale: "per100", per: "sessions", evidence: "tool_sessions", note: "Compactions in the middle of a task, per 100 sessions." },
  { id: "context_pressure", label: "Context pressure", numerator: "context_pressure", denominator: "window_sessions", scale: "percent", evidence: "window_sessions", note: "Sessions whose peak context exceeded 90% of the window, over sessions whose window is known." },
  { id: "corrections", label: "Steering load", numerator: "corrections", denominator: "typed_human_messages", scale: "percent", evidence: "authored_sessions", note: "Typed messages with a correction cue, over typed messages." },
  { id: "unverified_handbacks", label: "Verified handbacks", numerator: "verified_handbacks", denominator: "edit_handbacks", scale: "percent", evidence: "both_sessions", note: "Handbacks after an edit with a test or build after the last edit, over handbacks after an edit." },
  { id: "long_unprompted_runs", label: "Long unprompted runs", numerator: "long_unprompted_runs", denominator: "both_sessions", scale: "per100", per: "sessions", evidence: "both_sessions", note: "Runs of more than 150 tool calls without a typed message, per 100 sessions." },
  { id: "ended_on_failure", label: "Ended on a failed call", numerator: "ended_on_failure", denominator: "both_sessions", scale: "percent", evidence: "both_sessions", note: "Experimental. Sessions whose last tool call failed with nothing typed after it, over sessions." },
];
// The signals the sessions table can list sessions by: each with its count field.
export const sessionSignals = ["failed_calls", "failure_streak_max", "rejections", "interrupts", "hook_blocks", "identical_retries", "thrash_retries", "churned_files", "mid_task_compactions", "context_pressure", "corrections", "unverified_handbacks", "long_unprompted_runs", "ended_on_failure"] as const;
export type SessionSignal = typeof sessionSignals[number];
export const sessionSignalLabels: Record<SessionSignal, string> = {
  failed_calls: "Failed calls", failure_streak_max: "Longest failure streak", rejections: "Rejections", interrupts: "Interrupts", hook_blocks: "Hook blocks", identical_retries: "Identical retries",
  thrash_retries: "Edit–verify–edit retries", churned_files: "Churned files", mid_task_compactions: "Mid-task compactions", context_pressure: "Context pressure", corrections: "Corrections",
  unverified_handbacks: "Unverified handbacks", long_unprompted_runs: "Long unprompted runs", ended_on_failure: "Ended on a failed call",
};

// Days and their windows: the last seven days ending at end, and the eight
// seven-day windows before them. Days are the dataset's local YYYY-MM-DD.
export const trailingWindows = 8, windowDays = 7;
const dayTime = (day: string) => Date.parse(`${day}T00:00:00Z`);
export const shiftDay = (day: string, days: number) => new Date(dayTime(day) + days * 86_400_000).toISOString().slice(0, 10);
export const signalTableStart = (end: string) => shiftDay(end, -(windowDays * (trailingWindows + 1) - 1));
export type DayTotals = Map<string, Record<string, number>>;
// Each part's sums by day (one metric per part, grouped by day) as one row
// of parts per day.
export function dayTotals(results: Array<{ id: string; buckets: Array<{ keys: unknown[]; value: unknown }> }>): DayTotals {
  const totals: DayTotals = new Map();
  for (const result of results) for (const bucket of result.buckets) {
    const day = String(bucket.keys[0] ?? "");
    if (!/^\d{4}-\d{2}-\d{2}$/.test(day)) continue;
    const row = totals.get(day) ?? {};
    row[result.id] = (row[result.id] ?? 0) + (Number(bucket.value) || 0);
    totals.set(day, row);
  }
  return totals;
}
function windowTotals(totals: DayTotals, end: string, index: number): Record<string, number> {
  const sum: Record<string, number> = {};
  for (let offset = 0; offset < windowDays; offset++) {
    for (const [id, value] of Object.entries(totals.get(shiftDay(end, -(index * windowDays + offset))) ?? {})) sum[id] = (sum[id] ?? 0) + value;
  }
  return sum;
}
const scaled = (definition: SignalDefinition, numerator: number, denominator: number) => denominator > 0 ? numerator / denominator * (definition.scale === "per100" ? 100 : 1) : null;
// Below this many earlier windows with evidence, the range says too little.
export const minimumHistory = 4;
export type SignalVerdict = "above" | "below" | "within" | "no-history" | "no-evidence";
export type SignalRow = { definition: SignalDefinition; value: number | null; numerator: number; denominator: number; sessions: number;
  low: number | null; high: number | null; history: number; verdict: SignalVerdict };
// A value inside the trailing range, ends included, is no detectable change;
// a value outside it says only which side.
export function signalVerdict(value: number | null, history: number[]): Pick<SignalRow, "low" | "high" | "verdict"> {
  const low = history.length ? Math.min(...history) : null, high = history.length ? Math.max(...history) : null;
  if (value === null) return { low, high, verdict: "no-evidence" };
  if (history.length < minimumHistory) return { low, high, verdict: "no-history" };
  return { low, high, verdict: value > high! ? "above" : value < low! ? "below" : "within" };
}
export function signalRows(totals: DayTotals, end: string): SignalRow[] {
  const windows = Array.from({ length: trailingWindows + 1 }, (_, index) => windowTotals(totals, end, index));
  return signalDefinitions.map(definition => {
    const current = windows[0], numerator = current[definition.numerator] ?? 0, denominator = current[definition.denominator] ?? 0;
    const value = scaled(definition, numerator, denominator);
    const history = windows.slice(1).map(window => scaled(definition, window[definition.numerator] ?? 0, window[definition.denominator] ?? 0)).filter((rate): rate is number => rate !== null);
    return { definition, value, numerator, denominator, sessions: current[definition.evidence] ?? 0, history: history.length, ...signalVerdict(value, history) };
  });
}
export function formatSignal(definition: SignalDefinition, value: number | null): string {
  if (value === null) return "—";
  return definition.scale === "percent" ? `${(value * 100).toFixed(value < 0.1 ? 1 : 0)}%` : `${value.toFixed(value < 10 ? 1 : 0)} per 100 ${definition.per}`;
}

// The dashboard's filters without its dates: the signal table sets its own.
const dateFields = new Set(["started_at", "day", "week", "month"]);
export function withoutDates(where: WhereTerm[]): WhereTerm[] {
  return where.filter(term => predicatesOf(term).every(clause => !dateFields.has(clause.field)));
}
// The signal table's population: the dashboard's filters with its window,
// from local midnight of the first day to local midnight after end. The
// window bounds started_at: the service filters the text field day by
// equality only.
const localMidnight = (day: string) => new Date(`${day}T00:00:00`).toISOString();
export function signalTablePopulation(where: WhereTerm[], end: string): WhereTerm[] {
  return [...withoutDates(where), { field: "started_at", op: ">=", value: localMidnight(signalTableStart(end)) }, { field: "started_at", op: "<", value: localMidnight(shiftDay(end, 1)) }];
}
