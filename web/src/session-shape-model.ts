import { predicatesOf, type AggregationClause, type WhereTerm } from "@pythia-software/query-table-core";

// The Session Shape dashboard's own panels, without a DOM: percentiles by
// rank, a log-spaced histogram with its p50 and p90 marked, the archetype mix
// of each repository, and the signal table, which compares the last seven
// days with the eight weeks before them (docs/session-shape.md).

// The nearest-rank percentile's position in n ascending values: the value at
// ceil(p·n), counted from one. The dataset's own percentiles use this rule
// (nearestRank in session_shape.go).
export function nearestRankOffset(n: number, p: number): number | null {
  if (!Number.isInteger(n) || n < 1) return null;
  return Math.min(n, Math.max(1, Math.ceil(p * n))) - 1;
}
// Nearest-rank percentiles read as rows at their ranks, one request each.
// Sessions can be added or removed between requests, so every read must
// report the count the ranks came from; a changed count reads them again. A
// rank that returns no value is unavailable, never a crash.
export type RankRead = (offset: number) => Promise<{ value: number | null; total: number }>;
export async function readPercentiles(read: RankRead, ps: readonly number[], attempts = 3): Promise<{ n: number; values: Array<number | null> }> {
  for (let attempt = 0; attempt < attempts; attempt++) {
    const { total } = await read(0);
    const reads = await Promise.all(ps.map(p => { const offset = nearestRankOffset(total, p); return offset === null ? null : read(offset); }));
    if (reads.every(item => item === null || item.total === total)) return { n: total, values: reads.map(item => item?.value ?? null) };
  }
  throw new Error("the sessions changed while they were read; Refresh to try again");
}

// A duration in seconds, as people say it: 42 s, 12 min, 1 h 20 min.
export function formatSeconds(value: number): string {
  if (!Number.isFinite(value)) return "—";
  if (value < 60) return `${Math.round(value)} s`;
  if (value < 3600) return `${Math.round(value / 60)} min`;
  const hours = Math.floor(value / 3600), minutes = Math.round((value % 3600) / 60);
  return minutes ? `${hours} h ${minutes} min` : `${hours} h`;
}

// Durations in seconds spread over orders of magnitude, so the histogram's
// bins are log-spaced; the last is open.
export const durationEdges = [0, 30, 60, 120, 300, 600, 900, 1800, 3600, 7200, 14400, 28800] as const;
const durationText = (seconds: number) => seconds < 60 ? `${seconds} s` : seconds < 3600 ? `${seconds / 60} min` : `${seconds / 3600} h`;
// The axis names each bin by its lower edge; the tooltip gives the whole range.
export function binEdgeLabel(edges: readonly number[], index: number): string {
  return index === edges.length - 1 ? `${durationText(edges[index])}+` : durationText(edges[index]);
}
export function binLabel(edges: readonly number[], index: number): string {
  return index === edges.length - 1 ? `${durationText(edges[index])}+` : `${durationText(edges[index])}–${durationText(edges[index + 1])}`;
}
// One summed condition per bin, so a single request counts every bin.
export function binClauses(field: string, edges: readonly number[]): AggregationClause[] {
  return edges.map((low, index) => {
    const high = edges[index + 1];
    const condition = high === undefined ? `[${field}]>=${low}` : `[${field}]>=${low} AND [${field}]<${high}`;
    return { id: `bin_${index}`, op: "sum", expression: `SUM(IF(${condition},1,0))`, groupBy: [] };
  });
}
// Where a value falls along the bins, from 0 at the first edge to the bin
// count at the end of the last, interpolated in log space inside a bin (the
// first bin linearly, since it starts at zero). The open bin ends at twice
// its start.
export function binPosition(edges: readonly number[], value: number): number {
  if (!(value > 0)) return 0;
  for (let index = 0; index < edges.length; index++) {
    const low = edges[index], high = edges[index + 1] ?? low * 2;
    if (value < high || index === edges.length - 1) {
      const fraction = low === 0 ? value / high : Math.log(value / low) / Math.log(high / low);
      return index + Math.min(1, Math.max(0, fraction));
    }
  }
  return edges.length;
}

// Each repository's sessions by archetype, largest repositories first, with
// the rest folded into Other so every session counts once. A breakdown by
// repository and archetype together has too many groups to read whole, so
// the mix is read exactly in two steps: sessions by archetype and the largest
// repositories (mixFirstClauses), then each of those repositories by
// archetype (mixRepositoryClauses). Other is the archetype totals less the
// repositories shown.
export type MixRow = { key: string; total: number; counts: Record<string, number> };
export const mixOther = "\u0000other", mixRepositories = 10;
const bucketKey = (value: unknown) => value == null || value === "" ? "" : String(value);
export function mixFirstClauses(limit = mixRepositories): AggregationClause[] {
  return [{ id: "archetypes", op: "count", groupBy: ["archetype"] },
    { id: "repositories", op: "count", groupBy: ["repository_name"], sort: [{ key: "value", dir: "desc" }, { key: "group0", dir: "asc" }], groupLimit: limit }];
}
// The repositories shown: all of them when they fit, otherwise one fewer, so
// the last row can be Other.
// More repositories exist when the result counted groups beyond those
// returned or reports an omitted remainder.
export function mixShownRepositories(result: { buckets: Array<{ keys: unknown[]; value: unknown }>; groupCount?: number; other?: { value: unknown } }, limit = mixRepositories): { repositories: string[]; folded: boolean } {
  const keys = result.buckets.filter(bucket => Number(bucket.value) > 0).map(bucket => bucketKey(bucket.keys[0]));
  const folded = (result.groupCount ?? 0) > result.buckets.length || Number(result.other?.value) > 0 || keys.length > limit;
  return { repositories: folded ? keys.slice(0, limit - 1) : keys, folded };
}
export function mixRepositoryClauses(repositories: string[]): AggregationClause[] {
  return repositories.map((repository, index) => ({ id: `repository_${index}`, op: "count", groupBy: ["archetype"],
    where: [repository ? { field: "repository_name", op: "=", value: repository } : { field: "repository_name", op: "is_null", value: "" }] }));
}
export function archetypeMix(totals: Array<{ keys: unknown[]; value: unknown }>, repositories: string[], perRepository: Array<Array<{ keys: unknown[]; value: unknown }>>, folded: boolean): MixRow[] {
  const counts = (buckets: Array<{ keys: unknown[]; value: unknown }>) => {
    const result: Record<string, number> = {};
    for (const bucket of buckets) { const archetype = bucketKey(bucket.keys[0]); result[archetype] = (result[archetype] ?? 0) + (Number(bucket.value) || 0); }
    return result;
  };
  const sum = (record: Record<string, number>) => Object.values(record).reduce((total, value) => total + value, 0);
  const rows = repositories.map((key, index) => { const row = counts(perRepository[index] ?? []); return { key, total: sum(row), counts: row }; }).filter(row => row.total > 0);
  const other = counts(totals);
  for (const row of rows) for (const [archetype, value] of Object.entries(row.counts)) other[archetype] = (other[archetype] ?? 0) - value;
  for (const [archetype, value] of Object.entries(other)) if (value <= 0) delete other[archetype];
  const sorted = rows.sort((a, b) => b.total - a.total || a.key.localeCompare(b.key));
  return folded && sum(other) > 0 ? [...sorted, { key: mixOther, total: sum(other), counts: other }] : sorted;
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
