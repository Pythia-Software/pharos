# Local labels

Pharos can label every message's function on this Mac: what a person asked
for, how they steered, what they accepted or challenged, what an agent
reported, planned or got stuck on. The labels come from `kestrel-68m`, a
68M-parameter encoder fine-tuned to reproduce the labels large models assign
in [library labeling](library-labeling.md) (taxonomy `candidate-0.3`; the
research is in [labeler-research/](labeler-research/README.md), the decision
in [shipping-encoder.md](labeler-research/shipping-encoder.md)).

**Off by default.** Labels are stored in the catalog, shown by
`pharos labels status` and `pharos labels explain`, and highlighted in the
conversation reader when its **Message labels** highlight is on
([In the reader](#in-the-reader)). The [Message Labels dashboard and query pages](message-labels.md)
explore the stored labels without running inference. Conversation timelines
combine message labels, caller-text origin and recorded tool actions.

## Turning it on

In **Settings → Background Processing → Labeling on this Mac** (below the
schedule), turn on **Message labels** (beside it, **Agent events (preview)**
turns on [agent events](agent-events.md)). This saves `labels_enabled` in the library's configuration
and applies it immediately, without restarting Pharos. The control stays
visible while off, reports missing downloads or workers while on, and
queues earlier messages for the background schedule. In the app, enabling
also starts any missing model/runtime download. Turning it off prevents
future automatic runs; a run already in progress finishes and existing
labels remain available in the reader. The setting follows the library to
its other Macs when they next open it.

The model's weights are not in the repository or the app: each Mac
downloads them once, after install, into its own support directory
([Model download](#model-download)). Configuration (`archive.toml` or
`library.toml`) or the environment can point at a model directory, worker or
runtime instead, and comes first:

| Key | Environment | Meaning |
|---|---|---|
| `labels_enabled` | — | `true` labels messages as the chain's last stage and in the Message labels background item, and downloads the model when it is missing (default `false`) |
| `labels_model_dir` | `PHAROS_LABELS_MODEL_DIR` | the model directory (below); default the app bundle's, then the download |
| `labels_variant` | — | `int8` or `fp32`; managed downloads use the model release's default, while custom directories retain int8 unless set |
| `labels_worker` | `PHAROS_LABELS_WORKER` | the worker executable; default `pharos-labeler-ort` beside `pharos` |
| `labels_backend` | — | `auto` (default), `cpu`, or `mlx`; shared by message labels and agent events. MLX needs a verified FP32 companion ([Apple silicon GPU](#apple-silicon-gpu)) |
| `labels_mlx_runtime` | `PHAROS_MLX_LIBRARY` | native MLX engine override; default `mlx/libpharos_mlx.dylib` beside the worker |
| `labels_runtime` | `PHAROS_ORT_LIBRARY` | the ONNX Runtime library the worker loads; default the one beside the worker, then the download |
| `labels_threads` | — | CPU intra-op threads per worker (default 4); Settings → Background Processing → Labeling on this Mac → Message labels → CPU threads; ignored by MLX GPU inference |
| `labels_workers` | — | worker processes (default 1) |
| `labels_batch_tokens`, `labels_max_batch` | — | a batch's padded-token budget (default 16,384) and row limit (64) |
| `labels_thread_budget`, `labels_memory_budget_mb` | — | what every model's workers may use together: intra-op threads (default the larger of both models' workers × threads, message labels' and [agent events'](agent-events.md#the-pass), and half the cores; on battery, see [Scheduling and power](#scheduling-and-power)) and estimated memory (default 8,192 MB). A thread budget set here holds on battery too |
| `labels_idle_seconds` | — | how long finished workers stay warm for the next run (default 120) |

Relative paths resolve against the configuration file's folder. To build the
worker and fetch ONNX Runtime:

```sh
macos/fetch-onnxruntime.sh            # pinned release, SHA-256 checked, into build/onnxruntime-1.31.1/
CGO_ENABLED=1 go build -o build/pharos-labeler-ort ./cmd/pharos-labeler-ort
```

## Apple silicon GPU

The worker supports native C++ MLX inference for the Ettin 68M target-pooling
Kestrel message classifier and Jayhawk event/sentence classifier. Tokenization,
framing, thresholds and the worker protocol are shared with the CPU path.
MLX runs compiled FP32 operations on Metal; Python is unnecessary at runtime.
See [MLX compilation](https://ml-explore.github.io/mlx/build/html/usage/compile.html).

The pinned MLX 0.32.4 **macOS 26 wheel** and its native engine require macOS
26.2 or later and Apple silicon. These are the runtime binaries validated for
this backend; this is not a claim about every MLX distribution. Pharos and its
CPU worker retain their macOS 14 minimum. The app build bundles the engine,
`libmlx.dylib`, `libjaccl.dylib`, `mlx.metallib` and license notices, signing
nested libraries before the app. The service never links MLX.

`auto` selects MLX only for a selected FP32 ONNX model with a verified MLX
companion. Unsupported machines or unavailable native libraries use that same
FP32 ONNX file on CPU, with a startup diagnostic. `mlx` requires the GPU backend
and returns an error if it cannot start. Corrupt companions are errors; a
running session never changes device silently. Ready metadata reports the
actual runtime, device and provider. `cpu` forces ONNX Runtime.

Version 1 downloads contain only int8 ONNX, so they continue on
CPU. A prepared version 2 release kit includes int8, FP32 ONNX and MLX in each
model archive. Its pending descriptor selects FP32 for managed downloads;
after publication and pin promotion, enabling labels/events downloads those
models and selects MLX automatically on supported Macs. End users need no
model paths or companion configuration. Explicit int8 settings and custom
model directories retain their existing behavior. CPU fallback for a selected
FP32 model always uses FP32, preserving its model identity across devices.
See [Cleanroom model publication](#cleanroom-model-publication).

Build the native runtime and worker locally:

```sh
macos/build-labeler-mlx.sh build/mlx
CGO_CFLAGS="-O2 -g -mmacosx-version-min=14.0" \
  CGO_LDFLAGS="-mmacosx-version-min=14.0" CGO_ENABLED=1 go build -o build/pharos-labeler-ort ./cmd/pharos-labeler-ort
```

In an export environment with Python 3.11+, numpy, torch, onnx and safetensors,
add each companion to a **copy** of its complete FP32 model directory:

```sh
python3 tools/export_labeler_mlx.py /path/to/message-model \
  --checkpoint /path/to/message-checkpoint.pt \
  --encoder-config /path/to/encoder-config.json --task kestrel
python3 tools/export_labeler_mlx.py /path/to/event-model \
  --checkpoint /path/to/event-checkpoint.pt \
  --encoder-config /path/to/encoder-config.json --task jayhawk
python3 tools/package_labeler_model.py /path/to/message-model --variant fp32 --out build/models
```

The exporter checks the supported encoder configuration and that every FP32
checkpoint tensor occurs in the ONNX export (including transposed matrices).
It writes `model.mlx.safetensors`, metadata identifying `ettin-68m-v1` and its
task, the source ONNX SHA-256, and the companion digest in `manifest.json`.
Both model loaders verify the digest and source identity. The native engine
checks exact tensor names, shapes and FP32 types. The canonical model file
and SHA in the protocol remain the FP32 ONNX reference. As with every model
update, changing the manifest triggers the ledger's existing rebuild rule.

Configure the converted directories in the library configuration:

```toml
labels_model_dir = "/path/to/message-model"
labels_variant = "fp32"
agent_events_model_dir = "/path/to/event-model"
agent_events_variant = "fp32"
labels_backend = "auto"
# Development builds only; app bundles find these beside the worker:
labels_worker = "/path/to/pharos/build/pharos-labeler-ort"
labels_mlx_runtime = "/path/to/pharos/build/mlx/libpharos_mlx.dylib"
# labels_runtime still locates ONNX Runtime for CPU fallback.
```

One GPU worker is admitted across both models, with at least 3,072 MB reserved
from the existing memory budget. The native engine sets MLX's soft evaluation
limit to 3 GiB and its unused-buffer cache limit to 512 MiB. These are allocator
guidelines, not a hard process-memory cap; MLX can temporarily exceed them
during evaluation. Without an explicit cache limit, varied batch shapes can
retain tens of gigabytes of unused Metal buffers on a large-memory Mac.
Closing the model also clears that buffer cache. Idle workers are evicted when
the other model needs the GPU; active contention returns the existing
budget-exhausted error.
Admission conservatively reserves this capacity for eligible `auto` sessions
even when startup subsequently falls back to CPU. Power, cancellation, restart
and idle-expiry policies still apply. Batches respect the worker's 256-row and 16,384
padded-token limits; Kestrel accepts 512 tokens and Jayhawk 768 per row.

### Device status and GPU capacity

Settings reports the execution device separately for Message labels and Agent
events. Before a run, it combines the selected model variant with the worker's
`--probe-mlx` capability check (OS, worker build, native engine and Metal device).
The probe loads no model and runs no inference; results are cached for a minute
and invalidated when the worker or engine changes. The label is **expected** until
the worker's Ready handshake confirms its device. An automatic CPU fallback is
shown explicitly, including after an idle worker exits. Replacing the model,
worker or runtime invalidates that observation. Missing models and older/custom
workers that cannot report capabilities show an unconfirmed device instead of
guessing. A forced `mlx` configuration that cannot use GPU shows unavailable.

**CPU threads** appears only for expected or confirmed CPU execution. The saved
CPU target is retained for fallback. Changing it does not restart an active GPU
worker; CPU fallback workers still retune. Threads remain a limit on ONNX
intra-op parallelism, not an assignment of physical CPU cores or a cap on the
whole process's CPU usage.

There is no GPU core-count or utilization-percentage control. This engine submits
compiled operations to the default Metal stream; MLX's
[streams](https://ml-explore.github.io/mlx/build/html/usage/using_streams.html)
choose execution queues, while its
[memory limit](https://ml-explore.github.io/mlx/build/html/python/_autosummary/mlx.core.set_memory_limit.html)
is an allocation guideline. Neither assigns a share of GPU cores. Increasing
that memory limit would not by itself request more compute.

Higher throughput can be investigated through batching and the CPU preparation
pipeline. The current defaults allow 64 rows and 16,384 padded tokens per batch,
two outstanding batches, and one GPU worker shared across models. The engine
accepts up to 256 rows, but the token ceiling still applies. Increasing rows can
help short inputs; longer inputs already fill the token budget. Extra workers or
streams compete for the same device and memory and need measurement before
changing admission policy. Keep the existing limits and automatic scheduling
until representative end-to-end measurements show better throughput without
unacceptable memory use or responsiveness. No GPU intensity slider is exposed.

The optional Go integration test can recheck both native classifiers against
ONNX Runtime with short and maximum-length mixed batches, output selection,
padding and empty sentence slots:

```sh
PHAROS_MLX_TEST_MODELS="/path/to/message-model:/path/to/event-model" \
  PHAROS_MLX_LIBRARY="$PWD/build/mlx/libpharos_mlx.dylib" \
  PHAROS_ORT_LIBRARY="/path/to/libonnxruntime.dylib" \
  go test -run TestMLXNativeParity -v ./cmd/pharos-labeler-ort
```

The native implementation was checked on an M3 Ultra against saved FP32 ONNX
CPU outputs for 582 messages and 396 event candidates. All message label sets,
event verdicts, family decisions and sentence decisions matched. Maximum
probability differences were 0.000013 for messages and 0.000005 for events and
sentences. These are corpus checks, not a guarantee of identical floating
point values for every possible input.

## Commands

```sh
pharos [--config PATH] labels run [--limit N] [--batch-tokens N] [--max-batch N] [--workers N] [--threads N] [--backend auto|cpu|mlx] [--full] [--one-at-a-time] [--dry-run] [--json]
pharos [--config PATH] labels status [--json]
pharos [--config PATH] labels explain MESSAGE_ID [--infer]
pharos [--config PATH] labels fetch
```

- **run** brings the ledger up to date and prints what it did. It first
  brings the authorship ledger up to date if that is behind, since human
  messages are labeled from their typed pieces. `--limit N` stops after N
  messages; the rest stay pending for the next run. `--full` rebuilds every
  conversation's items, not only changed ones; only messages whose inputs
  changed are labeled again either way. `--one-at-a-time` sends one item per
  batch, one batch at a time: the baseline batching is measured against.
  `--dry-run` builds and frames the items and counts what would be labeled,
  with no worker and no label writes (it creates the empty ledger tables on
  a catalog that has none). Interrupt (Ctrl-C) stops between
  batches and keeps what was stored. It runs as operation `analysis.labels`
  under a Terminal lease, like other writing commands. It runs even when
  `labels_enabled` is off, and says so.
- **fetch** downloads what this Mac still needs (the model, and ONNX
  Runtime when the worker has none beside it), resuming a partial download,
  and prints progress; with nothing missing it prints where each is.
- **status** reads only: whether labeling waits for a download (not yet
  downloaded, downloading with progress, or partly downloaded), labeled
  messages by segment, conversations built
  and dirty, whether the ledger is current for this authorship generation,
  model and contract, and whether the model verifies. A catalog that never
  ran labels reads as never built; status creates nothing.
- **explain** shows how one message is labeled: why it is not a unit, or its
  items as the model sees them (segment, cohort, sender role, context, text,
  token counts, the segment's thresholds), its stored labels with
  probabilities, and whether they match its current inputs. `--infer` runs
  the model on it now, chunk by chunk.

## Architecture

```
analysis chain (after findings, when labels_enabled)      pharos labels run
        │                                                         │
        └───────────────► Catalog.RunLabels ◄─────────────────────┘
                              │
   choose conversations ──────┤  dirty marks (triggers), structure fingerprints,
                              │  label counts, forks of changed parents
   labelsItemBuilder ─────────┤  labeling prepare's own pass over the live catalog,
   (labels_items.go)          │  human pieces from the stored authorship ledger
   tokenizer + framing ───────┤  pure Go (internal/labeler), kestrel-framing-v1
   pack into batches ─────────┤  by length, under a padded-token budget
   worker pool ───────────────┤  separate processes, ≤ 2 batches in flight each,
   (internal/labeler)         │  restart with backoff, retry lost batches
   decide + merge ────────────┤  thresholds per segment, closure, chunk-union-v1
   writer ────────────────────┘  batched write transactions (≈500 rows or 2 s)
                                       │
                               message_labels (derived ledger)
```

- **The main process stays pure Go.** Everything that touches text (items,
  tokenizer, framing, thresholds) is Go in `pharos`. Only tensors cross the
  process boundary. `internal/labeler` is a small package of its own, outside
  the one `internal/archive` package, because the worker binary shares its
  protocol code and must not link the catalog.
- **The worker is a thin tensor runner** (`cmd/pharos-labeler-ort`). It is
  Go with cgo against the ONNX Runtime C API, and it loads the library at run
  time with `dlopen`, so neither binary links ONNX Runtime. The vendored C API
  headers (v1.31.1, unmodified) and ONNX Runtime's MIT license are in
  `cmd/pharos-labeler-ort/include/`.
  - It finds the library from `--runtime PATH`, then `PHAROS_ORT_LIBRARY`,
    then a `libonnxruntime.dylib` beside the executable, then the loader's
    search path. It asks for the header's C API version (31) and refuses an
    older library.
  - Session options are ONNX Runtime's defaults except the intra-op thread
    count, as in the Python reference.
  - Replies go out on a duplicate of file descriptor 1, which is pointed at
    stderr, so a native library's prints cannot corrupt the frames.
  - Built without cgo, it still speaks the protocol and refuses every hello
    with code `model`. `--version` prints its version.
  - Workers for other platforms or runtimes are drop-in (below).
- **One new module dependency:** `golang.org/x/text`, for the tokenizer's NFC
  normalizer (`unicode/norm`). The standard library has none, and NFC must
  match the tokenizer's exactly; the Go project maintains this package.

## The parity contract

The labels are only as good as the inputs' match with training. Three layers
guard it.

1. **One item builder.** The app builds its items with `labeling prepare`'s
   own per-conversation pass: cohort, human pieces and markers
   (`human-pieces-v3`), context (`context-v3`, the 600-character prompt the
   teachers saw), chunking (`chunking-v1`), and the message's role as the
   framing's sender (`units.sender_role`, never the sender identity: the
   FRAMING bug). Prepare classifies authorship in memory over its snapshot;
   the app reads the stored ledger, which the chain brings current before
   labels run. The pass reads through a reader interface, takes authorship
   from an accessor and runs items-only, skipping duplicate checks, actions
   and statistics. Both sides produce `labelingItem` from one function, which
   also computes the **segment** (`avocet-segments-v1`): the agent cohorts map
   to `AIUR`, `AR`, `DEL`, `TH`. A human item takes the statuses of the target
   and context pieces overlapping its chunk; pieces that are not shown count
   for every chunk. Typed text alone gives `H-typed`, typed text with context
   or another target kind `H-typed+ctx`, then feedback, template, re-sent.
   Datasets store each item's segment too.
   `TestLabelsItemsEqualPreparedItems` checks that the app's items equal
   prepare's, field for field, on the seeded fixture catalog. Codex
   continuations, which prepare once classified differently from the
   stored ledger, are one rule in both since `authorship-v12`
   (`authorshipContinuations`).
2. **Tokenizer and framing** (`internal/labeler/tokenizer.go`, `framing.go`):
   a pure-Go reading of the model's `tokenizer.json` (ModernBERT/ettin
   byte-level BPE with NFC and added tokens, as the `tokenizers` library does
   it), and `kestrel-framing-v1` exactly: the `cohort sender :` tag, the
   4,096-character text pre-cut, context cut by characters for agent segments
   and by tokens for human ones, the 512-token budget with at least 64 context
   tokens, and head-and-tail text truncation. `testdata/labeler/` holds the
   187 invented-string parity cases (no conversation text) and the unchanged
   ettin tokenizer (MIT). Token ids must match exactly.
3. **Scores and labels.** The model's `probs` output is the scorer (the mean
   of the `y3` and `y_hf` heads' sigmoids). A label is on when its probability
   reaches the segment's threshold (float32, as in the reference), and every
   label that is on adds its ancestors. A worker-backed test
   (`TestModelParityWithWorker`) checks a model directory's parity fixture:
   token ids and text spans exactly, probabilities within 1e-4, logits within
   1e-3, closed sets. It runs with the real model when
   `PHAROS_LABELER_MODEL_DIR`, `PHAROS_LABELER_WORKER` and
   `PHAROS_ORT_LIBRARY` are set, and skips with a message otherwise.

**Long messages** (more than 6,000 characters) are several items. Each chunk
is labeled with its own segment's thresholds. The message's labels are the
union of the chunks' closed sets and its probabilities the per-label maximum
(`chunk-union-v1`). This merge is not validated against teachers.

## Batching and incrementality

**What runs.**
- A run rebuilds the items of conversations that may have changed:
  - those marked dirty;
  - those whose structure fingerprint changed: the work's mirror status, the
    conversation row, whether reasoning extraction is current, and its agent
    sessions' kinds;
  - those never built;
  - those whose stored label count no longer matches their unit count, so
    labels deleted behind the ledger's back come back;
  - forks of any whose inputs changed, which copy their parent's history,
    and conversations that re-send one of their messages: a re-sent
    message's target text is resolved against its source's classification,
    so a source that becomes, say, quoted agent text changes the copy's items
    although the copy's own spans stay the same. Both follow transitively.
    A conversation pulled in this way gets a dirty mark of its own before
    the run starts work, cleared only once it is built: the source's own
    mark is cleared when the source is stored, so a run cut short by
    `--limit` or a stop would otherwise lose it.
- Triggers on `messages`, `message_authorship`,
  `reasoning_extraction_state`, `agent_sessions`, `agent_session_messages` and
  `conversations` write the dirty marks (`message_labels_dirty`). Each mark
  carries a fresh token, and a run clears only the marks it read: a write
  during the run leaves its mark for the next run.
- Building items costs reads, not inference. A message is labeled again only
  when its input hash (each chunk's segment, cohort, sender role, context and
  text), the model identity or the contract differs from its stored row.
- A message that is no longer a unit loses its row. A conversation that left
  the catalog loses all of its rows.

**Batches.**
- Items are framed as they are built. They gather in a window of a few
  batches' worth, are sorted by token length and packed into batches whose
  rows × padded length stays under `labels_batch_tokens`, at most
  `labels_max_batch` rows.
- A short run fills no window: it is packed as soon as the builder pauses,
  so a few new messages go out as one batch.
- Each worker has at most two batches in flight, so it never waits for the
  next one and a stop waits for little.
- **Padding is part of the contract.** Batches are right-padded to a
  multiple of 8 (`labeler.PadMultiple`). For the int8 model the padded length
  matters: at a length that is not a multiple of 4 its outputs move by up to
  0.05 in a probability (ONNX Runtime does the same from Python). At any
  multiple of 4 they equal the fixture's to 5e-7. FRAMING.md's "padding does
  not change the outputs" holds for fp32 only.

**Writes.** Finished conversations are stored in write transactions of
about 500 rows or every 2 s. A conversation counts as built (its fingerprint
stored, its dirty mark cleared) only when all of its messages are stored. A
conversation cut short by `--limit`, or holding a message the worker
rejected, stays pending. So does the background stage: a run that ends
incomplete (a failed message, an unbuildable conversation) leaves the labels
item pending, so the schedule tries again.

**The generation.** A run that covered everything, with no dirty mark left
and authorship unchanged meanwhile, records `labels_generation` =
authorship generation | model identity | contract, and `labels_built_at`. The
model identity is the model's name, its input contract, its file's SHA-256
and its manifest's (which carries the thresholds and the tokenizer's hash).
Every row records it, so switching models in either direction
(`kestrel-68m-tpool` and `kestrel-68m`) relabels every message, and a run
after that has nothing to do (`TestLabelsRelabelOnAModelSwitch`). The
contract is
`kestrel-framing-v1+labeling-units-v1+human-pieces-v3+context-v3+chunking-v1+avocet-segments-v1+chunk-union-v1`.
A new model or contract relabels every message.

**One run at a time.** A process allows one run, and a lease beside the
catalog (`catalog.sqlite3.labels.lock`) keeps the service and `pharos labels
run` apart. A second run reports that labeling is already running.

## The analysis chain and stopping

- With `labels_enabled` on, `analysis.labels` ("Label messages") is the
  chain's last stage, after findings, so a backlog never delays them.
  Without the setting the chain runs exactly as before, and the catalog gets
  no labels tables.
- The stage labels at most 2,000 messages inline, so an index ends in bounded
  time. A larger backlog (the first run on a library) continues as a
  background `analysis.labels` run (variant `backlog`) when this Mac is
  plugged in ([Scheduling and power](#scheduling-and-power)).
- Every run takes conversations newest first (by start time), as agent
  events do, so a backlog labels recent conversations before older history.
- No page read starts labeling. Pages read whatever ledger exists.

## In the reader

The reader's **Highlight** row, under Show, has a checkbox per kind of mark:
**Human writing** (on by default), **Agent events** ([agent
events](agent-events.md#in-the-reader)) and **Message labels**. Each is
switched on its own (`transcriptHighlightControl` in `ui.py`, with the options
from `readerHighlightOptions` in `reader-model.js`), in the full reader and in
a library card's Browse preview. A library that has labeling on offers
Message labels on every conversation:

- **On**, each labeled message's text is highlighted in its family's hue:
  Report, Control, Deliberation, Input, Feedback, Request, Frame, Difficulty
  (`LABEL_FAMILIES` in `reader-model.js`, the reference categorical palette's
  eight hues in that order, stepped for each theme). A message labeled only at
  a root (asks or steers, responds or reports) is grey. The family is that
  of the message's likeliest specific label; a legend lists the families the
  conversation uses. Where labels and human writing cover the same text, the
  label's hue wins; an agent event's sentence keeps its event hue.
- **What is highlighted** is the text the labels describe: a person's typed
  pieces when authorship finds them, otherwise the whole message
  (`labelMask`). Pasted or quoted context in a typed message stays plain.
- **Hovering** highlighted text shows the item's segment in words (typed
  text, agent reply…) and its most specific labels, each with its family,
  the model's probability and the taxonomy's definition (`messageLabelView`).
- **The setting** is view state, `reader_labels` in the URL (beside
  `reader_writing` and `reader_events`), so a copied link keeps it; the last
  choice is saved as the fallback for links without it. A checkbox only
  shows or hides the highlight; nothing re-renders.
- A conversation with a dirty mark, or never built, says "Still labeling
  this conversation" or "Not labeled yet" (the checkbox is disabled until
  it has labels).
- **Only prose messages** are highlighted. Thinking and delegations render as
  activity rows and are labeled but not shown yet.

`WorkConversation` attaches them (`messageLabelsAttacher` in `labels.go`):
each message's `labels` (`segment`, `ids` in the model's order, `probs` for
those ids), and the conversation's `labels` (`labeled`, `pending`, and the
taxonomy rows its labels name). It only reads, and attaches nothing to a
catalog without a ledger.

## Fingerprints in result tables

Library search and other workspace tables offer **Fingerprint** in the column
chooser. Conversation and message tables offer their conversation's
**Fingerprint** and, when the workspace is known, **Workspace fingerprint**.
Selected fingerprints also appear in Library's conversation cards. Column
selection, order and width travel with saved queries and copied URLs.

These use the same compact message-label ribbon as label-search results:
colors show accepted labels and hatching shows recorded branch overlap.
A workspace combines its conversations in time order. Fingerprints load only
when displayed, send no message bodies, and never start labeling. Each reads
at most 2,400 retained events; **Sample** marks a truncated fingerprint.
Pending conversations remain unlabeled without hiding accepted labels from
other conversations in the workspace.

## Scheduling and power

Labels follow the [background updates](background-work.md#11-background-updates)
schedule rather than one of their own:

- **Message labels** is a background item (Hourly before measurements,
  adaptive like Your writing; measured by `analysis.labels` runs, not
  `backlog` runs, so a backfill does not push it to Daily). It exists only
  where `labels_enabled` is on. Settings' overrides, Run now, pause, hold
  when busy and the battery policy apply to it as to every item; by default
  Hourly work waits on battery.
- Indexing marks it pending (`sync_deferred`), with the chain's other
  stages. A pass that labels everything completes it. A pass that leaves a
  backlog, or waits for the model download, keeps it pending, so a later
  pass carries on.
- **The budget comes from the power source** (`labelsForPower`, read as the
  schedule reads it, from `pmset` at most once a minute):

  | | Plugged in (or no battery) | On battery |
  |---|---|---|
  | Workers × threads | `labels_workers` × `labels_threads` | 1 × at most 2 |
  | Thread budget | `labels_thread_budget`, else the larger of workers × threads and half the cores | the run's threads |
  | Batch | `labels_batch_tokens` | at most 4,096 padded tokens |
  | Backlog past the chain's 2,000 | continues as a `backlog` run, which stops within a minute of unplugging | waits for power |

  `labels_thread_budget`, when set, overrides the battery reduction too, and
  `labels_memory_budget_mb` applies to both. The runtime's budget is set
  before workers start (`Manager.SetBudget`): a smaller one stops idle workers
  over it at once.
- **CPU threads is a live target.** Message labels and agent events read their
  Settings targets when workers start and every two minutes during an active
  run. When the target changes, message labels finish their in-flight batches
  and agent events finish their current scoring chunk, then replace the
  workers with the new thread count. Queued work and finished results are
  kept, and no app relaunch is needed. The shared budget and battery cap
  still apply. GPU workers ignore thread changes; the CPU target remains saved
  for fallback. Command-line runs keep the thread count they started with.
- Low battery (under 20%) or Low Power Mode makes the item wait, as it does
  all background work, unless you Run now.

| Event | What happens |
|---|---|
| Stop, eject or release | The run stops between batches and kills its workers rather than waiting for a batch in flight, including one still being written to a worker's input. Finished conversations are stored, and the next run resumes from the dirty marks and label counts. |
| A worker crashes, does not answer a batch within 5 min, or stops reading its input so a batch cannot even be sent (it is killed) | Its batches in flight fail, the worker restarts after a backoff (0.5 s doubling, at most 30 s) and lost batches are sent again, at most twice. |
| A batch keeps crashing workers, or the worker rejects it (`bad_request`, `run_failed`) | The batch is split and sent one item at a time, so one bad item fails only its message, which stays pending for the next run. |
| Workers keep crashing (8 restarts in a row with no batch succeeding) | The run ends **interrupted**, keeping what it stored. |
| A conversation cannot be built (a damaged row; a panic is caught) | It keeps its dirty mark for the next run; the others go on. A stored classification that no longer fits its message counts as none until authorship catches up. |
| Another operation holds the catalog writer | Finished labels wait in the bounded pipeline, slowing inference until the writer is available. Waiting during a running backfill has no separate save deadline. On stop, all finished work shares one 30-second drain budget; work that cannot be saved stays pending for the next run. |
| The labels cannot be stored | The run stops at once with the write's error. |

The worker holds no file on the library's drive, so it never delays an eject.

## The ledger

```sql
CREATE TABLE message_labels (
  message_id TEXT PRIMARY KEY,
  conversation_id TEXT NOT NULL,
  segment TEXT NOT NULL,          -- the first chunk's
  labels INTEGER NOT NULL,        -- closed label set, bit j = manifest.labels.order[j]
  probs BLOB NOT NULL,            -- float32 little-endian per label (max over chunks)
  chunks INTEGER NOT NULL, tokens INTEGER NOT NULL,
  input_hash TEXT NOT NULL,
  model TEXT NOT NULL,            -- name/input contract/model SHA-256/manifest SHA-256
  contract TEXT NOT NULL,         -- framing and item contracts
  labeled_at TEXT NOT NULL);
CREATE TABLE message_labels_conversations (conversation_id TEXT PRIMARY KEY, structure TEXT NOT NULL, units INTEGER NOT NULL, built_at TEXT NOT NULL);
CREATE TABLE message_labels_dirty (conversation_id TEXT PRIMARY KEY, token TEXT NOT NULL);
CREATE TABLE message_labels_models (model TEXT PRIMARY KEY, labels_json TEXT NOT NULL); -- each model's label order
```

A row's `labels` bits mean nothing without its model's label order, so each
run records the order under the model's identity before it writes labels;
readers decode every row with the order of the model that wrote it.

The first labels run creates these tables and triggers (`labels_schema_version`
3 in `meta`; a run on an older version adds what is missing and replaces the
triggers). There is no catalog schema version bump: the tables are
derived, older builds ignore them (and still mark conversations dirty through
the triggers), and every row records the inputs it was made from.

## The model artifact

A model directory holds:

| File | What it is |
|---|---|
| `manifest.json` | name, base model, framing contract, input contract (`input_contract`), files and their SHA-256s, label order, parents and ancestors, heads and scorer, segments, per-segment thresholds (`default` for the rest), training data and licenses, metrics |
| `model.int8.onnx`, `model.fp32.onnx` | inputs `input_ids`, `attention_mask` (int64 `[batch, seq]`); outputs `logits` `[batch, 5, 54]`, `probs` `[batch, 54]` |
| `tokenizer.json` | the ettin-encoder-68m tokenizer, unchanged |
| `FRAMING.md`, `reference.py`, `parity-fixture.json` | the framing contract, the Python oracle, the invented-string fixture |

Loading a model verifies the chosen variant's file and the tokenizer against
the manifest's SHA-256s, and refuses a manifest for a framing or input
contract this build does not implement.

**Input contracts** say which inputs a model takes, how Pharos makes them,
and how they are padded (`labeler.InputContracts`):

| Contract | Inputs (default names) | Padding |
|---|---|---|
| `ids-attention-v1` | `input_ids` (ids), `attention_mask` (attention); mean pooling over every real token happens inside the model. `kestrel-68m`'s manifest predates the field and means this. | multiple of 8 |
| `ids-attention-pool-v1` | the same plus `pooling_mask` (pool): 1 on the target text's tokens, so the model averages only those while attending to everything (LAPWING's target-only pooling) | multiple of 8 |

- The manifest may name the inputs (`inputs`, a list of `{name, role}`,
  which must have the contract's roles) and raise the padding multiple
  (`padding.multiple`); the runtime pads to a multiple of both.
- The pooling mask comes from the span the framing built for the text
  (`FrameSpans.Text`), never from looking for separator ids. Framing returns
  the token spans of the tag, the context and the text as it builds the
  sequence.
- An item whose text yields no tokens has nothing to pool. It is never sent:
  the message is counted as `MessagesEmptyTarget` and left unlabeled.
- **Padding to a multiple of 8 is part of every contract:** the int8 models'
  outputs change when the padded length is not a multiple of 4.
- The protocol, the workers' loops and the ledger are the same for every
  contract (`TestLabelsCarryAPoolingInput`, and the ONNX Runtime worker's
  declared-input tests).

A missing or failing model disables labeling with the reason in `labels
status`.

**The shipped model** is LAPWING's target-pooling `kestrel-68m-tpool`
(`ids-attention-pool-v1`). With no `labels_model_dir`, Pharos looks for it
at `../Resources/labeler/kestrel-68m-tpool` beside its executable, as in an
app bundle. Configuring another directory, such as `kestrel-68m`
(`ids-attention-v1`), still works: the runtime serves both contracts.

**Never commit weights.** The weights derive from this library's private
conversations; the fixture's strings are invented. The app does not carry
them either: see [Model download](#model-download).

The model is never stored on the library drive.

## Model download

The build pins the assets it downloads in `internal/archive/labels/assets.json`
(embedded in `pharos`):

| Asset | Pinned | Where it comes from |
|---|---|---|
| `model` | name `kestrel-68m-tpool`, version `1`, SHA-256 and size of the archive | the Pharos release `labeler-kestrel-68m-tpool-1`, asset `kestrel-68m-tpool-1.tar.gz` (URL pattern `…/releases/download/labeler-{name}-{version}/{name}-{version}.tar.gz`) |
| `onnxruntime` | `onnxruntime-osx-arm64` 1.31.1, SHA-256, size, and the library's path inside | Microsoft's ONNX Runtime release, as `macos/fetch-onnxruntime.sh` pins it; only needed when no `libonnxruntime.dylib` sits beside the worker |
| `agent_events_model` | name `jayhawk-68m`, version `1`, SHA-256 and size of the archive | the agent event verifier ([agent-events.md](agent-events.md)), from the release `labeler-jayhawk-68m-1` by the same URL pattern; fetched where `agent_events_enabled` is on |

- **Where.** `<support>/labeler/<name>-<version>/`, where `<support>` is
  this Mac's support directory (`~/Library/Application Support/Pharos`, or
  `PHAROS_SUPPORT_DIR`): never a library drive, never the repository. Every
  library on the Mac shares it.
- **How.** `fetchLabelsAsset` downloads to `<name>-<version>.download`,
  resuming a partial file with a range request (a server without ranges
  starts again), gives up on a transfer that receives nothing for a minute
  (keeping what arrived, so the lock is freed and the retry resumes),
  refuses more bytes than pinned, checks the size and SHA-256,
  unpacks into a new hidden directory (regular files, directories and links
  that stay inside it; anything else fails), writes `.verified-<sha256>`,
  and renames it into place. A reader sees all of it or none; a mismatch
  deletes the download.
- **Who.** Operation `labels.fetch` in the broker's registry: when a library
  with `labels_enabled` opens on a Mac that lacks an asset, the broker
  downloads it once, then asks every open library's service to run its
  Message labels item now. A failed download is retried 15 min later and
  on the next open; an interrupted one resumes. `pharos labels fetch` does
  the same from Terminal. A lock in the support directory
  (`labeler/.fetch.lock`) keeps it to one download per Mac.
- **Until then** labeling waits: the stage keeps its work pending, `pharos
  labels status` and the Message labels item say "waiting for the download"
  with each asset's state (not yet downloaded, downloading with progress,
  the last attempt's failure and time, partly downloaded), and `pharos
  labels run` fails saying so. A failure is recorded beside the download
  (`labeler/<name>-<version>.failed`) until an attempt succeeds.
- **Without a worker** no download helps, so Settings and the Message
  labels and Agent events items lead with that ("can't run"), and `pharos
  labels status` reports it as `problem`.
- **Precedence.** `labels_model_dir` / `labels_runtime` (or their
  environment variables), then the app bundle (the worker in
  `Contents/MacOS`, its runtime beside it, a model in
  `Contents/Resources/labeler/kestrel-68m-tpool`), then the download.
- `PHAROS_LABELS_MODEL_URL` fetches the model from another URL, such as a
  staged asset before its release is published; the pinned SHA-256 and size
  still apply.

### Attaching the model to a release

The model's release is separate from the app's and changes only with the
model. To publish a new model version:

1. Package the model directory:
   `python3 tools/package_labeler_model.py MODEL_DIR --out build/labeler --version N --descriptor internal/archive/labels/assets.json`.
   It writes `build/labeler/<name>-N.tar.gz` with the manifest, the selected
   ONNX file and the tokenizer at its root (`--variant all` includes both ONNX
   variants and the MLX companion). It checks them against the
   manifest, and pins the archive's SHA-256 and size. A single-variant archive
   also sets the managed download default to that variant. With `--variant all`,
   the existing default is preserved (int8 when unset); use `--default-variant`
   to select a different included variant. The archive is
   deterministic: packaging the same directory again gives the same bytes.
2. On the deployment Mac, create the GitHub release `labeler-<name>-N` on
   `Pythia-Software/pharos` and attach that archive under its own name, for
   example `gh release create labeler-<name>-N build/labeler/<name>-N.tar.gz --title "Labeler model <name> N" --notes "…"`.
   Never attach it to an app release (Sparkle's feed reads those), and never
   commit it.
3. Commit the descriptor. Builds from then on download that version; Macs
   that have an older one keep it until they label again with the new
   build, which downloads the new version beside it (rows record the model,
   so a switch relabels).

### Cleanroom model publication

The prepared MLX model releases are independent of the app release and its
signing keys. Export and package on the development/research Mac; transfer
only the finished release kit to the cleanroom. The cleanroom never needs
checkpoints, Python ML packages, conversion tools or research directories.

To prepare a new kit from already converted model directories:

```sh
python3 tools/prepare_labeler_release.py \
  --message-model /path/to/converted-kestrel \
  --event-model /path/to/converted-jayhawk \
  --version 2 --out build/labeler-models-v2 \
  --candidate internal/archive/labels/assets.mlx-v2.json \
  --validation-report /path/to/native-mlx-validation.json
```

The source directories must include both ONNX variants, verified MLX weights
and the tokenizer. The optional validation report carries aggregate parity
results only. The tool verifies every archived file and writes:

- `build/labeler-models-v2/`: both deterministic model archives, per-archive
  checksum files, release notes, candidate `assets.json`, a provenance index,
  an offline verifier/publisher and the supplied validation summary.
- `build/labeler-models-v2.tar` and `.tar.sha256`: the portable handoff. No
  model files or transfer archives belong in git.
- `internal/archive/labels/assets.mlx-v2.json`: reviewed pending app pins.
  **The active `assets.json` remains unchanged until publication succeeds.**

Transfer the tar and its checksum file to the cleanroom, then verify and unpack:

```sh
shasum -a 256 -c labeler-models-v2.tar.sha256
tar -xf labeler-models-v2.tar
python3 labeler-models-v2/publish_labeler_models.py verify labeler-models-v2
```

The transferred publisher uses only Python 3.10+ and the cleanroom's existing
`gh` login. From its clean `origin/master` checkout:

```sh
python3 /path/to/labeler-models-v2/publish_labeler_models.py \
  publish /path/to/labeler-models-v2
```

This creates the two separate `labeler-<name>-2` draft releases, uploads their
archives and checksum files, verifies GitHub's asset digests, and publishes
with `--latest=false`. It leaves app releases and the Sparkle feed alone.
Interrupted uploads reuse matching draft assets; conflicting or already
published assets are never overwritten. No app-signing setup is involved.

After both model releases are public, run this from a development checkout:

```sh
python3 /path/to/labeler-models-v2/publish_labeler_models.py \
  promote /path/to/labeler-models-v2
```

Promotion verifies both published assets before updating only `model` and
`agent_events_model` in the active descriptor, preserving runtime pins. Review,
commit and merge that small pin change. The normal cleanroom app release then
builds from clean `origin/master`; it embeds those pins and fetches the pinned
MLX SDK for its native engine, without requiring the prepared model archives.
End-user Macs fetch the model archives when labeling is enabled.

The version 2 pins declare `default_variant: fp32`. This is a deliberate model
migration: int8 and FP32 can yield different scores and some decisions. The
existing model identity/rebuild rules rescore affected ledgers once. Keeping
FP32 canonical on CPU and GPU avoids silently changing variants on fallback.
The int8 files remain available for explicit `labels_variant = "int8"` and
`agent_events_variant = "int8"` configurations.

The app release itself needs nothing new: `pharos` embeds the descriptor,
and `macos/build-app.sh` builds the worker into
`Contents/MacOS/pharos-labeler-ort` (universal, with cgo) and bundles the
pinned ONNX Runtime beside it as `Contents/MacOS/libonnxruntime.dylib`, its
license and notices in `Contents/Resources/licenses`, re-signed with the
app's identity. The worker alone is signed without library validation
(`com.apple.security.cs.disable-library-validation`), so a hardened,
Developer ID signed worker can still load the download or a configured
`labels_runtime`, which Microsoft or whoever built them signed. The pinned
runtime is arm64 only: an Intel Mac sets `labels_runtime`.

## Worker protocol `pharos-labeler/1`

A worker is any program that loads a model file and runs batches of token
tensors through it. Pharos tokenizes and frames text itself, so a worker never
sees text: it is a thin tensor runner, and one for another platform (Linux or
Windows ONNX Runtime, Core ML, DirectML) only has to speak this protocol.
`internal/labeler/protocol.go` is the reference encoder and decoder;
`cmd/pharos-labeler-ort` is the reference worker.

### Process

- Pharos starts the worker with only the arguments its configuration names
  (the ONNX Runtime worker takes `--runtime PATH`). It writes requests to the
  worker's **stdin** and reads replies from its **stdout**, which carries
  frames only. **Stderr** is free-form diagnostics; Pharos keeps its last
  lines for error messages.
- End of stdin means shut down. So does a `Q` frame, which the worker answers
  with `q` after finishing the requests it already read. `SIGTERM` or `SIGKILL`
  stops it at once.
- A worker serves one model for its lifetime, named in the hello.

### Frames

Every message in either direction is one frame:

| Bytes | Field |
|---|---|
| 4 | `N`, unsigned little-endian: the bytes that follow, `1 ≤ N ≤ 67,108,864` (64 MiB) |
| 1 | frame type, one ASCII letter |
| N − 1 | payload |

| Type | Direction | Payload |
|---|---|---|
| `H` hello | Pharos → worker | JSON `Hello` |
| `h` ready | worker → Pharos | JSON `Ready` |
| `R` run | Pharos → worker | binary run request |
| `r` result | worker → Pharos | binary run result |
| `e` error | worker → Pharos | JSON `ErrorReply` |
| `P` ping / `p` pong | Pharos → worker / back | 8 bytes: a u64 nonce, echoed |
| `Q` quit / `q` goodbye | Pharos → worker / back | empty |

### Handshake

The first frame is `H`:

```json
{"protocol": [1], "model": "/abs/path/model.int8.onnx", "model_sha256": "d95a…",
 "threads": 4, "vocab_size": 50368, "max_batch": 64, "max_seq": 512,
 "inputs": [{"name": "input_ids", "role": "ids"}, {"name": "attention_mask", "role": "attention"}],
 "options": {}}
```

`protocol` lists the versions Pharos speaks. `inputs` declares every input a
run request will carry, by the model's input names, with their roles (from
the model's input contract, below). A hello without `inputs` means
`input_ids`/`ids` and `attention_mask`/`attention`. A worker refuses a
declaration that is malformed (code `protocol`) or does not match the
model's inputs (code `model`), both fatal. Pharos always sends `threads`;
0 would mean the runtime's default, every core. The worker loads the model,
verifies `model_sha256` when given, and replies `h`:

```json
{"protocol": 1,
 "worker": {"name": "pharos-labeler-ort", "version": "1", "runtime": "onnxruntime 1.31.1",
            "device": "cpu", "providers": ["CPUExecutionProvider"]},
 "model": {"path": "…", "sha256": "…",
           "inputs":  [{"name": "input_ids", "dtype": "int64", "shape": ["batch", "seq"]}, …],
           "outputs": [{"name": "logits", "dtype": "float32", "shape": ["batch", 5, 54]}, …]},
 "limits": {"max_batch": 64, "max_seq": 512, "max_frame_bytes": 67108864},
 "capabilities": ["ping"]}
```

`protocol` is the highest version both sides speak. A worker that shares none
replies `e` with code `protocol`, fatal. One that cannot load the model, or
whose checksum fails, replies `e` with code `model`, fatal. Pharos waits up to
60 s for `h`.

### Run requests and results

Integers are little-endian. A tensor is encoded as: u8 name length, name
bytes, u8 dtype (`1` int32, `2` float32), u8 rank (≤ 8), rank × u32 dims, then
the elements row-major.

- **`R` run:** u64 request id, u16 tensor count, the tensors, u16 output-name
  count, the names (u8 length, bytes). The tensors are exactly the declared
  inputs, each int32 `[batch, seq]` with the same dims, right-padded. Their
  values follow their roles:
  - `ids`: token ids in `[0, vocab_size)` (exactly one input);
  - `attention`: 1 on real tokens and 0 on padding, at least one 1 per row
    (exactly one input);
  - `pool`: 1 on the tokens the model pools, at least one per row and only
    where `attention` is 1. Pharos pads `seq` to a multiple of
  8, which the int8 model needs (a multiple of 4 at least); a worker must not
  repad. The worker converts them to the
  model's input type (int64 for `kestrel-68m`). An empty name list asks for
  every output.
- **`r` result:** u64 request id, u16 tensor count, the output tensors,
  float32. `kestrel-68m` returns `logits` `[batch, 5, 54]` and `probs`
  `[batch, 54]`; Pharos asks for `probs` only.

Every `R` gets exactly one reply with its id: `r`, or `e` with that `id`.
Pharos keeps a bounded number of requests in flight (two per worker), so the
worker can start the next batch while Pharos handles the last one. A worker
may answer out of order; Pharos matches replies by id. A worker should answer
`P` promptly even while a run is in progress.

### Errors and malformed input

`e` carries `{"id": N, "code": "…", "message": "…", "fatal": false}` (`id` 0
when the error answers no request).

| Code | When | Fatal |
|---|---|---|
| `bad_request` | a run request that decodes badly or breaks the rules: an undeclared, missing or repeated input, wrong dtype or rank, shapes that differ, a value its role does not allow, a token id outside `[0, vocab_size)`, a mask value other than 0 or 1, a row with no tokens, batch or sequence over the limits, an unknown output name | no |
| `run_failed` | the runtime failed on a well-formed request | no |
| `unsupported` | an unknown frame type (its payload is skipped) | no |
| `bad_frame` | a frame length outside 1…64 MiB, or a stream that ends inside a frame: the stream cannot be resynchronized | yes |
| `protocol` | no common version, or a run before the hello | yes |
| `model` | the model cannot be loaded or fails its checksum | yes |
| `internal` | anything else, such as a recovered panic | worker's choice |

After a non-fatal error the worker keeps serving. After a fatal one it exits
with a non-zero status. `labeler.ValidateRun` implements the `bad_request`
rules, so every Go worker rejects the same requests.

### Compatibility

A new version number is needed for any change an existing worker or Pharos
would misread: a different tensor encoding, a new input role, a changed
meaning of a field. A model with more inputs of the existing roles (a
pooling mask) needs no new version: they are declared in the hello. Additive JSON fields, new optional capabilities and new
frame types that a peer may ignore with `unsupported` keep version 1.

## Adding a worker for another platform

1. Write a program that speaks the protocol above. In Go, reuse
   `internal/labeler` (`ReadFrame`, `DecodeRun`, `ValidateRun`,
   `EncodeResult`) and copy the loop in `cmd/pharos-labeler-ort/worker.go`,
   which is written against a small session interface. In another language,
   follow the frame tables; nothing in them is Go-specific.
2. Keep the model's inputs and outputs as they are: the same ONNX file for
   another ONNX Runtime execution provider (DirectML, CUDA, Core ML), or a
   converted model whose `probs` agree with the reference.
3. Prove parity: run the worker-backed fixture test with
   `PHAROS_LABELER_WORKER` pointing at the new worker. Probabilities must be
   within 1e-4 of the fixture's for the same variant and closed sets equal,
   except for a probability within 1e-4 of its threshold. Then compare labels
   on real items against the reference (as in the E2E below).
4. Point `labels_worker` (and any runtime path option) at it. Nothing in
   `pharos` changes.

Notes for authors:
- Feed the model exactly the inputs the hello declares, by name, converting
  int32 to the model's element type; check the declaration against the
  model's inputs once, at the hello.
- The ONNX Runtime worker loads its library with `dlopen`/`dlsym` from
  `OrtGetApiBase`; a Windows build would use `LoadLibrary`/`GetProcAddress`
  in a file beside `ort_dlopen.go`.
- Request the C API version of the header you build with, and fail with
  code `model` when the library is older.
- Write frames to stdout only. Anything that may print (a runtime's logging)
  must go to stderr.
- Pharos never sends a batch over the limits you announce in `Ready`; lower
  them there if your device needs smaller batches.

## Adding a model

The runtime is not tied to `message_labels`: a second model, with its own
ledger and pass, uses the same pieces.

**1. The model directory.** Each model has its own directory (in the app,
`Contents/Resources/labeler/<model>/`) with a `manifest.json` that
`labeler.LoadFramedModel` verifies against the caller's `labeler.Framing`
(`LoadModel` is the message labeler's, `KestrelFraming`):
- the variant's ONNX file and `tokenizer.json`, with their SHA-256s;
- `framing_contract` and `input_contract`, which this build must implement;
- `inputs`: names for the contract's roles;
- `onnx.outputs`: the output names, as a map or a list;
- `max_seq`: the longest sequence, at least the framing's maximum (512 for
  `kestrel-framing-v1`) and at most 8,192;
- `pad_multiple` (or `padding.multiple`): a padding multiple beyond the
  contract's 8;
- `memory_mb`: one worker's memory estimate (default 1,600);
- labels, thresholds and so on, for a model that labels (`Framing.Labels`;
  the agent event verifier does not).

The hello carries `max_seq` and the inputs. The worker must announce at
least that `max_seq`, and batches never exceed a lower `max_batch` it
announces.

**2. Framing and roles.** A framing function returns `labeler.Framed`: the
token ids, kestrel's tag/context/text spans (`Spans`), and any further named
spans it builds (`Extra`, for example sentence spans). Each input role is
registered (`labeler.RegisterRole`) with:
- a **filler**, which writes one row of the role's tensor from a `Framed`;
- a **validator**, composed from `AllOf`, `ValuesIn`, `ZeroWithoutAttention`,
  `NonEmptyRows` and `ContiguousRuns`, or written from scratch.

`BatchInputs` and `ValidateRun` dispatch through the registry, so a new role
is one registration in the main process and in the worker, with no change
to the protocol, the batcher or the workers' loops. The `segment` role
(`ids-attention-pool-segments-v1`, the agent event verifier's) is built in:
it numbers a framing's sentences 1…K from `Framed.Extra[SegmentSpans]`,
values in `[0, 16]`, zero on padding, one contiguous run each. The tensors stay int32 `[batch, seq]`. Register the role
and the input contract (`labeler.InputContracts`) in the PR that adds the
model.

**3. Running it.** `labeler.Runner` runs framed rows through one model's
workers:
- `RunBatch(ctx, rows, outputs)` sends one batch;
- `Run(ctx, rows, outputs, tokenBudget)` packs any number of rows by length,
  keeps the runner's capacity in flight, and returns each row's outputs by
  name, in row order.

Outputs must be ones the manifest declares. A second ledger builds its rows,
runs them, and stores what it needs. The labels ledger is one such client
(`RunBatch` inside its streaming pipeline). Reuse its patterns too:
- a dirty table fed by triggers with `WHEN` value guards;
- an input hash with model and contract identity per row;
- an operation kind with holds `D:w W:w`, registered in the kinds table and
  the contract;
- stopping between batches and killing the runner on a stop.

**4. Several models.** `labeler.Manager` (one per catalog, `labelerManager`)
keeps one runner per model, and per worker configuration, under one budget:
- **Threads and memory:** every runner's workers × threads and workers ×
  `memory_mb` count against `labels_thread_budget` and
  `labels_memory_budget_mb` together, not per model.
- **Making room:** a runner that does not fit first stops idle runners of
  other models (least recently used), then runs with fewer workers, down to
  one. If it still does not fit while others are in use, it fails with
  `ErrBudget`; its pass stays pending and retries.
- **Idle runners:** they stay warm for `labels_idle_seconds`. Closing the
  catalog stops them all.
- **Sharing in practice:** two 68M int8 models (about 1.5 GB per worker
  under load) share an 8 GB budget with room for two workers each, or one
  each while a third model runs.

One worker process per model keeps workers simple, with one model, one
session and one set of limits each. Workers are cheap to start (0.5 s), and
their memory is what the budget counts.

**5. Parity.** Ship a parity fixture of invented strings with the
directory's `parity-fixture.json`: token ids, spans (when the contract
pools), logits, probabilities and labels per case.
`TestModelParityWithWorker` checks any model directory through the real
worker.

**Padding:** int8 graphs may depend on the padded length, as
`kestrel-68m`'s does unless it is a multiple of 4. Every contract pads to a
multiple of 8, and a model can raise that with `pad_multiple`. Check a new
model's outputs against padding (+0, +8, +64 positions) when exporting, for
every output, as LAPWING did.

## Testing

| What | Command |
|---|---|
| Protocol, model loading, batching, worker client and pool (a fake worker in the test binary: crashes, restarts, malformed requests, stop) | `go test ./internal/labeler/...` |
| Tokenizer and framing against the 187 fixture cases | `go test -run 'Tokenizer\|Frame' ./internal/labeler` |
| Item equivalence with prepare; the segment rule; the ledger (backfill, incremental runs, healing, limit, one-at-a-time, worker crash, stop and resume, the chain with the setting on and off, what the reader receives) | `go test -vet=off -run 'TestLabels\|TestLabelingSegment' ./internal/archive` |
| The ONNX Runtime worker's protocol loop (a fake session) | `go test ./cmd/pharos-labeler-ort` |
| Downloads (a local HTTP server: verify, unpack, resume, refuse a mismatch or an escaping archive, one per Mac, waiting, the broker's operation) and power (budget, backlog waits on battery) | `go test -vet=off -run 'TestLabelsAsset\|TestLabelsFetch\|TestLabelsWait\|TestBrokerFetches\|TestLabelsForPower\|TestLabelsBacklog' ./internal/archive` |
| The reader's label view, highlight mask and marks | `npm --prefix web test -- reader/markdown` |
| Packaging the release asset | `python3 -m unittest discover -s tools -p 'test_package_labeler_model.py'` |
| Fixture parity with the real model (skipped without the variables) | `PHAROS_LABELER_MODEL_DIR=… PHAROS_LABELER_WORKER=build/pharos-labeler-ort PHAROS_ORT_LIBRARY=build/onnxruntime-1.31.1/lib/libonnxruntime.dylib go test -run 'ModelParity\|ORT' ./internal/labeler ./cmd/pharos-labeler-ort` (any model directory: it uses the directory's own `parity-fixture.json`, so it checks a pooling model's spans and inputs too) |

## Known limits

- **A source conversation's change rebuilds every conversation that re-sends
  its messages,** whether or not the change touched the re-sent message.
  That costs item building, not inference: messages whose inputs are
  unchanged keep their labels.
- **Unicode versions.** The tokenizer library's regex engine (Unicode 16)
  and NFC data are older than Go's and `x/text`'s. The tokenizer pins both to
  the library's, with generated tables (`onigClassFix`, `nfcUnknown`).
  - These are checked for the Unicode 15 and 17 tables that Go 1.26/1.27 and
    `x/text` v0.42 carry.
  - A test fails on any other version, so upgrading either means rerunning
    the probes (`studies/MERLIN/tokcheck`).
- **Segments the model does poorly on:** agent text in the user role (`AIUR`,
  J 0.69) and delegations (`DEL`, 0.67) are the weakest segments; the model
  never predicts `P.frame.criteria`. See the research for per-segment scores.
- **Long messages** use the unvalidated `chunk-union-v1`; a message's
  `segment` column is its first chunk's.
- **Old downloads stay.** A new model version downloads beside the old one,
  which nothing removes yet (about 160 MB unpacked).
- **Warm workers last `labels_idle_seconds`.** A run after that pays the
  worker's start-up again (about 0.5 s: process, 152 MB model, its SHA-256).
- **Only the conversation reader shows labels**: no query table or MCP tool
  reads them yet, and the reader leaves thinking and delegations plain. Hues
  are told apart by the legend and hover as well as colour, but past three
  families on screen some pairs are hard to separate for colour-blind
  readers. Labels are not in the freshness
  ledger (`an.labels` has no row in `Catalog.freshness`); `pharos labels
  status` reports it.
