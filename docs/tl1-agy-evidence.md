# TL1 and Antigravity evidence

TL1 records AGY's stream in `<attempt>-agy.jsonl`. Its `init.conversation_id`
is the native Antigravity session ID. Pharos retains both conversations and
their source locators, joining them through an explicit native alias in the
same account. No transcript is moved or overwritten to establish the link.

## Content and accounting

The TL1 workspace keeps its task, candidate, timing, outcome and disposition.
Its reader selects the linked native conversation when that conversation has
content. Candidate task responses expose `conversation_id` for reading,
`workflow_conversation_id` for the TL1 envelope, and `native_session_id` for
the original join key. Exact conversation and message IDs continue to address
their original retained evidence. A TL1 conversation overview also provides
`content_conversation_id` when a richer native copy is available.

Native sessions without a saved working directory inherit the repository
recorded by their linked TL1 task. Conflicting repository claims remain
unresolved. Existing explicit repository attribution takes precedence.

Native reported usage takes precedence over the stream's counters when it can
be attributed to one TL1 transcript. Archive usage and its header summary
count the linked native conversation once. Native-only subagents keep their
own usage; TL1 remains the root workflow envelope for usage attribution.
If native usage is unavailable, attempt accounting uses the stream.

Some AGY runs reuse a native ID across different TL1 attempts. Native usage
is a session aggregate, so assigning it to each attempt would multiply the
cost. Such attempts keep their own stream's content and counters and expose
`shared_native_session: true`; `native_conversation_id` still identifies the
linked native record for inspection. Archive-wide usage counts that native
session once. Shared-session native records may cover a different span from
an individual stream; attempt totals and archive totals need not agree.

AGY streams are `provider: antigravity`, `harness: agy`, with partial coverage.
They retain assistant deltas, tool parameters and available outputs, including
`run_command` output and its header's exit status. File-tool outputs may be
summaries or empty. Effort suffixes are separated from the model, and pricing
uses the existing Neon/Argon rate cards. Per-step usage is deduplicated by step
index; result totals supply unreported usage. Thinking is already included in
output and is never added again. An unmetered AGY run's historical reported
zero does not establish zero cost.

Pharos input and total tokens include cached input. AGY's `result.total_tokens`
excludes it. For the handoff example, 124,742 uncached input + 1,379,881 cached
input + 58,908 output = **1,563,531 Pharos total tokens**, corresponding to
**183,650 AGY total tokens**. The 33,731 thinking tokens are within output.

## Preservation and completeness

Antigravity is an expiring source: **Preserve expiring originals** captures it
hourly when background work is enabled. Captures retain previously acquired
files after the CLI removes them. The catalog also retains conversations absent
from later discovery passes. Fixture coverage exercises a linked native session
through capture, source eviction, another capture and recovery into a fresh
catalog, in both ingestion orders. Capture cannot recover an original evicted
before Pharos acquired it; a rolling window exhausted between capture passes
remains a source-history gap.

`tl1_overview.coverage.native_transcripts` reports the selected window's indexed
attempts: references, distinct native IDs, matches, missing sessions, missing
join keys and shared native IDs. Missing entries name the installation, task,
attempt and native session. This is a catalog-level check; unindexed transcripts
and unregistered installations are outside its scope. A nonempty unsupported
transcript produces a parser-mismatch ingestion error, visible through
`get_archive_status`. An init-only run is partial, not a parser error.

The November 22, 2026 read-only handoff audit inspected 875 local transcript
files: 874 references to **851 distinct native sessions**, all present with
messages in the mounted catalog. Eight native IDs were shared across multiple
transcript files; one brick attempt failed without an init join key. The brick
project accounted for 796 files and 795 references. These are transcript-file
counts, including files added since the handoff, rather than attempt counts.
The workspace's `.context/agy-native-completeness.json` contains the full audit.

## Reasoning

Native `PLANNER_RESPONSE.thinking`, when present, is indexed as a saved reasoning
summary. Thinking-token counters alone do not establish saved reasoning text.
AGY stream-json contains no reasoning text. Older native catalog rows may have
discarded the planner envelope; extracting their summaries requires reindexing
a native source or capture that still contains it. Missing text stays unknown.

Deploying these source changes does not itself reindex a running library. The
new TL1 extractor version requests replay through normal indexing; native
reasoning recovery still needs the retained source evidence described above.
