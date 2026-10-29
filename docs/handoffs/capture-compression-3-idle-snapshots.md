# Handoff: compress idle latest snapshots (capture compression, phase 3)

## Assignment

Compress, at rest, the *latest* SQLite snapshots whose source database has not
changed for `capture_compress_after_days` (from phase 2). Leave out any
snapshot the source's index opens on every run. Ship this as its own branch,
stacked on phase 2.

Read [the design](../capture-compression.md) and phases 1 and 2
(`capture-compression-1-generations.md`, `capture-compression-2-cold-files.md`)
first. This phase reuses their encoding, background operation and on-demand
decoding through the capture view.

## Evidence

Latest snapshots on the production library:

| Host / source | GB | Notes |
|---|---|---|
| Studio Conductor | 14.51 | read by every Conductor index: never compress |
| Studio Antigravity-CLI | 3.19 | one `conversations/<id>.db` per conversation, most idle |
| Studio TL1 | 0.72 | installation databases such as `explo-candidate-v2.db` (422 MB, last changed Sep 28) |
| Air Conductor | 0.29 | idle since Oct 15 |
| Studio Codex | 0.15 | `state_5.sqlite`, changes constantly |

Measured ratios: Antigravity databases 5.7×, TL1 databases 7.9×, Conductor
9.5×. The expected saving is about 3 GB, plus whatever the Air's idle
Conductor snapshot gives.

**Do not open anything under `/Volumes/euclid` with SQLite.** Build fixtures
under `t.TempDir()`.

## What to handle

- **Eligibility.** A latest snapshot qualifies when all of these hold:
  - its recorded source state (`capturedSnapshot.Source`: database and WAL
    mtimes) is older than the threshold;
  - the last capture found it unchanged (`snapshots_unchanged`);
  - the index marker covers it.

  Exclude snapshots that some reader opens on every index regardless of
  change. Find out which:
  - Conductor's `indexGenerations` reads the latest snapshot to diff
    against generations;
  - Codex's `state_5.sqlite` is likely read every run;
  - check the adapters' `discover`/`planParts` for each kind.

  Encode the rule per adapter, not as a list of names, and test it.
- **Readers.** Every `openReadOnlySQLite` and `exportSQLite` on a captured
  snapshot goes through phase 2's view access, which decodes on demand into
  the reader directory. The places are:
  - Antigravity's `summaries` and `requests` (`adapter_antigravity.go`,
    `adapter_antigravity_usage.go`);
  - the TL1 database in `adapter_tl1.go`;
  - the Conductor `database()`, for the Air's idle capture if it qualifies;
  - `capture_records.go` exports;
  - history backfill;
  - `verifiedSnapshotReceipt`;
  - `incremental_audit.go`.

  A normal sync over idle snapshots must decode none of them.
- **When the source changes again**, `snapshot()` in `capture_sqlite.go`
  takes a new snapshot and `preserve` moves the encoded one to `history/`,
  where it stays encoded (phase 1). `intact()` and the unchanged check
  compare `stored_size` and the recorded captured mtime of an encoded
  snapshot.
- **SQLite record capture** (`sqlite_record_capture`, `capture_records*.go`,
  `capture_pins.go`) and preservation pins must work on encoded latest
  snapshots. Extend their tests.

## Done when

- Idle latest snapshots, Conductor's excluded, are stored compressed.
- A normal sync decodes none of them.
- Changing the source again produces an uncompressed latest snapshot, with
  the encoded one in history.
- Indexing, forced reindex, record export and replay, backfill, pins,
  transfer and restore behave as with uncompressed captures.
- The PR reports savings on a copied sample (copy files, never open the live
  library with SQLite).
- `go test ./...`, `npm --prefix web run check` and
  `npm --prefix web run test:full` pass.
