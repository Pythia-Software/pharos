# Agent events (preview)

Analyze → **Agent Experience** shows what agents ran into while they worked: obstacles, environment friction, stale
docs, surprises, confusion, discoveries, their own mistakes, workarounds, unchecked claims, deferred work, assumptions,
getting around an instruction or safeguard, and more. Each event is one sentence in an agent's visible reply or its
thinking, with what it was about and what the agent did about it, linked to the message in the reader.

It is a preview. Settings → App preferences → **Agent Experience dashboard** lists its pages in Analyze; direct links
work either way.

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

They back two query datasets, `agent_events` (events at or above the threshold) and `agent_event_messages`, and the
dashboard's catalog (`web/src/agent-events-analysis-catalog.ts`). The dashboard also draws **events per 1,000 agent
messages** by day or week, dividing by the messages read in each period under the same filters: the engine's parts
split only additive measures, so this panel computes the rate from two grouped counts.

## The pass

With `agent_events_enabled = true` and `agent_events_model_dir` (or `PHAROS_AGENT_EVENTS_MODEL_DIR`) naming the
verifier's directory, the analysis chain runs an **agent events** stage after labels, its own background item (Agent
events, hourly and adaptive). `pharos agent-events run [--limit N] [--full] [--dry-run] [--conversation ID]...` runs
it from Terminal. It uses the message labeler's worker, ONNX Runtime and budget (`labels_worker`, `labels_runtime`,
`labels_threads`, …, and the runtime the labeler downloads), and the same power rules: each chain pass scores about
1,000 messages (`agentEventsChainLimit`); a larger backlog continues as an `analysis.agent_events` `backlog` run when
plugged in and stops if this Mac goes onto battery.

`RunAgentEvents` (`agent_events_run.go`):

- **What it reads.** Each conversation's units from labeling prepare's own pass (`labelsItemBuilder`), the cohorts
  `agent_reply` and `thinking`: what the study's data held, without inherited history, mirrored work or unextracted
  thinking. Every such message gets an `agent_event_messages` row, with or without candidates.
- **What it scores again.** A message whose source and text (`input_hash`) or verifier differ from its stored row's.
  The verifier's identity is its model identity (as the labels ledger's) plus `jayhawk-window-v1`, the lexicon,
  the units and the sentence versions; an imported row has no input hash, so the pass replaces it.
- **Which conversations it looks at, newest first.** Those marked in `agent_events_dirty`, by the same triggers as
  the labels ledger's (`itemDirtyTriggers`); those never built or whose structure fingerprint changed
  (`agent_events_conversations`); and those with rows another verifier made. Conversations that left the catalog lose
  their rows.
- **How it stores.** Candidates are gathered across conversations (512 at a time), scored in batches packed by length,
  and stored with each conversation's ledger row in one transaction. A stop between chunks keeps what was stored; the
  next run resumes.
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
The import is one transaction. It keeps an event only when its message exists with the text it was scored on
(SHA-256), its ranges fall on UTF-8 boundaries of that text, and its kind is one the product reports; it reports
what it skipped. Importing a message again replaces its events.

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

- **Shipping the verifier.** The pass needs the model on this Mac: a directory configured by hand today. Like
  `kestrel-68m-tpool`, it is to be a pinned download (`internal/archive/labels/assets.json`) from its own Pharos
  release, published from the deployment Mac, so that turning the item on is all a person does.
- **Cost.** Scoring dominates: about 18 candidates a second on four CPU threads with the machine to itself, and the
  study's October (100,000 agent messages, 524,000 candidates) would take about eight hours, some seventeen minutes a
  day. The int8 export agrees with fp32 on 99.9% of decisions at the same F1.
- **Not yet in the product:** how sure an event is, beyond the threshold, and highlighting an event's sentence in the
  reader. Risky actions are left to the tool-call history, where the actions themselves are recorded.
