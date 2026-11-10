# Query Table: log axes that draw the smallest value, round ticks, log-spaced histogram bins

Status: blocked on an upstream published release. Pharos pins the four Query
Table npm packages and the Go module to 1.0.4. Pharos's dashboards choose a log
axis by default for long-tailed values ([Default views](../dashboard-analysis.md#default-views));
these three changes make that axis read correctly in Query Table's native
charts. Pharos must not patch around them locally.

## Evidence

From a 30-day survey of a real library (every dashboard, default views):

- **Compare on a log axis** (Tool Calls by tool, Tool Summary by category,
  Pharos MCP by tool): the 12 largest groups span 10²–10⁵. The smallest bar
  always has zero length, so it reads as no calls at all (`send_message`,
  `user`, `list_repositories` in the screenshots).
- **The same axes' ticks** read `1,952 · 15,083 · 116,541` and `50 · 292 ·
  1,706`: the minimum, the geometric midpoint and the maximum. None is a round
  value, and a reader cannot tell the axis is logarithmic from them.
- **Histograms** (Distribution, no breakdown) of tokens, durations and
  counts: the first of 12 evenly spaced bins holds 72–100% of the records.
  A log axis cannot fix this, since the bins stay evenly spaced in the value,
  so Pharos draws these histograms linear by default and only boxes (by a
  breakdown) on a log axis.

## Requested behavior

1. **A log axis's inferred domain starts below the smallest positive value**:
   at the power of ten at or below it (or one 1-2-5 step below, if that
   leaves too much empty room), so the smallest bar has a visible length.
   Explicit `MetricAxisScale.min` keeps its meaning.
2. **Log ticks at round values**: powers of ten within the domain, or 1-2-5
   steps when fewer than two powers fit, as linear axes' nice ticks are
   round. Label the axis as logarithmic where the chart already notes
   omitted nonpositive values.
3. **Log-spaced histogram bins**: `MetricDistribution` for
   `kind: "histogram"` accepts a bin spacing, for example
   `binScale?: "linear" | "log"`, whose edges are geometric between the
   smallest positive value and the maximum, with nonpositive values counted
   in their own leading bin (or omitted and reported as the log axis does).
   A histogram with `xScale.mode === "log"` should use it by default, or
   reject the combination with a reason rather than draw stretched linear
   bins. Grouped histograms share edges as they do now.

## Confirmed upstream edit points

Inspected in the installed 1.0.4 distributions (source paths from their
source maps):

- `query-table-ui` `src/metricChartHelpers.ts`, `metricScale`: for
  `mode === "log"` the domain is `[min(valid), max(valid)]` (lines ~279–284),
  so the minimum maps to fraction 0; ticks are
  `[min, exp(mean(log min, log max)), max]` (~307–309).
- `query-table-core` `histogramEdges(values, bins)` (dist `index.js`, ~3838)
  builds evenly spaced edges; `histogramCounts` uses them. The `bins`
  validation (2–30) is in the clause checks (~5484).
- `query-table-core` `MetricDistribution` in `index.d.ts`:
  `{ kind, input, whiskers?, bins? }`.

## Done when

A Query Table release has these. Then Pharos pins it everywhere
(npm packages and Go module), and in `web/src/dashboard-analysis-model.ts`
`suggestedView` lets a long-tailed histogram without a breakdown take a log
axis (the Distribution branch now keeps it linear), with
`dashboard-catalogs.test.mjs` and `docs/dashboard-analysis.md#default-views`
updated.
