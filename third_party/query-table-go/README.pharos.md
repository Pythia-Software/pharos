# Pharos Query Table backend patch

This directory retains the MIT-licensed Go backend from Query Table **v0.9.0**
(release commit `bd2c730f0242f9d2b3e0913d55f1cd7c0d3229a2`), without upstream's
test files. Core, React, UI and codegen use the published **0.9.0** npm packages.
The root `go.mod` replacement selects this backend copy.

## Remaining local patch

Only two Go source lines differ from v0.9.0:

- `query_v3.go`, `QueryDatasetV3.limits`: accept an explicit host `MaxGroups`
  up to **100,000**, instead of upstream's 10,000.
- `sqlite_v2_dataset.go`, `SQLiteV2Dataset.limits`: the same host-budget ceiling
  for SQLite V2 metrics.

Defaults, row limits, population/byte/sample budgets and group-budget
enforcement are unchanged. Pharos opts into 100,000 groups only for Usage's
compact facts, including the Work preset for libraries with more than 10,000
workspaces. Other datasets retain their existing budgets. This is a host
configuration allowance; requests cannot raise it themselves.

The retained source otherwise matches the release exactly, including its new
in-memory metric executor. Pharos's host metric accelerators remain separate
from this patch.

## Performance patches now supplied upstream

The four local v0.8.0 performance patches have been replaced by Query Table's
released implementations:

- Reuse representative rows for identity validation and ROW projection (#127).
- Reuse validated staged values and their error sidecars (#128).
- Fuse compatible scalar SQLite V3 group summaries (#129).
- Inline cheap SQLite projections while preserving shared expensive evaluation
  and V2 reduction reuse (#133).

The previous SQL substring heuristic and unconditional field-guard inlining are
gone. The v0.8.0 `TestV2ReductionReuse` failure is no longer an accepted exception:
the upstream gate must pass without `-skip`.

## Testing and upgrades

`go test ./...` from the Pharos root does not run this nested module's upstream
tests. Use the release's own Go and real-SQLite suites against this copy:

```sh
tools/test-query-table-fork.sh
tools/test-query-table-fork.sh -run 'TestV2ReductionReuse|TestV3' -count=1
```

The script derives its default release tag from the root `go.mod`, extracts
upstream tests and schema fixtures into a temporary directory, and substitutes
this copy's sources. `QUERY_TABLE_UPSTREAM` selects the upstream checkout
(default `~/gbdubs/query-table`); `QUERY_TABLE_TAG` can override the release for
an explicit compatibility check. Neither checkout is modified.

Pharos's `query_table_v3_adapter_test.go` retains grouped-result integration
checks and `TestQueryTableGroupBudgetOverrides` for the remaining budget patch.
`TestUsageFusedSummariesMatchIndependentMetrics` compares grouped Usage values
with independent metric execution.

The [upstream host group-budget request](../../docs/handoffs/query-table-host-group-budget.md)
asks for the final allowance needed to remove this copy. Do not add or extend
local Query Table patches; required changes belong upstream, as specified in
[`AGENTS.md`](../../AGENTS.md). Once a published release accepts these budgets,
upgrade the package pins, remove the root replacement, this directory and the
fork test script, then run the host release checks with Usage's budgets intact.
