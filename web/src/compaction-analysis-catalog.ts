import type { AnalysisCatalog } from "./dashboard-analysis-model";
import { originLabel } from "./origin-dimensions";

// One boundary is one observation, even without a following model request.
export const compactionAnalysisCatalog: AnalysisCatalog = {
  dataset: "compactions", time: { first: "occurred_at", last: "occurred_at" },
  records: { label: "Compaction", plural: "Compactions", note: "One recorded context compaction boundary. Replayed Codex boundaries, mirrored transcripts and inactive conversation versions are excluded." },
  measures: {
    compactions: { label: "Compactions", unit: "compactions", format: { kind: "number", decimals: 0 }, op: "count", additive: true,
      note: "Counts boundary events, including consecutive compactions and boundaries without later usage. Human means manual; machine means automatic. Claude records the trigger; Codex infers it from turn position. Unknown triggers remain separate." },
    conversations: { label: "Conversations with compactions", unit: "conversations", format: { kind: "number", decimals: 0 }, op: "count_distinct", field: "conversation_id", additive: false,
      note: "Distinct recorded conversations with a compaction. A conversation may appear under several triggers, so groups do not add up to the total." },
  },
  breakdowns: {
    trigger: { label: "Trigger", fields: ["trigger"], order: ["human", "machine", "unknown"] },
    provider: { label: "Service", fields: ["provider"], empty: "Unknown service" },
    trigger_provider: { label: "Trigger + service", fields: ["trigger", "provider"] },
    harness: { label: "Agent / harness", fields: ["harness"], empty: "Unknown harness" },
    model: { label: "Model", fields: ["model"], empty: "Unknown model" },
    repository_name: { label: "Repository", fields: ["repository_name"], empty: "No repository" },
    channel: { label: "Channel", fields: ["channel"], empty: "Unknown channel" },
    machine_id: { label: "Machine", fields: ["machine_id"], empty: "Unknown machine" },
    execution_machine_id: { label: "Execution machine", fields: ["execution_machine_id"], empty: "Unknown machine" },
    trigger_evidence: { label: "Trigger evidence", fields: ["trigger_evidence"], order: ["recorded", "inferred", "unknown"] },
  },
  labels: {
    trigger: value => ({ human: "Human triggered", machine: "Machine triggered", unknown: "Unknown trigger" })[value] ?? value,
    trigger_evidence: value => ({ recorded: "Recorded", inferred: "Inferred", unknown: "Unknown" })[value] ?? value,
    channel: value => originLabel("channel", value), machine_id: value => originLabel("machine_id", value), execution_machine_id: value => originLabel("execution_machine_id", value),
  },
  units: {}, periods: { day: "day", week: "week", month: "month" }, timestamp: "occurred_at",
  cycle: { hour: { kind: "timestamp", field: "occurred_at" }, weekday: { kind: "timestamp", field: "occurred_at" } },
  trend: { metric: "compactions", split: "trigger" }, scatter: ["compactions", "conversations"],
  views: ["trend", "trend_line", "trend_bar", "compare", "cycle"],
};
