> Sanitized copy of ~/pharos-labeling/studies/KESTREL/REPORT.md (archived in full on euclid at "Pharos Labeling/2026-10-29-bird-studies/studies/KESTREL"). Verbatim conversation text was removed.

# KESTREL: the shipping encoder, trained on this library plus public sources (round 4)

2026-10-29 20:35 → 2026-10-30 03:00 MDT. All numbers are J (mean Jaccard of closed label sets) against **Flash∩Argon (F∩A)** on
held-out items, with **family-cluster bootstrap 95% CIs** (1,000 resamples). Every model comparison is **paired** on the same items.
- **dF** = J(student, Argon) − J(Flash, Argon): does the student predict Argon as well as Flash does.
- **Library sets:** AVOCET v2 held-out (test ∪ fresh) y3 sets, with Haiku's own F∩A score beside.
- **Thresholds:** tuned the same way for every model (ours, baselines, linear), per segment on **mixed dev**: library v2 dev y3,
  EGRET TH dev and public dev with both teachers. In leave-one-source-out (LOSO) folds the held-out source is left out of dev too.
- **GANNET:** scored once, for the two final candidates only.

## Answer (numbers first)

- **Training on several populations cuts the drop on unseen people for the encoder, not for the linear model.**
  - **Setup.** ettin-32m with each public source held out in turn (LOSO), against a control trained identically on library data only.
    Pooled over the four held-out sources: H-typed test, n = 3,070, 722 families.
  - **Encoder:** **+0.025 [+0.014, +0.036]** on the unseen population, and library H-typed is unchanged (−0.003 on average).
    - The drop (library − unseen) shrinks by **0.028 [0.015, 0.039]**.
    - Per source: AT +0.063 [+0.021, +0.112], PI +0.024 [+0.007, +0.043], WC +0.020 [+0.005, +0.036], OH −0.005 [−0.033, +0.023].
      OH's typed text is already near the library level (drop 0.03).
  - **Linear V4 recipe** on the same mixed data (vocabulary refit, public weight matched to the encoder's 26%):
    **+0.002 [−0.006, +0.009]** on the unseen population, while library H-typed falls by 0.014.
    - Difference of gains (encoder − linear): **+0.023 [+0.011, +0.037]**.
    - Same direction on H-typed+ctx (+0.032 vs −0.005) and AR (+0.018 vs −0.002).
  - **Level:** under diverse training, the encoder beats the linear model on unseen populations by **+0.092 [+0.073, +0.111]** (H-typed).
  - **68m, AT held out:** +0.035 [+0.001, +0.083] vs library-only `s68v-all` (0.705 vs 0.669).
- **The population's own labels are worth more still.** Adding a source's own train split on top of the other sources gives
  +0.078 [+0.062, +0.095] pooled (WC +0.146, PI +0.049; AT and OH ±0.01).
  - IBIS found that other populations' data did not transfer when used for adaptation. As training data for the encoder, it does now.
- **Final candidates:** trained on everything, thresholds per segment on mixed dev. The 68m variant was chosen on dev only (below).

  | H-typed first | **`kestrel-68m`** (`k68-all-d`) | `kestrel-32m` (`k32-all`) | `s68v-all` (library-only 68m) | `c68v-hum` | `s32v-all` | Haiku |
  |---|---|---|---|---|---|---|
  | library H-typed (n 1,494) | **0.780** | 0.760 | 0.781 | 0.773 | 0.755 | 0.769 |
  | public test H-typed (n 3,070) | **0.716** | 0.691 | 0.617 | 0.621 | 0.597 | – |
  | public test H-typed, coding-agent sources (n 1,857) | **0.707** | 0.685 | 0.669 | 0.668 | 0.635 | – |
  | TH, thinking (n 2,000) | **0.834** | 0.823 | 0.607 | 0.484 | 0.700 | – |
  | GANNET all (vs Argon; Flash 0.720) | **0.700** | 0.688 | 0.518* | 0.502* | – | – |

  \* BOBOLINK's GANNET predictions, made with the identity-sender framing bug (below).

  - **`kestrel-68m` vs `s68v-all` on the library: no regression beyond noise on any segment.**
    - H-typed −0.001 [−0.009, +0.007], H-typed+ctx −0.010 [−0.035, +0.012], AIUR +0.018, AR +0.000.
    - DEL −0.042 [−0.090, +0.008]; ALL −0.001 [−0.007, +0.005].
    - vs Haiku: H-typed +0.011 [−0.001, +0.023], ALL **+0.015 [+0.006, +0.023]**.
  - **On other people:** H-typed **+0.099 [+0.081, +0.120]** over `s68v-all`, H-typed+ctx +0.124, AR +0.040 [+0.026, +0.055].
  - **Thinking lives in the one shared model.** J vs Argon is 0.803 against Flash's 0.793 (dF +0.010 [+0.002, +0.019]).
    That is level with BOBOLINK's separate TH LoRA adapter (0.798). **One shared model, no adapter.**
- **Recommendation: ship `kestrel-68m` (int8).** It beats 32m on almost every set, paired:
  - library H-typed +0.020 [+0.011, +0.029], ALL +0.016;
  - public H-typed +0.026 [+0.016, +0.035], H-typed+ctx +0.060;
  - AR +0.024 / +0.037, TH +0.011;
  - GANNET ALL 0.700 vs 0.688.
  - Its cost on 4 threads: 24.6 items/s on the corpus mix, 33 ms per typed message, 152 MB, 0.8 GB RSS.
    `kestrel-32m` does 63 items/s in 13 ms, 92 MB and 0.5 GB; keep it as the low-power fallback.
- **An input-pipeline bug, found and fixed, that also affects earlier studies.**
  - Round 1's `items.parquet` `sender` column is `units.sender_role` (user/assistant/agent/tool).
  - IBIS's, EGRET's and GANNET's item tables carry `units.sender` instead. That is an identity field: NULL for human, AR, DEL and TH,
    `automation:*` for AIUR.
  - So their inputs were framed `human :` instead of `human user :`, `delegation :` instead of `delegation tool :`, and
    `thinking :` instead of `thinking assistant :`.
  - **Effect on library-only baselines** (`results/senderbug_impact.txt`): ≤ 0.007 on H-typed, so IBIS's H-typed conclusions stand.
    It cost DEL 0.04–0.09, AR up to 0.013 and TH 0.02–0.09.
  - A model continued on TH with the identity field collapsed on public H-typed (0.47); those runs are set aside in `models/_senderbug/`.
  - KESTREL uses `sender_role` everywhere (`data/sender_role.parquet`). FRAMING.md makes it part of the runtime contract.

## Data, licenses and spend

**Library** (private; this user's own data):
- the round-1 dataset with AVOCET v2 labels (three teachers on every human item);
- EGRET thinking: 14,000 TH train items labeled by Flash, with Argon on dev/test/fresh.

**Public.** IBIS's dataset, prepared by the real Pharos pipeline and cloned to `KESTREL/dataset`. Four sources:

| source | what | datasets |
|---|---|---|
| AT | raw Claude Code and Codex sessions | 27 Hugging Face datasets |
| PI | pi agent sessions | 32 datasets |
| OH | OpenHands feedback | `OpenHands/openhands-feedback` |
| WC | WildChat-1M coding chats | `allenai/WildChat-1M` |

**Exclusions:**
- `cfahlgren1/Fable-5-traces` (AGPL-3.0): 1,000 items, dropped from training and evaluation alike, including its items in IBIS's half B.
- 441 items in other datasets whose text copies it (same target hash).
- Every remaining dataset is MIT, Apache-2.0, BSD-3-Clause, CC0-1.0, CC-BY-4.0 or ODC-BY. Licenses come from IBIS's manifest; OH's and
  WC's were re-checked on the Hugging Face API.
- `data/licenses.csv` gives, per dataset: license, prepared, excluded and labeled items, and labeled items per split.

**Splits,** by family within each source:
- IBIS's labeled half B stays in test; half A goes to train.
- Other families are assigned by hash: train/dev/test 45/15/40 for AT and OH, 65/15/20 for PI and WC.
- One 11.9k-item family chains 12 datasets together through quoted passages. Families over 500 items are therefore split by root
  conversation instead.

**Labeled:** 18,895 public items with Flash; 10,260 of them also have Argon (all new dev/test items plus IBIS's 3,399).
- By split: train 10,170, dev 834, test 7,891.
- H-typed test items with both teachers: AT 319, PI 1,352, OH 186, WC 1,213. OH falls short of 300 because IBIS had already used
  nearly all of its typed messages.

**Labeling** (runner only, round-1 prompt and routes, `--refusal-registry`):
- `kes-flash` (agy gemini-3.8-flash low): 15,588 items, all usable, no dropped items.
- `kes-argon` (agy gemini-4-argon medium): 6,955 dev/test items. 4 gaps were repaired one item per request (`kes-argon-r1`).
- No quota errors and no Haiku.
- **Spend: $89.27** (Flash $32.03, Argon $57.15, repair $0.08), within the $150 ceiling.

## Training

**Recipe.** Each run continues one of BOBOLINK's v2 shared models (`s32v-all` or `s68v-all`) for 2 epochs, keeping round-1 framing
(with `sender_role`).
- Optimizer: lr 3e-5 (head 1e-3), bf16 on MPS.
- The sampler is balanced by source and segment. Each epoch (~76k items) draws:
  - all library human train items (12.5k);
  - 8k AIUR, 6k DEL and 22k AR from the library;
  - 7k TH;
  - every public train row twice, so public data is about 26% of an epoch.

**Targets:**
- Library rows: the v2 heads (y3 = three-teacher).
- TH and public rows: the y3 and y_hf heads learn F∩A where both teachers exist, else Flash. Each teacher head learns its own
  teacher, with that teacher's masked labels excluded.
- Scorer: mean(y3, y_hf), for every model.

**Runs:**
- `k32-lib`: the control (same sampler, no public data).
- `k32-x{AT,PI,OH,WC}`: the 32m LOSO folds.
- `k32-all`; `k68-xAT`; `k68-all`.
- `k32-all-d` and `k68-all-d`: library AIUR and DEL at 12k and AR at 30k per epoch. They were added after `k68-all` regressed on
  library DEL (−0.062 [−0.109, −0.020]).

**Selection, on dev only** (`results/select_*.txt`):
- `k68-all-d` vs `k68-all`: DEL +0.023 [+0.006, +0.045], AR +0.006, H-typed −0.005 [−0.013, +0.003], public dev ±0.000 → **chose `-d`**.
- `k32-all-d` vs `k32-all`: tied everywhere (DEL −0.001) → kept `k32-all`.
- On test afterwards: `k68-all-d` vs `k68-all` gives DEL +0.020, AR +0.012 [+0.003, +0.023], H-typed −0.001, H-typed+ctx −0.017.
  `k68-all`'s export is kept in `export/k68-all-not-selected/`.

**Cost.** 32m runs took 9–12 min, 68m runs 28–34 min (MPS, shared machine). Peak MPS memory 12.5 / 17.8 GB, RSS 5.4 GB.

**Machine.** The GPU was gated on memory: JAYHAWK held about 90 GB with full swap until 22:06, so training ran 22:11–02:16.

## LOSO: does training on several populations cut the drop on unseen people? (32m)

Sources: `results/loso_32.txt`, `pooled_32.txt`, `drop_32.txt`. All rows are H-typed, on the held-out source's **test** split (F∩A).

| held-out S | n / fam | F vs A | `s32v-all` | control `k32-lib` | **LOSO `k32-xS`** | LOSO − control | linear lib-only | linear mix-x (w) | linear gain | in-source `k32-all` |
|---|---|---|---|---|---|---|---|---|---|---|
| AT | 319 / 50 | 0.786 | 0.619 | 0.623 | **0.686** | +0.063 [+0.021, +0.112] | 0.516 | 0.534 | +0.019 [−0.001, +0.035] | 0.677 |
| PI | 1,352 / 242 | 0.767 | 0.603 | 0.609 | **0.633** | +0.024 [+0.007, +0.043] | 0.546 | 0.543 | −0.003 [−0.015, +0.008] | 0.681 |
| OH | 186 / 84 | 0.836 | 0.748 | 0.737 | 0.732 | −0.005 [−0.033, +0.023] | 0.700 | 0.649 | −0.050 [−0.088, −0.014] | 0.727 |
| WC | 1,213 / 346 | 0.772 | 0.532 | 0.532 | **0.552** | +0.020 [+0.005, +0.036] | 0.462 | 0.473 | +0.011 [−0.003, +0.025] | 0.699 |
| pooled | 3,070 / 722 | | | | | **+0.025 [+0.014, +0.036]** | | | **+0.002 [−0.006, +0.009]** | +0.078 over LOSO |

- **Linear columns.** "linear mix-x (w)" is DOTTEREL's V4-hybmaj recipe retrained on library + public minus S, with the vocabulary
  refit and public rows up-weighted to 26% of the training weight, as for the encoder.
  - Unweighted (public ≈ 4%), the pooled gain is −0.003 [−0.009, +0.004].
  - Its control, `lin-lib-same` (same pipeline, library only), is bit-identical to the shipped V4.
- **Drop** (library H-typed − unseen S, for the control model): AT 0.144, PI 0.153, OH 0.030, WC 0.231.
  - LOSO cuts it by 0.063, 0.034, 0.001 and 0.016; pooled **0.028 [0.015, 0.039]**.
  - The linear model's pooled "reduction" (0.016 [0.008, 0.025]) comes from its library score falling (−0.014), not from any gain on
    the unseen population (+0.002).
- **Encoder vs linear under diverse training.** The encoder's gain exceeds the linear model's by +0.023 [+0.011, +0.037] on H-typed,
  +0.037 [+0.008, +0.067] on H-typed+ctx and +0.020 [+0.006, +0.035] on AR. Its level on unseen people is higher by +0.092.
- **68m, AT held out:** `k68-xAT` 0.705 vs `s68v-all` 0.669 (+0.035 [+0.001, +0.083]).
  - vs `k32-xAT`: +0.019 [−0.005, +0.039]; on every AT item with both teachers, +0.025 [+0.008, +0.044].
  - Library H-typed is unchanged (+0.001).
  - No 68m control was trained. At 32m, the control equals the library-only model on public data (+0.004 [−0.011, +0.020] on AT).
- **AR,** LOSO − control, pooled: +0.018 [+0.005, +0.031]; linear −0.002.

## Final candidates vs library-only, per segment

Source: `results/final.txt`. `kestrel-68m` (`k68-all-d`), with paired deltas:

| set | n | `kestrel-68m` | vs Haiku | − `s68v-all` | − `c68v-hum` | − `k68-all` |
|---|---|---|---|---|---|---|
| library H-typed | 1,494 | 0.780 | +0.011 [−0.001, +0.023] | −0.001 [−0.009, +0.007] | +0.006 [−0.003, +0.016] | −0.001 |
| library H-typed+ctx | 364 | 0.745 | −0.022 | −0.010 [−0.035, +0.012] | −0.006 | −0.017 [−0.034, −0.001] |
| library H-template | 674 | 0.993 | +0.013 | +0.001 | +0.001 | +0.001 |
| library AIUR | 313 | 0.689 | −0.016 | +0.018 [−0.009, +0.045] | +0.008 | +0.008 |
| library AR | 947 | 0.795 | +0.035 | +0.000 [−0.012, +0.012] | +0.019 | +0.012 [+0.003, +0.023] |
| library DEL | 175 | 0.666 | +0.061 | −0.042 [−0.090, +0.008] | −0.017 | +0.020 |
| library ALL | 4,026 | 0.804 | **+0.015 [+0.006, +0.023]** | −0.001 [−0.007, +0.005] | +0.007 [+0.000, +0.012] | +0.003 |
| TH (EGRET test + fresh) | 2,000 | 0.834 (vs Argon 0.803, Flash 0.793) | | +0.227 | +0.350 | +0.005 |
| public H-typed, all sources | 3,070 | 0.716 | | **+0.099 [+0.081, +0.120]** | +0.095 | −0.002 |
| public H-typed: AT / PI / OH / WC | 319 / 1,352 / 186 / 1,213 | 0.689 / 0.705 / 0.755 / 0.730 | | +0.020 / +0.047 / +0.006 / +0.192 | | |
| public H-typed+ctx | 2,186 | 0.703 | | +0.124 [+0.088, +0.163] | +0.130 | +0.008 |
| public AR | 1,600 | 0.708 | | +0.040 [+0.026, +0.055] | +0.064 | −0.007 |
| public DEL | 254 | 0.747 | | +0.022 [−0.028, +0.068] | −0.005 | −0.011 |
| public TH | 409 | 0.783 | | +0.207 | +0.342 | −0.011 |

**`kestrel-32m`** (`k32-all`) vs `s32v-all`:
- library: H-typed +0.005 [−0.003, +0.012] (0.760; vs Haiku −0.009 [−0.020, +0.004]), AR −0.007, DEL −0.029 [−0.060, −0.000], ALL +0.001;
- public H-typed +0.094 [+0.076, +0.115]: 0.691, above the library-only 68m's 0.617;
- AR +0.047, TH +0.123.

## GANNET (untouched holdout; scored once)

Student vs Argon, beside Flash vs Argon, with family bootstrap; n = 2,231 items, 625 families.
- Rows marked "ref" are BOBOLINK's library-only GANNET predictions. They were made with the identity-sender framing, so they are a
  reference rather than a clean paired comparison.
- Source: `results/gannet_scores.csv`.

| model | ALL | H-typed (152 / 30 fam) | H-typed vs F∩A | AIUR (500) | AR (781) | DEL (84 / 12 fam) | TH (659) |
|---|---|---|---|---|---|---|---|
| Flash (bar) | 0.720 | 0.766 | | 0.544 | 0.736 | 0.795 | 0.803 |
| **kestrel-68m** | **0.700** (−0.019 [−0.038, +0.001]) | 0.677 (−0.089 [−0.120, −0.060]) | 0.746 | **0.612** (+0.069) | 0.705 (−0.031) | 0.192 | **0.818** (+0.016) |
| kestrel-32m | 0.688 (−0.032 [−0.050, −0.012]) | 0.681 (−0.085) | 0.732 | 0.564 (+0.020) | 0.697 (−0.039) | 0.181 | 0.823 (+0.020) |
| s68v-all (ref) | 0.518 | 0.684 (−0.083) | 0.749 | 0.448 | 0.693 | 0.457 | 0.306 |
| c68v-hum (ref) | 0.502 | 0.703 (−0.064) | 0.757 | 0.474 | 0.688 | 0.146 | 0.271 |

How to read it:
- **New conversations from this same user (H-typed, 30 families):** the KESTREL models are level with the library-only models
  (0.746 vs 0.749 against F∩A). Public data neither helps nor hurts here, as expected.
- **Overall,** they come within 0.02 of Flash. The gain over the references is mostly TH and AIUR.
- **GANNET DEL is still the known anomaly.** 77 of its 84 items are delegation results that the GANNET teachers label as reports.
  - With the correct `delegation tool :` tag, the students apply the library's result prior (few labels) and score 0.19.
  - The reference, fed the unseen `delegation :` tag, scored 0.46.
  - This supports the director's reading: a labeling difference on GANNET's delegation results, not model drift.

## Export, for product integration (`export/READY`)

Two exports, `export/kestrel-68m/` (from `k68-all-d`) and `export/kestrel-32m/` (from `k32-all`). Each contains:
- `model.fp32.onnx`: 274 MB for 68m, 128 MB for 32m.
- `model.int8.onnx`: 152 MB / 92 MB. Weight-only MatMulNBits, 8 bits, block 32, symmetric, accuracy_level 4 (76 / 40 nodes);
  the embedding table stays fp32.
  - Inputs: `input_ids` and `attention_mask`, int64 [batch, seq].
  - Outputs: `logits` [batch, 5, 54] (raw heads) and `probs` [batch, 54] (the scorer).
- `tokenizer.json` and `tokenizer_config.json`. SHA-256 `6c8aaa9a5420…` is the same for both models (the full hash is in the manifest).
- `manifest.json`:
  - labels: order, parents, ancestors;
  - head order and the scorer definition (`0.5·(σ(y3) + σ(y_hf))`, then threshold, then closure);
  - the segment list and per-segment thresholds (a default plus H-typed, H-typed+ctx, H-template, AIUR, AR, DEL and TH);
  - framing contract `kestrel-framing-v1` and the tokenizer SHA;
  - ONNX and int8 checks;
  - training sources with datasets, licenses and counts, plus the excluded AGPL dataset;
  - metrics: library, public, TH, GANNET, int8 quality and the CPU benchmark.
- `FRAMING.md`: the exact input construction.
  - Tag: `cohort sender_role :`.
  - Context: the last 512 characters for machine segments, the last 512 tokens for human segments, at least 64 tokens, kept from the end.
  - Text: pre-cut to 2,048 + ` … ` + 2,048 characters, then head 70% + tail 30% of the token budget.
  - Sequence: `[CLS] tag ctx [SEP] text [SEP]` with at most 512 tokens and right padding.
  - Then: mask-mean pooling, the scorer, per-segment thresholds and ancestor closure.
- `kframe.py`: the contract as pure Python plus `tokenizers`. It matches the training framing token for token on 20,279
  library and TH items (0 mismatches).
- `reference.py`: onnxruntime, from an item to labels.
- `make_fixture.py` and `parity-fixture.json`:
  - 187 invented cases: 11 framings (5 human segments, AIUR, AR assistant/agent, DEL result/prompt, TH) × 17 edge cases.
  - Edge cases include combining marks, ZWJ emoji, flags, tag sequences, CJK, RTL, CRLF, tabs, NBSP and zero-width characters,
    `⟦…⟧` markers, texts of 4,096 and 4,097 characters, very long text and context, and an `account:` sender.
  - Each case records token ids, the mask, and fp32 and int8 logits, probabilities and labels.
  - Re-running it reproduces every case: 0 mismatches, max |Δp| 5e-7.
- `bench.json`.

**Fidelity:**
- ONNX fp32 vs torch: max |Δlogit| 2e-5.
- int8 vs fp32 on 600 real held-out items per set (`results/int8_eval_*.json`):
  - 68m: library H-typed −0.000 [−0.002, +0.002], AR −0.004 [−0.009, +0.000], public H-typed +0.002, public AR −0.002;
  - 32m: within ±0.004.
- On the invented fixture strings, int8 and fp32 give equal label sets in 98–99% of cases.

**CPU benchmark.** 4 intra-op threads, int8, as a proxy for an M2 Air; this Mac was shared, so treat these as approximate.

| model | corpus mix items/s | H-typed items/s, p50 / p95 latency per message | AR | AIUR / DEL (≈ 450 tokens) | TH | peak RSS |
|---|---|---|---|---|---|---|
| kestrel-68m int8 | 24.6 | 32 /s, 33 / 59 ms | 30 /s, 29 ms | 11 / 12 /s, 100 ms | 20 /s | 0.79 GB |
| kestrel-68m fp32 | 15.0 | 19 /s, 54 / 92 ms | 18 /s | 7 / 8 /s | 12 /s | 0.99 GB |
| kestrel-32m int8 | 63.1 | 83 /s, 13 / 23 ms | 78 /s, 12 ms | 27 / 31 /s, 40 ms | 52 /s | 0.52 GB |
| kestrel-32m fp32 | 42.3 | 55 /s, 20 / 33 ms | 52 /s | 19 / 21 /s | 35 /s | 0.48 GB |

- Corpus mix by items: AR 0.817, AIUR 0.078, DEL 0.059, human 0.046. At these rates a 400k-item backfill takes about 4.5 h with 68m
  and 1.8 h with 32m.
- **Core ML/ANE (optional):** coremltools 9.0 does not convert ModernBERT cleanly; it fails on `new_ones` and the boolean-mask ops of
  the attention-mask construction (`export/kestrel-32m/coreml.json`). Not pursued.

## What worked, what didn't

- **Worked.**
  - Public data from other people, run through Pharos's own pipeline and labeled with Flash (Argon for evaluation), makes the encoder
    better on people it never saw: +0.025 pooled, +0.063 on the raw-session source, at no cost to this library.
  - Thinking folds into the shared model without an adapter.
  - Up-weighting library DEL/AIUR/AR, chosen on dev, removed the library DEL regression.
- **Didn't.**
  - The linear model cannot use the same data: neither unweighted nor up-weighted public rows move it on unseen populations, and
    up-weighting costs it 0.014 on the library.
  - Holding out OH gains nothing; every model's OH typed text is already near the library level.
  - The library DEL regression of the first recipe (−0.062) was fixed only partly (−0.042, with a CI that now includes zero).
- **Caveats.**
  - The 68m LOSO covers one source (AT, 50 families), so its CI is wide.
  - AT's public H-typed+ctx set is dominated by a few families (CIs ±0.15).
  - Public items are a minority of the mixed dev set: 418 of about 1.4k H-typed dev items.

## Cost

- **Labels:** $89.27.
- **GPU:** about 4 h on MPS (22:11–02:16) across 13 runs: 10 used, 3 discarded (sender bug).
- **Linear:** 70–130 s per run on CPU, 4–8 GB RSS. A helper agent built that pipeline (`linear/NOTES.md`).
- **Exports:** about 1 min each.
- **Inference:** see Export.

## Open questions

- **Library DEL.** Why do some delegation items go from exact to empty-handed after mixed training? It may be thresholds on
  empty-target items, or the format of PI's public delegations. A DEL dev set larger than 202 items would settle it.
- **A 68m control** (library-only continuation) would confirm that the AT gain at 68m is all due to public data. At 32m the control
  was neutral.
- **Rescoring earlier studies** with `sender_role` framing: IBIS's zero-shot AR/DEL and GANNET's DEL/AIUR/TH.

## Files (`~/pharos-labeling/studies/KESTREL/`)

- **`scripts/`:**
  - data: `build_public.py` (licenses, exclusion, splits, selection), `licenses.py`, `sender_roles.py`, `kdata.py`;
  - labeling: `create_job.sh`, `launch.sh`, `after_labels.sh`, `gaps.py`, `build_labels.py` (IBIS copy), `build_public_labels.sh`;
  - training and prediction: `kes_train.py`, `kpredict.py`, `queue_train*.sh`, `mem_gate.sh`;
  - scoring: `kscore.py`, `analysis.py`, `pooled.py`, `select_dev.py`, `gannet_eval.py`, `score_gannet.py` (copy, output path changed);
  - framing and export: `kframe.py`, `check_frame.py`, `export.py`, `finalize_export.sh`, `manifest_metrics.py`, `reference.py`,
    `make_fixture.py`, `FRAMING.md`, `export_queue.sh`;
  - checks: `bench.py`, `int8_eval.py`, `coreml_try.py`.
- **`data/`:** `public_items.parquet`, `licenses.csv`, `labels_public/`, `sender_role.parquet`, the selections. Runner exports are in `exports/`.
- **`models/<run>/`:** `final.pt`, `meta.json`, and `probs.npz`, which holds the raw 5-head outputs for every non-train row and
  every public row.
- **Predictions:** `preds_raw/` (baselines and linear runs) and `preds/gannet-*.npz`.
- **`results/`:** the tables above.
- **`linear/`:** the helper's linear pipeline and `NOTES.md`.
- **`export/`:**
  - `kestrel-68m/` and `kestrel-32m/`: the candidates.
  - `k68-all-not-selected/` and `dryrun-smoke/`: not candidates; each holds a note saying so.
- **Added package:** coremltools 9.0 (plus pyaml), for the optional Core ML attempt.

## Public-only variant (privacy option; added 03:45)

**Question.** Must the shipped weights avoid the user's private conversations? `kestrel-68m` continues `s68v-all`, so its weights are
shaped by this library. A public-only model starts from the **pretrained ettin encoder** and trains on the **permissive public train
splits only**. No library row and no EGRET thinking row is trained on.

How library data is used:
- **Thresholds:** library v2 dev and EGRET TH dev are used for per-segment threshold tuning only (mixed dev, as for every model).
  The manifest also carries `thresholds_public_dev_only`, tuned on public dev alone, which leaves no private data in the artifact at all.
- **Evaluation:** library held-out is used only to report how well the model transfers.

**Recipe.**
- `kes_train.py --init hf:jhu-clsp/ettin-encoder-{68m,32m} --public-only`, with heads initialised to label priors.
- lr 8e-5, the same 5 heads, targets, framing (`sender_role`) and scorer as the other models.
- Every public train row once per epoch (10,170 rows).
- **Epochs chosen on dev only**, between 4 and 8:
  - 68m: e4 vs e8 gives library dev ALL +0.018 [+0.002, +0.031], DEL +0.18, H-typed +0.002, public dev ALL +0.002 → **e4**.
  - 32m: e4 again (ALL +0.019, H-typed +0.006).
  - Eight epochs overfit the tiny segments.
- Training took 8 min (68m e4) and 3 min (32m e4) on MPS.

**How thin public data is.** Train rows, public vs library:

| segment | public train | library train |
|---|---|---|
| H-typed | 4,547 (PI 2,268, WC 1,794, OH 304, AT 181) | 7,527 |
| H-typed+ctx | 2,556 | 2,291 |
| H-resent / H-template / H-feedback | 199 / 25 / **0** | 132 / 2,436 / 161 |
| AR | 2,107 | 219,578 |
| TH | 603 (PI 601) | 14,000 (EGRET) |
| DEL | **129** | 17,364 |
| AIUR | **4** | 19,817 |

- AIUR, H-feedback and H-template are effectively absent. The public sources hold only 11 unique AIUR items and 428 DEL items in total.
- Public test sets exist only for H-typed, H-typed+ctx, AR, DEL (254) and TH (409). The cost for AIUR and H-feedback can be measured
  only on the library and GANNET.
- More public AR could be labeled cheaply (~38k unlabeled unique AR items; Flash ≈ $2 per 1k). AIUR and DEL cannot: the sources
  don't contain them.

**Quality cost of public-only.** `kestrel-68m-public` (`p68-e4`). Paired deltas with family-bootstrap 95% CIs; source: `results/public_final.txt`.

| set | n | public-only 68m | − `kestrel-68m` | − `s68v-all` (library-only) |
|---|---|---|---|---|
| **public H-typed, all sources** | 3,070 | 0.693 | **−0.023 [−0.033, −0.013]** | **+0.076 [+0.057, +0.099]** |
| — AT | 319 | 0.684 | −0.005 [−0.028, +0.017] | +0.015 [−0.021, +0.052] |
| — PI | 1,352 | 0.670 | −0.035 [−0.047, −0.024] | +0.012 [−0.006, +0.030] |
| — OH | 186 | 0.779 | +0.024 [−0.019, +0.066] | +0.030 [−0.013, +0.070] |
| — WC | 1,213 | 0.709 | −0.021 [−0.039, −0.004] | +0.171 [+0.126, +0.217] |
| public H-typed, coding-agent sources | 1,857 | 0.683 | −0.024 [−0.036, −0.013] | +0.014 [−0.001, +0.029] |
| public H-typed+ctx | 2,186 | 0.636 | −0.067 [−0.103, −0.032] | +0.057 [+0.017, +0.100] |
| public AR | 1,600 | 0.631 | −0.077 [−0.090, −0.063] | −0.037 [−0.054, −0.020] |
| public DEL | 254 | 0.699 | −0.048 [−0.092, +0.033] | −0.026 [−0.060, +0.035] |
| public TH | 409 | 0.754 | −0.029 [−0.055, −0.001] | +0.178 [+0.141, +0.218] |
| library H-typed | 1,494 | 0.710 (Haiku 0.769) | **−0.070 [−0.081, −0.058]** | −0.071 [−0.084, −0.059] |
| library H-typed+ctx | 364 | 0.682 | −0.063 [−0.091, −0.034] | −0.073 |
| library H-template | 674 | 0.986 | −0.007 [−0.013, −0.002] | −0.006 |
| library AIUR | 313 | 0.650 | −0.039 [−0.073, −0.005] | −0.021 [−0.054, +0.012] |
| library AR | 947 | 0.681 | −0.113 [−0.141, −0.091] | −0.113 |
| library DEL | 175 | 0.515 | −0.151 [−0.210, −0.090] | −0.192 |
| library ALL | 4,026 | 0.733 | −0.071 [−0.082, −0.061] | −0.072 |
| TH (EGRET held-out) | 2,000 | 0.760 | −0.074 [−0.084, −0.065] | +0.153 [+0.135, +0.170] |
| GANNET all, vs Argon (Flash 0.720) | 2,231 | 0.632 | kestrel-68m 0.700 | (ref 0.518) |
| GANNET H-typed vs F∩A | 152 | 0.660 | kestrel-68m 0.746 | (ref 0.749) |

**Further results:**
- **GANNET for the public-only 68m, vs Argon** (scored once; `results/gannet_scores_public.csv`): AIUR 0.558, AR 0.614, DEL 0.259,
  TH 0.745.
- **Thresholds tuned on public dev only** (`@pubdev`, the strictest option), compared with mixed-dev thresholds on the same weights:
  - library: H-typed −0.008 [−0.013, −0.002], ALL ±0.000, DEL −0.095;
  - TH −0.024;
  - public H-typed ±0.001, public DEL +0.071.
  So dropping the library from threshold tuning costs little, except on library DEL.
- **Public-only 32m** (`p32-e4`, not exported) vs `kestrel-32m`:
  - library H-typed 0.695 (−0.064 [−0.076, −0.053]), ALL −0.073, AIUR −0.142, DEL −0.143, AR −0.105;
  - public H-typed 0.675 (−0.015 [−0.025, −0.004]; +0.079 over `s32v-all`), AR −0.052;
  - GANNET all 0.604.

**Reading for the privacy decision:**
- **Other people's typed text,** the population a shipped model serves: public-only costs **0.023 [0.013, 0.033]** against
  `kestrel-68m`.
  - It is still +0.076 above the library-only model there.
  - Per source the cost is 0.00–0.04; OH is level.
- **Larger costs:** on other people's H-typed+ctx (−0.067), agent replies (−0.077) and thinking (−0.029). These are segments where
  public data is thin.
- **On this user's own library** it costs 0.07 across segments (0.11 on AR, 0.15 on DEL) and 0.07 on GANNET. That gap is the value
  of this user's own data for this user. It says little about other users.
- **The segments public data cannot supply** (AIUR with 4 train items, DEL with 129, H-feedback with 0, H-template with 25) are
  carried by transfer.
  - Library AIUR is −0.039 and DEL −0.151.
  - GANNET AIUR 0.558 is still above Flash (0.544).
- **Practical middle ground (not trained):** public-only weights plus more labeled public AR and TH would close the AR/TH part
  cheaply.
  - AIUR and DEL would need other permissive sources, or synthetic delegation and agent-in-user-role data.

**Export:** `export/kestrel-68m-public/`, same layout as the others.
- Files: ONNX fp32 and int8, tokenizer (the public ettin tokenizer, SHA `6c8aaa9a5420…`), `manifest.json`, FRAMING.md, `kframe.py`,
  `reference.py`, `make_fixture.py`, a parity fixture of 187 invented cases (re-run: 0 mismatches), `bench.json`.
- The manifest has `training.public_only_weights: true`, describes what library data was used for, and carries both threshold sets
  (`thresholds` from mixed dev; `thresholds_public_dev_only`).
- int8 − fp32 on real held-out items: within ±0.003 (`results/int8_eval_kestrel-68m-public.json`).
- CPU cost equals kestrel-68m's: int8, 4 threads, 23.8 items/s on the corpus mix, 34 ms per typed message, 0.8 GB RSS.
- `export/PUBLIC_READY` written.
