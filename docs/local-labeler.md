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
  with no worker and no label writes. Interrupt (Ctrl-C) stops between
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
  header and ONNX Runtime's MIT license are in
  `cmd/pharos-labeler-ort/include/`. Workers for other platforms or runtimes
  are drop-in (below).
- **One new module dependency:** `golang.org/x/text`, for the tokenizer's NFC
  normalizer (`unicode/norm`). The standard library has none, and NFC must
  match the tokenizer's exactly; the Go project maintains this package.

## The parity contract

The labels are only as good as the inputs' match with training. Three layers
guard it.

1. **One item builder.** The app builds its items with `labeling prepare`'s
   own per-conversation pass: cohort, human pieces and markers
   (`human-pieces-v2`), context (`context-v3`, the 600-character prompt the
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
   prepare's, field for field, on the seeded fixture catalog.
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
   label that is on adds its ancestors. A worker-backed test checks the
   fixture's probabilities (within 1e-4), logits (1e-3) and closed sets with
   the real model when `PHAROS_LABELER_MODEL_DIR`, `PHAROS_LABELER_WORKER` and
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
model identity is the model's name, its file's SHA-256 and its manifest's
(which carries the thresholds and the tokenizer's hash). The contract is
`kestrel-framing-v1+labeling-units-v1+human-pieces-v2+context-v3+chunking-v1+avocet-segments-v1+chunk-union-v1`.
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
- No page read starts labeling. Pages read whatever ledger exists (none yet).

| Event | What happens |
|---|---|
| Stop, eject or release | The run stops between batches and kills its workers rather than waiting for a batch in flight. Finished conversations are stored, and the next run resumes from the dirty marks and label counts. |
| A worker crashes | Its batches in flight fail, the worker restarts after a backoff (0.5 s doubling, at most 30 s) and lost batches are sent again, at most twice. A batch the worker rejects (`bad_request`, `run_failed`) is split and sent one item at a time, so one bad item fails only its message. After 5 restarts in a run the run ends **interrupted**, keeping what it stored. |

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
  input_hash TEXT NOT NULL, model TEXT NOT NULL, contract TEXT NOT NULL,
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
| `manifest.json` | name, base model, framing contract, files and their SHA-256s, label order, parents and ancestors, heads and scorer, segments, per-segment thresholds (`default` for the rest), training data and licenses, metrics |
| `model.int8.onnx`, `model.fp32.onnx` | inputs `input_ids`, `attention_mask` (int64 `[batch, seq]`); outputs `logits` `[batch, 5, 54]`, `probs` `[batch, 54]` |
| `tokenizer.json` | the ettin-encoder-68m tokenizer, unchanged |
| `FRAMING.md`, `reference.py`, `parity-fixture.json` | the framing contract, the Python oracle, the invented-string fixture |

Loading a model verifies the chosen variant's file and the tokenizer against
the manifest's SHA-256s, and refuses a manifest for another framing contract.
A missing or failing model disables labeling with the reason in `labels
status`.

**Never commit weights.** The weights derive from this library's private
conversations; the fixture's strings are invented. **For a release**:
- publish the model directory as a release asset (or keep it on the
  deployment Mac), and pin its manifest SHA-256 in the repository, as
  `macos/sparkle.json` pins Sparkle;
- `build-app.sh` fetches and verifies it into
  `Contents/Resources/labeler/kestrel-68m/`, with the worker in
  `Contents/MacOS/pharos-labeler-ort` and ONNX Runtime's library and license
  in `Contents/Frameworks/` (fetched by `macos/fetch-onnxruntime.sh`, which
  pins version 1.31.1, the version the Python reference ran with, by URL and
  SHA-256);
- the settings above then default to those paths, and Settings shows the
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
 "threads": 4, "vocab_size": 50368, "max_batch": 64, "max_seq": 512, "options": {}}
```

`protocol` lists the versions Pharos speaks. The worker loads the model,
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
  count, the names (u8 length, bytes). Version 1 has exactly two inputs,
  `input_ids` and `attention_mask`, both int32 `[batch, seq]`, right-padded,
  mask 1 on real tokens and 0 on padding. Pharos pads `seq` to a multiple of
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
| `bad_request` | a run request that decodes badly or breaks the rules: unknown or missing inputs, wrong dtype or rank, shapes that differ, a token id outside `[0, vocab_size)`, a mask value other than 0 or 1, a row with no tokens, batch or sequence over the limits, an unknown output name | no |
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
would misread: a new required input, a different tensor encoding, a changed
meaning of a field. Additive JSON fields, new optional capabilities and new
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
- The ONNX Runtime worker loads its library with `dlopen`/`dlsym` from
  `OrtGetApiBase`; a Windows build would use `LoadLibrary`/`GetProcAddress`
  in a file beside `ort_dlopen.go`.
- Request the C API version of the header you build with, and fail with
  code `model` when the library is older.
- Write frames to stdout only. Anything that may print (a runtime's logging)
  must go to stderr.
- Pharos never sends a batch over the limits you announce in `Ready`; lower
  them there if your device needs smaller batches.

## Testing

| What | Command |
|---|---|
| Protocol, model loading, batching, worker client and pool (a fake worker in the test binary: crashes, restarts, malformed requests, stop) | `go test ./internal/labeler/...` |
| Tokenizer and framing against the 187 fixture cases | `go test -run 'Tokenizer\|Frame' ./internal/labeler` |
| Item equivalence with prepare; the segment rule; the ledger (backfill, incremental runs, healing, limit, one-at-a-time, worker crash, stop and resume, the chain with the setting on and off) | `go test -vet=off -run 'TestLabels\|TestLabelingSegment' ./internal/archive` |
| The ONNX Runtime worker's protocol loop (a fake session) | `go test ./cmd/pharos-labeler-ort` |
| Fixture parity with the real model (skipped without the variables) | `PHAROS_LABELER_MODEL_DIR=… PHAROS_LABELER_WORKER=build/pharos-labeler-ort PHAROS_ORT_LIBRARY=build/onnxruntime-1.31.1/lib/libonnxruntime.dylib go test -run 'Kestrel\|ORT' ./internal/labeler ./cmd/pharos-labeler-ort` |

## Known limits

- **Codex continuations differ from training.** Prepare treats a
  continuation (a Codex filename fork, whose lineage is the root's own
  execution) as the person's, in memory. The stored authorship ledger, which
  the app reads, counts it as a sub-agent, so its messages are labeled as
  agent text in the user role (`AIUR`) rather than as human text. This also
  shifts template counts, and so other messages' segments. The equivalence
  test leaves those forks out. Aligning the authorship ledger with prepare
  would remove it.
- **Re-sent text is resolved against its source's current classification,
  but only conversations whose own inputs changed are rebuilt.** A change to
  a source message's classification that leaves the re-sending message's
  spans alone reaches the re-sending conversation's labels only through
  `--full`, a new model or contract, or its next change.
- **Unicode versions.** Go's tables (Unicode 17) and the tokenizer library's
  regex engine may classify characters assigned after Unicode 15 differently
  in pre-tokenization.
- **Segments the model does poorly on:** agent text in the user role (`AIUR`,
  J 0.69) and delegations (`DEL`, 0.67) are the weakest segments; the model
  never predicts `P.frame.criteria`. See the research for per-segment scores.
- **Long messages** use the unvalidated `chunk-union-v1`; a message's
  `segment` column is its first chunk's.
- **Workers start per run.** A run that labels anything pays the worker's
  start-up (loading the model, about a second) once; an idle pool kept
  between runs would remove it for small runs.
- **Not yet on any page, query table or MCP tool**, and not in the freshness
  ledger (`an.labels` has no row in `Catalog.freshness`); `pharos labels
  status` reports it.
