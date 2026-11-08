# Peer conversations: grouping parallel top-level agents

Status: the first version is built (2026-11-07). The proposal below records the
measurements and options; [What was built](#what-was-built) records the owner's
decisions where they changed it.

## What was built

**Owner decisions (2026-11-07)**
- Generic signals only. Pharos does not index the Conductor workspace id, or any
  other harness's container: most users don't run every review through one app.
  Conductor ids served only as an offline check of probable links
  (`peer_links_probe_test.go`).
- Express certainty, not confirmation. Links are *high* or *low* confidence.
  Certain related work is drawn as solid chips and joins the workspace's
  Hierarchy and Timeline; uncertain work is a dashed chip only.
- Search keeps related work as separate rows.
- The work page moves toward a work item: certain related work's agent sessions
  appear among the workspace's own, so idle gaps line up across them.
- Copies from sources older than 7 days stay out of scope (the authorship
  window already enforces it).

**Signals** (`peer_links.go`, stage `analysis.peers` after authorship)
- `copied_into`: a user turn quoted at least 12 words of another family's
  reply within a day (`quoted` authorship spans with a source). High from 40
  words, low below. Resent prompts (the person reusing their own words),
  launch prompts and generated forms are not hand-offs. A copy alone names no
  role: copies carry plans and debugging output between tasks as often as
  review findings.
- `read_written_by`: a family that ran a diff (`git|gh|jj` `diff|show|pr`)
  saw lines another family wrote in edit calls within 7 days. Only lines one
  writer wrote count. The reader is the reviewer, the writer the implementer.
  - **High:** the lines were in a diff's added lines (≥ 3, more than the
    runner-up) and timing agrees: the writer's next prompt followed the
    reader's last reply within 30 minutes, or the reader began within 30
    minutes of the writer's turn ending, wrote no code of its own, and leads
    the runner-up 3×.
  - **Low:** the same start timing with a closer runner-up, or a clear lead
    (≥ 3× the runner-up) with one supporting fact: a diff read by a
    conversation that wrote nothing itself, a diff with timing, or file reads
    with timing by a conversation that wrote nothing itself.
  - Anything else is dropped, including file reads without timing.
- `tl1_candidate`: each TL1 task on a candidate links to the candidate's first
  task (candidates hold up to 38 tasks). High.
- Not built: agent senders (no cross-family rows outside Conductor), checkout
  and branch, PRs (need the outcomes stack), Conductor attachments and diff
  comments.

**Measured on a full library** (27,000 families, `peer_links_probe_test.go`).
The owner expects nearly all real links to fall inside one Conductor workspace
or one TL1 work item, so a link is scored right when both sides share a
Conductor workspace (by id, or by checkout folder in either the
`conductor/workspaces/<repo>/<name>` or `<repo>/.conductor/<name>` layout) or
a TL1 work item (candidate or root task), and wrong otherwise. About 4% of
links can't be checked (a side with no Conductor folder or TL1 task) and are
left out.

| Links | Right | Wrong | Precision |
|---|---|---|---|
| Diff + the writer's next prompt followed | 105 | 4 | 0.96 |
| Diff + began as the writer's turn ended, wrote nothing, clear lead | 543 | 84 | 0.87 |
| Quoted copies within a day, ≥ 40 words | 757 | 93 | 0.89 |
| **Related pairs shown solid and in the hierarchy** | **1,115** | **170** | **0.87** |
| Related pairs shown dashed | 466 | 454 | 0.51 |
| Dashed: diff, no timing, reader wrote nothing | 262 | 217 | 0.55 |
| Dashed: diff + start timing, reader wrote code | 49 | 60 | 0.45 |
| Dashed: file reads + start timing | 122 | 133 | 0.48 |
| Dashed: quoted copies under 40 words | 56 | 36 | 0.61 |
| Quoted copies after a day (dropped; earlier run) | 15 | 62 | 0.19 |
| File reads, no timing (dropped; earlier run) | 44 | 492 | 0.08 |
| TL1 candidate links | 7,063 | 0 | 1.00 |

A pair counts as certain when one link is high or a copy and a read agree
(0.97 on their own). The first rules, tuned on review-titled sessions only,
made diffs without timing high and counted resent prompts as copies; scored
this way their solid pairs were 0.83.

A full rebuild of read
links takes 2.5–4 minutes on that library (copies 0.3–1.3 s, TL1 0.4 s), so
only a new rules version rebuilds everything; a day of new calls takes 2–4
seconds. Later runs derive again the readers with diff calls from
`peerRecheck` (3 hours) before the earliest new tool call, so the
implementer's next prompt can confirm a review that read nothing new, and drop
links whose work joined another family.

**Storage**: `work_peer_links`, directed edges between the mirror
representatives of each family's root workspace, with message anchors and
evidence. Groups are never stored.

**Reading** (`peer_reader.go`, `delegation_peers.go`)
- `WorkOverview.peers`: direct links of any confidence, plus work linked to a
  certain peer by another certain link, up to `peerMaxPeers`. A peer is certain
  when one of its links is, or when two kinds agree. Roles: whoever read the
  other's lines or whose reply was quoted is the reviewer.
- `WorkOverview.peer_conversations`: certain peers' conversations, which the
  reader opens in place.
- Messages carry `handoffs_in` / `handoffs_out` from link anchors.
- `/api/work/{id}/delegation` and `/parallel` merge certain peers' delegation
  snapshots as further top-level trees, each node marked with `peer`. `totals`
  stay the workspace's own; `group_totals` counts everything shown.

**Page**: a "Related work" chip strip under the heading with a "Why related"
disclosure, hand-off markers on the anchored messages (reveal = scroll +
highlight), related sessions labelled with their role in the Hierarchy,
sidebar and Timeline, and "With related work" beside the workspace totals.

## The problem

A lot of agent work happens in pairs or small teams of **independently started**
conversations:

- an implementing agent and a reviewing agent;
- the review's findings pasted back into the implementer;
- a second reviewer after the fixes, and so on.

The Conversation tab only knows one kind of relationship: **delegation**. That
covers a parent and its sub-agents (`conversations.parent_id`,
`agent_sessions.child_conversation_id`, `conversation_launches`). A reviewer
that the person started is not a sub-agent of anything, so today it shows up as
an unrelated row in Search. Reading the implementer, you see a large pasted
block with no hint of where it came from. Reading the reviewer, you can't tell
whether anyone acted on its findings.

**Goal:** when you open any member of such a group, the Conversation tab shows
the others as **parallel top-level agents** of the same piece of work. That
means:
- each member keeps its own sub-agent tree;
- hand-offs between members (review output → implementer prompt) are drawn as
  links in both directions;
- the person can read the members one at a time, or interleaved as one
  conversation.

## How common it is (measured 2026-11-07)

Two places were measured: the Conductor database on this Mac, read-only, and an
idle euclid dev copy (`pharos-dev-ankara-compactions`, messages up to
2026-11-07).

| Observation | Count |
|---|---|
| Conductor workspaces with more than one session | 1,119 of 3,427, holding 3,309 of 5,617 sessions |
| Review-titled Conductor sessions ("Workspace changes review", "Review request.md review", "Review pasted text", …) | 1,610, in 1,226 Conductor workspaces |
| Conductor workspaces where a review session sits beside a non-review session | 747 |
| Model-authored Conductor diff comments (reviewer → `DiffComment`) | about 1,800 |
| Pairs of top-level conversations linked by copied agent text (after removing mirror copies, sub-agents and launched runs) | 2,039 |
| …of which the copied text came from a review-titled conversation | 823 (761 in the same checkout) |
| …in the same checkout | 1,164 |

Worked example: Conductor workspace `calgary-v4` on 2026-11-06.
1. `Background processing tab sort, label` (Codex) implements the change, 12:00–12:03.
2. Conductor starts `Workspace changes review` (Codex) in the same workspace at 12:04, on the same branch and head.
3. That review session then creates PR #468.

In Pharos these are two unrelated workspaces with nothing linking them.

The heaviest consumer in the dev copy is `Query Table API structural
weaknesses`. It took copied text from 12 different review conversations: a
review loop that ran "until a full cycle of reviews with no P1 findings".

## What already exists

Most of the machinery is already there. What's missing is a stored relationship
*between top-level conversations* and a reader that shows it.

**Reader**
- `readDelegationSnapshot` (`delegation.go:203`) already returns several
  `Roots`, one for each conversation in a workspace without a main session.
- `ParallelActivity` (`web/src/delegation.tsx:1442`) already draws one swim
  lane per session.
- TL1 workspaces already hold several top-level conversations: one per attempt.
- The constraint is scope. Both the snapshot and `WorkConversation`
  (`work_reader.go:127`) filter on `workspace_id=?`.
- `docs/delegation-explorer.md` says it directly: "Siblings in other workspaces
  are not loaded."

**Copy provenance**
- This already exists, but it is buried and only goes one way.
- The authorship classifier (`authorship.go`, 8-word shingles, 12-word minimum,
  7-day window for agent output) stores the source of quoted text in
  `message_authorship_spans.source_conversation_id` and `source_message_id`.
- The only place it shows is the authorship tooltip, as "Open the matching text
  in …" (`web/src/authored.tsx:104`), and only when authorship colouring is on.
- Nothing on the *source* message says where its text was used.

**Other links today**
- **Mirrors:** `conversation_identity_links` (`native-alias`) pairs a Conductor
  session with its native Claude or Codex transcript. These must be collapsed
  before anything else, or every pair is counted twice.
- **Agent-to-agent senders:** `messages.sender = 'agent:<id>'`. Conductor's
  `sender_session_id`, Cursor, Copilot and OpenCode all produce it. It is only
  used as an authorship reason and is never resolved into a link. In
  Conductor's database it is empty today (0 rows).
- **TL1:** `tl1_tasks.candidate_id` / `root_task_id`, and
  `tl1_review_findings.review_task_id`, already group implementer and review
  tasks by candidate. That grouping lives only in the TL1 tables.
- **PRs:** `pull_requests` and `work_pr_links` are filled from PR URLs in
  Conductor message text, from TL1, and from canonical exports.
- **Commits and files:** commit-level links (`session_commit_links`,
  `session_outcomes.git_ops_json`) and `session_file_rework` exist only on the
  **unmerged** outcomes stack (#384→#466).

**Not stored**
- **Conductor workspace id.** The Conductor adapter reads the workspace row for
  location, branch and archive commit (`adapter_conductor.go:594`), then drops
  the id. Each Conductor *session* becomes its own Pharos workspace.
- **Claude Code paste records.** `~/.claude/history.jsonl` records each
  interactive prompt's `pastedContents` (the text, `sessionId`, `project`,
  `timestamp`).
- **Conductor `attachments` and `diff_comments`.** `attachments` includes type
  `review` with a `comment_id` pointing at the diff comment it carries, and type
  `text` for large pastes.

## Signals

Each signal produces a **typed, directed edge** between two top-level
conversations, with evidence and a confidence. Mirrors are collapsed first.
Members of a delegation family are represented by their root, so a sub-agent
never becomes a peer.

### 1. Declared containers (strong, exact)

**Sources**
- **Conductor workspace id** (`sessions.workspace_id`): add it to the adapter's
  workspace metadata as a generic `container_id`, namespaced by source account
  and host. Conductor databases are per Mac. *(Not built: the owner chose
  generic signals only.)*
- **TL1 candidate** (`tl1_tasks.candidate_id`, and `root_task_id` for retries).
- **Agent-to-agent sender** (`messages.sender='agent:<id>'`): resolve it to the
  sending conversation, giving a `sent_to` edge anchored on the message.

**Why it's allowed.** The user rule is "never hardcode Conductor; find
harness-agnostic signals". A container id is an adapter **capability** in the
same family as `native-alias`. Any harness that records a container declares
it, and nothing downstream knows which harness it came from.

**Guard.** Container ids can be reused over time. 151 of 3,215 Conductor folder
names were reused, up to 4 times. The id itself is unique, so key on the id,
never on the folder name.

**It is also ground truth for calibrating the inferred signals.** Hide
`container_id`, run signals 2–5, and measure how many same-container pairs they
recover and how many cross-container pairs they wrongly join. This is the same
method that calibrated git attribution with printed commit hashes hidden.

### 2. Copy provenance (strong; the user's signal (c))

**Edges**
- From `message_authorship_spans` rows whose source conversation (after
  collapsing mirrors) is a different top-level conversation.
- Kind `copied_into`, directed from source message to destination message.
- Confidence comes from matched words and the share of the destination turn,
  plus a bonus when the source is review-shaped (read-only tools; findings
  format such as `P1` or `file:line` bullets).

**This is the edge that matters most for the review → feedback pattern.** It
says which message fed which turn. It is also what lets the reader draw the
hand-off.

**Gaps to close (part of P3 below)**

- **Conductor large pastes become attachment files.** The transcript holds only
  `@⟦Review request.md⟧(.context/attachments/…)`. That accounts for 902 text
  attachments and 109 "Review pasted text" sessions.
  - Capture the attachment's content at sync while the file still exists, and
    run it through the same shingle match.
  - Attachment paths alone are not a signal: generic templates such as
    `PR instructions.md` recur across workspaces.
- **Reviewer diff comments** (`diff_comments`, carried to the implementer as
  `review` attachments via `comment_id`).
  - Attribute each comment to the reviewer conversation through its own
    `DiffComment` tool call, an exact match.
  - Then link it to the implementer turn that received the attachment, giving a
    `review_comment_into` edge.
- **Claude Code `pastedContents`.** These give exact paste boundaries for
  interactive CLI prompts. They sharpen both "this was pasted" and "where from".
- **Window.** Agent output is matched only within 7 days and 12+ words. Both
  limits are fine for hand-offs. Keep them, and note them in the evidence.

**Non-transitive by design.** An implementer that pastes yesterday's design doc
should not pull the design conversation's own feeders into the group. Copy
edges attach a *direct* feeder, forming a star around the consumer. They never
chain.

### 3. Same checkout and branch (supporting; the user's signal (b))

**Rule**
- Same `location` and same branch, with activity windows within a few hours.
- Branch is not the repository's default branch.

**Sources**
- Claude Code records `gitBranch` per event.
- Codex records `git.branch` per rollout.
- Conductor records the workspace branch.

**Why it is not enough alone**
- In a plain checkout such as `~/code/repo` on `main`, everything would merge.
- Even in dedicated worktrees, folders are reused for unrelated work over weeks.
  `calgary-v4` hosted `Pharos app update` on Oct 22 before the Nov 6 pair.

**Use**
- It joins a group when corroborated by another signal kind.
- It is enough alone only when the location is a dedicated worktree (not the
  repository's main checkout) *and* the branch and time window both match.

### 4. Same PR or commits (supporting; the user's signal (a))

PR links as stored today are too noisy to group on:

- 18,730 `work_pr_links` rows say `created` at confidence 1.0, and one workspace
  has 5,080 PR links.
- `Binary size increase debug` "created" 77 PRs, every one titled with its own
  PR's title. That is a PR-title attribution bug, worth fixing separately.
- `trace(pr=468)` is not scoped to a repository. It returned pharos #468
  alongside explo #468 and xlsx-exec #468.

Use an evidence ladder, and group only on the top two rungs:

1. **Created:** the `gh pr create` output URL, or the source's own PR record.
2. **Pushed to the PR head:** a shared `session_commit_links` commit, or a push
   to the PR's `head_ref`.
3. **Operated on:** `gh pr view|diff|checkout|review|comment|merge N`, with the
   repository resolved from `-R`, the checkout or `cd`. This rung groups only
   together with signal 3 or a review-shaped conversation.
4. **Mentioned in text:** never groups. It is shown as "mentions #N" only.

**Rules**
- Every PR key is `(repository_id, number)`.
- Suppress hubs: a PR linked to more than N top-level conversations, or a
  conversation operating on more than K PRs, adds no edges.

**Dependency.** This needs the outcomes stack (#421 `readGitOps`,
`session_commit_links`) merged first.

### 5. Shared work items (strong where present; the user's signal (d))

- TL1 is covered by signal 1.
- For other sources, two workspaces whose work items carry the same external
  key are joined, for example canonical-export ids.
- Work items stay what `docs/ui-vocabulary.md` says they are: source-recorded
  tasks. They are *evidence for* a group, never the group itself.

### 6. The reviewer read what the implementer wrote (strong; measured)

Most reviewers start by running `git diff`, reading files, or doing both. So
their **tool output** contains lines another conversation **wrote** earlier in
its edit calls: `new_string`, `content`, the `+` lines of a patch, or heredocs.

This needs no copy/paste, no shared container and no shared checkout, so it
works across apps and tools. It is the content counterpart of signal 2: signal
2 is "your output was pasted into my prompt"; this is "your edits appear in
what I read".

**Edge:** kind `read_written_by`, directed from implementer to reviewer, with
the matched-line count as evidence.

**Rule (measured)**
- Count the implementer's written lines of 40 or more characters, made before
  the reviewer started, that appear in the reviewer's tool output.
- Count only lines unique to one candidate.
- Pick the top candidate only when it has at least 3× the runner-up's count.

**Most wrong picks share one cause.** The reviewer also read code another
recent conversation had written that was already merged, so two candidates
score close together (for example 147 vs 180). Two refinements should fix this:
- count only reviewer lines from diff-shaped output (`+` lines of `git diff`);
- or drop lines already present at the reviewer's base commit.

**Cost**
- Index written lines as hashed shingles, the same way as the authorship copy
  index.
- Probe only conversations with review-shaped first prompts, or ones that run
  `git diff`, `gh pr diff` or `gh pr view`.
- Tool output is stored but not word-indexed, so the probe reads the reviewer's
  `tool_result` text once per new conversation.

### Timing (measured: a good confirmer, a poor finder)

Timing was tested in two forms:
- **Relative request timing:** the reviewer starts when the implementer's turn
  ends, and the implementer's next prompt follows when the review ends.
- **Lifecycle order:** a review is posted before the PR is created.

**Setup**
- 596 review-titled conversations whose true implementer is known from the
  shared Conductor workspace.
- For each review, the candidates were every other same-repository
  conversation active within 12 hours before it: a median of 8, because the
  owner runs many conversations in parallel.
- Location and branch were deliberately **not** used, to simulate the
  cross-app case.
- Scripts: `.context/peer-timing/` in workspace adelaide-v9.

| Rule: for each review, pick its implementer | Right | Wrong | Precision | Coverage |
|---|---:|---:|---:|---:|
| Random candidate (baseline) | 163 | 433 | 0.27 | 1.00 |
| Closest turn end before the review starts | 226 | 360 | 0.39 | 0.98 |
| …only when the runner-up is ≥ 5 min further | 176 | 74 | 0.70 | 0.42 |
| Closest return (implementer's next prompt after the review ends), margin 5 min | 264 | 73 | 0.78 | 0.57 |
| Turn end and return pick the same candidate | 162 | 40 | 0.80 | 0.34 |
| First PR created after the review (in either conversation) | 264 | 222 | 0.54 | 0.82 |
| Return and PR order pick the same candidate | 227 | 43 | 0.84 | 0.45 |
| Turn end, return and PR order all agree | 127 | 16 | 0.89 | 0.24 |
| Copy link (signal 2) | 346 | 0 | 1.00 | 0.58 |
| Reviewer read implementer-written lines (signal 6) | 530 | 52 | 0.91 | 0.98 |
| …top candidate ≥ 3× the runner-up | 471 | 19 | 0.96 | 0.82 |
| **Written lines and return timing agree** | **322** | **0** | **1.00** | **0.54** |
| **Ladder: copy → written lines (3×) → timing agreement** | **539** | **13** | **0.98** | **0.93** |

**Timing profile of true pairs**
- **Start:** the review began a median of 4 min (p90 86 min) after the
  implementer's turn ended.
- **Return:** the implementer's next prompt came a median of 4.4 min
  (p90 47 min) after the review ended. 82% of reviews were followed by a prompt
  in the implementer.
- **PR:** 62% had a PR created within 24 h after the review.

**Why timing alone is weak.** In bursty parallel work, some other conversation
nearly always finished a turn a minute before.

**Where timing helps**
- **As a confirmer:** written lines plus return timing made no mistakes at all.
  Timing also breaks ties between content candidates.
- **As a last resort:** it covers reviews with no content signal. Turn end,
  return and PR order must all agree, and its edges stay labelled *probable*.

PR order is useful in the same way, as an extra agreeing constraint ("the
review happened between the implementer's last edit and the PR"). It is not a
finder. It also depends on detecting the PR's creation reliably: rung 1 of
signal 4.

**Limits of this measurement**
- The ground truth is Conductor's review button, which starts the reviewer at a
  natural turn boundary.
- Human-driven reviews in other apps are too few in this library to measure
  separately: 85 Claude Code and 38 Codex human-driven top-level conversations
  since 2026-09-01.
- Some "wrong" picks are label noise. For example, a review of a pasted design
  document has no implementer in its own workspace.

### Not used for grouping

- **`session_file_rework`** ("a later session removed your lines") relates work,
  but mostly *sequential* work: later features rewriting earlier ones. Show it
  in file history, as it is today. It does not mean "same piece of work".
- **File-mediated hand-offs** are plausible but unmeasured: one conversation
  writes `.context/handoff.md`, another reads it. Measure them on the
  calibration set before adding them as a weak signal.

## Combining signals into groups

**Rules**
1. Collapse mirrors. Map every conversation to its delegation-family root.
2. **Declared edges** (container, TL1 candidate, agent sender) are transitive.
   Union-find over them gives the core groups.
3. **Content edges** attach the source's group to the consumer's group directly.
   They are not transitive beyond that one hop. Content edges are copy links
   (signal 2) and written lines read by the reviewer (signal 6, top candidate
   ≥ 3× the runner-up).
4. **Content candidates are confirmed by timing.** If the content pick also wins
   on return timing (the implementer's next prompt follows the review), it
   joins as *confirmed*. Otherwise it joins as *probable*.
5. **Supporting edges** attach a conversation only when it has two distinct
   kinds of evidence, or one strong edge plus a supporting one. Supporting edges
   are checkout + branch, PR rungs 2–3, and timing agreement (turn end, return
   and PR order). Timing agreement alone yields only a *probable* edge. The
   reader shows probable links with their evidence, and never merges usage
   totals for them.
6. **Bounds:** a group spans at most a configurable time (default 7 days, the
   same as the copy window). Beyond a member cap, the weakest edges are dropped
   first. Both are package variables that tests can lower.
7. **Roles** are derived per member, never stored as truth:
   - *implementer*: edits or commits;
   - *reviewer*: read-only, review-shaped first prompt, its output consumed by a
     peer;
   - *peer*: anything else.

   The reader shows roles; analyses don't count by them.

**Storage**
- **Store the edges.** Groups are a derived view. The UI must be able to say why
  ("Same Conductor workspace · its review was pasted into this conversation at
  12:13"), and edges can be revised incrementally without renumbering groups.

**URLs never carry a group id**
- `/work/{workspace}` stays the identity. The reader shows the peers of
  whichever member you opened.
- This keeps URLs stable as inference improves. It also fits the
  URL view-state contract: peers are a view of an entity, not a new entity.

**Alternative considered: re-scope workspaces**
- The idea: make the Conductor adapter put all sessions of a Conductor
  workspace into one Pharos workspace. The reader already handles several roots
  per workspace, as TL1 does.
- Rejected:
  - It changes every Conductor `/work/{id}` and every workspace count
    (Parallelism, Search totals).
  - It needs an index-version rebuild.
  - It only helps Conductor users.
  - Inferred groups (copy, checkout, PR) would still need a separate layer.
- One layer for declared and inferred groups is simpler.

## Data model

```sql
-- Directed evidence between top-level conversations (after mirror collapse).
CREATE TABLE conversation_peer_links (
  left_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,   -- source / earlier
  right_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,  -- consumer / later
  kind TEXT NOT NULL,          -- container | tl1_candidate | sent_to | copied_into | read_written_by |
                               -- review_comment_into | checkout_branch | pr_created | pr_pushed | pr_operated |
                               -- timing (turn end + return + PR order agree)
  strength TEXT NOT NULL,      -- confirmed | probable
  confidence REAL NOT NULL,
  left_message_id TEXT,        -- anchor for hand-off markers (copied_into, sent_to, review_comment_into)
  right_message_id TEXT,
  at TEXT,                     -- when the hand-off happened, for timeline arcs
  evidence_json TEXT NOT NULL, -- container id, words matched, PR key, branch, rule version
  PRIMARY KEY(left_id, right_id, kind, right_message_id)
);
-- Derived membership, rebuilt incrementally from links; never referenced by URLs.
CREATE TABLE conversation_peer_groups (
  conversation_id TEXT PRIMARY KEY REFERENCES conversations(id) ON DELETE CASCADE,
  group_key TEXT NOT NULL,     -- smallest member id; stable enough for caching, not for URLs
  role TEXT NOT NULL,          -- implementer | reviewer | peer
  joined_by TEXT NOT NULL      -- strongest link kind that admitted it
);
```

**Plumbing**
- Adapter: `container_id` in workspace metadata (Conductor `sessions.workspace_id`).
  - Bump the Conductor index version.
  - Regenerate the provider golden records
    (`PHAROS_UPDATE_GOLDEN=1 … TestProviderConformance`).
  - Add a capability flag so the Data readiness page can say which sources
    declare containers.
- Derivation: a background stage after authorship (needs spans) and after the
  outcomes git stage (needs commit and PR links).
  - Make it incremental, with a dirty set keyed by conversation, as
    `tool_rollup_dirty` does.
  - Bump a `peerLinksVersion` constant to rebuild.
  - Never run it on requests ("requests never write").
- Cost: the copy-edge query over the dev copy's 9,447 provenance spans takes
  about 0.3 s. The other signals are joins on existing indexed tables.
- Library plumbing: register the tables in `library_transfer.go` (including the
  private-Ask purge, as `message_authorship_spans` is),
  `capture_history_recovery.go`, `query_cache_generation.go`,
  `incremental_audit.go` and the labeling snapshot.
- Cross-host libraries: container ids are host-scoped, and copy spans already
  cross hosts. A review on the laptop pasted into an implementer on the Studio
  links correctly.

## Reader and API

**API**
- `WorkOverview` gains `peers`: for each member, its workspace and conversation
  ids, title, provider/model, role, active window, and the links that admitted
  it, with message anchors.
- `getDelegation` gains `scope=peers`. It loads each member's delegation family
  as a sibling root, with the cache key extended.
  - Group totals are reported separately from the opened workspace's own totals,
    so nothing double counts.
- `WorkConversation` accepts a conversation from a peer workspace when a peer
  link exists. It still refuses arbitrary cross-workspace reads.
- Message payloads gain:
  - `handoffs_in`: this user turn quoted that message;
  - `handoffs_out`: this reply was used in that turn.

  Both come from `copied_into`, `sent_to` and `review_comment_into` links.

**Conversation tab**

*Peer strip under the heading*
- One chip per member, in start order, for example
  `Implementer · Codex gpt-6.1 · 12:00–12:03`, then `Reviewer · Codex · 12:04–12:12`.
- The opened member is marked. Choosing a chip switches the reader to that
  member without leaving the page.
- A "Why grouped" disclosure lists the evidence edges.

*Hand-off markers in the transcript*
- **On the implementer's user turn:** "From Reviewer · 12:11", over the quoted
  span. This replaces the buried tooltip.
- **On the reviewer's reply:** "Used in Implementer · 12:13".
- Following a marker reveals the target: scroll + highlight, never focus. This
  is the bidirectional link the user asked for in (c).

*Together layout*
- A third layout beside the single-member view: the members interleaved as one
  chronological conversation.
- **Wide screens:** one column per member, aligned by time, with hand-off
  connectors between columns.
- **At 320–390 px:** one interleaved column with a member badge per turn.
- Sub-agents stay nested inside their own member, exactly as today. Peers are
  siblings, never children.

*Timeline tab*
- `ParallelActivity` adds one top-level lane per peer. Each peer's sub-agent
  lanes stay under its own lane.
- Hand-offs are drawn as arcs between lanes, reusing the event-link arc
  placement and pin popover from #450.
- The "Caller" lane stays per family.

*Hierarchy tab*
- A forest: each peer root is labelled with its role, and its sub-agents are
  nested under it. Usage shows per member, plus a group total.

**Search and analyses**
- Search rows gain a small "+1 peer" badge.
- No analysis changes its counting unit. Parallelism keeps counting workspaces
  and conversations.
- A later analysis could count review rounds per change. That is out of scope
  here.

**View state**
- Add view-contract entries for:
  - `peer=<conversation id>` (focused member);
  - `peers_layout=single|together`;
  - the "Why grouped" `ViewDisclosure`.
- Regenerate `docs/view-controls.json` and the site map.
- Add one `view-state.test.mjs` restoration case and one `structure.test.mjs`
  route for a grouped workspace at 1440 and 320 px. Grouping logic gets Go
  unit tests; the interleaving and hand-off model gets node tests.

**Vocabulary**
- Members are **top-level conversations**, the existing Data Guide term. The
  user-facing label is "Related conversations", with roles in the chips.
- Don't call the group a workspace or a work item.
- Add one Data Guide sentence under Top-level conversation: "Pharos can link
  top-level conversations that worked on the same change, such as a review and
  the conversation that acted on it."

## Phases

| Phase | Contents | Depends on |
|---|---|---|
| **P1: declared and content links** | Adapter `container_id`; `conversation_peer_links` from container, TL1 candidate, agent sender, existing copy spans and reviewer-read written lines, confirmed by return timing; `WorkOverview.peers`; peer strip and hand-off markers in both directions | — |
| **P2: together and timeline** | `scope=peers` snapshot; Together layout; peer lanes and hand-off arcs; Hierarchy forest | P1 |
| **P3: wider copy provenance** | Capture Conductor attachment contents and diff comments (`review_comment_into`); Claude Code `pastedContents`; reverse "Used in" everywhere | P1; the capture change needs golden-record and index-version bumps |
| **P4: inferred joins** | Checkout + branch rule; PR ladder with repository-scoped keys and hub suppression; calibration against hidden container ids | Outcomes stack merged (#384→#466) |

P1 alone covers the common case measured above: 747 Conductor review +
implement workspaces, plus 823 review hand-offs by copied text.

## Owner answers to the open questions

1. **Peer strip vs Together.** The chip strip, plus related sessions in the
   Hierarchy and Timeline. Together is not built.
2. **Reviewer conversations in Search.** Separate rows.
3. **Copy edges from old conversations.** Out of scope beyond 7 days.
4. **Group usage totals.** Shown on the work page beside its own totals only.

## Side findings

These came up while measuring and are worth fixing independently:

- **PR "created" over-claims.** Conductor PR extraction records every PR URL
  found in tool output as `created`, at confidence 1.0
  (evidence `"source":"native-tool-output"`).
  - In the dev copy, `Pharos API Pricing Refresh` has 5,080 such links from one
    tool result, a PR listing.
  - In the live library, `Binary size increase debug` lists 77 PRs, all given
    its own PR's title ("Replace Goja fingerprint rendering with native Go").
  - Tool output should give at most `associated`. `created` should require the
    `gh pr create` output, or the source's own PR record.
- **`trace(pr=N)` ignores the repository.** It mixes pharos, explo and
  xlsx-exec PRs with the same number.
