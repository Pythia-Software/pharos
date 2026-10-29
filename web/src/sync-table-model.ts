import { loadSchema, type FieldSchema } from "@pythia-software/query-table-core";

// /api/sync/status is a bounded snapshot (at most 200 runs on this Mac).
// Keep numeric and date values intact: formatting belongs to the cell renderer,
// so filters, ordering, grouping and metrics operate on the measured values.
export type SyncRecord = Record<string, any>;
export type SyncTable = { schema: FieldSchema<SyncRecord>; rows: SyncRecord[]; empty: string };
type Column = [name: string, label: string, type?: "text" | "number" | "date", render?: string, width?: number];
function schema(name: string, columns: Column[], sort?: string, hidden: Column[] = []): FieldSchema<SyncRecord> {
  return loadSchema<SyncRecord>({ name: `sync_${name}`, version: 1, idField: "id", defaultLimit: 25,
    defaultSort: sort ? [{ field: sort, dir: "desc" }] : [],
    defaultSelect: columns.map(([field, , , , width]) => ({ field, width: width ?? 140 })),
    fields: [{ name: "id", label: "Identity", type: "text", select: { default: false } },
      ...[...columns, ...hidden].map(([name, label, type = "text", render]) => ({ name, label, type, render, aggregate: { groupable: true } }))],
  });
}
const duration = (name: string, label: string): Column => [name, label, "number", "sync_seconds", 110];
const memory = (name: string, label: string): Column => [name, label, "number", "sync_memory", 110];
const date = (name: string, label: string): Column => [name, label, "date", "sync_time", 160];
const count = (name: string, label: string): Column => [name, label, "number", "number", 120];
const percent = (name: string, label: string): Column => [name, label, "number", "sync_percent", 140];
export const syncSchemas = {
  history: schema("history", [date("finished_at", "When"), ["result", "Result", "text", "sync_result", 140], ["sources", "Sources", "text", undefined, 170], count("conversations", "Conversations"), count("messages_in_changed_groups", "Messages"), duration("seconds", "Elapsed"), duration("cpu_seconds", "CPU time"), memory("rss_observed_peak_bytes", "RAM peak")], "finished_at", [
    ["state", "State"], ["trigger", "Trigger"], ["class", "Workload"], count("changed_groups", "Changed groups"), percent("cpu_percent_one_core", "CPU (% of one core)"), memory("rss_baseline_bytes", "RAM at start"), memory("allocated_bytes", "Allocated"), ["verification", "Verification"],
  ]),
  sources: schema("sources", [["source_name", "Source"], date("attempted_at", "Last checked"), date("succeeded_at", "Last success"), date("last_capture_at", "Last captured"), ["status", "Status", "text", "sync_result"], ["error", "Error", "text", "text", 300]]),
  costs: schema("costs", [["workload", "Workload"], count("samples", "Runs"), duration("elapsed_median", "Elapsed median"), duration("elapsed_p95", "Elapsed p95"), duration("cpu_median", "CPU median"), duration("cpu_p95", "CPU p95"), memory("ram_last", "Last RAM peak"), percent("cpu_duty", "Estimated CPU duty (% of one core)")]),
  accuracy: schema("accuracy", [["operation", "Operation"], ["checkpoint", "Checkpoint"], count("predicted", "Available"), count("attempts", "Attempts"), count("evaluated", "Evaluated"), duration("error_seconds", "Median error"), percent("error_percent", "Median relative error"), count("covered", "Within range"), duration("width_seconds", "Median range width")]),
  coverage: schema("coverage", [["source_name", "Source"], count("units", "Units"), ["outcome", "Result"], date("last_verification", "Last verified")]),
  deferred: schema("deferred", [["name", "Work"], date("changed_at", "Changed"), date("completed_at", "Last completed")]),
  sourceResults: schema("source_results", [["source", "Source"], count("conversations", "Conversations"), count("messages", "Messages"), count("workspaces", "Changed groups"), ["status", "Status"], ["error", "Error", "text", "text", 300]]),
  phases: schema("phases", [["name", "Phase"], duration("seconds", "Elapsed"), duration("cpu_seconds", "CPU time")]),
  stages: schema("stages", [["stage", "Stage", "text", undefined, 200], count("done", "Done"), count("total", "Total"), ["unit", "Unit"], duration("seconds", "Elapsed"), count("conversations", "Input conversations"), count("groups", "Input groups"), memory("bytes", "Input bytes"), ["bytes_basis", "Bytes basis"], count("messages", "Input messages"), duration("since_last_index_seconds", "Since prior index"), count("load_one_minute", "Load (1 min)")]),
  forecasts: schema("forecasts", [["checkpoint", "When predicted"], duration("elapsed_seconds", "Into run"), duration("point_seconds", "Estimated remaining"), duration("low_seconds", "Low estimate"), duration("high_seconds", "High estimate"), duration("actual_remaining_seconds", "Actual remaining"), duration("error_seconds", "Error (estimate − actual)"), count("predicted_steps", "Steps estimated"), count("remaining_steps", "Remaining steps")]),
};
export const operationLabels: Record<string, string> = { "automatic-sync": "Automatic changes", "manual-sync": "Manual changes", "capture-index": "Index captures", "library-update:update": "Capture & index", "library-update:full": "Full rebuild", "library-update:retained": "Retained repair" };
export const sourceNames = (run: SyncRecord): string[] => run.sources?.length ? run.sources : [...new Set<string>([
  ...(run.source_results ?? []).map((result: SyncRecord) => result.source),
  ...(run.phases ?? []).filter((phase: SyncRecord) => !["audit", "projections", "github", "identities", "git", "tools", "authorship", "findings"].includes(phase.name)).map((phase: SyncRecord) => phase.name),
])];
// A deferred run left sources a capture was using to the next run.
export const resultLabel = (run: SyncRecord): string => run.state === "failed" ? "Failed" : run.state === "interrupted" ? "Stopped"
  : run.audit?.outcome === "mismatch" ? "Needs attention" : run.state === "deferred" ? "Capture was running" : run.state && run.state !== "complete" ? run.state
  : run.audit?.outcome && run.audit.outcome !== "passed" ? "Check deferred" : run.changed_groups > 0 || run.conversations > 0 ? "Indexed" : "No changes";
export const runKey = (run: SyncRecord): string => run.run_id || run.finished_at;
const validDate = (value: unknown) => typeof value === "string" && Number.isFinite(Date.parse(value)) ? value : null;
const measured = (value: unknown): number | null => typeof value === "number" && Number.isFinite(value) ? value : null;
const rows = (items: SyncRecord[] | null | undefined, identity: (item: SyncRecord, index: number) => string): SyncRecord[] => (items ?? []).map((item, index) => ({ ...item, id: identity(item, index) }));

export function syncTables(status: SyncRecord): Record<"history" | "sources" | "costs" | "accuracy" | "coverage" | "deferred", SyncTable> {
  const table = (key: keyof typeof syncSchemas, rows: SyncRecord[], empty: string): SyncTable => ({ schema: syncSchemas[key], rows, empty });
  return {
    history: table("history", (status.history ?? []).map((run: SyncRecord) => ({
      id: runKey(run), finished_at: validDate(run.finished_at), result: resultLabel(run), sources: sourceNames(run).join(", ") || null,
      ...Object.fromEntries(["conversations", "messages_in_changed_groups", "seconds", "cpu_seconds", "rss_observed_peak_bytes", "changed_groups", "cpu_percent_one_core", "rss_baseline_bytes", "allocated_bytes"].map(key => [key, measured(run[key])])),
      state: run.state ?? null, trigger: run.trigger ?? null, class: run.class ?? null, verification: run.audit?.outcome ?? null,
    })), "No runs yet. Use Index changes now to index your selected sources."),
    sources: table("sources", rows(status.source_states, source => source.source_name).map(source => ({ ...source,
      attempted_at: validDate(source.attempted_at), succeeded_at: validDate(source.succeeded_at), last_capture_at: validDate(source.last_capture_at), error: source.error || null,
      status: source.error ? "Error" : source.pending_preservation ? "Needs capture" : "Up to date",
    })), "Source status will appear after the first run."),
    costs: table("costs", Object.entries<SyncRecord>(status.statistics ?? {}).map(([key, stats]) => ({ id: key, workload: key.replaceAll("_", " "), samples: stats.samples,
      elapsed_median: measured(stats.elapsed_seconds?.median), elapsed_p95: measured(stats.elapsed_seconds?.p95), cpu_median: measured(stats.cpu_seconds?.median), cpu_p95: measured(stats.cpu_seconds?.p95), ram_last: measured(stats.rss_observed_peak_bytes?.last), cpu_duty: measured(stats.estimated_cpu_duty_percent_one_core),
    })), "Performance measurements will appear after the first run."),
    accuracy: table("accuracy", Object.entries<SyncRecord>(status.forecast_accuracy ?? {}).flatMap(([model, checkpoints]) => Object.entries<SyncRecord>(checkpoints).filter(([, stats]) => stats.attempts > 0).map(([checkpoint, stats]) => ({
      id: JSON.stringify([model, checkpoint]), operation: operationLabels[model] || model, checkpoint: checkpoint === "discovered" ? "After discovery" : checkpoint,
      predicted: stats.predicted, attempts: stats.attempts, evaluated: stats.evaluated, covered: stats.evaluated ? stats.covered : null,
      error_seconds: stats.evaluated ? measured(stats.median_absolute_error_seconds) : null, error_percent: stats.evaluated && measured(stats.median_relative_error) !== null ? stats.median_relative_error * 100 : null,
      width_seconds: stats.evaluated ? measured(stats.median_interval_width_seconds) : null,
    }))), "Forecast measurements will appear after completed runs."),
    coverage: table("coverage", rows(status.coverage, item => JSON.stringify([item.source_name, item.outcome])).map(item => ({ ...item, last_verification: validDate(item.last_verification) })), "No verification coverage recorded yet."),
    deferred: table("deferred", rows(status.deferred, item => item.name).map(item => ({ ...item, changed_at: validDate(item.changed_at), completed_at: validDate(item.completed_at) })), "No pending library-wide work."),
  };
}

export function runTables(run: SyncRecord): Record<"sourceResults" | "phases" | "stages" | "forecasts", SyncTable> {
  const table = (key: keyof typeof syncSchemas, rows: SyncRecord[], empty: string): SyncTable => ({ schema: syncSchemas[key], rows, empty });
  return {
    sourceResults: table("sourceResults", rows(run.source_results, result => result.source).map(result => ({ ...result, status: result.error ? "Failed" : result.deferred ? "Capture was running" : result.yielded ? "Partial" : "Complete", error: result.error || null })), "Per-source counts were not recorded for this older run."),
    phases: table("phases", rows(run.phases, (phase, index) => `${phase.name}:${index}`).map(phase => ({ ...phase, name: phase.name === "audit" ? "Verification" : phase.name === "projections" ? "Library view" : phase.name })), "No phase measurements recorded."),
    stages: table("stages", (run.phases ?? []).flatMap((phase: SyncRecord, phaseIndex: number) => (phase.work ?? []).map((work: SyncRecord, index: number) => ({
      id: `${phaseIndex}:${index}`, stage: `${phase.name}: ${work.phase}`, done: work.unit ? measured(work.done) : null, total: work.unit && (work.total_known || work.total > 0) ? measured(work.total) : null, unit: work.unit || null, seconds: measured(work.seconds),
      ...Object.fromEntries(["conversations", "groups", "bytes", "messages", "since_last_index_seconds", "load_one_minute"].map(key => [key, measured(work.features?.[key])])), bytes_basis: work.features?.bytes_basis || null,
    }))), "No stage measurements recorded."),
    forecasts: table("forecasts", (run.forecasts ?? []).map((snapshot: SyncRecord, index: number) => {
      const estimate = snapshot.estimate ?? {}, predicted = estimate.point_seconds > 0, actual = measured(snapshot.actual_remaining_seconds);
      return { id: String(index), checkpoint: snapshot.reason === "launch" ? "At launch" : snapshot.reason === "discovered" ? "After discovery" : snapshot.reason,
        elapsed_seconds: measured(snapshot.elapsed_seconds), point_seconds: predicted ? estimate.point_seconds : null, low_seconds: predicted ? measured(estimate.low_seconds) : null, high_seconds: predicted ? measured(estimate.high_seconds) : null,
        actual_remaining_seconds: actual, error_seconds: predicted && actual !== null ? estimate.point_seconds - actual : null, predicted_steps: estimate.predicted_steps ?? 0, remaining_steps: estimate.remaining_steps ?? 0 };
    }), "No forecasts recorded."),
  };
}
