> Sanitized copy of ~/pharos-labeling/studies/BUFFLEHEAD/REPORT.md (archived in full on euclid at "Pharos Labeling/2026-10-29-bird-studies"). Verbatim conversation text was removed.

# BUFFLEHEAD: which segments share a model, and what ships

All numbers are from harness v2 on data v2. They are scored on heldout (test + fresh), with thresholds re-tuned per segment on v2 y3 dev.
- **Metrics.** LOO3 = the per-item mean of the fair-LOO Jaccards vs F∩A, H∩A and H∩F. FA is the headline LOO vs F∩A, with Haiku held out. Paired bootstrap 95% CIs use 2,000 item resamples. H-resent (n=20) and H-feedback (n=39) use y_hf J and are indicative only.
- **Inputs.** 34 pred files: 31 BOBOLINK runs and 5 DOTTEREL linear models (V1, V3, V4, V5, V5-f16go), plus 5 blends of my own. Per-item scores are in `items/`; tables are in `results/`.
- **Costs.** CPU is ONNX weight-only int8 at 4 threads (BOBOLINK `cpu_bench_t4.csv`, M3 Ultra P-cores, the M2 Air proxy). Go linear is DOTTEREL V5 f16 at 17.8k items/s on 4 goroutines. The 400k backfill uses the population mix AR 82 / AIUR 7.8 / DEL 5.9 / human 4.6 %. Human items split by corpus counts: H-typed 60 %, H-template 21 %, +ctx 17 %, feedback 1 %, resent 1 %.
- **Spend.** $0 on labels. No training finished (see "Left untested").

## Answer
- **Two weight sets, one architecture: a "human" set and a "machine" set.**
  - **Human set:** the 68m shared model continued on human data. It serves all five human segments plus AIUR and DEL.
  - **Machine set:** the round-1 68m shared model. It serves **AR only**.
- **AR is the only segment that the human-continued model hurts.** The loss is −0.026 to −0.033 LOO3, in 3 of 3 68m runs and every 32m run.
- **The human set ships as a LoRA adapter, not a second model.** `e68v-hum-lora16` (r16 on the frozen shared 68m, 2.08M parameters, 8.4 MB fp32 / ~4.2 MB f16) equals the fully continued `c68v-hum` on human data:
  - H-typed −0.003 [−0.009, +0.003];
  - +ctx +0.001;
  - **H-typed FA 0.768 vs Haiku 0.769**.
  - So the payload is **one 68m base (152 MB) plus a ~4 MB adapter**.
- **The user's "human vs machine" split is right, but the machine side shrinks to AR.** AIUR and DEL can go either way. They are not significantly different between the two weight sets, and AIUR is noise (see below). The greedy merge (tolerance 0.01 and not significantly worse) gives exactly `r1-e68: AR | c68v-hum: H-*, DEL | AIUR: any`.

## 1. Routing table

Each cell is the best model per family, selected on dev (mean LOO on y3 dev). The value is heldout LOO3, with Δ vs the heldout-best single model in brackets. That reference is optimistic: it is a max over 34.

| segment (n y3 heldout) | held-out LLMs: Haiku FA / mean LOO | 68m continued / shared | 68m LoRA on shared | 32m (any) | 32m LoRA | Go linear |
|---|---|---|---|---|---|---|
| **H-typed** (1,494) | 0.769 / 0.759 | c68v-hum 0.773 (FA 0.768); seed 1 0.777 (FA 0.774) | **e68v-hum-lora16 0.770** (FA 0.768), −0.007 [−0.013, −0.002] vs seed 1 | sv-c32v-hum 0.765 (FA 0.763), −0.012 | e32v-hum-lora16 0.756, −0.021 | V4 0.710, −0.066 |
| H-typed+ctx (364) | 0.767 / 0.748 | c68v-hum 0.763 | **0.764** (best) | c32v-hum 0.748, −0.017 [−0.035, +0.002] | 0.736, −0.028 | V3 0.664, −0.100 |
| H-template (674) | 0.980 / 0.945 | 0.990 | 0.989 | 0.988 | 0.989 | **V4 0.989** (−0.001, n.s.) |
| H-resent (20, y_hf) | 0.807 | 0.760 | 0.755 | 0.814 | 0.726 | 0.736 (all CIs about ±0.1) |
| H-feedback (39, y_hf) | 0.713 | 0.740 | 0.736 | 0.744 | 0.740 | 0.618 |
| AIUR (313) | 0.706 / 0.669 | c68v-hum 0.695 (seed 1 0.645) | 0.686 | c32-AIUR 0.704 | e32-AIUR-lora16 0.707 | V5 0.600, −0.107 |
| **AR** (947) | 0.759 / 0.753 | **r1-e68 0.806**; c68v-hum 0.778 (−0.028) | 0.772 (−0.033) | c32-AR 0.794, −0.012; r1-e32 0.789, −0.017 | 0.779 | V1 0.767, −0.039; V5 0.760, −0.045 |
| DEL (175) | 0.604 / 0.592 | c68-hum 0.742; c68v-hum 0.734 | 0.720 | d32-DEL 0.731 | 0.739 | V3 0.725, −0.017 (n.s.) |

- **H-typed.** Only 68m human-specialized weights reach Haiku: c68v-hum seeds 0 and 1, and the 68m LoRA. The best 32m (s32v-all continued on human) is −0.012; linear is −0.066.
- **H-template** is solved by anything, linear included.
- **AIUR is not a routing question.**
  - On in-distribution test items (n=63), every encoder scores 0.86–0.89 FA against Haiku's 0.83.
  - On fresh novel templates (n=250), encoders score 0.55–0.63 against Haiku's 0.67; linear scores 0.53.
  - Model rankings on AIUR flip with the seed: c68v-hum seed 1 is −0.051 vs seed 0. They also flip with blends (±0.08) and thresholds.
  - Dev cannot tell the models apart: all score 0.89–0.91 on AIUR dev.
- **DEL.** Every encoder and linear model is within CI, and all are far above the LLMs (+0.13 FA).

## 2. Cross-check: "model A on segment B" (`results/cross.csv`, LOO3 Δ [CI]; y_hf agrees)

| trained on → scored on | H-typed | +ctx | AIUR | AR | DEL |
|---|---|---|---|---|---|
| 68m continued on human (c68v-hum − r1-e68) | +0.022 [+0.015, +0.029] | +0.031 | +0.052 (seed 1: +0.001) | **−0.028 [−0.037, −0.018]** | +0.009 |
| 68m LoRA on human (e68v − r1-e68) | +0.019 | +0.032 | +0.043 | **−0.033** | −0.005 |
| 32m continued on human, v2 (c32v-hum − r1-e32) | +0.018 | +0.048 | −0.054 | **−0.023** | −0.012 |
| 32m human **+ 25k machine replay** (c32-hum-rep − r1-e32) | +0.007 | +0.022 | +0.005 | **−0.006 (n.s.)** | +0.001 |
| replay vs no replay, same labels (c32-hum-rep − c32-hum) | −0.005 [−0.010, −0.000] | +0.000 | +0.102 | +0.018 | +0.005 |
| 32m shared retrained on v2 labels (s32v-all − r1-e32) | +0.022 | +0.034 | +0.003 | −0.007 (n.s.) | +0.002 |
| 32m continued on AIUR / DEL / AR (vs r1-e32) | −0.014 / −0.016 / −0.013 | ±0.01 | +0.010 / −0.006 / −0.063 | −0.023 / −0.013 / +0.005 | −0.028 / +0.012 / −0.004 |
| pure specialists d32-* (from pretrained) | | | | −0.44 to −0.66 off-segment | |

- **The human model hurts AR, and only AR.**
- **Machine-trained models hurt human text.** Continuing on AIUR, DEL or AR costs H-typed about −0.015. Machine specialists buy nothing significant on their own segment except AR continued (+0.005, n.s.).
- **A single model continued on human data with replay is viable at 32m.** It removes the AR loss but keeps only about half the human gain: H-typed +0.007 vs +0.012 without replay.
- **Retraining the shared 32m on v2 labels does the same job in one model.** It gives H-typed +0.022 with AR −0.007.
- **At 68m the single-model option is untested.** Neither replay nor shared-v2 has a finished run (see "Left untested"). The LoRA route makes the question moot for payload, because base and adapter are one 152 MB file plus 4 MB.

## 3. Configuration frontier (`results/configs.csv`)

- **Columns.** Quality is heldout LOO3 per segment. Pop J is population-weighted over all 8 segments, as LOO3 or FA. "Δ H-typed" is vs the heldout-best H-typed model, c68v-hum seed 1.
- **CPU time.** The h/400k column is the 4-thread backfill time. The effective rate is 400k / time: 68m everywhere ≈ 21 items/s, 32m everywhere ≈ 58 items/s, linear 17.8k items/s.
- **Per-segment 68m rates** (items/s): H-typed 27, +ctx 23, H-rest 37, AIUR 9.8, AR 25, DEL 10.6.
- **Per-segment 32m rates** (items/s): H-typed 75, +ctx 62, H-rest 100, AIUR 26, AR 71, DEL 29.

| configuration | MB | h / 400k | H-typed | +ctx | H-tmpl | AIUR | AR | DEL | pop J LOO3 / FA | Δ H-typed |
|---|---|---|---|---|---|---|---|---|---|---|
| Go linear only (V5 f16) | 86 | 0.01 | 0.709 | 0.675 | 0.989 | 0.600 | 0.760 | 0.717 | 0.747 / 0.746 | −0.067 |
| 1 enc: shared 32m (r1-e32) | 92 | 1.90 | 0.733 | 0.700 | 0.988 | 0.694 | 0.789 | 0.724 | 0.779 / 0.771 | −0.043 |
| 1 enc: shared 32m on v2 labels (s32v-all) | 92 | 1.90 | 0.756 | 0.734 | 0.986 | 0.697 | 0.782 | 0.726 | 0.775 / 0.767 | −0.021 |
| 32m base + LoRA (human, AIUR, DEL) | 97 | 1.90 | 0.756 | 0.736 | 0.989 | 0.707 | 0.789 | 0.739 | 0.782 / 0.773 | −0.021 |
| 2 enc 32m (c32v-hum human; r1-e32 rest) | 184 | 1.90 | 0.752 | 0.748 | 0.989 | 0.694 | 0.789 | 0.724 | 0.780 / 0.772 | −0.025 |
| 1 enc: shared 68m (r1-e68) | 152 | 5.27 | 0.751 | 0.732 | 0.986 | 0.643 | 0.806 | 0.725 | 0.790 / 0.782 | −0.026 |
| 1 enc: 68m human-continued everywhere | 152 | 5.27 | 0.773 | 0.763 | 0.989 | 0.695 | 0.778 | 0.734 | 0.773 / 0.768 | −0.004 |
| **A: 68m base + human LoRA (human, AIUR, DEL); base on AR** | **156** | **5.27** | **0.770** | **0.764** | 0.989 | 0.686 | **0.806** | 0.720 | **0.794 / 0.787** | −0.007 |
| 2 enc 68m (c68v-hum human/AIUR/DEL; r1-e68 AR) | 304 | 5.27 | 0.773 | 0.763 | 0.989 | 0.695 | 0.806 | 0.734 | 0.796 / 0.789 | −0.004 |
| 68m human (c68v-hum) + 32m r1-e32 on AR | 244 | 2.96 | 0.773 | 0.763 | 0.989 | 0.695 | 0.789 | 0.734 | 0.782 / 0.774 | −0.004 |
| **B: 68m base + human LoRA (human, AIUR) + Go linear on AR, DEL** | **242** | **1.04** | 0.770 | 0.764 | 0.989 | 0.686 | 0.760 | 0.717 | 0.757 / 0.755 | −0.007 |
| 68m human + linear on AR (DEL on 68m) | 238 | 1.66 | 0.773 | 0.763 | 0.989 | 0.695 | 0.760 | 0.734 | 0.758 / 0.755 | −0.004 |
| 32m base + human LoRA + linear on AR, DEL, H-template | 180 | 0.40 | 0.756 | 0.736 | 0.989 | 0.694 | 0.760 | 0.717 | 0.757 / 0.754 | −0.021 |
| linear + 32m human LoRA on H-typed/+ctx only | 180 | 0.06 | 0.756 | 0.736 | 0.989 | 0.600 | 0.760 | 0.717 | 0.749 / 0.748 | −0.021 |

**Reading the frontier.**
- **AR is 82 % of items.** So AR decides both pop J and backfill time:
  - 68m on AR: 0.806, 3.6 h of the 5.3 h.
  - 32m on AR: 0.789, 1.3 h.
  - Linear on AR: 0.760 (still ≥ Haiku 0.759 FA / LLM mean 0.753), 20 s.
- **H-typed (the primary segment) costs little in CPU at any size.** Human items take only 0.18 h of 68m time per 400k. So the 68m human weights belong in every encoder configuration; the open choice is only how to run AR.
- **Ongoing traffic is about 1.9k items/day** (fresh = 14 days). That is about 1.5 min/day even for config A. The CPU-hours column matters only for the one-off backfill.
- **Memory.** ORT 68m w8 peaks at about 1.0 GB RSS at 2,048 tokens/batch; 32m at about 0.75 GB. Go linear needs 0.18–0.22 GB. Running segments in batches keeps one encoder resident at a time.
- **Robustness.** With one global threshold set instead of per-segment ones (`results/configs_globth.csv`), the top encoder configs move by ±0.005–0.01 pop J. For example, shared 68m and two-68m tie at 0.788. Differences below about 0.01 pop J are not meaningful.
- **Blends add nothing reliable.** I tested encoder + linear, 32m + 68m, and human + shared averaged (`preds/bl-*`). On AR, 32m + linear gives +0.005 (n.s.). Blends cost H-typed or AIUR elsewhere.

## 4. Recommendation

**A. Ship (quality): one ettin-68m w8 base (152 MB) plus a human LoRA adapter (~4 MB f16); about 156 MB in total.**
- **Routing:**
  - adapter on H-typed, H-typed+ctx, H-template, H-resent, H-feedback, AIUR and DEL;
  - base on AR.
- **Quality:**
  - H-typed FA 0.768 = Haiku 0.769;
  - +ctx 0.764 LOO3;
  - AR 0.806 (+0.05 over the LLMs);
  - pop J 0.794 LOO3.
- **Cost:** backfill 5.3 h on 4 threads; ongoing about 1.5 min/day.
- **Engineering note.**
  - Two separate merged w8 files (304 MB) give the same quality at twice the payload.
  - The single-payload form needs the adapter applied at runtime: ONNX Runtime's LoRA adapter support (`OrtLoraAdapter`, ORT ≥ 1.20) or an exported graph with LoRA side-branches over the MatMulNBits base. This is untested here; it needs a parity fixture like DOTTEREL's.
  - Ship c68v-hum (full continuation) only if the adapter path proves impractical.

**B. Ship if the backfill must be fast: config A for human and AIUR, plus DOTTEREL's pure-Go V5 f16 linear for AR and DEL (~242 MB).**
- **Cost:** backfill 1.0 h instead of 5.3 h.
- **Quality:** AR drops to 0.760 (−0.045 [−0.058, −0.034] vs the 68m base), which is still LLM level. DEL is n.s. (−0.017, still +0.11 over the LLMs).
- **Optional upgrade:** re-run AR through the 68m base later, in the background, to upgrade to A.
- **Middle option:** a 32m on AR (+92 MB) gives 0.789 in 3.0 h.

**Not recommended:**
- **Linear-only:** H-typed −0.067.
- **32m-only configs:** H-typed −0.021 vs 68m, and −0.013 to −0.017 below Haiku on FA. They suit a ≤100 MB payload budget: there, ship s32v-all or 32m base + LoRA (92–97 MB, 1.9 h, pop J 0.775–0.782).
- **Per-segment machine specialists:** none is significant on its own segment.
- **Pure specialists from pretrained:** catastrophic off-segment, so they are useless without perfect routing.
- **Blends.**

**LLM (Haiku) fallback.** Route novel-template AIUR to Haiku, plus an entropy top-up, as in round 1 (~10 % of AIUR, about $35 per 400k backfill, about $0.25/day ongoing).
- That is the only cell where every student trails Haiku: fresh AIUR, encoders 0.55–0.63 vs Haiku 0.67 FA.
- It is a template-novelty problem, not a model-choice problem, so no routing or adapter fixes it. A periodic about $25 Flash/Haiku refresh of the adapter or base is the other remedy (round 1: fresh AIUR 0.63 → 0.73).
- Nothing else needs the fallback.

## 5. Slot for thinking (TH, pending EGRET)
- **TH must be trained, not routed to an existing set.** EGRET's interim zero-shot result:
  - round-1 c-e32 scores −0.08 to −0.24 vs teacher agreement;
  - it fires P on 12 % of thinking items;
  - it misses deliberation.evidence, reconsider and hypothesis.
- **Slot: a third weight set on the same 68m base, as a TH LoRA adapter** (~4 MB, trained on EGRET's ~20k labels). This is the pattern that worked for human, where the LoRA matched full continuation.
  - **Decision rule:** if a TH adapter scored on AR stays within 0.01 of the base, it could later be merged into a retrained base. Otherwise TH stays its own adapter.
  - **Do not fold TH into the human adapter.** Human training hurts AR-like machine text, and TH is machine text.
- **Cost watch.** TH items are long (EGRET: J falls with length, 4k+ chars common), so 68m CPU is roughly AIUR/DEL speed (~10 items/s). If TH volume is large, give TH the same cheap-tier option as AR: a Go linear model trained on TH labels.
- **Measure before choosing.** EGRET's labeled split should score: base, TH adapter, and linear-with-TH-rows.

## Left untested (no confirmation training finished)
- **68m single model with machine replay** (c68v-hum-rep: human v2 + 25k replay, 2 epochs). It would answer "one 68m full model for everything" without an adapter runtime.
  - It was spec'd and queued (`scripts/queue_bh.sh`, `scripts/bh_train.py`), but the GPU never freed: BOBOLINK queues 11/12 ran through 14:15. One premature start at 11:51 was killed after about 1 min (STATUS.md). The waiter (PID 10224) was stopped at 14:14 on the director's instruction.
  - 32m evidence says replay works: AR −0.006 n.s., keeping about half the human gain.
- **68m shared retrained on v2 labels** (BOBOLINK s68v-all): not scored when this was written. At 32m the analogue (s32v-all) gave the human gain in one model with AR −0.007. **If s68v-all reaches H-typed ≈ 0.77 with AR ≈ 0.80, it replaces config A with a plain single model (152 MB).** Re-run `scripts/items.py --all && scripts/frontier.py all` when it lands; its config row is already defined.
- **c68-AIUR, c68-DEL, c68v-Ht, d68v-hum** (BOBOLINK queue12): not scored. They are unlikely to change routing, given that 32m machine specialists were n.s.
- **Runtime checks:**
  - ORT LoRA-adapter runtime over a w8 base, and its parity;
  - real M2 Air throughput (all CPU numbers are M3 Ultra 4-thread proxies);
  - whether ettin-32m and 68m share a tokenizer, which matters if mixing sizes.
- **AIUR CIs are item-level.** Items cluster by template, so the true AIUR uncertainty is larger than shown.

## Files
- **`scripts/items.py`:** per-item scores, both threshold variants.
- **`scripts/frontier.py`:** routing, cross, configs, greedy. Costs and payloads are in its `CPU` and `MB` dicts.
- **`scripts/blend.py`.**
- **`scripts/bh_train.py`, `bh_convert.py`, `queue_bh.sh`:** the confirmation-run machinery, unused.
- **Results:** `results/routing.csv`, `routing_all.csv`, `cross.csv`, `configs.csv`, `config_segments.csv`, `configs_globth.csv`, plus `logs/configs_final.txt` and `logs/greedy_final.txt`.
- **Blend predictions:** `preds/bl-*.npz`.
