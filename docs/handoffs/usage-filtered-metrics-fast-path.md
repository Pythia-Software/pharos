# Handoff: measure and speed up filtered Usage metrics

## Assignment

#157 moved every metric onto `/metrics` and added a native path that keeps
unfiltered Usage charts at legacy speed. Usage requests **with a filter** that
sum cost or count distinct values were never measured, and they take the
adapter's slower path. Measure them, and bring any that are materially
slower back to parity. Also confirm one CPU reading flagged during #157.

## Evidence

- **Unfiltered requests are fast.** `nativeRowMetricsV2` (`internal/archive/query_metrics_native.go`) answers Usage's counts, sums and distinct counts over the rows already in memory. Decimal cost sums go through `exactSum` (`query_metrics_exact.go`), which adds exactly and rounds once, the way the adapter does.
- **Filtered requests are not.** It declines any request with a `Where` (`if len(q.Where) > 0 { return …, false, nil }`). Those requests fall through to the adapter, which copies Usage's rows into a temporary table and reduces each metric in its own pass.
- **Measured unfiltered.** Before the native path existed, unfiltered requests on that adapter path were 8–12× slower than legacy:
  - the Token summary: 3 ms → 29 ms;
  - weekly cost × provider: 2 ms → 20 ms;
  - hourly cost × provider: 36 ms → 282 ms.
- **Machine Time all time.** In #157's last two probe runs it took about twice the CPU of the legacy route (wall time about equal). Earlier runs measured 1.0–1.3×, so this is probably load. Confirm or fix it.

## Where to look

- **The dispatch.** `internal/archive/query_table_v2.go` dispatches between `countOnlyMetricsV2`, `machineTimeMetricsV2`, `nativeMetricsV2` and the Usage branch that calls `nativeRowMetricsV2` (search for those names).
- **Filters.** `qt.CompileSQLiteWhere` compiles the adapter's own filter. The SQL native path already uses it. The in-memory path would need an equivalent row predicate, or should push filtered requests to SQL.
- **Parity tests.** `internal/archive/query_metrics_native_test.go`: `TestNativeMetricsMatchTheAdapter`, `TestNativeSumsMatchTheAdapterExactly` and `TestNativeMetricsMatchTheAdapterOnPharosDatasets`. They switch `nativeMetrics` off to compare with the adapter.

## Approach

1. **Measure.** Write a `-tags probe` test (see `AGENTS.md`) that times, with `nativeMetrics` on and off, on a synthetic Usage fixture of realistic size: tens of thousands of hourly rows, about 10 repositories, models and providers. Cover:
   - Token chart cost and distinct requests with typical filters (repository, model, provider);
   - Machine Time all time.

   Record warm medians and process CPU.
2. **Decide.** If a filtered surface is more than about 1.5× slower than an unfiltered one of the same size, extend the native path to it. Keep the existing rule: answer only what you compute exactly as the adapter does, and fall through otherwise.
3. **Report.** If the cleaner fix is upstream (QT-14 in `docs/query-table-next-release-handoff.md`), say so.

## Done when

- The probe results are recorded in the PR.
- Every filtered surface you extended has parity cases in `query_metrics_native_test.go`, including NULLs, empty strings and the exact-sum drift case.
- `go test ./...` passes.
