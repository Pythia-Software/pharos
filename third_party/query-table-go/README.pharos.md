# Pharos Query Table backend patch

This directory retains the MIT-licensed Go backend from Query Table **v0.8.0**.
The frontend stays on v0.8.0. The root `go.mod` replacement makes these backend
fixes reproducible without modifying the module cache or requiring an upstream
release.

Local changes:

- Inline cheap native field, CASE and numeric-guard projections in SQLite.
  Keep datetime/function payload evaluation materialized.
- Reuse executor-validated typed values and their error sidecars after staging.
  Caller-provided storage still passes the original validation.
- Fuse scalar V3 group summaries through the existing expression compiler and
  exact aggregate functions. Sampled summaries retain their original execution
  and budgets. Fall back when a combined plan exceeds compiler limits.
- Materialize representative rows once for both identity validation and update.
- Permit an explicit host group budget up to 100,000. Pharos uses this only for
  Usage's compact facts; other dataset budgets remain unchanged.

Revision checks, source authorization, staging byte/population checks, numeric
errors, lazy formula branches, representative validation, and cancellation stay
on their existing paths. Host regression tests compare fused summaries with
independent metric execution.

## Testing the patches

This copy holds no tests. `go test ./...` from the Pharos root would not run
them, because this is a nested module, and the published module has no V3
tests: those are in upstream's `backends/sqlite-tests`. Instead, run
upstream's suites at the release tag against this copy:

```sh
tools/test-query-table-fork.sh            # QUERY_TABLE_UPSTREAM defaults to ~/gbdubs/query-table
tools/test-query-table-fork.sh -run TestV3 -count=1
```

The script reads the tag from an upstream checkout with `git archive`. It then
runs upstream's `backends/go` package tests and its `backends/sqlite-tests`
suite with this copy's sources in place of upstream's, together with the
`schema` fixtures both suites read. Run it after changing this copy.

Known result at v0.8.0 (2026-10-19): 171 tests pass and 23 skip, most of
them because they need Postgres. One fails: `TestV2ReductionReuse` in
`backends/sqlite-tests`, which passes on unpatched v0.8.0. When `AVG` or
`MIN` is batched with `SUM` over the same field, this copy evaluates the
source once more for each of them: 39 or 54 source rows visited against one
scan's 24. The metric values match. Fix it here, or record why it is
acceptable. Until then, `-skip TestV2ReductionReuse` makes the rest a
pass/fail gate.

In Pharos, `TestVendoredPatchFormulaGroupErrorIdentities` and
`TestVendoredPatchHiddenAndDefaultSort`
(`internal/archive/query_table_v3_patch_test.go`) exercise the patched
grouped-summary path in the default test run.

Review local changes against v0.8.0 when updating this copy; remove the root
replacement once an upstream release includes equivalent fixes.
