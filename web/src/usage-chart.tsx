import React, { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { isOrGroup, type WhereClause, type WhereTerm } from "@pythia-software/query-table-core";
import { chartPeriods, isHourly, localDay, nextPeriod, parseDay, periodKey, periodUnit, validPeriodCount, type ChartPeriod } from "./chart-period";
import { columnScale, defaultChartScale, type ChartScale } from "./chart-scale";
import { rollingAverageBuckets, type ChartBucket, type ChartSeries } from "./chart-series";
import { ColumnMarks } from "./column-marks";
import { ChartLegendChip } from "./chart-legend-chip";
import { type LegendAction } from "./chart-selection";
import { Icon } from "./icons";

type ChartRange = "1d" | "7d" | "30d" | "90d" | "6m" | "1y";
export type FilterChange = (change: (where: WhereTerm[]) => WhereTerm[]) => void;

// Stacked column charts by day, week (Monday), or month over the table's time
// filter, or all time. Keys match the usage dataset's day, week, and month
// fields.
const chartRanges: Array<[ChartRange, string]> = [["30d", "30 days"], ["90d", "90 days"], ["6m", "6 months"], ["1y", "1 year"]];
const pad2 = (value: number) => String(value).padStart(2, "0");
export const chartPeriodCount = (view: { period: ChartPeriod; hours: number; days: number }) => view.period === "hours" ? view.hours : view.period === "days" ? view.days : 1;

// rangeStart is the local midnight a range preset starts from.
function rangeStart(range: ChartRange): Date {
  const start = new Date();
  start.setHours(0, 0, 0, 0);
  if (range.endsWith("d")) start.setDate(start.getDate() - parseInt(range) + 1);
  else if (range === "6m") { start.setMonth(start.getMonth() - 6); start.setDate(start.getDate() + 1); }
  else { start.setFullYear(start.getFullYear() - 1); start.setDate(start.getDate() + 1); }
  return start;
}

// The charts and the table share one time filter: a row matches when its
// activity, from its first to its last timestamp, overlaps the range. Dragging
// across columns, a range preset, and a filter typed into the table all set it.
type TimeFields = { first: string; last: string };
export const usageTime: TimeFields = { first: "first_usage_at", last: "last_usage_at" };
export const writingTime: TimeFields = { first: "first_message_at", last: "last_message_at" };
// to is exclusive.
export type TimeBounds = { from?: Date; to?: Date };

// parseStamp reads a datetime filter value as the service does: RFC 3339, or a
// zone-less date or time as UTC.
function parseStamp(raw: string): Date | undefined {
  let text = raw.trim().replace(" ", "T");
  if (/^\d{4}-\d{2}-\d{2}$/.test(text)) text += "T00:00:00";
  if (/T\d{2}:\d{2}(:\d{2}(\.\d+)?)?$/.test(text)) text += "Z";
  const date = new Date(text);
  return Number.isNaN(date.getTime()) ? undefined : date;
}

// localStamp writes a time as RFC 3339 with the local offset, so a day
// boundary picked in the chart is local midnight to the service too.
function localStamp(date: Date): string {
  const offset = -date.getTimezoneOffset(), hours = Math.floor(Math.abs(offset) / 60);
  return `${localDay(date)}T${pad2(date.getHours())}:${pad2(date.getMinutes())}:00${offset < 0 ? "-" : "+"}${pad2(hours)}:${pad2(Math.abs(offset) % 60)}`;
}

function isTimeTerm(term: WhereTerm, fields: TimeFields): term is WhereClause {
  return !isOrGroup(term) && (term.field === fields.first || term.field === fields.last) && !term.negated && [">", ">=", "<", "<="].includes(term.op);
}

export function timeBounds(where: WhereTerm[], fields: TimeFields): TimeBounds {
  const bounds: TimeBounds = {};
  for (const term of where) {
    if (!isTimeTerm(term, fields)) continue;
    const at = parseStamp(term.value);
    if (!at) continue;
    if (term.op === ">" || term.op === ">=") { if (!bounds.from || at > bounds.from) bounds.from = at; }
    else {
      const end = term.op === "<=" ? new Date(at.getTime() + 1) : at;
      if (!bounds.to || end < bounds.to) bounds.to = end;
    }
  }
  return bounds;
}

export function withTimeRange(where: WhereTerm[], fields: TimeFields, from?: Date, to?: Date): WhereTerm[] {
  const kept = where.filter(term => !isTimeTerm(term, fields));
  if (from) kept.push({ field: fields.last, op: ">=", value: localStamp(from) });
  if (to) kept.push({ field: fields.first, op: "<", value: localStamp(to) });
  return kept;
}

// chartSpan is the first and last day a chart shows: the time filter's range,
// else from the earliest data to today.
export function chartSpan(bounds: TimeBounds, earliest?: Date): [Date, Date] {
  const end = bounds.to ? new Date(bounds.to.getTime() - 1) : new Date();
  return [bounds.from ?? (earliest && earliest < end ? earliest : end), end];
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

// A date selection from the chart or table remains visible alongside presets.
export function RangeControl({ bounds, onChange, editable = false, includeTime = false, shortRanges = false }: { bounds: TimeBounds; onChange: (from?: Date, to?: Date) => void; editable?: boolean; includeTime?: boolean; shortRanges?: boolean }) {
  const ranges: Array<[ChartRange, string]> = shortRanges ? [["1d", "1 day"], ["7d", "7 days"], ...chartRanges] : chartRanges;
  const active = !bounds.from && !bounds.to ? "all" : bounds.from && !bounds.to ? ranges.find(([range]) => rangeStart(range).getTime() === bounds.from!.getTime())?.[0] : undefined;
  const [custom, setCustom] = useState(false);
  useEffect(() => { if (!bounds.from && !bounds.to) setCustom(false); }, [bounds.from?.getTime(), bounds.to?.getTime()]);
  const picker = <ChartSelect label="Range" value={custom || !active ? "custom" : active}
    options={[["all", "All time"], ...ranges.map(([range, text]): [string, string] => [range, `Last ${text}`]), ...(editable ? [["custom", "Custom dates…"] as [string, string]] : active ? [] : [["custom", rangeLabel(bounds), "Dates selected in the chart or table"] as [string, string, string]])]}
    title="Dates included in this usage view. Choose All time to clear the date filter."
    onChange={range => { setCustom(range === "custom"); if (range !== "custom") onChange(range === "all" ? undefined : rangeStart(range as ChartRange)); }} />;
  return editable ? <div className={`usage-date-range${custom || !active ? " custom" : ""}`}>{picker}{custom || !active ? <CustomDates key={includeTime ? "time" : "date"} bounds={bounds} onChange={onChange} includeTime={includeTime} /> : null}</div> : picker;
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
      <select aria-label={ariaLabel} value={value} disabled={disabled} title={title} onChange={event => onChange(event.target.value as T)}>
        {options.map(([option, text, reason]) => <option key={option} value={option} disabled={Boolean(reason)} title={reason}>{text}</option>)}
      </select>
      <Icon name="chevron-down" className="usage-chart-caret" />
    </span>
    {note ? <span className="usage-chart-control-note">{note}</span> : null}
  </label>;
}

export type PeriodView = { period: ChartPeriod; hours: number; days: number };
export function PeriodControl({ view, onChange }: { view: PeriodView; onChange: (next: Partial<PeriodView>) => void }) {
  const custom = view.period === "hours" || view.period === "days" ? view.period : null;
  return <div className="usage-chart-field usage-period-control" role="group" aria-label="Chart interval">
    <ChartSelect label="Interval" ariaLabel="Chart interval" value={view.period}
      options={[["hour", "Hour"], ["day", "Day"], ["week", "Week"], ["month", "Month"], ["hours", "Custom hours…"], ["days", "Custom days…"]]}
      title="Time covered by each chart column" onChange={period => onChange({ period })} />
    {custom ? <CustomPeriod key={custom} period={custom} value={view[custom]} onChange={value => onChange({ [custom]: value })} /> : null}
  </div>;
}

function CustomPeriod({ period, value, onChange }: { period: "hours" | "days"; value: number; onChange: (value: number) => void }) {
  const [draft, setDraft] = useState(String(value));
  useEffect(() => setDraft(String(value)), [value]);
  return <label className="usage-period-custom">
    <span>Every</span>
    <input type="number" aria-label={`Number of ${period}`} min={1} max={10000} step={1} value={draft}
      onChange={event => { setDraft(event.target.value); if (validPeriodCount(event.target.valueAsNumber)) onChange(event.target.valueAsNumber); }}
      onBlur={() => setDraft(String(value))} />
    <span>{period}</span>
  </label>;
}

export function ScaleControl({ value, onChange, percent = false }: { value: ChartScale; onChange: (next: ChartScale) => void; percent?: boolean }) {
  const descriptions = { linear: "Column heights are proportional to their values.", logarithmic: "Compresses large values so smaller values are easier to compare.", half: "Linear for smaller values, logarithmic for peaks." };
  return <ChartSelect label="Scale" value={percent ? "linear" : value} disabled={percent}
    options={[["linear", "Linear"], ["logarithmic", "Logarithmic"], ["half", "Linear + log"]]}
    title={descriptions[percent ? "linear" : value]} note={percent ? "Shares use a linear scale." : undefined} onChange={onChange} />;
}

export function RollingControl({ value, period, count = 1, onChange }: { value: number; period: ChartPeriod; count?: number; onChange: (next: number) => void }) {
  const unit = chartPeriods[period].unit;
  return <ChartSelect label="Smoothing" value={String(value <= 1 ? 0 : value)}
    options={[["0", "Off"], ...Array.from({ length: 9 }, (_, index): [string, string] => [String(index + 2), `${(index + 2) * count} ${unit}s`])]}
    title="Trailing average including the current interval. Off shows the original values. Empty intervals count as zero; the start of the range uses available intervals only."
    onChange={next => onChange(Number(next))} />;
}

// The legend can name even a single series. Hovering a column shows a key of the series
// it holds, largest first, with what a folded series contains.
// With onSelect, clicking a key selects its series: only selected series are
// drawn and the rest stay in the key, dimmed. With onRange, dragging across
// columns (or clicking or pressing Enter on one) picks a time range.
export function StackedColumns({ title, controls, series, buckets: sourceBuckets, period, periodCount = 1, rollingPeriods = 0, scale = defaultChartScale, format, tickFormat = format, noun, loading, error, legend = false, selected = [], excluded = false, percent = false, onSelect, onExpand, onRange }: {
  title: string; controls: React.ReactNode; series: ChartSeries[]; buckets: ChartBucket[]; period: ChartPeriod; periodCount?: number;
  format: (value: number) => string; tickFormat?: (value: number) => string; noun: string; loading: boolean; error: string; legend?: boolean;
  selected?: string[]; excluded?: boolean; percent?: boolean; rollingPeriods?: number; scale?: ChartScale; onSelect?: (key: string | null, action: LegendAction) => void; onExpand?: () => void; onRange?: (first: ChartBucket, last: ChartBucket) => void;
}) {
  const root = useRef<HTMLDivElement>(null), tipRef = useRef<HTMLDivElement>(null), bars = useRef<HTMLDivElement>(null);
  const [tip, setTip] = useState<{ bucket: ChartBucket; x: number; width: number; top: number } | null>(null);
  const [drag, setDrag] = useState<{ anchor: number; current: number } | null>(null);
  // Pointer events can outrun rendering, so the handlers read the drag from a ref.
  const dragRef = useRef(drag);
  const moveDrag = (next: typeof drag) => { dragRef.current = next; setDrag(next); };
  const drawn = selected.length ? series.filter(item => excluded ? !selected.includes(item.key) : selected.includes(item.key)) : series;
  const averagedBuckets = useMemo(() => rollingAverageBuckets(sourceBuckets, rollingPeriods), [sourceBuckets, rollingPeriods]);
  useEffect(() => setTip(null), [rollingPeriods]);
  const buckets = percent ? averagedBuckets.map(bucket => {
    const total = drawn.reduce((sum, item) => sum + (bucket.values[item.key] ?? 0), 0);
    const values = Object.fromEntries(drawn.map(item => [item.key, total ? 100 * (bucket.values[item.key] ?? 0) / total : 0]));
    const parts = Object.fromEntries(Object.entries(bucket.parts ?? {}).map(([key, entries]) => [key, entries.map(([name, value]) => [name, total ? 100 * value / total : 0] as [string, number])]));
    return { ...bucket, values, parts };
  }) : averagedBuckets;
  const total = (bucket: ChartBucket) => drawn.reduce((sum, item) => sum + (bucket.values[item.key] ?? 0), 0);
  const { y, ticks, split } = percent ? { y: (value: number) => value, ticks: [25, 50, 75, 100], split: undefined } : columnScale(buckets.map(total), scale);
  const unit = periodUnit(period, periodCount), stacked = drawn.length > 1;
  const averageDuration = (size: number) => `${size * periodCount}-${chartPeriods[period].unit}`;
  const averageLabel = rollingPeriods > 1 ? `${averageDuration(rollingPeriods)} rolling average` : "";
  const chartLabel = `${averageLabel ? `${title}; ${averageLabel}` : title}; ${percent || scale === "linear" ? "linear" : scale === "logarithmic" ? "logarithmic" : "linear and logarithmic"} scale`;
  const label = (date: Date) => isHourly(period) ? date.toLocaleString(undefined, { month: "short", day: "numeric", hour: "numeric", minute: "2-digit", timeZoneName: "short" }) : period === "month" ? date.toLocaleDateString(undefined, { month: "short", year: "numeric" }) : date.toLocaleDateString(undefined, { month: "short", day: "numeric", ...(period === "day" && buckets.length > 120 ? { year: "2-digit" } : {}) });
  const heading = (bucket: ChartBucket) => `${period === "week" ? "Week of " : ""}${label(bucket.date)}${averageLabel ? ` · ${averageDuration(bucket.windowSize ?? rollingPeriods)} average` : ""}`;
  const suffix = noun ? ` ${noun}` : "";
  const present = (bucket: ChartBucket) => drawn.filter(item => (bucket.values[item.key] ?? 0) > 0).sort((left, right) => bucket.values[right.key] - bucket.values[left.key]);
  const describe = (bucket: ChartBucket) => `${heading(bucket)}: `
    + (present(bucket).map(item => `${item.label} ${format(bucket.values[item.key])}`).join(", ") || "none")
    + (stacked ? `; ${format(total(bucket))}${suffix} in all` : suffix) + (bucket.detail ? `; ${bucket.detail}` : "");
  useLayoutEffect(() => {
    const element = tipRef.current, box = root.current;
    if (!tip || !element || !box) return;
    const width = element.offsetWidth, right = tip.x + tip.width / 2 + 10;
    element.style.left = `${right + width <= box.offsetWidth - 4 ? right : Math.max(0, tip.x - tip.width / 2 - 10 - width)}px`;
    element.style.top = `${Math.max(0, Math.min(tip.top, box.offsetHeight - element.offsetHeight - 4))}px`;
  }, [tip]);
  function show(event: React.SyntheticEvent<HTMLElement>, bucket: ChartBucket) {
    const box = root.current?.getBoundingClientRect(), at = event.currentTarget.getBoundingClientRect(), plot = event.currentTarget.parentElement?.getBoundingClientRect();
    if (box && plot) setTip({ bucket, x: at.left - box.left + at.width / 2, width: at.width, top: plot.top - box.top });
  }
  useEffect(() => {
    if (!drag) return;
    const cancel = (event: KeyboardEvent) => { if (event.key === "Escape") moveDrag(null); };
    window.addEventListener("keydown", cancel);
    return () => window.removeEventListener("keydown", cancel);
  }, [Boolean(drag)]);
  const indexAt = (x: number) => {
    const box = bars.current?.getBoundingClientRect();
    return box?.width ? Math.max(0, Math.min(buckets.length - 1, Math.floor((x - box.left) / box.width * buckets.length))) : 0;
  };
  const span = (value: { anchor: number; current: number } | null) => value ? [Math.min(value.anchor, value.current), Math.max(value.anchor, value.current)] : null;
  const picked = span(drag);
  const picking = onRange && buckets.length ? {
    onPointerDown(event: React.PointerEvent<HTMLDivElement>) {
      if (event.button !== 0) return;
      event.currentTarget.setPointerCapture(event.pointerId);
      const index = indexAt(event.clientX);
      moveDrag({ anchor: index, current: index });
    },
    onPointerMove(event: React.PointerEvent<HTMLDivElement>) {
      const current = dragRef.current, index = indexAt(event.clientX);
      if (current && index !== current.current) moveDrag({ ...current, current: index });
    },
    onPointerUp() {
      const range = span(dragRef.current);
      if (!range) return;
      moveDrag(null);
      setTip(null);
      onRange(buckets[range[0]], buckets[range[1]]);
    },
    onPointerCancel() { moveDrag(null); },
  } : {};
  const density = buckets.length > 300 ? "denser" : buckets.length > 120 ? "dense" : "";
  const middle = buckets[Math.floor(buckets.length / 2)], last = buckets[buckets.length - 1];
  const selectable = (item: ChartSeries) => Boolean(onSelect) && item.value !== undefined;
  return <div className="usage-chart" ref={root}>
    <div className="usage-chart-head"><h3>{title}{averageLabel ? <span className="usage-chart-average-label"> {averageLabel}</span> : null}</h3><div className="usage-chart-controls">{controls}</div></div>
    {series.length > 1 || legend || onRange ? <div className="usage-chart-legend">
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
      {selected.length > 0 ? <button type="button" className="usage-legend-item usage-legend-clear" onClick={() => onSelect?.(null, "clear")}>Show all</button> : null}
      {onRange ? <span className="usage-chart-hint">{series.some(selectable) ? "Click a key for filter options · drag" : "Drag"} across columns to filter dates</span> : null}
    </div> : null}
    {error ? <p className="query-table-error">{error}</p> : null}
    <div className={`usage-chart-plot${split ? " broken" : ""}`}>
      {split ? <div className="usage-chart-log" aria-hidden="true"><span>log scale</span></div> : null}
      {ticks.map(tick => <div key={tick} className="usage-chart-grid" style={{ bottom: `${y(tick)}%` }} aria-hidden="true"><span>{tickFormat(tick)}</span></div>)}
      <div ref={bars} className={`usage-chart-bars ${density} ${loading ? "loading" : ""} ${onRange ? "pickable" : ""}`} role="list" aria-busy={loading} aria-label={split ? `${chartLabel}; linear up to ${tickFormat(split)}, logarithmic above` : chartLabel} {...picking}>
        {buckets.map((bucket, index) => {
          const className = [tip?.bucket.key === bucket.key && !drag ? "active" : "", picked && index >= picked[0] && index <= picked[1] ? "picked" : ""].filter(Boolean).join(" ");
          return <span key={bucket.key} role="listitem" tabIndex={0} aria-label={describe(bucket)} className={className} onMouseEnter={event => show(event, bucket)} onFocus={event => show(event, bucket)} onMouseLeave={() => setTip(null)} onBlur={() => setTip(null)}
            onKeyDown={onRange ? event => { if (event.key === "Enter" || event.key === " ") { event.preventDefault(); onRange(bucket, bucket); } } : undefined}>
            <ColumnMarks bucket={bucket} series={drawn} scale={y} percent={percent} />
          </span>;
        })}
      </div>
      {split ? <div className="usage-chart-break" aria-hidden="true"><span>{tickFormat(split)}</span></div> : null}
      {picked ? <div className="usage-chart-pick" style={{ left: `${100 * picked[0] / buckets.length}%`, width: `${100 * (picked[1] - picked[0] + 1) / buckets.length}%` }} aria-hidden="true">
        <span>{picked[0] === picked[1] ? heading(buckets[picked[0]]) : `${label(buckets[picked[0]].date)} – ${label(buckets[picked[1]].date)}`}</span>
      </div> : null}
    </div>
    {buckets.length === 5000 ? <p className="muted">Showing the latest 5,000 periods. Narrow the date range to see earlier activity.</p> : null}
    <div className="usage-chart-axis"><span>{buckets.length ? label(buckets[0].date) : ""}</span><span>{middle && buckets.length > 4 ? label(middle.date) : ""}</span><span>{!last ? "" : last.key !== periodKey(new Date(), period, periodCount) ? label(last.date) : period === "day" ? "today" : `this ${unit}`}</span></div>
    {tip && !drag ? <div className="usage-chart-tip" ref={tipRef} role="status">
      <div className="usage-chart-tip-head">{heading(tip.bucket)}</div>
      {present(tip.bucket).length ? <ul>{present(tip.bucket).map(item => {
        const parts = [...(tip.bucket.parts?.[item.key] ?? [])].sort((left, right) => right[1] - left[1]);
        return <li key={item.key}>
          <span className="usage-chart-tip-row"><span className="usage-swatch" style={{ background: item.color }} /><span className="usage-chart-tip-label">{item.label}</span><span className="usage-chart-tip-value">{format(tip.bucket.values[item.key])}</span></span>
          {parts.length ? <ul className="usage-chart-tip-parts">
            {parts.slice(0, 4).map(([name, value]) => <li key={name} className="usage-chart-tip-row"><span className="usage-chart-tip-label">{name}</span><span className="usage-chart-tip-value">{format(value)}</span></li>)}
            {parts.length > 4 ? <li className="usage-chart-tip-more">+{parts.length - 4} more</li> : null}
          </ul> : null}
        </li>;
      })}</ul> : <div className="usage-chart-tip-more">Nothing in this {unit}</div>}
      {stacked && present(tip.bucket).length > 1 ? <div className="usage-chart-tip-row usage-chart-tip-total"><span className="usage-chart-tip-label">Total</span><span className="usage-chart-tip-value">{format(total(tip.bucket))}{suffix}</span></div> : null}
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
