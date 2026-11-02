> Sanitized copy of ~/pharos-labeling/studies/HERON/REPORT.md (archived in full on euclid at "Pharos Labeling/2026-10-29-bird-studies/studies/HERON"). Verbatim conversation text was removed, and private repository and project names
> were replaced with neutral labels.

# HERON: does the Go linear labeler degrade more than the small encoders under shift?

2026-10-29, 15:45–19:30 MDT. Leave-group-out folds inside the library, labels = round-1 + AVOCET v2 (no new labeling,
no LLM calls). Scored with AVOCET harness v2 primitives (`rlh2`): per item, **fair-LOO J** = mean Jaccard of the closed
prediction against F∩A, H∩A and H∩F (y3 items), plus J vs Haiku∩Flash (`j_yhf`, y_hf items; larger n for machine segments).
All comparisons are paired on the same held-out items with the same threshold protocol.

## Answer (numbers first)

- **H-typed (typed human text): no evidence that the linear model degrades more than the encoder.**
  Degradation lin − e32 (positive = linear degrades more), 95% CI including training-seed noise:
  repo folds pooled **−0.005 [−0.025, +0.015]**, Codex provider **−0.003 [−0.024, +0.018]**, time **+0.011 [−0.014, +0.035]**.
  Mean over the five folds −0.002. Both models lose 0–0.017 J on a new repo cluster / provider / period. What does not
  change under shift is the **level gap**: linear sits ~0.05 below ettin-32m in-group (0.70 vs 0.75) and out-of-group (0.70 vs 0.74).
  The GANNET impression (linear −0.04 vs encoder −0.01–0.02 on new conversations) does not replicate in paired leave-group-out tests.
- **Where the encoder generalizes clearly better: machine text whose format is new.**
  - AR under a new provider: Codex AR deg linear 0.096 vs e32 0.063, **diff +0.033 [+0.005, +0.060]** (j_yhf, n=14k: +0.018 [+0.007, +0.029]).
  - AR under time shift: linear 0.119 vs e32 0.028, **diff +0.091 [+0.031, +0.151]**.
  - AIUR under a new automation (TIME fold: post-cutoff AIUR is a new automation [in a spreadsheet-automation project; name omitted]): linear 0.866→0.445 (−0.42), e32 0.895→0.758 (−0.14),
    **diff +0.28 [+0.10, +0.47]**; RB (contains [that automation's project]) +0.13 [+0.02, +0.23].
  - Codex DEL: 199 y_hf items in 12 families are machine payloads ([JSON task payloads; text omitted], encrypted blobs) with empty targets; the linear model
    trained without Codex labels half of them (J 1.0→0.52), the encoders none. Tiny family count, but a concrete failure mode (format novelty).
  - Not everywhere: on RA and RC AIUR the encoder lost more (diff −0.040 / −0.063, CIs include 0 after seed noise), and pooled-repo AR j_yhf diff is +0.003.
- **Local adaptation can close much of the linear gap where it is large, and it does not need the source data.**
  Warm-starting the shipped linear weights on k of the group's own labeled items (k/2 human, k/2 machine; 2–6 s of training; deployable in Go):
  Codex AR gap closed 13% / 43% / 62% at k = 250 / 1k / 4k; TIME AIUR 16% / 35% / 72%; TIME AR 8% / 18% / 39%.
  Full retraining with the source data plus k items (an upper bound that needs the private corpus) is similar on AIUR and better on AR
  (TIME AR 42% / 71% / 77%). H-typed gaps are within noise already, so adaptation has nothing to recover there.
  The encoder, continued 2 epochs on the same k items, recovers less on AR (Codex AR −9% / −23% / +23%; TIME AR −22% / +36% / −9%:
  noise-level) and more on AIUR (TIME 33% / 42% / 79%). After local adaptation with k = 4k the **absolute** gap nearly vanishes on AR:
  Codex AR linear-warm 0.720 vs e32-continued 0.724; TIME AR linear-retrain 0.702 vs 0.702 (warm 0.657). TIME AIUR stays apart (0.758 vs 0.865).
- **ettin-68m (RA fold only):** H-typed deg +0.006 vs linear +0.004, diff −0.001 [−0.023, +0.020]; same picture as 32m.
- **Perturbations (indicative):** no model is uniformly more robust. Linear is immune to case and whitespace by construction and loses most
  to punctuation stripping (−0.057 vs −0.041 e32, −0.029 e68). ettin-32m is hurt by extra whitespace/newlines (−0.036 vs linear 0.000).
  ettin-68m is the most robust overall.
- **Mechanism:** lexical novelty (share of word 1–2-grams unseen in training) is small for H-typed (top quintile mean 4%) and its
  differential effect is ~0.01 J. For AR it matters more: in the most novel fifth, linear degrades 0.119 vs e32 0.027
  (slope difference CI [+0.58, +2.08]). The largest failures (TIME AIUR, Codex DEL) are new templates and payload formats.

## Design

| fold | held out | families | train items removed | eval y3 items (H-typed / AR / AIUR / DEL) |
|---|---|---|---|---|
| RA | repo cluster A [8 named repositories; names omitted] | 12.4% | 27,960 (10.4%) | 768 / 300 / 130 / 59 |
| RB | repo cluster B [a spreadsheet-automation project: 4 named repositories and 2 repository-name patterns; names omitted] | 16.0% | 40,384 (15.0%) | 273 / 134 / 196 / 13 |
| RC | repo cluster C: all small repos + unmapped workspaces (not the two largest repositories) | 6.9% | 33,879 (12.6%) | 359 / 235 / 63 / 64 |
| CODEX | provider codex | 33.2% | 73,349 (27.2%) | 1,009 / 580 / 218 / 28 |
| TIME | families started ≥ 2026-09-17 (eval: their dev+test items) | 33.5% | 61,222 (22.7%) | 371 / 250 / 218 / 14 |

- Repository = `units.workspace_id` → `workspaces.repository_id` in the static snapshot clone (read-only, immutable). The two largest repositories [names omitted] (34% of
  families and 31%) are too large to hold out at 15–20%; the three repo folds cover the rest. AGY (antigravity) fold built
  but not run (2 H-typed items; AIUR is covered by TIME/RB).
- Eval items = the group's dev + test + fresh items (TIME: dev + test). Thresholds for **every** model on a fold are tuned per segment on the
  non-group dev items only (`rlh2.tune_thresholds_by_segment`, y3, fallback y_hf). Training uses the train split only.
- Models (same targets and recipes): Go linear = DOTTEREL `V4-hybmaj` recipe (`scripts/hexp.py`, a copy of `dexp.py`; column cap and copy
  blocks computed on the rows actually trained on). In-group `LIN-IN` reproduces DOTTEREL's V4-hybmaj predictions exactly
  (RA H-typed 0.6939 both). Encoder = BOBOLINK `s32v-all` recipe (ettin-32m from pretrained, all segments, v2 labels, 1 epoch;
  `scripts/hb_train.py`, a copy of `bob_train.py`); in-group = BOBOLINK's s32v-all (seed 0) plus my seed-1 rerun `IN-s32-s1`.
  ettin-68m (`s68v-all` recipe) on RA.
- **Degradation** = mean over both seeds of J(in-group) − J(out-of-group), same items. Uncertainty: family-level bootstrap (2,000 draws;
  stratified by fold for the pooled repo row) combined with training noise estimated from the seed-0 vs seed-1 in-group runs
  (per-run SD on fold items: H-typed e32 0.007, lin 0.004; AR 0.008 / 0.005; AIUR 0.034 / 0.017). The bootstrap alone understates
  uncertainty: the encoder seed pair differs by up to 0.017 on H-typed fold items and 0.09 on RA AIUR.
- Caveat: the hybmaj target uses round-1 c68 pseudo-labels on Flash-only machine rows; that teacher saw all train items, so out-of-group
  linear runs get a little indirect knowledge of the group's machine rows (H-typed targets are unaffected).

## Degradation table (fair-LOO J, y3 items; in-group = mean of 2 seeds)

CI = family bootstrap + training noise. Positive diff = linear degrades more.

| fold | segment | n (fam) | lin in→out | lin deg | e32 in→out | e32 deg | **lin − e32** [95% CI] |
|---|---|---|---|---|---|---|---|
| REPO(RA+RB+RC) | **H-typed** | 1400 (466) | 0.701→0.698 | +0.004 [-0.006, +0.013] | 0.750→0.741 | +0.008 [-0.009, +0.026] | **-0.005** [-0.025, +0.015] |
| RA | **H-typed** | 768 (291) | 0.699→0.695 | +0.004 [-0.007, +0.016] | 0.754→0.745 | +0.009 [-0.009, +0.027] | **-0.004** [-0.026, +0.017] |
| RB | **H-typed** | 273 (83) | 0.710→0.718 | -0.008 [-0.022, +0.005] | 0.743→0.740 | +0.002 [-0.018, +0.023] | **-0.011** [-0.034, +0.013] |
| RC | **H-typed** | 359 (92) | 0.700→0.690 | +0.010 [-0.004, +0.025] | 0.746→0.733 | +0.012 [-0.009, +0.033] | **-0.002** [-0.028, +0.024] |
| CODEX | **H-typed** | 1009 (352) | 0.698→0.684 | +0.014 [+0.002, +0.026] | 0.750→0.734 | +0.017 [-0.002, +0.035] | **-0.003** [-0.024, +0.018] |
| TIME | **H-typed** | 371 (110) | 0.682→0.676 | +0.006 [-0.011, +0.022] | 0.750→0.755 | -0.005 [-0.025, +0.015] | **+0.011** [-0.013, +0.035] |
| REPO(RA+RB+RC) | H-typed+ctx | 297 (184) | 0.666→0.659 | +0.007 [-0.022, +0.037] | 0.723→0.712 | +0.011 [-0.042, +0.064] | **-0.003** [-0.065, +0.059] |
| RA | H-typed+ctx | 159 (105) | 0.671→0.671 | -0.000 [-0.033, +0.033] | 0.740→0.731 | +0.010 [-0.045, +0.065] | **-0.010** [-0.076, +0.056] |
| RB | H-typed+ctx | 83 (46) | 0.646→0.626 | +0.020 [-0.016, +0.056] | 0.675→0.663 | +0.012 [-0.044, +0.068] | **+0.008** [-0.057, +0.074] |
| RC | H-typed+ctx | 55 (33) | 0.685→0.674 | +0.011 [-0.035, +0.056] | 0.744→0.733 | +0.011 [-0.058, +0.080] | **-0.001** [-0.085, +0.084] |
| CODEX | H-typed+ctx | 227 (151) | 0.674→0.677 | -0.003 [-0.043, +0.037] | 0.729→0.731 | -0.001 [-0.056, +0.053] | **-0.002** [-0.069, +0.066] |
| TIME | H-typed+ctx | 50 (28) | 0.665→0.677 | -0.011 [-0.051, +0.029] | 0.648→0.660 | -0.012 [-0.071, +0.048] | **+0.001** [-0.077, +0.079] |
| REPO(RA+RB+RC) | H-template | 534 (355) | 0.986→0.980 | +0.007 [-0.001, +0.014] | 0.981→0.978 | +0.003 [-0.005, +0.011] | **+0.004** [-0.005, +0.013] |
| RA | H-template | 391 (258) | 0.988→0.979 | +0.009 [-0.001, +0.018] | 0.984→0.981 | +0.003 [-0.005, +0.011] | **+0.006** [-0.004, +0.016] |
| RB | H-template | 106 (69) | 0.982→0.979 | +0.002 [-0.003, +0.008] | 0.969→0.966 | +0.002 [-0.009, +0.013] | **+0.000** [-0.009, +0.009] |
| RC | H-template | 37 (28) | 0.985→0.987 | -0.002 [-0.008, +0.003] | 0.987→0.985 | +0.002 [-0.006, +0.010] | **-0.004** [-0.015, +0.006] |
| CODEX | H-template | 519 (425) | 0.994→0.994 | -0.001 [-0.004, +0.003] | 0.991→0.991 | -0.001 [-0.009, +0.008] | **+0.000** [-0.009, +0.009] |
| TIME | H-template | 137 (91) | 0.978→0.978 | -0.001 [-0.005, +0.004] | 0.970→0.978 | -0.009 [-0.024, +0.006] | **+0.008** [-0.008, +0.024] |
| REPO(RA+RB+RC) | AIUR | 389 (245) | 0.743→0.644 | +0.099 [+0.049, +0.149] | 0.810→0.751 | +0.059 [-0.030, +0.148] | **+0.040** [-0.066, +0.146] |
| RA | AIUR | 130 (118) | 0.566→0.507 | +0.059 [+0.015, +0.103] | 0.720→0.621 | +0.099 [+0.012, +0.186] | **-0.040** [-0.138, +0.058] |
| RB | AIUR | 196 (74) | 0.888→0.753 | +0.135 [+0.074, +0.196] | 0.904→0.895 | +0.008 [-0.075, +0.091] | **+0.127** [+0.024, +0.230] |
| RC | AIUR | 63 (53) | 0.657→0.585 | +0.072 [-0.025, +0.169] | 0.707→0.572 | +0.135 [+0.027, +0.243] | **-0.063** [-0.196, +0.069] |
| CODEX | AIUR | 218 (190) | 0.598→0.567 | +0.031 [-0.018, +0.079] | 0.729→0.699 | +0.030 [-0.055, +0.115] | **+0.001** [-0.095, +0.097] |
| TIME | AIUR | 218 (92) | 0.866→0.445 | +0.421 [+0.243, +0.599] | 0.895→0.758 | +0.137 [+0.048, +0.226] | **+0.284** [+0.097, +0.472] |
| REPO(RA+RB+RC) | AR | 669 (311) | 0.741→0.726 | +0.014 [-0.000, +0.029] | 0.752→0.755 | -0.003 [-0.025, +0.019] | **+0.017** [-0.009, +0.044] |
| RA | AR | 300 (187) | 0.743→0.721 | +0.022 [+0.003, +0.041] | 0.771→0.752 | +0.019 [-0.004, +0.042] | **+0.003** [-0.028, +0.033] |
| RB | AR | 134 (58) | 0.693→0.687 | +0.006 [-0.028, +0.041] | 0.692→0.718 | -0.026 [-0.059, +0.008] | **+0.032** [-0.013, +0.077] |
| RC | AR | 235 (66) | 0.766→0.756 | +0.009 [-0.010, +0.029] | 0.762→0.780 | -0.018 [-0.043, +0.007] | **+0.027** [-0.003, +0.057] |
| CODEX | AR | 580 (331) | 0.756→0.660 | +0.096 [+0.078, +0.114] | 0.772→0.709 | +0.063 [+0.039, +0.088] | **+0.033** [+0.005, +0.060] |
| TIME | AR | 250 (94) | 0.730→0.610 | +0.119 [+0.048, +0.191] | 0.733→0.705 | +0.028 [-0.002, +0.058] | **+0.091** [+0.031, +0.151] |
| REPO(RA+RB+RC) | DEL | 136 (62) | 0.737→0.740 | -0.003 [-0.037, +0.032] | 0.757→0.779 | -0.022 [-0.094, +0.050] | **+0.019** [-0.056, +0.095] |
| RA | DEL | 59 (32) | 0.642→0.654 | -0.013 [-0.064, +0.038] | 0.658→0.665 | -0.007 [-0.092, +0.077] | **-0.005** [-0.090, +0.080] |
| RC | DEL | 64 (23) | 0.844→0.827 | +0.017 [-0.018, +0.053] | 0.841→0.883 | -0.043 [-0.124, +0.039] | **+0.060** [-0.023, +0.143] |
| CODEX | DEL | 28 (5) | 1.000→0.500 | +0.500 [+0.404, +0.596] | 1.000→1.000 | +0.000 [-0.066, +0.066] | **+0.500** [+0.379, +0.621] |

Same, J vs Haiku∩Flash on y_hf items (machine segments; larger n):

| fold | segment | n (fam) | lin in→out | lin deg | e32 in→out | e32 deg | **lin − e32** [95% CI] |
|---|---|---|---|---|---|---|---|
| REPO(RA+RB+RC) | AIUR | 3163 (1153) | 0.751→0.668 | +0.083 [+0.042, +0.124] | 0.813→0.756 | +0.057 [-0.018, +0.132] | **+0.026** [-0.059, +0.113] |
| RA | AIUR | 1006 (583) | 0.611→0.559 | +0.052 [+0.011, +0.094] | 0.723→0.647 | +0.077 [+0.001, +0.152] | **-0.024** [-0.110, +0.061] |
| RB | AIUR | 1651 (350) | 0.871→0.771 | +0.100 [+0.055, +0.144] | 0.910→0.882 | +0.028 [-0.046, +0.102] | **+0.072** [-0.015, +0.158] |
| RC | AIUR | 506 (220) | 0.636→0.545 | +0.090 [+0.041, +0.140] | 0.672→0.562 | +0.110 [+0.031, +0.189] | **-0.019** [-0.111, +0.072] |
| CODEX | AIUR | 1795 (960) | 0.624→0.595 | +0.029 [-0.012, +0.070] | 0.716→0.673 | +0.043 [-0.032, +0.117] | **-0.014** [-0.098, +0.071] |
| TIME | AIUR | 1798 (438) | 0.856→0.425 | +0.431 [+0.285, +0.576] | 0.890→0.752 | +0.137 [+0.061, +0.214] | **+0.293** [+0.141, +0.446] |
| REPO(RA+RB+RC) | AR | 16290 (1710) | 0.733→0.714 | +0.018 [+0.009, +0.027] | 0.749→0.733 | +0.015 [+0.003, +0.028] | **+0.003** [-0.013, +0.019] |
| RA | AR | 7528 (1002) | 0.728→0.714 | +0.013 [+0.007, +0.019] | 0.745→0.732 | +0.013 [+0.004, +0.023] | **-0.000** [-0.012, +0.011] |
| RB | AR | 3167 (353) | 0.723→0.700 | +0.023 [+0.013, +0.033] | 0.736→0.691 | +0.045 [+0.016, +0.074] | **-0.022** [-0.050, +0.005] |
| RC | AR | 5595 (355) | 0.746→0.722 | +0.023 [+0.003, +0.043] | 0.761→0.759 | +0.002 [-0.008, +0.012] | **+0.021** [-0.001, +0.044] |
| CODEX | AR | 14265 (1644) | 0.746→0.665 | +0.081 [+0.074, +0.088] | 0.766→0.703 | +0.063 [+0.053, +0.072] | **+0.018** [+0.007, +0.029] |
| TIME | AR | 5420 (620) | 0.719→0.559 | +0.160 [+0.088, +0.233] | 0.714→0.682 | +0.032 [-0.001, +0.064] | **+0.129** [+0.068, +0.189] |
| REPO(RA+RB+RC) | DEL | 800 (127) | 0.750→0.734 | +0.016 [-0.007, +0.039] | 0.756→0.772 | -0.015 [-0.077, +0.046] | **+0.031** [-0.034, +0.096] |
| RA | DEL | 324 (52) | 0.699→0.679 | +0.020 [-0.020, +0.060] | 0.702→0.719 | -0.018 [-0.084, +0.049] | **+0.038** [-0.033, +0.108] |
| RB | DEL | 84 (25) | 0.787→0.785 | +0.002 [-0.032, +0.036] | 0.796→0.803 | -0.006 [-0.076, +0.063] | **+0.009** [-0.064, +0.082] |
| RC | DEL | 392 (50) | 0.784→0.769 | +0.015 [-0.017, +0.047] | 0.793→0.809 | -0.016 [-0.079, +0.048] | **+0.031** [-0.042, +0.104] |
| CODEX | DEL | 199 (12) | 1.000→0.523 | +0.477 [+0.418, +0.537] | 1.000→1.000 | +0.000 [-0.059, +0.059] | **+0.477** [+0.394, +0.561] |
| TIME | DEL | 83 (11) | 0.664→0.663 | +0.001 [-0.057, +0.059] | 0.672→0.652 | +0.021 [-0.041, +0.083] | **-0.020** [-0.113, +0.073] |

### ettin-68m (RA fold only; in-group = BOBOLINK s68v-all, one seed; training noise taken from e32)

| fold | segment | n (fam) | lin in→out | lin deg | e68 in→out | e68 deg | **lin − e68** [95% CI] |
|---|---|---|---|---|---|---|---|
| RA | **H-typed** | 768 (291) | 0.699→0.695 | +0.004 [-0.007, +0.016] | 0.780→0.774 | +0.006 [-0.012, +0.024] | **-0.001** [-0.023, +0.020] |
| RA | H-typed+ctx | 159 (105) | 0.671→0.671 | -0.000 [-0.033, +0.033] | 0.740→0.744 | -0.003 [-0.059, +0.052] | **+0.003** [-0.060, +0.066] |
| RA | H-template | 391 (258) | 0.988→0.979 | +0.009 [-0.001, +0.018] | 0.982→0.985 | -0.004 [-0.015, +0.007] | **+0.013** [-0.004, +0.029] |
| RA | AIUR | 130 (118) | 0.566→0.507 | +0.059 [+0.015, +0.103] | 0.702→0.693 | +0.009 [-0.078, +0.096] | **+0.050** [-0.048, +0.147] |
| RA | AR | 300 (187) | 0.743→0.721 | +0.022 [+0.003, +0.041] | 0.789→0.780 | +0.009 [-0.015, +0.034] | **+0.013** [-0.018, +0.043] |
| RA | DEL | 59 (32) | 0.642→0.654 | -0.013 [-0.064, +0.038] | 0.746→0.673 | +0.073 [-0.009, +0.155] | **-0.086** [-0.180, +0.008] |
| RA | AIUR | 1006 (583) | 0.611→0.559 | +0.052 [+0.011, +0.094] | 0.712→0.706 | +0.006 [-0.069, +0.081] | **+0.047** [-0.039, +0.132] |
| RA | AR | 7528 (1002) | 0.728→0.714 | +0.013 [+0.007, +0.019] | 0.767→0.757 | +0.009 [-0.000, +0.019] | **+0.004** [-0.008, +0.015] |
| RA | DEL | 324 (52) | 0.699→0.679 | +0.020 [-0.020, +0.060] | 0.725→0.697 | +0.028 [-0.035, +0.090] | **-0.008** [-0.081, +0.065] |

## Controls: training noise and data quantity

`results/controls.csv`. J(in-group, seed 0) − J(control) on the same fold items.
- **Seed** (same recipe, seed 1): linear H-typed −0.010 … +0.003 across folds (per-run SD 0.004); e32 H-typed −0.017 … +0.007 (SD 0.007);
  e32 AIUR up to −0.091 on RA (SD 0.034). Single-run encoder numbers on a few hundred AIUR items are not comparable at better than ±0.07.
- **Size** (drop random non-group families with the same number of train items as the fold removes): linear RA H-typed −0.003
  [−0.010, +0.005], CODEX/TIME within ±0.01 on every segment except AIUR (−0.026 on RA, i.e. noise). e32 RA H-typed −0.011, AR 0.000.
  So the out-of-group drops are not a data-quantity effect; they are about the held-out group.

## Mechanism: lexical novelty (`results/novelty_<SEG>.csv`, `scripts/novelty.py`, `scripts/novtab.py`)

Novelty = share of an item's distinct hashed word 1–2-grams (A-classic `raw_w`, text only) that never occur in the fold's training rows.
Mean novelty of held-out items: RA 0.025, RB 0.038, RC 0.023, CODEX 0.025, TIME 0.059 (vs 0.013–0.016 against the full train set).
Quintiles pooled over all five folds:

| H-typed novelty quintile (range) | n | lin in→out | lin deg | e32 in→out | e32 deg | lin − e32 |
|---|---|---|---|---|---|---|
| 1 (0) | 556 | 0.719→0.726 | −0.007 | 0.764→0.762 | +0.002 | −0.010 |
| 2 (0) | 556 | 0.737→0.730 | +0.007 | 0.788→0.763 | +0.025 | −0.018 |
| 3 (0–0.012) | 556 | 0.707→0.705 | +0.002 | 0.769→0.764 | +0.005 | −0.003 |
| 4 (0.012–0.025) | 556 | 0.671→0.650 | +0.021 | 0.725→0.716 | +0.008 | +0.013 |
| 5 (0.025–0.33) | 556 | 0.648→0.639 | +0.009 | 0.698→0.697 | +0.001 | +0.008 |

| AR novelty quintile (range) | n | lin in→out | lin deg | e32 in→out | e32 deg | lin − e32 |
|---|---|---|---|---|---|---|
| 1 (0) | 300 | 0.781→0.769 | +0.012 | 0.791→0.797 | −0.006 | +0.017 |
| 2 (0–0.011) | 300 | 0.766→0.693 | +0.073 | 0.799→0.742 | +0.057 | +0.017 |
| 3 (0.011–0.023) | 299 | 0.789→0.722 | +0.067 | 0.792→0.770 | +0.022 | +0.045 |
| 4 (0.023–0.042) | 300 | 0.731→0.681 | +0.050 | 0.768→0.727 | +0.041 | +0.010 |
| 5 (0.042–0.25) | 300 | 0.662→0.542 | +0.119 | 0.634→0.608 | +0.027 | +0.092 |

Single-seed (seed 0) in-group here. Slope of degradation on novelty, lin − e32 (family bootstrap): H-typed +0.39 per unit novelty
[+0.07, +0.78], i.e. about +0.01 J between the lowest and highest quintile; AR +1.37 [+0.58, +2.08], about +0.08 J at the top quintile.
Correlations of per-item degradation with novelty are small (H-typed lin 0.04, e32 −0.02; AR lin 0.17, e32 −0.02).
**Reading:** unseen vocabulary does explain part of the linear model's extra AR drop, concentrated in the most novel fifth of
items. For H-typed, novelty is too low and its effect too small to matter. The largest failures (TIME AIUR, Codex DEL) are new
formats and templates: a new automation's prompt, or JSON and blob payloads, whose label pattern the linear model never saw.

## Robustness perturbations (indicative; `results/perturb.csv`, `scripts/perturb.py`, `scripts/pscore.py`)

These are the shipped in-group models: Go V4-hybmaj run through the real Go `dlabel` binary, and BOBOLINK s32v-all and s68v-all.
The items are held-out H-typed y3 items (test + fresh, n = 1,494), and only items a perturbation actually changed are scored.
Labels are assumed unchanged. Markers (⟦…⟧) and fenced code are left untouched. Each cell is the J drop with a family-bootstrap CI.

| perturbation | n changed | linear | ettin-32m | ettin-68m | lin − e32 | lin − e68 |
|---|---|---|---|---|---|---|
| typos (~10% of words ≥ 4 chars, adjacent swap) | 1,042 | −0.016 [−0.024, −0.008] | −0.022 [−0.029, −0.015] | −0.013 [−0.020, −0.007] | +0.006 [−0.002, +0.014] | −0.003 |
| lowercase | 1,440 | 0.000 | −0.003 [−0.008, +0.003] | +0.001 | +0.003 | −0.001 |
| strip punctuation | 1,404 | **−0.057** [−0.069, −0.045] | −0.041 [−0.051, −0.032] | −0.029 [−0.038, −0.021] | −0.015 [−0.025, −0.006] | −0.028 [−0.039, −0.017] |
| expand contractions | 590 | −0.003 | −0.004 | +0.001 | +0.001 | −0.004 |
| contract | 268 | 0.000 | −0.003 | 0.000 | +0.003 | 0.000 |
| extra spaces and blank lines | 1,494 | −0.001 | **−0.036** [−0.044, −0.029] | −0.009 [−0.014, −0.005] | +0.035 [+0.028, +0.044] | +0.009 [+0.003, +0.015] |
| no space after punctuation | 991 | −0.003 | −0.005 | −0.001 | +0.002 | −0.002 |
| add greeting ("hey, ") | 1,494 | −0.020 [−0.028, −0.012] | −0.015 [−0.022, −0.009] | 0.000 | −0.005 | −0.020 [−0.028, −0.012] |
| swap greeting/sign-off style | 3 | too few items | | | | |

Consistency check: the encoder's unperturbed probabilities match BOBOLINK's saved test/fresh probabilities (max |Δp| 5e-4, from bf16).

## Adaptation curves (`results/adapt_<FOLD>.csv`, `scripts/adapt.py`, `scripts/adapt_md.py`)

k = number of the held-out group's own labeled train items added. The samples are nested; k/2 are human-segment items
(about 60% of those are H-typed) and k/2 are machine items.
- **Linear, retrain:** source train + k, the full V4 recipe (about 1 min on CPU). This needs the source corpus, so a shipped model can't do it.
- **Linear, warm:** start from the shipped out-of-group weights, train on the k items only (Adam, batch 128, 3 epochs, lr 0.01, or lr 0.003 for "warm-lo").
  This takes 1–6 s of training plus feature loading, and uses the same feature columns, so it is deployable as a local Go retrain.
- **Encoder, continue:** start from the out-of-group e32 and train on the k items only, 2 epochs at lr 3e-5 (BOBOLINK's continuation recipe).
  About 15 s of training on MPS.

"% closed" is (J_k − J_out) / (J_in − J_out), shown only when the gap exceeds 0.003. Single seed; read it against the per-run SDs above.

**RA / H-typed** (n=768). Linear in-group 0.694, out 0.695; e32 in-group 0.750, out 0.745.

| k | lin retrain (source + k) | lin warm (k only, lr .01) | lin warm-lo (k only, lr .003) | e32 continue (k only) |
|---|---|---|---|---|
| 250 | 0.695 (+0.001 vs in) | 0.699 (+0.005 vs in) | 0.697 (+0.003 vs in) | 0.747 (46% closed, -0.003 vs in) |
| 1000 | 0.694 (-0.000 vs in) | 0.698 (+0.004 vs in) | 0.696 (+0.003 vs in) | 0.752 (136% closed, +0.002 vs in) |
| 4000 | 0.697 (+0.003 vs in) | 0.703 (+0.009 vs in) | 0.700 (+0.006 vs in) | 0.759 (262% closed, +0.009 vs in) |

**RA / AR** (n=300). Linear in-group 0.747, out 0.721; e32 in-group 0.762, out 0.752.

| k | lin retrain (source + k) | lin warm (k only, lr .01) | lin warm-lo (k only, lr .003) | e32 continue (k only) |
|---|---|---|---|---|
| 250 | 0.719 (-6% closed, -0.028 vs in) | 0.719 (-7% closed, -0.028 vs in) | 0.717 (-14% closed, -0.030 vs in) | 0.750 (-17% closed, -0.012 vs in) |
| 1000 | 0.726 (21% closed, -0.021 vs in) | 0.712 (-33% closed, -0.035 vs in) | 0.719 (-6% closed, -0.028 vs in) | 0.753 (17% closed, -0.009 vs in) |
| 4000 | 0.722 (5% closed, -0.025 vs in) | 0.722 (7% closed, -0.025 vs in) | 0.718 (-8% closed, -0.029 vs in) | 0.755 (35% closed, -0.007 vs in) |

**RA / AIUR** (n=130). Linear in-group 0.549, out 0.507; e32 in-group 0.675, out 0.621.

| k | lin retrain (source + k) | lin warm (k only, lr .01) | lin warm-lo (k only, lr .003) | e32 continue (k only) |
|---|---|---|---|---|
| 250 | 0.544 (88% closed, -0.005 vs in) | 0.515 (20% closed, -0.034 vs in) | 0.507 (2% closed, -0.041 vs in) | 0.666 (83% closed, -0.009 vs in) |
| 1000 | 0.533 (62% closed, -0.016 vs in) | 0.514 (18% closed, -0.035 vs in) | 0.524 (41% closed, -0.025 vs in) | 0.647 (49% closed, -0.027 vs in) |
| 4000 | 0.530 (55% closed, -0.019 vs in) | 0.490 (-39% closed, -0.058 vs in) | 0.534 (66% closed, -0.015 vs in) | 0.664 (81% closed, -0.010 vs in) |

**CODEX / H-typed** (n=1009). Linear in-group 0.698, out 0.684; e32 in-group 0.753, out 0.734.

| k | lin retrain (source + k) | lin warm (k only, lr .01) | lin warm-lo (k only, lr .003) | e32 continue (k only) |
|---|---|---|---|---|
| 250 | 0.690 (44% closed, -0.008 vs in) | 0.692 (58% closed, -0.006 vs in) | 0.685 (6% closed, -0.013 vs in) | 0.736 (12% closed, -0.017 vs in) |
| 1000 | 0.692 (58% closed, -0.006 vs in) | 0.691 (51% closed, -0.007 vs in) | 0.691 (46% closed, -0.007 vs in) | 0.744 (56% closed, -0.008 vs in) |
| 4000 | 0.700 (111% closed, +0.002 vs in) | 0.697 (90% closed, -0.001 vs in) | 0.694 (67% closed, -0.005 vs in) | 0.756 (119% closed, +0.004 vs in) |

**CODEX / AR** (n=580). Linear in-group 0.757, out 0.660; e32 in-group 0.773, out 0.709.

| k | lin retrain (source + k) | lin warm (k only, lr .01) | lin warm-lo (k only, lr .003) | e32 continue (k only) |
|---|---|---|---|---|
| 250 | 0.664 (4% closed, -0.093 vs in) | 0.672 (13% closed, -0.085 vs in) | 0.660 (0% closed, -0.097 vs in) | 0.703 (-9% closed, -0.070 vs in) |
| 1000 | 0.707 (48% closed, -0.051 vs in) | 0.702 (43% closed, -0.055 vs in) | 0.670 (10% closed, -0.087 vs in) | 0.694 (-23% closed, -0.079 vs in) |
| 4000 | 0.708 (49% closed, -0.049 vs in) | 0.720 (62% closed, -0.037 vs in) | 0.697 (38% closed, -0.060 vs in) | 0.724 (23% closed, -0.049 vs in) |

**CODEX / AIUR** (n=218). Linear in-group 0.610, out 0.567; e32 in-group 0.708, out 0.699.

| k | lin retrain (source + k) | lin warm (k only, lr .01) | lin warm-lo (k only, lr .003) | e32 continue (k only) |
|---|---|---|---|---|
| 250 | 0.625 (132% closed, +0.014 vs in) | 0.558 (-20% closed, -0.052 vs in) | 0.561 (-13% closed, -0.049 vs in) | 0.690 (-102% closed, -0.018 vs in) |
| 1000 | 0.583 (37% closed, -0.028 vs in) | 0.567 (1% closed, -0.043 vs in) | 0.553 (-32% closed, -0.058 vs in) | 0.690 (-96% closed, -0.017 vs in) |
| 4000 | 0.567 (0% closed, -0.043 vs in) | 0.540 (-62% closed, -0.070 vs in) | 0.543 (-54% closed, -0.067 vs in) | 0.709 (120% closed, +0.002 vs in) |

**TIME / H-typed** (n=371). Linear in-group 0.684, out 0.676; e32 in-group 0.741, out 0.755.

| k | lin retrain (source + k) | lin warm (k only, lr .01) | lin warm-lo (k only, lr .003) | e32 continue (k only) |
|---|---|---|---|---|
| 250 | 0.670 (-86% closed, -0.014 vs in) | 0.673 (-43% closed, -0.011 vs in) | 0.674 (-34% closed, -0.010 vs in) | 0.745 (71% closed, +0.004 vs in) |
| 1000 | 0.678 (29% closed, -0.005 vs in) | 0.678 (25% closed, -0.006 vs in) | 0.678 (28% closed, -0.005 vs in) | 0.746 (66% closed, +0.004 vs in) |
| 4000 | 0.681 (70% closed, -0.002 vs in) | 0.680 (48% closed, -0.004 vs in) | 0.682 (74% closed, -0.002 vs in) | 0.733 (161% closed, -0.008 vs in) |

**TIME / AR** (n=250). Linear in-group 0.730, out 0.610; e32 in-group 0.735, out 0.705.

| k | lin retrain (source + k) | lin warm (k only, lr .01) | lin warm-lo (k only, lr .003) | e32 continue (k only) |
|---|---|---|---|---|
| 250 | 0.661 (42% closed, -0.069 vs in) | 0.620 (8% closed, -0.110 vs in) | 0.614 (3% closed, -0.116 vs in) | 0.698 (-22% closed, -0.037 vs in) |
| 1000 | 0.696 (71% closed, -0.034 vs in) | 0.632 (18% closed, -0.098 vs in) | 0.612 (1% closed, -0.118 vs in) | 0.716 (36% closed, -0.019 vs in) |
| 4000 | 0.702 (77% closed, -0.028 vs in) | 0.657 (39% closed, -0.072 vs in) | 0.627 (14% closed, -0.102 vs in) | 0.702 (-9% closed, -0.033 vs in) |

**TIME / AIUR** (n=218). Linear in-group 0.879, out 0.445; e32 in-group 0.893, out 0.758.

| k | lin retrain (source + k) | lin warm (k only, lr .01) | lin warm-lo (k only, lr .003) | e32 continue (k only) |
|---|---|---|---|---|
| 250 | 0.599 (35% closed, -0.281 vs in) | 0.513 (16% closed, -0.366 vs in) | 0.450 (1% closed, -0.430 vs in) | 0.802 (33% closed, -0.091 vs in) |
| 1000 | 0.698 (58% closed, -0.181 vs in) | 0.597 (35% closed, -0.282 vs in) | 0.450 (1% closed, -0.430 vs in) | 0.814 (42% closed, -0.079 vs in) |
| 4000 | 0.771 (75% closed, -0.108 vs in) | 0.758 (72% closed, -0.121 vs in) | 0.495 (12% closed, -0.384 vs in) | 0.865 (79% closed, -0.028 vs in) |

## Cost

| | linear V4 recipe | ettin-32m (s32v-all recipe) | ettin-68m (s68v-all recipe) |
|---|---|---|---|
| train one fold | 47–65 s on CPU (8 threads), 7–8.5 GB RSS | 6.3–9.4 min train compute, 13–17 min wall on MPS, 15.7–16.9 GB MPS | 28 min train compute, 66 min wall on MPS (RA), 21.7 GB MPS |
| adapt on k ≤ 4,000 local items | 1–6 s train + ~8 s feature load (Python; Go equivalent unbuilt) | ~15 s train + ~1.5 min predict on MPS | – |
| inference | Go 16k items/s on 4 workers (perturbation run) | ~1,000 items/s on MPS | ~420 items/s on MPS |

Spend: $0 (no labeling).
- GPU: 5 × 32m folds, 1 × 32m size control, 1 × 32m seed control, 1 × 68m fold, 9 short adaptation continuations and 2 perturbation inference runs.
- Memory: while a linear job overlapped an encoder fold, the total was about 24–26 GB, a brief overrun of the 24 GB budget by up to ~2 GB, for about 30 min in total.

## Recommendations

1. **For shipping typed-human-text labels, generalization does not decide the choice between linear and encoder.**
   On H-typed the two degrade alike under new repos, a new provider and later time, within ±0.01–0.02.
   The decision rests on the level gap, about 0.05 J in favour of the encoder in every fold, which matches DOTTEREL/BOBOLINK in-distribution.
2. **For machine text (AR, AIUR, DEL), expect the linear model to break harder on new formats.**
   - Examples: a new provider's agent replies, new automation templates, JSON or blob payloads.
   - If the linear tier serves AR or DEL (BUFFLEHEAD B), add drift detection.
   - Signals: the share of an item's hashed features unseen in training, label-rate shifts per provider, and items with no letters.
   - Fall back to the encoder, or locally adapt, when the signals fire.
3. **A local warm-start retrain is a credible mitigation for the linear tier.**
   - With about 1–4k locally labeled items (LLM-labeled on the user's machine), it recovers 40–70% of the large machine-segment gaps in seconds, without the source corpus.
   - Tune its learning rate on a separate fold: lr 0.003 recovered much less than lr 0.01.
   - It is not needed for H-typed.
4. **Report encoder comparisons with at least two seeds.** Seed-to-seed J differences on fold-sized eval sets (0.01 H-typed, 0.03–0.09 AIUR)
   are as large as many claimed effects. This also bears on round-2 single-run deltas.
5. **The GANNET DEL anomaly** fits the Codex DEL finding: machine payloads in delegation slots. Check GANNET's 84 DEL items for [JSON task payloads; key name omitted]
   or encrypted blobs before suspecting the input pipeline.

## Open questions

- Folds are within one user's library; other people's typing style is IBIS's question. The perturbations hint at the style dimensions
  that matter: punctuation for both models, whitespace for the 32m.
- Only one seed for the out-of-group runs, and for ettin-68m. The pooled repo row treats the three fold runs' noise conservatively, as if it did not average out.
- The two largest repositories [names omitted] (65% of families) were never held out.
- Adaptation used y3/y_hf/Flash labels as the local labels; a real user would have only their local LLM's labels.

## Files (`~/pharos-labeling/studies/HERON/`)

- `data/`
  - `item_meta.parquet` (item → repo, provider, segment, family)
  - `unit_repo.parquet`
  - `folds/<F>.{excl,eval,dev_ok,adapt<k>}.txt`, `folds.json`
  - `novelty.parquet`
  - `perturb.parquet`, `perturb_items.jsonl` (corpus text; local only)
- `scripts/`
  - training copies: `hb_train.py`, `hexp.py`; shared helpers `common.py`, `targets.py` (copies)
  - folds and metadata: `ws_repo.py`, `build_meta.py`, `folds.py`, `adapt_ids.py`
  - scoring and analysis: `hscore.py`, `degr.py` (`--avg-in`), `controls.py`, `adjci.py`, `adapt.py`, `adapt_md.py`, `novelty.py`, `novtab.py`, `perturb.py`, `pscore.py`
  - tables: `table.py`, `report_tables.py`
  - queues: `gq*.sh`, `lq*.sh`, `gpert.sh`
- `models/<run>/`: encoder runs, with `pred_probs.npz` (eval + non-group dev items; heads y_hf, haiku, flash, argon, y3) or dev/test/fresh probs.
- `probs/LIN-*.npz`, `models/LIN-*.npz`: linear runs.
- `results/`
  - `items/<F>__<model>.parquet`: per-item J
  - `degradation{,_avgin,_adj}.csv`, `controls.csv`, `adapt_<F>.csv`, `novelty_<SEG>.csv`, `perturb.csv`, `perturb_V4.npy`
