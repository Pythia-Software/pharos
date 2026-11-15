# Importer plugins: bringing transcripts in from other places

Design, revised 2026-11-15 against master and agreed with the owner the same
day (see [Decisions](#decisions)). **Nothing here is built yet.** This document replaces
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

A kind may arrive as a `files` drop when the registry says so
(`providerSpec.Import`). First: `claude`, `codex` and canonical JSON. Kinds
whose folders exist on Linux (Copilot CLI, OpenCode, Cline, Cursor CLI and
others) are added one at a time, each with a golden record taken from a Linux
layout; until then they are refused by name. Kinds bound to a Mac app's
storage stay refused.

### `capture-export`: a verified envelope

For machines that run Pharos's collector ([capture export](capture-export.md)).
The drop is one envelope folder, unchanged, which Pharos checks with
`VerifyCaptureExport`. This replaces `pharos-drop/1`, which is withdrawn, and
it needs the importer master lacks ("there is no capture-only importer").
Where a verified envelope lands depends on how it arrived:

- **A one-off envelope,** carried by hand or fetched once, lands as history
  copies do: published under `captures/.imports/<envelope id>/` with a
  no-replace rename and a receipt, and read through `capturedFileFor`.
- **A stream of envelopes from one pusher** (below) cannot land that way:
  each is the pusher's whole capture, and a copy per turn would multiply the
  history. The pusher's host is owned by the importer, nothing else writes
  its `captures/<host>/<source>/`, so each verified envelope is **added to**
  that capture: members the library lacks are written, the manifest is
  replaced last, and a receipt records the envelope. Nothing an earlier
  envelope brought is ever removed, whatever a later one omits.

Either way the first-scope limits hold: Claude Code and Codex only, no SQLite
record capture.

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
  part of a request's usage could be priced. Its `usd`, the known part,
  counts as cost. The unknown remainder makes any total that includes it an
  estimate, shown with the same mark API cost figures already carry when
  they rest on estimates (as with Claude.ai chats); no separate "incomplete"
  state is added.
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
  owned by its installation; a drop naming a host that another installation
  owns is refused. A drop naming a host this library captures natively (a
  laptop that both runs Pharos here and pushes to the bucket) is skipped and
  reported as this library's own, not treated as an error.
- **The machine registry carries the device.** Each imported host maps to a
  `machines` row through `host_machines`, from the envelope's `machine_id` or
  the importer's hint (`/etc/machine-id`), so Machine and Execution machine
  work for remote sessions with no new dimension. Without a hint the machine
  is derived from the host, and never guessed from a hostname.
- **Ephemeral machines are one machine per pool.** A pusher or drop may
  declare a machine `pool` (for example "CI runners" or a dev container's
  project). Every container in the pool is the same **machine**, of kind
  `cloud`, so a thousand runners are one value of Machine. Each container
  stays its own **host**, because each has its own capture and two captures
  must never write one folder. Hosts are bookkeeping, not a dimension people
  group by; lists show a pool as one line with its count and last activity.
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

A kind is importable when its entry in the provider registry says so
(`providerSpec.Import`): which drop forms it may use, its usual folders on
any operating system, and the globs of what its `Plan` captures.
[`contracts/plugins/kinds.json`](../contracts/plugins/kinds.json) publishes
that for importers, with each kind's aliases, `CaptureTables` and `Content`
capabilities. Three tests keep the hand-written lists from going stale
again: a Go test holds `kinds.json` equal to the registry and checks each
kind's globs against what its `Plan` really captures from a sample folder,
and Python tests hold the JSON Schemas and `ssh_pull.py` to `kinds.json`.
Today `claude`, `codex` and `canonical` arrive as files, `claude` and `codex`
as envelopes, and `chatgpt` and `claude-ai` as export files. Still to come:
`pharos plugin kinds` to print it, and consent and **Does this look right?**
showing the `Content` capabilities in the registry's words.

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

`ssh_pull.py` now fetches Codex `.jsonl.zst` rollouts, looks in
`~/.config/claude/projects` as well as `~/.claude/projects` (another profile
is one more entry in `roots`), and follows the remote `CLAUDE_CONFIG_DIR` and
`CODEX_HOME` only when `remote_env` is set, never unasked. A test holds its
patterns to `kinds.json`. Claude Code and Codex declare
no `CaptureTables`, so its database snapshot needs no filter today; a kind
that declares them is filtered by Pharos on capture, and its importer should
fetch only those tables where the remote `sqlite3` allows.

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

**`pharos capture-push`** captures the sources it was told about and uploads
what the destination lacks. It needs no cloud toolchain: uploads are plain
HTTPS from the binary, reusing the S3, GCS and Azure clients that cloud
backups already have (`cloud_upload.go`). Out of the box:

| Destination | Credential |
|---|---|
| Amazon S3 and S3-compatible stores | an access key, ideally one that may only put objects under the prefix |
| Google Cloud Storage | an HMAC key (the same signing as S3), or a service-account key |
| Azure Blob Storage | a SAS URL for the container or prefix |
| Anything else that accepts an HTTPS `PUT` | a URL template and an optional header, covering pre-signed and unauthenticated endpoints |

A credential comes from a file or an environment variable, never from the
command line. A write-only credential is enough and is what setup creates: a
sandbox then holds nothing that can read other machines' transcripts. Nothing
assumes `aws`, `gcloud` or `az` is installed or signed in, on either end.

**The bucket importer is built in.** Fetching needs no script or
interpreter: a manifest with `{"builtin": "bucket-import/1"}` runs in
Pharos's own binary with the same clients, like the declarative exporter. Its
credential may read and list, is imported at setup and stored with the plugin
in the library, as cloud backups store theirs; `fetches_from` shows the
bucket and prefix.

## Pushing after every turn

Setup encourages running the push from the agent app's own hooks: for Claude
Code a `Stop` hook (and `SessionEnd`), for others the nearest equivalent or a
timer. Every turn is then uploaded as it finishes, which is what makes
short-lived machines work without a reliable exit step. That puts three
pressures on the design: a push must cost what the turn added, several
things push at once, and nothing may be lost or counted twice.

### The bucket layout: nothing is ever overwritten

```text
<prefix>/chunks/<sha256>                              a piece of a member, named by its content
<prefix>/exports/<pusher>/<source>/<time>-<id>.json   one envelope: export.json plus each member's chunk list
<prefix>/hello/<pusher>.json                          written by `capture-push --check`; the one object a pusher rewrites
```

- **A pusher is one capture root.** It gets a random ID when the root is
  created. Every key a pusher writes is either under its own ID or named by
  the content it holds. Two writers therefore never put different bytes at
  one key, so the bucket needs no locking, no conditional writes and no
  read permission for pushers, and an interrupted or repeated upload is
  harmless.
- **An envelope's file is written after its chunks,** so a reader never
  sees one whose pieces are missing. A push that dies halfway leaves only
  unreferenced chunks, which the next push reuses.
- **Members are stored in content-defined chunks** (the `gear-v1` chunking
  the Claude export store already uses). Transcripts grow by appending, so a
  member's hash changes every turn while all but its last chunk stay the
  same; whole-file addressing would upload a long conversation again on
  every turn.
- **`<time>` orders a pusher's envelopes** and each names the one before
  it. Keys are never reused, so even a mistake that makes two machines share
  a pusher ID loses nothing: both chains are there to read.

### On the pushing machine

- **One push at a time, and the last turn always goes.** `capture-push`
  returns at once and works in the background, so a turn never waits on a
  network. It takes a lock on the capture root. A push asked for while one
  runs sets a flag, and the running push makes one more pass when it
  finishes: ten sessions ending turns together cause two passes, not ten,
  and the newest bytes are always in the last one. Passes are at least
  30 seconds apart.
- **A pass costs what changed.** Capture copies only changed files. The
  pusher builds the envelope from the capture root in place (it does not
  write the outbox copy `capture-export` makes), keeps each file's hashes
  and chunk list by path, size and time, and remembers which chunks it has
  uploaded.
- **Several pieces of work at once are one capture.** Sessions, worktrees
  and agent apps of one OS user share one capture root and one pusher;
  every pass carries all of them. Another OS user or container is another
  pusher and never touches the first one's keys.
- **A transcript caught mid-write is fine.** A pass may copy a file while
  an agent is appending to it. The next pass brings the rest, and the reader
  already tolerates an unfinished last line.
- **It never fails the hook.** Errors go to a log that
  `capture-push --status` reads. After repeated refusals from the bucket it
  tries once a day and says so, instead of failing on every turn forever.
- **`capture-push --wait`** uploads in the foreground, for the last step of
  a CI job: a background push can be killed with its machine.
- **A copied capture root is noticed.** The root records the machine it was
  made on; found on another machine (an image baked with one inside, a
  cloned volume) it takes a new pusher ID before pushing. Setup says not to
  bake one into an image.

### On the library's side

- **The importer reads each pusher's newest envelope,** plus any envelope
  the newest does not descend from. Its cursor is the last key it handled
  per pusher. Envelopes pushed between two fetches are skipped, since each
  one contains the ones before it.
- **It fetches only chunks it lacks,** keeping what it needs to know that in
  its `state_dir`, rebuilds each member, and hands Pharos a whole envelope to
  verify. A chunk whose bytes do not match its name is refused and reported.
- **Collection only adds** (see [envelopes](#capture-export-a-verified-envelope)).
  Handling an envelope twice changes nothing; handling them out of order
  changes nothing; an envelope that lacks something an earlier one had
  removes nothing and is reported as a regression.
- **A conversation that grows is one conversation.** Each turn's push makes
  the same file longer; indexing extends the same conversation, as it does
  for a transcript growing on this Mac.
- **The same conversation from several places is one conversation.** Two
  pushers that both hold a session (a transcript copied into a new container
  and resumed, or a pull and a push of the same machine) are copies under
  the authority rules above: a longer copy takes over, diverging copies
  merge by message, and no message is counted twice because messages are
  keyed by their native IDs within the conversation.
- **Usage is not doubled.** Tokens and cost come from the conversation's
  messages, so copies and repeated pushes cannot add to them.

### Keeping the bucket small

Setup gives the bucket a lifecycle rule that deletes objects after 90 days,
and tells the pusher so. A pusher uploads again any chunk its current
envelope still needs once its record of that chunk is half that old, which
resets the clock. So a live pusher's history stays whole, a machine that has
gone leaves nothing behind after 90 days, and an abandoned experiment cleans
itself up. The library keeps everything it collected; the bucket is a relay.
A library that first reads the bucket later than that gets what live pushers
still hold, not what dead ones once sent, and setup says so.

## Setting up

**Setup recommends the bucket.** The first screen offers three ways in and
marks the first:

1. **Other machines push to a cloud bucket (recommended).** Neither side
   needs the other to be switched on, reachable or awake at the same time; a
   sandbox that lives for a minute still delivers; nothing depends on SSH
   keys, addresses or this Mac's network.
2. **Pull over SSH,** for a machine you cannot install anything on.
3. **A script of your own.**

**The bucket path is two sets of commands, kept apart by privilege**, in the
manner of cloud backup's setup: each is offered as prepared Bash and as a
prompt to hand to an agent.

- **Create the bucket** runs once, where the person is signed in to their
  cloud account with rights to create things. It creates the bucket (private,
  with the lifecycle rule), a credential that can only write under the
  prefix, and a credential that can only read and list. It writes two files
  and prints nothing secret:
  - `pharos-push.json`: the destination and the write credential;
  - `pharos-read.json`: the destination and the read credential, which
    Pharos imports from the file and then offers to delete.
- **Set up a machine** runs on each sandbox, dev server or image, with
  `pharos-push.json` and nothing else. It installs the Linux CLI, puts the
  file where `capture-push` reads it (or names the CI secret to hold it),
  adds the hooks for the agent apps found there, and finishes with
  `capture-push --check`. The agent given this prompt never sees an
  administrator's session or a credential that can read.

Setup also saves the non-secret plan of what was created, so removal can
later print the exact commands that take it down.

**Hosted agent platforms get a recipe.** "Where do your agents run?" offers
Niteshift, dev containers, the vendors' agent clouds and others, each of
which turns the generic steps into exactly what to commit and where to add
a secret, or needs no secret at all. See
[importing from agent platforms](plugin-import-platforms.md).

**Setup shows which half is not working.** While it is open it checks the
bucket every few seconds and ticks off:

1. Pharos can read the bucket.
2. A machine has reached the bucket (a `hello` object, written by `--check`
   and by every first push).
3. An envelope has arrived.
4. Conversations were collected (then **Does this look right?**).

Each step that has not happened names what to run or look at. Someone who
stops at step 2 knows the bucket is fine and the sandbox is not.

## Integrations that never work, or stop

Many setups will be started and abandoned, or work for a month and go
quiet. They must not check a dead bucket forever, clutter Background
Processing, or leave credentials lying about. Pharos tells four states
apart, by what it received and not by whether runs "succeeded":

| State | Means | What Pharos does |
|---|---|---|
| **Setting up** | Nothing has ever been collected | Checks often for an hour, hourly for a day, daily for a week. Then pauses itself, with one notice: finish setup, or remove it. After 30 days paused it is removed, with its stored credential; nothing was imported, so nothing is lost |
| **Active** | Collected something recently | Runs on its cadence |
| **Quiet** | Fetches succeed and bring nothing new, for longer than 30 days and four times its longest gap so far | Drops to a daily check without asking. Its line says since when, with **Stop checking**. After 90 days quiet it pauses itself with one notice. What it imported stays |
| **Failing** | Fetches fail (credential expired, bucket gone, host unreachable) | The existing rule: retries, then paused after five failures, naming the cause and the one thing to do |

- **Quiet is not broken.** A bucket nobody pushed to and a bucket that
  refuses the credential are different states with different words. A
  missing measurement is never shown as zero conversations.
- **One notice per change of state,** never a repeating one. Paused
  integrations cost nothing and sit together at the bottom of **Plugins**
  under one count.
- **Pushers inside a working bucket** come and go by design. They are shown
  as a count and a last-active time per pool, never as a list of failures,
  and are never pruned from the library.
- **Removing is as guided as adding.** Remove shows what will stay (every
  imported conversation), deletes the stored credential, and prints from the
  saved plan the commands that delete the bucket and its credentials and
  that take the hook and key off each machine, as Bash or as an agent
  prompt. Running them is the person's choice.
- **The far side quiets itself too.** A pusher whose bucket has refused it
  for a week tries daily, not per turn, and the lifecycle rule empties a
  bucket nobody pushes to.

The automatic pause and removal apply only to importers, and removal only to
ones that never collected anything.

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

8. **Setup recommends the bucket,** with bucket creation and machine setup
   as separate command sets, each as Bash or an agent prompt.
9. **A partly priced usage record counts its known part,** and the
   uncertainty joins the existing estimate mark on API cost.
10. **Pushes are designed for every turn:** nothing in the bucket is
    overwritten, members are chunked, collection only adds.

11. **Envelopes from a pusher are added to that host's capture;**
    `.imports/` stays the home of one-off envelopes.
12. **Each container in a pool is its own host,** under one machine.
13. **The numbers:** 30 seconds between passes; a 90-day bucket lifecycle; a
    setup that never collected anything pauses after a week and is removed,
    with its credential, 30 days later; a quiet integration pauses after 90
    days.

The contracts for all of this are in
[`contracts/plugins/`](../contracts/plugins/): drop forms and pools in
`common.schema.json`, `state_dir` in the run request, `bucket-import/1` in
the manifest, and the bucket's documents in `push.schema.json`, each with
checked examples.

## Order of work

1. Contracts (done, with `kinds.json` and the registry's `Import`
   declarations).
2. Plugin runtime: manifests, installations, consent, Mac allowance, the
   background item, the native supervisor leaf with its contract tests,
   `plugin.run`.
3. `files` collection through capture, hosts and machines, review, receipts,
   Recover library. `ssh_pull.py` becomes a catalog plugin.
4. The envelope importer: one-off under `.imports/`, streams added to the
   pusher's capture.
5. `capture-push` with the bucket layout, chunking and the four
   destinations (built: [capture-push.md](capture-push.md), with keyless
   credentials, `--watch` and the Niteshift recipe); then `bucket-import/1`.
6. Setup: the recommended bucket path, both command sets, the four-step
   check; integration states, automatic pause and guided removal.
7. The Linux CLI: build, release artifact, native lifecycle tests on Linux,
   hook snippets.
8. `export` drops; usage imports with the labeling ledger importer.

Data-safety tests to write with the code, not after: a drop cannot name a
native or foreign host; a partial `files` run never reads absence as
deletion; an interrupted collection leaves valid captures and the old
cursor; an envelope that fails verification publishes nothing; the same
session from two places is one conversation; Recover library clears cursors
and pauses; a push interrupted at any point never leaves an envelope whose
chunks are missing; envelopes handled twice, out of order or from two
pushers sharing an ID add everything and remove nothing; overlapping pushes
on one machine always upload the newest bytes; a setup that collected
something is never removed automatically.
