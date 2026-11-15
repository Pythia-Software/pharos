import { ViewDisclosure } from "./view-disclosure";
import React, { useEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { inspectBucketQuery } from "@pythia-software/query-table-core";
import { useQueryTable, type QueryTableApi } from "@pythia-software/query-table-react";
import { DataTable, QueryBuilder, MetricsPanel, ModalSurface, defaultRenderers, type RenderRegistry } from "@pythia-software/query-table-ui";
import { clientTableStorageAdapter } from "./storage";
import { useLocationRevision } from "./nested-pages";
import { useViewEntry } from "./view-state";
import { syncTables, runTables, runKey, resultLabel, operationLabels, operationHistoryTable, operationStates, operationTriggers, operationReasons, verificationOutcomes, type SyncRecord, type SyncTable } from "./sync-table-model";

declare global {
  interface Window {
    pharosSync: {
      status: () => SyncRecord | null;
      tableStatus: () => SyncRecord | null;
      refresh: () => Promise<void>;
      format: { seconds: (value: unknown) => string; memory: (value: unknown) => string; when: (value: unknown) => string; number: (value: unknown) => string; percent: (value: unknown) => string };
    };
  }
}
const format = (key: keyof Window["pharosSync"]["format"], value: unknown) => window.pharosSync.format[key](value);
const plural = (count: number, unit: string) => `${format("number", count)} ${unit}${count === 1 ? "" : "s"}`;
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
  sync_result: ({ value }) => <span className={`sync-result${["Failed", "Stopped", "Needs attention", "Indexed; verification postponed", "Verification failed", "Verification incomplete", "Error", "Needs capture"].includes(String(value)) ? " attention" : ""}`}>{String(value ?? "—")}</span>,
};

function SyncQueryTable({ table, label, onInspect }: { table: SyncTable; label: string; onInspect?: (id: string) => void }) {
  const locationRevision = useLocationRevision();
  const active = location.pathname === "/settings/sync";
  const [collapsed, setCollapsed] = useViewEntry("query_layout", table.schema.name, false, active);
  const storage = useMemo(() => clientTableStorageAdapter(), []);
  // Each table owns its local view. The bounded status response is its entire
  // population; no extra catalog queries or table-specific polling is needed.
  const api = useQueryTable<SyncRecord>({ schema: table.schema, clientRows: table.rows, storage, active,
    syncUrl: true, queryParameter: `q_${table.schema.name}`, onRefresh: () => { void window.pharosSync.refresh(); } });
  const ref = useRef(api); ref.current = api;
  useEffect(() => {
    if (!active) return;
    void ref.current.restoreLocation(location.href).catch(() => {}); // The native API exposes restoration errors in api.error.
  }, [active, locationRevision]);
  // Loading until this exact query's rows have arrived (or failed).
  const loading = api.execution?.rows !== "succeeded" && api.execution?.rows !== "failed";
  const fields = api.query.grouping ? api.schema!.fields : api.computed.catalogue;
  return <div className="sync-query-table pharos-query-page" role="region" aria-label={label}>
    <QueryBuilder compact api={api} fields={fields} total={api.total} running={api.loading} collapsed={collapsed} onCollapsedChange={setCollapsed} />
    <MetricsPanel api={api} aggregations={api.aggregations} fields={fields} renderers={renderers} onInspect={(bucket, clause) => {
      const query = inspectBucketQuery(api.query, bucket, clause, table.schema); if (query) api.setQuery(query);
    }} />
    <DataTable fields={api.visibleFields} rows={api.rows} query={api.query} onQueryChange={api.setQuery} renderers={renderers} rowId={api.rowId} columnDrag={api.columnDrag} total={api.total} loading={loading}
      emptyMessage={loading ? "Loading results…" : table.rows.length ? "No rows match these filters." : table.empty}
      {...(onInspect && !api.query.grouping ? { trailing: (row: SyncRecord) => <button type="button" className="sync-detail-button" onClick={() => onInspect(String(row.id))}>Details</button>, trailingLabel: "Details" } : {})} />
    {api.error ? <p className="query-table-error" role="alert">{api.error.message}</p> : null}
  </div>;
}
const HistoryQueryTable = React.memo(SyncQueryTable);

function OperationDetails({ operation, onClose }: { operation: SyncRecord; onClose: () => void }) {
  return <ModalSurface title={operation.label || operation.kind} onClose={onClose} className="qt-modal sync-run-dialog"><div className="qt-overlay-body">
    <p><Timestamp value={operation.started_at} /> · {operationStates[operation.state] || operation.state}{operation.reason ? ` · ${operationReasons[operation.reason] || operation.reason}` : ""}</p>
    {operation.subject ? <p>Subject: {operation.subject}</p> : null}
    <p>Started by: {operation.trigger ? operationTriggers[operation.trigger] || operation.trigger : "Unknown"}{operation.process ? ` · ${operation.process}` : ""}</p>
    {operation.step_name ? <p>Last step: {operation.step_name}{operation.step_unit ? ` · ${format("number", operation.step_done)} ${operation.step_unit}` : ""}</p> : null}
    {operation.error ? <p role="alert">{operation.error}</p> : null}
    {operation.ended_at ? <p>Finished <Timestamp value={operation.ended_at} /></p> : null}
  </div></ModalSurface>;
}

function RunDetails({ run, onClose }: { run: SyncRecord; onClose: () => void }) {
  const tables = useMemo(() => runTables(run), [run]);
  return <ModalSurface title="Index run details" onClose={onClose} className="qt-modal sync-run-dialog">
    <div className="qt-overlay-body">
    <p><Timestamp value={run.finished_at} /> · {resultLabel(run)}</p>
    {resultLabel(run) === "Indexed; verification postponed" ? <p>Indexing finished, but the verification check was postponed. Inputs may still be changing or exceed the automatic check’s time or size limit.</p> : null}
    {run.audit ? <p>Verification: {run.audit.outcome ? verificationOutcomes[run.audit.outcome] || run.audit.outcome : "Unknown"} · {format("number", run.audit.discovered_units)} inputs found · {format("number", run.audit.deferred_units)} deferred. {run.audit.detail}</p> : null}
    {run.audit?.evidence_bytes > 0 ? <p>Selected verification evidence: {format("memory", run.audit.evidence_bytes)}.</p> : null}
    {run.audit?.attempts?.length ? <><h3>Verification attempts</h3><ul>{run.audit.attempts.map((attempt: SyncRecord, index: number) => <li key={`${attempt.source}:${attempt.unit}:${index}`}>
      {attempt.source} · {verificationOutcomes[attempt.outcome] || attempt.outcome}
      {attempt.reason ? ` · ${verificationReasons[attempt.reason] || attempt.reason}` : ""} · {format("seconds", attempt.seconds)}
      {attempt.evidence_bytes > 0 ? ` · ${format("memory", attempt.evidence_bytes)} of evidence` : ""}
    </li>)}</ul></> : null}
    <h3>Indexed by source</h3><SyncQueryTable table={tables.sourceResults} label="Source breakdown" />
    {tables.phases.rows.length ? <><h3>Time by phase</h3><SyncQueryTable table={tables.phases} label="Time by phase" /></> : null}
    {tables.stages.rows.length ? <><h3>Stage measurements</h3><SyncQueryTable table={tables.stages} label="Stage measurements" /></> : null}
    {tables.forecasts.rows.length ? <><h3>Forecast accuracy</h3><SyncQueryTable table={tables.forecasts} label="Forecast accuracy for run" /></> : null}
    {run.trigger ? <p className="muted">{run.trigger === "manual" ? "Manual index · includes analysis refreshes" : "Automatic index · analyses deferred"}</p> : null}
    <p className="muted">CPU: {format("percent", run.cpu_percent_one_core)} of one core · RAM at start: {format("memory", run.rss_baseline_bytes)} · Allocated: {format("memory", run.allocated_bytes)}</p>
    </div>
  </ModalSurface>;
}

function Diagnostics({ status, tables }: { status: SyncRecord; tables: ReturnType<typeof syncTables> }) {
  const queued: number = (status.verification?.pending ?? []).reduce((total: number, row: SyncRecord) => total + Number(row.units || 0), 0);
  return <ViewDisclosure viewKey="sync:diagnostics" className="sync-panel sync-disclosure" summary="Performance & verification details">
    {status.verification?.available ? <>
      <p>Last completed verification: <Timestamp value={status.verification.last_verified_at} />.</p>
      {queued > 0 ? <p>{plural(queued, "input")} queued for larger checks by Verify indexed data, on its background schedule.</p> : null}
      {status.verification.pending?.length ? <ul>{status.verification.pending.map((row: SyncRecord) => <li key={`${row.source_name}:${row.reason}`}>{row.source_name} · {format("number", row.units)} inputs · {verificationReasons[row.reason] || row.reason}</li>)}</ul> : null}
    </> : null}
    <p className="muted">Measured cost by workload, across the last 200 runs. Median is the typical run; p95 covers 95% of runs.</p>
    <SyncQueryTable table={tables.costs} label="Measured indexing cost" />
    {tables.accuracy.rows.length ? <>
      <h3>Forecast accuracy</h3><SyncQueryTable table={tables.accuracy} label="Operation forecast accuracy" />
      <p className="muted">Attempts counts every run at each checkpoint; Available counts those that had an estimate. Median error and Within range use completed runs only, one prediction per run at each checkpoint. Ranges widen with past errors, so coverage is measured, not guaranteed.</p>
    </> : null}
    {(status.forecast_runs ?? []).filter((run: SyncRecord) => run.model?.startsWith("library-update:") || run.model === "capture-index").slice(0, 5).map((run: SyncRecord) => <ViewDisclosure key={runKey(run)} viewKey={`sync:forecast:${runKey(run)}`} className="sync-separate" summary={<>{operationLabels[run.model] || run.model} · {format("when", run.finished_at)} · {run.state}</>}>
      <SyncQueryTable table={runTables(run).forecasts} label="Operation forecasts" />
    </ViewDisclosure>)}
    {status.measurement_limits ? <p className="muted">{status.measurement_limits}</p> : null}
    {tables.coverage.rows.length ? <><h3>Verification coverage</h3><SyncQueryTable table={tables.coverage} label="Verification coverage" /><p className="muted">Sampling does not verify the entire library.</p></> : null}
    {tables.deferred.rows.length ? <><h3>Pending library-wide work</h3><SyncQueryTable table={tables.deferred} label="Pending library-wide work" /></> : null}
  </ViewDisclosure>;
}

export function SyncTables() {
  // The tables read a snapshot that background polls leave alone; the busy
  // note reads the live status.
  const read = () => ({ live: window.pharosSync?.status() ?? null, snapshot: window.pharosSync?.tableStatus() ?? null });
  const [{ live, snapshot: status }, setStatus] = useState(read);
  const [selected, setSelected] = useState<string | null>(null);
  useEffect(() => {
    const update = () => setStatus(previous => { const next = read(); return next.live === previous.live && next.snapshot === previous.snapshot ? previous : next; });
    window.addEventListener("pharos:sync-status", update); update();
    return () => window.removeEventListener("pharos:sync-status", update);
  }, []);
  const tables = useMemo(() => syncTables(status ?? {}), [status]);
  const history = useMemo(() => operationHistoryTable(status?.operation_history), [status?.operation_history]);
  const selectedOperation = (status?.operation_history ?? []).find((operation: SyncRecord) => operation.id === selected);
  const selectedRun = selectedOperation?.legacy_ref && (status?.history ?? []).find((run: SyncRecord) => runKey(run) === selectedOperation.legacy_ref);
  if (!status?.settings) return <p className="muted" role="status">Loading recent background work…</p>;
  const current = live ?? status;
  return <>
    <ViewDisclosure viewKey="sync:history" defaultOpen className="sync-panel sync-disclosure" summary="Recent background work">
      <div className="sync-section-head"><p className="muted">Background work on this Mac: indexing, analyses, captures, backups and maintenance.</p>
        {current.busy ? <span className="sync-result">{current.pause_reason && current.pause_reason !== "Off" ? current.pause_reason : "Indexing in progress"}</span> : null}</div>
      <p className="muted">These tables update only when you use Run Now or their auto-refresh.</p>
      {status.operation_history_error ? <p role="alert">Could not load recent background work: {status.operation_history_error}</p> : null}
      <HistoryQueryTable table={history} label="Recent background work" onInspect={setSelected} />
      <p className="muted">Shows the 50 most recent background work runs on this Mac. Short, unchanged automatic checks may not be recorded.</p>
    </ViewDisclosure>
    {createPortal(<ViewDisclosure viewKey="sync:sources" defaultOpen className="sync-panel sync-disclosure" summary="Source status"><SyncQueryTable table={tables.sources} label="Source status" /><p className="muted">Needs capture means changes have been indexed but the source files have not been captured yet.</p></ViewDisclosure>, document.getElementById("syncSourceTables")!)}
    {createPortal(<Diagnostics status={status} tables={tables} />, document.getElementById("syncDiagnosticTables")!)}
    {selectedRun ? <RunDetails key={selected} run={selectedRun} onClose={() => setSelected(null)} /> : selectedOperation ? <OperationDetails key={selected} operation={selectedOperation} onClose={() => setSelected(null)} /> : null}
  </>;
}
