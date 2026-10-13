# MCP metric search: first-draft proposal

Status: implemented initial contract; usage and operational limits are documented
in [MCP statistics](mcp-statistics.md). Both conversation usage and
workspace totals including subagents are required in the first draft. The
recommended default for conversation search is the conversation's own agent
stream; callers can explicitly select either scope.

## Recommendation

Extend `search_conversations` and `search_work` with a shared, bounded metric
filter and sort contract. `search_conversations` already accepts an omitted or
empty text query, so it can also answer metric-only discovery requests. Support
an explicit metadata result mode in both tools. Add `query_statistics` as the
recommended entry point for bulk metadata and aggregate questions, backed by
the same metric engine. Add `list_metrics` to make names, definitions, units,
scope, and limitations discoverable.

Metadata queries are a first-draft requirement: an agent should be able to
retrieve workspace or conversation IDs and selected statistics without titles,
previews, snippets, or transcript retrieval. Row identity, metric scope, and
response detail are independent choices. A workspace result is useful on its
own, and conversation IDs provide optional handles for later inspection.

Examples this should enable:

- Find the most expensive conversations in a repository.
- Search for authentication work that used over 100,000 tokens.
- Find conversations with at least five tool failures and two compactions.
- Find long conversations with few user turns.
- Find conversations without complete token usage, including partial evidence
  (`is_missing` with the default `metric_quality: "complete"`).
- Retrieve IDs, cost, and token usage for hundreds of workspaces without prose.
- Retrieve conversation IDs associated with those workspaces for follow-up.
- Count matching workspaces or compare average cost across repositories without
  sending all matching rows to the model.

## What exists today

- [`mcp.go`](../internal/archive/mcp.go) advertises `query_metrics`, which reads
  one named metric with optional minimum and maximum bounds. Its SQL selects
  valued rows descending and then appends missing-value rows when there is room,
  but subsequent mirror suppression discards that value ordering. It returns
  workspace handles, has no repository/date/model filters, pagination, or
  metric-name discovery, and retains missing values.
- The same file passes `metric`, `minimum`, and `maximum` to `search_work`, but
  does not advertise them in that tool's schema. This filters metric records
  associated with a workspace, including any conversation-specific records; it
  does not provide one canonical, explicitly scoped value.
- [`mcp_conversations.go`](../internal/archive/mcp_conversations.go) supports
  conversation discovery with or without text, contextual filters, bounded
  cards, offsets, and mirror collapse. It has no numeric filters or metric
  sorting. Its cards already include message count.
- [`library.go`](../internal/archive/library.go) exposes substantially more
  workspace metrics than the generic `metrics` table: cost, turns, tools,
  compactions, subagents, files, and PRs. Reusing the generic table alone would
  miss much of the useful data.
- `Catalog.Search` in [`catalog.go`](../internal/archive/catalog.go) completes
  workspace results through `libraryPage`, which reads full Library rows,
  attaches costs, and fills first-input/latest-response previews. Conversation
  search calls `conversationCards` to attach descriptive cards and snippets.
  A metadata path must bypass those hydration steps, rather than strip their
  output after paying their read and serialization costs.
- [`usage_table.go`](../internal/archive/usage_table.go) already reads reconciled
  usage by agent session and conversation and applies historical pricing.
  [`tool_ledger.go`](../internal/archive/tool_ledger.go) classifies calls and
  results, including command exits, interruptions, timeouts, and MCP errors.

`query_metrics` has several existing correctness traps to avoid copying. Its
SQL selects metric records without choosing one canonical scope/version.
`suppressMirrors` then keys those records by workspace ID: the last row read
for each workspace wins, so duplicate results do not survive, but the surviving
value need not represent the intended scope/version. For a workspace with valued
rows 40 and 2, the descending SQL read leaves 2 as the surviving value; a minimum
of 3 excludes that row and leaves 40. Equal-valued records and fallback rows
without an explicit order can also make the surviving metadata depend on read
order. Duplicate rows consume SQL `LIMIT` slots before collapsing, so a page can
contain fewer unique workspaces without refilling.

The same suppression pass re-sorts by score, activity, and workspace ID. Because
`query_metrics` does not select score or activity, its final results are in
workspace-ID order, not metric-value order; null-valued rows can precede valued
ones despite the tool's note promising missing values last. Its fallback can
also return a workspace with an observed value outside the requested bounds as
a null-valued result, or overwrite a selected valued row with a stored null row
for another scope/version. These semantics need separate compatibility treatment,
not reuse as the new filter engine.

## Initial metrics

Prioritize seven useful measures; expose three token breakdowns from the same
usage accounting in the initial draft as well.

| Public name | Unit | Definition and purpose |
| --- | --- | --- |
| `cost_usd` | USD | Historical API list-price equivalent using the existing reconciled usage and price book. Finds expensive work; always carries `price_status`. |
| `total_tokens` | tokens | Reconciled token consumption. Finds large runs; cumulative checkpoints and request totals must not be added together. |
| `user_turn_count` | count | Retained user messages of kind `message`, assigned to the selected scope. A measure of interaction length, including harness-generated user prompts. |
| `tool_call_count` | count | Classified calls in the tool ledger, including delegation calls. One call counts once even when it contains multiple shell commands. |
| `tool_error_count` | count | Calls whose ledger status is `error`. This measures tool failures, not whether the task failed. |
| `compaction_count` | count | Attributed compaction events, using the existing Claude boundary and Codex trigger rules, without counting replayed events twice. |
| `wall_clock_span_seconds` | seconds | Valid recorded start-to-end span for the selected scope. Includes idle time; an unfinished or invalid interval is unknown. |
| `input_tokens` | tokens | Reconciled input, including cached input. Explains high total consumption. |
| `output_tokens` | tokens | Reconciled output, including reasoning where the source includes it. Do not add reasoning a second time. |
| `cache_read_input_tokens` | tokens | The cache-read portion of input. Helps explain differences between token count and cost. |

Source limitations must remain visible. For example, missing token breakdowns
do not become zero even when an aggregate total exists; compaction support
varies by source. A complete count over retained events still does not prove
the entire original conversation was captured. Preserve archive coverage too.

Defer error rates, cache ratios, subagent counts/depth, file edits, changed-file
counts, PR counts, test failures, peak context, and active execution duration.
They are worthwhile follow-ups, but introduce denominator, lineage, attribution,
or timing questions beyond the basic discovery contract. File and PR filters
already cover many artifact-oriented queries.

Do not expose `visible_errors` as a failure metric: its current derivation in
[`ingest.go`](../internal/archive/ingest.go) counts text containing `error`,
`failed`, or `failure`, including harmless discussion and quoted output.

## Scope and evidence

`search_conversations` defaults to `metric_scope: "conversation"`;
`search_work` defaults to `metric_scope: "workspace"` and initially supports
only that scope. Conversation search supports both scopes in the first draft.
Every returned metric states its scope and owning ID.

For example, these requests answer the two meanings of "conversations costing
more than $5":

```json
{
  "metric_scope": "conversation",
  "metric_filters": [{"name": "cost_usd", "op": "gt", "value": 5}]
}
```

```json
{
  "metric_scope": "workspace",
  "metric_filters": [{"name": "cost_usd", "op": "gt", "value": 5}]
}
```

Conversation scope means the conversation's own main agent stream. Embedded
delegates and separately archived child conversations are excluded consistently;
an archived child can be discovered as a conversation in its own right. Use
agent ownership, session-message mapping, and ledger attribution rather than
assuming that every event stored under a conversation belongs to its main agent.
If group-level usage cannot be attributed to an individual stream, report that
limitation instead of inventing a split or assigning it all to the parent.

Workspace scope means the current workspace's reconciled usage and retained
events across its conversations and agents. Reuse current-group membership and
mirror reconciliation. A workspace threshold can match several conversation
cards; mark the evidence as workspace-level and include the workspace handle.
Use `search_work` or `query_statistics` with `entity: "workspace"` when the
caller wants one result per workspace.

Keep the existing meaning of repository, model, effort, file, PR, and date
filters. Model/effort filters select matching conversations or workspaces; they
do not implicitly restrict a metric total to requests using that configuration.
Similarly, `from`/`to` selects entities using the existing tool-specific time
field. Metrics cover the entity's entire recorded lifetime, not just spending
inside that date window. Usage-window analytics is a separate future feature.

## Proposed request contract

Add these arguments to the advertised schemas and validate them at runtime:

```json
{
  "query": "authentication",
  "repository": "example-repository",
  "metric_scope": "conversation",
  "metric_filters": [
    {"name": "total_tokens", "op": "gte", "value": 100000},
    {"name": "tool_error_count", "op": "gte", "value": 5}
  ],
  "order_by": {"metric": "cost_usd", "direction": "desc"},
  "metric_quality": "complete",
  "limit": 8,
  "max_output_tokens": 2000
}
```

For metric-only discovery, omit `query`. Sorting alone is sufficient to request
the most expensive or longest conversations. With text and no explicit sort,
preserve relevance ranking. Without text and without explicit sort, preserve
the current recent-first order.

- `metric_filters`: at most eight predicates, combined with AND. Supported
  operators: `gt`, `gte`, `lt`, `lte`, `eq`, `is_missing`, `is_present`. Presence
  operators omit `value`; numeric operators require a finite number. Counts
  require integer values. Reject unknown names, unsupported scopes, contradictory
  ranges, and invalid arguments. Return supported names in the error.
- `order_by`: one allowlisted metric and `asc`/`desc`. Sort by the effective value
  under the selected quality mode, with unusable values last in both directions,
  then relevance when text is present, then recency and ID for stable ties.
- `metric_quality`: `complete` by default; `available` explicitly permits
  ready, partially covered or assumed numeric evidence. Apply the mode to
  comparisons, presence operators, and sorting using the effective-value rule
  below. Even complete `cost_usd` is an API-equivalent estimate under the existing
  price model, not a bill. Keep that distinction in its definition.
- `metric_scope`: applies to every metric in the request. Mixing scopes within
  a request can wait until there is a demonstrated use case.
- `result_mode`: `cards` by default in the existing search tools, or `metadata`
  for IDs and selected fields. `query_statistics` always returns metadata.
- `select`: allowlisted metric names and small contextual fields to return in
  metadata mode. Identity fields are mandatory. Include filter/sort metrics
  automatically as matching evidence, even if omitted from `select`, and report
  the effective selection once. An explicit empty selection supports ID-only
  requests with no numeric filters/sorts; an omitted selection includes only
  filter/sort metrics. Reject large text fields in metadata mode.

Define an effective value for each requested metric and quality mode:

| Recorded evidence | Effective value with `complete` | Effective value with `available` |
| --- | --- | --- |
| Ready, complete numeric value (including supported zero) | Recorded value | Recorded value |
| Ready, partial or assumed numeric value | Null | Recorded value |
| No numeric value, unsupported evidence, or unavailable attribution | Null | Null |
| Derivation pending/not ready, even if an older value is retained | Null | Null |

Completeness is assessed per metric: an aggregate token total can be complete
while its input/output breakdown is partial. For complete mode, `cost_usd`
requires `price_status: "priced"`; `assumed` and numeric partial costs require
available mode. Source archive coverage remains a separate reported limitation.

Numeric comparisons require a non-null effective value. `is_present` matches
exactly when that effective value is non-null; `is_missing` matches exactly when
it is null. These presence operators are complementary within the same mode.
Thus partial-only evidence matches `is_missing` and sorts after usable numbers
under default complete mode. Under available mode it matches `is_present`, can
pass numeric thresholds, and sorts by its recorded number. Sorting alone retains
entities with unusable values at the end rather than excluding them.

Keep the recorded value and readiness, coverage, attribution, and price status
in the response even when the effective value is null; explain whether it was
absent, incomplete, assumed, unattributable, or pending. `is_missing` means no
usable value under the requested mode, not necessarily no stored evidence. A
caller seeking absent numeric evidence rather than incomplete evidence can use
`is_missing` with available mode, inspecting readiness for pending cases. A
known zero is valid only when the source/derived data supports it.

`list_metrics` returns a bounded registry with names, aliases, units, definitions,
supported scopes/operators, quality limitations, and derivation version. Start
with this registry rather than exposing arbitrary database field names or the
entire query-table expression language. Map familiar aliases such as
`token_count` and `turn_count` to canonical names.

## Response contract

Keep existing card and offset-pagination shapes for default search calls.
Cards include each requested metric's recorded `value`, query `effective_value`,
and any reason the latter is null,
with scope, derivation, coverage/readiness, and price status where applicable.
Include effective filters, quality mode, and ordering once at response level, so
the client can repeat them while paging. Avoid attaching all ten metrics to
every card by default; an overview can expose the full metric set.

Metadata mode returns only selected fields, mandatory identity, and required
quality evidence. Use flat numeric fields for effective values, with names,
units, scope, price basis, and derivation definitions in a response-level metric
schema. Complete ready values inherit the schema's quality defaults. Sparse
per-row `metric_details` retain recorded values and explain exceptions such as
partial, assumed, unavailable, or pending evidence; null is never replaced by
zero. Source/archive coverage exceptions also remain explicit. Workspace rows
always include `workspace_id`; conversation rows include both `conversation_id`
and `workspace_id`. Workspace-scoped metrics on conversation rows explicitly
identify the workspace as their owner.

Return a bounded quality summary for requested metrics over the cohort selected
by the structured context filters, before numeric predicates. Label that cohort
explicitly: it is not the count of semantically relevant conversations. This
makes missing, partial, and pending coverage visible even when thresholds exclude
those entities. Do not silently reinterpret pending backfill as absent evidence.

The response budget must preserve requested metric evidence and pagination
metadata. Reduce snippets and row count first. If the budget cannot fit one
minimal row plus required metadata, return a useful budget error rather than a
zero-progress continuation. IDs, values, and metric ownership are essential.

## Bulk metadata and statistics

`query_statistics` makes the metadata workflow discoverable in the tool list.
Its description should recommend it for statistics questions and large ID/value
requests. It shares context filters, metric filters, quality rules, and sorting
with the search tools; it is a small entry point over the same query engine.
Choose `entity: "workspace"` or `"conversation"` separately from `metric_scope`.
Workspace entities require workspace scope; conversation entities support both
scopes. `result_type: "rows"` is the default, with `"aggregate"` for scalar or
grouped statistics.

For example, a metadata request to `query_statistics`:

```json
{
  "entity": "workspace",
  "result_type": "rows",
  "metric_scope": "workspace",
  "select": ["cost_usd", "total_tokens"],
  "order_by": {"metric": "cost_usd", "direction": "desc"},
  "limit": 100,
  "max_output_tokens": 4000
}
```

The core row payload is compact:

```json
{
  "items": [
    {"workspace_id": "w1", "cost_usd": 8.4, "total_tokens": 120000},
    {"workspace_id": "w2", "cost_usd": 6.2, "total_tokens": 90000}
  ]
}
```

This fragment omits the required response-level schema, effective request,
population/quality counts, freshness, snapshot, and continuation metadata. Those
fields appear once per response; definitions and status objects should not be
repeated for every complete numeric cell. Keep one object-row encoding for the
first draft and retain the familiar `items` envelope. Measure actual serialized
response bytes and estimated tokens for representative 100-row pages before
adding alternative columnar encodings.

### Conversation ID retrieval

Conversation IDs must be available without descriptive cards. Add bounded
`workspace_ids` filtering to `query_statistics` for up to 100 workspace handles;
`entity: "conversation"`, `select: []` returns flat conversation/workspace ID
pairs. This lets a caller fetch the conversations for a batch of returned
workspaces without repeating workspace statistics or making one detail call
per workspace. The same filter can select conversation statistics when needed.

Workspace rows may explicitly select `conversation_ids` as a convenience.
Return at most 20 canonical current-member IDs per workspace, alongside
`conversation_count` and `conversation_ids_complete`. If there are more IDs,
return a typed follow-up request for the flat conversation query, including
`snapshot_id` and the relevant `workspace_ids`. Selecting `conversation_ids`
captures complete membership in the initial snapshot even though the inline
list is bounded. The follow-up reads that relation within the same snapshot;
it does not change the parent metric population. If membership was not captured,
a later conversation query creates a new snapshot and reports its own time.
Do not claim the list is complete or silently drop IDs to meet the budget. Inline IDs cover
the workspace membership that produced its metrics, not just conversations
matching a text query elsewhere in that workspace. Preserve linked alias
handles separately when requested, without counting copies as new conversations.

All IDs must be valid handles for the existing overview/message/detail tools.
Workspace rows with no archived conversations still return their workspace
statistics and an explicit empty ID set when requested.

### Paging and budgets

Metadata requests need separate limits from descriptive cards. Propose a default
of 100 rows, a maximum of 500, and a 4,000-token approximate response budget,
with an explicit metadata-only maximum of 8,000. Existing card defaults and
their 20-conversation/100-workspace MCP ceilings remain unchanged. The budget
can reduce a page below the requested row limit; `returned_count` always reports
what was emitted. Never drop requested numeric columns, identity, or quality
exceptions to fit more rows. Smaller selections naturally fit more rows.
Advertised MCP schemas must express the conditional metadata limits instead
of retaining the existing card-only maximum on the shared `limit` property.

Use opaque `next_cursor` pagination for the new metadata path, bound to entity,
scope, effective selection, filters, quality mode, and ordering. Advance after
the last emitted row, not the last row fetched before budget trimming. Reject
changed query arguments or malformed/expired cursors with a useful error. Reject
mixing cursors and offsets; default legacy calls keep their offset contract.

Bulk paging must use a stable query snapshot: live changes to metrics, membership,
pricing, and mirror representatives must not duplicate, skip, or change values
between pages. Materialize the canonical ordered numeric/identity result and
requested membership in a bounded, short-lived server snapshot; do not hold a
SQLite read transaction open across MCP calls. Return `snapshot_id`, creation
time, expiry, exact `total_matching`, and `population_complete`. Propose a
five-minute TTL, with row/byte and concurrent-snapshot limits enforced explicitly
and validated against representative libraries. An expired or unavailable
snapshot requires a restart; do not silently resume against current data.
The initial implementation caps snapshots at 100,000 context/membership rows,
64 MiB each, and eight concurrent retained snapshots. Complete one-shot results
do not persist snapshots or consume slots by default. Retain them automatically
for cursor pagination or truncated conversation-ID access; support explicit
`retain_snapshot: true` for later reuse of a complete result. One-shot queries
must still work when all retained snapshot slots are occupied. Finished
snapshots can be released with `query_statistics` and `release_snapshot: true`
plus their `snapshot_id`.
Snapshot state must be reachable through the shared library service when MCP
requests originate from different processes. Opening a catalog independently
per call must not pretend to provide snapshot pagination without shared state.

Accept `snapshot_id` on a new rows/aggregate request to reuse the fixed entity
population, metric values, and quality evidence. New projections/reductions may
use only fields captured for selection, filters, sorting, or aggregation in the
initial request; otherwise require a new snapshot explicitly. Set
`retain_snapshot: true` on a one-shot initial request when this reuse is needed.
Population, scope, and quality changes require a new query. A snapshot reference is distinct
from a continuation cursor: cursors additionally bind the current result shape
and ordering. Membership follow-ups use the captured relation described above.

Budget limits affect the emitted page only. They must never turn a full-population
query into an unlabelled sample. Report normal continuation separately from
candidate/population limitations. Metadata-only queries without text must cover
every matching canonical entity up to explicit execution/snapshot limits; if
those limits are exceeded, return an error that requests narrower filters.
Text searches using bounded lexical/semantic candidates must state
`population_complete: false` and their retrieval limitations.

### Aggregate answers

Support `count`, `sum`, `avg`, `min`, and `max` over the same canonical filtered
population, with at most eight aggregations and optional grouping by repository
or source (at most two dimensions). Each aggregation names an `id`, `op`, and
`metric` except entity counts. Do not expose arbitrary SQL or formulas. An
aggregate-only response sends aggregates and coverage, not entity rows. IDs
remain available through a rows request on a retained snapshot; combined scalar
aggregates must not force every conversation ID into the aggregate response.

```json
{
  "entity": "workspace",
  "result_type": "aggregate",
  "metric_scope": "workspace",
  "aggregations": [
    {"id": "workspaces", "op": "count"},
    {"id": "average_cost", "op": "avg", "metric": "cost_usd"}
  ],
  "group_by": ["repository"]
}
```

Aggregates run before page limits and response-budget trimming. Entity counts
count canonical entities once; numeric `count` counts usable metric values.
Other numeric operations use effective values under the selected quality mode
and report their usable-value denominator and quality breakdown. Zero usable
values yields a count of zero and null numeric aggregates, including sum, rather
than asserting that unknown spending is zero. Label partial/assumed inputs under
available mode. Group lists are capped and must page against the same snapshot
or fail explicitly; they must never disappear behind a top-N presentation limit.

For workspace-scoped metrics, aggregate only workspace entities. A conversation
listing can repeat a workspace's cost on several rows, so summing those rows
would multiply spending. Reject `result_type: "aggregate"` with conversation
entity/workspace metric scope and direct the caller to workspace entities.
Keep group keys single-valued; per-model grouping remains deferred because a
workspace can contain multiple configurations. No-text aggregates are exact
over the stated snapshot population, subject to evidence quality; approximate
text candidate populations must not be presented as archive-wide statistics.
Retain separate named quality counts for the structured context cohort before
metric predicates and the actual filtered aggregate population.

## Implementation approach

1. Add a typed metric registry and shared request parser/compiler, with
   allowlisted expressions and parameterized values. Keep legacy
   `metric`/`minimum`/`maximum` arguments working in `search_work`; reject an
   ambiguous combination with the new filter array.
2. Build one canonical metric projection per conversation/owning stream and
   workspace. Reuse reconciled usage and historical pricing for tokens/cost;
   ledger status for tool errors; existing compaction attribution for counts.
   The current Library tool-error expression only checks a subset of error
   evidence, so it is not an adequate substitute for the classified ledger.
   Update that Library definition if aligning the public metric requires it.
3. Maintain the projection incrementally from authoritative changes, including
   session attribution, tool-ledger rebuilds, identity links, group membership,
   timestamps, pricing, and model aliases. Store derivation/readiness metadata.
   Backfill retained evidence in bounded batches through existing upgrade/build
   paths. MCP reads should not launch a full-library rebuild.
4. Apply metric predicates before ranking and pagination. Push the eligible
   conversation IDs into lexical candidate selection before both cuts: the
   `recentMatches` window of the 25,000 most recently indexed text matches
   (ordered by FTS rowid) and the subsequent 500-message ranking limit. Compute
   the recency cutoff over eligible matches too; restricting only the outer
   query or the ranked 500 still drops eligible older hits behind unrelated
   matches. Preserve bounded semantic retrieval and make candidate limits
   visible if they prevent exhaustive text search.
5. Make metric-only queries use indexed numeric projections. Add indexes based
   on measured plans for common cost/token/error thresholds. Hydrate card
   details only in card mode. Metadata and aggregate execution read only fields
   needed for selection, filters, sorting, identity resolution, and quality;
   bypass `libraryPage`, preview enrichment, and `conversationCards`. Selected
   conversation IDs use batched membership reads with an explicit completeness
   contract. Compute aggregates against the full canonical population, not the
   hydrated page.
6. Resolve mirror groups deterministically before thresholds, final metric
   ordering, totals, and pagination, using authoritative metric evidence without
   summing copies. Preserve filtered identity/source evidence. Both existing
   paths re-sort: `collapseConversationMirrors` orders conversation results by
   relevance, start/activity time, and ID; `Catalog.Search` calls
   `suppressMirrors`, whose `suppressMirrorRows` orders workspace results by
   score, activity, and ID. An earlier metric sort would be discarded in either
   path. Make the final ordering honor the requested comparator after canonical
   mirror resolution in both tools, before totals and pagination.
7. Attach metric summaries to conversation overviews, refreshing only the
   requested conversation's workspace. Report unavailable metrics for inactive
   group members rather than pending evidence that cannot be projected.
   Advertise the new
   schemas and update MCP documentation. Keep `query_metrics` available for
   compatibility, mark it as legacy, and direct conversation discovery to
   `search_conversations`. Address its scope/version winner, pre-collapse limit,
   final ordering, and null/bounds defects in a separately documented
   compatibility change with targeted regression tests based on the actual
   returned behavior described above.
8. Add the metadata projection and `query_statistics` entry point, stable shared
   snapshots, validated cursor paging, and aggregate reductions. Reuse filters
   across every entry point, retain response-level schema/quality defaults, and
   enforce explicit row/byte/time/concurrency limits. Update MCP history's
   argument allowlist and result/truncation accounting for modes, selection,
   aggregation, and cursor output; distinguish returned rows/groups from total
   matching entities and ordinary paging from incomplete retrieval.

Reuse query-table validation/compiler utilities where they fit, but give MCP a
small typed schema and bounded responses. Reusing default `Catalog.Search` row
hydration would still return workspace data without solving conversation
ownership or coverage, and would read large display fields that metadata
requests do not need. Reuse query-table projection/aggregation machinery where
applicable without routing metadata through UI page enrichment.

## Acceptance checks

- Metric-only discovery, text plus multiple thresholds, and sort-only queries.
- Metadata mode returns only mandatory IDs, selected and filter/sort metrics,
  requested small context, and required quality exceptions. Default card calls
  retain their existing response shapes and bounds.
- Both bulk workspace and conversation rows support 100/500-row requests, with
  paging when the token budget reduces the actual emitted row count. ID-only
  selection works; numeric columns and quality evidence are never omitted.
- A workspace with over 20 conversations reports an incomplete inline ID list
  and supports complete flat batch ID retrieval without duplicated workspace
  statistics. Include no-conversation workspaces, children, aliases, and pending
  evidence; every returned handle works with the existing detail tools.
- Page an active library while ingestion changes costs, groups, pricing, and
  mirror representatives. Snapshot pages preserve IDs, values, membership, and
  order; changed requests and expired cursors fail explicitly. Cover multiple
  MCP processes, snapshot caps, and budget-trimmed cursor advancement.
- Consecutive one-shot aggregates and complete metadata results consume no
  snapshot slots, including when all eight retained slots are occupied.
  Explicit retention permits later compatible projections; cursor paging and
  truncated membership access retain their captured data automatically.
- Conversation overviews refresh their own workspace on the first call among
  more than 100 dirty workspaces. Inactive members report unavailable metrics
  on repeated reads and recover when group membership becomes active again.
- Scalar and grouped aggregates equal a reference over the entire canonical
  population, including unequal page sizes and missing/partial denominators.
  Zero usable values remain unknown rather than zero spending; grouping caps
  do not silently truncate. Repeated workspace metrics on conversation rows
  cannot multiply workspace totals.
- Measure response bytes/estimated tokens for a 100-row ID/two-metric page and
  an aggregate answer. Verify metadata paths do not read previews or call card
  hydration, and aggregate paths do not serialize entity rows. Compare those
  costs with card mode on the same population; publish measured results before
  selecting final metadata limits.
- Values exactly on inclusive/exclusive boundaries, known zero, absent value,
  partial evidence, pending derivation, and unsupported source fields.
- Under both quality modes, cover partial-only and assumed-only evidence with
  every numeric operator, complementary `is_present`/`is_missing`, and ascending
  and descending sorting. Include sort-only results with unusable values last,
  preserve recorded values/reasons, and cover a known zero and a pending older
  value. Default `is_missing` must discover partial token usage.
- A two-conversation workspace where only one conversation meets the threshold;
  nested embedded and archived delegates; unassignable group usage.
- Mirror copies with differing completeness, including stable equal-value ties
  and pagination after deduplication.
- Tool errors from nonzero exits, timeout/interruption, and MCP errors; harmless
  `rg` no-match and textual mentions of failure must not inflate failures.
- Token checkpoint/request reconciliation, cached input, included reasoning,
  historical prices, partial/unpriced cost, and price-book invalidation.
- Explicit metric sorting survives mirror collapse in both `search_conversations`
  and `search_work`, plus output-budget trimming; pages make progress while
  repeating the effective request.
- Selective metric/text queries with an eligible hit ranked below the previous
  global top 500, and an eligible hit older by FTS rowid than the previous global
  most-recent 25,000 window. Unrelated ineligible hits must not consume either
  cut, including the subquery used to compute the recency cutoff.
- Migration/backfill and incremental refresh match a reference calculation;
  representative large-library queries avoid full transcript scans.
- Old tool calls retain their expected schemas/behavior until a documented
  compatibility change. MCP call-history counts and truncation remain correct.
- Characterize legacy `query_metrics` with workspace and conversation/version
  rows valued 40 and 2 (last-read winner 2, or 40 with minimum 3); duplicates
  exhausting `LIMIT` before collapse; valued/null results in workspace-ID order;
  stored-null overwrites; and out-of-bounds values appearing as null fallback
  rows. The compatibility fix needs separate assertions for its intended
  canonical scope/version, unique-result limits, value ordering, and bounds.

## Product decisions

The user confirmed that both scopes should be available. They are required in
the initial release, rather than treating one as a later addition. The default
remains a recommendation: conversation search uses its own usage, while work
search uses workspace totals. Both modes use the same filters and sorting.

High-volume requests for workspace/conversation IDs and statistics are also an
initial requirement. The recommended surface is metadata mode on both search
tools plus `query_statistics` for bulk rows and aggregate-only answers, all
sharing one metric/filter engine. Basic grouped statistics are now included;
more complex ratios, distribution analysis, and model-attributed grouping remain
follow-ups.
