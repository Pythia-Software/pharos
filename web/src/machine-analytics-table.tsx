import React, { useEffect, useMemo, useRef } from "react";
import { inspectBucketQuery, loadSchema, memoryStorageAdapter, type WhereTerm } from "@pythia-software/query-table-core";
import { useQueryTable, type QueryTableApi } from "@pythia-software/query-table-react";
import { DataTable, QueryBuilder, MetricsPanel, defaultRenderers, type RenderRegistry } from "@pythia-software/query-table-ui";
import { activityPresentation, groupTime, timeLabel, totalTime, type TimeFact } from "./machine-activity-data";

type Row = Record<string, any>;
export type MachineGrouping = "operation" | "tool" | "activity";
type Filter = (where: WhereTerm[], scroll?: boolean) => void;

export function factGroup(fact: TimeFact, grouping: MachineGrouping): string {
  return grouping === "operation" ? fact.operation : grouping === "tool" ? JSON.stringify([fact.tool, fact.server]) : fact.activity;
}
export function groupFilters(fact: TimeFact, grouping: MachineGrouping): WhereTerm[] {
  return grouping === "operation" ? [{ field: "operation_key", op: "=", value: fact.operation }]
    : grouping === "activity" ? [{ field: "activity", op: "=", value: fact.activity }]
    : [{ field: "tool_name", op: fact.tool ? "=" : "is_null", value: fact.tool }, { field: "mcp_server", op: fact.server ? "=" : "is_null", value: fact.server }];
}

const columns = [
  ["name", "Command family / tool", "text", "machine_name", 210],
  ["activity", "Activity", "text", "machine_activity", 115],
  ["duration", "Total time", "number", "duration_ms", 95],
  ["calls", "Calls", "number", "number", 70],
  ["average", "Time / timed run", "number", "duration_ms", 115],
  ["previous_average", "Previous / run", "number", "duration_ms", 115],
  ["per_run_change", "Change / run", "number", "machine_change", 100],
  ["timed", "Timed calls", "number", "number", 80],
] as const;
const schema = loadSchema<Row>({ name: "machine_time_analytics", version: 1, idField: "id", defaultLimit: 25,
  defaultSort: [{ field: "duration", dir: "desc" }],
  defaultSelect: columns.map(([field, , , , width]) => ({ field, width })),
  fields: [
    { name: "id", label: "Identity", type: "text", select: { default: false } },
    ...columns.map(([name, label, type, render]) => ({ name, label, type, render, aggregate: { groupable: true } })),
    { name: "coverage", label: "Timing coverage (%)", type: "number", render: "number", select: { default: false }, aggregate: { groupable: true } },
    { name: "failedDuration", label: "Time in failed calls", type: "number", render: "duration_ms", select: { default: false }, aggregate: { groupable: true } },
  ],
});

export function MachineAnalyticsTable({ current, previous, grouping, compared, filter, selectionKey }: { current: TimeFact[]; previous: TimeFact[]; grouping: MachineGrouping; compared: boolean; filter: Filter; selectionKey: string }) {
  const rows = useMemo(() => {
    const baseline = new Map(groupTime(previous, fact => factGroup(fact, grouping)).map(group => [group.key, group]));
    return groupTime(current, fact => factGroup(fact, grouping)).map(group => {
      const fact = group.facts[0], prior = baseline.get(group.key) ?? totalTime([]);
      const average = group.timed ? group.duration / group.timed : null;
      const previousAverage = compared && prior.timed ? prior.duration / prior.timed : null;
      return { ...group, id: group.key, name: grouping === "operation" ? fact.name : grouping === "tool" ? [fact.server, fact.tool || "(Unidentified tool)"].filter(Boolean).join(" / ") : activityPresentation(group.key).label,
        activity: grouping === "tool" && new Set(group.facts.map(fact => fact.activity)).size > 1 ? "Multiple activities" : fact.activity,
        average, previous_average: previousAverage,
        per_run_change: average === null || previousAverage === null || previousAverage === 0 ? null : (average / previousAverage - 1) * 100,
        coverage: group.calls ? group.timed / group.calls * 100 : null, fact, compared };
    });
  }, [current, previous, grouping, compared]);
  const storage = useMemo(() => memoryStorageAdapter(), []);
  const api = useQueryTable<Row>({ schema, clientRows: rows, storage });
  const apiRef = useRef(api); apiRef.current = api;
  const stableApi = useMemo(() => new Proxy({} as QueryTableApi<Row>, { get: (_target, property) => apiRef.current[property as keyof QueryTableApi<Row>] }), []);
  useEffect(() => { apiRef.current.setQuery(query => query.offset ? { ...query, offset: 0 } : query); }, [selectionKey, grouping]);
  const renderers: RenderRegistry<Row> = {
    ...defaultRenderers,
    machine_name: ({ value, row }) => row.fact ? <button type="button" className="machine-cell-link" onClick={() => filter(groupFilters(row.fact, grouping))}>{String(value)}</button> : String(value ?? ""),
    machine_activity: ({ value }) => activityPresentation(String(value)).label,
    duration_ms: ({ value }) => value == null ? "—" : timeLabel(Number(value)),
    machine_change: ({ value, row }) => value == null ? <span className="muted">{row.compared ? "No timed baseline" : "Select a period"}</span> : `${Number(value) > 0 ? "+" : ""}${Number(value).toFixed(1)}%`,
  };
  return <section className="machine-analysis" aria-label="Tool analytics table">
    <QueryBuilder api={stableApi} fields={api.query.grouping ? api.schema!.fields : api.computed.catalogue} total={api.total} running={api.loading} defaultCollapsed />
    <MetricsPanel api={stableApi} aggregations={api.aggregations} fields={api.query.grouping ? api.schema!.fields : api.computed.catalogue} renderers={renderers}
      onInspect={(bucket, clause) => { const rows = inspectBucketQuery(api.query, bucket, clause, schema); if (rows) api.setQuery(rows); }} />
    <DataTable fields={api.visibleFields} rows={api.rows} query={api.query} onQueryChange={api.setQuery} renderers={renderers} rowId={api.rowId} columnDrag={api.columnDrag} total={api.total} loading={api.loading}
      emptyMessage="No operations match these analytics-table filters." {...(!api.query.grouping ? { trailing: (row: Row) => <button type="button" className="machine-cell-link" onClick={() => filter(groupFilters(row.fact, grouping), true)}>Inspect calls ↓</button>, trailingLabel: "Source calls" } : {})} />
    {api.error ? <p role="alert">{api.error.message}</p> : null}
  </section>;
}
