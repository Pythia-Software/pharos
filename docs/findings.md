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

- **Findings tab.** Open findings are ranked by the unit you choose in settings:
  dollars (API-equivalent, the default), tokens, agent time, or failures. Every
  card shows all four. The tab's badge counts findings that appeared since your
  last visit.
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

    saved = max(0, ½ × before rate − this week's rate) × this week's relevant work
            × the before window's cost of one occurrence

The factor of one half is fixed: untouched patterns tend to fade to about half
their rate on their own. The context a change adds is subtracted: the growth of
the target's instruction files since the copy (from the loaded-instructions
record where the harness writes one, otherwise from the default branch or this
Mac's global files), times the requests of the sessions that read them, priced as
cache reads. A win whose pattern comes back (its rate over the weeks since the
result is no longer below half the baseline) reopens as regressed and keeps what
it saved so far.

## When it runs

The full pass runs on the service's background executor after the tool rollup,
on the first index after local midnight, when the detectors change, or when you
choose **Refresh findings now**. Every other index measures only the findings
being watched, the wins still accruing, and snoozes waiting for a pattern to get
worse. On a 57 GB library the full pass takes about a minute and only reads the
source tables.

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
content. Neither tool changes any finding's state. The MCP page offers a short
`pharos-optimize` skill that works through them.

`pharos findings --preview [--detector NAME] [--threshold N] [--verbose]` runs
every detector against a catalog opened read-only and prints the candidates with
their gate numbers, which is safe beside a running service. `pharos findings
--refresh` runs the full pass, and `pharos findings --json` prints the overview.
