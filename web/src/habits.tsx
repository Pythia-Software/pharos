import { EMPTY_QUERY, loadSchema } from "@pythia-software/query-table-core";
import { schemaDocuments } from "./schema-documents";
import { habitsQueryContext } from "./dashboard-query-adapters";
import { createPortal } from "react-dom";
import { UsageDashboardLinks, useDashboardHeadline } from "./usage-workspace";
import { habitsAutomationsLink } from "./habits-automations-query";
import React, { createContext, useContext, useEffect, useMemo, useState } from "react";
import { DashboardSelect } from "./dashboard-select";
import { ChartSelect } from "./usage-chart";
import { preferences } from "./preferences";
import { useViewState } from "./view-state";
import { ViewDisclosure } from "./view-disclosure";
import { DashboardResultActions } from "./create-dashboard-card";
import { CalendarChart, DaysChart, PunchcardChart } from "./habits-charts";
import { habitsCardTitle, habitsViews, habitsViewTarget, readHabitsView, readHabitsCardView, type HabitsView, type HabitsCardTarget, type HabitsCardView } from "./habits-card";
import {
  defaultHabitsSettings, engagedGaps, formatClock, formatDay, formatHours, habitsSettingsKey, healthStats,
  normalizeHabitsSettings, recentWork, punchcardViews, streakActors, streakDays, todayLabel, toggleVacation, validDay,
  type HabitsDay, type HabitsMode, type HabitsSettings, type HabitsSummary, type PunchcardView, type StreakActor,
} from "./habits-model";
import "./habits.css";

// Habits: streaks, calendars, a punchcard, the shape of your days and the
// automations that run without you, from the service's summary
// (/api/habits, internal/archive/habits.go). Work Hours explores the same
// records (rhythm) by actor and hour in the standard catalog dashboard.

const savedSettings = () => normalizeHabitsSettings(preferences().get<unknown>(habitsSettingsKey, defaultHabitsSettings));
const saveSettings = (settings: HabitsSettings) => preferences().set(habitsSettingsKey, settings);
const HabitsViewContext = createContext<HabitsCardView>(readHabitsCardView(new URLSearchParams()));

function useHabits(mode: HabitsMode, days: number) {
  const [settings, setSettings] = useState(savedSettings);
  const [state, setState] = useState<{ data?: HabitsSummary; error?: string; loading: boolean }>({ loading: true });
  const [revision, setRevision] = useState(0);
  useEffect(() => {
    const saved = (event: Event) => { if (Object.hasOwn((event as CustomEvent<{ set?: object }>).detail?.set ?? {}, habitsSettingsKey)) setSettings(savedSettings()); };
    const refresh = () => setRevision(value => value + 1);
    window.addEventListener("pharos:preferences-saved", saved);
    window.addEventListener("pharos:usage-refresh", refresh);
    return () => { window.removeEventListener("pharos:preferences-saved", saved); window.removeEventListener("pharos:usage-refresh", refresh); };
  }, []);
  useEffect(() => {
    let active = true;
    setState(previous => ({ ...previous, loading: true }));
    // The settings travel with the request, so a change draws before the save lands.
    const query = new URLSearchParams({ mode, days: String(days), habits_settings: JSON.stringify(settings) });
    fetch(`/api/habits?${query}`).then(async response => {
      if (!response.ok) throw new Error(`Could not read your habits (${response.status})`);
      return response.json() as Promise<HabitsSummary>;
    }).then(data => { if (active) setState({ data, loading: false }); }, error => { if (active) setState({ error: error instanceof Error ? error.message : String(error), loading: false }); });
    return () => { active = false; };
  }, [mode, days, settings, revision]);
  const update = (next: HabitsSettings) => { setSettings(next); saveSettings(next); };
  return { ...state, settings, update };
}

// One set of URL-backed controls chooses the chart. Summary cards keep their
// place above and below it, and share its summary request.
export function HabitsDashboard() {
  const headline = useDashboardHeadline();
  const [chart, setChart] = useViewState<HabitsView>("habits_view", readHabitsView(new URLSearchParams(location.search)));
  const [mode, setMode] = useViewState<HabitsMode>("habits_streak", "all");
  const [weeks, setWeeks] = useViewState<string>("habits_weeks", "53");
  const [calendar, setCalendar] = useViewState<StreakActor>("habits_calendar", "you");
  const [weekends, setWeekends] = useViewState<string>("habits_weekends", "show");
  const [view, setView] = useViewState<PunchcardView>("habits_punchcard", "you");
  const [punchcardDays, setPunchcardDays] = useViewState<string>("habits_punchcard_days", "90");
  const [shapeDays, setShapeDays] = useViewState<string>("habits_shape_days", "28");
  const [selected, setSelected] = useViewState<string>("habits_day", "");
  const { data, error, loading, settings, update } = useHabits(mode, punchcardDays === "all" ? 0 : Number(punchcardDays));
  const day = validDay(selected) ? data?.days.find(item => item.day === selected) : undefined;
  const cardView: HabitsCardView = { mode, weeks: Number(weeks), calendar, weekends: weekends !== "hide", punchcard: view, range: punchcardDays, shapeDays: Number(shapeDays) };
  const target = habitsViewTarget(chart);
  return <HabitsViewContext.Provider value={cardView}>
    {headline ? createPortal(<div className="usage-chart-title usage-chart-headline"><h2>{habitsCardTitle(target, cardView)}</h2><DashboardResultActions target={target} /></div>, headline) : null}
    <section className="usage-habits-dashboard habits-panel" aria-label="Habits" aria-busy={loading}>
      <div className="usage-chart-controls habits-controls">
        <ChartSelect label="View" value={chart} onChange={setChart} options={habitsViews} />
        <ChartSelect label="Streaks count" value={mode} onChange={setMode} options={[["all", "Every day"], ["weekdays", "Weekdays only"]]} />
        {chart === "calendar" ? <>
          <ChartSelect label="Whose days" value={calendar} onChange={setCalendar} options={streakActors.map(actor => [actor.id, actor.label])} />
          <ChartSelect label="Range" ariaLabel="Calendar range" value={weeks} onChange={setWeeks} options={[["13", "Last 13 weeks"], ["26", "Last 26 weeks"], ["53", "Last year"]]} />
          <ChartSelect label="Weekends" value={weekends} onChange={setWeekends} options={[["show", "Show Saturday and Sunday"], ["hide", "Weekdays only"]]} />
        </> : chart === "punchcard" ? <>
          <ChartSelect label="Whose time" ariaLabel="Punchcard time" value={view} onChange={setView} options={punchcardViews} />
          <ChartSelect label="Range" ariaLabel="Punchcard range" value={punchcardDays} onChange={setPunchcardDays} options={punchcardRanges} />
        </> : <ChartSelect label="Range" ariaLabel="Your days range" value={shapeDays} onChange={setShapeDays} options={shapeRanges} />}
      </div>
      {!data ? error ? <p className="query-table-error" role="alert">{error}</p> : <p className="muted" role="status">Loading your habits…</p> : <>
        <div><Heading target="habits-streaks" /><Streaks data={data} settings={settings} /></div>
        {chart === "calendar" ? <div className="habits-card" data-pharos-target="section:habits-calendar">
          <Heading target="habits-calendar" primary />
          <p className="muted">{streakActors.find(actor => actor.id === calendar)?.rule(settings)} Each square is a day; darker is more time. Click a day for its details, or to mark it as a vacation day.</p>
          <div className="habits-calendar-row">
            <CalendarChart data={data} actor={calendar} weeks={Number(weeks)} weekends={weekends !== "hide"} selected={day?.day ?? ""} onSelect={setSelected} />
            <DayDetail data={data} day={day} settings={settings} onSelect={setSelected} onVacation={() => day && update(toggleVacation(settings, day.day))} />
          </div>
        </div> : chart === "punchcard" ? <Punchcard data={data} view={view} /> : <WorkdayShape data={data} days={Number(shapeDays)} />}
        <Health data={data} />
        <p className="usage-dashboard-note">Your time is estimated from the messages you typed: each one adds its typing time, and messages within {settings.engagedGap} minutes join one engaged block. Agent turns are split by whether you were engaged and by who started them. {data.coverage.unmatched_turns ? `${data.coverage.unmatched_turns.toLocaleString()} of ${data.coverage.turns.toLocaleString()} turns have no prompt to read who started them; they count as unattended.` : ""}</p>
        <UsageDashboardLinks dataset="rhythm" schema={loadSchema(schemaDocuments.rhythm)} query={EMPTY_QUERY} context={habitsQueryContext(data, chart, cardView, day?.day)} extra={habitsAutomationsLink(data, new URLSearchParams(location.search))} />
        <HabitsSettingsPanel settings={settings} onChange={update} />
      </>}
    </section>
  </HabitsViewContext.Provider>;
}

// A section's heading, with the standard control that makes it a Home card.
function Heading({ target, primary = false }: { target: HabitsCardTarget; primary?: boolean }) {
  const view = useContext(HabitsViewContext), headline = useDashboardHeadline();
  if (primary && headline) return null;
  return <div className="habits-card-title"><h3>{habitsCardTitle(target, view)}</h3><DashboardResultActions target={target} /></div>;
}

// Your streak only: agents and automatic runs have their calendars below.
function Streaks({ data, settings }: { data: HabitsSummary; settings: HabitsSettings }) {
  const streak = data.streaks.you, work = recentWork(data.days);
  return <div className="mcp-metrics dashboard-stats habits-streaks" aria-label="Streaks" data-pharos-target="section:habits-streaks">
    <div className={`mcp-metric habits-streak habits-today-${streak.today}`} title={streakActors[0].rule(settings)}><span>Your streak</span><strong>{streakDays(streak.current)}</strong><small>{todayLabel(streak)}</small></div>
    <div className="mcp-metric"><span>Longest streak</span><strong>{streakDays(streak.longest)}</strong><small>{streak.longest_from ? `${formatDay(streak.longest_from)} – ${formatDay(streak.longest_to ?? streak.longest_from)}` : "No working days yet"}</small></div>
    <div className="mcp-metric"><span>Yesterday</span><strong>{formatHours(work.yesterdayMs)}</strong><small>engaged</small></div>
    <div className="mcp-metric"><span>Last 7 days</span><strong>{formatHours(work.weekMs)}</strong><small>engaged, today included</small></div>
  </div>;
}

// The selected day, beside the calendar: your time, agents' time, and the
// vacation switch.
function DayDetail({ data, day, settings, onSelect, onVacation }: { data: HabitsSummary; day?: HabitsDay; settings: HabitsSettings; onSelect: (day: string) => void; onVacation: () => void }) {
  const vacation = Boolean(day && settings.vacations.includes(day.day));
  return <aside className="habits-day" aria-label={day ? `Details for ${formatDay(day.day)}` : "Day details"}>
    <input type="date" aria-label="Day" data-view-field="habits_day" value={day?.day ?? ""} min={data.period_start} max={data.today} onChange={event => onSelect(event.target.value)} />
    {day ? <>
      <dl>
        <dt>You</dt><dd>{formatHours(day.you.engaged_ms)}{day.you.first !== undefined ? <small>{clock(day.you.first)}–{clock(day.you.last!)}</small> : null}</dd>
        <dt>Agents you started</dt><dd>{formatHours(day.agents_ms)}</dd>
        <dt>Automatic runs</dt><dd>{formatHours(day.automated.elapsed_ms)}</dd>
      </dl>
      <button type="button" className="habits-vacation" aria-pressed={vacation} title="A vacation day neither breaks nor extends a streak." onClick={onVacation}>{vacation ? "Vacation day ✓" : "Mark as vacation"}</button>
    </> : <p className="muted">Choose a day for its details.</p>}
  </aside>;
}
const clock = (at: number) => new Date(at).toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });

const punchcardRanges: Array<[string, string]> = [["7", "Last 7 days"], ["30", "Last 30 days"], ["90", "Last 90 days"], ["365", "Last year"], ["all", "All time"]];
function Punchcard({ data, view }: { data: HabitsSummary; view: PunchcardView }) {
  return <div className="habits-card" data-pharos-target="section:habits-punchcard">
    <Heading target="habits-punchcard" primary />
    <p className="muted">Minutes per week in each hour, {formatDay(data.punchcard.from)} to {formatDay(data.punchcard.to)}.{view === "difference" ? " Filled is more agent time than yours; hollow, more of yours." : ""} Colours are the time of day.</p>
    <div className="habits-scroll"><PunchcardChart data={data} view={view} /></div>
  </div>;
}

const shapeRanges: Array<[string, string]> = [["7", "Last 7 days"], ["14", "Last 14 days"], ["28", "Last 28 days"], ["56", "Last 56 days"], ["90", "Last 90 days"]];
function WorkdayShape({ data, days }: { data: HabitsSummary; days: number }) {
  return <div className="habits-card" data-pharos-target="section:habits-days">
    <Heading target="habits-days" primary />
    <p className="muted">The faint bar runs from your first engaged minute to your last; the solid pieces are when you were engaged, coloured by time of day.</p>
    <DaysChart data={data} count={days} />
  </div>;
}

function Health({ data }: { data: HabitsSummary }) {
  const health = useMemo(() => healthStats(data.days, 28), [data.days]);
  const percent = (value?: number) => value === undefined ? "—" : `${Math.round(value * 100)}%`;
  return <section className="habits-pace" data-pharos-target="section:habits-pace">
    <Heading target="habits-pace" />
    <p className="muted">The last four weeks.</p>
    <div className="mcp-metrics dashboard-stats habits-health">
      <div className="mcp-metric"><span>Days off</span><strong>{health.restDays}</strong><small>longest run without one: {streakDays(health.longestWithoutRest)}</small></div>
      <div className="mcp-metric"><span>Typical day</span><strong>{health.typicalHours === undefined ? "—" : `${health.typicalHours.toFixed(1)} h`}</strong><small>median engaged time on days you worked</small></div>
      <div className="mcp-metric"><span>Agents while you're away</span><strong>{health.leverage === undefined ? "—" : `${health.leverage.toFixed(1)}×`}</strong><small>unattended agent hours per engaged hour</small></div>
      <div className="mcp-metric"><span>Focus</span><strong>{percent(health.focusShare)}</strong><small>of engaged time in blocks of 25 min or more</small></div>
    </div>
  </section>;
}

function HabitsSettingsPanel({ settings, onChange }: { settings: HabitsSettings; onChange: (settings: HabitsSettings) => void }) {
  const set = <K extends keyof HabitsSettings>(key: K, value: HabitsSettings[K]) => onChange(normalizeHabitsSettings({ ...settings, [key]: value }));
  const number = (key: "youMessages" | "agentMinutes", low: number, high: number) => <input type="number" min={low} max={high} step={1} value={settings[key]}
    onChange={event => { const value = Number(event.target.value); if (Number.isInteger(value) && value >= low && value <= high) set(key, value); }} />;
  return <ViewDisclosure viewKey="habits-settings" summary="Habit settings" className="habits-card habits-settings">
    <div className="habits-settings-grid">
      <label>Messages for your day to count{number("youMessages", 1, 1000)}</label>
      <label>Agent minutes for their day to count{number("agentMinutes", 1, 24 * 60)}</label>
      <label>Engaged gap<DashboardSelect aria-label="Engaged gap" value={settings.engagedGap} onChange={event => set("engagedGap", Number(event.target.value))}>{engagedGaps.map(gap => <option key={gap} value={gap}>{gap} minutes</option>)}</DashboardSelect></label>
      <label>Day starts at<DashboardSelect aria-label="Day starts at" value={settings.dayStart} onChange={event => set("dayStart", Number(event.target.value))}>{[0, 1, 2, 3, 4, 5, 6].map(hour => <option key={hour} value={hour}>{formatClock(hour)}</option>)}</DashboardSelect></label>
    </div>
    <h4>Vacation days</h4>
    {settings.vacations.length ? <ul className="habits-vacations">{settings.vacations.map(day => <li key={day}>{formatDay(day)} <button type="button" aria-label={`Remove vacation day ${formatDay(day)}`} onClick={() => onChange(toggleVacation(settings, day))}>Remove</button></li>)}</ul>
      : <p className="muted">None. Choose a day in the calendar to mark it.</p>}
  </ViewDisclosure>;
}
