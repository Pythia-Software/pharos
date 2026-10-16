import type { SummaryColumn, WhereTerm } from "@pythia-software/query-table-core";
import type { QueryRecipe } from "./curiosity-data";

const sum = (id: string, field: string, label: string): SummaryColumn => ({ id, op: "sum", field, label });
const distinct = (id: string, field: string, label: string): SummaryColumn => ({ id, op: "count_distinct", field, label });
const present = (field: string): WhereTerm => ({ field, op: "is_not_null", value: "" });
const nonempty = (field: string): WhereTerm[] => [present(field), { field, op: "!=", value: "" }];
const failed: WhereTerm = { field: "error_count", op: ">", value: "0" };
const calls = sum("card_call_count", "call_count", "All recorded calls");
const failures = sum("card_error_count", "error_count", "Error calls");
const count: SummaryColumn = { id: "card_count", op: "count", label: "Error calls" };
const conversations = distinct("card_conversation_id", "conversation_id", "Agent conversations");
const rate: SummaryColumn = { id: "card_rate", op: "formula", expression: "SUM([error_count])/NULLIF(SUM([call_count]),0)", label: "Recorded error-call rate" };
const population = "Each tool-call row is one recorded call, selected by its start time. error_count is one for status=error and zero otherwise. Recorded errors include rejected, interrupted, blocked, and timed-out calls as well as execution errors; these are not automatically mistakes or avoidable failures. Calls with no recorded result remain in all-call denominators and are not asserted errors. Counts do not measure wasted time, model quality, or whether the overall task succeeded.";
const signatureNote = "Signatures are retained normalized diagnostic excerpts, not semantic root-cause labels. The same text can have different causes; different text can describe the same cause. Blank/missing signatures are excluded. Conversation counts use recorded agent transcripts, not whole delegated trees; missing IDs are excluded from distinct counts but their recorded error calls can still contribute.";
const copy: Record<string, { title: string; description: string; action: string; note?: string; emptyLabel?: string }> = {
  "tool-failures": { title: "Error volume by tool", description: "Recorded error calls started in the last {days} days, split by tool.", action: "Inspect tool error calls" },
  "tool-failure-rates": { title: "Tool failure rates", description: "Error calls divided by all calls started in the last {days} days; tools with ten or more calls.", action: "Compare failure rates", note: "Divide summed error_count by summed call_count, not an average of per-conversation rates. Require ten total calls and at least one error, then show six highest rates. All recorded calls, including no-result calls, remain in the denominator. Numerator, denominator, and affected-conversation counts remain inspectable." },
  "error-signatures": { title: "Recurring error signatures", description: "Diagnostic signatures on at least two error calls started in the last {days} days, with conversation reach.", action: "Inspect recurring error evidence", note: signatureNote },
  "error-reach": { title: "Errors across repositories", description: "Signatures shared by at least two named repositories over the last {days} days, ranked by repository reach.", action: "Compare cross-repository errors", note: signatureNote },
  "cross-repo-errors": { title: "Repository error hotspots", description: "Repository/signature pairs ranked by recorded error calls started in the last {days} days; counts, not rates.", action: "Explore repository error evidence", note: signatureNote },
  "edit-failures": { title: "Failed edit paths", description: "Error-marked edit calls started in the last {days} days, grouped by repository and mapped relative path.", action: "Inspect failed file edits", emptyLabel: "No recorded edit errors with mapped paths", note: "Only edit-category calls with a mapped, nonempty repository-relative path contribute. Identical paths in different repositories stay separate. Unmapped paths are excluded. Expanding a group shows the recorded calls and error types; counts do not establish that edits were ultimately abandoned." },
  "test-failures": { title: "Reported test failures", description: "Calls with a recorded test-failure signal started in the last {days} days, grouped by repository and command.", action: "Inspect reported test failures", note: "Select test_failure=true independently of tool error status. The flag is a retained output/classification signal, not a count of failed test cases, a complete test-run census, or proof that a test later passed. A flagged call may have status=ok; an error call may have no test-failure signal. No pass rate is inferred. Missing command names remain Unspecified." },
  "skill-load-failures": { title: "Failed skill calls", description: "Distinct parent calls with error-marked skill evidence during the last {days} days, grouped by skill and provider.", action: "Inspect failed skill evidence", note: "Count distinct nonmissing tool_call_id among status=error skill evidence; duplicate evidence for one skill/provider counts once. Evidence can describe an explicit invocation or a file load. One parent call mentioning several skills can appear under each skill, so group counts are not additive. Missing call IDs are excluded; evidence rows and affected conversations remain inspectable. This does not measure instruction compliance." },
  "environment-failures": { title: "Setup and access errors", description: "Recorded package/build/container errors or missing-file/permission errors in the last {days} days, grouped by repository and error type.", action: "Inspect setup and access calls", note: "Uses recorded command_category in package/build/container OR error_type in permission_denied/file_not_found, restricted to error calls. A call matching both contributes once. Build errors can be code failures, and missing files can be ordinary typos. This is a diagnostic slice, not a confirmed environment diagnosis or an exposure-normalized rate." },
  "repeated-failed-commands": { title: "Repeated failed commands", description: "Exact commands with at least two error calls in one agent conversation over the last {days} days.", action: "Inspect repeated command attempts", emptyLabel: "No repeated command errors within one conversation", note: "Only command-category tool calls contribute, including run, read, test, build, and other command categories. Group by repository, exact retained command text, and recorded agent conversation ID; require two failed calls inside the window. Missing/empty commands and conversation IDs are excluded. The count includes the first failed call. Failures need not be consecutive; successful calls or edits may intervene. Commands with different whitespace or arguments remain distinct. Summed call durations are not wall-clock delay or wasted time. This is not a measured retry chain, proof of a loop, or a count of failed tests." },
};

export const errorPatternRecipes: QueryRecipe[] = [
  { id: "repository-failure-rates", idea: "tools-01", category: "errors", dataset: "tool_calls", field: "started_at", days: [7], unit: "percent", measures: [],
    title: "Repository failure rates", description: "Error calls divided by all calls started in the last {days} days; repositories with ten or more calls.", action: "Compare repository failure rates", note: `${population} Ratios use all recorded calls in each repository, not averages of tool or conversation percentages. Require ten calls; include zero-error groups, and show six highest rates. Missing repository labels remain Unspecified. Different tool mixes and recording coverage can explain differences.`,
    exploration: { keys: ["repository_name"], summaries: [rate, failures, calls, conversations], having: [{ field: calls.id, op: ">=", value: "10" }], valueField: rate.id, unit: "percent", subjects: "repository groups", action: "Compare repository failure rates", labelFields: ["repository_name"], context: [{ field: failures.id, label: "errors" }, { field: calls.id, label: "calls" }] },
    cover: { mode: "result", metricId: rate.id, kind: "bar-horizontal", maxGroups: 6, showOther: false },
  },
  { id: "conversation-error-counts", idea: "tools-02", category: "errors", dataset: "tool_calls", field: "started_at", days: [7], unit: "errors", where: [failed, ...nonempty("conversation_id")], measures: [],
    title: "Failures per conversation", description: "Error calls per affected agent conversation over the last {days} days; tools with three or more affected conversations.", action: "Explore conversation error counts", note: `${population} One observation is the error-call count for one tool in one recorded agent conversation. Only affected conversations contribute; zero-error conversations are excluded. A conversation using several failing tools contributes one observation under each. Require three affected conversations per tool before selecting six tools by observation count. All individual affected conversations remain inspectable, including tools below the cover cutoff. Boxes share a logarithmic count axis with quartiles, median, Tukey whiskers, sampled outliers, and extremes. Counts do not establish consecutive retries or recovery.`,
    exploration: { keys: ["tool_name", "conversation_id"], summaries: [count], valueField: count.id, unit: "errors", subjects: "affected tool/conversation groups", action: "Explore conversation error counts", labelFields: ["tool_name", "conversation_id"],
      coverMetric: { id: count.id, op: "count", field: count.id, expression: `MEDIAN([${count.id}])`, groupBy: ["tool_name"], groupLimit: 10000, sort: [{ key: "samples", dir: "desc" }], distribution: { kind: "box", input: `[${count.id}]`, whiskers: "tukey" }, label: "Error calls per affected agent conversation", display: { kind: "box", maxGroups: 6, xLabel: "Errors / affected conversation", xScale: { mode: "log" } } } },
    cover: { mode: "result", metricId: count.id, kind: "box", maxGroups: 6, density: "compact", minSamples: 3 },
  },
];

export function refineErrorPatternRecipe(recipe: QueryRecipe): QueryRecipe {
  const text = copy[recipe.id];
  if (!text) return recipe;
  const view = recipe.exploration!;
  let next: QueryRecipe = { ...recipe, ...text, days: [7], note: `${recipe.dataset === "tool_calls" ? population : ""} ${text.note ?? ""}`.trim(),
    exploration: { ...view, action: text.action },
    cover: { mode: "result", metricId: view.valueField, kind: "bar-horizontal", maxGroups: 6, showOther: false },
  };
  if (recipe.id === "tool-failures") next = { ...next,
    exploration: { ...next.exploration!, coverMetric: { id: view.valueField, op: "sum", field: view.valueField, groupBy: view.keys, groupLimit: 10000, label: "Recorded error calls", display: { kind: "pie" } } },
    cover: { mode: "result", metricId: view.valueField, kind: "pie", maxGroups: 5, showOther: true, showLegend: true, showHeadline: false },
  };
  if (["error-signatures", "error-reach", "cross-repo-errors"].includes(recipe.id)) next.where = [failed, ...nonempty("error_signature")];
  if (recipe.id === "edit-failures") next.where = [...recipe.where!, ...nonempty("repo_path")];
  if (recipe.id === "test-failures") next.exploration = { ...next.exploration!, keys: ["repository_name", "command_name"], labelFields: ["command_name", "repository_name"] };
  if (recipe.id === "skill-load-failures") {
    const parentCalls = distinct("card_tool_call_id", "tool_call_id", "Distinct failed parent calls");
    next = { ...next, unit: "calls", where: [...recipe.where!, ...nonempty("tool_call_id")],
      exploration: { ...next.exploration!, summaries: [parentCalls, { ...count, label: "Error-marked evidence records" }, conversations], valueField: parentCalls.id, unit: "calls" },
      cover: { ...next.cover!, metricId: parentCalls.id },
    };
  }
  if (recipe.id === "repeated-failed-commands") next = { ...next, where: [failed, { field: "tool_category", op: "=", value: "command" }, ...nonempty("command"), ...nonempty("conversation_id")],
    exploration: { ...next.exploration!, keys: ["repository_name", "command", "conversation_id"], labelFields: ["command", "repository_name", "conversation_id"], summaries: [count, { id: "card_duration_ms", op: "sum", field: "duration_ms", label: "Sum of recorded call durations (ms)" }], context: undefined },
  };
  return ["repeated-failed-commands", "environment-failures", "skill-load-failures"].includes(recipe.id) ? { ...next, retired: true } : next;
}
