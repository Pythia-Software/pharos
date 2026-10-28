import { EMPTY_QUERY, encodeQuery, type WhereClause, type WhereTerm } from "@pythia-software/query-table-core";
import writing from "../../schemas/writing.schema.json";
import writingMessages from "../../schemas/writing_messages.schema.json";
import type { AnalysisCatalog, AnalysisMeasure, AnalysisState } from "./dashboard-analysis-model";

// Human Words over writing_messages: one record is one classified user
// message, with its words split by where the text came from. Messages carry
// true timestamps (sent_at), so Over time and the cycle place them in the
// viewer's calendar.

// Writing categories as the page reports them. Each group lists the
// message_authorship categories it combines (see docs/human-authorship.md)
// and the writing_messages field that sums them.
export const writingGroups = [
  { key: "typed", label: "Typed", description: "Written or dictated by you", categories: ["typed"], field: "typed_words", color: "var(--sea)" },
  { key: "pasted", label: "Likely pasted", description: "Code, logs, tables, agent-style formatting, or sent faster than typing", categories: ["pasted"], field: "pasted_words", color: "var(--heat-1)" },
  { key: "copied", label: "Copied or re-sent", description: "Matches agent output from the previous 48 hours, or anything you sent before", categories: ["quoted", "resent"], field: "copied_words", color: "var(--gold)" },
  { key: "prompts", label: "Templates and attachments", description: "Repeated one-click prompts, slash commands, attachment references", categories: ["template", "attachment"], field: "prompt_words", color: "var(--heat-2)" },
  { key: "machine", label: "Harness and automation", description: "Harness instructions and prompts sent by scripts or other agents", categories: ["harness", "automated"], field: "machine_words", color: "var(--muted)" },
];

const count = { kind: "number", decimals: 0 } as const, mean = { kind: "number", decimals: 1 } as const, percent = { kind: "percent", decimals: 1 } as const;
const present = (field: string): WhereTerm[] => [{ field, op: "is_not_null", value: "" }, { field, op: "!=", value: "" }];
const typed: WhereTerm[] = [{ field: "typed_words", op: ">", value: "0" }];
// Every message: COUNT of its id.
const messages = { op: "count" as const, field: "id", label: "messages" };
const cue = (field: string, label: string) => ({ op: "sum" as const, expression: `IF([${field}],1,0)`, label });
const measures: Record<string, AnalysisMeasure> = {
  typed: { label: "Typed words", unit: "words", format: count, op: "sum", field: "typed_words", value: "typed_words", additive: true, note: "Typed words are text you typed or dictated; pastes, copied agent output, templates, attachments, harness instructions and automated prompts are counted separately." },
  words: { label: "All user words", unit: "words", format: count, op: "sum", field: "words", value: "words", additive: true, note: "All user words count every word of each classified user message, whatever its source; Text source splits them." },
  typed_messages: { label: "Messages with typed text", unit: "messages", format: count, op: "count", additive: true, where: typed, note: "Messages holding at least one typed or dictated word." },
  messages: { label: "User messages", unit: "messages", format: count, op: "count", additive: true, note: "Every classified user message, including those holding only supplied text." },
  conversations: { label: "Conversations", unit: "conversations", format: count, op: "count_distinct", field: "conversation_id", additive: false, note: "Distinct conversations with a matching message. A conversation in several groups counts once in each, so groups do not add up to the total." },
  typed_share: { label: "Typed share of words", unit: "percent", format: percent, op: "sum", ratio: ["typed_words", "words"], additive: false, note: "Typed words divided by all user words, pooled over each group's messages rather than averaging per-message shares." },
  words_per_message: { label: "Typed words per message", unit: "words", format: mean, op: "sum", ratio: ["typed_words", { op: "sum", expression: "IF([typed_words]>0,1,0)", label: "messages with typed text" }], additive: false, note: "Typed words divided by messages holding any, pooled; messages without typed text do not dilute it." },
  correction_share: { label: "Correction-cue share", unit: "percent", format: percent, op: "sum", ratio: [cue("correction_cue", "messages with an authored correction cue"), messages], additive: false, note: "Messages whose typed text holds a correction cue (the dataset's own rule), divided by all messages, pooled." },
  positive_share: { label: "Positive-cue share", unit: "percent", format: percent, op: "sum", ratio: [cue("positive_cue", "messages with an authored positive cue"), messages], additive: false, note: "Messages whose typed text holds a positive cue (the dataset's own rule), divided by all messages, pooled." },
  copied_share: { label: "Copied-from-conversation share", unit: "percent", format: percent, op: "sum", ratio: [cue("has_source", "messages holding text copied from a conversation"), messages], additive: false, note: "Messages holding text matched to an earlier conversation, divided by all messages, pooled." },
};

const single = (entries: Array<[string, string]>): AnalysisCatalog["breakdowns"] => Object.fromEntries(entries.map(([field, label]) => [field, { label, fields: [field] }]));
const origin = { channel: ["channel"], machine_id: ["machine_id"], execution_machine_id: ["execution_machine_id"] };

// Older links named the series endpoint's scope and splits: typed or all
// words, the text-source kind, and a workspace's combined channels, machines
// and providers, which a message holds one of each.
const splits: Record<string, string> = { channels: "channel", machines: "machine_id", execution_machines: "execution_machine_id", providers: "provider" };
function legacy(state: AnalysisState): AnalysisState {
  const { scope, ...rest } = state as AnalysisState & { scope?: unknown };
  if (scope === undefined && !Object.hasOwn(splits, state.split) && !(state.split === "kind" && state.metric === undefined)) return state;
  const all = scope === "all", split = splits[state.split] ?? state.split;
  // Older links had no metric of their own (a dashboard's defaults may fill
  // one in), so their scope decides it. The typed scope's text source was
  // the typed series alone: one total.
  const metric = scope === undefined ? rest.metric : all ? "words" : "typed";
  return { ...rest, metric, split: split === "kind" && metric !== "words" ? "none" : split };
}

export const humanAnalysisCatalog: AnalysisCatalog = {
  dataset: "writing_messages", time: { first: "sent_at", last: "sent_at" }, measures,
  breakdowns: {
    repository_name: { label: "Repository", fields: ["repository_name"], empty: "No repository" },
    ...single([["source_kind", "App"], ["provider", "Provider"], ["channel", "Channel"], ["machine_id", "Machine"], ["execution_machine_id", "Execution machine"], ["main_category", "Mostly"]]),
  },
  components: { kind: { label: "Text source", measures: ["words"], fallback: "none", parts: writingGroups.map(group => ({ key: group.key, label: group.label, field: group.field, color: group.color })) } },
  records: { label: "Message", plural: "Messages", note: "A message is one classified user message, placed at the time it was sent." },
  units: {
    conversation: { label: "Conversation", plural: "Conversations", key: "conversation_id", where: present("conversation_id"),
      breakdowns: { repository_name: ["repository_name"], source_kind: ["source_kind"], provider: ["provider"], ...origin },
      note: "A conversation is one recorded agent conversation; a delegated subagent's conversation is its own. Only its messages in the range count." },
    work: { label: "Work", plural: "Works", key: "work_id", where: present("work_id"), breakdowns: { repository_name: ["repository_name"], source_kind: ["source_kind"] },
      note: "A work is one workspace's conversations with the delegated work folded into it (work_id), as Human words by workspace counts them; it is not a working directory. Only its messages in the range count." },
  },
  cycle: { hour: { kind: "timestamp", field: "sent_at" }, weekday: { kind: "timestamp", field: "sent_at" } },
  periods: { day: "day", week: "week", month: "month" }, timestamp: "sent_at",
  trend: { metric: "typed", split: "none" },
  legacy, legacySlices: true,
  scatter: ["typed", "words"],
};
humanAnalysisCatalog.breakdowns.provider.order = ["claude", "antigravity", "codex", "chatgpt", "tl1", "canonical"];
// The series endpoint grouped a whole work by its combined providers,
// channels or machines; messages are grouped by their own.
for (const [split, label] of [["provider", "providers"], ["channel", "channels"], ["machine_id", "machines"], ["execution_machine_id", "execution machines"]]) {
  humanAnalysisCatalog.breakdowns[split].note = `Each message counts under its own conversation's ${label.replace(/s$/, "")}, not its work's combined ${label}.`;
}

// Filters older (unversioned) links wrote against the writing dataset, one
// row per work. Those that keep their meaning on messages are read as
// message filters: the time bounds as sent_at, a work id as work_id, a
// combined field's single value as the message's own field, and fields
// both datasets hold with one meaning (repository, app, title, origin).
// The rest are never rewritten: a work's totals (typed_words > 100 meant a
// work's total), a combined value ("claude,codex" meant works holding
// exactly both) and its inequalities, and fields messages lack. They are
// left out and listed (unsupported), with the legacy Writing view, where
// they still apply. A versioned link is already in message terms.
const timeFields: Record<string, string> = { first_message_at: "sent_at", last_message_at: "sent_at", id: "work_id" };
const messageFields = new Set<string>(writingMessages.fields.map(field => field.name));
const workTotals = new Set<string>(writing.fields.filter(field => field.type === "number").map(field => field.name));
export type HumanLinkRead = { where: WhereTerm[]; unsupported: WhereTerm[] };
function readClause(clause: WhereClause): WhereClause | undefined {
  if (Object.hasOwn(timeFields, clause.field)) return { ...clause, field: timeFields[clause.field] };
  if (Object.hasOwn(splits, clause.field)) return clause.op === "=" && !clause.negated && !clause.value.includes(",") ? { ...clause, field: splits[clause.field] } : undefined;
  return workTotals.has(clause.field) || !messageFields.has(clause.field) ? undefined : clause;
}
export function readHumanLink(where: WhereTerm[], versioned: boolean): HumanLinkRead {
  if (versioned) return { where, unsupported: [] };
  const read: WhereTerm[] = [], unsupported: WhereTerm[] = [];
  let changed = false;
  for (const term of where) {
    const clauses = ("any" in term ? term.any : [term]).map(readClause);
    if (clauses.some(clause => !clause)) { unsupported.push(term); changed = true; continue; }
    const next: WhereTerm = "any" in term ? { ...term, any: clauses as WhereClause[] } : clauses[0]!;
    changed ||= JSON.stringify(next) !== JSON.stringify(term);
    read.push(next);
  }
  return { where: changed ? read : where, unsupported };
}
// The legacy Writing view of an older link's filters, where they still apply.
export const humanLegacyURL = (where: WhereTerm[]) => `/analyze?${new URLSearchParams({ analyze_view: "writing", q_writing: encodeQuery({ ...EMPTY_QUERY, where }) })}`;
const describeClause = (clause: WhereClause) => `${clause.negated ? "not " : ""}${clause.field} ${clause.op} ${clause.value}`.trim();
export const describeTerm = (term: WhereTerm) => "any" in term ? term.any.map(describeClause).join(" or ") : describeClause(term);
