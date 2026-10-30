import React, { useEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { inspectBucketQuery } from "@pythia-software/query-table-core";
import { useQueryTable, type QueryTableApi } from "@pythia-software/query-table-react";
import { DataTable, QueryBuilder, MetricsPanel, ModalSurface, defaultRenderers, type RenderRegistry } from "@pythia-software/query-table-ui";
import { clientTableStorageAdapter } from "./storage";
import { syncTables, runTables, runKey, resultLabel, operationLabels, type SyncRecord, type SyncTable } from "./sync-table-model";

declare global {
  interface Window {
    pharosSync: {
      status: () => SyncRecord | null;
      refresh: () => Promise<void>;
      format: { seconds: (value: unknown) => string; memory: (value: unknown) => string; when: (value: unknown) => string; number: (value: unknown) => string; percent: (value: unknown) => string };
    };
  }
}
const format = (key: keyof Window["pharosSync"]["format"], value: unknown) => window.pharosSync.format[key](value);
const verificationReasons: Record<string, string> = { evidence_size: "Selected evidence exceeds the check’s size limit", time_budget: "Verification time limit reached", source_changed: "Selected inputs changed", cancelled: "Stopped", unsupported_capture: "Required source capture is unavailable", source_unavailable: "Waiting for the source to be available", no_eligible_input: "Inputs are changing, queued or waiting for retry", read_error: "Could not read selected evidence" };
function Timestamp({ value }: { value: unknown }) {
  if (typeof value !== "string" || !Number.isFinite(Date.parse(value))) return <span className="muted">Not yet</span>;
  return <time dateTime={new Date(value).toISOString()} title={new Date(value).toLocaleString()}>{format("when", value)}</time>;
}
const renderers: RenderRegistry<SyncRecord> = {
  ...defaultRenderers,
  sync_time: ({ value }) => <Timestamp value={value} />,
  sync_seconds: ({ value }) => value == null ? "—" : Number(value) < 0 ? `−${format("seconds", -Number(value))}` : format("seconds", value),
  sync_memory: ({ value }) => format("memory", value),
  sync_percent: ({ value }) => format("percent", value),
  sync_result: ({ value }) => <span className={`sync-result${["Failed", "Stopped", "Needs attention", "Success without verification", "Verification failed", "Verification incomplete", "Error", "Needs capture"].includes(String(value)) ? " attention" : ""}`}>{String(value ?? "—")}</span>,
};

function SyncQueryTable({ table, label, onInspect }: { table: SyncTable; label: string; onInspect?: (id: string) => void }) {
  const storage = useMemo(() => clientTableStorageAdapter(), []);
  // Each table owns its local view. The bounded status response is its entire
  // population; no extra catalog queries or table-specific polling is needed.
  const api = useQueryTable<SyncRecord>({ schema: table.schema, clientRows: table.rows, storage, syncUrl: false, onRefresh: () => { void window.pharosSync.refresh(); } });
  const ref = useRef(api); ref.current = api;
  const stableApi = useMemo(() => new Proxy({} as QueryTableApi<SyncRecord>, { get: (_target, key) => ref.current[key as keyof QueryTableApi<SyncRecord>] }), []);
  // Loading until this exact query's rows have arrived (or failed).
  const loading = api.execution?.rows !== "succeeded" && api.execution?.rows !== "failed";
  const fields = api.query.grouping ? api.schema!.fields : api.computed.catalogue;
  return <div className="sync-query-table pharos-query-page" role="region" aria-label={label}>
    <QueryBuilder compact api={stableApi} fields={fields} total={api.total} running={api.loading} defaultCollapsed />
    <MetricsPanel api={stableApi} aggregations={api.aggregations} fields={fields} renderers={renderers} onInspect={(bucket, clause) => {
      const query = inspectBucketQuery(api.query, bucket, clause, table.schema); if (query) api.setQuery(query);
    }} />
    <DataTable fields={api.visibleFields} rows={api.rows} query={api.query} onQueryChange={api.setQuery} renderers={renderers} rowId={api.rowId} columnDrag={api.columnDrag} total={api.total} loading={loading}
      emptyMessage={loading ? "Loading results…" : table.rows.length ? "No rows match these filters." : table.empty}
      {...(onInspect && !api.query.grouping ? { trailing: (row: SyncRecord) => <button type="button" className="sync-detail-button" onClick={() => onInspect(String(row.id))}>Details</button>, trailingLabel: "Run details" } : {})} />
    {api.error ? <p className="query-table-error" role="alert">{api.error.message}</p> : null}
  </div>;
}
const HistoryQueryTable = React.memo(SyncQueryTable);

function RunDetails({ run, onClose }: { run: SyncRecord; onClose: () => void }) {
  const tables = useMemo(() => runTables(run), [run]);
  return <ModalSurface title="Index run details" onClose={onClose} className="qt-modal sync-run-dialog">
    <div className="qt-overlay-body">
    <p><Timestamp value={run.finished_at} /> · {resultLabel(run)}</p>
    {resultLabel(run) === "Success without verification" ? <p>Indexing finished, but the verification check was postponed. Inputs may still be changing or exceed the automatic check’s time or size limit.</p> : null}
    {run.audit ? <p>Verification: {run.audit.outcome || "Unknown"} · {format("number", run.audit.discovered_units)} discoverable units · {format("number", run.audit.deferred_units)} deferred. {run.audit.detail}</p> : null}
    {run.audit?.evidence_bytes > 0 ? <p>Selected verification evidence: {format("number", run.audit.evidence_bytes)} bytes.</p> : null}
    {run.audit?.attempts?.length ? <><h3>Verification attempts</h3><ul>{run.audit.attempts.map((attempt: SyncRecord, index: number) => <li key={`${attempt.source}:${attempt.unit}:${index}`}>
      {attempt.source} · {({passed: "Verified", mismatch: "Mismatch", deferred: "Skipped", read_error: "Could not verify"} as Record<string,string>)[attempt.outcome] || attempt.outcome}
      {attempt.reason ? ` · ${verificationReasons[attempt.reason] || attempt.reason}` : ""} · {format("seconds", attempt.seconds)}
      {attempt.evidence_bytes > 0 ? ` · ${format("number", attempt.evidence_bytes)} evidence bytes` : ""}
    </li>)}</ul></> : null}
    <h3>Indexed by source</h3><SyncQueryTable table={tables.sourceResults} label="Source breakdown" />
    {tables.phases.rows.length ? <><h3>Time by phase</h3><SyncQueryTable table={tables.phases} label="Time by phase" /></> : null}
    {tables.stages.rows.length ? <><h3>Stage measurements</h3><SyncQueryTable table={tables.stages} label="Stage measurements" /></> : null}
    {tables.forecasts.rows.length ? <><h3>Forecast accuracy</h3><SyncQueryTable table={tables.forecasts} label="Forecast accuracy for run" /></> : null}
    {run.trigger ? <p className="muted">{run.trigger === "manual" ? "Manual sync · includes analysis refreshes" : "Automatic indexing · analyses deferred"}</p> : null}
    <p className="muted">CPU: {format("percent", run.cpu_percent_one_core)} of one core · RAM at start: {format("memory", run.rss_baseline_bytes)} · Allocated: {format("memory", run.allocated_bytes)}</p>
    </div>
  </ModalSurface>;
}

function Diagnostics({ status, tables }: { status: SyncRecord; tables: ReturnType<typeof syncTables> }) {
  return <details className="sync-panel sync-disclosure">
    <summary>Performance &amp; verification details</summary>
    {status.verification?.available ? <>
      <p>Last completed verification: <Timestamp value={status.verification.last_verified_at} />.</p>
      <p>{format("number", (status.verification.pending ?? []).reduce((total: number, row: SyncRecord) => total + Number(row.units || 0), 0))} larger checks queued for Verify indexed data on the background schedule.</p>
      {status.verification.pending?.length ? <ul>{status.verification.pending.map((row: SyncRecord) => <li key={`${row.source_name}:${row.reason}`}>{row.source_name} · {format("number", row.units)} inputs · {verificationReasons[row.reason] || row.reason}</li>)}</ul> : null}
    </> : null}
    <p className="muted">Measured cost by workload, across the last 200 runs. Median is the typical run; p95 covers 95% of runs.</p>
    <SyncQueryTable table={tables.costs} label="Measured indexing cost" />
    {tables.accuracy.rows.length ? <>
      <h3>Forecast accuracy</h3><SyncQueryTable table={tables.accuracy} label="Operation forecast accuracy" />
      <p className="muted">Availability includes runs without an estimate. Errors and range coverage use completed runs only, with one prediction per run at each checkpoint. Ranges widen using previous errors; coverage is measured rather than guaranteed.</p>
    </> : null}
    {(status.forecast_runs ?? []).filter((run: SyncRecord) => run.model?.startsWith("library-update:") || run.model === "capture-index").slice(0, 5).map((run: SyncRecord) => <details key={runKey(run)} className="sync-separate">
      <summary>{operationLabels[run.model] || run.model} · {format("when", run.finished_at)} · {run.state}</summary>
      <SyncQueryTable table={runTables(run).forecasts} label="Operation forecasts" />
    </details>)}
    {status.measurement_limits ? <p className="muted">{status.measurement_limits}</p> : null}
    {tables.coverage.rows.length ? <><h3>Verification coverage</h3><SyncQueryTable table={tables.coverage} label="Verification coverage" /><p className="muted">Sampling does not verify the entire library.</p></> : null}
    {tables.deferred.rows.length ? <><h3>Pending library-wide work</h3><SyncQueryTable table={tables.deferred} label="Pending library-wide work" /></> : null}
  </details>;
}

export function SyncTables() {
  const [status, setStatus] = useState<SyncRecord | null>(() => window.pharosSync?.status() ?? null);
  const [selected, setSelected] = useState<string | null>(null);
  useEffect(() => {
    const update = () => setStatus(window.pharosSync?.status() ?? null);
    window.addEventListener("pharos:sync-status", update); update();
    return () => window.removeEventListener("pharos:sync-status", update);
  }, []);
  const tables = useMemo(() => syncTables(status ?? {}), [status]);
  const history = useMemo(() => syncTables({history: status?.history}).history, [status?.history]);
  const selectedRun = (status?.history ?? []).find((run: SyncRecord) => runKey(run) === selected);
  if (!status?.settings) return <p className="muted" role="status">Loading capture and index history…</p>;
  const latest = status.history?.[0];
  return <>
    <section className="sync-panel" aria-label="Recent runs">
      <div className="sync-section-head"><div><h3>Recent runs</h3><p className="muted">Index changes on this Mac{latest ? <> · Last run <Timestamp value={latest.finished_at} /> · {resultLabel(latest)}</> : null}</p></div>
        {status.busy ? <span className="sync-result">{status.pause_reason || "Indexing in progress"}</span> : null}</div>
      {latest ? <div className="sync-summary" aria-label="Last run cost">{[
        ["Conversations indexed", format("number", latest.conversations)], ["Elapsed time", format("seconds", latest.seconds)], ["CPU time", format("seconds", latest.cpu_seconds)], ["RAM peak", format("memory", latest.rss_observed_peak_bytes)],
      ].map(([label, value]) => <div key={label} className="sync-metric"><span>{label}</span><strong>{value}</strong></div>)}</div> : null}
      <p className="muted">Automatic table refresh is off. Use Run Now to refresh this table’s history.</p>
      <HistoryQueryTable table={history} label="Recent run history" onInspect={setSelected} />
      <p className="muted">Counts cover whole indexed conversations, not just newly added messages. CPU and RAM reflect the whole Pharos process, including other work. History includes up to 200 runs on this Mac.</p>
    </section>
    {createPortal(<section className="sync-panel"><h3>Source status</h3><SyncQueryTable table={tables.sources} label="Source status" /><p className="muted">Needs capture means changes have been indexed but the source files have not been captured yet.</p></section>, document.getElementById("syncSourceTables")!)}
    {createPortal(<Diagnostics status={status} tables={tables} />, document.getElementById("syncDiagnosticTables")!)}
    {selectedRun ? <RunDetails key={selected} run={selectedRun} onClose={() => setSelected(null)} /> : null}
  </>;
}
