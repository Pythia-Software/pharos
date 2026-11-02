import { todayBounds } from "./day-window";
import { clipIntervals, concurrencyMetrics, concurrencySpans, mergeIntervals, minute, workSessions, zonedDayBounds, type ConcurrencyCounting, type ConcurrencyData, type ConcurrencyMetrics } from "./concurrency-data";
import { readConcurrencySettings, resolveConcurrencyRange } from "./concurrency-settings";

let cached: ConcurrencyData | null = null, pending: Promise<ConcurrencyData> | null = null, loadError = "";
const summaryCache = new Map<string, ConcurrencyMetrics | null>();
const runtimeCache = new Map<string, {agent: number; active: number} | null>();
export function loadConcurrencyData(refresh = false): Promise<ConcurrencyData> {
  if (pending) return pending;
  if (cached && !refresh) return Promise.resolve(cached);
  // The dev UI can explicitly substitute a captured export for visual previews.
  const prototype = typeof document !== "undefined" && document.documentElement.hasAttribute("data-concurrency-prototype");
  pending = fetch(prototype ? "/api/usage/concurrency-prototype" : "/api/usage/parallelism").then(async response => {
    if (!response.ok) throw new Error(`Could not read the timing snapshot (${response.status})`);
    return response.json() as Promise<ConcurrencyData>;
  }).then(data => {
    // Go names the system timezone "Local" when TZ is unset. The loopback
    // service and browser share that timezone; Intl needs its IANA name.
    if (data.timezone === "Local") data = { ...data, timezone: Intl.DateTimeFormat().resolvedOptions().timeZone };
    cached = data; loadError = ""; summaryCache.clear(); runtimeCache.clear(); return data;
  }).catch(error => { loadError = error instanceof Error ? error.message : String(error); throw error; }).finally(() => { pending = null; });
  return pending;
}

// The prototype has a bounded timing export. Never label its seven days as
// all-time/30-day coverage. Fully covered idle windows have a 0× average.
export function threadedSummaryWindow(data: ConcurrencyData, gap: number, repository: string, key: string, referenceTime: number, counting: ConcurrencyCounting = "workspace", fromTimestamp?: string): ConcurrencyMetrics | null {
  const bounds = quickStatsTimingBounds(data, key, referenceTime, fromTimestamp);
  if (!bounds) return null;
  const threads = data.threads.filter(thread => !repository || thread.repository === repository);
  const sessions = clipIntervals(workSessions(threads), bounds);
  const spans = concurrencySpans(threads, "user", gap, counting);
  return concurrencyMetrics(spans, sessions, bounds);
}

function quickStatsTimingBounds(data: ConcurrencyData, key: string, end: number, fromTimestamp?: string): [number, number] | null {
  if (!Number.isFinite(end)) return null;
  const durations: Record<string, number> = { "5m": 5 * minute, "15m": 15 * minute, "1h": 60 * minute, "6h": 6 * 60 * minute, "24h": 24 * 60 * minute, "7d": 7 * 24 * 60 * minute, "30d": 30 * 24 * 60 * minute };
  if (![...Object.keys(durations), "today", "yesterday", "week", "month", "all"].includes(key)) return null;
  const from = fromTimestamp ? Date.parse(fromTimestamp) : key === "today" ? Date.parse(todayBounds(new Date(end))[0]) : end - (durations[key] ?? Infinity);
  const coverageStart = zonedDayBounds(data.period_start, data.timezone)[0];
  const coverageEnd = zonedDayBounds(data.period_end, data.timezone)[1];
  if (!Number.isFinite(from) || !Number.isFinite(end) || from >= end || from < coverageStart || end > coverageEnd) return null;
  return [from, end];
}

// Fixed to top-level sessions across all repositories: changing the chart's
// counting unit or repository filter must not change these library totals.
export function quickStatsRuntimeWindow(data: ConcurrencyData, gap: number, key: string, end: number, from?: string): { agent: number; active: number } | null {
  const bounds = quickStatsTimingBounds(data, key, end, from);
  if (!bounds) return null;
  const spans = [...concurrencySpans(data.threads, "user", gap, "agent").values()].map(intervals => clipIntervals(intervals, bounds));
  const duration = (intervals: [number, number][]) => intervals.reduce((sum, [a, b]) => sum + b - a, 0);
  return { agent: spans.reduce((sum, intervals) => sum + duration(intervals), 0), active: duration(mergeIntervals(spans.flat())) };
}

declare global {
  interface Window {
    routeFromLocation?: () => void;
    pharosConcurrencySummary?: {
      open(): void;
      load(): Promise<void>;
      value(key: string, generatedAt: string | null, from?: string): { amount: string; title: string };
      runtime(metric: string, key: string, generatedAt: string | null, from?: string): { value: number | null; title: string };
      note(): string;
    };
  }
}

export function openConcurrencyModal(): void {
  const url = new URL(location.href);
  url.searchParams.set("parallelism", "1");
  if (url.href !== location.href) history.pushState(null, "", url);
  window.dispatchEvent(new Event("pharos:parallelism"));
  window.dispatchEvent(new Event("pharos:uri-changed"));
}

export function closeConcurrencyModal(): void {
  const url = new URL(location.href);
  url.searchParams.delete("parallelism");
  history.replaceState(null, "", url);
  window.dispatchEvent(new Event("pharos:parallelism"));
  window.dispatchEvent(new Event("pharos:uri-changed"));
}

export function startConcurrencySummary(): void {
  window.pharosConcurrencySummary = {
    open: openConcurrencyModal,
    async load() { await loadConcurrencyData(true); },
    value(key, generatedAt, from) {
      if (!cached) return { amount: loadError ? "—" : "…", title: loadError || "Loading timing data" };
      const settings = readConcurrencySettings(), now = generatedAt ? Date.parse(generatedAt) : Date.now();
      const cacheKey = JSON.stringify([settings.counting, settings.gap, settings.repository, key, now, from]);
      if (!summaryCache.has(cacheKey)) summaryCache.set(cacheKey, threadedSummaryWindow(cached, settings.gap, settings.repository, key, now, settings.counting, from));
      const result = summaryCache.get(cacheKey);
      return { amount: result ? `${result.average.toFixed(2)}×` : "—", title: result ? `Average concurrent ${settings.counting === "agent" ? "sessions (root conversations)" : "workspaces"} over ${key === "today" ? "today so far" : `the full ${key} window`}, including idle time · ${settings.gap}-minute turn gap · ${settings.repository || "all repositories"}` : `Incomplete timing coverage for this window. Available: ${cached.period_start} – ${cached.period_end}.` };
    },
    runtime(metric, key, generatedAt, from) {
      if (!cached || loadError) return {value: null, title: loadError || "Loading timing data"};
      const gap = readConcurrencySettings().gap;
      const end = generatedAt ? Date.parse(generatedAt) : Date.now(), cacheKey = JSON.stringify([gap,key,end,from]);
      if (!runtimeCache.has(cacheKey)) runtimeCache.set(cacheKey, quickStatsRuntimeWindow(cached, gap, key, end, from));
      const result = runtimeCache.get(cacheKey);
      if (!result) return {value: null, title: `Incomplete timing coverage for this window. Available: ${cached.period_start} – ${cached.period_end}.`};
      return {value: metric === "agent_time_ms" ? result.agent : result.active, title: `Estimated ${metric === "agent_time_ms" ? "summed top-level session time; simultaneous sessions count separately" : "wall-clock time with a top-level session active; overlap counts once"} · available transcript timings · ${gap}-minute turn gap · all repositories`};
    },
    note() {
      const settings = readConcurrencySettings();
      const range = cached ? resolveConcurrencyRange(cached, settings) : null;
      return `Parallelism: ${settings.counting === "agent" ? "sessions (root conversations)" : "workspaces"} · full-window average, including idle time · ${settings.gap}-minute turn gap · ${settings.repository || "all repositories"}${range ? ` · chart ${range.startDay}${range.startDay === range.endDay ? "" : ` – ${range.endDay}`}${settings.zoom ? " (zoomed)" : ""}` : ""}${cached ? ` · timing coverage ${cached.period_start} – ${cached.period_end}` : loadError ? ` · ${loadError}` : ""}`;
    },
  };
}
