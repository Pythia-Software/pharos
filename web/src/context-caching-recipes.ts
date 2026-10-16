import type { AggregationClause, SummaryColumn, WhereTerm } from "@pythia-software/query-table-core";
import type { QueryRecipe } from "./curiosity-data";

const sum = (id: string, field: string, label: string): SummaryColumn => ({ id, op: "sum", field, label });
const formula = (id: string, expression: string, label: string): SummaryColumn => ({ id, op: "formula", expression, label });
const present = (field: string): WhereTerm => ({ field, op: "is_not_null", value: "" });
const positive = (field: string): WhereTerm => ({ field, op: ">", value: "0" });
const calls: SummaryColumn = { id: "cc_calls", op: "count", label: "Recorded calls" };
const conversations: SummaryColumn = { id: "cc_conversations", op: "count_distinct", field: "conversation_id", label: "Recorded conversations" };
const resultTokens = sum("cc_results", "result_tokens", "Attributed result tokens");
const carried = sum("cc_carried", "carried_tokens", "Later token-exposures");
const input = sum("cc_input", "input_tokens", "All recorded input tokens");
const reads = sum("cc_reads", "cache_read_input_tokens", "Cache-read input tokens");
const fresh = sum("cc_fresh", "uncached_input_tokens", "Fresh input tokens");
const writes = sum("cc_writes", "cache_creation_input_tokens", "Cache-write input tokens");
const shareExpression = "SUM([cache_read_input_tokens])/NULLIF(SUM([input_tokens]),0)";
const share = formula("cc_share", shareExpression, "Cache-read share of all input");
const toolKeys = ["tool_name", "result_tokens_source"];
const attribution = "Result tokens are attributed additions to context, not model output tokens. ‘Measured’ allocates the next request’s positive prompt growth across incoming content; ‘estimated’ uses retained result size. Neither is exact per-tool tokenization. Attribution sources remain separate. Zero or missing result sizes do not establish an empty original output.";
const carriage = "Carriage is result_tokens × later recorded requests before compaction in the same stream. The window selects calls by start time; those later requests can fall outside it. These are recorded lifetime token-exposures for selected calls, not input consumed during the period, extra unique content, billed tokens, or cache savings.";
const caching = "Cache-read share is SUM(cache_read_input_tokens) / SUM(input_tokens), weighted by tokens rather than averaging row percentages. All input includes cache reads, cache writes, and fresh input; unlike the previous cache-efficiency definition, writes now remain in the denominator. Provider accounting and retained usage coverage can differ. These totals reflect usage-fact time placement, not exact request timestamps, unique text, saved money, or model quality.";

function card(id: string, title: string, description: string, keys: string[], summaries: SummaryColumn[], unit: string, action: string, extra: Partial<QueryRecipe> = {}): QueryRecipe {
  return { id, title, description, category: "context", idea: "context-02", dataset: "tool_calls", field: "started_at", days: [7], unit, measures: [],
    exploration: { keys, summaries, valueField: summaries[0].id, unit, subjects: "tool/source groups", action, labelFields: keys },
    cover: { mode: "result", metricId: summaries[0].id, kind: "bar-horizontal", maxGroups: 6, showOther: false }, action, note: attribution, ...extra };
}
function cacheCard(id: string, title: string, keys: string[], subject: string): QueryRecipe {
  const action = `Inspect ${subject} input reuse`;
  return card(id, title, `Cache-read share of all input over the last {days} days, weighted by tokens; cache writes included.`, keys, [share, reads, fresh, writes, input], "percent", action, {
    dataset: "usage", field: "first_usage_at", idea: "cost-03", where: [positive("input_tokens")], note: caching,
    exploration: { keys, summaries: [share, reads, fresh, writes, input], valueField: share.id, unit: "percent", subjects: `${subject} groups`, action, labelFields: keys, context: [{ field: input.id, label: "input", unit: "tokens" }] },
  });
}
const cacheModel = cacheCard("cache-efficiency", "Cache reuse by model", ["provider", "model_family"], "model");
const cacheRepo = cacheCard("cache-repositories", "Cache reuse by repository", ["repository_name"], "repository");
const rootKeys = ["root_provider", "root_model_family", "root_conversation_id"];
const scatter: AggregationClause = { id: "cc_cache_workload", op: "sum", expression: "SUM([input_tokens])", expressionY: shareExpression, groupBy: rootKeys, groupLimit: 2000, sort: [{ key: "value", dir: "desc" }], label: "Whole conversations", display: { kind: "scatter", maxGroups: 2000, xLabel: "All input tokens (log)", yLabel: "Cache-read share", xScale: { mode: "log" }, yScale: { mode: "linear" }, xFormat: { kind: "number", decimals: 0 }, yFormat: { kind: "percent", decimals: 1 }, legendPosition: "none" } };
const cacheWorkload = card("cache-workload", "Input volume and reuse", "Whole-conversation input versus cache-read share over the last {days} days, including delegated work; logarithmic input axis.", rootKeys, [input, share, reads, fresh, writes], "tokens", "Explore conversation input reuse", {
  dataset: "usage", field: "first_usage_at", idea: "cost-03", where: [positive("input_tokens"), present("root_conversation_id")], measures: [scatter], cover: { mode: "source", metricId: scatter.id, kind: "scatter", height: 300 },
  exploration: { keys: rootKeys, summaries: [input, share, reads, fresh, writes], valueField: input.id, unit: "tokens", subjects: "whole conversations", action: "Explore conversation input reuse", labelFields: rootKeys },
  note: `${caching} Each point sums a whole top-level conversation, including recorded delegated work, linked launches, and continuations, and uses its top-level recorded model color. Missing/cyclic lineage is excluded. Up to 2,000 conversations ranked by input are drawn; all remain in the destination. Only X is logarithmic: the linear percentage axis preserves zero-cache conversations. Large total input does not mean a large individual context window.`,
});
const sizes: AggregationClause = { id: "cc_size_box", op: "count", groupBy: toolKeys, groupLimit: 10000, sort: [{ key: "samples", dir: "desc" }], distribution: { kind: "box", input: "[result_tokens]", whiskers: "tukey" }, label: "Nonempty tool result sizes", display: { kind: "box", maxGroups: 6, xLabel: "Result tokens", xScale: { mode: "log" } } };
const truncated = formula("cc_truncated", "SUM(IF([truncated],1,0))", "Truncated calls");
const truncationRate = formula("cc_truncation_rate", "SUM(IF([truncated],1,0))/NULLIF(COUNT(),0)", "Truncated share of calls");
const reuse = formula("cc_reuse", "SUM([carried_tokens])/NULLIF(SUM([result_tokens]),0)", "Token-weighted later requests");
const definitions: QueryRecipe[] = [
  cacheModel, cacheRepo, cacheWorkload,
  card("large-outputs", "Tool context additions", "Attributed result tokens from calls started in the last {days} days, split by tool and attribution source.", toolKeys, [resultTokens, calls, conversations], "tokens", "Inspect tool context additions", {
    where: [positive("result_tokens")], cover: { mode: "result", metricId: resultTokens.id, kind: "pie", maxGroups: 5, showOther: true, showLegend: true, showHeadline: false },
    // Fetch every tool/source total before folding Other, including on hosts
    // that do not return an aggregate remainder for limited grouped metrics.
    exploration: { keys: toolKeys, summaries: [resultTokens, calls, conversations], valueField: resultTokens.id, unit: "tokens", subjects: "tool/source groups", action: "Inspect tool context additions", labelFields: toolKeys,
      coverMetric: { id: resultTokens.id, op: "sum", field: resultTokens.id, groupBy: toolKeys, groupLimit: 10000, label: "Attributed result tokens", display: { kind: "pie" } } },
  }),
  card("output-distribution", "Tool result sizes", "Nonempty result sizes for calls started in the last {days} days; three-call minimum per tool/source group.", toolKeys, [formula("cc_median", "MEDIAN([result_tokens])", "Median result tokens"), resultTokens, { id: "cc_max", op: "max", field: "result_tokens", label: "Largest result" }, calls], "tokens", "Explore tool result sizes", {
    where: [positive("result_tokens")], measures: [sizes], cover: { mode: "source", metricId: sizes.id, kind: "box", maxGroups: 6, minSamples: 3, density: "compact" },
    note: `${attribution} Each nonempty recorded call is one observation. Require three calls per tool/source cohort before selecting six cohorts by observation count. All cohorts remain in the destination. Shared logarithmic token axis; median, quartiles, Tukey whiskers, sampled outliers and extremes. Hover supplies mean, range, and sample size. Zero-size calls are excluded from this size comparison.`,
  }),
  card("truncated-outputs", "Tool truncation rates", "Truncated share of all calls started in the last {days} days, by tool; ten-call minimum.", ["tool_name"], [truncationRate, truncated, calls, conversations], "percent", "Inspect truncated tool calls", {
    exploration: { keys: ["tool_name"], summaries: [truncationRate, truncated, calls, conversations], having: [{ field: calls.id, op: ">=", value: "10" }], valueField: truncationRate.id, unit: "percent", subjects: "tools", action: "Inspect truncated tool calls", labelFields: ["tool_name"], context: [{ field: truncated.id, label: "truncated" }, { field: calls.id, label: "calls" }] },
    note: "For each tool, divide calls marked truncated by all recorded calls started in the selected window, including successful and zero-size calls. Require ten calls per tool, then show six highest rates. Missing truncation flags are not asserted truncations and remain in the denominator; recording coverage can vary. Truncation does not establish that important content was lost. The destination retains numerator and denominator, with underlying call rows available through group expansion.",
  }),
  card("context-carried", "Tool context exposure", "Later token-exposures of tool results from calls started in the last {days} days; includes later requests.", toolKeys, [carried, resultTokens, calls], "token-exposures", "Inspect carried tool results", { where: [positive("carried_tokens")], idea: "context-01", note: `${attribution} ${carriage}` }),
  card("tool-result-reuse", "Tool result reuse", "Token-weighted later requests per result from calls started in the last {days} days, including never-carried results.", toolKeys, [reuse, carried, resultTokens, calls], "later requests", "Explore result reuse patterns", {
    where: [positive("result_tokens")], idea: "context-01", note: `${attribution} ${carriage} SUM(carried_tokens) / SUM(result_tokens) is a result-token-weighted mean number of later requests, not mean requests per call. Results never carried contribute zero to the numerator and their tokens to the denominator. A large result has more weight.`,
  }),
  card("read-paths", "Most-read repository paths", "Read calls started in the last {days} days per repository path, with distinct recorded conversation counts.", ["repository_name", "repo_path"], [calls, conversations, resultTokens], "calls", "Explore frequently read paths", {
    where: [{ field: "tool_category", op: "=", value: "read" }, present("repo_path")], idea: "context-08", note: "Count all retained read calls with a mapped repository-relative path. Repository and path remain separate keys; identical paths in different repositories are not merged. This shows frequency across conversations, not repeated reads within one conversation. Missing paths and non-read tools are excluded. A recorded call can refer to a partial file; frequency does not establish unchanged content or redundant work.",
    exploration: { keys: ["repository_name", "repo_path"], summaries: [calls, conversations, resultTokens], valueField: calls.id, unit: "calls", subjects: "repository paths", action: "Explore frequently read paths", labelFields: ["repo_path", "repository_name"], context: [{ field: conversations.id, label: "conversations" }] },
    cover: { mode: "result", metricId: calls.id, kind: "table", maxGroups: 6 },
  }),
  card("repeated-reads", "Rereads within conversations", "Repository paths read at least twice in one recorded conversation during the last {days} days.", ["repository_name", "repo_path", "conversation_id"], [calls, resultTokens], "calls", "Inspect repeated conversation reads", {
    where: [{ field: "tool_category", op: "=", value: "read" }, present("repo_path"), present("conversation_id")], idea: "context-03",
    exploration: { keys: ["repository_name", "repo_path", "conversation_id"], summaries: [calls, resultTokens], having: [{ field: calls.id, op: ">=", value: "2" }], valueField: calls.id, unit: "calls", subjects: "conversation/path groups", action: "Inspect repeated conversation reads", labelFields: ["repo_path", "repository_name", "conversation_id"], context: [{ field: resultTokens.id, label: "result", unit: "tokens" }] },
    cover: { mode: "result", metricId: calls.id, kind: "table", maxGroups: 6 },
    note: "Count read calls per repository/path/conversation, then retain groups with at least two calls in the window. Recorded conversation IDs identify individual agent transcripts, not whole delegated trees. The count includes the first read; it is not an extra-read count. File content, read ranges, and prior reads outside the window can differ. Repetition is an inspection cue, not proof of wasted effort. Missing paths/conversation IDs are excluded; source calls retain their identities.",
  }),
];
const byID = new Map(definitions.map(recipe => [recipe.id, recipe]));
export const contextCachingRecipes = definitions.filter(recipe => ["cache-repositories", "cache-workload", "tool-result-reuse"].includes(recipe.id));
export function refineContextCachingRecipe(recipe: QueryRecipe): QueryRecipe {
  if (byID.has(recipe.id)) {
    const next = byID.get(recipe.id)!;
    return ["cache-workload", "context-carried", "tool-result-reuse"].includes(recipe.id) ? { ...next, retired: true } : next;
  }
  if (recipe.id === "output-duration") return { ...recipe, retired: true };
  if (recipe.id !== "cache-reads") return recipe;
  return { ...recipe, retired: true, title: "Cached input over time", description: "Recorded cache-read tokens over the last {days} days, grouped by day and model.", days: [7], action: "Explore daily cached input", note: caching,
    exploration: { ...recipe.exploration!, action: "Explore daily cached input" },
    cover: { mode: "result", metricId: recipe.exploration!.valueField, kind: "auto", maxGroups: 5, showOther: true, showLegend: true, height: 300 } };
}
