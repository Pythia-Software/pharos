# Query Table explicit host group budgets

## Request

Please allow an explicit host `MaxGroups` of up to **100,000** in the Go
backend's `SQLiteV2Dataset` and `QueryDatasetV3`. This is the final upstream
change Pharos needs to remove its vendored Query Table backend and Go module
replacement entirely. One small PR and a published release are sufficient.

Query Table v0.9.0 supplied the four performance improvements requested in the
[earlier PR stack](query-table-performance-stack.md). Pharos now carries only
two source-line changes relative to that release, documented in
[README.pharos.md](../../third_party/query-table-go/README.pharos.md).

## Why Pharos needs the allowance

Pharos's Usage dataset contains compact execution/day/model facts. Its Work
preset groups by workspace and must cover libraries with more than 10,000
workspaces. Its token charts also group long periods by hour and repository or
model. Pharos therefore configures `MaxGroups: 100_000` for Usage in both V2
metrics and V3 queries.

The existing upstream validation rejects that configuration before execution,
even when the current query produces only a few groups. Other Pharos datasets
retain their existing budgets: ordinarily 2,000 groups for V2 and 10,000 for
V3. The larger allowance is selected by trusted host configuration, never by
the request.

Host integration references:

- `internal/archive/query_table_v2.go`: `queryV2UsageMaxGroups` and
  `queryV2Dataset`.
- `internal/archive/query_table_v3.go`: `queryV3Dataset`.
- `internal/archive/query_table_v3_adapter_test.go`:
  `TestQueryTableGroupBudgetOverrides`.

## Implementation

Against Query Table **v0.9.0**, release commit
`bd2c730f0242f9d2b3e0913d55f1cd7c0d3229a2`, change the upper bound in:

| Upstream file | Function | Current maximum | Requested maximum |
|---|---|---|---|
| `backends/go/sqlite_v2_dataset.go` | `SQLiteV2Dataset.limits` | 10,000 | 100,000 |
| `backends/go/query_v3.go` | `QueryDatasetV3.limits` | 10,000 | 100,000 |

In each validation condition, replace `groups > 10000` with
`groups > 100000`. A shared internal constant is also suitable. The V3
validation is shared by SQLite and PostgreSQL, so cover both dialects.

Retain the zero-value default of 2,000 groups, positive-budget validation and
the configured budget's enforcement against actual groups. Preserve request
`groupLimit`, row paging, population, staging-byte, sample and timeout limits.
A small top-N result must not hide an exceeded host group budget. Document the
accepted host ceiling and include the change in the release notes.

## Acceptance and validation

- A host configuration with `MaxGroups: 100_000` executes successfully on a
  small valid fixture in SQLite V2 and V3; V3 PostgreSQL accepts the same
  configuration.
- `MaxGroups: 100_001` and negative budgets still return `resource_limit`.
  `MaxGroups: 0` retains the current default and advertised capabilities.
- A small fixture exceeding a deliberately lowered host group budget still
  returns the existing budget diagnostic, including when `groupLimit` or
  result paging would hide most groups. Preserve V2's per-metric error behavior
  and V3's request error behavior.
- Existing grouping, calendar, distribution, cancellation, revision and
  reduction-reuse coverage remains green. Use the upstream contribution test
  commands, including real SQLite and configured PostgreSQL integration tests.

Extend existing tests with small fixtures; accepting the configuration does
not require inserting 100,000 groups. Pharos's budget-boundary test already
checks the two ceilings using six source rows. Its Usage tests separately
exercise actual budget enforcement.

## Removing the Pharos patch after release

Publish the fix with the normal Go module release tag. Then Pharos can:

1. Upgrade to the published release, keeping its frontend package pins aligned
   with the release as usual.
2. Remove the `go.mod` replacement for
   `github.com/Pythia-Software/query-table/backends/go` and delete
   `third_party/query-table-go/` and `tools/test-query-table-fork.sh`.
3. Retain the host's Usage budgets and integration tests. Update the patch
   documentation and the temporary exception in `AGENTS.md` to reflect that
   Query Table is now an ordinary published dependency.
4. Verify with `GOWORK=off go list -m -json
   github.com/Pythia-Software/query-table/backends/go` that the selected module
   has the released version and no `Replace`. Run `go test -short ./...`,
   `go test ./...`, `go vet ./...`, `npm --prefix web run check` and
   `npm --prefix web run test:full` with supported Node, regenerating schemas
   and rebuilding the frontend for the upgrade.

Completion means Pharos can keep its current Usage functionality using the
published backend with no vendored Query Table source or patch-maintenance
tooling.
