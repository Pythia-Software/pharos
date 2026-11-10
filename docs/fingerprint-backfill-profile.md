# Fingerprint backfill profile — 100 real conversations

This records the original 0.8.9 Goja implementation and its earlier optimization.
The current renderer is native Go; see [native parity and performance](fingerprint-native-renderer.md).

The cohort contains 100 distinct conversation IDs selected by sorting the library's
IDs by SHA-256 of a fixed seed plus ID. It contains 1,896 messages, 628 agent events
and 3,197 tools. Each pass generates and publishes 400 PNGs: both approved profiles
in both themes, with events enabled.

The source library was opened with SQLite `mode=ro` and `query_only`; no migration,
indexing, inference or cache writes ran against it. PNGs were published through the
production transaction into disposable catalogs. The durable run used an owned
temporary directory on the external volume, removed after the catalog closed.

## Measurements

| Batch condition | Before | Optimized |
| --- | ---: | ---: |
| Library input loading + rendering + publication, alternating ABBA passes, test storage flushes disabled | 6.20 s | 1.85 s |
| Same frozen-input pipeline, one renderer, median of three alternating pairs | 5.15 s | 3.14 s |
| Production FULL/fullfsync settings, external-drive temporary cache, two renderers | Not measured | 2.62 s |

The comparable library-reading passes improve by 3.35×
(70.1% less elapsed time). The optimized durable run confirms
that production drive flushing remains enabled; the speedup is not obtained by
weakening commit durability. The table's before/after comparison uses identical
test-storage settings; it must not be read as a measured durable-to-durable speedup.

The initial profiled run, including first-time input export, took 7.75 s:
2.30 s input loading/export, 5.31 s rendering and 0.14 s publication. Subsequent
paired passes avoid mixing that first-export cost into the comparison. For two
renderers, summed per-item phase durations overlap and should not be added to
estimate elapsed time.

The 620-message / 1,476-event dense control improved from about 8.1 s to 5.6 s.
All 400 PNG byte hashes match the original on every comparison pass; all four
PNGs for the dense control match too. The earlier 12-input SVG comparison, across
both profiles/themes/event appearances, also stays byte-identical.

## Changes retained

- Target the embedded bundle at ES2020, supported by Goja, so object spreads
  execute natively instead of through expensive ES2015 helper functions.
- Compute label-family prefixes once and use fixed family rank lookups instead
  of repeatedly splitting strings inside nested searches during sorting.
- Reject covered columns early; use numeric typed-array sweeps for stack limits,
  eliminating many small edge arrays and JavaScript comparator calls.
- Reuse bounded raster buffers, PNG encoder buffers and output scratch. Keep a
  64-entry / 4 MiB LRU per renderer for identical drawings; ignore nonvisual
  counts in SVG titles. PNG bytes are copied before cache/publication so future
  scratch reuse cannot mutate them.
- Use two independently owned renderers during full-speed continuous processing.
  Reservations prevent duplicate work, including newer generations of an entity
  already rendering. Publication still validates source generations atomically.
- Remove the fixed 10 ms full-speed inter-item delay. Retain foreground query
  gates, cooperative yields, cancellation, and the slower battery policy's single
  worker and delay.

## Reproduction and limits

The build-tagged `TestCompactFingerprintBatch` probe documents all environment
variables. It requires a manifest of exactly 100 unique IDs. First passes save
frozen input projections next to the manifest; later passes can replay those.
Use a fresh manifest directory to include live library reads again. Set
`PHAROS_FINGERPRINT_PROFILE=1` for Go CPU profiles (the historical Goja
profile hook has been removed),
`PHAROS_FINGERPRINT_WORKERS=1` or `2` to compare concurrency, and
`PHAROS_FINGERPRINT_DURABLE=1` plus `PHAROS_FINGERPRINT_CACHE_ROOT` for production
flushes on a chosen volume. Destinations remain disposable and separate from the
source library. The queue is seeded before timing; timed work includes the real
input loader, renderer, PNG encoding/hash and publication transaction, rather
than UI polling and daemon scheduling. In-memory job reservations are covered
separately by concurrent worker tests.

Measurements were taken on this busy Mac with warm filesystem caches, not an
otherwise idle benchmark machine. The library is heavy-tailed and this sample
has many short delegated conversations. It is not sufficient for a reliable
whole-library completion-time forecast, especially for workspace aggregations or
very large conversations. Test storage mode, source loading, queue dispatch and
full drive flushing are deliberately distinguished above.

Artifacts remain private under `.context/fingerprint-backfill/`: baseline and
optimized probe binaries, frozen inputs, CPU/JS profiles, per-conversation timings,
all 400 hashes per run, and alternating-run results. No live-library backfill was
started.
