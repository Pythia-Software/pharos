import type { QueryRecipe } from "./curiosity-data";
const base = {
  dataset: "model_requests",
  field: "requested_at",
  days: [7, 30, 90],
  category: "models",
  idea: "models-01",
  unit: "seconds",
} as const;
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
    thinking: { state: { view: "histogram", groupBy: "none" } },
    action: "Explore Thinking",
    note: "Same request population as Thinking. The compact native histogram uses automatic bins; the full dashboard uses shared rounded logarithmic edges. Neither measures pure inference or first-token latency.",
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
    thinking: { overview: true, state: {} },
    action: "Explore Waiting and Compaction",
    note: "Includes missing timestamps, stale anchors, cross-stream links and excluded durations. The overview explains how timing is reconstructed and provides source-specific validation.",
  },
];
