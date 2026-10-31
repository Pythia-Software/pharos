import { DashboardSelect } from "./dashboard-select";
import { ViewDisclosure } from "./view-disclosure";
import React, { useEffect, useMemo, useRef, useState } from "react";
import { loadSchema, type WhereTerm } from "@pythia-software/query-table-core";
import type { QueryTableApi } from "@pythia-software/query-table-react";
import { machineFilterKey, useDrilldownHistory, ZoomNavigation } from "./machine-navigation";
import toolCallsDocument from "../../schemas/tool_calls.schema.json";
import { MetricRequestError, postMetrics, postQueryV3Metrics } from "./metric-request";
import { MachineAnalyticsTable, type MachineGrouping } from "./machine-analytics-table";
import { activityPresentation, groupTime, machineDrilldownWhere, selectedMachineRepository, machineDay, shiftMachineDay, machineWhere, previousTimeWhere, relativeChange, timeAggregationWhere, timeAggregations, timeFacts, timeLabel, timeValue, totalTime, type MachineCall, type MachineScope, type MachineWindow, type DayTime, type MachineTime, type TimeMetric } from "./machine-activity-data";

type Row = Record<string, any>;
export type MachineCallsSurface = (props: { scopeWhere: WhereTerm[]; appliedScope: React.MutableRefObject<WhereTerm[]>; header: (api: QueryTableApi<Row>) => React.ReactNode }) => React.ReactNode;
const callSchema = loadSchema<MachineCall>(toolCallsDocument);
const percent = (value: number, total: number) => total ? `${Math.round(value / total * 100)}%` : "—";
const emptyTime: MachineTime = { facts: [], days: [] };

function useTimeFacts(where: WhereTerm[], scope: MachineScope, refresh: number) {
  const priorWhere = scope.days ? previousTimeWhere(where, scope.days) : null;
  const key = JSON.stringify([where, scope.days, refresh]);
  const [state, setState] = useState<{ key: string; current: MachineTime; previous: MachineTime; error: string }>({ key: "", current: emptyTime, previous: emptyTime, error: "" });
  useEffect(() => {
    const controller = new AbortController();
    const period = scope.days ? "day" : "month";
    async function load(filters: WhereTerm[]): Promise<MachineTime> {
      const where = timeAggregationWhere(filters), clauses = timeAggregations(period);
      try {
        return timeFacts(await postMetrics("tool_calls", callSchema, where, clauses, controller.signal));
      } catch (error) {
        // Filters beyond the cube's fields, such as a command, read raw calls
        // within /metrics's budgets. /query-v3 can stage many more.
        if (!(error instanceof MetricRequestError && error.resourceLimit)) throw error;
        return timeFacts(await postQueryV3Metrics("tool_calls", callSchema, where, clauses, controller.signal));
      }
    }
    Promise.all([load(where), priorWhere ? load(priorWhere) : Promise.resolve(emptyTime)])
      .then(([current, previous]) => { if (!controller.signal.aborted) setState({ key, current, previous, error: "" }); })
      .catch(error => { if (!controller.signal.aborted) setState({ key, current: emptyTime, previous: emptyTime, error: error.message }); });
    return () => controller.abort();
  }, [key]);
  return { ...state, loading: state.key !== key, current: state.key === key ? state.current : emptyTime, previous: state.key === key ? state.previous : emptyTime, compared: priorWhere !== null };
}

function TimeTrend({ current, previous, days, window, metric, compared, onDay }: { current: DayTime[]; previous: DayTime[]; days: number; window: MachineWindow; metric: TimeMetric; compared: boolean; onDay: (day: string) => void }) {
  const host = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(480);
  useEffect(() => {
    const observer = new ResizeObserver(entries => setWidth(Math.max(240, entries[0].contentRect.width)));
    if (host.current) observer.observe(host.current);
    return () => observer.disconnect();
  }, []);
  const currentDays = useMemo(() => new Map(groupTime(current, fact => fact.day).map(group => [group.key, group])), [current]);
  const previousDays = useMemo(() => new Map(groupTime(previous, fact => fact.day).map(group => [group.key, group])), [previous]);
  const dates = days ? Array.from({ length: days }, (_, index) => shiftMachineDay(machineDay(window.start), index)) : [...currentDays.keys()].sort();
  const values = dates.map(day => timeValue(currentDays.get(day) ?? totalTime([]), metric));
  const baseline = days && compared ? dates.map(day => timeValue(previousDays.get(shiftMachineDay(day, -days)) ?? totalTime([]), metric)) : [];
  const ceiling = Math.max(metric === "calls" ? 1 : 1000, ...values.filter((value): value is number => value !== null), ...baseline.filter((value): value is number => value !== null)) * 1.15;
  const x = (index: number) => 48 + index / Math.max(1, dates.length - 1) * (width - 62);
  const y = (value: number) => 140 - value / ceiling * 120;
  const label = (value: number) => metric === "calls" ? Math.round(value).toLocaleString() : timeLabel(value);
  const path = (numbers: Array<number | null>) => {
    let connected = false;
    return numbers.map((value, index) => { if (value === null) { connected = false; return ""; } const point = `${connected ? "L" : "M"}${x(index)},${y(value)}`; connected = true; return point; }).join(" ");
  };
  const ticks = [...new Set([0, Math.floor((dates.length - 1) / 2), dates.length - 1])].filter(index => index >= 0);
  const title = metric === "average" ? "Time per timed run" : metric === "calls" ? "Call count" : "Total tool time";
  return <div ref={host} className="machine-trend">
    <svg width="100%" height="172" viewBox={`0 0 ${width} 172`} role="img" aria-label={`${days ? "Daily" : "Monthly"} ${title.toLowerCase()}${compared ? ", compared with the previous period" : ""}`}>
      {[0, .5, 1].map(fraction => <g key={fraction}><line x1="48" x2={width - 14} y1={y(ceiling * fraction)} y2={y(ceiling * fraction)} className="machine-trend-grid" /><text x="39" y={y(ceiling * fraction) + 4} textAnchor="end">{label(ceiling * fraction)}</text></g>)}
      {baseline.length ? <path d={path(baseline)} className="machine-trend-previous" /> : null}
      <path d={path(values)} className="machine-trend-current" />
      {values.map((value, index) => value === null || !currentDays.has(dates[index]) ? null : <circle key={index} cx={x(index)} cy={y(value)} r="4" className="machine-trend-point" role="button" tabIndex={0} aria-label={`Inspect calls on ${dates[index]}: ${label(value)}${metric === "average" ? " per timed run" : ""}`} onClick={() => onDay(dates[index])} onKeyDown={event => { if (event.key === "Enter" || event.key === " ") { event.preventDefault(); onDay(dates[index]); } }}>
        <title>{dates[index]}: {label(value)}{metric === "average" ? " / timed run" : ""} · {currentDays.get(dates[index])!.calls} calls ({currentDays.get(dates[index])!.timed} timed) · Total: {timeLabel(currentDays.get(dates[index])!.duration)}{compared ? ` · Previous: ${baseline[index] === null ? "No timing" : label(baseline[index])}` : ""}</title>
      </circle>)}
      {ticks.map(index => <text key={index} x={x(index)} y="164" textAnchor={index === 0 ? "start" : index === dates.length - 1 ? "end" : "middle"}>{days ? dates[index].slice(5) : dates[index]}</text>)}
    </svg>
    <div className="machine-trend-legend"><span><i />This period</span>{compared ? <span><i className="previous" />Previous period</span> : null}<span>{metric === "average" ? "Total duration ÷ timed calls" : metric === "calls" ? "All recorded calls" : "Sum of recorded durations"}</span></div>
  </div>;
}

function MachineTimeDashboard({ api, scope, window, onRepository }: { api: QueryTableApi<Row>; scope: MachineScope; window: MachineWindow; onRepository: (repository: string) => void }) {
  const where = (api.query.grouping?.source ?? api.query).where;
  const baseline = machineWhere(scope, window);
  // Native query restoration is asynchronous. Its empty undo history distinguishes
  // loading a saved view from a person changing the current exploration.
  const navigation = useDrilldownHistory(where, JSON.stringify(scope), filters => JSON.stringify(filters.map(machineFilterKey).sort()), api.canUndo);
  const extraFilters = where.filter(term => !baseline.some(base => machineFilterKey(base) === machineFilterKey(term)));
  const zoomed = extraFilters.length > 0;
  function restore(filters: WhereTerm[]) {
    api.setQuery(query => query.grouping
      ? { ...query, offset: 0, grouping: { ...query.grouping, source: { ...query.grouping.source, where: filters, offset: 0 } } }
      : { ...query, where: filters, offset: 0 });
  }
  const [refresh, setRefresh] = useState(0);
  useEffect(() => { const changed = () => setRefresh(value => value + 1); windowGlobal.addEventListener("pharos:machine-refresh", changed); return () => windowGlobal.removeEventListener("pharos:machine-refresh", changed); }, []);
  const result = useTimeFacts(where, scope, refresh);
  const [measure, setMeasure] = useState<"hours" | "share">("hours");
  const [metric, setMetric] = useState<TimeMetric>("average");
  const [grouping, setGrouping] = useState<MachineGrouping>("operation");
  const current = result.current.facts, previous = result.previous.facts;
  const total = useMemo(() => totalTime(current), [current]), previousTotal = useMemo(() => totalTime(previous), [previous]);
  const repositories = useMemo(() => groupTime(current, fact => fact.repository), [current]);
  const kinds = useMemo(() => [...new Set([...current, ...previous].map(fact => fact.activity))].map(activityPresentation), [current, previous]);
  const operations = useMemo(() => groupTime(current, fact => fact.operation), [current]);
  const selected = (field: string) => where.find(term => !("any" in term) && term.field === field && term.op === "=") as { value: string } | undefined;
  const operation = selected("operation_key")?.value ?? "";
  const activity = selected("activity")?.value ?? "";
  const repository = selectedMachineRepository(where, scope.repository);
  const operationName = current.find(fact => fact.operation === operation)?.name ?? previous.find(fact => fact.operation === operation)?.name;
  const selectionKey = JSON.stringify(where);
  const max = Math.max(1, ...repositories.map(repository => repository.duration));
  function filter(terms: WhereTerm[], scroll = false, toggle = false) {
    api.setQuery(query => {
      const source = query.grouping?.source ?? query;
      const next = { ...source, where: machineDrilldownWhere(source.where, terms, toggle), offset: 0 };
      return query.grouping ? { ...query, offset: 0, grouping: { ...query.grouping, source: next } } : next;
    });
    if (scroll) requestAnimationFrame(() => document.getElementById("machineRawCalls")?.scrollIntoView({ behavior: "smooth", block: "start" }));
  }
  function clear() { restore(baseline); }
  function onDay(day: string) {
    const start = new Date(`${day.length === 7 ? `${day}-01` : day}T00:00:00`), end = new Date(start);
    if (day.length === 7) end.setMonth(end.getMonth() + 1); else end.setDate(end.getDate() + 1);
    filter([{ field: "started_at", op: ">=", value: start.toISOString() }, { field: "started_at", op: "<", value: end.toISOString() }], true);
  }
  return <>
    <ZoomNavigation canBack={navigation.canBack} zoomed={zoomed} onBack={() => navigation.back(restore)} onReset={clear}>
      <div className="machine-breadcrumb"><button type="button" onClick={clear}>All activity</button>{repository ? <><span>/</span><strong>{repository}</strong></> : null}{activity ? <><span>/</span><strong>{activityPresentation(activity).label}</strong></> : null}{operation ? <><span>/</span><strong>{operationName || "Selected command family"}</strong></> : null}</div>
      <span className="machine-zoom-status" aria-live="polite">{zoomed ? `${extraFilters.length} active call filter${extraFilters.length === 1 ? "" : "s"}` : "Full view"}</span>
    </ZoomNavigation>
    <div className="machine-time-dashboard" aria-busy={result.loading}>
    <div className="machine-section-heading"><div><h2>Where machine time goes</h2><p className="muted">Compare total work with time per run. Every selection filters the calls below.</p></div><button type="button" className="machine-text-button" onClick={() => api.refresh()}>Refresh</button></div>
    {result.error && !result.loading ? <div className="machine-error" role="alert"><strong>Machine time could not be loaded.</strong><p>{result.error}</p><button type="button" onClick={() => api.refresh()}>Try again</button></div> : null}
    {result.loading ? <p className="machine-loading" role="status">Reading recorded machine activity…</p> : !result.error ? <>
      <div className="machine-stats">
        <div><span>Total recorded tool time</span><strong>{timeLabel(total.duration)}</strong><small>{result.compared ? `${relativeChange(total.duration, previousTotal.duration)} vs previous period` : "Sum of timed tool calls"}</small></div>
        <div><span>Call volume</span><strong>{total.calls.toLocaleString()}</strong><small>{result.compared ? `${relativeChange(total.calls, previousTotal.calls)} calls vs previous period` : "All calls, including missing timing"}</small></div>
        <div><span>Time per timed run</span><strong>{total.timed ? timeLabel(total.duration / total.timed) : "—"}</strong><small>{result.compared && previousTotal.timed && total.timed ? `${relativeChange(total.duration / total.timed, previousTotal.duration / previousTotal.timed)} per run · ${total.timed.toLocaleString()} timed` : `${total.timed.toLocaleString()} calls with timing`}</small></div>
        <div><span>Timing coverage</span><strong>{percent(total.timed, total.calls)}</strong><small>{(total.calls - total.timed).toLocaleString()} calls without a duration</small></div>
      </div>
      <div className="machine-investigation-heading"><div className="machine-inline-actions">{repository && !scope.repository ? <button type="button" className="machine-text-button" onClick={() => onRepository(repository)}>Use repository in all views ↗</button> : null}<button type="button" className="machine-text-button" onClick={() => document.getElementById("machineRawCalls")?.scrollIntoView({ behavior: "smooth" })}>Inspect {total.calls.toLocaleString()} raw calls ↓</button></div></div>
      {!current.length ? <p className="machine-empty">No recorded calls match the current filters. Adjust the source query below or return to All activity.</p> : <>
        <div className="machine-overview">
          <section className="machine-panel" aria-label="Time by repository"><div className="machine-panel-heading"><div><h3>Total time by repository</h3><p>Segments show activities. Select one to filter the source calls.</p></div><div className="machine-measure" role="group" aria-label="Repository chart measure"><button type="button" aria-pressed={measure === "hours"} onClick={() => setMeasure("hours")}>Hours</button><button type="button" aria-pressed={measure === "share"} onClick={() => setMeasure("share")}>Share</button></div></div>
            <div className="machine-repository-chart">{repositories.map(group => <div key={group.key} className="machine-repository-row"><div className="machine-repository-heading"><button type="button" aria-pressed={repository === group.key} onClick={() => filter(machineWhere({ days: 0, repository: group.key, provider: "", model: "" }, window), false, true)}>{group.key}</button><span>{timeLabel(group.duration)}<small>{group.calls.toLocaleString()} calls</small></span></div><div className="machine-bar-track"><div className="machine-stacked-bar" style={{ width: `${measure === "share" ? 100 : group.duration / max * 100}%` }} role="group" aria-label={`${group.key} activity breakdown`}>{kinds.map(kind => {
              const duration = totalTime(group.facts.filter(fact => fact.activity === kind.id)).duration;
              return duration > 0 ? <button type="button" key={kind.id} style={{ width: `${duration / group.duration * 100}%`, background: kind.color }} aria-label={`${group.key} · ${kind.label}: ${timeLabel(duration)}, ${percent(duration, group.duration)}`} title={`${kind.label} · ${timeLabel(duration)}`} onClick={() => filter([...machineWhere({ days: 0, repository: group.key, provider: "", model: "" }, window), { field: "activity", op: "=", value: kind.id }])} /> : null;
            })}</div></div></div>)}</div>
            <div className="machine-activity-legend">{kinds.map(kind => <button type="button" key={kind.id} aria-pressed={activity === kind.id} onClick={() => filter([{ field: "activity", op: "=", value: kind.id }], false, true)}><i style={{ background: kind.color }} />{kind.label}</button>)}</div>
          </section>
          <section className="machine-panel"><div className="machine-panel-heading"><div><h3>{metric === "average" ? "Time per timed run" : metric === "calls" ? "Call volume" : "Total tool time"}</h3><p>{operationName || (activity ? activityPresentation(activity).label : "All command families")}{scope.days ? ` · ${scope.days} days` : " · monthly buckets"}</p></div><label className="machine-chart-select">Measure<DashboardSelect aria-label="Trend measure" value={metric} onChange={event => setMetric(event.target.value as TimeMetric)}><option value="average">Time per timed run</option><option value="total">Total time</option><option value="calls">Call count</option></DashboardSelect></label></div>
            <label className="machine-trend-family">Command family<DashboardSelect aria-label="Trend command family" value={operation} onChange={event => event.target.value ? filter([{ field: "operation_key", op: "=", value: event.target.value }]) : restore(where.filter(term => "any" in term || term.field !== "operation_key"))}><option value="">All matching families</option>{operations.map(group => <option key={group.key} value={group.key}>{group.facts[0].name}</option>)}</DashboardSelect></label>
            <TimeTrend current={result.current.days} previous={result.previous.days} days={scope.days} window={window} metric={metric} compared={result.compared} onDay={onDay} />
            <p className="machine-chart-note">{metric === "average" && !operation ? "Select a command family for a like-for-like comparison; an overall mean changes with the mix of work. " : ""}{scope.days && !result.compared ? "This date filter cannot be shifted safely for comparison. " : ""}Select a point to inspect that date’s calls.</p>
          </section>
        </div>
        <div className="machine-panel-heading machine-analytics-heading"><div><h3>Tool & command analytics</h3><p>Compare named tools and parsed command families. Inspect a group to filter its underlying calls.</p></div><label className="machine-chart-select">Group by<DashboardSelect aria-label="Analytics grouping" value={grouping} onChange={event => setGrouping(event.target.value as MachineGrouping)}><option value="operation">Command family / tool</option><option value="tool">Specific tool</option><option value="activity">Activity</option></DashboardSelect></label></div>
        <MachineAnalyticsTable current={current} previous={previous} grouping={grouping} compared={result.compared} filter={filter} selectionKey={selectionKey} />
      </>}
      <ViewDisclosure viewKey="machine:methodology" className="machine-methodology" summary="How identities and time are measured"><p>Command families use the parsed program and named subcommand, such as npm run test:unit. Environment wrappers, paths to executables, and arguments are kept in the full invocation below. Different scripts, subcommands, named tools, and MCP servers retain separate identities. Activity categories help navigation; unclassified tools remain individually named.</p><p>Total time sums recorded tool durations, including overlapping calls. Time per timed run divides that sum by the number of calls with recorded timing, including recorded zeroes; calls without durations are excluded from the mean. An empty day has no per-run measurement. A family can still contain different flags or targets: inspect its exact invocations before attributing a change to performance. A run means one recorded tool call, not a completed user task.</p><p>Compound commands keep one primary family and their full source invocation. Their duration is counted once; it is not assigned to each subcommand. Timestamp timing may include approval waits. Comparisons shift the same call filters to the preceding complete local calendar days. Model inference and preflight phases are not yet attributed.</p></ViewDisclosure>
    </> : null}
  </div></>;
}
const windowGlobal = globalThis.window;

export function MachineTime({ scope, window, onRepository, calls }: { scope: MachineScope; window: MachineWindow; onRepository: (repository: string) => void; calls: MachineCallsSurface }) {
  const appliedScope = useRef<WhereTerm[]>([]);
  const where = useMemo(() => machineWhere(scope, window), [scope, window]);
  const header = (api: QueryTableApi<Row>) => <MachineTimeDashboard api={api} scope={scope} window={window} onRepository={onRepository} />;
  return <section className="machine-time" aria-label="Machine Time">{calls({ scopeWhere: where, appliedScope, header })}</section>;
}
