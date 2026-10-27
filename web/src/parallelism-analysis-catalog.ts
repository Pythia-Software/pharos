import type { AnalysisCatalog, AnalysisMeasure } from "./dashboard-analysis-model";

// Parallelism over the parallelism dataset: one record is one local hour of
// one agent (a workspace, or a top-level session, by the user's counting) at
// one machine-wide level of concurrency. The service builds every record
// with the user's saved counting and turn-gap settings, and serves no
// others, so the catalog filters nothing by them; the dashboard reads the
// settings the returned records carry and says which they are
// (parallelismSettingsNote). A per-request override, like Carbon's, would
// need a Go change.

export type ParallelismCounting = "workspace" | "agent";
export type ParallelismSettings = { counting: ParallelismCounting; gap: number };

const hours = { kind: "number", decimals: 1 } as const, ratio = { kind: "number", decimals: 2 } as const, count = { kind: "number", decimals: 0 } as const;
const measures: Record<string, AnalysisMeasure> = {
  agent_hours: { label: "Agent hours", unit: "hours", format: hours, op: "sum", field: "unit_hours", value: "unit_hours", additive: true, note: "Agent hours count each active agent's time, so two agents working through one hour are two agent hours." },
  active_hours: { label: "Active hours", unit: "hours", format: hours, op: "sum", field: "elapsed_hours", value: "elapsed_hours", additive: true, note: "Active hours are wall time with any agent active. Each record holds its share of that time (one over the agents active at once), so groups add up to the wall time without counting an hour twice." },
  parallel_hours: { label: "Hours with two or more at once", unit: "hours", format: hours, op: "sum", field: "parallel_elapsed_hours", value: "parallel_elapsed_hours", additive: true, note: "Wall time with two or more agents active at once, shared among them as active hours are." },
  at_once: { label: "Agents at once while active", unit: "agents", format: ratio, op: "sum", ratio: ["unit_ms", "elapsed_ms"], additive: false, note: "Agent time divided by wall time with any agent active, pooled over each group's records: the time-weighted number of agents working together while any is." },
  agents: { label: "Agents", unit: "agents", format: count, op: "count_distinct", field: "unit_id", additive: false, note: "Distinct agents (workspaces or top-level sessions, by your counting) active in each group. One active in several groups counts in each." },
};

const single = (entries: Array<[string, string]>): AnalysisCatalog["breakdowns"] => Object.fromEntries(entries.map(([field, label]) => [field, { label, fields: [field] }]));
const origin = { channel: ["channel"], machine_id: ["machine_id"], execution_machine_id: ["execution_machine_id"] };

// Provider and model belong to a top-level session: the service records them
// only under agent counting, so other counting offers neither. Without
// settings (reading links) every breakdown is offered.
export function parallelismAnalysisCatalog(counting?: ParallelismCounting): AnalysisCatalog {
  const agent = counting === undefined || counting === "agent";
  const catalog: AnalysisCatalog = {
    dataset: "parallelism", time: { first: "started_at", last: "started_at" }, measures,
    breakdowns: {
      level_band: { label: "Agents at once", fields: ["level_band"], order: ["1", "2", "3", "4+"] },
      repository_name: { label: "Repository", fields: ["repository_name"], empty: "No repository" },
      ...single([...agent ? [["provider", "Provider"], ["model", "Model"]] as Array<[string, string]> : [], ["workspace_name", "Workspace"], ["channel", "Channel"], ["machine_id", "Machine"], ["execution_machine_id", "Execution machine"]]),
    },
    records: { label: "Agent hour", plural: "Agent hours", note: "A record is one local hour of one agent at one level of agents active at once." },
    units: {
      agent: { label: "Agent", plural: "Agents", key: "unit_id", where: [{ field: "unit_id", op: "is_not_null", value: "" }],
        breakdowns: { repository_name: ["repository_name"], ...agent ? { provider: ["provider"] } : {}, workspace_name: ["workspace_name"], ...origin },
        note: "An agent is one workspace, or one top-level session, by your counting setting; its repository and origin are constant." },
    },
    cycle: { hour: { kind: "timestamp", field: "started_at" }, weekday: { kind: "timestamp", field: "started_at" } },
    periods: { day: "day", week: "week", month: "month" }, timestamp: "started_at",
    trend: { metric: "agent_hours", split: "level_band" },
    scatter: ["agent_hours", "active_hours"],
  };
  if (catalog.breakdowns.provider) catalog.breakdowns.provider.order = ["claude", "antigravity", "codex", "chatgpt", "tl1", "canonical"];
  return catalog;
}
export const parallelismCatalogShape = parallelismAnalysisCatalog();

// What the returned records were built with, in words, and a warning when
// that is not what the page believes is saved (another window changed the
// settings, say). returned lists the distinct settings the records carry.
export function parallelismSettingsNote(returned: ParallelismSettings[], believed: ParallelismSettings): { note: string; warning?: string } {
  const describe = ({ counting, gap }: ParallelismSettings) => `count ${counting === "agent" ? "top-level sessions" : "workspaces"} and join turns across gaps up to ${gap} minutes`;
  if (!returned.length) return { note: `No records in range; records are built with your saved Parallelism settings, which here ${describe(believed)}.` };
  const note = `These records ${returned.map(describe).join("; others ")}: the saved Parallelism settings the service built them with.`;
  const differs = returned.some(item => item.counting !== believed.counting || item.gap !== believed.gap);
  return differs ? { note, warning: `This page believes the saved settings ${describe(believed)}; the records were built with others, perhaps saved in another window. Reload to follow them.` } : { note };
}
