# Query Table: honor colorBy for compound pie and donut slices

Request against published Query Table **1.0.5** (all four npm packages and
`backends/go` v1.0.5), dated 2026-11-10. Pharos consumes published releases;
it does not patch installed packages or renderer bundles.

## Problem

Pharos persists a project color per stable identity. Its host theme supplies
`dimensions.project_name.overrides` and `colorBy: "project_name"`, so a project
keeps its color in bars, boxes, list rows, trends and card previews. The 1.0.5
public `MetricTheme` contract explicitly excludes compound pie/ring slices
from `colorBy`: these still color each whole key tuple. Single-field project
pie/donut charts already honor their dimension overrides.

For example, grouping by project and workspace draws two slices for the same
project in different colors, even though the requested coloring dimension is
the project. This is a remaining limitation of the project-color integration,
not an invitation to alter tuple identities or collapse the groups locally.

The same blocker applies to model/provider overrides in Settings → Colors → Model &
provider colors. For example, compound slices grouped by `model_family` and
`provider` must retain the model shade across several provider tuples. Include
those dimensions in the upstream resolver coverage as well.

## Minimal reproduction

Render the same clause/result first as a pie, then as a donut:

```ts
const clause = {
  id: "workspaces",
  op: "count",
  groupBy: ["project_name", "workspace_name"],
  display: { kind: "pie", colorBy: "project_name" },
};
const result = {
  buckets: [
    { keys: ["app", "feature"], value: 12 },
    { keys: ["app", "review"], value: 4 },
    { keys: ["notes", "draft"], value: 8 },
  ],
};
const theme = {
  colorBy: "project_name",
  dimensions: {
    project_name: {
      layer: "projects",
      overrides: [
        { value: "app", color: "#2a83a4" },
        { value: "notes", color: "#b27430" },
      ],
    },
  },
};
```

## Requested behavior

Honor the same coloring dimension selection for compound pie/donut slices as
for bars/boxes/list rows. Respect a clause's explicit `display.colorBy`, then
the host's `MetricTheme.colorBy` priority list and dimension identities. Both
`app` slices should use `#2a83a4`, with their separate labels, values, tuple
identities, inspection targets and accessible descriptions intact. Legends
and tooltips must agree with the slices. Keep the existing exact Other value
and neutral color. Without a coloring dimension, preserve whole-tuple colors.

Add renderer/resolver coverage for pie and donut, explicit clause precedence,
host priority lists, null keys and repeated project values across several
tuples. Publish a release with the updated public contract. Pharos can then
upgrade the four npm packages and Go module together, rebuild, and remove
this documented limitation without a local renderer workaround.
