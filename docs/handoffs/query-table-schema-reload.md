# Handoff: stop Query Table requests reloading the catalog schema

## Assignment

Every staged Query Table request (V2 and V3) makes its reader connection
parse the whole catalog schema again, twice. That adds about 6 ms of CPU to
each request. On macOS the parse also takes the one lock that all SQLite
allocations share, so concurrent card queries wait on each other. Make a
request leave its connection's schema loaded. Keep every staging and
consistency guarantee the lease gives today.

## Evidence

Measured 2026-10-20 on a copy of the test template catalog, which has the
production schema: 578 objects and 140 KB of SQL, including 263 triggers. One
reader connection was used, and each step was followed by one ordinary query.

| Step | Cost |
|---|---|
| Plain query | 4 µs |
| Transaction that creates a temp table, then **rolls back** | **2.9 ms** |
| The same, but the temp table is dropped and the transaction commits | 53 µs |
| `PRAGMA temp_store` set and restored on its own | 84 µs |
| `beginQueryWithTempStore` lease, staging one temp table, release, with an unchanged `temp_store` | 2.9 ms |
| The same lease with a changed `temp_store`, as every request does | **5.8 ms** |

- **Why.** A full schema load on a new connection costs about 3.2 ms, which is the same order. SQLite resets every schema on a connection, `main` included, in two cases:
  - when it rolls back a transaction that changed a schema, such as by creating a temp table;
  - when `PRAGMA temp_store` changes while the connection has a temp database.

  The next statement then re-parses all 578 objects.
- **Every request pays both.** The reader pool's connections start with `temp_store` 0, the default. V2 leases with mode 2 and V3 with 1 or 2, so `original != mode` on every request. The staging tables are created inside the leased transaction, and `release` rolls it back and then restores `temp_store`.
- **Why it matters beyond CPU.** On macOS, `modernc.org/libc` puts every SQLite allocation in the process behind one mutex, and a schema parse makes thousands of them. Home issues a Query Table request per card, and the reader pool serves up to eight requests at once, so concurrent requests' schema reloads serialise on that mutex. The same lock is why the Go tests run in several processes rather than with `t.Parallel` (see `internal/archive/processes_test.go`).

## Where to look

- `internal/archive/query_table_v2.go`:
  - `beginQueryWithTempStore` (around line 590) sets `temp_store` and `temp.cache_size`, begins the transaction, and returns `release`, which rolls back and restores both.
  - `beginQueryV2` and `leaseQueryV2`; the V2 caller is around line 974.
- `internal/archive/query_table_v3.go`, around line 207: V3 picks mode 1 or 2 per dataset and population size.
- Query Table creates its own staging temp tables, in its `backends/go/query_v3_execute.go` and `sqlite_v2_reuse.go`. Pharos uses the published module unpatched, so a change needed there is an upstream request (see the dependency policy in `AGENTS.md`).
- The reader pool: `readerDSN` and `readerConns` in `internal/archive/durability.go`.
- Other code that creates temp tables inside a transaction may pay the same reload if it rolls back: `mcp_statistics.go`, `share.go`, `findings_features.go`, `ingest.go`, `tool_rollup_incremental.go`. Check them once the main path is fixed; they run far less often.

## Approach

Choose with evidence, and say why. Some directions:

- **End the lease without rolling back schema changes.** For example, drop the staged temp tables and commit the read-only transaction, or create the tables outside it, if that keeps the snapshot guarantee.
- **Stop changing `temp_store` per request.** For example, open the reader connections with the mode most requests need, or keep separate pools per mode.

  First find out why the current comment requires restoring the mode "before returning the pool connection or performing host enrichment". Keep whatever that protects.
- Whatever you choose, a connection returned to the pool must hold no staged tables, and it must have the settings the next borrower expects.

## Constraints

- **Keep the behaviour.** Requests must keep the same results, the same staging-byte budgets, and V3's spill to disk for large populations. One request must still read one snapshot.
- **No timing assertions.** Prove the reload is gone with a probe, a `-tags probe` file with its run command in its header, as the other probes have. It should time a lease and release, plus one query, before and after. A unit test may assert behaviour, such as no temp tables left on a returned connection and the original settings restored, but not durations.
- **Measure in one process.** The Go tests run in several processes. Use `PHAROS_TEST_PROCESSES=1` when profiling or timing a test.

## Done when

- The probe shows a lease, release and one query costs close to the 53 µs above, not milliseconds. Record before and after in the PR.
- In a CPU profile of `TestHomeQueryRecipesV2`, `_sqlite3ReadSchema` comes only from opening connections, not from statements on connections already open. That test issues 256 Query Table requests.
- `go test ./...` passes.
