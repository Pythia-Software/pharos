# Findings: evidence-backed optimization prompts

Design date: 2026-09-28. Status: proposal.

Pharos already shows where tokens, time, and errors go. This design turns the
largest, most fixable patterns into **findings**. Each finding is a specific,
evidenced recommendation that comes with a prompt an agent can act on. Pharos
then measures whether the problem went away after the prompt was copied, and
reports the result on a sparkline that marks the day of the intervention.

Every number below comes from the live library on `euclid` (57 GB catalog,
5.19M messages, 1.26M tool calls, 30 days ending 2026-09-28). The queries ran
read-only against the catalog while the service was running, so the timings
reflect a real, busy library.

## Goals and non-goals

Goals:

- Recommend only changes whose effect we can observe within 30 days.
- Say where each fix goes (the *lever*) and whether the user controls it.
- Hand off with one click: copying the prompt starts the measurement.
- Make findings easy to dismiss or snooze, and have them stay that way.
- Keep Pharos model-free. The user's own agent reads the evidence over MCP and
  writes the fix, and nothing leaves the machine.

Non-goals:

- Pharos editing repositories or instruction files itself.
- Findings that can't be measured, such as general style advice or "consider
  a better model". These can appear as *informational* at most.
- Real-time detection. Findings are batch output.

## Concepts

### Finding

| Field | Meaning |
| --- | --- |
| `id` | Stable across rebuilds: `detector:scope:signature-hash`. Dismissals and handoffs attach to it. |
| `detector` | Which detector produced it (see the catalog below). |
| `scope` | `repo:<repository_id>`, `global`, `automation:<script or TL1 flavor>`, or `provider:<name>`. |
| `lever` | Where the fix goes (see below). |
| `signature` | The normalized pattern, such as an error line, command shape, or file path. |
| `metric` | The one number verification tracks, with its exposure (the denominator). |
| `baseline` | Events, exposure, rate, and cost over the last 28 days. |
| `impact` | Tokens, API-equivalent dollars, and minutes over the last 28 days. |
| `evidence` | Up to 20 message and tool-call handles, newest first, for MCP to read. |
| `prompt` | Rendered handoff text. |
| `providers` | Event split by provider and model, which drives the choice between `CLAUDE.md` and `AGENTS.md`. |

### Levers and controllability

A finding names exactly one lever. The lever decides the prompt template and
whether verification is meaningful.

| Lever | Example target | Controlled by the user? |
| --- | --- | --- |
| `repo-instructions` | `AGENTS.md` and/or `CLAUDE.md` in the repository | Yes |
| `global-instructions` | `~/.claude/CLAUDE.md`, `~/.codex/AGENTS.md` | Yes |
| `repo-tooling` | A script, a Makefile target, a `--quiet` flag, a skill in the repo | Yes |
| `automation-prompt` | The prompt a `claude -p` / `codex exec` script or a TL1 flavor sends | Yes, but outside the repo |
| `harness-settings` | Claude Code hooks or permissions, Conductor instructions | Partly |
| `none` | Text the harness injects that never appears in the transcript (Claude Code's system prompt, ultra-mode orchestration instructions) | No. The finding is informational only |

Pharos can already tell who wrote the text around a finding: the authorship
index labels spans as `harness` ("Conductor system instruction"), `automated`
("Sent by a headless claude -p run", "Sent by a codex exec run", "Sent by TL1
orchestration"), `template`, and `typed`. In the last 30 days, Conductor
instructions came to about 526k tokens over 2,626 messages, and headless runs
sent about 1.79M tokens of prompts. If most of a finding's conversations
started from an `automated` span, the lever is the automation prompt, not
`AGENTS.md`. The TL1 `tl1m handoff --help` case below is an example.

Some harness text never appears in the transcript at all. Ultra-mode
orchestration instructions and the harness system prompt are examples. We only
see their effects (sub-agent trees, tool calls, guard messages), so the
finding's lever is `none` unless the instruction files can work around the
behavior. Claude Code's `Blocked: sleep N followed by …` guard is harness text,
but the agent that trips it keeps doing so, so a line in the global
instructions is still a valid lever.

### Lifecycle

```
            ┌────────── snooze (7d / 30d / until worse) ──────────┐
            ▼                                                     │
 candidate ──gate──▶ open ──copy prompt──▶ watching ──30d──▶ improved | unchanged | worse | inconclusive
            │         │                                           │
            │         └── dismiss (not real / not worth it / won't fix)
            └── below gate: kept for trend, never shown
```

- **Copying the prompt is the intervention.** Its timestamp is the
  intervention date, and we assume the fix takes effect then. The UI also
  offers "I didn't apply this" (back to open, date cleared). An MCP
  `get_finding` call from an agent counts as a copy, because that is the same
  handoff initiated from the agent's side.
- **Dismiss** takes an optional reason (`not real`, `not worth it`,
  `won't fix`). A dismissed finding comes back only if its detector version
  changes its signature.
- **Snooze** offers 7 days, 30 days, or *until worse*. *Until worse* brings the
  finding back when its 7-day rate exceeds twice the rate at snooze time.
- User actions live in a `finding_actions` table that rebuilds never touch.
  The table sits in the catalog, so it travels with the SSD and is included
  in backups.

## The observability gate

A finding is shown only if we expect to be able to see it move within 30 days.
The rule combines frequency and recency:

1. **Frequency:** at least 10 affected conversations in the last 28 days.
2. **Recency:** at least 1 affected conversation in the last 7 days.
3. **Live scope:** the scope has at least 5 top-level conversations in the
   last 7 days. A dormant repository can't show an effect.
4. **Mirrors counted once.** Conductor's wrapper and the native session it
   wraps are one conversation (`tool_mirror_workspaces`).

### Backtest

The backtest clustered error signatures over 120 days, applied the gate at day 30 using
days 30 to 60 as history, and checked the following 30 days:

| Gate at day 30 | Findings | Recurred at all | Recurred in ≥5 conversations |
| --- | --- | --- | --- |
| ≥3 conversations in window | 30 | 83% | 50% |
| ≥3, and active in last 14 days | 27 | 93% | 56% |
| ≥3, and active in last 7 days | 12 | 100% | 75% |
| ≥5 conversations | 17 | 94% | 65% |
| **≥10 conversations** | **7** | **100%** | **86%** |

Frequency predicts recurrence well, and recency adds precision at low counts.
The threshold of 10 also matches what verification can detect. With equal
exposure before and after, a baseline of 10 events that falls to 3 is
significant (conditional binomial, p ≈ 0.046). So the gate lets us call
reductions of about 60–70% within one 28-day window. Smaller baselines can
only show "inconclusive".

## Verification

### Metric and exposure

Raw counts are useless because activity swings widely. Top-level conversations
per week over the last 16 weeks ranged from 15 to 1,303. Every metric is
therefore a **rate over an exposure chosen for that detector**:

| Detector family | Metric | Exposure |
| --- | --- | --- |
| Error signature | conversations hitting the signature | conversations in scope that ran the same program or tool |
| Orientation tax | median tokens added before the first edit | top-level conversations in scope with an edit |
| Delegable exploration | carried tokens from pre-edit exploration | same |
| Context-heavy command | mean result tokens per call | calls matching the command shape |
| CLI or skill friction | help calls, usage errors, tokens per engagement (all three) | conversations that used the CLI |
| Cost outlier | share of scope tokens from conversations above the 95th percentile | total scope tokens |

Using "conversations that ran `pytest`" rather than "all conversations" as the
exposure keeps a quiet week, or a week of mostly UI work, from looking like a
fix.

### Verdict

After the intervention, compare the 28 days before with the 28 days after
(the post window can be shorter once it contains enough exposure):

- **Improved:** the post rate is at most half the baseline and a conditional
  binomial or Poisson rate test gives p < 0.05.
- **Worse:** the post rate is at least 1.5× the baseline, with p < 0.05.
- **Unchanged:** enough exposure, and neither of the above.
- **Inconclusive:** post exposure is below 50% of baseline exposure after 30 days.

The report states the effect in the finding's own units. For example (the
numbers are illustrative): "`pytest` ImportError: 15 of 31 conversations
before, 1 of 28 after, about 180k tokens a month saved."

### Sparkline

Show one point per week for the 12 weeks before and up to 8 weeks after,
plotting the rate (not the count), with a vertical rule on the copy date.
Weeks whose exposure is below 25% of the median are drawn hollow so a noisy
point doesn't read as a trend. Store the daily numerator and denominator so the
line costs nothing to draw:

```
finding_daily(finding_id, day, events, exposure, cost_usd)
```

### Confounders

The live data has a clear example. Claude's worktree guard ("this command is
too complex to verify that it stays inside the worktree") hit 90, 19, and 41
conversations in weeks 34 to 36, then 3, 0, and 0 in weeks 37 to 39. Nobody
intervened; the harness most likely changed. If someone had copied a prompt in
week 36, the report would have credited it. Mitigations:

- Record the **harness version** (Claude Code `version`, Codex CLI version) on
  each conversation. It isn't stored today. When the version changes in the
  window, the verdict gets a warning.
- Use a **comparison group** where one exists. For a repo finding, check the
  same signature in other repositories. For a provider-specific finding, check
  the other provider. A drop that shows up everywhere is attributed to the
  environment, not to the fix.
- Word verdicts as observations ("fell 94% after"), not as causes.

## Detector catalog

Numbers are for the last 30 days unless stated otherwise. Timings are
single-threaded reads of the live catalog.

### D1. Recurring failure signatures (repo or global)

**Signal.** Errored tool calls whose result has a salient error line,
normalized with the existing `tl1Signature` rules (ids, paths, and numbers
removed). Tracebacks and pytest collection errors use their *last* exception
line; a bare "Traceback (most recent call last)" matches every Python
failure and says nothing.

**Excluded:** results where a test ran and failed (`--- FAIL`,
`=== FAILURES ===`, `test result: FAILED`, assertion errors). A red test
means the tool worked. This removed 324 of 4,903 errored calls.

**Scope.** A signature seen in 3 or more repositories becomes one `global`
finding instead of several repo findings.

**Live results** (4,903 errored calls, 68% signatured, 1.3 s total):

| Finding | Scope | Conversations | Last seen | Gate | Lever and fix |
| --- | --- | --- | --- | --- | --- |
| zsh `[ a == b ]` → `(eval):N: == not found` (plus `===` and `=` variants) | global, 6–7 repos | ~77 | 09-27 | pass | global instructions: the shell is zsh, so use `=` or `[[ ]]` |
| `UnicodeDecodeError … byte 0x8b` (gzip read as UTF-8) | global, 3 repos | 61 | 09-27 | pass | instructions: these artifacts are gzipped, so use `gzip.open` |
| `Blocked: sleep N followed by …` (Claude harness guard) | global | ~45 | 09-27 | pass | global `CLAUDE.md`: poll with Monitor or background tasks |
| `zsh: command not found: python` | global, 5 repos | 18 | 09-23 | pass | global instructions: use `python3` |
| Read/StructuredOutput "input could not be parsed as JSON" | global | 24+15 | 09-27 | pass | lever `none`: informational |
| `pytest` "ImportError while importing test module" | tl1 | 15 | 09-20 | fail (recency) | repo: run as `PYTHONPATH=. pytest` |
| `gh pr create` "you must first push the current branch" | explo | 17 | 09-14 | fail (recency) | repo: push first or pass `--head` |
| `Error cleaning Git LFS object: operation not permitted` | explo, Codex only | 18 | 09-16 | fail (recency) | Codex-only, so `AGENTS.md` or sandbox settings |
| `SSL: CERTIFICATE_VERIFY_FAILED` from `xplo-perf-query` | explo, Codex | 10 | 09-10 | fail (recency) | repo-tooling: fix the script's CA bundle |

Each of these gives a one-line instruction and an observable metric. The rows
that fail recency show the gate doing its job: those errors have stopped,
perhaps because they were fixed, and a finding made now couldn't show an
effect. They stay as candidates and come back if they recur.

**Tuning noted.** 860 `nonzero_exit` calls had no salient line and are left
out. Generic lines such as `const err = new Error(message)` (Node stack frames)
need a denylist.

### D2. Fail-then-fix recoveries (evidence for D1)

**Signal.** A failed call followed, in the same conversation, by a successful
call of the same program with a changed command line. The difference between
the two is often the whole lesson: `pytest -q …` became `PYTHONPATH=. pytest
-q …`, and `gh pr create …` became `gh pr create --head <branch> …`.

This isn't its own finding. It attaches the corrected command to the D1
finding and puts it in the prompt ("agents recovered by …"). It also filters
D1: a signature whose recovery is always a different *test target* is ordinary
iteration, not a setup problem. Cost: 5.6 s over 30 days with a window
function over `tool_calls`.

### D3. Orientation tax and hot files (repo)

**Signal.** Tool calls and tokens added before a conversation's first edit,
and the files most often read in that phase.

| Repo | Conversations with an edit | Mean calls before first edit | Mean tokens added before first edit |
| --- | --- | --- | --- |
| explo | 486 | 31.4 | 97,864 |
| xlsx-exec | 71 | 32.6 | 81,766 |
| excel-corpus | 41 | 16.3 | 63,118 |
| alexandria (Pharos) | 93 | 17.0 | 48,607 |

In explo, `excel_helpers.rs` is read before the first edit in 54
conversations (243k tokens) and `rust_ast_translator.go` in 34 (283k).
`docs/RULES.md` and `docs/OVERVIEW.md` are read in 33 each because the
instructions ask for them; that is expected cost, not waste.

**Lever.** `repo-instructions`: a short map of where things live, with a
summary of the hot file's API so agents don't read it in full.
`repo-tooling` when the hot file is really too big.
**Metric.** Median tokens added before the first edit. 0.55 s.
**Prerequisite.** File paths need to be repo-relative. Today they include the
worktree root (`…/.conductor/copenhagen-v12/server/…`), which splits one file
into one entry per worktree.

### D4. Delegable exploration: sub-agent opportunities (repo)

**Signal.** Root-session read, search, and command calls before the first
edit. They are re-read as carried context by every later request. If a
sub-agent did that exploration and returned about a 3k-token summary, the
carried cost would mostly go away.

| Repo | Conversations | With ≥20 pre-edit calls | Carried tokens | Upper-bound saving |
| --- | --- | --- | --- | --- |
| explo | 486 | 332 | 2.95B | 2.47B |
| xlsx-exec | 71 | 41 | 410M | 339M |
| alexandria | 93 | 26 | 282M | 163M |

This is an upper bound: some of those results are used again after the first
edit. The tokens are mostly cache reads, so the dollar value is much lower
than the token count suggests. Show the dollar figure (from
`tool_usage_daily.context_cost_usd`) next to the tokens.

**Lever.** `repo-instructions` ("start with an Explore sub-agent for
questions spanning more than N files"), or `automation-prompt` for scripted
runs.
**Metric.** Carried tokens from pre-edit exploration per conversation. 0.58 s.
**Data gap.** One Claude orchestration conversation reports 1.35B tokens on
its root session, against 22M in its own requests. Investigation showed this
is a double count, not missing data: the child conversations already hold the
sub-agents' usage (see G4). Until G4 lands, the detector skips conversations
whose session total is more than 2× their request total.

### D5. Context-heavy command shapes (repo or global)

Carried context comes mostly from mid-sized results, not from rare huge ones:

| Result size | Calls | Share of carried tokens |
| --- | --- | --- |
| < 2k | 310,331 | 23.0% |
| 2–10k | 87,945 | 54.3% |
| 10–30k | 11,406 | 21.2% |
| 30k+ | 945 | 1.5% |

So this detector targets command *shapes* with a high average result and high
volume: `rg` without `-l` or a path limit, `git diff` without `--stat`, `sed -n`
ranges over 300 lines, and `cat` of generated files. In explo, `sed` alone
accounted for $1,002 of context cost in 30 days, and `rg` for $528. Codex
reads files with `sed`, so the lever is guidance on narrowing reads ("read by
symbol with `rg -n` first, then ranges of 120 lines or fewer"), and the
finding is scoped by provider.
**Metric.** Mean result tokens per call for the shape. 20 ms from
`tool_usage_daily`.

### D6. Cost outliers (repo)

The top 5% of conversations (160) used 52.2% of all tokens. The largest were
single Codex conversations of 450–570M tokens with 3,500–4,100 requests and
31–42 compactions, and Claude conversations with context peaks of 730k–930k
tokens. The single largest (1.35B) is the attribution gap described in D4.
Outliers become findings only when they share a cause the detector can name:

- **Runaway length:** more than 1,000 requests or more than 10 compactions.
  The lever is to split tasks, plus a checkpoint or handoff instruction.
- **Oversized context:** peak input above 500k. The lever is compacting
  earlier or delegating.
- **Unattributed usage:** see D4. This is informational and the lever is `none`.

**Metric.** The share of the scope's tokens from conversations above its own
95th percentile. Verification uses the share, not the count, because a single
outlier conversation is too few events for a count test.

### D7. CLI and external-tool friction → skill candidates (repo or automation)

**Signal.** Repo-specific CLIs (anything not on a list of common programs)
with high help-call or usage-error rates, or many calls per engagement.

| Program | Calls | Conversations | Help calls | Errors | Calls per conversation |
| --- | --- | --- | --- | --- | --- |
| `tl1m` | 1,525 | 615 | 801 (52%) | 114 | 2.5 |
| `excelvalidate` | 1,572 | 497 | 237 | 142 | 3.2 |
| `gh` | 1,635 | 684 | 124 | 47 | 2.4 |
| `xplo-perf-query` | 530 | 172 | 35 | 23 | 3.1 |
| `psql` | 740 | 91 | 40 | 16 | 8.1 |

`tl1m handoff --help` is the most common `tl1m` command. Those conversations
are TL1 tasks, so the lever is the TL1 flavor prompt (`automation-prompt`):
include the handoff schema so the agent doesn't have to look it up. For
interactive use, the lever is a skill.

**Metrics.** One number isn't enough, and each single metric
fails in its own way:

| Metric | Why it can mislead alone |
| --- | --- |
| Help calls per engagement | A skill can move the reading into the skill file, which is also context |
| Usage errors per engagement | Can drop because the tool is used less, not better |
| Tokens per engagement (all calls and results for that CLI in a conversation) | Rises when agents do *more* useful work with the tool |
| Calls per engagement | Same as tokens |
| Engagement success: last call in the engagement is `ok` | Hides retries in the middle |

The verdict needs **help rate and usage-error rate both down, with
engagement success flat or up**. Tokens per engagement is reported but doesn't
decide the verdict. An engagement is the calls to one CLI within one
conversation.

**Also check.** For MCP servers (`mcp_server`) and web hosts (`tool_urls`),
repeated fetches of the same documentation host lead to "vendor the reference
into the repo". These use the same metrics.

### D8. Recurring human corrections (repo), assisted by the user's agent

Typed text is small: 2,181 messages and 79k words in 30 days, roughly 110k
tokens. A regex pre-filter (`don't`, `instead`, `revert`, `I told you`,
`actually`, …) matched 474 of 3,403 typed messages in 60 days, but most
matches were ordinary feature direction. The durable ones read like
architecture rules: "we have quotas to ensure individual users can't flood our
service, and that's the right place for this rate limiting, not at a global
config level." A regex can't tell those apart from the rest, and a model can.

**Design.** Pharos doesn't classify. It emits one standing *digest finding*
per active repo: "N candidate corrections in the last 28 days". The prompt
asks the user's agent to read the candidates over MCP, keep only the durable
rules, check them against the current instruction files, and propose
additions. This finding can't be verified by counting, so it's labeled
**unverified**. Its proxy metric, candidate corrections per 100 typed
messages, is shown but doesn't produce a verdict.

### D9. Instruction and skill format drift (repo or global)

Each harness reads its own files. Claude Code reads `CLAUDE.md` and
`.claude/skills`, and Codex reads `AGENTS.md` and its own skill locations. A
repository written for one harness gives nothing to the others, and nothing
fails loudly: the agent just works without the rules, or spends tokens
looking for them.

**Live evidence.** On its default branch, `explo` has `CLAUDE.md` and three
skills in `.claude/skills`. It has no `AGENTS.md`, and `.codex/config.toml`
sets no fallback filename. In the last 30 days, 957 of its 1,200 top-level
conversations were Codex:

| Repo | Provider | Conversations | Searched for `AGENTS.md` | Read `CLAUDE.md` by hand | Read a `SKILL.md` by hand |
| --- | --- | --- | --- | --- | --- |
| explo | codex | 957 | 330 (34%) | 94 (10%) | 393 |
| explo | claude | 243 | 0 | 2 | 15 |
| xlsx-exec | codex | 125 | 55 (44%) | 3 | 62 |
| excel-corpus | codex | 95 | 40 (42%) | 21 | 55 |

In `explo`, Codex read Claude-format skills from `.claude/skills` in 186
conversations (about 1.06M tokens). Claude reads its own instructions without
a tool call. The typical searches are `rg --files -g 'AGENTS.md'` and
`find .. -name AGENTS.md`, sometimes repeated within the same conversation.

**Signals, strongest first:**

1. **Loaded-instructions record**, where the harness writes one. Claude Code
   writes an `instructions` attachment listing every loaded file with its type
   (`Project`, `User`, `AutoMem`); 737 of 846 Claude sessions in the last 14
   days have one, and 690 loaded a project `CLAUDE.md`. Codex writes
   `world_state.agents_md` and a `skills_instructions` developer message; the
   `agents_md` field was empty in all 1,038 Codex sessions in the last 14 days,
   which matches the repos having no `AGENTS.md`. Antigravity transcripts
   record nothing about loaded rules. Pharos doesn't keep any of this today.
2. **Instruction hunting:** tool calls that search for or read another
   harness's instruction or skill files (`AGENTS.md`, `CLAUDE.md`, `GEMINI.md`,
   `SKILL.md`, `.claude/`, `.agents/`, `.codex/`).
3. **A coverage matrix:** instruction files tracked on each repo's default
   branch, set against each provider's share of that repo's conversations. A
   provider with at least 20% of the conversations and no native instruction
   file is a candidate even without hunting evidence.
4. **Provider skew in other findings:** a D1–D5 finding where one provider
   accounts for more than 80% of the events, in a repo whose other harness's
   file already covers the rule. The prompt asks the agent to confirm that.

**Lever.** `repo-tooling`. Make one file canonical and point every harness at
it: a symlink, an import line in `CLAUDE.md`, or the harness's fallback
filename setting. Do the same for one skills directory. Which of these each
harness version honors must be checked, not assumed (see the canary probe
below). The prompt asks the agent to choose the mechanism the repo's harnesses
support, and to keep the canonical file short, since every harness now pays
for it on every session.
**Metric.** Instruction-hunting conversations per 100 conversations of the
drifting provider in that repo. Expected effect: 34% → near 0, which is
detectable within a week.
**Gate.** The standard gate, applied to the drifting provider's
conversations.

**Canary probe (for harnesses that don't record loaded files).** The only
reliable way to learn what `agy` reads is to ask it. An opt-in "Check
instruction coverage" action copies nothing from the repo. It creates a temp
directory with a `CLAUDE.md`, `AGENTS.md`, and `GEMINI.md`, each holding a
distinct harmless marker, and runs each installed harness headless once
("which markers do you see?"). The resulting matrix, harness × file, is stored
with the harness version and re-run when a version changes. It costs a few
cents per harness. Antigravity volume is too low to pass the gate today
(61 sessions, 57 of them in `/tmp`), so until it grows the probe is the only
useful signal for `agy`.

### Deferred

- **Agent realizations** ("I hadn't realized", "I misunderstood"): only 32
  and 9 matches across all history. Too sparse to pass the gate. Include them
  as evidence in D8 digests instead.
- **Cheaper-model fit:** needs task-similarity matching and outcome labels.
  Informational at best.
- **Cache reuse:** TL1 already has this detector per flavor. Generalize it
  later.

## Handoff

### Prompt

Every prompt has the same outline, so the agent's job is always the same:

```
Pharos finding <id> (<scope>, <lever>).
What: <signature>, in <n> of <exposure> conversations over 28 days, <impact>.
Agents recovered by: <D2 corrected command, if any>.
Evidence: call get_finding("<id>") on the Pharos MCP server, and read 3–5 of
the linked calls before deciding.
Change: <lever-specific instruction, e.g. "a minimal addition to AGENTS.md
(and CLAUDE.md, if Claude also works in this repo), under 5 lines">.
Rules: paraphrase, and never paste transcript text, paths from other
machines, or secrets. If the evidence shows this is not a real problem, say
so and change nothing. Open a PR; do not commit to the default branch.
```

A templated prompt that tells the agent to fetch its own evidence stays short
(about 150 tokens) and keeps transcript excerpts off the clipboard.

### MCP

Add two read-only tools alongside the existing ones:

- `list_findings(repository?, scope?, state?)`: compact cards, open findings
  by default.
- `get_finding(id)`: the baseline, the provider split, the D2 recoveries, and
  up to 20 evidence handles to read with `get_conversation_messages`. Calling
  it records a handoff unless one is already recorded.

A `/pharos-optimize` skill can then be one line: "call `list_findings` for
this repository, pick the finding with the highest impact, and follow its
prompt."

## When to run

Measured costs of one full pass on this library:

| Step | Time |
| --- | --- |
| D1 signatures (fetch error results, classify) | 1.3 s |
| D2 recoveries | 5.6 s |
| D3, D4 (pre-edit phases) | ~0.6 s each |
| D5, D7 (rollup and ledger aggregates) | 0.02–0.5 s |
| D6 (per-conversation aggregates) | 0.5 s |
| D8 candidate extraction | 0.2 s |
| **Total** | **~10 s**, all reads, no writes to source tables |

A pass is cheap next to what already happens after indexing: the tool rollup
rebuild alone reads every tool call and takes about 30 s. Refreshing findings
too often isn't a performance problem; the concern is that the list changes
under the user.

| Trigger | Pros | Cons |
| --- | --- | --- |
| After every index | Always current | Indexing runs every 30 minutes, so the list churns and baselines shift |
| Timer (daily) | A stable list with a predictable cadence | Misses a burst of new evidence until tomorrow |
| After a *significant* sync (for example, ≥50 new conversations since the last pass) | Tracks real change | A threshold to tune; a small library may rarely cross it |
| User action ("Refresh findings") | Fully deliberate | Easy to forget, so verification data goes stale |

**Recommendation.** Follow the existing generation-keyed pattern
(`ensureToolRollup`, `ensureAuthorship`). Compute a findings generation from
`tool_ledger_generation`, `authorship_generation`, the detector version, and
the local day. The first index after local midnight rebuilds the findings, and
so does the "Refresh findings" button. Every index updates **only
`finding_daily` for findings in `watching`**, a few indexed queries, so
verification sparklines stay current without new findings appearing at
random. Run the pass on the service's background executor after the tool
rollup, so it reads a fresh cube.

## Infrastructure: what exists and what's missing

Already in place:

- The tool ledger (`tool_calls`, `tool_commands`, `tool_urls`) with statuses,
  error types, context added and carried, and per-call durations.
- Daily rollups and the cube (`tool_usage_daily`, `tool_call_cube`) for fast
  aggregates.
- Mirror suppression, so Conductor and native sessions aren't counted twice.
- Authorship spans with provenance (typed, harness, automated, template, …).
- The TL1 detector pattern (`tl1_insights.go`): id, severity, impact,
  evidence, filter, prompt. Findings generalize it.
- `tl1Signature` normalization, reusable for D1.
- The generation-keyed background rebuild pattern and the MCP server.

Missing or needing change. Each item below was traced to code and checked
against the live catalog. Items 1–4 share one ledger rebuild.

### G1. Error signature and test failures (ledger `tools-v3`)

`classifyToolOutcome` (`tool_ledger.go:761`) keeps only the status and error
type. The result text is read (`toolResultText`, `:517`) and then thrown away.

- Add `unwrapToolOutput`, which strips Codex's `Chunk ID…/Wall time…/Output:`
  and `Script completed…` headers, Claude's `Exit code N`, and
  `<tool_use_error>` tags. Then add `toolErrorSignature`, which picks the
  salient line (last exception line, `panic:`, `--- FAIL`, `file:line:`,
  `error:`/`fatal:`, `command not found`) and normalizes it through
  `tl1Signature`. Wrap `tl1Signature` rather than changing it, because TL1
  clusters depend on it.
- Store `error_signature` (with a partial index) and `test_failure` on
  `tool_calls`, adding them through the ALTER list in `catalog.go:173`. Set
  `test_failure` from test-command segments plus failure markers, and leave
  `status` alone. About 3,750 test calls marked `ok` contain FAIL markers,
  usually because the output was piped through `tail`.
- **Fix two misclassifications at the same time.** About 12,700 `Tool
  permission request failed: Error: Stream closed` errors and 603
  `<tool_use_error>Blocked: …` results are recorded as `nonzero_exit` or
  `tool_error`. Adding harness needles (`harness_error`, `hook_blocked`)
  fixes both. D1 and the Tools tab's error rates are both skewed until then.

### G2. Repo-relative file paths (`tools-v3`)

314k of 334k stored paths are absolute worktree paths (`.conductor/<name>/`,
`conductor/workspaces/<repo>/<name>/`, `.task-worktrees/`,
`.claude/worktrees/`). `change_files` is already repo-relative, through
`conductorRelativePath` (`adapter_conductor.go:608`).

- Generalize that into a pure-string `repoRelativePath(path, cwd, roots)`
  that matches the known worktree layouts first (rightmost match wins, for
  nested worktrees) and then the longest known repository location. It never
  touches the filesystem, so it works on captures from other Macs.
- Store `repo_path` and `path_repository`. The second matters after a `cd`
  into another repo. Temp and home paths are left NULL, with a scope.
- Compute it in a post-pass in `replaceToolLedger`, so `buildToolLedger`
  stays pure.

### G3. Shell lexer (`tools-v3`)

`shellWords` (`shell_command.go:200`) splits on every unquoted space and
doesn't know about `$(`, `$((`, `${`, or backticks. `T=$(mktemp -d)` becomes
the words `T=$(mktemp`, `-d`, …, and `-d` becomes the program. 16,185 commands
have a program starting with `-`. The `TrimSuffix(")")` at `:331` hides more
cases (`create` 2,173, `%s` 940, `rev-parse` 590). Subshells like
`(cd x && go test)` are recorded as `cd`.

- Treat substitutions as atomic words, with a `matchSubstitution` helper that
  tracks nesting and quotes and falls back to the old behavior when the
  command was truncated. Optionally recurse into assignment-only segments
  (`SHA=$(git rev-parse HEAD)` → `git rev-parse`) while still preferring
  non-assignment segments as the primary command, and re-parse `( … )`
  subshells.
- `TestParseShellCommand` has no substitution cases today. Add the ~13
  cases from the investigation, plus a ledger test that no program starts
  with `-`.

### G4. Sub-agent usage attribution: a double count

The earlier "missing sub-agent usage" is really a **double count**. The
El Paso root's 1.35B is Claude Code's whole-process `cost-state` total. Its
114 sub-agent transcripts are separate child conversations that already
account for 1.32B. `conversationTokenReports` (`token_usage.go:269`) treats
`cost-state` as a session report, subtracts only usage from the same
conversation, and books the remainder on the root as `session-start`. The
workspace total becomes 2.66B, about twice the real figure.

| Source (30 days) | Roots | Root session > 2× its requests | Excess | Cause |
| --- | --- | --- | --- | --- |
| Claude, native | 1,123 | 6–8 | ~2.6B | `cost-state` double count (11 roots with children account for almost all of it) |
| Claude via Conductor | 442 | 247 | ~4.4B | Conductor emits one event per content block, each repeating `usage`, with no `message.id`, so none are deduplicated |
| Codex via Conductor, Codex TL1 | 1,356 | — | — | Aggregate-only usage, with no per-request data |

The Conductor copies are nearly all suppressed as mirrors, so the Usage
totals mostly avoid that overcount. The native-Claude double count is not
suppressed, and very likely inflates Usage by about 2.6B tokens over the last
30 days: roughly 20% of native Claude tokens, concentrated in a few
orchestration-heavy sessions. **This is a bug in numbers Pharos already
shows, independent of findings.**

Fix:
- Ignore `cost-state` as a usage source when request-level usage exists.
- Compute a cross-conversation remainder (claim minus requests over the root
  and its children, clamped at 0) after the whole session group is ingested.
- Link delegation sessions to their child conversations through the
  `agentId` and `runId` already stored in `delegation_result`, instead of
  leaving them as zero-token sub-agents.
- Deduplicate Conductor's id-less assistant events the way the ledger does.
- Add a Health check comparing session totals with request sums.
- Aggregate-only sources get a label ("session totals only"), not a flag.

No reparse is needed; everything is in stored messages. The Usage numbers for
affected workspaces will drop, which is the intended correction.

### G5. Harness version per conversation

Nothing is stored today.

- Claude lines carry `version` and `entrypoint`. 4.5% of recent transcripts
  span two versions.
- Codex `session_meta` carries `cli_version` and `originator`.
- Conductor and Antigravity have no per-conversation version.

Add `harness`, `harness_version_first`, `harness_version_last`, and
`harness_version_source` to `conversations`, using `omitempty` record fields
so unchanged records don't rewrite.

Backfill:
- **Claude:** SQL only. The raw usage events are already in
  `messages.raw_text`.
- **Codex:** read only the first lines of each rollout (live or captured),
  or bump the Codex-only extractor.
- **Conductor:** inherit the version from its native-alias peer.
- **Antigravity:** the installed app version, labeled `installed-app`, and
  only for live indexing.

### G6. Repository identity across renames

Pharos alone has four repository rows: `gbdubs/alexandria` (227 workspaces),
a remoteless `alexandria` (57), `gbdubs/pharos` (1), and `Pythia-Software/pharos`
(4). All three GitHub slugs resolve to the same repository, and every clone
shares root commit `682e134`. This isn't specific to Pharos: 94 repository
rows share 44 display names (for example, `excel-corpus` has six across
ssh/https and two owners), because `canonical_remote` is the raw origin URL
and `upsertRepository` never merges rows.

Fix:
- Normalize remotes to `host/owner/name`.
- Add `root_commit`, computed when a clone is available, and an optional
  `forge_id` from `gh api`, looked up in a background job.
- Make `upsertRepository` merge by, in order: forge id, then normalized
  remote, then root commit with the same host and owner lineage, then a
  shared local location. Union aliases and locations instead of overwriting
  them.
- Add a `[repositories.aliases]` override in `library.toml` for renames
  with no clone left.
- Don't merge on root commit alone: forks and TL1's stress-test `origin.git`
  copies share roots.

The backfill is DB-only, plus one `git rev-list` per known location and one
`gh api` call per slug. The Library cache has to be invalidated for the
re-pointed workspaces.

### G7. Loaded-instructions record (for D9)

Claude's `instructions` attachment and Codex's `world_state.agents_md` and
`skills_instructions` say which instruction files and skills a session
loaded, but Pharos doesn't keep them. Store them in
`conversation_instructions(conversation_id, harness, path, kind, bytes,
hash)`. That also gives the per-session token cost of each instruction file,
the "every line costs tokens" figure findings need.

### Order

| Order | Work | Blocks | Why this order |
| --- | --- | --- | --- |
| 1 | G4 usage attribution | D4, D6, and the Usage tab today | Fixes numbers users already see. Small, DB-only backfill. |
| 2 | G1 + G2 + G3 as ledger `tools-v3` | D1, D2, D3, D5, D7 | One rebuild (about 1.5–2.5 hours at the last backfill's rate). The Tools tab benefits on its own. |
| 3 | G5 harness version | Verdicts (confounders) | Cheap. Must land before the first verdicts, which are 30 days after the first handoff. |
| 4 | G6 repository identity | Verdicts (continuity), repo scoping | Riskier merge logic. Start with remote normalization and the config aliases; add root-commit merging after that. |
| 5 | G7 instructions record | D9 signal 1 | D9 works on signals 2–4 without it. |

Findings work (phase 2 of the rollout) can start once items 1 and 2 land.
Items 3 and 4 only need to land before findings start producing verdicts.

### Status after the prerequisites merged (#25–#28)

G1–G6 are merged. The live library hasn't been rebuilt; it waits for the next
major release. None of the four PRs could read the live catalog during
development, so they were validated afterwards, in memory and read-only:

| Area | Result | Follow-up needed |
| --- | --- | --- |
| G4 usage attribution | Double count removed. El Paso goes from 2.664B to 1.457B, but should be about 1.347B | **Yes, before release.** The `cost-state` claim is keyed `claude-opus-5-5[1m]` while requests say `claude-opus-5-5`, so the whole Opus claim becomes "remainder". Across 89 recent workspaces with sub-agents: 1.68B of remainder, of which 1.67B is this mismatch and 8.9M is real side calls. Match models through `modelFamily` (`usage_table.go:276`). |
| G1 signatures | 98% of 1,582 errored calls get a signature (1,500 conversations, 57,525 calls) | Low-information lines become signatures: `{`, `<path>`, `usage:`, `Validation failed:`, and `node:internal…` stack frames. Add them to the denylist and continue to the next salient line. D1 must exclude `test_failure` calls. |
| G1 test failures | 635 flagged, 439 of them previously recorded as `ok` | None |
| G3 lexer | Programs starting with `-`: 1 in the sample, down from thousands | None |
| G2 paths | 92% of absolute paths resolve; the rest are temp, home, or external | `path_repository` stores the display name at build time, so the repository merge must run **before** the ledger rebuild. Consider storing the repository id. |
| G5 harness | Claude: 297 of 300 sampled conversations have a version in stored messages. Codex: 87% of sampled source files still exist locally (captures cover more) | The version change alters record digests, so the first index after upgrading re-ingests every discovered workspace. The cost on the live library is unmeasured. |
| G6 repositories | The dry run works on the current catalog: 21 sensible groups, and the `origin` rows stay separate | **Yes.** Remoteless rows aren't attached: `excel-corpus` (10,323 workspaces), `explo-candidate-v2` (2,631, which is the `explo` repo), `explo` (2,447), and the remoteless `alexandria` (57; its Conductor directory is claimed by two rows that end up in the same group, so the ambiguity check should run after grouping). Resolve remoteless rows at a known clone path through that clone's root commit and remote. |

Fixed on this branch after that check:

- **G4:** claims and requests are matched by model family, and the repair
  version is bumped so any catalog repaired by the buggy build is redone.
  Fresh ingests now record that their attribution is current, so the repair
  (and the upgrade) skips them.
- **G1:** low-information lines are skipped, short labels are joined with
  their next line, and one-byte hex values (`0x8b`) are kept.
- **G6:** rows without a remote attach to the one merge group whose clone
  path, worktree, or Conductor directory they sit in, with ambiguity judged
  per group. The live dry run now makes 24 groups: Pharos has 4 rows,
  `excel-corpus` takes in its 10,323-workspace row, `explo` takes in
  `explo-candidate-v2` and its remoteless row, and no `origin` row merges.
- **G2:** a worktree's repository comes from known checkouts before its
  directory name, the ledger stores the repository ID as well as the name,
  and merging repositories re-points both.
- **Ledger version:** these G1 and G2 changes bump the ledger to `tools-v4`,
  so a library that already built `tools-v3` rebuilds it.
- **Upgrade flow:** one in-app, resumable **Upgrade this library** job runs
  the repository merge, usage repair, harness backfill, ledger rebuild, and
  rollup, in that order (`library_upgrade.go`, `assets/upgrade.js`,
  `pharos upgrade`). Merges record retired repository names, and the UI
  rewrites saved table views that filter on them.

### Upgrade rehearsal (2026-09-28)

The full upgrade ran on a snapshot of the live catalog (57 GB, copied with
SQLite's backup API to the internal SSD in 99 seconds). The live library was
only read.

| Step | Work | Time |
| --- | --- | --- |
| First open (schema migrations) | schema 8 → 9, new columns and indexes | 17 s |
| Merge repository identities (with GitHub lookups) | 24 groups; 94 → 49 rows | 47 s |
| Correct token attribution | 9,142 workspaces | 21 min 48 s |
| Record harness versions (+ Conductor aliases) | 22,651 conversations | 38 s |
| Rebuild the tool ledger | 22,651 conversations, 1.26M calls | ~23 min 30 s |
| Rebuild the Tools rollup | | ~20 s |
| **Total** | | **~47 min** |

The ledger step found a panic in the new worktree resolution (a worktree root
path sliced past its end). It is fixed with a regression test, and all 95,588
distinct stored file paths now resolve without error.

Results against the live catalog before the upgrade:

| Check | Before | After |
| --- | --- | --- |
| El Paso orchestration workspace | 2,663M tokens | 1,346M |
| Native Claude tokens, 30 days | 14,146M | 11,532M (−18.5%) |
| Codex and TL1 tokens, 30 days | 17,618M / 2,726M | unchanged |
| Repository rows | 94 (Pharos in 4) | 49 (Pharos in 1; 10 `origin` rows kept) |
| Calls with a program starting with `-` | ~6,900 | 97 |
| Errored calls with a signature | none | 56,286 of 58,364 (96%) |
| Test failures flagged | none | 7,918 (4,023 had looked successful) |
| Repo-scoped paths with a repository ID | none | 319,712 of 320,152 |
| `harness_error` / `hook_blocked` | counted as command failures | 12,664 / 603 |
| Conversations with a harness version | none | Claude 2,515 of 2,527; Codex all 5,118; Conductor 2,428 of 6,460 (those with a native copy); TL1 none |

Harness-version warnings in verdicts therefore cover native and linked
Conductor work but not TL1 runs or Conductor sessions without a native copy.
The snapshot is kept at `~/pharos-rehearsal` as the upgraded development
catalog for the findings work.

## Rollout

1. **Groundwork:** G4, then ledger `tools-v3` (G1–G3). See Order above.
2. **D1 + D2 + D7 + D9**, the tables, the Findings view, copy-as-handoff,
   dismiss and snooze, and `list_findings`/`get_finding`. These detectors
   have the clearest metrics and the most gate-passing findings today. D9's
   `explo` Codex case alone would be a strong first finding.
3. **Verification:** `finding_daily`, the sparkline, verdicts, and
   comparison groups. G5 and G6 must have landed.
4. **D3–D6.**
5. **D8 digests, G7, and the canary probe.**

## Open questions

- Should a global finding copy one prompt that edits both global files, or
  one prompt per provider?
- Do automation-prompt findings for TL1 belong in the TL1 tab (next to its
  existing concerns) or in Findings?
- Is 28 days before and after right for low-volume repositories, or should
  the post window extend until it has the baseline's exposure (up to 60
  days)?
- Should findings be computed per Mac or only per library? Global findings
  like zsh `==` may be specific to one machine's shell.
