# Antigravity and TL1 telemetry

Pharos retains source evidence and counts non-zero command exits without deciding
whether a code means a failed check, a verdict, or a crash. The corpus handoff's
worked candidate and full 796-file corpus are not included in this repository;
the acceptance fixture is synthetic. Its native and fallback records each contain
27 calls, including six non-zero exits: four code 1 and two code 2. Local tests
verify those counts, numeric result storage, and linked-copy suppression. They do
not establish the production corpus's totals or its 466-task batch measurements.
The earlier `PHAROS_AGY_TRANSCRIPT_HANDOFF.md` and
`TL1_AGY_TELEMETRY_HANDOFF.md` referenced by the supplied handoff are absent from
this checkout.

## Tool-call failures and exit codes

A call fails when its exit code is non-zero, it has an explicit error/`is_error`
flag, or a tool-level error. A false flag cannot cancel separate failure evidence.
This applies across providers, including search commands whose code 1 may simply
mean no matches. The consumer interprets that code. Missing exit codes remain null
and never enter the code-zero bucket; non-command tools often have no exit code.
A call with no result remains `no_result`.

Native Antigravity and TL1 AGY stream ingestion extract the numeric code from
`Command exited with code N.` in the CLI header of a `run_command` result,
including after Created At / Completed At lines. The command-output body cannot
invent an exit status by printing that sentence. The code is stored on the result JSON as `exit_code`
and `details.exit_code`, and indexed in `tool_calls.exit_code`. The ledger upgrade
also recovers codes from already-retained command output. Parser versions request
reindexing of available sources. No source or capture is written by these reads.

The TL1 fallback retains command output and exit lines. `read_file` may keep only
a size/line summary, `write_file`/`edit_file` may have empty results, and long
command output may be replaced with a truncation notice. Fallback coverage stays
partial. An exit omitted by truncation is unknown.

Use MCP `query_statistics` for a complete, bounded report over current retained
calls, for example:

```json
{
  "entity": "tool_call",
  "result_type": "aggregate",
  "executor": "agy",
  "project": "brick-object-corpus",
  "group_by": ["executor", "model", "project", "day"],
  "limit": 500,
  "max_output_tokens": 8000
}
```

Each group returns `tool_call_count`, `failed_tool_call_count`,
`unknown_exit_code_count`, `no_result_tool_call_count`, and `exit_code_histogram`
with numeric codes and a null unknown bucket. Histogram buckets also return their
failed counts, so an error flag with code zero or unknown is visible. `executor`
is normalized from the transcript provider (`antigravity` → `agy`), rather than
inferred from a task's latest configured executor. `provider` can be grouped or
filtered directly. Models and local calendar days come from individual calls;
untimed fallback calls have a null day. Project groups include stable project IDs.

This call-level report supports only provider/executor, model, project, from/to,
workspace IDs, group_by, limit, and output budget. Date filters apply to call time;
untimed calls cannot match a date bound. No date bound includes them. It returns
all groups atomically or fails asking for narrower filters/a larger budget; it
does not silently truncate or provide snapshot/cursor paging. Maximums are four
dimensions, 500 groups, 10,000 histogram cells, and 100,000 conversations. Linked
conversation copies count once using the statistics canonical identity rules;
native-only child conversations remain included. Missing/stale ledgers are
excluded, with conservative pending coverage (not narrowed by call model/date).
Partial source coverage and missing history remain explicit limitations.

Legacy MCP `query_metrics` supports the same report with `name: "tool_outcomes"`
in place of `entity: "tool_call"`. Workspace/conversation `query_statistics`
still exposes `tool_call_count` and `tool_error_count` with its existing scope,
quality, filtering and snapshot semantics.

## Thinking evidence

Antigravity does store some thinking text: native `transcript_full.jsonl`
`PLANNER_RESPONSE.thinking` holds summaries. Pharos retains these separately as
`kind=reasoning`, `reasoning_format=summary`, indexes them, and can retrieve them
through `search_conversation_passages`. `search_text` with `surface=reasoning`
restricts search to reasoning. Summaries are not inserted into assistant prose.
A missing summary does not establish that the model generated no reasoning.

Native request databases retain `thinking_output_tokens` in protobuf
`gen_metadata`. Pharos preserves the original per-request counters in
`model_usage` and normalizes them to `reasoning_output_tokens`. AGY stream
`thinking_tokens` uses the same normalized counter. Output already includes
thinking; it is not added twice. Missing usage evidence stays unavailable.

TL1 AGY stream-json does not carry thinking text or per-step timestamps. Its
overview coverage note states that limitation and points to linked native
conversations. Native conversation notes and catalog reasoning coverage describe
retained summaries; they do not claim complete historical reasoning coverage.

## Queue-adjusted TL1 timing

`tl1_candidate` task rows and `tl1_overview.timing` expose:

- `queue_wait_seconds`: task enqueue to first retained claim; a set uses its
  earliest enqueue to first claim. Claim events repair a later retry's claim
  timestamp when those earlier events exist.
- `attempt_seconds`: sum of recorded attempt end minus start, including retries
  and tools. This is the requested LLM-time measure for LLM tasks.
  `llm_attempt_seconds` restricts mixed task sets to LLM attempts.
- `wall_seconds`: task creation to completion; a set spans earliest selected
  task creation to latest selected task completion (`makespan_seconds`).

Missing or reversed attempt endpoints make the total null. Valid zero-length
intervals remain zero. `known_attempt_seconds` reports the measured subtotal and
`attempts_missing_endpoints` identifies incompleteness. An unfinished task makes
the set's final makespan unknown. These spans do not infer a current end time.

`timing_batches` uses the existing enqueue groups: at least five root tasks,
with gaps no greater than ten minutes. Descendants belong to their original
root's enqueue, including those that finish later. Only tasks selected by the
request's window/scope contribute. The JSON overview returns up to twelve recent
batches; MCP keeps five. A window's `timing` also provides a caller-selected
cohort using the existing since/until/scope filters.

The TL1 attempt metrics table exposes additive `attempt_seconds` plus
`queue_wait_seconds` and `wall_seconds`. The latter describe the containing task
and repeat across its attempts, so they are explicitly non-measures; do not sum
them. Attempt seconds label LLM time when execution class is LLM.

For finished batches, `used_slot_hours` sums LLM attempt intervals and
`average_llm_concurrency` divides that time by makespan. Historical capacity is
not ingested. `slot_utilisation`, `capacity_slot_hours`, `idle_slot_hours`, and
ramp/tail idle hours therefore remain null with `capacity_status=unavailable`
and an explanation. Flavor `max_parallelism` and peak observed concurrency are
not substitutes for capacity history. TL1 must supply capacity changes before
Pharos can establish the corpus batch's utilisation or split idle ramp/tail time.
