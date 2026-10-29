# Findings

All scores are mean per-item Jaccard on hierarchy-closed label sets (empty
versus empty counts as 1), on held-out conversation families. "Fair" or "LOO"
means leave-one-out:

- each LLM is scored against the intersection of the other two LLMs' labels;
- a student is scored against the same intersections;
- a student "matches Haiku" when its score against Flash∩Argon equals Haiku's.

See [methodology.md](methodology.md). Every figure here comes from the shared
harnesses and appears in the [reports](reports/).

## The data

**Corpus.** 364,908 items, prepared from the Oct 27 backup (schema 14). By
cohort:

| Cohort | Items |
|---|---|
| Agent replies | about 299k |
| Agent text in the user role (scripts, headless runs, sub-agent prompts) | 29k |
| Delegations | 22k |
| Human pieces | 17k |

**Teachers.**

| Teacher | Items labeled | Spend |
|---|---|---|
| Gemini 3.8 Flash | all | $553 |
| Haiku 5.5 | 180k | $159 |
| Gemini 4 Argon | a stratified 14k | $91 |

Round 2 (AVOCET) gave every human item all three teachers, for $105.

**Teacher agreement.** Teachers agree with each other at about 0.69 on raw
returned labels, and about 0.75–0.77 on supported, closed label sets.

**Fair LLM bars:**

| Set | Haiku vs F∩A | Flash vs H∩A | Argon vs H∩F |
|---|---|---|---|
| All segments, round 1 | 0.757 | 0.742 | 0.728 |
| Typed human text (H-typed), v2 labels, n=1,494 | 0.769 | 0.758 | 0.749 |

## Round 1 (2026-10-28 overnight)

| System | J vs three-teacher intersection | Fair LOO vs F∩A | 400k items |
|---|---|---|---|
| ettin-encoder-68m, fine-tuned | 0.782–0.788 | 0.772–0.774 (Haiku 0.757) | 15 min GPU, 2.5 h CPU (int8) |
| ettin-encoder-32m, fine-tuned | 0.774 | 0.757 (= Haiku) | 6 min GPU, 1.1 h CPU (int8, 92 MB) |
| Same 32m, never shown a Haiku label | 0.731 | 0.755 | |
| Linear: LightGBM stacker over linear outputs | 0.743 | 0.736 | minutes, CPU |
| Linear: single hashed TF-IDF logistic model | 0.721 | 0.716 | seconds, CPU |
| Static embeddings (fine-tuned Model2Vec) + MLP | 0.718–0.737 | 0.709–0.716 | ~25 s, CPU |
| Hashed char n-gram smoke model (`tools/labeling_smoke.py`) | 0.658 | 0.647 | |
| Per-cohort label prior | 0.447 | | |

What round 1 established:

- **Training target.**
  - Consensus targets beat Flash-only labels by 0.01–0.02.
  - More data beats purer labels: training only on the three-teacher
    intersection, or only on Argon's labels, was worst.
  - About 100k consensus-labeled items gets most of the quality. The 32m
    learning curve: 10k 0.648, 30k 0.729, 100k 0.767, 269k 0.774.
- **Distribution shift.** The dev-to-test drop came almost entirely from one
  cell: automation prompts sent as user messages, in the last 14 days. These
  were new orchestration templates the training split never saw.
  - The teachers drop there too: Haiku against F∩A goes from 0.83 to 0.67.
  - The fix that worked: retrain on about 13k labels of recent traffic (about
    $25 of Flash plus Haiku). On held-out new families this lifts that cell
    from 0.627 to 0.732.
- **Quantization.** Dynamic int8 quantization costs the encoders about 0.02,
  because a few activation channels are about 400× larger than the rest.
  Weight-only int8 (MatMulNBits) costs nothing.
- **Audit and cascades.**
  - A hand audit of 112 disputed decisions found:
    - every case where the student missed a label all three teachers agreed on
      was a real error;
    - the student's extra labels were usually right: one teacher had omitted
      the label, or quote validation had rejected escaped text.
  - The strict three-teacher intersection therefore under-credits correct
    extra labels.
  - Sending the least-confident 10% of items to Haiku costs about $35 per
    400k and lifts the score against F∩A to about 0.786.
- **Typed human text.** The 32m student trailed Haiku there by −0.051, and the
  68m by −0.030 [−0.054, −0.006]. The headline result hid this, because human
  text is 4.6% of items.

## Round 2 (2026-10-29): the bird studies

| Study | Question | Answer |
|---|---|---|
| AVOCET | Label data and harness v2 | Every human item labeled by all three teachers ($105). Held-out H-typed y3 set: 366 → 1,494 items. Harness v2 scores by segment. Found a runner performance bug. |
| BOBOLINK | Do per-segment encoders help? | **No**, once the shared model trains on the same labels. The 68m shared model retrained on v2 labels scores H-typed **0.772 vs Haiku 0.769**. Every specialist lands within ±0.004. The round-1 gap was missing human labels, not context: any context of 64 tokens or more scores the same. |
| BUFFLEHEAD | Which segments should share a model? | One 68m base for agent replies, plus a 4–8 MB human LoRA adapter for human text, automation prompts and delegations, plus an 8 MB thinking adapter. Human training costs agent replies 0.02–0.03, so they stay on the base. |
| CHICKADEE | Does more labeling context help? | **No.** Longer context (earlier turns, 4,000 chars) and the full last assistant message (up to 6,000 chars) gave no better labels and no better students, by blind judge, a 10,000-char three-teacher yardstick, or the standard harness. Re-running the standard labeling moves results as much as any context change. Keep 600 characters. |
| DOTTEREL | The best pure-Go linear model | `V4-hybmaj`; results below. |
| EGRET | Thinking text | Teachers agree best on thinking (Flash–Argon 0.796). Round-1 students fail on it zero-shot (0.60). Trained on 14k thinking items, an 8.4 MB LoRA reaches 0.798 vs Argon (Flash 0.793), and a TF-IDF linear model 0.768. |
| GANNET | An untouched holdout | 2,351 items from 697 families that started after the round-1 snapshot. Labeled by Flash and Argon only (Claude quota); scored once. |

### DOTTEREL's Go linear model, by segment

Fair LOO on the v2 held-out sets (vs F∩A / H∩A / H∩F):

| Segment | Go linear `V4-hybmaj` | 68m encoder | LLM bar |
|---|---|---|---|
| H-typed (primary) | 0.711 / 0.717 / 0.708 | 0.768–0.774 | Haiku 0.769 |
| H-typed + quoted/pasted context | 0.676 | 0.72–0.76 | |
| H-template | ~0.99 | ~0.99 | |
| Agent replies (82% of items) | 0.770 / 0.771 / 0.766 | 0.806 | ~0.76 |
| Delegations | 0.72–0.74 | similar | 0.57–0.61 |
| Automation prompts (AIUR) | 0.594 | 0.60–0.68 (fresh) | 0.67 (fresh) |

- **Model:** one shared logistic model with about 500k hashed features. The
  feature blocks are the round-1 blocks, a segment one-hot, word-order
  features, and human-only copies of the main blocks (human rows weighted
  ×3). The target is 2-of-3 majority on human rows; machine rows use
  consensus, with encoder pseudo-labels filling Flash-only rows.
- **Plateau:** human-fold cross-validation puts every variant at 0.712–0.718.
  More human labels add about +0.002, so the model is feature-limited.
- **What didn't help:** per-segment models, per-segment thresholds, richer
  context features, distillation (helps agent replies and delegations, hurts
  H-typed), small nonlinear stackers.
- **Pure Go:** standard library only, no cgo. It matches Python to 1.3e-13 on
  logits, with 0 label flips on 95,602 items.
  - Throughput: 4–7.8k items/s on one goroutine, 17–32k/s on four.
  - Memory: 135–220 MB RSS.
  - Files: 95–161 MB float32, or 33 MB for the 150k-feature float16 variant,
    which scores the same.
  - Python/Go differences it had to absorb: Unicode version, lowercasing,
    regexp `\w`/`\b`, float32 `log1p`, fused multiply-add. See
    [reports/r2-DOTTEREL-PARITY.md](reports/r2-DOTTEREL-PARITY.md).

### Speed and latency

**Encoder per message.** Measured on human messages, int8 ONNX, 4 CPU
threads, M3 Ultra:

| Model | Median | p90 | p99 |
|---|---|---|---|
| 68m | 31 ms | 48 ms | 98 ms |
| 32m | 12 ms | 19 ms | 38 ms |

An M2 Air should be about 1.3–1.5× slower.

**Encoder backfill.** A 400k-item backfill takes about 5.3 h for the 68m,
almost all of it agent replies.

**Linear model.** The same backfill takes seconds.

### GANNET: new conversations

The metric here is the student against Argon, beside Flash against Argon,
since there are no Haiku labels. It was calibrated on the v2 held-out sets,
because even in-distribution a consensus-trained student trails a single
teacher on it.

| Model | H-typed vs F∩A, in-distribution → GANNET | Agent replies vs F∩A, in-distribution → GANNET |
|---|---|---|
| 68m encoder (`c68v-hum`, 2-seed blend) | 0.768 → 0.757–0.769 | 0.777 → 0.743–0.748 |
| Go linear `V4-hybmaj` | 0.708 → 0.666 | 0.763 → 0.698 |

- **The linear model degrades more on new conversations.** On H-typed it loses
  about 0.04, against the encoder's 0.01–0.02. That widens the gap on new
  data to about 0.08–0.10 (30 families, so a wide CI).
  - Mitigation: retrain often. The linear model retrains in about a minute on
    CPU; new labels cost about $0.0015 per item with Flash.
- **Delegations are anomalous for every model**, encoder and linear alike:
  0.12–0.46 against 0.58–0.63 in-distribution, on 84 items in 12 families.
  Because every model fails together, the likely cause is a change in how
  delegations are stored or prepared between the schema-14 and schema-21
  snapshots, not model drift. Investigate before shipping delegation labels.
- **Thinking without training data:** zero-shot, the linear model scores 0.41
  on thinking. Thinking needs its own training labels.

## Operational lessons

- **Labeling runner performance.** Without an index on `attempts(job)`, the
  runner rescans the whole ledger inside the write-locked transaction on every
  lease, so it slows as the ledger grows. Adding the index cut runner CPU from
  96% to 2%.
- **Quota errors.** The runner records agy quota errors (HTTP 429) as invalid
  or tool use, instead of halting as documented.
- **Dropped items.** Flash silently drops items from multi-item requests:
  about 15% of the GANNET items at first. Re-requesting them one item per
  request fixes most.
- **Label noise.** Label noise between two runs of the same configuration is
  about 0.016, the same size as many effects we looked for. Use paired CIs,
  family-level bootstrap, and repeated reads where it matters.

## Caveats

- **Labels:** these are LLM labels, not reviewed truth. The taxonomy itself is
  marked "structural proposal, not validated".
- **One library:** the results describe one person's library and one
  two-week fresh window.
- **Weak cells:** confidence intervals on H-typed GANNET and on delegations are
  wide.
- **Weak labels:** the students never predict `P.frame.criteria`, and are weak
  on revise, verify, constraint and `C.input`.
