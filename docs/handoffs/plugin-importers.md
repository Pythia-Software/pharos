# Bring transcripts in from other places

## Goal

People want Pharos to pull their agent transcripts in from places other than
this Mac, on a regular cadence: an SSH host (Linux, possibly Windows), a
sandbox or container (dev containers, cloud development environments, CI
runners), a cloud bucket that a portable machine pushes to, and one-off
exports. This handoff takes over the **import** side of the plugin design in
draft PR #347 (branch `gbdubs/pharos-remote-transcripts`). The export side
(connector API, privacy, sharing tiers, receivers) stays with the original
thread.

The work so far is design only. Revise the import design against master
first, and agree its shape with the owner before building service code.

## What exists

- [`docs/plugins.md`](../plugins.md): the shared plugin model (manifest,
  consent, installing, triggers, limits, runs) and, for imports, **Importers**,
  **Usage imports** and **Cursors**.
  [`docs/plugins-implementation.md`](../plugins-implementation.md): Go
  interfaces, catalog tables (`plugin_imports`, `imported_usage`), operation
  kinds and order of work.
- [`contracts/plugins/`](../../contracts/plugins/): `run-request`,
  `run-result`, `drop-bundle`, `usage-record` and the manifest schema, with
  examples and `test_contracts.py`.
- [`importers/`](../../importers/): `pharos_importer.py` (stdlib helper for
  the run contract) and `ssh_pull.py`, an SSH reference importer for Claude
  Code and Codex, with an end-to-end test using a fake `ssh`.
- [`plugins-design-review.md`](plugins-design-review.md): review rounds and
  the owner's settled decisions.

## Decided; keep

- Pharos runs the importer, a script or program, on Pharos's own schedule
  and triggers. The script owns the connection, the configuration and an
  opaque cursor. Pharos never tells it what the library already holds, so
  re-sending duplicates is acceptable.
- Pharos limits time and bytes, whichever is reached first. Each run's
  bandwidth and conversation count are visible to the person.
- Plugins are non-hostile but fallible ("a misbehaving child"). Pharos
  bounds them; it does not sandbox them.
- Permissions are static and the answer is yes or no. A plugin belongs to
  its library, and each Mac allows a scripted plugin once.
- The first run after any change is held for **Does this look right?**
  before adoption.
- Uninstalling never deletes what was imported.
- Usage-only imports (the labeling ledger) bring no text. They are
  deduplicated by `(plugin, namespace, id)`, the namespace belongs to the
  plugin that declares it, and estimates never count as cost.

## What master changed (found by syncing on 2026-11-14)

Most consequential first. Each needs a design revision.

1. **Capture export is the transport.** `pharos capture-export` and
   `capture-export-verify` ([capture-export.md](../capture-export.md)) copy a
   captured Claude Code or Codex source byte for byte into a verified,
   catalog-free envelope. [portability.md](../portability.md) says "there is
   no capture-only importer" and "reuse capture and SQLite snapshot machinery
   rather than building another collector engine". So:
   - replace our `pharos-drop/1` bundle with capture-export envelopes;
   - write the missing importer, landing like history copies do: verified,
     under `captures/.imports/`, with a receipt (see `capturedFileFor` in
     `capture_index.go` and
     [library-management.md](../library-management.md#verified-transfer-and-recovery)).

   Note the first-scope limits: Claude Code and Codex only, no SQLite record
   capture. The Linux adapter has never run on Linux.
2. **Import kinds come from the provider registry**
   (`internal/archive/provider_registry.go`).
   - A drop's layout is the kind's `Plan`: exactly what the adapter reads.
   - `CaptureTables` limits database captures so credentials stay out.
   - `Content` capabilities belong on the consent and review screens.
   - Our hand-written `transcript_kind` list in `common.schema.json` and the
     globs in `plugins.md` are stale. `chatgpt-export` is an alias, and
     `claude-ai` is missing. Several agent apps we called "Mac-only" (Copilot
     CLI, OpenCode, Cline, Cursor CLI and others) run on Linux.
3. **Export files already have a pipeline.** `capture.import` (variant
   `claude-ai`) collects Claude.ai exports with an account privacy filter
   ([claude-export-storage.md](../claude-export-storage.md),
   [claude-export-lifecycle.md](../claude-export-lifecycle.md)). Exports
   pulled from a bucket should enter that way, not as mirrored files.
4. **Fix the operation-contract row** in
   [background-work.md](../background-work.md):
   - add the `Label · verb` and `Trigger` columns;
   - use Stop `work` (`Op.setStopper`);
   - take `D:w`, and adoption shares `capture.import`'s holds
     (`D:w I:w C(h):w`);
   - make Affects a freshness domain such as `capt:h/*`.

   Consider making import a `capture.import` variant. Map triggers and guards
   onto Background Processing's existing items (cadences, `hold_when_busy`,
   battery and pause modes) rather than a parallel scheduler.
5. **Process supervision** must live in the reviewed native leaves:
   `process_isolation*.go`, the labeling runner's process code, and
   `internal/architecture/native_test.go`. It must also:
   - take one cooperative CPU grant per run
     ([work-portability.md](../work-portability.md));
   - use process-start identity, not PIDs, for cleanup.

   Go cannot set per-child `RLIMIT_CPU` or `RLIMIT_FSIZE` on darwin. Either
   add a launcher shim or drop those limits.
6. **Paths and machines.**
   - Capture manifests use POSIX path semantics, so restrict drop roots to
     POSIX until a path-flavor migration exists (`internal/sourcepath`,
     portability.md "Recorded paths").
   - Our `imp_…` host IDs break "one host is a Mac and OS user"
     ([channel-and-machine.md](../channel-and-machine.md)). Map remote
     machines onto the machine and execution-machine dimensions instead.
7. **`ssh_pull.py` gaps:**
   - Codex `.jsonl.zst` rollouts;
   - `~/.config/claude` and other Claude profiles;
   - `CLAUDE_CONFIG_DIR` and `CODEX_HOME`;
   - raw `.backup` of a state database ignores `CaptureTables`.
8. **Smaller items.**
   - Recover library must reset plugin trigger and run state and start
     plugins paused.
   - Source discovery ([configuration.md](../configuration.md), "Look in the
     usual places") forbids unasked reads, which matches importer consent.
   - Capture-based transfers must carry Ask Pharos exclusions
     (`ask_native_sessions`).
   - Usage records lack the labeling ledger's `partial` status.
   - "Automation" already names a message sender (`automation:<name>`), so
     labeling usage needs a usage kind, not a new channel.
   - "Cost at today's prices" was removed (#706).
   - Use workspace, agent app and the rest of
     [ui-vocabulary.md](../ui-vocabulary.md). "Work item" in the plugin docs
     means workspace.

## Questions to settle with the owner

1. **Pull files, or run Pharos's collector remotely?** Raw pulls
   (`ssh_pull.py`) work on any box today. The alternative is to run
   `pharos capture` plus `capture-export` near the source and fetch the
   verified envelope. That is the direction portability.md describes, but it
   needs a Pharos CLI build for Linux (and later Windows), which is a separate
   task. One answer: raw pull now, envelopes as the target.
2. **Buckets.** Something on the portable machine has to write the bucket,
   for example a small push agent that runs `capture-export` and uploads. A
   bucket importer then verifies envelopes and adopts them. Decide where that
   agent comes from and how bucket credentials appear at consent
   (`fetches_from`).
3. **Sandboxes are ephemeral.** Should they push at exit, or be pulled while
   alive? How should many short-lived machines appear: one machine each, or
   one "sandbox" machine with execution machines beneath it?
4. **The same conversation captured twice,** natively and through an
   importer, should stay one conversation with two copies. Check this against
   the provider registry's identity rules.

## Shared with the export thread

Manifest, consent, installation tables, triggers, limits, supervisor and
operation kinds are shared; keep `contracts/plugins` compatible with the
export work. The export thread is replacing per-field consent with sharing
tiers, which apply to exports only: an importer's consent describes its
sources and what it produces.

## Checks

```sh
python3 -m unittest discover -s contracts/plugins
python3 -m unittest discover -s importers
```

Merge master with `tools/update-branch.py` (Node 24: `nvm use`). Before any
service code, read `CLAUDE.md`'s testing and portability rules.
