import React, { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { clipIntervals, concurrencyAverage, concurrencyMetrics, concurrencySpans, concurrencyUnit, conversationTimings, formatConcurrencyDuration as duration, mergeIntervals, minute, ribbonPath, stackSegments, workSessions, zonedDayBounds, type ConcurrencyCounting, type ConcurrencyData, type ConcurrencyMetrics, type ConcurrencyMode, type ConcurrencyThread, type Interval, type Segment } from "./concurrency-data";
import { SmoothingControl } from "./usage-chart";
import { concurrencyRangePresets, readConcurrencySettings, readMachineConcurrencySettings, normalizeConcurrencySettings, resolveConcurrencyRange, saveConcurrencySettings, type ConcurrencyRange, type ConcurrencySettings } from "./concurrency-settings";
import { closeConcurrencyModal, loadConcurrencyData } from "./concurrency-store";
import { pushMachineLocation } from "./machine-location";
import { useDrilldownHistory, ZoomNavigation } from "./machine-navigation";
import { Icon } from "./icons";
import type { MachineScope, MachineWindow } from "./machine-activity-data";
import "./concurrency.css";

export const concurrencyPrototypeEnabled = document.documentElement.hasAttribute("data-concurrency-prototype");
const palette = ["var(--sea)", "var(--gold)", "var(--heat-0)", "var(--heat-1)", "var(--heat-2)", "var(--heat-3)",
  "color-mix(in srgb,var(--sea) 45%,var(--gold))", "color-mix(in srgb,var(--heat-1) 55%,var(--heat-3))",
  "color-mix(in srgb,var(--sea) 40%,var(--heat-0))", "color-mix(in srgb,var(--gold) 65%,var(--heat-2))",
  "color-mix(in srgb,var(--heat-0) 40%,var(--heat-1))", "color-mix(in srgb,var(--heat-2) 60%,var(--heat-0))"];
const percent = (value: number) => `${Math.round(value * 100)}%`;
const sumSpans = (spans: Interval[]) => spans.reduce((total, [a, b]) => total + b - a, 0);
type Hover = { time: number; unit: string | null; conversation?: string };

function Metrics({ value, daily, counting }: { value: ConcurrencyMetrics; daily: boolean; counting: ConcurrencyCounting }) {
  return <div className="concurrency-metrics"><span title={`Average concurrent ${counting === "agent" ? "sessions" : "workspaces"} including idle time`}><strong>{value.average.toFixed(2)}×</strong> {daily ? "daily average" : "window average"}</span><span><strong>{percent(value.parallel)}</strong> of working time with 2+ {counting === "agent" ? "sessions" : "workspaces"}</span><span><strong>{value.peak}</strong> peak</span></div>;
}

function ConcurrencyChart({ mode, counting, threads, historyThreads, historyStart, gap, order, bounds, ceiling, colors, selected, hover, onHover, onSelect, onZoom, timezone, smoothingMinutes }: {
  mode: ConcurrencyMode; counting: ConcurrencyCounting; threads: ConcurrencyThread[]; gap: number; order: string[]; bounds: Interval; ceiling: number;
  colors: Map<string, string>; selected: string | null; hover: Hover | null; onHover: (next: Hover | null) => void;
  onSelect: (id: string) => void; onZoom: (bounds: Interval) => void; timezone: string;
  smoothingMinutes: number;
  historyThreads: ConcurrencyThread[]; historyStart: number;
}) {
  const host = useRef<HTMLDivElement>(null), svg = useRef<SVGSVGElement>(null);
  const [drag, setDrag] = useState<{ anchor: number; current: number; pointerId: number } | null>(null);
  const dragRef = useRef(drag);
  const moveDrag = (next: typeof drag) => { dragRef.current = next;setDrag(next); };
  const cancelDrag = () => {
    const pointerId = dragRef.current?.pointerId;
    moveDrag(null);onHover(null);
    if (pointerId !== undefined && svg.current?.hasPointerCapture(pointerId)) svg.current.releasePointerCapture(pointerId);
  };
  useEffect(() => {
    if (!drag) return;
    const cancel = (event: KeyboardEvent) => {
      if (event.key === "Escape") { event.preventDefault();event.stopPropagation();cancelDrag(); }
    };
    window.addEventListener("keydown", cancel, true);
    return () => window.removeEventListener("keydown", cancel, true);
  }, [!!drag]);
  const [width, setWidth] = useState(900);
  useLayoutEffect(() => {
    const element = host.current;
    if (!element) return;
    const observer = new ResizeObserver(() => setWidth(element.clientWidth));
    observer.observe(element);setWidth(element.clientWidth);
    return () => observer.disconnect();
  }, []);
  const segments = useMemo(() => stackSegments(threads, mode, gap, order, bounds, counting, true), [threads, mode, gap, order, bounds, counting]);
  const byUnit = useMemo(() => {
    const groups = new Map<string, Segment[]>();
    for (const segment of segments) { if (!groups.has(segment.unit)) groups.set(segment.unit, []);groups.get(segment.unit)!.push(segment); }
    return groups;
  }, [segments]);
  const left = 52, right = width - 18, top = 20, bottom = 232, height = 276;
  const average = useMemo(() => smoothingMinutes ? concurrencyAverage(concurrencySpans(historyThreads, mode, gap, counting), bounds, smoothingMinutes * minute, Math.ceil(width / 2), historyStart) : [], [historyThreads, historyStart, mode, gap, counting, bounds, smoothingMinutes, width]);
  const chartCeiling = Math.max(ceiling, ...average.map(point => point.value));
  const x = (value: number) => left + (value - bounds[0]) / Math.max(1, bounds[1] - bounds[0]) * (right - left);
  const y = (value: number) => bottom - value / chartCeiling * (bottom - top);
  const paths = useMemo(() => Array.from(byUnit, ([unit, cells]) => ({ unit, base: ribbonPath(cells, x, y), active: mode === "user" ? ribbonPath(cells.filter(cell => cell.running), x, y) : "" })), [byUnit, width, bounds, chartCeiling, mode]);
  const averagePath = average.map((point, index) => `${index ? "L" : "M"}${x(point.at).toFixed(2)},${y(point.value).toFixed(2)}`).join("");
  const multipleDays = new Intl.DateTimeFormat("en-CA", { timeZone: timezone }).format(bounds[0]) !== new Intl.DateTimeFormat("en-CA", { timeZone: timezone }).format(bounds[1] - 1);
  const fmt = new Intl.DateTimeFormat(undefined, { timeZone: timezone, ...(multipleDays ? { month: "short", day: "numeric" } as const : {}), ...(bounds[1] - bounds[0] < 2 * 24 * 60 * minute ? { hour: "numeric", minute: "2-digit" } as const : {}) });
  const selectionFmt = new Intl.DateTimeFormat(undefined, { timeZone: timezone, ...(multipleDays ? { month: "short", day: "numeric" } as const : {}), hour: "numeric", minute: "2-digit" });
  const picked: Interval | null = drag ? [Math.min(drag.anchor, drag.current), Math.max(drag.anchor, drag.current)] : null;
  const tickCount = width < 500 ? 3 : 7;
  const ticks = Array.from({ length: tickCount }, (_, i) => bounds[0] + (bounds[1] - bounds[0]) * i / (tickCount - 1));
  const yStep = Math.max(1, Math.ceil(chartCeiling / 6));
  const yTicks = Array.from({ length: Math.floor(chartCeiling / yStep) + 1 }, (_, i) => i * yStep);
  const coordinates = (event: React.PointerEvent<SVGSVGElement>) => {
    const rect = svg.current!.getBoundingClientRect(), px = Math.max(left, Math.min(right, (event.clientX - rect.left) * width / rect.width));
    const time = bounds[0] + (px - left) / (right - left) * (bounds[1] - bounds[0]);
    const count = (bottom - (event.clientY - rect.top)) / (bottom - top) * chartCeiling;
    const segment = segments.find(s => time >= s.start && time < s.end && count >= s.bottom && count < s.bottom + 1);
    return { time, unit: segment?.unit ?? null, conversation: segment?.conversation };
  };
  return <div className={`concurrency-chart${drag ? " dragging" : ""}`} ref={host}>
    <svg ref={svg} viewBox={`0 0 ${width} ${height}`} height={height} role="img" aria-label={`${mode === "user" ? "Continuous conversation threads" : "Estimated model activity"} stacked by ${counting === "agent" ? "session" : "workspace"}; drag to zoom`}
      onPointerMove={event => { const point = coordinates(event), current = dragRef.current;if (current) moveDrag({ ...current, current: point.time });else onHover(point); }} onPointerLeave={() => { if (!dragRef.current) onHover(null); }}
      onPointerDown={event => {
        if (event.button !== 0) return;
        const rect = event.currentTarget.getBoundingClientRect();
        if (event.clientY - rect.top < top || event.clientY - rect.top > bottom) return;
        event.preventDefault();const time = coordinates(event).time;
        moveDrag({ anchor: time, current: time, pointerId: event.pointerId });onHover(null);event.currentTarget.setPointerCapture(event.pointerId);
      }}
      onPointerUp={event => {
        const current = dragRef.current;if (!current || current.pointerId !== event.pointerId) return;
        const point = coordinates(event);cancelDrag();
        if (Math.abs(x(point.time) - x(current.anchor)) > 6) onZoom([Math.min(current.anchor, point.time), Math.max(current.anchor, point.time)]);
        else if (point.unit) onSelect(point.unit);
      }} onPointerCancel={cancelDrag} onLostPointerCapture={() => { if (dragRef.current) cancelDrag(); }}>
      <title>{mode === "user" ? "User parallelism" : "True parallelism (estimated)"}</title>
      <desc>Time runs left to right. Each {counting === "agent" ? "independently started conversation" : "workspace"} occupies one unit of height. Ribbon colors identify repositories. Select a ribbon to see its conversations.</desc>
      <text x={left} y={12} className="concurrency-axis-title">Concurrent {counting === "agent" ? "sessions" : "workspaces"}</text>
      {yTicks.map(value => <g key={value}><line x1={left} x2={right} y1={y(value)} y2={y(value)} className="concurrency-grid"/><text x={left - 9} y={y(value) + 4} textAnchor="end">{value}</text></g>)}
      {paths.map(({ unit, base, active }) => <g key={unit} opacity={!selected || selected === unit ? 1 : .14}>
        <path d={base} fill={colors.get(unit)} opacity={mode === "user" ? .28 : .88}/>
        {mode === "user" ? <path d={active} fill={colors.get(unit)} opacity=".78"/> : null}
      </g>)}
      {averagePath ? <path d={averagePath} fill="none" stroke="var(--ink)" strokeWidth="2.5" pointerEvents="none"><title>{smoothingMinutes}-minute trailing average of concurrent {counting === "agent" ? "sessions" : "workspaces"}</title></path> : null}
      <rect x={left} y={top} width={Math.max(0, right - left)} height={bottom - top} fill="none" className="concurrency-frame"/>
      {ticks.map((at, i) => <text key={at} x={x(at)} y={bottom + 20} textAnchor={i === 0 ? "start" : i === ticks.length - 1 ? "end" : "middle"}>{fmt.format(at)}</text>)}
      <text x={(left + right) / 2} y={height - 4} textAnchor="middle">{multipleDays ? "Date" : "Time of day"} · {timezone}</text>
      {!drag && hover && hover.time >= bounds[0] && hover.time <= bounds[1] ? <g className="concurrency-hover-guide"><line x1={x(hover.time)} x2={x(hover.time)} y1={top} y2={bottom}/>{segments.filter(s => hover.time >= s.start && hover.time < s.end).map(s => <circle key={s.unit} cx={x(hover.time)} cy={y(s.bottom + .5)} r="3" fill={colors.get(s.unit)}/>)}</g> : null}
      {picked ? <rect className="concurrency-pick" x={x(picked[0])} y={top} width={Math.max(1, x(picked[1]) - x(picked[0]))} height={bottom - top} rx="3" aria-hidden="true" /> : null}
    </svg>
    {picked ? <div className="concurrency-pick-label" role="status" style={{ left: `${Math.max(0, Math.min(width - 300, (x(picked[0]) + x(picked[1])) / 2 - 150))}px` }}>{selectionFmt.format(picked[0])} – {selectionFmt.format(picked[1])} · {duration(picked[1] - picked[0])}</div> : null}
  </div>;
}

export function ConcurrencyModal() {
  const requested = () => new URLSearchParams(location.search).get("parallelism") === "1";
  const [open, setOpen] = useState(requested);
  useEffect(() => {
    const restore = () => setOpen(requested());
    const events = ["pharos:route", "pharos:parallelism"];
    events.forEach(event => window.addEventListener(event, restore));
    return () => events.forEach(event => window.removeEventListener(event, restore));
  }, []);
  return open ? <ConcurrencyDialog /> : null;
}

function ConcurrencyDialog() {
  const dialog = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const element = dialog.current!, previous = document.activeElement as HTMLElement | null;
    element.showModal();
    return () => { element.close(); previous?.focus({ preventScroll: true }); };
  }, []);
  return <dialog ref={dialog} className="concurrency-dialog" aria-labelledby="parallelism-title" data-feedback-label="Parallelism modal"
    onCancel={event => { event.preventDefault(); closeConcurrencyModal(); }}
    onClick={event => { const rect = event.currentTarget.getBoundingClientRect(); if (event.target === event.currentTarget && (event.clientX < rect.left || event.clientX > rect.right || event.clientY < rect.top || event.clientY > rect.bottom)) closeConcurrencyModal(); }}>
    <header className="concurrency-dialog-heading"><div><h2 id="parallelism-title">Parallelism</h2><p>Concurrent activity over time.</p></div><button type="button" className="concurrency-close" aria-label="Close parallelism" onClick={closeConcurrencyModal}><Icon name="close" /></button></header>
    <ConcurrencyPanel />
  </dialog>;
}

function ConcurrencyInfoDialog({ data, onClose }: { data: ConcurrencyData; onClose: () => void }) {
  const dialog = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const element = dialog.current!, previous = document.activeElement as HTMLElement | null;
    element.showModal();
    return () => { element.close();if (previous?.isConnected) previous.focus({ preventScroll: true }); };
  }, []);
  return <dialog ref={dialog} id="parallelism-calculation" className="concurrency-info-dialog" aria-labelledby="parallelism-calculation-title"
    onCancel={event => { event.preventDefault();event.stopPropagation();onClose(); }}
    onClick={event => {
      event.stopPropagation();const rect = event.currentTarget.getBoundingClientRect();
      if (event.target === event.currentTarget && (event.clientX < rect.left || event.clientX > rect.right || event.clientY < rect.top || event.clientY > rect.bottom)) onClose();
    }}>
    <header className="concurrency-dialog-heading"><h2 id="parallelism-calculation-title">How parallelism is calculated</h2><button type="button" className="concurrency-close" aria-label="Close calculation details" onClick={onClose}><Icon name="close" /></button></header>
    <h3>Threaded</h3><p>Connects turns within each root conversation using the selected turn gap. Gaps up to that length count as continuous activity, with no padding after the final turn. Pale ribbons show the gaps between turns.</p>
    <h3>Model activity</h3><p>Estimates when a model is running from input-to-response timestamps, excluding recorded tool waits. This is estimated timing rather than measured inference telemetry.</p>
    <h3>Workspaces or sessions</h3><p><strong>Workspaces</strong> counts each checkout once, even when it has several conversations active. <strong>Sessions</strong> counts each independently started root conversation separately, including conversations in the same checkout. Linked delegated compute belongs to its parent conversation in either view. Colors identify repositories, and rows stay with the same repository where packing allows.</p>
    <h3>Averages and working time</h3><p>Average parallelism includes the full selected date range or zoom interval, including idle time. Header summary columns use their full trailing windows. Working time and its percentage with 2+ workspaces or sessions use a fixed one-hour idle cutoff.</p>
    <h3>Timing coverage</h3><p>This timing data covers {data.period_start} – {data.period_end} in {data.timezone}. {data.coverage.mirrors_removed} source mirrors were removed and {data.coverage.unexplained_waits_excluded} unexplained waits over ten minutes were omitted. Header stats use the saved counting choice, turn gap and repository; windows outside this timing export show —.</p>
  </dialog>;
}

export type ConcurrencySelection = { settings: ConcurrencySettings; bounds: Interval; selected: ConcurrencyThread | null };
export function ConcurrencyPanel({ scope, activityWindow, routeParameter, onSelection }: { scope?: MachineScope; activityWindow?: MachineWindow; routeParameter?: string; onSelection?: (selection: ConcurrencySelection) => void } = {}) {
  const [data, setData] = useState<ConcurrencyData | null>(null), [error, setError] = useState("");
  const readSettings = () => {
    if (routeParameter) {
      const encoded = new URLSearchParams(location.search).get(routeParameter);
      if (encoded) { try { return readConcurrencySettings(JSON.parse(encoded)); } catch { /* Use the saved counting settings. */ } }
      return { ...readConcurrencySettings(), range: "30d" as const, workspace: "", agent: "", zoom: null };
    }
    return scope ? readMachineConcurrencySettings() : readConcurrencySettings();
  };
  const [settings, setSettings] = useState<ConcurrencySettings>(readSettings);
  const selectionCallback = useRef(onSelection); selectionCallback.current = onSelection;
  const [hover, setHover] = useState<Hover | null>(null);
  const [routeRevision, setRouteRevision] = useState(0);
  const [showInfo, setShowInfo] = useState(false);
  const update = (next: Partial<ConcurrencySettings>) => {
    const merged = normalizeConcurrencySettings({ ...settings, ...next });
    if (scope || routeParameter) {
      const url = new URL(location.href);
      url.searchParams.set(routeParameter ?? "q_machine_parallelism", JSON.stringify(merged));
      pushMachineLocation(url);
    }
    saveConcurrencySettings(merged); setHover(null);
  };
  useEffect(() => {
    const parameter = routeParameter ?? (scope ? "q_machine_parallelism" : "");
    const url = new URL(location.href);
    url.searchParams.delete("machine_demo");
    if (parameter && !url.searchParams.has(parameter)) url.searchParams.set(parameter, JSON.stringify(readSettings()));
    history.replaceState(history.state, "", url);
    const restore = () => { setSettings(readSettings()); setHover(null); };
    window.addEventListener("pharos:concurrency-settings", restore);
    const restoreRoute = () => { restore(); setRouteRevision(value => value + 1); };
    if (scope || routeParameter) window.addEventListener("pharos:route", restoreRoute);
    return () => { window.removeEventListener("pharos:concurrency-settings", restore); window.removeEventListener("pharos:route", restoreRoute); };
  }, []);
  useEffect(() => {
    let active = true;
    setError(""); setData(null);
    if (!concurrencyPrototypeEnabled) return;
    loadConcurrencyData().then(next => { if (active) setData(next); }).catch(reason => { if (active) setError(String(reason.message ?? reason)); });
    return () => { active = false; };
  }, []);
  const scopeKey = JSON.stringify(scope);
  useEffect(() => { if (scope) { setSettings(readMachineConcurrencySettings()); setHover(null); } }, [scopeKey]);
  const repositories = useMemo(() => Array.from(new Set(data?.threads.map(thread => thread.repository))).sort(), [data]);
  const repository = scope ? scope.repository : repositories.includes(settings.repository) ? settings.repository : "";
  const range = useMemo(() => {
    if (!data) return { startDay: "", endDay: "", bounds: [0, 1] as Interval };
    if (!scope) return resolveConcurrencyRange(data, settings);
    const captured = resolveConcurrencyRange(data, { ...settings, range: "all" });
    if (!scope.days || !activityWindow) return captured;
    const start = Math.max(captured.bounds[0], Date.parse(activityWindow.start)), end = Math.min(captured.bounds[1], Date.parse(activityWindow.end));
    return { startDay: new Date(start).toISOString().slice(0, 10), endDay: new Date(end - 1).toISOString().slice(0, 10), bounds: [start, Math.max(start + 1, end)] as Interval };
  }, [data, settings.range, settings.day, settings.startDay, settings.endDay, scope?.days, activityWindow]);
  const rangeBounds = range.bounds;
  const singleDay = range.startDay === range.endDay;
  const { mode, gap, counting } = settings;
  const threads = useMemo(() => (data?.threads ?? []).filter(thread => (!repository || thread.repository === repository) && (!scope?.provider || thread.provider === scope.provider) && (!scope?.model || thread.model === scope.model)), [data, repository, scope?.provider, scope?.model]);
  const sessions = useMemo(() => workSessions(threads), [threads]);
  const rangeThreads = useMemo(() => threads.map(thread => ({ ...thread, turns: clipIntervals(thread.turns, rangeBounds), model_spans: clipIntervals(thread.model_spans, rangeBounds), continuous_spans: clipIntervals(mergeIntervals(thread.turns, gap * minute), rangeBounds) })).filter(thread => thread.turns.length || thread.model_spans.length || thread.continuous_spans.length), [threads, rangeBounds, gap]);
  const rangeSessions = useMemo(() => clipIntervals(sessions, rangeBounds), [sessions, rangeBounds]);
  const zoom = settings.zoom && settings.zoom[0] >= rangeBounds[0] && settings.zoom[1] <= rangeBounds[1] ? settings.zoom : null;
  const bounds = useMemo<Interval>(() => zoom ?? (singleDay && rangeSessions.length ? [Math.max(rangeBounds[0], Math.floor(rangeSessions[0][0] / (30 * minute)) * 30 * minute), Math.min(rangeBounds[1], Math.ceil(rangeSessions.at(-1)![1] / (30 * minute)) * 30 * minute)] : rangeBounds), [zoom, singleDay, rangeBounds, rangeSessions]);
  const unitInfo = useMemo(() => {
    const info = new Map<string, ConcurrencyThread>();
    for (const thread of rangeThreads.slice().sort((a, b) => a.agent_depth - b.agent_depth || (a.turns[0]?.[0] ?? Infinity) - (b.turns[0]?.[0] ?? Infinity))) if (!info.has(concurrencyUnit(thread, counting))) info.set(concurrencyUnit(thread, counting), thread);
    return info;
  }, [rangeThreads, counting]);
  const order = useMemo(() => Array.from(unitInfo.keys()), [unitInfo]);
  const selectedId = counting === "agent" ? settings.agent : settings.workspace;
  const selected = unitInfo.has(selectedId) ? selectedId : null;
  useEffect(() => {
    if (data) selectionCallback.current?.({ settings: { ...settings, repository }, bounds, selected: selected ? unitInfo.get(selected) ?? null : null });
  }, [data, bounds[0], bounds[1], repository, counting, mode, gap, selected]);
  const navigation = useDrilldownHistory({ zoom, workspace: settings.workspace, agent: settings.agent }, JSON.stringify([scopeKey, rangeBounds, repository, counting, routeRevision]));
  const colors = useMemo(() => new Map(Array.from(unitInfo, ([id, thread]) => [id, palette[Math.max(0, repositories.indexOf(thread.repository)) % palette.length]])), [unitInfo, repositories]);
  const highlighted = selected ?? hover?.unit ?? null;
  const focusedThreads = rangeThreads.filter(thread => concurrencyUnit(thread, counting) === highlighted && thread.agent_depth === 0).sort((a, b) => Number(b.id === hover?.conversation) - Number(a.id === hover?.conversation));
  const scopedRows = rangeThreads.filter(thread => thread.agent_depth === 0 && (!selected || concurrencyUnit(thread, counting) === selected)).sort((a, b) => sumSpans(b.turns) - sumSpans(a.turns));
  const rowTimings = useMemo(() => conversationTimings(rangeThreads, gap), [rangeThreads, gap]);
  const modelRange = useMemo(() => concurrencySpans(rangeThreads, "model", gap, counting), [rangeThreads, gap, counting]);
  const userRange = useMemo(() => concurrencySpans(rangeThreads, "user", gap, counting), [rangeThreads, gap, counting]);
  const visibleMetrics = useMemo(() => {
    const averagingBounds = zoom ?? rangeBounds;
    return { model: concurrencyMetrics(modelRange, rangeSessions, averagingBounds), user: concurrencyMetrics(userRange, rangeSessions, averagingBounds) };
  }, [zoom, rangeBounds, rangeSessions, modelRange, userRange]);
  const activeAt = (at: number) => Array.from(mode === "user" ? userRange : modelRange).filter(([, spans]) => spans.some(([a, b]) => a <= at && at < b)).length;
  const chooseUnit = (unit: string) => update(counting === "agent" ? { agent: selected === unit ? "" : unit, showConversations: true } : { workspace: selected === unit ? "" : unit, showConversations: true });
  if (!concurrencyPrototypeEnabled) return <div className="machine-empty"><h3>Machine Parallelism</h3><p>No captured timing snapshot is available for this library.</p></div>;
  if (error) return <p role="alert">{error}</p>;
  if (!data) return <p className="muted" aria-busy="true">Reading captured timing…</p>;
  if (scope?.days && activityWindow && (Date.parse(activityWindow.start) >= zonedDayBounds(data.period_end, data.timezone)[1] || Date.parse(activityWindow.end) <= zonedDayBounds(data.period_start, data.timezone)[0])) return <p className="machine-empty">No captured timing overlaps this period. Snapshot coverage: {data.period_start} – {data.period_end}. Choose another period.</p>;
  const chooseRange = (value: string) => update(/^\d{4}-\d{2}-\d{2}$/.test(value) ? { range: "day", day: value, workspace: "", agent: "", zoom: null } : { range: value as ConcurrencyRange, startDay: range.startDay, endDay: range.endDay, workspace: "", agent: "", zoom: null });
  const clock = new Intl.DateTimeFormat(undefined, { timeZone: data.timezone, ...(!singleDay ? { month: "short", day: "numeric" } as const : {}), hour: "numeric", minute: "2-digit" });
  return <section className="concurrency-panel" aria-label="Parallelism chart">
    {scope || routeParameter ? <p className="machine-parallelism-note">Captured timing: {data.period_start} – {data.period_end} · {data.timezone}. {scope?.days ? "Showing the captured portion of the selected period; averages use only this portion. " : ""}Model activity is estimated from response timing; tool waits are excluded.</p> : null}
    <div className="concurrency-controls">
      <div className="concurrency-control"><div className="concurrency-control-label"><label htmlFor="parallelism-view">View</label><button type="button" className="concurrency-info-button" aria-label="About parallelism views" aria-haspopup="dialog" aria-controls="parallelism-calculation" title="How parallelism is calculated" onClick={() => setShowInfo(true)}><Icon name="info" /></button></div><select id="parallelism-view" aria-label="View" value={mode} onChange={event => update({ mode: event.target.value as ConcurrencyMode })}><option value="user">Threaded</option><option value="model">Model activity</option></select></div>
      <label>Count by<select aria-label="Count by" value={counting} onChange={event => update({ counting: event.target.value as ConcurrencyCounting, workspace: "", agent: "" })}><option value="workspace">Workspaces</option><option value="agent">Sessions</option></select></label>
      {!scope ? <div className={`usage-date-range${settings.range === "custom" ? " custom" : ""}`}><label className="concurrency-control usage-chart-picker">Range<select aria-label="Range" value={settings.range === "day" ? range.startDay : settings.range} title={`Presets end at the latest captured day, ${data.period_end}`} onChange={event => chooseRange(event.target.value)}>
        {concurrencyRangePresets.map(([key, , label]) => <option key={key} value={key}>{label}</option>)}
        <option value="custom">Custom dates…</option>
        <optgroup label="Individual days">{data.days.slice().sort().map(date => <option key={date}>{date}</option>)}</optgroup>
        <option value="all">All captured time</option>
      </select></label>
        {settings.range === "custom" ? <div className="usage-custom-dates">
          <label><span>From</span><input aria-label="From date" type="date" min={data.period_start} max={range.endDay} value={range.startDay} onChange={event => { if (event.target.value && event.currentTarget.validity.valid) update({ startDay: event.target.value, workspace: "", agent: "", zoom: null }); }} /></label>
          <label><span>Through</span><input aria-label="Through date" type="date" min={range.startDay} max={data.period_end} value={range.endDay} onChange={event => { if (event.target.value && event.currentTarget.validity.valid) update({ endDay: event.target.value, workspace: "", agent: "", zoom: null }); }} /></label>
          <span className="muted concurrency-range-note">Both dates included · {data.timezone}</span>
        </div> : null}
      </div> : null}
      <label>Turn gap<select aria-label="Turn gap" value={gap} disabled={mode === "model"} onChange={event => update({ gap: Number(event.target.value) })}>{[0, 15, 30, 60].map(value => <option key={value} value={value}>{value === 0 ? "No bridging" : `${value} minutes`}</option>)}</select></label>
      {!scope ? <label>Repository<select aria-label="Repository" value={repository} onChange={event => update({ repository: event.target.value, workspace: "", agent: "", zoom: null })}><option value="">All repositories</option>{repositories.map(repo => <option key={repo}>{repo}</option>)}</select></label> : null}
      <SmoothingControl value={settings.smoothingMinutes} unit="minutes" onChange={smoothingMinutes => update({ smoothingMinutes })} note="Average occupancy line" />
    </div>
    <ZoomNavigation canBack={navigation.canBack} zoomed={Boolean(zoom || selected)} onBack={() => navigation.back(snapshot => update(snapshot))} onReset={() => update({ zoom: null, workspace: "", agent: "" })}>
      <span className="machine-zoom-status" aria-live="polite">{zoom ? `${clock.format(bounds[0])} – ${clock.format(bounds[1])}` : "Full time range"}{selected ? " · Selected conversations" : ""}</span>
    </ZoomNavigation>
    {settings.smoothingMinutes ? <p className="usage-dashboard-note">The line shows a {settings.smoothingMinutes}-minute trailing average. Ribbons, hover counts, summary metrics, and linked records retain actual activity.</p> : null}
    <ConcurrencyChart key={counting} mode={mode} counting={counting} threads={rangeThreads} historyThreads={threads} historyStart={zonedDayBounds(data.period_start, data.timezone)[0]} gap={gap} order={order} bounds={bounds} ceiling={Math.max(2, visibleMetrics[mode].peak)} colors={colors} selected={highlighted} hover={hover} onHover={setHover} onSelect={chooseUnit} onZoom={value => update({ zoom: value })} timezone={data.timezone} smoothingMinutes={settings.smoothingMinutes}/>
    <div className="concurrency-plot-summary" role="group" aria-label="Parallelism summary"><Metrics value={visibleMetrics[mode]} daily={singleDay && !zoom} counting={counting} /><div className="concurrency-working-summary muted"><span>{duration(visibleMetrics[mode].working)} working time</span><span className="concurrency-view-note">{mode === "user" ? "Pale = gap between turns" : "Estimated timing; tool waits excluded"}</span></div></div>
    <div className="concurrency-hover-detail" aria-live="polite">{hover ? <><strong>{clock.format(hover.time)}</strong> · {activeAt(hover.time)} concurrent {counting === "agent" ? "sessions" : "workspaces"}{focusedThreads[0] ? <span> · {focusedThreads[0].repository} / {focusedThreads[0].workspace_name} · {focusedThreads[0].title}</span> : null}</> : selected && focusedThreads[0] ? <>{focusedThreads[0].repository} / {focusedThreads[0].workspace_name} · {focusedThreads.length} conversation{focusedThreads.length === 1 ? "" : "s"}</> : "Select a ribbon to follow its conversations. Drag across the chart to zoom."}</div>
    <div className="concurrency-chart-footer"><div className="concurrency-repositories">{repositories.filter(repo => rangeThreads.some(thread => thread.repository === repo)).map(repo => <span key={repo}><i style={{ background: palette[repositories.indexOf(repo) % palette.length] }}/>{repo}</span>)}</div></div>
    <details className="concurrency-conversations" open={settings.showConversations} onToggle={event => { if (event.currentTarget.open !== settings.showConversations) update({ showConversations: event.currentTarget.open }); }}><summary>{scopedRows.length} conversations{selected ? (counting === "agent" ? " for selected session" : ` in ${unitInfo.get(selected)?.workspace_name}`) : " in this range"}</summary>
      {selected ? <button type="button" className="concurrency-clear-selection" onClick={() => update({ workspace: "", agent: "" })}>Show all conversations</button> : null}
      {settings.showConversations ? scopedRows.length ? <table><thead><tr><th scope="col">Repository</th><th scope="col">Conversation</th><th scope="col">Model time</th><th scope="col">Thread time</th><th scope="col" className="concurrency-open-cell" aria-label="Open conversation"><span className="concurrency-open-text">Open conversation</span><span className="concurrency-open-icon" aria-hidden="true">↗</span></th></tr></thead><tbody>{scopedRows.map(thread => <tr key={thread.id} className={selected === concurrencyUnit(thread, counting) ? "selected" : ""}>
        <td><span className="concurrency-conversation-repo" title={thread.repository}><i style={{ background: colors.get(concurrencyUnit(thread, counting)) }} /><span>{thread.repository}</span></span></td>
        <td><button type="button" className="concurrency-thread-button" title={thread.title} onClick={() => chooseUnit(concurrencyUnit(thread, counting))}><span>{thread.title}</span></button></td>
        <td>{duration(rowTimings.get(thread.id)!.model)}</td><td>{duration(rowTimings.get(thread.id)!.threaded)}</td>
        <td className="concurrency-open-cell"><a href={`/work/${encodeURIComponent(thread.work_id)}?conversation=${encodeURIComponent(thread.conversation_id)}`} target="_blank" rel="noreferrer" aria-label={`Open conversation: ${thread.title}`} title="Open conversation"><span className="concurrency-open-text">Open conversation </span>↗</a></td>
      </tr>)}</tbody></table> : <p className="muted">No observed conversations in this selection.</p> : null}
    </details>
    {showInfo ? <ConcurrencyInfoDialog data={data} onClose={() => setShowInfo(false)} /> : null}
  </section>;
}
