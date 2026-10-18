import { EMPTY_QUERY, encodeQuery, type AggregationClause, type WhereTerm } from "@pythia-software/query-table-core";
import type { CardLoadingKind } from "./card-loading-model";
import { chartSeriesStyle, otherSeriesColor } from "./chart-colors";
import type { ChartBucket, ChartSeries } from "./chart-series";
import { clipIntervals, concurrencyMetrics, concurrencySpans, workSessions, zonedDayBounds, type ConcurrencyData, type Interval } from "./concurrency-data";
import { readConcurrencySettings } from "./concurrency-settings";
import { activityPresentation, machineDay, machineWhere, machineWindow, relativeChange, shiftMachineDay, timeAggregationWhere, timeLabel, type Metric } from "./machine-activity-data";
import type { DraftCoverData, DraftRow } from "./draft-cover";
import type { QueryCategory } from "./query-categories";
import type { QueryPreview } from "./query-previews";
import type { HumanStudyPreview } from "./human-study";

// A draft card answers one question from the same API as the page it opens.
// Loaders share one context per review execution so cards reading the same
// endpoint make one request with one fixed "now".
export type DraftCard = {
  id: string; category: QueryCategory; window?: "current" | "snapshot"; windowLabel?: string;
  title: (days: number) => string; description: (days: number) => string; action: string; url: (days: number) => string;
  loading: CardLoadingKind; loadingCount?: number; height?: number;
  load: (context: DraftContext) => Promise<QueryPreview>;
};
export type DraftContext = { days: number; now: number; signal: AbortSignal; json: <T>(url: string, body?: unknown) => Promise<T> };
export const day = 86_400_000;

export function draftContext(days: number, signal: AbortSignal, now = Date.now()): DraftContext {
  const requests = new Map<string, Promise<unknown>>();
  const json = <T,>(url: string, body?: unknown): Promise<T> => {
    const key = `${url}\n${body === undefined ? "" : JSON.stringify(body)}`;
    if (!requests.has(key)) requests.set(key, (async () => {
      const response = await fetch(url, { signal, ...(body === undefined ? {} : { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) }) });
      const text = await response.text();
      let parsed: any;
      try { parsed = JSON.parse(text); } catch { throw new Error(response.ok ? `${url} returned an unreadable response.` : `${url}: ${response.status} ${response.statusText}`); }
      if (!response.ok) throw new Error(typeof parsed?.error === "string" ? parsed.error : parsed?.error?.message ?? `${url}: ${response.status}`);
      return parsed;
    })());
    return requests.get(key) as Promise<T>;
  };
  return { days, now, signal, json };
}

export const preview = (draft: DraftCoverData, extra: Partial<QueryPreview> = {}): QueryPreview => ({ state: "ready", total: 1, summary: "", draft, ...extra });
export const empty = (label: string, extra: Partial<QueryPreview> = {}): QueryPreview => ({ state: "empty", total: 0, summary: "", emptyLabel: label, ...extra });
export const sig = (value: number, digits = 3) => Number(value.toPrecision(digits)).toLocaleString();
export const percent = (value: number) => value.toLocaleString(undefined, { style: "percent", maximumFractionDigits: value < .1 ? 1 : 0 });
export const hours = (ms: number) => ms >= 36e5 * 10 ? `${sig(ms / 36e5, 3)} h` : ms >= 36e5 ? `${(ms / 36e5).toFixed(1)} h` : ms >= 6e4 ? `${Math.round(ms / 6e4)} min` : `${Math.round(ms / 1000)} s`;
export const count = (value: number, noun: string, plural = `${noun}s`) => `${value.toLocaleString()} ${value === 1 ? noun : plural}`;

// Keep the strongest groups and fold every remaining value into an exact Other.
export function foldRows(rows: DraftRow[], limit: number, other = (value: number) => ({ display: String(value) })): DraftRow[] {
  const sorted = [...rows].sort((a, b) => b.value - a.value), kept = sorted.slice(0, limit), rest = sorted.slice(limit);
  const remainder = rest.reduce((sum, row) => sum + row.value, 0);
  return remainder > 0 ? [...kept, { label: `Other (${rest.length})`, value: remainder, color: otherSeriesColor, ...other(remainder) }] : kept;
}
export const seriesColor = (scope: string, name: string) => chartSeriesStyle(scope, name).color;

// ----- Carbon ---------------------------------------------------------------

type AggregationResponse = { metrics: Metric[] };
type TokenCounts = Record<string, Record<string, number>>;
type CarbonFactors = { tiers: Array<{ key: string; label: string; match?: string[] }>; default_tier?: string; equivalents?: Array<{ key: string; label: string; g_co2e_per_unit: number }> };
type CarbonResponse = { factors: CarbonFactors; windows: Array<{ tokens: TokenCounts }>; series: Array<{ day: string; value: string; tokens: TokenCounts }>; undated_tokens: number; unclassified_tokens?: number };
type CarbonResult = { pue: number; co2: number; facilityWh: number; byCategory: Record<string, { wh: number }>; grams: (wh: number) => number };
type Calculator = { estimate: (factors: CarbonFactors, period: { tokens: TokenCounts }) => CarbonResult; mass: (grams: number) => string; energy: (wh: number) => string };

// The Carbon page's calculator applies the user's tier, PUE, and grid choices.
// carbon.js loads with `defer`; wait for it rather than duplicating the math.
async function calculator(signal: AbortSignal): Promise<Calculator> {
  const ready = () => (window as unknown as { pharosCarbon?: Calculator }).pharosCarbon;
  if (ready()) return ready()!;
  await new Promise<void>((resolve, reject) => {
    const done = () => { clearTimeout(timer); window.removeEventListener("pharos:carbon-ready", done); resolve(); };
    const timer = setTimeout(() => { window.removeEventListener("pharos:carbon-ready", done); reject(new Error("The carbon calculator did not load.")); }, 10_000);
    window.addEventListener("pharos:carbon-ready", done); signal.addEventListener("abort", done);
  });
  if (!ready()) throw new Error("The carbon calculator did not load.");
  return ready()!;
}
const localDay = (time: number) => { const date = new Date(time); return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, "0")}-${String(date.getDate()).padStart(2, "0")}`; };
// Local midnight `days` calendar days from `time`. Days are 23 or 25 hours long
// across daylight-saving changes, so never step by 24-hour multiples.
const localMidnight = (time: number, days = 0) => { const date = new Date(time); date.setHours(0, 0, 0, 0); date.setDate(date.getDate() + days); return date.getTime(); };
const stamp = (time: number) => { const date = new Date(time), offset = -date.getTimezoneOffset(), pad = (n: number) => String(Math.abs(n)).padStart(2, "0"); return `${localDay(time)}T${pad(date.getHours())}:${pad(date.getMinutes())}:00${offset < 0 ? "-" : "+"}${pad(Math.floor(Math.abs(offset) / 60))}:${pad(Math.abs(offset) % 60)}`; };
export const usageSince = (from: number): WhereTerm[] => [{ field: "last_usage_at", op: ">=", value: stamp(from) }];
const addTokens = (target: TokenCounts, source: TokenCounts) => { for (const [tier, counts] of Object.entries(source)) for (const [category, value] of Object.entries(counts)) (target[tier] ??= {})[category] = (target[tier][category] ?? 0) + value; return target; };
const tokenTotal = (tokens: TokenCounts) => Object.values(tokens).reduce((sum, counts) => sum + Object.values(counts).reduce((n, value) => n + value, 0), 0);

async function carbon(context: DraftContext, split: "" | "tier" | "repository_name" | "provider", from: number) {
  const [calc, data] = await Promise.all([calculator(context.signal), context.json<CarbonResponse>("/api/query/usage/carbon", { where: usageSince(from), hourly: false, split })]);
  return { calc, data };
}
// The Carbon page reads its filters from q_carbon, an encoded Query Table state.
const carbonURL = (days: number) => `/usage?usage=carbon&q_carbon=${encodeURIComponent(encodeQuery({ ...EMPTY_QUERY, where: usageSince(localMidnight(Date.now(), 1 - days)) }))}`;
// Daily carbon series are keyed by local day; every card uses the same local
// calendar period (today and the N − 1 days before) so their totals agree.
const carbonFrom = (context: DraftContext) => localMidnight(context.now, 1 - context.days);
const carbonNote = "Uses your Carbon Impact settings (estimate range, facility overhead, grid). Token counts are reported; energy and CO₂e are modeled estimates.";

// The Carbon endpoint splits only by provider, repository and tier. Model and
// effort breakdowns sum the same usage facts' token categories from Usage
// aggregations, place each model family in a tier exactly as carbon.go does,
// and apply the same calculator. Daily usage rows filtered from local midnight
// select the same facts as the Carbon endpoint's hour-precision filter.
export function carbonTier(factors: CarbonFactors, family: string): string {
  for (const tier of factors.tiers) for (const fragment of tier.match ?? []) if (fragment && family.includes(fragment.toLowerCase())) return tier.key;
  return factors.default_tier ?? "";
}
const carbonCategories: Record<string, string[]> = { uncached_input: ["uncached_input_tokens", "unclassified_tokens"], cache_write: ["cache_creation_input_tokens"], cache_read: ["cache_read_input_tokens"], output: ["output_tokens"] };
async function carbonBy(context: DraftContext, field: "model_family" | "provider" | "effort") {
  const from = carbonFrom(context), { calc, data } = await carbon(context, "", from);
  const fields = Object.values(carbonCategories).flat(), groupBy = field === "model_family" ? ["model_family"] : ["model_family", field];
  const response = await context.json<AggregationResponse>("/api/query/usage/aggregations", { where: usageSince(from), aggregations: [...fields.map(name => ({ id: name, op: "sum", field: name, groupBy })), { id: "population", op: "count", groupBy: [] }], requireComplete: true });
  const population = Number(response.metrics.find(metric => metric.id === "population")?.buckets[0]?.value ?? 0), groups = new Map<string, TokenCounts>();
  for (const name of fields) {
    const buckets = response.metrics.find(metric => metric.id === name)?.buckets ?? [];
    if (buckets.reduce((sum, bucket) => sum + (bucket.count ?? 0), 0) !== population) throw new Error("Usage groups are incomplete. Choose a shorter preview window.");
    const category = Object.keys(carbonCategories).find(key => carbonCategories[key].includes(name))!;
    for (const bucket of buckets) {
      const family = String(bucket.keys[0] ?? ""), key = String(bucket.keys[groupBy.length - 1] ?? ""), tier = carbonTier(data.factors, family);
      const tokens = groups.get(key) ?? groups.set(key, {}).get(key)!;
      (tokens[tier] ??= {})[category] = (tokens[tier][category] ?? 0) + (Number(bucket.value) || 0);
    }
  }
  return { calc, data, rows: [...groups].map(([key, tokens]) => ({ key, tokens, grams: calc.estimate(data.factors, { tokens }).co2 })).filter(row => row.grams > 0) };
}
const providerNames: Record<string, string> = { claude: "Claude", codex: "Codex", chatgpt: "ChatGPT", antigravity: "Antigravity", canonical: "Canonical", tl1: "TL1" };
const tierOf = (factors: CarbonFactors, tokens: TokenCounts) => Object.keys(tokens).map(key => factors.tiers.find(tier => tier.key === key)?.label ?? key).join(", ");

export const carbonDrafts: DraftCard[] = [
  { id: "draft-carbon-trend", category: "carbon", loading: "line", loadingCount: 31, height: 260,
    title: () => "Estimated CO₂e", description: days => `Modeled emissions from recorded tokens over each trailing ${days}-day period, across the last 30 days.`,
    action: "Explore carbon impact", url: carbonURL,
    async load(context) {
      const endpoints = 31, first = localMidnight(context.now, 2 - endpoints - context.days);
      const { calc, data } = await carbon(context, "", first);
      const byDay = new Map(data.series.map(point => [point.day, point.tokens]));
      const points: number[] = [], pointTimes: number[] = [];
      for (let index = 0; index < endpoints; index++) {
        const end = localMidnight(context.now, index - endpoints + 1), tokens: TokenCounts = {};
        for (let back = 0; back < context.days; back++) { const counts = byDay.get(localDay(localMidnight(end, -back))); if (counts) addTokens(tokens, counts); }
        points.push(calc.estimate(data.factors, { tokens }).co2 / 1000); pointTimes.push(localMidnight(end, 1) - 1);
      }
      const current = points.at(-1)!, prior = points.slice(0, -1), car = data.factors.equivalents?.find(item => item.key === "car_miles");
      if (!points.some(value => value > 0)) return empty("No recorded tokens in this period");
      return preview({ kind: "trend", study: { kind: "trend", value: current, valueLabel: `kg CO₂e · past ${context.days} days`, sampleLabel: "", pointValueLabel: "kg CO₂e", baseline: prior.reduce((a, b) => a + b, 0) / prior.length, points, pointTimes, lookbackDays: context.days, trendLabel: `Trailing ${context.days}d` } },
        { coverHeight: 260, evidence: car ? `≈ ${sig(current * 1000 / car.g_co2e_per_unit, 2)} ${car.label}` : undefined, note: `${carbonNote} Daily endpoints use local calendar days, so the latest point includes today so far.` });
    } },
  { id: "draft-carbon-tiers", category: "carbon", loading: "pie", height: 230,
    title: () => "CO₂e by model size", description: days => `Share of estimated emissions from small, mid-size and large models over the past ${days} days.`,
    action: "Compare model tiers", url: carbonURL,
    async load(context) {
      const { calc, data } = await carbon(context, "", carbonFrom(context));
      const tokens = data.windows[0]?.tokens ?? {}, tones: Record<string, string> = { small: "var(--heat-0)", medium: "var(--heat-1)", large: "var(--heat-2)" };
      const rows = Object.entries(tokens).map(([tier, counts]) => {
        const result = calc.estimate(data.factors, { tokens: { [tier]: counts } });
        return { label: data.factors.tiers.find(item => item.key === tier)?.label ?? (tier || "Unclassified"), value: result.co2, display: calc.mass(result.co2), detail: `${sig(tokenTotal({ [tier]: counts }) / 1e9, 3)}B tokens`, color: tones[tier] ?? otherSeriesColor };
      }).filter(row => row.value > 0).sort((a, b) => b.value - a.value);
      return rows.length ? preview({ kind: "pie", rows, legendColumns: 3 }, { coverHeight: 230, note: `${carbonNote} Models are assigned to tiers by name; unmatched models use the default tier.` }) : empty("No recorded tokens in this period");
    } },
  { id: "draft-carbon-repositories", category: "carbon", loading: "list", loadingCount: 6, height: 230,
    title: () => "CO₂e by repository", description: days => `Repositories with the largest estimated emissions from recorded tokens over the past ${days} days.`,
    action: "Compare repositories", url: carbonURL,
    async load(context) {
      const { calc, data } = await carbon(context, "repository_name", carbonFrom(context));
      const byRepo = new Map<string, TokenCounts>();
      for (const point of data.series) addTokens(byRepo.get(point.value) ?? byRepo.set(point.value, {}).get(point.value)!, point.tokens);
      const rows = [...byRepo].map(([name, tokens]) => { const grams = calc.estimate(data.factors, { tokens }).co2; return { label: name || "No repository", value: grams, display: calc.mass(grams), detail: `${sig(tokenTotal(tokens) / 1e6, 3)}M tokens`, color: seriesColor("repository_name", name) }; });
      const folded = foldRows(rows.filter(row => row.value > 0), 5, value => ({ display: calc.mass(value) }));
      return folded.length ? preview({ kind: "rows", rows: folded }, { coverHeight: 230, note: `${carbonNote} Undated tokens cannot be placed in a period and are omitted here.` }) : empty("No recorded tokens in this period");
    } },
  { id: "draft-carbon-models", category: "carbon", loading: "list", loadingCount: 6, height: 230,
    title: () => "CO₂e by model", description: days => `Models with the largest estimated emissions from recorded tokens over the past ${days} days.`,
    action: "Compare models", url: carbonURL,
    async load(context) {
      const { calc, data, rows } = await carbonBy(context, "model_family");
      const listed = rows.map(row => ({ label: row.key || "Unknown model", value: row.grams, display: calc.mass(row.grams), detail: `${tierOf(data.factors, row.tokens)} · ${sig(tokenTotal(row.tokens) / 1e6, 3)}M tokens`, color: seriesColor("model_family", row.key) }));
      return listed.length ? preview({ kind: "rows", rows: foldRows(listed, 5, value => ({ display: calc.mass(value) })) }, { coverHeight: 230, note: `${carbonNote} Each model family is placed in its size tier by name, as on the Carbon page.` }) : empty("No recorded tokens in this period");
    } },
  { id: "draft-carbon-providers", category: "carbon", loading: "pie", height: 300,
    title: () => "CO₂e by provider", description: days => `Share of estimated emissions by agent provider over the past ${days} days.`,
    action: "Compare providers", url: carbonURL,
    async load(context) {
      const { calc, rows } = await carbonBy(context, "provider");
      const listed = rows.map(row => ({ label: providerNames[row.key] ?? (row.key || "Unknown provider"), value: row.grams, display: calc.mass(row.grams), detail: `${sig(tokenTotal(row.tokens) / 1e9, 3)}B tokens`, color: seriesColor("provider", row.key) }));
      return listed.length ? preview({ kind: "pie", rows: foldRows(listed, 5, value => ({ display: calc.mass(value) })), legendColumns: 3 }, { coverHeight: 300, note: carbonNote }) : empty("No recorded tokens in this period");
    } },
  { id: "draft-carbon-effort", category: "carbon", loading: "pie", height: 300,
    title: () => "CO₂e by effort", description: days => `Share of estimated emissions by recorded reasoning effort over the past ${days} days.`,
    action: "Compare effort levels", url: carbonURL,
    async load(context) {
      const { calc, rows } = await carbonBy(context, "effort");
      if (!rows.some(row => row.key)) return empty(rows.length ? "No usage in this period records effort" : "No recorded tokens in this period", { note: "Effort is read from transcripts when they are indexed. Libraries indexed before effort tracking fill it in when sources are reparsed; sources that never recorded effort stay unspecified." });
      const order = ["none", "minimal", "low", "medium", "high", "xhigh", "ultra", "max"], rank = (key: string) => key ? (order.indexOf(key.toLowerCase()) + 1 || 99) : 100;
      const listed = rows.sort((a, b) => rank(a.key) - rank(b.key)).map(row => ({ label: row.key || "Unspecified", value: row.grams, display: calc.mass(row.grams), detail: `${sig(tokenTotal(row.tokens) / 1e9, 3)}B tokens`, color: row.key ? seriesColor("effort", row.key) : otherSeriesColor }));
      return preview({ kind: "pie", rows: listed }, { coverHeight: 300, note: `${carbonNote} Effort is the level recorded on each usage fact; unspecified effort stays its own slice.` });
    } },
];

// ----- Machine time ---------------------------------------------------------

async function machineFacts(context: DraftContext, groupBy: string[], offsetDays = 0) {
  const window = machineWindow(context.days, new Date(localMidnight(context.now, -offsetDays)));
  const where = timeAggregationWhere(machineWhere({ days: context.days, repository: "", provider: "", model: "" }, window));
  const aggregations: AggregationClause[] = [{ id: "time", op: "sum", field: "duration_ms", groupBy }, { id: "timed", op: "count", field: "duration_ms", groupBy }, { id: "population", op: "count", groupBy: [] }];
  const response = await context.json<AggregationResponse>("/api/query/tool_calls/aggregations", { where, aggregations, requireComplete: true });
  const metric = (id: string) => response.metrics.find(item => item.id === id)?.buckets ?? [];
  const timed = new Map(metric("timed").map(bucket => [JSON.stringify(bucket.keys), Number(bucket.value) || 0]));
  const population = Number(metric("population")[0]?.value ?? 0), rows = metric("time");
  if (rows.reduce((sum, bucket) => sum + (bucket.count ?? 0), 0) !== population) throw new Error("Operation groups are incomplete. Choose a shorter preview window.");
  return { window, population, rows: rows.map(bucket => ({ keys: bucket.keys.map(key => String(key ?? "")), duration: Math.max(0, Number(bucket.value) || 0), calls: bucket.count ?? 0, timed: timed.get(JSON.stringify(bucket.keys)) ?? 0 })) };
}
const machineURL = (days: number) => `/usage?usage=tokens&machine_view=time&activity_days=${[7, 14, 30, 90].includes(days) ? days : 30}`;
const machineNote = "Sums recorded tool-call durations, so overlapping calls add up: this is machine work, not wall-clock time. Calls without a recorded duration count toward calls but not time.";

// Daily tool-call totals for 31 endpoints, each covering the trailing seven
// complete local days. Both trend cards send the same request.
const trailingDays = 7, endpoints = 31;
async function toolCallTrend(context: DraftContext) {
  const window = machineWindow(endpoints - 1 + trailingDays, new Date(context.now)), groupBy = ["day"];
  const days = Array.from({ length: endpoints - 1 + trailingDays }, (_, index) => shiftMachineDay(machineDay(window.start), index));
  // Selecting whole local days through the day column uses the SQL cube; a
  // started_at range over 37 days scans calls. Stay within 30 day predicates.
  const responses = await Promise.all([days.slice(0, -30), days.slice(-30)].map(chunk => context.json<AggregationResponse>("/api/query/tool_calls/aggregations", { where: [{ any: chunk.map(value => ({ field: "day", op: "=", value })) }], aggregations: [{ id: "calls", op: "count", groupBy }, { id: "timed", op: "count", field: "duration_ms", groupBy }, { id: "time", op: "sum", field: "duration_ms", groupBy }], requireComplete: true })));
  const daily = (id: string) => new Map(responses.flatMap(response => response.metrics.find(metric => metric.id === id)?.buckets ?? []).map(bucket => [String(bucket.keys[0]), Number(bucket.value) || 0]));
  const calls = daily("calls"), timed = daily("timed"), time = daily("time"), last = shiftMachineDay(machineDay(window.end), -1);
  return Array.from({ length: endpoints }, (_, index) => {
    const end = shiftMachineDay(last, index - endpoints + 1), days = Array.from({ length: trailingDays }, (_, back) => shiftMachineDay(end, -back));
    const total = (map: Map<string, number>) => days.reduce((sum, key) => sum + (map.get(key) ?? 0), 0);
    return { day: end, time: new Date(`${end}T23:59:59`).getTime(), calls: total(calls), timed: total(timed), duration: total(time) };
  });
}
const trend = (points: (number | null)[], extra: Omit<HumanStudyPreview, "kind" | "points" | "baseline" | "sampleLabel"> & { sampleLabel?: string }): HumanStudyPreview => {
  const prior = points.slice(0, -1).filter((value): value is number => value !== null);
  return { kind: "trend", sampleLabel: "", points, baseline: prior.length ? prior.reduce((a, b) => a + b, 0) / prior.length : undefined, value: points.at(-1) ?? undefined, ...extra };
};

export const machineTimeDrafts: DraftCard[] = [
  { id: "draft-machine-time", category: "machines", loading: "columns", loadingCount: 30, height: 240,
    title: () => "Recorded machine time", description: days => `Tool and command time across the past ${days} complete days, by the kind of work.`,
    action: "Explore machine time", url: machineURL,
    async load(context) {
      const [current, previous] = await Promise.all([machineFacts(context, ["activity", "day"]), machineFacts(context, [], context.days)]);
      const total = current.rows.reduce((sum, row) => sum + row.duration, 0), before = previous.rows.reduce((sum, row) => sum + row.duration, 0);
      if (!total) return empty("No timed tool calls in this period");
      const byActivity = new Map<string, number>();
      for (const row of current.rows) byActivity.set(row.keys[0], (byActivity.get(row.keys[0]) ?? 0) + row.duration);
      const top = [...byActivity].sort((a, b) => b[1] - a[1]).slice(0, 6).map(([id]) => id);
      const series: ChartSeries[] = [...top.map(id => { const style = activityPresentation(id); return { key: id, label: style.label, color: style.color }; }), { key: "other", label: "Other work", color: otherSeriesColor }];
      const buckets: ChartBucket[] = [];
      for (let date = new Date(current.window.start); date < new Date(current.window.end); date.setDate(date.getDate() + 1)) buckets.push({ key: machineDay(date), date: new Date(date), values: {} });
      const byDay = new Map(buckets.map(bucket => [bucket.key, bucket]));
      for (const row of current.rows) { const bucket = byDay.get(row.keys[1]); if (bucket) { const key = top.includes(row.keys[0]) ? row.keys[0] : "other"; bucket.values[key] = (bucket.values[key] ?? 0) + row.duration / 36e5; } }
      const timed = current.rows.reduce((sum, row) => sum + row.timed, 0);
      return preview({ kind: "columns", buckets, series, unit: "h", yLabel: "Hours", legendColumns: 3 }, { coverHeight: 240, headlineMetric: { value: hours(total).split(" ")[0], unit: hours(total).split(" ")[1] ?? "", label: before ? `${relativeChange(total, before)} vs the previous ${context.days} days` : "recorded" },
        evidence: `${count(current.population, "call")} · ${percent(current.population ? timed / current.population : 0)} with a recorded duration`, note: machineNote });
    } },
  { id: "draft-machine-activities", category: "machines", loading: "list", loadingCount: 6, height: 230,
    title: () => "Where machine time goes", description: days => `Share of recorded tool time by kind of work over the past ${days} complete days.`,
    action: "Compare activities", url: machineURL,
    async load(context) {
      const { rows, population } = await machineFacts(context, ["activity"]);
      const total = rows.reduce((sum, row) => sum + row.duration, 0);
      if (!total) return empty("No timed tool calls in this period");
      const listed = rows.map(row => { const style = activityPresentation(row.keys[0]); return { label: style.label, value: row.duration, display: `${hours(row.duration)} · ${percent(row.duration / total)}`, detail: `${count(row.calls, "call")} · ${row.timed ? `${timeLabel(row.duration / row.timed)} per timed call` : "no recorded durations"}`, color: style.color }; });
      return preview({ kind: "rows", rows: foldRows(listed, 5, value => ({ display: `${hours(value)} · ${percent(value / total)}` })) }, { coverHeight: 230, evidence: `${hours(total)} across ${count(population, "call")}`, note: `${machineNote} Activities follow the Machine Time classification of commands and tools.` });
    } },
  { id: "draft-machine-operations", category: "machines", loading: "list", loadingCount: 5, height: 230,
    title: () => "Longest-running operations", description: days => `Commands and tools with the most recorded time over the past ${days} complete days.`,
    action: "Inspect slow operations", url: machineURL,
    async load(context) {
      const { rows } = await machineFacts(context, ["operation_name", "activity"]);
      const total = rows.reduce((sum, row) => sum + row.duration, 0);
      if (!total) return empty("No timed tool calls in this period");
      const listed = rows.filter(row => row.duration > 0).sort((a, b) => b.duration - a.duration).slice(0, 6).map(row => ({ label: row.keys[0] || "(Unidentified tool)", value: row.duration, display: hours(row.duration), detail: `${activityPresentation(row.keys[1]).label} · ${count(row.calls, "call")} · ${row.timed ? `${timeLabel(row.duration / row.timed)} per timed call` : ""} · ${percent(row.duration / total)} of all time`, color: activityPresentation(row.keys[1]).color }));
      return preview({ kind: "rows", rows: listed }, { coverHeight: 230, note: `${machineNote} Operations group a program and subcommand (for example “go test”) or a named tool.` });
    } },
  { id: "draft-machine-call-trend", category: "machines", windowLabel: "7d avg", loading: "line", loadingCount: 31, height: 260,
    title: () => "Tool calls per day", description: () => "Daily tool calls averaged over each trailing seven complete days, across the last 30 days.",
    action: "Explore machine time", url: () => machineURL(30),
    async load(context) {
      const series = await toolCallTrend(context);
      if (!series.some(point => point.calls)) return empty("No tool calls in the last 37 days");
      return preview({ kind: "trend", study: trend(series.map(point => point.calls / trailingDays), { valueLabel: "calls a day · 7-day average", pointValueLabel: "calls a day", pointTimes: series.map(point => point.time), lookbackDays: trailingDays, trendLabel: "7d avg" }) },
        { coverHeight: 260, evidence: `${series.at(-1)!.calls.toLocaleString()} calls in the last 7 complete days`, note: "Counts every recorded tool call, including errors and calls without a duration, by its local start day. Each point divides seven days' calls by seven, including idle days; today is excluded until it is complete." });
    } },
  { id: "draft-machine-latency-trend", category: "machines", windowLabel: "7d avg", loading: "line", loadingCount: 31, height: 260,
    title: () => "Average tool call time", description: () => "Mean recorded duration per tool call over each trailing seven complete days, across the last 30 days.",
    action: "Inspect slow operations", url: () => machineURL(30),
    async load(context) {
      const series = await toolCallTrend(context);
      if (!series.some(point => point.timed)) return empty("No timed tool calls in the last 37 days");
      return preview({ kind: "trend", study: trend(series.map(point => point.timed ? point.duration / point.timed / 1000 : null), { valueLabel: "seconds per call · 7-day mean", pointValueLabel: "seconds per timed call", pointTimes: series.map(point => point.time), lookbackDays: trailingDays, trendLabel: "7d mean" }) },
        { coverHeight: 260, evidence: `${series.at(-1)!.timed.toLocaleString()} timed calls in the last 7 complete days`, note: "Total recorded duration ÷ calls with a recorded duration, pooled over each seven-day window (not an average of daily averages). Long-running calls such as waits and watches pull the mean up; Longest-running operations names them. Windows without timed calls are gaps." });
    } },
];

// ----- Parallelism (timing snapshot) ----------------------------------------

async function snapshot(context: DraftContext) {
  let data: ConcurrencyData;
  try { data = await context.json<ConcurrencyData>("/api/usage/concurrency-prototype"); }
  catch { return null; }
  const end = zonedDayBounds(data.period_end, data.timezone)[1], available = data.days.length, days = Math.min(context.days, available);
  const start = zonedDayBounds(data.days[available - days], data.timezone)[0], settings = readConcurrencySettings();
  return { data, days, bounds: [start, end] as Interval, settings, partial: days < context.days };
}
const snapshotMissing = () => empty("Parallelism needs a captured timing snapshot", { note: "The production service has no timing endpoint yet; this preview uses the dev prototype's snapshot when one is served." });
const unitWord = (counting: string, n = 2) => counting === "agent" ? n === 1 ? "session" : "sessions" : n === 1 ? "workspace" : "workspaces";
const snapshotLabel = (s: { days: number; partial: boolean; data: ConcurrencyData }) => `Last ${s.days} captured days${s.partial ? " (the whole snapshot)" : ""}, through ${new Date(`${s.data.period_end}T12:00:00`).toLocaleDateString(undefined, { month: "short", day: "numeric" })}`;
// Time at each concurrency level within [from, to): level → milliseconds.
function levels(units: Map<string, Interval[]>, from: number, to: number): number[] {
  const events: Array<[number, number]> = [];
  for (const spans of units.values()) for (const [a, b] of clipIntervals(spans, [from, to])) events.push([a, 1], [b, -1]);
  events.sort((a, b) => a[0] - b[0] || a[1] - b[1]);
  const result: number[] = [];
  let active = 0, previous = from;
  for (const [at, delta] of events) { if (active > 0 && at > previous) result[active] = (result[active] ?? 0) + at - previous; active += delta; previous = at; }
  return result;
}
const parallelismURL = () => "/usage?usage=tokens&machine_view=parallelism";
const parallelNote = (counting: string, gap: number) => `Counts ${unitWord(counting)} with an active threaded conversation: observed prompt-to-response turns joined across gaps up to ${gap} minutes (your Parallelism setting). Delegated agents run inside their root's workspace. Timing is estimated from transcript timestamps.`;

export const parallelismDrafts: DraftCard[] = [
  { id: "draft-parallel-days", category: "machines", window: "snapshot", loading: "columns", loadingCount: 14, height: 228,
    title: () => "Parallel hours by day", description: () => "Hours with one, two, or three or more agents active, for each captured day.",
    action: "See each day's timeline", url: parallelismURL,
    async load(context) {
      const s = await snapshot(context);
      if (!s) return snapshotMissing();
      const units = concurrencySpans(s.data.threads, "user", s.settings.gap, s.settings.counting), word = unitWord(s.settings.counting);
      // Stacks draw from the first series up: keep the most parallel time on the baseline.
      const series: ChartSeries[] = [{ key: "more", label: `3+ ${word}`, color: "var(--heat-1)" }, { key: "two", label: `2 ${word}`, color: "color-mix(in srgb,var(--heat-1) 70%,var(--panel))" }, { key: "one", label: `1 ${unitWord(s.settings.counting, 1)}`, color: "color-mix(in srgb,var(--heat-1) 35%,var(--panel))" }];
      let parallel = 0, active = 0;
      const buckets: ChartBucket[] = s.data.days.slice(-s.days).map(dayKey => {
        const [from, to] = zonedDayBounds(dayKey, s.data.timezone), byLevel = levels(units, from, to);
        const one = byLevel[1] ?? 0, two = byLevel[2] ?? 0, more = byLevel.slice(3).reduce((sum, value) => sum + (value ?? 0), 0);
        parallel += two + more; active += one + two + more;
        return { key: dayKey, date: new Date(`${dayKey}T12:00:00`), values: { one: one / 36e5, two: two / 36e5, more: more / 36e5 } };
      });
      if (!active) return empty("No captured agent activity in these days");
      return preview({ kind: "columns", buckets, series, unit: "h", yLabel: "Hours", legendColumns: 3 }, { coverHeight: 228, headlineMetric: { value: percent(parallel / active), unit: "", label: `of active time had 2+ ${word}` }, evidence: snapshotLabel(s), note: parallelNote(s.settings.counting, s.settings.gap) });
    } },
  { id: "draft-parallel-trend", category: "machines", window: "snapshot", windowLabel: "7d avg", loading: "line", loadingCount: 8, height: 260,
    title: () => "Agents at once", description: () => "Average number of agents running together while any is active, over each trailing seven captured days.",
    action: "Open the parallelism view", url: parallelismURL,
    async load(context) {
      const s = await snapshot(context);
      if (!s) return snapshotMissing();
      const units = concurrencySpans(s.data.threads, "user", s.settings.gap, s.settings.counting), days = s.data.days;
      if (days.length < trailingDays) return empty(`The snapshot covers fewer than ${trailingDays} days`);
      const ends = days.slice(trailingDays - 1), points = ends.map(endDay => {
        const index = days.indexOf(endDay), from = zonedDayBounds(days[index - trailingDays + 1], s.data.timezone)[0], to = zonedDayBounds(endDay, s.data.timezone)[1], byLevel = levels(units, from, to);
        const active = byLevel.reduce((sum, value) => sum + (value ?? 0), 0);
        return active ? byLevel.reduce((sum, value, level) => sum + (value ?? 0) * level, 0) / active : null;
      });
      if (!points.some(value => value !== null)) return empty("No captured agent activity in these days");
      return preview({ kind: "trend", study: trend(points, { valueLabel: `${unitWord(s.settings.counting)} at once while active · 7-day average`, pointValueLabel: `${unitWord(s.settings.counting)} at once`, pointTimes: ends.map(endDay => zonedDayBounds(endDay, s.data.timezone)[1] - 1), lookbackDays: trailingDays, trendLabel: "7d avg", historyDays: points.length - 1 }) },
        { coverHeight: 260, evidence: `${points.length} seven-day windows in the ${days.length}-day snapshot, through ${new Date(`${s.data.period_end}T12:00:00`).toLocaleDateString(undefined, { month: "short", day: "numeric" })}`, note: `${parallelNote(s.settings.counting, s.settings.gap)} Each point weights concurrency by time: agent-hours ÷ hours with any agent active. The first point needs seven captured days, so the line starts on the seventh day of the snapshot.` });
    } },
  { id: "draft-parallel-hours", category: "machines", window: "snapshot", loading: "columns", loadingCount: 24, height: 170,
    title: () => "When agents overlap", description: () => "Average number of agents running at each hour of the day, over the latest captured days.",
    action: "Find your parallel hours", url: parallelismURL,
    async load(context) {
      const s = await snapshot(context);
      if (!s) return snapshotMissing();
      const units = concurrencySpans(s.data.threads, "user", s.settings.gap, s.settings.counting), area = Array<number>(24).fill(0);
      for (const dayKey of s.data.days.slice(-s.days)) {
        const [from] = zonedDayBounds(dayKey, s.data.timezone);
        // Daylight-saving days have 23 or 25 hours; step by real hours from midnight.
        for (let hour = 0; hour < 24; hour++) { const start = from + hour * 36e5, byLevel = levels(units, start, start + 36e5); area[new Date(start).getHours()] += byLevel.reduce((sum, value, level) => sum + (value ?? 0) * level, 0); }
      }
      const values = area.map(value => value / (s.days * 36e5));
      if (!values.some(Boolean)) return empty("No captured agent activity in these days");
      const busiest = values.indexOf(Math.max(...values));
      return preview({ kind: "hours", values, format: value => `${value.toFixed(2)}×`, detail: `average concurrent ${unitWord(s.settings.counting)} across ${s.days} days`, timeZone: s.data.timezone }, { coverHeight: 170, evidence: `${snapshotLabel(s)} · busiest at ${String(busiest).padStart(2, "0")}:00 (${values[busiest].toFixed(1)}×)`, note: `${parallelNote(s.settings.counting, s.settings.gap)} Each hour averages over every captured day, including idle days.` });
    } },
  { id: "draft-parallel-repositories", category: "machines", window: "snapshot", loading: "list", loadingCount: 6, height: 230,
    title: () => "Agent hours by repository", description: () => "Active agent hours per repository over the latest captured days, counting parallel agents separately.",
    action: "Compare repositories", url: parallelismURL,
    async load(context) {
      const s = await snapshot(context);
      if (!s) return snapshotMissing();
      const byRepository = new Map<string, ConcurrencyData["threads"]>();
      for (const thread of s.data.threads) { const name = thread.repository || "No repository"; byRepository.set(name, [...byRepository.get(name) ?? [], thread]); }
      const rows = [...byRepository].map(([name, threads]) => {
        const units = concurrencySpans(threads, "user", s.settings.gap, s.settings.counting), byLevel = levels(units, ...s.bounds);
        const workspaceHours = [...units.values()].reduce((sum, spans) => sum + clipIntervals(spans, s.bounds).reduce((n, [a, b]) => n + b - a, 0), 0), elapsed = byLevel.reduce((sum, value) => sum + (value ?? 0), 0);
        return { label: name, value: workspaceHours, display: hours(workspaceHours), detail: `${hours(elapsed)} elapsed with any active · ${(elapsed ? workspaceHours / elapsed : 0).toFixed(1)}× while active · ${units.size} ${unitWord(s.settings.counting, units.size)}`, color: seriesColor("repository_name", name) };
      }).filter(row => row.value > 0);
      return rows.length ? preview({ kind: "rows", rows: foldRows(rows, 5, value => ({ display: hours(value) })) }, { coverHeight: 230, evidence: snapshotLabel(s), note: parallelNote(s.settings.counting, s.settings.gap) }) : empty("No captured agent activity in these days");
    } },
];

// ----- Findings -------------------------------------------------------------

type Impact = { usd?: number; tokens?: number; minutes?: number; failures?: number };
type FindingsResponse = { settings?: { rank?: keyof Impact }; summary: { open: number; watching: number; won: number; dismissed: number; snoozed: number; regressed: number; at_stake_usd: number; likely_usd?: number; new: number; next_result_days: number | null; saved: Impact & { added_usd?: number; added_tokens?: number; weeks?: number } }; findings?: Array<{ id: string; title: string; detector_label: string; target_label?: string; change_label?: string; lever?: string; impact: Impact; expected?: Impact; rate?: string; new?: unknown }>; wins?: Array<{ id: string; title: string; saved?: Impact }> };
const money = (value: number) => value.toLocaleString(undefined, { style: "currency", currency: "USD", maximumFractionDigits: value >= 100 ? 0 : 2 });
const impactText: Record<keyof Impact, (value: number) => string> = { usd: money, tokens: value => `${sig(value / 1e6, 2)}M tokens`, minutes: value => hours(value * 6e4), failures: value => count(Math.round(value), "failure") };

// A finding's lever says where its change is made. Labels such as "A line in
// AGENTS.md" describe one specific step and are written to follow "Suggests:".
const leverNames: Record<string, string> = { "repo-instructions": "Repository instructions", "global-instructions": "Global instructions", "repo-tooling": "Repository tools & skills", "harness-settings": "Harness settings", "automation-prompt": "Automation prompts" };
function fixKind(card: { lever?: string; change_label?: string }): string {
  if (card.lever) return leverNames[card.lever] ?? "Other change";
  // Services before the lever field: name the step without its article.
  const label = (card.change_label ?? "").replace(/^(an?|one)\s+/i, "");
  return label ? label[0].toUpperCase() + label.slice(1) : "Other change";
}

export const findingDrafts: DraftCard[] = [
  { id: "draft-findings-open", category: "findings", window: "current", loading: "list", loadingCount: 3, height: 205,
    title: () => "Biggest open findings", description: () => "Optimization findings with the largest likely monthly saving, from the latest Findings analysis.",
    action: "Review your findings", url: () => "/findings",
    async load(context) {
      const data = await context.json<FindingsResponse>("/api/findings?state=open"), rank = data.settings?.rank ?? "usd";
      const cards = (data.findings ?? []).map(card => ({ card, value: Number((card.expected ?? card.impact)[rank]) || 0 })).filter(item => item.value > 0).sort((a, b) => b.value - a.value);
      if (!cards.length) return empty("No open findings right now");
      return preview({ kind: "rows", rows: cards.slice(0, 3).map(({ card, value }) => ({ label: card.title, value, display: `${impactText[rank](value)}/mo`, detail: `${card.detector_label}${card.target_label ? ` · ${card.target_label}` : ""}\nLikely saving per month; ${impactText[rank](Number(card.impact[rank]) || 0)} at stake${card.rate ? `\nRate: ${card.rate}` : ""}` })) },
        { coverHeight: 205, headlineMetric: { value: impactText[rank](cards.reduce((sum, item) => sum + item.value, 0)), unit: "", label: `likely a month across ${count(data.summary.open, "open finding")}` }, note: "Likely saving = what the pattern cost × the share a change can remove × how often a change takes × how often the pattern persists untreated. Ranked by your Findings unit." });
    } },
  { id: "draft-findings-fixes", category: "findings", window: "current", loading: "pie", height: 300,
    title: () => "Savings by kind of fix", description: () => "Likely monthly savings across open findings, by where the suggested change is made.",
    action: "Review your findings", url: () => "/findings",
    async load(context) {
      const data = await context.json<FindingsResponse>("/api/findings?state=open"), rank = data.settings?.rank ?? "usd", groups = new Map<string, { value: number; count: number }>();
      for (const card of data.findings ?? []) {
        const value = Number((card.expected ?? card.impact)[rank]) || 0, label = fixKind(card), group = groups.get(label) ?? { value: 0, count: 0 };
        if (value > 0) groups.set(label, { value: group.value + value, count: group.count + 1 });
      }
      const rows = [...groups].map(([label, group]) => ({ label, value: group.value, display: `${impactText[rank](group.value)}/mo`, detail: count(group.count, "open finding"), color: seriesColor("change", label) }));
      if (!rows.length) return empty("No open findings right now");
      return preview({ kind: "pie", rows: foldRows(rows, 5, value => ({ display: `${impactText[rank](value)}/mo` })) }, { coverHeight: 300, note: "Each open finding counts once, under the kind of change its prompt makes: repository instructions, repository tools and skills, harness settings, automation prompts or your global instructions. Likely savings are additive monthly estimates in your Findings unit." });
    } },
  { id: "draft-findings-results", category: "findings", window: "current", loading: "value", height: 150,
    title: () => "Net savings from findings", description: () => "What copied finding prompts have saved after their added context cost, across verified results.",
    action: "See your wins", url: () => "/findings?list=wins",
    async load(context) {
      const { summary } = await context.json<FindingsResponse>("/api/findings?state=summary"), saved = summary.saved;
      const gross = Number(saved.usd) || 0, added = Number(saved.added_usd) || 0;
      return preview({ kind: "value", value: money(gross - added), label: "saved, net", detail: `${money(gross)} saved minus ${money(added)} of added context cost`, footnote: saved.weeks ? `Measured across ${count(saved.weeks, "week")} of verified results` : "No verified results yet" }, { coverHeight: 150, note: "Copying a prompt starts its measurement. Savings come from each finding's fixed before/after plan; dismissed and snoozed findings are excluded." });
    } },
];

// ----- Pricing --------------------------------------------------------------

type Pricing = { priced_tokens: number; assumed_tokens: number; partial_tokens: number; unpriced_tokens: number; total_tokens: number; proposed_changes: number; unpriced_models: Array<{ model: string; provider: string; tokens: number; last_day: string }>; assumed_models: Array<{ model: string; provider: string; tokens: number; last_day: string }> };

export const pricingDrafts: DraftCard[] = [
  { id: "draft-library-pricing", category: "library", window: "current", loading: "list", loadingCount: 4, height: 205,
    title: () => "Models needing prices", description: () => "Models whose tokens are unpriced or priced by assumption, by recorded token volume.",
    action: "Review pricing", url: () => "/settings",
    async load(context) {
      const data = await context.json<Pricing>("/api/health/pricing");
      const rows = [...data.unpriced_models.map(model => ({ ...model, status: "Unpriced", color: "var(--bad)" })), ...data.assumed_models.map(model => ({ ...model, status: "Assumed", color: "var(--warn)" }))].sort((a, b) => b.tokens - a.tokens);
      const uncovered = data.unpriced_tokens + data.assumed_tokens + data.partial_tokens;
      if (!rows.length) return preview({ kind: "stats", stats: [{ label: "Priced tokens", value: percent(data.total_tokens ? data.priced_tokens / data.total_tokens : 1), tone: "good" }, { label: "Proposed changes", value: String(data.proposed_changes) }] }, { coverHeight: 205 });
      return preview({ kind: "rows", rows: rows.slice(0, 5).map(model => ({ label: model.model, value: model.tokens, display: `${sig(model.tokens / 1e6, 3)}M`, detail: `${model.status} · ${model.provider} · last used ${model.last_day}`, color: model.color })) },
        { coverHeight: 205, headlineMetric: { value: percent(data.total_tokens ? uncovered / data.total_tokens : 0), unit: "", label: "of tokens lack a confirmed price" }, evidence: data.proposed_changes ? `${count(data.proposed_changes, "proposed price change")} awaiting review` : undefined, note: "All-time token volume. Assumed models borrow a sibling model's price; partial facts are lower bounds." });
    } },
];
