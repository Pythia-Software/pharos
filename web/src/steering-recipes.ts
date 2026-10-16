import type { SummaryColumn, WhereTerm } from "@pythia-software/query-table-core";
import type { QueryRecipe } from "./curiosity-data";

const present = (field: string): WhereTerm[] => [{ field, op: "is_not_null", value: "" }, { field, op: "!=", value: "" }];
const authored: WhereTerm[] = [{ field: "typed_words", op: ">", value: "0" }, ...present("conversation_id")];
// Cue fields are computed against individual typed spans by Pharos. Supplied
// text cannot trigger a match, even when the same message also has human words.
const corrections: SummaryColumn = { id: "st_corrections", op: "formula", expression: "SUM(IF([correction_cue],1,0))", label: "Messages with correction cues" };
const positive: SummaryColumn = { id: "st_positive", op: "formula", expression: "SUM(IF([positive_cue],1,0))", label: "Messages with positive cues" };
const messages: SummaryColumn = { id: "st_messages", op: "count", label: "Eligible authored messages" };
const count: SummaryColumn = { id: "card_count", op: "count", label: "Matching messages" };
const title: SummaryColumn = { id: "st_title", op: "row", field: "title", label: "Work context" };
const excerpt: SummaryColumn = { id: "st_excerpt", op: "row", field: "text", label: "Latest matching message" };
const latest = { orderBy: [{ field: "sent_at", dir: "desc" as const, nulls: "last" as const }, { field: "id", dir: "asc" as const, nulls: "last" as const }] };
const keys = ["repository_name", "conversation_id"];
const population = "Only recorded user messages containing words classified as typed or dictated contribute to phrase comparisons. Cue fields match inside individual typed spans, so pasted, quoted, attached, re-sent, and automated spans cannot supply a cue. Mixed messages remain eligible when their human-authored spans contain a cue; matches do not bridge excluded spans. Select by message sent time. Conversation means an individual recorded transcript, not a whole delegated task; repository is the message’s recorded repository, with missing labels retained. English case-insensitive substring cues can include negation, politeness, hypothetical examples, and unrecognized quotes. They are inspectable evidence, not semantic labels, measured quality, task completion, satisfaction, or blame.";
const repeatPopulation = "Exact full-message text, including case, punctuation and whitespace, defines a repeat. Include positive typed or resent words; exclude pasted, quoted, template, attachment, harness and automated words. Re-sends can be legitimate and short continuations can recur; repetition does not establish ignored instructions, frustration, or waste. Select each message by sent time, including messages in conversations started earlier. These are individual recorded transcripts; no delegated-tree merging or near-duplicate detection is performed.";
function card(id: string, name: string, description: string, groupKeys: string[], summaries: SummaryColumn[], action: string, extra: Partial<QueryRecipe> = {}): QueryRecipe {
  return { id, title: name, description, category: "steering", idea: "conversation-02", dataset: "writing_messages", field: "sent_at", days: [7, 30], unit: "messages", measures: [], where: authored, note: population, action,
    exploration: { keys: groupKeys, summaries, valueField: summaries[0].id, unit: "messages", subjects: "recorded conversations", action, labelFields: [title.id, "repository_name"], representative: latest },
    cover: { mode: "result", metricId: summaries[0].id, kind: "bar-horizontal", maxGroups: 6, showOther: false }, ...extra };
}
function reach(id: string, name: string, description: string, measure: SummaryColumn, action: string, idea: string): QueryRecipe {
  const summaries = [measure, messages, title];
  return card(id, name, description, keys, summaries, action, { idea, unit: "percent", note: `${population} First build one row per repository/conversation pair with at least one eligible authored message. Within each repository, divide conversations containing a cue by all eligible conversation rows. A conversation counts once regardless of its matching-message volume; zero-match conversations stay in the denominator and destination. Conversations with no typed/dictated words are absent; mixed messages remain eligible. This is period reach, not the percentage of messages, lifetime conversations, or all active tasks.`,
    exploration: { keys, summaries, valueField: measure.id, unit: "percent", subjects: "eligible recorded conversations", action, labelFields: [title.id, "repository_name"], representative: latest,
      coverMetric: { id: measure.id, op: "count", field: measure.id, expression: `SUM(IF([${measure.id}]>0,1,0))/NULLIF(COUNT(),0)`, groupBy: ["repository_name"], groupLimit: 10000, label: "Share of eligible conversations with cues", display: { kind: "bar-horizontal", format: { kind: "percent", decimals: 1 } } } },
  });
}
export const steeringRecipes: QueryRecipe[] = [
  reach("correction-conversation-share", "Conversations with correction cues", "Share of eligible conversations containing correction phrases over the last {days} days, grouped by repository.", corrections, "Inspect correction reach", "conversation-02"),
  reach("positive-conversation-share", "Conversations with positive feedback", "Share of eligible conversations containing praise or thanks over the last {days} days, grouped by repository.", positive, "Inspect positive-feedback reach", "conversation-07"),
  card("within-conversation-repeats", "Messages repeated within conversations", "Exact message text sent repeatedly within one recorded conversation over the last {days} days.", ["conversation_id", "text"], [count, title, { id: "st_repository", op: "row", field: "repository_name", label: "Repository" }], "Read repeated messages in context", {
    idea: "conversation-03", note: `${repeatPopulation} Group by conversation and exact text before requiring at least two messages; this is local repetition rather than reuse across conversations. Count includes the first send. Order by occurrences, not consecutive runs. The latest matching message supplies work/repository context.`,
    exploration: { keys: ["conversation_id", "text"], summaries: [count, title, { id: "st_repository", op: "row", field: "repository_name", label: "Repository" }], valueField: count.id, unit: "messages", subjects: "conversation/message pairs", action: "Read repeated messages in context", labelFields: ["text", title.id], representative: latest, having: [{ field: count.id, op: ">=", value: "2" }] },
    cover: { mode: "result", metricId: count.id, kind: "list", maxGroups: 6, showOther: false },
  }),
];

export function refineSteeringRecipe(recipe: QueryRecipe): QueryRecipe {
  // The category was rejected in full. Keep saved definitions executable.
  if (recipe.category !== "steering") return recipe;
  recipe = { ...recipe, retired: true };
  if (["correction-phrases", "positive-feedback"].includes(recipe.id)) {
    const correcting = recipe.id === "correction-phrases", cueField = correcting ? "correction_cue" : "positive_cue";
    const action = correcting ? "Read correction-cue messages" : "Read positive-feedback messages";
    return { ...recipe, title: correcting ? "Correction cues by conversation" : "Positive feedback messages", description: correcting ? "Conversations ranked by correction-cue messages sent in the last {days} days; inspect their actual wording." : "Conversations ranked by praise or thanks messages sent in the last {days} days; inspect the wording.", days: [7, 30], where: [...authored, { field: cueField, op: "=", value: "true" }], action, note: `${population} Rank recorded conversations by matching messages, including conversations with a single match. The latest matching message supplies the excerpt, not an inferred outcome or preceding agent response.`,
      exploration: { keys, summaries: [count, title, excerpt], valueField: count.id, unit: "messages", subjects: "conversations with matching messages", action, labelFields: [title.id, "repository_name"], representative: latest },
      cover: { mode: "result", metricId: count.id, kind: "bar-horizontal", maxGroups: 6, showOther: false },
    };
  }
  if (["repeated-messages", "within-conversation-repeats"].includes(recipe.id)) {
    const where: WhereTerm[] = [{ any: ["typed_words", "resent_words"].map(field => ({ field, op: ">", value: "0" })) }, ...["pasted_words", "quoted_words", "template_words", "attachment_words", "harness_words", "automated_words"].map(field => ({ field, op: "=" as const, value: "0" })), ...present("conversation_id"), ...present("text")];
    if (recipe.id === "within-conversation-repeats") return { ...recipe, where };
    return { ...recipe, days: [7, 30], title: "Messages reused across conversations", description: "Exact messages appearing in at least three recorded conversations over the last {days} days.", where, action: "Explore shared message text", note: `${repeatPopulation} Require three distinct nonempty conversation IDs per exact text before ranking by conversation reach. Repeated sends inside a single conversation increase message count but not reach. Repository counts reflect recorded message attribution; missing repositories do not count toward repository reach.`,
      exploration: { ...recipe.exploration!, action: "Explore shared message text", labelFields: ["text"] }, cover: { mode: "result", metricId: recipe.exploration!.valueField, kind: "list", maxGroups: 6, showOther: false } };
  }
  return recipe;
}
