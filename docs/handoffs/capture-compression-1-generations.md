# Handoff: compress captured snapshot generations (capture compression, phase 1)

## Assignment

Store superseded SQLite snapshot generations under each capture's `history/`
zstd-compressed, without changing what any reader of them sees. Lay the
foundation that phase 2 (cold plain files) will reuse:

- an encoding recorded in the manifest;
- one decode-and-verify helper;
- decode-on-lease for adapters.

Read [Compressing captured originals at rest](../capture-compression.md)
first. Its invariants are part of this assignment.

## Evidence

On the production library, snapshot generations hold ~21.6 GB:

| Capture | GB |
|---|---|
| Studio Conductor | 14.50 |
| Air Conductor (an Oct 3 copy kept for a 291 MB current database) | 3.91 |
| Antigravity-CLI | 1.84 |
| TL1 | 1.15 |
| Codex | 0.15 |

zstd level 3 shrinks Conductor databases 9.5× (cache.db 4.6×), TL1 databases
7.9× and Antigravity databases 5.7×, at about 400 MB/s, I/O-bound. Conductor's
generation is replaced about hourly. Each time, the previous 14.5 GB snapshot
is hard-linked into `history/<stamp>/` (`preserve`, `capture.go`), and
`retain` (`capture_sqlite.go`) keeps `capture_snapshot_generations` of them,
default 1.

**Do not open anything under `/Volumes/euclid` with SQLite.** That includes
the live catalog and its captures. A read pins the live catalog's WAL. For
manual checks, copy a capture to a temporary directory first, or use the
backups (`/Volumes/euclid/Pharos Backup …`, opened with `?immutable=1`).
Build every test fixture under `t.TempDir()`.

## Who touches generation bytes

From a code survey. Line numbers are approximate; re-check them. Every item
needs a decision and a test.

- **Manifest model.** `capturedVersion` (`capture.go`) carries
  `Size`/`SHA256`/`Source` for a generation; generations often have no
  SHA256. Add `Encoding` (`"zstd"` or empty) and `StoredSize`. Record the
  uncompressed SHA256 when compressing, since that is when it gets read
  anyway.
- **Manifest version.** `loadCaptureManifest` refuses
  `Version > captureManifestVersion` (`capture.go:617`), and writers stamp the
  current version (`capture.go:686`, `capture_pins.go:444`). Older builds
  must refuse a manifest that contains an encoded entry. Prefer writing the
  new version only when an entry is encoded, so libraries without compressed
  entries stay readable by older builds. If that is not workable, say why in
  the PR.
- **Writing.** Compress after the capture's manifest is committed, never
  inside the copy path. Only compress a generation that is all of the
  following:
  1. superseded, so not the latest snapshot;
  2. covered by the source's index marker (`indexMarker.has(snapshot.Path,
     generation.CapturedAt)`) when the source has one;
  3. not partial or unverified evidence that `retain` treats specially.

  Encode to `<name>.pharos-tmp`. Verify by decoding and hashing against the
  SHA256 taken from the uncompressed file. fsync, rename over the name, then
  commit the manifest with `encoding`/`stored_size`/`sha256` after a device
  flush, the same order capture uses. Hold the host's capture lock
  (`captureLockName`) for the manifest update. Run the work as a
  low-priority background operation, through the operation registry and IO
  priority classes from background work phases 1–4 (`docs/background-work.md`).
  It must not run on a request path. Decide whether a Mac compresses other
  hosts' captures, such as the Air's 3.9 GB, under those hosts' capture
  locks, and justify it.
- **Leases.** `freezeManifestInputPolicy` and `freezeRetainedFile`
  (`capture_acquire.go`) check the on-disk size against the manifest, then
  hard-link files into `.reader-*.pharos-tmp/` or durable
  `inputs/<revision>/`. For an encoded generation:
  - compare against `stored_size`;
  - link the encoded file into durable inputs;
  - decode it into the temporary reader directory only when a reader will
    open it.

  `indexGenerations` (`capture_index.go`) opens generations only when the
  marker lacks them or the index is forced. A forced full reindex therefore
  decodes them all, which is acceptable. Do not decode on every lease.
- **SQLite readers of generations**, each of which must get a decoded
  temporary copy:
  - `indexGenerations` and `conductorSessionIDs`;
  - `exportSnapshot`/`exportSQLite` and `dependencies()` in
    `capture_records.go`, where hashes must describe the decoded bytes and
    the gzip record objects stay as they are;
  - history backfill: the plan in `capture_history.go` (hash and dedup) and
    `exportUnit` in `capture_history_job.go`;
  - `verifiedSnapshotReceipt`;
  - `incremental_audit.go`'s `conductorSessionIDsContext`, if it ever reaches
    generations.
- **Size and hash checks.** These must compare against `stored_size` or hash
  the decoded stream:
  - `pinVersion` and `pinHistoryVersion` (`capture_pins.go`);
  - `validateCaptureEvidence` (`library_recover.go`), which restore and
    library transfer both run;
  - `verifyReceipt` and the view units in `capture_history_job.go`;
  - `retainedTree`/`copyVerified` in `library_transfer.go`, which copy the
    raw bytes. That is fine, as long as validation afterwards understands
    the encoding.
- **Pruning.** `removePruned` and `canPruneSnapshot` work by name, which is
  unchanged. Make sure a `<name>.pharos-tmp` left behind by a killed
  compression is swept by `removeCaptureTemporaries` and never adopted.
- **Measurement.** `measureCaptures` (`drive.go`) already sums on-disk bytes,
  so the Storage section reflects the saving with no change. Confirm it with
  a test.
- **Backups.** Local backup copies raw bytes and keeps hard links; check that
  a backup taken mid-compression restores to a valid capture. Cloud backup
  recompresses everything. Leave it, and note the follow-up.

## Constraints

- Follow `CLAUDE.md`, especially "Writing tests". These are data-safety paths:
  extend the existing capture, records, history, pins, transfer and recover
  tests rather than adding `*_regression` files.
- Kill the process at each step of the compression order (encode, verify,
  rename, manifest) using the existing `captureFault` hook pattern. The
  capture must stay valid and readable after every kill.
- Use `github.com/klauspost/compress/zstd`, already a dependency through
  `cloud_archive.go`. Use level 3 and cap the decoder's memory.
- Requests never write. No compression or decoding work on a request path.

## Done when

- A capture whose generations are compressed:
  - indexes, salvages missing Conductor sessions from a generation (the
    existing salvage tests, now run against an encoded generation), exports
    records, backfills, pins, restores and transfers exactly as an
    uncompressed one does;
  - leaves no decoded copies outside temporary reader directories.
- An older build, simulated by the version check, refuses a manifest with an
  encoded entry with a clear error.
- The Storage section shows the smaller history.
- The PR states how long compressing a 14.5 GB generation takes and its peak
  temporary disk use. Measure on a copy, not the live library.
- `go test ./...`, `npm --prefix web run check` and
  `npm --prefix web run test:full` pass.
