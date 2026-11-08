import { scatterMetrics } from "./scatter-metrics";
import type { AnalysisCatalog, AnalysisMeasure } from "./dashboard-analysis-model";

const breakdowns = { provider: { label: "Provider", fields: ["provider"] }, repository_name: { label: "Repository", fields: ["repository_name"] }, model: { label: "Model", fields: ["model"] } };
export const scatterAnalysisCatalog: AnalysisCatalog = {
  dataset: "scatter", views: ["scatter"], time: { first: "first_at", last: "last_at" },
  measures: Object.fromEntries(scatterMetrics.map(item => [item.id, {
    label: item.measure.label, unit: item.measure.unit, format: item.measure.format, note: item.measure.note,
    op: "max", field: item.id, value: item.id, additive: false, group: item.group, units: Object.keys(item.keys),
  } satisfies AnalysisMeasure])),
  breakdowns,
  units: Object.fromEntries(([["conversation", "Conversation", "Conversations"], ["work", "Workspace", "Workspaces"], ["agent_session", "Agent session", "Agent sessions"], ["message", "Message", "Messages"]] as const).map(([key, label, plural]) => [key, {
    label, plural, key: "entity_id", where: [{ field: "point_kind", op: "=", value: key }],
    breakdowns: Object.fromEntries(Object.entries(breakdowns).filter(([id]) => key !== "work" || id === "repository_name").map(([id, entry]) => [id, entry.fields])),
    note: key === "message" ? "Message points use only measurements recorded for that message; conversation costs and session durations are not assigned to individual messages."
      : key === "work" ? "A workspace pools records with the same recorded workspace identity."
      : key === "agent_session" ? "One recorded agent session. Words, usage and ledger records belong to their recorded owner. Session Shape covers a conversation's own main stream; embedded helpers without their own transcript have no shape measurements."
      : "A conversation is one recorded transcript. Delegated transcripts are separate points; hourly activity metrics describe top-level conversations and their activity.",
  }])),
  records: { label: "Point", plural: "Points", note: "One matched entity and its independently measured values." },
  periods: {}, trend: { metric: "human_typed", split: "none" }, scatter: ["human_typed", "tokens_cost"],
  rangeNote: "Each metric is computed independently from its source records in the range, then matched by entity identity. Points need both values; missing measurements stay missing. Session metrics describe sessions that overlap the range and retain their complete session values.",
};
