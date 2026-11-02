> Sanitized copy of ~/pharos-labeling/research/tracks/E-ensemble/REPORT.md (archived in full on euclid at "Pharos Labeling/2026-10-28-student-research"). Verbatim conversation text was removed.

# Track E: ensembles, calibration and LLM-fallback cascades

Every number comes from the shared harness: `rlh.evaluate` (track `E-ensemble`, rows `E-final-*`), or `rlh.score`
and its helpers applied to the same closed predictions for cells the harness does not stratify (fresh AIUR,
cascades, lenient credit). gold3_test has n = 2,056 and a J SE of about 0.006. Paired ΔJ SEs are given where they
matter, and family-cluster bootstrap SEs (942 families) match the item-level ones. Tuning used gold3_dev and
gold2_dev only. The work ran on CPU with ≤ 5 processes.

The final re-run (00:45) used the newest runs. Excluded: every C `adapt` run, A `A-tgt-*` and rules-only, and B's
cross-track `Bblend*`. The auto-picked components were:
- C: `c-e68-512-all` (new best C: 0.782 / 0.787) and `c-e68-512-100k`;
- A: `A-dist-stack-csoft-nosvd` (distilled from C), with `A-stack-multi-char` as the best A not distilled from C;
- B: `Bst-base32-ft-3bag-svd-seed1` (static), with `B-gte-full` as the best encoder-embedding B.

"C-all" below means the director's reference `c-e32-512-all` (0.774 / 0.776).

## Answers

### 1. Ensembles: +0.01 over C-all, but only ≈ +0.005 over the newest single C

The gain comes from blending two fine-tuned encoders. A and B add nothing reliable on top.

**Blends over the earlier C-all (22:50 pass).** C-all .5 + C-100k .125 + A-stack .25 + B-gte .125 scored 0.788 / 0.786:
- ΔJ +0.014 ± 0.003 raw and +0.010 ± 0.004 weighted;
- fresh AIUR 0.701 (C-all 0.675).

**Over the newest single C** (`c-e68-512-all`, 0.782 / 0.787):
- **c-e68-all .9 + c-e68-100k .1 (C+C2) is 0.788 / 0.792**:
  - ΔJ +0.006 ± 0.002 raw and +0.005 ± 0.002 weighted over c-e68-all;
  - split=test weighted 0.803, split=fresh weighted 0.794, fresh AIUR 0.690;
  - LOO F∩A / H∩A / H∩F 0.780 / 0.786 / 0.785.
- **The dev-selected best blend is no better than its C.** This is C+C2 + A-stack-multi .125 + B-gte-full .25, the highest
  2-fold-CV J on gold2_dev at 0.8131 vs 0.8104 for C+C2. It scores 0.783 / 0.787:
  - ΔJ +0.001 ± 0.004 over c-e68-all;
  - fresh AIUR 0.629, which is −0.042 ± 0.017.
- **Blends with A-dist add nothing (ΔJ ≤ +0.003).** A-dist is distilled from C, so it brings no diversity.
- **Other combinations add nothing either:**
  - a per-label logistic stacker over the model logits: 0.774 vs C-all 0.774;
  - per-cohort threshold offsets: CV +0.000;
  - mixing C's 5 heads: CV +0.003.
- **Weights are not reproducible on 1.3k dev items.** Two weight-selection protocols picked different weights with
  gains of the same size. Treat "+0.005–0.01, mostly from a second encoder" as the result, not any particular weight
  vector.

**The CPU-only, Go-trivial tier gains nothing from blending.**
- **The final tier is 0.745 / 0.758**: A-dist-stack .7 + B-static-svd .3, with fresh AIUR 0.552. A alone scores
  0.741 / 0.753.
- **The earlier tier was flat in the weighted score**: A-stack .7 + potion .3 scored 0.745 / 0.754, vs A-stack alone
  0.738 / 0.756.
- **The tier trails C-all by 0.03 raw and 0.02 weighted, and by 0.12 on fresh AIUR.**

Run costs are in the table under Tables §1. The best blend takes ~87 MPS-min plus ~7 CPU-min per 400k items; C+C2
takes ~31 MPS-min (two ettin-68m checkpoints).

### 2. Confidence: a plain label-free entropy score is enough

The score is the negative summed binary entropy of the closed probabilities. For the final best blend:
- **Deciles:** per-item J vs y3 rises from 0.54 in the least-confident decile to 0.98 in the most confident;
  Spearman ρ = 0.50.
- **Other scores are no better:**

| score | Spearman ρ |
|---|---|
| ridge / GBM on 10 confidence features, fitted on gold3_dev | 0.49 / 0.51 |
| min margin | 0.47 |
| C-vs-A/B disagreement (Jaccard of outputs) | 0.42 |
| mean margin | 0.41 |
| template novelty | 0.37 |

- **A learned router does not beat entropy.** It predicts J_student − J_LLM on dev.
- **Uncertain items are fresh-AIUR novel templates.** The bottom decile is 40 % fresh AIUR and 96 % novel templates.
- **Calibration:**
  - C-all's cell probabilities are calibrated: pooled ECE 0.002.
  - Probability blends over-predict (ECE 0.008; mean p 0.079 vs a positive rate of 0.070). Isotonic regression fitted on
    gold3_dev brings this to 0.0025, but thresholds absorb the shift, so J is unchanged.
  - The CPU tier under-predicts on fresh AIUR (mean p 0.053 vs a rate of 0.078).

### 3. Cascades: the student alone already ties Haiku on Haiku's fair LOO score; routing 10 % to Haiku (~$35 per 400k) clearly beats Haiku-alone ($352)

**Without any routing**, compared with each LLM on its own fair score:
- **The best blend ties or beats Haiku:** 0.764 / 0.776 vs F∩A, against Haiku-alone's 0.757 / 0.749. That is
  +0.007 ± 0.006 raw and +0.027 ± 0.007 weighted.
- **C+C2 is clearly ahead of Haiku:** 0.780 / 0.787 (+0.023 ± 0.006 raw, +0.038 ± 0.007 weighted).
- **It beats Flash by +0.04:** vs H∩A, 0.779–0.786 against Flash-alone's 0.742.
- **The exception is fresh AIUR.** Against F∩A the students score 0.54–0.64 there, vs Haiku's 0.674.

**Haiku fallback, routing by entropy** (k = share of the population routed; cost per 400k items):

| k | best blend vs F∩A (raw / weighted) | gain over Haiku-alone | fresh AIUR | cost |
|---|---|---|---|---|
| 5 % | 0.777 / 0.782 | | | $17 |
| **10 %** | **0.786 / 0.786** | **+0.029 ± 0.005 / +0.037 ± 0.007** | 0.650 | **$35** |
| 20 % | 0.788 / 0.788 | | | $70 |
| 100 % (pure Haiku) | 0.757 / 0.749 | 0 | 0.674 | $352 |

- **C+C2 + 10 % Haiku scores 0.790 / 0.791.**
- **Replacing the student with Haiku stops helping past ~20–30 %:** 0.782 at 50 %, falling to 0.757 at 100 %.
- **Merging instead of replacing keeps improving.** On routed items, keep a Haiku label if the student's logit is
  within 1.5 below its threshold, and add student labels 0.5–1 above it (2 offsets tuned on gold3_dev). This reaches
  0.790 / 0.793 at 30 % and 0.794 / 0.800 at 100 %.
- **Flash as fallback** reaches 0.794 / 0.793 at 10 % ($60), vs Flash-alone's 0.742 ($600).
- **Argon as fallback never helps:** at most 0.785, for $259 at 10 %.
- **What gets routed (10 %):** 6 % of agent_reply, 43 % of AIUR, 5 % of delegation and 29 % of human items, and 54 %
  of fresh AIUR.

**Routing by rule vs by confidence.** The rule routes every AIUR item whose 200-char normalized prefix is unseen in
train: 6.2 % of the population (~25k items, $22 with Haiku).

| policy (Haiku fallback) | J raw / weighted | fresh AIUR | cost per 400k |
|---|---|---|---|
| rule | 0.780 / 0.784 | **0.681** (above Haiku-alone's 0.674) | $22 |
| entropy, same budget | 0.781 / 0.784 | 0.631 | $22 |
| **rule + entropy top-up to 10 %** | **0.786 / 0.786** | **0.681** | **$35** |
| same, with C+C2 as student | 0.791 / 0.792 | | $35 |

- **Overall J is the same either way**, but the rule does better on fresh AIUR: rule − entropy = +0.050 ± 0.019 on
  that cell.
- **The rule + entropy top-up is the recommended policy.**

**The CPU-only tier with Haiku needs more routing:**

| Haiku routed | CPU tier vs F∩A (raw / weighted) | vs Haiku-alone | cost per 400k |
|---|---|---|---|
| 0 % | 0.727 / 0.745 | below | $0 |
| 10 % | 0.755 / 0.758 | tie | $35 |
| 30 % | 0.772 / 0.769 | +0.015 ± 0.005 raw, +0.020 ± 0.006 weighted | $106 |
| rule + entropy top-up to 20 % | | | $70 |

**A LOO-clean check confirms the gains.** `c-e32-512-100k-noH` was trained without any Haiku labels.
- **Alone:** 0.755 / 0.763 vs F∩A.
- **With Haiku:** 0.779 / 0.774 at 10 % and 0.783 / 0.776 at 20 %.
- **So the cascade gains do not depend on the student having seen Haiku's labels.**

### 4. Per label: the student is reliably better on the C-subtree and worse on rare or shifting P-labels

These are F1 scores for the final best blend on gold3_test, over the 31 labels with ≥ 20 y3 positives. Student vs
Haiku is scored against F∩A, student vs Flash against H∩A.

**Where the student is worse:**

| label | student vs F∩A | Haiku vs F∩A |
|---|---|---|
| P.frame.criteria | 0.00 (never predicted at the dev-tuned threshold; C-all had 0.22) | 0.46 |
| P.request.work.revise | 0.54 | 0.80 |
| P.request.work.verify | 0.51 | 0.71 |
| P.frame.constraint | 0.72 | 0.86 |
| C.input | 0.50 | 0.62 |
| C.feedback.challenge | 0.49 | 0.60 |
| P.control.continue | 0.70 | 0.80 |
| P.request.input.information | 0.67 | 0.75 (Flash 0.79) |

**Where the student is better** (Δ vs Haiku / Δ vs Flash):
- C.deliberation.plan: +0.05 / +0.10
- C.report.finding: +0.04 / +0.07
- C.feedback.accept: +0.03 / +0.15
- C.feedback: +0.02 / +0.09
- C.report.limitation vs Haiku: +0.10

These are the C-subtree labels where single LLMs over-label.

**Never trust from the student:**
- **criteria, revise and verify.** Their recall vs y3 is 0.00 / 0.42 / 0.41, and their gold2_test F1 is 0.58 / 0.38 /
  0.36. revise is a fresh-AIUR shift label.
- **C.input.**
- **Every label with < 20 gold3 positives whose gold2_test F1 is ≤ 0.3:**
  - P.request.input: permission, clarification, guidance;
  - P.control: redirect, pause, cancel;
  - C.input: option, permission*;
  - C.feedback: qualify, reject;
  - C.deliberation: evidence, reconsider;
  - C.difficulty: ambiguity, inconsistency.

Treat these as LLM-only, or force routing when the student is near their thresholds.

### 5. Lenient credit and abstention-aware targets (director 23:35; Tables §5)

**Lenient credit** is J = |P∩U| / |P∪I| for each teacher pair, where I is the pair's intersection and U its union.

- **With thresholds tuned for y3, the audit's claim reproduces: the LLMs gain more.**

| pair | best blend | the excluded LLM |
|---|---|---|
| F∩A | 0.819 / 0.843 | Haiku 0.862 / 0.865 |
| H∩A | 0.837 / 0.851 | Flash 0.866 / 0.865 |
| H∩F | 0.856 / 0.873 | Argon 0.884 / 0.891 |

- **That comparison is not apples to apples.** Lenient credit pays for extra labels: predicting U scores 1. A student
  tuned for the strict intersection is handicapped by its own thresholds.
- **With one global logit offset tuned on gold3_dev for the lenient metric**, the best blend beats or ties every LLM
  overall. The offset is −0.75 to −1.5.

| pair | best blend (offset-tuned) | the excluded LLM | fresh AIUR: blend vs LLM |
|---|---|---|---|
| F∩A | **0.875 / 0.895** | Haiku 0.862 / 0.865 | 0.757 vs 0.793 |
| H∩A | **0.884 / 0.899** | Flash 0.866 / 0.865 | 0.779 vs 0.824 |
| H∩F | **0.886 / 0.899** | Argon 0.884 / 0.891 | 0.816 vs 0.818 |

- **C-all with the same offset** scores 0.854 / 0.877 vs Haiku and 0.869 / 0.885 vs both Flash and Argon. Even
  offset-tuned, the students trail on fresh AIUR by 0.00–0.05.

**Abstention-aware LOO target.** A label is positive if ≥ 1 teacher of the pair asserts it and the other teacher
asserts it or abstains (uncertain or unsupported).

| pair | best blend | the excluded LLM |
|---|---|---|
| F∩A | 0.759 / 0.776 | Haiku 0.772 / 0.768 |
| H∩A | 0.769 / 0.778 | Flash 0.767 / 0.767 |
| H∩F | 0.784 / 0.788 | Argon 0.772 / 0.772 |

- **The 10 % Haiku cascade scores 0.787 / 0.789**, so it stays clearly above Haiku.

**Abstention-aware y3** counts a label as positive if every non-abstaining teacher asserts it and at least two do. It
has 4.27 labels per item vs 3.88 for strict y3.
- **Students:** best blend 0.802 / 0.809 (strict 0.783 / 0.787); C-all 0.788 / 0.796.
- **LLMs:** Haiku 0.837, Flash 0.828, Argon 0.795. These are not LOO: each LLM is in its own target.
- **The split=test vs fresh breakdown** is in Tables §5.
- **Conclusion:** "the student matches Haiku" survives every scoring variant overall. It does not survive in fresh
  AIUR, where Haiku stays ahead by 0.03–0.13 under every variant.

## What was done

- **Inputs**: saved probabilities (`tracks/*/preds/<run>.npz`) of every C/A/B run; components picked automatically by
  gold3_dev J per track (C1, C2 = two best C runs; A1 = best A; Alin = best non-stacked linear A; B1 = best B; Bst =
  best static B). Runs using non-student-legal inputs (`A-tgt-*`, rules-only) excluded.
- **Blends** (`scripts/pipeline.py`): weighted mean of probabilities or of logits, weights on a simplex grid (step 0.1
  for 2 models, 0.125 for 3–4), chosen by 2-fold CV J on gold2_dev (17.9k items, y_hf; thresholds tuned on one half,
  scored on the other) and reported with the 5-fold CV J on gold3_dev; then per-label-Jaccard thresholds on each dev
  set (gold3_dev for gold3, gold2_dev for gold2, flash_dev for flash where all components have it) and `rlh.evaluate`.
  Also tried: weights by 5-fold CV on gold3_dev (`explore.py`), a per-label logistic stacker over the models' logits
  (pooled model for labels with < 30 dev positives; 5-fold OOF on gold3_dev), per-cohort logit offsets.
- **Confidence** (`scripts/conf.py`): from the closed probabilities P and thresholds t, with d = |logit P − logit t|:
  mean d, min d, −Σ binary entropy; template novelty (D's `p200` unseen in train); disagreement with a second model
  (Jaccard of binary outputs, L1 of probabilities); and a ridge / small GBM predicting per-item J from those features +
  cohort + length, fitted on gold3_dev.
- **Cascades**: rank gold3_test items by confidence (label-free, so this is what deployment would do over the 400k),
  route the least confident until their **cohort-weighted share reaches k %** (so k is the population fraction and
  cost = k × 400k × price), replace (or merge) the student's labels by the fallback teacher's closure, and score
  against the intersection of the other two teachers. Haiku $0.00088, Flash $0.0015, Argon $0.0065 per item; the student
  is counted as free. ΔJ vs LLM-alone is paired per item.
- **Per label** (`scripts/perlabel.py`): F1 of the student vs y3, vs F∩A (beside Haiku's), vs H∩A (beside Flash's);
  labels with < 20 y3 positives also get the student's gold2_test F1 vs y_hf (≥ 10× the positives) as a reliability hint.

## Tables

### 1. Ensembles (gold3_test unless noted)

LLM fair LOO references on gold3_test: Haiku vs F∩A 0.757 (weighted 0.749), Flash vs H∩A 0.742 (0.746), Argon vs H∩F 0.728 (0.723). Other tried variants (gold3_test raw / weighted): per-label logistic stacker over C, A-stack, Bst logits 0.774 / 0.776 (ΔJ −0.000 ± 0.004 vs C); per-cohort logit offsets: CV gain 0.000; C head mixing: CV +0.003. Rows marked g2 = weights by 2-fold CV on gold2_dev (primary); g3 = 5-fold CV on gold3_dev (earlier pass, components of that time).

| combo | weights chosen by | components (weights, space) | gold3_dev CV J | gold3_test J raw ± se / weighted | ΔJ vs c-e32-512-all (paired, raw / weighted) | split=test w | split=fresh w | fresh AIUR (y3 / vs F∩A) | LOO FA / HA / HF | macro-F1 | gold2_test J |
|---|---|---|---|---|---|---|---|---|---|---|---|
| C | - | c-e68-512-all 1.00 (prob) | 0.803 | 0.782 ± 0.006 / 0.787 | +0.008 ± 0.004 / +0.011 ± 0.005 | 0.798 | 0.791 | 0.671 / 0.614 | 0.774 / 0.781 / 0.779 | 0.756 | 0.793 |
| C2 | - | c-e68-512-100k 1.00 (prob) | 0.794 | 0.771 ± 0.006 / 0.774 | -0.003 ± 0.004 / -0.001 ± 0.005 | 0.795 | 0.771 | 0.633 / 0.598 | 0.763 / 0.771 / 0.766 | 0.744 | 0.775 |
| C+C2 | g2 | c-e68-512-all 0.90, c-e68-512-100k 0.10 (prob) | 0.801 | 0.788 ± 0.006 / 0.792 | +0.014 ± 0.004 / +0.016 ± 0.005 | 0.803 | 0.794 | 0.690 / 0.642 | 0.780 / 0.786 / 0.785 | 0.767 | 0.793 |
| C+A | g2 | c-e68-512-all 0.80, A-dist-stack-csoft-nosvd 0.20 (logit) | 0.810 | 0.782 ± 0.006 / 0.784 | +0.008 ± 0.004 / +0.008 ± 0.005 | 0.793 | 0.789 | 0.677 / 0.603 | 0.771 / 0.785 / 0.780 | 0.761 | 0.792 |
| C+A+B | g2 | c-e68-512-all 0.62, A-dist-stack-csoft-nosvd 0.12, Bst-base32-ft-3bag-svd-seed1 0.25 (prob) | 0.806 | 0.783 ± 0.006 / 0.788 | +0.009 ± 0.004 / +0.012 ± 0.005 | 0.802 | 0.789 | 0.670 / 0.571 | 0.766 / 0.783 / 0.780 | 0.759 | 0.793 |
| C+C2+A+B | g2 | c-e68-512-all 0.62, c-e68-512-100k 0.12, A-dist-stack-csoft-nosvd 0.12, Bst-base32-ft-3bag-svd-seed1 0.12 (prob) | 0.807 | 0.785 ± 0.006 / 0.785 | +0.011 ± 0.004 / +0.010 ± 0.005 | 0.797 | 0.789 | 0.683 / 0.600 | 0.775 / 0.789 / 0.784 | 0.765 | 0.794 |
| C+A+Bst | g2 | c-e68-512-all 0.62, A-dist-stack-csoft-nosvd 0.12, Bst-base32-ft-3bag-svd-seed1 0.25 (prob) | 0.806 | 0.783 ± 0.006 / 0.788 | +0.009 ± 0.004 / +0.012 ± 0.005 | 0.802 | 0.789 | 0.670 / 0.571 | 0.766 / 0.783 / 0.780 | 0.759 | 0.793 |
| C+C2+A | g2 | c-e68-512-all 0.75, c-e68-512-100k 0.12, A-dist-stack-csoft-nosvd 0.12 (prob) | 0.804 | 0.786 ± 0.006 / 0.787 | +0.011 ± 0.004 / +0.012 ± 0.005 | 0.801 | 0.786 | 0.678 / 0.597 | 0.771 / 0.785 / 0.782 | 0.762 | 0.793 |
| C+C2+Andist+Bemb | g2 | c-e68-512-all 0.50, c-e68-512-100k 0.12, A-stack-multi-char 0.12, B-gte-full-mlp2x2048 0.25 (prob) | 0.810 | 0.783 ± 0.006 / 0.787 | +0.009 ± 0.004 / +0.012 ± 0.005 | 0.807 | 0.785 | 0.629 / 0.542 | 0.763 / 0.779 / 0.776 | 0.749 | 0.793 |
| cpu:Alin+Bst | g2 | A-dist-cpl-200k 0.40, Bst-base32-ft-3bag-svd-seed1 0.60 (logit) | 0.777 | 0.740 ± 0.007 / 0.753 | -0.034 ± 0.006 / -0.022 ± 0.006 | 0.779 | 0.747 | 0.567 / 0.471 | 0.720 / 0.737 / 0.735 | 0.691 | 0.758 |
| cpu:A+Bst | g2 | A-dist-stack-csoft-nosvd 0.70, Bst-base32-ft-3bag-svd-seed1 0.30 (prob) | 0.788 | 0.745 ± 0.007 / 0.758 | -0.029 ± 0.005 / -0.018 ± 0.005 | 0.783 | 0.751 | 0.552 / 0.452 | 0.727 / 0.747 / 0.742 | 0.702 | 0.759 |
| cpu:A+Alin+Bst | g2 | A-dist-stack-csoft-nosvd 0.25, A-dist-cpl-200k 0.38, Bst-base32-ft-3bag-svd-seed1 0.38 (prob) | 0.779 | 0.747 ± 0.007 / 0.759 | -0.027 ± 0.005 / -0.017 ± 0.006 | 0.782 | 0.754 | 0.555 / 0.456 | 0.722 / 0.741 / 0.741 | 0.689 | 0.760 |
| A | - | A-dist-stack-csoft-nosvd 1.00 (prob) | 0.779 | 0.741 ± 0.007 / 0.753 | -0.034 ± 0.005 / -0.022 ± 0.005 | 0.783 | 0.741 | 0.552 / 0.455 | 0.720 / 0.743 / 0.739 | 0.688 | 0.754 |
| Alin | - | A-dist-cpl-200k 1.00 (prob) | 0.769 | 0.735 ± 0.007 / 0.749 | -0.040 ± 0.006 / -0.027 ± 0.006 | 0.781 | 0.733 | 0.525 / 0.457 | 0.714 / 0.729 / 0.730 | 0.665 | 0.748 |
| B | - | Bst-base32-ft-3bag-svd-seed1 1.00 (prob) | 0.761 | 0.736 ± 0.007 / 0.748 | -0.041 ± 0.006 / -0.033 ± 0.006 | 0.778 | 0.737 | 0.549 / 0.435 | 0.713 / 0.730 / 0.730 | 0.682 | 0.748 |
| Bst | - | Bst-base32-ft-3bag-svd-seed1 1.00 (prob) | 0.761 | 0.736 ± 0.007 / 0.748 | -0.041 ± 0.006 / -0.033 ± 0.006 | 0.778 | 0.737 | 0.549 / 0.435 | 0.713 / 0.730 / 0.730 | 0.682 | 0.748 |

Run cost per blend:

| combo | models: device, min per 400k items | total min (MPS / CPU) |
|---|---|---|
| C (-) | c-e68-512-all: MPS, 15.3 min | 15 / 0 |
| C2 (-) | c-e68-512-100k: MPS, 15.5 min | 15 / 0 |
| C+C2 (g2) | c-e68-512-all: MPS, 15.3 min; c-e68-512-100k: MPS, 15.5 min | 31 / 0 |
| C+A (g2) | c-e68-512-all: MPS, 15.3 min; A-dist-stack-csoft-nosvd: CPU 1 core*, 7.4 min | 15 / 7 |
| C+A+B (g2) | c-e68-512-all: MPS, 15.3 min; A-dist-stack-csoft-nosvd: CPU 1 core*, 7.4 min; Bst-base32-ft-3bag-svd-seed1: CPU 8 threads, 0.4 min | 15 / 8 |
| C+C2+A+B (g2) | c-e68-512-all: MPS, 15.3 min; c-e68-512-100k: MPS, 15.5 min; A-dist-stack-csoft-nosvd: CPU 1 core*, 7.4 min; Bst-base32-ft-3bag-svd-seed1: CPU 8 threads, 0.4 min | 31 / 8 |
| C+A+Bst (g2) | c-e68-512-all: MPS, 15.3 min; A-dist-stack-csoft-nosvd: CPU 1 core*, 7.4 min; Bst-base32-ft-3bag-svd-seed1: CPU 8 threads, 0.4 min | 15 / 8 |
| C+C2+A (g2) | c-e68-512-all: MPS, 15.3 min; c-e68-512-100k: MPS, 15.5 min; A-dist-stack-csoft-nosvd: CPU 1 core*, 7.4 min | 31 / 7 |
| C+C2+Andist+Bemb (g2) | c-e68-512-all: MPS, 15.3 min; c-e68-512-100k: MPS, 15.5 min; A-stack-multi-char: CPU 1 core*, 7.4 min; B-gte-full-mlp2x2048: MPS, 56.5 min | 87 / 7 |
| cpu:Alin+Bst (g2) | A-dist-cpl-200k: CPU 1 core*, 2.8 min; Bst-base32-ft-3bag-svd-seed1: CPU 8 threads, 0.4 min | 0 / 3 |
| cpu:A+Bst (g2) | A-dist-stack-csoft-nosvd: CPU 1 core*, 7.4 min; Bst-base32-ft-3bag-svd-seed1: CPU 8 threads, 0.4 min | 0 / 8 |
| cpu:A+Alin+Bst (g2) | A-dist-stack-csoft-nosvd: CPU 1 core*, 7.4 min; A-dist-cpl-200k: CPU 1 core*, 2.8 min; Bst-base32-ft-3bag-svd-seed1: CPU 8 threads, 0.4 min | 0 / 11 |
| A (-) | A-dist-stack-csoft-nosvd: CPU 1 core*, 7.4 min | 0 / 7 |
| Alin (-) | A-dist-cpl-200k: CPU 1 core*, 2.8 min | 0 / 3 |
| B (-) | Bst-base32-ft-3bag-svd-seed1: CPU 8 threads, 0.4 min | 0 / 0 |
| Bst (-) | Bst-base32-ft-3bag-svd-seed1: CPU 8 threads, 0.4 min | 0 / 0 |

C encoders on CPU via ONNX fp32 run ~55–80 items/s (8–16 threads; track C), i.e. ~85–120 min per 400k for ettin-32m. MPS minutes are uncontended measurements by each track; * Track A throughput not on its row; taken from the matching A-final-* row (stack 900 items/s, char-linear 1.1k/s, word-linear 2.4k/s per core). 'CPU 1 core' times parallelize ~10× over 12 processes.

### 2. Confidence deciles

**Best student: student `E-final-C-C2-Andist-Bemb`, disagreement partner `E-final-cpu-A-Bst`**

| decile (1 = least confident) | mean_margin | min_margin | neg_entropy | not_novel | agree_jacc | neg_prob_l1 | learned_ridge | learned_gbm | AIUR share* | fresh-AIUR share* | novel share* |
|---|---|---|---|---|---|---|---|---|---|---|---|
| 1 | 0.567 | 0.641 | 0.535 | 0.577 | 0.508 | 0.566 | 0.488 | 0.507 | 0.41 | 0.40 | 0.96 |
| 2 | 0.627 | 0.660 | 0.622 | 0.628 | 0.731 | 0.657 | 0.657 | 0.587 | 0.29 | 0.29 | 0.97 |
| 3 | 0.728 | 0.656 | 0.740 | 0.734 | 0.777 | 0.773 | 0.686 | 0.711 | 0.22 | 0.20 | 0.90 |
| 4 | 0.785 | 0.744 | 0.743 | 0.785 | 0.757 | 0.763 | 0.758 | 0.783 | 0.07 | 0.05 | 0.87 |
| 5 | 0.791 | 0.702 | 0.758 | 0.804 | 0.679 | 0.752 | 0.793 | 0.783 | 0.11 | 0.10 | 0.92 |
| 6 | 0.835 | 0.818 | 0.793 | 0.856 | 0.815 | 0.758 | 0.816 | 0.817 | 0.04 | 0.03 | 0.95 |
| 7 | 0.850 | 0.823 | 0.836 | 0.812 | 0.875 | 0.810 | 0.833 | 0.846 | 0.05 | 0.00 | 0.96 |
| 8 | 0.825 | 0.876 | 0.895 | 0.861 | 0.838 | 0.861 | 0.881 | 0.873 | 0.11 | 0.04 | 0.87 |
| 9 | 0.873 | 0.916 | 0.926 | 0.937 | 0.898 | 0.924 | 0.934 | 0.932 | 0.09 | 0.05 | 0.88 |
| 10 | 0.950 | 0.994 | 0.983 | 0.837 | 0.953 | 0.967 | 0.986 | 0.993 | 0.13 | 0.04 | 0.97 |
| Spearman ρ (score, item J) | 0.406 | 0.466 | 0.499 | 0.373 | 0.418 | 0.431 | 0.494 | 0.508 | | | |

\* composition columns are for the neg_entropy deciles. Item J is against y3 on gold3_test (n=2,056, ~206 per decile).

### 3. Cascades

Student `E-final-C-C2-Andist-Bemb`, routed by neg_entropy (least confident first); k = share of the population (cohort-weighted) sent to the LLM.
Cells: J raw / J cohort-weighted [fresh AIUR]; scored vs the other two teachers' intersection.

| k % | routed items (raw) · AR / AIUR / DEL / HUM · fresh AIUR | Haiku fallback (vs F∩A) | Δ vs Haiku-alone (raw, w) | $ /400k | Haiku merge (vs F∩A) | Flash fallback (vs H∩A) | $ /400k | Argon fallback (vs H∩F) | $ /400k |
|---|---|---|---|---|---|---|---|---|---|
| 0 | 0.00 · 0.00 / 0.00 / 0.00 / 0.00 · 0.00 | 0.764 / 0.776 [0.542] | +0.007 ± 0.006, +0.027 ± 0.007 | 0 | 0.764 / 0.776 [0.542] | 0.779 / 0.789 [0.604] | 0 | 0.776 / 0.780 [0.625] | 0 |
| 5 | 0.09 · 0.03 / 0.26 / 0.02 / 0.14 · 0.32 | 0.777 / 0.782 [0.610] | +0.020 ± 0.006, +0.033 ± 0.007 | 17 | 0.776 / 0.782 [0.609] | 0.788 / 0.792 [0.644] | 30 | 0.779 / 0.781 [0.632] | 128 |
| 10 | 0.19 · 0.06 / 0.43 / 0.05 / 0.29 · 0.54 | 0.786 / 0.786 [0.650] | +0.029 ± 0.005, +0.037 ± 0.007 | 35 | 0.785 / 0.786 [0.649] | 0.794 / 0.793 [0.679] | 60 | 0.785 / 0.783 [0.672] | 259 |
| 20 | 0.30 · 0.16 / 0.61 / 0.10 / 0.41 · 0.73 | 0.788 / 0.788 [0.669] | +0.031 ± 0.005, +0.039 ± 0.006 | 70 | 0.789 / 0.791 [0.656] | 0.796 / 0.793 [0.695] | 120 | 0.781 / 0.781 [0.660] | 519 |
| 30 | 0.40 · 0.25 / 0.65 / 0.33 / 0.50 · 0.77 | 0.787 / 0.786 [0.676] | +0.030 ± 0.004, +0.037 ± 0.006 | 105 | 0.790 / 0.793 [0.656] | 0.786 / 0.783 [0.685] | 180 | 0.768 / 0.769 [0.654] | 779 |
| 50 | 0.55 · 0.47 / 0.75 / 0.52 / 0.59 · 0.88 | 0.782 / 0.778 [0.674] | +0.025 ± 0.003, +0.029 ± 0.004 | 176 | 0.793 / 0.797 [0.657] | 0.776 / 0.773 [0.666] | 300 | 0.761 / 0.758 [0.650] | 1300 |
| 100 | 1.00 · 1.00 / 1.00 / 1.00 / 1.00 · 1.00 | 0.757 / 0.749 [0.674] | +0.000 ± 0.000, +0.000 ± 0.000 | 352 | 0.794 / 0.800 [0.657] | 0.742 / 0.746 [0.634] | 600 | 0.728 / 0.723 [0.598] | 2600 |

CPU-only tier (Go-native story):


| k % | routed items (raw) · AR / AIUR / DEL / HUM · fresh AIUR | Haiku fallback (vs F∩A) | Δ vs Haiku-alone (raw, w) | $ /400k | Haiku merge (vs F∩A) | Flash fallback (vs H∩A) | $ /400k | Argon fallback (vs H∩F) | $ /400k |
|---|---|---|---|---|---|---|---|---|---|
| 0 | 0.00 · 0.00 / 0.00 / 0.00 / 0.00 · 0.00 | 0.727 / 0.745 [0.452] | -0.030 ± 0.007, -0.003 ± 0.007 | 0 | 0.727 / 0.745 [0.452] | 0.747 / 0.766 [0.528] | 0 | 0.742 / 0.754 [0.552] | 0 |
| 5 | 0.10 · 0.04 / 0.10 / 0.02 / 0.23 · 0.12 | 0.742 / 0.751 [0.480] | -0.015 ± 0.006, +0.003 ± 0.007 | 17 | 0.734 / 0.749 [0.480] | 0.761 / 0.771 [0.556] | 30 | 0.749 / 0.757 [0.560] | 129 |
| 10 | 0.19 · 0.07 / 0.24 / 0.04 / 0.38 · 0.30 | 0.755 / 0.758 [0.539] | -0.002 ± 0.006, +0.009 ± 0.007 | 35 | 0.743 / 0.753 [0.522] | 0.769 / 0.772 [0.600] | 60 | 0.758 / 0.761 [0.593] | 258 |
| 20 | 0.32 · 0.16 / 0.47 / 0.11 / 0.53 · 0.56 | 0.766 / 0.765 [0.592] | +0.009 ± 0.005, +0.017 ± 0.007 | 70 | 0.751 / 0.759 [0.564] | 0.776 / 0.774 [0.637] | 120 | 0.764 / 0.764 [0.618] | 520 |
| 30 | 0.41 · 0.26 / 0.58 / 0.18 / 0.62 · 0.70 | 0.772 / 0.769 [0.638] | +0.015 ± 0.005, +0.020 ± 0.006 | 106 | 0.759 / 0.766 [0.601] | 0.782 / 0.779 [0.670] | 180 | 0.771 / 0.769 [0.657] | 780 |
| 50 | 0.57 · 0.47 / 0.68 / 0.53 / 0.67 · 0.79 | 0.769 / 0.766 [0.643] | +0.012 ± 0.004, +0.017 ± 0.005 | 176 | 0.762 / 0.770 [0.603] | 0.771 / 0.771 [0.648] | 300 | 0.756 / 0.755 [0.632] | 1300 |
| 100 | 1.00 · 1.00 / 1.00 / 1.00 / 1.00 · 1.00 | 0.757 / 0.749 [0.674] | +0.000 ± 0.000, +0.000 ± 0.000 | 352 | 0.766 / 0.777 [0.603] | 0.742 / 0.746 [0.634] | 600 | 0.728 / 0.723 [0.598] | 2600 |

Simplest strong student C+C2 (E-final-C-C2):


| k % | routed items (raw) · AR / AIUR / DEL / HUM · fresh AIUR | Haiku fallback (vs F∩A) | Δ vs Haiku-alone (raw, w) | $ /400k | Haiku merge (vs F∩A) | Flash fallback (vs H∩A) | $ /400k | Argon fallback (vs H∩F) | $ /400k |
|---|---|---|---|---|---|---|---|---|---|
| 0 | 0.00 · 0.00 / 0.00 / 0.00 / 0.00 · 0.00 | 0.780 / 0.787 [0.642] | +0.023 ± 0.006, +0.038 ± 0.007 | 0 | 0.780 / 0.787 [0.642] | 0.786 / 0.795 [0.667] | 0 | 0.785 / 0.787 [0.695] | 0 |
| 5 | 0.10 · 0.03 / 0.22 / 0.02 / 0.17 · 0.28 | 0.786 / 0.790 [0.647] | +0.030 ± 0.006, +0.041 ± 0.007 | 18 | 0.787 / 0.790 [0.664] | 0.792 / 0.797 [0.677] | 30 | 0.785 / 0.787 [0.673] | 130 |
| 10 | 0.18 · 0.06 / 0.41 / 0.04 / 0.29 · 0.50 | 0.790 / 0.791 [0.671] | +0.033 ± 0.005, +0.043 ± 0.007 | 35 | 0.788 / 0.791 [0.670] | 0.795 / 0.797 [0.690] | 60 | 0.783 / 0.786 [0.666] | 259 |
| 20 | 0.29 · 0.16 / 0.51 / 0.22 / 0.39 · 0.62 | 0.790 / 0.791 [0.669] | +0.033 ± 0.004, +0.042 ± 0.006 | 70 | 0.791 / 0.794 [0.672] | 0.792 / 0.792 [0.686] | 120 | 0.776 / 0.778 [0.661] | 520 |
| 30 | 0.38 · 0.26 / 0.56 / 0.35 / 0.47 · 0.67 | 0.787 / 0.787 [0.669] | +0.030 ± 0.004, +0.038 ± 0.006 | 105 | 0.792 / 0.794 [0.674] | 0.785 / 0.785 [0.684] | 180 | 0.767 / 0.768 [0.654] | 778 |
| 50 | 0.55 · 0.47 / 0.71 / 0.58 / 0.57 · 0.83 | 0.779 / 0.777 [0.673] | +0.022 ± 0.003, +0.028 ± 0.004 | 176 | 0.795 / 0.799 [0.675] | 0.779 / 0.779 [0.680] | 300 | 0.761 / 0.761 [0.657] | 1299 |
| 100 | 1.00 · 1.00 / 1.00 / 1.00 / 1.00 · 1.00 | 0.757 / 0.749 [0.674] | +0.000 ± 0.000, +0.000 ± 0.000 | 352 | 0.795 / 0.799 [0.676] | 0.742 / 0.746 [0.634] | 600 | 0.728 / 0.723 [0.598] | 2600 |

LOO-clean student (c-e32-512-100k-noH, trained without any Haiku labels; Haiku fallback is the relevant column):


| k % | routed items (raw) · AR / AIUR / DEL / HUM · fresh AIUR | Haiku fallback (vs F∩A) | Δ vs Haiku-alone (raw, w) | $ /400k | Haiku merge (vs F∩A) | Flash fallback (vs H∩A) | $ /400k | Argon fallback (vs H∩F) | $ /400k |
|---|---|---|---|---|---|---|---|---|---|
| 0 | 0.00 · 0.00 / 0.00 / 0.00 / 0.00 · 0.00 | 0.755 / 0.763 [0.599] | -0.002 ± 0.007, +0.014 ± 0.008 | 0 | 0.755 / 0.763 [0.599] | 0.744 / 0.762 [0.586] | 0 | 0.729 / 0.735 [0.606] | 0 |
| 5 | 0.10 · 0.03 / 0.24 / 0.01 / 0.17 · 0.29 | 0.767 / 0.767 [0.626] | +0.010 ± 0.006, +0.018 ± 0.007 | 18 | 0.769 / 0.768 [0.646] | 0.756 / 0.766 [0.625] | 30 | 0.738 / 0.739 [0.624] | 130 |
| 10 | 0.20 · 0.06 / 0.43 / 0.03 / 0.36 · 0.54 | 0.779 / 0.774 [0.662] | +0.022 ± 0.006, +0.025 ± 0.007 | 35 | 0.778 / 0.772 [0.671] | 0.766 / 0.771 [0.656] | 60 | 0.745 / 0.743 [0.647] | 260 |
| 20 | 0.31 · 0.16 / 0.58 / 0.06 / 0.49 · 0.70 | 0.783 / 0.776 [0.668] | +0.026 ± 0.005, +0.027 ± 0.007 | 70 | 0.782 / 0.777 [0.680] | 0.772 / 0.778 [0.660] | 120 | 0.749 / 0.748 [0.642] | 519 |
| 30 | 0.40 · 0.27 / 0.63 / 0.13 / 0.56 · 0.76 | 0.785 / 0.779 [0.674] | +0.028 ± 0.005, +0.030 ± 0.006 | 106 | 0.785 / 0.779 [0.682] | 0.774 / 0.779 [0.666] | 180 | 0.754 / 0.754 [0.650] | 779 |
| 50 | 0.55 · 0.48 / 0.73 / 0.30 / 0.64 · 0.87 | 0.783 / 0.779 [0.676] | +0.026 ± 0.004, +0.030 ± 0.005 | 176 | 0.789 / 0.786 [0.681] | 0.770 / 0.774 [0.656] | 300 | 0.753 / 0.755 [0.644] | 1300 |
| 100 | 1.00 · 1.00 / 1.00 / 1.00 / 1.00 · 1.00 | 0.757 / 0.749 [0.674] | +0.000 ± 0.000, +0.000 ± 0.000 | 352 | 0.791 / 0.789 [0.681] | 0.742 / 0.746 [0.634] | 600 | 0.728 / 0.723 [0.598] | 2600 |

Rule vs confidence routing (best student):

| policy | J raw | J weighted | fresh AIUR | ΔJ vs LLM-alone raw / weighted | routed (pop.) | routed raw | $ per 400k |
|---|---|---|---|---|---|---|---|
| haiku · aiur_novel | 0.780 | 0.784 | 0.681 | +0.023 / +0.035 | 0.062 | 0.12 | 22 |
| haiku · aiur_all | 0.778 | 0.783 | 0.674 | +0.021 / +0.034 | 0.078 | 0.15 | 27 |
| haiku · aiur_novel+gbm10 | 0.782 | 0.787 | 0.681 | +0.025 / +0.038 | 0.100 | 0.16 | 35 |
| haiku · aiur_novel+entropy10 | 0.786 | 0.786 | 0.681 | +0.029 / +0.038 | 0.100 | 0.19 | 35 |
| haiku · aiur_novel+gbm20 | 0.788 | 0.791 | 0.684 | +0.031 / +0.042 | 0.199 | 0.27 | 70 |
| haiku · aiur_novel+entropy20 | 0.789 | 0.789 | 0.677 | +0.032 / +0.040 | 0.200 | 0.32 | 70 |
| haiku · aiur_novel+gbm30 | 0.789 | 0.793 | 0.683 | +0.032 / +0.044 | 0.299 | 0.37 | 105 |
| haiku · aiur_novel+entropy30 | 0.788 | 0.786 | 0.677 | +0.032 / +0.037 | 0.300 | 0.41 | 106 |
| haiku · aiur_novel · merge | 0.777 | 0.783 | 0.660 | +0.020 / +0.034 | 0.062 | 0.12 | 22 |
| haiku · neg_entropy@rule_budget | 0.781 | 0.784 | 0.631 | +0.024 / +0.035 | 0.062 | 0.12 | 22 |
| haiku · not_novel@rule_budget | 0.777 | 0.782 | 0.603 | +0.020 / +0.033 | 0.062 | 0.12 | 22 |
| haiku · learned_gbm@rule_budget | 0.773 | 0.783 | 0.593 | +0.016 / +0.034 | 0.062 | 0.08 | 22 |
| haiku · mean_margin@rule_budget | 0.777 | 0.782 | 0.603 | +0.020 / +0.033 | 0.062 | 0.12 | 22 |
| flash · aiur_novel | 0.781 | 0.790 | 0.649 | +0.039 / +0.043 | 0.062 | 0.12 | 37 |
| flash · aiur_all | 0.778 | 0.788 | 0.634 | +0.036 / +0.042 | 0.078 | 0.15 | 47 |
| flash · aiur_novel+gbm10 | 0.783 | 0.794 | 0.645 | +0.041 / +0.047 | 0.100 | 0.16 | 60 |
| flash · aiur_novel+entropy10 | 0.786 | 0.791 | 0.646 | +0.044 / +0.044 | 0.100 | 0.19 | 60 |

Rule vs confidence routing (CPU-only tier):

| policy | J raw | J weighted | fresh AIUR | ΔJ vs LLM-alone raw / weighted | routed (pop.) | routed raw | $ per 400k |
|---|---|---|---|---|---|---|---|
| haiku · aiur_novel | 0.753 | 0.759 | 0.672 | -0.004 / +0.010 | 0.062 | 0.12 | 22 |
| haiku · aiur_all | 0.752 | 0.759 | 0.674 | -0.005 / +0.010 | 0.078 | 0.15 | 27 |
| haiku · aiur_novel+gbm10 | 0.760 | 0.762 | 0.677 | +0.003 / +0.013 | 0.099 | 0.17 | 35 |
| haiku · aiur_novel+entropy10 | 0.761 | 0.762 | 0.672 | +0.004 / +0.013 | 0.100 | 0.19 | 35 |
| haiku · aiur_novel+gbm20 | 0.770 | 0.767 | 0.675 | +0.013 / +0.019 | 0.200 | 0.29 | 70 |
| haiku · aiur_novel+entropy20 | 0.776 | 0.770 | 0.684 | +0.019 / +0.021 | 0.200 | 0.35 | 70 |
| haiku · aiur_novel+gbm30 | 0.772 | 0.769 | 0.676 | +0.015 / +0.020 | 0.299 | 0.40 | 105 |
| haiku · aiur_novel+entropy30 | 0.776 | 0.770 | 0.680 | +0.019 / +0.022 | 0.299 | 0.44 | 105 |
| haiku · aiur_novel · merge | 0.745 | 0.755 | 0.604 | -0.012 / +0.006 | 0.062 | 0.12 | 22 |
| haiku · neg_entropy@rule_budget | 0.745 | 0.753 | 0.493 | -0.012 / +0.004 | 0.062 | 0.13 | 22 |
| haiku · not_novel@rule_budget | 0.743 | 0.752 | 0.479 | -0.014 / +0.003 | 0.062 | 0.12 | 22 |
| haiku · learned_gbm@rule_budget | 0.742 | 0.754 | 0.515 | -0.015 / +0.005 | 0.062 | 0.09 | 22 |
| haiku · mean_margin@rule_budget | 0.743 | 0.752 | 0.481 | -0.014 / +0.003 | 0.062 | 0.11 | 22 |
| flash · aiur_novel | 0.757 | 0.771 | 0.633 | +0.015 / +0.025 | 0.062 | 0.12 | 37 |
| flash · aiur_all | 0.756 | 0.771 | 0.634 | +0.014 / +0.025 | 0.078 | 0.15 | 47 |
| flash · aiur_novel+gbm10 | 0.762 | 0.771 | 0.639 | +0.020 / +0.025 | 0.099 | 0.17 | 60 |
| flash · aiur_novel+entropy10 | 0.764 | 0.774 | 0.633 | +0.022 / +0.027 | 0.100 | 0.19 | 60 |

### 4. Per-label F1 (best student; ≥ 20 y3 positives on gold3_test; sorted by Δ vs Haiku)

| label | y3 pos | student F1 vs y3 (P / R) | vs F∩A: student / Haiku (Δ) | vs H∩A: student / Flash (Δ) | gold2_test F1 vs y_hf (pos) |
|---|---|---|---|---|---|
| P.frame.criteria | 45 | 0.00 (0.00 / 0.00) | 0.00 / 0.46 (-0.46) | 0.00 / 0.48 (-0.48) | 0.58 (672) |
| P.request.work.revise | 112 | 0.56 (0.84 / 0.42) | 0.54 / 0.80 (-0.26) | 0.54 / 0.83 (-0.29) | 0.38 (515) |
| P.request.work.verify | 32 | 0.53 (0.76 / 0.41) | 0.51 / 0.71 (-0.20) | 0.42 / 0.72 (-0.30) | 0.36 (196) |
| P.frame.constraint | 274 | 0.76 (0.93 / 0.64) | 0.72 / 0.86 (-0.13) | 0.75 / 0.83 (-0.08) | 0.84 (2426) |
| C.input | 40 | 0.57 (0.85 / 0.42) | 0.50 / 0.62 (-0.12) | 0.47 / 0.63 (-0.16) | 0.59 (362) |
| C.feedback.challenge | 41 | 0.61 (0.68 / 0.56) | 0.49 / 0.60 (-0.11) | 0.59 / 0.61 (-0.02) | 0.51 (137) |
| P.control.continue | 37 | 0.79 (1.00 / 0.65) | 0.70 / 0.80 (-0.10) | 0.71 / 0.77 (-0.06) | 0.76 (166) |
| P.request.input.information | 47 | 0.66 (0.71 / 0.62) | 0.67 / 0.75 (-0.08) | 0.64 / 0.79 (-0.15) | 0.70 (113) |
| P.control | 44 | 0.72 (1.00 / 0.57) | 0.62 / 0.68 (-0.06) | 0.64 / 0.73 (-0.09) | 0.74 (191) |
| P.frame | 377 | 0.81 (0.87 / 0.76) | 0.79 / 0.85 (-0.06) | 0.81 / 0.82 (-0.01) | 0.86 (2781) |
| P.frame.scope | 194 | 0.66 (0.70 / 0.63) | 0.67 / 0.71 (-0.04) | 0.65 / 0.68 (-0.03) | 0.78 (1485) |
| P.request.work.implement | 275 | 0.79 (0.84 / 0.75) | 0.78 / 0.83 (-0.04) | 0.78 / 0.80 (-0.01) | 0.80 (1518) |
| P.request.input | 72 | 0.79 (0.76 / 0.83) | 0.79 / 0.82 (-0.03) | 0.81 / 0.83 (-0.02) | 0.85 (361) |
| P.request.work.review | 121 | 0.87 (0.81 / 0.94) | 0.86 / 0.89 (-0.03) | 0.87 / 0.90 (-0.03) | 0.83 (599) |
| C.report.intent | 80 | 0.59 (0.86 / 0.45) | 0.52 / 0.54 (-0.02) | 0.46 / 0.58 (-0.12) | 0.66 (2171) |
| P | 885 | 0.96 (0.98 / 0.94) | 0.95 / 0.97 (-0.02) | 0.96 / 0.96 (-0.01) | 0.92 (4172) |
| P.request | 773 | 0.93 (0.92 / 0.95) | 0.93 / 0.94 (-0.02) | 0.95 / 0.94 (+0.01) | 0.90 (3668) |
| P.request.work | 690 | 0.92 (0.90 / 0.94) | 0.91 / 0.93 (-0.02) | 0.93 / 0.92 (+0.00) | 0.89 (3308) |
| C.report.verification | 241 | 0.88 (0.84 / 0.93) | 0.86 / 0.88 (-0.02) | 0.89 / 0.87 (+0.01) | 0.89 (5687) |
| C.report.product | 125 | 0.77 (0.89 / 0.69) | 0.72 / 0.72 (+0.00) | 0.72 / 0.77 (-0.05) | 0.80 (3006) |
| C | 1118 | 0.95 (0.94 / 0.97) | 0.94 / 0.93 (+0.00) | 0.96 / 0.92 (+0.04) | 0.98 (22395) |
| P.request.work.investigate | 105 | 0.79 (0.69 / 0.92) | 0.82 / 0.81 (+0.01) | 0.80 / 0.75 (+0.04) | 0.75 (508) |
| C.report.progress | 278 | 0.84 (0.84 / 0.85) | 0.83 / 0.82 (+0.01) | 0.83 / 0.80 (+0.03) | 0.82 (6488) |
| C.report.completion | 44 | 0.73 (0.69 / 0.77) | 0.72 / 0.70 (+0.01) | 0.73 / 0.73 (-0.00) | 0.71 (1011) |
| C.feedback | 126 | 0.87 (0.86 / 0.87) | 0.79 / 0.78 (+0.02) | 0.85 / 0.75 (+0.09) | 0.72 (504) |
| C.report | 763 | 0.92 (0.89 / 0.95) | 0.91 / 0.89 (+0.02) | 0.92 / 0.88 (+0.04) | 0.94 (17596) |
| C.feedback.accept | 79 | 0.93 (0.90 / 0.95) | 0.88 / 0.84 (+0.03) | 0.93 / 0.78 (+0.15) | 0.79 (327) |
| C.report.finding | 282 | 0.78 (0.68 / 0.92) | 0.82 / 0.77 (+0.04) | 0.83 / 0.76 (+0.07) | 0.79 (6458) |
| C.deliberation | 272 | 0.80 (0.78 / 0.82) | 0.80 / 0.76 (+0.04) | 0.80 / 0.73 (+0.08) | 0.82 (7713) |
| C.deliberation.plan | 257 | 0.81 (0.77 / 0.85) | 0.81 / 0.76 (+0.05) | 0.81 / 0.71 (+0.10) | 0.82 (7331) |
| C.report.limitation | 49 | 0.64 (0.75 / 0.55) | 0.62 / 0.52 (+0.10) | 0.52 / 0.59 (-0.07) | 0.63 (1564) |

Labels with < 20 y3 positives on gold3_test (student F1 on gold2_test vs y_hf, P / R, positives): P.request.input.clarification 0.29 (1.00/0.17, 12); P.request.input.choice 0.72 (0.78/0.67, 96); P.request.input.permission 0.00 (0.00/0.00, 40); P.request.input.guidance 0.00 (0.00/0.00, 10); P.control.redirect 0.10 (1.00/0.05, 19); P.control.pause 0.00 (0.00/0.00, 2); P.control.cancel 0.00 (0.00/0.00, 1); C.input.answer 0.58 (0.67/0.51, 291); C.input.option 0.30 (0.71/0.19, 26); C.input.permission 0.46 (0.89/0.31, 26); C.input.permission.grant 0.47 (0.89/0.32, 25); C.input.permission.deny 0.00 (0.00/0.00, 0); C.input.permission.defer 0.00 (0.00/0.00, 1); C.feedback.qualify 0.11 (0.17/0.09, 23); C.feedback.reject 0.00 (0.00/0.00, 20); C.deliberation.hypothesis 0.67 (0.70/0.65, 313); C.deliberation.evidence 0.08 (0.46/0.04, 142); C.deliberation.reconsider 0.41 (0.72/0.28, 144); C.difficulty 0.55 (0.59/0.51, 261); C.difficulty.ambiguity 0.00 (0.00/0.00, 9); C.difficulty.inconsistency 0.33 (0.64/0.23, 31); C.difficulty.guidance 0.00 (0.00/0.00, 0); C.difficulty.blocker 0.58 (0.59/0.57, 218)
### 5. Lenient credit and abstention-aware targets (director 23:35)

| target | pair | system | all raw / w | split=test raw / w | split=fresh raw / w | fresh AIUR raw |
|---|---|---|---|---|---|---|
| strict LOO | FA | C-all (c-e32-512-all) | 0.757 / 0.767 | 0.789 / 0.804 | 0.719 / 0.722 | 0.598 |
| lenient LOO | FA | C-all (c-e32-512-all) | 0.819 / 0.843 | 0.850 / 0.878 | 0.782 / 0.796 | 0.649 |
| abstain-aware LOO | FA | C-all (c-e32-512-all) | 0.751 / 0.767 | 0.789 / 0.806 | 0.707 / 0.718 | 0.567 |
| strict LOO | FA | best blend | 0.764 / 0.776 | 0.805 / 0.813 | 0.716 / 0.732 | 0.542 |
| lenient LOO | FA | best blend | 0.819 / 0.843 | 0.859 / 0.878 | 0.773 / 0.797 | 0.590 |
| abstain-aware LOO | FA | best blend | 0.759 / 0.776 | 0.806 / 0.814 | 0.705 / 0.727 | 0.509 |
| strict LOO | FA | CPU tier | 0.727 / 0.745 | 0.780 / 0.794 | 0.665 / 0.693 | 0.452 |
| lenient LOO | FA | CPU tier | 0.791 / 0.820 | 0.846 / 0.871 | 0.726 / 0.761 | 0.501 |
| abstain-aware LOO | FA | CPU tier | 0.728 / 0.753 | 0.785 / 0.803 | 0.661 / 0.695 | 0.427 |
| strict LOO | FA | C noH (no Haiku in training) | 0.755 / 0.763 | 0.791 / 0.798 | 0.714 / 0.726 | 0.599 |
| lenient LOO | FA | C noH (no Haiku in training) | 0.842 / 0.860 | 0.874 / 0.891 | 0.805 / 0.838 | 0.694 |
| abstain-aware LOO | FA | C noH (no Haiku in training) | 0.760 / 0.772 | 0.800 / 0.809 | 0.713 / 0.730 | 0.588 |
| strict LOO | FA | haiku alone | 0.757 / 0.749 | 0.760 / 0.758 | 0.753 / 0.739 | 0.674 |
| lenient LOO | FA | haiku alone | 0.862 / 0.865 | 0.860 / 0.866 | 0.864 / 0.862 | 0.793 |
| abstain-aware LOO | FA | haiku alone | 0.772 / 0.768 | 0.777 / 0.776 | 0.766 / 0.756 | 0.672 |
| strict LOO | FA | cascade best + haiku 10% | 0.786 / 0.786 | 0.812 / 0.814 | 0.756 / 0.747 | 0.650 |
| lenient LOO | FA | cascade best + haiku 10% | 0.857 / 0.862 | 0.875 / 0.883 | 0.836 / 0.825 | 0.753 |
| abstain-aware LOO | FA | cascade best + haiku 10% | 0.787 / 0.789 | 0.814 / 0.816 | 0.756 / 0.748 | 0.642 |
| strict LOO | FA | cascade best + haiku 30% | 0.787 / 0.786 | 0.805 / 0.810 | 0.766 / 0.755 | 0.676 |
| lenient LOO | FA | cascade best + haiku 30% | 0.873 / 0.878 | 0.883 / 0.892 | 0.860 / 0.853 | 0.791 |
| abstain-aware LOO | FA | cascade best + haiku 30% | 0.794 / 0.796 | 0.812 / 0.815 | 0.773 / 0.767 | 0.669 |
| strict LOO | HA | C-all (c-e32-512-all) | 0.773 / 0.782 | 0.797 / 0.807 | 0.746 / 0.768 | 0.655 |
| lenient LOO | HA | C-all (c-e32-512-all) | 0.838 / 0.851 | 0.868 / 0.884 | 0.803 / 0.820 | 0.718 |
| abstain-aware LOO | HA | C-all (c-e32-512-all) | 0.766 / 0.777 | 0.798 / 0.812 | 0.728 / 0.743 | 0.625 |
| strict LOO | HA | best blend | 0.779 / 0.789 | 0.806 / 0.812 | 0.746 / 0.781 | 0.604 |
| lenient LOO | HA | best blend | 0.837 / 0.851 | 0.873 / 0.883 | 0.795 / 0.824 | 0.656 |
| abstain-aware LOO | HA | best blend | 0.769 / 0.778 | 0.805 / 0.812 | 0.727 / 0.752 | 0.573 |
| strict LOO | HA | CPU tier | 0.747 / 0.766 | 0.781 / 0.796 | 0.706 / 0.754 | 0.528 |
| lenient LOO | HA | CPU tier | 0.810 / 0.831 | 0.859 / 0.874 | 0.753 / 0.796 | 0.572 |
| abstain-aware LOO | HA | CPU tier | 0.736 / 0.755 | 0.783 / 0.797 | 0.682 / 0.722 | 0.486 |
| strict LOO | HA | C noH (no Haiku in training) | 0.744 / 0.762 | 0.770 / 0.788 | 0.713 / 0.727 | 0.586 |
| lenient LOO | HA | C noH (no Haiku in training) | 0.840 / 0.855 | 0.872 / 0.888 | 0.803 / 0.829 | 0.705 |
| abstain-aware LOO | HA | C noH (no Haiku in training) | 0.753 / 0.768 | 0.779 / 0.797 | 0.722 / 0.726 | 0.611 |
| strict LOO | HA | flash alone | 0.742 / 0.746 | 0.754 / 0.759 | 0.728 / 0.725 | 0.634 |
| lenient LOO | HA | flash alone | 0.866 / 0.865 | 0.877 / 0.877 | 0.853 / 0.848 | 0.824 |
| abstain-aware LOO | HA | flash alone | 0.767 / 0.767 | 0.780 / 0.785 | 0.752 / 0.738 | 0.671 |
| strict LOO | HA | cascade best + flash 10% | 0.794 / 0.793 | 0.811 / 0.812 | 0.775 / 0.788 | 0.679 |
| lenient LOO | HA | cascade best + flash 10% | 0.872 / 0.866 | 0.890 / 0.888 | 0.852 / 0.841 | 0.812 |
| abstain-aware LOO | HA | cascade best + flash 10% | 0.796 / 0.790 | 0.815 / 0.815 | 0.773 / 0.767 | 0.701 |
| strict LOO | HA | cascade best + flash 30% | 0.786 / 0.783 | 0.802 / 0.807 | 0.767 / 0.753 | 0.685 |
| lenient LOO | HA | cascade best + flash 30% | 0.887 / 0.877 | 0.903 / 0.900 | 0.868 / 0.851 | 0.844 |
| abstain-aware LOO | HA | cascade best + flash 30% | 0.798 / 0.791 | 0.814 / 0.818 | 0.779 / 0.756 | 0.714 |
| strict LOO | HF | C-all (c-e32-512-all) | 0.770 / 0.775 | 0.791 / 0.799 | 0.746 / 0.760 | 0.668 |
| lenient LOO | HF | C-all (c-e32-512-all) | 0.854 / 0.871 | 0.882 / 0.899 | 0.821 / 0.843 | 0.756 |
| abstain-aware LOO | HF | C-all (c-e32-512-all) | 0.772 / 0.778 | 0.803 / 0.812 | 0.735 / 0.743 | 0.654 |
| strict LOO | HF | best blend | 0.776 / 0.780 | 0.797 / 0.800 | 0.751 / 0.777 | 0.625 |
| lenient LOO | HF | best blend | 0.856 / 0.873 | 0.888 / 0.898 | 0.818 / 0.851 | 0.703 |
| abstain-aware LOO | HF | best blend | 0.784 / 0.788 | 0.820 / 0.820 | 0.742 / 0.759 | 0.621 |
| strict LOO | HF | CPU tier | 0.742 / 0.754 | 0.774 / 0.784 | 0.704 / 0.743 | 0.552 |
| lenient LOO | HF | CPU tier | 0.826 / 0.849 | 0.872 / 0.887 | 0.772 / 0.818 | 0.617 |
| abstain-aware LOO | HF | CPU tier | 0.745 / 0.760 | 0.793 / 0.802 | 0.689 / 0.723 | 0.518 |
| strict LOO | HF | C noH (no Haiku in training) | 0.729 / 0.735 | 0.751 / 0.762 | 0.703 / 0.699 | 0.606 |
| lenient LOO | HF | C noH (no Haiku in training) | 0.845 / 0.859 | 0.873 / 0.887 | 0.812 / 0.823 | 0.753 |
| abstain-aware LOO | HF | C noH (no Haiku in training) | 0.752 / 0.759 | 0.780 / 0.794 | 0.719 / 0.710 | 0.639 |
| strict LOO | HF | argon alone | 0.728 / 0.723 | 0.740 / 0.737 | 0.713 / 0.705 | 0.598 |
| lenient LOO | HF | argon alone | 0.884 / 0.891 | 0.895 / 0.898 | 0.872 / 0.879 | 0.818 |
| abstain-aware LOO | HF | argon alone | 0.772 / 0.772 | 0.787 / 0.788 | 0.754 / 0.744 | 0.652 |
| strict LOO | HF | cascade best + argon 10% | 0.785 / 0.783 | 0.798 / 0.798 | 0.769 / 0.783 | 0.672 |
| lenient LOO | HF | cascade best + argon 10% | 0.887 / 0.888 | 0.902 / 0.903 | 0.868 / 0.872 | 0.833 |
| abstain-aware LOO | HF | cascade best + argon 10% | 0.802 / 0.798 | 0.826 / 0.823 | 0.775 / 0.773 | 0.701 |
| strict LOO | HF | cascade best + argon 30% | 0.768 / 0.769 | 0.784 / 0.791 | 0.750 / 0.736 | 0.654 |
| lenient LOO | HF | cascade best + argon 30% | 0.897 / 0.902 | 0.912 / 0.918 | 0.880 / 0.876 | 0.846 |
| abstain-aware LOO | HF | cascade best + argon 30% | 0.797 / 0.799 | 0.818 / 0.825 | 0.772 / 0.755 | 0.696 |
| y3 (strict) | - | C-all (c-e32-512-all) | 0.774 / 0.776 | 0.792 / 0.797 | 0.753 / 0.767 | 0.675 |
| y3_abst (3 teachers) | - | C-all (c-e32-512-all) | 0.788 / 0.796 | 0.819 / 0.830 | 0.752 / 0.764 | 0.668 |
| y3 (strict) | - | best blend | 0.783 / 0.787 | 0.806 / 0.807 | 0.757 / 0.785 | 0.629 |
| y3_abst (3 teachers) | - | best blend | 0.802 / 0.809 | 0.838 / 0.841 | 0.759 / 0.782 | 0.626 |
| y3 (strict) | - | CPU tier | 0.745 / 0.758 | 0.775 / 0.783 | 0.710 / 0.752 | 0.552 |
| y3_abst (3 teachers) | - | CPU tier | 0.760 / 0.778 | 0.806 / 0.820 | 0.707 / 0.745 | 0.525 |
| y3 (strict) | - | C noH (no Haiku in training) | 0.731 / 0.738 | 0.753 / 0.761 | 0.705 / 0.707 | 0.599 |
| y3_abst (3 teachers) | - | C noH (no Haiku in training) | 0.764 / 0.774 | 0.791 / 0.806 | 0.731 / 0.726 | 0.641 |
| y3 (strict) | - | haiku (not LOO) | 0.833 / 0.813 | 0.830 / 0.815 | 0.836 / 0.818 | 0.834 |
| y3_abst (3 teachers) | - | haiku (not LOO) | 0.837 / 0.824 | 0.837 / 0.828 | 0.837 / 0.822 | 0.825 |
| y3 (strict) | - | flash (not LOO) | 0.797 / 0.807 | 0.804 / 0.811 | 0.789 / 0.795 | 0.706 |
| y3_abst (3 teachers) | - | flash (not LOO) | 0.828 / 0.834 | 0.838 / 0.848 | 0.815 / 0.810 | 0.732 |
| y3 (strict) | - | argon (not LOO) | 0.745 / 0.739 | 0.756 / 0.752 | 0.732 / 0.723 | 0.614 |
| y3_abst (3 teachers) | - | argon (not LOO) | 0.795 / 0.794 | 0.810 / 0.810 | 0.778 / 0.770 | 0.676 |

y3_abst positives per item 4.27 vs strict y3 3.88; empty 0.076 vs 0.100.

Lenient LOO with the student's thresholds shifted by one global logit offset tuned on gold3_dev for that metric (lenient credit rewards extra labels, so a y3-tuned student is handicapped):

| pair | system | offset | all raw / w | split=test raw / w | split=fresh raw / w | fresh AIUR | LLM alone all [fresh AIUR] |
|---|---|---|---|---|---|---|---|
| FA | C-all (c-e32-512-all) | -1.50 | 0.854 / 0.877 | 0.878 / 0.904 | 0.825 / 0.848 | 0.749 | haiku: 0.862 / 0.865 [0.793] |
| HA | C-all (c-e32-512-all) | -1.00 | 0.869 / 0.885 | 0.890 / 0.909 | 0.844 / 0.860 | 0.796 | flash: 0.866 / 0.865 [0.824] |
| HF | C-all (c-e32-512-all) | -1.00 | 0.869 / 0.885 | 0.894 / 0.911 | 0.841 / 0.856 | 0.803 | argon: 0.884 / 0.891 [0.818] |
| FA | best blend | -1.50 | 0.875 / 0.895 | 0.895 / 0.915 | 0.852 / 0.880 | 0.757 | haiku: 0.862 / 0.865 [0.793] |
| HA | best blend | -1.00 | 0.884 / 0.899 | 0.906 / 0.920 | 0.857 / 0.880 | 0.779 | flash: 0.866 / 0.865 [0.824] |
| HF | best blend | -0.75 | 0.886 / 0.899 | 0.907 / 0.919 | 0.861 / 0.874 | 0.816 | argon: 0.884 / 0.891 [0.818] |
| FA | CPU tier | -1.00 | 0.832 / 0.862 | 0.871 / 0.897 | 0.786 / 0.815 | 0.648 | haiku: 0.862 / 0.865 [0.793] |
| HA | CPU tier | -0.75 | 0.841 / 0.864 | 0.878 / 0.898 | 0.797 / 0.831 | 0.671 | flash: 0.866 / 0.865 [0.824] |
| HF | CPU tier | -0.25 | 0.838 / 0.862 | 0.882 / 0.898 | 0.787 / 0.830 | 0.653 | argon: 0.884 / 0.891 [0.818] |


## Cost and deployment notes

- Per-blend run cost (models, device, minutes per 400k items) is tabled under Tables §1. Final best blend: two ettin-68m
  encoders (~15 MPS-min each; CPU ONNX would be several hours), A-stack (~7 CPU-min on 1 core, parallelizes ~10×),
  B-gte-full (~57 MPS-min) — ~87 MPS-min + 7 CPU-min, and four models to port. C+C2: ~31 MPS-min, one architecture.
  CPU tier (A-dist-stack + static B): ~8 CPU-min on 1 core plus featurization; Go port easy-moderate (linear + LightGBM
  + static embedding bags). Blending itself is a weighted mean (trivial in Go).
- Confidence: −Σ binary entropy of the closed probabilities, trivial in Go. Novelty: D's normalized 200-char prefix
  looked up in a hash set of train prefixes (trivial; refresh with each retrain).
- LLM fallback: Haiku at 10 % of 400k = 40k items ≈ $35 (rule part: ~25k novel AIUR items ≈ $22). Going forward
  (fresh window: 26,110 items in 14 days ≈ 1.9k/day; 240/day novel AIUR) the rule costs ≈ $0.21/day with Haiku.

## Recommendations

1. Ship the best C plus a second C checkpoint (C+C2: +0.005 over the best single C, ~31 MPS-min per 400k; A/B blends add nothing reliable) + **Haiku fallback for every novel-template AIUR item
   plus the least-confident items by entropy up to 10 % of volume** (~$35 per 400k). That beats Haiku-alone on Haiku's
   own fair score by ~+0.02 raw / +0.03 weighted for 1/10 of Haiku's cost, and lifts the fresh-AIUR cell above Haiku's.
2. If paying for an LLM on more items, *merge* rather than replace: student-gated Haiku labels keep improving to ~30 %
   and never fall below the student, while pure replacement peaks at 20–30 % and decays to Haiku-alone.
3. Do not use Argon as a fallback (it is the weakest teacher on its fair score and 7× Haiku's price).
4. Treat P.request.work.revise, P.frame.criteria, P.request.work.verify, C.input and the rare permission/control/
   difficulty leaves as untrusted from the student (LLM-only, or force routing when they are near threshold).
5. The CPU-only static tier (A-dist-stack + static B) is ~0.04 J below the best C and ~0.12 below on fresh AIUR; with
   30 % Haiku routing (~$106) it exceeds Haiku-alone (+0.015 ± 0.005 raw), so it is a viable no-GPU fallback, not a peer of C.

## Caveats and open questions

- Family-cluster bootstrap (942 families, `scripts/boot.py`, final run) gives the same SEs as item-level: best blend −
  c-e68-all +0.001 ± 0.004 (fresh AIUR −0.042 ± 0.017); best blend − Haiku vs F∩A +0.007 ± 0.007 raw / +0.027 ± 0.008
  weighted; 10 % cascade − Haiku +0.029 ± 0.005 / +0.037 ± 0.007; rule − entropy routing on fresh AIUR +0.050 ± 0.019
  (n = 250); CPU tier + 30 % Haiku − Haiku +0.015 ± 0.005 / +0.020 ± 0.006.
- Dev selection among blends of similar CV J is a coin flip on test (dev-picked best 0.783 vs C+C2 0.788); C+C2 is
  reported as the simplest alternative, not as a test-selected winner.
- The fallback-teacher comparisons are against different targets (F∩A for Haiku, H∩A for Flash, H∩F for Argon); compare
  each cascade only to its own LLM-alone row.
- Most students were trained on all three teachers' train-split labels; the LOO-clean `c-e32-512-100k-noH` reproduces the
  Haiku-cascade gains (0.755 → 0.779 at 10 %).
- Weight selection is noisy on 1.3k gold3_dev items; the two protocols picked different weights with the same gain.
  Treat "+0.01" as the ensemble effect, not a particular weight vector.
- Rule routing depends on train coverage of templates; after retraining on fresh data the novel share will drop and
  the rule must be recomputed.

Scripts: `tracks/E-ensemble/scripts/` (`run_final.sh TAG` reruns everything); outputs in `out/` (per-item JSON,
tables), logs in `logs/`. Harness rows: `E-*` on the leaderboard.
