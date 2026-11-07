# Compact fingerprint images

Fingerprint columns, search result facts and Message Labels conversation cards
use precomputed PNGs. Detailed conversation and timeline components retain their
interactive rendering. The compact layout uses the reviewed equal-spacing ribbon,
500-event ceiling, diversity and relationality scoring, collision checks, parallel
stripes and representative tool dots/tails.

## Rendering and storage

Two profiles are generated at 1024 pixels wide: `wide` is 1024×128 (8:1) and
`card` is 1024×256 (4:1). Each has light and dark appearances. The default event
appearance is precomputed; turning off Agent events in fingerprints requests an
additional set lazily. The event-hidden set remains subscribed to source updates
once requested. Identical PNG bytes share one SHA-256-addressed blob, including
identical appearances or conversation/workspace images.

The service embeds a build of the shared TypeScript layout and runs it in Goja.
A pure-Go rasterizer supports precisely the generated SVG drawing vocabulary.
There is no Node, browser, font, external asset or subprocess dependency at
runtime. `npm --prefix web run build` prepares this ignored embedded bundle along
with the UI bundles. Bundle verification checks it too.

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

Opening a catalog adds queue metadata for existing entities. A changed embedded
layout hash or native rasterizer version requeues existing images while retaining
the last good PNGs. Library opening and HTTP handlers never render. Full retained
histories are loaded by the background worker; the older bounded evidence
endpoint keeps its existing contract.

Full-speed continuous processing uses two workers, each with an embedded VM and
bounded reusable raster/PNG scratch.
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

Shutdown/eject cancels source reads, event target matching and JavaScript
execution. Interrupted work stays dirty. Render failures retain last good images
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

Profile geometry and budgets live in `web/src/compact-fingerprint.ts`; the embedded
render entry is `web/src/fingerprint-renderer.ts`. Register additional profiles in
that registry and the Go `fingerprintProfiles` registry; update request/component
types for any new UI placements. The worker derives completeness from the registry
and the embedded entry enumerates the shared profiles. Change the native version
in `fingerprint_render.go` when rasterization changes. Layout and palette source
changes invalidate automatically through the embedded bundle hash.

The [100-conversation backfill profile](fingerprint-backfill-profile.md) records
the measured speedup, storage/durability conditions and reproduction commands.

Go tests cover source invalidation, publication races, restart/retry/cancellation,
shared-blob cleanup, authentication, sizes and palette. JS tests cover compact
geometry and column batching/recovery. Browser tests cover narrow layouts,
keyboard navigation, view restoration and the persisted event preference. The
build-tagged `TestCompactFingerprintNativeRender` probe accepts previously exported
inputs for timings/visual checks without opening a live library; see its header.
