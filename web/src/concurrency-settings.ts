import { todayBounds } from "./day-window";
import { preferences } from "./preferences";
import { zonedDayBounds, type ConcurrencyCounting, type ConcurrencyData, type ConcurrencyMode, type Interval } from "./concurrency-data";
import { validRollingPeriods } from "./chart-series";

export const concurrencySettingsKey = "pharos-concurrency-v1";
export type ConcurrencyRange = "today" | "day" | "3d" | "7d" | "30d" | "90d" | "180d" | "365d" | "all" | "custom";
export const concurrencyRangePresets = [["3d", 3, "Last 3 days"], ["7d", 7, "Last 7 days"], ["30d", 30, "Last 30 days"], ["90d", 90, "Last 90 days"], ["180d", 180, "Last 180 days"], ["365d", 365, "Last 365 days"]] as const;

export function concurrencyWindowRange(days?: number | null): ConcurrencyRange {
  if (days === 1) return "day";
  return concurrencyRangePresets.find(([, count]) => count === days)?.[0] ?? "all";
}
export type ConcurrencySettings = {
  mode: ConcurrencyMode; gap: number; repository: string; day: string;
  counting: ConcurrencyCounting; agent: string;
  range: ConcurrencyRange; startDay: string; endDay: string;
  workspace: string; zoom: Interval | null; showConversations: boolean;
  smoothingMinutes: number;
};
export const defaultConcurrencySettings: ConcurrencySettings = {
  mode: "user", gap: 60, repository: "", day: "", workspace: "", zoom: null, showConversations: false,
  range: "day", startDay: "", endDay: "",
  counting: "workspace", agent: "",
  smoothingMinutes: 0,
};

function validDay(value: unknown): string {
  if (typeof value !== "string" || !/^\d{4}-\d{2}-\d{2}$/.test(value)) return "";
  const instant = Date.parse(`${value}T00:00:00Z`);
  return Number.isFinite(instant) && new Date(instant).toISOString().slice(0, 10) === value ? value : "";
}

export function shiftCalendarDay(day: string, offset: number): string {
  const date = new Date(`${day}T00:00:00Z`);
  date.setUTCDate(date.getUTCDate() + offset);
  return date.toISOString().slice(0, 10);
}

export function resolveConcurrencyRange(data: Pick<ConcurrencyData, "period_start" | "period_end" | "timezone">, settings: ConcurrencySettings, clock = new Date()): { startDay: string; endDay: string; bounds: Interval } {
  if (settings.range === "today") {
    const [from, to] = todayBounds(clock);
    const day = new Intl.DateTimeFormat("en-CA").format(clock);
    return { startDay: day, endDay: day, bounds: [Date.parse(from), Date.parse(to)] };
  }
  const clamp = (day: string) => day < data.period_start ? data.period_start : day > data.period_end ? data.period_end : day;
  let startDay = clamp(settings.day || data.period_end), endDay = startDay;
  if (settings.range === "all") { startDay = data.period_start; endDay = data.period_end; }
  else if (settings.range === "custom") {
    startDay = clamp(settings.startDay || startDay);endDay = clamp(settings.endDay || startDay);
    if (endDay < startDay) [startDay, endDay] = [endDay, startDay];
  } else {
    const preset = concurrencyRangePresets.find(([key]) => key === settings.range);
    if (preset) { endDay = data.period_end;startDay = clamp(shiftCalendarDay(endDay, 1 - preset[1])); }
  }
  return { startDay, endDay, bounds: [zonedDayBounds(startDay, data.timezone)[0], zonedDayBounds(endDay, data.timezone)[1]] };
}

export function normalizeConcurrencySettings(value: unknown): ConcurrencySettings {
  const saved = value && typeof value === "object" ? value as Partial<ConcurrencySettings> : {};
  const zoom = Array.isArray(saved.zoom) && saved.zoom.length === 2 && saved.zoom.every(Number.isFinite) && saved.zoom[1] > saved.zoom[0]
    ? [saved.zoom[0], saved.zoom[1]] as Interval : null;
  return {
    mode: saved.mode === "model" ? "model" : "user",
    counting: saved.counting === "agent" ? "agent" : "workspace",
    agent: typeof saved.agent === "string" ? saved.agent : "",
    gap: [0, 15, 30, 60].includes(saved.gap as number) ? saved.gap! : 60,
    repository: typeof saved.repository === "string" ? saved.repository : "",
    day: validDay(saved.day),
    range: saved.range === "today" || saved.range === "day" || saved.range === "all" || saved.range === "custom" || concurrencyRangePresets.some(([key]) => key === saved.range) ? saved.range! : "day",
    startDay: validDay(saved.startDay), endDay: validDay(saved.endDay),
    workspace: typeof saved.workspace === "string" ? saved.workspace : "",
    zoom, showConversations: saved.showConversations === true,
    smoothingMinutes: validRollingPeriods(saved.smoothingMinutes) ? saved.smoothingMinutes : 0,
  };
}

export function readConcurrencySettings(overrides: Partial<ConcurrencySettings> = {}): ConcurrencySettings {
  let source: unknown = preferences().get(concurrencySettingsKey, defaultConcurrencySettings);
  if (typeof location !== "undefined" && location.pathname === "/analyze") {
    const encoded = new URLSearchParams(location.search).get("analyze_parallelism");
    if (encoded !== null) {
      // A linked view starts from product defaults, including for fields the
      // agent omitted. Never mix another viewer's saved assumptions into it.
      try { source=JSON.parse(encoded); } catch { source=defaultConcurrencySettings; }
    }
  }
  return normalizeConcurrencySettings({...normalizeConcurrencySettings(source),...overrides});
}

export function readMachineConcurrencySettings(): ConcurrencySettings {
  const captured = new URLSearchParams(location.search).get("q_machine_parallelism");
  if (captured !== null) {
    try { return normalizeConcurrencySettings(JSON.parse(captured)); }
    catch { return normalizeConcurrencySettings(null); }
  }
  return { ...readConcurrencySettings(), zoom: null, workspace: "", agent: "" };
}

export function saveConcurrencySettings(next: ConcurrencySettings): void {
  preferences().set(concurrencySettingsKey, normalizeConcurrencySettings(next));
  window.dispatchEvent(new Event("pharos:concurrency-settings"));
}

// A deliberate change in a linked view updates only the chosen defaults;
// opening a link and its other overrides never overwrite library preferences.
export function saveConcurrencyChanges(change: Partial<ConcurrencySettings>): void {
  const saved=normalizeConcurrencySettings(preferences().get(concurrencySettingsKey,defaultConcurrencySettings));
  saveConcurrencySettings(normalizeConcurrencySettings({...saved,...change}));
}
