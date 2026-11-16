# Background processing audit (November 2026)

An audit of every kind of background work on three questions: does it run
more often than it needs to, what does a representative run cost, and how
accurate is its time estimate. This page is the ledger: what was measured,
what changed, and what is still open.

## Method

- **Run history.** The operation journal, sync history and estimator history
  of one production library over 2026-11-07 to 11-13 (4,922 journal rows,
  277 index runs, 50,558 conversations, 57,680 workspaces, a 110 GB catalog).
  The journal drops runs under each kind's threshold, so short no-op runs are
  under-counted.
- **Profiles.** Each kind was run on its own copy-on-write clone of a
  backup of that library, on the classic engine, with about four cores,
  alongside other work on the same Mac. Wall times on this catalog swing
  with page-cache warmth; where that matters the table gives rows, bytes or
  instructions instead.
- **Before and after.** Every fix was re-measured on a clone with the old
  and new build. "Projected" figures multiply a measured per-run saving by
  the run counts in the history; they are not measured days.

The scheduled engine (`PHAROS_WORK_ENGINE=scheduled`) was not measured.

## What the history showed

Seconds per day by kind, from the journal (wall time, not CPU):

| Kind | s/day | What it was doing |
|---|---|---|
| Agent events | 12,670 | Model inference is about 1%. The rest: a whole-library Fixes rebuild inside almost every pass, and saving unchanged conversations at 1.04 s each under the writer |
| Library view | 11,400 | 87% from ten runs that each re-marked 20,000 or more workspaces |
| Fingerprints | 9,950 | Five whole-library passes; triggers watched columns the renderer never reads |
| New conversations (index) | 4,700 | Mostly writing changed groups; no-op runs are 237 s/day |
| Label query facts | 3,892 | 261,439 conversations rebuilt (5× the library) while 1,672 got new messages; 0.16% of rebuilt facts changed |
| Library upgrade | 2,600 | Three tool-ledger version bumps rebuilt all conversations |
| Message labels | 1,486 | No message re-inferred that did not change; the cost is rebuilding and republishing |
| Writing, peers, refine | 1,390 | Refine is a full pass daily; peers rescans the library after any import |
| Findings measure | 1,194 | 39 runs a day to re-measure 7 watched findings |
| Result cache | not journalled | About 1.4 cores on an idle library, 76% of rebuilds byte-identical |

The framework's own ticking is small: about 1.8% of a core idle.

## Changes and their measured effect

### Frequency: work that ran with nothing to do

| Change | Before | After |
|---|---|---|
| Label facts rebuild only when stored labels change; "pending" is applied at read time | 5,100 marked conversations: facts loop processed 11,073 conversations, wrote 151,054 rows, 78.7 s, writer held 24.2 s | 200 conversations, 3,366 rows, 2.9 s, 0.51 s. Final tables byte-identical |
| Library view and workspace metrics are marked only when a column they read changes | Usage replay on 300 workspaces: 285 marks. Stamping 10,107 pull requests: 5,936 marks, 61.9 s | 12 marks. 0 marks, 0.22 s |
| Each Library view column has its own version; a changed column is computed alone, in the background | A definition change dropped the table and marked every workspace: 56,980 whole rows, 428.5 s on the clone (8,434 s for the 11-12 bump in production) | Opening a catalog from the previous build marks 0 workspaces and keeps all 57,680 rows (the step takes 0.5 s). The four columns below, for every workspace: 75.6 s, writer held 5.2 s in all, 5.1 ms at worst |
| Fingerprint triggers watch only what the renderer reads | Rewriting all agent-session tokens re-queued 50,558 conversations and 41,971 workspaces; a new labels model row re-queued everything | 0 and 0. A 50-conversation delta queues 8, not 50 |
| A tool-ledger change names the conversations it can reach, by provider, source kind or both (`toolLedgerScopes`); see [Changing the ledger's derivation](../tool-analytics.md#changing-the-ledgers-derivation) | `tools-v15` changed how 194 Claude.ai export conversations are replayed and rebuilt all 50,558: 2,704 s in production. Three such bumps cost about 14,100 s in six days | A `claude-ai` source-kind revision on a clone already at `tools-v16`: 194 conversations pending, the whole upgrade 10.6 s (the ledger step 5.6 s). The scheme itself marks nothing: a catalog the previous build rebuilt to `tools-v16` has 0 pending, and one on `tools-v15` gets the one `tools-v16` rebuild (50,558 conversations, 2,004 s on the clone), with rows identical to the previous build's on the 5,736 conversations compared |
| A new identity link or a moved ledger generation no longer forces a full tool rollup | 50 conversations plus one link: 73.2 s, writer held 9.8 s, 201,707 query-cache bumps | 2.0 s, 0.26 s, 3,797. Cube, daily table and mirrors identical to a full rebuild |
| Peers rescans only where new evidence could link | After an old-dated chat import: 307 s, 26 GB allocated, 6,920 readers | 1.6 s, 51 MB, 0 readers, identical links |
| Peers is told of a call changed in place by conversation, and only when a column a link is derived from changed | After the tool-ledger rebuild for a new version (v15 → v16, 50,558 conversations): the one library-wide time moved back to 2025-11-09; the next pass took 318 s, 28.4 GB allocated, 7,959 readers | No conversation noted; 0.6 s, under 0.1 MB, 0 readers. Same 3,419 links. One call from June reclassified: 1 conversation noted, 12.3 s, 0.6 GB, 378 readers |
| Fixes is rebuilt at most daily from agent events passes | About 7.6 rebuilds a day at about 671 s each | At most one (projected: about 85 → 11 min/day) |
| Authorship changes no longer mark agent events dirty; a full refine rewrites only real changes | Refine rewrote 3,652 rows (3,647 a counter in the reason text), each marking labels, agent events and findings | 5 rows |
| A new app session's identity link, and rows without a time, no longer discard the writing checkpoint | One new linked session (1 link, 949 messages): full refine, 280.6 s, 44.9 GB allocated, 3.4 GB peak. A morning's arrivals (23 links, 48 conversations, 143 prompts): full refine, about 300 s, 44.5 GB | 5.5 s, 2.65 GB allocated, 1.0 GB peak, 1 row rewritten. 19.0 s (14.2 s of it one writer hold), 2.71 GB, 1.0 GB peak, 5 rows. Stored rows identical to the old build's full pass |
| The result cache's unasked sweep, derived-value use marks and debounce | See "Result cache" below | |
| Findings measure is never placed faster than Hourly; placement weighs a day's fixed cost, with hysteresis | 17 cadence moves in 5.9 days (replay) | 2 |
| Ask suggestions refresh by day and six-hour bucket, not after every index | 12 or more runs a day | About 6 (projected) |
| An Ask Pharos chat writes ingest's upgrade markers | One chat left a daily upgrade pending | None |
| Hourly items about to be due run in one pass | 39.8 hourly passes a day | At most 25.3 (projected) |
| Queued verification backs off inputs that never fit its budget | 81% of runs hit the 30 s deadline on the same inputs | Not measured end to end |

### Cost of a run

| Change | Before | After |
|---|---|---|
| Saving unchanged agent-event conversations: 200 per transaction, dependency lookups primed | 1.04 s per conversation; 440 writer holds of 1 s or more in 838 conversations | 3.2 ms per conversation; none |
| Project colour query groups before joining | 5.1–5.7 s per identities pass; 3.6–3.9 s inside an ingest write for the largest repository | 0.18 s; about 0 |
| Findings usage query reads the window's conversations first | 17.96 s cold, 0.93 s warm | 4.90 s, 0.28 s, identical rows |
| Library view counts turns, tool calls, tool errors and compactions from covering indexes and the ledgers, not message rows | 2,000 workspaces (342,196 messages): 481,638 pages read (241 per workspace); refresh 11.85 ms per workspace on first read, 1.77 ms warm; 48.5 G instructions warm | 101,554 pages (51 per workspace); 7.24 ms and 1.11 ms; 27.5 G |
| Search update triggers skip unchanged text | 5,000 tool-call rewrites: 4.1–10.3 s, 142–174 MB of WAL | 0.43–2.2 s, 43–45 MB |
| Capture compression pass that finds nothing | 4.1 s, 61 G instructions | 0.45–0.55 s, 8.3 G |
| Git merge stage with nothing new | 125 `git` processes, 4.0–4.8 s | 15, 0.8 s |
| Session Git streams diffs | 3.30 GB peak | 0.68 GB |
| A large workspace's fingerprint input reads shared digests | 147 s for 997 conversations, past its 2-minute timeout, retried forever | 4.3 s, identical input |
| A one-conversation workspace shares its conversation's fingerprint images | 0.49–0.54 s | About 4 ms |
| Library view and conversation names compute on a reader | Writer held 140–240 ms per batch (worst 2.2 s); names wrote on every idle pass (38 ms) | 0.9–1.9 ms (worst 31 ms); no idle write |
| Checkpoint retries while the same reader pins the WAL | 1.01 s holding the writer, one journal row each | 0.1 ms, no row |
| Agent events and labels plan from the cached execution snapshot | 17–27 s and 7.8 s per run | 0.45 s and 0.42 s warm |
| The mirror scan in the writing stage runs on a reader | 17.7 s on the writer | 0.22 s |

### Estimates

Replayed over the stored history, training only on earlier runs:

| Checkpoint | Before (median error, coverage, range ÷ actual) | After |
|---|---|---|
| Automatic index, after discovery | 30%, 100%, 281× | 21%, 83%, 1.2× |
| Automatic index, 25–75% through | 27–34%, 100%, 286–333× | 22–33%, 76–86%, 1.3× |
| Manual index, after discovery | 32%, 93%, 2.2× | 34%, 59%, 0.77× |
| Schedule item cost, review | 31× off | 1.4× |
| Schedule item cost, writing | 6.9× off | 1.8× |

The operation forecast now fits each step as a fixed cost plus a per-unit
cost, takes ranges from residual quantiles, borrows the previous version's
history across a version bump, and trains on the completed steps of
interrupted runs. Manual index ranges under-cover (50–76% against an 80%
target, on 17 runs) and its point error did not improve. Schedule items are
costed from their own runs in `background_item_runs`; the tools item did not
improve (2.9× → 4.0× on 11 runs) because its runs are bimodal.

Progress totals were added or corrected for fingerprints, the tool rollup,
queued verification, the quick writing pass and peers.

## Result cache

The cache's background refresher rebuilt every clock-relative entry every
10 minutes for three days after its last view, and every entry after any
source write. Now the unasked sweep queues an entry only when its data
stamp is no longer current and has been unchanged for 15 minutes, swept
rebuilds do not count as use of derived values, and a build that failed is
not retried until the data changes. Requests are unchanged: a clock-stale
entry is answered as stale with its real build time and rebuilt at once.

Measured on clones seeded with the production cache file (392 swept
entries). Rebuild counts are exact; CPU figures are approximate on a shared
machine.

| | Before | After |
|---|---|---|
| Idle 20 minutes: rebuilds | 483 | 0 |
| Idle: process CPU-seconds per minute | 100.7 | 4.0 |
| Four minutes after one title change | 197 rebuilds, 397 CPU-s | 0 |
| Three changes four minutes apart (41.5 min) | 1,639 rebuilds, 4,021 CPU-s | 386 rebuilds (one round), 1,546 CPU-s |

The first view of a clock-relative card after more than 10 minutes away now
shows it as stale with its age for a second or two, where it was usually
already fresh. Skipping the rewrite of identical bodies was dropped: every
body carries the clock the frontend draws from, so none is identical.
Narrower per-table stamps are a follow-up in [the result cache
notes](../result-cache.md).

### What the Library view's ledger columns changed

Turns and compactions are the same in all 57,680 workspaces of the audit
copy. Turns still include a prompt on a branch since abandoned (25 of
62,382), which the index of selected messages would have dropped.

Tool calls come from the tool ledger, which holds a call once. In 29
workspaces (28 Codex sessions run by Conductor and one Claude Code cloud
export) the transcript repeats earlier calls under the same call ID, and the
message count had counted each copy: 2,044,548 before, 2,033,493 now.

Tool errors are the ledger's failed calls, where they were results flagged
`is_error`: 70,054 before, 85,337 now, in 13,047 workspaces where it was
9,601. The count changes in 3,947 workspaces. It rises in 3,918, mostly for
nonzero exits and timeouts no flag recorded, and falls in 29 where a
repeated result was counted twice. The ledger does not count an exit status
of 1 from `rg`, `grep`, `diff`, `cmp` or `test` when the command runs that
one program; a chained command ending that way still counts as a failure.

The whole-library re-marks whose cause the audit could not find (about
32,000 s in the window, probably another build's definition) now cost only
the columns that build defines differently.

## Open

Not done, largest first by the audit's estimate:

- **Refine is still a full pass on most days.** A new session's identity
  link and undated rows no longer force one (see below), but each of these
  still does (44.5 GB allocated):
  - A repeated prompt crossing the template threshold. This refused the one
    22-hour replay period that had no other cause (49 links, 296 prompts).
  - A copy that arrives after its original was classified. Its old-dated
    messages and its link both change history. In the newest 1,000 links,
    every TL1 copy and 25% of Conductor copies were first seen more than two
    minutes after the other copy.
  - Old-dated imports, and mirror representatives changing among workspaces
    with history.
- **Every written record marks all nine analysis stages.** Narrower marks
  need ingest to say what changed.
- **Ingest rewrites carried counts** on every tool call still in context on
  each append (17–28% of write time).
- **Conductor discovery** scans a 2.46M-row message index when Conductor's
  database changed (381 s/day).
- **Safety refresh** re-reads 28 days of tool calls on every full findings
  pass (about 130 s), and the failure detector builds maps for candidates it
  discards (a full pass peaks at 8.9–9.7 GB).
- **Fingerprint selection** costs about 0.2 s per unit when many
  conversations are held back.
- **"Other sources"** hard-links the whole TL1 capture into a lease every
  hour (11.5 s); a safe skip needs a fingerprint of everything the index
  reads.
- **Origins** recompute every conversation on each revision bump (36–38 s
  after one new link).
- **Local backup** copies the whole catalog each run and ends its progress
  at about 85%.
- **Startup.** A cold execution snapshot built at background I/O priority
  held derived values and result rebuilds for about 30 minutes on a clone.
- **Estimates.** About 85% of background wall time still shows no time:
  the panel reads only the top-level operation, and chain stages under a
  background pass have no forecast.
