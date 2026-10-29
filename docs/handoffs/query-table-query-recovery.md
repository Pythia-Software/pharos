# Query restoration after failed execution

**Delivered in Query Table 0.9.7**: `persistLast: "succeeded"` saves the
last-used query only after its rows and metrics load, and `api.execution`
is an execution receipt. Pharos adopts them in a later change.

Query Table release: 0.9.2 (all four npm packages and Go backend).

## Report and observations

A Pharos Usage tab can retain its controls while its results remain Loading.
Returning to `/usage` also restores the saved Usage tab, machine scope and
last-used query, so removing query parameters is not a reliable escape.

The reported URL carries `q_tool_calls=2d.VY_BCsIwEET_Zc85pGpB-yulhNBECCZZ2d0cpPTfXVNQPM5jZ2dmA4ZpnoHFk8TgvIAZRruYGQQxu-pLVHLqZMVSfA1gzrZrNUljMNeuIhGSk9fzZwiNvCSsrujVcJgocsviBB-xfuhwvPZESfO_-HjQO6zI4hpr7m3sMEnWjNPFLosB1P4b3FPMAaa_HRASKQqRVxW15axbIXsW2NWYYRrt_gY`.
Its decoded state selects ordinary tool-call columns, sorts `started_at DESC
NULLS LAST` and limits to 50, without predicates or metrics. On current Pharos,
Usage Machine Time uses `q_machine_calls`; `q_tool_calls` is offscreen Tools
state and does not explain the active query. The live library's preferences
selected Machine Time and seven complete days. In an isolated browser context,
the original URL and a switch to Machine Tokens both completed successfully.
The existing tab's full stored state was unavailable, so the original stall's
cause remains unconfirmed. A connection stall is a recovery scenario, not a
proven diagnosis of that tab.

## Confirmed persistence behavior

`useQueryTable` in query-table-react 0.9.2 schedules `storage.saveLast` 400 ms
after query changes once restoration is ready. That effect does not wait for
row execution to succeed. A delayed request that subsequently fails can already
have replaced the last-used state. The public API exposes `query`, `loading`,
`error`, and request activity, but not an execution receipt associating the
complete query with its committed result. A host cannot safely use
`!loading && !error && total !== null` as a success receipt: during a query
change, these can still describe the previous result before the next effect.

## Minimal upstream reproduction

1. Mount a remote table with a recording StorageAdapter and a Transport whose
   row request is controlled by the test.
2. Complete query A successfully.
3. Change to query B. Hold its response beyond the persistence debounce, then
   reject it (also exercise response validation failures and deadlines).
4. Inspect the last-used state and remount without an explicit query link.

Currently B can be saved before it has produced a usable result.

## Requested upstream behavior

Provide an opt-in last-successful-query persistence policy or a public execution
receipt that includes the exact query identity, execution generation and result
status. Preserve explicit URL restoration, named queries and configured defaults
as distinct user choices. Distinguish execution success with zero rows from
failure. Do not infer query invalidity from every offline or service error.

Define what success means for queries with independent metrics: successful rows
with a failed metric must not silently erase that metric or report a complete
successful view. Report restoration and execution phases separately, including
terminal failure and cancellation, so hosts can offer retry/reset controls.

Acceptance coverage should include:

- A succeeds, B fails or times out, bare remount restores A under the opted-in
  policy; B remains explicitly retryable and exportable.
- A slow A completes after B: A cannot mark B healthy or overwrite B's result.
- Empty successful results remain restorable, including out-of-range pages.
- Grouped/computed queries retain source, HAVING, metrics and presentation.
- A cancelled or hidden table cannot publish a stale persistence receipt.
- Invalid and unavailable explicit links remain visible errors until an explicit
  reset or navigation; they never silently broaden to defaults.
- Named saves/default selection remain intact after an execution failure.

## Pharos-owned recovery

Pharos now bounds its row, capability and metric reads at 45 seconds, supplies
bounded fetchers to snapshot/computed stores, and offers reset/retry controls.
A slow table offers recovery after 12 seconds. Usage's always-visible Reset page
opens an explicit baseline with `fresh=1`, bypassing last-used/named defaults,
saved chart options, Usage tab and machine scope. Empty results are not failures.

The host storage adapter skips the exact query after a row request is rejected
with HTTP 400/422 or reaches the client deadline. This marker affects implicit
restoration only; it does not modify named views or resolve explicit links
otherwise. Explicit retry removes the marker. Authentication, network failures,
rate limiting and service errors do not mark the query. This is a host preference
policy using public APIs, not a change to Query Table's execution or persistence.

Next useful diagnostic: an opt-in bounded report of the active dataset, route,
restoration phase, elapsed time, endpoint/status and request generation. Preserve
the failing query for explicit retry, but omit tokens, result rows and filter
values from automatic diagnostics. This would distinguish a network wait from
restoration or repeated cancellation if the original tab stalls again.
