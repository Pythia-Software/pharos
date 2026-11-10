import { localeDate, localeDateTime } from "./locale-format";
import { hourOfDayColor } from "./chart-colors";

// The Habits panel's model, without a DOM: the service's summary
// (internal/archive/habits.go), laid out as calendars, a punchcard, a workday
// shape and health figures. Streaks and qualifying days come from the
// service, so the page, the menu bar and MCP agree; this module only draws.

export const habitsSettingsKey = "pharos-habits-v1";
export type HabitsMode = "all" | "weekdays";
export type HabitsSettings = { vacations: string[]; youMessages: number; agentMinutes: number; engagedGap: number; dayStart: number };
export const defaultHabitsSettings: HabitsSettings = { vacations: [], youMessages: 1, agentMinutes: 10, engagedGap: 15, dayStart: 0 };
export const engagedGaps = [5, 10, 15, 30];

type ActorDay = { elapsed_ms: number; active_ms: number; turns: number };
export type HabitsDay = {
  day: string; weekday: number; vacation?: boolean; excused?: boolean;
  you: { engaged_ms: number; messages: number; typed_words: number; first?: number; last?: number; focus_blocks: number; focus_ms: number; blocks?: Array<[number, number]> };
  with_you: ActorDay; unattended: ActorDay; automated: ActorDay;
  // Wall time with any agent you started active; automatic runs never share it.
  agents_ms: number; parallel_ms: number; peak_agents: number; qualifies: Record<StreakActor, boolean>;
};
export type StreakActor = "you" | "agents" | "automated";
export type HabitsStreak = { current: number; current_from?: string; longest: number; longest_from?: string; longest_to?: string; today: "done" | "pending" | "excused" };
export type HabitsAutomation = { initiator: string; label: string; runs: number; active_days: number; agent_hours: number; first_run: number; last_run: number; median_gap_ms: number; missed: number; daily: Record<string, number> };
export type PunchcardActor = "you" | "with_you" | "unattended" | "automated";
export type HabitsSummary = {
  generated_at: string; timezone: string; today: string; period_start: string; mode: HabitsMode; settings: HabitsSettings;
  days: HabitsDay[]; streaks: Record<StreakActor, HabitsStreak>;
  windows: Array<{ days: number; active: Partial<Record<StreakActor, number>>; considered: number }>;
  // weeks: how many times the range holds each weekday × hour slot.
  punchcard: { from: string; to: string; weeks: number[][]; cells: Record<PunchcardActor, number[][]> };
  automations: HabitsAutomation[]; coverage: Record<string, number>;
};

// Agents you started answer a message you sent; automatic runs were started
// by a schedule, a script (claude -p, codex exec…), an orchestrator such as
// TL1, or another agent, with no message from you.
export const streakActors: Array<{ id: StreakActor; label: string; rule: (settings: HabitsSettings) => string }> = [
  { id: "you", label: "You", rule: settings => `A day counts with ${settings.youMessages === 1 ? "a message" : `${settings.youMessages} messages`} you typed. Agents working without you never count.` },
  { id: "agents", label: "Agents you started", rule: settings => `Agents answering a message you sent, while you were there or after you left. A day counts once any of them has been working for ${settings.agentMinutes} minutes, counting overlapping agents once.` },
  { id: "automated", label: "Automatic agent runs", rule: () => "Agents started without a message from you: by a schedule, a script such as claude -p or codex exec, an orchestrator such as TL1, or another agent. A day counts with any run." },
];

// The saved settings, with what is invalid replaced by defaults: the same
// rules as the service's normalizeHabitsSettings.
export function normalizeHabitsSettings(value: unknown): HabitsSettings {
  const saved = value && typeof value === "object" ? value as Record<string, unknown> : {};
  const whole = (key: string, low: number, high: number) => { const item = saved[key]; return typeof item === "number" && Number.isInteger(item) && item >= low && item <= high ? item : undefined; };
  const gap = whole("engagedGap", 0, 60);
  const vacations = Array.isArray(saved.vacations) ? [...new Set(saved.vacations.filter((day): day is string => typeof day === "string" && validDay(day)))].sort().slice(0, 1000) : [];
  return {
    vacations, youMessages: whole("youMessages", 1, 1000) ?? defaultHabitsSettings.youMessages, agentMinutes: whole("agentMinutes", 1, 24 * 60) ?? defaultHabitsSettings.agentMinutes,
    engagedGap: gap !== undefined && engagedGaps.includes(gap) ? gap : defaultHabitsSettings.engagedGap,
    dayStart: whole("dayStart", 0, 6) ?? defaultHabitsSettings.dayStart,
  };
}
export function validDay(value: string) {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(value)) return false;
  const instant = Date.parse(`${value}T00:00:00Z`);
  return Number.isFinite(instant) && new Date(instant).toISOString().slice(0, 10) === value;
}
export const toggleVacation = (settings: HabitsSettings, day: string): HabitsSettings => ({ ...settings,
  vacations: settings.vacations.includes(day) ? settings.vacations.filter(other => other !== day) : [...settings.vacations, day].sort() });

export const shiftDay = (day: string, offset: number) => { const date = new Date(`${day}T00:00:00Z`); date.setUTCDate(date.getUTCDate() + offset); return date.toISOString().slice(0, 10); };

// What a calendar shades for each actor: your engaged time, your agents' wall
// time, automation's wall time. A day that does not qualify is level 0.
export function calendarValue(day: HabitsDay, actor: StreakActor) {
  if (!day.qualifies[actor]) return 0;
  return actor === "you" ? day.you.engaged_ms : actor === "agents" ? day.agents_ms : day.automated.elapsed_ms;
}

// Four levels from the quartiles of the non-zero values, so one huge day does
// not wash out the rest; with fewer than eight, equal quarters of the largest.
export function quartileLevels(values: number[]): number[] {
  const active = values.filter(value => value > 0).sort((a, b) => a - b);
  if (!active.length) return [Infinity, Infinity, Infinity];
  if (active.length < 8) { const top = active.at(-1)!; return [top / 4, top / 2, top * 3 / 4]; }
  const at = (q: number) => active[Math.min(active.length - 1, Math.floor(q * active.length))];
  return [at(0.25), at(0.5), at(0.75)];
}
export const level = (value: number, thresholds: number[]) => value <= 0 ? 0 : 1 + thresholds.filter(threshold => value > threshold).length;

// Weeks as columns of seven days, Monday first, ending with today's week.
// Days before the period or after today are null.
export function calendarWeeks(days: HabitsDay[], weeks: number): Array<Array<HabitsDay | null>> {
  if (!days.length) return [];
  const byDay = new Map(days.map(day => [day.day, day])), today = days.at(-1)!;
  const monday = shiftDay(today.day, -today.weekday), first = shiftDay(monday, -7 * (weeks - 1));
  return Array.from({ length: weeks }, (_, week) => Array.from({ length: 7 }, (_, weekday) => byDay.get(shiftDay(first, week * 7 + weekday)) ?? null));
}
// The month a column starts, for its label: only where it changes.
export const monthLabels = (columns: Array<Array<HabitsDay | null>>) => columns.map((column, index) => {
  const day = column.find(Boolean)?.day, previous = index ? columns[index - 1].find(Boolean)?.day : undefined;
  return day && (!previous || previous.slice(0, 7) !== day.slice(0, 7)) ? localeDateTime(new Date(`${day}T00:00:00Z`), { month: "short", timeZone: "UTC" }, "en-US") : "";
});

export type PunchcardView = PunchcardActor | "agents" | "difference";
export const punchcardViews: Array<[PunchcardView, string]> = [["you", "You"], ["agents", "Agents you started"], ["unattended", "Agents you started, while you're away"], ["automated", "Automatic agent runs"], ["difference", "Agents you started, minus you"]];
// Minutes per week in each weekday × hour slot: the slot's time over the
// range, divided by how many times the range holds that slot.
export function punchcardGrid(summary: Pick<HabitsSummary, "punchcard">, view: PunchcardView): number[][] {
  const { cells, weeks } = summary.punchcard;
  const grid = (actor: PunchcardActor) => cells[actor] ?? Array.from({ length: 7 }, () => Array(24).fill(0));
  const perWeek = (values: number[][]) => values.map((hours, weekday) => hours.map((value, hour) => weeks[weekday]?.[hour] ? value / weeks[weekday][hour] / 60_000 : 0));
  const sum = (...actors: PunchcardActor[]) => perWeek(grid(actors[0]).map((hours, weekday) => hours.map((_, hour) => actors.reduce((total, actor) => total + grid(actor)[weekday][hour], 0))));
  if (view === "agents") return sum("with_you", "unattended");
  if (view === "difference") { const agents = sum("with_you", "unattended"), you = sum("you"); return agents.map((hours, weekday) => hours.map((value, hour) => value - you[weekday][hour])); }
  return sum(view);
}

// One column per recent day: when your engaged time started and ended, in
// hours after the day's start, and the engaged blocks between, cut at local
// hours so each piece takes its hour's colour. Pieces are placed by the clock
// and last the time that passed, so a skipped or repeated hour at a
// daylight-saving change draws where the work was.
export function workdayShape(days: HabitsDay[], count: number, dayStart: number, zone?: string) {
  const hourOf = (at: number, day: string) => {
    const parts = new Intl.DateTimeFormat("en-CA", { timeZone: zone, year: "numeric", month: "2-digit", day: "2-digit", hour: "numeric", minute: "numeric", second: "numeric", hourCycle: "h23" }).formatToParts(at);
    const get = (type: string) => Number(parts.find(part => part.type === type)?.value);
    const date = `${parts.find(part => part.type === "year")!.value}-${parts.find(part => part.type === "month")!.value}-${parts.find(part => part.type === "day")!.value}`;
    // Hours after the day starts; a later local date continues past 24.
    const offset = Math.round((Date.parse(`${date}T00:00:00Z`) - Date.parse(`${day}T00:00:00Z`)) / 86_400_000);
    return offset * 24 + get("hour") + get("minute") / 60 + get("second") / 3600 + (at % 1000) / 3_600_000 - dayStart;
  };
  const pieces = (blocks: Array<[number, number]>, day: string) => blocks.flatMap(([from, to]) => {
    const cut: Array<{ start: number; end: number; color: string }> = [];
    // Walk real time to each local hour's end; offsets change only there.
    for (let at = from; at < to;) {
      const start = hourOf(at, day), hour = Math.floor(start + 1e-9), next = Math.min(to, at + Math.max(0, hour + 1 - start) * 3_600_000);
      cut.push({ start, end: start + (next - at) / 3_600_000, color: hourOfDayColor(((hour + dayStart) % 24 + 24) % 24) });
      at = next;
    }
    return cut;
  });
  return days.slice(-count).map(day => {
    const blocks = pieces(day.you.blocks ?? [], day.day);
    const first = day.you.first !== undefined ? hourOf(day.you.first, day.day) : undefined, last = day.you.last !== undefined ? hourOf(day.you.last, day.day) : undefined;
    return { day: day.day, weekday: day.weekday, vacation: Boolean(day.vacation),
      start: blocks.length ? Math.min(...blocks.map(piece => piece.start)) : first, end: blocks.length ? Math.max(...blocks.map(piece => piece.end)) : last,
      blocks, blockCount: day.you.blocks?.length ?? 0,
      engagedHours: day.you.engaged_ms / 3_600_000, unattendedHours: day.unattended.elapsed_ms / 3_600_000 };
  });
}

// Pace over the last `window` days, today included: days you did not work
// (vacations included), your typical (median) engaged time on days you
// worked, agent time while you were away per engaged hour, and the share of
// engaged time in focus blocks.
export function healthStats(days: HabitsDay[], window = 28) {
  const recent = days.slice(-window), total = (pick: (day: HabitsDay) => number) => recent.reduce((sum, day) => sum + pick(day), 0);
  const engaged = total(day => day.you.engaged_ms);
  let longestWithoutRest = 0, run = 0;
  for (const day of recent.slice(0, -1)) { run = day.qualifies.you && !day.vacation ? run + 1 : 0; longestWithoutRest = Math.max(longestWithoutRest, run); }
  return {
    days: recent.length,
    restDays: recent.slice(0, -1).filter(day => !day.qualifies.you || day.vacation).length,
    longestWithoutRest,
    typicalHours: median(recent.filter(day => day.qualifies.you && day.you.engaged_ms > 0).map(day => day.you.engaged_ms / 3_600_000)),
    leverage: engaged ? total(day => day.unattended.elapsed_ms) / engaged : undefined,
    focusShare: engaged ? total(day => day.you.focus_ms) / engaged : undefined,
    engagedHours: engaged / 3_600_000,
  };
}

// Your engaged time yesterday and over the last seven days, today included.
export function recentWork(days: HabitsDay[]) {
  return { yesterdayMs: days.at(-2)?.you.engaged_ms ?? 0, weekMs: days.slice(-7).reduce((sum, day) => sum + day.you.engaged_ms, 0) };
}
const median = (values: number[]) => { if (!values.length) return undefined; const sorted = [...values].sort((a, b) => a - b), middle = sorted.length >> 1; return sorted.length % 2 ? sorted[middle] : (sorted[middle - 1] + sorted[middle]) / 2; };
export const streakDays = (count: number) => `${count.toLocaleString()} ${count === 1 ? "day" : "days"}`;
export const todayLabel = (streak: HabitsStreak) => streak.today === "done" ? "Today counts" : streak.today === "excused" ? "Today is a day off" : "Today: not yet";
export function formatHours(ms: number) {
  const minutes = Math.round(ms / 60_000);
  return minutes < 60 ? `${minutes} min` : `${(minutes / 60).toFixed(minutes < 600 ? 1 : 0)} h`;
}
export function formatGap(ms: number) {
  if (!ms) return "—";
  const minutes = ms / 60_000;
  return minutes < 1 ? "under a minute apart" : minutes < 90 ? `every ${Math.round(minutes)} min` : minutes < 36 * 60 ? `every ${(minutes / 60).toFixed(minutes < 600 ? 1 : 0)} h` : `every ${(minutes / 1440).toFixed(1)} days`;
}
export const formatDay = (day: string) => localeDate(new Date(`${day}T00:00:00Z`), { weekday: "short", month: "short", day: "numeric", year: "numeric", timeZone: "UTC" }, "en-US");
// Clock times as 9 AM or 1:15 PM; hours past 24 wrap to the next day.
export const formatClock = (hours: number) => {
  const total = Math.round(hours * 60), h = ((Math.floor(total / 60) % 24) + 24) % 24, m = ((total % 60) + 60) % 60;
  return `${h % 12 || 12}${m ? `:${String(m).padStart(2, "0")}` : ""} ${h < 12 ? "AM" : "PM"}`;
};
