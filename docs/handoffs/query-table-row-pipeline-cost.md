# Query Table: per-row cost of the typed metric pipeline

Pharos consumes unmodified Query Table 1.0.3 (Go `backends/go` v1.0.3). No
local dependency patches are permitted; this is a request for upstream work.

## Problem

A V3 metric over a large SQL source spends most of its time in the staged
typed pipeline, not in the host's source statement. On a 6.5 million row
source (`agent_event_rates`: one row per message read and reported kind) the
Home card "friction rate by repository and kind, 90 days" runs 8.5 s, of
which the source statement accounts for 2.4 s.

Measured on a replica of a production catalog (2026-11-09, SQLite via
modernc v1.61, 28 cores, warm page cache):

| statement | rows reaching the pipeline | time |
|---|---|---|
| the host source alone, grouped by the same two keys with `SUM` and `COUNT(DISTINCT)` | 1,529,479 | 2.4 s |
| the V3 metric request Query Table compiles over that source | 1,529,479 | 8.5 s |

The request is one grouped metric: `SUM([events])/NULLIF(COUNT_DISTINCT([message_id])/1000,0)`
grouped by `repository_name, family` with `groupLimit: 200` and a `minCount`.

## What the compiled statement does

For each field the metric reads, the plan adds a `MATERIALIZED` CTE that
copies every surviving row and calls a Go function per row:

```sql
qt1 AS NOT MATERIALIZED (SELECT f5,f6,f11,f20 FROM qt0 WHERE …),
qt2 AS MATERIALIZED (SELECT f5,f6,f11,
  CASE WHEN COALESCE(NULL,qt_v2_field_error(f20,'text')) IS NULL THEN f20 END COLLATE BINARY AS v1,
  COALESCE(NULL,qt_v2_field_error(f20,'text')) AS e1 FROM qt1),
qt3 AS MATERIALIZED (SELECT f5,f11,v1,e1, … qt_v2_field_error(f6,'text') … FROM qt2),
qt4 AS MATERIALIZED (SELECT f11,v1,e1,v2,e2, … qt_v2_field_error(f5,'number') … FROM qt3),
qt6 AS MATERIALIZED (SELECT v1,e1,v2,e2,v4,e4, … qt_v2_field_error(f11,'text') … FROM qt5),
qt7 AS MATERIALIZED (SELECT v1 AS group0, … qt_v2_sum(v4) …, COUNT(DISTINCT v5) … FROM qt6 GROUP BY v1,e1,v2,e2)
```

So 1.5 million rows are written to four temporary B-trees in turn, with two
`qt_v2_field_error` calls written per row per stage (up to eight Go
callbacks per row through the driver's function bridge), before the one
`GROUP BY`. The
aggregate itself (`qt_v2_sum`) is another callback per row.

A grouped request pays for the pipeline twice. Before it stages the validated
rows, the executor runs the same chain inside a budget probe
(`SELECT COUNT(*),COALESCE(SUM(length(...)),0) FROM (WITH qt0 ... LIMIT 10000001) AS qt_source_budget`),
then runs it again to fill the staging table. On the Home card "Tool calls
by operation and activity, 30 days" (268,740 rows, four validated fields) the
request takes 4.9 s: 2.0 s in the probe and 2.2 s in the staging statement
that repeats it.

The same shape appears in every metric over a SQL source, so the cost grows
with the rows a metric reads on Pharos's other large sources as well (Tool
calls, Message labels, Model requests); those were not measured separately.

## Request

Any of these would remove most of the cost; the first two need no host
change:

1. **Validate in one pass.** Compute every field's `(value, error)` pair in
   a single `NOT MATERIALIZED` projection feeding the `GROUP BY`, instead of
   one materialized stage per field. If materialization is there so the
   check runs once per row, a single stage that computes every field's
   error column, followed by one projection that reads them, does the same
   with one copy instead of four.
2. **Skip validation the schema already proves.** A field whose declared
   kind is `text` cannot fail `qt_v2_field_error(…,'text')` for a SQL source
   column that is `TEXT` or `NULL`. A host hint on the dataset (per field:
   "this binding is already of its declared kind") would let the compiler
   bind `v = f, e = NULL` directly.
3. **Native aggregates for safe domains.** `SUM` over an integer field the
   host declares safe (no values beyond 2^53) could use SQLite's `SUM`
   instead of `qt_v2_sum`, as the grouped-source reducer added in 0.9.4
   already does for summaries.

4. **Budget while staging.** Enforce the row and byte budget as the staging
   statement writes (or from the staged table afterwards, dropping it when
   over), so the chain runs once.

Semantics to preserve: error precedence (`MIN(e)` per group), NULL versus
zero sums, unsafe-integer detection where the host has not declared the
field safe, `COLLATE BINARY` group identity, `minCount`, `groupLimit`, and
revision checks.

## Reproduce

Any SQLite source with a few million rows: `id TEXT`, `g1 TEXT`, `g2 TEXT`,
`n INTEGER`, `m TEXT`. Request one metric
`SUM([n])/NULLIF(COUNT_DISTINCT([m]),0)` grouped by `g1, g2`. Compare with
`SELECT g1,g2,SUM(n),COUNT(DISTINCT m) FROM source GROUP BY 1,2`. Profile the
Go process: time is in the function bridge and temporary B-tree inserts.

## Where Pharos stands

Pharos narrowed what it can on its side (PR #621: joins once per message,
kind and prompter filters applied in the source, a multiplied count). The
card's remaining 6 s is the pipeline above. Pharos has host reducers for
grouped sources and for Usage (`query_groups_native.go`,
`executeUsageQueryV3`); a general host reducer for metrics would duplicate
the compiler, which is the kind of local workaround the dependency policy
rules out.

Done when a published release runs the request above within about 1.5x of
the hand-written aggregate, and Pharos consumes it by a normal version bump.
