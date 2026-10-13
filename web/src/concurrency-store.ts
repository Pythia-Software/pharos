import { clipIntervals, concurrencyMetrics, concurrencySpans, minute, workSessions, zonedDayBounds, type ConcurrencyCounting, type ConcurrencyData, type ConcurrencyMetrics } from "./concurrency-data";
import { readConcurrencySettings, resolveConcurrencyRange } from "./concurrency-settings";

let cached: ConcurrencyData | null = null, pending: Promise<ConcurrencyData> | null = null, loadError = "";
const summaryCache = new Map<string, ConcurrencyMetrics | null>();
export function loadConcurrencyData(refresh = false): Promise<ConcurrencyData> {
  if (pending) return pending;
  if (cached && !refresh) return Promise.resolve(cached);
  pending = fetch("/api/usage/concurrency-prototype").then(async response => {
    if (!response.ok) throw new Error(`Could not read the timing snapshot (${response.status})`);
    return response.json() as Promise<ConcurrencyData>;
  }).then(data => {
    cached = data; loadError = ""; summaryCache.clear(); return data;
  }).catch(error => { loadError = error instanceof Error ? error.message : String(error); throw error; }).finally(() => { pending = null; });
  return pending;
}

// The prototype has a bounded timing export. Never label its seven days as
// all-time/30-day coverage. Fully covered idle windows have a 0× average.
export function threadedSummaryWindow(data: ConcurrencyData, gap: number, repository: string, key: string, referenceTime: number, counting: ConcurrencyCounting = "workspace"): ConcurrencyMetrics | null {
  const durations: Record<string, number> = { "1h": 60 * minute, "6h": 6 * 60 * minute, "24h": 24 * 60 * minute, "7d": 7 * 24 * 60 * minute, "30d": 30 * 24 * 60 * minute };
  const from = key === "all" ? -Infinity : referenceTime - (durations[key] ?? Infinity);
  const coverageStart = zonedDayBounds(data.period_start, data.timezone)[0];
  const coverageEnd = zonedDayBounds(data.period_end, data.timezone)[1];
  if (!Number.isFinite(referenceTime) || from < coverageStart || referenceTime > coverageEnd) return null;
  const threads = data.threads.filter(thread => !repository || thread.repository === repository);
  const bounds: [number, number] = [from, referenceTime];
  const sessions = clipIntervals(workSessions(threads), bounds);
  const spans = concurrencySpans(threads, "user", gap, counting);
  return concurrencyMetrics(spans, sessions, bounds);
}

declare global {
  interface Window {
    routeFromLocation?: () => void;
    pharosConcurrencySummary?: {
      open(): void;
      load(): Promise<void>;
      value(key: string, generatedAt: string | null): { amount: string; title: string };
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
    value(key, generatedAt) {
      if (!cached) return { amount: loadError ? "—" : "…", title: loadError || "Loading timing data" };
      const settings = readConcurrencySettings(), now = generatedAt ? Date.parse(generatedAt) : Date.now();
      const cacheKey = JSON.stringify([settings.counting, settings.gap, settings.repository, key, now]);
      if (!summaryCache.has(cacheKey)) summaryCache.set(cacheKey, threadedSummaryWindow(cached, settings.gap, settings.repository, key, now, settings.counting));
      const result = summaryCache.get(cacheKey);
      return { amount: result ? `${result.average.toFixed(2)}×` : "—", title: result ? `Average concurrent ${settings.counting === "agent" ? "sessions (root conversations)" : "workspaces"} over the full ${key} window, including idle time · ${settings.gap}-minute turn gap · ${settings.repository || "all repositories"}` : `Incomplete timing coverage for this window. Available: ${cached.period_start} – ${cached.period_end}.` };
    },
    note() {
      const settings = readConcurrencySettings();
      const range = cached ? resolveConcurrencyRange(cached, settings) : null;
      return `Parallelism: ${settings.counting === "agent" ? "sessions (root conversations)" : "workspaces"} · full-window average, including idle time · ${settings.gap}-minute turn gap · ${settings.repository || "all repositories"}${range ? ` · chart ${range.startDay}${range.startDay === range.endDay ? "" : ` – ${range.endDay}`}${settings.zoom ? " (zoomed)" : ""}` : ""}${cached ? ` · timing coverage ${cached.period_start} – ${cached.period_end}` : loadError ? ` · ${loadError}` : ""}`;
    },
  };
}
