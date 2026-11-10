import {peopleLabelBreakdowns} from "./people-message-labels";
import type { AnalysisCatalog } from "./dashboard-analysis-model";
import { projectBreakdown, projectLegacy } from "./project-analysis";

const number = { kind: "number", decimals: 0 } as const;
const breakdowns = {
  ...peopleLabelBreakdowns("conversation_activity"),
  provider: { label: "Top-level provider", fields: ["root_provider"] },
  model_family: { label: "Top-level model", fields: ["root_model_family"] },
  provider_model: { label: "Top-level provider + model", fields: ["root_provider", "root_model_family"] },
  effort: { label: "Top-level effort", fields: ["root_effort"] },
  project_name: projectBreakdown(),
};
// Conversations over conversation_activity: human messages and recorded
// output are separate events, each attributed to its top-level conversation.
export const conversationAnalysisCatalog: AnalysisCatalog = {
  dataset: "conversation_activity", time: { first: "event_at", last: "event_at" },
  measures: {
    conversations: { label: "Conversations", unit: "conversations", format: number, op: "count_distinct", field: "root_conversation_id", additive: false, note: "Distinct top-level conversations with a human message or recorded output in the range, including their delegated activity. A conversation in several groups counts once in each, so groups do not add up to the total." },
    messages: { label: "Human messages", unit: "messages", format: number, op: "sum", field: "human_messages", value: "human_messages", additive: true, note: "Classified user messages holding any text besides app instructions and automated prompts." },
    output: { label: "Recorded output tokens", unit: "tokens", format: number, op: "sum", field: "output_tokens", value: "output_tokens", additive: true, note: "Recorded model output attributed to each conversation." },
  },
  breakdowns,
  records: { label: "Activity record", plural: "Activity records", note: "One recorded conversation activity event." },
  units: { conversation: { label: "Whole conversation", plural: "Whole conversations", key: "root_conversation_id", where: [{ field: "root_conversation_id", op: "is_not_null", value: "" }, { field: "root_conversation_id", op: "!=", value: "" }],
    // Project belongs to the event's workspace; delegation can cross
    // projects within one whole conversation.
    breakdowns: { provider: breakdowns.provider.fields, model_family: breakdowns.model_family.fields, provider_model: breakdowns.provider_model.fields, effort: breakdowns.effort.fields }, note: "A top-level conversation includes its attributed delegated activity. Only activity in the selected range counts." } },
  periods: {}, timestamp: "event_at",
  cycle: { hour: { kind: "timestamp", field: "event_at" }, weekday: { kind: "timestamp", field: "event_at" } },
  trend: { metric: "conversations", split: "provider_model" }, scatter: ["messages", "output"], legacy: projectLegacy(),
};
