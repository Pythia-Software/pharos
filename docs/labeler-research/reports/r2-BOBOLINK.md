> Sanitized copy of ~/pharos-labeling/studies/BOBOLINK/REPORT.md (archived in full on euclid at "Pharos Labeling/2026-10-29-bird-studies"). Verbatim conversation text was removed.

# BOBOLINK: segment specialists vs one shared model, and context framing for human text

2026-10-29, 08:45–14:30 MDT. All numbers come from AVOCET harness v2 on **data v2**: every human item has all three teachers.
Part = held-out (test ∪ fresh). The headline segment is **H-typed** (y3 n=1,494). LOO-FA is the student vs Flash∩Argon, with Haiku's score on the same items beside it.
Unless noted, Δ values are paired bootstrap 95% CIs over 2,000 item resamples. Scorer: mean(y3 head, y_hf head). Thresholds: per segment, tuned on the v2 y3 dev set.
Pred files: `preds/b2-<run>.npz`.

## Answer
- **Does per-segment training help? Only as a way to get more weight or more labels onto a segment. Once one shared model trains on the same labels, specialization adds nothing measurable.**
  - **68m.** The shared model retrained on v2 labels (`s68v-all`) scores **0.772** on H-typed (Haiku 0.769, Δ +0.003 [−0.008,+0.015]).
    The human specialists built on the round-1 68m shared model score the same against it:
    - continued, seeds 0 and 1: −0.004 and +0.002;
    - LoRA: −0.004;
    - pure specialist: −0.004;
    - H-typed-only: 0.000.
  - **32m.** The shared v2 model scores 0.752.
    - Pure specialist, LoRA and continuation from the round-1 shared model: within ±0.003 of it.
    - Continuing `s32v-all` itself on human (`sv-c32v-hum`): 0.762, **+0.010 [+0.004,+0.016]**. This is the one specialization gain that holds against a same-label shared model.
  - **With round-1 (v1) labels**, specializing on human gives +0.013–0.016 on H-typed at both sizes. Example: `c68-hum` vs 68m shared, +0.016 [+0.008,+0.023].
    v2 adds three-teacher labels (y3) to every human item, which up-weights human data inside the shared model. That reproduces the gain.
- **Primary target met.** On H-typed, a 68m student now matches Haiku on the fair bar and beats Flash and Argon:
  - `s68v-all`: FA 0.772 vs 0.769; HA 0.782 vs Flash 0.758; HF vs Argon +0.023.
  - `c68v-hum`: 0.768 / 0.774 for seeds 0/1; 2-seed blend 0.775 (Δ vs Haiku +0.006 [−0.005,+0.017]).
  - Round 1's −0.030 gap came from human data being 4.6% of training and lacking Argon labels. It did not come from context.
- **Context and framing: not the cause, and not a lever.**
  - Removing context costs about 0.01.
  - Every budget from 64 tokens up to all 600 characters scores the same.
  - Round-1 framing is already as good as anything else tried.
- **Where segment training helps a little at 32m** (vs shared 32m round-1, same labels):
  - AIUR J vs Haiku∩Flash: +0.029 [+0.022,+0.036] (n=2,601); LOO-FA +0.008, not significant.
  - DEL flash J: +0.021 to +0.033; LOO-FA +0.009 to +0.015, not significant.
  - AR (n=947): +0.004, not significant.
  - Pure specialists fail where data is small and the segment is not distinct: AIUR −0.074 [−0.104,−0.044]. DEL holds up (+0.013).
- **Per-segment thresholds on the shared model (option b) do nothing.** H-typed ±0.000. They hurt 68m AIUR (−0.032), because AIUR's dev set has only 189 items.

## Per-segment table (v2 held-out, LOO-FA vs the held-out LLM; Δ vs the 68m shared v2 model `s68v-all`)
| segment (n y3) | shared 32m r1 | shared 68m r1 | **shared 68m v2** | best specialist (Δ vs shared 68m v2) | Haiku FA | best HA / Flash HA |
|---|---|---|---|---|---|---|
| H-typed (1,494) | 0.731 | 0.747 | **0.772** | c68v-hum-s1 0.774 (+0.002); 2-seed blend 0.775 (+0.003) | 0.769 | 0.786 / 0.758 |
| H-typed+ctx (364) | 0.698 | 0.722 | 0.746 | c68v-hum 0.760 (+0.013 [−0.009,+0.034]) | 0.767 | 0.786 / 0.744 |
| H-template (674) | 0.992 | 0.991 | ≈0.99 | all ≈0.993 (saturated) | 0.980 | |
| H-resent (20), H-feedback (39) | | | | too small; indicative only (cross_matrix.csv) | | |
| AIUR (313) | 0.662 | 0.616* | 0.671 | c32-AIUR 0.670 (vs 32m r1 +0.008 [−0.006,+0.021]; J_hf +0.029) | 0.706 | 0.712 / 0.656 |
| AR (947) | 0.785 | **0.803** | 0.786 | c32-AR 0.789 (vs 32m r1 +0.004) | 0.759 | 0.816 / 0.762 |
| DEL (175) | 0.675 | 0.673 | **0.707** | e32-DEL-lora16 0.690 (vs 32m r1 +0.015 [−0.017,+0.050]) | 0.604 | 0.800 / 0.605 |
| ALL (4,026) | 0.777 | 0.785 | **0.799** (+0.010 [+0.002,+0.017] vs Haiku) | routed c68v-hum + 68m r1: 0.797 | 0.789 | 0.810 / 0.765 |

\* 68m r1 on AIUR with per-segment thresholds. With global thresholds it scores 0.648.

Notes on the table:
- `s68v-all` is a single seed. It trails round-1 68m on AR by 0.017 FA, so the AR difference is within seed spread (about 0.01 LOO in round 1).
- Routed composites (pred files `b2-route-*`):
  - `b2-route-c68vhum+e32` (68m human specialist, 32m shared elsewhere): H-typed 0.768, ALL 0.797.
  - `b2-route-d32vhum+e32` (all 32m): H-typed 0.756, ALL 0.788.

### Variants on H-typed (FA; Δ vs the same-size shared model trained on the same labels)
| variant | 32m (v2 labels) | 68m (v2 labels) | 68m / 32m with v1 labels (vs r1 shared) |
|---|---|---|---|
| (a) shared retrained, same labels | s32v-all 0.752 | s68v-all 0.772 | r1: 0.747 / 0.731 |
| (b) + per-segment thresholds | ±0.000 | | ±0.000 |
| (c) shared continued on human, 2 ep | from r1: 0.750 (−0.002); from s32v: **0.762 (+0.010 [+0.004,+0.016])** | c68v-hum 0.768 / s1 0.774 (−0.004 / +0.002) | c68-hum 0.763 (+0.016) / c32-hum 0.744 (+0.013) |
| (c') continued on H-typed only | 0.751 (−0.002) | c68v-Ht 0.772 (0.000) | c32-Ht 0.746 (+0.015) |
| (d) pure specialist from pretrained (the user's hypothesis) | d32v-hum 0.756 (+0.003) | d68v-hum 0.768 (−0.004) | d32-hum 0.744 (+0.014) |
| (d') pure, H-typed only | | | d32-Ht 0.739 (+0.008) |
| (e) LoRA r16 + heads on shared | e32v-hum-lora16 0.754 (+0.002) | e68v-hum-lora16 0.768 (−0.004) | e32-hum-lora16 0.740 (+0.009) |
| (e') heads only, frozen encoder | | | e32-hum-heads 0.738 (+0.001) |
| continued + 25k replay (one model) | | | c32-hum-rep 0.739 (+0.009), keeps AR intact |
| LOO-clean: never trained or tuned on Haiku | noH32 base 0.716 → continued on human 0.748 | | |

Seed check: c68v-hum seed 0 / seed 1 = 0.768 / 0.774, so a single run is good to about ±0.005.

Segment grouping:
- H-template, H-resent and H-feedback (2.7k train items) are grouped with H-typed and H-typed+ctx for "human" training (12.5k items).
- H-typed alone is 7.5k items.
- Training on all human data beats H-typed-only for H-typed+ctx (pure H-typed-only specialist: −0.029), and ties on H-typed.

## Experiment 1: context and framing for human text (within the LLM's 600 characters)
Setup:
- 32m, the round-1 shared model continued 2 epochs on human train with each framing (v1 labels).
- Scored on v2, Δ LOO-FA on H-typed vs the round-1-framing control (n=1,494).
- Table: `results/framing_v2_H-typed.csv`.

| variant | Δ FA [95% CI] | | variant | Δ FA |
|---|---|---|---|---|
| control seed 1 | +0.003 [−0.001,+0.008] | | assistant part only | −0.002 |
| **no context** | **−0.009 [−0.016,−0.001]** | | user part only | **−0.010 [−0.017,−0.002]** |
| **hard 64 tokens** (round-1 hypothesis) | −0.002 [−0.008,+0.003] | | text first, context after | **−0.009 [−0.016,−0.003]** |
| 150 tokens | −0.001 | | labelled "Earlier conversation: / Current message:" | −0.003 |
| 256 tokens | −0.001 | | no sender tag | +0.002 |
| all 600 chars (2 seeds) | +0.002 / +0.002 | | segment name as tag | −0.004 |
| text max_len 128 | −0.003 (J −0.009) | | text max_len 256 | +0.001 |

Readings:
- Round 1's "64-token context" cap did not bind for short text. Its encoder kept the last 512 of the 600 characters and gave context the leftover budget.
- For H-typed, the 600 characters are mostly the previous assistant message: 77% of items contain it, 398 characters on average. The previous user request is 21% of items, 28 characters on average.

## Cost
| model | train (wall, MPS, shared with other studies) | MPS inference | CPU ONNX w8, 4 threads, items/s | artifact |
|---|---|---|---|---|
| 32m shared | 25 min (s32v-all; 269k items) | 700 items/s | H-typed 75, H-rest 100, AR 71, AIUR 26, DEL 29 | 92 MB w8 (128 MB fp32) |
| 68m shared | 51 min (s68v-all) | 417 items/s | H-typed 27, AR 25, AIUR 9.8, DEL 10.6 | 152 MB w8 (273 MB fp32) |
| (c) continued on human | 32m 1.3 min; 68m 3.4 min | same as base | same as base | a second full model |
| (d) pure human specialist | 32m 2.6 min; 68m 6.5 min | same | same | a second full model |
| (e) LoRA r16 | 32m 2.1 min; 68m 9.0 min | same once merged | same once merged; +~5% unmerged | **3.5 MB (32m) / 8.4 MB (68m)** fp32 |
| heads only | 0.6 min | same | same | 0.4 MB |

- **CPU per corpus mix** (AR .817, AIUR .078, DEL .059, human .046), from `results/cpu_bench_t4.csv`, 4 threads:
  - 32m only: about 59 items/s (400k in 1.9 h).
  - 68m only: about 21 items/s (5.3 h).
  - Routed, 32m shared plus a 68m human model: about 55 items/s (2.0 h). Human items are 4.6% of traffic and short, so a separate human model costs about 6% CPU.
- **Cheaper framing:** no context makes H-typed 2.5x faster on CPU (187 vs 75 items/s) at −0.009 FA. Human is a small share of traffic, so this saves little overall.
- **Label spend:** $0 by BOBOLINK. The v2 human labels are AVOCET's ($105).

## Recommendations
1. **Ship one shared model trained on v2 labels, not per-segment models.** 68m `s68v-all` matches Haiku on H-typed and beats it overall (ALL FA 0.799 vs 0.789).
   For the 32m CPU path, use `s32v-all` (0.752) plus a 2-epoch continuation on human (`sv-c32v-hum`, 0.762). Ship it either as:
   - a merged second model routed to human items; or
   - a LoRA adapter (3.5 MB) if the runtime can do adapters.
2. **Spend labels, not architecture, on weak segments.** The H-typed gap closed through three-teacher labels on human train data. The same lever applies to AIUR, where specialists did not close the −0.04 gap to Haiku.
3. **Keep round-1 framing.** Don't spend effort on context packing within 600 characters. CHICKADEE's longer-context test is the remaining context question.
4. **Don't use per-segment thresholds on small dev sets.** Don't train pure specialists for AIUR or other small, non-distinct segments.

## Open questions
- `s68v-all` is a single seed, and its AR score is 0.017 below round-1 68m. Is that seed noise, or did human emphasis cost AR? A second seed would tell.
- Would a 68m shared v2 model continued on human (as at 32m, +0.010) add anything at 68m?
- GANNET (post-snapshot) scoring of the frozen models is pending with the director (`gannet/`).

## Files (`~/pharos-labeling/studies/BOBOLINK/`)
- **Pred files** (harness v2): `preds/b2-*.npz`, plus `.v2.metrics.json`. `b-*` are v1-label scorings (superseded); `b2s-*` are the framing screen.
- **Cross-segment matrix:** `results/cross_matrix.csv` (35 models × 10 groups, J ± CI, LOO FA/HA/HF with the LLM and paired CI, y_hf and flash J). See also `MATRIX_READY`.
- **Models:** `models/<run>/` (final.pt / adapter.pt / heads.pt, meta.json, dev/test/fresh probs for 5 heads).
  - Best: `models/c68v-hum`, `c68v-hum-s1`, `s68v-all`.
- **GANNET holdout predictions:** `gannet/{c68v-hum, c68v-hum-s1, blend-c68vhum-2seeds, r1-c-e68-512-all, s68v-all}.npz`.
  Each holds probs, a segment per item and v2-dev per-segment thresholds; `READY` is written. GANNET labels were never read.
- **Scripts:** `scripts/` (bob_train.py, framing.py, to_v2.py, paired_v2.py, matrix.py, cpu_bench.py, costs.py, gannet_predict.py, queue*.sh).
- **Other results:** `results/framing_v2_H-typed.csv`, `results/cpu_bench_t4.csv`, `results/costs.csv`.
- **Added package:** peft 0.21.2.

## TH adapter (thinking; added 14:48)

**Data:** EGRET thinking labels (`EGRET/data/`).
- 14,000 TH train items, Flash labels only.
- Tuned on TH dev items that have Argon (n=1,000), target y_fa.
- Scored on TH test ∪ fresh items that have Argon (n=2,000).

**Base and framing:**
- Base: round-1 68m shared model `c-e68-512-all`.
- Framing: round-1 encoder (tag `thinking :`, context = the 600-char `context` field).
- Loss: Flash head only, with the `flash_masked` loss mask.

**Scorer:** chosen on dev. The Flash head won (dev y_fa J 0.850). Thresholds are per-label Jaccard.

**Script:** `scripts/th_train.py`. Outputs are in `th/<run>/` (`result.json`, `heldout_probs.npz`, `thresholds.npy`, adapter or weights).

| model | train (MPS wall) | J vs Argon | Flash vs Argon (same items) | Δ vs Flash [95% CI] | J vs Flash∩Argon | test / fresh vs Argon | artifact |
|---|---|---|---|---|---|---|---|
| round-1 68m zero-shot, re-tuned on TH dev | 0 | 0.600 | 0.793 | −0.193 [−0.205,−0.181] | 0.596 | 0.653 / 0.548 | (base) |
| **TH LoRA r16 on 68m shared (`th68-lora16`)** | **5.5 min** (2 ep) | **0.798** | 0.793 | **+0.005 [−0.003,+0.014]** | 0.843 | 0.803 / 0.794 | **8.4 MB adapter** |
| 68m shared continued 2 ep on TH (`th68-cont`) | 5.3 min | 0.804 | 0.793 | +0.011 [+0.003,+0.020] | 0.844 | 0.809 / 0.800 | 273 MB full model (152 MB w8) |

**Reading the result:**
- One 5-minute adapter takes the student from 0.60 to Flash's level against Argon on thinking.
- The full continuation is slightly better: +0.006 over LoRA.
- The student was trained only on Flash labels, so "vs Argon beside Flash" measures how well it reproduces a Flash-quality labeler. It is not an independent third-teacher test.

**Zero-shot discrepancy:** EGRET's analysis reports zero-shot round-1 students at 0.42–0.56 vs Argon with round-1 thresholds. My dev re-tuned zero-shot number (0.600) is consistent with that. The "≈0.72" in the brief matches the overall Flash–Argon J, not a student score.

**The base is untouched:**
- The adapter is saved separately (LoRA A/B weights plus heads, `th/th68-lora16/adapter.pt`).
- The base `final.pt` SHA-256 was checked unchanged after every run (`base_sha256_unchanged: true` in each result.json).
- Non-TH segments keep using the base model's predictions (`preds/b2-r1-e68.npz`), so their scores are unaffected by construction.

**Inference cost:** MPS 271 items/s with the LoRA unmerged, 357 items/s merged or full. TH inputs are long (mean ~300 tokens), so CPU cost is about AIUR-level, roughly 10 items/s for 68m w8 on 4 threads.
