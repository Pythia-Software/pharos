import React, { createContext, useContext, useEffect, useMemo, useState } from "react";
import { DashboardSelect } from "./dashboard-select";
import { ChartSelect } from "./usage-chart";
import { preferences } from "./preferences";
import { useViewState } from "./view-state";
import { ViewDisclosure } from "./view-disclosure";
import { initiatorLabels } from "./habits-analysis-catalog";
import { CreateDashboardCard } from "./create-dashboard-card";
import { CalendarChart, DaysChart, PunchcardChart } from "./habits-charts";
import { habitsCardTitle, readHabitsCardView, type HabitsCardTarget, type HabitsCardView } from "./habits-card";
import {
  defaultHabitsSettings, engagedGaps, formatClock, formatDay, formatGap, formatHours, habitsSettingsKey, healthStats,
  normalizeHabitsSettings, recentWork, punchcardViews, streakActors, streakDays, todayLabel, toggleVacation, validDay,
  type HabitsDay, type HabitsMode, type HabitsSettings, type HabitsSummary, type PunchcardView, type StreakActor,
} from "./habits-model";
import "./habits.css";

// Habits: streaks, calendars, a punchcard, the shape of your days and the
// automations that run without you, from the service's summary
// (/api/habits, internal/archive/habits.go). The catalog views below the
// panel read the same records (rhythm) by actor and hour.

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

// Keep one summary request and view state for the panels on either side of
// CatalogDashboard. The chart stays in the standard dashboard flow, outside
// the panels' styling and loading/error boundary.
export function HabitsPanels({ children }: { children: (before: React.ReactNode, after: React.ReactNode) => React.ReactNode }) {
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
  const before = !data ? error ? <p className="query-table-error" role="alert">{error}</p> : <p className="muted" role="status">Loading your habits…</p>
    : <section className="habits-panel" aria-label="Habits" aria-busy={loading}>
    <div className="habits-streaks-head">
      <div className="usage-chart-controls habits-controls">
        <ChartSelect label="Streaks count" value={mode} onChange={setMode} options={[["all", "Every day"], ["weekdays", "Weekdays only"]]} />
      </div>
      <Heading target="habits-streaks" />
    </div>
    <Streaks data={data} settings={settings} />
    <div className="habits-card" data-pharos-target="section:habits-calendar">
      <Heading target="habits-calendar" />
      <div className="usage-chart-controls habits-controls">
        <ChartSelect label="Whose days" value={calendar} onChange={setCalendar} options={streakActors.map(actor => [actor.id, actor.label])} />
        <ChartSelect label="Range" ariaLabel="Calendar range" value={weeks} onChange={setWeeks} options={[["13", "Last 13 weeks"], ["26", "Last 26 weeks"], ["53", "Last year"]]} />
        <ChartSelect label="Weekends" value={weekends} onChange={setWeekends} options={[["show", "Show Saturday and Sunday"], ["hide", "Weekdays only"]]} />
      </div>
      <p className="muted">{streakActors.find(actor => actor.id === calendar)?.rule(settings)} Each square is a day; darker is more time. Click a day for its details, or to mark it as a vacation day.</p>
      <div className="habits-calendar-row">
        <CalendarChart data={data} actor={calendar} weeks={Number(weeks)} weekends={weekends !== "hide"} selected={day?.day ?? ""} onSelect={setSelected} />
        <DayDetail data={data} day={day} settings={settings} onSelect={setSelected} onVacation={() => day && update(toggleVacation(settings, day.day))} />
      </div>
    </div>
    <Health data={data} />
    <div className="habits-pair habits-charts-pair">
      <Punchcard data={data} view={view} onView={setView} days={punchcardDays} onDays={setPunchcardDays} />
      <WorkdayShape data={data} days={Number(shapeDays)} onDays={setShapeDays} />
    </div>
  </section>;
  const after = data ? <section className="habits-panel" aria-label="Habit automations and settings" aria-busy={loading}>
    <p className="usage-dashboard-note">Your time is estimated from the messages you typed: each one adds its typing time, and messages within {settings.engagedGap} minutes join one engaged block. Agent turns are split by whether you were engaged and by who started them. {data.coverage.unmatched_turns ? `${data.coverage.unmatched_turns.toLocaleString()} of ${data.coverage.turns.toLocaleString()} turns have no prompt to read who started them; they count as unattended.` : ""}</p>
    <Automations data={data} />
    <HabitsSettingsPanel settings={settings} onChange={update} />
  </section> : null;
  return <HabitsViewContext.Provider value={cardView}>{children(before, after)}</HabitsViewContext.Provider>;
}

// A section's heading, with the standard control that makes it a Home card.
function Heading({ target }: { target: HabitsCardTarget }) {
  const view = useContext(HabitsViewContext);
  return <div className="habits-card-title"><h3>{habitsCardTitle(target, view)}</h3><CreateDashboardCard target={target} /></div>;
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
function Punchcard({ data, view, onView, days, onDays }: { data: HabitsSummary; view: PunchcardView; onView: (view: PunchcardView) => void; days: string; onDays: (days: string) => void }) {
  return <div className="habits-card" data-pharos-target="section:habits-punchcard">
    <Heading target="habits-punchcard" />
    <div className="usage-chart-controls habits-controls">
      <ChartSelect label="Whose time" ariaLabel="Punchcard time" value={view} onChange={onView} options={punchcardViews} />
      <ChartSelect label="Range" ariaLabel="Punchcard range" value={days} onChange={onDays} options={punchcardRanges} />
    </div>
    <p className="muted">Minutes per week in each hour, {formatDay(data.punchcard.from)} to {formatDay(data.punchcard.to)}.{view === "difference" ? " Filled is more agent time than yours; hollow, more of yours." : ""} Colours are the time of day.</p>
    <div className="habits-scroll"><PunchcardChart data={data} view={view} /></div>
  </div>;
}

const shapeRanges: Array<[string, string]> = [["7", "Last 7 days"], ["14", "Last 14 days"], ["28", "Last 28 days"], ["56", "Last 56 days"], ["90", "Last 90 days"]];
function WorkdayShape({ data, days, onDays }: { data: HabitsSummary; days: number; onDays: (days: string) => void }) {
  return <div className="habits-card" data-pharos-target="section:habits-days">
    <Heading target="habits-days" />
    <div className="usage-chart-controls habits-controls"><ChartSelect label="Range" ariaLabel="Your days range" value={String(days)} onChange={onDays} options={shapeRanges} /></div>
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

function Automations({ data }: { data: HabitsSummary }) {
  const days = useMemo(() => { const result: string[] = []; for (let day = data.punchcard.from; day <= data.punchcard.to; day = shift(day)) result.push(day); return result.slice(-400); }, [data.punchcard]);
  if (!data.automations.length) return <div className="habits-card" data-pharos-target="section:habits-automations"><Heading target="habits-automations" /><p className="muted">No agent runs started by a schedule, script, orchestrator or another agent in this range.</p></div>;
  return <div className="habits-card" data-pharos-target="section:habits-automations">
    <Heading target="habits-automations" />
    <p className="muted">What started agents without a message from you, {formatDay(data.punchcard.from)} to {formatDay(data.punchcard.to)}. A scheduled run is missed when a gap is more than twice its usual one.</p>
    <div className="habits-scroll"><table className="habits-automations">
      <thead><tr><th scope="col">Started by</th><th scope="col">Kind</th><th scope="col">Runs</th><th scope="col">Days</th><th scope="col">Agent time</th><th scope="col">Cadence</th><th scope="col">Missed</th><th scope="col">Last run</th><th scope="col">Daily runs</th></tr></thead>
      <tbody>{data.automations.map(item => {
        const largest = Math.max(1, ...Object.values(item.daily));
        return <tr key={`${item.initiator}:${item.label}`}>
          <th scope="row">{item.label}</th><td>{initiatorLabels[item.initiator] ?? item.initiator}</td><td>{item.runs.toLocaleString()}</td><td>{item.active_days}</td>
          <td>{item.agent_hours.toFixed(1)} h</td><td>{formatGap(item.median_gap_ms)}</td><td>{item.initiator === "scheduled" ? item.missed : "—"}</td>
          <td>{item.last_run ? new Date(item.last_run).toLocaleDateString() : "—"}</td>
          <td><svg width={days.length * 2} height={14} aria-hidden="true">{days.map((day, index) => item.daily[day] ? <rect key={day} x={index * 2} y={14 - Math.max(2, 14 * item.daily[day] / largest)} width={2} height={Math.max(2, 14 * item.daily[day] / largest)} className="habits-strip" /> : null)}</svg></td>
        </tr>;
      })}</tbody>
    </table></div>
  </div>;
}
const shift = (day: string) => { const date = new Date(`${day}T00:00:00Z`); date.setUTCDate(date.getUTCDate() + 1); return date.toISOString().slice(0, 10); };

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
