# Reader admission control

The reader pool remains at eight connections. A process-wide FIFO admission
queue allows three heavy executions at once, leaving connection capacity for
preferences, findings, background, upgrade and status reads. This is an internal
driver limit, with no Settings control or configuration override.

`executeQueryV3` acquires admission before leasing its execution connection and
holds it through staging, execution and transaction cleanup. Source preparation
and cached dependencies finish first. In particular, hourly Usage metrics load
their second population before starting the transaction. Enrichment after the
transaction no longer holds admission.

Derived-cache computations use the same limit, including cache warming and the
uncached fallback when generation tracking fails. Cache hits and callers waiting
for an existing computation need no slot. A builder yields its slot while asking
for a nested cached dependency, then reacquires before resuming; independent
builders never share ownership of a slot. This prevents a queue full of parent
builds from deadlocking on their children.

Request cancellation immediately removes a queued query. Shared cache builds
retain their existing independent lifetime: a caller can abandon its wait even
before admission, while the build remains reusable by other callers. Catalog
close cancels and joins both queued and running builds.

## Clone measurements

The 2026-11-07 probe used an APFS clone of the performance catalog at
`/Volumes/euclid/pharos-dev-sqlite-performance/catalog/catalog.sqlite3`, with a
`.pharos-dev` marker. It never opened the live catalog. Each pass warmed the four
source requests, then ran 32 requests from 24 clients through `executeQueryV3`:
tool-call and message-label counts, a 30-day tool-call count by day, and 30-day
model-request output tokens by day. A `meta` count sampled the same reader pool
every 250 ms. The temporary probe was excluded from the committed source.

### Final paired comparison

After the Go checks finished, the paired repeat reproduced the handoff's
four-reader baseline. The machine remained shared, with load averages decreasing
from about 24 to 15 during the run:

| Readers | Heavy slots | Burst | Process CPU | Heavy p95 | Light p95 | Light max |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 4 | Unbounded baseline | 88.3 s | 298.8 s | 88.0 s | 17,975.595 ms | 18,456.077 ms |
| 8 | 3 | 78.2 s | 207.0 s | 62.0 s | 0.182 ms | 7.681 ms |

Both acceptance targets were observed: the burst beat the four-reader baseline
and the approximately 90-second target, while light-read p95 stayed below
100 ms. The burst was about 11% shorter and used about 31% less process CPU.
These remain single-pass observations on a shared machine. The three-slot
default leaves five reader connections available for light requests.

### Earlier loaded runs

The first series ran amid changing shared-machine load (roughly 31–94), including
other test jobs. These are single-run observations, not a controlled throughput
comparison:

| Readers | Heavy slots | Burst | Process CPU | Heavy p95 | Light p95 | Light max |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 4 | Unbounded baseline | 173.7 s | 338.0 s | 156.5 s | 13,258 ms | 57,252 ms |
| 8 | 4 | 220.8 s | 291.7 s | 165.8 s | 5 ms | 124 ms |
| 8 | 3 | 183.5 s | 301.3 s | 128.4 s | <0.5 ms | 45 ms |

The initial three-slot pass met the light-read target, including its worst
sample, but its burst was 5.6% slower than the earlier four-reader baseline.
Those loaded runs alone did not establish the throughput acceptance criterion;
the final paired repeat above was needed.

## Verification

The Go fast tier (`go test -short ./...`) passes. Focused tests also pass under
`-race`, covering FIFO admission, the concurrency bound, cancellation/grant races,
SQL and staged query cancellation before a connection lease, light endpoints
under saturation, nested/shared cache builds, shutdown and error cleanup. The
existing query-shape tests run with one slot to cover cold hourly Usage sources.

## Removing the page-cache mutex: upstream research

Inspected on 2026-11-07, without changing the driver or module cache:

- The pinned `modernc.org/sqlite v1.61.0` Darwin generator enables
  `SQLITE_ENABLE_MEMORY_MANAGEMENT`. Its shared generated implementation in
  `lib/sqlite_g_0000000000000003.go`, `_pcache1Init`, sets
  `FseparateCache = 0` and allocates `SQLITE_MUTEX_STATIC_LRU` for the shared
  page-cache group. This matches [SQLite's page-cache initialization](https://github.com/sqlite/sqlite/blob/master/src/pcache1.c).
- The first upstream question is whether modernc can publish a supported build
  without that flag. SQLite documents its purpose as enabling
  [`sqlite3_release_memory`](https://sqlite.org/compile.html#enable_memory_management).
  Inference from the initialization code: removing the flag should allow
  separate cache groups in this threaded build. It needs upstream validation of
  memory reclamation, memory use and concurrent-scan performance before adoption.
- SQLite also offers an [application-defined page cache](https://sqlite.org/c3ref/pcache_methods2.html)
  through `SQLITE_CONFIG_PCACHE2`. This could replace the shared group, but would
  require a complete thread-safe cache and modernc integration. SQLite recommends
  the built-in cache for most applications. Configuration must occur before
  initialization; it cannot be added as a connection PRAGMA. See
  [configuration timing](https://sqlite.org/c3ref/c_config_covering_index_scan.html).

Increasing `cache_size`, selecting private database caches or changing mmap does
not change the compile-time choice of page-cache group. Admission control can be
revisited after a published upstream release removes this contention.
