import { EMPTY_QUERY, type AggregationClause, type QueryState, type WhereTerm } from "@pythia-software/query-table-core";
import { type QueryCategory } from "./query-categories";
import { queryURL, resolveRollingQuery, type QueryDataset, type RollingWindow } from "./query-links";
import { richerQueryRecipes } from "./query-recipes-v2";
import { enrichCardRecipe, newCardRecipes, type CardExploration } from "./card-recipes";
import { wishlistCardRecipes } from "./wishlist-card-recipes";
import type { CardCover } from "./card-cover";
import { humanWordRecipes, refineHumanRecipe, applyHumanCardCopy } from "./human-word-recipes";
import type { HumanStudy } from "./human-study";
export { clearPreviewCache, loadQueryPreview, loadQueryPreviews, formatPreviewValue } from "./query-previews";
export type { QueryPreview } from "./query-previews";

export type QueryRecipe = {
  id: string; idea: string; title: string; description: string; dataset: QueryDataset;
  field: string; days?: number[]; where?: WhereTerm[]; measures: AggregationClause[];
  unit: string; category: QueryCategory; scope?: string; note?: string;
  eligibility?: { metricId: string; minValue: number };
  exploration?: CardExploration;
  cover?: CardCover;
  calendarTimezone?: "local";
  humanStudy?: HumanStudy;
  retired?: boolean;
  action?: string;
};
export type QuerySuggestion = QueryRecipe & { query: QueryState; window: RollingWindow; url: string; instanceId: string };
const agg = (op: AggregationClause["op"], field: string, groupBy: string[], label: string): AggregationClause => ({ id: `${op}:${field}:${groupBy.join(",")}`, op, ...(field ? { field } : {}), groupBy, label });
const count = (group: string[], label: string) => agg("count", "", group, label);
const sum = (field: string, group: string[], label: string) => agg("sum", field, group, label);
const distinct = (field: string, group: string[], label: string) => agg("count_distinct", field, group, label);
const positive = (field: string): WhereTerm => ({ field, op: ">", value: "0" });
const equal = (field: string, value: string): WhereTerm => ({ field, op: "=", value });
const calls = { dataset: "tool_calls" as const, field: "started_at", unit: "calls" };
const usage = { dataset: "usage" as const, field: "first_usage_at", unit: "tokens" };

// Only executable, evidence-based recipes belong here. The full idea tracker
// records richer variants we cannot answer at their required grain yet.
const baseRecipes: QueryRecipe[] = [
  { id: "correction-phrases", category: "steering", idea: "conversation-02", title: "Where am I correcting course?", description: "Your messages containing explicit correction phrases.", dataset: "writing_messages", field: "sent_at", unit: "messages", where: [equal("typed_message", "true"), { any: ["you misunderstand", "you misunderstood", "that's not what", "that is not what", "I meant"].map(value => ({ field: "text", op: "contains" as const, value })) }], measures: [], note: "Literal phrase matches; not a semantic classification" },
  { ...calls, id: "tool-failures", category: "errors", idea: "tools-01", title: "Which tools keep failing?", description: "The calls behind recurring tool failures.", days: [1, 7, 30], where: [positive("error_count")], measures: [sum("error_count", ["tool_name"], "Failures by tool")] },
  { ...calls, id: "error-signatures", category: "errors", idea: "tools-02", title: "What error keeps coming back?", description: "Repeated error signatures, with the original calls.", where: [positive("error_count"), { field: "error_signature", op: "is_not_null", value: "" }], measures: [count(["error_signature"], "Calls by error signature"), distinct("conversation_id", ["error_signature"], "Conversations per signature"), distinct("repository_name", ["error_signature"], "Repositories per signature")] },
  { ...calls, id: "slow-commands", category: "tools", idea: "tools-05", title: "Where is the waiting?", description: "Recorded elapsed time by command; includes approval waits.", where: [positive("duration_ms"), { field: "command_name", op: "is_not_null", value: "" }], measures: [sum("duration_ms", ["command_name"], "Recorded elapsed time by command")], unit: "ms" },
  { ...calls, id: "edit-failures", category: "errors", idea: "tools-07", title: "Where do edits fail?", description: "Failed edit calls, grouped by repository path.", where: [positive("error_count"), equal("tool_category", "edit")], measures: [count(["repo_path"], "Failed edits by path"), count(["error_type"], "Edit failure mechanisms"), distinct("conversation_id", ["repo_path"], "Conversations with failed edits")] },
  { ...calls, id: "test-failures", category: "errors", idea: "verification-01", title: "Which checks need attention?", description: "Recorded test failures by command, ready to inspect.", where: [equal("test_failure", "true")], measures: [count(["command_name"], "Calls with test failures")] },
  { ...calls, id: "context-carried", category: "context", idea: "context-01", title: "What keeps riding in context?", description: "Tool output carried into later requests, in attributed tokens.", where: [positive("carried_tokens")], measures: [sum("carried_tokens", ["tool_name"], "Context carried by tool")], unit: "tokens", note: "Measured or estimated context attribution" },
  { ...calls, id: "large-outputs", category: "context", idea: "context-02", title: "What fills the context?", description: "Result tokens added by each tool; inspect the largest outputs.", where: [positive("result_tokens")], measures: [sum("result_tokens", ["tool_name"], "Result tokens by tool")], unit: "tokens", note: "Measured or estimated result tokens" },
  { ...calls, id: "truncated-outputs", category: "context", idea: "context-02", title: "What gets cut short?", description: "Truncated tool outputs and the calls that produced them.", where: [equal("truncated", "true")], measures: [count(["tool_name"], "Truncated outputs by tool")] },
  { ...calls, id: "read-paths", category: "context", idea: "context-08", title: "What do agents read?", description: "Most-read repository paths, ranked by read calls.", where: [equal("tool_category", "read")], measures: [count(["repo_path"], "Read calls by path")] },
  { ...calls, id: "research-queries", category: "research", idea: "memory-02", title: "What keeps getting researched?", description: "Exact recorded search queries ranked by frequency.", where: [{ field: "search_query", op: "is_not_null", value: "" }], measures: [count(["search_query"], "Calls by exact search query")] },
  { ...calls, id: "primary-domains", category: "research", idea: "memory-01", title: "Where does research lead?", description: "Primary recorded hosts on tool calls; open the source URLs.", where: [{ field: "host", op: "is_not_null", value: "" }], measures: [count(["host"], "Calls by primary host")], note: "Primary hosts; not every reached URL" },
  { ...calls, id: "mcp-methods", category: "tools", idea: "skills-06", title: "Which MCP methods do I use?", description: "Recorded method calls across connected tools.", where: [{ field: "mcp_method", op: "is_not_null", value: "" }], measures: [count(["mcp_method"], "Calls by MCP method")] },
  { id: "skill-evidence", category: "skills", idea: "skills-01", title: "Which skills are showing up?", description: "Invocations and observed loads; evidence of use, not compliance.", dataset: "skill_usages", field: "created_at", unit: "records", measures: [count(["skill_name"], "Evidence by skill"), count(["skill_name", "evidence_type"], "Skill evidence types"), distinct("conversation_id", ["skill_name"], "Conversations per skill")] },
  { id: "skill-load-failures", category: "errors", idea: "skills-04", title: "Which skill loads fail?", description: "Skill evidence whose parent call recorded an error.", dataset: "skill_usages", field: "created_at", unit: "records", where: [equal("status", "error")], measures: [count(["skill_name"], "Failed skill evidence"), count(["skill_path", "provider"], "Failed skill paths and providers"), distinct("conversation_id", ["skill_name"], "Conversations with failed skill evidence")] },
  { id: "human-words", category: "human", idea: "human-01", title: "How much am I writing?", description: "Words classified as typed or dictated, day by day.", dataset: "writing_messages", field: "sent_at", unit: "words", where: [positive("typed_words")], measures: [sum("typed_words", ["day"], "Your words per day")] },
  { id: "explaining-repos", category: "human", idea: "human-02", title: "Where do I explain the most?", description: "Your typed words across repositories in this window.", dataset: "writing_messages", field: "sent_at", unit: "words", where: [positive("typed_words")], measures: [sum("typed_words", ["repository_name"], "Your words by repository")], scope: "repository_name" },
  { ...usage, id: "usage-models", category: "models", idea: "models-01", title: "Which models do the work?", description: "Token use by model, day by day.", where: [positive("total_tokens")], measures: [sum("total_tokens", ["day", "model_family"], "Daily tokens by model")], scope: "model_family" },
  { ...usage, id: "usage-repos", category: "usage", idea: "cost-01", title: "Where does my usage go?", description: "Daily tokens across your repositories.", where: [positive("total_tokens")], measures: [sum("total_tokens", ["day", "repository_name"], "Daily tokens by repository")], scope: "repository_name" },
  { ...usage, id: "cost-repos", category: "cost", idea: "cost-01", title: "What is the API equivalent?", description: "Recorded usage priced at standard API rates, by repository.", where: [positive("cost_usd")], measures: [sum("cost_usd", ["day", "repository_name"], "Daily API equivalent by repository")], unit: "USD", scope: "repository_name", note: "API equivalent; partial and assumed pricing can contribute" },
  { ...usage, id: "cache-reads", category: "context", idea: "cost-03", title: "Where is caching used?", description: "Recorded cache-read tokens by model; inspect usage details.", where: [positive("cache_read_input_tokens")], measures: [sum("cache_read_input_tokens", ["day", "model_family"], "Daily cache-read tokens by model")], scope: "model_family" },
  { ...usage, id: "subagent-usage", category: "delegation", idea: "models-06", title: "What happens in subagents?", description: "Attributed subagent tokens across repositories.", where: [equal("session_kind", "subagent"), positive("total_tokens")], measures: [sum("total_tokens", ["day", "repository_name"], "Daily subagent tokens")], scope: "repository_name" },
  ...richerQueryRecipes,
  ...newCardRecipes,
  ...wishlistCardRecipes,
  ...humanWordRecipes,
];
export const queryRecipes = baseRecipes.map(enrichCardRecipe).map(refineHumanRecipe).map(applyHumanCardCopy).map(recipe => recipe.humanStudy ? { ...recipe, days: [7] } : recipe);

export function instantiateRecipe(recipe: QueryRecipe, days: number, clock = new Date()): QuerySuggestion {
  const window: RollingWindow = { days, field: recipe.field, mode: "relative" };
  const measures = recipe.calendarTimezone === "local" ? recipe.measures.map(metric => metric.dateGrouping ? { ...metric, dateGrouping: { ...metric.dateGrouping, timeZone: Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC" } } : metric) : recipe.measures;
  const source = { ...EMPTY_QUERY, where: recipe.where ?? [], aggregations: measures };
  const view = recipe.exploration;
  const coverMetric = recipe.cover?.mode === "source" ? recipe.measures.find(metric => metric.id === recipe.cover!.metricId) : undefined;
  const keys = coverMetric?.groupBy ?? view?.keys ?? [];
  const query = resolveRollingQuery(view ? {
    ...EMPTY_QUERY,
    grouping: { source, keys, summaries: view.summaries, representative: view.representative ?? null },
    select: [...keys, ...view.summaries.map(column => column.id)].map(field => ({ field })),
    where: view.having ?? [], orderBy: [{ field: view.valueField, dir: "desc", nulls: "last" }, ...(view.tieBreakField ? [{ field: view.tieBreakField, dir: "desc" as const }] : []), ...keys.map(field => ({ field, dir: "asc" as const }))],
    aggregations: view.coverMetric && !coverMetric ? [view.coverMetric] : [],
  } : source, window, clock);
  const describe = (text: string) => text.replaceAll("{days}", String(days)).replaceAll("{period}", days === 7 ? "Weekly" : days === 1 ? "Daily" : `${days}-day`).replaceAll("{lastDays}", days === 7 ? "Last seven days" : days === 1 ? "Last 24 hours" : `Last ${days} days`);
  return { ...recipe, title: describe(recipe.title), description: describe(recipe.description), measures, window, query, url: queryURL(recipe.dataset, query, window), instanceId: `${recipe.id}:${days}` };
}

export function shuffled<T>(items: T[]): T[] {
  const result = [...items];
  for (let i = result.length - 1; i > 0; i--) { const j = Math.floor(Math.random() * (i + 1)); [result[i], result[j]] = [result[j], result[i]]; }
  return result;
}

export function suggestionPool(clock = new Date(), recipes = queryRecipes): QuerySuggestion[] {
  // One explicit window per question per visit. Keep the mix broad, without
  // filling the belt with three nearly identical windows of the same card.
  return shuffled(recipes.filter(recipe => !recipe.retired)).map((recipe, index) => instantiateRecipe(recipe, (recipe.days ?? [7, 30, 90])[index % (recipe.days?.length ?? 3)], clock));
}
