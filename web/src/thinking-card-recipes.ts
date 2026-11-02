import type { QueryRecipe } from "./curiosity-data";
const base = {
  dataset: "model_requests",
  field: "requested_at",
  days: [7, 30, 90],
  category: "thinking",
  idea: "models-01",
  unit: "seconds",
} as const;
// Cards open Waiting and Compaction (overview: true) for the whole-library
// questions, and Thinking for one comparison, already in its view.
export const thinkingCardRecipes: QueryRecipe[] = [
  {
    ...base,
    days: [...base.days],
    id: "thinking-wait-distribution",
    title: "Model wait distribution",
    description:
      "Post-tool request gaps over the last {days} days. Missing timing stays missing.",
    measures: [
      {
        id: "waits",
        display: { kind: "histogram" },
        op: "count",
        groupBy: [],
        distribution: { kind: "histogram", input: "[gap_seconds]", bins: 16 },
        label: "Observed post-tool wait",
      },
    ],
    thinking: { overview: true, state: {} },
    action: "Explore Waiting and Compaction",
    note: "Same request population as Waiting and Compaction, which adds the typical wait, the slowest tenth, per-model curves and what each wait is associated with. The compact native histogram uses automatic bins. Neither measures pure inference or first-token latency.",
  },
  {
    ...base,
    days: [...base.days],
    unit: "compactions",
    id: "thinking-compactions",
    title: "Context compactions",
    description:
      "Requests that follow a context compaction, day by day by model, over the last {days} days.",
    where: [{ field: "compacted_before", op: "=", value: "true" }],
    measures: [
      {
        id: "compactions_daily",
        op: "count",
        groupBy: ["day", "model"],
        groupLimit: 10000,
        label: "Compactions by day",
      },
    ],
    cover: { mode: "source", metricId: "compactions_daily", kind: "auto", maxGroups: 6, showOther: true, showLegend: true, height: 240 },
    thinking: { overview: true, state: {}, open: "thinking:conversation", coverWhere: true },
    action: "Explore Waiting and Compaction",
    note: "One compaction is a request whose stream compacted its context since the previous request. Waiting and Compaction pairs the waits, context and output before and after each compaction within one stream, model and effort.",
  },
  {
    ...base,
    days: [...base.days],
    id: "thinking-model-effort",
    title: "Wait by model and effort",
    description:
      "Observed wait distributions by recorded model and effort over the last {days} days.",
    measures: [
      {
        id: "wait_efforts",
        display: { kind: "box" },
        op: "count",
        groupBy: ["model_effort"],
        distribution: {
          kind: "box",
          input: "[gap_seconds]",
          whiskers: "tukey",
        },
        label: "Wait by model and effort",
      },
    ],
    thinking: { state: { view: "wait-box", groupBy: "model_effort" } },
    action: "Explore Thinking",
    note: "Individual post-tool gaps. Compact native boxes use Tukey whiskers; Thinking uses p10–p90 whiskers. The full dashboard keeps every model/effort pair and exposes support counts.",
  },
  {
    ...base,
    days: [...base.days],
    id: "thinking-preceding-tool",
    title: "Wait by preceding tool",
    description:
      "Observed waits after each tool's result, by the tool that ran, over the last {days} days.",
    measures: [
      {
        id: "wait_tools",
        display: { kind: "box" },
        op: "count",
        groupBy: ["preceding_tool"],
        distribution: {
          kind: "box",
          input: "[gap_seconds]",
          whiskers: "tukey",
        },
        label: "Wait by preceding tool",
      },
    ],
    cover: { mode: "source", metricId: "wait_tools", kind: "box", maxGroups: 8, density: "compact", minSamples: 20 },
    thinking: { state: { view: "wait-box", groupBy: "preceding_tool", sortBy: "median", sortDirection: "desc" } },
    action: "Explore Thinking",
    note: "The gap from a tool result to the next response, grouped by the tool whose result arrived last. A large result adds context to read, so tools differ in more than speed. The cover keeps tools with at least 20 timed requests; Thinking shows every tool with its support counts.",
  },
  {
    ...base,
    days: [...base.days],
    unit: "tokens",
    id: "thinking-reasoning-effort",
    title: "Reported reasoning by effort",
    description:
      "Positive reasoning reports across models and effort in the last {days} days, including requests without timing.",
    measures: [
      {
        id: "reasoning_efforts",
        display: { kind: "box" },
        op: "count",
        groupBy: ["model_effort"],
        distribution: {
          kind: "box",
          input:
            "IF([reasoning_output_tokens]>0,[reasoning_output_tokens],NULL)",
          whiskers: "tukey",
        },
        label: "Positive reasoning tokens",
      },
    ],
    thinking: { state: { view: "reasoning", groupBy: "model_effort" } },
    action: "Explore Thinking",
    note: "Only positive reports; zero cannot distinguish no reasoning from unreported reasoning. The full dashboard reports retained and eligible populations separately.",
  },
  {
    ...base,
    days: [...base.days],
    unit: "requests",
    id: "thinking-timing-coverage",
    title: "Timing evidence coverage",
    description:
      "Retained requests grouped by timing evidence over the last {days} days.",
    measures: [
      {
        id: "timing_quality",
        display: { kind: "bar-horizontal" },
        op: "count",
        groupBy: ["timing_quality"],
        label: "Timing evidence coverage",
      },
    ],
    thinking: { overview: true, state: {}, open: "thinking:coverage" },
    action: "Explore Waiting and Compaction",
    note: "Includes missing timestamps, stale anchors, cross-stream links and excluded durations. The overview explains how timing is reconstructed and provides source-specific validation.",
  },
];
