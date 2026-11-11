// The Thinking dashboards' data: model_requests through query-v3, plus the
// slim /api/thinking for what needs whole conversations in order (the
// workload fit's estimates, factors, compaction pairs, traces and
// validation). Both answer the same population, so the page merges them
// into one ThinkingResponse by group key (testdata/thinking-group-keys.json).
import { loadSchema, metricDateFloor, metricDateNext, type AggregationClause, type FieldSchema, type MetricDistributionResult, type WhereTerm } from "@pythia-software/query-table-core";
import modelRequestDocument from "../../schemas/model_requests.schema.json";
import { todayBounds } from "./day-window";
import { postMetrics, type MetricBucket, type MetricResult } from "./metric-request";
import { liveScope } from "./result-cache";
import { supportedThinkingPeriod, thinkingRangeDays, thinkingRequest, thinkingWhere, type ThinkingCell, type ThinkingGroup, type ThinkingResponse, type ThinkingState, type ThinkingStats } from "./thinking-model";
export { maxThinkingDays, supportedThinkingPeriod, thinkingHourChoices } from "./thinking-model";

// Wait bands, as model_requests' gap_bin labels them: each includes its
// lower edge, the last its upper one too.
export const thinkingEdges = [.1, .2, .3, .5, 1, 2, 3, 5, 10, 20, 30, 50, 100, 200, 300, 500, 600];
const bands = thinkingEdges.length - 1;
export const thinkingBandLabels = thinkingEdges.slice(0, bands).map((edge, i) => `${edge}–${thinkingEdges[i + 1]} s`);
const bandIndex = new Map(thinkingBandLabels.map((label, i) => [label, i]));
// Exact-linear percentiles: p10 (low whisker), q1, median, p75 (q3), p90
// (high whisker) and p95.
const statProbabilities = [.1, .25, .5, .75, .9, .95];
const periodPercentiles = [["median", .5], ["p75", .75], ["p90", .9], ["p95", .95]] as const;
const cdfProbabilities = Array.from({ length: 101 }, (_, i) => i / 100);
// Go lists every period between the bounds; past this a coarser bucket reads better.
const maxPeriods = 5000;

let schema: FieldSchema | undefined;
const modelRequests = () => schema ??= loadSchema(modelRequestDocument);

// The model_requests fields each breakdown groups by, in key order.
export const thinkingBreakdownFields: Record<ThinkingState["groupBy"], string[]> = {
  none: [], model: ["model"], effort: ["effort"], speed: ["speed"], model_effort: ["model", "effort"], model_speed: ["model", "speed"],
  model_effort_speed: ["model", "effort", "speed"], provider: ["provider"], harness: ["harness"], project: ["project_name"], machine: ["machine"],
  preceding_tool: ["preceding_tool"], agent_depth: ["agent_depth"],
};

const text = (value: unknown, missing = "Unreported") => value === null || value === undefined || String(value).trim() === "" ? missing : String(value);
const speedLabel = (value: unknown) => String(value).toLowerCase() === "fast" ? "Fast" : "Standard";

// A group's key and label as /api/thinking's thinkingGroupKey gives them,
// from the breakdown's bucket keys.
export function thinkingGroupIdentity(by: ThinkingState["groupBy"], keys: readonly unknown[]): Pick<ThinkingGroup, "key" | "label" | "model" | "effort"> {
  const value = (field: string) => keys[thinkingBreakdownFields[by].indexOf(field)];
  const single = (label: string, model = "", effort = "") => ({ key: label, label, model, effort });
  // A missing value is one group, named; a value spelled like that name is
  // another, keyed apart with a leading NUL and shown quoted (thinkingNamed).
  const named = (raw: unknown, missing = "Unreported") => {
    if (raw === null || raw === undefined || String(raw).trim() === "") return single(missing);
    const s = String(raw);
    return s === missing || s.startsWith("\u0000") ? { key: "\u0000" + s, label: `“${s}”`, model: "", effort: "" } : single(s);
  };
  switch (by) {
    case "none": return single("All requests");
    case "model": return single(text(value("model")), text(value("model")));
    case "effort": return single(text(value("effort")), "", text(value("effort")));
    case "speed": return { key: speedLabel(value("speed")).toLowerCase(), label: speedLabel(value("speed")), model: "", effort: "" };
    case "model_effort": case "model_speed": case "model_effort_speed": {
      const model = text(value("model")), effort = by === "model_speed" ? "" : text(value("effort"));
      const parts = [model, ...by === "model_speed" ? [] : [effort], ...by === "model_effort" ? [] : [speedLabel(value("speed"))]];
      return { key: JSON.stringify(parts), label: parts.join(" · "), model, effort };
    }
    case "project": return named(value("project_name"), "No project");
    case "agent_depth": return single(String(value("agent_depth") ?? 0));
    case "preceding_tool": return single(String(value("preceding_tool") ?? ""));
    default: return named(value(thinkingBreakdownFields[by][0]));
  }
}

type Bucket = MetricBucket & { distribution?: MetricDistributionResult };
const quantiles = (bucket: Bucket | undefined) => bucket?.distribution?.kind === "quantiles" ? bucket.distribution.summary : null;
const number = (value: unknown) => typeof value === "number" && Number.isFinite(value) ? value : 0;
export const emptyThinkingStats = (): ThinkingStats => ({ n: 0, median: null, p75: null, p90: null, p95: null, q1: null, q3: null, low: null, high: null });
function statsOf(bucket: Bucket | undefined): ThinkingStats {
  const summary = quantiles(bucket);
  if (!summary?.n) return emptyThinkingStats();
  const [low, q1, median, p75, high, p95] = summary.values;
  return { n: summary.n, median, p75, p90: high, p95, q1, q3: p75, low, high };
}

// Go's RFC3339Nano in UTC: no trailing zero fraction.
export const thinkingStamp = (ms: number | undefined) => ms === undefined ? "" : new Date(ms).toISOString().replace(/\.(\d*?)0*Z$/, (_, digits: string) => digits ? `.${digits}Z` : "Z");

// A period's key as Go wrote it: an RFC3339 local time for hour buckets, the
// local date for day and week buckets, the local month for months.
const formatters = new Map<string, Intl.DateTimeFormat>();
function localParts(ms: number, zone: string) {
  let format = formatters.get(zone);
  if (!format) formatters.set(zone, format = new Intl.DateTimeFormat("en-US", { timeZone: zone, hourCycle: "h23", year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit" }));
  const parts = Object.fromEntries(format.formatToParts(new Date(ms)).map(part => [part.type, part.value]));
  return { year: parts.year, month: parts.month, day: parts.day, hour: parts.hour, minute: parts.minute, second: parts.second };
}
export function thinkingPeriodKey(ms: number, period: ThinkingState["period"] | ThinkingState["calendarRows"], zone: string): string {
  const p = localParts(ms, zone);
  if (period === "month") return `${p.year}-${p.month}`;
  if (period !== "hour" && period !== "hours") return `${p.year}-${p.month}-${p.day}`;
  const offset = Math.round((Date.UTC(+p.year, +p.month - 1, +p.day, +p.hour, +p.minute, +p.second) - Math.floor(ms / 1000) * 1000) / 60000);
  const sign = offset < 0 ? "-" : "+", abs = Math.abs(offset);
  return `${p.year}-${p.month}-${p.day}T${p.hour}:${p.minute}:${p.second}${offset ? `${sign}${String(Math.floor(abs / 60)).padStart(2, "0")}:${String(abs % 60).padStart(2, "0")}` : "Z"}`;
}

type Interval = { interval: "hour" | "day" | "week" | "month"; every?: number };
export function thinkingInterval(state: Pick<ThinkingState, "period" | "hours" | "days">): Interval {
  const { hours, days } = supportedThinkingPeriod(state);
  switch (state.period) {
    case "hour": return { interval: "hour" };
    case "hours": return hours > 1 ? { interval: "hour", every: hours } : { interval: "hour" };
    case "days": return days > 1 ? { interval: "day", every: days } : { interval: "day" };
    case "week": return { interval: "week" };
    case "month": return { interval: "month" };
    default: return { interval: "day" };
  }
}

// The Range's instants, as /api/thinking read them: presets end now.
export function thinkingBounds(state: ThinkingState, now: number): { from?: number; to?: number } {
  if (state.range === "custom") return { from: state.from ? Date.parse(state.from) : undefined, to: state.to ? Date.parse(state.to) : undefined };
  if (state.range === "today") return { from: Date.parse(todayBounds(new Date(now), state.timezone || undefined)[0]), to: now };
  const days = Object.entries(thinkingRangeDays).find(([, range]) => range === state.range)?.[0];
  return days ? { from: now - Number(days) * 86_400_000, to: now } : {};
}

// Every period from the Range's start (or the first request) through its
// end (or the last request), with bounds clipped to the Range.
export function thinkingPeriods(interval: Interval, zone: string, range: { from?: number; to?: number }, observed: { first?: number; last?: number }, key: (ms: number) => string) {
  const keys: string[] = [], bounds: Record<string, { From: string; To: string }> = {};
  const start = range.from ?? observed.first, end = range.to !== undefined ? range.to - 1 : observed.last;
  if (observed.first === undefined || start === undefined || end === undefined) return { keys, bounds };
  for (let t = metricDateFloor(start, interval.interval, zone, interval.every); t <= end; t = metricDateNext(t, interval.interval, zone, interval.every)) {
    if (keys.length >= maxPeriods) throw new Error("more than 5,000 time buckets; choose a coarser Time Bucket or shorter Range");
    const name = key(t), next = metricDateNext(t, interval.interval, zone, interval.every);
    keys.push(name);
    bounds[name] = { From: thinkingStamp(range.from !== undefined && t < range.from ? range.from : t), To: thinkingStamp(range.to !== undefined && next > range.to ? range.to : next) };
  }
  return { keys, bounds };
}

const instant = (value: unknown) => typeof value === "number" ? value : typeof value === "string" ? Date.parse(value) : NaN;

// thinkingClauses are the query-v3 metrics for one dashboard state.
export function thinkingClauses(state: ThinkingState, overview: boolean): AggregationClause[] {
  const by = thinkingBreakdownFields[state.groupBy], reasoning = state.view === "reasoning";
  const timed: WhereTerm[] = [{ field: "gap_seconds", op: "is_not_null", value: "" }];
  const valued: WhereTerm[] = reasoning ? [{ field: "reasoning_output_tokens", op: ">", value: "0" }] : timed;
  const input = reasoning ? "[reasoning_output_tokens]" : "[gap_seconds]";
  const stats = (id: string, groupBy: string[], probabilities = statProbabilities, value = "[gap_seconds]", where?: WhereTerm[]): AggregationClause =>
    ({ id, op: "count", groupBy, ...where ? { where } : {}, distribution: { kind: "quantiles", input: value, probabilities } });
  const conversations = (id: string, groupBy: string[], where?: WhereTerm[]): AggregationClause => ({ id, op: "count_distinct", field: "conversation_id", groupBy, ...where ? { where } : {} });
  const count = (id: string, groupBy: string[], where?: WhereTerm[]): AggregationClause => ({ id, op: "count", groupBy, ...where ? { where } : {} });
  const clauses: AggregationClause[] = [
    count("retained", []), conversations("conversations", []), stats("summary", []),
    count("group_retained", by), conversations("group_retained_conversations", by), stats("group_stats", by, statProbabilities, input, reasoning ? valued : undefined),
    count("group_timed", by, timed), conversations("group_conversations", by, valued), count("group_bands", [...by, "gap_bin"], [...valued, ...reasoning ? timed : []]),
  ];
  if (!overview && (state.view === "time" || state.view === "calendar"))
    clauses.push({ id: "first", op: "min", field: "requested_at", groupBy: [] }, { id: "last", op: "max", field: "requested_at", groupBy: [] });
  if (!overview && state.view === "time") {
    const dateGrouping = { field: "requested_at", ...thinkingInterval(state), timeZone: state.timezone, includeEmpty: false, range: "observed" as const };
    // Date grouping takes scalar metrics, so each line the chart draws is
    // its own percentile.
    const periods = ["requested_at", ...by];
    clauses.push({ ...count("period_retained", periods), dateGrouping }, { ...count("period_timed", periods, timed), dateGrouping },
      ...periodPercentiles.map(([key, p]): AggregationClause => ({ id: `period_${key}`, op: "count", expression: `PERCENTILE([gap_seconds],${p})`, groupBy: periods, dateGrouping })),
      { ...conversations("period_conversations", periods, timed), dateGrouping }, { ...count("period_bands", [...periods, "gap_bin"], timed), dateGrouping });
  }
  if (overview) {
    clauses.push(stats("group_cdf", by, cdfProbabilities));
    for (const field of ["model", "harness"]) clauses.push(count(`${field}_retained`, [field]), stats(`${field}_stats`, [field]), conversations(`${field}_conversations`, [field], timed));
  }
  return clauses;
}

// thinkingFromMetrics shapes query-v3 results as /api/thinking answered them.
export function thinkingFromMetrics(state: ThinkingState, overview: boolean, results: MetricResult[], options: MetricResult[], now: number): ThinkingResponse {
  const byId = new Map(results.map(result => [result.id, result.buckets as Bucket[]]));
  const buckets = (id: string) => byId.get(id) ?? [];
  const by = thinkingBreakdownFields[state.groupBy], width = by.length, zone = state.timezone;
  const identity = (keys: readonly unknown[]) => thinkingGroupIdentity(state.groupBy, keys);
  const groups = new Map<string, ThinkingGroup>();
  const group = (keys: readonly unknown[]) => {
    const id = identity(keys.slice(0, width));
    let g = groups.get(id.key);
    if (!g) groups.set(id.key, g = { ...id, Retained: 0, Timed: 0, Adjusted: 0, Conversations: 0, RetainedConversations: 0, stats: emptyThinkingStats(),
      counts: Array(bands).fill(0), periods: {}, calendar: [], bands: [], comparison: { days: 0, Ratio: null, Low: null, High: null } });
    return g;
  };
  for (const b of buckets("group_retained")) group(b.keys).Retained = number(b.value);
  for (const b of buckets("group_retained_conversations")) group(b.keys).RetainedConversations = number(b.value);
  for (const b of buckets("group_stats")) group(b.keys).stats = statsOf(b);
  for (const b of buckets("group_timed")) group(b.keys).Timed = number(b.value);
  for (const b of buckets("group_conversations")) group(b.keys).Conversations = number(b.value);
  for (const b of buckets("group_bands")) { const band = bandIndex.get(String(b.keys[width])); if (band !== undefined) group(b.keys).counts[band] += number(b.value); }
  for (const b of buckets("group_cdf")) {
    const summary = quantiles(b);
    if (summary?.n) group(b.keys).cdf = summary.values.map((value, i) => [value, summary.probabilities[i]]);
  }
  const first = instant(buckets("first")[0]?.value), last = instant(buckets("last")[0]?.value);
  const observed = { first: Number.isFinite(first) ? first : undefined, last: Number.isFinite(last) ? last : undefined };
  const range = thinkingBounds(state, now);
  let periods = { keys: [] as string[], bounds: {} as Record<string, { From: string; To: string }> }, calendar = periods;
  if (!overview && state.view === "time") {
    periods = thinkingPeriods(thinkingInterval(state), zone, range, observed, ms => thinkingPeriodKey(ms, state.period, zone));
    // A request without a timestamp is in its group's totals but in no
    // period: its date bucket has no key.
    const cell = (keys: readonly unknown[]): ThinkingCell => {
      const at = instant(keys[0]);
      if (!Number.isFinite(at)) return { stats: emptyThinkingStats(), retained: 0, conversations: 0, counts: Array(bands).fill(0) };
      const g = group(keys.slice(1)), name = thinkingPeriodKey(at, state.period, zone);
      return g.periods[name] ??= { stats: emptyThinkingStats(), retained: 0, conversations: 0, counts: Array(bands).fill(0) };
    };
    for (const b of buckets("period_retained")) cell(b.keys).retained = number(b.value);
    for (const b of buckets("period_timed")) cell(b.keys).stats.n = number(b.value);
    for (const [key] of periodPercentiles) for (const b of buckets(`period_${key}`)) {
      const stats = cell(b.keys).stats, value = typeof b.value === "number" && Number.isFinite(b.value) ? b.value : null;
      stats[key] = value;
      if (key === "p75") stats.q3 = value;
      if (key === "p90") stats.high = value;
    }
    for (const b of buckets("period_conversations")) cell(b.keys).conversations = number(b.value);
    for (const b of buckets("period_bands")) { const band = bandIndex.get(String(b.keys[width + 1])); if (band !== undefined) cell(b.keys).counts[band] += number(b.value); }
  }
  if (!overview && state.view === "calendar") {
    // The cells themselves come from the fit (withThinkingFit).
    calendar = thinkingPeriods({ interval: state.calendarRows }, zone, range, observed, ms => thinkingPeriodKey(ms, state.calendarRows, zone));
  }
  const coverage = (field: "model" | "harness") => {
    const rows = new Map<string, ThinkingGroup>();
    const row = (key: unknown) => { const id = thinkingGroupIdentity(field, [key]); let g = rows.get(id.key);
      if (!g) rows.set(id.key, g = { ...id, Retained: 0, Timed: 0, Adjusted: 0, Conversations: 0, RetainedConversations: 0, stats: emptyThinkingStats(), counts: Array(bands).fill(0), periods: {}, calendar: [], bands: [], comparison: { days: 0, Ratio: null, Low: null, High: null } });
      return g; };
    for (const b of buckets(`${field}_retained`)) row(b.keys[0]).Retained = number(b.value);
    for (const b of buckets(`${field}_stats`)) { const g = row(b.keys[0]); g.stats = statsOf(b); g.Timed = g.stats.n; }
    for (const b of buckets(`${field}_conversations`)) row(b.keys[0]).Conversations = number(b.value);
    return sortThinkingGroups([...rows.values()], state);
  };
  const summary = statsOf(buckets("summary")[0]);
  return {
    periodBounds: periods.bounds, calendarBounds: calendar.bounds, periods: periods.keys, calendarPeriods: calendar.keys,
    Retained: number(buckets("retained")[0]?.value), Timed: summary.n, Adjusted: 0, Conversations: number(buckets("conversations")[0]?.value), Hidden: 0,
    Models: thinkingOptions(options[0], false), Efforts: thinkingOptions(options[1], true), edges: thinkingEdges,
    groups: sortThinkingGroups([...groups.values()], state), summary,
    From: thinkingStamp(range.from), To: thinkingStamp(range.to), timezone: zone,
    ...overview ? { coverage: coverage("model"), agentCoverage: coverage("harness") } : {},
  };
  function thinkingOptions(result: MetricResult | undefined, effort: boolean) {
    const names = (result?.buckets ?? []).map(b => text(b.keys[0]));
    return [...new Set(names)].sort(effort ? (a, b) => effortOrder(a) - effortOrder(b) || (a < b ? -1 : a > b ? 1 : 0) : undefined);
  }
}

const efforts = ["none", "minimal", "low", "medium", "high", "xhigh", "ultra", "max"];
export const effortOrder = (effort: string) => effort === "Unreported" ? 100 : (i => i < 0 ? 99 : i)(efforts.indexOf(effort.toLowerCase()));

// Groups in the Sort control's order, missing values last, ties by label;
// effort breakdowns keep model, then effort order.
export function sortThinkingGroups(groups: ThinkingGroup[], state: Pick<ThinkingState, "groupBy" | "sortBy" | "sortDirection">): ThinkingGroup[] {
  const label = (a: ThinkingGroup, b: ThinkingGroup) => a.label < b.label ? -1 : a.label > b.label ? 1 : 0;
  return [...groups].sort((a, b) => {
    if (state.groupBy === "effort" || state.groupBy === "model_effort")
      return (a.model < b.model ? -1 : a.model > b.model ? 1 : 0) || effortOrder(a.effort) - effortOrder(b.effort) || label(a, b);
    const value = (g: ThinkingGroup) => state.sortBy === "volume" ? g.stats.n : state.sortBy === "p95" ? g.stats.p95 : g.stats.median;
    const av = value(a), bv = value(b);
    if (av === null || bv === null) return av === null && bv === null ? label(a, b) : av === null ? 1 : -1;
    return av === bv ? label(a, b) : state.sortDirection === "asc" ? av - bv : bv - av;
  });
}

// The slim endpoint's answer: fitted estimates by group, the time-of-day
// cells (a query-v3 metric takes one calendar key, so the zoned day and hour
// cells are read with the fit), and the overview's and evidence's sections.
export type ThinkingFit = Pick<ThinkingResponse, "Adjusted" | "timezone" | "factors" | "compactions" | "traces" | "validation"> & {
  groups: (Pick<ThinkingGroup, "key" | "Adjusted" | "bands" | "comparison" | "calendar"> & { stats?: ThinkingStats; Conversations?: number })[];
};

export async function loadThinkingFit(url: string, signal?: AbortSignal): Promise<ThinkingFit> {
  const response = await fetch(url, { signal }), body = await response.json();
  if (!response.ok) throw new Error(body.error ?? "Unable to load request timing.");
  return body;
}

// withThinkingFit adds the fitted estimates, factors, compactions and traces.
export function withThinkingFit(data: ThinkingResponse, fit: ThinkingFit, state: ThinkingState): ThinkingResponse {
  const fitted = new Map(fit.groups.map(group => [group.key, group]));
  const groups = data.groups.map(group => {
    const g = fitted.get(group.key);
    if (!g) return group;
    return { ...group, Adjusted: g.Adjusted, bands: g.bands, comparison: g.comparison, calendar: g.calendar,
      ...g.stats ? { stats: g.stats } : {}, ...g.Conversations !== undefined ? { Conversations: g.Conversations } : {} };
  });
  return { ...data, Adjusted: fit.Adjusted, groups: state.view === "calendar" && state.calendarMetric === "ratio" ? sortThinkingGroups(groups, state) : groups,
    ...fit.factors ? { factors: fit.factors } : {}, ...fit.compactions ? { compactions: fit.compactions } : {}, ...fit.traces ? { traces: fit.traces } : {}, ...fit.validation ? { validation: fit.validation } : {} };
}

// The state a page or card reads: the Waiting overview shows histograms by
// model unless grouped otherwise.
export const thinkingQueryState = (state: ThinkingState, overview: boolean): ThinkingState =>
  supportedThinkingPeriod(overview ? { ...state, view: "histogram", groupBy: state.groupBy === "none" ? "model" : state.groupBy, minimum: 0 } : state);

// loadThinkingData reads one dashboard state: query-v3 for the population,
// groups, periods and curves, and /api/thinking only for what needs the fit
// (time of day, the Waiting overview). The fit is computed now, so a view
// that joins it reads query-v3 live too, both ending at the same instant:
// a stored answer could describe other requests than the fit does.
export async function loadThinkingData(input: { state: ThinkingState; overview: boolean; where: WhereTerm[]; filters: string; now?: number }, signal?: AbortSignal): Promise<ThinkingResponse> {
  const now = input.now ?? Date.now(), state = thinkingQueryState(input.state, input.overview), schema = modelRequests();
  const where = [...input.where, ...thinkingWhere(state, now)], params = { timezone: state.timezone };
  const needsFit = state.view === "calendar" || input.overview;
  const [results, options, fit] = await Promise.all([
    needsFit ? postMetrics("model_requests", schema, where, thinkingClauses(state, input.overview), liveScope(signal), params, now)
      : postMetrics("model_requests", schema, where, thinkingClauses(state, input.overview), signal, params),
    postMetrics("model_requests", schema, [], [{ id: "models", op: "count", groupBy: ["model"] }, { id: "efforts", op: "count", groupBy: ["effort"] }], signal),
    needsFit ? loadThinkingFit(`${thinkingRequest(input.state, input.overview, false, input.filters)}&now=${now}`, signal) : undefined,
  ]);
  const data = thinkingFromMetrics(state, input.overview, results, options, now);
  return fit ? withThinkingFit(data, fit, state) : data;
}
