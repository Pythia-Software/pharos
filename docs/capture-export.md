# Capture export

A capture export copies one already-captured Claude Code or Codex source,
exactly as Pharos stored it, into a directory that can be carried to
another machine and checked there for corruption. It needs no catalog,
library, configuration or Pharos service on either side.

```text
pharos capture-export --root CAPTURE_ROOT --host HOST --out EXISTING_OUTBOX SOURCE
pharos capture-export-verify EXPORT_DIR
```

Both commands print the verified envelope (`export.json`) as JSON. They take
exactly the arguments above. They never load a configuration, open a
catalog, discover this Mac's host or providers, run a capture or a provider
parser, or start an HTTP server. Ctrl-C or SIGTERM cancels the command up to
the rename that publishes the export, leaving nothing in the outbox. After
that rename the export exists, and only its flush remains.

The export is a transport format only. Pharos has no import yet, and an
export can be neither unpacked into a library nor used as a source. It does
not delete or move anything in the capture.

Code: `internal/archive/capture_export.go`, with the confined file I/O in
`capture_export_io*.go`. Tests: `capture_export_test.go`. Canonical envelope
fixture: `testdata/capture-export/envelope-v1.json`.

## Layout

```text
OUT/<id>/export.json
OUT/<id>/captures/<host>/host.json
OUT/<id>/captures/<host>/<source>/manifest.json
OUT/<id>/captures/<host>/<source>/files/...     each file's latest version and each snapshot's latest copy
OUT/<id>/captures/<host>/<source>/history/...   earlier file versions and snapshot generations
```

`host.json`, `manifest.json` and every member are byte-for-byte the stored
files, at the same relative names. Source JSON is never reserialized, and
encoded (zstd) members stay encoded. Nothing else is copied: the export
holds no catalog, credentials, settings, host TOML, locks, index marker or
runtime endpoint. The only directories are the ancestors of those files and,
for a capture with no latest member, the empty `files/` described under
[Closure](#closure).

## Envelope, version 1

```json
{"format":"pharos-capture-export","version":1,"id":"<sha256>","scope":"native-capture","host_id":"<host>","source_name":"<source>","source_kind":"<original kind>","source_completeness":"unknown","files":[{"path":"<relative slash path>","size":123,"sha256":"<stored-byte sha256>"}]}
```

- `files` lists every regular file except `export.json`, sorted by the
  UTF-8 bytes of `path`. `size` and `sha256` describe the stored bytes, so
  an encoded member is hashed as stored, not decoded.
- `id` is the lowercase SHA-256 of the envelope's canonical serialization
  with `id` omitted (the key absent, not empty). The canonical
  serialization is Go's compact `json.Marshal` of the struct in the field
  order above. It has no trailing newline, and it escapes `<`, `>`, `&`,
  U+2028 and U+2029 while leaving other non-ASCII text as UTF-8.
- `export.json` must be that canonical serialization, with `id`, optionally
  followed by one newline. A reader refuses an unknown field or version, a
  duplicate key, `null`, a second JSON value, invalid UTF-8, unsorted or
  duplicate paths, an `id` or `sha256` other than 64 lowercase hex digits,
  and a size that is not an integer from 0 to 2^53-1.
- `source_kind` keeps the manifest's spelling (`Claude`, `codex`, ...).
- `source_completeness` is always `unknown`. A verified closure shows that
  the export holds everything the manifest declares. It does not show that
  the provider kept all of its history: an empty capture, a successful last
  run or a quiet warnings list does not prove that. The manifest's own
  warnings and run record travel unchanged.

The exporter names the directory `<id>`. A verifier takes identity from
contents alone, so copying or renaming the directory changes nothing.
Location, permissions and timestamps are not part of the id. The hashes
detect corruption; they do not authenticate who made the export.

## Closure

The export holds exactly what the manifest declares:

- each file's `captured` copy and every `previous` version;
- each snapshot's `captured` copy and every `generations` entry;
- the entries above whose source has gone (`missing_since`), which are
  still evidence.

If two entries name the same stored path, their size, encoding and
stored-size claims must match, and so must their digests where both record
one.

Each `path_map` root must name a latest member or an ancestor directory of
one. The one exception is the `files` root: mapped with no latest member
below it, it is exported as an empty directory, with no file invented for
it, so an empty capture mapped to `files` exports an empty `files/`. Any
other mapped root is refused, alone or beside valid members: a file that is
gone (`files/missing.sqlite`), an earlier version, a path the capture holds
as an undeclared file, or an empty directory the manifest does not declare.
Absent evidence never becomes a directory, and nothing is dropped from the
manifest to make it pass. The exporter and the verifier apply the same rule.

Unreferenced regular files under `files/` and `history/` (a
`*.pharos-tmp` left by a killed copy, a `*.conflict-N` set aside) are left
out. A symbolic link or special file anywhere under them is refused, so the
export examines every entry there, within the inspection bounds below.

At the top of the source directory, an export admits only `manifest.json`,
`files/` and `history/`. It leaves out the state it recognizes:
`indexed.json`, `.index.lock`, `compression.json` and reader-lease
directories (`.reader-*.pharos-tmp`, `.lease-*.pharos-tmp`). Every other
entry is refused, even an empty one. This includes a Finder `.DS_Store` and
a temporary file left by an interrupted manifest write; the next capture
clears the latter.

### First-scope restrictions

These captures are refused with an explicit scope error instead of being
exported without part of their evidence. Nothing should be deleted to make
an export pass; these layouts wait for a later version that carries their
full retention closure.

- Providers other than Claude Code and Codex. Kinds are matched through the
  provider registry, and no provider code runs.
- Manifest versions other than 1–8, or newer than this build reads. Also a
  manifest whose encoded entries need a later version than it records (an
  encoded generation needs 6, an encoded file 7, an encoded latest
  snapshot 8).
- SQLite record capture (`sqlite_record_capture`, `latest_records`,
  `sqlite_record_coverage`), retained inputs (`retained_input_capture`), a
  recovery ID on any entry, or nonempty `installations`.
- A `records`, `inputs`, `preservation-pins.json`, `.delivered.json` or
  `.reader-pin-*` entry in the source directory, or a `.history-backfill`
  directory at the capture root.
- More than 100,000 files in the export (metadata included), a metadata file
  over 16 MiB (`host.json`, `manifest.json`, `export.json`), a path over
  4,096 UTF-8 bytes, or more than 1 TiB of stored bytes or of decoded bytes
  in total, counted separately.
- A source directory whose inspection would examine more than 200,000
  entries in one pass, or reach a directory more than 128 levels below the
  export root (the source directory is level 3, `captures/<host>/<source>`),
  or a path over 4,096 bytes measured as it would sit in the export. Every
  name counts: kept members, directories, unreferenced leftovers and the
  top-level state left out. Directories are listed in bounded batches, with
  cancellation checked before every name, and passing a bound fails the
  export instead of skipping the rest. The same bounds apply to verifying an
  export.
- Paths that would not travel: an empty, `.` or `..` segment, a backslash,
  NUL or colon, a drive or UNC form, a segment ending in a dot or space, or
  a Windows reserved name (`CON`, `PRN`, `AUX`, `NUL`, `COM0`–`COM9`,
  `LPT0`–`LPT9`, superscript `COM¹`–`³`/`LPT¹`–`³`, `CONIN$`, `CONOUT$`),
  extensions included (`nul.tar.gz`). Two paths that collide once
  normalized to NFC and case-folded, as whole names or at any directory
  component, are refused. Stored names are never normalized or renamed.

`source_fingerprint` in a manifest is kept as opaque provenance. It does not
authorize delivery and is not interpreted.

## Metadata validation

`host.json` (as `captureHostRecord`) and `manifest.json` are read with a
size bound and decoded strictly before anything else uses them. Each must
be one valid UTF-8 JSON value with exactly the known keys, spelled exactly,
none repeated, every value of its field's JSON type, and integer sizes.
`null` is allowed only where Pharos's own writer emits it for an empty list
(`files`, `snapshots`, `path_map`, `last_run.errors`). Only after that does
the manifest's existing captured-path check run, since that check is not a
JSON validator. The host and source must match the directory names, the
manifest's `host.id` and `source.name`, and `host.json`'s `id`.

## Reading a capture safely

The export resolves the capture root and outbox once, including any alias,
and requires both to exist. It refuses an outbox inside the capture root,
or the reverse, by directory identity. It then reads only through pinned
directory handles. On macOS and Linux every component is opened with
`openat(O_NOFOLLOW | O_DIRECTORY | O_CLOEXEC)`, and every file with
`O_NOFOLLOW | O_NONBLOCK` and classified by `fstat` on that handle. A
symbolic link is never followed, and a FIFO swapped in is refused without
blocking. Listing uses the same handles.

It takes the host's existing `.capture.lock` with an exclusive,
non-blocking `flock` (the shared `filelock` adapter). It neither creates nor
truncates the lock, and a missing lock is an error. A held lock fails at
once with "another capture is already running for this host". The lock is
held from the first metadata read until the staged copy has verified, so a
cooperating capture, compression or index pass cannot change the capture
in between. It is not a filesystem snapshot: a process writing in place
outside the lock protocol can still race. Such a change is detected, and
the export fails instead of publishing.

Each member is copied from one open handle. The stored bytes are hashed as
they are copied, and the copy is a new file, never a hard link. Each member
is checked against its manifest claim:

- A plain entry's size and, where recorded, SHA-256 must match.
- A zstd entry is decoded as a stream with the existing capture decoder's
  memory limit, reading at most the recorded size plus one byte. Its
  original size and recorded digest must match.
- An entry recorded as zstd whose file still holds its verified original
  bytes passes, as the capture readers accept it: this is a compression
  killed before its rename. Compressed bytes under a plain entry fail.
- A legacy plain snapshot with no recorded digest gets only its stored-byte
  hash.

Nothing in the capture is ever repaired, recompressed or rewritten. The
file's identity, size and modification time must be the same before and
after the copy, and its byte count must equal its size. Before the lock is
released, the metadata is read again and must be identical. Every member is
then reopened through the same confined handles and its stored bytes are
hashed again, without decoding, reading at most its copied size plus one
byte. They must hash to the copy's stored SHA-256, and the file's identity,
size and modification time must match the copy before and after that read
and at its name. This second read is not counted against the stored-byte
limit again. A mismatch fails the attempt and removes only its unpublished
stage. There is no retry, and this is not a filesystem snapshot: any finite
check can still be raced by a writer outside the lock protocol. Reading may
update access times; contents, names and modification times stay as they
were.

## Publication

The copy is staged in a private (0700) `OUT/.stage-<random>` directory, and
`export.json` is written last. The staged tree is then verified in full, as
a receiver would verify it. After that the lock is released, the stage's
files and directories are flushed, and the stage is renamed to `OUT/<id>`
with an atomic rename that never replaces: `renameatx_np(RENAME_EXCL)` on
macOS and `renameat2(RENAME_NOREPLACE)` on Linux. A filesystem without that
rename is reported as unsupported, with no check-then-rename fallback. A
failure before the rename removes only this attempt's stage. A stage is
never treated as a publication.

If `OUT/<id>` already exists, it must verify in full as the same export,
with the same id and canonical envelope. It is then flushed again and the
export succeeds; anything else there, an empty directory included, is an
error and is left untouched. When flushing the outbox fails after the
rename, the valid export stays in place. The command reports that
durability was not confirmed, and running it again confirms it.

## Verification

`capture-export-verify` (`VerifyCaptureExport`) checks a directory from its
contents alone:

1. Its top level is exactly `export.json` and `captures/`.
2. The envelope is canonical and its id matches.
3. `host.json` and `manifest.json` pass the metadata validation above.
4. The files present, the files the envelope lists and the files the
   manifest declares are the same set. The directories are exactly the
   derived ones, and nothing is a link or special file. The permitted tree
   is derived from the metadata first, so an undeclared entry is refused as
   soon as it is seen, without listing what is inside it.
5. Every file has its recorded size and stored SHA-256.
6. Every member also passes the same decoded check against its manifest
   claim.

## Platforms

macOS and Linux have adapters, and the tests run on macOS. The Linux adapter
cross-compiles, but its locking, no-follow, no-replace and flush behaviour
still needs a run on a native Linux machine against its target filesystems.
Other systems, Windows included, report the export as unsupported.
Cross-compiling is not support.
