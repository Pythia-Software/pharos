> Sanitized copy of ~/pharos-labeling/studies/DOTTEREL/REPORT.md (archived in full on euclid at "Pharos Labeling/2026-10-29-bird-studies"). Verbatim conversation text was removed.

# DOTTEREL: how good can a pure-Go linear labeler get, especially on H-typed?

Numbers come from harness v2 (AVOCET `rlh2`) unless marked otherwise. Heldout = test + fresh. Data v2 has three teachers on every
human item; on it, H-typed heldout y3 has n = 1,494. "LOO FA / HA / HF" = the student scored against Flash∩Argon / Haiku∩Argon
/ Haiku∩Flash, with the held-out LLM on the same items beside it. CIs are paired bootstraps (2,000 resamples). All selection
used dev (H-typed y3 dev n = 972 on v2) and a human-fold cross-validation on train (below); heldout was read only through the harness.

## Answer in numbers

- **Best Go-native model: `V4-hybmaj`**
  - It is one shared logistic model, 500k features, run by the pure-Go scorer with exact parity.
  - **H-typed (v2 heldout): 0.711 / 0.717 / 0.708** (LOO FA / HA / HF). The held-out LLMs score 0.769 / 0.758 / 0.749.
  - Gaps on H-typed, as Δ on FA [95% CI]:

    | vs | Δ (FA) | other targets |
    |---|---|---|
    | round-1 linear `A-final-linear-200k` (0.673 / 0.679 / 0.676) | **+0.038** [+0.028, +0.048] | HA +0.037, HF +0.033 |
    | ettin-68m encoder `r1-c-e68-512-all`, published thresholds | **−0.025** [−0.036, −0.015] | HA −0.022, HF −0.032 |
    | e68 with per-segment thresholds retuned on v2 (0.748 / 0.753 / 0.750) | −0.036 [−0.047, −0.026] | |
    | ettin-32m encoder (e32) | −0.013 [−0.024, −0.002] | |
    | Haiku (held-out LLM) | −0.057 [−0.070, −0.045] | HA −0.042, HF −0.041 vs Flash / Argon |

    Against the v2-retuned encoder, round 1's −0.064 gap is now −0.036 (V4 − e68-segth; same e68 run in both).
- **Compact variant `V5-hybmaj-150k`:** 0.707 / 0.716 / 0.706 with per-segment thresholds. Its weights are 66 MB fp32 or **33 MB f16**. The Go f16 weights score the same on the harness: 0.706 / 0.716 / 0.706.
- **Cost in Go**, pure Go with no cgo, on a machine at load 18–26 of 28 cores (so these are lower bounds):

  | | 1 goroutine | 4 goroutines |
  |---|---|---|
  | throughput | **4.0k items/s** (G1 at lower load: 4.95k/s) | **17–20k items/s** |
  | 400k items | ~100 s | ~22 s |

  - Model-only RSS is 178–220 MB.
  - Files are 119–161 MB, with load times of 40–80 ms.
  - Training takes 65 s in Python (torch, 8 threads) with an 8.3 GB peak.
  - Label spend was $0. The study used AVOCET's v2 labels.
- **Parity:** Go and Python logits agree within 1.1e-13 on all 95,602 eval items for every exported model (D5-xi, G1, V4, V5), with 0 label flips.

## Results tables

### v2 labels, heldout (`logs/table_v2.md`)

| run | H-typed LOO FA / HA / HF | H+ctx FA | AIUR FA | AR FA | DEL FA | ALL FA | ALL y3 |
|---|---|---|---|---|---|---|---|
| held-out LLM (Haiku / Flash / Argon) | 0.769 / 0.758 / 0.749 | | 0.706 | 0.759 | 0.604 | 0.789 | |
| encoder r1-c-e68-512-all (−segth) | 0.737 / 0.739 / 0.740 (0.748 / 0.753 / 0.750) | 0.723 | 0.667 | 0.799 | 0.710 | 0.786 | 0.791 |
| encoder r1-c-e32-512-all | 0.725 / 0.729 / 0.725 | 0.703 | 0.659 | 0.783 | 0.672 | 0.774 | 0.785 |
| r1-A-stack-multi-char (LightGBM, round 1) | 0.688 / 0.690 / 0.692 | 0.670 | 0.573 | 0.771 | 0.737 | 0.750 | 0.755 |
| r1-A-final-linear-200k (round-1 baseline) | 0.673 / 0.679 / 0.676 | 0.630 | 0.536 | 0.748 | 0.758 | 0.733 | 0.736 |
| V0: round-1 recipe retrained on v2 labels | 0.675 / 0.689 / 0.683 | 0.635 | 0.542 | 0.753 | 0.744 | 0.736 | 0.740 |
| G1 (final features, v1 labels; v2 thresholds) | 0.705 / 0.714 / 0.711 | 0.673 | 0.514 | 0.751 | 0.698 | 0.745 | 0.758 |
| V1: G1 features on v2 labels | 0.706 / 0.718 / 0.714 | 0.666 | 0.543 | 0.770 | 0.748 | 0.755 | 0.761 |
| V2: + human target 2-of-3 majority | 0.712 / 0.718 / 0.709 | 0.681 | 0.497 | 0.754 | 0.696 | 0.749 | 0.758 |
| V3: + c68 pseudo-labels on Flash-only machine rows | 0.708 / 0.723 / 0.720 | 0.666 | 0.538 | 0.776 | 0.725 | 0.755 | 0.766 |
| **V4-hybmaj** (V2 human target + V3 machine target) | **0.711 / 0.717 / 0.708** | 0.676 | 0.499 | 0.770 | 0.720 | 0.753 | 0.759 |
| V4-hybmaj-segth | 0.708 / 0.716 / 0.708 | 0.673 | 0.594 | 0.763 | 0.718 | 0.757 | 0.761 |
| V5-hybmaj-150k-segth | 0.707 / 0.716 / 0.706 | 0.665 | 0.588 | 0.761 | 0.702 | 0.754 | 0.759 |
| V5 f16 weights scored by Go (segth) | 0.706 / 0.716 / 0.706 | 0.664 | 0.588 | 0.761 | 0.702 | 0.754 | 0.759 |

Machine-segment FA cells move by ±0.05 with threshold choice (DEL n=175, AIUR n=313); read them with y_hf (much larger n) in
`logs/qv3.out`.

### v1 labels: the feature and modelling experiments

Round-1 labels; H-typed heldout y3 n=366, y_hf n=741. Full table in `logs/table_v1.md`.

| run | what changed (cumulative unless noted) | H-typed LOO FA / HA / HF | H-typed y_hf |
|---|---|---|---|
| r1-A-final-linear-200k | round-1 baseline (reproduced exactly as D0-base: test 0.7205 / Jw 0.738) | 0.662 / 0.667 / 0.666 | 0.677 |
| D1-iaw | target: consensus with single-teacher rows ×0.5 | 0.667 / 0.672 / 0.669 | 0.679 |
| D4-pos | + segment one-hot + word order (first 6 / last 4 tokens, first/last bigram, length bin, first token × length) | 0.676 / 0.691 / 0.698 | 0.694 |
| D7-all | + context split (user vs assistant), context tail 200, text×context interactions, cue features | 0.675 / 0.693 / 0.691 | 0.693 |
| D8 | + human-only copies of the text/context/head/pos/interaction/tail blocks (feature augmentation) | 0.677 / 0.693 / 0.693 | 0.701 |
| D9 | + human rows ×3 | 0.696 / 0.711 / 0.708 | 0.716 |
| G1 | + char_wb 2–5 on human rows | **0.699 / 0.715 / 0.711** | **0.715** |
| E1 | D4 + char_wb on human rows (no copies or weights) | 0.691 / 0.700 / 0.707 | 0.711 |
| E2 | D4 + frozen potion-base-32M embeddings (text + context) for human rows | 0.692 / 0.697 / 0.695 | 0.701 |
| F1 / W3 | + extended word order (positions 6–9, sentence-initial tokens, last 8 context tokens) | 0.675–0.689 | 0.699–0.706 |
| T1 / T3 | D4 with c68 pseudo-labels / 50% c68-soft mix | 0.663 / 0.670 FA | 0.681 / 0.687 |
| D4-pos-segth | per-segment thresholds (v1 dev, 216 H-typed items) | 0.675 / 0.685 / 0.684 | 0.687 |
| S1 / S2 / S3 | LightGBM (all rows / human rows) or MLP over OOF G1 logits + meta | 0.689 / 0.702 / 0.700 FA | 0.701–0.720 |
| P-HT, P-H, P-M | per-segment models (dev; combined via routing) | dev H-typed 0.647 (alone) / 0.714–0.723 combined vs 0.722 shared | |

**Human-fold cross-validation**, used to select with large n (`scripts/hcv.py`; `logs/hcv.out`):
- Method: 3 family folds over human train rows. Each fold model trains on all other train rows. The score is the out-of-fold Jaccard on H-typed train rows.
- v1 labels, n=3,704 rows with ≥ 2 teachers:

  | D1 | D4 | E1 | D9 | W1 (H×5) | W3 | G1 |
  |---|---|---|---|---|---|---|
  | 0.690 | 0.700 | 0.708 | 0.709 | 0.710 | 0.712 | **0.712** |

- v2 labels, n=7,521, mean J against the three pair intersections: all candidates fall in 0.712–0.718, i.e. a plateau.

  | V7 | V1 | V8 | V4 | V5 | V6 | V2 |
  |---|---|---|---|---|---|---|
  | 0.712 | 0.714 | 0.715 | 0.716 | 0.716 | 0.717 | 0.718 |

## What worked (H-typed)

1. **Word-order features** give the first big step.
   - These are tagged first/last tokens and bigrams, length bins, and first token × length.
   - D4 − baseline: FA +0.014 [−0.001, +0.029], HA +0.024 [+0.007, +0.040], HF +0.031 [+0.016, +0.046].
   - Bags of n-grams cannot tell "yes, fix it" from "fix it? yes"; short human replies hinge on their first words.
2. **Human-specific capacity inside one shared model** is the second step. Separate models are worse; the same effect comes from:
   - human-only copies of the main blocks;
   - human rows ×3 (×5 and ×10 are worse);
   - char_wb n-grams for human rows.

   Together they add about +0.02 over D4 on heldout (G1 vs D4: FA +0.023, HA +0.024). Human-fold CV also rises from 0.700 to 0.712.
3. **Targets:**
   - Consensus for human rows. Using the 2-of-3 majority instead of the 3-way intersection is a tie (the CV favours it by 0.004).
   - For machine rows, the c68 pseudo-labels on Flash-only rows help AR/DEL (A's `cpl`; v1: DEL dev +0.03, AR heldout FA +0.010) but hurt H-typed when applied to human rows.
   - `hybmaj` combines the two.
4. **v2 labels barely help the linear model.**
   - G1 trained on v1 labels and scored on v2: 0.705 / 0.714 / 0.711. Retrained on v2 (V1): 0.706 / 0.718 / 0.714.
   - Round 1 already had Flash on every human item and Haiku on half. The linear model is limited by representation, not by human label volume.

## What did not work (H-typed)

- **Per-segment models:**
  - H-typed alone (7.5k rows) and all-human models are far worse alone (dev 0.647–0.657 vs 0.722 shared).
  - Routed combinations (shared machine model + per-human model) are a tie with the shared model. The cohort one-hot plus copies already gives segment specificity.
- **Per-segment thresholds:**
  - Within ±0.003 on H-typed, both on v1 (dev n=216) and v2 (dev n=972).
  - They help AIUR on v2 (V4: 0.499 → 0.594 FA) and are noisy for DEL.
- **Context features:** user/assistant split, 200-char tail, context×text word interactions and the last 8 context tokens. All are about 0 alone on dev and heldout.
  - The linear model cannot use the 600-char context the way the LLMs do.
  - CHICKADEE / BOBOLINK find context matters for encoders.
- **Cue features** (negation, imperative first verb, question form, permission/option asks in context): about 0. Word-order features already carry them.
- **Unicode-safe normalization (NFKC + casefold):** it changes the token stream of 1 of 10,000 H-typed items (0.6% have any non-ASCII). It is not a quality lever, only a parity issue (below).
- **Distillation from the round-1 encoder** (`research/tracks/C-finetune/corpus/`; train rows are in-sample):
  - pseudo-labels (T1) H-typed −0.013 FA vs D4; soft (T2) −0.02 dev; 50% mix (T3) −0.006.
  - It helps DEL and AR, hence its use on machine rows only.
- **Cheap nonlinearity** over OOF linear logits + meta2 + cue + segment:
  - LightGBM (54 per-label forests, 3,266–5,219 trees) and a tiny MLP (109→256→54).
  - Dev H-typed +0.006, but heldout H-typed: LGB on all rows −0.010, LGB on human rows +0.003, MLP +0.001. None is significant.
  - Port cost: about 200 lines of tree walker plus 3.3–5.2k trees (LGB), or two small matmuls (MLP), plus a 3× OOF training pipeline.
  - **Not worth it.**
- **Others:**
  - Frozen potion-32M embeddings for human rows: +0.00–0.01, at the cost of a WordPiece port and a 129 MB table.
  - Probability averages of two linear models: +0.00.
  - Higher L2, 5 epochs: no change.

## Best Go-native model per segment (v2 heldout; vs encoder e68, paired Δ on FA)

| segment | model | LOO FA / HA / HF | vs e68 | vs held-out LLM | recommendation |
|---|---|---|---|---|---|
| H-typed | V4-hybmaj | 0.711 / 0.717 / 0.708 | −0.025 [−0.036, −0.015] | −0.057 / −0.042 / −0.041 | linear is 0.04–0.06 below the LLMs; use an encoder if quality matters, linear as the cheap tier |
| H-typed+ctx | V4-hybmaj | 0.676 FA | −0.047 [−0.074, −0.023] | −0.091 | encoder |
| H-template | any | 0.99 | ≈ 0 | above the LLMs | linear is enough |
| H-all | V4-hybmaj | 0.780 / 0.781 / 0.776 | −0.022 | −0.043 / −0.009 / −0.028 | |
| AR (82% of items) | V4-hybmaj | 0.770 / 0.771 / 0.766 | −0.028 [−0.042, −0.015] | +0.011 / +0.009 / +0.028 | **linear is enough** (at LLM level) |
| DEL | V4-hybmaj | 0.720 / 0.739 / 0.739 | +0.010 [−0.032, +0.050] | +0.115 / +0.134 / +0.173 | **linear is enough** |
| AIUR | V4-hybmaj-segth | 0.594 FA | −0.168 (global thresholds) | −0.206 | **route to the encoder or the Haiku cascade**; no lexical fix |

## The Go prototype (`go/`, see `go/README.md`)

- **Structure:**
  - Package `labeler`: load the export, featurize, score, threshold per segment, close upwards.
  - `cmd/dlabel`: JSONL in, `.npy` logits or labels out, `-bench`, `-quant=f16`.
  - `tools/parity.sh`: Python oracle `scripts/refscore.py` vs Go on all eval items, with a gate.
  - Fixtures: `go/testdata/fixtures.jsonl`, 384 cases including §5 of PARITY.md and 300 fuzz cases, plus a synthetic model that uses every source.
- **Parity tolerance:** the gate is max |Δlogit| ≤ 1e-4 and max |Δp| ≤ 2e-5, and the result is 1e-13. The features are bit-identical, and the only remaining difference is float64 dot-product order. The tolerance allows float32 TF-IDF summation-order noise (≤ 1e-6 relative over ≤ 500 terms with |W| ≲ 10). It is still 100× below a probability gap that could flip a threshold at 0.02 grid spacing. In practice there were 0 flips.
- **Throughput:** featurization dominates; the 34 regex rules dominated until an exact literal prefilter was added (2.2k → 7.8k items/s per core on D5-xi).

  | model | 1 goroutine | 4 goroutines | RSS, model only | files |
  |---|---|---|---|---|
  | D5-xi (no human blocks) | 7.8k/s | 32k/s | 135 MB | |
  | V4 | 4.0k/s | 16.7k/s | 220 MB | 161 MB |
  | V5 | 4.0k/s | 17.8k/s | 178 MB | 119 MB |
  | V5 f16 | 3.9k/s | 20.4k/s | | W 33 MB |

  - Measured at machine load 18–26/28. For an M2 MacBook Air, take the 1- and 4-goroutine figures as the proxy.
  - JSON parsing adds 5%.
  - The human blocks (char_wb, copies) cost about 2× per item over D5-xi.
- **Size:**
  - f16 W halves memory with no harness change. Its max |Δp| is 6e-4; on D5-xi it caused 9 flips in 95k.
  - int8 per row or per column is not usable (max |Δp| 1–5e-2).
  - The 1.3M-entry IDF table of the `w` block (21 MB) exists only for the pre-cap L2 norm. Retraining with norms over kept columns would cut about 30 MB (untested).
- **Export format:**
  - `model.json`: labels, parent, bias, blocks, groups, seg_order, meta2_cols, and thresholds {default, by_segment}.
  - `<block>.tbl`: little-endian records of u32 hash index, f32 idf, i32 col (−1 = norm only), and an i32 copy column per copy group.
  - `W.f32`: [F, 54] float32 row-major.

## Parity contract (`PARITY.md`) and where Go differs from Python

- **Unicode:**
  - Go 1.27 ships **Unicode 17.0**; Python 3.12 uses 15.0. So the Go side uses tables generated from Python (`scripts/gen_unitab.py` → `unitab_gen.go`) for `\w`, `\d`, `isspace`, `str.lower` and the Final_Sigma properties. It never uses package `unicode`.
  - Python's `str.lower` maps İ to two code points and applies final sigma (Σ occurs in 130 corpus items, İ in 10). Go's `strings.ToLower` does neither.
  - `isspace` includes `\x1c`–`\x1f`, which `unicode.IsSpace` lacks.
- **Regex:**
  - RE2's `\w \b \d \s` are ASCII-only, and its `\s` lacks `\v`.
  - Fixes: a hand tokenizer, and the 34 Python regexes run on an ASCII "shadow string":
    - Python-whitespace → ' ';
    - non-ASCII digits → '0';
    - İ/ı/ſ/K → i/i/s/k, the only non-ASCII code points Python `re.I` matches to ASCII letters;
    - other word chars → '_'.
  - No genuine regex difference remains on the corpus.
- **Hashing:** sklearn's murmur3_32 (seed 0, signed, abs % n, with the special case for −2^31).
- **Numerics:**
  - numpy's float32 `log1p` is not correctly rounded at 49 integers below 2^24; the Go side carries an exceptions table.
  - Go's FMA fusion of `ss += v*v` must be blocked with explicit float32 conversions.
  - Python multiplies by the float32 reciprocal of the norm.
- **Inputs:** `chars` is in code points, and slicing (`text[:500]`, `text[:3000]`, `context[-200:]`) is by code point. The segment is an input; stale segment labels were the only parity failure ever seen (91 items).

## Cost summary

| item | cost |
|---|---|
| Training (Python/torch, 8 threads) | 65 s, 8.3 GB peak (V4); feature hashing for all 403k rows takes 6 s on 10 processes |
| Human-fold CV per config | ~3.5 min |
| Inference, pure Go | 4k items/s per core, 17–20k/s on 4 cores, 178–220 MB RSS, 119–161 MB files (33 MB W in f16), 40–80 ms load |
| Label spend | $0 |

## Recommendations

1. **Ship one linear model, `V4-hybmaj` or the compact V5 in f16**, in pure Go with this parity contract, as the default for AR, DEL, H-template and H-resent.
   - On AR and DEL it is at or above LLM level.
   - On H-typed it is the best Go-native option: +0.038 over round 1.
   - But it is still −0.025 to −0.036 below the 68m encoder and −0.04 to −0.06 below the LLMs.
2. **For H-typed and H-typed+ctx,** if quality matters, use an encoder (BOBOLINK) or the Haiku cascade.
   - Human items are about 4.6% of items, so an encoder there costs little even on CPU (~100 items/s int8 → 16.7k human items in ~3 min).
3. **AIUR:** send it to the encoder or the novel-template Haiku fallback. No linear variant reaches 0.6 FA.
4. **Don't add LightGBM or MLP stackers, per-segment linear models, or distillation for human text** to the Go tier. They do not pay for their port cost on H-typed.

## Open questions

- Linear H-typed quality plateaus at about 0.71 FA with features and targets. Would a fine-tuned static table (B-style, pure Go, WordPiece port) on human rows add the semantic generalization that n-grams lack? The frozen table gave +0.00–0.01.
- Is the full IDF table needed? Renormalizing over kept columns would shrink the files by about 30 MB.
- Throughput was measured on a heavily loaded M3 Ultra. Re-run `dlabel -bench` on an idle machine or a real M2 Air.

## Files

| what | where |
|---|---|
| Prediction files (v2 convention, for BUFFLEHEAD) | `preds/<run>.npz` and `preds/<run>-segth.npz`; `-v2th` = a v1-trained run with thresholds retuned on v2 dev |
| Probabilities for every non-train item | `probs/` |
| Exported models | `models/<run>/` |
| Go module | `go/` |
| Scripts | `scripts/`, listed in `CHECKPOINT.md` |
| Logs | `logs/` (`table_v1.md`, `table_v2.md`, `hcv.out`, `q*.out`, `qv2.out`, `qv3.out`) |
| Contract | `PARITY.md` |

Python package added: none.
