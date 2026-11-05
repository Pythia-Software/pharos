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
  `apply_patch`, Antigravity's edits). The ledger keeps one path per call,
  so a patch's files are read from the patch itself and placed the way the
  ledger places paths, each on its own: a patch whose first file is scratch
  still changes its other files. A patch in a Codex code-mode script, where
  the script holds it as a string literal, changes the files it names; the
  ledger (from `tools-v11`) records its first file too. An older Codex
  patch that lists its files without their lines changes those files.
  Files in temporary directories and the agent's own home (plans, memory)
  are left out. A file in a repository
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
  those lines.
  - Lines come from each edit's input as the transcript holds it: an Edit's
    old and new text, a Write's whole file, a patch's `-` and `+` lines. A
    patch that deletes a file removes all its lines, and a later Write
    removes whatever of the session's lines the new file no longer has.
  - An Edit's text (and Antigravity's, batches included) can start or end
    inside a line, and part of a line never matches the whole line another
    session adds or removes. So a changed first or last line of a
    replacement is left out, unless a newline in the text shows where it
    starts or ends; the lines between are whole. On the real library 2% of
    Claude Code's edits are a single line and 18% change their first or
    last line. A file only such edits changed is not judged.
  - A line removed and added again (moved or reindented) is unchanged, and
    only lines of 8 or more characters, trimmed, count, so a brace or a
    blank line says nothing.
  - A mirror of another workspace (`tool_mirror_workspaces`, such as a
    Conductor workspace's copy of its Claude session) repeats that
    workspace's edits and is never rework. When a workspace becomes or stops
    being a mirror, the sessions sharing its files are judged again.
  - A session whose edits cannot be read line by line (an input the
    transcript did not keep, or only parts of lines) is not judged, and its
    "why" says so.

  Editing the same files again is not rework: continued work on a file
  usually adds to it. The calibration is under
  [Rework calibration](#rework-calibration).
- **Commit, branch and PR evidence in tool output.** From the output of a
  successful `git commit` (`[main 1a2b3c4] …`, giving hash and branch),
  `git push` (`a..b  main -> main`, `* [new branch]`), and `gh pr create`
  (the pull request URL), as the model read it, out of the stored result's
  envelope. Only these commands' output is read. A hash here
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
`session_outcomes` is a query cache dependency, so publishing outcomes
renews cached Library rows, filters and metrics.

A line fingerprint is the first 6 bytes of the SHA-256 of the trimmed line:
enough to compare sets of lines without collisions that matter, and not the
line. Pharos stores no diff and no file content for outcomes; the
transcript it was read from already holds the text.

### Tier 2: from local git (repositories only, read-only)

`internal/archive/outcome_git.go` asks local git about the repositories in
which sessions changed files or printed a commit, using only checkouts this
Mac recorded that still exist.

- **When.** In background stages only: after the tool summaries in
  incremental recovery, and as the "linking commits" phase of a library
  update. It never runs in `ensureToolRollup`, which a page view can start.
  Before each repository it yields to interactive queries, and git runs
  under `nice -n 10` with a 60-second timeout for the log and 10 seconds for
  everything else.
- **Cache.** A repository's scan key is its refs (heads, remotes, tags and
  `refs/notes/ai`) and its worktrees' detached HEADs, plus its sessions'
  edit windows, files (with each file's first and last edit and its kept
  line fingerprints) and printed commits. An unchanged key runs no `git log` and writes nothing, so the
  cached views stay valid. A scan that could not start is keyed by its
  reason; one that timed out is tried again when the repository changes or
  on the next day.
- **What it reads.** `git log --branches --remotes --tags --no-renames
  --numstat`, plus the worktrees' detached HEADs (`git worktree list
  --porcelain`), since a day before the earliest session edit or printed
  commit: each commit's hash, parent count, author and commit times, and
  lines per file. For a commit several sessions could have made, also its
  diff and the HEAD reflog of the candidates' checkouts: an entry of a
  commit made there (a commit, cherry-pick, revert or a commit a rebase
  wrote), not one checked out, reset to or a rebase's start, finish or
  abort. The diff's added lines are fingerprinted and compared in memory,
  never stored. Every diff is read with `--no-textconv --no-ext-diff`: a
  repository's attributes can name a converter or diff program, which
  would run, and a converter with a cache writes notes.
- **Commits linked to the session.** A link comes either from a hash the
  session's own `git commit` printed (direct, confidence 1.0) or by
  inference: the commit is not a merge, its author time falls in the
  session's edit window (plus an hour of grace), and it touches a file the
  session had edited by then. When several sessions qualify, the commit goes
  to the one whose checkout's reflog made it (0.9), else to the one whose
  kept lines it adds at least twice as many of as any other (0.8),
  comparing line fingerprints in memory; else to the one still editing its files, or that stopped nearest
  before it, then to the tightest window (CodeBurn's rule), at 0.5; a tie
  links nothing. A 0.5 link is kept as a candidate but counts toward no
  session's commits or label (see [Commit-linking
  calibration](#commit-linking-calibration)). Every link stores its
  evidence: the printed hash, or the author time, the window, the shared
  files and the number of candidates.
- **Mainline.** Mainline is `origin/HEAD`, else `origin/main` or
  `origin/master`, else a local `main` or `master`. A linked commit landed
  when mainline reaches it, or when a mainline commit since has the same
  `git patch-id --stable` (a rebase, cherry-pick or one-commit squash). A
  cherry-pick keeps the author time, so both copies link to the session;
  the change is linked once, as the copy, with the stronger evidence of
  the two (a printed hash still counts when only inference linked the
  copy). Another session's link to the original stays. In a shallow clone a commit
  mainline does not reach is unknown, not "not landed", and so is a commit
  whose patch could not be read.
- **Reverts.** A later commit reverts a linked one when its message says
  `This reverts commit <hash>`, or when it changes the same files with the
  added and removed counts swapped and its patch-id equals the linked
  commit's inverse patch-id (read with `--no-prefix`, since `-R` swaps the
  `a/` and `b/` prefixes).
- **Per-commit numstat** is stored as the commit's file count and line
  totals.
- **Git AI notes** (`refs/notes/ai`) are listed and recorded as a flag on
  the commit. They are never written.

What it stores: `git_commits` (one row per linked commit: hash, times,
parents, counts, mainline and revert evidence), `session_commit_links`
(method, confidence and evidence per link), `session_git` (each session's
state and counts) and `git_repository_scans` (each repository's scan key
and state on this host). The Library reads them as `git_check`,
`git_check_reason`, `linked_commits` and `landed_commits`, and the outcome
label as Landed, Committed or, for a revert, Reworked.

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
| Not applicable | The project is a folder or none, or the session changed no file in its repository. | "Not a repository" / "No git data" / "Not applicable" |
| Not checked | A repository, but Pharos could not inspect it. Reasons: no `git` binary, checkout deleted or recorded only on another Mac, a timeout, or git failing to read it. | "Not checked", with the reason |
| None | Inspected, and nothing was found. | "Not on mainline", "No commits" |

A "Not checked" state is never negative evidence.

### Analyze → Outcomes

The Outcomes dashboard reads the `outcomes` dataset (`outcomes_table.go`):
one row per session the Library lists, with mirrors folded the same way.
A row has the session's outcome label, its evidence tier, project, branch,
priced usage, main model and Tier 1 facts.

- **When a session counts.** At its start: its first usage hour, else its
  first edit, else its last activity. Its whole cost counts then.
- **Cost** is priced as the Library prices it, from the same usage ledger.
  So a session's cost here equals its Library cost.
- **Main model.** The model that took most of the session's cost, or of its
  tokens if none was priced. Its provider is the main provider.
- **Evidence tier.** The strongest evidence the label rests on:
  - Local Git (Tier 2), where local Git checked the session;
  - Transcript (Tier 1), where the session recorded tool calls;
  - No evidence, otherwise.
- **Branch.** Only for a session in a repository. In the chart, the Branch
  breakdown counts repository sessions only; a repository session without
  one is "No branch". In the table, a folder session's branch reads "Not a
  repository" and a session without a folder "No git data".

The measures:

- sessions, cost and tokens;
- files changed, and lines added and removed;
- rework rate: lines later sessions removed over lines kept, pooled over
  every session's lines rather than averaged per session;
- cost per kept change: all the sessions' cost over the sessions labelled
  Landed, Committed, or Changed and kept. Sessions that changed nothing and
  reworked ones count in the cost, because they are part of what a kept
  change cost;
- kept share: kept sessions over all sessions.

The page opens on spend by outcome label over time. Below the chart are
three panels:

- rework rate by project;
- cost per kept change by main model;
- evidence coverage: how many sessions reached each tier.

The tiers a library reached are read over the whole library, not the
filters. A breakdown that needs a tier no session reached is offered
disabled with its reason, and never drawn empty. Today that is the local
Git check, whose reason reads "No session has local Git evidence: local Git
checks only repositories on this Mac". There is no Tier 3 breakdown: Pharos
holds no forge data for outcomes yet.

### Spend by branch

Analyze → Spend by Branch shows what sessions in Git repositories spent on
each branch. It follows CodeBurn's by-branch lens (see References).

- **Turn by turn.** Claude Code records the checkout's branch (`gitBranch`)
  on each event. Each turn is on the last branch recorded at or before it,
  so a session that switches branches spends on each in turn. A response
  whose usage is repeated on each content block counts once. Claude Code
  records `gitBranch` on its responses as well as its prompts (all 10,166
  usage events in a sample of the library had one), so each turn's own
  record places it, and a switch recorded on a prompt is recorded again on
  the next response.
- **How it is stored.** The usage ledger is hourly, per session and model.
  `agent_session_branch_shares` records the tokens each branch's turns used
  in each of those hours. The `branch_usage` dataset splits each priced
  ledger row among branches by those tokens and keeps its hour, so hourly
  trends and time ranges place the spend when it happened. Only branch
  names and token counts are stored, both already in the transcript.
- **When it runs.** A background stage (`refreshBranchUsage`,
  `branch_usage.go`) runs after commit linking and never on a request. It
  reads only the events Pharos kept whole, which include every usage event.
  It skips a conversation whose sessions' usage has not changed since it
  was last read (`branch_usage_scans`). When the rules of outcomes, commit
  linking or spend by branch change, opening the library schedules the
  stage (`scheduleToolDerivations`), so a library with nothing new to index
  still derives again.
- **Session-level branches.** Codex records one branch per session, and
  Conductor one per workspace; their messages carry no per-turn branch. A
  session without per-turn branches is on that branch for all its usage.
- **Coverage split.** It sums to the repository sessions' whole spend:

  | Coverage | Meaning |
  |---|---|
  | On a branch | Usage on a recorded branch, per turn or per session. |
  | Unknown branch | In a session that recorded a branch: usage before its first branch, on a detached HEAD, or not placed on a turn. Never relabelled as `main`. |
  | No branch data | A session that recorded no branch. |

  "Unknown branch" is wider than CodeBurn's "unknown, before first branch".
  It also covers a detached HEAD and usage the transcript reports for the
  whole session rather than a turn, such as a Claude Code cost-state
  remainder. Neither can honestly be given a branch.
- **Git sessions only.** Sessions in folders, or without a project, are not
  in the dataset. A library without Git shows a note on the page instead of
  an empty chart.
- **On a real library** (the 2026-11-01 backup, read immutable by
  `branch_usage_probe_test.go`):
  - It has 18,629 Claude-model conversations in repositories. Claude Code's
    own sessions recorded per-turn branches in 5,186 of 5,198. Conductor's
    5,020 use its workspace branch. TL1's 8,411 mostly have no branch data.
  - A first pass read 1.5 million kept events in 2 min 24 s; the slowest
    conversation took 1.1 s. A pass with nothing new runs only the
    change-detection query, which took 0.37 s.
  - Coverage, by tokens:

    | Coverage | Share |
    |---|---|
    | On a branch, per session | 85.0% |
    | On a branch, per turn | 13.0% |
    | Unknown branch | 0.1% |
    | No branch data | 1.9% |
- **Approximation.** Within one hour, a session's cost is split by tokens.
  If a session switched branches mid-hour, the two branches' token types
  (cache reads and output, for example) are priced as that hour's mix.

## Commit-linking rules and confidence

| Evidence | Confidence |
|---|---|
| Hash printed by `git commit` in the session's own tool output, and the commit exists locally | 1.0 |
| Hash printed by `git commit`, but the commit is not in the local repository | Not linked; it stays a Tier 1 fact (`recorded_commits`) |
| Inferred: author time in the edit window plus an hour, touching the session's edited files, not a merge, and the only containing session | 0.7 |
| Inferred, of several overlapping sessions, the one whose checkout's reflog made the commit | 0.9 |
| Inferred, of several, the one whose kept lines the commit adds at least twice as many of as any other | 0.8 |
| Inferred, of several, the one editing nearest the commit (then the tightest window) | 0.5, a candidate: stored with its evidence, never counted |
| Inferred, and tied for the tightest window | Not linked |
| Inferred from time alone (no file overlap) | Not linked |

Links of 0.7 and above count toward a session's linked and landed commits
and its label.

## Commit-linking calibration

Measured on 2026-11-04 against the same backup copy as the rework
calibration, with read-only git in this Mac's 47 recorded repositories
(`outcome_git_probe_test.go`).

- **Coverage.** 6,498 sessions were checked. 983 were not checked, each
  with its reason: git could not read 2 repositories (477 sessions), no
  checkout was recorded on this Mac (265), or the recorded checkouts are
  gone (241).
- **Speed.** A first pass over every repository took 2 m 11 s to 2 m 21 s,
  about 20 to 30 s of it reading the diffs of contested commits (1 m 53 s
  without). The largest, a
  TL1 experiment repository with about 4,100 linked commits, took 66 s of
  git. A pass with nothing changed took under 1.3 s and ran no `git log` or
  write.
- **Grace.** Of 787 commits whose session printed the hash and had edits,
  186 were authored during the edits, 313 within 10 minutes after the last
  edit, 140 within the hour, 48 within four hours, 87 later and 13 before
  the first edit. An hour of grace covers 81%. Four hours would add 6% and
  widen the overlap between sessions.
- **Transcript hash vs inference.** For each commit a session printed, the
  probe asked what inference alone would have linked:

  | Case | Commits |
  |---|---|
  | The printing session edited a shared file in the window: inference picked it | 916 of 971 (94%) |
  | ... the only candidate (0.7) | 392 of 392 |
  | ... of several, by the checkout that made it (0.9) | 13 of 13 |
  | ... of several, by the lines it adds (0.8) | 485 of 519 (93%) |
  | ... of several, nearest the commit (0.5, not counted) | 26 of 45 |
  | The printing session made no edits ("Create PR", "Review changes") | 730 |
  | Authored outside the printing session's window plus grace | 145 |
  | A merge commit (never inferred) | 77 |
  | Sharing no file the printing session edited | 34 |

  When the printing session made no edits, an inferred link goes to the
  session that did, which is the session whose work the commit holds.
- **Telling contested commits apart.** 577 of the 971 could have been made
  by several sessions. Each way of choosing among them, on its own:

  | Signal | Right | Wrong | Undecided |
  |---|---|---|---|
  | The tightest window (the brief's rule) | 59% of the 971 overall | | |
  | Nearest edit, then tightest window | 258 | 319 | 0 |
  | Lines the commit adds that the session kept, most of any | 512 | 43 | 22 |
  | ... at least twice the runner-up (used) | 498 | 34 | 45 |
  | ... at least five times the runner-up | 459 | 20 | 98 |
  | The checkout whose reflog made it | 13 | 0 | 564 |
  | The session's recorded branch holds it | 0 | 0 | 577 |

  A session that edited a file once, an hour earlier, has the tightest
  window but did not make the commit, so nearest edit comes first; it is
  still wrong more often than right, so a link it alone decides is a
  candidate and counts toward nothing. The lines a commit adds name its
  session 92% of the time and decide 96% of contested commits. Requiring
  twice the runner-up's lines raises that to 94% and still decides 92%;
  the wrong links that survive even five times look like one session
  committing another's work, which no margin removes. A minimum number of
  matching lines added nothing (at least five lines removed 4 more wrong
  links and 34 right ones). Reflogs
  are exact but rarely there: they expire after 90 days and go with a
  deleted worktree. The branch decided nothing, since recorded branches
  are deleted after merging or hold every older mainline commit, so it is
  not read.
- **Labels.** Of 7,187 sessions with changes: Landed 2,187, Committed 271,
  Changed and kept 4,415, Reworked 252 (9 counted commits were reverted),
  Changed (too recent) 62, with 40,175 No changes and 16 Unknown.

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
40,774 "No changes", 6,257 "Changed and kept", 244 "Reworked", 87 "Changed,
too recent" and 16 "Unknown" (chat exports). That is 3.7% of the 6,588
sessions with edits, counting Conductor workspaces and sessions outside
mirrors that the first measurement left out. 1,888 of them lost at least one
line to a later session. Every session's edits could be read line by line.
The fingerprints take 6.1 MB for 53,533 session files; 1,289 of those
files come from patches that changed more than one file.

Speed: deriving every session took 27 seconds, and deriving the 10 latest
sessions again took 45 ms. A first version judged rework inside each batch,
so a file edited by hundreds of sessions was judged again by every batch
that touched it. Batches grew from 56 ms to over 700 ms, and the full pass
ran for more than 75 minutes before it was stopped. Facts are now derived
first and rework judged once per marked session (`session_rework_dirty`).

## What Pharos never does with git

- It never runs `git fetch` or `git pull`, writes notes, installs hooks, or
  changes config, refs or the working tree. Every command is read-only and
  sets `GIT_NO_LAZY_FETCH=1`, and no diff runs a program the repository
  names (`--no-textconv --no-ext-diff`).
- It never runs git in a path another Mac recorded
  (`workspace_sightings.host_id`), because that path may be an unrelated
  checkout here.
- It never runs git on a request. Pages and MCP tools read stored facts.
  Git runs in background stages at low priority, with timeouts and
  cancellation, and caches by the repository's refs.
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
- **No empty git panels.** In Analyze → Outcomes, the local Git breakdown
  is disabled with the reason "No session has local Git evidence: local Git
  checks only repositories on this Mac". The Branch breakdown counts
  repository sessions only. Every panel draws from transcripts, and the
  coverage panel shows every session at Transcript or No evidence. You never
  see an empty chart, "0 PRs", "0% merged" or a red state. Where the Library
  or a query table has a git column, a folder session shows "Not a
  repository", not 0.
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
- **Linking on the recorded branch:** git does not record the branch a
  commit was made on, branches are usually deleted after merging, and
  Claude Code stores only a workspace's last branch. Inference uses the
  edit window and shared files instead, and the branch a printed commit
  names is kept only as evidence. Tried as a tie-breaker between sessions,
  the recorded branch decided none of 577 contested commits.
- **The tightest window:** on its own it picked the right session 59% of
  the time. Contested commits are decided first by the checkout that made
  them and the lines they add, and the window only orders what is left, as
  an uncounted candidate.
- **Shallow clones and size:** a shallow clone is not a reason to skip a
  repository. Only a mainline answer it cannot give is unknown. There is no
  size budget: the log is bounded by time (since the earliest session edit)
  and by its timeout.
- **Mainline without a remote:** a repository with no `origin` uses its
  local `main` or `master`, so local-only work can still land.
- **Squash merges:** a squash of several commits has no single patch-id
  match, so those commits stay Committed. A one-commit squash or a rebase
  is recognised.
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
| Spend by branch | Not available. | Git sessions only; a library without Git sees a note, not an empty chart. |
| Library `branch_count`, `pr_count` | 0 for folder sessions, the same as a git session with no branch or PR. | (Outcome PR) Shown as "Not a repository". |
| File search | Found only Conductor sessions. | (Tier 1 PR) Every harness that records tool calls. |

## Delivery

1. **Projects for everyone:** the project rule, its storage and backfill,
   Library, Analyze, Storage and MCP, with legacy mapping and the parity
   fixture.
2. **Tier 1 outcome facts and the outcome labels.**
3. **Tier 2 local git linking, reverts and mainline.**
4. **Outcome views in Analyze** (Analyze → Outcomes), then **spend by
   branch** in its own pull request.
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
