# Query Table 1.0.0 follow-up from Pharos

Pharos consumes unmodified core/react/ui/codegen 1.0.0 and Go v1.0.0. Current
remote consumers use V3 throughout. No local dependency patches are permitted.

## Numeric metric renderers

`query-table-ui`'s `formatMetricValue` still calls a numeric measure's custom
renderer only when its key is in `UNIT_RENDER_KEYS` (`duration_ms`, `byte_size`).
Pharos uses custom `usd` and compact token renderers. Its host adapter clones
those field definitions with `render: "byte_size"`, then dispatches back to the
correct presentation via object identity (`web/src/index.tsx`, `metricFields`
and `metricRenderers`). Core's numeric display/format definitions do not expose
a currency format that would preserve this presentation directly.

Please publish a supported custom numeric metric formatter or recognize a
measure's registered renderer consistently for scalar metrics, grouped charts
and totals. Counts should retain their counting presentation; NULLs/errors and
explicit metric display formats must keep their existing precedence. A release
with that behavior would let Pharos delete the field alias and identity maps.

## Complete authorized identity plans for host keyword verification

Pharos verifies an individual record's command/text with host search semantics.
It must apply the complete typed source WHERE first: a `CASE WHEN identity IN
(typed source plan) THEN host_keyword_match(text) ELSE 0 END` prevents SQLite
from verifying excluded records with oversized text or expensive expressions.
The old public computed-row compiler could build this identity relation without
a page/population cap. In 1.0.0 that compiler is private, while `SourcePlan`
sets a `MaxPopulation+1` LIMIT, even for this use of a complete identity relation.
`QueryDatasetV3` caps MaxPopulation at ten million.

Pharos uses the public planner and counts its bounded relation before using it.
Above the budget it reports `resource_limit`, rather than silently excluding
eligible identities. Please publish a complete, unpaged authorized source-plan
API (or an executor hook that guarantees typed predicates run before a host
record verifier), including computed revision checks, bound arguments, NULLs,
OR-of-AND filters and the request's evaluation clock. Do not require consumers
to remove LIMIT from compiled SQL or copy private compiler code.

Acceptance: a host population larger than the staging budget can be narrowed
by keyword to a small exact result, while excluded oversized records are never
verified; schema validation, expected revisions, identity and argument binding
remain library-owned.

## Distinct source-plan dependencies

`QueryDatasetV3.SourcePlan` does not include `Distinct.Field` in its base-field
dependency discovery. Pharos stages projected map populations, so a request
with `rows: false`, an empty result selection and a distinct target outside
source predicates otherwise stages only identity and returns no values.
Pharos discovers dependencies by planning an equivalent row selection of the
distinct target with `source.where`, then executes the original distinct
request unchanged (`queryV3SourceFields` in `query_table_v3.go`).

Please include the distinct target and its computed dependencies/revisions in
the public source plan. Acceptance: a distinct-only request over an otherwise
unselected base or computed field stages every required input, applies source
predicates and returns the same distinct values as an unprojected source.

## Wrapping host links in screen fitting

The existing [wrapping-link fitting report](query-table-fit-wrapping-links.md)
remains reproducible with 1.0.0. Pharos's structural browser sweep marks
`Analyze workspace query usage @ 1440` as a known TODO: an ordinary host anchor
whose title allows wrapping still sets an unshrinkable one-line control floor.
Fresh views now clear explicit widths as the native Fit All to Screen action
does, so this is independent of pinned column sizing. The library's measuring
code is unmodified. Please support wrapping links without treating their full
one-line label as the minimum, while retaining usable button minimums.
