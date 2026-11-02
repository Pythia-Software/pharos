# Query Table: outstanding requests from Pharos after 0.9.6

**Resolved in Query Table 0.9.7** (upstream PR #146), consumed by Pharos's
matching published core/React/UI/codegen packages and Go module. Every request
below landed. Pharos took the fixes that need no feature of its own on
upgrading: chart labels (1), whole-key colours (2), nearest-point picking (4),
fitted links and Detail/JSON buttons (5, 6) and the metadata flood (16), and
removed the browser suite's `KNOWN` entries for them. The capabilities (3, 7–15) are
adopted in later changes. See
[the integration guide](../query-table-integration.md#097-changes).

The original assignment below is dated **2026-10-28** and describes the
**0.9.6** implementation.

For the Query Table team. This is a self-contained assignment against the
published **0.9.6** release, dated **2026-10-28**. It collects every request
from Pharos that is still open and replaces the earlier per-topic handoffs.
Each numbered request can ship on its own. Implement them as reusable library
behaviour, and publish them in one aligned Go and npm release (core, react, ui,
codegen and `backends/go`).

## Context

The consumer is **Pharos**, a Go service with a React frontend and an embedded
SQLite catalog of coding-agent conversations. It uses Query Table throughout:

- `DataTable` and `useQueryTable` for every query page, with `fitToScreen`,
  native item Detail, group link summaries (`op: "link"`) and the native Saved
  controls;
- V2 metrics and V3 grouped rows from the Go backend over host-authorized
  SQLite sources, plus `ExecuteV2Rows` over rows held in memory;
- `MetricCard` and `MetricChart` for Home cards and the Analyze dashboards.
  Those dashboards compare groups, show distributions, plot two measures per
  entity and profile hour-of-day and weekday cycles. Each view is an ordinary
  query: a grouping stage for per-entity units, then an `AggregationClause`.

Pharos does not patch, vendor or wrap Query Table. Where a capability is
missing, it picks the nearest honest behaviour, says so in the UI, and asks
here.

The Pharos baseline is master
[`cb3636a`](https://github.com/Pythia-Software/pharos/tree/cb3636a), which
pins 0.9.5. The move to 0.9.6 is in review in
[Pharos PR #267](https://github.com/Pythia-Software/pharos/pull/267).
Each problem below was checked against the `v0.9.6` tag's source. Pharos's
browser suite was also run with the 0.9.6 packages installed. Every browser
reproduction cited here fails the same way on 0.9.5 and 0.9.6.

### Delivered, not requested again

- 0.9.6: the Go query-link codec (`EncodeQueryLink`, `DecodeQueryLink`), metric
  windows, shares and whole intervals, and `--qt-touch-target-scale`.
- 0.9.5: the content-sized filter-value combobox.
- 0.9.4: direct V3 grouping. Pharos deleted its V3 pre-reducer. Computed
  previews over negotiated transports without `fetchRows`.
- 0.9.0–0.9.3: representative and staged-value reuse, scalar V3 fusion, host
  group budgets up to 100,000, `ExecuteV2Rows` and its speedups, cross-field V2
  fusion and the scratch-cleanup contract.

### Reproducing in Pharos

Use Node 24 (`.nvmrc`) and the Go toolchain from `go.mod`:

```sh
tools/setup.sh                    # locked npm install and frontend build
npm --prefix web run test:browser -- --test-name-pattern='<route name>'
```

The browser suite (`web/tests/browser/structure.test.mjs`) loads each page
against stubbed data at 1440, 1280, 1024, 390 and 320 px. No catalog or
personal data is involved. It reports controls and SVG text that a
non-scrolling ancestor cuts off. Bugs listed in its `KNOWN` table run as todo.
To try a candidate release, point the four npm pins in `web/package.json` at
it, then run `npm --prefix web install` and `npm --prefix web run build`.

---

# Charts (`query-table-ui` `MetricChart`)

## 1. Fit chart group labels instead of clipping them

**Problem.** `MetricChart` reserves a fixed left margin for group labels,
`left = compactBox ? 8 : compact ? 44 : Math.min(78, W * 0.24)`
(`packages/ui/src/MetricChart.tsx`, line 135 at v0.9.6). Box rows and
horizontal bars draw each label right-aligned at `left - 8`, inside an SVG
that clips its content. A label wider than about 70 px loses its *start*:
"root_model_family-0" is drawn as "odel_family-0". Real labels are often
longer, for example repository names or "claude / claude-opus-4".

**Request.** Do one of these:
- size the margin to the widest label, up to a cap such as 40% of the chart
  width, and truncate longer labels at their *end* with an ellipsis. Keep the
  full label in the existing tooltip and `aria-label`.
- wrap labels onto at most two lines, as list displays already do with
  `list.maxLabelLines`.

**Acceptance.**
- At 1440 px and 390 px, a box or horizontal-bar chart with 30-character
  group labels shows the start of each label, either whole or truncated at
  its end.
- No label text extends outside the chart's SVG.
- Charts with short labels keep today's layout.

**Pharos evidence.** 42 dashboard cases are todo for this, for example
`--test-name-pattern='time dashboard distribution'`. The `KNOWN` entry is
`chart label "…" cut off by svg … in div.qt-metric`.

## 2. Colour multi-key groups by their whole key

**Problem.** `MetricChart` colours each mark by the bucket's first key only,
`colors.color(clause.groupBy[0] ?? "", b.keys[0] ?? null)` (line 106 at
v0.9.6). A pie, bar or box chart grouped by `["provider", "model_family"]`
gives every group from one provider the same colour, so adjacent pie slices
can't be told apart and the legend repeats colours. Pharos needs this
two-field breakdown because one model family can be served by several
providers.

**Request.** Do one of these:
- colour by the whole key tuple for every kind that draws one mark per group
  (pie, donut, bar, box, histogram);
- or add `MetricDisplay.colorBy`: `"first"` (today's behaviour), `"all"`
  (the whole tuple), or a field name.

Scatter can keep colouring by its first key. There the first key is the
series and the remaining keys identify the point.

**Acceptance.**
- A pie over `["provider", "model_family"]` with buckets `claude/opus`,
  `claude/sonnet` and `codex/gpt-5` uses three distinct colours.
- A tuple keeps its colour across renders and across charts that share a
  `MetricTheme`.
- Single-key charts are unchanged.

## 3. Hour-of-day and day-of-week grouping

**Problem.** Dashboards need cycle profiles: a typical hour of the day, a
typical weekday. `MetricDateGrouping.interval` is still
`"hour" | "day" | "week" | "month" | "year"` at v0.9.6. Pharos therefore:
- requests every hour or day in range. A year of hours is 8,760 groups, so it
  needs `groupLimit: 10000` and treats a truncated result as incomplete;
- folds those buckets into 24 or 7 slots in the browser, filling idle slots
  and computing box statistics with the published `boxSummary`;
- disables drill-down on cycle marks, because a slot spans many dates and no
  filter selects it.

**Request.** Add two cyclic intervals to `AggregationClause.dateGrouping`, in
core and the Go backend:
- `hourOfDay`, keyed `0` to `23`;
- `dayOfWeek`, keyed with ISO numbers `1` (Monday) to `7`.

A record falls in the slot its timestamp occupies in `dateGrouping.timeZone`.

1. **Grouping.** Group by the slot, combined with any other `groupBy` fields,
   in `/metrics` (V2), `/query-v3`, `evaluateMetrics` and `applyAggregations`.
2. **Per-occurrence statistics.** Add an option to summarise each slot over
   its per-occurrence totals, not only by pooling records. "A typical Monday"
   needs each Monday's total first, then a box or mean over those totals.
   - One possible shape: `dateGrouping: { field, interval: "dayOfWeek",
     timeZone, occurrence: { interval: "day", includeEmpty: true } }` together
     with `distribution: { kind: "box" }`.
   - Idle occurrences count as zero for `sum`, `count` and `count_distinct`,
     and have no value for ratio expressions.
   - Only whole occurrences inside the query's time bounds count. A partial
     first or last day or hour is left out. 0.9.6's
     `partialIntervals: "exclude"` already defines this boundary for calendar
     intervals, so the occurrence option could reuse it.
3. **Drill-down.** `inspectBucketQuery` returns a query that selects exactly
   one slot's records. That can be a native predicate, such as an
   `HOUR_OF_DAY([field], zone) = 9` formula or filter. Otherwise it returns a
   documented null.

**Acceptance.**
- `{ interval: "hourOfDay", timeZone: "America/New_York", includeEmpty: true }`
  returns 24 buckets keyed `0` to `23` and handles daylight-saving
  transitions.
- A zone with a fractional-hour offset (`Asia/Kolkata`) buckets by local
  hour.
- With the occurrence option, a weekday box summarises each whole date's
  total, counting idle dates as zero for additive operations. The partial
  boundary dates of a relative range, such as "last 30 days" opened at 14:00,
  are left out.
- `minCount` and `hiddenGroups` behave as they do for other groupings.
- The Go and TypeScript executors return identical buckets for the same rows.

**Pharos integration once released.** For datasets whose records carry real
timestamps, Pharos replaces its browser fold with the native interval and
enables drill-down on cycle marks. Usage is the exception: its facts are
day-grained, so it keeps reading its own hourly ledger.

## 4. Pick the nearest point on line and scatter charts (lower priority)

**Problem.** Every line and scatter point draws a transparent 12 px hit
circle over its 4 px mark (`MetricChart.tsx`, lines 563–574 and around 680 at
v0.9.6). Where series meet at one date, a later series' circle covers an
earlier series' visible point. A small positive value can't be clicked
because a zero in the next series sits on top of it. This is common when a
host pads missing intervals with zeros so that a line is continuous. Keyboard
focus and Enter still reach the intended point; only the pointer picks the
wrong one.

**Reproduction.** Use a `MetricCard` with a line clause grouped by
`["day", "tool"]` and two series sharing three ISO dates. Give one series
values near zero and the other a large peak plus zeros. At the low-valued
date, a pointer click on the visible low point activates the other series.

**Request.** Resolve pointer hits to the nearest visible point, with a
deterministic rule for exact ties (or a choice among coincident series).
Keep keyboard navigation, tooltips and the exact bucket identity passed to
`onInspect`. Optionally let a host mark padded points as not inspectable, so
measured intervals stay distinct from presentation-only samples.

**Acceptance.** Pointer and keyboard activation pick the intended bucket for
dense, crossing, zero, negative and missing-value series.

---

# Tables (`DataTable` with `fitToScreen`)

Pharos turns on `fitToScreen: true` for every Analyze query, its suggested
queries and computed query-link destinations. The preference survives URL
encoding. Requests 5 and 6 are two cases of one rule: **screen fitting must
not shrink a cell below what its native controls need.** One fix that
accounts for interactive content when choosing minimum column widths may
solve both.

## 5. Fitted columns clip group-link anchors

**Problem.** `createGroupLinkRenderer` renders a link summary as an inline
`a.qt-link.qt-group-link`. When fitting narrows its column, the anchor
overflows `td.qt-cell`, which clips horizontally. Template links ("Open
conversation", "Open work") clip the same way. No custom renderer or CSS is
involved.

**Reproduction.** Build a native grouped `DataTable` with eight or more visible
columns: a text grouping key, numeric SUM and COUNT summaries, and a link
summary at the end:

```json
{ "id": "records", "op": "link", "label": "Drill In",
  "link": { "label": "Matching records", "to": { "kind": "query", "target": "fixture" } } }
```

Register `linkTargets.fixture`, set `fitToScreen: true` and render at 1440,
1024, 390 and 320 px. Wait for fitting and queries to settle, then compare each
anchor's bounds with its cell. In Pharos,
`--test-name-pattern='Query ideas'` fails 48 of 92 cases on both 0.9.5 and
0.9.6:

```text
a control is cut off where nothing scrolls:
a.qt-link.qt-group-link "Matching records" in td.qt-cell "Matching records" (36 of 96px shown)
```

The same sweep passed before fitting was turned on.

**Request.** A fitted link keeps a usable anchor and an understandable label
inside its cell. For example, count interactive labels when choosing minimum
widths, or wrap or ellipsize with the full accessible label kept. If the
minimum usable widths exceed the viewport, keep the table reachable by its
normal scrolling. Don't just hide the overflowing control.

**Acceptance.** Query and template links, null or unavailable destinations,
and short and long labels stay inside their cells, including in a table
reopened from an encoded `fitToScreen: true` query. Source and key filters,
URL state, manual resizing, modified clicks, href navigation, keyboard
activation and focus are unchanged. Upstream browser tests assert containment
and usability, not exact pixel sizes.

## 6. Fitted columns clip native Detail and JSON buttons at phone widths

**Problem.** At 390 and 320 px, screen fitting reaches its uniform 48 px column
floor. After cell padding, a Detail column configured with both `modal` and
`expand` modes cannot hold its buttons, and the second one is clipped. An
invalid-JSON preview button, about 50 px wide, also overflows its 48 px column.

**Reproduction.** Use `fitToScreen: true`, six or more data columns and native
Detail with `modal` and `expand`, at 390 or 320 px. In Pharos,
`--test-name-pattern='Analyze workspace query'` has 18 cases todo for this.
`'MCP query'`, `'Data guide'` and `'Analyze record keyword'` fail at the same
widths:

```text
a control is cut off where nothing scrolls:
button.qt-link-btn.qt-item-detail-button in td.qt-cell.qt-item-detail-column (10 of 24px shown)
button.qt-json-preview.qt-json-preview--invalid "JSON?" in td.qt-cell (36 of 50px shown)
```

**Request.** Keep every enabled native action reachable under screen fitting.
Count native interactive content and cell padding in each column's minimum
usable width, or provide a narrow-column presentation that keeps every
action. If the minimum widths exceed the viewport, scroll horizontally rather
than clip a control inside its own cell. Consider a public per-field minimum
width for host-rendered interactive content too.

**Acceptance.** Two or more Detail modes, valid and invalid JSON previews,
empty and loading states and custom cell padding all keep their actions
reachable. `fitToScreen` URL persistence, manual width overrides, column
resizing and fit-to-content are unchanged, and modal and inline focus return
still works.

## 7. Native Detail for grouped results

**Problem.** Native item Detail is disabled whenever a query is grouped, in
both places at v0.9.6:

```ts
// packages/react/src/useQueryTable.ts:565
const detailsEnabled = Boolean(opts.itemDetails) && !query.grouping;
// packages/ui/src/DataTable.tsx:141
const details = query.grouping ? undefined : props.itemDetails;
```

Pharos ends every Analyze table with its drill-in link columns followed by
native Detail. Grouped tables keep their link summaries and a Records
drill-down, but they cannot show Detail, so a group's keys and summaries can
only be read across a narrow row.

**Request.** Support the Detail column and its modal and inline property
views for grouped results. Use the effective grouped schema and show the
group's keys, scalar summaries and link summaries. Don't load a source item by
the group's ID. Let Detail be ordered after the link columns through the
existing select API. Use the library's canonical group identity for the
active row across paging and reload, and show an unavailable-group state when
the population changes. Showing group properties must not broaden the Records
drill-down's exact-match source query.

**Acceptance.**
- Group human messages by conversation, adding count and word-total
  summaries. Open a group's Detail, move through its properties and dismiss
  it; focus returns to the opener. The same works after paging and after a
  URL reload.
- Link summaries stay independent columns, and Detail can come last.
- Missing groups and null keys are covered. Ordinary item Detail is unchanged,
  including on returning from a group to its records.

---

# Query lifecycle (`useQueryTable`, saved queries, storage)

## 8. Save and restore host query state with the table query

**Problem.** Pharos adds a population clause to the native builder through
the public `whereRow` slot. On Analyze it is a keyword condition on one
record field. On Search it is a message, file or URL text search plus a
Table/Conversations display choice. Pharos runs those clauses itself; their
definitions live in extra URL parameters (`record_<dataset>`, or
`text_builder`, `kind`, `search`, `case` and `view`). The native Save, Update,
Saved, Default, Share, Reset and Undo/Redo controls only handle `QueryState`.
Loading a save can therefore change which records are searched while the UI
says the save was restored.

The API gap at v0.9.6:
- `SavedQuery` has `id`, `name`, `savedAt`, `query: QueryState` and an
  optional `snapshotCode`, with no host payload.
- `StorageAdapter.loadLast`, `saveLast`, `saveNamed` and `updateNamed` take
  only `QueryState`.
- `useSavedQueries` restores through `applyQueryState(q)`, so host state can't
  be restored in the same step.
- `matchesSavedQuery`, `summary`, `shareUrl` and `savedQueryControls` help with
  presentation, but they add no persistence or transaction.

**Request.** Add an optional, versioned **complete query definition**: the
table `QueryState` plus host state the host explicitly registers, which the
existing native controls save, restore, share, reset and undo as one unit.
The host keeps rendering and running its own clauses. Don't require a second
saved-search UI or a replacement builder. One possible shape:

```ts
type HostQueryState = { version: 1; params: Record<string, string[]> };
type QueryDefinition = { query: QueryState; hostState: HostQueryState };
interface SavedQuery { /* existing fields */ hostState?: HostQueryState }

useQueryTable({
  hostQuery: {
    ownedParameters: ["record_tool_calls"],
    defaults: { version: 1, params: {} },
    normalize: normalizeRecordKeywordState, // validate, migrate, canonicalize; may reject
  },
});
api.definition;     // the committed QueryDefinition
api.setDefinition(previous => ({ query: { ...previous.query, offset: 0 }, hostState: next }));
```

Whatever the names, these semantics are required:

1. URL writes replace exactly the registered parameters. They keep unrelated
   navigation parameters and other tables' state. Core and host can't both
   own a parameter.
2. Save and Update capture the committed definition before any I/O. Unapplied
   drafts and cancelled modal edits are never saved.
3. Identity covers the whole normalized definition. The same `QueryState` with
   different keyword text, field, scope, options or display is a different
   save. Incidental editor IDs and JSON key order are ignored.
4. A save without `hostState`, which includes every existing save, resets the
   registered parameters to their defaults. It clears an active keyword
   instead of inheriting it.
5. A malformed, oversized or unsupported-version payload rejects the whole
   restore. The live definition and URL stay untouched, and no broadened
   table-only query runs.
6. An instance without the host integration rejects a save carrying host
   state it doesn't support, rather than dropping it silently.
7. Grouping, HAVING, column changes and source drill-downs keep host source
   restrictions. Leaving grouping restores the source query and keeps host
   state.
8. A restore commits query, host state, URL, saved identity and execution
   generation in one transaction, so the transport sees the new host state on
   the same render. No request goes out for a mixed definition during a saved
   or default load, back/forward, reset or undo/redo. Requests that become
   obsolete are aborted and their late results are rejected.
9. One accepted host edit is one history entry, in the same undo/redo stream
   as table edits. Reset restores the table defaults and the registered host
   defaults together.
10. Storage gains optional definition methods (`loadLastDefinition`,
    `saveLastDefinition`, `saveNamedDefinition`, `updateNamedDefinition` or
    similar). When host state is configured and an adapter lacks them, Save
    reports that clearly instead of falling back to a table-only save.
    Table-only adapters keep working when no host state is configured.
11. Durable snapshots and `snapshotCode` identify the complete definition, so
    a short `@…` link resolves in another browser. Budget query and host bytes
    together; fail explicitly rather than truncate.

The Go executor doesn't need to interpret host JSON, and query-v3 `where`,
grouping and HAVING semantics don't change.

**Acceptance.**
- Save Tool calls grouped by Tool, with source Status = error, HAVING
  Calls > 2 and the Command keyword condition `(cache OR buffer) NOT
  obsolete`. Remove the keyword and change the grouping, then load the save:
  all of it comes back together, including the keyword chip and its editable
  modal, before any new request runs.
- A Search save with a phrase, a user-message scope and the Conversations
  display restores all three. Grouped rows are never handed to the
  conversation renderer.
- The cases in rules 2–11: legacy saves, defaults and last-used state,
  unapplied drafts, undo/redo, reset, grouping round-trips, Share links in a
  fresh browser, bad payloads, failed or unsupported writes, live edits during
  an async save, rapid loads and dataset switches. State and transaction tests
  belong in core and React unit tests.

## 9. Persist the last-used query only after it succeeds

**Problem.** `useQueryTable` saves the last-used query 400 ms after every
change, `setTimeout(() => void storage.saveLast(sourceSchema.name, query), 400)`
(`useQueryTable.ts`, line 1004 at v0.9.6), whether or not the query ever
produced a result. A slow request that later fails or times out has already
replaced the last-used state, so a bare reload restores a query that doesn't
work. The public API exposes `query`, `loading` and `error`, but nothing ties
a result to the exact query that produced it: during a change, those values
can still describe the previous result.

**Reproduction.** Mount a remote table with a recording `StorageAdapter` and a
controllable `Transport`. Complete query A, change to query B, hold B's
response past the 400 ms debounce, then reject it. Repeat with a validation
failure and a deadline. Remount without a query link: B was saved.

**Request.** Add an opt-in policy that saves only the last *successful* query,
or a public execution receipt carrying the exact query identity, execution
generation and result status, so a host can implement that policy. Keep
explicit URLs, named queries and configured defaults as separate user choices.
A successful empty result is a success, and an offline or service error does
not make a query invalid. Define success for queries with independent metrics:
rows that load while a metric fails are not a complete success. Report
restoration and execution phases separately, including failure and
cancellation, so hosts can offer retry and reset.

**Acceptance.**
- A succeeds and B fails or times out: a bare remount restores A, and B stays
  retryable.
- A slow A finishing after B can't mark B healthy or overwrite B's result.
- Empty successful results, including out-of-range pages, stay restorable.
- A cancelled or hidden table publishes no stale receipt.
- Invalid explicit links stay visible errors and never fall back silently to
  defaults. Named saves and the default are intact after a failure.

**Pharos today.** Pharos's storage adapter skips restoring a query whose row
request returned HTTP 400 or 422 or hit the client deadline. Pharos also
offers Reset and Retry controls. That only covers failures the host can see.

---

# Query capabilities (lower priority)

## 10. A minimum count over distinct values

**Problem.** `AggregationClause.minCount` is a row count
(`minCount?: number` in `packages/core/src/query.ts`). "API cost by model,
hiding models used in fewer than five conversations" needs a minimum on
distinct conversations. Pharos can only express it by first grouping records
into one row per conversation, which forces that view when a records-level
threshold would do.

**Request.** Add `minCountDistinct: { field, min }`, or let `minCount` take
`{ min, distinct: field }`. Run it natively, remove groups before
`groupLimit` and report them in `hiddenGroups`, as `minCount` does.

**Acceptance.**
- Grouped by `model_family` with `{ field: "conversation_id", min: 5 }`, a
  model with 40 records in 3 conversations is hidden. A model with 6 records
  in 6 conversations is kept.
- `hiddenGroups` lists the hidden groups.
- `/metrics`, `/query-v3` and the in-process evaluators agree.
- `metricPopulationError` reports hosts without support, as it does for
  `minCount`.

## 11. A minimum over a summed field

**Problem.** Some datasets are rollups. One row of Pharos's `tools` dataset
holds one day's calls for one tool group, so `minCount` counts daily
summaries, not calls. Hiding tools with fewer than ten calls needs a minimum
on `SUM(call_count)`. Pharos works around this with a grouping stage that has
`HAVING calls >= 10`. The chart can't then report what was removed, so the
dashboard can't say how many tools it hid.

**Request.** `minCount: { min, sum: field }` (or `minSum: { field, min }`),
removing groups before `groupLimit` and reporting them in `hiddenGroups`.
Request 10's distinct form could share the same shape.

**Acceptance.**
- A failure rate grouped by `tool_name` with `{ min: 10, sum: "call_count" }`
  hides a tool whose rows hold 9 calls. It keeps one with 3 rows holding 12.
- `hiddenGroups` lists the hidden groups, and the kept groups' values are
  unchanged.
- `/metrics`, `/query-v3` and the in-process evaluators agree.

## 12. Calendar keys in a grouping stage

**Problem.** A clause can group a datetime field by calendar interval with
`dateGrouping`, but a grouping stage's `keys` can't. Pharos's dashboards over
timestamped records (MCP requests, tool calls, skill evidence, user messages,
hourly parallelism) chart daily or hourly totals. Their "View supporting
query" link should open those totals as grouped rows, but it can only open the
raw records.

**Request.** Allow a grouping key with a calendar interval, for example
`keys: [{ field: "called_at", interval: "day", timeZone }]`, producing one
grouped row per interval keyed by the interval's start in that zone.

**Acceptance.**
- Grouping requests by `called_at` per local day in `America/Denver` gives one
  row per local date, keyed by its local midnight. The totals match the
  equivalent `dateGrouping` clause. The same holds for UTC hours.
- Group filters, ordering and drill-down (`inspectBucketQuery` on a grouped
  row) select exactly that interval's records.

## 13. Disjunctions of conjunctions in `where`

**Problem.** `where` is an AND of terms, each a clause or an OR of clauses. A
legend selection of combined groups, such as the (provider, model) pairs
(claude, opus) and (codex, gpt-5), is an OR of ANDs. Pharos rewrites it as
the equivalent AND of ORs, which grows as fields^pairs. Past 64 terms Pharos
stops filtering and picks the selected groups from those already returned,
so a selected group beyond the group limit goes missing.

**Request.** Allow a term that is an OR of conjunctions,
`{ any: [{ all: [clause, clause] }, { all: [clause, clause] }] }`, on
`/metrics`, `/query-v3` (source and group filters) and in process.

**Acceptance.**
- Ten (provider, model) pairs filter in one term, and the result equals the
  union of the ten pairs' records.
- A group filter on grouped rows accepts the same term.
- `decodeQueryStrict`, `encodeQuery` and the Go link codec round-trip it.

---

# Performance (lower priority)

## 14. Close the remaining V2 scalar-metric gap

**Problem.** Pharos still answers plain integer count and sum batches with its
own cached SQL reducer, `nativeMetricsV2` (`internal/archive/query_metrics_native.go`).
It compiles and validates each request through Query Table, then runs one
reduction per grouping, with exact storage checks, safe-integer checks and
caches scoped to a data generation. At the 0.9.4 upgrade, Pharos's synthetic
HTTP probe measured the adapter at **49–77 ms** and the host reducer at
**16–33 ms**, with identical responses. That gap is why the reducer stays.
Direct V3 grouping (66 ms against the deleted host reducer's 45 ms) was
accepted as a fair trade and is not requested again.

**Reproduction.** In Pharos:

```sh
PHAROS_TEST_PROCESSES=1 GOWORK=off go test -tags probe \
  -run '^TestUsagePerformanceSQLHost$' -v -count=1 ./internal/archive
```

It builds a temporary 50,000-row catalog and calls the real HTTP routes,
switching between host and adapter execution. One request is a V2 batch of
`COUNT` plus nine `SUM`s over stored integer columns, run ungrouped, grouped by
`day`, and grouped with a `provider = "codex"` filter. The probe checks that
both paths return identical complete responses.

**Request.** Profile normal V2 scalar execution on that shape. Look at proofs,
planning, staging, savepoints and result mapping rather than assuming a
cause, and reduce the overhead enough to decide whether the host reducer can
go. Distinguish the host's cached source bounds from guarantees the library
must check on every execution for arbitrary input.

**Acceptance.** Cross-field sharing, independent metric errors, formulas,
sorting, top-N, minimum counts, exact decimals, budgets and revisions are
unchanged. Report full adapter cost next to guarded and bare SQL. Pharos's
`TestNativeMetricsMatchTheAdapter`, `TestNativeMetricsMatchTheAdapterOnPharosDatasets`
and the probe pass with the host reducer turned off.

## 15. Accept an exact host-provided predicate relation

**Problem.** Pharos keeps a transactionally maintained trigram index for
substring filters on large text fields (`internal/archive/query_text_index.go`).
For ASCII needles of three or more characters, its membership relation is
exact, so the typed `contains` check need not run again over wide payloads. It
can only use the index through host-specific execution paths.

**Request.** A seam that lets a host supply a relation for a predicate,
marked *exact* or *candidate*. Only a complete exact relation may replace the
predicate; candidates keep the final typed check. OR pushdown is allowed only
when every branch has a safe relation. Negation keeps the compiler's null and
empty guard. Every relation uses the execution's data snapshot.

**Acceptance.** Results match ordinary execution for short and non-ASCII
needles, unsupported operators, incomplete backfills, negation and OR.
Pharos's parity cases are in `internal/archive/tool_search_test.go`.

## 16. Stop source-filter controls flooding metadata requests

**Problem.** As a grouped view initializes, its source filter controls issue
many repeated metadata reads (distinct values and suggestions). In Chrome over
HTTP/1, these fill the origin's connection limit and delay the foreground V3
query; one audit saw a 20-second transition despite fast execution. Pharos
now shares in-flight metadata reads, keeps a result for five seconds and
allows one metadata request on the network at a time
(`web/src/query-metadata.ts`).

**Request.** Keep source-filter API and callback identities stable across
renders, and fetch suggestions only for controls that need them, such as an
open value editor.

**Acceptance.** Opening a grouped table with several source filters issues at
most one metadata read per control that needs one, and none for closed
editors. Rows and metrics are never blocked behind them.

---

## Not requested here

These belong to Pharos:
- `/query-v3` grouping Usage by `hour`: Usage holds day-grained rows plus a
  separate hourly ledger.
- Schema reloads and the scratch-table transaction policy: 0.9.3's cleanup
  contract already supports them.
- Adopting 0.9.6's windows and shares in place of Pharos's own trend
  smoothing.

## Delivery

Ship focused, independently reviewable changes. Put correctness tests in the
library's unit suites and layout and focus checks in its browser suites; avoid
exact pixel assertions. Publish matching Go, core, react, ui and codegen
releases with migration notes for any new option. Pharos will then upgrade
all five pins, regenerate schemas, rebuild its frontend, and remove the
matching `KNOWN` entries and browser workarounds.
