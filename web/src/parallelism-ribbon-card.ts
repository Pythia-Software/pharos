import type { ChartBucket, ChartSeries } from "./chart-series";
import { clipIntervals, concurrencyPalette, concurrencySpans, concurrencyUnit, minute, type ConcurrencyData, type Interval } from "./concurrency-data";
import { concurrencyRangePresets, resolveConcurrencyRange, type ConcurrencySettings } from "./concurrency-settings";

// The Parallelism ribbon as a Home card: the ribbon's range, zoom, counting,
// gap, mode and repository, drawn as the average number of concurrent units
// in each period, stacked by repository in the ribbon's colors.
const hour = 60 * minute;
const periods = [hour, 3 * hour, 6 * hour, 12 * hour, 24 * hour, 7 * 24 * hour];
const maxColumns = 48;

const units = (settings: ConcurrencySettings) => settings.counting === "agent" ? "sessions" : "workspaces";

export function ribbonCardTitle(settings: ConcurrencySettings): string {
  return `Concurrent ${units(settings)}${settings.mode === "model" ? " with model activity" : " over time"}${settings.repository ? ` in ${settings.repository}` : ""}`;
}

export function ribbonCardDescription(settings: ConcurrencySettings): string {
  const range = settings.zoom ? `Selected interval ${new Date(settings.zoom[0]).toISOString()} to ${new Date(settings.zoom[1]).toISOString()}.`
    : settings.range === "custom" ? `Dates ${settings.startDay || settings.day || "latest day"} to ${settings.endDay || settings.startDay || settings.day || "latest day"}.`
    : settings.range === "day" && settings.day ? `Date ${settings.day}.` : "";
  return `${range ? `${range} ` : ""}Average concurrent ${units(settings)}, stacked by repository${settings.repository ? `, limited to ${settings.repository}` : ""}. ${settings.mode === "model" ? "Model activity intervals." : `Agent turns; join activity across gaps up to ${settings.gap} minutes.`}`;
}

export function ribbonCardWindow(settings: ConcurrencySettings): string {
  if (settings.zoom) return "Saved range";
  if (settings.range === "today") return "Today";
  const preset = concurrencyRangePresets.find(([key]) => key === settings.range);
  return preset ? `Past ${preset[1]} days` : settings.range === "all" ? "All time" : settings.range === "day" && !settings.day ? "Latest day" : "Saved range";
}

export function ribbonCover(data: ConcurrencyData, settings: ConcurrencySettings, now = new Date()) {
  const range = resolveConcurrencyRange(data, settings, now).bounds;
  const bounds: Interval = settings.zoom && settings.zoom[0] >= range[0] && settings.zoom[1] <= range[1] ? settings.zoom : range;
  const period = periods.find(size => (bounds[1] - bounds[0]) / size <= maxColumns) ?? periods.at(-1)!;
  const repositories = [...new Set(data.threads.map(thread => thread.repository))].sort();
  const threads = data.threads.filter(thread => !settings.repository || thread.repository === settings.repository);
  // A unit takes its top-level thread's repository, as its ribbon does.
  const owner = new Map<string, string>();
  for (const thread of threads.slice().sort((a, b) => a.agent_depth - b.agent_depth)) {
    const unit = concurrencyUnit(thread, settings.counting);
    if (!owner.has(unit)) owner.set(unit, thread.repository);
  }
  const spans = [...concurrencySpans(threads, settings.mode, settings.gap, settings.counting)].map(([unit, intervals]) => ({ repository: owner.get(unit)!, intervals: clipIntervals(intervals, bounds) })).filter(unit => unit.intervals.length);
  const buckets: ChartBucket[] = [];
  for (let start = bounds[0]; start < bounds[1]; start += period) {
    const end = Math.min(bounds[1], start + period), values: Record<string, number> = {};
    for (const { repository, intervals } of spans) {
      const busy = clipIntervals(intervals, [start, end]).reduce((total, [a, b]) => total + b - a, 0);
      if (busy) values[repository] = (values[repository] ?? 0) + busy / (end - start);
    }
    buckets.push({ key: String(start), date: new Date(start), values });
  }
  const series: ChartSeries[] = repositories.filter(repository => buckets.some(bucket => bucket.values[repository])).map(repository => ({
    key: repository, label: repository, value: repository, color: concurrencyPalette[repositories.indexOf(repository) % concurrencyPalette.length],
  }));
  return { buckets, series, hourly: period < 24 * hour, label: `Average concurrent ${units(settings)}` };
}
