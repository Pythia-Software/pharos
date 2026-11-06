import { fittedCoverHeight, type CardLoadingSpec } from "./card-loading-model";
import { initiatorLabels } from "./habits-analysis-catalog";
import {
  defaultHabitsSettings, formatDay, formatGap, formatHours, habitsSettingsKey, healthStats, normalizeHabitsSettings, punchcardViews, recentWork, streakActors, streakDays, todayLabel,
  type HabitsMode, type HabitsSummary, type PunchcardView, type StreakActor,
} from "./habits-model";
import { preferences } from "./preferences";
import type { QueryCategory } from "./query-categories";
import type { QueryPreview } from "./query-previews";
import { readViewState } from "./view-state";

// The Habits page's sections as Home cards. The page's address (its habits_*
// view fields) is the card definition, as for any dashboard card; a card
// reads /api/habits as the page does and draws its section's chart.

export const habitsCardTargets = ["habits-streaks", "habits-calendar", "habits-punchcard", "habits-days", "habits-pace", "habits-automations"] as const;
export type HabitsCardTarget = typeof habitsCardTargets[number];
// A card is about a fifth of the page's width: its calendar draws the most
// recent weeks that stay legible there, and opens the full range.
export const cardCalendarWeeks = 17;
export const isHabitsCardTarget = (target: string): target is HabitsCardTarget => (habitsCardTargets as readonly string[]).includes(target);

// What a Habits cover draws: one of the page's charts, at card width.
export type HabitsCover =
  | { chart: "calendar"; summary: HabitsSummary; actor: StreakActor; weeks: number; weekends: boolean }
  | { chart: "punchcard"; summary: HabitsSummary; view: PunchcardView }
  | { chart: "days"; summary: HabitsSummary; count: number };

export type HabitsCardView = { mode: HabitsMode; weeks: number; calendar: StreakActor; weekends: boolean; punchcard: PunchcardView; range: string; shapeDays: number };
export function readHabitsCardView(params: URLSearchParams): HabitsCardView {
  return {
    mode: readViewState<HabitsMode>("habits_streak", "all", params), weeks: Number(readViewState("habits_weeks", "53", params)),
    calendar: readViewState<StreakActor>("habits_calendar", "you", params), weekends: readViewState<string>("habits_weekends", "show", params) !== "hide",
    punchcard: readViewState<PunchcardView>("habits_punchcard", "you", params), range: readViewState("habits_punchcard_days", "90", params),
    shapeDays: Number(readViewState("habits_shape_days", "28", params)),
  };
}

const actorLabel = (actor: StreakActor) => streakActors.find(item => item.id === actor)!.label;
const viewLabel = (view: PunchcardView) => punchcardViews.find(([id]) => id === view)![1];
const rangeLabel = (range: string) => range === "all" ? "All time" : range === "365" ? "Past year" : `Past ${range} days`;

// Standalone names describe the chart without needing the Habits page around it.
// The page, library and creation dialog share these readings of the controls.
export function habitsCardTitle(target: HabitsCardTarget, view: HabitsCardView): string {
  switch (target) {
    case "habits-streaks": return view.mode === "weekdays" ? "Your weekday activity streak" : "Your daily activity streak";
    case "habits-calendar": return `${view.calendar === "you" ? "Your active days" : view.calendar === "agents" ? "Days with agents you started" : "Days with automatic agent runs"}${view.weekends ? "" : " · weekdays only"}`;
    case "habits-punchcard": return view.punchcard === "you" ? "When you work" : view.punchcard === "agents" ? "When agents you started work" : view.punchcard === "unattended" ? "When agents work while you're away" : view.punchcard === "automated" ? "When automatic agents run" : "Agent time minus your engaged time by weekday and hour";
    case "habits-days": return "Your daily work schedule";
    case "habits-pace": return "Your work and rest patterns";
    case "habits-automations": return "Automatic agent runs";
  }
}

export function habitsCardDescription(target: HabitsCardTarget, view: HabitsCardView): string {
  switch (target) {
    case "habits-streaks": return `Days in a row with a message you typed${view.mode === "weekdays" ? ", counting weekdays only" : ""}; your longest run; and your engaged time yesterday and over the last seven days.`;
    case "habits-calendar": return `${habitsCardWindow(target, view)}${view.weekends ? "" : ", weekdays only"}. Each square shows ${view.calendar === "you" ? "your engaged time" : `time with ${actorLabel(view.calendar).toLowerCase()}`}; darker is more time.${view.weeks > cardCalendarWeeks ? ` Open the dashboard for all ${view.weeks} weeks.` : ""}`;
    case "habits-punchcard": return `${rangeLabel(view.range)}. ${view.punchcard === "difference" ? "Agent time minus your engaged time" : `Time for ${view.punchcard === "you" ? "you" : viewLabel(view.punchcard).toLowerCase()}`} in each weekday hour, in minutes per week, coloured by time of day.`;
    case "habits-days": return `Each of the last ${view.shapeDays} days, from your first engaged minute to your last, with the blocks you were engaged coloured by time of day.`;
    case "habits-pace": return "Days off, your typical engaged day, agent time while you're away, and focus, over the last four weeks.";
    case "habits-automations": return `${rangeLabel(view.range)}. What started agents without a message from you: schedules, scripts, orchestrators and other agents, by runs.`;
  }
}

export function habitsCardWindow(target: HabitsCardTarget, view: HabitsCardView): string {
  switch (target) {
    case "habits-streaks": return "Today";
    case "habits-calendar": return `Past ${Math.min(view.weeks, cardCalendarWeeks)} weeks`;
    case "habits-punchcard": case "habits-automations": return rangeLabel(view.range);
    case "habits-days": return `Past ${view.shapeDays} days`;
    default: return "Past 28 days";
  }
}

export function habitsCardPresentation(target: HabitsCardTarget, view: HabitsCardView): { category: QueryCategory; loading: CardLoadingSpec } {
  const agents = target === "habits-automations" || target === "habits-calendar" && view.calendar !== "you" || target === "habits-punchcard" && view.punchcard !== "you";
  const kind: CardLoadingSpec["kind"] = target === "habits-streaks" || target === "habits-pace" || target === "habits-automations" ? "list" : "columns";
  const count = target === "habits-automations" ? 6 : kind === "list" ? 4 : target === "habits-days" ? view.shapeDays : 24;
  return { category: agents ? "machines" : "human", loading: { kind, count, height: target === "habits-calendar" ? view.weekends ? 130 : 105 : target === "habits-punchcard" ? 115 : target === "habits-days" ? 160 : fittedCoverHeight(kind, count, kind === "list" ? { belowLabels: true, labelLines: 1 } : {}) } };
}

// The summary the page would draw for this address, with the saved settings.
export async function loadHabitsSummary(view: HabitsCardView, signal?: AbortSignal): Promise<HabitsSummary> {
  const settings = normalizeHabitsSettings(preferences().get<unknown>(habitsSettingsKey, defaultHabitsSettings));
  const query = new URLSearchParams({ mode: view.mode, days: view.range === "all" ? "0" : view.range, habits_settings: JSON.stringify(settings) });
  const response = await fetch(`/api/habits?${query}`, { signal });
  if (!response.ok) throw new Error(`Could not read your habits (${response.status})`);
  return response.json() as Promise<HabitsSummary>;
}

export function habitsCardPreview(target: HabitsCardTarget, view: HabitsCardView, summary: HabitsSummary): QueryPreview {
  const base = { action: "Explore this dashboard", summary: habitsCardTitle(target, view), total: summary.days.length };
  const percent = (value?: number) => value === undefined ? "—" : `${Math.round(value * 100)}%`;
  switch (target) {
    case "habits-streaks": {
      const streak = summary.streaks.you, work = recentWork(summary.days);
      return { ...base, state: "ready", insights: [
        { label: "Your streak", value: streakDays(streak.current), context: todayLabel(streak) },
        { label: "Longest streak", value: streakDays(streak.longest), context: streak.longest_from ? `${formatDay(streak.longest_from)} – ${formatDay(streak.longest_to ?? streak.longest_from)}` : "" },
        { label: "Yesterday", value: formatHours(work.yesterdayMs), context: "engaged" },
        { label: "Last 7 days", value: formatHours(work.weekMs), context: "engaged, today included" },
      ] };
    }
    case "habits-pace": {
      const health = healthStats(summary.days, 28);
      return { ...base, state: health.days ? "ready" : "empty", headline: "The last four weeks", insights: [
        { label: "Days off", value: String(health.restDays), context: `longest run without one: ${streakDays(health.longestWithoutRest)}` },
        { label: "Typical day", value: health.typicalHours === undefined ? "—" : `${health.typicalHours.toFixed(1)} h`, context: "median engaged time on days you worked" },
        { label: "Agents while you're away", value: health.leverage === undefined ? "—" : `${health.leverage.toFixed(1)}×`, context: "unattended agent hours per engaged hour" },
        { label: "Focus", value: percent(health.focusShare), context: "of engaged time in blocks of 25 min or more" },
      ] };
    }
    case "habits-automations": {
      const runs = summary.automations.slice().sort((a, b) => b.runs - a.runs).slice(0, 6);
      return { ...base, state: runs.length ? "ready" : "empty", emptyLabel: "No automatic agent runs in this range",
        note: "Runs by what started them, without a message from you.",
        ranking: runs.map((item, index) => ({ label: `${item.label} · ${initiatorLabels[item.initiator] ?? item.initiator} · ${formatGap(item.median_gap_ms)}`, value: item.runs, color: "var(--heat-1)" })) };
    }
    case "habits-calendar":
      return { ...base, state: summary.days.some(day => day.qualifies[view.calendar]) ? "ready" : "empty", emptyLabel: "No active days yet",
        note: `${streakActors.find(actor => actor.id === view.calendar)!.rule(summary.settings)}${view.weeks > cardCalendarWeeks ? ` The card shows the last ${cardCalendarWeeks} weeks; open it for ${view.weeks === 53 ? "the year" : `all ${view.weeks}`}.` : ""}`,
        coverHeight: view.weekends ? 130 : 105, habits: { chart: "calendar", summary, actor: view.calendar, weeks: Math.min(view.weeks, cardCalendarWeeks), weekends: view.weekends } };
    case "habits-punchcard":
      return { ...base, state: "ready", note: `Minutes per week in each hour, ${formatDay(summary.punchcard.from)} to ${formatDay(summary.punchcard.to)}. Colours are the time of day.`,
        coverHeight: 115, habits: { chart: "punchcard", summary, view: view.punchcard } };
    case "habits-days":
      return { ...base, state: summary.days.slice(-view.shapeDays).some(day => day.you.first !== undefined) ? "ready" : "empty", emptyLabel: `No engaged time in the last ${view.shapeDays} days`,
        note: "From your first engaged minute to your last; solid pieces are when you were engaged, coloured by time of day.",
        coverHeight: 160, habits: { chart: "days", summary, count: view.shapeDays } };
  }
}
