# Query Table: colour a grouped bar by one of its keys

**Resolved in Query Table 1.0.5**: `MetricTheme.colorBy` and a chart's saved `display.colorBy` colour bars, boxes and list rows grouped by several keys by one named key.

For the Query Table team. This is a self-contained request against the published **1.0.0** release (Go module
`backends/go` v1.0.0 and the npm core, react, ui and codegen packages at 1.0.0), dated **2026-11-07**. Pharos will
consume it through a published release; it does not patch Query Table locally.

## Context

Pharos draws a bar chart of a metric grouped by two fields, through `@pythia-software/query-table-ui`'s metric
renderer. For example, Agent Events shows events per 1,000 messages grouped by `repository_name` and `family` (the
kind of event), giving bars such as "textkit / Out-of-date docs" and "pharos / Pre-existing or flaky failure".

The kinds have a fixed palette: each kind of event has a colour that Pharos uses everywhere else, in the reader, the
trend and the legend. The host passes it as a `MetricTheme` dimension with `overrides` on `family`.

## What is missing

A grouped bar's colour comes from `tupleColors(fields, tuples)`. That function hashes the whole tuple
(`identity\0tupleKey`) into the first field's layer and ignores every field's `overrides`.

As a result, the same kind gets a different colour on each repository's bar, and the host's palette for `family` has
no effect. A single-field grouping by `family` does honour the overrides (through `color(field, value)`).

## Request

Let a theme name the field whose value colours a grouped bar.

- One option: a `colorBy` (or `colorField`) on `MetricTheme`, or on the clause's display, naming one of the
  `groupBy` fields.
- When it is set, a tuple's colour is `color(colorBy, values[groupBy.indexOf(colorBy)])`, so that field's
  `overrides`, `domain` and layer apply.
- Without it, tuples keep today's hashing.
- The legend, hover card and any "Other" bar should keep their current behaviour.

## Evidence

On the Agent Events dashboard (Compare, repository × friction kind), the twelve bars show eight kinds across nine
repositories. "Missing tool or capability" is drawn in three different colours on three rows, and "Environment
friction" in three. With the request, every bar of one kind takes that kind's colour (for example `--event-friction`
shades), as the legend and trend already do.

## Done when

- A theme or display option names the field that colours grouped bars.
- The UI renderer applies that field's overrides to each tuple, with tests for a two-field grouping.
- The option is documented in the integration guide.

Pharos will then set it in the Agent Events dashboard and card covers (`web/src/dashboard-analysis.tsx`,
`web/src/chart-colors.ts`) and pin the release.
