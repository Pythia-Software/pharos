import React, { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { hourOfDayColor } from "./chart-colors";
import {
  calendarValue, calendarWeeks, formatClock, formatDay, formatHours, level, monthLabels, punchcardGrid, punchcardViews, quartileLevels, streakActors, workdayShape,
  type HabitsDay, type HabitsSummary, type PunchcardActor, type PunchcardView, type StreakActor,
} from "./habits-model";
import type { HabitsCover } from "./habits-card";
import { HabitsChartInspection, type HabitChartPoint } from "./habits-chart-inspection";
import "./habits.css";

// The Habits page's three drawings, shared by the page and its Home cards.
// The day chart fills its page card; the calendar and punchcard can scroll.
// On a Home card, `fit` is the cover's measured size: each chart lays itself
// out across all of it, with labels at their full size rather than scaled down.

type Fit = { width: number; height: number };
const cell = 11, gapPx = 2, pitch = cell + gapPx;
export const weekdayNames = ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"];
// A cover's axis labels: their size (habits.css) and a width for "Aug" or "Mon".
const fitLabel = 11, fitWord = 22;
// A label near the right edge ends there instead of running past it.
const anchored = (x: number, width: number, right: number) => x + width > right ? { x: right, textAnchor: "end" as const } : { x };
// Rows too short for a label each name every other day, as Mon, Wed, Fri.
const labelRow = (row: number, pitch: number) => pitch >= fitLabel + 1 || row % 2 === 0;
// A row label's baseline: centred on the row, but never below the drawing's foot.
const rowLabel = (centre: number, height: number) => Math.min(centre + 4, height - 3);

export function CalendarChart({ data, actor, weeks, weekends, selected = "", onSelect, fit }: {
  data: HabitsSummary; actor: StreakActor; weeks: number; weekends: boolean; selected?: string; onSelect?: (day: string) => void; fit?: Fit;
}) {
  const columns = useMemo(() => calendarWeeks(data.days, weeks), [data.days, weeks]);
  const months = useMemo(() => monthLabels(columns), [columns]);
  const thresholds = useMemo(() => quartileLevels(data.days.map(day => calendarValue(day, actor))), [data.days, actor]);
  const rows = weekends ? [0, 1, 2, 3, 4, 5, 6] : [0, 1, 2, 3, 4];
  const label = streakActors.find(item => item.id === actor)?.label ?? actor;
  // A cover stretches the cells to fill it; the page keeps square ones.
  const left = fit ? 28 : 30, top = fit ? fitLabel + 4 : 14, width = fit ? fit.width : left + columns.length * pitch + 24, height = fit ? fit.height : top + rows.length * pitch;
  const pitchX = fit ? (width - left) / Math.max(1, columns.length) : pitch, pitchY = fit ? (height - top) / rows.length : pitch;
  const cellX = Math.max(1, pitchX - gapPx), cellY = Math.max(1, pitchY - gapPx);
  const points: HabitChartPoint[] = columns.flatMap(column => column.filter((day): day is HabitsDay => Boolean(day && rows.includes(day.weekday)))).map(day => ({
    key: `${actor}:${day.day}`, heading: formatDay(day.day), selection: { day: day.day },
    rows: actor === "you" ? [
      { label: "Engaged time", value: formatHours(day.you.engaged_ms), color: "var(--sea)", actors: ["you"] },
      { label: "Typed messages", value: day.you.messages.toLocaleString(), actors: ["you"] },
    ] : actor === "agents" ? [{ label: "Agents you started", value: formatHours(day.agents_ms), color: "var(--accent)", actors: ["with_you", "unattended"] }]
      : [{ label: "Automatic agent time", value: formatHours(day.automated.elapsed_ms), color: "var(--gold)", actors: ["automated"] },
        { label: "Turns started", value: day.automated.turns.toLocaleString(), actors: ["automated"] }],
    detail: day.vacation ? "Vacation day" : day.excused ? "Not counted toward your streak" : undefined,
  }));
  const byDay = new Map(points.map(point => [point.key, point]));
  return <div className={`habits-calendar habits-calendar-${actor}`}>
    <HabitsChartInspection chart="habits-calendar" data={data} points={points} fit={Boolean(fit)} onSelect={key => {
      const day = points.find(point => point.key === key)?.selection;
      if (day && "day" in day) onSelect?.(day.day === selected ? "" : day.day);
    }}>{mark => <svg width={width} height={height} role={fit ? "img" : "group"} aria-label={`${label}: ${data.streaks[actor].current} day streak`}>
      {months.map((month, index) => month ? <text key={index} {...fit ? anchored(left + index * pitchX, fitWord, width) : { x: left + index * pitch }} y={fit ? fitLabel : 10} className="habits-axis">{month}</text> : null)}
      {rows.map((weekday, row) => fit && !labelRow(row, pitchY) ? null : <text key={weekday} x={0} y={fit ? rowLabel(top + row * pitchY + cellY / 2, height) : top + row * pitch + cell - 2} className="habits-axis">{weekdayNames[weekday]}</text>)}
      {columns.map((column, week) => rows.map((weekday, row) => { const day = column[weekday]; return day ? <rect key={day.day} x={left + week * pitchX} y={top + row * pitchY} width={cellX} height={cellY} rx={2}
        {...mark(byDay.get(`${actor}:${day.day}`)!)}
        className={`habits-level-${level(calendarValue(day, actor), thresholds)}${day.excused ? " habits-excused" : ""}${day.day === selected ? " habits-selected" : ""}${day.day === data.today ? " habits-today" : ""}`}>
        {fit ? <title>{dayTitle(day, actor)}</title> : null}</rect> : null; }))}
    </svg>}</HabitsChartInspection>
  </div>;
}

function dayTitle(day: HabitsDay, actor: StreakActor) {
  const what = actor === "you" ? `${formatHours(day.you.engaged_ms)} engaged, ${day.you.messages} messages`
    : actor === "agents" ? `${formatHours(day.agents_ms)} of agents you started`
    : `${day.automated.turns} automatic agent runs`;
  return `${formatDay(day.day)}: ${what}${day.vacation ? " · vacation" : day.excused ? " · not counted" : ""}`;
}

export function PunchcardChart({ data, view, fit }: { data: HabitsSummary; view: PunchcardView; fit?: Fit }) {
  const grid = useMemo(() => punchcardGrid(data, view), [data, view]);
  const largest = Math.max(1, ...grid.flat().map(Math.abs));
  const left = fit ? 30 : 34, top = fit ? fitLabel + 5 : 16, width = fit ? fit.width : left + 24 * 22, height = fit ? fit.height : top + 7 * 22;
  const stepX = (width - left) / 24, stepY = (height - top) / 7, step = Math.min(stepX, stepY);
  const radius = (value: number) => Math.sqrt(Math.abs(value) / largest) * Math.max(1, step / 2 - 1);
  // Hour labels every three hours, or six where three would collide.
  const every = stepX * 3 >= 34 ? 3 : 6;
  const cx = (hour: number) => left + hour * stepX + stepX / 2, cy = (weekday: number) => top + weekday * stepY + stepY / 2;
  const views = view === "difference" ? ["agents", "you"] as const : [view];
  const grids = views.map(actor => punchcardGrid(data, actor));
  const points: HabitChartPoint[] = grid.flatMap((hours, weekday) => hours.flatMap((value, hour) => value ? [{
    key: `${view}:${weekday}:${hour}`, heading: `${weekdayNames[weekday]} ${formatClock(hour)}–${formatClock(hour + 1)}`, selection: { weekday, hour },
    rows: views.map((actor, index) => ({ label: punchcardViews.find(([id]) => id === actor)?.[1] ?? actor,
      value: `${Math.round(grids[index][weekday][hour])} min/week`, color: hourOfDayColor(hour),
      actors: actor === "agents" ? ["with_you", "unattended"] : [actor] as PunchcardActor[],
    })), detail: `${view === "difference" ? `Difference: ${Math.round(value)} min/week. ` : ""}${formatDay(data.punchcard.from)} to ${formatDay(data.punchcard.to)} · ${data.punchcard.weeks[weekday][hour]} occurrences`,
  }] : []));
  const bySlot = new Map(points.map(point => [point.key, point]));
  return <HabitsChartInspection chart="habits-punchcard" data={data} points={points} fit={Boolean(fit)}>{mark => <svg width={width} height={height} role={fit ? "img" : "group"} aria-label={`Punchcard of ${punchcardViews.find(([id]) => id === view)?.[1]}`}>
    {Array.from({ length: 24 }, (_, hour) => hour % every === 0 ? <text key={hour} x={cx(hour)} y={fit ? fitLabel : 10} textAnchor="middle" className="habits-axis">{formatClock(hour)}</text> : null)}
    {grid.map((hours, weekday) => <g key={weekday}>
      {fit && !labelRow(weekday, stepY) ? null : <text x={0} y={rowLabel(cy(weekday), height)} className="habits-axis">{weekdayNames[weekday]}</text>}
      {hours.map((value, hour) => value ? <g key={hour} {...mark(bySlot.get(`${view}:${weekday}:${hour}`)!)}>
        <circle cx={cx(hour)} cy={cy(weekday)} r={step / 2} fill="transparent" />
        <circle cx={cx(hour)} cy={cy(weekday)} r={Math.max(1.5, radius(value))} className={value < 0 ? "habits-dot habits-dot-negative" : "habits-dot"} style={value < 0 ? { stroke: hourOfDayColor(hour) } : { fill: hourOfDayColor(hour) }}>
          {fit ? <title>{`${weekdayNames[weekday]} ${formatClock(hour)}–${formatClock(hour + 1)}: ${Math.round(value)} min a week`}</title> : null}</circle></g> : null)}
    </g>)}
  </svg>}</HabitsChartInspection>;
}

export function DaysChart({ data, count = 28, fit }: { data: HabitsSummary; count?: number; fit?: Fit }) {
  const days = useMemo(() => workdayShape(data.days, count, data.settings.dayStart, data.timezone === "Local" ? undefined : data.timezone), [data, count]);
  const container = useRef<HTMLDivElement>(null);
  const [availableWidth, setAvailableWidth] = useState(0);
  useEffect(() => {
    if (fit || !container.current) return;
    const element = container.current;
    const observer = new ResizeObserver(() => setAvailableWidth(element.clientWidth));
    observer.observe(element);
    setAvailableWidth(element.clientWidth);
    return () => observer.disconnect();
  }, [fit]);
  // The plot runs from top to height, leaving room for half a clock label at either end.
  const span = Math.max(24, ...days.map(day => Math.ceil(day.end ?? 0))), top = fit ? 7 : 6, left = fit ? 38 : 42;
  const height = fit ? Math.max(top + 1, fit.height - 8) : 210;
  const width = fit ? fit.width : Math.max(left + 1, availableWidth || left + days.length * 14);
  const pitchX = (width - left) / Math.max(1, days.length), bar = Math.min(18, pitchX * 0.72);
  const x = (index: number) => left + index * pitchX + (pitchX - bar) / 2;
  const y = (hours: number) => top + (hours / span) * (height - top);
  const clock = (hours: number) => formatClock(hours + data.settings.dayStart);
  const points: HabitChartPoint[] = days.map(day => ({ key: day.day, heading: formatDay(day.day), selection: { day: day.day }, rows: [
    { label: "Engaged time", value: `${day.engagedHours.toFixed(1)} h`, color: "var(--sea)", actors: ["you"] },
    { label: "Engaged blocks", value: String(day.blockCount) },
    { label: "First engaged", value: day.start === undefined ? "—" : clock(day.start) },
    { label: "Last engaged", value: day.end === undefined ? "—" : clock(day.end) },
  ], detail: day.vacation ? "Vacation day" : undefined }));
  return <div className="habits-days-chart" ref={container}><HabitsChartInspection chart="habits-days" data={data} points={points} fit={Boolean(fit)}>{mark => <svg width={width} height={fit ? fit.height : height + 10} role={fit ? "img" : "group"} aria-label="When you were engaged each day">
    {[0, 6, 12, 18, 24].filter(hours => hours <= span).map(hours => <g key={hours}><line x1={left - 4} x2={width} y1={y(hours)} y2={y(hours)} className="habits-grid" /><text x={0} y={y(hours) + 4} className="habits-axis">{clock(hours)}</text></g>)}
    {days.map((day, index) => day.start !== undefined && day.end !== undefined ? <g key={day.day} {...mark(points[index])} className={day.vacation ? "habits-excused" : undefined}>
      <rect x={x(index)} y={y(day.start)} width={bar} height={Math.max(2, y(day.end) - y(day.start))} rx={2} className="habits-span">
        {fit ? <title>{`${formatDay(day.day)}: ${clock(day.start)} to ${clock(day.end)}, ${day.engagedHours.toFixed(1)} h engaged in ${day.blockCount} ${day.blockCount === 1 ? "block" : "blocks"}`}</title> : null}</rect>
      {day.blocks.map((piece, part) => <rect key={part} x={x(index)} y={y(piece.start)} width={bar} height={Math.max(1, y(piece.end) - y(piece.start))} style={{ fill: piece.color }} pointerEvents="none" />)}
    </g> : null)}
  </svg>}</HabitsChartInspection></div>;
}

// A Home card's cover: the section's chart, drawn to the cover's size. The
// chart waits for the first measurement, taken before the cover is painted.
export function HabitsCoverChart({ cover }: { cover: HabitsCover }) {
  const box = useRef<HTMLSpanElement>(null), fit = useCoverSize(box);
  return <span className="habits-cover" ref={box}>
    {!fit ? null : cover.chart === "calendar" ? <CalendarChart data={cover.summary} actor={cover.actor} weeks={cover.weeks} weekends={cover.weekends} fit={fit} />
      : cover.chart === "punchcard" ? <PunchcardChart data={cover.summary} view={cover.view} fit={fit} />
      : <DaysChart data={cover.summary} count={cover.count} fit={fit} />}
  </span>;
}

// The element's layout size in whole pixels, so a drawing of exactly that
// size never grows its container. Layout size ignores the card's arrival
// transform, which a bounding rectangle would include.
function useCoverSize(ref: React.RefObject<HTMLElement | null>): Fit | undefined {
  const [size, setSize] = useState<Fit>();
  useLayoutEffect(() => {
    const element = ref.current;
    if (!element) return;
    const measure = () => {
      const width = Math.floor(element.clientWidth), height = Math.floor(element.clientHeight);
      setSize(previous => previous?.width === width && previous.height === height ? previous : width > 0 && height > 0 ? { width, height } : undefined);
    };
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    return () => observer.disconnect();
  }, [ref]);
  return size;
}
