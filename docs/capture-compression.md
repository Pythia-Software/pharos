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
4. **Compression never loses the only copy.** An entry is encoded to
   `<name>.pharos-tmp`, the encoded stream is verified by decoding and
   hashing, the result is fsynced and renamed over the name, and then the
   manifest is replaced atomically after a device flush. That is the same
   order capture already uses. Readers holding the old inode through a
   `.reader-*` hard link keep reading the uncompressed bytes.
5. **Older builds must not misread an encoded capture.** Bump the capture
   manifest version, so that builds that do not know `encoding` refuse the
   capture instead of handing zstd to SQLite.

## Phases

Each phase ships as one PR.

1. **Snapshot generations** ([handoff](handoffs/capture-compression-1-generations.md)).
   This adds the encoding field, a shared decode and verify helper, and
   decode-on-lease. It compresses superseded SQLite snapshot generations once
   they are indexed. Generations are only read by full reindexes, salvage,
   record exports, backfill, pins and transfer. They hold ~21.6 GB on
   production, which becomes ~3 GB.
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
