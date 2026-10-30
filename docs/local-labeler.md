# Local labels

Pharos can label every message's function on this Mac: what a person asked
for, how they steered, what they accepted or challenged, what an agent
reported, planned or got stuck on. The labels come from `kestrel-68m`, a
68M-parameter encoder fine-tuned to reproduce the labels large models assign
in [library labeling](library-labeling.md) (taxonomy `candidate-0.3`; the
research is in [labeler-research/](labeler-research/README.md), the decision
in [shipping-encoder.md](labeler-research/shipping-encoder.md)).

**Status: prototype, off by default.** Labels are stored in the catalog and
shown by `pharos labels status` and `pharos labels explain`. No page, query
table or MCP tool reads them yet.

## Turning it on

The model and the worker are not in the repository. For the prototype they
are located by configuration (`archive.toml` or `library.toml`) or by the
environment:

| Key | Environment | Meaning |
|---|---|---|
| `labels_enabled` | — | `true` runs labeling after each manual index (default `false`) |
| `labels_model_dir` | `PHAROS_LABELS_MODEL_DIR` | the model directory (below) |
| `labels_variant` | — | `int8` (default) or `fp32` |
| `labels_worker` | `PHAROS_LABELS_WORKER` | the worker executable; default `pharos-labeler-ort` beside `pharos` |
| `labels_runtime` | `PHAROS_ORT_LIBRARY` | the ONNX Runtime library the worker loads |
| `labels_threads` | — | intra-op threads per worker (default 4) |
| `labels_workers` | — | worker processes (default 1) |
| `labels_batch_tokens`, `labels_max_batch` | — | a batch's padded-token budget (default 16,384) and row limit (64) |
| `labels_thread_budget`, `labels_memory_budget_mb` | — | what every model's workers may use together: intra-op threads (default the larger of workers × threads and half the cores) and estimated memory (default 8,192 MB) |
| `labels_idle_seconds` | — | how long finished workers stay warm for the next run (default 120) |

Relative paths resolve against the configuration file's folder. To build the
worker and fetch ONNX Runtime:

```sh
macos/fetch-onnxruntime.sh            # pinned release, SHA-256 checked, into build/onnxruntime-1.31.1/
CGO_ENABLED=1 go build -o build/pharos-labeler-ort ./cmd/pharos-labeler-ort
```

## Commands

```sh
pharos [--config PATH] labels run [--limit N] [--batch-tokens N] [--max-batch N] [--workers N] [--threads N] [--full] [--one-at-a-time] [--dry-run] [--json]
pharos [--config PATH] labels status [--json]
pharos [--config PATH] labels explain MESSAGE_ID [--infer]
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
- **status** reads only: labeled messages by segment, conversations built
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
  - forks of any of these, which copy their parent's history.
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
rejected, stays pending.

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
  background `analysis.labels` run.
- Background updates (Frequently, Hourly, Daily) run the chain's stages on
  their own cadences; labels follow the writing classification
  (`authorship`), so an update that classifies new messages labels them too.
- No page read starts labeling. Pages read whatever ledger exists (none yet).

| Event | What happens |
|---|---|
| Stop, eject or release | The run stops between batches and kills its workers rather than waiting for a batch in flight. Finished conversations are stored, and the next run resumes from the dirty marks and label counts. |
| A worker crashes, or does not answer a batch within 5 min (it is killed) | Its batches in flight fail, the worker restarts after a backoff (0.5 s doubling, at most 30 s) and lost batches are sent again, at most twice. |
| A batch keeps crashing workers, or the worker rejects it (`bad_request`, `run_failed`) | The batch is split and sent one item at a time, so one bad item fails only its message, which stays pending for the next run. |
| Workers keep crashing (8 restarts in a row with no batch succeeding) | The run ends **interrupted**, keeping what it stored. |
| A conversation cannot be built (a damaged row; a panic is caught) | It keeps its dirty mark for the next run; the others go on. A stored classification that no longer fits its message counts as none until authorship catches up. |
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
```

The first labels run creates these tables and triggers (`labels_schema_version`
1 in `meta`). There is no catalog schema version bump: the tables are
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
conversations; the fixture's strings are invented. **For a release**:
- publish the model directory as a release asset (or keep it on the
  deployment Mac), and pin its manifest SHA-256 in the repository, as
  `macos/sparkle.json` pins Sparkle;
- `build-app.sh` fetches and verifies it into
  `Contents/Resources/labeler/kestrel-68m-tpool/`, with the worker in
  `Contents/MacOS/pharos-labeler-ort` and ONNX Runtime's library and license
  in `Contents/Frameworks/` (fetched by `macos/fetch-onnxruntime.sh`, which
  pins version 1.31.1, the version the Python reference ran with, by URL and
  SHA-256);
- the settings above then default to those paths (the worker beside
  `pharos`, the model under `Resources/labeler`), and Settings shows the
  labeler's state.

The model is never stored on the library drive.

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
`labeler.LoadModel` verifies:
- the variant's ONNX file and `tokenizer.json`, with their SHA-256s;
- `framing_contract` and `input_contract`, which this build must implement;
- `inputs`: names for the contract's roles;
- `onnx.outputs`: the output names, as a map or a list;
- `max_seq`: the longest sequence, at least the framing's maximum (512 for
  `kestrel-framing-v1`) and at most 8,192;
- `pad_multiple` (or `padding.multiple`): a padding multiple beyond the
  contract's 8;
- `memory_mb`: one worker's memory estimate (default 1,600);
- labels, thresholds and so on, for a model that labels.

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
(a `segment` role numbering sentences 1…K, values in `[0, 16]`, zero on
padding, one contiguous run each) is one registration in the main process
and in the worker, with no change to the protocol, the batcher or the
workers' loops. The tensors stay int32 `[batch, seq]`. Register the role
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
| Item equivalence with prepare; the segment rule; the ledger (backfill, incremental runs, healing, limit, one-at-a-time, worker crash, stop and resume, the chain with the setting on and off) | `go test -vet=off -run 'TestLabels\|TestLabelingSegment' ./internal/archive` |
| The ONNX Runtime worker's protocol loop (a fake session) | `go test ./cmd/pharos-labeler-ort` |
| Fixture parity with the real model (skipped without the variables) | `PHAROS_LABELER_MODEL_DIR=… PHAROS_LABELER_WORKER=build/pharos-labeler-ort PHAROS_ORT_LIBRARY=build/onnxruntime-1.31.1/lib/libonnxruntime.dylib go test -run 'ModelParity\|ORT' ./internal/labeler ./cmd/pharos-labeler-ort` (any model directory: it uses the directory's own `parity-fixture.json`, so it checks a pooling model's spans and inputs too) |

## Known limits

- **Re-sent text is resolved against its source's current classification,
  but only conversations whose own inputs changed are rebuilt.** A change to
  a source message's classification that leaves the re-sending message's
  spans alone reaches the re-sending conversation's labels only through
  `--full`, a new model or contract, or its next change.
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
- **Warm workers last `labels_idle_seconds`.** A run after that pays the
  worker's start-up again (about 0.5 s: process, 152 MB model, its SHA-256).
- **Not yet on any page, query table or MCP tool**, and not in the freshness
  ledger (`an.labels` has no row in `Catalog.freshness`); `pharos labels
  status` reports it.
