# Agent events (preview)

Analyze → **Agent Experience** shows what agents ran into while they worked: obstacles, environment friction, stale
docs, surprises, confusion, discoveries, their own mistakes, workarounds, unchecked claims, deferred work, assumptions,
getting around an instruction or safeguard, and more. Each event is one sentence in an agent's visible reply or its
thinking, with what it was about and what the agent did about it, linked to the message in the reader.

It is a preview. Settings → Background Processing → **Agent events (preview)**, beside message labels, turns it on for a library: it lists the
pages in Analyze and starts the pass that finds events on this Mac (below). `agent_events_enabled = true` in the
library's configuration does both too, and locks the switch on; `/assets/preferences.js` tells the page so
(`window.pharosConfiguredFlags`), since no preference records it. Direct links work either way.

## How events are found

The JAYHAWK study (kept outside this repository) built the method:

1. **Candidates.** Broad keyword lists, one per kind of event, find candidate sentences in agent replies and thinking.
   Sentences inside fenced code are skipped. Keywords are a rough proxy: about one hit in five is a real event.
2. **Verifier.** A small encoder (ettin 68M) reads the candidate sentence with up to six sentences before it and two
   after, and the kind's definition. It answers whether the sentence really expresses that kind, which sentences say
   what it was about, and which say what the agent did. A sentence can count under more than one kind.
3. **Threshold.** An event is a candidate at or above probability 0.5 (`agentEventThreshold`). The threshold belongs
   to the model; counts across a model change are not comparable, so compare rates within one model.

On the study's held-out ground truth (Opus, Sol and Argon majority), the verifier's F1 was 0.79 for the original
twenty kinds, close to the Haiku and Flash labels it learned from (0.82, 0.81). Keyword candidates reach roughly
three quarters of the event sentences; the rest are phrased in ways no keyword anticipates.

## Data

Two derived tables, created at catalog open (`agent_events.go`), with no schema version change:

- `agent_event_messages`: every agent message the pass read, with or without events (the denominator for rates).
- `agent_events`: one row per scored (message, sentence, kind) at or above probability 0.3
  (`agentEventStoreFloor`), with its probability, sentence, subject and response text and their UTF-8 byte ranges in
  `messages.text`.

A message that is deleted, or whose text changes, loses both kinds of row at once (two triggers on `messages`): they
were scored on text it no longer has. The datasets' day, week and month come from `occurred_at` in this Mac's time
zone when queried, as the other datasets' do; the stored `day` is only the import's or the pass's record.

The reported kinds, their labels and groups are `agentEventFamilies` (`agent_events.go`). It writes
`web/src/agent-event-kinds.json`, which the dashboard's catalog reads; the schemas' options and the reader's group
colours are checked against it (`TestAgentEventKindsAgreeEverywhere`, `PHAROS_UPDATE_AGENT_EVENT_KINDS=1` rewrites
the file).

They back two query datasets, `agent_events` (events at or above the threshold) and `agent_event_messages`, and the
dashboard's catalog (`web/src/agent-events-analysis-catalog.ts`). The dashboard also draws **events per 1,000 agent
messages** by day or week, dividing by the messages read in each period under the same filters: the engine's parts
split only additive measures, so this panel computes the rate from two grouped counts.

### In the reader

A conversation the pass has read offers **Agent events** in the reader's Highlight row, beside Human writing and
Message labels (view field `reader_events`; the dashboard's example links turn it on). It marks each event's sentence in its kind group's hue and underlines what the
event was about and what the agent did; hovering a marked sentence names its events, with probability and those
sentences. Thinking is marked inside its Reasoning row, which shows a count of its events.

- `agentEventsAttacher` (`agent_events.go`) attaches each agent message's events at or above the threshold to the
  live reader's conversation, with ranges converted from UTF-8 bytes to UTF-16 code units (JavaScript's string
  indexes) in one pass per message, and a summary for the checkbox's note. An event whose stored sentence is not where it was
  found is left out.
- `messageEventView`, `eventMask` and `eventsAtMark` (`reader-model.js`) turn them into marks and pick the events
  under a hovered mark: each marked sentence is a mark of its own whose class names its span (`event-at-N`). Events
  apply only where the reader shows the stored text they were scored on: a reply's card, or a thinking block's own
  text (Codex keeps the JSON event in `raw_text` and the prose in `text`). `normalizeMessages` drops them from blocks it
  splits out of a JSON message.

## The pass

With the Settings switch on (preference `pharos-agent-events-v1`, read as a feature flag, `agentEventsFlag`) or
`agent_events_enabled = true` in a library's configuration, the analysis chain runs an **agent events** stage after
labels, its own background item (Agent events, hourly and adaptive). It uses the message labeler's worker, ONNX
Runtime and budget (`labels_worker`, `labels_runtime`, `labels_thread_budget`, …) with its own threads per worker,
`agent_events_threads` (Settings → Agent events → Cores; unset, `labels_threads`), and the same power rules: each chain
pass scores about 1,000 messages (`agentEventsChainLimit`); a larger backlog continues as an `analysis.agent_events`
`backlog` run when plugged in and stops if this Mac goes onto battery.

Either backlog run (this one and message labels') can be stopped: Stop in its Background Processing item, or in the
status panel, cancels it (`Op.setStopper`). Committed conversations remain; unfinished conversations are read again
on the next run. Agent events prepares upcoming conversations while the model scores the current window and a
writer saves earlier results, one atomic transaction per conversation. A shared budget bounds queued candidates
and message bookkeeping to three windows of about 512 rows, plus the conversation currently being built. A large
conversation stays whole and occupies the budget until saved. Partial windows run as soon as preparation pauses,
so the worker need not wait for 512 candidates. Preparation uses the separate read-only catalog pool. Active runs
check the Cores target every two minutes and replace their workers after the current scoring chunk finishes,
keeping its results. Changing cores needs no stop or app relaunch; the shared budget and battery cap still apply.

- **The verifier** is a pinned download, `agent_events_model` in `internal/archive/labels/assets.json`
  (`jayhawk-68m`, version 1: the manifest, the int8 ONNX file and the tokenizer, 146 MB), unpacked into
  `<support>/labeler/jayhawk-68m-1/` like the labeler's model. The broker's `labels.fetch` downloads it, and the
  runtime, when a library whose configuration turns agent events on opens, even with labels off, then asks the
  library to run the item. The broker does not read a library's preferences, so for the Settings switch the stage
  downloads what is missing itself, under the same per-Mac lock; turning the switch on runs the item at once. Until
  the download completes the item says it is waiting for it. `agent_events_model_dir` (or
  `PHAROS_AGENT_EVENTS_MODEL_DIR`) names another directory; `PHAROS_AGENT_EVENTS_MODEL_URL` fetches from another URL,
  the pinned digest and size still applying. Package a new version with
  `python3 tools/package_labeler_model.py DIR --out build/labeler --version N --descriptor internal/archive/labels/assets.json --key agent_events_model`
  and attach it to the release `labeler-jayhawk-68m-N`. A model release carries only the archive and is not an app
  release: the cleanroom Mac that builds the app never needs the weights, only the pin in git, so it can be published
  from any Mac with `gh` (`gh release create labeler-jayhawk-68m-N ARCHIVE --latest=false`). Version 1 is published.
- **The horizon.** `agent_events_days` (default 90; 0 for everything) is how far back the pass reads: older messages
  stay unread, conversations whose recorded end is before it are not looked at (one without a recorded end is), and
  rows stored earlier stay. Each conversation records the horizon it was read from (`agent_events_conversations.since`),
  so a wider horizon, `--days 0` included, reads what a narrower one left.
- **Terminal.** `pharos agent-events run [--limit N] [--days N] [--full] [--dry-run] [--conversation ID]...`,
  `pharos agent-events fetch` (the verifier, and the runtime unless one is configured or bundled) and
  `pharos agent-events status` (what the pass would use or waits for, and what the tables hold).

`RunAgentEvents` (`agent_events_run.go`):

- **What it reads.** Each conversation's units from labeling prepare's own pass (`labelsItemBuilder`), the cohorts
  `agent_reply` and `thinking`: what the study's data held, without inherited history, mirrored work or unextracted
  thinking. Every such message gets an `agent_event_messages` row, with or without candidates.
- **What it scores again.** A message whose source and text (`input_hash`) or verifier differ from its stored row's.
  A message whose only change is its time keeps its events, moved to the new day in both tables.
  The verifier's identity is its model identity (as the labels ledger's) plus `jayhawk-window-v1`, the lexicon,
  the units and the sentence versions; an imported row has no input hash, so the pass replaces it.
- **Which conversations it looks at, newest first.** Those marked in `agent_events_dirty`, by the same triggers as
  the labels ledger's (`itemDirtyTriggers`); those never built or whose structure fingerprint changed
  (`agent_events_conversations`); those with rows another verifier made; and the forks of a conversation whose inputs
changed, since what a fork inherited from its parent decides which replies are its own. A fork followed that way gets a
dirty mark of its own, so a limited run that stops before it keeps it pending. Conversations that left the catalog lose
  their rows.
- **How it stores.** Preparation, scoring windows and saving overlap (`agent_events_pipeline.go`). Candidates are
  scored in batches packed by length, and each conversation's results, ledger row and original dirty token are
  saved in one transaction. A stop keeps committed conversations and kills the verifier's workers, so batches queued
  at them do not run on; the next run resumes. A finished run leaves them warm for `labels_idle_seconds`. Run stats
  and the final progress line include preparation, inference and save time; these overlap rather than adding to
  elapsed time.
- **What it refuses.** A verifier trained on another lexicon, deciding at another threshold than the datasets'
  (0.5), lacking a kind's definition or the `verdict` and `sentences` outputs.

### Import

Events can also be imported from the study's predictions; the pass scores those messages again:

```sh
pharos --config LIBRARY.toml agent-events import FILE.jsonl
pharos --config LIBRARY.toml agent-events status
```

`FILE.jsonl` (`pharos-agent-events-v1`) starts with a header line naming the model and lexicon, then one line per
message read and one per scored event. Byte ranges are zero-based UTF-8 offsets into `messages.text`, end exclusive.
Blank lines are ignored, and of repeated lines for one message or one (message, sentence, kind) the last counts. It
keeps a message only when it exists with the text it was scored on (SHA-256) and its source is `reply` or `thinking`,
and an event only when its kind is one the product reports, its probability is in [0, 1] and its ranges fall on UTF-8
boundaries of that text; it reports what it skipped. Importing a message again replaces its events, and a message the
file names that is gone or whose text changed loses those an earlier import stored.

It reads first and writes after: the file, then each message once in a short statement, keeping its metadata and its
events' texts but never its whole text; then one tracked write transaction (`writeTransaction`: the write gate, busy
retries and the lease audit), which skips a message whose text changed since it was read.

## Candidates and framing in Go

`agent_event_candidates.go` makes the verifier's inputs inside Pharos, framing contract `jayhawk-window-v1`. The
model's directory carries the contract (`FRAMING.md`), the Python oracle (`reference.py`) and an invented-string
fixture, copied here as `testdata/agent-event-parity.json`:

- `agentEventSentences`: a message's sentences, cut as `labeling prepare` cut the study's (`sentences-v1` over the
  target pieces, secrets and unrecognized harness blocks left out).
- `agentEventCandidates`: sentences outside fenced code with a cue for a product kind. The lexicon
  (`agent_event_lexicon.json`, `jayhawk-lexicon-2`) is the model's `lexicon.json`. Cues are Python patterns with
  Unicode `\w`, `\d` and `\b`; Go's are ASCII, so matching runs on a copy of the normalized sentence where non-ASCII
  digits become `D` and other non-ASCII letters and numbers `X` (cues are lower-case ASCII, which the test checks).
  A cue's pattern runs only when the sentence has one of the literals every match must contain.
- `agentEventLines`: the header and window lines the verifier reads.
- `agentEventFrame`: their tokens within the model's 768, as a `labeler.Framed` whose text span is the anchor's
  tokens (the `pool` role) and whose `labeler.SegmentSpans` are each window line's (the `segment` role).

The model's input contract is `ids-attention-pool-segments-v1` (`labeler.InputContracts`): `input_ids`,
`attention_mask`, `pooling_mask` and `segment_ids`, which numbers the window's sentences 1…9 (16 at most) so the
model pools each one. `labeler.LoadFramedModel` loads it with `agentEventFraming`: a framing that does not label needs
no labels or thresholds, and the message labeler's `LoadModel` refuses it.

`TestAgentEventCandidatesMatchReference` and `TestAgentEventFramingMatchesReference` check normalization, every cue's
compiled pattern, matches, fences, clipping, lines, token ids, the budget (an anchor cut to fit, neighbours left out,
the earlier of two neighbours kept) and the pool and segment inputs against the fixture exactly. On 4,000 of the
study's messages (about 104,000 sentences, checked locally and not kept), sentences, clips and all 84,636 candidates
matched the Python. `TestAgentEventModelParityWithWorker` runs the real model through the real worker
(`PHAROS_AGENT_EVENTS_MODEL_DIR`, `PHAROS_LABELER_WORKER`, `PHAROS_ORT_LIBRARY`) and compares every output with the
reference's; with ONNX Runtime 1.31.1 both variants match it exactly, in batches packed and padded as the runtime packs
them.

## Next

- **Cost.** Scoring dominates: about 18 candidates a second on four CPU threads with the machine to itself, and the
  study's October (100,000 agent messages, 524,000 candidates) would take about eight hours, some seventeen minutes a
  day. The int8 export agrees with fp32 on 99.9% of decisions at the same F1.
- **Not yet in the product:** how sure an event is, beyond the threshold and the probability the reader shows. Risky
  actions are left to the tool-call history, where the actions themselves are recorded.
