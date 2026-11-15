# Importer plugins: bringing transcripts in from other places

Design, revised 2026-11-15 against master. **Proposal: nothing here is built,
and the decisions marked "Proposed" await the owner.** This document replaces
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
  `<id>/` folder holding every member. Fetching one every few hours would
  move the whole history each time. Proposed: Pharos gives each installation
  a private `state_dir` that survives runs; an importer keeps the previous
  envelope's members there and fetches only members whose `sha256` it lacks
  (the envelope lists them), then assembles the new envelope for Pharos to
  verify. The byte limit counts what was fetched. A content-addressed outbox
  on the sending side would make this cheap for buckets too; that is a
  request to the capture-export owner, not something to work around here.
- **Hashes detect corruption; they do not authenticate the sender.** Whoever
  can write where the importer fetches from can add conversations to the
  library. Consent says so in those words for bucket and shared-folder
  importers.
- **Ask Pharos exclusions do not travel.** `ask_native_sessions` is applied
  before parsing, in the library that ran Ask. An envelope from another
  machine that runs Ask Pharos would bring its private Ask sessions in as
  conversations. Until the envelope carries that exclusion list (a second
  request to the capture-export owner), an envelope whose host has run Ask
  Pharos cannot be told apart, so the review screen names the risk and the
  importer lands envelopes only from hosts the person confirms do not use Ask
  there. Recorded as a blocker for unattended envelope imports, not an
  opt-out.

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
  part of a request's usage could be priced. Its `usd` is a floor and does
  not count as cost, like every status other than `priced` and `assumed`.
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
- **Ephemeral machines are one machine per pool. Proposed.** A manifest or
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

`RLIMIT_CPU` and `RLIMIT_FSIZE` are dropped: Go cannot set them per child on
darwin, and a launcher shim is one more native binary to sign and review for
limits that sampling already covers. Where no process-tree reader exists
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

## The four places

| Place | How it comes in | Needs |
|---|---|---|
| SSH host | `ssh_pull.py`, `files` drops, today. Later, where Pharos runs there: fetch `capture-export` envelopes | Nothing on the host; later a Linux Pharos CLI |
| Dev container or cloud environment that stays up | The same importer through the tool's own transport (`ssh`, `docker exec`, the provider's CLI), `files` drops, machine `pool` | The transport on this Mac |
| CI runner and other short-lived machines | A step at the end of the job copies the agent folders to a bucket or artifact store; a bucket importer brings them as `files` drops, machine `pool` | A push step; nothing to pull from once the machine is gone |
| Portable machine pushing to a bucket | It runs Pharos: `capture` and `capture-export` to an outbox, synced to the bucket by the person's own tool (`aws s3 sync`, `rclone`); a bucket importer fetches and Pharos verifies | Works today from a Mac; Linux needs the CLI |
| One-off export files | `export` drops into the export collector | — |

`ssh_pull.py` changes that follow: read `kinds.json` instead of its own
patterns; fetch Codex `.jsonl.zst` rollouts; look in `~/.config/claude` and
other Claude profiles; honour the remote `CLAUDE_CONFIG_DIR` and `CODEX_HOME`
when the person's parameters say to (never by reading the remote environment
unasked); dump only `CaptureTables` from a state database when the remote
`sqlite3` allows, falling back to `.backup`, which Pharos filters on capture.

**Bucket credentials.** Pharos stores none and ships no cloud SDK. A bucket
importer calls the person's installed tool with a profile they name in the
install form; `fetches_from` shows the bucket and prefix, **Allow on this
Mac** shows the tool and profile found here, and consent carries the
"anyone who can write here" sentence above.

## Decisions for the owner

1. **Pull files, or run Pharos's collector remotely?** Proposed: both forms,
   one collection path. Build `files` first, since it works on any machine
   today and reuses capture; build the envelope importer second, since master
   needs it anyway and it serves Macs at once; treat a Linux Pharos CLI as its
   own task.
2. **Buckets.** Proposed: the pusher is Pharos's own `capture-export` plus the
   person's sync tool, with no Pharos push agent and no stored credentials.
   Two requests go to the capture-export owner: a content-addressed outbox,
   and the Ask exclusion list in the envelope.
3. **Sandboxes.** Proposed: pull while alive where the environment persists,
   push at exit where it does not, and one machine per declared pool.
4. **The same conversation twice.** Already one conversation with several
   copies under the registry's identity; no change proposed.
5. **`partial` usage cost.** Proposed: a floor that does not count as cost.
   The alternative is to count the known part and mark the total incomplete.
6. **Dropping `RLIMIT_CPU` and `RLIMIT_FSIZE`** in favour of sampling, rather
   than adding a launcher shim.

## Order of work, once agreed

1. Contracts: drop `form`, machine `pool`, `state_dir`, withdraw
   `pharos-drop/1`, `kinds.json` with its Go test.
2. Plugin runtime: manifests, installations, consent, Mac allowance, the
   background item, the native supervisor leaf with its contract tests,
   `plugin.run`.
3. `files` collection through capture, hosts and machines, review, receipts,
   Recover library. `ssh_pull.py` becomes a catalog plugin.
4. The envelope importer under `.imports/`, then a bucket importer.
5. `export` drops; usage imports with the labeling ledger importer.

Data-safety tests to write with the code, not after: a drop cannot name a
native or foreign host; a partial `files` run never reads absence as
deletion; an interrupted collection leaves valid captures and the old
cursor; an envelope that fails verification publishes nothing; the same
session from two places is one conversation; Recover library clears cursors
and pauses.
