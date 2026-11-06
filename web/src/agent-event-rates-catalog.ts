import type { WhereTerm } from "@pythia-software/query-table-core";
import type { AnalysisCatalog, AnalysisMeasure, RatioPart } from "./dashboard-analysis-model";
import { agentEventGroups, agentEventKindLabel, agentEventKinds } from "./agent-events-analysis-catalog";

// Agent Events: how often agents run into each kind of event, over time and
// across breakdowns, over agent_event_rates. One record is one agent message
// the events pass read, crossed with one reported kind, holding that kind's
// events in the message (usually none). Every message read is in every kind,
// so each rate's denominator is the distinct messages, turns or conversations
// the pass read under the filters, whichever kinds are chosen or broken down;
// the numerator counts only the chosen kinds. The pass reads agent replies
// and thinking (agent_event_messages), never all messages.
//
// Rates are descriptive: they say how often agents wrote such sentences, not
// how well anyone prompted or worked.

const percent = { kind: "percent", decimals: 1 } as const;
const withEvent = "[events]>0";
const distinct = (field: string, label: string, when?: string): RatioPart => ({ op: "count_distinct", field, label, ...when ? { when } : {} });
// What a drill-down opens: the messages with a matching event, each with a
// link into the reader with its events marked.
const drill: WhereTerm[] = [{ field: "events", op: ">", value: "0" }];
const shareOf = (field: string, unit: string, label: string, note: string): AnalysisMeasure => ({
  label, unit: "percent", format: percent, op: "sum", additive: false, drill, note,
  ratio: [distinct(field, `${unit} with a matching event`, withEvent), distinct(field, `${unit} read`)],
});

const measures: Record<string, AnalysisMeasure> = {
  per_thousand: { label: "Events per 1,000 messages read", unit: "events per 1,000 messages", format: { kind: "number", decimals: 0 }, op: "sum", additive: false, drill,
    ratio: ["events", { op: "count_distinct", field: "message_id", per: 1000, label: "thousands of messages read" }],
    note: "Matching events divided by the agent messages the events pass read (replies and thinking blocks), per 1,000. A message can hold several events." },
  message_share: shareOf("message_id", "messages", "Share of messages read with an event",
    "Agent messages with at least one matching event, divided by the agent messages the pass read."),
  turn_share: shareOf("turn_id", "turns", "Share of turns with an event",
    "Turns with at least one matching event in a message read, divided by the turns with a message read. A turn is a prompt and what followed it; a turn counts in every group it has messages in."),
  conversation_share: shareOf("conversation_id", "conversations", "Share of conversations with an event",
    "Conversations with at least one matching event, divided by the conversations with a message read. A conversation counts in every group it has messages in; a delegated subagent is a conversation of its own."),
  events: { label: "Events", unit: "events", format: { kind: "number", decimals: 0 }, op: "sum", field: "events", additive: true, drill,
    note: "Sentences the model judged to express a kind of event, at or above its confidence threshold. Counts follow how much agents worked; the rates divide by what the pass read." },
  messages: { label: "Messages read", unit: "messages", format: { kind: "number", decimals: 0 }, op: "count_distinct", field: "message_id", additive: false,
    note: "Agent messages the events pass read: the denominator of the per-message rates. A kind filter does not change it." },
};

const sourceLabel = (value: string) => value === "thinking" ? "Thinking" : value === "reply" ? "Visible reply" : value;
const promptedLabels: Record<string, string> = { person: "A person", automated: "An automation", agent: "A delegating agent", unclassified: "Not yet classified", none: "No prompt recorded" };
const lengthLabels: Record<string, string> = { "1-10": "1–10 words", "11-30": "11–30 words", "31-100": "31–100 words", "101-300": "101–300 words", "300+": "Over 300 words" };
export const agentEventPromptedOrder = Object.keys(promptedLabels);
export const agentEventLengthOrder = Object.keys(lengthLabels);
const person: WhereTerm[] = [{ field: "prompted_by", op: "=", value: "person" }];

export const agentEventRatesCatalog: AnalysisCatalog = {
  dataset: "agent_event_rates", views: ["trend", "compare"], time: { first: "occurred_at", last: "occurred_at" }, measures,
  breakdowns: {
    family_group: { label: "Kind group", fields: ["family_group"], order: [...agentEventGroups] },
    family: { label: "Kind", fields: ["family"], order: agentEventKinds.map(kind => kind.key) },
    source: { label: "Reply or thinking", fields: ["source"], order: ["reply", "thinking"] },
    prompted_by: { label: "Who prompted", fields: ["prompted_by"], order: agentEventPromptedOrder },
    prompt_length: { label: "Prompt length", fields: ["prompt_length"], order: agentEventLengthOrder, where: person,
      note: "Prompt length counts the words a person typed in the prompt that started the turn, so it covers turns a person prompted only." },
    agent_role: { label: "Main agent or subagent", fields: ["agent_role"], order: ["main", "subagent"], values: { main: "Main agent", subagent: "Subagent" } },
    model: { label: "Model", fields: ["model"], empty: "Unknown model" },
    model_effort: { label: "Model and effort", fields: ["model_effort"] },
    effort: { label: "Effort", fields: ["effort"], empty: "Effort not recorded" },
    provider: { label: "Provider", fields: ["provider"], order: ["claude", "codex", "antigravity", "chatgpt", "tl1", "canonical"] },
    harness: { label: "Harness", fields: ["harness"], empty: "Harness not recorded" },
    repository_name: { label: "Repository", fields: ["repository_name"], empty: "No repository" },
    week: { label: "Week", fields: ["week"], empty: "No time recorded" },
    family_source: { label: "Kind + reply or thinking", fields: ["family", "source"] },
    group_source: { label: "Kind group + reply or thinking", fields: ["family_group", "source"] },
    model_source: { label: "Model + reply or thinking", fields: ["model", "source"] },
    family_model: { label: "Kind + model", fields: ["family", "model"] },
    group_role: { label: "Kind group + main agent or subagent", fields: ["family_group", "agent_role"] },
    family_role: { label: "Kind + main agent or subagent", fields: ["family", "agent_role"] },
    group_repository: { label: "Kind group + repository", fields: ["family_group", "repository_name"] },
  },
  labels: {
    family: agentEventKindLabel, source: sourceLabel,
    prompted_by: value => promptedLabels[value] ?? value, prompt_length: value => lengthLabels[value] ?? value,
    agent_role: value => value === "subagent" ? "Subagent" : value === "main" ? "Main agent" : value,
  },
  records: { label: "Message read × kind", plural: "Messages read × kinds",
    note: "One record is one agent message the events pass read, once for each kind of event, with that kind's events in it (usually none). Rates divide by distinct messages, turns or conversations, so each counts once whichever kinds are chosen." },
  units: {},
  // A compare's minimum counts messages read, not message × kind records.
  samples: { unit: "messages read", label: "Messages read", distinct: "message_id" },
  periods: { day: "day", week: "week", month: "month" }, timestamp: "occurred_at",
  trend: { metric: "per_thousand", split: "family_group" },
  scatter: ["per_thousand", "message_share"],
};
