# Handoff: compress cold captured files (capture compression, phase 2)

## Assignment

Compress, at rest, the captured plain files whose source has gone cold:

- **Superseded file versions** under `history/`, such as the Air's 1.65 GB of
  Codex versions.
- **Latest files** under `files/` whose source has not changed for
  `capture_compress_after_days`. Add this setting to the configuration, with
  a default of 14; 0 turns compression of latest files off.

Readers must see exactly what they see today. A sync must never decompress
files it is not going to parse.

This builds on phase 1 (`capture-compression-1-generations.md`): its manifest
`encoding`/`stored_size`, decode-and-verify helper, background operation and
version gate. Read [the design](../capture-compression.md) and its invariants
first. Ship this as its own branch stacked on phase 1.

## Evidence

Latest plain files on the production library:

| Host / source | GB |
|---|---|
| Studio Codex | 12.04 |
| Studio Claude | 4.80 |
| Air Codex | 3.78 |
| Studio TL1 | 1.81 |
| Air TL1 + Claude | 1.81 |
| Studio Antigravity-CLI | 0.61 |

Superseded versions add 1.68 GB. Most sessions stop changing within days.

Measured zstd level 3 ratios:

- Claude JSONL: 3.0×.
- Codex rollouts: 2.2×, because of encrypted reasoning and base64 images.
- TL1 is mixed, about 5×.

The expected saving is about 15 GB.

**Do not open anything under `/Volumes/euclid` with SQLite.** Build fixtures
under `t.TempDir()`.

## The hard part: readers must not decode what they do not parse

Today a capture index leases the capture: `freezeManifestInputPolicy` in
`capture_acquire.go` hard-links every file into a `.reader-*` directory.
Adapters then walk that directory and take each part's size and version from
`stat`:

- `jsonlAdapter.planParts` and `parseFile` in `adapters.go`;
- antigravity's group parts and post-parse recheck in
  `adapter_antigravity.go`;
- TL1's transcripts, run logs and `readTail` (`ReadAt`) in `adapter_tl1.go`.

Ingest compares those sizes and versions with `source_item_states`
(`ingest_parts.go`), which live ingest shares, and parses only what changed.

Decoding every encoded file on every lease would rewrite gigabytes per sync,
so don't. The intended shape:

- **File access through the capture view.** In capture mode, adapters list,
  stat and open captured files through the `captureView`, which knows the
  manifest, instead of calling `filepath.WalkDir`/`os.Stat`/`os.Open` on the
  reader directory.
  - Listing and stat come from the manifest: the uncompressed `size` and the
    source `mtime_ns` (or what the adapter uses today).
  - Opening an encoded file decodes it on demand. Decode into the reader
    directory at its path for random access (TL1's `ReadAt`, SQLite), or
    return a streaming reader where the caller only reads forward.
  - Live sources, with no view, keep using the OS directly.
- **The lease links every file as today.** It compares encoded files to
  `stored_size` and decodes nothing up front.
- **Durable `inputs/<revision>/`** (retained-input capture, history
  verification) keeps links to the encoded files. Its readers, such as
  `historyViewAdapter` in `capture_history_verify.go`, go through the same
  view access.
- **Other direct readers** of a capture directory need the same treatment:
  - `openCaptureAdapter` for planning and audit (`capture_index.go`,
    `incremental_audit.go`);
  - `harnessSourcePath`/`codexHarnessFromFileContext` and
    `readInstructionsFile` (`harness_version.go`, `instructions.go`);
  - Claude `sessions-index.json`, Codex `session_index.jsonl` and
    `codexRolloutIdentity` (`adapter_titles.go`);
  - `readJSONVersion`.

  Never compress index or name files that every plan reads: keep a short
  allowlist of what may be compressed, or an explicit denylist, and test it.

## Writing

- **Superseded versions.** When `preserve` moves a version to `history/`, it
  is cold by definition. Compress it in the background after the manifest
  commits, with phase 1's order and verification.
- **Latest files.** Compress a latest file when its manifest entry is cold:
  its source mtime is older than the threshold and the last capture found it
  unchanged. Also require that the source's index marker, or
  `source_item_states`, shows the current version indexed, so the next
  index will not need it.
- **When a cold file changes again**, `copyOnce` rewrites the latest copy in
  full, as today, so it becomes uncompressed and the previous version is
  preserved. Two checks must treat an encoded previous copy as intact by
  `stored_size`:
  - `intact()` (`capture.go`);
  - the "previous kept as prefix" hash check, which reads only the *source*.
- **Delivery.** `capture_delivery.go` fingerprints from manifest fields, so
  it must not change. Add a test proving that.

## Constraints

- Follow `CLAUDE.md`'s test rules. Extend the existing adapter, capture,
  index, history, transfer and recover tests.
- Add a test that indexes a capture with encoded files twice. The second
  pass must decode nothing; count decodes through a test hook.
- Add a test that a full reindex (`force`) parses encoded files and
  produces the same catalog as an uncompressed capture.
- Requests never write; the work runs in the background operation from
  phase 1.

## Done when

- Cold files and superseded versions are stored compressed.
- A normal sync over unchanged cold files decodes nothing.
- Indexing, forced reindex, retained-input sync, history backfill, pins,
  transfer, restore and local backup behave exactly as with uncompressed
  captures.
- The PR reports on-disk savings on a copied sample of real captures (copy
  files, never open the live library with SQLite), and the decode count for
  a normal sync.
- `go test ./...`, `npm --prefix web run check` and
  `npm --prefix web run test:full` pass.
