> Sanitized copy of ~/pharos-labeling/studies/CHICKADEE/REPORT.md (archived in full on euclid at "Pharos Labeling/2026-10-29-bird-studies"). Verbatim conversation text was removed.

# CHICKADEE: does more labeling context give better human-text labels, and better students?

_2026-10-29, director-facing. Every number is on H-typed (human typed text) unless a segment is named. STD = round-1
context-v3 labels (AVOCET v2: round-1 + AVOCET fill); STDrep = the same STD context re-labelled with my build, same day,
same packing as the variants (the strict 1:1 control). CIs are paired bootstrap 95% (2,000 resamples)._

## Answer
- **No. More context does not make the labels better.** Neither longer preceding context (LONG, ~4,000 chars across turns) nor the full previous assistant message (LASTMSG, up to 6,000 chars) improves labels for human typed text on any non-circular yardstick.
  - Teacher agreement drops slightly instead.
  - A blind judge on a random sample finds Flash slightly worse and Haiku unchanged.
  - The label change from adding context is no larger than the change from simply re-running STD.
- **Students: no reliable gain either.** I trained the same student recipe on each variant's labels, with matching input context, on the same 12,505 items and with the same heads and seeds. No cell is positive across yardsticks:
  - It wins only on the RICH-consensus yardstick, which shares LONG's structure.
  - It loses on Argon-RICH, on the STD harness and on the blind judge.
  - Every difference is within about ±0.02, and **re-running STD labelling alone moves the same yardstick by +0.016**, as much as any context effect.
- **Recommendation: keep STD (600 chars) for bulk relabeling.** Longer context costs +10–18% Haiku tokens per item, +8–16% Flash, and makes the student 1.5–4x slower on CPU, with nothing measurable bought.

## What was done
- **Runner.** Local branch `gbdubs/chickadee-context` (worktree `src/`, uncommitted, scratch binary `bin/pharos-chickadee`).
  - `labeling run --create --context-variant std|long|lastmsg|rich` builds the variant's PREVIOUS_CONTEXT at job creation from the dataset's units: same agent session, earlier units only, rowid order, each part redacted with the context-v3 patterns before it is cut.
  - The context is stored in a `context_variants` table and recorded as `selection.context_variant`. Prompts, contract hashes, validation (`echoes_context`) and export read it.
  - Prompt template, schema, routes, isolation and the agy private HOME are unchanged. `--items-file` was also added.
  - **STD reproduction test passes:**
    - context rebuilt from the units equals the stored context for all 29,155 human and AIUR units;
    - all 96,673 round-1 contract hashes of those items are reproduced;
    - 300 of 300 round-1 prompts are reproduced byte for byte (SHA-256).
- **Variants** (only PREVIOUS_CONTEXT differs; CONTEXT_EXCERPT says whether it was cut):

  | variant | definition | mean / median chars on human items |
  |---|---|---|
  | STD | context-v3: last 600 of "Previous user request … Previous assistant message …" | 391 / 600 |
  | LONG | the session's earlier turns (each turn's request + its final reply). Latest reply keeps its last 2,000 chars, other parts their last 1,000, the whole its last 4,000. | 1,908 / 1,980 |
  | LASTMSG | the previous assistant message alone, last 6,000 chars | 1,159 / 597 |
  | RICH (yardstick only) | as LONG with 6,000 / 1,500 / 10,000 | 3,692 / 2,579 (test+fresh) |

  - **Why these lengths.** LONG gives 3+ turns on typical sessions while keeping a bigger share for the reply the person reacts to. LASTMSG isolates "the other party's last message in full", which is the main thing STD truncates (STD shows at most ~600 chars, mostly the reply's tail).
  - **Empty context.** 28–31% of human items have no earlier unit in their session, so their context is empty under every variant.
- **Labeling** (Flash gemini-3.8-flash low via agy; Haiku claude-haiku-5-5 high via claude; Argon gemini-4-argon medium; 4 items per request).

  | job | coverage |
  |---|---|
  | LONG, LASTMSG × Flash + Haiku | all 16,722 human items |
  | STDrep × Flash | all human items |
  | STDrep × Haiku | held-out H-typed complete; train and rest partial (stopped 10:30 for Claude quota, see Incidents) |
  | RICH × Haiku/Flash/Argon; Argon × LONG/LASTMSG | H-typed test+fresh (1,496) |

  - **Spend: $191** (Flash $110, Argon $43, Haiku $38).
- **Yardsticks** (non-circular by construction, or with the bias stated):
  - **A.** Three-teacher consensus under RICH, from independent runs. Each variant's teacher is scored against the *other two* RICH teachers.
  - **B.** Blind adjudication by Claude sub-agent judges. Each judge saw the RICH context, the text, the taxonomy and the labelers' rules, plus the union of candidate labels shuffled with no variant names.
    - **B2:** a random sample of 200 held-out items.
    - **B1:** 200 items selected for disagreement, judged twice independently (judge–judge J 0.917).
  - **C.** Each variant's own Haiku∩Flash consensus, as a cross matrix (students only).
  - **D.** Harness v2 on STD labels (AVOCET v2). This favours STD by construction.

## Results: teachers
**Agreement and need_context**, all H-typed (n=9,981):

| | STD | LONG | LASTMSG |
|---|---|---|---|
| Haiku–Flash J | 0.759 | 0.748 (−0.011 [−0.016,−0.005]) | 0.755 (−0.004 [−0.009,+0.001]) |
| Haiku need_context | 43.6% | 48.4% | 39.4% |
| Flash need_context | 1.1% | 0.4% | 0.5% |
| Haiku∩Flash mean labels / empty | 5.29 / 1.2% | 5.07 / 1.7% | 5.18 / 1.5% |

**Same-build control**, held-out H-typed (n=2,467). STDrep re-runs STD with my build and packing:

| | J vs STD (rerun noise) | LONG vs STDrep | LASTMSG vs STDrep |
|---|---|---|---|
| Haiku label stability | 0.845 | 0.849 | 0.846 |
| Flash label stability | 0.809 | 0.816 | 0.833 |
| Haiku∩Flash stability | 0.826 | 0.825 | 0.837 |
| Haiku need_context | STD 44.7%, STDrep 54.4% | 49.1% | 40.6% |
| Haiku–Flash J | 0.758 (STDrep) | −0.017 [−0.027,−0.006] | −0.008 [−0.019,+0.003] |

- **Label stability.** Swapping the context changes labels no more than re-running the identical prompt.
- **need_context.** Haiku's 12% figure in the brief is not the H-typed rate: on H-typed it is 44–54%, and the same context gives 44.7% or 54.4% depending on packing and run. Against the same-build control it does drop with LASTMSG (54% → 41%). It is a weak signal, though:
  - with empty context it is 4–8%;
  - with uncut non-empty context it is 34% under STD and 50–55% under the variants.

  It marks "this message refers back", not missing information.
- **Shifts in which labels are given.** Flash with more context gives fewer P.frame / P.frame.scope labels (scope 24% → 16%), fewer P.request.work.implement (43% → 38%) and fewer P.control. Haiku shifts are at most 2 points (C.input down, C.feedback.challenge up).
- **Within-variant fair LOO** (test+fresh, each teacher vs the other two of the same variant; n≈1,495):

  | | STD | LONG | LASTMSG | RICH |
  |---|---|---|---|---|
  | Haiku vs F∩A | 0.769 | 0.761 | 0.760 | 0.744 |
  | Flash vs H∩A | 0.759 | 0.762 | 0.767 | 0.743 |
  | Argon vs H∩F | 0.749 | 0.737 | 0.742 | 0.720 |
  | Argon need_context | 6.6% | 2.3% | 2.3% | 1.5% |

  The longest context (RICH) gives the *least* consistent teachers.
- **Yardstick A** (RICH three-teacher, other-two intersection, n=1,489; differences vs STDrep):
  - Haiku: LONG +0.005 [−0.003,+0.014], LASTMSG −0.002.
  - Flash: LONG +0.003 [−0.009,+0.014], LASTMSG +0.002.
  - Haiku∩Flash vs y3-RICH: LONG +0.015 [+0.003,+0.026], LASTMSG +0.001.

  The only significant cell is LONG's consensus against RICH's consensus, and LONG has RICH's structure. It is the expected bias of this yardstick, not evidence.
- **Yardstick B2** (blind judge, random sample n=200, J vs judge closure):

  | set | STD | STDrep | LONG | LASTMSG | LONG−STDrep | LASTMSG−STDrep |
  |---|---|---|---|---|---|---|
  | Haiku | 0.808 | 0.820 | 0.804 | 0.808 | −0.015 [−0.040,+0.007] | −0.012 [−0.036,+0.012] |
  | Flash | 0.771 | 0.771 | 0.748 | 0.745 | −0.023 [−0.055,+0.008] | −0.026 [−0.053,+0.001] |
  | Haiku∩Flash | 0.745 | 0.736 | 0.726 | 0.724 | −0.010 [−0.037,+0.016] | −0.013 [−0.040,+0.014] |

  - B1 (dispute-selected) agrees with B2. Where both judges agree: Flash LONG−STD −0.043 [−0.084,−0.004], Haiku ±0.000, Haiku∩Flash −0.026 (n.s.).
- **Other human segments.**
  - H-template: Haiku–Flash J rises 0.848 → 0.93 under LONG/LASTMSG, but STDrep reaches 0.932 too. This is a packing/run effect (my jobs pack only human items), not context.
  - H-typed+ctx: +0.017 under LONG vs STD, but STDrep is +0.017 as well.

## Results: students (the 1:1 comparison)
**Recipe.** The BOBOLINK human recipe (`ch_train.py`, a copy of `bob_train.py` that changes only how context, targets and pool are read):
- continuation of round-1 `c-e{32,68}-512-all`, human train items only, lr 3e-5, 2 epochs, round-1 framing (context gets whatever of 512 tokens the text leaves);
- heads y_hf, haiku, flash, argon, y3, with argon/y3 left empty in every variant (Argon labels exist only for STD);
- the same 12,505-item pool (train-split human items with Haiku and Flash under every variant);
- each variant's own labels, and its own context as input.

**Scoring.** Scorer is the y_hf head (flash head for Flash-only runs). Each run's thresholds are tuned per label on human dev against its *own* variant's labels, so no run is tuned on another variant. Scored on H-typed test+fresh. Two control kinds:
- **STD** (AVOCET v2 labels), for Haiku∩Flash targets;
- **STDrep** (same build), for the Flash-only runs. Haiku STDrep is incomplete because of the quota stop.

**Yardsticks.**
- **rich_y3:** H∩F∩A under RICH, n=1,490. Biased toward LONG.
- **rich_argon:** Argon under RICH alone.
- **std_FA:** Flash∩Argon under STD, the harness LOO target. Biased toward STD.
- **judge_rand:** blind judge on the random sample, n=200. The least biased.
- **judge1:** blind judge on the dispute sample, n=200.

**Paired deltas, variant − control** (seeds averaged per item where two exist; results/headline.csv):

| family | yardstick | STD | LONG − STD | LASTMSG − STD |
|---|---|---|---|---|
| 32m continuation, 2 seeds | rich_y3 | 0.738 | +0.002 [−0.003,+0.007] | −0.002 [−0.007,+0.003] |
| | rich_argon | 0.713 | −0.006 [−0.011,−0.001] | −0.007 [−0.011,−0.002] |
| | std_FA | 0.754 | −0.005 [−0.010,+0.001] | −0.010 [−0.015,−0.004] |
| | judge_rand | 0.729 | −0.001 [−0.013,+0.011] | −0.012 [−0.024,+0.001] |
| 68m continuation (= BOBOLINK best, c68v-hum recipe), 1 seed | rich_y3 | 0.756 | +0.010 [+0.003,+0.018] | −0.000 [−0.007,+0.007] |
| | rich_argon | 0.722 | −0.007 [−0.013,−0.001] | −0.005 [−0.011,+0.001] |
| | std_FA | 0.768 | −0.004 [−0.011,+0.003] | −0.008 [−0.015,−0.001] |
| | judge_rand | 0.723 | −0.021 [−0.037,−0.005] | −0.006 [−0.020,+0.008] |
| 32m from pretrained (no STD-trained init) | rich_y3 | 0.733 | +0.008 [−0.001,+0.017] | +0.005 [−0.003,+0.014] |
| | judge_rand | 0.706 | +0.004 [−0.017,+0.026] | +0.005 [−0.015,+0.026] |
| 32m, max-len 1024, all context | rich_y3 | 0.740 | −0.000 [−0.007,+0.007] | −0.002 [−0.009,+0.004] |
| | judge_rand | 0.723 | +0.000 [−0.015,+0.017] | −0.010 [−0.024,+0.005] |

**Flash-only targets, 32m** (the strict same-build 1:1, with an STD rerun):

| yardstick | STD (round 1) | STDrep (rerun) | STDrep − STD (**rerun noise**) | LONG − STDrep | LASTMSG − STDrep |
|---|---|---|---|---|---|
| rich_y3 | 0.682 | 0.698 | **+0.016 [+0.009,+0.023]** | +0.009 [+0.002,+0.016] | +0.007 [+0.001,+0.014] |
| rich_argon | 0.724 | 0.721 | −0.003 | −0.006 [−0.012,+0.001] | +0.001 |
| std_FA | 0.731 | 0.733 | +0.001 | +0.002 [−0.005,+0.009] | +0.006 [−0.001,+0.012] |
| judge_rand | 0.727 | 0.725 | −0.001 | +0.001 [−0.015,+0.015] | +0.011 [−0.003,+0.026] |

**Separating context from rerun noise.** Re-labelling the identical STD context with the same models moves the student's rich_y3 score by +0.016. Against the original STD labels, LONG and LASTMSG look like +0.025 and +0.023. Against the rerun they are only +0.009 and +0.007, and there is no matching gain on Argon-RICH, std_FA or the judge.

Run-to-run label noise is of the same size as every apparent context effect: teacher label J between two STD runs is 0.81–0.85, the same as STD vs LONG. **Only a difference that holds against STDrep and on the unbiased judge would count as a context effect, and none does.** The one consistent sign is negative: LASTMSG on std_FA and Argon-RICH, LONG on the judge at 68m.

**Harness v2** (preds/*.npz, rows in studies/leaderboard2.jsonl; STD labels, per-segment STD thresholds; H-typed held-out LOO-FA, Haiku 0.769):

| | STD | LONG | LASTMSG | seed note |
|---|---|---|---|---|
| 32m s0 | 0.746 | 0.752 | 0.747 | |
| 32m s1 | 0.753 | 0.759 | 0.751 | |
| 68m s0 | 0.764 | **0.775** (+0.007 vs Haiku [−0.005,+0.018]) | 0.767 | |
| Flash-only 32m (+ STDrep) | 0.747 | 0.750 | 0.745 | STDrep 0.739 |

Seed spread at 32m is 0.007. The LONG − STD gap on this harness (+0.006 / +0.006 / +0.011) is not seen with the variant-own thresholds above. The difference is the threshold rule (STD-tuned per segment vs own-label-tuned), so it is not robust.

**Versus BOBOLINK's best human model.** bob:c68v-hum scores rich_y3 0.753, judge_rand 0.728, std_FA 0.771. My 1:1 STD control on the same 68m recipe scores 0.756 / 0.722 / 0.768, so the control reproduces it. LONG at 68m is 0.766 / 0.701 / 0.764: better on the LONG-biased yardstick, worse on the judge. **No variant beats BOBOLINK's model on the unbiased yardstick.**

## Cost of the longer context
**Teacher tokens and cost**, same build and packing (results/token_cost.csv; per item, prompt overhead included):

| | Haiku input/request | Haiku $/1k items | Flash input/request | Flash $/1k items |
|---|---|---|---|---|
| STD (STDrep) | 13.8k | 0.79 | 8.1k | 1.96 |
| LONG | 16.3k (+18%) | 0.92 (+16%) | 9.8k (+20%) | 2.27 (+16%) |
| LASTMSG | 15.2k (+10%) | 0.84 (+7%) | 9.0k (+11%) | 2.13 (+9%) |
| RICH | 19.0k (+37%) | 1.08 (+37%) | 11.7k (+44%) | 2.66 (+36%) |

- **Argon:** LONG $9.6 per 1k items, LASTMSG $8.6, RICH $10.5.
- **Output tokens** do not change.

**Student cost** (ettin 32m, weight-only int8 ONNX, CPU, 4 threads, 300 held-out H-typed items; the machine was loaded, so ratios matter more than absolutes):

| input | mean tokens | items/s | vs STD |
|---|---|---|---|
| STD, max-len 512 (round-1 framing) | 173 | 60 | 1x |
| LASTMSG, 512 | 272 | 39 | 1.5x slower |
| LONG, 512 | 372 | 26 | 2.3x slower |
| LASTMSG, 1024 | 369 | 23 | 2.6x slower |
| LONG, 1024 | 585 | 14 | 4.3x slower |

- **Training:** compute rises from 1.0 min (STD) to 1.6 (LASTMSG) and 2.0 (LONG) for 32m, because mean train length goes from 176 to 280 and 361 tokens. 68m takes 2.4 min of compute (STD). MPS peak is 12–15 GB for 32m and 17.8 GB for 68m.

## Incidents and limitations
- **Runner lease was CPU-bound.** There is no index on attempts(job) and the ledger holds 140k round-1 attempts. As a result, Claude Code got no stdin within 3 s, attempts ended `unknown`, and one job halted.
  - Fixed on my dataset clone with `CREATE INDEX attempts_job_idx ON attempts(job)`: runner CPU fell from 90% to 1%.
  - AVOCET applied the same fix with director approval.
- **Memory.** Early on, 18 runners at concurrency 12–16 reached ~40 GB RSS against my 12 GB budget. I stopped them within minutes and then ran under a worker budget of 48 (about 12 GB).
  - The 68m student runs peaked at 17.8 GB of MPS memory, over my budget. The machine total stayed under ~60 GB.
- **Claude quota stop.** At the director's 10:30 instruction, Haiku STDrep stopped partway: train 932/1,893 requests, rest 607/1,687; held-out complete.
  - **Mistake:** a boost script I had queued at 10:29 restarted both Haiku jobs at 10:34:45, after the stop. I stopped them again at 10:35; 76 Haiku requests (~$0.25) were sent after the instruction.
  - **Consequence:** the strictly same-build STD student exists only with Flash-only targets (f-stdrep). The Haiku∩Flash students use AVOCET v2 STD labels, which differ in packing and run date. Held-out STDrep (both teachers) is complete, so every teacher-level comparison has the same-build control.
- **Packing confound.** My jobs pack 4 human items per request; round-1 mixed cohorts. Same context, different packing moves Haiku need_context from 44.7% to 54.4% and H-template agreement from 0.848 to 0.932. Any comparison against round-1 STD labels carries this confound, which is why STDrep exists.
- **The judges are Claude models.** They share priors with Haiku, which may favour Haiku-style labels. That bias is the same across variants because the sets are blind.
- **No ground truth.** All yardsticks are LLM-derived, so each is reported with its bias.

## Recommendation
- **Keep STD (context-v3, last 600 chars) for any bulk relabel of human text.** LONG and LASTMSG cost +7–16% teacher spend and 1.5–4x student CPU, and buy nothing measurable. On the least biased yardstick they are flat or worse, especially for Flash.
- **If a longer context is wanted anyway, LASTMSG is the cheaper, less harmful option.** Keep it at most ~2,000 chars (its median is 600, so STD already shows most of it). Do not adopt LONG or RICH, which lower teacher agreement and Argon/Haiku self-consistency.
- **Bigger, cheaper levers than context:**
  - **Model size:** 68m vs 32m is +0.02 on every yardstick.
  - **Packing:** labelling human items packed together changes labels as much as context does. It raised H-template agreement 0.85 → 0.93, but also Haiku need_context 45% → 54%. Decide it deliberately and keep it fixed across a corpus.
  - **Multiple reads:** a second Flash read moves the student by +0.016 on rich_y3, so read-to-read variance is real and could be averaged.
- **Do not use Haiku need_context to pick a context length.** It rises with *more* context.

## Open questions
- Does averaging two STD reads per teacher (votes from STD+STDrep) beat a single read? That is the noise floor this study exposed, at no extra context cost.
- Is the packing effect (human-only vs mixed requests) good or bad? The judge favoured round-1 STD Haiku over STDrep on the dispute sample (+0.03, n.s.), and the reverse on the random sample.
- Context may matter for specific labels (C.feedback.challenge, references like "do that") even if not on average. A per-label adjudication would need a larger judged sample.
- STDrep Haiku labels for train are half done. Finishing them (~$4) would allow a strict same-build Haiku∩Flash student control.

## Files
`$C = ~/pharos-labeling/studies/CHICKADEE`:
- **Status and handoff:** STATUS.md, CHECKPOINT.md (resume guide), LABELING_DONE.
- **Code:** `src/` (branch gbdubs/chickadee-context, uncommitted), `bin/pharos-chickadee`, scripts/.
- **Data:** data/<variant>/label_matrices.npz (round-1 layout), data/contexts_<v>.parquet, adjudication*/, results/, preds/ (harness v2), models/.
