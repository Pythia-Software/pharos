# Handoff: make in-memory V2 metrics as fast as a hand-written reducer (QT-17)

For the Query Table team. File paths are in the Query Table repository unless
marked **Pharos**. This follows QT-16
([query-table-in-memory-metrics.md](query-table-in-memory-metrics.md)), which
v0.9.0 delivered as `ExecuteV2Rows` and `CompileRowFilter`.

## Assignment

Speed up two hot spots in `ExecuteV2Rows` without changing any result:

- **Group keys.** Building a group key costs several times the rest of the row's work.
- **Decimal sums.** Sums of everyday float costs leave the scaled-integer fast path for `big.Rat` arithmetic on every row.

Pharos has moved Usage onto `ExecuteV2Rows` and deleted its own in-memory
reducer. Filtered requests got 5–30× faster, but unfiltered ones, which are
Usage's default view, got 2–3.5× slower than the reducer Pharos deleted.

## Evidence

**Measured on Pharos's probe** (**Pharos** `TestUsagePerformanceFilteredMetrics` in
`internal/archive/usage_performance_test.go`). The fixture has 20,000 daily and
60,000 hourly synthetic Usage rows, with 10 repositories, models and providers.
Times are warm medians on a Mac Studio (Apple M3 Ultra). Real catalogs have about
33,000 daily rows.

| Unfiltered request | Pharos's reducer (deleted) | `ExecuteV2Rows` |
|---|---|---|
| Token summary: 5 sums (one of `cost_usd`) and `count_distinct(agent_session_id)` | ~12 ms | ~40 ms |
| Weekly `cost_usd` by provider | ~8 ms | ~23 ms |
| Daily token types: 5 integer sums by day | ~9.5 ms | ~16 ms |
| Hourly `cost_usd` by provider, 60,000 rows | ~22 ms | ~60 ms |

**The two causes, isolated.** Each line runs `ExecuteV2Rows` alone over 20,000 rows:

| Metric | Time |
|---|---|
| `count`, ungrouped | 0.40 ms |
| `count` grouped by two text keys (`week`, `provider`) | 7.6 ms |
| `sum(total_tokens)` (int64), ungrouped | 0.82 ms |
| `sum(cost_usd)`, values rounded to 6 decimal places | 1.6 ms |
| `sum(cost_usd)`, the same values as Go computes them | 23 ms |
| `count_distinct(agent_session_id)` | 1.4 ms |

A CPU profile of the unfiltered Usage requests attributes most of the time to:
- **`rowMetricCohort.group`:** 26% of CPU, 43% of that in `encoding/json.Marshal`.
- **`rowDecimalAggregate.add`:** 20%, almost all of it in `sqliteV2Aggregate.Step` and `big.(*Int).lehmerGCD`.
- **Garbage collection:** a large share, driven by both.

### 1. Group keys are JSON-encoded on every row

`rowMetricCohort.group` (`backends/go/rows_metrics.go`) does three things for
each matching row:
- it allocates a `keys` slice;
- it marshals that slice to JSON;
- it converts the bytes to a string to look the group up.

All of that happens even when the group already exists, which is almost every
row. Group keys are only text, enum or bool, or NULL, so a much cheaper
encoding identifies them exactly. For example:
- length-prefix each value into a reused `[]byte`, with distinct markers for NULL, and for `false` and `true`;
- look the group up with `c.groups[string(buf)]`, which Go compiles without allocating;
- allocate the key slice and string only when a group is created.

### 2. Float noise sends sums to `big.Rat`

`rowDecimalAggregate` (`backends/go/rows_decimal.go`) keeps a sum as an int64
coefficient at one shared scale. It promotes to the shared `sqliteV2Aggregate`
(`big.Rat`) for good as soon as either of these happens:
- **a value needs 19 or more decimal places.** `rowDecimalParts` stops at `len(rowPowersOfTen)`. Everyday float costs hit this: 1,834 tokens at $0.30 per million, computed in float64 (`tokens * price / 1e6`), is `0.0005501999999999999`.
- **rescaling to the largest scale seen would overflow int64.** At scale 18, a value with 16 or 17 significant digits fills int64 within tens to hundreds of additions.

Costs computed in Go do both. `cost_usd` is tokens × price per token, and a
day's cost is a float sum of its hours. Once promoted, every later value pays a
`big.Rat` add, which allocates and reduces by GCD.

**Prior art in Pharos:** the deleted `exactSum` stayed fast on the same
values. It is in **Pharos** `internal/archive/query_metrics_exact.go` at commit
`71976f8`. It kept one int64 accumulator per decimal exponent (`m×10^e` adds
into the part for `e`), so no value ever forces a rescale. A part moves to
`big.Int` only when that part would overflow. It built one `big.Rat` at the
end, to round once.

## Request

1. **Group without per-row encoding or allocation**, as above. The current group identity, bucket keys and order, and group budget must stay as they are.
2. **Keep exact sums off `big.Rat` per row.** Possible approaches:
   - accumulate by decimal exponent, as `exactSum` did;
   - or use a `big.Int` coefficient with an explicit scale, so adds never reduce by GCD.

   Convert to a rational once, for the final rounding. Everything the result depends on stays as it is:
   - exact decimal semantics (ten `0.1`s sum to exactly `1`);
   - error priority (`unsafe_integer`, `storage_type`, out-of-range values);
   - the absolute-sum bound;
   - AVG's use of the same accumulator.
3. **Add benchmarks** to `backends/go`, so regressions show up:
   - 20,000 rows, a count grouped by two text keys;
   - an ungrouped sum of float-noise decimals;
   - the six-metric Token summary above.

## Acceptance

**Results:**
- Every existing V2 and in-memory parity suite passes unchanged, including `backends/sqlite-tests`.
- Results stay identical to staging the rows and calling `ExecuteV2In`.

**Benchmarks**, over 20,000 rows on comparable hardware:
- a count grouped by two text keys takes at most about 2 ms;
- a sum of float-noise costs takes at most twice the same sum over 6-decimal-place values.

**In Pharos**, `TestUsagePerformanceFilteredMetrics` puts each unfiltered request
within about 1.25× of the deleted reducer:
- Token summary ≤ 15 ms;
- weekly cost by provider ≤ 10 ms;
- hourly cost by provider ≤ 28 ms.

## What Pharos does once this lands

Upgrade the package, and rerun **Pharos** `TestUsagePerformanceFilteredMetrics`
to record the numbers. No Pharos code needs to change.
