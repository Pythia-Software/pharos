import type { WhereTerm } from "@pythia-software/query-table-core";
import type { AnalysisCatalog, AnalysisMeasure } from "./dashboard-analysis-model";

// Agent Experience over agent_events: one record is one event, a sentence in
// an agent's visible reply or thinking that a model judged to express one kind
// of event (an obstacle, a discovery, a workaround…) at or above its fixed
// confidence threshold. Events of one message or conversation group into
// message and conversation units. Rates per message read agent_event_messages,
// every agent message the pass read (see the dashboard's summary).

const count = { kind: "number", decimals: 0 } as const, percent = { kind: "percent", decimals: 1 } as const;
const present = (field: string): WhereTerm[] => [{ field, op: "is_not_null", value: "" }, { field, op: "!=", value: "" }];

// The kinds in the order the dashboard groups them, with their labels.
export const agentEventGroups = ["Friction", "Reactions and learning", "Worth a second look", "Choices you may not see"] as const;
export const agentEventKinds: Array<{ key: string; label: string; group: typeof agentEventGroups[number] }> = [
  { key: "obstacle", label: "Obstacle or roadblock", group: "Friction" },
  { key: "env_friction", label: "Environment friction", group: "Friction" },
  { key: "stale_guidance", label: "Out-of-date docs or instructions", group: "Friction" },
  { key: "preexisting_failure", label: "Pre-existing or flaky failure", group: "Friction" },
  { key: "missing_capability", label: "Missing tool or capability", group: "Friction" },
  { key: "ambiguous_request", label: "Unclear or conflicting instructions", group: "Friction" },
  { key: "stuck", label: "Stuck", group: "Friction" },
  { key: "gave_up", label: "Giving up or handing back", group: "Friction" },
  { key: "discovery", label: "Discovery", group: "Reactions and learning" },
  { key: "confusion", label: "Confusion", group: "Reactions and learning" },
  { key: "surprise", label: "Surprise", group: "Reactions and learning" },
  { key: "own_mistake", label: "Own mistake or correction", group: "Reactions and learning" },
  { key: "unblocking_insight", label: "Lesson that unblocked progress", group: "Reactions and learning" },
  { key: "circumventing_constraint", label: "Getting around an instruction or safeguard", group: "Worth a second look" },
  { key: "risk_flagged", label: "Risk raised", group: "Choices you may not see" },
  { key: "deferred", label: "Deferred or skipped work", group: "Choices you may not see" },
  { key: "workaround", label: "Workaround", group: "Choices you may not see" },
  { key: "unverified", label: "Unchecked claim", group: "Choices you may not see" },
  { key: "assumption", label: "Assumption", group: "Choices you may not see" },
  { key: "tech_debt", label: "Technical debt noticed", group: "Choices you may not see" },
  { key: "pushback", label: "Disagreeing with the user", group: "Choices you may not see" },
];
const kindLabel = new Map(agentEventKinds.map(kind => [kind.key, kind.label]));
export const agentEventKindLabel = (key: string) => kindLabel.get(key) ?? key;

const measures: Record<string, AnalysisMeasure> = {
  events: { label: "Events", unit: "events", format: count, op: "count", additive: true, note: "Sentences the model judged to express a kind of event, at or above its confidence threshold. A sentence can count under more than one kind." },
  conversations: { label: "Conversations with events", unit: "conversations", format: count, op: "count_distinct", field: "conversation_id", additive: false, note: "Distinct agent conversations with a matching event; a conversation can appear in several groups." },
  messages: { label: "Messages with events", unit: "messages", format: count, op: "count_distinct", field: "message_id", additive: false, note: "Distinct agent messages (visible replies or thinking blocks) with a matching event." },
  repositories: { label: "Repositories with events", unit: "repositories", format: count, op: "count_distinct", field: "repository_name", additive: false, note: "Distinct repositories with a matching event." },
  thinking_share: { label: "Share in thinking", unit: "percent", format: percent, op: "sum", ratio: [{ op: "sum", field: "in_thinking", label: "events in thinking" }, { op: "count", field: "id", label: "events" }], additive: false,
    note: "Events found in agents' private reasoning, divided by all matching events. Visible replies are the rest." },
  explained_share: { label: "Share with a stated cause", unit: "percent", format: percent, op: "sum", ratio: [{ op: "sum", expression: "IF([has_subject],1,0)", label: "events whose cause is another sentence" }, { op: "count", field: "id", label: "events" }], additive: false,
    note: "Events whose cause the model found in a sentence other than the event's own, divided by all matching events." },
};

export const agentEventsAnalysisCatalog: AnalysisCatalog = {
  dataset: "agent_events", time: { first: "occurred_at", last: "occurred_at" }, measures,
  breakdowns: {
    family_group: { label: "Kind group", fields: ["family_group"], order: [...agentEventGroups] },
    family: { label: "Kind", fields: ["family"], order: agentEventKinds.map(kind => kind.key) },
    source: { label: "Reply or thinking", fields: ["source"], order: ["reply", "thinking"] },
    family_source: { label: "Kind + reply or thinking", fields: ["family", "source"] },
    repository_name: { label: "Repository", fields: ["repository_name"], empty: "No repository" },
    family_repository: { label: "Kind + repository", fields: ["family", "repository_name"] },
    provider: { label: "Provider", fields: ["provider"], order: ["claude", "antigravity", "codex", "chatgpt", "tl1", "canonical"] },
    model: { label: "Model", fields: ["model"], empty: "Unknown model" },
    title: { label: "Conversation", fields: ["title"] },
  },
  labels: {
    family: agentEventKindLabel,
    source: value => value === "thinking" ? "Thinking" : value === "reply" ? "Visible reply" : value,
  },
  records: { label: "Event", plural: "Events", note: "One record is one sentence judged to express one kind of event. Events are placed at their message's time." },
  units: {
    message: { label: "Agent message", plural: "Agent messages", key: "message_id", where: present("message_id"), breakdowns: { source: ["source"], repository_name: ["repository_name"], provider: ["provider"] },
      note: "An agent message is one visible reply or one thinking block; its events of a kind form one observation." },
    conversation: { label: "Agent conversation", plural: "Agent conversations", key: "conversation_id", where: present("conversation_id"), breakdowns: { repository_name: ["repository_name"], provider: ["provider"] },
      note: "An agent conversation is one recorded agent transcript; delegated subagents are conversations of their own." },
  },
  cycle: { hour: { kind: "timestamp", field: "occurred_at" }, weekday: { kind: "timestamp", field: "occurred_at" } },
  periods: { day: "day", week: "week", month: "month" }, timestamp: "occurred_at",
  trend: { metric: "events", split: "family_group" },
  scatter: ["events", "conversations"],
};
