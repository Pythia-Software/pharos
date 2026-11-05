import type { AnalysisCatalog, AnalysisMeasure } from "./dashboard-analysis-model";
import { projectBreakdown, projectLegacy } from "./project-analysis";

// Habits over the rhythm dataset: one record is one local hour of one actor in
// one top-level conversation. You are your engaged time, estimated from the
// messages you typed; agents are Parallelism's turns, split by who started
// them and whether you were engaged (internal/archive/rhythm.go). Wall time
// shares each hour among what was active at once, so groups add up.

const hours = { kind: "number", decimals: 1 } as const, ratio = { kind: "number", decimals: 2 } as const, count = { kind: "number", decimals: 0 } as const;
const measures: Record<string, AnalysisMeasure> = {
  wall_hours: { label: "Wall hours", unit: "hours", format: hours, op: "sum", field: "elapsed_hours", value: "elapsed_hours", additive: true,
    note: "Wall hours are clock time: each record holds its share of the hour, one over the agents (or, for you, conversations) active at once, so groups add up without counting an hour twice. Your time and your agents' overlap: add them only within one." },
  active_hours: { label: "Active hours", unit: "hours", format: hours, op: "sum", field: "active_hours", value: "active_hours", additive: true,
    note: "Active hours count each agent's own time, so two agents working through one hour are two hours." },
  parallel_hours: { label: "Hours with two or more agents", unit: "hours", format: hours, op: "sum", field: "parallel_elapsed_hours", value: "parallel_elapsed_hours", additive: true,
    note: "Wall time with two or more agents active at once, shared among them as wall hours are." },
  messages: { label: "Typed messages", unit: "messages", format: count, op: "sum", field: "messages", value: "messages", additive: true,
    note: "Messages with text you typed, counted in the hour you sent them." },
  typed_words: { label: "Typed words", unit: "words", format: count, op: "sum", field: "typed_words", value: "typed_words", additive: true,
    note: "Words you typed, from Human Words' classification." },
  turns: { label: "Agent turns started", unit: "turns", format: count, op: "sum", field: "turns", value: "turns", additive: true,
    note: "Agent turns, each counted in the hour its prompt was sent." },
  at_once: { label: "Agents at once while active", unit: "agents", format: ratio, op: "sum", ratio: ["active_ms", "elapsed_ms"], additive: false,
    note: "Active time over wall time, pooled: the time-weighted number of agents working together while any is. For you it is always one." },
  conversations: { label: "Conversations", unit: "conversations", format: count, op: "count_distinct", field: "unit_id", additive: false,
    note: "Distinct top-level conversations active in each group. One active in several groups counts in each." },
};

const single = (entries: Array<[string, string]>): AnalysisCatalog["breakdowns"] => Object.fromEntries(entries.map(([field, label]) => [field, { label, fields: [field] }]));
export const actorLabels: Record<string, string> = { you: "You", with_you: "Agents you started, with you", unattended: "Agents you started, unattended", automated: "Automatic agent runs" };
export const initiatorLabels: Record<string, string> = { human: "You", scheduled: "Schedule", scripted: "Script", orchestrated: "Orchestration", agent: "Another agent", delegated: "Sub-agent", unknown: "Unknown" };

export const habitsAnalysisCatalog: AnalysisCatalog = {
  dataset: "rhythm", time: { first: "started_at", last: "started_at" }, measures,
  breakdowns: {
    actor: { label: "Actor", fields: ["actor"], order: ["you", "with_you", "unattended", "automated"], values: actorLabels },
    initiator: { label: "Started by", fields: ["initiator"], order: ["human", "scheduled", "scripted", "orchestrated", "agent", "delegated", "unknown"], values: initiatorLabels },
    automation: { label: "Automation", fields: ["automation"], empty: "Not automated" },
    project_name: projectBreakdown(),
    ...single([["provider", "Provider"], ["model", "Model"], ["workspace_name", "Workspace"], ["channel", "Channel"], ["machine_id", "Machine"], ["execution_machine_id", "Execution machine"]]),
  },
  records: { label: "Actor hour", plural: "Actor hours", note: "A record is one local hour of one actor (you, or agents with you, unattended or automated) in one top-level conversation." },
  units: {
    conversation: { label: "Conversation", plural: "Conversations", key: "unit_id", where: [{ field: "unit_id", op: "is_not_null", value: "" }],
      breakdowns: { project_name: ["project_name"], provider: ["provider"], model: ["model"], workspace_name: ["workspace_name"], channel: ["channel"], machine_id: ["machine_id"], execution_machine_id: ["execution_machine_id"] },
      note: "A conversation is one top-level conversation, with its continuations and delegated runs; its project, provider and origin are constant." },
  },
  cycle: { hour: { kind: "timestamp", field: "started_at" }, weekday: { kind: "timestamp", field: "started_at" } },
  periods: { day: "day", week: "week", month: "month" }, timestamp: "started_at",
  trend: { metric: "wall_hours", split: "actor" }, legacy: projectLegacy(),
  scatter: ["wall_hours", "turns"],
};
