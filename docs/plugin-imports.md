# Importer plugins: bringing transcripts in from other places

Design, revised 2026-11-15 against master, with the owner's decisions of the
same day (listed under [Decisions](#decisions)). **Nothing here is built.** This document replaces
the *Importers* and *Cursors* sections of [plugins](plugins.md) and the import
parts of the [implementation plan](plugins-implementation.md). The shared
plugin model (manifest, consent, installing, limits, the run contract) stays in
[plugins](plugins.md); the export side is a separate thread.

## Goal

Pull agent transcripts into a library from places other than this Mac, on a
regular cadence:

- an SSH host (Linux, or Windows through WSL);
- a sandbox or container: dev containers, cloud development environments, CI
  runners;
- a cloud bucket that a portable machine pushes to;
- one-off export files.

## What stays as decided

- Pharos runs the importer, a script or program, on Pharos's schedule. The
  script owns the connection, its configuration and an opaque cursor. Pharos
  never tells it what the library holds, so sending something again is fine.
- Pharos limits time and bytes, whichever comes first, and shows each run's
  bandwidth and conversation count.
- Plugins are non-hostile but fallible. Pharos bounds them; it does not
  sandbox them.
- Permissions are static, yes or no. A plugin belongs to its library, and each
  Mac allows a scripted plugin once.
- The first run after any change is held for **Does this look right?**
- Uninstalling never deletes what was imported.
- Usage-only imports bring no text, are deduplicated by
  `(plugin, namespace, id)`, and estimates never count as cost.

## The shape, in one paragraph

An importer **fetches**; Pharos **collects**. The script's only job is to put
bytes into its staging folder in one of three declared forms. Pharos then runs
its own, existing machinery over the staging folder: capture for raw source
files, capture-export verification for envelopes, and the export collector
for export files. No second collector, parser or deduplication exists for
imports, and everything an importer brings ends up as an ordinary capture of a
host, indexed like any other.

```text
script (fetch)            staging/<run>/out            Pharos (collect)                 library
────────────────          ─────────────────            ───────────────────────────      ─────────────────────
ssh, docker, bucket  ──▶  files drop      ──────────▶  capture with the kind's Plan ──▶ captures/<host>/<source>
                          envelope drop   ──────────▶  capture-export verify        ──▶ captures/.imports/<id>
                          export drop     ──────────▶  export collector (claude-ai) ──▶ .claude-imports/<host>/…
                          usage drop      ──────────▶  usage importer               ──▶ imported_usage
```

## Drop forms

`result.json` declares each folder of `output_dir` as one drop. The form is
new; the rest of the run contract is unchanged.

### `files`: a mirror of a source's folder

For machines that run no Pharos. The folder mirrors the source's layout under
its original `root`, exactly as today's `ssh_pull.py` produces it.

Pharos does not adopt these files directly. It **captures** the staged folder
as a source of the remote host, with the kind's own `Plan` from the provider
registry (`provider_registry.go`), recording each file's original path as
`root` joined with its relative path. Consequences:

- What is read is exactly what the adapter reads on a Mac. The stale glob
  list in the old design goes away: a file the `Plan` does not ask for is
  ignored and reported, not adopted.
- A staged database goes through capture's SQLite snapshot machinery, and
  `CaptureTables` limits what is kept, so tables that hold credentials never
  enter the library even when the script fetched the whole file. Staging is
  deleted when the run ends.
- Versions, `history/`, compression at rest and capture retention behave as
  for any capture. "A dropped file replaces its captured copy, a missing file
  is left alone" is what capture already does for a source that shows only
  part of itself; the plan marks the run as partial so absence is never read
  as deletion.
- Codex's `.jsonl.zst` rollouts keep the plain file's identity, as capture
  already arranges.

A kind may arrive as a `files` drop when the registry says so (a new
`Importable` field on `providerSpec`). First: `claude` and `codex`. Kinds
whose folders exist on Linux (Copilot CLI, OpenCode, Cline, Cursor CLI and
others) are added one at a time, each with a golden record taken from a Linux
layout; until then they are refused by name. Kinds bound to a Mac app's
storage stay refused.

### `capture-export`: a verified envelope

For machines that run `pharos capture` and `pharos capture-export`
([capture export](capture-export.md)). The drop is one envelope folder,
unchanged. Pharos runs `VerifyCaptureExport` on it, then lands it as history
copies land: under `captures/.imports/<envelope id>/` with a receipt, read
through `capturedFileFor`. This replaces `pharos-drop/1`, which is withdrawn.

It needs the importer master lacks ("there is no capture-only importer").
That importer is part of this work:

- verify, then publish under `.imports/<id>` with a no-replace rename; an
  envelope already present is a no-op;
- write a receipt (below) and let indexing read the capture from there;
- inherit the envelope's first-scope limits: Claude Code and Codex only, no
  SQLite record capture.

Three properties of envelopes shape the design:

- **An envelope is a whole capture, not a change.** Every export is a new
  `<id>/` folder holding every member, so moving one every few hours would
  move the whole history each time. Envelopes therefore travel in the
  [pushed layout](#pushing-from-a-machine-that-runs-pharos), which stores
  each member once by its hash; the importer fetches only what it lacks and
  reassembles the envelope for Pharos to verify. The envelope format itself
  does not change.
- **Hashes detect corruption; they do not authenticate the sender.** Whoever
  can write where the importer fetches from can add conversations to the
  library. That is the person's choice to make: setup recommends a bucket
  that needs credentials to write and says why, and does not refuse one that
  does not.
- **Ask Pharos sessions travel as conversations.** `ask_native_sessions`
  is applied in the library that ran Ask, so an envelope from another
  machine that uses Ask Pharos brings those sessions in as ordinary
  conversations. Decided: that is acceptable.

### `export`: provider export files

A Claude.ai or ChatGPT export that a script downloaded (from a bucket, a
shared folder) is not mirrored. The drop's files are handed to the existing
collector, the same path as **Collect Claude export**
(`collectClaudeExport`, kind `claude-ai`; ChatGPT's `conversations.json`
through kind `chatgpt`, of which `chatgpt-export` is an alias). The account
privacy filter and `light_metadata` exclusions apply before anything is
published, and the collection is named for the installation. Export drops
name no machine: an export does not acquire the device that downloaded it.

### `usage-records/1`

Unchanged in form; see [Usage imports](plugins.md#usage-imports). Corrections:

- `cost.status` gains `partial`, which the labeling ledger records when only
  part of a request's usage could be priced. Whether its `usd` counts as
  cost is [open](#decisions).
- Labeling usage is a **usage kind** (`automation`, with the job and dataset
  as fields), not a new channel. Channel answers how work started, and
  `automation:<name>` already names a message sender.
- "Cost at today's prices" no longer exists (#706); Pharos stores the
  record's own cost and status.
- "Work item" in the usage and plugin text means **workspace**.

## Hosts and machines

The old design invented `imp_…` hosts. Instead:

- **A host stays what it is: one machine and one OS user.** A remote
  account is a row in `hosts` like a Mac's, with captures under
  `captures/<host>/`. For a `files` drop Pharos derives the ID with
  `stableID("host", …)` from the installation, the importer's machine `key`
  and the remote user, so a script cannot name an existing host. For an
  envelope the ID is the one the sending Pharos gave itself in `host.json`.
- **A host belongs to the first thing that wrote it.** An imported host is
  owned by its installation; a drop naming a host that this library captured
  natively, or that another installation owns, is refused. Envelopes never
  land in a live `captures/<host>/` in any case.
- **The machine registry carries the device.** Each imported host maps to a
  `machines` row through `host_machines`, from the envelope's `machine_id` or
  the importer's hint (`/etc/machine-id`), so Machine and Execution machine
  work for remote sessions with no new dimension. Without a hint the machine
  is derived from the host, and never guessed from a hostname.
- **Ephemeral machines are one machine per pool.** A manifest or
  drop may declare a machine `pool` (for example "CI runners" or a dev
  container's project). Every container in the pool is the same machine and
  host, of kind `cloud`; the container's own ID stays in the run record as
  evidence. A thousand runners must not become a thousand values of Machine.
- **Recorded paths are POSIX.** Capture manifests use legacy POSIX lexical
  paths (`internal/sourcepath`). A drop's `root` must be an absolute POSIX
  path; a Windows-style root is refused until a path-flavor migration exists.
  Windows machines come in through WSL paths for now.

**The same conversation twice.** Native session IDs are global: a
conversation is identified by `(provider, account, native_id)`, with no host
in it. A session captured on this Mac and imported from another place, or
imported by two importers, is one conversation with several copies, under
the existing authority rules in `copies.go` (a copy takes over only when it
holds every stored message; diverging copies merge append-only). Imports add
no rule. One consequence to accept: a `files` pull and an envelope of the
same remote account have different host IDs, so they are two copies of each
conversation, which costs storage but not correctness.

## Kinds come from the registry

`transcript_kind` in `common.schema.json` and the globs in the old text were
hand-written and went stale. Proposed: `pharos plugin kinds` prints, from the
provider registry, each importable kind with its aliases, drop forms, the
files its `Plan` reads, its `CaptureTables`, its `Content` capabilities and
its usual folders. The checked-in copy,
`contracts/plugins/kinds.json`, is held equal to the registry by a Go test,
and the JSON Schemas and `ssh_pull.py` read it. Consent and **Does this look
right?** show the `Content` capabilities in the words the registry uses.

Usual folders on other operating systems are data for importers only. Source
discovery's rule holds: Pharos reads nowhere it was not told to, and an
importer's consent names every place it reads.

## Running an import

### Operation

One run is one operation with one stage:

| Kind | Label · verb | Trigger | Proc | Holds | Affects | Stop | On stop or eject | Latency | Journal | Pri |
|---|---|---|---|---|---|---|---|---|---|---|
| `plugin.run` (variant `import`) | Import from *name* · Importing | schedule (background item), user (Run now), cli | svc (+ plugin process) | D:w (script, staging) | — | work | Signals the plugin's process group, then force-kills it after 10 s; staging is discarded and nothing is adopted. | record | always | bg |
| `capture.import` (variant `plugin`) | Collect import · Collecting | stage of `plugin.run`; its index runs as a chained `background.capture` | svc | D:w I:w C(h):w | capt:h/\* | work | Stops between files; what was collected stays valid, and the next run sends the rest again. | record | always | bg |

The fetch holds only the drive, so a slow network never blocks indexing.
Collection shares `capture.import`'s holds, on the imported host's capture
lock rather than this Mac's, and its freshness domain is that host's.

### Triggers are background items

No second scheduler. Each installed importer is one item in Background
Processing, under **Plugins**:

- its cadence is Frequently, Hourly, Daily or Only when you ask; the
  manifest's `schedule` picks the default (Hourly unless it asks for less
  often), and the person can change it;
- `hold_when_busy`, the battery choices, Low Power Mode and pause apply as to
  any item, and **Run now** overrides them;
- the manifest's guards (minimum interval, runs a day) remain as ceilings the
  item cannot exceed;
- retries and "paused after five failures" stay as designed, shown through
  the item's state.

`after_index` and `threshold` are exporter triggers and do not apply.

### A place to keep state

Besides `output_dir`, which lasts one run, each installation gets a private
`state_dir` in the library that survives runs. An importer keeps there what
saves it fetching again, such as the members of the last envelope. Pharos
never reads it, counts it against the byte limit, and deletes it on
uninstall, on Recover library and whenever the person asks.

### Process supervision

Supervision lives in a reviewed native leaf, `plugin_process*.go`, listed in
`internal/architecture/native_test.go`, built on the same mechanisms as the
labeling runner's (`labeling_process*.go`): own process group, group
observation with unknown kept apart from absent, terminate then force-kill,
and the leader's **start identity** recorded with the run. Recovery after a
crash signals a group only when the recorded start identity matches; a PID
alone never authorizes it. A platform that cannot provide the group refuses
to run scripted plugins; it does not run them unsupervised.

Each run takes one cooperative CPU grant from the process's `workAuthority`
for its whole life and tells the script its size (`PHAROS_CPUS`). The grant
is returned after the group is gone.

Limits, revised:

| Limit | How |
|---|---|
| Wall time | deadline, then stop |
| Staging bytes | measuring `output_dir` and `state_dir` every 2 s |
| Memory | sampling the group's footprint where a process-tree reader exists |
| CPU time | sampled with memory, as a multiple of the grant |

There is no `RLIMIT_CPU` or `RLIMIT_FSIZE`: Go cannot set them per child on
darwin, and a launcher shim would be one more native binary to sign and
review for limits that sampling already covers. Where no process-tree reader exists
(Linux today), memory and CPU limits are reported as not enforced, never as
passed.

### Review and adoption

- **Does this look right?** is computed from what collection would do, before
  anything is published: hosts and machines, sources, conversation counts,
  date range, a few titles, the content capabilities of each kind, ignored
  files and warnings. **Keep** collects; **Discard** deletes staging and
  keeps the cursor.
- Files become durable first; then the run record, receipts and cursor
  commit together. A stop in between leaves valid captures and an old
  cursor, so the next run sends the same files again and capture finds them
  unchanged.
- **Receipts.** `plugin_imports` records, per run, host and source: the drop
  form, the envelope ID or the capture run it produced, bytes, and the
  installation. The per-file table in the old plan is dropped; capture
  manifests already record every file, size and hash.

### Recover library

A recovered or restored library has forgotten what it collected after the
backup, while a stored cursor says it was sent. Recover library therefore
clears every importer's cursor, trigger state and run state, and starts
every plugin paused, with the same notice exporters get. Sending again is
safe by design.

## The places

| Place | How it comes in | Needs on that machine |
|---|---|---|
| SSH host | Pulled: `ssh_pull.py`, `files` drops. Or pushed, as below | Nothing to pull; the Linux CLI to push |
| Dev container or cloud environment that stays up | Pulled through the tool's own transport (`ssh`, `docker exec`, the provider's CLI) as `files` drops, or pushed | Nothing, or the Linux CLI |
| CI runner and other short-lived machines | Pushed before the machine goes away | The Linux CLI and an upload credential |
| Portable machine | Pushed to a bucket | Pharos, or the Linux CLI |
| One-off export files | `export` drops into the export collector | — |

All of these are supported: pulling raw files from a machine that runs
nothing of ours, and pushing from a machine that runs Pharos's collector.

`ssh_pull.py` changes that follow: read `kinds.json` instead of its own
patterns; fetch Codex `.jsonl.zst` rollouts; look in `~/.config/claude` and
other Claude profiles; honour the remote `CLAUDE_CONFIG_DIR` and `CODEX_HOME`
when the person's parameters say to (never by reading the remote environment
unasked); dump only `CaptureTables` from a state database when the remote
`sqlite3` allows, falling back to `.backup`, which Pharos filters on capture.

### Pushing from a machine that runs Pharos

**A prebuilt Linux CLI.** Releases gain a static `pharos` binary for Linux
(x86-64 and arm64) that someone adds to a machine with one download. Its
supported surface there is deliberately small: `capture`, `capture-export`,
`capture-export-verify` and the new `capture-push`. It keeps a capture root
and nothing else: no library, catalog, service or UI. This is the "Linux
host" task the portability guide sets apart, scoped to those commands, and it
is done when they have run and passed their lifecycle tests on Linux, not
when they cross-compile. A Mac pushes with the same command from the app's
own binary.

**`pharos capture-push DESTINATION`** captures the sources it was told
about, exports each, and uploads what the destination lacks:

```text
<prefix>/members/<sha256>            each envelope member, stored once
<prefix>/exports/<host>/<source>/<id>.json   the envelope's export.json
<prefix>/heads/<host>/<source>.json  the newest envelope ID, written last
```

- It is incremental at both ends. Capture copies only what changed; the
  pusher remembers which members it uploaded (and can ask the bucket when
  its credential may), so a turn that added a few kilobytes uploads a few
  kilobytes. The head is written last, so a reader never sees an envelope
  whose members are not all there.
- Old envelopes and members no head refers to can be removed by the
  bucket's own lifecycle rule; nothing depends on them.
- It needs no cloud toolchain. Uploads are plain HTTPS from the binary,
  reusing the S3, GCS and Azure clients that cloud backups already have
  (`cloud_upload.go`). Out of the box:

  | Destination | Credential |
  |---|---|
  | Amazon S3 and S3-compatible stores | an access key, ideally one that may only put objects under the prefix |
  | Google Cloud Storage | an HMAC key (the same signing as S3), or a service-account key |
  | Azure Blob Storage | a SAS URL for the container or prefix |
  | Anything else that accepts an HTTPS `PUT` | a URL template and an optional header, covering pre-signed and unauthenticated endpoints |

  A credential comes from an environment variable or a file, never from the
  command line. A write-only credential is enough and is what setup
  suggests: a sandbox then holds nothing that can read other machines'
  transcripts. Nothing assumes `aws`, `gcloud` or `az` is installed or
  signed in, on either end.

**Run it from the agent's own hooks.** Setup shows, per agent app, the
snippet that runs the push automatically: for Claude Code a `Stop` hook (and
`SessionEnd`), so every turn is uploaded as it finishes, which is what makes
short-lived machines work without a reliable exit step; for others, the
nearest equivalent or a timer. For that to be safe in a hook, `capture-push`:

- returns at once and works in the background, so a turn never waits on a
  network;
- takes a lock, so overlapping turns start one push, and a push asked for
  while one runs is folded into one more pass;
- never fails the hook: errors go to a log that `capture-push --status`
  reads.

**The bucket importer is built in.** Fetching from a bucket needs no script
or interpreter: a manifest with `{"builtin": "bucket-import/1"}` runs in
Pharos's own binary with the same clients, like the declarative exporter. It
reads the heads, fetches members it has not kept in its `state_dir`,
reassembles each envelope and declares it as a `capture-export` drop. Its
read credential is entered at install and stored with the plugin in the
library, as cloud backups store theirs; `fetches_from` shows the bucket and
prefix.

**Setup is one screen that produces both halves.** Choosing "a bucket other
machines push to" yields the importer on this Mac and, to copy, the install
line for the Linux CLI, the `capture-push` configuration and the hook
snippet, with the recommendation to use a bucket that requires credentials.

## Decisions

Decided by the owner on 2026-11-15:

1. **Every mode is supported:** raw pulls, pushes from machines that run
   Pharos, and a prebuilt Linux CLI that makes pushing easy to set up.
2. **Uploads to AWS, GCP and Azure work out of the box,** over plain HTTPS
   with whatever credential the person supplies, assuming no vendor CLI and
   no signed-in toolchain.
3. **Sandboxes:** pull where the environment persists, push where it does
   not, one machine per declared pool, and setup that points people at
   agent hooks so pushes happen after every turn.
4. **The same conversation twice** is one conversation with several copies,
   under the registry's identity. No change.
5. **Ask Pharos sessions may be imported** as conversations.
6. **Unauthenticated buckets are allowed;** setup recommends authenticated
   ones.
7. **No `RLIMIT_CPU` or `RLIMIT_FSIZE`:** limits are enforced by sampling.

Open:

- **A usage record whose cost is only partly known.** The labeling ledger
  marks some requests `partial`: it could price some of the tokens but not
  all. Either such a record adds nothing to cost totals and is shown as
  "at least $x, unpriced in part" (proposed, since every other not-fully-
  priced status already stays out of cost), or its known part is added to
  cost and the total is marked incomplete.

## Order of work

1. Contracts: drop `form`, machine `pool`, `state_dir`, the pushed layout,
   `bucket-import/1`, withdraw `pharos-drop/1`, `kinds.json` with its Go
   test.
2. Plugin runtime: manifests, installations, consent, Mac allowance, the
   background item, the native supervisor leaf with its contract tests,
   `plugin.run`.
3. `files` collection through capture, hosts and machines, review, receipts,
   Recover library. `ssh_pull.py` becomes a catalog plugin.
4. The envelope importer under `.imports/`.
5. `capture-push` with the pushed layout and the four destinations, on the
   Mac first; then `bucket-import/1` and the setup screen.
6. The Linux CLI: build, release artifact, native lifecycle tests on Linux,
   hook snippets.
7. `export` drops; usage imports with the labeling ledger importer.

Data-safety tests to write with the code, not after: a drop cannot name a
native or foreign host; a partial `files` run never reads absence as
deletion; an interrupted collection leaves valid captures and the old
cursor; an envelope that fails verification publishes nothing; the same
session from two places is one conversation; Recover library clears cursors
and pauses; a push interrupted at any point leaves the previous head
readable and never a head whose members are missing.
