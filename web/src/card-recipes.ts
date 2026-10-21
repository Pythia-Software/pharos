import type { AggregationClause, GroupingDefinition, SummaryColumn, WhereTerm } from "@pythia-software/query-table-core";
import type { QueryRecipe } from "./curiosity-data";

export type CardExploration = {
  keys: string[]; summaries: SummaryColumn[]; representative?: GroupingDefinition["representative"];
  having?: WhereTerm[]; valueField: string; unit: string; subjects: string; action: string;
  labelFields?: string[]; context?: { field: string; label: string; unit?: string }[];
  coverMetric?: AggregationClause;
  tieBreakField?: string;
};
const count = (id = "card_count", label = "Calls"): SummaryColumn => ({ id, op: "count", label });
const sum = (field: string, label: string): SummaryColumn => ({ id: `card_${field}`, op: "sum", field, label });
const distinct = (field: string, label: string): SummaryColumn => ({ id: `card_${field}`, op: "count_distinct", field, label });
const formula = (id: string, expression: string, label: string): SummaryColumn => ({ id, op: "formula", expression, label });
const conversations = distinct("conversation_id", "Conversations");
const repositories = distinct("repository_name", "Repositories");
const context = (field: string, label: string, unit?: string) => ({ field, label, ...(unit ? { unit } : {}) });
const minimum = (field: string, value: number): WhereTerm[] => [{ field, op: ">=", value: String(value) }];
const comparison = (keys: string[], summaries: SummaryColumn[], subjects: string, action: string, unit = "", extra: Partial<CardExploration> = {}): CardExploration => ({
  keys, summaries, valueField: summaries[0].id, subjects, action, unit, ...extra,
});
const counted = (keys: string[], subjects: string, action: string, repeat = false) => comparison(keys, [count(), conversations, repositories], subjects, action, "calls", {
  context: [context(conversations.id, "conversations")], ...(repeat ? { having: minimum("card_count", 2) } : {}),
});

// Covers and destinations share the same ranked query. Source metrics remain
// available when returning to records; result WHERE is deliberate HAVING.
const views: Record<string, CardExploration> = {
  "tool-failures": comparison(["tool_name"], [sum("error_count", "Failures"), count(), conversations], "tools", "Compare failing tools", "failures", { context: [context(conversations.id, "conversations")] }),
  "error-signatures": counted(["error_signature"], "repeated errors", "Explore repeated errors", true),
  "slow-commands": comparison(["command_name"], [sum("duration_ms", "Elapsed time"), formula("card_median", "MEDIAN([duration_ms])", "Median duration"), count()], "commands", "Rank command wait time", "ms", { context: [context("card_count", "calls"), context("card_median", "median", "ms")] }),
  "edit-failures": counted(["repository_name", "repo_path"], "paths", "Inspect failed edits"),
  "test-failures": counted(["command_name"], "checks", "Compare failing checks"),
  "context-carried": comparison(["tool_name"], [sum("carried_tokens", "Carried tokens"), sum("result_tokens", "Output tokens"), count()], "tools", "Compare carried context", "tokens", { context: [context("card_count", "calls")] }),
  "large-outputs": comparison(["tool_name", "result_tokens_source"], [sum("result_tokens", "Output tokens"), formula("card_median", "MEDIAN([result_tokens])", "Median output"), count()], "tool/source groups", "Rank tool output", "tokens", { context: [context("card_count", "calls")] }),
  "truncated-outputs": counted(["tool_name"], "tools", "Inspect truncation"),
  "read-paths": counted(["repository_name", "repo_path"], "paths", "Explore read paths"),
  "research-queries": counted(["search_query"], "repeated searches", "Explore repeated searches", true),
  "primary-domains": counted(["host"], "hosts", "Compare research sources"),
  "mcp-methods": counted(["mcp_method"], "methods", "Compare MCP methods"),
  "skill-evidence": comparison(["skill_name"], [count("card_count", "Evidence records"), conversations, repositories], "skills", "Compare skill evidence", "records", { context: [context(conversations.id, "conversations")] }),
  "skill-load-failures": comparison(["skill_name", "provider"], [count("card_count", "Failed evidence"), conversations], "skill/provider groups", "Inspect failed loads", "records", { context: [context(conversations.id, "conversations")] }),
  "explaining-repos": comparison(["repository_name"], [sum("typed_words", "Your words"), count("card_count", "Messages"), conversations], "repositories", "Compare your writing", "words", { context: [context("card_count", "messages")] }),
  "tool-failure-rates": comparison(["tool_name"], [formula("card_rate", "SUM([error_count])/NULLIF(SUM([call_count]),0)", "Failure rate"), sum("error_count", "Failed calls"), sum("call_count", "All calls"), conversations], "tools with failures", "Compare failure rates", "percent", { having: [...minimum("card_error_count", 1), ...minimum("card_call_count", 10)], tieBreakField: "card_call_count", context: [context("card_error_count", "failed"), context("card_call_count", "calls")] }),
  "duration-distribution": comparison(["command_name"], [formula("card_median", "MEDIAN([duration_ms])", "Median duration"), { id: "card_longest", op: "max", field: "duration_ms", label: "Longest duration" }, sum("duration_ms", "Total elapsed time"), count()], "commands", "Compare typical waits", "ms", { context: [context("card_count", "calls"), context("card_longest", "max", "ms")] }),
  "error-reach": comparison(["error_signature"], [repositories, conversations, count()], "cross-project errors", "Compare error reach", "repos", { having: minimum(repositories.id, 2), context: [context("card_count", "calls")] }),
  "skill-reach": comparison(["skill_name"], [repositories, conversations, count("card_count", "Evidence records")], "cross-project skills", "Compare skill reach", "repos", { having: minimum(repositories.id, 2), context: [context(conversations.id, "conversations")] }),
  "tl1-advancement": comparison(["project", "flavor", "execution_class", "shape_version", "configuration"], [formula("card_rate", "SUM([advanced_count])/NULLIF(SUM([attempt_count]),0)", "Advancement rate"), formula("card_escalation", "SUM([escalated_count])/NULLIF(SUM([attempt_count]),0)", "Escalation rate"), sum("attempt_count", "Attempts")], "configurations", "Compare TL1 cohorts", "percent", { having: minimum("card_attempt_count", 3), labelFields: ["configuration", "project", "flavor", "shape_version", "execution_class"], context: [context("card_attempt_count", "attempts")] }),
  "repeated-reads": { ...counted(["repository_name", "repo_path"], "repeated paths", "Inspect repeated reads", true), labelFields: ["repo_path", "repository_name"] },
  "human-word-mix": comparison(["repository_name"], [formula("card_rate", "SUM([typed_words])/NULLIF(SUM([words]),0)", "Your word share"), sum("typed_words", "Your words"), sum("words", "Retained words"), count("card_count", "Messages")], "repositories", "Compare word authorship", "percent", { having: minimum("card_typed_words", 1), context: [context("card_typed_words", "your words")] }),
  "usage-work": comparison(["workspace_id"], [sum("total_tokens", "Tokens in window"), { id: "card_work_title", op: "row", field: "title", label: "Work" }, sum("cost_usd", "API equivalent in window"), distinct("agent_session_id", "Agent sessions")], "workspaces", "Compare work in this window", "tokens", { labelFields: ["card_work_title"], representative: { orderBy: [{ field: "last_usage_at", dir: "desc", nulls: "last" }] }, context: [context("card_agent_session_id", "sessions"), context("card_cost_usd", "API", "USD")] }),
  "cache-efficiency": comparison(["provider", "model_family"], [formula("card_rate", "SUM([cache_read_input_tokens])/NULLIF(SUM([cache_read_input_tokens])+SUM([uncached_input_tokens]),0)", "Cache hit rate"), sum("cache_read_input_tokens", "Cached input"), sum("uncached_input_tokens", "Uncached input"), distinct("agent_session_id", "Agent sessions")], "model/provider groups", "Compare cache efficiency", "percent", { having: [{ field: "card_rate", op: "is_not_null", value: "" }], labelFields: ["model_family", "provider"], context: [context("card_agent_session_id", "sessions"), context("card_cache_read_input_tokens", "cached", "tokens")] }),
  "output-distribution": comparison(["result_tokens_source"], [formula("card_output_median", "MEDIAN([result_tokens])", "Median output tokens"), sum("result_tokens", "Total output tokens"), { id: "card_largest_output", op: "max", field: "result_tokens", label: "Largest output" }, count()], "attribution sources", "Compare output distributions", "tokens"),
  "output-duration": comparison(["tool_name", "result_tokens_source"], [{ id: "card_avg_duration", op: "avg", field: "duration_ms", label: "Average duration" }, { id: "card_avg_output", op: "avg", field: "result_tokens", label: "Average output tokens" }, count()], "tool/source groups", "Compare duration and output", "ms"),
  "price-comparison": comparison(["repository_name"], [sum("cost_usd", "Historical API equivalent"), sum("cost_today_usd", "At current prices"), formula("card_price_delta", "SUM([cost_today_usd]-[cost_usd])", "Current minus historical"), count("card_count", "Priced usage records")], "repositories", "Compare price changes", "USD"),
  "expensive-work": comparison(["id"], [sum("cost_usd", "Lifetime API equivalent"), { id: "card_work_title", op: "row", field: "title", label: "Work" }, { id: "card_repository", op: "row", field: "repository_name", label: "Repository" }], "workspaces", "Rank lifetime work cost", "USD", { representative: { orderBy: [{ field: "activity_at", dir: "desc", nulls: "last" }] }, labelFields: ["card_work_title"] }),
  "distribution-cohorts": comparison(["provider"], [formula("card_median", "MEDIAN([duration_ms])", "Median duration"), { id: "card_longest", op: "max", field: "duration_ms", label: "Longest duration" }, count()], "providers", "Compare provider durations", "ms"),
  "cross-repo-errors": comparison(["repository_name", "error_signature"], [count(), conversations], "repository/error groups", "Explore the error matrix", "calls"),
  "effort-cost": comparison(["provider", "model_family", "effort"], [sum("cost_usd", "API equivalent"), sum("total_tokens", "Tokens"), sum("reasoning_output_tokens", "Reasoning tokens"), distinct("agent_session_id", "Agent sessions")], "model/effort groups", "Compare model effort", "USD", { context: [context("card_agent_session_id", "sessions")] }),
  "harness-versions": comparison(["harness", "harness_version_first", "harness_version_last"], [distinct("agent_session_id", "Agent sessions"), sum("total_tokens", "Tokens"), sum("cost_usd", "API equivalent")], "harness/version groups", "Compare harness versions", "sessions"),
  "delegation-depth": comparison(["provider", "depth"], [sum("total_tokens", "Subagent tokens"), sum("cost_usd", "API equivalent"), distinct("agent_session_id", "Agent sessions")], "provider/depth groups", "Explore delegation depth", "tokens"),
  "price-coverage": comparison(["price_status"], [sum("total_tokens", "Tokens"), sum("cost_usd", "Recorded API equivalent"), distinct("agent_session_id", "Agent sessions")], "pricing statuses", "Inspect pricing coverage", "tokens"),
  "batched-tools": comparison(["tool_name"], [count(), { id: "card_batch_size", op: "avg", field: "parallel_count", label: "Mean calls per request" }, sum("error_count", "Failed calls"), conversations], "batched tools", "Inspect request batches", "calls"),
  "mcp-response-size": comparison(["tool_name"], [sum("response_bytes", "Response bytes"), count(), sum("error_count", "Failed calls"), sum("truncated_count", "Truncated responses")], "MCP tools", "Compare MCP responses", "bytes"),
};

export const newCardRecipes: QueryRecipe[] = [
  { id: "usage-work", idea: "cost-02", category: "usage", title: "Which work uses the most tokens?", description: "Workspaces ranked by tokens in this window, with API cost and agent sessions.", dataset: "usage", field: "first_usage_at", unit: "tokens", where: [{ field: "total_tokens", op: ">", value: "0" }], measures: [{ id: "work-tokens", op: "sum", field: "total_tokens", groupBy: ["workspace_id"], label: "Tokens by workspace" }], note: "Each workspace keeps its identity; the latest usage row supplies its title. Totals cover this window, not lifetime usage." },
  { id: "cache-efficiency", idea: "cost-03", category: "context", title: "Which models reuse their input?", description: "Weighted cache hit rates by model and provider, with cached and uncached tokens.", dataset: "usage", field: "first_usage_at", unit: "tokens", measures: [{ id: "cache-rate", op: "count", expression: "SUM([cache_read_input_tokens])/NULLIF(SUM([cache_read_input_tokens])+SUM([uncached_input_tokens]),0)", groupBy: ["provider", "model_family"], label: "Cache hit rate", display: { kind: "list", format: { kind: "percent", decimals: 1 } } }], note: "Ratio of recorded cache-read input to cached plus uncached input; cache creation is not included in the denominator." },
  { id: "effort-cost", idea: "models-01", category: "models", title: "Where does reasoning effort go?", description: "API equivalent, tokens and sessions by provider, model and recorded effort.", dataset: "usage", field: "first_usage_at", unit: "USD", measures: [{ id: "effort-cost", op: "sum", field: "cost_usd", groupBy: ["provider", "model_family", "effort"], label: "API equivalent by effort" }], note: "Missing effort stays a separate cohort. Cost is API equivalent, not subscription spend; effort does not establish quality." },
  { id: "harness-versions", idea: "models-01", category: "tools", title: "Which harness versions am I using?", description: "Agent sessions by harness and its first/last recorded versions, with tokens and API cost.", dataset: "usage", field: "first_usage_at", unit: "sessions", measures: [{ id: "harness-sessions", op: "count_distinct", field: "agent_session_id", groupBy: ["harness", "harness_version_first", "harness_version_last"], label: "Agent sessions by harness version" }], note: "Version ranges and missing versions remain visible. A session can appear in several version cohorts." },
  { id: "delegation-depth", idea: "models-06", category: "delegation", title: "How deep does delegation go?", description: "Attributed subagent tokens, API cost and distinct sessions by provider and depth.", dataset: "usage", field: "first_usage_at", unit: "tokens", where: [{ field: "session_kind", op: "=", value: "subagent" }], measures: [{ id: "depth-tokens", op: "sum", field: "total_tokens", groupBy: ["provider", "depth"], label: "Subagent tokens by depth" }], note: "Recorded agent depth and attributed usage; does not measure simultaneous execution." },
  { id: "price-coverage", idea: "cost-01", category: "cost", title: "How much usage has a confirmed price?", description: "Tokens and recorded API equivalent, keeping priced, assumed, partial and unpriced usage separate.", dataset: "usage", field: "first_usage_at", unit: "tokens", measures: [{ id: "pricing-tokens", op: "sum", field: "total_tokens", groupBy: ["price_status"], label: "Tokens by pricing status", display: { kind: "donut" } }], note: "Missing cost is unknown, not zero spend. API equivalent excludes subscriptions." },
  { id: "batched-tools", idea: "tools-01", category: "delegation", title: "Which tools are called together?", description: "Calls from multi-call model requests, with batch sizes and recorded failures.", dataset: "tool_calls", field: "started_at", unit: "calls", where: [{ field: "parallel_count", op: ">", value: "1" }], measures: [{ id: "batched-calls", op: "count", groupBy: ["tool_name"], label: "Calls in multi-call requests" }], note: "Co-issued calls in a model request; not evidence that their execution overlapped." },
  { id: "mcp-response-size", idea: "skills-06", category: "tools", title: "Which MCP responses are the largest?", description: "Recorded response bytes by MCP tool, with call counts, errors and truncations.", dataset: "mcp_calls", field: "called_at", unit: "bytes", measures: [{ id: "mcp-bytes", op: "sum", field: "response_bytes", groupBy: ["tool_name"], label: "MCP response bytes" }], note: "Covers Pharos MCP server requests; response bytes are not token counts." },
];

export function enrichCardRecipe(recipe: QueryRecipe): QueryRecipe {
  let exploration = views[recipe.id];
  // Daily covers keep their timeline; the destination adds comparable columns
  // at the same day/subject grain, rather than opening thousands of raw facts.
  const metric = recipe.measures[0];
  // A formula reads source fields, so it cannot cover the grouped daily rows.
  if (!exploration && metric?.op === "sum" && metric.field && !metric.expression && metric.groupBy[0] === "day") {
    const primary = sum(metric.field, metric.label ?? "Total");
    const summaries = [primary, count("card_count", recipe.dataset === "writing_messages" ? "Messages" : recipe.dataset === "mcp_calls" ? "Requests" : "Usage records")];
    if (recipe.dataset === "usage") {
      for (const field of ["total_tokens", "cost_usd"]) if (field !== metric.field) summaries.push(sum(field, field === "cost_usd" ? "API equivalent" : "Total tokens"));
      summaries.push(distinct("agent_session_id", "Agent sessions"));
    }
    exploration = comparison(metric.groupBy, summaries, "daily groups", "Explore daily comparison", recipe.unit, {
      coverMetric: { ...metric, field: primary.id },
    });
  }
  return exploration ? { ...recipe, exploration, ...(recipe.id === "cross-repo-errors" ? { cover: { mode: "source" as const, metricId: recipe.measures[0].id, kind: "table" as const } } : recipe.id === "price-coverage" ? { cover: { mode: "result" as const, metricId: "card_total_tokens", kind: "donut" as const } } : {}), ...(recipe.id === "tool-failure-rates" ? { description: "Failure rates among tools with 10+ calls, with sample sizes." } : {}) } : recipe;
}
