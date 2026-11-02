> Sanitized copy of ~/pharos-labeling/studies/IBIS/REPORT.md (archived in full on euclid at "Pharos Labeling/2026-10-29-bird-studies/studies/IBIS"). Verbatim conversation text was removed.

# IBIS: shipped labelers on other people's data (round 3)

**Question.** How do the shipped candidates (pure-Go linear DOTTEREL `V4-hybmaj`, and the ettin encoders) do on **other people's**
coding-agent conversations, zero-shot, and how much does each recover with a little adaptation? H-typed first.

## Answer (numbers first)

- **Both degrade far more on other users than on new conversations from the same library (GANNET).** H-typed, student vs
  Flash∩Argon, 1,682 items / 911 families from four public populations:

  | model | in-library | other users | drop |
  |---|---|---|---|
  | Go linear V4-hybmaj | 0.708 | **0.540** | −0.167 [−0.191, −0.143] |
  | encoder s68v-all (68m) | 0.772 | **0.657** | −0.115 [−0.140, −0.089] |
  | encoder c68v-hum (68m) | 0.768 | 0.648 | −0.121 [−0.145, −0.097] |

  The teachers do not get worse: Flash vs Argon is 0.795 on these items, against 0.768 in-library. So this is student degradation.
- **The encoder holds up better.** The linear model loses **0.052 [0.032, 0.073] more** than s68v-all on H-typed (paired
  difference of drops), 0.061 [0.039, 0.083] on the three coding-agent sources alone. On other users the encoder leads by
  **+0.117 [+0.100, +0.134]**, against +0.064 in-library. In every source the linear model drops more; the CI excludes zero in 3 of 4.
- **Adaptation widens the gap.** With 1,765 labeled items from half the families (H-typed 866), on the other half:

  | H-typed vs F∩A, held-out half B | zero-shot | + thresholds | + weights + thresholds |
  |---|---|---|---|
  | V4 linear | 0.540 | 0.583 (+0.043) | **0.586 (+0.046 [+0.024, +0.064])** |
  | s68v-all | 0.659 | 0.670 (+0.011) | **0.719 (+0.060 [+0.042, +0.077])** |
  | c68v-hum | 0.646 | 0.678 (+0.033) | **0.711 (+0.066 [+0.048, +0.088])** |

  The linear model gets most of its gain from re-tuned thresholds; its weights barely move it (+0.003 beyond thresholds).
  After adaptation the encoder leads by 0.133 [0.111, 0.157], against 0.119 zero-shot. The adapted encoder is still
  0.05 below its in-library level; the adapted linear model is still 0.12 below its.
- **Agent replies (AR) degrade about equally.** Drops are −0.21 (linear) and −0.18 to −0.20 (encoders); the linear model loses
  0.010–0.029 more (CIs include zero for the 68m models; s32v-all −0.029 [−0.051, −0.008]). On the coding-agent sources alone (excluding
  ChatGPT chat replies), the linear model loses 0.028 [0.004, 0.053] more than s68v-all. After adaptation: AR linear 0.583,
  s68v-all 0.663 (+0.082).
- **Adapting on other public populations doesn't transfer.** In leave-one-source-out runs, the gain on the unseen source
  averages +0.00–0.03 for both models. Recovery needs labels from the target population.
- **Verdict.** On other people's data, **the encoders hold up better**: by about **0.05 more retained J on H-typed zero-shot**
  (about 0.03 on AR), and by **0.12–0.13 absolute** after a little adaptation. The Go linear model drops to 0.54 on other
  people's typed text and predicts Argon 0.29 worse than Flash does (−0.09 in-library), which is not shippable as the human-text labeler without
  local labels. Neither model is "ship and forget" for other users: even s68v-all loses 0.115 on H-typed.

## Sources and licenses

All downloaded to `downloads/<source>/`, treated as untrusted data (read with `python -I`, nothing executed). Not in git.

| code | source | population | license | in the sample (H-typed / ctx / AR) |
|---|---|---|---|---|
| AT | 58 Hugging Face `format:agent-traces` datasets: raw Claude Code JSONL (17 datasets) and raw Codex rollouts (13), surveyed and filtered for interactive human use (`downloads/agent-traces/MANIFEST.json`, 137 skipped with reasons: no or `other` license, forks, benchmarks, synthetic prompts) | 18 uploaders, mostly individual developers and hackathon teams, 2026 | MIT 40, Apache-2.0 9, CC-BY-4.0 6, CC0 1, BSD-3 1, AGPL-3.0 1 (cfahlgren1/Fable-5-traces, research use only) | 339 / 127 / 250, 80 families |
| PI | the pi-format datasets of the same survey (pi coding agent sessions, `pi-share-hf`); 968 sessions | one heavy user (thomasmustier, about 80% of messages) plus ~20 others | MIT mostly; Apache-2.0, CC-BY-4.0, BSD-3 | 484 / 173 / 250, 396 families |
| OH | `OpenHands/openhands-feedback` (275 trajectories users shared with feedback, 2024) | OpenHands users | MIT | 480 / 137 / 250, 202 families |
| WC | `allenai/WildChat-1M`, shard 13 of 14: English, non-toxic, first user turn with ≥ 2 coding keywords; 3,000 of 8,404 candidates | ChatGPT users asking coding questions (chat, not an agent) | ODC-BY | 379 / 281 / 250, 668 families |

## Pipeline (the real Pharos pipeline) and fidelity caveats

1. **Session files.** AT files are **raw** Claude Code / Codex session files, copied unchanged. PI, OH and WC were converted to
   Claude Code JSONL by `scripts/convert.py`:
   - user text becomes `type:user` lines;
   - agent text becomes `type:assistant` text blocks;
   - tool calls and observations become `tool_use` / `tool_result` pairs;
   - every line has `entrypoint: "cli"`, `userType: "external"` and the source's timestamps.
   PI keeps its tree (`id`/`parentId` → `uuid`/`parentUuid`), so Pharos's own branch selection applies.
2. **Throwaway library.** `pharos init-library lib`, with `[[sources]]` only for `IBIS/sessions/{claude,codex}` (no default
   sources, no `~/.claude`, no service). Then CLI `ingest` (4,448 conversations) and `build-tools`.
3. **Snapshot and prepare.** The catalog was cloned (`cp -c`) to `snap3/`, then
   `pharos labeling prepare --snapshot-copy` built `dataset/` (121,313 items). Binary built from `seoul-v13` 86777936, whose
   labeling code equals 0cd9422. So human pieces, authorship, context-v3, chunking, families and dedup are the real pipeline.
4. **Segments.** Applied AVOCET's rules to the dataset's `pieces` table, restricted to each item's visible range.
5. **Sample** (`scripts/sample.py`, seeded): one item per `target_hash`, no `duplicate_of`, per-family caps. Halves are split by
   family hash: A (adaptation) and B (evaluation).
6. **Labels.** Through the runner only, with the round-1 routes, the default prompt and 4 items per request:
   - Flash: `agy gemini-3.8-flash low`;
   - Argon: `agy gemini-4-argon medium`;
   - `--refusal-registry ~/pharos-labeling/labeling-refusals.jsonl`.
   Flash labeled all 3,401 items, with no drops this time. Argon labeled 3,385; a one-item-per-request repair (`ibis-argon-r1`)
   brought it to 3,399. Label tables come from `scripts/build_labels.py` (EGRET's, with the dataset path changed).

**Fidelity caveats.**
- Converted sources (PI, OH, WC) are labeled with provider `claude`. Their tool and observation structure is approximated.
  OpenHands "thought" text becomes assistant text before a tool call, as Claude Code writes it.
- WC is a chat product, not a coding agent. Its "agent replies" are long ChatGPT answers (median 2.5k characters), which explains
  the AR collapse there (0.37–0.40 for every model). The AGENT rows exclude WC.
- AT and PI messages can include agent-to-agent traffic that the authorship classifier calls human. That is what Pharos would do
  on those users' libraries too.
- The populations differ from the in-library user in language and length:
  - non-English share of H-typed: PI 17%, others 2–4%;
  - median H-typed length: 48–60 characters (WC 177), against 139 in-library;
  - F∩A labels per item: 3.9–4.6, against 5.5 in-library.
  The students under-label further still: V4 averages 3.1 labels per item, s68v-all 3.1–4.0. The largest error is missed
  `P.request.work.implement`.
- The 2-teacher fair comparison (student vs Argon beside Flash vs Argon) is used as in GANNET; there is no Haiku (round-3 rule).
  "In-library" is the same metric on AVOCET v2 test+fresh (`calibrate_gannet.py`; same numbers in `results/zs.txt`, rows `lib`).
- The highest-fidelity source, AT (raw files, no conversion), shows the same pattern as the converted ones. Its linear drop is
  −0.165, against −0.074 for s68v-all, so the result is not a conversion artifact.

## Zero-shot (all items with both teachers; family-cluster bootstrap, 1,000 resamples)

`results/zs.txt` and `results/zs.csv` hold every model × source × segment. Columns:
- **F∩A**: student vs Flash∩Argon;
- **dF**: student vs Argon − Flash vs Argon;
- **drop**: change from in-library on F∩A;
- **ddrop**: linear − encoder difference of drops.

**H-typed, all sources (n = 1,682, 911 families, Flash vs Argon 0.795; in-library 0.768):**

| model | in-library F∩A | other users F∩A | drop | dF in-library → other | linear − enc (other) | ddrop (lin − enc) |
|---|---|---|---|---|---|---|
| V4-hybmaj (Go linear) | 0.708 | 0.540 [0.521, 0.560] | −0.167 | −0.088 → −0.286 | | |
| V5-hybmaj-150k (Go linear) | 0.706 | 0.541 | −0.165 | −0.086 → −0.283 | | |
| s68v-all | 0.772 | 0.657 [0.634, 0.678] | −0.115 | −0.041 → −0.176 | −0.117 [−0.134, −0.100] | **−0.052 [−0.073, −0.032]** |
| c68v-hum | 0.768 | 0.648 [0.625, 0.668] | −0.121 | −0.041 → −0.182 | −0.107 [−0.126, −0.089] | −0.046 [−0.067, −0.026] |
| r1 c-e68-512-all | 0.747 | 0.619 | −0.129 | −0.061 → −0.212 | −0.078 | −0.038 [−0.058, −0.019] |
| s32v-all | 0.752 | 0.605 | −0.147 | −0.054 → −0.219 | −0.065 | −0.020 [−0.039, −0.002] |
| c32v-hum | 0.750 | 0.612 | −0.138 | −0.070 → −0.217 | −0.072 | −0.029 [−0.048, −0.011] |
| r1 c-e32-512-all | 0.730 | 0.555 | −0.176 | −0.075 → −0.272 | −0.015 | +0.009 [−0.009, +0.026] |

**H-typed by source** (F∩A; drop; ddrop for V4 − s68v-all):

| source | n / families | F vs A | V4 | s68v-all | c68v-hum | ddrop V4 − s68v-all |
|---|---|---|---|---|---|---|
| AT (raw Claude Code / Codex) | 339 / 66 | 0.780 | 0.542 (−0.165) | 0.698 (−0.074) | 0.681 (−0.087) | −0.091 [−0.128, −0.054] |
| PI (pi agent) | 484 / 297 | 0.782 | 0.520 (−0.187) | 0.650 (−0.122) | 0.648 (−0.120) | −0.066 [−0.101, −0.030] |
| OH (OpenHands) | 480 / 170 | 0.825 | 0.613 (−0.095) | 0.713 (−0.059) | 0.709 (−0.060) | −0.036 [−0.064, −0.008] |
| WC (ChatGPT coding chat) | 379 / 379 | 0.788 | 0.473 (−0.235) | 0.559 (−0.212) | 0.540 (−0.228) | −0.022 [−0.062, +0.017] |
| AGENT (AT+PI+OH) | 1,303 / 533 | 0.797 | 0.560 (−0.148) | 0.686 (−0.086) | 0.679 (−0.089) | −0.061 [−0.083, −0.039] |

**H-typed+ctx** (n = 718): V4 0.466 (−0.207), s68v-all 0.575 (−0.172), c68v-hum 0.581 (−0.178). ddrop V4 − s68v-all is
−0.036 [−0.078, +0.007].

**AR** (n = 999; Flash vs Argon 0.718, against 0.781 in-library):

| model | in-library | all sources | drop | ddrop (lin − enc) | AGENT only (no WC) | ddrop AGENT |
|---|---|---|---|---|---|---|
| V4 | 0.763 | 0.554 | −0.209 | | 0.607 (−0.156) | |
| s68v-all | 0.786 | 0.587 | −0.199 | −0.010 [−0.034, +0.014] | 0.658 (−0.128) | −0.028 [−0.053, −0.004] |
| c68v-hum | 0.777 | 0.594 | −0.183 | −0.025 [−0.052, +0.002] | 0.660 (−0.117) | −0.039 [−0.066, −0.012] |
| r1 c-e68 | 0.803 | 0.613 | −0.189 | −0.019 [−0.043, +0.004] | 0.684 (−0.118) | −0.037 [−0.060, −0.015] |
| s32v-all | 0.778 | 0.599 | −0.179 | −0.029 [−0.051, −0.008] | 0.659 (−0.119) | −0.037 [−0.060, −0.014] |

## Adaptation (train on half A, score on half B: 1,636 items in disjoint families)

`results/adapt.txt` and `results/adapt.csv`. Target is Flash∩Argon, else Flash. Levels:
- **thr**: per-segment thresholds re-tuned on A (`rlh.tune_thresholds`, `per_label_jaccard`);
- **w**: weights adapted on A;
- **w+thr**: both.

How each model adapts:
- **Linear (`scripts/adapt_lin.py`).** Features come from DOTTEREL's Python reference scorer (refscore.Model, the Go parity
  oracle; max |Δlogit| vs the Go binary is 5e-5). W and b are fine-tuned from the shipped weights with BCE and Adam.
  Learning rate, epochs and an L2 pull toward the shipped weights were chosen by inner 2-fold CV over A families from 18
  configs; it picked lr 0.01, 3 epochs, L2 0. This takes about 1 min on CPU. The vocabulary is fixed: new n-grams outside the
  exported tables cannot be learned. A full retrain with vocabulary refit (dexp with IBIS rows appended) was not done.
- **Encoder (`scripts/adapt_enc.py`).** Training continues from the shipped weights: 2 epochs, AdamW, lr 2e-5 (head 1e-4),
  batch 16, BCE on the y3 and y_hf heads that the scorer reads, with framing as at training. No tuning. It took 12.5 min per 68m
  model and 4.5 min for 32m, on 8 CPU threads.

**H-typed, half B (n = 816, 450 families):**

| model | zs | thr | w | w+thr | gain w+thr | linear − this (w+thr) |
|---|---|---|---|---|---|---|
| V4 | 0.540 | 0.583 | 0.564 | **0.586** | +0.046 [+0.024, +0.064] | |
| s68v-all | 0.659 | 0.670 | 0.706 | **0.719** | +0.060 [+0.042, +0.077] | −0.133 [−0.157, −0.111] |
| c68v-hum | 0.646 | 0.678 | 0.706 | 0.711 | +0.066 [+0.048, +0.088] | −0.125 [−0.148, −0.102] |
| r1 c-e68 | 0.614 | 0.643 | | 0.694 | +0.080 [+0.062, +0.100] | −0.108 |
| s32v-all | 0.608 | 0.656 | | 0.689 | +0.081 [+0.062, +0.103] | −0.103 |
| c32v-hum | 0.612 | 0.640 | | | +0.028 (thr only) | |

**By source** (H-typed, w+thr):

| source | V4 | s68v-all | c68v-hum |
|---|---|---|---|
| AT | 0.536 → 0.612 (+0.075) | 0.675 → 0.708 (+0.033) | 0.687 → 0.708 |
| PI | 0.509 → 0.522 (+0.014) | 0.651 → 0.696 (+0.044) | 0.637 → 0.683 |
| OH | 0.654 → 0.674 (+0.020) | 0.749 → 0.770 (+0.021) | 0.742 → 0.758 |
| WC | 0.473 → 0.557 (+0.084) | 0.563 → 0.712 (+0.149) | 0.517 → 0.707 (+0.190) |

**H-typed+ctx** (n = 353): V4 0.478 → 0.533 (+0.055), s68v-all 0.595 → 0.668 (+0.073), c68v-hum 0.572 → 0.646 (+0.074).

**AR** (n = 465): V4 0.546 → 0.583 (+0.038), s68v-all 0.581 → 0.663 (+0.082), c68v-hum 0.601 → 0.665, r1-e68 0.622 → 0.669,
s32v-all 0.595 → 0.631.

**Learning curve** (H-typed half B; weights and thresholds both learned on the same fraction of A families; `results/curve.txt`):

| labeled items in A (H-typed) | V4 w+thr | s68v-all w | s68v-all w+thr |
|---|---|---|---|
| 0 | 0.540 | 0.659 | 0.659 |
| 580 (~290) | 0.574 (+0.034) | 0.687 (+0.028) | 0.676 (+0.017) |
| 995 (~490) | 0.577 (+0.037) | 0.694 (+0.034) | 0.697 (+0.038) |
| 1,765 (866) | 0.586 (+0.046) | 0.706 (+0.047) | 0.719 (+0.060) |

The linear model gets nearly all of its gain at the smallest size (thresholds); the encoder keeps improving with more labels.
Thresholds tuned on about 290 H-typed items are noisy for the encoder: w+thr is below w at 25%.

**Leave-one-source-out** (does adapting on *other* public populations help an *unseen* one?). Adapt on half A of the other
three sources, then score half B of the held-out source (`results/loso-*.txt`, `scripts/loso.sh`). H-typed gains over
zero-shot:

| held-out | n / families | V4 thr | V4 w+thr | s68v-all thr | s68v-all w+thr | s68v-all − V4 after w+thr |
|---|---|---|---|---|---|---|
| AT | 197 / 31 | +0.079 [+0.034, +0.104] | +0.050 | +0.016 | +0.026 [−0.004, +0.050] | +0.115 |
| PI | 250 / 152 | +0.024 | −0.032 | +0.005 | +0.017 [−0.012, +0.045] | +0.192 |
| OH | 185 / 83 | +0.008 | −0.038 | −0.014 | −0.004 [−0.033, +0.024] | +0.128 |
| WC | 184 / 184 | +0.019 | +0.031 | +0.005 | +0.008 [−0.022, +0.037] | +0.067 |
| n-weighted mean | 816 | +0.033 | +0.001 | +0.003 | +0.012 | +0.13 |

- **Adaptation on other people's public data barely transfers to a new population.** In-population adaptation gave
  +0.046 (V4) and +0.060 (s68v-all); out-of-population it gives about +0.00–0.03.
- For the linear model, thresholds re-tuned on other users help a little (+0.03). Its weights adapted on other populations
  often hurt the unseen one.
- So the gains above come from **labels from the same population**, not from "public data" in general.

## What worked, what didn't

- **Worked.** The real Pharos pipeline on public data, end to end, with no live catalog and only the CLI: conversion → throwaway
  library → `prepare --snapshot-copy`. Raw Claude Code and Codex files from HF ingest unchanged.
- **Didn't.** Weight adaptation of the linear model adds almost nothing beyond thresholds: +0.003 on H-typed, −0.000 on AR.
  Most of what the linear model misses on new users is vocabulary and phrasing it has no features for.
- **Surprise.** The teachers agree *better* on other users' typed text (0.795 vs 0.768): shorter, simpler requests. So the
  students' drop is not label noise.
- **Not tested:**
  - retraining the linear model with a vocabulary refit on library + public data;
  - LoRA adapters;
  - longer encoder adaptation or hyperparameter search (the encoder was not tuned, the linear model was);
  - Haiku as a third teacher.

## Cost

- **Labels: $32.72.**
  - Flash $6.75: 3,401 items, 851 requests, about $2.0 per 1k.
  - Argon $25.97: $25.63 plus the $0.34 repair; 3,399 items, about $7.6 per 1k. A further $0.10 is "possibly charged" on 2
    failed attempts.
  - No Haiku. No quota errors.
- **Compute.** All CPU, no GPU.
  - Ingest: about 4 + 1 + 13 min.
  - Prepare: 51–89 s, about 0.75 GB RSS.
  - Zero-shot inference on 3,401 items: Go linear 12.4k items/s on 4 workers; 68m encoders about 40 items/s and 32m about
    100–120 items/s (fp32, 8 threads, shared machine).
  - Encoder adaptation: about 6.3 GB RSS for 68m.
- **Disk:** downloads 1.6 GB, sessions 0.97 GB, throwaway library 2.9 GB, snapshots 4.8 GB (snap1 and snap2 are superseded test snapshots; nothing deleted), dataset 1.1 GB.

## Recommendations

1. **For typed human text, ship the encoder (s68v-all or c68v-hum), not the Go linear model,** to users other than this one.
   On other people's data, the linear model's H-typed quality (0.54) is far below the encoder's (0.66), and the gap (0.12) is
   twice the in-library gap.
2. **Plan for per-user or public-data adaptation.** Even the encoder loses about 0.11 on H-typed.
   - The cheapest fix is per-segment thresholds tuned on a small labeled sample: +0.01 to +0.05.
   - A short continued training on about 1.8k labeled items from the same population gives +0.06 to +0.08.
   - Adapting on *other* public populations does not transfer (LOSO: +0.00 to +0.03). A per-user calibration set, a little LLM
     labeling of the user's own library (about $10 per 1.7k items with Flash + Argon), is what buys the recovery.
3. **Keep the Go linear model only as a cheap tier where quality loss is tolerable:** AR and DEL backfill. Re-tune its
   thresholds per user if labels exist. On AR it is within about 0.03 of the encoders on other users.
4. **Retrain on mixed-population data.** The round-2 models are trained on one person's library. Adding the public sources
   (about 7k usable human messages found, licenses permitting) to training is the obvious next experiment for both families.

## Open questions

- Does a vocabulary-refit linear retrain on library + public data close the gap, or is it a capacity limit?
- How much of the encoder's remaining drop is length and language (PI 17% non-English) versus intent mix? A per-length or
  per-language breakdown is in reach from `results/` plus `data/sample.parquet`.
- Three-teacher LOO, with Haiku, when the quota allows.

## Files

- `scripts/`: `convert.py`, `build_items.py`, `sample.py`, `create_job.sh`, `launch.sh`, `build_labels.py`, `predict_lin.py`,
  `predict_enc.py`, `thr_adapt.py`, `adapt_lin.py`, `adapt_enc.py`, `score.py`, `adapt_table.py`, `fmt_zs.py`, `diag.py`,
  `queue_enc.sh`, and `survey/` (the HF survey).
- Data: `data/` (`items_all.parquet`, `sample.parquet`, `labels/`), `exports/`, `preds/{zs,thr,adapt}/`, `results/`,
  `logs/calibrate.txt`.
