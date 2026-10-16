import type { SummaryColumn, WhereTerm } from "@pythia-software/query-table-core";
import type { QueryRecipe } from "./curiosity-data";

const known: WhereTerm[] = [
  { field: "total_tokens", op: ">", value: "0" },
  { field: "root_conversation_id", op: "is_not_null", value: "" },
  { field: "root_conversation_id", op: "!=", value: "" },
  { field: "delegated_run_depth", op: ">=", value: "0" },
];
const delegated: WhereTerm[] = [...known, { field: "delegated_run_depth", op: ">", value: "0" }];
const tokens: SummaryColumn = { id: "dg_tokens", op: "sum", field: "total_tokens", label: "Period tokens" };
const runs: SummaryColumn = { id: "dg_runs", op: "formula", expression: 'COUNT_DISTINCT(NULLIF([delegated_session_id],""))', label: "Active delegated runs" };
const childTokens: SummaryColumn = { id: "dg_child_tokens", op: "formula", expression: "SUM(IF([delegated_run_depth]>0,[total_tokens],0))", label: "Delegated tokens" };
const depth: SummaryColumn = { id: "dg_depth", op: "max", field: "delegated_run_depth", label: "Deepest active delegate" };
const title: SummaryColumn = { id: "dg_title", op: "row", field: "root_title", label: "Root task" };
const repository: SummaryColumn = { id: "dg_repository", op: "row", field: "root_repository_name", label: "Root repository" };
const tasks: SummaryColumn = { id: "dg_tasks", op: "count_distinct", field: "root_conversation_id", label: "Active root tasks" };
const taskKeys = ["root_repository_name", "root_conversation_id"];
const latest = { orderBy: [{ field: "last_usage_at", dir: "desc" as const, nulls: "last" as const }, { field: "id", dir: "asc" as const, nulls: "last" as const }] };
const population = "Retained positive-token usage of every pricing status, selected by recorded fact time placement. Cached input contributes again when processed again. Resolve canonical execution ancestry, including embedded agents, exact handles and linked child transcripts, linked launches, mirrors, and continuations. Missing/cyclic execution ancestry is excluded. Active means positive-token evidence inside this window; tokenless or uncaptured runs do not contribute. Root-task comparisons attribute all included executions to the root repository; missing repository stays Unspecified. These are recorded workload patterns, not quality, productivity, simultaneous execution, or measured savings.";
const execution = "Each delegated observation is one canonical execution inside a root conversation, summed over the selected period. Handles and their child transcripts merge; continuations add tokens to the same execution. Include linked child transcripts even when their recorded session kind says root. Own tokens exclude descendant tokens, so each selected fact contributes to one execution only. Depth is the resolved level in the full recorded ancestry, not the number of active ancestors in this window.";
function card(id: string, name: string, description: string, keys: string[], summaries: SummaryColumn[], action: string, extra: Partial<QueryRecipe> = {}): QueryRecipe {
  return { id, title: name, description, category: "delegation", idea: "models-06", dataset: "usage", field: "first_usage_at", days: [7, 30], unit: "tokens", where: known, measures: [], action, note: population,
    exploration: { keys, summaries, valueField: summaries[0].id, unit: extra.unit ?? "tokens", subjects: "delegation groups", action, labelFields: keys, representative: latest },
    cover: { mode: "result", metricId: summaries[0].id, kind: "bar-horizontal", maxGroups: 6, showOther: false }, ...extra };
}
function taskCountBox(): QueryRecipe {
  const summaries = [runs, tokens, childTokens, depth, title];
  return card("task-delegate-count-ranges", "Delegates per delegating task", "Active delegated runs per delegating root task over the last {days} days, grouped by repository.", taskKeys, summaries, "Explore delegating task sizes", {
    unit: "runs", note: `${population} First count distinct canonical delegated runs per root task, then exclude zero-delegation tasks. Require three delegating tasks per root repository before choosing cohorts by sample count. All boxes share a linear run-count axis starting at zero. Sparse repositories remain in the destination. This is total active descendant count, not direct-child fan-out.`,
    exploration: { keys: taskKeys, summaries, valueField: runs.id, unit: "runs", subjects: "delegating root tasks", action: "Explore delegating task sizes", labelFields: [title.id, "root_repository_name"], representative: latest, having: [{ field: runs.id, op: ">", value: "0" }],
      coverMetric: { id: runs.id, op: "count", field: runs.id, groupBy: ["root_repository_name"], groupLimit: 10000, sort: [{ key: "samples", dir: "desc" }], distribution: { kind: "box", input: `[${runs.id}]`, whiskers: "tukey" }, label: "Active delegates per delegating root task", display: { kind: "box", xLabel: "Delegated runs / task", xScale: { mode: "linear", min: 0 } } } },
    cover: { mode: "result", metricId: runs.id, kind: "box", maxGroups: 6, density: "compact", minSamples: 3, sampleUnit: "delegating tasks" },
  });
}
const runKeys = ["root_repository_name", "root_conversation_id", "delegated_session_id"];
export const delegationRecipes: QueryRecipe[] = [
  card("repository-delegated-share", "Delegated token share", "Delegated tokens divided by all resolved task tokens over the last {days} days, by root repository.", ["root_repository_name"], [
    { id: "dg_share", op: "formula", expression: "SUM(IF([delegated_run_depth]>0,[total_tokens],0))/NULLIF(SUM([total_tokens]),0)", label: "Delegated token share" }, childTokens, tokens, runs, tasks,
  ], "Inspect delegated token shares", { unit: "percent", note: `${population} Divide summed delegated tokens by all tokens for the same resolved repository population. Root and root-continuation tokens enter only the denominator. Keep repositories with zero delegated tokens; child-only activity can reach 100%. This is a ratio of summed tokens, not an average of task shares. Each token belongs once to its root repository.` }),
  card("delegating-task-share", "Tasks using delegation", "Share of active root tasks with active delegated runs over the last {days} days, by repository.", taskKeys, [runs, tokens, childTokens, depth, title], "Explore tasks using delegation", {
    note: `${population} First make one row per active root task, including roots with child-only activity. A task counts as delegating if it has a positive canonical delegated-run count in this period. Divide the number of delegating task rows by all active task rows in the same root repository; zero-delegation tasks remain in that denominator and in the destination. Inactive historical children alone do not make a task delegating for this measure.`,
    exploration: { keys: taskKeys, summaries: [runs, tokens, childTokens, depth, title], valueField: runs.id, unit: "runs", subjects: "root tasks", action: "Explore tasks using delegation", labelFields: [title.id, "root_repository_name"], representative: latest,
      coverMetric: { id: runs.id, op: "count", field: runs.id, expression: `SUM(IF([${runs.id}]>0,1,0))/NULLIF(COUNT(),0)`, groupBy: ["root_repository_name"], groupLimit: 10000, label: "Root tasks with active delegation", display: { kind: "bar-horizontal", format: { kind: "percent", decimals: 1 } } } },
  }),
  card("delegated-run-token-ranges", "Delegated run sizes", "Own tokens per active delegated run over the last {days} days, grouped by root repository.", runKeys, [tokens, depth, title], "Explore individual delegated workloads", {
    where: delegated, note: `${population} ${execution} Sum each execution before constructing repository distributions; each run gets equal observation weight. Require three delegated runs per root repository before selecting cohorts by observation count. Boxes share one log-token axis; sparse runs and outliers remain inspectable.`,
    exploration: { keys: runKeys, summaries: [tokens, depth, title], valueField: tokens.id, unit: "tokens", subjects: "delegated runs", action: "Explore individual delegated workloads", labelFields: [title.id, "delegated_session_id"], representative: latest,
      coverMetric: { id: tokens.id, op: "count", field: tokens.id, groupBy: ["root_repository_name"], groupLimit: 10000, sort: [{ key: "samples", dir: "desc" }], distribution: { kind: "box", input: `[${tokens.id}]`, whiskers: "tukey" }, label: "Own tokens per delegated run", display: { kind: "box", xLabel: "Own tokens / delegated run", xScale: { mode: "log" } } } },
    cover: { mode: "result", metricId: tokens.id, kind: "box", maxGroups: 6, density: "compact", minSamples: 3, sampleUnit: "delegated runs" },
  }),
  taskCountBox(),
  card("delegation-tree-shape", "Tree depth and size", "Deepest active delegate versus active descendant count per delegating task over the last {days} days.", taskKeys, [depth, runs, tokens, childTokens, title], "Inspect delegation tree shapes", {
    unit: "runs", note: `${population} One point per delegating root task. X is the greatest resolved depth among its active delegated runs; Y is its distinct active descendant count, including nested descendants. X is linear and Y logarithmic. This compares depth with recorded tree size, not direct-child fan-out or concurrency. An active grandchild can retain depth two even if its parent has no period usage. Limit the cover by largest active run counts; the destination retains all delegating tasks.`,
    exploration: { keys: taskKeys, summaries: [depth, runs, tokens, childTokens, title], valueField: depth.id, unit: "levels", subjects: "delegating root tasks", action: "Inspect delegation tree shapes", labelFields: [title.id, "root_repository_name"], representative: latest, having: [{ field: runs.id, op: ">", value: "0" }],
      coverMetric: { id: depth.id, op: "max", field: depth.id, expression: `MAX([${depth.id}])`, expressionY: `MAX([${runs.id}])`, groupBy: taskKeys, groupLimit: 2000, sort: [{ key: "y", dir: "desc" }], label: "Depth and active delegated runs", display: { kind: "scatter", xLabel: "Deepest active delegation level", yLabel: "Active delegated runs", xScale: { mode: "linear", min: 0 }, yScale: { mode: "log" }, xFormat: { kind: "number", decimals: 0 }, yFormat: { kind: "number", decimals: 0 }, legendPosition: "none" } } },
    cover: { mode: "result", metricId: depth.id, kind: "scatter", maxGroups: 2000, height: 300 },
  }),
];

export function refineDelegationRecipe(recipe: QueryRecipe): QueryRecipe {
  if (["subagent-usage", "task-delegate-count-ranges", "delegation-tree-shape", "batched-tools"].includes(recipe.id)) recipe = { ...recipe, retired: true };
  if (recipe.id === "subagent-usage") {
    const keys = ["day", "root_repository_name"];
    return { ...recipe, title: "Delegated tokens over time", description: "Recorded delegated token volume over the last {days} days, attributed to each task’s root repository.", days: [7, 30], where: delegated, action: "Explore daily delegated workloads", note: `${population} ${execution} Group tokens by their retained local day and root repository. Sum the same selected facts for the headline. Daily time placement may be session-based; this is processing volume, not unique content.`,
      exploration: { keys, summaries: [tokens, runs], valueField: tokens.id, unit: "tokens", subjects: "daily repository groups", action: "Explore daily delegated workloads", labelFields: keys,
        coverMetric: { id: tokens.id, op: "sum", field: tokens.id, groupBy: keys, groupLimit: 10000, label: "Daily delegated tokens" } },
      cover: { mode: "result", metricId: tokens.id, kind: "auto", maxGroups: 5, showOther: true, showLegend: true, height: 300 } };
  }
  if (recipe.id === "delegation-depth") {
    const keys = ["delegated_run_depth", "root_conversation_id", "delegated_session_id"], summaries = [tokens, title, repository];
    return { ...recipe, days: [7, 30], title: "Delegated runs by depth", description: "Active canonical delegated runs over the last {days} days, counted at their resolved ancestry depth.", unit: "runs", where: delegated, action: "Inspect delegated execution depths", note: `${population} ${execution} Reduce to one canonical execution per depth before counting. A handle and linked child transcript count once. Root executions at depth zero are excluded; nested parents remain delegated executions. This counts runs, not tokens or overlapping execution.`,
      exploration: { keys, summaries, valueField: tokens.id, unit: "tokens", subjects: "delegated executions", action: "Inspect delegated execution depths", labelFields: [title.id, "delegated_session_id"], representative: latest,
        coverMetric: { id: tokens.id, op: "count", field: tokens.id, groupBy: ["delegated_run_depth"], groupLimit: 10000, sort: [{ key: "group0", dir: "asc" }], label: "Active delegated runs by depth", display: { kind: "bar-horizontal", valueLabel: "Active runs" } } },
      cover: { mode: "result", metricId: tokens.id, kind: "bar-horizontal", maxGroups: 12, showOther: false } };
  }
  if (recipe.id === "batched-tools") return { ...recipe, days: [7, 30], title: "Calls issued together", description: "Tool calls in multi-call model requests started in the last {days} days, grouped by tool.", action: "Inspect co-issued tool calls", note: "Each tool-call row is one call started inside the window. Include failed and no-result calls if parallel_count exceeds one. Call counts count rows, not distinct model requests. The mean batch size is call-weighted: a batch of N calls can contribute N observations. Shared issuance does not prove simultaneous execution, wall-clock savings, or delegated agents.",
    cover: { mode: "result", metricId: recipe.exploration!.valueField, kind: "bar-horizontal", maxGroups: 8, showOther: false } };
  return recipe;
}
