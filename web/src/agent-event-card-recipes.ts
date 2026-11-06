import type { AggregationClause, WhereTerm } from "@pythia-software/query-table-core";
import type { QueryRecipe } from "./curiosity-data";

// Agent Events cards: each opens the Agent Events dashboard
// (agent-event-rates-catalog.ts) at the view its cover draws, over the
// messages the events pass read. They describe how often agents wrote such
// sentences; none of them scores a person, a prompt or a model.

const base = { dataset: "agent_event_rates", field: "occurred_at", category: "agent_events", idea: "conversation-01" } as const;
const days = [7, 30, 90];
// The dashboard's compare minimum, in messages read, so the cover hides the
// same small groups.
const floor = { min: 200, distinct: "message_id" } as const;
const perThousand = "SUM([events])/NULLIF(COUNT_DISTINCT([message_id])/1000,0)";
const shareOf = (field: string) => `COUNT_DISTINCT(IF([events]>0,[${field}],NULL))/NULLIF(COUNT_DISTINCT([${field}]),0)`;
const rate = (id: string, groupBy: string[], label: string, extra: Partial<AggregationClause> = {}): AggregationClause => ({
  id, op: "sum", expression: perThousand, groupBy, groupLimit: 200, label, display: { kind: "bar-horizontal", format: { kind: "number", decimals: 0 } }, ...extra,
});
const share = (id: string, field: string, groupBy: string[], label: string): AggregationClause => ({
  id, op: "sum", expression: shareOf(field), groupBy, groupLimit: 200, label, minCount: floor, display: { kind: "bar-horizontal", format: { kind: "percent", decimals: 1 } },
});
const bars = (metricId: string, maxGroups = 12) => ({ mode: "source" as const, metricId, kind: "bar-horizontal" as const, maxGroups, showOther: false, showLegend: false, showHeadline: false, height: 300 });
const opus: WhereTerm = { field: "model", op: "=", value: "claude-opus-5-5" };
const friction: WhereTerm = { field: "family_group", op: "=", value: "Friction" };
const read = "Rates divide by the agent replies and thinking blocks the events pass read under the same filters, not by all messages. Opening the dashboard does not run the pass.";
const empty = "No agent messages read for events in this window. Turn on Agent Experience in Settings → Background Processing to run the events pass.";

export const agentEventCardRecipes: QueryRecipe[] = [
  { ...base, days, id: "agent-events-thinking-vs-reply", title: "Thinking says more than replies", unit: "events per 1,000 messages",
    description: "Events per 1,000 Opus messages read over the last {days} days, thinking blocks beside visible replies.", where: [opus],
    measures: [rate("events_by_source", ["source"], "Events per 1,000 messages read", { minCount: floor })], cover: bars("events_by_source"),
    action: "Compare thinking and replies", emptyLabel: empty,
    note: `Limited to Opus, whose thinking is nearly all of the thinking text read, so both bars come from one model. Thinking narrates what a reply leaves out; a higher rate there is not more trouble. ${read}` },
  { ...base, days, id: "agent-events-groups-by-source", title: "What thinking narrates", unit: "events per 1,000 messages",
    description: "Events per 1,000 messages read over the last {days} days, by kind group, in thinking and in visible replies.",
    measures: [rate("groups_by_source", ["family_group", "source"], "Events per 1,000 messages read", { minCount: floor })], cover: bars("groups_by_source", 8),
    action: "Compare kind groups", emptyLabel: empty,
    note: `Each bar's denominator is every message read in thinking or in replies, so the kind groups of one side add up to its total rate. ${read}` },
  { ...base, days, id: "agent-events-delegated-turns", title: "Turns with an event, by who prompted", unit: "turns",
    description: "Share of turns over the last {days} days with at least one event in a message read, by who started the turn.",
    measures: [share("turns_by_prompter", "turn_id", ["prompted_by"], "Share of turns with an event")], cover: bars("turns_by_prompter"),
    action: "Compare who prompted", emptyLabel: empty,
    note: `A delegated turn is a whole subagent run, usually far longer than a turn a person typed, and a longer turn has more chances to hold an event. This compares turns as they are, not prompts. ${read}` },
  { ...base, days, id: "agent-events-models-by-source", title: "Events by model, replies and thinking", unit: "events per 1,000 messages",
    description: "Events per 1,000 messages read over the last {days} days, by model and by reply or thinking.",
    measures: [rate("models_by_source", ["model", "source"], "Events per 1,000 messages read", { minCount: floor })], cover: bars("models_by_source"),
    action: "Compare models", emptyLabel: empty,
    note: `Models differ in how much they narrate and in how much thinking their harness records, so these rates describe what each model wrote, not how well it worked. ${read}` },
  { ...base, days, id: "agent-events-repository-friction", title: "Friction by repository", unit: "events per 1,000 messages",
    description: "Friction events per 1,000 messages read over the last {days} days, by repository.", where: [friction],
    measures: [rate("friction_by_repository", ["repository_name"], "Friction events per 1,000 messages read", { minCount: floor })], cover: bars("friction_by_repository"),
    action: "Compare repositories", emptyLabel: empty,
    note: `Friction is obstacles, environment friction, out-of-date guidance, pre-existing failures, missing capabilities, unclear instructions, getting stuck and giving up. The denominator is every message read in the repository, so a kind filter does not shrink it. ${read}` },
  { ...base, days, id: "agent-events-ambiguity-by-length", title: "Unclear instructions by prompt length", unit: "turns",
    description: "Share of person-prompted turns over the last {days} days in which the agent said the instructions were unclear or conflicting, by prompt length.",
    where: [{ field: "family", op: "=", value: "ambiguous_request" }, { field: "prompted_by", op: "=", value: "person" }],
    measures: [share("ambiguity_by_length", "turn_id", ["prompt_length"], "Share of turns with unclear instructions")], cover: bars("ambiguity_by_length"),
    action: "Compare prompt lengths", emptyLabel: empty,
    note: `Descriptive only: a long prompt often describes a larger task, and an agent finds more to call unclear when there is more to read. Counts are small; open a bar to read the events behind it. ${read}` },
  { ...base, days, id: "agent-events-subagent-kinds", title: "What subagents and main agents report", unit: "events per 1,000 messages",
    description: "Events per 1,000 messages read over the last {days} days, by kind, for main agents and subagents.",
    measures: [rate("kinds_by_role", ["family", "agent_role"], "Events per 1,000 messages read", { minCount: floor })], cover: bars("kinds_by_role", 12),
    action: "Compare subagents", emptyLabel: empty,
    note: `A subagent is a conversation an agent delegated to. Each side's denominator is its own messages read, so the kinds compare as rates rather than counts. ${read}` },
  { ...base, id: "agent-events-friction-trend", title: "Friction day by day", unit: "events per 1,000 messages", days: [30, 90],
    description: "Friction events per 1,000 messages read each day over the last {days} days, by kind.", where: [friction],
    measures: [{ id: "friction_daily", op: "sum", expression: perThousand, groupBy: ["day", "family"], groupLimit: 10000, label: "Friction events per 1,000 messages read" }],
    cover: { mode: "source", metricId: "friction_daily", kind: "auto", maxGroups: 8, showOther: false, showLegend: true, showHeadline: false, height: 300 },
    action: "Explore friction over time", emptyLabel: empty,
    note: `Each day divides its events by that day's messages read. Days with few messages read swing widely. ${read}` },
];
