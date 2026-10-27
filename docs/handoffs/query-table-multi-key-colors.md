# Query Table: color multi-key groups by their whole key

## Request

In `@pythia-software/query-table-ui`, `MetricCard`'s chart renderer colors
each mark by the bucket's first key only. The code is
`shade = b => colors.color(clause.groupBy[0], b.keys[0])`, in `MetricChart`.

A pie, bar or box chart grouped by two fields, such as
`["provider", "model_family"]`, therefore gives every group with the same
first key the same color:

- adjacent pie slices for two Claude models are indistinguishable;
- the legend repeats one color for each Claude group.

Please make one of these changes and publish it in a release:

- color by the whole key tuple for every kind that draws one mark per group
  (pie, donut, bar, box, histogram);
- or add a `MetricDisplay.colorBy` option: `"first"` (today's behavior),
  `"all"` (whole tuple), or a field name.

Scatter can keep coloring by the first key: there, the first key is the
series and the second is the point.

## Why Pharos needs it

Tokens and Cost's Compare view offers a "Provider + model" breakdown because
cards key models by provider and family; see
[`docs/dashboard-analysis.md`](../dashboard-analysis.md). The share display of
that breakdown draws same-provider slices in one color.

## Acceptance

- A pie over `["provider", "model_family"]` with buckets
  `claude/opus`, `claude/sonnet` and `codex/gpt-5` gives three distinct
  colors.
- Colors stay stable for the same tuple across renders.
- Single-key charts are unchanged.
