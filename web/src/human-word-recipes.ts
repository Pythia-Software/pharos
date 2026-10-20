import type { AggregationClause, SummaryColumn } from "@pythia-software/query-table-core";
import { trendCover } from "./card-trend";
import type { QueryRecipe } from "./curiosity-data";

const words: SummaryColumn = { id: "human_words", op: "sum", field: "typed_words", label: "Your words" };
const messages: SummaryColumn = { id: "human_messages", op: "count", label: "Messages with your words" };
const conversations: SummaryColumn = { id: "human_conversations", op: "count_distinct", field: "conversation_id", label: "Conversations with your words" };
const formula = (id: string, expression: string, label: string): SummaryColumn => ({ id, op: "formula", expression, label });
const base = { dataset: "writing_messages", field: "sent_at", category: "human", idea: "human-01", unit: "words", where: [{ field: "typed_words", op: ">", value: "0" }] } as const;
const dailyHuman = (id: string, op: AggregationClause["op"], field: string, label: string): AggregationClause => ({ id, op, ...(field ? { field } : {}), groupBy: ["day"], groupLimit: 10000, label });
const ownWords = "Only messages containing words classified as typed or dictated contribute. Pasted and automated words are excluded. Counts describe writing, not time or quality.";
function summary(id: string, title: string, description: string, primary: SummaryColumn, extra: SummaryColumn[], denominator: string): QueryRecipe {
  const measures = [primary, ...extra].map(column => ({ id: column.id, op: column.op === "formula" ? "count" : column.op, ...(column.field ? { field: column.field } : {}), ...(column.op === "formula" ? { expression: column.expression } : {}), groupBy: [], label: column.label, display: { kind: "value", format: { kind: "number", decimals: column.op === "formula" ? 1 : 0 } } } as AggregationClause));
  return { ...base, where: [...base.where], id, title, description, measures,
    cover: { mode: "source", metricId: primary.id, kind: "value" }, note: `${denominator} ${ownWords}` };
}

// Every average is a ratio over the same windowed source population. A work
// item is canonical work_id, not a title, repository, or individual agent chat.
export const humanWordRecipes: QueryRecipe[] = [
  summary("human-word-total", "Writing volume vs. usual", "Trailing word count beside its historical norm and a 30-day sparkline.", words, [messages, conversations], "Sum of your words sent during this window."),
  summary("human-words-per-message", "Words per message", "Average message length, with a trailing-window sparkline and historical norm.", formula("words_per_message", "SUM([typed_words])/NULLIF(COUNT(),0)", "Words per message"), [words, messages], "Denominator: messages containing at least one of your words; automated-only messages do not dilute the average."),
  summary("human-words-per-conversation", "Words per conversation", "Average writing per conversation, with a trailing-window sparkline.", formula("words_per_conversation", "SUM([typed_words])/NULLIF(COUNT_DISTINCT([conversation_id]),0)", "Words per conversation"), [words, conversations, messages], "Denominator: distinct conversations with your writing in this window, including conversations started earlier. This is not a lifetime conversation total."),
  { ...base, where: [...base.where], id: "human-words-per-work", title: "Words per workspace directory", description: "Conversations sharing the same repository and working directory count together.", measures: [], humanStudy: "workspace-average", note: "Directory identity comes from the Library location joined through the message’s work ID. All conversations in the same repository and exact directory are combined. Missing mappings are excluded and disclosed. A work ID alone is not a workspace directory." },
  { ...base, where: [...base.where], id: "human-repo-conversation-depth", title: "Words per conversation by repository", description: "Repositories ranked by your average words per conversation, with sample sizes.", measures: [],
    exploration: { keys: ["repository_name"], summaries: [formula("words_per_conversation", "SUM([typed_words])/NULLIF(COUNT_DISTINCT([conversation_id]),0)", "Words per conversation"), words, conversations, messages], valueField: "words_per_conversation", unit: "words", subjects: "repositories", action: "Compare depth and sample sizes", context: [{ field: conversations.id, label: "conversations" }] },
    cover: { mode: "result", metricId: "words_per_conversation", kind: "bar-horizontal", maxGroups: 5, showOther: false },
    note: `Divide each repository's word total by its distinct conversations with your writing. This is a weighted ratio, not a mean of message averages. Small samples remain visible in the table. ${ownWords}` },
  { ...base, where: [...base.where], id: "human-conversation-messages", title: "Which conversations need more of my input?", description: "Conversations ranked by messages you wrote, alongside words and average message length.", measures: [], unit: "messages",
    exploration: { keys: ["conversation_id"], summaries: [messages, words, formula("words_per_message", "SUM([typed_words])/NULLIF(COUNT(),0)", "Words per message"), { id: "human_title", op: "row", field: "title", label: "Work" }, { id: "human_repository", op: "row", field: "repository_name", label: "Repository" }], representative: { orderBy: [{ field: "sent_at", dir: "desc", nulls: "last" }] }, valueField: messages.id, unit: "messages", subjects: "conversations", action: "Read the conversations", labelFields: ["human_title", "human_repository"], context: [{ field: words.id, label: "words" }] },
    cover: { mode: "result", metricId: messages.id, kind: "bar-horizontal", maxGroups: 5 },
    note: `Count your messages per conversation during this window. Several conversations can share a work title; they remain separate. More messages can mean collaboration or iteration, not necessarily corrections. ${ownWords}` },
  { ...base, where: [...base.where], id: "human-message-lengths", title: "Conversation word-count distribution", description: "Writing per conversation in this window, including short and long conversations.",
    measures: [{ id: "conversation_words", op: "sum", field: "typed_words", groupBy: ["conversation_id"], label: "Your words per conversation" }],
    exploration: { keys: ["conversation_id"], summaries: [words, messages, { id: "human_title", op: "row", field: "title", label: "Work" }], valueField: words.id, subjects: "conversations", action: "Inspect conversation totals", unit: "words", representative: { orderBy: [{ field: "sent_at", dir: "desc", nulls: "last" }] }, coverMetric: { id: "conversation_word_distribution", op: "sum", field: words.id, groupBy: [], label: "Words per conversation", distribution: { kind: "histogram", input: "[human_words]", bins: 10 }, display: { kind: "histogram", xLabel: "Your words in a conversation", yLabel: "Conversations" } } },
    cover: { mode: "result", metricId: words.id, kind: "histogram" },
    note: `Each sample is a conversation's total typed/dictated words during the window. Conversations started earlier are included; this is not a lifetime conversation length. ${ownWords}` },
  { ...base, where: [{ any: ["typed_words", "pasted_words", "attachment_words"].map(field => ({ field, op: ">" as const, value: "0" })) }], id: "human-contribution-patterns", title: "Human contribution patterns", description: "Brief continuations, concrete feedback cues, supplied context, and other writing.", measures: [], unit: "percent", humanStudy: "contribution-patterns", note: "Mutually exclusive message categories based on visible human-authored evidence. A brief or unclassified message is not necessarily low value. No quality score is inferred." },
  { ...base, where: [...base.where], id: "human-specific-feedback", title: "Feedback with concrete detail", description: "Direction or correction paired with a reason, condition, target, example, or reference.", measures: [], unit: "percent", humanStudy: "specific-feedback", note: "Share of fully visible authored messages matching explicit feedback and detail cues. Inspect the linked excerpts to judge whether the rule captures useful feedback." },
  { ...base, where: [...base.where], id: "human-messages-per-day", title: "Messages per day", description: "Messages containing your words per day, averaged over each trailing seven complete days, across the last 30 days.", days: [30], unit: "messages", action: "Explore daily message counts",
    measures: [dailyHuman("human_daily_messages", "count", "", "Messages with your words per day")], cover: trendCover("human_daily_messages"),
    trend: { value: { op: "count" }, perDay: true, lookback: 7, completeDays: true, valueLabel: "messages a day · 7-day average", pointValueLabel: "messages a day", trendLabel: "7d avg", sample: "messages",
      detail: "Each point: messages containing your words in its trailing seven complete local days ÷ 7. The norm is the mean of the prior 30 endpoints." },
    note: `Counts messages by their local sent day. Each point divides seven days' messages by seven, including idle days; today is excluded until it is complete. ${ownWords}` },
  { ...base, where: [...base.where], id: "human-conversations-per-day", title: "Conversations per day", description: "Conversations in which you wrote per day, averaged over each trailing seven complete days, across the last 30 days.", days: [30], unit: "conversations", action: "Explore daily conversation counts",
    measures: [dailyHuman("human_daily_conversations", "count_distinct", "conversation_id", "Conversations with your words per day")], cover: trendCover("human_daily_conversations"),
    trend: { value: { op: "count_distinct", field: "conversation_id" }, count: { op: "count_distinct", field: "conversation_id" }, perDay: true, lookback: 7, completeDays: true, valueLabel: "conversations a day · 7-day average", pointValueLabel: "conversations a day", trendLabel: "7d avg", sample: "conversation-days",
      detail: "Each point: conversations you wrote in on each of its trailing seven complete local days, summed and ÷ 7. The norm is the mean of the prior 30 endpoints." },
    note: `A conversation counts on every day you wrote in it, so one that runs across several days is counted on each. A conversation is counted when you write in it, not when it ends. Each point divides seven days' counts by seven, including idle days; today is excluded until it is complete. ${ownWords}` },
];

// Keep retired definitions resolvable for saved collections; new suggestions
// and the review use the revised set. Descriptive titles apply library-wide.
export function refineHumanRecipe(recipe: QueryRecipe): QueryRecipe {
  if (["work-writing", "human-conversation-messages", "conversation-writing", "human-action-phrases", "human-contribution-patterns"].includes(recipe.id)) return { ...recipe, retired: true };
  if (recipe.id === "human-word-total") return { ...recipe, humanStudy: "volume", measures: [...recipe.measures, { id: "human_daily_words", op: "sum", field: "typed_words", groupBy: ["day"], label: "Daily human-written words", display: { kind: "bar-vertical", xLabel: "Day sent", yLabel: "Human-written words" } }] };
  if (recipe.id === "human-words-per-message") return { ...recipe, humanStudy: "message-average" };
  if (recipe.id === "human-words-per-conversation") return { ...recipe, humanStudy: "conversation-average" };
  if (recipe.id === "human-words") return { ...recipe, title: "Trailing {days}-day word count", description: "Your writing volume in the trailing window, broken down by day." };
  if (recipe.id === "explaining-repos") return { ...recipe, title: "Words written per repository" };
  if (recipe.id === "human-word-mix") return { ...recipe, title: "Authored word share by repository", description: "Your typed or dictated words as a share of retained user-message text, grouped by repository.", cover: { mode: "result", metricId: "card_rate", kind: "bar-horizontal", maxGroups: 12, showOther: false } };
  if (recipe.id === "writing-hours") return { ...recipe, title: "Writing by local hour", description: "Words contributed at each hour of the day across the selected window.", humanStudy: "hours", note: "Uses the device’s timezone. All dates are combined into 24 clock-hour buckets. Inspect source messages for the full window." };
  if (recipe.id === "human-example-phrases") return { ...recipe, title: "Supplied examples and context", description: "Images/screenshots, pasted text/code, and file references or links as shares of human contributions.", where: [{ any: ["typed_words", "pasted_words", "attachment_words"].map(field => ({ field, op: ">" as const, value: "0" })) }], measures: [], exploration: undefined, humanStudy: "examples", note: "Counts observed supply formats, not literal example phrases. Categories overlap. Percentages use human messages/contributions, including context-only messages; no link to resulting code changes or quality is inferred." };
  return recipe;
}

const humanCardCopy: Record<string, { title: string; description: string; action: string }> = {
  "human-word-total": { title: "{period} writing volume over time", description: "Human-written words over each trailing {days}-day period, with 30 days of history for comparison.", action: "Explore daily writing trends" },
  "human-words": { title: "{lastDays} of writing", description: "Human-written words sent during the last {days} days, grouped by the day they were sent.", action: "Explore daily word counts" },
  "explaining-repos": { title: "Words written per repository", description: "Human-written words over the last {days} days, grouped by repository.", action: "See breakdown by repo" },
  "human-words-per-message": { title: "Words per message", description: "Average human-written words per message over each trailing {days}-day period; excludes messages without human writing.", action: "Inspect message lengths" },
  "human-words-per-conversation": { title: "Words per conversation", description: "Average human-written words per conversation over each trailing {days}-day period, including conversations started earlier.", action: "Explore conversation writing" },
  "human-words-per-work": { title: "Words per workspace directory", description: "Average human-written words per workspace directory over trailing {days} days, combining conversations sharing that directory.", action: "Inspect contributing messages" },
  "human-repo-conversation-depth": { title: "Conversation words by repo", description: "Average human-written words per conversation in the last {days} days, compared across repositories with sample counts.", action: "Compare repository averages" },
  "human-message-lengths": { title: "Conversation size profile", description: "Conversations grouped by human-written word totals over the last {days} days; each bar shows a size band.", action: "Compare conversation totals" },
  "human-word-mix": { title: "Authorship by repository", description: "Human-written share of retained user-message text over the last {days} days, grouped by repository.", action: "Explore authorship by repo" },
  "writing-hours": { title: "When you work", description: "Human-written words by local clock hour over the last {days} days; includes all dates combined.", action: "Explore writing times" },
  "human-specific-feedback": { title: "Feedback with concrete detail", description: "Share of assessed human-written messages with concrete feedback cues over each trailing {days}-day period.", action: "Read source messages" },
  "human-example-phrases": { title: "Supplied examples and context", description: "Human contributions containing images, pasted text, or file references during the last {days} days; categories overlap.", action: "Inspect human contributions" },
};
export function applyHumanCardCopy(recipe: QueryRecipe): QueryRecipe {
  const copy = humanCardCopy[recipe.id];
  if (!copy || recipe.retired) return recipe;
  return { ...recipe, ...copy, ...(recipe.exploration ? { exploration: { ...recipe.exploration, action: copy.action } } : {}),
    ...(recipe.id === "explaining-repos" ? { note: [recipe.note, "Pie shares use the full human-word total; Other combines every repository beyond the seven named."].filter(Boolean).join(" "), cover: { mode: "result", metricId: "card_typed_words", kind: "pie", maxGroups: 7, showOther: true, showLegend: true } } : {}),
    ...(recipe.id === "human-word-mix" ? { cover: { mode: "result", metricId: "card_rate", kind: "bar-horizontal", maxGroups: 8, showOther: false } } : {}),
    ...(recipe.id === "human-message-lengths" ? { humanStudy: "conversation-lengths" } : {}),
  };
}
