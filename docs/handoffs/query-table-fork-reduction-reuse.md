# Handoff: fix reduction reuse in our patched Query Table adapter

## Assignment

Our patched copy of the Query Table Go backend
(`third_party/query-table-go`) fails one of upstream v0.8.0's own tests:
`TestV2ReductionReuse`. When `AVG` or `MIN` is batched with `SUM` over the same
field, it evaluates the source again for each of them. Fix the patch so the
upstream suite passes in full, or show that the extra passes are inherent to a
patch we want to keep, and record why.

## Evidence

- **The gate.** `tools/test-query-table-fork.sh` runs upstream's `backends/go` and `backends/sqlite-tests` suites at v0.8.0 against our copy. It needs an upstream checkout, `~/gbdubs/query-table` by default.
- **The result** (2026-10-19): 171 tests pass, 23 skip (Postgres-only), and 1 fails:

  ```
  --- FAIL: TestV2ReductionReuse
      reuse_test.go:100: batch [0 1 2 3 4 5] visited 54 samples; one scan visits 24
  ```

- **Our copy is the cause.** Unpatched v0.8.0 passes. Values still match, so this costs time but gives no wrong answers. `README.pharos.md` records it, and until it is fixed the gate runs with `-skip TestV2ReductionReuse`.
- **Affected surfaces.** Since #157, plain counts and sums skip the adapter through Pharos's native path. Metric panels that batch `AVG`/`MIN`/`MAX` with sums still pay the extra scans.

## Where to look

- **Our patches.** `third_party/query-table-go/README.pharos.md` lists them. The likely suspects are "Reuse executor-validated typed values and their error sidecars after staging" and "Fuse scalar V3 group summaries…". Diff our copy against the v0.8.0 module (`go env GOMODCACHE`, `github.com/!pythia-!software/query-table/backends/go@v0.8.0`) to see every local change.
- **The failing test.** It is upstream's `backends/sqlite-tests/reuse_test.go`.
- **Pharos's patch guards, which must stay green:**
  - `TestVendoredPatch*`;
  - `TestUsageFusedSummariesMatchIndependentMetrics`;
  - the V2 reuse and indexed-window tests in `internal/archive`.

## Done when

- `tools/test-query-table-fork.sh` passes with no `-skip`.
- `README.pharos.md` no longer lists the failure.
- `go test ./...` passes, and the patch guards above still pass.
