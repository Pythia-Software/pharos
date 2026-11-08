import type { AggregationClause, WhereTerm } from "@pythia-software/query-table-core";
import { agentEventWorkTurns } from "./agent-event-rates-catalog";
import type { QueryRecipe } from "./curiosity-data";

// Agent Events cards: each opens the Agent Events dashboard
// (agent-event-rates-catalog.ts) at the view its cover draws, over the
// messages the events pass read. Each points at something in the person's
// own repositories they can change or review (docs, checks, environment, the
// choices agents made), with the events behind every bar a click away; none
// of them scores a person, a prompt or a model. What did not pass that test
// (thinking against replies, models, who prompted, prompt length, subagents)
// is a breakdown with its caveat, not a card: docs/agent-events.md.

const base = { dataset: "agent_event_rates", field: "occurred_at", category: "agent_events", idea: "conversation-01" } as const;
const days = [7, 30, 90];
// The dashboard's compare minimum, in messages read, so the cover hides the
// same small groups.
export const agentEventFloor = { min: 200, distinct: "message_id" } as const;
const floor = agentEventFloor;
// Card covers draw a day-by-day series only from additive sums, so a rate
// over time is the dashboard's trend rather than a card.
export const agentEventsPerThousand = "SUM([events])/NULLIF(COUNT_DISTINCT([message_id])/1000,0)";
const perThousand = agentEventsPerThousand;
const rate = (id: string, groupBy: string[], label: string, extra: Partial<AggregationClause> = {}): AggregationClause => ({
  id, op: "sum", expression: perThousand, groupBy, groupLimit: 200, label, display: { kind: "bar-horizontal", format: { kind: "number", decimals: 1 } }, ...extra,
});
const bars = (metricId: string, maxGroups = 12) => ({ mode: "source" as const, metricId, kind: "bar-horizontal" as const, maxGroups, showOther: false, showLegend: false, showHeadline: false, height: 300 });
export const agentEventFriction: WhereTerm = { field: "family_group", op: "=", value: "Friction" };
const choices: WhereTerm = { field: "family_group", op: "=", value: "Choices you may not see" };
const kind = (value: string): WhereTerm => ({ field: "family", op: "=", value });
const inRepository: WhereTerm = { field: "repository_name", op: "is_not_null", value: "" };
// Plain obstacles are most friction in every repository ("I see the bug"):
// left out, the bars show the kinds a change to the setup can address.
export const agentEventNotObstacle: WhereTerm = { field: "family", op: "!=", value: "obstacle" };
// In thinking, workarounds and assumptions mostly narrate ordinary steps of
// the work; in replies they are what the agent told the person.
const inReplies: WhereTerm = { field: "source", op: "=", value: "reply" };
export const agentEventsRead = "Rates divide by the agent replies and thinking blocks the events pass read under the same filters, not by all messages, in turns a person or a delegating agent started: scripted runs are left out. Opening the dashboard does not run the pass.";
export const agentEventsEmpty = "No agent messages read for events in this window. Turn on Agent Experience in Settings → Background Processing to run the events pass.";

export const agentEventCardRecipes: QueryRecipe[] = [
  { ...base, days, id: "agent-events-repository-friction", title: "Where agents hit friction", unit: "events per 1,000 messages",
    description: "Environment problems, missing tools, out-of-date docs, failures already there and unclear instructions per 1,000 agent messages read over the last {days} days, by repository and kind.", where: [agentEventFriction, agentEventNotObstacle, inRepository, ...agentEventWorkTurns],
    measures: [rate("friction_by_repository", ["repository_name", "family"], "Friction events per 1,000 messages read", { minCount: floor })], cover: bars("friction_by_repository"),
    action: "See where agents struggle", emptyLabel: agentEventsEmpty,
    note: `Each repository's rate divides by its own messages read, so a busy repository does not rank high for volume alone. Plain obstacles, most of the friction everywhere, are left out; the dashboard shows them. Open a bar for the sentences behind it; Fixes groups the recurring ones into changes to docs, checks, environment or tools. ${agentEventsRead}` },
  { ...base, days, id: "agent-events-stale-docs", title: "Docs agents found out of date", unit: "events per 1,000 messages",
    description: "Times per 1,000 agent messages over the last {days} days that an agent said a doc, comment or instruction no longer matched the code, by repository.", where: [kind("stale_guidance"), inRepository, ...agentEventWorkTurns],
    measures: [rate("stale_by_repository", ["repository_name"], "Out-of-date guidance per 1,000 messages read", { minCount: floor })], cover: bars("stale_by_repository"),
    action: "Read what was out of date", emptyLabel: agentEventsEmpty,
    note: `A review or audit that reads docs against the code reports more of these, so read the sentences before the rate: each one names a doc, comment or instruction to fix. Fixes collects the ones named more than once into an update you can hand to an agent. ${agentEventsRead}` },
  { ...base, days, id: "agent-events-known-failures", title: "Failures agents stepped around", unit: "events per 1,000 messages",
    description: "Times per 1,000 agent messages over the last {days} days that an agent met a failure it judged already there, or flaky, and left it, by repository.", where: [kind("preexisting_failure"), inRepository, ...agentEventWorkTurns],
    measures: [rate("failures_by_repository", ["repository_name"], "Pre-existing failures per 1,000 messages read", { minCount: floor })], cover: bars("failures_by_repository"),
    action: "Read the failures", emptyLabel: agentEventsEmpty,
    note: `A failure every agent steps around costs each of them the time to rule it out. Open a bar for the sentences; Fixes lists the checks that keep failing. ${agentEventsRead}` },
  { ...base, days, id: "agent-events-unseen-choices", title: "Choices agents made without asking", unit: "events per 1,000 replies",
    description: "Deferred work, risks, unchecked claims, workarounds and assumptions per 1,000 agent replies over the last {days} days, by repository and kind.", where: [choices, inReplies, inRepository, ...agentEventWorkTurns],
    measures: [rate("choices_by_repository", ["repository_name", "family"], "Unseen choices per 1,000 replies read", { minCount: floor })], cover: bars("choices_by_repository"),
    action: "Review the choices", emptyLabel: agentEventsEmpty,
    note: `These are decisions an agent stated in a reply, such as a check it did not run or a step it did by hand, that are easy to read past. Replies only: in thinking the same kinds mostly narrate ordinary steps, at about three times the rate. Open a bar to review them in their conversations. Risky actions lists the risky commands themselves. ${agentEventsRead}` },
];
