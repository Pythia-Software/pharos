import { useViewEntry } from "./view-state";
import React, { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { chartPeriods, isHourly, localDay, nextPeriod, parseDay, periodKey, periodUnit, validPeriodCount, type ChartPeriod } from "./chart-period";
import { chartRanges, rangeStart, withTimeRange, type ChartRange, type FilterChange, type RangeChange, type TimeBounds, type TimeFields } from "./chart-range";
import { columnScale, defaultChartScale, type ChartScale } from "./chart-scale";
import { cumulativeBuckets, rollingAverageBuckets, validRollingPeriods, type ChartBucket, type ChartDisplay, type ChartSeries } from "./chart-series";
import { ColumnMarks } from "./column-marks";
import { ChartLegendChip } from "./chart-legend-chip";
import { type LegendAction } from "./chart-selection";
import { Icon } from "./icons";
import { loadUsageHistory } from "./usage-history";
import { chartGesture, chartInspectionRange, type ChartInspectionRange } from "./chart-inspection";
import { PageLink } from "./nested-pages";

export { chartSpan, smoothingWhere, timeBounds, usageTime, withTimeRange, writingTime, type ChartRange, type FilterChange, type TimeBounds, type TimeFields } from "./chart-range";

const pad2 = (value: number) => String(value).padStart(2, "0");
export const chartPeriodCount = (view: { period: ChartPeriod; hours: number; days: number }) => view.period === "hours" ? view.hours : view.period === "days" ? view.days : 1;

// Preset changes reset manual bucket choices to a readable calendar grid.
// Keep daily detail for 90 days; longer bounded ranges use weeks.
export function presetPeriod(range: ChartRange | "all", oldestConversation?: Date, now = new Date()): Partial<PeriodView> & { period: ChartPeriod } {
  switch (range) {
    case "today": case "1d": return { period: "hour" };
    case "7d": return { period: "hours", hours: 4 };
    case "30d": case "90d": return { period: "day" };
    case "6m": case "1y": return { period: "week" };
    case "all": {
      // Clamp the calendar cutoff for dates such as October 31 → February 28.
      const cutoff = new Date(now), day = cutoff.getDate();
      cutoff.setDate(1);
      cutoff.setMonth(cutoff.getMonth() - 20);
      cutoff.setDate(Math.min(day, new Date(cutoff.getFullYear(), cutoff.getMonth() + 1, 0).getDate()));
      return { period: oldestConversation && oldestConversation < cutoff ? "month" : "week" };
    }
  }
}

// Record dashboards currently offer calendar day/week/month groupings only.
export function calendarPresetPeriod(range: ChartRange | "all", oldestConversation?: Date, now = new Date()): "day" | "week" | "month" {
  const { period } = presetPeriod(range, oldestConversation, now);
  return period === "hour" || period === "hours" ? "day" : period === "days" ? "week" : period;
}


export function selectChartRange(range: ChartRange | "all" | "custom" | "custom-times", onChange: RangeChange, onPresetChange?: (range: ChartRange | "all") => void) {
  if (range === "custom" || range === "custom-times") return;
  onPresetChange?.(range);
  onChange(range === "all" ? undefined : rangeStart(range), undefined, range);
}









// bucketRange is the time from the first bucket's start to the last one's end.
// A range reaching into the future is left open, so it keeps today.
function bucketRange(first: ChartBucket, last: ChartBucket, period: ChartPeriod, count = 1): [Date, Date | undefined, Date] {
  const from = new Date(first.date), to = new Date(last.date);
  nextPeriod(to, period, count);
  return [from, to > new Date() ? undefined : to, to];
}

// A range picked in a chart shows enough columns to read: a short one moves to
// a finer period.
export function finerPeriod(period: ChartPeriod, from: Date, to: Date, count = 1): ChartPeriod {
  const days = (to.getTime() - from.getTime()) / 86_400_000;
  if (period === "month" && days < 240) period = "week";
  if ((period === "week" || period === "days" && count > 1) && days < 56) period = "day";
  if ((period === "day" || period === "hours" && count > 1) && days < 1) period = "hour";
  return period;
}

export function rangeLabel(bounds: TimeBounds): string {
  if (bounds.preset === "today") return "Today";
  const year = new Date().getFullYear();
  const timed = [bounds.from, bounds.to].some(date => date && (date.getHours() || date.getMinutes() || date.getSeconds()));
  if (timed) {
    const stamp = (date: Date) => date.toLocaleString(undefined, { month: "short", day: "numeric", ...(date.getFullYear() !== year ? { year: "numeric" } : {}), hour: "numeric", minute: "2-digit" });
    if (bounds.from && bounds.to) return `${stamp(bounds.from)} – ${stamp(bounds.to)} (end exclusive)`;
    return bounds.from ? `Since ${stamp(bounds.from)}` : bounds.to ? `Before ${stamp(bounds.to)}` : "";
  }
  const day = (date: Date) => date.toLocaleDateString(undefined, { month: "short", day: "numeric", ...(date.getFullYear() !== year ? { year: "numeric" } : {}) });
  const last = bounds.to ? new Date(bounds.to.getTime() - 1) : undefined;
  if (bounds.from && last) return localDay(bounds.from) === localDay(last) ? day(last) : `${day(bounds.from)} – ${day(last)}`;
  return bounds.from ? `Since ${day(bounds.from)}` : last ? `Through ${day(last)}` : "";
}

// Calendar dates include the whole last day, even when switching from times.
export function customDateBounds(bounds: TimeBounds): TimeBounds {
  const from = bounds.from ? new Date(bounds.from) : undefined;
  const to = bounds.to ? new Date(bounds.to.getTime() - 1) : undefined;
  from?.setHours(0, 0, 0, 0);
  if (to) { to.setHours(0, 0, 0, 0); to.setDate(to.getDate() + 1); }
  return { from, to };
}

// A date selection from the chart or table remains visible alongside presets.
export function RangeControl({ bounds, onChange, onPresetChange, editable = true }: { bounds: TimeBounds; onChange: RangeChange; onPresetChange?: (range: ChartRange | "all", oldestConversation?: Date) => void; editable?: boolean }) {
  const needsHistory = Boolean(onPresetChange);
  const [history, setHistory] = useState<{ ready: boolean; oldest?: Date }>({ ready: !needsHistory });
  useEffect(() => {
    if (!needsHistory) return;
    let mounted = true;
    const load = () => {
      setHistory(previous => ({ ...previous, ready: false }));
      void loadUsageHistory(true).then(data => {
        if (mounted) setHistory({ ready: true, oldest: data.oldest_conversation_at ? new Date(data.oldest_conversation_at) : undefined });
      }, () => { if (mounted) setHistory({ ready: true }); });
    };
    load();
    window.addEventListener("pharos:usage-refresh", load);
    return () => { mounted = false; window.removeEventListener("pharos:usage-refresh", load); };
  }, [needsHistory]);
  const active = bounds.preset ?? (!bounds.from && !bounds.to ? "all" : bounds.from && !bounds.to ? chartRanges.find(([range]) => rangeStart(range).getTime() === bounds.from!.getTime())?.[0] : undefined);
  const [custom, setCustom] = useState<"custom" | "custom-times" | null>(null);
  useEffect(() => { if (bounds.preset || !bounds.from && !bounds.to) setCustom(null); }, [bounds.preset, bounds.from?.getTime(), bounds.to?.getTime()]);
  const timed = [bounds.from, bounds.to].some(date => date && (date.getHours() || date.getMinutes() || date.getSeconds() || date.getMilliseconds()));
  const mode = custom ?? (active ? null : timed ? "custom-times" : "custom");
  const picker = <ChartSelect label="Range" value={mode ?? active!}
    options={[...chartRanges.map(([range, text]): [string, string] => [range, range === "today" ? text : `Last ${text}`]), ...(editable ? [["custom", "Custom dates…"], ["custom-times", "Custom times…"]] as Array<[string, string]> : active ? [] : [["custom", rangeLabel(bounds), "Dates selected in the chart or table"] as [string, string, string]]), ["all", "All time", history.ready ? undefined : "Loading conversation history…"]]}
    title="Dates included in this usage view. Choose All time to clear the date filter."
    onChange={range => {
      if (range === "custom" || range === "custom-times") {
        setCustom(range);
        const fixed = range === "custom" ? customDateBounds(bounds) : bounds;
        onChange(fixed.from, fixed.to);
      } else {
        setCustom(null);
        selectChartRange(range as ChartRange | "all", onChange, preset => onPresetChange?.(preset, history.oldest));
      }
    }} />;
  return editable ? <div className={`usage-date-range${mode ? " custom" : ""}`}>{picker}{mode ? <CustomDates key={mode} bounds={bounds} onChange={onChange} includeTime={mode === "custom-times"} /> : null}</div> : picker;
}

function CustomDates({ bounds, onChange, includeTime }: { bounds: TimeBounds; onChange: (from?: Date, to?: Date) => void; includeTime: boolean }) {
  const inputValue = (date: Date) => includeTime ? `${localDay(date)}T${pad2(date.getHours())}:${pad2(date.getMinutes())}` : localDay(date);
  const from = bounds.from ? inputValue(bounds.from) : "", to = bounds.to ? inputValue(includeTime ? bounds.to : new Date(bounds.to.getTime() - 1)) : "";
  const [draft, setDraft] = useState({ from, to });
  useEffect(() => setDraft({ from, to }), [from, to]);
  const invalidRange = (next: typeof draft) => Boolean(next.from && next.to && (includeTime ? next.from >= next.to : next.from > next.to));
  const invalid = invalidRange(draft);
  function change(key: "from" | "to", value: string) {
    const next = { ...draft, [key]: value };
    setDraft(next);
    if (invalidRange(next)) return;
    const parse = (value: string) => includeTime ? new Date(value) : parseDay(value);
    const start = next.from ? parse(next.from) : undefined, end = next.to ? parse(next.to) : undefined;
    if (start && !Number.isFinite(start.getTime()) || end && !Number.isFinite(end.getTime())) return;
    if (end && !includeTime) end.setDate(end.getDate() + 1);
    onChange(start, end);
  }
  return <div className="usage-custom-dates">
    {(["from", "to"] as const).map(key => <label key={key}><span>{key === "from" ? "From" : includeTime ? "Until (exclusive)" : "Through"}</span>
      <input type={includeTime ? "datetime-local" : "date"} aria-label={includeTime ? key === "from" ? "From date and time" : "Until date and time" : key === "from" ? "From date" : "Through date"} aria-invalid={invalid} value={draft[key]} onChange={event => change(key, event.target.value)} />
    </label>)}
    {invalid ? <span className="query-table-error" role="alert">{includeTime ? "Start must be before end." : "Start date must be on or before end date."}</span> : null}
  </div>;
}

export function ChartSelect<T extends string>({ label, ariaLabel = label, value, options, onChange, title, disabled = false, note }: {
  label: string; ariaLabel?: string; value: T; options: Array<[T, string, string?]>; onChange: (next: T) => void;
  title?: string; disabled?: boolean; note?: string;
}) {
  return <label className="usage-chart-field usage-chart-picker">
    <span className="usage-preset-label">{label}</span>
    <span className="usage-chart-select">
      <select data-pharos-target={`control:chart:${ariaLabel.toLowerCase().replace(/\s+/g,"-")}`} aria-label={ariaLabel} value={value} disabled={disabled} title={title} onChange={event => onChange(event.target.value as T)}>
        {options.map(([option, text, reason]) => <option key={option} value={option} disabled={Boolean(reason)} title={reason}>{text}</option>)}
      </select>
      <Icon name="chevron-down" className="usage-chart-caret" />
    </span>
    {note ? <span className="usage-chart-control-note">{note}</span> : null}
  </label>;
}

export type PeriodView = { period: ChartPeriod; hours: number; days: number };
export function PeriodControl({ view, onChange, disabled }: { view: PeriodView; onChange: (next: Partial<PeriodView>) => void; disabled?: Partial<Record<ChartPeriod, string>> }) {
  const custom = view.period === "hours" || view.period === "days" ? view.period : null;
  return <div className="usage-chart-field usage-period-control" role="group" aria-label="Time Bucket">
    <ChartSelect label="Time Bucket" value={view.period}
      options={([["hour", "Hour"], ["day", "Day"], ["week", "Week"], ["month", "Month"], ["hours", "Custom hours…"], ["days", "Custom days…"]] as Array<[ChartPeriod, string]>).map(([period, text]): [ChartPeriod, string, string?] => [period, text, disabled?.[period]])}
      title="Time covered by each chart bucket" onChange={period => onChange({ period })} />
    {custom ? <CustomPeriod key={custom} period={custom} value={view[custom]} onChange={value => onChange({ [custom]: value })} /> : null}
  </div>;
}

function CustomPeriod({ period, value, onChange }: { period: "hours" | "days"; value: number; onChange: (value: number) => void }) {
  const [draft, setDraft] = useState(String(value));
  useEffect(() => setDraft(String(value)), [value]);
  return <label className="usage-period-custom">
    <input type="number" aria-label={`Number of ${period}`} min={1} max={10000} step={1} value={draft}
      title={`Number of ${period} per interval`}
      onChange={event => { setDraft(event.target.value); if (validPeriodCount(event.target.valueAsNumber)) onChange(event.target.valueAsNumber); }}
      onBlur={() => setDraft(String(value))} />
  </label>;
}

export function ScaleControl({ value, onChange, percent = false }: { value: ChartScale; onChange: (next: ChartScale) => void; percent?: boolean }) {
  const descriptions = { linear: "Column heights are proportional to their values.", logarithmic: "Compresses large values so smaller values are easier to compare.", half: "Linear for smaller values, logarithmic for peaks." };
  return <ChartSelect label="Scale" value={percent ? "linear" : value} disabled={percent}
    options={[["linear", "Linear"], ["logarithmic", "Logarithmic"], ["half", "Linear + log"]]}
    title={descriptions[percent ? "linear" : value]} note={percent ? "Shares use a linear scale." : undefined} onChange={onChange} />;
}

export function DisplayControl({ value = "interval", onChange, disabled }: { value?: ChartDisplay; onChange: (next: ChartDisplay) => void; disabled?: string }) {
  return <ChartSelect label="Display" value={value} options={[["interval", "Per interval"], ["cumulative", "Cumulative", disabled]]} onChange={onChange} />;
}

export function SmoothingControl({ value, unit, onChange, disabled = false, note = "Trailing average · includes preceding history" }: { value: number; unit: string; onChange: (next: number) => void; disabled?: boolean; note?: string }) {
  const [draft, setDraft] = useState(String(value));
  const previous = useRef(value >= 2 ? value : 7);
  useEffect(() => { setDraft(String(value)); if (value >= 2) previous.current = value; }, [value]);
  // Older links and preferences can contain the former one-interval no-op.
  // Upgrade them through the owner so the chart and its URL stay in sync.
  useEffect(() => { if (value === 1) onChange(2); }, [value, onChange]);
  const enabled = value > 0;
  return <div className="usage-chart-field usage-smoothing-control" role="group" aria-label="Chart smoothing">
    <div className="usage-smoothing-row">
      <ChartSelect label="Smoothing" value={enabled ? "on" : "off"} options={[["off", "Off"], ["on", "On"]]} disabled={disabled}
        onChange={next => onChange(next === "off" ? 0 : previous.current)} />
      {enabled ? <label className="usage-smoothing-input"><input type="number" aria-label="Smoothing window" min={2} step={1} value={draft} disabled={disabled}
        title="Trailing average window in intervals, including history before the selected range."
        onChange={event => { setDraft(event.target.value); if (validRollingPeriods(event.target.valueAsNumber) && event.target.valueAsNumber >= 2) onChange(event.target.valueAsNumber); }} onBlur={() => setDraft(String(value))} /><span>{unit}</span></label> : null}
    </div>
    {enabled || disabled ? <span className="usage-chart-control-note">{note}</span> : null}
  </div>;
}

export function RollingControl({ value, period, count = 1, onChange, disabled, note, display }: { value: number; period: ChartPeriod; count?: number; onChange: (next: number) => void; disabled?: boolean; note?: string; display?: ChartDisplay }) {
  const cumulative = display === "cumulative";
  return <SmoothingControl value={cumulative ? 0 : value} unit={`${count > 1 ? `× ${count} ` : ""}${chartPeriods[period].unit}s`} onChange={onChange} disabled={disabled || cumulative} note={cumulative ? "Cumulative values use original amounts. Smoothing applies per interval." : note} />;
}

// The legend can name even a single series. Hovering a column shows a key of the series
// it holds, largest first, with what a folded series contains.
// With onSelect, clicking a key selects its series: only selected series are
// drawn and the rest stay in the key, dimmed. With onRange, dragging across
// two or more columns picks a time range. A single column pins its key.
//
// By default the chart smooths, accumulates and takes shares of the interval
// amounts it is given. Prepared buckets arrive already transformed, with
// shares as fractions, and are only drawn: a pooled ratio or a rolling sum
// must not be averaged again. Series that do not add up (lines) are drawn as
// lines with no total, and a reference value draws a labelled level line.
export function StackedColumns({ title, controls, series, buckets: sourceBuckets, period, periodCount = 1, rollingPeriods = 0, display = "interval", cumulativeStart, scale = defaultChartScale, format, tickFormat = format, noun, loading, error, legend = false, selected = [], excluded = false, percent = false, prepared = false, lines = false, additive = true, rolling = "average", reference, onSelect, onExpand, onRange, seriesHref }: {
  title: string; controls: React.ReactNode; series: ChartSeries[]; buckets: ChartBucket[]; period: ChartPeriod; periodCount?: number;
  format: (value: number) => string; tickFormat?: (value: number) => string; noun: string; loading: boolean; error: string; legend?: boolean;
  selected?: string[]; excluded?: boolean; percent?: boolean; rollingPeriods?: number; display?: ChartDisplay; cumulativeStart?: Date; scale?: ChartScale; onSelect?: (key: string | null, action: LegendAction) => void; onExpand?: () => void; onRange?: (first: ChartBucket, last: ChartBucket) => void;
  prepared?: boolean; lines?: boolean; additive?: boolean; rolling?: "average" | "sum" | "pooled"; reference?: { value: number; label: string };
  seriesHref?: (range: ChartInspectionRange, series: ChartSeries) => string | undefined;
}) {
  const root = useRef<HTMLDivElement>(null), tipRef = useRef<HTMLDivElement>(null), bars = useRef<HTMLDivElement>(null);
  const [tip, setTip] = useState<{ bucket: ChartBucket; x: number; width: number; top: number; pinned: boolean; focus: boolean } | null>(null);
  const [pin, setPin] = useViewEntry("chart_pins", title, "", location.pathname === "/analyze" && !new URLSearchParams(location.search).has("analyze_view"));
  const restoringFocus = useRef(false);
  const [drag, setDrag] = useState<{ anchor: number; current: number } | null>(null);
  // Pointer events can outrun rendering, so the handlers read the drag from a ref.
  const dragRef = useRef(drag);
  const moveDrag = (next: typeof drag) => { dragRef.current = next; setDrag(next); };
  const drawn = selected.length ? series.filter(item => excluded ? !selected.includes(item.key) : selected.includes(item.key)) : series;
  const cumulative = display === "cumulative", linear = cumulative || lines;
  // A cumulative line always shows exact totals. Retain the interval smoothing
  // setting in view state, but don't average away the accumulated amounts.
  const averagedBuckets = useMemo(() => (prepared ? sourceBuckets : cumulative ? cumulativeBuckets(sourceBuckets) : rollingAverageBuckets(sourceBuckets, rollingPeriods, { period, count: periodCount })).filter(bucket => !bucket.history), [sourceBuckets, rollingPeriods, cumulative, period, periodCount, prepared]);
  useEffect(() => { setTip(null); moveDrag(null); }, [rollingPeriods, display, sourceBuckets, selected.join("\u0000"), excluded, percent, period, periodCount, loading, error]);
  const buckets = percent && prepared ? averagedBuckets.map(bucket => ({ ...bucket,
    values: Object.fromEntries(Object.entries(bucket.values).map(([key, value]) => [key, 100 * value])),
    parts: bucket.parts && Object.fromEntries(Object.entries(bucket.parts).map(([key, entries]) => [key, entries.map(([name, value]) => [name, 100 * value] as [string, number])])) }))
  : percent ? averagedBuckets.map(bucket => {
    const total = drawn.reduce((sum, item) => sum + (bucket.values[item.key] ?? 0), 0);
    const values = Object.fromEntries(drawn.map(item => [item.key, total ? 100 * (bucket.values[item.key] ?? 0) / total : 0]));
    const parts = Object.fromEntries(Object.entries(bucket.parts ?? {}).map(([key, entries]) => [key, entries.map(([name, value]) => [name, total ? 100 * value / total : 0] as [string, number])]));
    return { ...bucket, values, parts };
  }) : averagedBuckets;
  const total = (bucket: ChartBucket) => drawn.reduce((sum, item) => sum + (bucket.values[item.key] ?? 0), 0);
  const peaks = (linear ? buckets.map(bucket => drawn.reduce((peak, item) => Math.max(peak, bucket.values[item.key] ?? 0), 0)) : buckets.map(total)).concat(reference ? [reference.value] : []);
  const { y, ticks, split } = percent ? { y: (value: number) => value, ticks: [25, 50, 75, 100], split: undefined } : columnScale(peaks, scale);
  const unit = periodUnit(period, periodCount), stacked = drawn.length > 1 && additive;
  const averageDuration = (size: number) => `${size * periodCount}-${chartPeriods[period].unit}`;
  const averageLabel = !cumulative && rollingPeriods > 1 ? `${averageDuration(rollingPeriods)} rolling ${rolling}` : "";
  const chartLabel = `${averageLabel ? `${title}; ${averageLabel}` : title}; ${percent || scale === "linear" ? "linear" : scale === "logarithmic" ? "logarithmic" : "linear and logarithmic"} scale`;
  const label = (date: Date) => isHourly(period) ? date.toLocaleString(undefined, { month: "short", day: "numeric", hour: "numeric", minute: "2-digit", timeZoneName: "short" }) : period === "month" ? date.toLocaleDateString(undefined, { month: "short", year: "numeric" }) : date.toLocaleDateString(undefined, { month: "short", day: "numeric", ...(period === "day" && buckets.length > 120 ? { year: "2-digit" } : {}) });
  const heading = (bucket: ChartBucket) => {
    if (cumulative) {
      const end = new Date(bucket.date); nextPeriod(end, period, periodCount);
      return `Cumulative through ${label(new Date(end.getTime() - 1))}`;
    }
    return `${period === "week" ? "Week of " : ""}${label(bucket.date)}${averageLabel ? ` · ${averageDuration(bucket.windowSize ?? rollingPeriods)} ${rolling}` : ""}`;
  };
  const suffix = noun ? ` ${noun}` : "";
  // A ratio of zero is a value; an amount of zero is nothing to list.
  const present = (bucket: ChartBucket) => drawn.filter(item => additive ? (bucket.values[item.key] ?? 0) > 0 : bucket.values[item.key] !== undefined).sort((left, right) => bucket.values[right.key] - bucket.values[left.key]);
  const describe = (bucket: ChartBucket) => `${heading(bucket)}: `
    + (present(bucket).map(item => `${item.label} ${format(bucket.values[item.key])}`).join(", ") || "none")
    + (stacked ? `; ${format(total(bucket))}${suffix} in all` : suffix) + (reference ? `; ${reference.label} ${format(reference.value)}` : "") + (bucket.detail ? `; ${bucket.detail}` : "");
  // Additive running totals start at zero; a series that does not add up
  // breaks its line where it has no value.
  const path = (key: string) => {
    let pen = "M";
    const steps = buckets.flatMap((bucket, index) => {
      const value = bucket.values[key] ?? (additive ? 0 : undefined);
      if (value === undefined) { pen = "M"; return []; }
      const step = `${pen} ${1000 * (index + .5) / buckets.length} ${100 - y(value)}`;
      pen = "L";
      return [step];
    });
    const lone = buckets.length === 1 && steps.length ? ` L 1000 ${100 - y(buckets[0].values[key] ?? 0)}` : "";
    return (additive && steps.length ? `M ${500 / buckets.length} ${100 - y(buckets[0].values[key] ?? 0)} ` : "") + steps.join(" ").replace(/^M/, additive ? "L" : "M") + lone;
  };
  const columnAt = (index: number) => bars.current?.querySelectorAll<HTMLButtonElement>(":scope > button")[index];
  useLayoutEffect(() => {
    const element = tipRef.current, box = root.current;
    if (!tip || !element || !box) return;
    const width = element.offsetWidth, right = tip.x + tip.width / 2 + 10;
    element.style.left = `${right + width <= box.offsetWidth - 4 ? right : Math.max(0, tip.x - tip.width / 2 - 10 - width)}px`;
    element.style.top = `${Math.max(0, Math.min(tip.top, box.offsetHeight - element.offsetHeight - 4))}px`;
    if (tip.focus) element.querySelector<HTMLAnchorElement>("a")?.focus();
  }, [tip]);
  function show(index: number, pinned = false, focus = false) {
    if (restoringFocus.current || loading || error || !pinned && (tip?.pinned || dragRef.current)) return;
    const box = root.current?.getBoundingClientRect(), at = columnAt(index)?.getBoundingClientRect(), plot = bars.current?.getBoundingClientRect();
    if(pinned)setPin(buckets[index].key);
    if (box && at && plot) setTip({ bucket: buckets[index], x: at.left - box.left + at.width / 2, width: at.width, top: plot.top - box.top, pinned, focus });
  }
  useEffect(() => {
    if(!pin||loading||error)return;
    const index=buckets.findIndex(bucket=>bucket.key===pin);if(index<0)return;
    const frame=requestAnimationFrame(()=>show(index,true));return()=>cancelAnimationFrame(frame);
  }, [pin, sourceBuckets, percent, loading, error]);
  const hidePreview = () => { if (!tip?.pinned) setTip(null); };
  useEffect(() => {
    if (!tip && !drag) return;
    const cancel = (event: KeyboardEvent) => {
      if (event.key !== "Escape") return;
      moveDrag(null); setTip(null);if(pin)setPin("");
      if (tip?.pinned) {
        restoringFocus.current = true;
        columnAt(buckets.findIndex(bucket => bucket.key === tip.bucket.key))?.focus();
        restoringFocus.current = false;
      }
    };
    const outside = (event: PointerEvent) => {
      if (event.target instanceof Node && !bars.current?.contains(event.target) && !tipRef.current?.contains(event.target)) {setTip(null);if(pin)setPin("");}
    };
    const resize = () => { moveDrag(null); setTip(null); };
    window.addEventListener("keydown", cancel);
    document.addEventListener("pointerdown", outside);
    window.addEventListener("resize", resize);
    return () => { window.removeEventListener("keydown", cancel); document.removeEventListener("pointerdown", outside); window.removeEventListener("resize", resize); };
  }, [tip, Boolean(drag), pin]);
  const indexAt = (x: number) => {
    const box = bars.current?.getBoundingClientRect();
    return box?.width ? Math.max(0, Math.min(buckets.length - 1, Math.floor((x - box.left) / box.width * buckets.length))) : 0;
  };
  const span = (value: { anchor: number; current: number } | null) => value ? chartGesture(value.anchor, value.current) : null;
  const picked = span(drag);
  const picking = buckets.length && !loading && !error ? {
    onPointerDown(event: React.PointerEvent<HTMLDivElement>) {
      if (event.button !== 0 || !event.isPrimary) return;
      event.currentTarget.setPointerCapture(event.pointerId);
      const index = indexAt(event.clientX);
      moveDrag({ anchor: index, current: index });
    },
    onPointerMove(event: React.PointerEvent<HTMLDivElement>) {
      const current = dragRef.current, index = indexAt(event.clientX);
      if (current && index !== current.current) moveDrag({ ...current, current: index });
    },
    onPointerUp(event: React.PointerEvent<HTMLDivElement>) {
      const current = dragRef.current;
      const range = current ? span({ ...current, current: indexAt(event.clientX) }) : null;
      if (!range) return;
      if (event.currentTarget.hasPointerCapture(event.pointerId)) event.currentTarget.releasePointerCapture(event.pointerId);
      moveDrag(null);
      if (range.inspect) show(range.first, true);
      else { setTip(null); onRange?.(buckets[range.first], buckets[range.last]); }
    },
    onPointerCancel() { moveDrag(null); },
    onLostPointerCapture() { moveDrag(null); },
  } : {};
  const density = buckets.length > 300 ? "denser" : buckets.length > 120 ? "dense" : "";
  const middle = buckets[Math.floor(buckets.length / 2)], last = buckets[buckets.length - 1];
  const selectable = (item: ChartSeries) => Boolean(onSelect) && item.value !== undefined;
  return <div className="usage-chart" ref={root} data-view-field="chart_pins" data-pharos-target={`section:chart:${title}`}>
    <div className="usage-chart-head"><h3>{title}{averageLabel ? <span className="usage-chart-average-label"> {averageLabel}</span> : null}</h3><div className="usage-chart-controls">{controls}</div></div>
    {series.length > 1 || legend || onRange || reference ? <div className="usage-chart-legend">
      {series.length > 1 || legend ? [...new Set(series.map(item => item.group))].map(group => {
        const items = series.filter(item => item.group === group).map(item => {
          const on = selected.length > 0 && (excluded ? !selected.includes(item.key) : selected.includes(item.key)), dimmed = selected.length > 0 && !on;
          const content = <><span className="usage-swatch" style={{ background: item.color }} />{item.label}</>;
          return item.hiddenCount && onExpand ? <button key={item.key} type="button" className="usage-legend-item" title={`Show next ${Math.min(5, item.hiddenCount)} values`} onClick={onExpand}>{content}</button>
            : selectable(item) ? <ChartLegendChip key={item.key} item={item} active={on} dimmed={dimmed} selectionActive={selected.length > 0} soleSelection={on && !excluded && selected.length === 1} onAction={action => onSelect!(item.key, action)} />
            : <span key={item.key} className={`usage-legend-item${dimmed ? " dimmed" : ""}`}>{content}</span>;
        });
        return group ? <div key={group} className="usage-legend-group" role="group" aria-label={group}><span className="usage-legend-provider">{group}</span>{items}</div>
          : <React.Fragment key="ungrouped">{items}</React.Fragment>;
      }) : null}
      {reference ? <span className="usage-legend-item usage-legend-reference" title={reference.label}><span className="usage-swatch usage-reference-swatch" aria-hidden="true" />{reference.label} · {format(reference.value)}</span> : null}
      {selected.length > 0 ? <button type="button" className="usage-legend-item usage-legend-clear" onClick={() => onSelect?.(null, "clear")}>Show all</button> : null}
      {onRange ? <span className="usage-chart-hint">{series.some(selectable) ? "Click a key for filter options · " : ""}Click to inspect · drag across 2+ {linear ? "points" : "columns"} to filter dates</span> : null}
    </div> : null}
    {error ? <p className="query-table-error">{error}</p> : null}
    <div className={`usage-chart-plot${split ? " broken" : ""}`}>
      {split ? <div className="usage-chart-log" aria-hidden="true"><span>log scale</span></div> : null}
      {ticks.map(tick => <div key={tick} className="usage-chart-grid" style={{ bottom: `${y(tick)}%` }} aria-hidden="true"><span>{tickFormat(tick)}</span></div>)}
      <div ref={bars} className={`usage-chart-bars ${density} ${loading ? "loading" : ""} ${onRange ? "pickable" : ""} ${drag ? "picking" : ""}`} role="group" aria-busy={loading} aria-label={split ? `${chartLabel}; linear up to ${tickFormat(split)}, logarithmic above` : chartLabel} {...picking}>
        {linear && buckets.length ? <svg className="usage-chart-lines" viewBox="0 0 1000 100" preserveAspectRatio="none" aria-hidden="true">
          {drawn.map(item => <path key={item.key} fill="none" stroke={item.color} strokeWidth="2" vectorEffect="non-scaling-stroke" d={path(item.key)} />)}
        </svg> : null}
        {reference ? <div className="usage-chart-reference" style={{ bottom: `${y(reference.value)}%` }} aria-hidden="true" /> : null}
        {buckets.map((bucket, index) => {
          const className = [tip?.bucket.key === bucket.key && !drag ? "active" : "", tip?.pinned && tip.bucket.key === bucket.key && !drag ? "inspected" : "", picked && index >= picked.first && index <= picked.last ? "picked" : ""].filter(Boolean).join(" ");
          return <button key={bucket.key} type="button" aria-label={describe(bucket)} aria-pressed={Boolean(tip?.pinned && tip.bucket.key === bucket.key)} className={className} onMouseEnter={() => show(index)} onFocus={() => show(index)} onMouseLeave={hidePreview} onBlur={hidePreview}
            onClick={event => { if (event.detail === 0) show(index, true, true); }}>
            {linear ? <>{tip?.bucket.key === bucket.key && !drag ? <span className="usage-chart-line-focus" aria-hidden="true">
              {drawn.filter(item => additive || bucket.values[item.key] !== undefined).map(item => <i key={item.key} className="usage-chart-line-point" style={{ bottom: `${y(bucket.values[item.key] ?? 0)}%`, background: item.color }} />)}
            </span> : null}</> : <ColumnMarks bucket={bucket} series={drawn} scale={y} percent={percent} />}
          </button>;
        })}
      </div>
      {split ? <div className="usage-chart-break" aria-hidden="true"><span>{tickFormat(split)}</span></div> : null}
    </div>
    {buckets.length === 5000 ? <p className="muted">Showing the latest 5,000 periods.{cumulative ? " Cumulative balances include earlier activity in the selected range." : " Narrow the date range to see earlier activity."}</p> : null}
    <div className="usage-chart-axis"><span>{buckets.length ? label(buckets[0].date) : ""}</span><span>{middle && buckets.length > 4 ? label(middle.date) : ""}</span><span>{!last ? "" : last.key !== periodKey(new Date(), period, periodCount) ? label(last.date) : period === "day" ? "today" : `this ${unit}`}</span></div>
    {picked ? <div className="usage-chart-pick-status" role="status">{picked.count} {picked.count === 1 ? "period" : "periods"} · {picked.inspect ? heading(buckets[picked.first]) : `${label(buckets[picked.first].date)} – ${label(buckets[picked.last].date)}`} · Release to {picked.inspect ? "inspect" : "filter"}</div> : null}
    {tip && !drag ? <div className={`usage-chart-tip${tip.pinned ? " pinned" : ""}`} ref={tipRef} role={tip.pinned ? "group" : "status"} aria-label={tip.pinned ? `Details for ${heading(tip.bucket)}` : undefined}>
      <div className="usage-chart-tip-head">{heading(tip.bucket)}</div>
      {present(tip.bucket).length ? <ul>{present(tip.bucket).map(item => {
        const parts = [...(tip.bucket.parts?.[item.key] ?? [])].sort((left, right) => right[1] - left[1]);
        const href = tip.pinned ? seriesHref?.(chartInspectionRange(tip.bucket, period, periodCount, display, cumulativeStart ?? averagedBuckets[0]?.date), item) : undefined;
        return <li key={item.key}>
          <span className="usage-chart-tip-row"><span className="usage-swatch" style={{ background: item.color }} />{href ? <PageLink className="usage-chart-tip-label" href={href}>{item.label}</PageLink> : <span className="usage-chart-tip-label">{item.label}</span>}<span className="usage-chart-tip-value">{format(tip.bucket.values[item.key])}</span></span>
          {parts.length ? <ul className="usage-chart-tip-parts">
            {parts.slice(0, 4).map(([name, value]) => <li key={name} className="usage-chart-tip-row"><span className="usage-chart-tip-label">{name}</span><span className="usage-chart-tip-value">{format(value)}</span></li>)}
            {parts.length > 4 ? <li className="usage-chart-tip-more">+{parts.length - 4} more</li> : null}
          </ul> : null}
        </li>;
      })}</ul> : <div className="usage-chart-tip-more">Nothing in this {unit}</div>}
      {stacked && present(tip.bucket).length > 1 ? <div className="usage-chart-tip-row usage-chart-tip-total"><span className="usage-chart-tip-label">Total</span><span className="usage-chart-tip-value">{format(total(tip.bucket))}{suffix}</span></div> : null}
      {reference ? <div className="usage-chart-tip-row usage-chart-tip-total"><span className="usage-chart-tip-label">{reference.label}</span><span className="usage-chart-tip-value">{format(reference.value)}{suffix}</span></div> : null}
      {tip.bucket.detail ? <div className="usage-chart-tip-more">{tip.bucket.detail}</div> : null}
    </div> : null}
  </div>;
}

// Picking a range in a chart filters the table to it, moving to a finer
// period when the range is short.
export function pickRange(onFilter: FilterChange, fields: TimeFields, period: ChartPeriod, count: number, onPeriod: (next: ChartPeriod) => void) {
  return (first: ChartBucket, last: ChartBucket) => {
    const [from, to, end] = bucketRange(first, last, period, count);
    onFilter(where => withTimeRange(where, fields, from, to));
    const finer = finerPeriod(period, from, end, count);
    if (finer !== period) onPeriod(finer);
  };
}
