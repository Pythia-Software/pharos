import type { WhereTerm } from "@pythia-software/query-table-core";
import type { AnalysisCatalog, AnalysisMeasure } from "./dashboard-analysis-model";

// Tokens and Cost over the usage ledger. One record is one agent session's
// usage of one model and effort on one recorded day.

const tokens = { kind: "number", decimals: 0 } as const, usd = { kind: "number", decimals: 2 } as const, percent = { kind: "percent", decimals: 1 } as const;
const priced: WhereTerm[] = [{ field: "price_status", op: "=", value: "priced" }];
const measures: Record<string, AnalysisMeasure> = {
  tokens: { label: "Tokens", unit: "tokens", format: tokens, op: "sum", field: "total_tokens", value: "total_tokens", additive: true, note: "Tokens include cached input and reported reasoning output." },
  cost: { label: "API cost (USD)", unit: "USD", format: usd, op: "sum", field: "cost_usd", value: "cost_usd", additive: true, note: "API cost is the list-price equivalent, including assumed and partial prices; unpriced usage has no cost and adds nothing." },
  priced_cost: { label: "Fully priced API cost (USD)", unit: "USD", format: usd, op: "sum", field: "cost_usd", value: "cost_usd", additive: true, where: priced, base: "cost", note: "Only fully priced usage (price_status=priced) is included; assumed, partial and unpriced usage is left out of every measure in this view." },
  input: { label: "Input tokens", unit: "tokens", format: tokens, op: "sum", field: "input_tokens", value: "input_tokens", additive: true, note: "Input includes cache reads and cache writes." },
  output: { label: "Output tokens", unit: "tokens", format: tokens, op: "sum", field: "output_tokens", value: "output_tokens", additive: true, note: "Output includes reported reasoning output." },
  cache_read_share: { label: "Cache-read share of input", unit: "percent", format: percent, op: "sum", ratio: ["cache_read_input_tokens", "input_tokens"], additive: false, note: "Cache reads divided by all input, cache writes included, weighted by tokens rather than averaging per-record percentages." },
  output_share: { label: "Output share of tokens", unit: "percent", format: percent, op: "sum", ratio: ["output_tokens", "total_tokens"], additive: false, note: "Output tokens divided by all recorded tokens, weighted by tokens." },
  conversations: { label: "Conversations reached", unit: "conversations", format: tokens, op: "count_distinct", field: "conversation_id", additive: false, note: "Distinct conversations with usage. A conversation using several groups counts once in each, so groups do not add up to the total." },
  agent_sessions: { label: "Agent sessions", unit: "sessions", format: tokens, op: "count_distinct", field: "agent_session_id", additive: false, note: "Distinct agent sessions with usage. A session using several groups counts once in each." },
  delegated_runs: { label: "Delegated runs", unit: "runs", format: tokens, op: "count_distinct", field: "delegated_session_id", additive: false, note: "Distinct delegated executions with usage; the top-level run is not one. Handles and child transcripts count once." },
};

const breakdowns: AnalysisCatalog["breakdowns"] = {
  ...Object.fromEntries(([
    ["channel", "Channel"], ["machine_id", "Machine"], ["execution_machine_id", "Execution machine"], ["provider", "Provider"], ["model_family", "Model"],
  ] as const).map(([name, label]) => [name, { label, fields: [name] }])),
  // Cards key models by provider and family: one family can be served by
  // several providers.
  provider_model: { label: "Provider + model", fields: ["provider", "model_family"] },
  ...Object.fromEntries(([
    ["model_effort", "Model + effort"], ["effort", "Effort"], ["repository_name", "Repository"], ["session_kind", "Root agent/subagent"], ["price_status", "Price status"],
  ] as const).map(([name, label]) => [name, { label, fields: [name] }])),
};

const root = { provider: ["root_provider"], model_family: ["root_model_family"], provider_model: ["root_provider", "root_model_family"], effort: ["root_effort"], repository_name: ["root_repository_name"], channel: ["channel"], machine_id: ["machine_id"] };
const present = (name: string): WhereTerm[] => [{ field: name, op: "is_not_null", value: "" }, { field: name, op: "!=", value: "" }];

export const tokenAnalysisCatalog: AnalysisCatalog = {
  dataset: "usage", time: { first: "first_usage_at", last: "last_usage_at" }, measures, breakdowns,
  records: { label: "Usage record", plural: "Usage records", note: "A usage record is one agent session’s usage of one model and effort on one recorded day, so record sizes depend on how long sessions ran." },
  units: {
    conversation: { label: "Conversation", plural: "Conversations", key: "root_conversation_id", where: present("root_conversation_id"), breakdowns: root,
      note: "A conversation is one top-level conversation including its delegated runs, linked launches and continuations, grouped by its top-level provider, model, effort and repository even where delegated runs used others. Usage with unresolved lineage is left out." },
    agent_session: { label: "Agent session", plural: "Agent sessions", key: "agent_session_id", where: present("agent_session_id"),
      breakdowns: { provider: ["provider"], repository_name: ["repository_name"], session_kind: ["session_kind"], channel: ["channel"], machine_id: ["machine_id"], execution_machine_id: ["execution_machine_id"] },
      note: "An agent session is one recorded root agent or subagent session. Model and effort can change within a session, so sessions are not split by them." },
    delegated_run: { label: "Delegated run", plural: "Delegated runs", key: "delegated_session_id", where: [...present("delegated_session_id"), { field: "delegated_run_depth", op: ">", value: "0" }], breakdowns: root,
      note: "A delegated run is one delegated execution below a top-level conversation, nested and linked launches included, grouped by its top-level conversation’s provider, model, effort and repository." },
  },
  cycle: { hour: "hour", weekday: "day" },
  trend: { metrics: { tokens: { measure: "tokens", share: false }, cost: { measure: "cost", share: false }, percent: { measure: "tokens", share: true }, percent_cost: { measure: "cost", share: true } },
    splits: ["type", "channel", "machine_id", "execution_machine_id", "provider", "model_family", "model_effort", "effort", "repository_name", "session_kind"], metric: "tokens", split: "model_family" },
  scatter: ["tokens", "cost"],
};
