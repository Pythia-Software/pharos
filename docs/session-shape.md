# Session shape and forensics

The `session_shape` dataset describes what single sessions look like and
which of them went badly, in what way, and where. It feeds an Analyze
catalog like any other ([dashboard analysis](dashboard-analysis.md)).
Everything is computed deterministically from data Pharos already indexes:
message timestamps, Human Words authorship ([human authorship](human-authorship.md))
and the tool ledger ([tool analytics](tool-analytics.md)). It makes no
model calls and uses no new classifiers.

There is no single score. Each signal stands alone and is read against the
user's own history. A combined effectiveness score is deferred until each
signal has been validated against outcomes.

## The dataset

One row is one **agent session**: one recorded conversation. A delegated
sub-agent run is its own row. Each row carries `root_conversation_id`, the
top-level conversation Usage resolves for it, so a catalog unit can roll
sub-agents up to their root.

- **No duplicates.** Mirrors and aliases are removed as Parallelism removes
  them. By workspace, `suppressMirrors` prefers native transcripts over
  Conductor's. By identity link, only the canonical conversation of
  `usageConversationRoots` is kept, which covers native aliases. Inactive
  conversation-group members are left out (`currentGroupConversation`).
- **Delegation.** `agent_depth` follows Parallelism. A run another
  conversation launched counts as delegated even at recorded depth 0.
  `agent_kind` is `root` or `subagent`.
- **Own messages only.** Messages and calls owned by an embedded sub-agent
  (an `agent_sessions` row other than `main`) belong to that agent, not to
  the conversation. Its context samples are excluded the same way.
- **Dimensions.** These have the names and meanings other datasets use:
  - `provider`, `harness`, `model` and `model_family`, `effort`: the
    conversation's own, or its main agent session's;
  - `repository_name`, `workspace_id` and `title`: the session's own
    workspace;
  - the origin fields (`channel`, `machine`, `execution_machine` and their
    IDs, kinds and statuses);
  - `started_at`: the first timestamped message;
  - `day`, `week` (Monday) and `month`: local dates of `started_at`.
- **Tokens and cost.** `input_tokens`, `output_tokens`, `cost_usd` and
  `price_status` are the session's Usage rows summed. Nothing is re-derived.

### Shape fields

All times come from message timestamps.

| Field | Definition |
|---|---|
| `messages` | The session's own messages: prompts, responses, reasoning, tool calls and tool results. Metadata events (usage, compaction markers) are left out. Pharos stores each tool call and each result as its own message. |
| `typed_human_messages` | User messages holding typed text (`typed_words > 0`, the Human Words `typed_message`). Pasted, quoted, re-sent, template, harness and automated text does not count. |
| `tool_calls` | The session's own ledger calls. |
| `wall_seconds` | Last message time minus first. |
| `active_seconds` | The sum of gaps between consecutive messages, counting only gaps of 300 s or less. A longer gap counts as zero. |
| `idle_seconds` | `wall_seconds − active_seconds`. |
| `active_hours` | `active_seconds / 3600`, for sums. |
| `autonomy`, `autonomy_prompts` | `tool_calls / max(1, typed_human_messages)`, and its denominator, so the catalog pools Σ tool calls ÷ Σ prompts. |
| `turn_cycles`, `turn_cycle_p50_seconds`, `turn_cycle_p90_seconds` | For each typed human message, the gap to the next assistant message (any assistant kind: prose, reasoning or a tool call). A user prompt in between voids that turn. Gaps over 1,800 s, and gaps of zero or less, are excluded. Percentiles are nearest-rank. Null when no turn qualifies. |
| `first_response_seconds` | The same gap for the first typed human message only. |
| `max_unprompted_tool_calls` | The longest run of tool calls with no typed human message between them. |
| `files_edited` | Distinct `file_path`s of edit-category calls that did not fail. |
| `subagents` | Delegated runs the session launched: embedded and linked sub-agent sessions (`agent_sessions` other than `main`), plus headless runs its shell commands started (`conversation_launches`). |
| `compactions` | Compaction markers, as the Library's `compaction_count` counts them: Claude's `compact_boundary`, and Codex compactions the adapter attributed to a turn (`compaction_trigger`). |

### Bands

The bands are fixed, ordered labels, so the share of sessions in each band
is comparable from week to week.

| Field | Bands | Source |
|---|---|---|
| `length_band` | messages 1–5, 6–15, 16–30, 31–60, 61–120, 121+ | AgentsView `LengthBucket` |
| `active_band` | active time <5 m, 5–15 m, 15–30 m, 30–60 m, 1–2 h, 2 h+ | AgentsView `DurationBucket` |
| `tool_band` | tool calls 0, 1–5, 6–20, 21–50, 51–100, 101+ | Splitrail `SESSION_SHAPES.toolCalls` |
| `autonomy_band` | autonomy <0.5, 0.5–1, 1–2, 2–5, 5–10, 10+ | AgentsView `AutonomyBucket` |
| `archetype` | Automated (no typed human message); otherwise by typed human messages: Quick ≤5, Standard ≤15, Deep ≤50, Marathon >50 | this document |

AgentsView's autonomy is assistant messages with a tool use per user
message. Pharos's is tool calls per *typed* message, which runs higher, but
the edges are kept so the bands read the same way. A session with no
messages of its own (metadata only) has no row.

### Forensic signals

Each signal is a count, with the messages that triggered it as evidence
(see [Evidence](#evidence)). In the definitions:

- a **turn** runs from one typed human message to the next. Work before the
  first one, prompted by pasted or automated text, is a turn too.
- a **verification** is a shell call (`tool_category` `command`) whose
  `command_category` is `test` or `build`, or that the ledger marked as a
  test failure (`test_failure`). Package scripts named `test`, `lint`,
  `check` or `typecheck`, and `go vet`, are `test` in that taxonomy. Read,
  search, list and version-control commands (`cat`, `rg`, `ls`, `git log`)
  are not verifications, and nor is anything else.
- an **edit** is an edit-category call with a `file_path` that did not fail.

| Signal | Definition |
|---|---|
| `failed_calls` | Calls with `status` `error` whose `error_type` is not `user_rejected`, `hook_blocked` or `interrupted` (and not flagged `interrupted`). `harness_error` and `timeout` count as failures. |
| `rejections`, `interrupts`, `hook_blocks` | The excluded cases, counted separately: `user_rejected`; `interrupted` or the interrupted flag; `hook_blocked`. |
| `failure_streak_max` | The longest run of consecutive failed calls. Any other call ends a run. Evidence: the longest run's calls (`failure_streak`), when it is two or more. |
| `identical_retries` | Runs of 3 or more consecutive calls with the same tool name and byte-identical call text (the stored `{tool, input}` envelope, compared by its content hash). Each run adds count − 1. Evidence: every call after a run's first. |
| `thrash_retries` | Within one turn: an edit to file F, then a verification, then another edit to F. Each such edit counts once. Evidence: the second edit. |
| `edit_turns`, `one_shot_edit_turns` | Turns with an edit, and those with no thrash retry. The one-shot rate pools the two. |
| `churned_files` | Files with 3 or more edits whose message positions fall within a span of fewer than 10. The cluster then extends over following edits while the span stays under 10 (AgentsView `churnCluster`). One per file. Evidence: the first cluster's edits. |
| `mid_task_compactions` | Compactions where at least 2 distinct tool names appear both among the 10 tool calls before the marker and among the 5 after it. Evidence: the compaction marker. |
| `context_peak_tokens`, `context_window`, `context_peak_ratio` | The largest request input of the session's own stream (input including cache reads and writes: the occupancy the reader's context rail plots), the window, and their ratio. The window is the one the rail uses (`contextWindow` in `reader-model.js`). That is the reported `model_context_window` (Codex). Failing that, a Claude-family model (by name: claude, opus, sonnet, haiku, fable) gets 200K, or 1M when the model says `[1m]` or context grew past 200K. Otherwise the window is unknown and the ratio is null, never zero. |
| `context_pressure` | 1 when `context_peak_ratio` exceeds 0.9 (AgentsView's `HighContextPressure`), else 0; null with an unknown window. Evidence: the first assistant message at or after the peak request. |
| `corrections` | Typed human messages with a Human Words `correction_cue`: a correction phrase inside a typed span. A cue in pasted or quoted text does not count. Steering load pools `corrections / typed_human_messages`. |
| `verified_handbacks`, `edit_handbacks` | A handback is the last assistant message of a turn: its last prose message, or its last assistant message of any kind when it has no prose. Edit handbacks end turns with an edit. A verified handback had a verification after the turn's last edit. Verification discipline pools the two. Evidence for `unverified_handbacks`: the handback message. |
| `ended_on_failure` | Experimental. 1 when the session's last tool call is a failed call and no typed human message follows it. Shown as "Ended on a failed call", never "abandoned": a person may have finished elsewhere. |
| `long_unprompted_runs` | Runs of 150 or more tool calls with no typed human message, AgentsView's runaway threshold (`IterationRunawayMinToolCalls`). Evidence: the 150th call. |
| `forensic_events` | The sum of the counts above that carry evidence: failed calls, identical and thrash retries, churned files, mid-task compactions, context pressure, corrections, interrupts, rejections, hook blocks, unverified handbacks, an ended-on-failure and long runs. The streak is left out because its calls are already failed calls. This sum is only a sort key for finding sessions to read, not a score. |

### Coverage

Each row records which evidence it had:

| Flag | True when | Signals null without it |
|---|---|---|
| `has_timestamps` | at least one of the session's messages has a timestamp | wall, active and idle time, turn cycles, first response |
| `has_authorship` | Human Words classified the session's user prose messages, or it has none | typed messages, corrections, archetype, autonomy, turns (and so thrash, edit turns, handbacks, long runs, ended on a failed call) |
| `has_tool_data` | the ledger holds the session's calls, or it has no tool-call messages | every tool signal |
| `has_context_window` | a peak context and a window are both known | context share and pressure |

A signal without evidence is null, never zero. The catalog's rates take
each measure's coverage as an implied population, and the dashboard shows,
for each signal, how many sessions had enough evidence. A session without
enough evidence is reported as such, never counted as healthy.

### Where the caps come from

- **300 s active gap.** AgentsView's `ActiveGapCapSec` and Splitrail's
  under-5-minutes rule. AgentsView caps a longer gap *at* 300 s, while
  Splitrail and Pharos count it as zero. A long gap is a person away, not
  300 s of work.
- **1,800 s turn cycle.** AgentsView's `processSessionVelocity` cap. A longer
  wait for the first response is someone away from the keyboard, not the
  loop's speed. Habits uses its own, configurable engaged gap.

### What each signal can and cannot tell you

- **Failed calls and streaks** show friction the agent ran into. They cannot
  tell an expected failure (a test written to fail first) from a real one.
- **Rejections, interrupts and hook blocks** are a person or a guard
  stopping the agent. They are counted separately because they are not the
  agent failing.
- **Identical retries** catch an agent repeating itself verbatim. A
  deliberate poll (rerunning a status command) looks the same.
- **Thrash retries** catch edit-test-edit loops on one file. They miss loops
  that run tests through `npx`, a script runner or a linter the command
  taxonomy files under `run` or `other`.
- **Churn** catches repeated edits to one file in quick succession. Positions
  count messages, and Pharos stores a call and its result separately, so the
  span is in messages, not calls.
- **Mid-task compactions** suggest compaction cut into active work. Shared
  tool names are a coarse proxy for "the same work".
- **Context pressure** is the peak share of a window, as the reader's rail
  infers it. For models whose window is unknown (Gemini, for example) it is
  null.
- **Corrections** are a phrase list over typed text only. They miss
  corrections phrased otherwise and cannot weigh how serious one was.
- **Verification discipline** reads "a check ran after the last edit", not
  "the check passed".
- **Ended on a failed call** is experimental. Sessions often end on a
  failure the person then handled outside the transcript.

## Storage and incremental behaviour

The derivation follows Parallelism's precedent
([`parallelism.go`](../internal/archive/parallelism.go)).

- `session_shape_facts` holds one JSON document of facts per conversation,
  computed by `deriveSessionShape` (`session_shape.go`).
- `session_shape_evidence` holds, per conversation and signal, the JSON list
  of evidence message IDs. It is a side table, so a dataset read never reads
  evidence; the reader's signal strip reads one conversation's rows by key.
- Triggers mark a conversation in `session_shape_dirty`, with a fresh token,
  when any input changes:
  - its messages: inserted, deleted, or changed in conversation, role, kind,
    time or content;
  - its authorship rows, tool calls or model requests;
  - its model;
  - which of its messages an embedded sub-agent owns.
- The maintenance loop stores dirty conversations' facts after Parallelism's
  (`maintenance.session-shape`), in batches of 200 with short read snapshots
  and write transactions. A build clears only the token it read, so a change
  made during the build is derived again.
- A page never writes. It reads stored facts for clean conversations and
  derives dirty ones afresh, as Parallelism does. Damaged stored facts are
  derived again.
- Dimensions, roots, mirrors, sub-agent counts, tokens and cost are joined
  when the rows are built and cached until the catalog changes. A rename or
  re-attribution therefore needs no re-derivation.
- `sessionShapeVersion` names the derivation. Changing it marks every
  conversation dirty, and the facts are rebuilt in the background.

The query-table engine stages map rows per request. A request's AND terms
on `started_at` pre-select the rows, and only the requested fields are
copied (`sessionShapeQueryRows`).

## Attribution

The bands, caps and several detectors are adapted from MIT-licensed tools.
See [THIRD_PARTY_NOTICES.md](../THIRD_PARTY_NOTICES.md).

- **AgentsView** (commit `10667a6`):
  - the length, duration and autonomy bands;
  - the 300 s active gap and the 1,800 s turn-cycle cap;
  - identical retry runs and edit churn clusters (`toolhealth.go`);
  - mid-task compaction (`context.go`);
  - the runaway threshold (`friction/signal.go`) and context pressure
    (`score.go`);
  - its "unscored" rule (`score.go`), which shows insufficient evidence
    rather than counting it as healthy.
- **CodeBurn** (commit `4169d12`): the verify-separated retry
  (`classifier.ts` `countRetries`). Pharos reads verification from its own
  command taxonomy rather than CodeBurn's read-shaped command test.
- **Splitrail** (commit `11bf8ca`): the tool-call bands, the
  under-5-minutes active rule and the nearest-rank percentile.

AgentsView's penalty-from-100 health grade is deliberately not adopted.

## Related dashboards

Session shape answers these questions; the dashboards below already answer
neighbouring ones and are linked rather than duplicated.

| Question | Existing answer |
|---|---|
| Waits after a tool result, and the effect of compaction on waits and context | Waiting and Compaction ([thinking dashboards](thinking-dashboards.md)). Its gap runs from a tool result to the next model request. The turn cycle here runs from a typed human message to the first assistant message. |
| Tool calls and failures per conversation, by tool | Tool Time's conversation and tool-in-conversation units |
| Human messages per conversation | Conversations, and Human Words with the conversation unit |
| Engaged time per day, streaks and workday shape | Habits |
