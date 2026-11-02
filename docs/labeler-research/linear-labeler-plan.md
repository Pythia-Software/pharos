# Plan: the Go linear labeler in Pharos

**Status: superseded (2026-10-30)** by the encoder decision in [shipping-encoder.md](shipping-encoder.md). It is kept for the record. Its pipeline phases (shared item builder, `message_labels` ledger, `analysis.labels` operation, flag, retraining loop) still describe the integration; only the model and runtime change.

Originally proposed 2026-10-29. This follows the decision in the
[README](README.md): label every message locally with DOTTEREL's pure-Go
linear model, and accept about 0.06 Jaccard lower quality on typed human text
in exchange for no native runtime, a small payload and near-instant labeling.

## Goal

Every message Pharos indexes carries message-function labels from taxonomy
`candidate-0.3`, computed locally:

- human typed text;
- human text with quoted or pasted context;
- templates, re-sent text and feedback comments;
- agent text in the user role;
- agent replies;
- delegations;
- thinking.

The labels must be:

- **deterministic:** the same inputs and model give the same labels;
- **versioned:** every label records the model that made it;
- **rebuilt in the background**, never on a request path.

They are shown in the transcript reader and Analyze, and exposed to query
tables and MCP tools.

Targets:
- **CPU:** a library of about 700k items labeled in about a minute on four
  cores of an M2 Air.
- **Payload:** 33–150 MB.
- **Dependencies:** none beyond the Go standard library.

## Where it fits

Labels are a **derived ledger**, like `message_authorship`
([human authorship](../human-authorship.md)). They depend on authorship,
because a human message's model-visible text is its typed pieces, which come
from authorship spans. So the labels ledger rebuilds after authorship, in the
same analysis chain, and pages show the last ledger until then
(the requests-never-write rule).

**Inputs.** The training inputs came from `pharos labeling prepare`.
Inference inputs must be byte-identical, so the app builds its items with the
same Go functions that `prepare` uses:

- `humanPieces`, `renderPieces`, harness and secret handling
  (`labeling_pieces.go`);
- `chunkLabelingText`;
- `labelingContext`;
- cohort and segment rules.

**One code path for training and inference is the parity guarantee that
matters most.** It is stronger than any fixture.

## Phase 0: research follow-ups before shipping

1. **Investigate the delegation anomaly.**
   - The problem: every model scores 0.12–0.46 on GANNET delegations,
     against 0.58–0.63 in-distribution.
   - The suspect: delegation storage or preparation differs between the
     schema-14 training snapshot and schema 21.
   - The check: compare `CURRENT_TEXT` for delegation units prepared from
     both snapshots.
   - Gate: hide delegation labels until this is fixed.
2. **Prepare the training dataset from a current-schema backup copy**, with
   `--splits-from` the old dataset.
   - That way training inputs are what this build computes.
   - `import-results` carries over labels for unchanged items.
   - Label new items with Flash, plus Argon on held-out samples. Add Haiku
     when Claude quota allows; the DOTTEREL human target is 2-of-3.
   - Include EGRET's thinking labels (14k train items) and AVOCET's
     three-teacher human labels.
3. **Train and select the production model.**
   - Use the DOTTEREL `V4-hybmaj` recipe at 150k columns.
   - Store weights in float16 (`V5-hybmaj-150k`): 33 MB, and the same scores
     as `V4-hybmaj`.
   - Tune per-segment thresholds on dev, including a TH threshold set.
   - Score once on a GANNET-style untouched holdout, and record the numbers
     in the model manifest.
4. **Fix the labeling runner,** a separate PR, needed for regular retraining:
   - index `attempts(job)`, and move spend accounting out of the write lock;
   - halt on agy quota errors;
   - re-request items Flash drops from multi-item requests.

## Phase 1: the labeler package (no behavior change)

Files: `internal/archive/labeler_*.go`. The repository is one package.

- **Port DOTTEREL's `go/labeler`** (about 2,500 lines plus generated tables):
  - text normalization on the generated Unicode 15.0 tables
    (`labeler_unitab_gen.go`, never package `unicode`);
  - the ASCII shadow string and the 34 rules behind a literal prefilter;
  - murmur3 hashing;
  - the hashed feature blocks, dense blocks and copy groups;
  - the float32 `log1p` table and the guard against fused multiply-add;
  - sparse scoring, sigmoid, per-segment thresholds and upward closure.
- **Generators:** `go generate` regenerates the tables from scripts in
  `tools/labeler/`.
- **Model format `pharos-linear-labeler-v1`:** a directory or a single file
  containing:
  - `model.json`: labels, parents, blocks, groups, segment order, thresholds
    per segment, taxonomy SHA-256, input contract versions (units, pieces,
    context, chunking, segments), training dataset and export IDs, and
    holdout metrics;
  - weight tables in float16.

  `LoadLabeler` refuses a model whose taxonomy or contract versions differ
  from the build's.
- **Parity fixture:** `testdata/labeler-parity.json`, built from invented
  strings only, plus a tiny synthetic model.
  - It records feature indices and values, logits, and closed label sets.
  - It covers every Unicode case in PARITY.md §4: final sigma, İ/ı,
    combining marks, ZWJ, flags, CJK, U+2028, `⟦…⟧` markers.
  - Go tests and a Python test both check it (as `testdata/labeling-parity.json`
    does for the smoke model).
  - **Never** use DOTTEREL's `go/testdata/fixtures.jsonl`: it comes from
    corpus text.
- **Benchmarks:** `go test -bench` for featurize plus score at 1 and 4
  goroutines, and RSS after load. The budget is at least 4k items/s per
  goroutine and under 250 MB RSS.

## Phase 2: one item builder for `prepare` and the app

- **Extract per-message item construction** from `labeling_prepare.go` into a
  function that works on a catalog read: cohort and segment, pieces, visible
  text with markers, context-v3 (`labelingContext` over the previous request
  and reply in the same agent session), chunking-v1, and the metadata fields
  in `inputs.jsonl`.
  - `prepare` keeps calling it over its snapshot.
  - The ledger calls it over the live catalog's read connection, inside a
    rebuild, just as authorship reads its inputs.
- **Equivalence test:** on a seeded fixture catalog, the app's items equal
  `prepare`'s items byte for byte, every field. This test guards against
  silent train/inference skew.
- **Thinking:** units come from the normalized reasoning store, as in
  `prepare`. Redacted or encrypted reasoning is skipped, exactly as
  `prepare` skips it.

## Phase 3: the ledger

**Schema migration 22.**

```sql
CREATE TABLE message_labels (
  message_id   INTEGER PRIMARY KEY,
  segment      TEXT NOT NULL,      -- H-typed | H-typed+ctx | H-template | H-resent | H-feedback | AIUR | AR | DEL | TH
  labels       INTEGER NOT NULL,   -- 54-bit mask over candidate-0.3, closed upward; 0 = no label
  margin       REAL NOT NULL,      -- confidence: min |logit - threshold logit| over labels (for display and audits)
  chunks       INTEGER NOT NULL,   -- items merged (chunk-union-v1 for long messages)
  input_hash   TEXT NOT NULL       -- SHA-256 of the item inputs, so unchanged messages are skipped
);
CREATE INDEX message_labels_segment ON message_labels(segment);
```

Taxonomy nodes and their names come from the embedded taxonomy file, so no
label table is needed.

**Generation:**
- `labels_generation` = authorship generation + model SHA-256 + input
  contract version.
- A new model, a contract change, or new authorship invalidates it.
- The first version does a full rebuild, like authorship. At 4k–8k items/s on
  one goroutine (17–32k on four), about 700k items take 30–90 s plus I/O.
- Use `input_hash` to make rebuilds incremental once the full version is
  proven.

**Operation kind `analysis.labels`** ("Label messages"):
- Holds `D:w W:w`, affects `an.labels`, chained right after
  `analysis.authorship` in `analysis.chain`.
- Never started by a page read: pages read whatever ledger exists and say
  when it is stale.
- Cancellable at chunk boundaries; writes in batched transactions; registered
  with the operation broker, so eject and stop rules apply.

**Long messages:** the per-chunk label union (`chunk-union-v1`), flagged
unvalidated in the docs, as in library labeling.

**Model file:** a resource in the app bundle
(`Contents/Resources/labeler/<model>.plm`), never on the library drive.
- Loaded once per process and verified against the SHA-256 compiled into the
  build.
- If it is missing or wrong, labeling is disabled, and status and Settings
  say why. The CLI finds it next to the executable.

## Phase 4: where labels appear

| Surface | v1 |
|---|---|
| **Transcript reader** | Function chips per message (taxonomy names; a tooltip with the definition and "estimated by the local labeler"). For human messages, a note that labels come from the typed text only. |
| **Analyze → Your writing** | `writing_messages` gains a **Functions** column, filterable with *includes*. The conversation table counts requests, steering (redirect, cancel, pause), challenges and approvals per conversation. |
| **Query tables** | A `message_functions` dataset: one row per labeled message, with segment, labels, work, repository, provider and day. Metrics such as challenge rate by repository over time. Register metrics with the V2 metric sources. |
| **MCP** | A `labels` filter on `search_messages` and `search_conversation_passages`, and label dimensions in `query_metrics`. Regenerate the site map (`npm --prefix web run generate-site-map`). |
| Findings, cards | Later, once v1 is in use. |

**Display rules from the research:**
- **Automation prompts (AIUR):** this segment scores 0.59. Show its labels
  marked "low confidence", or hide them by default.
- **Never predicted:** the model never predicts `P.frame.criteria`. Hide that
  label, and list the weak labels in the docs: criteria, revise, verify,
  constraint, `C.input`.
- **Delegations:** hidden until the Phase 0 anomaly is resolved.

## Phase 5: retraining

The linear model degrades more on new conversations than the encoder: −0.04
on typed human text in GANNET, against −0.01 to −0.02 for the encoder.
Retraining is cheap, so refresh regularly.

**Code:** `tools/labeler/` holds the trainer (ported from DOTTEREL's
`dexp.py`, `dfeat.py`, `export.py` and `refscore.py`), the fixture generator,
the Unicode table generator and the evaluation harness (from `rlh2`). It
needs numpy and scipy, so its tests skip without them. The stdlib-only
`tools/labeling_smoke.py` stays as is.

**Cadence:** about every two to four weeks, or when the share of automation
prompts with templates never seen in training rises:
1. Prepare from the latest backup copy with `--splits-from`.
2. `import-results` for unchanged items.
3. Label new items with Flash, plus Argon on a held-out sample, about $30 per
   refresh.
4. Train: minutes on CPU.
5. Evaluate per segment.

**Release gates:** compared with the shipped model, on the same held-out
sets:
- typed human text scores at least the shipped model's score minus 0.01 on
  the fair comparison;
- no segment drops by more than its 95% CI.

**Publish:** a new model file, SHA-256 and version in the next release. The
ledger relabels on the next analysis run.

## Phase 6: rollout

A setting or flag (`labels.enabled`, off by default at first, as the async
operations work was) gates:
- the ledger;
- the analysis chain step;
- the surfaces.

Run on the dev library first, then on euclid, then turn it on by default.

## Proposed PRs

| # | PR | Contents |
|---|---|---|
| 1 | Labeling runner fixes | Index, quota halt, dropped items. Phase 0.4. |
| 2 | Labeler package | Phase 1: port, model loader, parity fixture, benchmarks. No behavior change. |
| 3 | Shared item builder | Phase 2: refactor `prepare` and add the equivalence test. |
| 4 | Ledger | Phase 3: migration, `analysis.labels`, the flag, status, model resource in `build-app.sh`. |
| 5 | Reader and Analyze | Phase 4, first two rows. |
| 6 | Query tables and MCP | Phase 4, remaining rows; site map. |
| 7 | Retraining tools and docs | Phase 5: `tools/labeler/`, `docs/local-labeler.md` (user-facing). |

## Decisions needed

1. **Where model weights live.** The weights are derived from private
   conversations (hashed features, but derived all the same).
   - Recommended: keep them out of git, the same way Sparkle is fetched
     (`macos/fetch-sparkle.sh`). Publish them as a release asset or on the
     deployment Mac, with the SHA-256 pinned in the repo. `build-app.sh`
     fetches and verifies them.
   - The alternatives are git LFS, or committing the 33 MB float16 file.
2. **Model size.** The 150k-feature float16 model (33 MB, same scores) is
   recommended over the 500k-feature model (107–161 MB).
3. **Automation prompts (AIUR)**: show with a low-confidence mark, or hide.
4. **v1 surfaces:** reader plus Analyze plus MCP (recommended), or more.
5. **Optional Haiku fallback.** Round 1 showed that sending novel
   automation-prompt templates and low-confidence items to Haiku (about $35
   per 400k) lifts quality above Haiku alone. It conflicts with "local
   only" and Claude quota, so it is left out of this plan.
