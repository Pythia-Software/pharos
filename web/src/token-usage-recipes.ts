import type { SummaryColumn, WhereTerm } from "@pythia-software/query-table-core";
import type { QueryRecipe } from "./curiosity-data";

const positive: WhereTerm[] = [{ field: "total_tokens", op: ">", value: "0" }];
const roots: WhereTerm[] = [...positive, { field: "root_conversation_id", op: "is_not_null", value: "" }, { field: "root_conversation_id", op: "!=", value: "" }];
const tokens: SummaryColumn = { id: "tu_tokens", op: "sum", field: "total_tokens", label: "Recorded tokens" };
const input: SummaryColumn = { id: "tu_input", op: "sum", field: "input_tokens", label: "Input tokens" };
const output: SummaryColumn = { id: "tu_output", op: "sum", field: "output_tokens", label: "Output tokens" };
const tasks: SummaryColumn = { id: "tu_tasks", op: "count_distinct", field: "root_conversation_id", label: "Top-level conversations" };
const title: SummaryColumn = { id: "tu_title", op: "row", field: "root_title", label: "Top-level conversation" };
const latest = { orderBy: [{ field: "last_usage_at", dir: "desc" as const, nulls: "last" as const }, { field: "id", dir: "asc" as const, nulls: "last" as const }] };
const rootKeys = ["root_project_name", "root_conversation_id"];
const recorded = "Retained positive-token usage records, including assumed, partial, and unpriced usage. Tokens are recorded processing volume, not unique text, context-window size, human effort, quality, or value. Repeated cached input contributes again on each recorded request. Time selection uses first_usage_at at the recorded row level, which can use request time or session-based placement (session-start or reconciled session-remainder). Facts are not prorated. Daily buckets use their recorded local day. Missing projects remain Unspecified.";
const whole = "A conversation is one resolved top-level conversation including recorded embedded agents, delegated conversations, linked launches, and continuations. Attribute descendants to the top-level project, including across workspaces and projects. Roots with only child activity in the selected period still count; independent roots in one workspace stay separate. Missing/cyclic ancestry is excluded from conversation comparisons. Only selected-period usage contributes, not lifetime totals.";
function card(id: string, title: string, description: string, keys: string[], summaries: SummaryColumn[], action: string, extra: Partial<QueryRecipe> = {}): QueryRecipe {
  return { id, title, description, category: "usage", idea: "cost-01", dataset: "usage", field: "first_usage_at", days: [7, 30], unit: "tokens", where: positive, measures: [], action, note: recorded,
    exploration: { keys, summaries, valueField: summaries[0].id, unit: "tokens", subjects: "token groups", action, labelFields: keys },
    cover: { mode: "result", metricId: summaries[0].id, kind: "bar-horizontal", maxGroups: 6, showOther: false }, ...extra };
}
function taskBox(id: string, name: string, description: string, measure: SummaryColumn, action: string): QueryRecipe {
  return card(id, name, description, rootKeys, [measure, ...(measure.id === tokens.id ? [input, output] : [tokens, input]), title], action, {
    idea: "cost-02", where: roots, note: `${recorded} ${whole} Each top-level conversation supplies one observation. Require three conversations per project before selecting groups by sample count. All boxes share one token axis: logarithmic for strictly positive values, linear if any visible group contains zero. Zero output remains a valid observation; it is not a failed conversation. Sparse groups and outliers remain in the destination.`,
    exploration: { keys: rootKeys, summaries: [measure, ...(measure.id === tokens.id ? [input, output] : [tokens, input]), title], representative: latest, valueField: measure.id, unit: "tokens", subjects: "top-level conversations", action, labelFields: [title.id, "root_project_name"],
      coverMetric: { id: measure.id, op: "count", field: measure.id, groupBy: ["root_project_name"], groupLimit: 10000, sort: [{ key: "samples", dir: "desc" }], distribution: { kind: "box", input: `[${measure.id}]`, whiskers: "tukey" }, label: `${name} (tokens)`, display: { kind: "box", xLabel: "Tokens / top-level conversation", xScale: { mode: "log" } } } },
    cover: { mode: "result", metricId: measure.id, kind: "box", maxGroups: 6, minSamples: 3, sampleUnit: "top-level conversations", density: "compact" },
  });
}

const modelKeys = ["root_provider", "root_model_family"];
const configurationKeys = [...modelKeys, "root_effort"];
const taskConfigurationKeys = [...configurationKeys, "root_conversation_id"];
const rootRepository: SummaryColumn = { id: "tu_repository", op: "row", field: "root_project_name", label: "Top-level project" };
const delegates: SummaryColumn = { id: "tu_delegates", op: "formula", expression: 'COUNT_DISTINCT(NULLIF([delegated_session_id],""))', label: "Active delegated runs" };
const delegatedExpression = 'SUM(IF(IS_NULL([delegated_session_id]),0,IF([delegated_session_id]="",0,[total_tokens])))';
const delegatedTokens: SummaryColumn = { id: "tu_delegated_tokens", op: "formula", expression: delegatedExpression, label: "Delegated tokens" };
const delegatedShare: SummaryColumn = { id: "tu_delegated_share", op: "formula", expression: `${delegatedExpression}/NULLIF(SUM([total_tokens]),0)`, label: "Delegated token share" };
const cohorts = "Groups use the top-level conversation’s recorded configuration. Model class means the existing model-family grouping, with providers kept separate; unspecified model/effort stays visible. Children keep the root group even when they use another model or effort. Names can combine model versions. These are observed workloads with different conversations and recording coverage, not controlled comparisons of capability or efficiency.";
function cohortBox(id: string, name: string, description: string, by: string[], maxGroups: number): QueryRecipe {
  const keys = [...by, "root_conversation_id"], summaries = [tokens, input, output, title, rootRepository];
  const action = "Explore conversations in each group";
  return card(id, name, description, keys, summaries, action, {
    idea: "models-01", where: roots, note: `${recorded} ${whole} ${cohorts} Sum each top-level conversation’s selected-period tokens before constructing distributions. Require three conversations per group before selecting groups by sample count. Each conversation has equal observation weight. Compact boxes share one log-token axis, with exact values on hover; sparse groups and outliers remain in the destination.`,
    exploration: { keys, summaries, valueField: tokens.id, unit: "tokens", subjects: "top-level conversations", action, representative: latest, labelFields: [title.id, rootRepository.id, ...by],
      coverMetric: { id: tokens.id, op: "count", field: tokens.id, groupBy: by, groupLimit: 10000, sort: [{ key: "samples", dir: "desc" }], distribution: { kind: "box", input: `[${tokens.id}]`, whiskers: "tukey" }, label: name, display: { kind: "box", xLabel: "Tokens / top-level conversation", xScale: { mode: "log" } } } },
    cover: { mode: "result", metricId: tokens.id, kind: "box", maxGroups, minSamples: 3, sampleUnit: "top-level conversations", density: "compact" },
  });
}
function taskDelegationScatter(id: string, name: string, description: string, share: boolean): QueryRecipe {
  const summaries = [tokens, delegates, delegatedTokens, delegatedShare, input, output, title, rootRepository];
  const action = share ? "Explore delegated token shares" : "Explore conversation delegation workloads";
  return card(id, name, description, taskConfigurationKeys, summaries, action, {
    idea: "models-06", where: roots, note: `${recorded} ${whole} ${cohorts} One point per root task, colored by its root model. Active delegated runs count distinct canonical delegated_session_id values with positive-token facts inside the window, deduplicating linked execution handles and child transcripts. Root/continuation usage has no delegated identity. Delegated tokens sum only facts with a nonempty delegated identity; the share divides by the same task’s total tokens. Zero-delegation tasks remain in the destination. ${share ? "Total-token X is logarithmic; delegated-share Y is linear from zero to one." : "Both axes are logarithmic; zero-delegation points are omitted with a visible count and retained in the destination."} Nonpositive coordinates on the log axis stay in the destination. Points are ranked by conversation token volume; a limited cover is not a random sample.`,
    exploration: { keys: taskConfigurationKeys, summaries, valueField: tokens.id, unit: "tokens", subjects: "top-level conversations", action, representative: latest, labelFields: [title.id, rootRepository.id, ...configurationKeys],
      coverMetric: { id: tokens.id, op: "max", field: tokens.id, expression: `MAX([${share ? tokens.id : delegates.id}])`, expressionY: `MAX([${share ? delegatedShare.id : tokens.id}])`, groupBy: taskConfigurationKeys, groupLimit: 2000, sort: [{ key: share ? "value" : "y", dir: "desc" }], label: name,
        display: { kind: "scatter", xLabel: share ? "Conversation tokens" : "Active delegated runs", yLabel: share ? "Delegated token share" : "Conversation tokens", xScale: { mode: "log" }, yScale: share ? { mode: "linear", min: 0, max: 1 } : { mode: "log" }, xFormat: { kind: "number", decimals: 0 }, yFormat: share ? { kind: "percent", decimals: 1 } : { kind: "number", decimals: 0 }, legendPosition: "none" } } },
    cover: { mode: "result", metricId: tokens.id, kind: "scatter", maxGroups: 2000, height: 300 },
  });
}

const runCohorts = ["effort", "run_role"];
const runKeys = [...runCohorts, "root_conversation_id", "delegated_session_id"];
const runSummaries = [tokens, input, output, title, rootRepository];
const runAction = "Explore runs by effort";

export const tokenUsageRecipes: QueryRecipe[] = [
  ...([ ["channel", "Channel"], ["machine_id", "Machine"], ["execution_machine_id", "Execution machine"] ] as const).flatMap(([field,label]) => [
    card(`origin-tokens-${field}`, `Tokens by ${label.toLowerCase()}`, `Recorded tokens over the last {days} days, grouped by ${label.toLowerCase()}.`, [field], [tokens, { id: "origin_sessions", op: "count_distinct", field: "agent_session_id", label: "Agent sessions" }], `Inspect ${label.toLowerCase()} usage`, {
      note: `${recorded} Channel and Machine follow the initiating conversation, including delegated usage. Execution machine identifies the session's execution location. Unknown attribution stays in the denominator; a capture host is not necessarily an execution device.`,
      cover: { mode:"result",metricId:tokens.id,kind:"bar-horizontal",maxGroups:6,showOther:true },
    }),
    card(`origin-cost-${field}`, `API cost by ${label.toLowerCase()}`, `Recorded API-equivalent cost over the last {days} days, grouped by ${label.toLowerCase()}.`, [field], [{ id:"origin_cost",op:"sum",field:"cost_usd",label:"API equivalent" },tokens], `Inspect ${label.toLowerCase()} cost`, {
      unit:"USD",note:"API list-price equivalent, not subscription spend or invoices. Assumed and partial prices retain their existing meaning; missing costs do not become zero. Channel and Machine follow conversation initiation, including descendants; execution placement stays separate. Unknown origin groups remain visible.",
      cover:{mode:"result",metricId:"origin_cost",kind:"bar-horizontal",maxGroups:6,showOther:true},
    }),
  ]),
  card("run-effort-token-ranges", "Run tokens by effort", "Own tokens per standalone, parent, or delegated run by recorded effort over the last {days} days.", runKeys, runSummaries, runAction, {
    idea: "models-06", where: [...roots, { field: "run_role", op: "is_not_null", value: "" }],
    note: `${recorded} One observation is a recorded execution’s own selected-period tokens at one recorded effort, excluding its descendants’ tokens. Separate Standalone (root with no known children), Parent (root with known children), and Delegated (any non-root execution, even if it delegates further). Roles use full recorded parent links, including tokenless children and linked launches. Missing/cyclic execution ancestry is excluded. Group by top-level conversation and distinct delegated identity, merging continuations and exact handles/child transcripts. Own effort comes from each usage record, not the root; a run changing effort contributes a separate, nonoverlapping run/effort observation to each setting. Missing effort remains Unspecified. Require three run/effort observations per group; sparse groups stay in the destination. All boxes share one log-token axis. Workload and model mix can differ across roles.`,
    exploration: { keys: runKeys, summaries: runSummaries, representative: latest, valueField: tokens.id, unit: "tokens", subjects: "run/effort pairs", action: runAction, labelFields: [title.id, ...runCohorts],
      coverMetric: { id: tokens.id, op: "count", field: tokens.id, groupBy: runCohorts, groupLimit: 10000, sort: [{ key: "samples", dir: "desc" }], distribution: { kind: "box", input: `[${tokens.id}]`, whiskers: "tukey" }, label: "Run tokens by effort", display: { kind: "box", xLabel: "Own tokens / run–effort", xScale: { mode: "log" } } } },
    cover: { mode: "result", metricId: tokens.id, kind: "box", maxGroups: 24, minSamples: 3, sampleUnit: "run/effort pairs", density: "compact" },
  }),
  card("repository-token-share", "Project token share", "Recorded tokens over the last {days} days, attributed to each usage record’s project; includes cached input.", ["project_name"], [tokens, input, output], "Explore project token shares", {
    exploration: { keys: ["project_name"], summaries: [tokens, input, output], valueField: tokens.id, unit: "tokens", subjects: "projects", action: "Explore project token shares", labelFields: ["project_name"],
      coverMetric: { id: tokens.id, op: "sum", field: tokens.id, groupBy: ["project_name"], groupLimit: 10000, label: "Project tokens", display: { kind: "pie" } } },
    cover: { mode: "result", metricId: tokens.id, kind: "pie", maxGroups: 5, showOther: true, showLegend: true, legendColumns: 2 },
  }),
  taskBox("task-token-ranges", "Conversation token ranges", "Period tokens per top-level conversation including delegation over the last {days} days; three conversations per project required.", tokens, "Explore conversation token ranges"),
  taskBox("task-output-ranges", "Conversation output ranges", "Recorded output per top-level conversation including delegation over the last {days} days; zero-output conversations remain included.", output, "Explore conversation output ranges"),
  card("repository-output-share", "Output share by project", "Output tokens divided by all recorded tokens over the last {days} days, grouped by project.", ["project_name"], [{ id: "tu_output_share", op: "formula", expression: "SUM([output_tokens])/NULLIF(SUM([total_tokens]),0)", label: "Output share" }, output, tokens, input], "Inspect output-share denominators", {
    unit: "percent", note: `${recorded} Divide summed output_tokens by summed total_tokens for the same project population, not the mean of per-fact ratios. Total tokens include reused input and retained unclassified tokens. Providers differ in output/reasoning reporting; reasoning_output_tokens is a subset and is not added again. A lower output share is not evidence of waste or lower usefulness.`,
    exploration: { keys: ["project_name"], summaries: [{ id: "tu_output_share", op: "formula", expression: "SUM([output_tokens])/NULLIF(SUM([total_tokens]),0)", label: "Output share" }, output, tokens, input], valueField: "tu_output_share", unit: "percent", subjects: "projects", action: "Inspect output-share denominators", labelFields: ["project_name"], context: [{ field: output.id, label: "output tokens" }, { field: tokens.id, label: "total tokens" }] },
    cover: { mode: "result", metricId: "tu_output_share", kind: "bar-horizontal", maxGroups: 6, showOther: false },
  }),
  cohortBox("task-effort-token-ranges", "Conversation tokens by effort", "Top-level conversation tokens including delegation over the last {days} days, grouped by top-level effort; three-conversation minimum.", ["root_effort"], 9),
  cohortBox("task-model-token-ranges", "Conversation tokens by model", "Top-level conversation tokens including delegation over the last {days} days, grouped by top-level provider and model family.", modelKeys, 8),
  cohortBox("task-model-effort-token-ranges", "Conversation tokens by model–effort", "Top-level conversation tokens including delegation over the last {days} days, grouped by top-level model family and effort.", configurationKeys, 12),
  card("model-task-token-spread", "Typical and largest conversations", "Median versus maximum top-level conversation tokens over the last {days} days, grouped by top-level provider and model family.", [...modelKeys, "root_conversation_id"], [tokens, input, output, title], "Inspect model conversation-size tails", {
    idea: "models-01", where: roots, note: `${recorded} ${whole} ${cohorts} First sum tokens per top-level conversation. Each point compares MEDIAN and MAX of those conversation totals within a top-level provider/model group, requiring three conversations before plotting. Hover retains the observation count. Larger groups have more opportunities for an extreme maximum; this is not a model quality or variance score. Both axes are logarithmic. Sparse groups stay in the destination.`,
    exploration: { keys: [...modelKeys, "root_conversation_id"], summaries: [tokens, input, output, title], valueField: tokens.id, unit: "tokens", subjects: "top-level conversations", action: "Inspect model conversation-size tails", representative: latest,
      coverMetric: { id: tokens.id, op: "count", field: tokens.id, expression: `MEDIAN([${tokens.id}])`, expressionY: `MAX([${tokens.id}])`, groupBy: modelKeys, groupLimit: 10000, label: "Model conversation-size tails", display: { kind: "scatter", xLabel: "Median conversation tokens", yLabel: "Largest conversation tokens", xScale: { mode: "log" }, yScale: { mode: "log" }, xFormat: { kind: "number", decimals: 0 }, yFormat: { kind: "number", decimals: 0 }, legendPosition: "none" } } },
    cover: { mode: "result", metricId: tokens.id, kind: "scatter", maxGroups: 100, minSamples: 3, sampleUnit: "top-level conversations", height: 300 },
  }),
  taskDelegationScatter("task-token-delegation-count", "Conversation volume and delegation", "Top-level conversation tokens versus active delegated-run count over the last {days} days; log–log axes.", false),
  taskDelegationScatter("task-token-delegation-share", "Conversation volume and delegated share", "Top-level conversation tokens versus their delegated token share over the last {days} days; zero-delegation conversations remain visible.", true),
  card("repository-input-output", "Project input and output", "Input versus output tokens by project over the last {days} days; logarithmic axes include reused input.", ["project_name"], [input, output, tokens], "Compare project token workloads", {
    note: `${recorded} One point per recorded project compares summed input_tokens with summed output_tokens from the same positive-token facts. Cached input is included in input. Both axes are logarithmic; nonpositive points are omitted with a count and remain in the destination. This is workload volume, not a controlled efficiency comparison.`,
    exploration: { keys: ["project_name"], summaries: [input, output, tokens], valueField: input.id, unit: "tokens", subjects: "projects", action: "Compare project token workloads", labelFields: ["project_name"],
      coverMetric: { id: input.id, op: "max", field: input.id, expression: `MAX([${input.id}])`, expressionY: `MAX([${output.id}])`, groupBy: ["project_name"], groupLimit: 10000, label: "Project input and output", display: { kind: "scatter", xLabel: "Input tokens", yLabel: "Output tokens", xScale: { mode: "log" }, yScale: { mode: "log" }, legendPosition: "none" } } },
    cover: { mode: "result", metricId: input.id, kind: "scatter", maxGroups: 200 },
  }),
  card("token-time-placement", "Token timing evidence", "Recorded token shares by request or session-based time placement over the last {days} days; unknown labels retained.", ["attribution"], [tokens, { id: "tu_facts", op: "count", label: "Usage facts" }], "Inspect token time placement", {
    note: `${recorded} Partition token volume by the retained attribution field, not by fact count. A daily fact with mixed placement inherits its least precise contributing placement. Session-based placement can concentrate later activity on an earlier date. This is evidence about the timing of recorded volume, not provider billing or activity completeness.`,
    exploration: { keys: ["attribution"], summaries: [tokens, { id: "tu_facts", op: "count", label: "Usage facts" }], valueField: tokens.id, unit: "tokens", subjects: "placement types", action: "Inspect token time placement", labelFields: ["attribution"],
      coverMetric: { id: tokens.id, op: "sum", field: tokens.id, groupBy: ["attribution"], groupLimit: 10000, label: "Tokens by time placement", display: { kind: "donut" } } },
    cover: { mode: "result", metricId: tokens.id, kind: "donut", maxGroups: 5, showOther: true, showLegend: true, showHeadline: false },
  }),
];

export function refineTokenUsageRecipe(recipe: QueryRecipe): QueryRecipe {
  if (["repository-input-output", "token-time-placement", "model-task-token-spread", "task-token-delegation-count", "task-token-delegation-share"].includes(recipe.id)) return { ...recipe, retired: true };
  if (recipe.id === "usage-repos") return { ...recipe, days: [7, 30], title: "Daily token volume", description: "Recorded tokens by day and project over the last {days} days; cached and unpriced usage included.", where: positive, action: "Explore daily project tokens", note: recorded,
    exploration: { ...recipe.exploration!, action: "Explore daily project tokens", coverMetric: { ...recipe.exploration!.coverMetric!, groupLimit: 10000 } },
    cover: { mode: "result", metricId: recipe.exploration!.valueField, kind: "auto", maxGroups: 5, showOther: true, showLegend: true, height: 300 } };
  if (recipe.id !== "usage-work") return recipe;
  const summaries: SummaryColumn[] = [{ id: "card_total_tokens", op: "sum", field: "total_tokens", label: "Period tokens" }, { id: "card_work_title", op: "row", field: "root_title", label: "Top-level conversation" }, input, output, { id: "card_agent_session_id", op: "count_distinct", field: "agent_session_id", label: "Recorded runs" }];
  return { ...recipe, days: [7, 30], title: "Largest token workloads", description: "Top-level conversation token totals including recorded delegated work over the last {days} days, ranked by period volume.", where: roots, action: "Inspect large token workloads", note: `${recorded} ${whole} Rank top-level conversation totals and use the root workspace title. Retain input, output, and distinct recorded run IDs for inspection; run count does not establish parallelism.`,
    exploration: { keys: rootKeys, summaries, valueField: "card_total_tokens", unit: "tokens", subjects: "top-level conversations", action: "Inspect large token workloads", labelFields: ["card_work_title", "root_project_name"], representative: latest },
    cover: { mode: "result", metricId: "card_total_tokens", kind: "bar-horizontal", maxGroups: 6, showOther: false } };
}
