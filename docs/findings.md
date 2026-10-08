# Findings

Findings are recurring patterns in your agents' work that a change to a
repository, its instructions, or a harness setting can fix. Each one says what
happens, what it costs, and the change to make, in words about your own work.
You collect findings into a prompt for an agent; copying that prompt starts a
before-and-after measurement, and the result lands on a sparkline that marks the
day you handed it off. Improvements go to a wins list with a running estimate of
what they saved.

The design, with the product decisions and the evidence behind each threshold,
is [`auto-optimization-design.md`](auto-optimization-design.md). This page
describes what shipped.

## Using findings

- **Findings tab.** Open findings are ranked by their likely saving in the unit
  you choose in settings: dollars (API-equivalent, the default), tokens, agent
  time, or failures (see [Ranking](#ranking)). Every card leads with the likely
  saving, shows what the pattern cost ("at stake") beside it, lists the other
  units down its right-hand side, and names the detector that found it. The tab's badge always counts the open findings; cards that
  appeared since your last visit carry a "New" chip. Each card describes the
  Problem and the Solution.
- **Dismiss and turn off.** Dismiss a finding for one of three reasons, dismiss
  every open finding from its detector at once, or turn the detector off. Turned
  off detectors are listed in Findings settings, where they come back on;
  findings you are already measuring keep going.
- **Add to prompt.** Each finding suggests where its fix goes: a repository, your
  global instruction files for one provider on one Mac, or an automation such as
  a TL1 flavor. You can pick another target and move items between prompts later.
  The prompts ("carts") are saved in the library, so they follow it between Macs.
- **Review and copy.** A target's prompt lists its ticked findings in one outline
  and asks the agent to make the smallest change, preferring a script or hook to
  a new instruction line. It asks for a pull request where the repository's work
  usually ends in one, and for the diff otherwise (global files and automations
  always show the diff). Copying it starts measuring every ticked finding; the
  rest stay in the prompt.
- **Measuring.** A finding being measured shows its day count and a weekly
  chart. If you didn't apply the change, choose **I didn't apply this** and the
  finding returns to open.
- **Results.** *Improved* findings move to Wins. *Unchanged* ones reopen with the
  next, stronger change (an instruction line, then a script or hook, then a
  skill). *Worse* ones ask the agent to undo the change. *Inconclusive* ones had
  too little of the relevant work after the change to tell.
- **Dismiss or snooze.** Dismiss with a reason (not real, not worth it, won't
  fix); it stays dismissed across rebuilds. Snooze for 7 days, 30 days, or until
  it gets worse (its weekly rate doubles).
- **Settings** (Findings → Settings, also linked from Settings): the threshold,
  the ranking unit, what prompts ask for (per repository or everywhere), and
  **Refresh findings now**.

## Where findings come from

Each detector writes its own title, explanation, impact line, evidence rows, and
a ladder of changes for successive attempts.

| Detector | What it looks for | Metric | Where the fix goes |
| --- | --- | --- | --- |
| Recurring failures (D1) | Errored tool calls with the same normalized error line, excluding test failures, the agent's own iteration (stale edits, exploring missing paths, exceptions from scripts it wrote inline), and harness notices | Conversations that ran the program (or any shell command, or the tool) and hit the error | Repository instructions; global instructions when it appears in three or more repositories; the automation's prompt for scripted runs |
| Fail-then-fix recoveries (D2) | The working command that followed each failure. A consistent change of flags, environment, or steps becomes the proposed change; a different target or script each time marks ordinary iteration | (evidence for D1) | |
| Instruction drift (D9) | A provider searching for its own instruction file, or reading another harness's instructions or skills by hand; plus which instruction files the default branch tracks and which the harness recorded loading | That provider's conversations in the repository that looked for instructions | One shared instructions file, then a fallback setting, then a pointer file |
| CLI friction (D7) | A command-line tool used mostly in one repository or automation, whose engagements (its calls within one conversation) read its help or fail on usage. Help the conversation's own prompt asked for doesn't count; general tools (git, gh, psql, …) are left out | Engagements with friction; a result also needs help and usage errors each to fall and engagements to keep ending in success | A skill, or the tool's usage in the automation's prompt |
| Documentation hosts (D7) | A repository's agents fetching the same reference site again and again | Conversations that fetched it | A local copy of the reference |
| Orientation tax (D3) | Non-documentation files read before the first edit in many conversations | Tokens read before the first edit | A map in the instructions, then smaller files |
| Delegable exploration (D4) | 20 or more reads, searches, and commands before the first edit, re-read as context by every later request | Tokens carried from pre-edit exploration | A sub-agent instruction for the providers that do the work |
| Context-heavy commands (D5) | `rg` over everything, whole diffs and patch logs, `sed -n` ranges over 300 lines, generated files read in full | Tokens returned per call of that shape | A line in the instructions, then a hook |
| Cost outliers (D6) | Conversations past 1,000 requests or 10 compactions, or with a request over 500k tokens of context | Share of the repository's tokens from those conversations | Handoff instructions; an earlier auto-compaction setting |
| TL1 flavors | Error clusters, reruns on the same candidate, and runs costing three times the flavor's median | Runs of the flavor | The flavor's prompt, schema, budgets, or configuration |

A pattern with no fix the user controls (a harness's own tool-input parsing, a
browser that isn't available) is kept for trend and never shown.

## Ranking

A finding's impact is everything its pattern touched in the last 28 days.
Ranked by that alone, the biggest tasks always came first: on one library the
top card was $1,700 from six long conversations, of which splitting them could
remove about a sixth. Cards are ranked by the likely saving instead:

    likely = impact × removable × takes × persists

- **Removable:** the part of the cost the change could remove if agents followed
  it every time. Each detector works it out from what it measured:

  | Detector | Removable part |
  | --- | --- |
  | Recurring failures | The failed calls, in full |
  | CLI friction | The engagement's help calls and usage errors, not its real work |
  | Instruction drift | The searches for the file; hand reads stay, since the harness loads the same text |
  | Context-heavy commands | For long `sed` ranges, all but the 180 lines a narrow reader reads of a file; for other shapes a fixed share (half for searches and diffs, 0.7 for patch logs, 0.8 for generated files) |
  | Delegable exploration | What later requests carry after the exploration, less a 3k-token summary; a sub-agent pays the carry within the exploration too |
  | Cost outliers | For runaway conversations, the context above the repository's typical request; for oversized ones, the context above 250k |
  | Orientation, documentation hosts, TL1 errors | Assumed: 0.3, 0.5, 0.5. TL1 repeat runs and the cost above a flavor's median count in full |

  Where a model has no price, the share is taken from tokens.
- **Takes:** how often a change of its kind works, by the lever of the change the
  card proposes next. A harness setting or hook starts at 0.9; a concrete
  instruction line (use this command, not that one) or a line in an
  automation's prompt at 0.7; a script, skill, or file at 0.6; a line asking
  agents to work differently at 0.3; and asking them to delegate to a sub-agent
  at 0.3 for Claude and 0.1 for Codex and Antigravity. The instruction and habit
  numbers come from natural experiments in one repository's instruction history:
  a line naming the build path cut builds to the wrong path from 33% to 4% of
  conversations, while "read the rules first" was followed in 46% and "commit
  early" made no visible difference. The others are assumptions. Each improved,
  unchanged, or worse result in the library moves its kind's number (the prior
  counts as ten results).
- **Persists:** how much of a pattern like it would still be there a month later
  with nobody fixing it. Each full pass backtests every detector on its own 91
  days: at weekly gate days from four weeks back, every untouched pattern that
  passed a gate of 5 is followed for 28 days, and its after rate over its before
  rate (at most 2, and 0 where the work stopped) is one case. A detector's share
  is the mean case, with a prior counting as five cases (failures 0.45, CLI 0.85,
  outliers 0.9, documentation hosts and TL1 0.7, the rest 1), kept between 0.1
  and 1.

Cards explain the three shares under **Why likely**, and MCP returns them as
`estimate`. The context a change itself adds and overlap between findings that
share conversations aren't subtracted yet.

## The observability gate

A finding is shown only when a change could visibly move it within 30 days:

1. at least *threshold* affected conversations (or runs, or engagements) in the
   last 28 days;
2. at least one in the last 7 days;
3. at least five top-level conversations in its scope in the last 7 days;
4. spread over at least three days and three workspaces.

Mirrored copies (a Conductor session and the native session it wraps) count once,
and probe runs (Antigravity sessions in `/tmp`, test folders) are left out.

The threshold is a setting. The daily pass previews it at 3, 5, 10, 15, and 20:
how many findings would show, the typical wait for a result, and the share of
results expected to be clear (a baseline where a 70% drop would be significant).
Pharos recommends the smallest threshold of at least 5 where three in four
results would be clear within 30 days, and otherwise 10 for libraries with 300 or
more conversations a week and 5 below that.

## Measurement

Copying a prompt fixes the analysis plan:

- **Before:** the 28 days ending on the copy day (the change takes effect after
  the agent makes it).
- **After:** from the next day until it holds as much relevant work as the
  before window, at the recent rate of that work, between 7 and 60 days. The
  length is set at the copy and never changed.
- **Test:** for counts, a one-sided Fisher exact test on the units that did or
  didn't hit the pattern; for tokens per unit, a one-sided Mann-Whitney test.

**Improved** needs the after rate (or median) at most half the before one,
p < 0.05, and no guard more than 10% worse: tokens per conversation (with
sub-agents), the program's error rate for failures, and, for CLI friction,
engagements ending in success with help and usage errors both no higher.
**Worse** needs at least 1.5 times, p < 0.05. **Unchanged** is a completed window
with neither; **inconclusive** had less than half the before window's work.

Each result is worded as an observation and lists what else changed: the harness
version updating during the windows, other findings copied in the same scope, and
how the same pattern moved elsewhere (other repositories, or the other provider
in the same repository).

### Savings

Savings accrue weekly from the copy for 90 days, and only count toward the total
once a finding improves:

    saved = max(0, persists × before rate − this week's rate) × this week's relevant work
            × the before window's cost of one occurrence

*Persists* is the detector's share from the backtest (see [Ranking](#ranking)),
fixed in the plan at the copy: only the decline beyond what an untouched
pattern would keep counts. Plans copied before it was measured use one half. The context a change adds is subtracted: the growth of
the target's instruction files since the copy (from the loaded-instructions
record where the harness writes one, otherwise from the default branch or this
Mac's global files), times the requests of the sessions that read them, priced as
cache reads. A win whose pattern comes back (its rate over the weeks since the
result is no longer below half the baseline) reopens as regressed and keeps what
it saved so far.

## When it runs

Manual **Index changes** runs a full pass after refreshing tool rollups and
Human Words. Scheduled automatic indexing does not start findings work.
Capture & index and separate index actions retain the daily refresh policy:
a full pass on the first refresh after local midnight or a detector change,
and otherwise measurements of watched findings, wins still accruing, and
snoozes waiting for a pattern to get worse. **Refresh findings now** also
requests a full pass. Full discovery reuses durable conversation features while still evaluating all
candidates and measurement plans. See [the measured performance report](performance/incremental-analysis.md).

## Data

| Table | Written by | Holds |
| --- | --- | --- |
| `findings` | the pass | one row per candidate with at least 3 affected units, or with user state: scope, spec (what to measure), wording, metric, baseline, impact, gate numbers, evidence handles, extracted facts |
| `finding_aliases` | the pass | earlier IDs of each finding: detector key changes, repository findings folded into a global one, and scopes of repositories a merge retired |
| `finding_daily` | the pass | daily events, exposure, and cost per finding (and `<id>#elsewhere` for the same pattern outside its scope) |
| `finding_actions` | the user | dismiss, snooze, restore |
| `finding_cart` | the user | prompt targets and tick boxes |
| `finding_interventions` | the copy, then each index | the plan fixed at the copy, the result, savings, and regression |
| `finding_settings` | the user | threshold, ranking unit, handoff, per-repository handoff, last visit |
| `repository_retirements` | repository merges | retired repository IDs and their survivors |

A finding's ID is `detector:scope:hash`. IDs appear only in prompts and MCP.

## API, MCP, and CLI

```text
GET  /api/findings                  cards, carts, wins, savings, settings, threshold preview
GET  /api/findings/{id}             weekly chart, evidence, attempts, one-finding prompt
POST /api/findings/{id}/action      dismiss | snooze | restore | cart_add | cart_move | cart_remove | cart_tick | not_applied
POST /api/findings/cart/prompt      preview a target's prompt (starts nothing)
POST /api/findings/cart/copy        record the copy: starts measuring the ticked findings
POST /api/findings/settings         threshold, rank, handoff, repository_handoff
POST /api/findings/seen             the Findings view was opened
POST /api/findings/refresh          run the full pass now
```

MCP adds `list_findings(repository?, scope?, state?)` and `get_finding(id)`.
`get_finding` separates the **extracted facts** Pharos computed from the
**evidence** handles, which point into transcripts and are labeled as untrusted
content. Neither tool changes any finding's state.

`pharos findings --preview [--detector NAME] [--threshold N] [--verbose]` runs
every detector against a catalog opened read-only and prints the candidates with
their gate numbers, which is safe beside a running service. `pharos findings
--refresh` runs the full pass, and `pharos findings --json` prints the overview.

## Incremental detector inputs

`finding_tool_revisions` and `finding_message_revisions` track changes to
conversation inputs. SQL triggers update them in the authoritative transaction,
including deletions of a conversation's last call. `finding_feature_partitions`
stores detector inputs per conversation; `finding_feature_builds` records the
query, rule, and rolling-window version. Inputs include call groups, failures,
instruction hunts, pre-edit work, command shapes, documentation hosts, MCP
servers, and first user prompts. Prompt-only changes use their independent
message revisions; Human Words retains its separate input generation.

A pass reads context, user state, features, and revision tokens from one WAL
snapshot. Changed partitions are reconstructed from raw evidence. Immutable
Go inputs are reused only when their persisted revision and feature version
still match, including writes by another process. Malformed persisted JSON is
reconstructed for that conversation. Query/rule changes and window rollover
rebuild features; `runFindingsPassReference` bypasses them for parity audits.
The feature extraction version is `features-v2`; extraction changes must bump
it. A restart may reload persisted JSON and has a different cost from a warm
in-process refresh.

Cached inputs do not skip candidate discovery, thresholds, persistence
backtests, fixed intervention plans, savings, or regression decisions. Every
manual refresh still runs these stages even when source scans are unchanged.
Evidence traversal and equal-timestamp selections now use stable identity
ordering; equal-sized failure-family candidates are considered in sorted order.
This makes full and incremental passes reproducible while retaining the
existing preference for the candidate with more affected conversations.

Set `PHAROS_ANALYSIS_PROFILE=1` to print detector times. CPU profiles include
`analysis` labels for detector attribution. The scratch-replica harness and
full-reference comparisons are described in the performance report.
