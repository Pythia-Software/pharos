import React, { useMemo } from "react";
import { hourOfDayColor } from "./chart-colors";
import {
  calendarValue, calendarWeeks, formatClock, formatDay, formatHours, level, monthLabels, punchcardGrid, punchcardViews, quartileLevels, streakActors, workdayShape,
  type HabitsDay, type HabitsSummary, type PunchcardView, type StreakActor,
} from "./habits-model";
import type { HabitsCover } from "./habits-card";
import "./habits.css";

// The Habits page's three drawings, shared by the page and its Home cards.
// On the page they keep their natural size and scroll; on a card (fit) they
// shrink to fit the cover, never past their natural size.

const cell = 11, gapPx = 2, pitch = cell + gapPx;
export const weekdayNames = ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"];
const size = (fit: boolean | undefined, width: number, height: number) => fit
  ? { viewBox: `0 0 ${width} ${height}`, width: "100%", height: "100%", preserveAspectRatio: "xMinYMin meet", style: { maxWidth: width, maxHeight: height } }
  : { width, height };

export function CalendarChart({ data, actor, weeks, weekends, selected = "", onSelect, fit }: {
  data: HabitsSummary; actor: StreakActor; weeks: number; weekends: boolean; selected?: string; onSelect?: (day: string) => void; fit?: boolean;
}) {
  const columns = useMemo(() => calendarWeeks(data.days, weeks), [data.days, weeks]);
  const months = useMemo(() => monthLabels(columns), [columns]);
  const thresholds = useMemo(() => quartileLevels(data.days.map(day => calendarValue(day, actor))), [data.days, actor]);
  const rows = weekends ? [0, 1, 2, 3, 4, 5, 6] : [0, 1, 2, 3, 4];
  const label = streakActors.find(item => item.id === actor)?.label ?? actor;
  const left = 30, top = 14, width = left + columns.length * pitch + 24;
  return <div className={`habits-calendar habits-calendar-${actor}`}>
    <svg {...size(fit, width, top + rows.length * pitch)} role="img" aria-label={`${label}: ${data.streaks[actor].current} day streak`}>
      {months.map((month, index) => month ? <text key={index} x={left + index * pitch} y={10} className="habits-axis">{month}</text> : null)}
      {rows.map((weekday, row) => <text key={weekday} x={0} y={top + row * pitch + cell - 2} className="habits-axis">{weekdayNames[weekday]}</text>)}
      {columns.map((column, week) => rows.map((weekday, row) => { const day = column[weekday]; return day ? <rect key={day.day} x={left + week * pitch} y={top + row * pitch} width={cell} height={cell} rx={2}
        {...onSelect ? { "data-view-field": "habits_day", onClick: () => onSelect(day.day === selected ? "" : day.day) } : {}}
        className={`habits-level-${level(calendarValue(day, actor), thresholds)}${day.excused ? " habits-excused" : ""}${day.day === selected ? " habits-selected" : ""}${day.day === data.today ? " habits-today" : ""}`}>
        <title>{dayTitle(day, actor)}</title></rect> : null; }))}
    </svg>
  </div>;
}

function dayTitle(day: HabitsDay, actor: StreakActor) {
  const what = actor === "you" ? `${formatHours(day.you.engaged_ms)} engaged, ${day.you.messages} messages`
    : actor === "agents" ? `${formatHours(day.agents_ms)} of agents you started`
    : `${day.automated.turns} automatic agent runs`;
  return `${formatDay(day.day)}: ${what}${day.vacation ? " · vacation" : day.excused ? " · not counted" : ""}`;
}

export function PunchcardChart({ data, view, fit }: { data: HabitsSummary; view: PunchcardView; fit?: boolean }) {
  const grid = useMemo(() => punchcardGrid(data, view), [data, view]);
  const largest = Math.max(1, ...grid.flat().map(Math.abs));
  const left = 34, top = 16, step = 22, radius = (value: number) => Math.sqrt(Math.abs(value) / largest) * (step / 2 - 1);
  return <svg {...size(fit, left + 24 * step, top + 7 * step)} role="img" aria-label={`Punchcard of ${punchcardViews.find(([id]) => id === view)?.[1]}`}>
    {Array.from({ length: 24 }, (_, hour) => hour % 3 === 0 ? <text key={hour} x={left + hour * step + step / 2} y={10} textAnchor="middle" className="habits-axis">{formatClock(hour)}</text> : null)}
    {grid.map((hours, weekday) => <g key={weekday}>
      <text x={0} y={top + weekday * step + step / 2 + 4} className="habits-axis">{weekdayNames[weekday]}</text>
      {hours.map((value, hour) => value ? <circle key={hour} cx={left + hour * step + step / 2} cy={top + weekday * step + step / 2} r={Math.max(1.5, radius(value))} className={value < 0 ? "habits-dot habits-dot-negative" : "habits-dot"} style={value < 0 ? { stroke: hourOfDayColor(hour) } : { fill: hourOfDayColor(hour) }}>
        <title>{`${weekdayNames[weekday]} ${formatClock(hour)}–${formatClock(hour + 1)}: ${Math.round(value)} min a week`}</title></circle> : null)}
    </g>)}
  </svg>;
}

export function DaysChart({ data, count = 28, fit }: { data: HabitsSummary; count?: number; fit?: boolean }) {
  const days = useMemo(() => workdayShape(data.days, count, data.settings.dayStart, data.timezone === "Local" ? undefined : data.timezone), [data, count]);
  const span = Math.max(24, ...days.map(day => Math.ceil(day.end ?? 0))), height = 210, top = 6, bar = 10, pitchX = 14, left = 42;
  const y = (hours: number) => top + (hours / span) * (height - top);
  const clock = (hours: number) => formatClock(hours + data.settings.dayStart);
  return <svg {...size(fit, left + days.length * pitchX, height + 10)} role="img" aria-label="When you were engaged each day">
    {[0, 6, 12, 18, 24].filter(hours => hours <= span).map(hours => <g key={hours}><line x1={left - 4} x2={left + days.length * pitchX} y1={y(hours)} y2={y(hours)} className="habits-grid" /><text x={0} y={y(hours) + 4} className="habits-axis">{clock(hours)}</text></g>)}
    {days.map((day, index) => day.start !== undefined && day.end !== undefined ? <g key={day.day} className={day.vacation ? "habits-excused" : undefined}>
      <rect x={left + index * pitchX} y={y(day.start)} width={bar} height={Math.max(2, y(day.end) - y(day.start))} rx={2} className="habits-span">
        <title>{`${formatDay(day.day)}: ${clock(day.start)} to ${clock(day.end)}, ${day.engagedHours.toFixed(1)} h engaged in ${day.blockCount} ${day.blockCount === 1 ? "block" : "blocks"}`}</title></rect>
      {day.blocks.map((piece, part) => <rect key={part} x={left + index * pitchX} y={y(piece.start)} width={bar} height={Math.max(1, y(piece.end) - y(piece.start))} style={{ fill: piece.color }} pointerEvents="none" />)}
    </g> : null)}
  </svg>;
}

// A Home card's cover: the section's chart, scaled to the card.
export function HabitsCoverChart({ cover }: { cover: HabitsCover }) {
  return <span className="habits-cover">
    {cover.chart === "calendar" ? <CalendarChart data={cover.summary} actor={cover.actor} weeks={cover.weeks} weekends={cover.weekends} fit />
      : cover.chart === "punchcard" ? <PunchcardChart data={cover.summary} view={cover.view} fit />
      : <DaysChart data={cover.summary} fit />}
  </span>;
}
