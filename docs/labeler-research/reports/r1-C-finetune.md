> Sanitized copy of ~/pharos-labeling/research/tracks/C-finetune/REPORT.md (archived in full on euclid at "Pharos Labeling/2026-10-28-student-research"). Verbatim conversation text was removed.

# Track C: fine-tuned small encoders: REPORT

2026-10-28/29, track C (fine-tuned transformer encoders). All numbers come from the shared harness (`rlh.evaluate`;
leaderboard rows `c-*`). The cell numbers (split × cohort, F2) come from `rlh.score` on the harness-saved predictions
(`scripts/cells.py`, `scripts/f2cells.py`).

## Question
How close can a fine-tuned small encoder get to the three-teacher intersection `y3`, and to the LLMs on the
leave-one-out (LOO) bar? What does it cost to train, and to run over 400k items?

## Phase 3 headline (details at the end)
- **Corpus labeled:** all 364,908 items by 68m (14.0 min, 436 items/s) and 32m (5.4 min, 1,117 items/s) in `corpus/`.
- **CPU int8 fixed:** weight-only int8 with blockwise int8 compute keeps the fp32 quality.
  32m: 0.777 / 0.781 at 99 items/s on 16 threads (400k in 1.1 h). 68m: 0.784 / 0.788 at 45 items/s.
- **Seed spread:** on all data the y3 J spread is ≤ 0.002 and LOO about 0.01; at 100k with a new sample, about 0.008.
  68m vs 32m (+0.011 J, two seeds each) and 68m > Haiku on LOO-FA (0.774 / 0.772 vs 0.757) are real.
  The best single run is now `c-e68-512-all-s1` (0.788 / 0.790).
- **Consensus-head masking (audit fix):** no change on strict y3 or LOO (inside the seed spread). It needs an
  abstain-aware metric to show value.
- **Deployment candidate `c-e68-all-adaptF1-hf`** (F2 only): gold3 F2 0.801, AIUR 0.769, LOO-FA 0.764 vs Haiku 0.753.
  The refresh costs about $25–30 in labels plus 3 min of MPS.

## Answer (numbers first)
- **Best: `c-e68-512-all`.** ettin-encoder-68m (68M params), all 269k train items, 1 epoch, 20 min MPS compute.
  gold3_test J **0.782 raw / 0.787 weighted** (smoke char-LR 0.658 / 0.697; Haiku vs y3 0.833 / 0.813).
  On LOO it **beats all three LLMs**: vs F∩A 0.774 (Haiku 0.757), vs H∩A 0.781 (Flash 0.742), vs H∩F 0.779
  (Argon 0.728). Weighted: 0.784 / 0.791 / 0.782 vs LLM 0.749 / 0.746 / 0.724. gold2_test 0.793, flash_test 0.784.
  Inference is 436 items/s on MPS, so 400k items take 15 min.
- **Best per unit of cost: `c-e32-512-all`.** ettin-encoder-32m (32M), all data, 13 min training. gold3_test 0.774 / 0.776.
  On LOO it matches Haiku and beats Flash and Argon: FA 0.757 (Haiku 0.757), HA 0.773, HF 0.770 (weighted 0.767 / 0.782 / 0.775).
  Inference is 1,065 items/s on MPS (400k in 6 min) and 80 items/s on CPU.
- **LOO-clean claim holds.** `c-e32-512-100k-noH` never saw a Haiku label (heads Flash, Argon, F∩A; 100k items):
  LOO-FA **0.755 raw / 0.763 weighted vs Haiku 0.757 / 0.749.** A 32M student matches Haiku on the fair bar overall.
- **Capacity:** ettin-68m at 100k (`c-e68-512-100k`) = 0.771 / 0.774, LOO-FA 0.763, about the same as 32m on all
  data, at 2.5× the inference cost.
  On all data 68m gains +0.008 raw / +0.011 weighted over 32m, and +0.017 LOO-FA (0.774 vs 0.757).
- **Where it falls short: fresh agent_in_user_role (AIUR)**, new automation templates: J 0.675 vs y3, LOO-FA 0.598
  vs Haiku 0.674 (c-e32-512-all; 68m-all: 0.671 and 0.614). More of the same train data does not help there
  (32m 100k: 0.672, 269k: 0.675; 68m 100k: 0.633, all: 0.671).
- **Fresh adaptation fixes most of it cheaply.** Adding 12.7k Flash labels from half the fresh families (~$19)
  to a 1.5–3-min continuation lifts the held-out half's AIUR cell: flash_test F2 AIUR 0.609 → 0.675 with 32m and
  0.616 → 0.702 with 68m (replay-only control 0.606). gold3 F2 AIUR LOO-FA goes 0.63 → 0.72 (Haiku 0.693), and
  gold3 F2 LOO-FA goes 0.743 → 0.769 with 68m (Haiku 0.753).
- **Cost:** 400k items in ~6 min on MPS (1,065 items/s, bf16) or ~1.4 h on CPU (ONNX fp32, 16 threads, 80 items/s).
  Training on all data: 13 min MPS compute (27 min wall under contention). Peak MPS allocation 15.7 GB (68m: 22.8 GB; driver allocation incl. cache, RSS 3–5 GB).

## Recipe
- Input: `[CLS] <cohort> <sender kind> : <context, cut from the left, ≤64 tokens> [SEP] <text> [SEP]`, max_len 512.
  Text longer than its budget keeps 70% head + 30% tail. Context takes more room when the text is short.
  Length-bucketed batches (12,288-token budget; 8,192 for 68m), sequences padded to a multiple of 32.
- Model: encoder → masked mean pool → dropout 0.1 → one 54-wide linear head **per target** (multi-task):
  `y_hf`, `haiku`, `flash`, `argon`, `y3`. BCE per head over rows where that target is present. Teacher heads
  use the teacher's `masked` matrix as a loss mask; the intersection heads are unmasked, as scored.
  Head biases start at label log-odds.
- Sampling: every Argon row (10,478, so every y3 row) plus a sample weighted by cohort (agent_reply 1,
  AIUR 2, delegation 2, human 3) × rare-label boost (inverse frequency of the rarest positive, clipped to 1..20).
  "all" uses the whole pool.
- Optimizer: AdamW, lr 8e-5 for the encoder and 1e-3 for the heads, wd 0.01, 5% warmup, linear decay, 1 epoch,
  bf16 autocast on MPS (fp32 was no faster), gradient clip 1.0. Checkpoint every 10 min, resumable.
- Scoring: on each family's dev set, try each head and combinations (mean of y3+y_hf heads, product or min of the
  teacher heads) with per-label-F1 and global-Jaccard thresholds, then refine the winner with per-label-Jaccard
  coordinate ascent. For y3 the winner was usually mean(y3 head, y_hf head) (32m runs). The 68m runs picked product or min of the three
  teacher heads (100k: prod, all: min). Dev differences between these scorers were ≤ 0.01. Closure: `rlh.close_binary`.

## Results (harness; gold3_test unless noted; J = mean Jaccard)

| run | model | train items | J raw / w | split=test raw / w | split=fresh raw / w | fresh AIUR | LOO FA / HA / HF (raw) | gold2_test | flash_test | train min (MPS) | MPS items/s |
|---|---|---|---|---|---|---|---|---|---|---|---|
| c-e32-512-10k | ettin-32m | 10,478 (Argon rows only) | 0.648 / 0.649 | 0.675 / 0.691 | 0.617 / 0.621 | 0.565 | 0.647 / 0.656 / 0.642 | 0.654 | - | 0.4 | ~1,100 |
| c-e32-512-30k | ettin-32m | 30,000 | 0.729 / 0.727 | 0.753 / 0.757 | 0.701 / 0.710 | 0.604 | 0.716 / 0.728 / 0.722 | 0.734 | - | 1.1 | ~1,100 |
| c-e32-512-100k | ettin-32m | 100,000 | 0.767 / 0.765 | 0.783 / 0.781 | 0.748 / 0.766 | 0.672 | 0.751 / 0.767 / 0.761 | 0.763 | 0.759 | 7.8* | 604* |
| **c-e32-512-all** | ettin-32m | 269,306 | **0.774 / 0.776** | 0.793 / 0.797 | 0.753 / 0.767 | 0.675 | **0.757** / 0.773 / 0.770 | 0.778 | 0.772 | 13.3 | 1,065 |
| c-e68-512-100k | ettin-68m | 100,000 | 0.771 / 0.774 | 0.792 / 0.795 | 0.747 / 0.771 | 0.633 | 0.763 / 0.771 / 0.766 | 0.775 | 0.775 | 9.2 | 430 |
| **c-e68-512-all** | ettin-68m | 269,306 | **0.782 / 0.787** | 0.795 / 0.798 | 0.768 / 0.791 | 0.671 | **0.774 / 0.781 / 0.779** | 0.793 | 0.784 | 20.1 | 436 |
| c-e32-512L1024-100k | ettin-32m, AIUR/delegation at 1024 | 100,000 | 0.765 / 0.765 | 0.784 / 0.787 | 0.743 / 0.755 | 0.676 | 0.761 / 0.767 / 0.763 | 0.767 | 0.753 | 4.9 | 906 |
| c-e32-512-100k-anti | ettin-32m, dedup + template damp + chunk tag | 100,000 | 0.767 / 0.768 | 0.792 / 0.793 | 0.739 / 0.756 | 0.635 | 0.752 / 0.768 / 0.765 | 0.763 | 0.758 | 3.1 | ~1,100 |
| c-e32-512-100k-noH (LOO-clean) | ettin-32m, no Haiku | 100,000 | 0.731 / 0.738 | 0.753 / 0.761 | 0.705 / 0.707 | 0.599 | **0.755** / 0.744 / 0.729 | - | - | 3.7 | 1,115 |
| c-e32-512-100k-onnx-int8 | 100k run, ONNX int8, CPU | - | 0.743 / 0.743 | | | | 0.728 / 0.741 / 0.740 | - | - | - | 85-98 (CPU) |
| base-smoke-char-lr (reference) | char n-gram LR | 132k | 0.658 / 0.697 | 0.701 / 0.734 | 0.608 / 0.688 | 0.342 | 0.647 / 0.653 / 0.653 | 0.692 | 0.680 | 1.4 (CPU) | ~1,500 (CPU) |
| ref-haiku / ref-flash / ref-argon | LLM | - | 0.833 / 0.797 / 0.745 | | | 0.834 / 0.706 / - | LLM bar: Haiku FA 0.757, Flash HA 0.742, Argon HF 0.728 | | | | |

\* contended with track B and with my own CPU benchmarks. Uncontended ettin-32m inference is ~1,050–1,115 items/s.
gold3 J standard error is about 0.005–0.01. Fresh AIUR on gold3_test has n=250.

LLM references on the fresh-AIUR cell (LOO, fair): Haiku vs F∩A 0.674, Flash vs H∩A 0.634.
On gold3_test with cohort weighting, LOO-FA: c-e68-512-all 0.784, c-e32-512-all 0.767, c-e68-512-100k 0.771, noH 0.763; Haiku 0.749.

### Learning curve (ettin-32m, max_len 512, 1 epoch, gold3_test J raw)
10.5k: 0.648 → 30k: 0.729 → 100k: 0.767 → 269k: 0.774 (gold2_test 0.654 → 0.734 → 0.763 → 0.778).
Most of the gain comes by 100k items; ×2.7 more data adds +0.007 on gold3 and +0.015 on gold2. A 68m model at 100k
gives the same gain as 32m on all data. The data is not exhausted: gold2/flash keep rising. The fresh-AIUR cell
does not move with more of the same train data (0.672 → 0.675). Only new templates move it (below).
A refresh needs on the order of 10–30k labeled items to train a new model from scratch. Adapting an existing model
needs ~13k fresh labels (below).

### Fresh adaptation (deployment simulation; F2-only numbers)
The fresh families are split by `md5(family_id) % 2` into F1 (adapt) and F2 (eval). Continuation of `c-e32-512-all`:
lr 3e-5, a 20k train replay (all Argon rows + sample) plus all 12,733 F1 fresh items, with Flash labels only, or
Flash + Haiku + H∩F where they exist (6,285 Haiku). Argon labels of fresh items were never used. Each run took
1.2–1.5 min of MPS compute (68m: same recipe from `c-e68-512-all`, 2.9 min). Labeling F1 with Flash would cost 12,733 × $0.0015 ≈ **$19**.
The whole-set harness rows of the `adaptF1` runs are leaky (F1 items' labels were in training) and must not be read.
Their notes say so. Only F2 below:

| run (scored on F2 only) | gold3 F2 J (n=472) | w | gold3 F2 AIUR (n=122) | F2 LOO-FA | F2 AIUR LOO-FA | flash_test F2 (n=13,346) | flash_test F2 AIUR (n=1,989) |
|---|---|---|---|---|---|---|---|
| c-e32-512-all (no adaptation) | 0.768 | 0.773 | 0.688 | 0.736 | 0.637 | 0.738 | 0.609 |
| c-e32-all-cont-control (replay only) | 0.777 | 0.774 | 0.719 | 0.744 | 0.661 | 0.735 | 0.606 |
| c-e32-all-adaptF1-flash | 0.777 | 0.774 | 0.737 | 0.761 | 0.720 | 0.753 | 0.675 |
| c-e32-all-adaptF1-hf | 0.800 | 0.792 | 0.791 | 0.762 | 0.720 | 0.754 | 0.684 |
| c-e68-512-all (no adaptation) | 0.773 | 0.798 | 0.662 | 0.743 | 0.627 | 0.752 | 0.616 |
| c-e68-all-adaptF1-flash | 0.787 | 0.802 | 0.726 | 0.769 | 0.719 | 0.772 | **0.702** |
| ref-haiku | 0.850 | 0.834 | 0.850 | 0.753 | 0.693 | - | - |

The reliable cell is flash_test F2 AIUR (n=1,989). The control shows that continued training alone does nothing
there (0.609 → 0.606), while Flash labels of new traffic give +0.07 (32m) and +0.09 with 68m (0.616 → 0.702;
2.9 min of continuation). So capacity does pay once the new templates are in training. On gold3 F2 the student's LOO-FA after
adaptation (0.761, AIUR 0.720) is at or above Haiku's (0.753, 0.693), but n=122–472, so the error is ±0.02–0.04.

## Cost (400k items)
| path | items/s | 400k | notes |
|---|---|---|---|
| ettin-32m, MPS bf16 (torch) | 1,065 (uncontended), ~600 contended | **6 min** (11 min contended) | includes tokenization; length-bucketed; 32k-token batches |
| ettin-68m, MPS bf16 | 436 | 15 min | |
| ettin-32m, CPU ONNX fp32, 16 threads | 80 (55 with 8 threads) | 1.4 h (2 h) | matches torch (max abs Δp 4e-6) |
| ettin-32m, CPU ONNX dynamic int8 | 98 | 1.1 h | **−0.02 J** (see below); 32 MB model |
| ettin-68m, CPU ONNX fp32 / int8, 16 threads | 31 / 50 | 3.6 h / 2.2 h | |
| training (ettin-32m) | 100k: 3.7–7.8 min; all 269k: 13.3 min MPS compute | | peak MPS 15.7 GB (driver allocation incl. cache), RSS ~3–5 GB |
| training (ettin-68m) | 100k: 9.2 min; all 269k: 20.1 min MPS compute (41 min wall, contended) | | peak MPS 22.8 GB |

Candidate benchmark before READY (MPS bf16, real texts, contended with track B; train | inference items/s at max_len 512):
ettin-32m 221 | 858; bge-small 162 | 707; MiniLM-L12 74 | 377; ettin-68m 99 | 306; ModernBERT-base 31 | 84; ettin-150m 26 | 83
(`logs/bench.jsonl`). ettin (ModernBERT architecture, code-heavy pretraining, 8k context) was the fastest at every length.

CPU timings were taken on a shared machine (another track's SVD at 100% CPU), so they are a lower bound.
Mean input length was ~225 tokens. Most agent replies are short (median 39 text tokens), while AIUR and delegation
items fill all 512.

**Why int8 loses 0.02 J:** onnxruntime's dynamic quantization scales activations per tensor at runtime. ettin's residual
stream has a few outlier channels (max |x| ≈ 5,300 against a median channel max of ≈ 13, about 400× larger, in
layers 7–9), so a uint8 step of ~20 erases the ordinary channels. Per-channel weights or quantizing MatMul only did
not help (0.744 / 0.743). Static, calibrated quantization with outlier handling, or weight-only int8, would be the
fix. fp32 on CPU is fast enough anyway.

**Go portability:** the realistic path is ONNX Runtime through its C API (cgo; e.g. `yalue/onnxruntime_go`). It needs
a ~20 MB dylib, a 128 MB fp32 model (32 MB int8) and a tokenizer. ettin uses a ModernBERT/OLMo-style BPE
(`tokenizers` JSON). Go needs a byte-identical tokenizer (e.g. `daulet/tokenizers` via Rust FFI, or a pure-Go BPE
port) and a parity fixture, per the repo's "Production student" rule. Head+tail truncation and context packing
(`scripts/common.py: Encoder`) are ~40 lines to port. This is heavy but tractable. A pure-Go forward pass is not
reasonable. Alternatively, run the Python/ONNX labeler as an offline batch job (6 min on MPS) and import its labels.

## What did not work / caveats
- **Bigger models buy little per unit of cost.** At 100k items 68m ≈ 32m on all data. On all data 68m adds
  +0.008 raw / +0.011 weighted J and +0.017 LOO-FA at 2.5× the inference cost. On the fresh-AIUR cell it does not help
  (0.671 vs 0.675). ettin-150m and ModernBERT-base are 7–8× slower to train than 32m (26–31 items/s at 512) and were
  not run. deberta-v3-small was skipped (its tokenizer needs extra packages, and it was slow on MPS).
- **int8 dynamic quantization:** −0.02 J (above).
- **Anti-memorization did not help** (`c-e32-512-100k-anti`: dedup by target_hash, which takes the pool from 269k to
  253k; weight × (100-char-prefix group size)^-½ × (family size/50)^-½; "part k of n" tag on chunks). Overall it
  equals the baseline (0.767 / 0.768). Fresh AIUR is 0.635 vs 0.672 on gold3 and 0.590 vs 0.595 on flash_test F2.
  The damping also halved the AIUR items in the sample (11.0k → 5.6k), which confounds the comparison. The shifted
  cell is new templates, not memorized old ones, so reweighting old data cannot fix it. New labels do (adaptation).
- Dev → test gap: gold3_dev 0.814 vs gold3_test 0.774. Most of it is fresh (split=test 0.793 vs fresh 0.753). The
  per-label thresholds are tuned on 1,333 dev items, so the dev numbers are optimistic.
- The non-LOO-clean LOO numbers (`c-e32-512-all` etc.) come from students trained on all three teachers, so "matches
  Haiku" rests on the noH run, which shows the same result.
- **Longer input for long cohorts is roughly neutral** (`c-e32-512L1024-100k`: AIUR/delegation at max_len 1024, the rest
  at 512; vs `c-e32-512-100k`). gold3_test is 0.765 vs 0.767. Fresh AIUR is 0.676 vs 0.672 (LOO-FA 0.639 vs 0.605).
  flash_test F2 AIUR is 0.611 vs 0.595, but flash_test overall drops to 0.753 from 0.759. That is a small AIUR gain at
  −15% inference speed and 24.7 GB peak MPS (just over the 24 GB budget). Not worth it alone.

## Recommendations
1. **Ship a fine-tuned ettin encoder at max_len 512 with multiple heads, trained on all train data.** Use 68m
   (`c-e68-512-all`: beats every LLM on LOO, 400k in 15 min on MPS, 20 min training) when MPS/Python is available.
   Use 32m (`c-e32-512-all`: matches Haiku, 400k in 6 min on MPS or ~1.4 h on CPU ONNX) for a CPU/Go path.
2. **Plan periodic adaptation; it matters more than model size.** (68m gains more from adaptation than 32m.) Label a sample of new traffic with Flash (~13k items ≈ $19
   per refresh; more for weeks with many new templates) and continue training for ~2 min. That is what moves the
   weak cell (new automation prompts). Haiku labels where they exist help a little more.
3. Use the probabilities for a cascade: AIUR items with an unseen prefix template are where the student is weakest.
   Director/track E have all dev/test/fresh probs (`models/c-e32-512-all/` and `models/c-e68-512-all/all_{dev,test,fresh}_probs.npz`,
   5 heads in order y_hf, haiku, flash, argon, y3; fp16)
   and harness preds for every run.
4. For Go, use ONNX Runtime via cgo plus a tokenizer parity fixture, or batch-label offline. Use weight-only int8
   (Phase 3), not dynamic int8.

## Open questions
- Would separate encoding of the instruction head vs quoted material (D-shift's suggestion) help AIUR more than a longer max_len?
- Seed variance was not measured. Differences < 0.01 on gold3 are noise.
- Is fresh-adaptation gain stable over successive refreshes (e.g. weekly)? How many labels per refresh are needed
  (only 12.7k was tested)?

## Files
- Scripts: `scripts/train.py` (training + harness eval), `common.py` (input encoding, bucketing), `bench.py`
  (MPS throughput), `cpu_bench.py` (CPU/ONNX), `onnx_eval.py`, `cells.py`, `f2cells.py`, queues `queue*.sh`.
- Models: `models/<run>/final.pt`, `meta.json`, per-eval-set probs; ONNX in `models/onnx/`.
- Logs: `logs/<run>.out`; benchmark `logs/bench.jsonl`, `logs/cpu_bench.jsonl`.
- Packages added to the venv: `onnx` 1.23.2 (and its dependency `ml-dtypes`).

# Phase 3 (00:45–03:30 MDT)

## 1. Full-corpus labels (done 01:04)
Every item of `data/items.parquet` (364,908) was labeled by both models, under `corpus/` (README there).
Each npz holds fp16 per-head probabilities, the y3-scorer probabilities, the gold3_dev thresholds and the closed label sets.

| model | wall | items/s (MPS bf16, incl. tokenization) | check vs harness (gold3_test rows identical / J) | mean labels per item |
|---|---|---|---|---|
| c-e68-512-all | 14.0 min | 436 | 100% / 0.7824 | 3.65 |
| c-e32-512-all | 5.4 min | 1,117 | 100% / 0.7744 | 3.66 |

So 400k items take 15.3 min (68m) or 6.0 min (32m) on this Mac.

## 4. CPU int8 fixed: weight-only quantization
onnxruntime `MatMulNBitsQuantizer` was used: symmetric, block size 32, `accuracy_level=4`. That means int8 weights and
int8 compute with blockwise (per-32) activation quantization, so an outlier channel only coarsens its own block.
Embeddings (Gather) stay fp32. Thresholds were re-tuned on gold3_dev from each variant's own probabilities
(`scripts/quant.py`, `onnx_eval.py`).

| model / variant (CPU, 16 threads, corpus-like sample) | gold3_test J raw / w | LOO vs F∩A | items/s | 400k | file |
|---|---|---|---|---|---|
| 32m fp32 ONNX | 0.776 / 0.778 | 0.757 | 74 | 1.5 h | 128 MB |
| 32m dynamic int8 (Phase 1) | 0.764 / 0.770 | 0.752 | 118 | 0.9 h | 32 MB |
| **32m weight-only int8 (w8, block 32, acc 4)** | **0.777 / 0.781** | 0.759 | **99** | **1.1 h** | 92 MB |
| 32m weight-only int4 (block 32, acc 4) | 0.775 / 0.776 | 0.755 | 105 | 1.1 h | 86 MB |
| 32m w8 with fp32 compute (acc 0) | 0.775 / 0.778 | 0.757 | 80 | 1.4 h | 92 MB |
| 68m fp32 ONNX | (= harness 0.782 / 0.787) | 0.774 | 36 | 3.1 h | 274 MB |
| **68m weight-only int8 (w8, block 32, acc 4)** | **0.784 / 0.788** | 0.778 | **45** | **2.5 h** | 152 MB |
| 68m dynamic int8 | not scored | | 58 | 1.9 h | 69 MB |

Weight-only int8 loses nothing (it is within dev-threshold noise of fp32) and is 1.3× faster than fp32. Dynamic int8
is faster still but costs 0.01–0.02 J. For Pharos/Go the candidate is the 32m w8 model: ONNX Runtime CPU, about 1 h
per 400k items on 16 threads, or the 68m w8 at about 2.5 h. The contrib op MatMulNBits is in stock onnxruntime
builds, including the C API that Go bindings use.

## 2. Seed variance

| pair (same recipe, different seed) | gold3_test J raw / w | LOO FA / HA / HF | gold2_test | flash_test | fresh AIUR | dev-chosen y3 scorer |
|---|---|---|---|---|---|---|
| c-e32-512-all (seed 0) | 0.774 / 0.776 | 0.757 / 0.773 / 0.770 | 0.778 | 0.772 | 0.675 | mean(y3, y_hf) |
| c-e32-512-all-s1 (seed 1) | 0.774 / 0.774 | 0.768 / 0.779 / 0.771 | 0.779 | 0.775 | 0.654 | y3 head |
| c-e32-512-100k (seed 0) | 0.767 / 0.765 | 0.751 / 0.767 / 0.761 | 0.763 | 0.759 | 0.672 | mean(y3, y_hf) |
| c-e32-512-100k-s1 (seed 1, also a new 100k sample) | 0.759 / 0.757 | 0.751 / 0.758 / 0.756 | 0.765 | 0.759 | 0.622 | mean(y3, y_hf) |
| c-e68-512-all (seed 0) | 0.782 / 0.787 | 0.774 / 0.781 / 0.779 | 0.793 | 0.784 | 0.671 | min of teacher heads |
| c-e68-512-all-s1 (seed 1) | 0.788 / 0.790 | 0.772 / 0.785 / 0.781 | 0.794 | 0.787 | 0.663 | mean(y3, y_hf) |

Seed 1 changes the head initialization, batch order and dropout, and at 100k also which items are sampled.
- **On all data the y3 J spread is tiny (≤ 0.002)**, but LOO-FA moves by 0.011 and fresh AIUR by 0.02. The dev-selected
  scorer and thresholds differ between seeds, and LOO targets and small cells are more sensitive to them than y3 J.
- **At 100k the spread is ~0.008 J**, because the sample changes too.
- Readings:
  - "32m on all data ≈ 0.774" is solid.
  - "32m matches Haiku on LOO-FA (0.757–0.768 vs 0.757)" holds for both seeds. If anything the student is slightly
    above Haiku.
  - **68m vs 32m is real:** 0.782 and 0.788 against 0.774 and 0.774 (+0.011 mean raw, +0.014 weighted), and
    gold2 and flash rise by +0.015 too.
  - **68m beats Haiku on LOO-FA in both seeds:** 0.774 and 0.772 vs 0.757, about 3× the seed spread. It beats Flash
    on HA (0.781–0.785 vs 0.742) and Argon on HF (0.779–0.781 vs 0.728) by wide margins.
  - Single-run differences under ~0.01 at 100k, or ~0.01 LOO, are noise.

## 3. Audit-driven target fix: consensus-head masking (no gain on strict y3)
`c-e32-512-100k-cmask` uses the same recipe as `c-e32-512-100k`. The teacher heads were already masked on their own
teacher's `masked` labels. This run also loss-masks the consensus heads where any member teacher masked the label
(y_hf: Haiku or Flash, 2.2% of present cells; y3: any of three, 3.7%).

| run (100k) | gold3_test J raw / w | split=test / fresh | fresh AIUR | LOO FA / HA / HF | gold2_test | flash_test |
|---|---|---|---|---|---|---|
| c-e32-512-100k (seed 0) | 0.767 / 0.765 | 0.783 / 0.748 | 0.672 | 0.751 / 0.767 / 0.761 | 0.763 | 0.759 |
| c-e32-512-100k-s1 | 0.759 / 0.757 | 0.783 / 0.731 | 0.622 | 0.751 / 0.758 / 0.756 | 0.765 | 0.759 |
| **c-e32-512-100k-cmask** | 0.759 / 0.759 | 0.779 / 0.736 | 0.658 | 0.751 / 0.762 / 0.756 | 0.760 | 0.761 |

On strict y3 and LOO the masked run is inside the seed spread of the two baselines, so it is neither better nor
worse. That is expected. The fix stops penalizing the model for labels a teacher abstained on, which matters for an
abstain-aware or credited metric. Strict y3 still counts those labels as negatives, and the thresholds tuned on
strict-y3 dev absorb the difference.
To test it properly, score `preds/c-e32-512-100k-cmask.npz` against `preds/c-e32-512-100k.npz` with F-audit's
credited/abstain-aware scorer, after retuning thresholds for that target.
Masking is cheap and harmless, so keep it if the business metric moves to abstain-aware.

## 5. Deployment candidate: c-e68-512-all + fresh adaptation (F2-only)
`c-e68-all-adaptF1-hf` continues `c-e68-512-all` (lr 3e-5, 3.0 min MPS) on a 20k train replay plus the 12,733 F1
fresh items. It uses Flash labels on all of them, and Haiku and H∩F labels on the 6,285 that Haiku labeled; never
Argon. Scored on the F2 half only (the whole-set rows are leaky by design; notes say so):

| model (F2 only) | gold3 F2 J / w (n=472) | gold3 F2 AIUR (122) | F2 LOO-FA | F2 AIUR LOO-FA | flash_test F2 (13,346) | flash_test F2 AIUR (1,989) |
|---|---|---|---|---|---|---|
| c-e68-512-all (no adaptation) | 0.773 / 0.798 | 0.662 | 0.743 | 0.627 | 0.752 | 0.616 |
| c-e68-all-adaptF1-flash | 0.787 / 0.802 | 0.726 | 0.769 | 0.719 | 0.772 | 0.702 |
| **c-e68-all-adaptF1-hf** | **0.801 / 0.806** | **0.769** | 0.764 | **0.732** | 0.772 | 0.700 |
| ref-haiku | 0.850 / 0.834 | 0.850 | 0.753 | 0.693 | - | - |

The recommended going-forward labeler is `c-e68-all-adaptF1-hf`. On new families it beats Haiku on the fair bar
(LOO-FA 0.764 vs 0.753; AIUR 0.732 vs 0.693, with n=122, so ±0.04).
- Labeling the refresh sample cost: 12.7k Flash ≈ $19, plus 6.3k Haiku ≈ $5.5. If Haiku were run on all 12.7k,
  the refresh would cost ≈ $30.
- The refresh itself is 3 min on MPS.
- Inference is 436 items/s on MPS, or 45 items/s CPU w8.
- Weights are at `models/c-e68-all-adaptF1-hf/final.pt` (thresholds in `preds/c-e68-all-adaptF1-hf.npz`, tuned on
  gold3_dev).
