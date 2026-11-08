# Compact fingerprint images

Fingerprint columns, search result facts and Message Labels conversation cards
use precomputed PNGs. Detailed conversation and timeline components retain their
interactive rendering. The compact layout uses the reviewed equal-spacing ribbon,
500-event ceiling, diversity and relationality scoring, collision checks, parallel
stripes and representative tool dots/tails.

## Rendering and storage

Two profiles are generated at 1024 pixels wide: `wide` is 1024×128 (8:1) and
`card` is 1024×256 (4:1). Each has light and dark appearances with agent
events. The API also supports an event-hidden appearance, generated lazily and
kept up to date once requested. Identical PNG bytes share one
SHA-256-addressed blob, including identical appearances or conversation/workspace
images.

The service computes the compact layout, emits the small fingerprint SVG
vocabulary, and rasterizes it entirely in Go. Its domain is deliberately limited
to the two profiles above, a 320-unit layout, 1024-pixel PNGs, a 500-event ceiling,
and the existing tool/relationship selection rules. It does not execute JavaScript
or accept arbitrary SVG. There is no Node, browser, font, external asset or
subprocess dependency at runtime.

The TypeScript model remains the browser/prototype reference. Shared synthetic
fixtures check both implementations' exact SVG hashes and the original 0.8.9
Goja renderer's PNG hashes, including both event appearances. The renderer
version is the digest of those expected PNGs, so a change to the pixels requeues
cached images and a refactor that draws the same pixels does not.
See [native renderer parity and performance](fingerprint-native-renderer.md).

All state lives in the library's SQLite catalog:

- `fingerprint_entities`: durable work queue, random source generation, requested
  event appearances, visible-row priority and retry state.
- `fingerprint_images`: profile/theme/event metadata, source and renderer versions,
  and the current PNG hash.
- `fingerprint_blobs`: deduplicated PNG bytes.
- `fingerprint_orphans`: superseded hashes awaiting garbage collection.

The cache travels with catalog backup and restore. It contains derived data and
can be regenerated from retained conversation evidence. Collection deletes
unreferenced blobs after a one-hour grace period for URLs already handed to
browsers, in bounded batches. SQLite can reuse the freed pages; the physical
catalog file need not shrink immediately.
Hourly, daily and manual passes drain expired entries in bounded transactions
before rendering, even when no images need refreshing. Continuous processing
also runs periodic cleanup.

## Invalidation, processing and recovery

Source-transaction triggers enqueue the affected conversation and its workspace
when messages, labels, events, tool calls or recorded sessions change. Moving a
conversation invalidates both workspaces. Deletion removes image ownership; a
fresh random generation prevents a render for a deleted ID from publishing onto
an entity later recreated with that same ID. Rolled-back source writes have no
cache effect.

Rendering waits while the analysis chain is running or its tools, writing,
related-conversation, message-label or agent-event stages have pending generations.
Disabled message-label and agent-event analyses do not hold rendering for ordinary
backlog; stored evidence known to be superseded still cannot be stamped current.
An interrupted analysis keeps its pending generation, so stopping a run does not
make its fingerprints eligible. Outside the chain, dirty labels or agent events
and superseded execution evidence hold the affected conversation and its workspace
until that analysis completes. Both event appearances wait. Waiting entities keep
their last good images and queue state; they do not render interim images, write
retry attempts or increment rendering progress. Ready entities can progress past
a higher-priority entity waiting on analysis. Expired-blob cleanup still runs.

Hourly, Daily and Ad hoc passes run fingerprints after the analysis stages and
writing refinement. If analysis is still pending, the pass records neither
success nor failure and retains its request for a later background check. It
resumes when work becomes eligible even if continuous rendering is disabled;
scheduled retries retain scheduled priority and power policy. A requested
maintenance task likewise stays blocked rather than acknowledging an empty pass.

Opening a catalog adds queue metadata for existing entities. A changed renderer
version requeues existing images while retaining the last good PNGs, unless
`fingerprintSameImages` records that the earlier renderer drew the same images:
then they are restamped and only entities missing current images stay queued.
Library opening and HTTP handlers never render. Full retained histories are
loaded by the background worker; the older bounded evidence endpoint keeps its
existing contract.

Full-speed continuous processing uses two workers, each with bounded reusable
native raster/PNG scratch.
Each keeps an LRU of at most 64 identical drawings / 4 MiB of PNGs; nonvisual
counts in SVG titles do not defeat reuse. In-memory reservations prevent two
workers from rendering the same entity, including a newer source generation.
Unfinished work stays dirty in SQLite, so reservations need no persistent leases.

The workers yield between units to foreground queries, prioritize visible requests,
respect the Conversation fingerprints background schedule and power policy, and
support Run now. The slower battery policy uses one worker and retains its
inter-unit delay. Full-speed processing has no fixed per-image sleep. Operations
are registered as
`maintenance.fingerprints`. Each unit prepares one requested event appearance
across both profiles and themes, outside the write transaction. Publication
atomically stores PNGs and acknowledges only the matching source generation.
A source update or new appearance request during rendering remains queued.

Shutdown/eject cancels source reads, event target matching, native input
preparation and mark selection. Interrupted work stays dirty. Render failures retain last good images
and record a bounded error with exponential retry delay (5 seconds to 5 minutes).
Retries survive restart. A per-unit two-minute deadline prevents a pathological
conversation from monopolizing the worker indefinitely; other entities progress
while it backs off.

## HTTP and UI

- `POST /api/fingerprints`: `{items:[{entity,id,profile}],events:true}` accepts
  1–250 items (`entity` is `conversation` or `workspace`). It promotes queue work
  and returns `ready`, `pending`, `retrying` or `not_found` metadata, image URLs
  for both themes and a detail link. Pending/retrying entries may retain stale
  images. Responses are not cached.
- `GET /api/fingerprints/images/{sha256}.png`: authenticated immutable image with
  a private one-year cache lifetime, ETag and conditional GET support.
- `GET /api/fingerprints/status`: queue counts, retry counts, image references,
  unique blobs, PNG payload bytes and renderer version. Payload bytes exclude
  SQLite page/index/WAL overhead.

The UI batches subscriptions across a column, shares duplicate requests, and
polls pending images every two seconds. Ready images recheck metadata every
30 seconds for source changes; unmounted cells stop polling. Transport/image
errors retry, refresh notifications invalidate outstanding metadata, and theme
changes select the existing image URL without rerendering geometry. Images decode
asynchronously and load lazily. Each image links to its detailed view.

## Extending and checking

Profile geometry and budgets live in `web/src/compact-fingerprint.ts` and the
native `fingerprint_layout.go`. The native input projection and SVG encoder are
`fingerprint_prepare.go` and `fingerprint_svg.go`; `fingerprint_render.go` owns
rasterization and versioning. Extend both profile definitions and the Go
`fingerprintProfiles` registry when adding a profile, then update request/component
types and the shared parity corpus. `TestFingerprintRendererVersion` fails when
the corpus's expected PNGs change until `fingerprintVersion` is set to their new
digest; a change the corpus cannot see needs a case there. Browser-only
experimental widths and budgets are outside the native renderer's domain.

The [100-conversation backfill profile](fingerprint-backfill-profile.md) records
the measured speedup, storage/durability conditions and reproduction commands.

Go tests cover source invalidation, publication races, restart/retry/cancellation,
shared-blob cleanup, authentication, sizes and palette. JS tests cover compact
geometry and column batching/recovery. Browser tests cover narrow layouts,
keyboard navigation, view restoration and the persisted event preference. The
build-tagged `TestCompactFingerprintNativeRender` probe accepts previously exported
inputs for timings/visual checks without opening a live library; see its header.
