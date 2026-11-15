# Engagement labels (preview)

Engagement labels mark spans of what a person typed to an agent: what they ask for, what they rule out, how they
want the result checked, and so on. A small token-classification encoder reads each typed message with the tail of
the agent message before it and gives every token a score per label; runs of tokens at or above a label's threshold
become spans. The conversation reader underlines them.

It is a preview and off by default. Nothing runs, and no table exists, until `engagement_enabled` is set and a model
directory is configured. The model comes from the NUTHATCH study, which is kept outside this repository; no model
file, label list or conversation text is checked in. The label list is whatever the model's `model.json` declares.
A label `bounds.scope` belongs to the dimension `bounds`; the reader groups by dimension.

## The model contract

The worker protocol and the kestrel models are described in [Local labeler](local-labeler.md). A token-label model
directory holds:

| File | Contents |
|---|---|
| `model.json` | `contract` (`ids-attention-token-labels-v1`), `framing` (`nuthatch-span-framing-v1`), `labels`, `max_seq_tokens`, `max_context_tokens`, `window_overlap_tokens`, `cls_id`, `sep_id`, `pad_id`, `variants` (name → file), `files` (name → SHA-256), and optionally `thresholds` (a list in label order, or a map by label) |
| the variant's ONNX file | inputs `input_ids` and `attention_mask` `[batch, tokens]`; output `logits` `[batch, tokens, labels]`; sigmoid per logit |
| `tokenizer.json` | the tokenizer; `internal/labeler` reproduces its token offsets (`Tokenizer.EncodeOffsets`) |

`internal/labeler/tokenlabels.go` owns the contract:

- **Framing** (`FrameWindows`): `[CLS] context [SEP] text [SEP]`. The context keeps its last `max_context_tokens`
  tokens. Text longer than the room left is cut into windows that overlap by `window_overlap_tokens`.
- **Sparse results.** A hello option `sparse` (`{output, activation: "sigmoid", floor}`) asks the worker to return an
  output as `[n, 4]` rows of (row, token, label, probability) at or above the floor, instead of the dense tensor. A
  worker that can do this announces the capability `sparse`; the pass refuses one that does not. The pass uses a
  floor of 0.3, so thresholds down to 0.3 can be tried without another run of the model.
- **Merging and decoding** (`MergeWindows`, `DecodeSpans`): a token in two windows keeps its larger probability. A
  span is a maximal run of text tokens at or above the label's threshold (0.5 unless `thresholds` says otherwise),
  trimmed of white space, scored by its largest probability.

`TestTokenLabelParity` checks tokens, offsets, windows and spans against a fixture made by the study's Python
(`tools/engagement_parity_fixture.py`, synthetic text only). `TestTokenLabelParityWithWorker` runs the real model
through the worker when `PHAROS_ENGAGEMENT_MODEL_DIR`, `PHAROS_LABELER_WORKER` and `PHAROS_ORT_LIBRARY` are set: on
the fixture's 14 cases the spans are identical and probabilities differ by at most 3e-06.

## What the model reads

Only messages a person typed in a top-level conversation: role `user`, kind `message`, agent depth 0, and
`message_authorship.typed_words > 0` ([Human authorship](human-authorship.md)).

- **Target text** (`engagementTargetText`): the message's typed spans in order. Each run of other text (pasted,
  quoted, harness and so on) becomes one marker, ` ⟦category⟧ `. The result is trimmed and cut at 6,000 code points.
  A message with nothing left, or with no letter, is recorded as not scored.
- **Context**: the last 1,200 characters of the agent message before it in the conversation (role `assistant`, kind
  `message`, by `source_order`), with `…` in front when cut.
- **Storage offsets**: every span is mapped back to bytes of `messages.text`. A span that crosses a marker is
  stored as one row per typed stretch, so a stored span never covers text the person did not type.

## Storage

The first run creates the tables (`engagementSchemaDDL` in `internal/archive/engagement.go`).

| Table | Rows |
|---|---|
| `engagement_messages` | one per message looked at: `input_hash` (target text, where its bytes lie in the message, and the context), `model` (the model's name, file hash, contract, framing and target rules), `outcome` (`scored`, `empty`, `no_letters`), `tokens`, `spans`, `processed_at` |
| `engagement_spans` | one per span: `message_id`, `position`, `label`, `start_byte`, `end_byte` (bytes of `messages.text`), `score` |
| `engagement_dirty` | conversations whose messages or authorship changed since they were read (the labeler's triggers) |

A message is read again when its input hash or the model identity differs: its text changed, its authorship spans
changed, the agent message before it changed, or another model is configured. A message that is no longer typed, or
is gone, loses its rows.

### What the tables cost with the preview off

Turning `engagement_enabled` off stops the pass and stops the pages showing anything (see In the reader); the
tables, their rows and the triggers stay. `ensureEngagementSchema` creates 16 `AFTER` triggers
(`itemDirtyTriggers`, the labeler's set under the prefix `engagement_dirty_`):

| Table | Triggers |
|---|---|
| `messages`, `message_authorship`, `reasoning_extraction_state`, `agent_sessions` | insert, delete, update |
| `agent_session_messages` | insert, delete |
| `conversations` | delete, update |

Each firing upserts one row of `engagement_dirty` by its primary key, the conversation ID, with a new random
token: one upsert for an insert or delete and two for an update (old and new conversation). An update trigger that
names columns compares them first and does nothing when none changed. `engagement_dirty` therefore holds at most
one row per conversation, and nothing reads or empties it while the preview is off. This was reasoned from the
trigger text, not timed.

## Running it

```toml
engagement_enabled = true
engagement_model_dir = "/path/to/model/onnx"   # or PHAROS_ENGAGEMENT_MODEL_DIR
# engagement_variant = "fp32"                  # a name in model.json's variants
# engagement_threads = 10                      # per worker; default labels_threads
# engagement_batch_tokens = 4096               # padded tokens per batch (the default)
```

The worker and ONNX Runtime are the labeler's (`labels_worker`, `labels_runtime`, or the ones beside the app).

```sh
pharos engagement status
pharos engagement run [--limit N] [--full] [--dry-run] [--conversation ID]...
                      [--threads N] [--workers N] [--batch-tokens N] [--max-batch N] [--variant NAME] [--model-dir DIR]
```

A run works newest first in chunks of 1,024 messages, one transaction per chunk, so stopping it loses at most a
chunk and the next run carries on. After one complete run, later runs look only at conversations marked in
`engagement_dirty`; `--full` looks at every typed message again and scores those whose input changed. Reading and
framing the next chunks overlaps with inference. The run holds a lock (`.engagement.lock`), so two cannot overlap.

With `engagement_enabled` on, the analysis chain's last stage (`engagement`, background item **Engagement labels**,
operation `analysis.engagement`) scores about 1,000 messages per pass and leaves the rest pending. The item is not
listed where the preview is off. The stage downloads nothing.

To try it on a copy of a library, never on the live one: clone the library, set the keys above in the clone's
`library.toml` with its own `port`, run `pharos --config <clone>/library.toml engagement run`, then start the app
on the clone with `tools/dev.py --reuse-library`.

## In the reader

The reader and the workspace Timeline show engagement only while `engagement_enabled` is on in a library that has
the tables (`Catalog.engagementShown`). With the setting off neither payload carries any of it, so the Key groups,
chips, underlines and Human events rows are gone and the pages are as they were before.

`WorkConversation` attaches the spans (`engagementAttacher`): each typed message gets `engagement`, a list of
`{label, dimension, score, start, end}` with UTF-16 offsets into its text, and the conversation gets a summary
(`messages_read`, `messages_with_spans`, `dimensions`, `model`, and `pending` while a change waits to be read).

The reader's Key offers **Engagement** once the pass has read the conversation (view field `reader_engagement`,
default on, saved like the other highlights). It draws underlines only, and only on human-written text:

- the page shows a label only when its score reaches `ENGAGEMENT_MIN_SCORE` (0.70, one constant in
  `reader-model.js`). The pass still stores everything from 0.5 up; the threshold applies in
  `messageEngagementView`, so underlines, the popover, the Key's dimensions and the timeline all follow it. There
  is no control for it yet: a choice would have to re-render the open cards, which the Key's highlights do not do;
- a span is drawn on whole words (`engagementSnap`): blanks at its edges are dropped, an edge inside a word moves
  to the nearer end of that word, and a span that is only part of one word is not shown. Words stop where the
  typed text stops;
- every dimension's underline is the same solid line, drawn as a background of the dimension's nested span;
  dimensions differ by hue alone (`eng-slot-N` in `ui.py`: `--eng-hue`), from a palette of nine. A slot is only a
  colour: a dimension's Key chip, hide key, timeline row and letter go by its ID. The nine known dimensions keep a
  fixed order. A dimension the reader has not met (a retrained model's) takes a palette slot none of the
  dimensions shown with it uses (`engagementSlots`, `engagementAssignSlots`, per conversation or per set of
  Timeline lanes); only when all nine are in use does it share one, chosen by its name. Two dimensions of one
  colour are still two underlines, two chips and two hides;
- the lines sit on a 3 px grid in the 6 px under the text (`--eng-line`, `--eng-first`): 2 px lines 1 px apart
  on a 1× screen, 1.5 px lines 1.5 px apart from 2×, so both are whole device pixels. Where dimensions overlap,
  the nested spans stack their lines on that grid. A stretch draws at most two, the
  best-scoring dimensions that are switched on (`engagementDrawn`), and a card with engagement marks gets line
  height `2em - 2px`;
- the lines of the last row hang below its line box, so such a card's prose has 4 px of bottom padding to hold
  them. The existing clamp (`measureMessageOverflow`) then measures a message that fits as fitting, and offers
  "Show more" only when text is cut. A long message is cut after ten whole lines (`max-height: calc(20em - 16px)`
  in place of the 175 px limit, which holds about ten lines without engagement);
- with Human writing on, that highlight's band fills the whole line on such a card: 6 px below the text for the
  two lines and the rest of the line above, so wrapped lines meet with no strip of page between them. The top
  padding comes from the font's content height, measured once per panel (`measureEngagementBand`,
  `--eng-content`);
- hovering a stretch tints every annotation over it from end to end (`engagementHoverTints`): an annotation is
  one span as displayed, however many marks it is drawn as. The tint is the dimension's hue at low strength;
  where annotations of several dimensions overlap the stretch is striped in each. It covers the annotations the
  popover lists, including ones the two-line cap leaves undrawn;
- a dimension is shown under a one-word display name with its own first letter (`ENGAGEMENT_DIMENSION_NAMES`,
  `engagementDimensionName`): Intent, Grounding, Bounds, Verification (`acceptance`), Delegation (`rights`),
  Evaluation, Questions (`inquiry`), Authorship, Tone (`register`). These are display names only: label IDs,
  the model's labels, URL values and the API keep `acceptance.*`, `rights.*`, `inquiry.*` and `register.*`. A
  dimension the reader has not met shows its own name;
- `register.filler` is never displayed (`ENGAGEMENT_UNDISPLAYED`, dropped in `messageEngagementView`): no
  underline, popover row, timeline marker, Key chip or count. The pass still computes and stores it, and the
  API still returns it, so it stays available for telling typed from dictated text;
- the Key lists the dimensions present, one chip each: a solid line in the dimension's hue and its name. Hiding
  is by dimension: `reader_highlight_hidden` holds `engagement:<dimension ID>` (`engagementKey`), as the
  Timeline's `human` section does. A hide saved as `engagement:eng-slot-N` by an earlier build reads as that
  slot's known dimension (`readerHighlightHidden`) and is written back in the new form. These chips control the
  underlines only. Every key default shows every dimension;
- hovering an underlined span lists all its labels and scores, drawn or not. Clicking pins the list. With two
  or more annotations each row gets a copy button (`copyValueButton`, through `pharosHost.clipboard`) for that
  annotation's whole text; with one, "Copy this text" copies it. The text is the annotation's marks in reading
  order, typed text only: whatever lies between two of its marks becomes one space (`engagementCopyText`);
- text a later agent event links back to keeps its engagement marks. `eventTargetMask` gives a character under
  both a flag naming the target and the engagement segment, so the text is cut at both sets of boundaries and
  each piece is a mark with both sets of classes (`event-target event-target-at-T human-written engagement
  eng-at-N`). The dashed line stays at the baseline (thinner and closer on such a mark) with the engagement lines
  below; the numbered markers, selection and arcs are unchanged. Hovering lists the links and then the
  annotations, and tints and copies each annotation whole, since those go by segment. A flag can name the first
  32 targets and 512 segments of a message; past that the target alone is marked.

The logic is in `reader-model.js` (`messageEngagementView`, `engagementSnap`, `engagementSegments`,
`engagementMask`, `engagementLayers`, `engagementDrawn`, `engagementTipView`, `engagementHoverTints`,
`engagementTimeline`) with unit
tests in `web/tests/unit/reader/engagement.test.mjs`. Spans are shown only where the reader shows the message's
stored text unchanged, as agent events are.

### Human events on the timeline

The reader's timeline has a **Human events** row beside Agent events and Tool actions (`reader-navigator.tsx`,
`HumanEventsRows`). It has one marker for each annotation (a displayed span) in a person's message: the first
letter of the dimension's display name (I G B V D E Q A T; `humanEventLetter`, `.eng-glyph`) as a heavy capital
in the dimension's hue, the same as its underline, in a 7 px box like an agent event's marker. The same letter
marks the dimension's row label, its Timeline Key chip and its inspector card. Like Agent events, the row
folds out into one row per kind, here per dimension, each with its count of markers (disclosure
`reader-timeline:<conversation>:human`). Hovering a marker opens the ribbon inspector card with the annotation's
label, score and the text it covers; clicking reveals the message, scrolls to the annotation and flashes it.

The Key has two separate groups, as agent events do: **Human events** under Timeline chooses the dimensions on these
rows (`timeline_visibility`, section `human`, values are dimension IDs such as `intent` or `register`), one chip per dimension, and
**Engagement** under Highlights chooses the underlines (`reader_engagement`, `reader_highlight_hidden`). Neither
changes the other.

A person's message is an instant on the clock, so `humanEventLayout` (`web/src/human-events-model.ts`) gives each
one a slot (10 px per marker, at most eight markers wide) and squeezes the rest of the clock in proportion; every
timeline row uses that clock. The slots together take at most half the row: past that they shrink alike. Within
its slot a marker sits where its annotation starts in the message (`humanEventX`), at the same x on the folded
row and on its dimension's row. Markers that would touch stack in lanes above and below the row's axis, using
the lanes and placement of the tool-call marks (`ribbon-marks.ts`, through `humanEventPlacement`); the row keeps
its height, and a stretch with more markers than lanes overlaps as little as it can. Tests:
`web/tests/unit/reader/human-events.test.mjs`.

On the expanded timeline the other rows answer a hover the same way: the ribbon rows (Session, Message modes,
Agent events, Tool actions) show the card the workspace Timeline shows for that mark (`SessionRibbon`'s `hover`),
and a turn on the Turns row shows its header's words (`turnCardView`: number, who, when, the ask, counts and
tokens).

## Measured speed

One Mac (20 performance and 8 efficiency cores, 96 GB), a clone of a real library, the study's first development
model (a 68M-parameter encoder, fp32 ONNX), ONNX Runtime 1.31.1 on CPU. Other jobs were using a few cores
throughout, so these are conservative. The corpus: 16,264 typed messages in 5,782 conversations, 16,273 windows,
2,254,563 tokens; 5 messages were not scored (no letter).

| Run | Wall | Messages/s | Tokens/s | Inference | Worker memory (peak, each) |
|---|---|---|---|---|---|
| 1 worker × 8 threads, 16,384-token batches (first run, cold file cache) | 417 s | 39 | 5,400 | 409 s | 2.1 GB |
| 2 workers × 10 threads, 8,192-token batches | 189 s | 86 | 12,000 | 183 s | 1.4 GB |
| the same with the `w8` variant (weights quantized to 8 bits) | 131 s | 125 | 17,300 | 124 s | 1.4 GB |

Everything but inference is small: planning 0.5 s, reading 5 s warm (53 s on a cold cache, overlapped with
inference), framing and tokenizing 0.3 s, decoding 0.35 s, writing 5 s.

What mattered, on the newest 4,096 messages (788,000 tokens):

| Setting | Tokens/s |
|---|---|
| 1 × 8 threads | 5,500 |
| 1 × 16 threads | 9,500 |
| 1 × 20 threads | 9,400 |
| 2 × 10 threads | 11,500 |
| 3 × 8 threads | 11,300 |
| 2 × 10, batches of 8,192 tokens | 11,800 |
| 2 × 10, batches of 32,768 tokens | 10,000 |
| 2 × 10, `w8` | 16,500 |

One worker stops gaining past 16 threads; two workers of 10 threads use the same cores better. The labeler's memory
budget allowed at most three workers. Smaller batches are slightly faster and need less memory; the chosen model confirmed
this down to 4,096, which is the default (below). `w8` is 1.45 times faster but is not the same model in effect: on this corpus 78% of its spans
matched fp32's exactly, and a quarter of the messages had identical span sets (the development model puts many
tokens near the threshold). It stays opt-in (`engagement_variant`) until a model is validated with it. The `int8`
variant is not usable.

### The first real model (`bulk0`)

Same corpus and settings (2 workers × 10 threads, 8,192-token batches), run while a training job and other work
held the machine at a load average of 40 to 48, so the times are not comparable with the table above:

| Variant | Wall | Messages/s | Tokens/s |
|---|---|---|---|
| fp32 | 351 s | 48 | 6,600 |
| `w8` | 263 s | 65 | 9,100 |

At threshold 0.5, fp32 gives 246,971 spans over 16,259 scored messages: 15.2 per message, 77 messages with none,
and a median of three dimensions per message. `w8` reproduces 88.2% of fp32's spans exactly and 90.5% with the same
label and an overlap (IoU) of at least 0.8; 51% of messages have identical span sets. One span in ten differing is
too many for a default, so fp32 stays the default and `w8` stays opt-in.

### The chosen model (`bulk2`), quiet machine

`bulk2` (68M parameters, the same contract and labels) on the same corpus, with nothing else running. The default
is now batches of 4,096 padded tokens; a whole-library run is best started as
`pharos engagement run --workers 2 --threads 10` on a Mac with 20 performance cores (threads and workers otherwise
follow `labels_threads` and `labels_workers`, which are sized for background use).

| Full corpus, fp32 | Wall | Messages/s | Tokens/s | Worker memory (peak, all workers) |
|---|---|---|---|---|
| 2 × 10 threads, 8,192-token batches (cold file cache) | 196 s | 87 | 12,000 | 2.9 GB |
| 2 × 10 threads, 4,096-token batches | 164 s | 99 | 13,800 | 2.0 GB |

On the newest 4,096 messages (788,000 tokens):

| Setting | Tokens/s | Memory per worker |
|---|---|---|
| 2 × 10, 8,192 | 12,900 | 1.4 GB |
| 2 × 14, 8,192 | 10,900 | 1.4 GB |
| 3 × 9, 8,192 | 12,700 | 1.3 GB |
| 4 × 7, 8,192 | 12,600 | 1.4 GB |
| 3 × 9, 4,096 | 13,300 | 0.9 GB |
| 3 × 9, 16,384 | 12,100 | 2.0 GB |
| 4 × 7, 4,096 | 13,400 | 0.9 GB |

Throughput is flat once the 20 performance cores are busy: more workers add memory, not speed, and threads beyond
the performance cores slow it down. Three and four workers needed `labels_memory_budget_mb` raised from 8,192 to
16,384 for the experiment; two workers fit the default budget, so 2 × 10 with 4,096-token batches is the
recommended bulk setting.

At threshold 0.5, `bulk2` gives 184,608 spans over 16,259 scored messages: 11.4 per message, 113 messages with
none, and every one of the 104 labels appears. The most frequent label has 20,252 spans and the least frequent 1.

### Human events on the workspace Timeline

The workspace Timeline's session lanes show the same Human events (`LaneHumanEvents` in
`web/src/label-session-ribbon.tsx`): a row under the lane's Message modes, Agent events and Tool actions rows that
folds out by dimension with counts (disclosure `delegation-labels:<session>:human`), the same letter marks, hover
card and stacking (`web/src/human-events-rows.tsx` is shared with the reader), and Human events entries in that
page's Key, folded icon mode included (`timeline_visibility`, section `human`).

The lane data comes through the page's existing endpoint, `/api/work/<id>/parallel?labels=1`. Where engagement
is shown, `attachLaneEngagement` (`engagement.go`) reads spans by message ID for the rows shown only and gives
each of the person's messages:

| Field | Holds |
|---|---|
| `engagement` | the spans the page can show: score at least 0.70 and not a label the page never displays |
| `engagement_text` | an excerpt of the stored text: each span with up to 48 bytes on either side, parts that meet joined, "…" where text between two parts is left out |
| `engagement_typed` | the ranges of the excerpt the person typed |
| `engagement_at`, `engagement_length` | where each part of the excerpt starts in the excerpt and in the message, and the message's length |

Offsets are UTF-16 units, and a span's `start` and `end` index the excerpt. The excerpt is never longer than the
text, and a long message with a few spans costs what the spans do, so there is no size limit on the message. The
display rules stay in one place: `laneHumanEvents` (`human-events-model.ts`) hands the lanes to
`engagementLaneTimeline` in `reader-model.js`, which runs `messageEngagementView` on the excerpt as the reader
does on the text, so the threshold, whole words, typed text only, `register.filler` and the display names are the
reader's, and a marker's position in the message is the reader's too.

The threshold and the undisplayed labels are therefore written twice, in `reader-model.js` and in `engagement.go`.
`testdata/engagement-display.json` holds both values; a Go test and a JS test each compare their copy with it.

On the development library the saving is small, because no annotated message there is over 33 KB. For the
workspace with the most spans (3 lanes, 82 annotated messages), the response went from 959,485 to 847,050 bytes
and from 3,109 spans to 1,901; for a 40-lane workspace from 2,928,890 to 2,856,181 bytes. The request took about
4 s and 9.5 s before and after: the time is the rest of the endpoint's.

What differs from the reader:

- every lane shares one clock, so a message's slot is laid over what follows its instant
  (`humanEventPointSlots`) instead of widening the clock;
- a marker is a link that opens the conversation at the message; it does not scroll to the span;
- a span longer than 600 bytes is quoted from its first 504 and last 96, so its card can show "…" inside the
  quote;
- the Caller row has no Human events row; they are on the session's lane.

### The Key as icons only

The reader's Key has an **Icons only** toggle. On, the Key is one strip of swatches (Timeline entries with the
Human events letters, Content icons, Highlights with the Engagement lines); each names itself on hover and focus
and opens the same menu on click, as the workspace Timeline's folded key does (`ChartLegendChip` `iconOnly`). The
choice is a preference saved with the library (`pharos-reader-key-compact` through `pharosPrefs`;
`web/src/reader-key-compact.ts`), so it holds for every conversation and is never in a URL. It is classified as a
setting in `docs/view-controls.json`: it changes how the same entries are drawn and hides no content, so it is a
pressed-state toggle rather than a disclosure.

### Token usage records are never shown

The reader never shows the token usage and cost records a harness writes between messages, which the Key listed as
Usage & quota › System events › Event (`transcriptUndisplayed` in `reader-model.js`; the service's Find mirrors it
in `transcript_find.go`). Their information is already shown in the Turns · context bar and in each turn's token
summary, both of which still read them. The kinds: a `token_usage` payload, and metadata of type `token_count`,
`token_usage_record` or `cost-state`, and Claude's `system` / `thinking_tokens`. They get no row, no Key entry and
no count, and Find leaves them out. Other System events (for example `reasoning_unavailable`, `task_started`,
`token_budget`) stay, as does a usage record that is an error. The Usage & quota group shows only when something
else is in it.
