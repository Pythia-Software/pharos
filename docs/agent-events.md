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
- `agent_events`: one row per scored (message, sentence, kind), with its probability, sentence, subject and response
  text and their UTF-8 byte ranges in `messages.text`.

They back two query datasets, `agent_events` (events at or above the threshold) and `agent_event_messages`, and the
dashboard's catalog (`web/src/agent-events-analysis-catalog.ts`). The dashboard also draws **events per 1,000 agent
messages** by day or week, dividing by the messages read in each period under the same filters: the engine's parts
split only additive measures, so this panel computes the rate from two grouped counts.

### Import

Until the model runs inside Pharos, events are imported from the study's predictions:

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

`agent_event_candidates.go` is the first half of running the model inside Pharos, framing contract
`jayhawk-window-v1`. The model's directory carries the contract (`FRAMING.md`), the Python oracle (`reference.py`) and
an invented-string fixture, copied here as `testdata/agent-event-parity.json`:

- `agentEventSentences`: a message's sentences, cut as `labeling prepare` cut the study's (`sentences-v1` over the
  target pieces, secrets and unrecognized harness blocks left out).
- `agentEventCandidates`: sentences outside fenced code with a cue for a product kind. The lexicon
  (`agent_event_lexicon.json`, `jayhawk-lexicon-2`) is the model's `lexicon.json`. Cues are Python patterns with
  Unicode `\w`, `\d` and `\b`; Go's are ASCII, so matching runs on a copy of the normalized sentence where non-ASCII
  digits become `D` and other non-ASCII letters and numbers `X` (cues are lower-case ASCII, which the test checks).
  A cue's pattern runs only when the sentence has one of the literals every match must contain.
- `agentEventLines`: the header and window lines the verifier reads, before tokens.

`TestAgentEventCandidatesMatchReference` and `TestAgentEventLinesMatchReference` check normalization, every cue's
compiled pattern, matches, fences, clipping and lines against the fixture exactly. On 4,000 of the study's messages
(about 104,000 sentences, checked locally and not kept), sentences, clips and all 84,636 candidates matched the
Python. Token ids, the budget and padding, also in the fixture, wait for the labeler's tokenizer.

## Next

The model is to run on the local-labeler runtime (`docs/local-labeler.md`) as a second model beside the message
labeler, with its own input contract (a `segment` input marks each window sentence's tokens), its own pass and the
same tables. Its int8 export agrees with fp32 on 99.9% of decisions at the same F1, and a row's outputs are the same
alone and in any batch padded to a multiple of 8. Scoring is the cost: about 18 candidates a second on four CPU
threads, and the study's October (100,000 agent messages, 524,000 candidates) would take about eight hours, some
seventeen minutes a day. Risky actions are left to the tool-call history, where the actions themselves are recorded.
