> Sanitized copy of ~/pharos-labeling/research/tracks/A-classic/REPORT.md (archived in full on euclid at "Pharos Labeling/2026-10-28-student-research"). Verbatim conversation text was removed.

# Track A (classic: lexical, linear, tree models): report

Question: how close can cheap, CPU-only, Go-portable models get to `y3` (Haiku ∩ Flash ∩ Argon closures) and to the LLMs on the leave-one-out comparisons, and at what cost?

All numbers come from the shared harness (`rlh.evaluate`; rows on `leaderboard.jsonl`, probabilities in `preds/` and `probs/`). gold3_test unless noted. Jw = cohort-weighted. Fresh AIUR = agent_in_user_role items in split=fresh (n=250). Thresholds are per-label Jaccard (`per_label_jaccard`), tuned on gold3_dev, which is 1,333 items, so differences under ~0.01 are noise.

## Answer in numbers

| run | dev J | test J / Jw | split=test Jw | fresh J / Jw | AIUR test / fresh | LOO FA / HA / HF | train | CPU inference (Python) |
|---|---|---|---|---|---|---|---|---|
| LLM fair refs: Haiku (vs FA), Flash (vs HA), Argon (vs HF) | 0.763 / 0.746 / 0.740 | | | | Haiku fair, fresh AIUR ≈ 0.674 | **0.757 / 0.742 / 0.728** | | |
| C `c-e32-512-100k` (fine-tuned 32M encoder, MPS) | 0.807 | 0.767 / 0.765 | 0.781 | 0.748 / 0.766 | 0.910 / 0.672 | 0.751 / 0.767 / 0.761 | 467 s | ~600/s on MPS; int8 CPU ~100/s at 16 threads (0.743 / 0.749) |
| **A-stack-multi-char** (LightGBM over OOF linear probs from 3 targets + meta + rules + SVD64) | 0.786 | **0.743 / 0.758** | **0.792** | 0.704 / 0.713 | 0.885 / 0.590 | 0.736 / 0.737 / **0.739** | ~16 min total | ~0.75k/s per core (est.), ~7–8k/s with 12 processes |
| A-stack-multi-char-nosvd (same, no SVD: Go-simpler) | 0.781 | 0.741 / 0.757 | 0.791 | 0.704 / 0.709 | 0.854 / 0.613 | 0.730 / 0.734 / 0.738 | ~16 min | same |
| **A-final-linear-char** (one logistic model: word + char_wb + context + head-500 + meta2 + rules, 3.05M cols) | 0.775 | 0.727 / 0.745 | 0.781 | 0.688 / 0.697 | 0.834 / 0.549 | 0.730 / 0.725 / 0.725 | 272 s, 13.8 GB | 1.1k/s per core; 10.9k/s with 12 processes |
| **A-final-linear-200k** (same without char, top-200k columns, 43 MB weights) | 0.774 | 0.721 / 0.738 | 0.776 | 0.680 / 0.686 | 0.817 / 0.544 | 0.716 / 0.715 / 0.718 | 36 s, 5.3 GB | 2.4k/s per core; 22.7k/s with 12 processes |
| **A-final-linear-50k** (top-50k columns, 11 MB weights) | 0.770 | 0.718 / 0.736 | 0.774 | 0.674 / 0.687 | 0.865 / 0.537 | 0.714 / 0.712 / 0.712 | 18 s, 5.3 GB | same as 200k |
| base-smoke-char-lr (reference) | 0.729 | 0.658 / 0.697 | 0.734 | 0.608 / 0.688 | 0.827 / 0.342 | 0.647 / 0.653 / 0.653 | 84 s | ~1.5k/s |
| base-cohort-prior (reference) | 0.518 | 0.447 / 0.432 | 0.485 | 0.401 / 0.392 | 0.429 / 0.272 | 0.427 / 0.441 / 0.449 | | |
| A-rules-only (34 regexes) | 0.453 | 0.442 / 0.376 | 0.423 | 0.437 / 0.322 | 0.669 / 0.481 | 0.440 / 0.436 / 0.446 | 0 | ~45k/s (10 procs) |

The full table, with flash_test and gold2_test columns, is in `logs/final_table.md` (regenerate with `python report.py <runs>`).

Per cohort, gold3_test raw J:

| | agent_reply (82% of items) | AIUR | delegation | human | common_short_text |
|---|---|---|---|---|---|
| A-stack-multi-char | **0.778** | 0.649 | 0.619 | 0.773 | 0.865 |
| A-final-linear-char | 0.768 | 0.607 | 0.589 | 0.763 | 0.829 |
| A-final-linear-50k | 0.757 | 0.603 | 0.615 | 0.746 | 0.827 |
| C c-e32-512-100k | 0.769 | 0.720 | 0.753 | 0.790 | 0.754 |
| C int8 CPU | 0.760 | 0.660 | 0.695 | 0.774 | 0.740 |
| Haiku (vs y3, optimistic) | 0.806 | 0.839 | 0.825 | 0.873 | 0.852 |

**Gap to C.**
- On agent_reply (82% of the population) and on the cohort-weighted split=test number, the best classic models equal or beat C: agent_reply 0.778 against 0.769, and split=test Jw 0.792 against 0.781.
- The whole gap is in three places:
  - Fresh AIUR: 0.59–0.61 against 0.67 (the teachers themselves fall to about 0.67 there).
  - Delegation: 0.62 against 0.75 (n=175).
  - Human items that need context: 0.66 against 0.73 on dev.
- Against C's CPU int8 build, which is the like-for-like deployable comparison, the stacker is equal on raw J (0.743 against 0.743), better on Jw (0.758 against 0.749), and about 70× faster on CPU.
- The Go-trivial single linear model (50k columns) trails C by 0.049 raw / 0.029 Jw on test, and by 0.135 on fresh AIUR.

**LOO against the LLMs.**
- Dev: every A model from `A-ia-wxhmr` up beats all three fair LLM references.
- Test (the honest number):
  - The stacker beats Argon (HF 0.739 against 0.728).
  - It is about equal to Flash (HA 0.737 against 0.742).
  - It trails Haiku by 0.021 (FA 0.736 against 0.757).
  - The linear-char model is 0.003–0.027 below the three references.

## Training-target study (which teacher signal best predicts y3)

All rows share the same logistic model and hyperparameters, and thresholds are tuned for y3 on gold3_dev. The **v1** rows use word + context + meta, where the meta included `units.sender` (the automation identity) and `common_short_text`. Those two are **not export input fields**, so the v1 rows (`A-tgt-*`) are not student-legal. They are valid as a relative comparison: removing those features changed nothing (A-ia-wxm2 = A-tgt-inter_avail, 0.765 against 0.763 dev). The **v2** rows are student-legal: word + context + head-500 + meta2 + rules, top-200k columns.

| target | n_train | dev J | test J / Jw | fresh Jw | AIUR fresh | LOO FA / HA / HF |
|---|---|---|---|---|---|---|
| v1 flash (Flash closure, all rows) | 269,285 | 0.753 | 0.698 / 0.717 | 0.677 | 0.472 | 0.704 / 0.693 / 0.693 |
| v1 flash, masked labels ignored | 269,285 | 0.743 | 0.697 / 0.717 | 0.684 | 0.490 | 0.701 / 0.696 / 0.692 |
| v1 haiku | 132,487 | 0.760 | 0.701 / 0.717 | 0.715 | 0.478 | 0.679 / 0.711 / 0.699 |
| v1 y_hf (Haiku∩Flash) | 132,466 | **0.765** | 0.708 / 0.723 | **0.719** | 0.483 | 0.686 / 0.706 / 0.703 |
| v1 inter_avail (intersection of the teachers present on each row; Flash-only rows → Flash) | 269,306 | 0.763 | **0.711 / 0.732** | 0.692 | 0.479 | **0.715** / 0.710 / 0.708 |
| v1 soft (votes / n_teachers) | 269,306 | 0.760 | 0.701 / 0.723 | 0.678 | 0.460 | 0.708 / 0.700 / 0.697 |
| v1 soft, rows weighted by n_teachers | 269,306 | **0.765** | 0.703 / 0.725 | 0.690 | 0.468 | 0.710 / 0.703 / 0.698 |
| v1 union | 269,306 | 0.750 | 0.698 / 0.721 | 0.682 | 0.492 | 0.705 / 0.700 / 0.695 |
| v1 per-teacher models averaged (flash + haiku) | | 0.763 | 0.699 / 0.718 | | | 0.704 / 0.707 / 0.698 |
| v1 per-teacher models averaged (+ argon) | | 0.758 | 0.691 / 0.704 | | | 0.692 / 0.697 / 0.687 |
| v1 argon only | 10,478 | 0.718 | 0.655 / 0.653 | 0.611 | 0.419 | 0.667 / 0.664 / 0.651 |
| v1 y3 only | 10,478 | 0.748 | 0.684 / 0.684 | 0.658 | 0.450 | 0.670 / 0.684 / 0.680 |
| v2 flash | 269,285 | 0.756 | 0.715 / 0.731 | 0.694 | 0.547 | 0.712 / 0.705 / 0.711 |
| v2 y_hf | 132,466 | 0.778 | **0.729 / 0.741** | **0.725** | 0.542 | 0.704 / 0.723 / 0.724 |
| v2 inter_avail | 269,306 | 0.774 | 0.721 / 0.738 | 0.686 | 0.544 | 0.716 / 0.715 / 0.718 |
| v2 inter_avail, 1-teacher rows ×0.5 | 269,306 | **0.779** | **0.729** / 0.738 | 0.712 | **0.551** | 0.710 / 0.722 / 0.724 |
| v2 soft, n_teachers-weighted | 269,306 | 0.773 | 0.724 / 0.736 | 0.684 | 0.540 | 0.717 / 0.717 / 0.718 |

Conclusions:
- **Consensus targets win.** y_hf, inter_avail (+ down-weighting single-teacher rows) and soft_nw are within noise of each other. All beat Flash-only by about +0.02 dev and +0.01–0.015 test.
- Union, masking, plain soft targets and averaging per-teacher models are no better than Flash-only.
- Argon-only and y3-only (10k rows) are clearly worst: 0.03–0.05 below. Data volume dominates label purity, and intersecting teachers is the cheap way to get both.
- y_hf reaches the same quality on half the rows, so the Flash-only rows add little.
- The stacker uses inter_avail, flash and soft_nw OOF outputs together: about +0.005 test over a single target, about 0 on dev.

## What else was tried

| experiment | result |
|---|---|
| Learning rate / epochs / L2 sweep (Adam, batch 2048) | lr 0.1, 3 epochs, L2 3e-6 best on dev; no L2 overfits (−0.03) |
| + char_wb 2–5 (head 1,200 + tail 600 chars) | +0.007 test J; ×2.3 train time, ×2 inference cost, 13.8 GB peak |
| + head-500-char word block (instruction part) and the 34 rules as features, added together | +0.009 test, **+0.06 fresh AIUR** (0.485 → 0.544); not ablated separately |
| Column cap by train df (200k / 50k columns) | no loss at 200k (−0.000), −0.003 at 50k; train 120 s → 36 s / 18 s |
| Template down-weighting (1/sqrt(count) of a 150-char prefix key) | +0.02 fresh AIUR, −0.005 elsewhere; 1/count hurts everywhere. Fresh AIUR is a coverage problem (new templates), not memorization |
| No cohort one-hot | −0.009 test raw, −0.03 fresh AIUR |
| Cohort-specific models (4 models) | = the shared model (0.722 against 0.720) |
| One hidden layer (h=64, sparse→64→54) | dev 0.778 at best against 0.775 linear; overfits after 1 epoch. Not pursued |
| LightGBM stacker over OOF probs (3 family folds) | +0.011–0.016 test J over its base; biggest gains in AIUR test/fresh and human short texts |
| Hierarchy (dev) | predicting every node + per-label thresholds + closing the binary set is best (0.775). close_probs is neutral (0.775); noisy-or parents 0.771; **leaves-only + closure 0.742** (broad parent-only labels matter) |
| Thresholds (dev) | per_label_jaccard 0.775 > per_label_f1 0.761 > global 0.756 |
| Empty sets | thresholds already predict 12.5% empty (target 16.5%); forcing a top-1 label when empty: −0.01 to −0.04 |
| Hand rules alone | 0.44 test, below the cohort prior: not worth anything alone. As features they are cheap and kept (see the head-500 row) |

## Error analysis (dev unless noted)

- **Weak labels** (F1, dev):
  - P.frame.criteria 0.32 (mostly FN);
  - C.input broad parent 0.40;
  - P.request.input.information 0.55;
  - C.feedback.challenge 0.56 (FP: "still/wrong" in neutral text);
  - revise 0.63, intent 0.65, scope 0.68, product 0.68 (FN);
  - progress 0.72 (FP 57).

  Strong labels:
  - roots and P.request.work: 0.90–0.95;
  - plan 0.86, implement 0.80, constraint 0.79.
- **Boundary pairs:**
  - verification ↔ finding is the main confusion: 17 of 31 verification FPs are true findings.
  - intent ↔ plan: 4 of 11 intent FNs were predicted as plan.
  - answer ↔ finding, progress ↔ product and investigate ↔ information are minor.
- **By length:**
  - Long agent replies (>2k chars) score 0.67 against 0.79 for short ones.
  - Human 100–2,000 chars: 0.63–0.68.
  - Short human replies (<30 chars) are easy: 0.90.
  - Items where a teacher asked for more context: 0.76 against 0.80 (C 0.79). Human need_context items: 0.66 (C 0.73).
- **Delegation:** the gap is in delegation *results*: test 0.55 against C's 0.73. The model over-predicts C / C.report on structured results (43 of 120 test items). *Corrected by the follow-up below:* this is label noise in the training target, not a feature limit. inter_avail falls back to Flash on Flash-only rows, and Flash says C.report on 52% of multi-teacher delegation rows against 33% for the consensus. Training on Haiku∩Flash rows only, or on C pseudo-labels, lifts delegation from 0.61 to 0.71–0.74.
- **Fresh AIUR** (the shift cell): the student predicts 2.0 labels per item and 56% empty, against targets of 4.2 labels and 27% empty. It misses the P.request / P.frame labels of new orchestration prompts and over-predicts C.report on prompts that quote reports (consistent with D-shift). The head-500 block and the stacker recover part of it (0.48 → 0.59–0.61); no lexical trick closes it.

## Cascade (cost/quality)

The student labels everything; the most uncertain items (sum over labels of min(p, 1−p)) go to Haiku. Scored fairly against F∩A, the LOO-FA comparison on which Haiku alone scores 0.757. Fractions are of the population, cohort-weighted. Source: `logs/cascade_fa.txt`.

| student | defer to Haiku | test J / Jw vs F∩A | fresh | fresh AIUR | Haiku cost for 400k |
|---|---|---|---|---|---|
| A-stack-multi-char | 0% | 0.736 / 0.753 | 0.675 | 0.499 | $0 |
| A-stack-multi-char | 5% | 0.751 / 0.759 | 0.697 | 0.541 | ~$18 |
| A-stack-multi-char | 12% | **0.763 / 0.765** (≥ Haiku alone) | 0.717 | 0.589 | ~$42 |
| A-stack-multi-char | 21% | 0.767 / 0.767 | 0.727 | 0.612 | ~$74 |
| A-final-linear-50k | 10% | 0.751 / 0.749 | 0.705 | 0.555 | ~$35 |
| Haiku alone | 100% | 0.757 | | | ~$352 |

Uncertainty-based deferral works much better than deferring all AIUR items (0.756 / 0.763 for 7.8% of the population).

## Cost and portability

- **Features**:
  - scikit-learn HashingVectorizer (murmur3, 2^21 / 2^19 buckets), then log1p TF × train IDF, then L2 normalization per block;
  - 33 meta features from export input fields;
  - 34 regexes.

  Fitting is train-only: IDF and column pruning in `featurize.py`, regexes in `rules.py`. Training is a torch sparse multi-label logistic regression (`lin.py`, masked/weighted BCE, Adam), 12 CPU threads.
- **Inference**: 93% of the time is featurization. Python numbers were measured end to end from raw text (`bench.py`, 24k items at the population mix, 1,257 chars/item on average) while other tracks loaded the machine at ~20/28 cores:

  | model | 1 core | 12 processes | 400k items |
  |---|---|---|---|
  | linear-50k / 200k | 2.4k/s | 22.7k/s | 2.8 min on one core, 18 s on 12 |
  | linear-char | 1.1k/s | 10.9k/s | 6 min / 37 s |
  | stacker (estimated) | ~0.75k/s | ~7–8k/s | ~9 min / ~1 min |

  The stacker estimate breaks down into char featurization, 9 linear fold models (cheap), and 47 LightGBM label models with 6,251 trees in total at ~5k items/s per core. A Go implementation should be at least as fast. C int8 on CPU runs at ~100/s (about 1.1 h for 400k).
- **Training**:
  - linear-200k: 36 s;
  - linear-char: 4.5 min;
  - stacker: ~16 min end to end (three OOF runs: char 10 min, 2 × 100 s; LightGBM 3 min).
- **Peak memory**:
  - linear: 5–8 GB;
  - char: 13.8 GB;
  - char OOF: **16.2 GB, over the 14 GB budget** for part of its 10-minute run. It can stay under budget if folds release their matrices.
- **Go port**:
  - **Linear (easy).** Needs murmur3_32 hashing, a regex tokenizer with Unicode `\w`/`\b` and Python `str.lower` parity (RE2 `\b` is ASCII-only, so a hand tokenizer is needed), the char_wb analyzer's word padding, an IDF/keep table, and a dense float32 or int8 weight matrix: 11 MB (50k), 43 MB (200k) or 660 MB (char, prunable). Ship a parity fixture as the docs require. Estimate: 1–2 days.
  - **Regex rules (easy).** No lookarounds; same `\b` caveat.
  - **Stacker (moderate).** LightGBM text model, inferred with `leaves` or a ~200-line tree walker. The SVD feature needs a 64 × 1.3M projection matrix (330 MB); the no-SVD variant loses 0.002 and drops it.

## Recommendations

1. **Default production student:**
   - The Go-native tier is `A-final-linear-200k` (or 50k): word + context + head-500 + meta2 + rules logistic, trained on a consensus target (y_hf or inter_avail with single-teacher rows ×0.5). It reaches test 0.72–0.73 / Jw 0.74, trains in under 1 minute, and labels 400k items in under 3 minutes on one core.
   - If an extra ~0.02 is worth a LightGBM dependency, use `A-stack-multi-char-nosvd` (0.741 / 0.757, beats Argon on LOO).
2. **Train on teacher intersections, not on Flash alone, Argon or y3.** More Haiku∩Flash data would probably help more than more Argon.
3. **Cascade:** send the ~10% most uncertain items to Haiku (~$40 for the 400k backfill). That beats Haiku alone against F∩A and lifts fresh AIUR by about 0.09. Retrain on new automation templates regularly; AIUR quality is a coverage problem.
4. **For the director's ensembles:** dev and test probabilities of every run are saved. `A-stack-multi-char` and `A-final-linear-char` are the natural inputs. They are strong where C is relatively weak (agent_reply 0.778 against 0.769, common short texts 0.865 against 0.754) and weak where C is strong (delegation, AIUR), so averaging with C should help.

## Open questions / caveats

- gold3_dev is small and shares the train window, so it cannot see the fresh AIUR shift. Selection was made on dev; test and fresh were only read through the harness. Some late ablations (dedup, no-cohort) were decided in advance and reported whatever their outcome.
- Thresholds tuned on gold3_dev (1,333 items) are noisy. Tuning on a bigger dev proxy (gold2_dev with a y3 offset) is untested.
- Delegation results lag (C.report over-prediction on structured results; see error analysis). Fixes are untested.
- The `A-tgt-*` rows used non-student meta (`units.sender`, `common_short_text`); see the training-target section.
- No packages were installed. Scripts: `featurize.py`, `lin.py`, `exp.py`, `oof.py`, `stack.py`, `rules.py`, `rules_eval.py`, `combine.py`, `hier.py`, `cascade.py`, `bench.py`, `report.py`, `finalrows.py` (all in this directory).

## Follow-up: distillation (23:00–23:20 MDT)

Question: can the Go-native tier close part of its gap to C by distilling from C?

**Method**
- I ran C's `c-e32-512-all` (`models/c-e32-512-all/final.pt`, read-only) with C's own `common.py` encoder on all 269,306 train ids: MPS bf16, 16k-token batches, 340 s at 792 items/s while C was training on the GPU. Script: `c_infer.py`. Output: `probs/c_all_train_probs.npz`.
- I checked it first on gold3_dev: it reproduces C's saved probabilities exactly (max |Δ| = 0).
- **These are in-sample predictions.** C trained one epoch on these same items with all three teachers. The distilled students also see Argon indirectly, so LOO is not clean (that already held partly for inter_avail, which uses Argon on its 10k three-teacher rows).

Targets (students: the 200k-column linear model; the no-SVD stacker over OOF linear outputs):
- `csoft`: C's y3 scorer, the mean of its y3 and y_hf heads, as a soft target.
- `cmix`: 0.5 × inter_avail + 0.5 × csoft.
- `cpl`: the consensus where at least two teachers labelled the row; on the 51% of train rows that only Flash labelled (Haiku never did), Flash ∧ C's Haiku head (≥ 0.5, closed under the hierarchy).
- `cpl_soft`: the same, with Flash × C-Haiku probability on those rows.

Stackers use their target for the GBDT (cross-entropy for soft targets), with OOF from the target's own linear model added to the earlier OOF inputs (char inter_avail, flash, soft_nw). Early stopping is on gold2_dev inter_avail. Inference cost is unchanged.

**Results** (gold3_test; per-cohort numbers in `logs/distill_table.md` and the harness rows `A-dist-*`):

| run | J / Jw | split=test Jw | fresh J / Jw | fresh AIUR | delegation | LOO FA / HA / HF | dev J |
|---|---|---|---|---|---|---|---|
| A-final-linear-200k (inter_avail, baseline) | 0.721 / 0.738 | 0.776 | 0.680 / 0.686 | 0.544 | 0.614 | 0.716 / 0.715 / 0.718 | 0.774 |
| A-t2-y_hf-200k (Haiku∩Flash rows only, no C) | 0.729 / 0.741 | 0.773 | 0.689 / 0.725 | 0.542 | 0.742 | 0.704 / 0.723 / 0.724 | 0.778 |
| A-dist-csoft-200k | 0.729 / 0.748 | 0.781 | 0.685 / 0.736 | 0.499 | 0.719 | 0.708 / 0.727 / 0.724 | 0.784 |
| A-dist-cmix-200k | 0.729 / 0.742 | 0.780 | 0.685 / 0.716 | 0.529 | 0.689 | 0.717 / 0.726 / 0.723 | 0.781 |
| **A-dist-cpl-200k** | **0.734 / 0.749** | 0.781 | 0.694 / 0.733 | 0.525 | 0.713 | 0.714 / 0.729 / 0.730 | 0.788 |
| A-dist-cpl_soft-200k | 0.732 / 0.743 | 0.771 | 0.695 / 0.732 | 0.531 | 0.736 | 0.712 / 0.728 / 0.725 | 0.782 |
| A-stack-multi-char-nosvd (no C) | 0.741 / 0.757 | 0.791 | 0.704 / 0.709 | **0.613** | 0.621 | 0.730 / 0.734 / 0.738 | 0.781 |
| **A-dist-stack-cpl-nosvd** | **0.744 / 0.756** | 0.784 | 0.707 / **0.745** | 0.578 | 0.723 | 0.729 / 0.745 / 0.744 | 0.792 |
| A-dist-stack-csoft-nosvd | 0.741 / 0.753 | 0.783 | 0.704 / 0.741 | 0.552 | 0.706 | 0.720 / 0.743 / 0.739 | 0.797 |
| C c-e32-512-all (teacher) | 0.774 / 0.776 | 0.797 | 0.753 / 0.767 | 0.675 | 0.742 | 0.757 / 0.773 / 0.770 | 0.814 |

Per cohort, test raw J (agent_reply / AIUR / delegation / human):

| run | agent_reply | AIUR | delegation | human |
|---|---|---|---|---|
| linear-200k | 0.759 | 0.599 | 0.614 | 0.754 |
| dist-cpl-200k | 0.766 | 0.587 | 0.713 | 0.767 |
| stack-nosvd | 0.776 | 0.662 | 0.621 | 0.762 |
| dist-stack-cpl | 0.770 | 0.635 | 0.723 | 0.765 |
| C-all | 0.782 | 0.716 | 0.742 | 0.800 |

**Findings**
1. **Linear tier: distillation closes about a quarter to a third of the gap to C.** `cpl` gains +0.013 raw / +0.011 Jw over A-final-linear-200k. The gap to C-all shrinks from 0.053 / 0.038 to 0.040 / 0.027. The largest gains are on fresh Jw (+0.047) and delegation (+0.10).
2. **Most of that gain does not need C.** Haiku∩Flash-only training (`A-t2-y_hf-200k`) already gets delegation to 0.742 and fresh Jw to 0.725. The mechanism is replacing Flash-only labels, which over-label C.report on delegation results, with consensus-like labels. C's pseudo-labels on the Flash-only rows add only +0.005 raw / +0.008 Jw over y_hf, because they keep the extra 137k rows usable. That difference is within noise on gold3 (SE ~0.005–0.01).
3. **Distillation hurts fresh AIUR**: −0.02 for the linear model and −0.035 for the stacker. Soft C targets hurt most (csoft 0.499). C's in-sample train probabilities carry its train-template fit, which the linear student copies, while C's own generalization to new templates (0.675) does not transfer through lexical features.
4. **Stacker: no net gain on the headline.** 0.744 / 0.756 against 0.741 / 0.757. It trades fresh AIUR (−0.035) and agent_reply (−0.006) for delegation (+0.10) and fresh Jw (+0.036). The gap to C-all stays 0.030 / 0.020.
5. **Dev overstates distillation** (+0.011–0.016 dev against +0.000–0.013 test), presumably because C's train fit resembles dev, which shares the train window.
6. **Soft targets are no better than hard ones.** csoft ≈ cmix ≈ cpl_soft ≤ cpl, so hard pseudo-labels were the best way to use C.

**Recommendation**
- **For a single Go-native linear model, use the `cpl` target**: consensus where available, Flash ∧ C-Haiku elsewhere (0.734 / 0.749, fresh Jw 0.733).
- **Without C, train on Haiku∩Flash rows only** (0.729 / 0.741), which gets nearly the same result.
- **For the stacker,** pick by what matters:
  - fresh AIUR and agent_reply: keep the non-distilled no-SVD stacker;
  - delegation and fresh Jw: use `A-dist-stack-cpl-nosvd`.

  An ensemble of the two is the obvious next step. I did not try it, to avoid tuning on test.
- **Distillation does not reach C on new automation templates.** That still needs either C itself (C at ~1k items/s on MPS takes ~6 min for 400k) or the Haiku cascade.

Cost: one C inference pass over train (~6 min on MPS) plus the usual student training (~1 min linear; ~5 min OOF + 3 min stacker). Peak memory ≤ 7.1 GB for every follow-up run.
