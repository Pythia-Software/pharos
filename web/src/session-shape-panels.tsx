import React, { useEffect, useMemo, useState } from "react";
import type { AggregationClause, FieldSchema, QueryRequestV3, WhereTerm } from "@pythia-software/query-table-core";
import { postMetrics, type MetricResult } from "./metric-request";
import { postQueryRows } from "./query-transport";
import { useDatasetRefresh } from "./use-dataset-refresh";
import { loadFollowing, type ServedReadiness } from "./result-cache";
import { conversationRowURL } from "./usage-row-model";
import { useViewState } from "./view-state";
import { sessionShapeAnalysisCatalog as catalog } from "./session-shape-analysis-catalog";
import { formatSeconds, dayTotals, formatSignal, sessionSignalLabels, sessionSignals, signalParts, signalRows, signalTablePopulation, type SessionSignal, type SignalRow } from "./session-shape-model";
import "./session-shape.css";

// The Session Shape dashboard's panels below its engine chart, for what the
// chart cannot draw: the signal table, the sessions to read and the
// definitions (docs/session-shape.md). Each follows the dashboard's filters.

const dataset = "session_shape";
const seconds = formatSeconds;

// Rows from the session_shape query table, again on Refresh.
async function postRows(result: QueryRequestV3["result"], signal: AbortSignal): Promise<{ rows: Array<Record<string, unknown>>; total: number }> {
  return postQueryRows<Record<string, unknown>>(dataset, result, signal);
}
type Loaded<T> = { value: T | null; error: string; loading: boolean; readiness?: ServedReadiness };
// stored: whether the load may be answered from the result cache. The
// aggregations are; the list of sessions is read live, as tables are.
function useLoad<T>(key: string, load: (signal: AbortSignal) => Promise<T>, stored = false): Loaded<T> {
  const [refresh, takePolicy] = useDatasetRefresh(dataset);
  const [state, setState] = useState<{ key: string; value: T | null; error: string; loading: boolean; readiness?: ServedReadiness }>({ key: "", value: null, error: "", loading: true });
  useEffect(() => {
    const controller = new AbortController();
    setState(previous => ({ ...previous, loading: true }));
    const publish = (value: T, readiness?: ServedReadiness) => setState({ key, value, error: "", loading: false, readiness }), policy = takePolicy();
    (stored ? loadFollowing(load, publish, controller.signal, policy) : load(controller.signal).then(value => publish(value)))
      .catch(failure => { if (!controller.signal.aborted) setState({ key, value: null, error: failure instanceof Error ? failure.message : String(failure), loading: false }); });
    return () => controller.abort();
  }, [key, refresh]);
  return state.key === key ? state : { value: null, error: "", loading: true };
}
function useSessionMetrics(where: WhereTerm[], schema: FieldSchema<any>, requests: AggregationClause[][]): Loaded<MetricResult[]> {
  return useLoad(JSON.stringify([where, requests]), async signal => (await Promise.all(requests.map(clauses => postMetrics(dataset, schema, where, clauses, signal)))).flat(), true);
}

// The signal table: the last seven days against your eight weeks before
// them, under the dashboard's filters but not its dates.
const localDay = (time: number) => new Intl.DateTimeFormat("en-CA", { year: "numeric", month: "2-digit", day: "2-digit" }).format(time);
const verdictText: Record<SignalRow["verdict"], string> = { within: "No detectable change", above: "Above your range", below: "Below your range", "no-history": "Not enough history", "no-evidence": "Not enough evidence" };
export function SignalTable({ where, schema, end = localDay(Date.now()) }: { where: WhereTerm[]; schema: FieldSchema<any>; end?: string }) {
  const population = useMemo(() => signalTablePopulation(where, end), [JSON.stringify(where), end]);
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
      {result.readiness?.refreshing || result.readiness?.refreshError ? <p className="muted" role="status">Showing results from {new Date(result.readiness.updatedAt).toLocaleString(undefined, { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" })}. {result.readiness.refreshing ? "Pharos is updating them." : "Refresh to check for newer results."}</p> : null}
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
      <p className="muted">Sessions where a signal fired, most first. Open one in the reader.</p>
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
  "corrections", "steering_load", "verification", "long_unprompted_runs", "ended_on_failure", "autonomy", "calls_per_words", "turn_cycle", "active_time"];
export function SessionShapeDefinitions() {
  return <section className="session-shape-panel session-shape-definitions" id={definitionsSection}>
    <header><h3>Definitions</h3>
      <p className="muted">Every value is counted from what Pharos already indexes; there is no score. A signal is empty, not zero, when a session lacks the evidence it needs: tool data, timestamps, your classified prompts or a known context window. See docs/session-shape.md for each rule's origin and limits.</p></header>
    <dl>{definitionMeasures.map(id => <React.Fragment key={id}><dt>{catalog.measures[id].label}</dt><dd>{catalog.measures[id].note}</dd></React.Fragment>)}</dl>
  </section>;
}
