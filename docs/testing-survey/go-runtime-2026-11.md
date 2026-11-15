# Go test runtime follow-up, November 2026

Measured on November 6 at `bdc4ef7c`, on the shared Mac while other workspaces
were compiling and testing. Load averages ranged roughly from 40 to 60.
These timings describe contention, not a benchmark of an idle machine.

## Slowest fifth

The baseline ran 1,331 top-level tests and skipped 11. Its slowest 267 tests
(20%, rounded up) took **4,023 of 4,960 summed seconds: 81%**. The cutoff
was 3.31 seconds. Subtests are counted in their parents, never counted twice.
The first full run reached Go's 10-minute timeout; its unfinished tests were
measured separately using a binary built from the same committed sources.

The largest concentration was `library_lifecycle_test.go`: 11 tests in the
slowest fifth accounted for 1,689 seconds. Tiny libraries still carried the
complete catalog schema. Sanitizing each transfer snapshot dropped hundreds
of derived objects in separate transactions, creating a rollback journal for
each one. Batching those drops in one transaction preserves the same retained
tables and privacy checks. `VACUUM` and the final device flush still happen
after commit, before the snapshot can be published.

Deleting the transfer, recovery, capture, adoption, or cross-host tests would
discard valuable data-safety checks. Keep their expectations and make their
shared setup cheaper.

## Changes

| Cost | Decision |
| --- | --- |
| Transfer/recovery schema cleanup | Keep all integration cases; batch schema removal into one transaction. |
| Disposable retained files, source SQLite fixtures, and labeling ledgers | Follow the existing catalog harness: retain writes, transactions, renames, closes, and process-crash checks, but skip device-cache flushes. `useDurableCatalog` restores real flushing for durability tests. Production defaults remain durable. |
| Compaction cancellation | Replace a 60 MB timing race with a partial writer held until a real progress report cancels it. Keep actual SQLite compaction in the successful case. |
| Compaction fixture | Keep rowid gaps and free pages with 12 messages and 16 filler pages instead of 300 messages and 200 filler pages. |
| Adoption fixture | Copy the initialized template before rewinding to schema 4; retain migration checks and the searchable session-7 case with 16 messages instead of 96. |
| Rollup mutation/parity fixtures | Preserve all mutations, filters, and differential comparisons with 40–200 calls instead of 100–1,000. |
| Runaway detector | Lower the test's threshold; seed 637 requests instead of 8,820. Keep the production threshold of 1,000. |
| Diagnostic journal bounds | Lower the test's event/byte limits and exercise overflow and rewriting with 15 events instead of 350. |
| Safety handoff command matrix | Remove nine duplicate integration cases. Keep the command matrix in `TestSafetyIdentityAndExecution` and lifecycle checks for resolved and unresolved identities. |
| Resource-sampling performance assertion | Move the 2,000-round live process-table loop to an on-demand benchmark. Keep scripted behavior tests and the real kernel integration check. |
| Ask save retry helper | Wait for both the visible commit and release of the in-memory hold; the baseline raced those two events under contention. |

Skipping device flushes in disposable fixtures does not simulate power loss.
The previous catalog harness already made that tradeoff: killing a process
leaves its committed writes in the OS cache. The dedicated durability tests
still use real flushing, and injected flush failures check that published
files survive and unfinished copies are removed.

No test family was deleted merely because its implementation seemed stable.
The useful deletion was duplicate coverage; most savings came from setup and
I/O rather than fewer behavioral expectations.

## Results

The final uncached full run passed all 1,331 executed top-level tests, with
the same 11 skips as the baseline. The archive package finished in **456.7 s
(7m37s)**; the entire command, including compilation, took **514.4 s
(8m34s)**. The baseline full command had timed out rather than completing.

Summed test time fell from **4,959.6 s to 3,061.7 s, about 38%**. The original
slowest fifth fell from **4,022.6 s to 2,177.0 s, about 46%**. These are
indicative comparisons across contended runs; machine load and the grouping
of unfinished baseline tests differed. They are not a claimed wall-clock
percentage improvement for an idle Mac.

| Test | Before | After |
| --- | ---: | ---: |
| Transfer stored identities and accepted revisions | 628.6 s | 110.6 s |
| Pending capture after delivered inputs | 366.0 s | 74.4 s |
| Transfer copy/move and independent preferences | 233.9 s | 59.2 s |
| Recovery candidates and missing retained evidence | 210.6 s | 104.6 s |
| Compaction failures and cancellation | 104.3 s | 23.2 s |
| Retained SQLite grouped acquisition | 29.4 s | 13.5 s |
| Diagnostic journal bounds | 21.4 s | 0.4 s |
| Safety handoff lifecycle | 16.6 s | 4.7 s |

Transfer and recovery remain the largest costs. They still exercise real
catalogs, migrations, replay, identity mapping, privacy exclusions, verified
publication, and recovery from missing or changed inputs. Naming audits and
background verification also remain expensive; these changes do not claim
to eliminate their repeated capture/ingest setup.

Validation passed:

- `go test -count=1 -timeout=25m -json ./...`: archive 456.7 s, command 514.4 s.
- `go test -short -count=1 ./...`: archive 302.6 s, command 313.1 s.
- The resource-sampling benchmark, 100 iterations, completed successfully.
- Focused compaction, adoption, rollup, detector, safety, and transfer checks,
  plus formatting and diff checks.

## Measuring again

Use `go test -count=1 -json -timeout=25m ./...` for uncached timings. Rank
top-level `pass`/`fail` events by `Elapsed`, excluding skipped tests and
subtest events. Keep the normal test process count for before/after runs and
record machine load. Summed test seconds describe work across processes;
they are not elapsed suite time.

Run the live sampling benchmark only when investigating that implementation:

```sh
go test -run '^$' -bench '^BenchmarkResourceSampling$' -benchtime=100x ./internal/archive
```
