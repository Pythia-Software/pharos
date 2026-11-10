import type { WhereTerm } from "@pythia-software/query-table-core";
import type { AnalysisCatalog, AnalysisMeasure, RatioPart } from "./dashboard-analysis-model";
import { agentEventGroups, agentEventKindLabel, agentEventKinds } from "./agent-events-analysis-catalog";
import { projectBreakdown } from "./project-analysis";

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

// The Fixes kind (fixes-model.ts's fixesKinds) that groups each friction kind
// into a change to the person's setup: what to do about a rate.
export const agentEventFixKinds: Record<string, { kind: string; label: string }> = {
  stale_guidance: { kind: "stale", label: "out-of-date docs" },
  preexisting_failure: { kind: "flaky", label: "failing or flaky checks" },
  env_friction: { kind: "env", label: "environment problems" },
  missing_capability: { kind: "tool", label: "missing tools" },
  obstacle: { kind: "obstacle", label: "recurring obstacles" },
};
// Fixes links for a population: one kind's when it names one, else all.
// Kinds the population leaves out (family != kind), or whose group it does
// not keep (family_group = another group), get no link.
export function agentEventFixLinks(where: WhereTerm[]): { href: string; label: string }[] {
  const terms = where.flatMap(term => "field" in term ? [term] : []);
  const values = (field: string, op: string) => terms.filter(term => term.field === field && term.op === op).map(term => String(term.value));
  const kinds = values("family", "="), dropped = new Set(values("family", "!=")), groups = values("family_group", "=");
  const group = new Map(agentEventKinds.map(kind => [kind.key, kind.group]));
  const chosen = (kinds.length ? kinds.filter(kind => agentEventFixKinds[kind]) : Object.keys(agentEventFixKinds))
    .filter(kind => !dropped.has(kind) && (!groups.length || groups.includes(group.get(kind) ?? "")));
  return chosen.map(kind => ({ href: `/findings?${new URLSearchParams({ optimize_area: "fixes", fixes_kind: agentEventFixKinds[kind].kind })}`, label: agentEventFixKinds[kind].label }));
}
export const agentEventLengthOrder = Object.keys(lengthLabels);
const person: WhereTerm[] = [{ field: "prompted_by", op: "=", value: "person" }];
// Turns a person or a delegating agent started: scripted runs (an automation,
// or no prompt at all, such as a corpus generator) leave out, by the same
// who-prompted field the Who prompted breakdown shows. They were a third of
// the messages read in a week, with rates of their own (a review script that
// reads every doc finds more of them stale).
export const agentEventWorkTurns: WhereTerm[] = [{ field: "prompted_by", op: "!=", value: "automated" }, { field: "prompted_by", op: "!=", value: "none" }];
const scripted = "Leaves out scripted runs: turns an automation started or with no prompt recorded (Who prompted shows them).";
const inGroup = (group: string): WhereTerm[] => [{ field: "family_group", op: "=", value: group }, ...agentEventWorkTurns];
const kindsOf = (group: string) => agentEventKinds.filter(kind => kind.group === group).map(kind => kind.key);
// What the events pass sees differs by text, not only by what happened:
// thinking narrates far more than a reply, and nearly all thinking text read
// is one model's. These breakdowns say so beside the chart.
const narrates = "Thinking narrates far more than a reply does, and nearly all the thinking text read comes from Claude models: compare reply with reply, and thinking with thinking.";
const byModel = `Models differ in how much they narrate, in whether their thinking is recorded at all, and in the work they were given, so a model's rate describes what it wrote on its tasks, not how well it worked. ${narrates}`;

export const agentEventRatesCatalog: AnalysisCatalog = {
  dataset: "agent_event_rates", views: ["trend", "compare", "distribution"], time: { first: "occurred_at", last: "occurred_at" }, measures,
  breakdowns: {
    friction: { label: "Friction kind", fields: ["family"], order: kindsOf("Friction"), where: inGroup("Friction"),
      note: `Friction is what got in an agent's way: obstacles, environment problems, out-of-date docs, failures that were already there, missing tools, unclear instructions, getting stuck and giving up. Each rate is over every message read. ${scripted}` },
    // The first view: plain obstacles ("I see the bug") are most friction in
    // every repository, so they would fill the bars; Friction kind shows them.
    friction_repository: { label: "Repository + friction kind (no plain obstacles)", fields: ["repository_name", "family"], where: [...inGroup("Friction"), { field: "family", op: "!=", value: "obstacle" }],
      note: `Each repository's rates divide by its own messages read, so a busy repository does not rank high for volume alone. Plain obstacles, most of the friction everywhere, are left out so the bars show what a change to docs, checks, environment or tools can address; Friction kind shows them. ${scripted}` },
    choices_repository: { label: "Repository + unseen choice (replies)", fields: ["repository_name", "family"], where: [...inGroup("Choices you may not see"), { field: "source", op: "=", value: "reply" }],
      note: `Choices you may not see: workarounds, claims left unchecked, assumptions, deferred work, technical debt and risks an agent stated in a reply. Replies only: in thinking the same kinds mostly narrate ordinary steps of the work, at about three times the rate (Kind with Reply or thinking shows both). Each rate divides by the repository's replies read. ${scripted}` },
    family_group: { label: "Kind group", fields: ["family_group"], order: [...agentEventGroups] },
    family: { label: "Kind", fields: ["family"], order: agentEventKinds.map(kind => kind.key) },
    source: { label: "Reply or thinking", fields: ["source"], order: ["reply", "thinking"], note: narrates },
    prompted_by: { label: "Who prompted", fields: ["prompted_by"], order: agentEventPromptedOrder },
    prompt_length: { label: "Prompt length", fields: ["prompt_length"], order: agentEventLengthOrder, where: person,
      note: "Prompt length counts the words a person typed in the prompt that started the turn, so it covers turns a person prompted only." },
    agent_role: { label: "Main agent or subagent", fields: ["agent_role"], order: ["main", "subagent"], values: { main: "Main agent", subagent: "Subagent" },
      note: `Most of a subagent's messages read are thinking, a main agent's less so. ${narrates}` },
    model: { label: "Model", fields: ["model"], empty: "Unknown model", note: byModel },
    model_effort: { label: "Model and effort", fields: ["model_effort"], note: byModel },
    effort: { label: "Effort", fields: ["effort"], empty: "Effort not recorded" },
    provider: { label: "Provider", fields: ["provider"], order: ["claude", "codex", "antigravity", "chatgpt", "tl1", "canonical"], note: byModel },
    harness: { label: "Harness", fields: ["harness"], empty: "Harness not recorded", note: byModel },
    repository_name: { label: "Repository", fields: ["repository_name"], empty: "No repository" },
    project_name: projectBreakdown(),
    week: { label: "Week", fields: ["week"], empty: "No time recorded" },
    family_source: { label: "Kind + reply or thinking", fields: ["family", "source"], note: narrates },
    group_source: { label: "Kind group + reply or thinking", fields: ["family_group", "source"], note: narrates },
    model_source: { label: "Model + reply or thinking", fields: ["model", "source"], note: byModel },
    family_model: { label: "Kind + model", fields: ["family", "model"], note: byModel },
    group_role: { label: "Kind group + main agent or subagent", fields: ["family_group", "agent_role"], note: narrates },
    family_role: { label: "Kind + main agent or subagent", fields: ["family", "agent_role"], note: narrates },
    group_repository: { label: "Kind group + repository", fields: ["family_group", "repository_name"] },
  },
  labels: {
    family: agentEventKindLabel, source: sourceLabel,
    prompted_by: value => promptedLabels[value] ?? value, prompt_length: value => lengthLabels[value] ?? value,
    agent_role: value => value === "subagent" ? "Subagent" : value === "main" ? "Main agent" : value,
  },
  records: { label: "Message read × kind", plural: "Messages read × kinds",
    note: "One record is one agent message the events pass read, once for each kind of event, with that kind's events in it (usually none). Rates divide by distinct messages, turns or conversations, so each counts once whichever kinds are chosen." },
  // A conversation's events: its distribution shows whether a rate rests on
  // many conversations or on one that carried it. Its repository, provider,
  // harness and role are its own; a kind is a filter, not a breakdown.
  units: {
    conversation: { label: "Conversation", plural: "Conversations", key: "conversation_id", where: [{ field: "conversation_id", op: "is_not_null", value: "" }],
      breakdowns: { repository_name: ["repository_name"], project_name: ["project_name"], provider: ["provider"], harness: ["harness"], agent_role: ["agent_role"] },
      note: "A conversation is one recorded agent transcript the events pass read; a delegated subagent is a conversation of its own. Its events count the kinds the population keeps." },
  },
  // Kind group, kind, repository and the rest as chips beside Range; the
  // legend's Count only and Leave out write them.
  population: true,
  // A compare's minimum counts messages read, not message × kind records.
  samples: { unit: "messages read", label: "Messages read", distinct: "message_id" },
  periods: { day: "day", week: "week", month: "month" }, timestamp: "occurred_at",
  trend: { metric: "per_thousand", split: "friction" },
  scatter: ["per_thousand", "message_share"],
};
