# Query Table: native Detail for grouped query results

**Delivered in Query Table 0.9.7** as native Detail for grouped results
(`api.itemDetails` with `subject: "group"`). Pharos adopts it in a later
change; grouped tables keep their Records drill-down until then.

The original assignment below describes the **0.9.5** implementation.

Pharos uses the published Query Table **0.9.5** release for all four npm
packages and the Go module. No dependency or generated-bundle patches are
permitted in this repository.

## User-visible requirement

Every Analyze query table should end with separate **Drill in** destination
columns followed by native **Detail**. Ordinary records now use public derived
fields and renderers for each destination, with explicit source dependencies
and the native `ITEM_DETAILS_COLUMN` last. This also works with narrow saved
data projections. Destination links retain ordinary anchor behavior.

Grouped queries still expose their native link-summary columns and a Records
drilldown, but cannot show native Detail. Query Table 0.9.5 deliberately disables
item details for grouped rows in both `useQueryTable` and `DataTable`:

```ts
// React controller
const detailsEnabled = Boolean(opts.itemDetails) && !query.grouping;
// UI
const details = query.grouping ? undefined : props.itemDetails;
```

The relevant Pharos integration is `QuerySurface` in `web/src/index.tsx`.
Its grouped Records action uses `groupSourceQuery` to preserve source filters,
computed fields and exact key matching. It explicitly sorts the destination:
chronological within one conversation/work/unit, newest first across records.

## Requested upstream behavior

Support the native Detail presentation column and modal/inline property views
for a grouped result. Its properties should use the effective grouped schema
and show the selected group's keys, scalar summaries and link summaries.
Do not load a source item using the group ID. Preserve source record details
when returning from a group to records, including saved layouts and active-row
URL behavior. Detail should be orderable after the drilldown/link columns using
the existing select API.

Resolve how grouped active-row identity survives paging and reload using the
library's canonical group identity, with an unavailable-group state if the
population changes. Preserve exact-match safety for Records; displaying group
properties must not broaden its source query.

## Reproduction and acceptance

1. In Pharos, open Analyze → Queries → Human messages.
2. Group by conversation/work, adding count and word-total summaries.
3. Observe that group properties have no native modal/inline Detail action,
   even when item details are configured on the table.
4. After the change, open group Detail, navigate the properties, dismiss it
   and verify focus returns to its opener. Repeat after paging and URL reload.
5. Check link summaries remain independent columns and Detail can appear last.
6. Follow Records for a single work: preserve all source filters and order by
   message timestamp ascending. Across works, use descending message timestamp.
7. Cover missing groups and null keys, and retain ordinary item Detail behavior.

Publish an aligned npm/Go release. Pharos can then upgrade all five pins,
regenerate schemas, rebuild the ignored frontend and enable grouped Detail
through the supported API.
