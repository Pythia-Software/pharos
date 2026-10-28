# Compressing captured originals at rest

Captured originals are 67 GB of the production library (2026-10-28). They are
written by capture and read rarely: to index what changed, to salvage sessions
from superseded snapshots, and by explicit exports, backfill, transfer and
restore. This document is the plan for storing them zstd-compressed without
changing what any of those readers sees.

## Where the bytes are

Production library (euclid), from each capture's `manifest.json`, in GB:

| Host / source | latest files | latest snapshots | snapshot generations | superseded file versions |
|---|---|---|---|---|
| Studio conductor | — | 14.51 | 14.50 | — |
| Studio codex | 12.04 | 0.15 | 0.15 | — |
| Studio claude | 4.80 | — | — | 0.03 |
| Studio antigravity-cli | 0.61 | 3.19 | 1.84 | — |
| Studio tl1 | 1.81 | 0.72 | 1.11 | — |
| Air codex | 3.78 | 0.01 | — | 1.65 |
| Air conductor | — | 0.29 | 3.91 | — |
| Air claude, tl1 | 1.81 | 0.04 | 0.04 | — |

zstd level 3 on samples: Conductor databases 9.5× (cache.db 4.6×), TL1
databases 7.9×, Antigravity databases 5.7×, Claude JSONL 3.0×. Codex rollouts
only reach 2.2×: they carry encrypted reasoning and base64 images. Level 3
runs at about 400 MB/s on the drive with four threads, so it is I/O-bound.

Conductor's 14.5 GB snapshot is re-taken whenever the database changes, about
hourly. Each re-take moves the previous full copy into `history/`, and
capture keeps one generation (`capture_snapshot_generations`).

## Invariants

These come from the map of every reader and writer of captured bytes. The
handoffs cite file and line for each.

1. **Names do not change.** The manifest, pins, receipts, delivery, pruning
   and planning all key captured files by their exact `Captured` path. A
   compressed file keeps its name; the manifest entry records
   `encoding: "zstd"` and `stored_size`.
2. **`size`, `sha256` and the mtimes keep describing the uncompressed
   bytes.** Delivery fingerprints, `source_item_states` (shared with live
   ingest), `LimitReader(size)` in adapters, history backfill dedup, pins and
   record-store dependency hashes all rely on that. Checks that compare a
   manifest size to the file on disk (`intact`, `freezeManifestInputPolicy`,
   `validateCaptureEvidence`, `pinVersion`) compare against `stored_size` for
   an encoded entry. Checks that hash the file hash the decoded stream.
   `recordObject{Hash, Length, Encoding: "gzip"}` in `capture_records.go` is
   the precedent.
3. **Adapters never see a compressed file.** They open paths directly
   (`os.Open`, `openReadOnlySQLite`, TL1's `ReadAt`), and SQLite cannot read
   zstd. Whoever hands an adapter a path to an encoded entry first decodes it
   into the reader's temporary directory, at the same relative path, with the
   recorded mtime. Durable `inputs/<revision>/` trees keep links to the
   encoded files and never hold permanent decoded copies.
4. **Compression never loses the only copy.** An entry is encoded into a
   flocked `.reader-*` lease of its own, which no capture's sweep removes
   while it is held. The encoded stream is flushed to the drive
   (F_FULLFSYNC) and verified by decoding and hashing. Then, under the
   capture lock, the manifest naming it encoded is replaced atomically after
   a device flush, and only then is the file renamed over the name. The
   manifest goes first so that whenever the name may hold zstd, the manifest
   already has the version older builds refuse (invariant 5). A kill between
   the two leaves an entry marked encoded over the original bytes, at the
   original size and with no zstd frame; readers read it as plain and the
   next pass corrects the entry. Readers holding the old inode through a
   `.reader-*` hard link keep reading the uncompressed bytes.
5. **Older builds must not misread an encoded capture.** The manifest
   version is per kind of encoded entry, because a release may ship between
   phases: 6 when encoded snapshot generations are present (phase 1), 7 when
   an encoded latest or earlier plain file is (phase 2), 8 when an encoded
   latest snapshot is (phase 3). A writer stamps the lowest version that
   describes its encoded entries, and 5 when there are none, so captures
   without encoded entries stay readable by older builds. A build reads up
   to its own version and refuses the rest, instead of handing zstd to SQLite
   or a parser.

## Phases

Each phase ships as one PR.

1. **Snapshot generations** ([handoff](handoffs/capture-compression-1-generations.md)).
   This adds the encoding field, a shared decode and verify helper, and
   decode-on-lease. It compresses superseded SQLite snapshot generations once
   they are indexed. Generations are only read by full reindexes, salvage,
   record exports, backfill, pins and transfer. They hold ~21.6 GB on
   production, which becomes ~3 GB.

   **Cost, accepted.** With one retained generation, Conductor's is replaced
   about hourly, and each replacement is compressed once indexed and pruned
   at the next rotation. That re-reads ~14.5 GB and writes ~1.5–2 GB an hour
   at utility IO priority (an 11.2 GB generation took 55 s, encode through
   verify, at 6.8×), for ~13 GB saved in steady state. Capture already
   rewrites the 14.5 GB snapshot hourly, so this adds about one more read
   of it. A pass remembers generations it left uncompressed (incompressible,
   a digest mismatch, repeated verification failures, an unverified record
   export) in the source's `compression.json`, so it does not read them
   again every pass.

   Follow-up: on a volume without hard links (exFAT), verify-all's lease of
   a generation falls back to copying it, and does so while holding the
   host's capture lock; move that copy out from under the lock.
2. **Cold files** ([handoff](handoffs/capture-compression-2-cold-files.md)). Compress superseded plain-file versions, and latest files
   whose source has not changed for a configurable period (14 days, say).
   Capture always rewrites a changed file in full (`copyOnce`), so a cold
   file that changes again is simply recopied uncompressed. Planning must
   take part sizes and versions from the manifest rather than from `stat` of
   an encoded file, and decode only the parts it will parse. This saves about
   15 GB, mostly Codex and Claude.
3. **Idle latest snapshots** ([handoff](handoffs/capture-compression-3-idle-snapshots.md)). Antigravity-CLI keeps a database per
   conversation, and most are idle. Compress latest snapshots whose source
   has not changed for the same period. Keep Conductor's latest snapshot
   uncompressed: every index of Conductor reads it. This saves about 3 GB.

Phase 1 and 2 together take the originals from ~67 GB to ~34 GB. The rest is
dominated by Conductor's latest 14.5 GB snapshot. Shrinking that is the job
of SQLite row evidence (`docs/sqlite-records.md`), not compression.

Local backups copy the encoded files as they are. Cloud backups recompress
the whole tree at level 3; they could store already-compressed entries
without recompressing, as a later optimisation.
