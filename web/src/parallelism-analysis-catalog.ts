import type { WhereTerm } from "@pythia-software/query-table-core";
import type { AnalysisCatalog, AnalysisMeasure } from "./dashboard-analysis-model";

// Parallelism over the parallelism dataset: one record is one local hour of
// one agent (a workspace, or a top-level session, by the user's counting) at
// one machine-wide level of concurrency. The service builds the records with
// the user's saved counting and turn-gap settings; the catalog names them as
// the population every query runs under, so a record built with other
// settings is never counted and the note says which settings apply.

export type ParallelismRequest = { counting: "workspace" | "agent"; gap: number };

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

export function parallelismAnalysisCatalog(settings?: ParallelismRequest): AnalysisCatalog {
  const where: WhereTerm[] = settings ? [{ field: "counting", op: "=", value: settings.counting }, { field: "gap_minutes", op: "=", value: String(settings.gap) }] : [];
  const catalog: AnalysisCatalog = {
    dataset: "parallelism", time: { first: "started_at", last: "started_at" }, measures,
    breakdowns: {
      level_band: { label: "Agents at once", fields: ["level_band"], order: ["1", "2", "3", "4+"] },
      repository_name: { label: "Repository", fields: ["repository_name"], empty: "No repository" },
      ...single([["provider", "Provider"], ["model", "Model"], ["workspace_name", "Workspace"], ["channel", "Channel"], ["machine_id", "Machine"], ["execution_machine_id", "Execution machine"]]),
    },
    records: { label: "Agent hour", plural: "Agent hours", note: "A record is one local hour of one agent at one level of agents active at once." },
    units: {
      agent: { label: "Agent", plural: "Agents", key: "unit_id", where: [{ field: "unit_id", op: "is_not_null", value: "" }],
        breakdowns: { repository_name: ["repository_name"], provider: ["provider"], workspace_name: ["workspace_name"], ...origin },
        note: "An agent is one workspace, or one top-level session, by your counting setting; its repository and origin are constant." },
    },
    cycle: { hour: { kind: "timestamp", field: "started_at" }, weekday: { kind: "timestamp", field: "started_at" } },
    periods: {}, timestamp: "started_at",
    request: settings ? { where, note: `Records count ${settings.counting === "agent" ? "top-level sessions" : "workspaces"} and join turns across gaps up to ${settings.gap} minutes: your saved Parallelism settings, with which the service builds them.` } : undefined,
    trend: { metric: "agent_hours", split: "level_band" },
    scatter: ["agent_hours", "active_hours"],
  };
  catalog.breakdowns.provider.order = ["claude", "antigravity", "codex", "chatgpt", "tl1", "canonical"];
  return catalog;
}
export const parallelismCatalogShape = parallelismAnalysisCatalog();
