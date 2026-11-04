import React, { useEffect, useMemo, useState } from "react";
import type { AggregationClause, AggregationResultEntry, FieldSchema, WhereTerm } from "@pythia-software/query-table-core";
import { MetricCard, defaultRenderers } from "@pythia-software/query-table-ui";
import { analysisQuery, normalizeAnalysis, type AnalysisState } from "./dashboard-analysis-model";
import { useAnalysisResult } from "./dashboard-analysis";
import { toolMetricPresentation } from "./tools-dashboard-model";
import { postMetrics, type MetricResult } from "./metric-request";
import { onDatasetRefresh } from "./dataset-refresh";
import { chartSeriesStyle } from "./chart-colors";
import { ChartLegend } from "./chart-legend";
import { conversationRowURL } from "./usage-row-model";
import { usageWorkspaceURL } from "./usage-workspace-model";
import { useViewState } from "./view-state";
import { archetypes, bandBreakdowns, sessionShapeAnalysisCatalog as catalog, type BandBreakdown } from "./session-shape-analysis-catalog";
import { archetypeMix, mixFirstClauses, mixOther, mixRepositoryClauses, mixShownRepositories, readPercentiles, binClauses, formatSeconds, binEdgeLabel, binLabel, binPosition, dayTotals, durationEdges, formatSignal, sessionSignalLabels, sessionSignals, signalParts, signalRows, signalTableStart,
  withoutDates, type SessionSignal, type SignalRow } from "./session-shape-model";
import "./session-shape.css";

// The Session Shape dashboard's panels around its engine chart: the band
// toggle with the median and p90, active time per session, turn cycles by
// provider, archetype mix by repository, the signal table, the sessions to
// read and the definitions (docs/session-shape.md). Each follows the
// dashboard's filters.

const dataset = "session_shape";
const seconds = formatSeconds;
const fieldFormat: Record<string, (value: number) => string> = {
  messages: value => value.toLocaleString(), tool_calls: value => value.toLocaleString(), active_seconds: seconds, autonomy: value => value.toLocaleString(undefined, { maximumFractionDigits: 1 }),
};

// Rows from the session_shape query table, again on Refresh.
function useRefresh() {
  const [refresh, setRefresh] = useState(0);
  useEffect(() => onDatasetRefresh(dataset, () => setRefresh(value => value + 1)), []);
  return refresh;
}
async function postRows(request: Record<string, unknown>, signal: AbortSignal): Promise<{ rows: Array<Record<string, unknown>>; total: number }> {
  const response = await fetch(`/api/query/${dataset}/rows-v2`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ version: 2, profile: "qt-sqlite-v1", ...request }), signal });
  const body = await response.json();
  if (!response.ok) throw new Error(body.error?.message ?? body.error ?? "Sessions could not be loaded.");
  return { rows: body.rows ?? [], total: Number(body.total) || 0 };
}
type Loaded<T> = { value: T | null; error: string; loading: boolean };
function useLoad<T>(key: string, load: (signal: AbortSignal) => Promise<T>): Loaded<T> {
  const refresh = useRefresh();
  const [state, setState] = useState<{ key: string; value: T | null; error: string; loading: boolean }>({ key: "", value: null, error: "", loading: true });
  useEffect(() => {
    const controller = new AbortController();
    setState(previous => ({ ...previous, loading: true }));
    load(controller.signal).then(value => setState({ key, value, error: "", loading: false }))
      .catch(failure => { if (!controller.signal.aborted) setState({ key, value: null, error: failure instanceof Error ? failure.message : String(failure), loading: false }); });
    return () => controller.abort();
  }, [key, refresh]);
  return state.key === key ? state : { value: null, error: "", loading: true };
}
function useSessionMetrics(where: WhereTerm[], schema: FieldSchema<any>, requests: AggregationClause[][]): Loaded<MetricResult[]> {
  return useLoad(JSON.stringify([where, requests]), async signal => (await Promise.all(requests.map(clauses => postMetrics(dataset, schema, where, clauses, signal)))).flat());
}

// The median and p90 of a field over the sessions that have it: the
// nearest-rank values, read as the rows at those ranks.
type Percentiles = { n: number; p50: number | null; p90: number | null };
function usePercentiles(where: WhereTerm[], field: string): Loaded<Percentiles> {
  return useLoad(JSON.stringify([where, field]), async signal => {
    const request = { select: [field], where: [...where, { field, op: "is_not_null", value: "" }], orderBy: [{ field, dir: "asc" }, { field: "id", dir: "asc" }] };
    const { n, values: [p50, p90] } = await readPercentiles(async offset => {
      const { rows, total } = await postRows({ ...request, limit: 1, offset }, signal);
      const value = rows[0]?.[field] == null ? NaN : Number(rows[0][field]);
      return { value: Number.isFinite(value) ? value : null, total };
    }, [0.5, 0.9]);
    return { n, p50, p90 };
  });
}
const sessionsText = (n: number) => `${n.toLocaleString()} ${n === 1 ? "session" : "sessions"}`;

// Above the engine chart: which band the chart's shares follow, and that
// band's measure in the range, median and p90.
export function SessionShapeBandSummary({ where, split, onSplit }: { where: WhereTerm[]; split: string; onSplit: (split: BandBreakdown) => void }) {
  const band = (split in bandBreakdowns ? split : "length_band") as BandBreakdown, { field, label } = bandBreakdowns[band];
  const result = usePercentiles(where, field), format = fieldFormat[field];
  const value = result.value;
  return <div className="session-shape-band-summary">
    <div className="combo-button session-shape-bands" role="group" aria-label="Band">
      {(Object.keys(bandBreakdowns) as BandBreakdown[]).map(id => <button key={id} type="button" aria-pressed={band === id && split === id} onClick={() => onSplit(id)}>{bandBreakdowns[id].label}</button>)}
    </div>
    <p className="muted" role="status" aria-busy={result.loading}>
      {result.error ? `${label} percentiles are unavailable: ${result.error}` : result.loading || !value ? `${label} per session: loading…`
        : !value.n ? `No sessions in this range have a ${label.toLowerCase()} value.`
        : value.p50 === null || value.p90 === null ? `${label} percentiles are unavailable; Refresh to try again.`
        : <>{label} per session: median <strong>{format(value.p50)}</strong>, p90 <strong>{format(value.p90)}</strong>, over {sessionsText(value.n)}.</>}
    </p>
  </div>;
}

const binColor = chartSeriesStyle("session_shape", "sessions").color;
// Active time per session as a histogram over log-spaced bins, with the
// median and p90 marked where they fall.
export function ActiveTimeDistribution({ where, schema, onOpen }: { where: WhereTerm[]; schema: FieldSchema<any>; onOpen: (state: AnalysisState) => void }) {
  const clauses = useMemo(() => binClauses("active_seconds", durationEdges), []);
  const bins = useSessionMetrics(where, schema, [clauses]), percentiles = usePercentiles(where, "active_seconds");
  const counts = durationEdges.map((_, index) => Number(bins.value?.find(result => result.id === `bin_${index}`)?.buckets[0]?.value) || 0);
  const highest = Math.max(1, ...counts), total = counts.reduce((sum, value) => sum + value, 0);
  // A rank without a value is left unmarked.
  const marks: Array<[string, number]> = [];
  if (percentiles.value?.n) for (const [label, value] of [["p50", percentiles.value.p50], ["p90", percentiles.value.p90]] as const) if (value !== null) marks.push([label, value]);
  const loading = bins.loading || percentiles.loading, error = bins.error || percentiles.error;
  return <section className="session-shape-panel" aria-busy={loading}>
    <header>
      <h3>Active time per session</h3>
      <p className="muted">Time between consecutive messages, counting gaps of five minutes or less; a longer gap counts as nothing. Bins widen as time grows. {total ? `${sessionsText(total)} with timestamps.` : ""}</p>
    </header>
    {error ? <p className="query-table-error" role="alert">{error}</p> : !loading && !total ? <p className="muted">No sessions with timestamps match these filters.</p> : <>
      <div className="session-shape-histogram" role="img" aria-label={`Sessions by active time${marks.map(([label, value]) => `; ${label === "p50" ? "median" : label} ${seconds(value)}`).join("")}`}>
        <div className="session-shape-bins">
          {counts.map((count, index) => <i key={index} className="qt-metric-mark" style={{ height: `${100 * count / highest}%`, background: binColor }} data-chart-tooltip={`${binLabel(durationEdges, index)}\n${sessionsText(count)}`} />)}
          {marks.map(([label, value]) => {
            const at = 100 * binPosition(durationEdges, value) / durationEdges.length;
            return <span key={label} className={`session-shape-mark${at > 50 ? " is-end" : ""}`} style={{ left: `${at}%` }}><b>{label}</b> {seconds(value)}</span>;
          })}
        </div>
        <div className="session-shape-bin-labels muted">{durationEdges.map((_, index) => <span key={index}>{binEdgeLabel(durationEdges, index)}</span>)}</div>
      </div>
    </>}
    <p className="session-shape-panel-actions"><button type="button" className="secondary" onClick={() => onOpen({ view: "distribution", unit: "records", metric: "active_time", split: "none" })}>Open in the chart above</button></p>
  </section>;
}

// A fixed engine view drawn as the engine draws it, beside the dashboard's
// own chart: a panel, not a second set of controls.
function FixedAnalysis({ state, where, schema }: { state: AnalysisState; where: WhereTerm[]; schema: FieldSchema<any> }) {
  const view = normalizeAnalysis(catalog, state), zone = Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  const analysis = useMemo(() => analysisQuery(catalog, view, where, { zone }), [JSON.stringify(view), JSON.stringify(where), zone]);
  const result = useAnalysisResult(dataset, schema, analysis.query);
  const entry = result.results[0];
  const presentation = useMemo(() => toolMetricPresentation(entry, analysis.clause.groupBy, catalog.labels), [entry, analysis.clause]);
  if (result.error) return <p className="query-table-error" role="alert">{result.error}</p>;
  if (!result.loading && entry && !entry.buckets.some(bucket => bucket.distribution?.kind === "box" ? bucket.distribution.summary : true)) return <p className="muted">No sessions with a turn cycle match these filters.</p>;
  return <MetricCard clause={analysis.clause} result={presentation.metric as AggregationResultEntry | undefined} fields={schema.fields} loading={result.loading} renderers={defaultRenderers} />;
}
const turnCycles: AnalysisState = { view: "distribution", unit: "records", metric: "turn_cycle", split: "provider" };
export function TurnCycleByProvider({ where, schema, onOpen }: { where: WhereTerm[]; schema: FieldSchema<any>; onOpen: (state: AnalysisState) => void }) {
  return <section className="session-shape-panel">
    <header>
      <h3>Turn cycle by provider</h3>
      <p className="muted">Each session's median gap from a message you typed to the agent's next message, leaving out gaps over 30 minutes: how fast the loop turns for you. It differs from the post-tool gap on <a href={usageWorkspaceURL({ mode: "dashboards", topic: "thinking-overview", report: "" }, new URLSearchParams())}>Waiting and Compaction</a>, which times the model's wait after each tool result, inside a turn, whoever started it.</p>
    </header>
    <FixedAnalysis state={turnCycles} where={where} schema={schema} />
    <p className="session-shape-panel-actions"><button type="button" className="secondary" onClick={() => onOpen(turnCycles)}>Open in the chart above</button></p>
  </section>;
}

// Each repository's sessions by archetype, as shares of its sessions.
const archetypeColor = (archetype: string) => chartSeriesStyle("archetype", archetype).color;
const archetypeLabel = (archetype: string) => (archetypes.values as Record<string, string>)[archetype] ?? (archetype || "Not enough evidence");
export function ArchetypeMix({ where, schema, onOpen }: { where: WhereTerm[]; schema: FieldSchema<any>; onOpen: (state: AnalysisState) => void }) {
  const result = useLoad(JSON.stringify(["mix", where]), async signal => {
    const [totals, repositories] = await postMetrics(dataset, schema, where, mixFirstClauses(), signal);
    for (const metric of [totals, repositories]) if (metric?.error) throw new Error(metric.error);
    const shown = mixShownRepositories(repositories);
    const each = shown.repositories.length ? await postMetrics(dataset, schema, where, mixRepositoryClauses(shown.repositories), signal) : [];
    for (const metric of each) if (metric.error) throw new Error(metric.error);
    return archetypeMix(totals.buckets, shown.repositories, each.map(metric => metric.buckets), shown.folded);
  });
  const rows = result.value ?? [];
  // The known archetypes in their order, then any value the data adds.
  const present = [...new Set([...archetypes.order, ...rows.flatMap(row => Object.keys(row.counts)).sort(), ""])].filter(archetype => rows.some(row => row.counts[archetype]));
  return <section className="session-shape-panel" aria-busy={result.loading}>
    <header>
      <h3>Archetype mix by repository</h3>
      <p className="muted">Each repository's sessions by how many messages you typed: Automated (none), Quick (up to 5), Standard (up to 15), Deep (up to 50) and Marathon. Shares of each repository's own sessions.</p>
    </header>
    {result.error ? <p className="query-table-error" role="alert">{result.error}</p> : !result.loading && !rows.length ? <p className="muted">No sessions match these filters.</p> : <>
      <ChartLegend items={present.map(archetype => ({ key: archetype || "none", label: archetypeLabel(archetype), color: archetypeColor(archetype) }))} />
      <ul className="session-shape-mix">
        {rows.map(row => <li key={row.key}>
          <span className="session-shape-mix-label">{row.key === mixOther ? "Other repositories" : row.key || "No repository"}</span>
          <span className="session-shape-mix-bar">{present.filter(archetype => row.counts[archetype]).map(archetype => <i key={archetype} className="qt-metric-mark" style={{ flexGrow: row.counts[archetype], background: archetypeColor(archetype) }}
            data-chart-tooltip={`${archetypeLabel(archetype)}: ${(100 * row.counts[archetype] / row.total).toFixed(0)}%\n${sessionsText(row.counts[archetype])} of ${row.total.toLocaleString()}`} />)}</span>
          <span className="session-shape-mix-n muted">{sessionsText(row.total)}</span>
        </li>)}
      </ul>
    </>}
    <p className="session-shape-panel-actions"><button type="button" className="secondary" onClick={() => onOpen({ view: "compare", unit: "records", metric: "sessions", split: "archetype", compareAs: "share" })}>Open in the chart above</button></p>
  </section>;
}

// The signal table: the last seven days against your eight weeks before
// them, under the dashboard's filters but not its dates.
const localDay = (time: number) => new Intl.DateTimeFormat("en-CA", { year: "numeric", month: "2-digit", day: "2-digit" }).format(time);
const verdictText: Record<SignalRow["verdict"], string> = { within: "No detectable change", above: "Above your range", below: "Below your range", "no-history": "Not enough history", "no-evidence": "Not enough evidence" };
export function SignalTable({ where, schema, end = localDay(Date.now()) }: { where: WhereTerm[]; schema: FieldSchema<any>; end?: string }) {
  const population = useMemo(() => [...withoutDates(where), { field: "day", op: ">=", value: signalTableStart(end) }, { field: "day", op: "<=", value: end }] as WhereTerm[], [JSON.stringify(where), end]);
  const requests = useMemo(() => {
    const clauses = signalParts.map((part): AggregationClause => ({ id: part.id, op: "sum", expression: part.expression, groupBy: ["day"], groupLimit: 100 }));
    return [clauses.slice(0, 11), clauses.slice(11)];
  }, []);
  const result = useSessionMetrics(population, schema, requests);
  const rows = result.value ? signalRows(dayTotals(result.value), end) : [];
  return <section className="session-shape-panel" aria-busy={result.loading}>
    <header>
      <h3>Signals against your own range</h3>
      <p className="muted">The seven days to {new Date(`${end}T12:00:00`).toLocaleDateString(undefined, { month: "short", day: "numeric" })} against the lowest and highest of the eight weeks before them, under the dashboard's filters but not its dates. A value inside that range is no detectable change. Each rate counts only sessions with the evidence it needs.</p>
    </header>
    {result.error ? <p className="query-table-error" role="alert">{result.error}</p> : <div className="session-shape-table-scroll"><table className="session-shape-signals">
      <thead><tr><th scope="col">Signal</th><th scope="col">Last 7 days</th><th scope="col">Your range, 8 weeks before</th><th scope="col">Change</th><th scope="col">Sessions with evidence</th></tr></thead>
      <tbody>{rows.map(row => <tr key={row.definition.id} className={row.verdict === "above" || row.verdict === "below" ? "is-outside" : undefined}>
        <th scope="row" title={row.definition.note}>{row.definition.label}</th>
        <td>{formatSignal(row.definition, row.value)}{row.value !== null ? <small className="muted"> {row.numerator.toLocaleString()} of {row.denominator.toLocaleString()}</small> : null}</td>
        <td>{row.low === null ? "—" : `${formatSignal(row.definition, row.low)} – ${formatSignal(row.definition, row.high)}`}<small className="muted"> {row.history} of 8 weeks</small></td>
        <td>{verdictText[row.verdict]}</td>
        <td>{row.sessions.toLocaleString()}</td>
      </tr>)}</tbody>
    </table></div>}
  </section>;
}

// Sessions to read: the most forensic events first, or the most of one
// signal, each linking to its conversation in the reader.
const sessionFields = ["id", "conversation_id", "workspace_id", "title", "started_at", "repository_name", "provider", "archetype", "messages", "active_seconds", "forensic_events", ...sessionSignals];
const when = (value: unknown) => { const at = new Date(String(value)); return Number.isFinite(at.getTime()) ? at.toLocaleString(undefined, { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" }) : ""; };
export function SessionsToRead({ where }: { where: WhereTerm[] }) {
  const [signal, setSignal] = useViewState<"all" | SessionSignal>("session_signal", "all");
  const [shown, setShown] = useState(20);
  const field = signal === "all" ? "forensic_events" : signal;
  const result = useLoad(JSON.stringify([where, field, shown]), signal => postRows({ select: sessionFields, where: [...where, { field, op: ">", value: "0" }],
    orderBy: [{ field, dir: "desc" }, { field: "started_at", dir: "desc" }, { field: "id", dir: "asc" }], limit: shown, offset: 0 }, signal));
  const rows = result.value?.rows ?? [], total = result.value?.total ?? 0;
  return <section className="session-shape-panel" aria-busy={result.loading}>
    <header>
      <h3>Sessions to read</h3>
      <p className="muted">Sessions where a signal fired, most first. Open one in the reader; its Session signals strip marks where each fired.</p>
      <label className="session-shape-signal-select">Signal <select value={signal} data-view-field="session_signal" onChange={event => setSignal(event.target.value as "all" | SessionSignal)}>
        <option value="all">All forensic events</option>
        {sessionSignals.map(id => <option key={id} value={id}>{sessionSignalLabels[id]}</option>)}
      </select></label>
    </header>
    {result.error ? <p className="query-table-error" role="alert">{result.error}</p> : !result.loading && !rows.length ? <p className="muted">No sessions in this range have {signal === "all" ? "a forensic event" : sessionSignalLabels[signal].toLowerCase()}.</p>
      : <div className="session-shape-table-scroll"><table className="session-shape-sessions">
        <thead><tr><th scope="col">Session</th><th scope="col">Started</th><th scope="col">Repository</th><th scope="col">{signal === "all" ? "Forensic events" : sessionSignalLabels[signal]}</th><th scope="col">Messages</th><th scope="col">Active time</th></tr></thead>
        <tbody>{rows.map(row => {
          const href = conversationRowURL(row);
          return <tr key={String(row.id)} data-pharos-target={`row:${String(row.id)}`}>
            <th scope="row">{href ? <a href={href}>{String(row.title || "Untitled session")}</a> : String(row.title || "Untitled session")}</th>
            <td>{when(row.started_at)}</td>
            <td>{String(row.repository_name ?? "")}</td>
            <td>{Number(row[field]).toLocaleString()}</td>
            <td>{Number(row.messages).toLocaleString()}</td>
            <td>{row.active_seconds == null ? "—" : seconds(Number(row.active_seconds))}</td>
          </tr>;
        })}</tbody>
      </table></div>}
    {rows.length < total ? <p className="session-shape-panel-actions"><button type="button" className="secondary" disabled={result.loading} onClick={() => setShown(value => value + 20)}>Show more</button> <span className="muted">{rows.length.toLocaleString()} of {total.toLocaleString()}</span></p> : null}
  </section>;
}

// Every signal's rule, one click from the dashboard's note.
export const definitionsSection = "session-shape-definitions";
export function revealDefinitions() {
  const url = new URL(location.href);
  url.searchParams.set("reveal", `section:${definitionsSection}`);
  history.replaceState(history.state, "", url);
  window.dispatchEvent(new Event("pharos:uri-changed"));
}
const definitionMeasures = ["failed_calls", "rejections", "interrupts", "hook_blocks", "identical_retries", "thrash_retries", "one_shot_rate", "churned_files", "mid_task_compactions", "context_peak", "context_pressure",
  "corrections", "steering_load", "verification", "long_unprompted_runs", "ended_on_failure", "autonomy", "turn_cycle", "active_time"];
export function SessionShapeDefinitions() {
  return <section className="session-shape-panel session-shape-definitions" id={definitionsSection}>
    <header><h3>Definitions</h3>
      <p className="muted">Every value is counted from what Pharos already indexes; there is no score. A signal is empty, not zero, when a session lacks the evidence it needs: tool data, timestamps, your classified prompts or a known context window. See docs/session-shape.md for each rule's origin and limits.</p></header>
    <dl>{definitionMeasures.map(id => <React.Fragment key={id}><dt>{catalog.measures[id].label}</dt><dd>{catalog.measures[id].note}</dd></React.Fragment>)}</dl>
  </section>;
}
