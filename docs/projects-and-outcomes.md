# Projects and outcomes

Pharos is used for work in git repositories and for work in folders without
git: writing, research, data work, scratch directories, and chat exports that
have no working directory at all. Everything below follows one rule:
**git is enrichment, never a gate.** Every page, dashboard, filter, card and
MCP tool works for a library with zero repositories. Where git data exists,
it adds detail.

This document has three parts:

- the project rule (implemented);
- the outcome model and its evidence tiers (designed; see
  [Delivery](#delivery) for what is built);
- a map of what Pharos already does with git, with corrections to earlier
  assumptions.

## The project rule

Every workspace belongs to exactly one **project**, chosen in this order:

1. **The repository**, when the workspace's recorded working directory is in a
   git checkout. Repository identity, merges and aliases are unchanged
   (`repository_identity.go`, `repository_merge.go`).
2. **The repository a folder later became.** Suppose a workspace has no
   repository, and its folder is (or is inside) a checkout location of
   exactly one known repository. Then it joins that repository's project.
   This covers a folder that later became a repository: its earlier
   sessions join the repository project as soon as Pharos sees a session in
   the new checkout. It also covers a session recorded on another Mac in a
   checkout this library knows.
3. **The working folder.** The location is normalized as a folder key:
   - The home directory is shown as `~`. For `/Users/<name>/…` and
     `/home/<name>/…` that applies on any Mac, not only the current one.
   - Temporary directories (`/tmp`, `/private/tmp`, `/var/folders`) form
     one project, `/tmp`. Otherwise each throwaway directory would be a
     project of its own.
4. **No project**, for a workspace with no working directory, such as a
   ChatGPT export.

The data keeps the three states apart. `project_kind` is `repository`,
`folder` or `none`. `project_name` is empty only for `none`, and the UI
shows that as "No project".

A folder can also contain a repository nested below it. Only the folder
itself, or a folder inside a checkout, joins a repository. A repository at the
home directory (or at `/`) is joined only by the exact folder, never by
everything under it. Otherwise a home directory under git would swallow
every folder project.

### Identity of a folder project

A folder project has no identity beyond its home-relative path. Moving or
renaming the folder starts a new project, and the old project keeps the
sessions recorded there.

This was decided deliberately. A plain folder carries no evidence that two
paths are the same work: no root commit, no remote and no ID file. Pharos must
not write marker files into the user's folders. Guessing from file names
would merge unrelated folders. The home-relative key has two good
properties:

- the same folder layout synced across Macs (for example `~/Documents/notes`)
  is one project;
- the key is readable as a name.

A repository project keeps the repository's own identity, which does survive
moves (remote, root commit, forge ID).

### Where it is stored

| Column | Meaning |
|---|---|
| `workspaces.project_folder` | The normalized folder key of `location` (empty without one). |
| `workspaces.folder_repository_id` | For a workspace without `repository_id`, the one repository whose checkout holds its folder. |

These columns are derived and are written only by ingest and maintenance,
never by a request:

- Ingest sets them for each workspace it writes.
- When a repository gains checkout locations, workspaces in those folders
  are resolved again.
- A repository merge moves `folder_repository_id` to the survivor.
- Capture recovery, which restores repositories without that writer,
  resolves folder workspaces again when it changes any checkout.
- A build from before projects can still write to the catalog (the schema
  version is unchanged), leaving the columns missing or stale. Each build's
  first open derives them for every workspace, and the identities stage of
  the Repositories pass does again while it runs, writing only rows that
  differ; with none, it takes no write lock.

Readers use `projectJoin`/`projectNameSQL` (`projects.go`). They join
`repositories` on `COALESCE(repository_id, folder_repository_id)` and derive
`project_id` (`repository:<id>` or `folder:<key>`), `project_name` and
`project_kind`.

### Where projects appear

- **Library:** a Project column and filter, with Repository kept as a git
  detail.
- **Analyze:** a "Project" breakdown in every catalog that had
  "Repository". Links that name `repository_name` read as the project
  breakdown (the catalog's `legacy` mapping), so saved links and card
  destinations keep working.
- **Storage:** a By project tab.
- **MCP:** `list_projects` lists repositories and folders alike, with the
  count of conversations without a project; `list_repositories` is kept
  unchanged as the repository-only view. `search_conversations`,
  `search_work`, `search_messages`, `search_text` and `query_statistics`
  take a `project` filter, and `query_statistics` groups by `project`.
  `trace_worktree` and `list_findings` stay repository-only: findings are
  computed per repository.

## Evidence tiers

Each outcome fact is labelled with the tier it came from.

| Tier | Source | Who gets it |
|---|---|---|
| 1. Transcript | Tool calls and their results in the archived transcript. | Everyone. |
| 2. Local git | Read-only git commands in checkouts this Mac recorded. | Repositories on this Mac. |
| 3. Forge | Pull request state from a configured forge integration. | Not available yet: see [Corrections](#corrections-to-the-brief). |

### Tier 1: from the transcript

A **session** is a workspace, a Library row: a Claude Code or Codex session,
a Conductor workspace. Its delegated agents' calls are its own.

- **Files changed.** Each file a successful edit call changed, read from
  `tool_calls` (Claude Code's Edit, Write and MultiEdit, Codex's
  `apply_patch`, Antigravity's edits). Files in temporary directories and
  the agent's own home (plans, memory) are left out. A file in a repository
  is identified by repository and repository-relative path, so every
  checkout and worktree of a repository shares it; any other file by its
  absolute path. Lines added and removed come from the ledger; Antigravity
  records none, so its counts are empty ("Not recorded"), not zero. Edits a
  shell command makes (`sed -i`, redirections) are not counted.
- **Verified after the last edit.** How the last command whose
  `command_category` is `test` or `build` after the session's last edit
  ended: passed, failed, or no result; "not run" when none ran. The ledger
  has no lint category, so linters count only when they run as a build.
- **Rework.** Which of the lines this session added and kept a later
  session in any harness removed. A later session counts when it edited one
  of the same files after this session's last edit of it, starting within N
  days of it. N is the library setting `outcome_rework_days`, 14 by default.
  The session is "Reworked" when later sessions removed at least half of
  those lines. Lines come from each edit's input as the transcript holds it:
  an Edit's old and new text, a Write's whole file, a patch's `-` and `+`
  lines. A line removed and added again (moved or reindented) is unchanged,
  and only lines of 8 or more characters, trimmed, count, so a brace or a
  blank line says nothing. A later Write removes whatever of the session's
  lines the new file no longer has. A mirror of another workspace
  (`tool_mirror_workspaces`, such as a Conductor workspace's copy of its
  Claude session) repeats that workspace's edits and is never rework. A
  session whose edits cannot be read line by line (an input the transcript
  did not keep) is not judged, and its "why" says so. Editing the same files
  again is not rework: continued work on a file usually adds to it. The
  calibration is under [Rework calibration](#rework-calibration).
- **Commit, branch and PR evidence in tool output.** From the output of a
  successful `git commit` (`[main 1a2b3c4] …`, giving hash and branch),
  `git push` (`a..b  main -> main`, `* [new branch]`), and `gh pr create`
  (the pull request URL). Only these commands' output is read. A hash here
  is Tier 1 evidence: it is stored, but "Committed" waits for local Git to
  confirm it (Tier 2).

Storage: `session_files` (one row per session and file: path, repository,
first and last edit, edits, lines, and fingerprints of the lines it added
and kept and of the lines it removed) and `session_outcomes` (one row per
session: the facts above, the rework judgement and the window it used).
They are derived in the tools stage, after the tool ledger, for the
sessions whose calls changed (`session_outcome_dirty`, filled by triggers on
`tool_calls` and `workspaces`). Each pass then judges rework again, once,
for those sessions and every earlier session sharing a file with them,
before or after the change, whose window reaches it
(`session_rework_dirty`). A new rule version or rework window derives every
session again. The label is read
from the stored facts, so a change stops being too recent to judge with no
write. The Library shows the label (`outcome_label`, with `outcome_why`),
files changed, lines, verification, recorded commits and reworked files.

A line fingerprint is the first 6 bytes of the SHA-256 of the trimmed line:
enough to compare sets of lines without collisions that matter, and not the
line. Pharos stores no diff and no file content for outcomes; the
transcript it was read from already holds the text.

### Tier 2: from local git (repositories only, read-only)

- **Commits linked to the session.** A link comes either from a hash in the
  transcript (direct, confidence 1.0) or by inference: the commit's author
  time falls in the session window (plus a short grace period), on the
  recorded branch, and it touches files the session edited. When several
  sessions overlap a commit, the commit goes to the session with the
  tightest window that contains it (CodeBurn's rule), at a lower
  confidence. Every link stores its evidence, as `work_pr_links` does.
- **Mainline.** Whether those commits reached mainline. This reuses
  `git_integration.go`, applied to linked commits rather than only to
  `workspaces.head_ref`.
- **Reverts.** A later commit is a revert when its message is
  `Revert "…"` with `This reverts commit <hash>`, or when its diff is the
  exact inverse of the linked commit's diff.
- **Per-commit numstat.**
- **Git AI notes** (`refs/notes/ai`), when present, are read as Tier 2
  evidence. They are never written.

### Tier 3: from the forge

PR state, merge and review outcome. This tier shows only when a forge
integration is configured, and today none exists (see the corrections). The
outcome model is built so that a missing Tier 3 never changes a label:
landing is decided by Tier 2 mainline evidence.

## Outcome labels

Each session gets one label. Each label has a "why": the evidence that
decided it and its tier.

| Label | Git meaning | Non-git meaning |
|---|---|---|
| Landed | Its commits reached mainline. | Not used. |
| Committed | Committed, not on mainline (yet). | Not used. |
| Changed and kept | Edits with no commit evidence, and no later rework within N days. | Edits with no later rework within N days. |
| Changed, too recent | Edits less than N days ago, so rework cannot be judged yet. | Same. |
| Reworked | Its changes were reverted, or later sessions removed at least half of the lines it added within N days. | Later sessions removed at least half of the lines it added within N days. |
| No changes | The session changed no files. Research, review and questions are normal. | Same. |
| Unknown | Not enough evidence, for example a harness that records no tool calls. | Same. |

"Changed, too recent" is a revision of the brief's table. Without it, every
session from the last two weeks would read as "kept" before rework could
have happened.

The constraints:

- No label implies the session was bad.
- "No changes" is neutral.
- "Committed" and "Landed" appear only with Tier 2 evidence.

Each git-only fact has three distinct states, both in data and in the UI:

| State | Meaning | Shown as |
|---|---|---|
| Not applicable | The project is a folder or none. | "Not a repository" / "No git data" |
| Not checked | A repository, but Pharos could not inspect it. Reasons: no `git` binary, shallow clone, checkout deleted or recorded on another Mac, repository over the size budget, or a timeout. | "Not checked", with the reason |
| None | Inspected, and nothing was found. | "Not on mainline", "No commits" |

A "Not checked" state is never negative evidence.

## Commit-linking rules and confidence

| Evidence | Confidence |
|---|---|
| Hash printed by `git commit` in the session's own tool output, and the commit exists locally | 1.0 |
| Hash printed by `git commit`, but the commit is not in any local repository | 0.9 (Tier 1 only) |
| Inferred: author time in the window, on the recorded branch, touching the session's edited files, and the only containing session | 0.7 |
| Inferred, and the tightest of several overlapping sessions | 0.5 |
| Inferred from time alone (no file overlap) | Not linked |

## Rework calibration

Measured on 2026-11-03 against a copy of a real library (the euclid backup
of 2026-11-01, opened immutable and read-only): 139,310 successful edits in
9,210 sessions, 4,060 of them with edits outside mirrors.

**The first rule failed.** The rule as first written counted a session as
"Reworked" when more than half of its files were edited again by a later
session within N days, or when one later session removed at least half as
many lines as it added. It marked 61.9% of the sessions with edits
"Reworked" at N=14 (52% to 73% across N of 7 to 30 and shares of a quarter
to three quarters). The files criterion alone marked 54.5%, the lines
criterion 44.7%. Re-editing is how work continues, not a sign it failed:

- 47.1% of the files a session edited were edited again by a later session.
  Of those, 53% were edited again within a day, 80.5% within 7 days, 86.8%
  within 14 and 91.9% within 30.
- A few files draw most of the work: one file was edited by 619 sessions,
  and 92 files by 20 or more.
- 196,453 pairs of edits by different sessions came one after the other,
  against 2,716 from sessions running in parallel.
- A later session's removed lines are not this session's lines: per file
  edited again, removed over added was 0.05 at the 25th percentile, 0.25 at
  the median, 1.0 at the 75th and 3.25 at the 90th.

Counting files and lines cannot tell continued work from rework, and a
label on most work that reads as failure breaks "no label implies the
session was bad".

**The rule shipped: line survival.** Rework is now the share of the lines a
session added and kept that later sessions removed. On the same library,
4,002 sessions added lines that count:

| Window | Any line removed later | Removed ≥ 25% | Removed ≥ 50% | Removed ≥ 75% |
|---|---|---|---|---|
| 7 days | 42.2% | 7.8% | 3.7% | 2.2% |
| 14 days | 45.7% | 9.7% | 4.8% | 2.8% |
| 30 days | 48.5% | 11.2% | 5.7% | 3.3% |

The median session loses none of its lines and the 90th percentile 24% at
14 days. 75% of sessions had a file edited again within 14 days, and for
39% of those no later session removed any of their lines. Small sessions
are reworked more often: 13.0% of sessions adding 1 to 9 lines, 5.7% of
10 to 49, 2.1% of 50 or more. That is expected, since one replaced line is
a whole small change.

N stays 14: going from 14 to 30 days adds less than one session in a
hundred, and at 7 days a change made before a weekend and replaced the
week after would read as kept. The half-share stays because it is the
plain reading of "most of it was redone", and it marks about one session
in twenty.

Limits:

- A later Write of a whole file counts every line of the session's that the
  new file lacks. When the session itself wrote the file whole, all the
  file's lines count as its own, including lines from earlier sessions.
- Edits made by shell commands (`sed -i`, redirections) are not read.
- A revert by Git is Tier 2 and comes in PR 3.

**The shipped code on the same library.** The Go probe
`outcomes_probe_test.go` (`-tags probe`) derives outcomes from an extract of
the backup (the command is in its header). Of 53,328 sessions, it labelled
40,774 "No changes", 6,252 "Changed and kept", 248 "Reworked", 88 "Changed,
too recent" and 16 "Unknown" (chat exports). That is 3.8% of the 6,588
sessions with edits, counting Conductor workspaces and sessions outside
mirrors that the first measurement left out. 1,883 of them lost at least one
line to a later session. Every session's edits could be read line by line.
The fingerprints take 6.0 MB for 52,244 session files.

Speed: deriving every session took 27 seconds, and deriving the 10 latest
sessions again took 36 ms. A first version judged rework inside each batch,
so a file edited by hundreds of sessions was judged again by every batch
that touched it. Batches grew from 56 ms to over 700 ms, and the full pass
ran for more than 75 minutes before it was stopped. Facts are now derived
first and rework judged once per marked session (`session_rework_dirty`).

## What Pharos never does with git

- It never runs `git fetch` or `git pull`, writes notes, installs hooks, or
  changes config, refs or the working tree. Every command is read-only and
  sets `GIT_NO_LAZY_FETCH=1`.
- It never runs git in a path another Mac recorded
  (`workspace_sightings.host_id`), because that path may be an unrelated
  checkout here.
- It never runs git on a request. Pages and MCP tools read stored facts.
  Git runs in background stages with timeouts and cancellation, and caches
  by repository tip.
- It never stores diffs or file contents. It stores hashes, paths, counts
  and timestamps.
- It never makes a network call for git data.

## If you don't use git

If none of your sessions run in a repository, you get:

- **Projects:** your working folders, shown as `~/…`. Chat exports with no
  folder are under "No project". Every Library filter, Analyze breakdown,
  Storage tab and MCP tool that groups by project works for you.
- **Outcomes:** every session gets a label from its own transcript: Changed
  and kept, Changed (too recent to tell), Reworked, No changes, or
  Unknown. Each label has a "why" listing the tool calls that decided it.
- **No empty git panels.** Git-only breakdowns (branch, commit, mainline)
  and Tier 2 and 3 measures are disabled with the reason "No sessions in a
  git repository". You never see an empty chart, "0 PRs", "0% merged" or a
  red state. Where the Library has a git column, a folder session shows
  "Not a repository", not 0.
- **File history:** the timeline of every session that touched a path,
  built from transcripts.

## Map of existing git behaviour

### Repository identity

- `repositoryAt` (`repository_location.go`) names a workspace's repository
  in this order:
  1. the origin remote (on this Mac, `git config remote.origin.url`; from
     another Mac, only a remote the transcript recorded);
  2. the checkout root's name (`git rev-parse --git-common-dir`);
  3. the Conductor repository directory (`conductor/workspaces/<dir>`).

  It returns nothing otherwise.
- **A working directory that is not a git checkout** gets no repository
  (`repository_id` NULL). The one exception is a Conductor workspace, which
  is named after its Conductor directory even without git.
- **A session with no working directory** (ChatGPT export) has no location
  and no repository.
- **A session recorded on another Mac** asks no git questions
  (`repositoryFromName`). Without a recorded remote, it has a repository
  only through the Conductor layout.
- The tool ledger resolves each file path against known checkouts
  (`tool_paths.go`). A path in no repository has scope `temp`, `agent_home`
  or `external`. For a workspace without a repository, its own location
  still serves as a root, so paths under it are relative to it.

### Changes

`change_sets` and `change_files` are written by `upsertChanges`.

- **Conductor** writes `attempted` changes from tool calls (`complete:false`),
  and a `checkpointed` change from `git show --name-status` of its archive
  commit.
- **Canonical and TL1 exports** pass through whatever the file says.
- **Claude Code, Codex, Antigravity, TL1 and ChatGPT** write none.

No code writes `proposed` or `merged`.

### Pull requests

`pull_requests` and `work_pr_links` come from:

- **Conductor:** PR URLs in message text, stored as `created` with
  confidence 1.0.
- **TL1:** candidate PR fields.
- **Canonical exports.**

Claude Code and Codex produce none. The work page also discovers PR URLs in
messages at read time (`discovered_prs`), without storing them.

### Mainline integration

`git_integration.go` checks `workspaces.head_ref` against local
`origin/HEAD` (then `origin/main`, then `origin/master`) with
`merge-base --is-ancestor`. It finds the first-parent commit that brought the
head in, and writes `main_merge_*`. Its properties:

- It uses local objects only (`GIT_NO_LAZY_FETCH=1`), with a 5-second
  timeout per command.
- A failure stops lookups in that repository for the run and is never
  stored.
- It writes negatives (`git_main_negative_lookups`) only when the check
  was conclusive and the clone is not shallow.
- It runs only in paths this host recorded (`workspace_sightings.host_id`).
- It runs in background stages, never on a request.
- It leaves squash merges unclassified.

`head_ref` is set by Conductor (the archive commit), Codex
(`session_meta.git.commit_hash`) and TL1. Claude Code sets none.

### Files

- Library file search (`find_kind=file`), `trace?file=` and the MCP `file`
  filters read `change_files` only. A change is attributed to a
  conversation only when the workspace has one conversation, its work item
  matches, or its time falls within one conversation.
- `tool_calls` holds `file_path`, `path_absolute`, `repo_path`,
  `lines_added` and `lines_removed`, but file search does not read it.

### Transcript git facts

- **Claude Code** records `cwd` and `gitBranch` on every event. Pharos keeps
  the last non-empty `cwd` as the workspace location and the last branch
  as `workspaces.branch`. It stores no per-message branch or `cwd`; the
  ledger carries `cwd` forward only to resolve tool paths.
- **Codex** keeps `cwd`, `git.repository_url` (repository identity),
  `git.branch` and `git.commit_hash` (as `head_ref`).
- **Library fields:**
  - `branch_count` is 0 or 1 per workspace.
  - `pr_count`, `pr_numbers` and `pr_details` come from `work_pr_links`.
  - `main_merge_title` comes from `workspaces`.

### Corrections to the brief

- **Tier 3:** there is no GitHub enrichment. `github_token`/`GITHUB_TOKEN` is
  read into the configuration and excluded from backups, and nothing else
  uses it. `docs/configuration.md` describes it, but no code makes a GitHub
  request. (`gh api` is used only to resolve repository IDs for identity.)
  Tier 3 is therefore unavailable, and this work adds no network calls.
- **Mainline coverage:** `git_integration.go` checks only a workspace's
  `head_ref`. Claude Code sessions have none, so they never get a mainline
  answer. For Codex, `head_ref` is the commit the session *started from*,
  so a "landed" answer says only that the session began on a mainline
  commit, not that its work landed. Tier 2's commit links replace this.
- **Changes:** Claude Code, Codex and Antigravity sessions get no
  `change_sets`, so Library file search never finds them. That is a gap for
  git and non-git users alike, and Tier 1 closes it from `tool_calls`.
- **Branch:** only the last branch per workspace is stored. Spend by branch
  needs per-message branches, which Tier 1 extracts from Claude Code
  events.
- **Transcript commits and PR URLs:** nothing stores commit hashes from
  `git commit` output. PR URLs are stored only for Conductor.
- **Exports:** there is no Claude.ai export source; ChatGPT is the only chat
  export.
- **`session_shape`:** neither `docs/session-shape.md` nor a `session_shape`
  dataset exists yet (2026-11-03).
- **Docs:** `docs/technical-overview.md` has no "Past Work" or "Work detail"
  sections. Its Library paragraph covers both.

## Where non-git users had a worse experience

| Place | Before | Now |
|---|---|---|
| Library, Analyze, Storage and MCP grouping | Every folder session fell into "No repository" (Analyze, Storage) or was blank (Library). `list_repositories` counted them only as `conversations_without_repository`. | Each folder is a project. Chat exports are "No project". |
| MCP `repository` filters | A folder could not be named, and passing a path returned an error. | `project` filters accept a folder key or path. |
| Library `branch_count`, `pr_count` | 0 for folder sessions, the same as a git session with no branch or PR. | (Outcome PR) Shown as "Not a repository". |
| File search | Found only Conductor sessions. | (Tier 1 PR) Every harness that records tool calls. |

## Delivery

1. **Projects for everyone:** the project rule, its storage and backfill,
   Library, Analyze, Storage and MCP, with legacy mapping and the parity
   fixture.
2. **Tier 1 outcome facts and the outcome labels.**
3. **Tier 2 local git linking, reverts and mainline.**
4. **Outcome views in Analyze, plus spend by branch.**
5. **File history timeline and MCP.**

Handoffs between parts are in `.context/projects-and-outcomes-handoff.md`
(not committed).

## References

These open-source projects informed the definitions. None of their code is
ported yet. Any port gets a file header naming the repository, commit and
license, plus a `THIRD_PARTY_NOTICES.md` entry.

| Project | Used for | License |
|---|---|---|
| [CodeBurn yield](https://github.com/getagentseal/codeburn/blob/4169d12589ddd8296247c0cc8ce5a0ff01dda5e7/docs/yield.md) | Tightest-window matching, revert detection, squash trees | MIT |
| [CodeBurn by branch](https://github.com/getagentseal/codeburn/blob/4169d12589ddd8296247c0cc8ce5a0ff01dda5e7/docs/by-branch.md) | Mid-session branch changes, coverage split | MIT |
| [AgentsView git](https://github.com/kenn-io/agentsview/tree/10667a619ac1150366e84e7f294b8bc0181156a4/internal/db/git) | `git log --numstat` totals | MIT |
| [ctx blame](https://github.com/ctxrs/ctx/blob/103ee33ecb3c04d82efffbdcd28a4d834f9632cf/docs/blame.md) | File history to session | Apache-2.0 |
| [Git AI](https://github.com/git-ai-project/git-ai) | Reading AI attribution notes | Apache-2.0 |

None of them handles a folder without git. Tier 1 has no precedent.
