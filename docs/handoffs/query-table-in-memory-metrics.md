# Handoff: run V2 metrics over rows in memory (QT-16)

For the Query Table team. File paths are in the Query Table repository unless
marked **Pharos**.

## Assignment

Add a Go API that runs a V2 `MetricQuery` (its row filter and its metrics) over
rows a host already holds in memory. It must return what the SQLite adapter
returns for the same rows after staging them. Also export the row filter on its
own.

Today a Go host can filter and reduce only in SQL. A host whose rows are built
in memory must copy them into a temporary table on every request, and the
adapter then reduces each metric in its own pass over that copy. The TypeScript
core already has an in-memory executor (`applyAggregations` and `matchesClause`
in `packages/core/src/apply.ts`); the Go backend has none.

## Why Pharos needs it

- **Usage is built in memory.** Pharos computes its Usage rows in Go, from a ledger joined with pricing, carbon settings and conversation lineage. They are not a table. A catalog has tens of thousands of daily rows and more hourly ones.
- **Staging is slow.** Before Pharos added its own fast path, Usage metric requests through the adapter were 8–12× slower than Pharos's old single-pass route:
  - Token summary (five sums and a distinct count): 3 ms → 29 ms;
  - weekly cost by provider: 2 ms → 20 ms;
  - hourly cost by provider: 36 ms → 282 ms.
- **Pharos's workaround cannot filter.** Pharos answers unfiltered plain counts, sums and distinct counts over its rows itself (**Pharos** `internal/archive/query_metrics_native.go`: `nativeRowMetricsV2`, `nativeRowGroups`). It reproduces the adapter's exact decimal sum, rounding, budgets and bucket order (**Pharos** `query_metrics_exact.go`: `exactSum`, `adapterNumbers`). It declines any request with a `Where`, because Pharos has no in-memory filter that agrees with `CompileSQLiteWhere`. So the common case, a user narrowing by repository, model, provider or time range, still stages a copy, and cost sums and distinct counts take the per-metric path.
- **Pharos's older filter disagrees with the compiler.** Pharos still filters Usage's hourly rows with its own pre-adapter filter (**Pharos** `internal/querytable/apply.go`). For example, it reads `contains` on a NULL as `contains` on `"<nil>"`, so `repository_name contains "nil"` matches rows with no repository; the SQLite compiler never matches NULL there. One chart request can therefore filter its hourly and daily metrics differently. Pharos wants to delete that filter and use Query Table's.
- **The rules belong upstream.** Mirroring `sqliteLiteral` in Pharos would mean copying unexported helpers (`sqliteParseTime`, `parseRelativeDuration`, `relativeTimestamp`, `coerce`, and the functions behind `qt_lower`, `qt_ends_with`, `qt_length`, `qt_regexp`, `qt_datetime` and `qt_array_json`), and the copy would drift. Upstream also already has the parity suites (`backends/sqlite-tests`) that compare the TypeScript evaluators with SQLite.

This is separate from QT-14 (fuse metrics over different fields into one SQL
pass). QT-14 alone would still copy the rows on every request. This request
would let Pharos delete its in-memory path and its old filter.

## Request

Names are illustrative.

1. **A row filter.** `CompileRowFilter(terms []WhereTerm, s Schema, o SQLiteOptions) (RowFilter, error)`, where `RowFilter.Match(row map[string]any)` selects exactly the rows `CompileSQLiteWhere` selects over the same rows staged.
   - It must return the same compile errors: unknown or disabled field, op not enabled, invalid regex, unparsable datetime, non-finite number, invalid length.
   - It must skip a blank value the same way. A blank value is a no-op except for `is_null`, `is_not_null`, and `=`/`!=` on text and enum.
   - It must treat NULLs the same way:
     - for text, enum and datetime, `is_null` also matches `""`;
     - text `=`/`!=` compare `COALESCE(value, '')`;
     - number, bool and datetime `!=` match NULL;
     - a negated predicate never matches a null value.
   - It must handle everything else the same way: lower-casing, code-point length, RE2 regex, datetime parsing and `utc-millis` rounding, relative times against `o.Now`, and case-sensitive or case-insensitive array `includes`.
   - A filter on a computed field, or anything else it cannot evaluate exactly, returns a sentinel error (say `ErrRowsUnsupported`) at compile time.
2. **Metrics over rows.** `ExecuteV2Rows(ctx, tx, d SQLiteV2Dataset, rows []map[string]any, q MetricQuery, o PlanOptions) (SQLiteV2ExecutionResult, error)`. It should:
   - apply `q.Where` with the row filter;
   - reduce every metric that shares a grouping in one pass over the matching rows;
   - return what staging the rows and calling `ExecuteV2In` returns. That covers:
     - bucket values and order;
     - `GroupCount`, `ProcessedRows` and `Coverage`;
     - exact decimal sums rounded once;
     - per-metric errors (unsafe integers, out-of-range values, text in a number field, a group budget exceeded);
     - request errors (population over `MaxPopulation`, failed validation, stale `ExpectedRevisions`).

   `tx` is there for revisions and computed definitions; a request that needs neither should not touch it.
3. **An explicit fallback.** For any metric, grouping or value it does not compute exactly, return `ErrRowsUnsupported` before returning a result, so the host can stage the rows instead. The minimum Pharos needs:
   - plain `count`, `count` of a field, `sum` of a number field, and `count_distinct` of a text or enum field;
   - grouped by text or enum fields;
   - every filter op on base fields.

   `avg`, `min`, `max`, per-metric `where` and `minCount` would let Pharos drop more of its staging.

### What counts as "the same rows staged"

The reference is binding each map value as a `database/sql` parameter into an
untyped temporary-table column:
- `string` → TEXT;
- `int64` → INTEGER;
- `float64` → REAL;
- a missing key or `nil` → NULL;
- a textarray as its JSON text;
- an empty datetime string → NULL.

**Pharos** `stageQueryRows` in `internal/archive/query_table_v2.go` does exactly
this. Please document which Go types each field kind accepts. Where a value's
type does not match its field, such as text in a number field, either reproduce
SQLite's comparison by storage class or return `ErrRowsUnsupported`. Never
return a different answer. Exporting the staging helper itself would give hosts
and tests one shared reference.

### What Pharos sends

- **Token chart:**
  - one `sum` of `total_tokens` or `cost_usd`, grouped by a period (`hour`, `day`, `week` or `month`, all text) and a split (`provider`, `model`, `repository_name`, and so on);
  - or, split by token type, five sums grouped by the period.
- **Token summary:** five ungrouped sums and `count_distinct(agent_session_id)`.
- **Filters:**
  - time ranges on `first_usage_at`/`last_usage_at` (datetime, `rfc3339`) or `hour` (text);
  - set and equality filters on enums such as `provider`;
  - any text op on `repository_name`, `model` and the rest of the table's fields.
- **Values:** `cost_usd` is a fractional float64, and many token counts are int64.

## Acceptance

- **Differential tests in `backends/sqlite-tests`.** For generated rows and requests, `ExecuteV2Rows` equals staging the same rows and calling `ExecuteV2In`, including:
  - NULLs, missing keys and empty strings in every field kind;
  - non-ASCII case for `contains`, `starts_with`, `ends_with` and `includes`;
  - datetimes in each accepted text form and at millisecond boundaries;
  - relative times with a fixed `Now`;
  - regex and code-point lengths;
  - ten `0.1`s summing to exactly `1`;
  - integers beyond 2^53, text in a number field, and exceeded population and group budgets.
- **The filter alone.** `RowFilter` selects the same rows as `CompileSQLiteWhere` over the same fixture. If the cases live in `schema/fixtures`, the TypeScript `matchesClause` can run them too.
- **One pass.** N metrics over different fields that share a grouping and filter read the rows once. Count visits as `TestV2ReductionReuse` does.
- **Speed.** A benchmark over 50,000 rows with a filter and the Token summary's six metrics is faster than staging plus the adapter. Pharos's target is a filtered Token summary within about 1.5× of its unfiltered fast path (about 3 ms).

## What Pharos does once this lands

- Take the release, or copy it into **Pharos** `third_party/query-table-go` and note it in `README.pharos.md`.
- Replace `nativeRowMetricsV2`, `nativeRowGroups`, `exactSum` and `adapterNumbers` with `ExecuteV2Rows`. Filtered Usage requests then stop staging, and staging remains only for `ErrRowsUnsupported`.
- Replace `internal/querytable.Filter` in `hourlyUsageForAggregation` (**Pharos** `internal/archive/usage_table.go`) with `RowFilter`, so hourly and daily metrics filter by the same rules.
- Finish **Pharos** `docs/handoffs/usage-filtered-metrics-fast-path.md`, with probe numbers before and after.
