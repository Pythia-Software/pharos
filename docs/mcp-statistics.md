# MCP statistics and metric discovery

Use `list_metrics` to discover the ten initial metrics and their definitions.
Use `query_statistics` for workspace/conversation IDs, selected numeric fields,
or aggregate answers. Both `search_work` and `search_conversations` accept the
same metric filters and sorting, plus `result_mode: "metadata"` to skip previews.
Plain `search_work` cards honor `max_output_tokens` (200–4,000, estimated as
three JSON bytes per token), defaulting to 4,000 and clamping oversized requests. Copy `next_offset` with the original filters to continue
cards, including when the budget returns fewer than `limit`. A null offset ends
the results. Work descriptions are short previews; use `get_work_detail` for
full fields. A budget too small for one card returns an error.

## Workspace and conversation usage

`entity` chooses one row per workspace or conversation. `metric_scope` chooses
what the numeric values describe:

- Workspace entities use workspace scope: current conversations and embedded
  agents, reconciled without counting linked delegation placeholders twice.
- Conversation entities default to their own main agent stream. Embedded
  delegates and separately archived child conversations are excluded.
- Conversation entities can explicitly select workspace scope. Each conversation
  then carries its workspace's metrics; these repeated totals cannot be aggregated
  as conversation statistics. Choose workspace entities for those aggregates.

For example, `query_statistics` can find workspaces costing over $5:

```json
{
  "entity": "workspace",
  "metric_filters": [{"name": "cost_usd", "op": "gt", "value": 5}],
  "select": ["cost_usd", "total_tokens", "conversation_ids"],
  "order_by": {"metric": "cost_usd", "direction": "desc"}
}
```

For individual conversation costs, use `entity: "conversation"`. To find
conversations in workspaces exceeding that threshold, also specify
`metric_scope: "workspace"`.

`cost_usd` is the API list-price equivalent at historical prices, not a bill.
The other metrics are `total_tokens`, `input_tokens`, `output_tokens`,
`cache_read_input_tokens`, `user_turn_count`, `tool_call_count`,
`tool_error_count`, `compaction_count`, and `wall_clock_span_seconds`.
Reasoning is included in reported output rather than added twice. Span includes
idle time and is unknown for unfinished or invalid intervals. Tool errors come
from classified ledger outcomes; textual mentions of failure do not count.

## Filters and selected fields

Up to eight `metric_filters` combine with AND. Operators are `gt`, `gte`, `lt`,
`lte`, `eq`, `is_missing`, and `is_present`. Numeric operators require finite
values; counts require integers. Presence operators omit `value`. Contradictory
predicates and unsupported names fail explicitly. Aliases include `token_count`,
`turn_count`, `tool_use_count`, and `wall_clock_span`.

Context filters include repository, source, provider, model, effort, file, PR,
from/to, and up to 100 `workspace_ids`. Repository text is a case-insensitive
literal substring of a repository name or remote; `%` and `_` are ordinary
characters. Date filters select entity lifetimes, not usage inside a date
window. Provider, model, and effort select a configuration recorded in the
same conversation. Model and effort may come from the conversation or from
one of its messages; the provider belongs to that conversation. These filters
select entities containing that configuration, not just tokens consumed under
that configuration.

`select` chooses up to 20 allowlisted metrics or small context fields. An empty
array returns IDs only. Filter/sort metrics are included automatically. Workspace
rows always include `workspace_id`; conversation rows include both IDs. Optional
context fields include repository, source, activity time, and conversation model,
provider, effort, start time, and coverage. Large descriptive fields are rejected.
Alias handles are separately available as `mirrored_workspace_ids` or
`linked_conversation_ids` for the corresponding entity type.

Metric schema and defaults appear once per response. Rows use flat numeric
effective values, with sparse `metric_details` for exceptions. Quality/readiness
and archive-coverage limitations remain visible even in compact responses.

## Quality and missing values

Default `metric_quality: "complete"` uses only complete, ready numeric evidence.
`"available"` also accepts ready partial or assumed values. Comparisons, presence,
and sorting use the same effective value under that mode:

| Evidence | Complete mode | Available mode |
| --- | --- | --- |
| Complete and ready, including known zero | Numeric value | Numeric value |
| Partial or assumed, ready | Null | Recorded value |
| Absent, unattributable, or pending | Null | Null |

`is_missing` matches null effective values and `is_present` is its complement.
Sorts keep nulls last in both directions. Exceptions preserve the recorded value
and status; missing evidence never becomes zero. Completeness is per metric: an
aggregate token total can be complete while its breakdown is partial.

Conversation overviews with `include_metrics: true` refresh the requested
workspace. Inactive group members have no current metric projection; their
overviews report `unavailable` with
`metrics_unavailable_reason: "inactive_group_member"`, rather than a pending
value to retry.

The initial numeric projection is backfilled in bounded batches by Library
maintenance. A query computes up to 100 dirty workspaces from its own snapshot,
without storing them; remaining workspaces are explicitly pending. Tool counts
are pending until the current tool ledger is built. Coverage counts distinguish pending, unavailable,
partial, assumed, and complete evidence. A complete metric over retained records
does not imply the entire original conversation was archived.

## Conversation IDs and pagination

Selecting `conversation_ids` returns up to 20 canonical current-member IDs per
workspace, plus `conversation_count` and `conversation_ids_complete`. An
incomplete list includes a typed follow-up request against the same snapshot.
Flat batch retrieval uses `entity: "conversation"`, `workspace_ids`, and
`select: []`; it does not need previews or one detail request per workspace.
IDs remain handles for the existing conversation overview/message tools.

Metadata defaults to 100 rows, allows up to 500, and uses a conservative
4,000-token response estimate. The explicit maximum is 8,000. A budget can reduce
page size; it never removes requested numbers, quality exceptions, or IDs.
`returned_count` reports actual rows/groups. If one complete row cannot fit,
the tool asks for a larger budget or smaller selection.

Copy `next_cursor` into `cursor` with the original arguments (the normalized
`effective_request` is returned). Cursors advance after the last emitted row.
They are signed and reject changes to query shape; do not combine them with
offsets. Existing card calls retain offset pagination and their original limits.

Complete one-shot rows and aggregates do not retain snapshots or consume slots
by default. Pagination and truncated conversation-ID follow-ups retain snapshots
automatically. Set `retain_snapshot: true` in a metadata query to capture values
for later reuse even when the result fits in one response; this returns
`snapshot_id` and its creation and expiry times.

Snapshots preserve numeric values, membership, and ordering across ingestion
and price updates. They live in the memory of the process that answered, the
Pharos service or the agent's MCP process, so a cursor fails as expired once the
service starts or stops. They expire after five minutes. A new rows or
aggregate request can reuse `snapshot_id` with the same population, scope,
quality, and order, using only captured fields. Requested membership follow-ups
reuse the captured relation. Other fields or changed populations require a new
query; unavailable/expired snapshots fail rather than silently changing data.

Limits are 100,000 context rows or captured membership rows, 64 MiB per snapshot,
eight concurrent retained snapshots, and a 30-second query deadline. The slot
limit does not block complete one-shot queries. Retained snapshots remain
available until expiry, including after the final page, and can be released early:

```json
{"snapshot_id": "the-returned-snapshot-id", "release_snapshot": true}
```

Snapshots store local derived metadata, not transcripts. Expired snapshots are
freed whenever a snapshot is created, read, or released. No SQLite read transaction remains open
between MCP calls. Metadata-only queries without text report exact retrieval
populations; metrics may still have pending/partial evidence. Text searches
apply eligibility before the 25,000 recent-match and top-500 lexical cuts, and
report `population_complete: false` with their retrieval limitations.

## Aggregate answers

`result_type: "aggregate"` computes up to eight named `count`, `sum`, `avg`,
`min`, or `max` reductions before pagination and output-budget trimming:

```json
{
  "entity": "workspace",
  "result_type": "aggregate",
  "aggregations": [
    {"id": "workspaces", "op": "count"},
    {"id": "average_cost", "op": "avg", "metric": "cost_usd"}
  ],
  "group_by": ["repository"]
}
```

An entity count omits `metric`; numeric count counts usable values. Numeric
reductions report usable-value denominators and quality breakdowns. With no
usable values, numeric count is zero and other aggregates, including sum, are
null. Grouping supports repository and source, up to two dimensions and 2,000
groups; groups page or fail explicitly rather than silently becoming top-N.
Per-model grouping is excluded because workspaces can contain several models.
Text-based aggregate populations retain the same incomplete-retrieval flag.

Aggregates return groups, not entity rows. Use `retain_snapshot: true` when a
later compatible rows request should retrieve IDs from the same snapshot.
`get_conversation_overview` optionally accepts `include_metrics: true` for the
full own-stream metric set; increase its
budget if needed. The legacy `query_metrics` tool is retained with its historical
suppression behavior; new clients should use the scoped tools above.

## Payload verification

`TestStatisticsMetadataPayloadEfficiency` uses 101 synthetic workspaces. A page
of 100 IDs with cost and token usage serialized to 8,454 bytes (about 2,818 tokens
under Pharos's byte estimate). Card mode serialized 10,835 bytes for five rows
(about 3,612 tokens). An average over all 101 workspaces used 994 bytes
(about 332 tokens). These are fixture measurements, not guarantees for arbitrary
IDs, selections, or quality exceptions; budgets bound each actual response.
