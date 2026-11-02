import type { AnalysisCatalog } from "./dashboard-analysis-model";

const number = { kind: "number", decimals: 0 } as const;
const breakdowns = {
  provider: { label: "Top-level provider", fields: ["root_provider"] },
  model_family: { label: "Top-level model", fields: ["root_model_family"] },
  provider_model: { label: "Top-level provider + model", fields: ["root_provider", "root_model_family"] },
  effort: { label: "Top-level effort", fields: ["root_effort"] },
  repository_name: { label: "Repository", fields: ["repository_name"] },
};
// Conversation activity keeps estimated supplied text and recorded output in
// their original units. It must not be projected onto classified message words.
export const conversationAnalysisCatalog: AnalysisCatalog = {
  dataset: "conversation_activity", time: { first: "event_at", last: "event_at" },
  measures: {
    human_input: { label: "Estimated human text tokens", unit: "tokens", format: number, op: "sum", field: "human_input_tokens_estimate", value: "human_input_tokens_estimate", additive: true, note: "Estimated tokens of human-supplied text, including pasted material; these are not authored word counts." },
    output: { label: "Recorded output tokens", unit: "tokens", format: number, op: "sum", field: "output_tokens", value: "output_tokens", additive: true, note: "Recorded model output attributed to each conversation." },
    messages: { label: "Human messages", unit: "messages", format: number, op: "sum", field: "human_messages", value: "human_messages", additive: true, note: "Human messages recorded in conversation activity." },
  },
  breakdowns,
  records: { label: "Activity record", plural: "Activity records", note: "One recorded conversation activity event." },
  units: { conversation: { label: "Whole conversation", plural: "Whole conversations", key: "root_conversation_id", where: [{ field: "root_conversation_id", op: "is_not_null", value: "" }, { field: "root_conversation_id", op: "!=", value: "" }],
    // Repository belongs to the event's workspace; delegation can cross
    // repositories within one whole conversation.
    breakdowns: { provider: breakdowns.provider.fields, model_family: breakdowns.model_family.fields, provider_model: breakdowns.provider_model.fields, effort: breakdowns.effort.fields }, note: "A top-level conversation includes its attributed delegated activity. Only activity in the selected range counts." } },
  periods: {}, timestamp: "event_at",
  cycle: { hour: { kind: "timestamp", field: "event_at" }, weekday: { kind: "timestamp", field: "event_at" } },
  trend: { metric: "human_input", split: "provider_model" }, scatter: ["human_input", "output"],
};
