> Sanitized copy of ~/pharos-labeling/research/tracks/B-embeddings/REPORT.md (archived in full on euclid at "Pharos Labeling/2026-10-28-student-research"). Verbatim conversation text was removed.

# Track B: frozen pretrained embeddings + lightweight heads

**Question.** How close do frozen sentence embeddings plus cheap heads get to the three-teacher intersection `y3`
(and to the LLMs on the harness LOO comparison), and what does embedding cost per 400k items?

**Short answer.**
- Frozen transformer embeddings + MLP plateau at gold3_test J **0.730 raw / 0.736 weighted** (B-gte-full). That is
  about TF-IDF level (track A 0.727 / 0.745), well below fine-tuning (track C's best clean row c-e68-512-all: 0.782 / 0.787; the 0.795 c-e32-all-adaptF1-hf row cited earlier is a leaky fresh-adaptation row), and needs the GPU.
- The best thing this track found is a **CPU-only static-embedding model**: a Model2Vec table fine-tuned end to end,
  plus mean bags, track A's TF-IDF SVD and an MLP. It scores **0.737 / 0.747** at **~17k items/s on 8 CPU threads**
  (400k items in about 25 s plus featurisation) and is easy to port to Go.
- Averaged with A's TF-IDF model (still CPU only) it scores **0.749 / 0.761**. Its LOO matches Flash (HA 0.742 vs
  0.742) and beats Argon (HF 0.741 vs 0.728), but stays below Haiku (FA 0.725 vs 0.757).
- Fresh agent_in_user_role (AIUR) is still the weak cell: **0.55–0.59** (C reaches 0.60–0.67; teacher ceiling there is about 0.67).

## Results (shared harness; gold3_test, target y3; LOO = vs F∩A / H∩A / H∩F, LLM refs 0.757 / 0.742 / 0.728)

The fresh AIUR cell is the agent_in_user_role × split=fresh crossing (n=250). Its Jaccard comes from the saved preds
through `rlh.score` (`scripts/strata.py`). Every other number is the harness's own.

| run | inputs / head | J raw/w | split=test r/w | fresh r/w | fresh AIUR | LOO FA/HA/HF | gold2_test J | flash_test J | inference |
|---|---|---|---|---|---|---|---|---|---|
| **Bblend-static-svd-2seeds+A-ia** | 2 seeds of Bst-base32-ft-3bag-svd + A-ia-wcxhmr, prob. average (weights 0.1/0.5/0.4 chosen on gold2_dev) | **0.749/0.761** | 0.777/0.788 | 0.716/0.750 | 0.574 | 0.725/**0.742**/**0.741** | – | – | CPU only |
| Bblend-static-svd+A-ia | Bst-base32-ft-3bag-svd + A-ia-wcxhmr (0.5/0.5) | 0.744/0.757 | 0.772/0.785 | 0.712/0.725 | **0.585** | 0.730/0.740/0.737 | 0.755 | – | CPU only |
| Bblend-purestatic+A-ia | pure static Bst-base32-ft-3bag-h256 + A-ia-wcxhmr (0.5/0.5); no SVD projection | 0.737/0.745 | 0.764/0.772 | 0.704/0.708 | 0.587 | 0.736/0.736/0.732 | – | – | CPU only |
| **Bst-base32-ft-3bag-svd** | supervised static table (potion-base-32M init) bags text/head128/ctx + meta + A TF-IDF SVD 256+48; MLP 2x1024 | **0.737/0.747** | 0.773/0.778 | 0.696/0.735 | 0.548 | 0.716/0.737/0.731 | 0.744 | 0.738 | **16.9k/s CPU** (8 thr) + SVD featurisation 6.3k/s/thread |
| Bst-base32-ft-3bag-svd-seed1 | same, seed 1 | 0.733/0.743 | 0.764/0.770 | 0.698/0.734 | 0.548 | 0.718/0.733/0.728 | – | – | 17.6k/s CPU |
| Bst-base32-ft-svd-h256-elr1e2 | same, head bag 256, table lr 1e-2 | 0.734/0.744 | 0.769/0.775 | 0.692/0.728 | 0.555 | 0.715/0.731/0.729 | 0.745 | 0.738 | 17.8k/s CPU |
| Bst-base32-ft-svd-inter | same + inter_avail head used for gold3 | 0.726/0.740 | 0.761/0.768 | 0.686/0.709 | 0.529 | 0.727/0.723/0.722 | 0.745 | 0.738 | 17.3k/s CPU |
| **Bst-base32-ft-3bag-h256** | pure static (no TF-IDF): table + bags + meta; MLP | 0.718/0.733 | 0.752/0.764 | 0.679/0.706 | 0.518 | 0.709/0.716/0.713 | – | – | **18.8k/s CPU**, 129 MB table |
| Bst-base32-ft-4bag-bigram | pure static + hashed WordPiece-bigram bag (2^20 x 64), 5-epoch schedule (best epoch 1) | 0.721/0.735 | 0.750/0.762 | 0.686/0.730 | 0.514 | 0.717/0.722/0.718 | 0.730 | 0.728 | 17.2k/s CPU |
| Bst-base32-ft-4bag-bigram-e2 | same, 2-epoch schedule | 0.714/0.726 | 0.743/0.756 | 0.681/0.709 | 0.538 | 0.714/0.719/0.712 | 0.733 | 0.726 | 16.5k/s CPU |
| Bst-potionret32-ft-3bag | pure static, potion-retrieval-32M init | 0.711/0.723 | 0.743/0.752 | 0.674/0.709 | 0.503 | 0.705/0.711/0.708 | 0.726 | 0.721 | 14.3k/s CPU |
| B-gte-full-mlp2x2048 | frozen gte-modernbert-base (512) + bge-small head128/chunk-mean/ctx + potion + A SVD + meta; MLP 2x2048 | 0.730/0.736 | 0.755/0.757 | 0.701/0.737 | 0.527 | 0.716/0.731/0.728 | 0.740 | 0.736 | 118/s (shared MPS), ~57 min/400k |
| B-gte-combo-mlp2x2048 | gte-modernbert + bge-small ctx + potion text/ctx + meta | 0.723/0.731 | 0.750/0.762 | 0.690/0.711 | 0.528 | 0.716/0.723/0.718 | 0.723 | 0.717 | 217/s MPS |
| B-combo4-mlp2x2048 | bge-small (512) + ctx + potion text/ctx + meta | 0.706/0.717 | 0.737/0.747 | 0.670/0.684 | 0.515 | 0.713/0.707/0.703 | 0.717 | 0.709 | 682/s MPS |
| *refs* A-ia-wcxhmr (TF-IDF) | | 0.727/0.745 | 0.760/0.781 | 0.688/0.697 | 0.549 | | | | CPU |
| *refs* c-e68-512-all (fine-tuned 68M, C's best clean row) | | 0.782/0.787 | 0.798 (w) | 0.768/0.791 | 0.671 | 0.774/0.781/0.779 | 0.793 | 0.784 | 436/s MPS |

Dev numbers (gold3_dev) are about 0.04 higher across the board. Selection used dev only: gold2_dev and flash_dev
Jaccard, plus a diagnostic on dev items whose 200-char prefix never occurs in train. Test and fresh were looked at only
through serious runs. gold3 SE is about 0.005–0.01, so rows within 0.01 are ties.

## Embedding cost (full pass over all 403,202 dataset items; length-sorted token-budget batches; MPS fp16 shared with track C)

| embedder | params | device | max tokens | items/s | 400k items | peak RSS / MPS |
|---|---|---|---|---|---|---|
| potion-retrieval-32M / potion-base-32M (Model2Vec, static) | 32M (table) | CPU | full text | 13.4k / 12.0k | 30–34 s | 7 GB RSS (all texts in RAM) |
| potion-code-16M | 16M | CPU | full text | 5.7k | 70 s | 5 GB |
| supervised static model (tokenise + bags + MLP, end to end) | 33M | CPU 8 thr | full text | 17–19k | ~25 s | – |
| bge-small-en-v1.5 | 33M | MPS fp16 | 512 (head) | 1,050 | 6.4 min | 6.3 GB / 1.1 GB |
| bge-small-en-v1.5 | 33M | MPS | 128 | 1,410 | 4.7 min | |
| bge-small-en-v1.5 chunk-mean | 33M | MPS | 512 x 602k windows | 364 | 18 min | |
| bge-small-en-v1.5 context (600 chars) | 33M | MPS | 256 | 2,450 | 2.7 min | |
| gte-modernbert-base | 149M | MPS fp16 | 512 | 244 (contended; 232 benchmark) | 27 min | 4.8 GB / 2.2 GB |
| gte-modernbert-base | 149M | MPS | 2048 | 75 (bench) | 1.5 h | |
| Qwen3-Embedding-0.6B | 600M | MPS fp16 | 512 | 61 (bench) | 1.8 h | 2.9 GB / 3.4 GB |
| bge-small / MiniLM-L6 on CPU (torch fp32) | 33M / 22M | CPU | 512 / 256 | 138 / 500 | 48 min / 13 min | |

Tokens: mean 368 per item, median 55, p90 1,450. 21% of items exceed 512 tokens; agent_in_user_role averages
5,150 characters and holds most of the characters. Head training is CPU-only: 3–10 min, and 7–14 GB peak RSS
(ru_maxrss). Two static runs at once briefly exceeded the 16 GB budget; a bigram run needs 12 GB, so I killed the second one. Head inference is >50k items/s and negligible.

## What worked
1. **Fine-tuning the static table end to end** (fastText-style; `scripts/train_static.py`). The table starts as a
   Model2Vec potion model and is trained with SparseAdam, along with mean bags over the full text, the first 128 tokens
   (the instruction part) and the context. This gives about +0.03 dev J over the same model with a frozen table
   (gold2_dev 0.737 → 0.763–0.766). It also beats frozen bge-small or gte-modernbert + MLP.
2. **Full text beats truncation.** Static Model2Vec over the whole untruncated text (CPU, 30 s) matches bge-small
   head-truncated at 512 tokens, and the two are complementary (+0.015 when combined). Chunk-mean and a separate
   128-token head embedding also help the transformer heads (s6a: gold3_dev 0.758 → 0.777).
3. **Big consensus targets over y3.** Heads trained on y_hf (132k train rows) and scored with dev-tuned thresholds
   beat a y3 head (10.5k rows) by about 0.01 on gold3_dev. Multi-task y_hf + flash heads share one MLP. An inter_avail
   head did not beat y_hf for this model (0.726 vs 0.737 test; 0.775 vs 0.779 dev).
4. **Meta one-hots** (cohort, actor_kind, sender, provider, kind, channel, chunk flags) add +0.02.
5. **Embeddings and TF-IDF are complementary.** Labels on flash_test, AIUR:
   - Embeddings win on P.frame.criteria (+0.27 F1), P.request.work.verify/revise, C.feedback and
     C.report.verification. Against A on gold3_test, delegation scores 0.749 vs 0.589.
   - TF-IDF wins on lexically marked labels: P.request.input.choice, P.control.continue,
     C.deliberation.hypothesis/reconsider, and P.request.work.implement on fresh AIUR.

   A plain probability average of the two CPU models adds +0.007–0.012 on test.

## What didn't work
- **Anti-memorisation weighting.** I tried target_hash dedup weights, 1/count(200-char prefix)^x, 1/family_size^0.5
  and family caps. None helped on any dev proxy, including dev AIUR items with an unseen prefix (flash_dev novel AIUR
  0.646 → 0.58–0.60). Every serious run is unweighted.
- **A separate context embedding** gave about zero gain on gold2_dev. It stays in as a cheap feature.
- **Bigger frozen encoders** gave small gains at 4–9x the GPU cost. Going from bge-small to gte-modernbert-base added
  +0.01–0.017. Qwen3-Embedding-0.6B was benchmarked (61 items/s, 1.8 h per 400k) but not run, after the GPU was given
  to track C.
- **Hashed bigram bags (fastText n-grams)** give the lowest dev loss of any model (y_hf BCE 0.0515 vs 0.0531) but
  no Jaccard gain (gold2_dev 0.764–0.767 vs 0.767; test 0.714–0.721 vs 0.718). The bigram table memorises and
  overfits after one epoch, and it does not replace the TF-IDF SVD (which is worth +0.019 on test).
- **Threshold tuning on gold3_dev overfits.** Per-label Jaccard tuning on the 1,333 items inflates dev, so I used
  per-label F1. The dev → test gap is about 0.04 in every configuration, mostly from fresh AIUR.
- **Blending with track C.** A dev-only check averaging Bst-base32-ft-3bag-svd with c-e32-all-adaptF1-hf (the leaky row) (0.5/0.5,
  weights on flash_dev) gave flash_dev 0.795 and gold3_dev 0.807, against C alone at 0.802 / 0.816. So the CPU model
  does not lift C by simple averaging. A learned stacker might still find something.
- **Fresh AIUR.** Every B model under-predicts there: 2.8 predicted labels vs 4.2 target, with recall collapsing on
  P.request.work.implement (0.09) and P.frame.constraint. That fits the label shift D-shift found (train AIUR 1.8
  labels/item, fresh 4.2). Semantic embeddings did **not** generalise better than n-grams in this cell
  (0.50–0.55 vs A 0.549). Only the blend reached 0.585.

## Cost per 400k items (inference)
- **CPU static model:** about 25 s compute at 17k items/s on 8 threads. Add about 65 s on one thread for A's TF-IDF
  hashing and SVD projection, or drop the SVD and take Bst-base32-ft-3bag-h256 at −0.019 J. Training takes about
  9 min on CPU.
- **Blend with A's TF-IDF LR:** about 2–3 min CPU in total.
- **Frozen gte-modernbert pipeline:** about 1 h on the shared MPS GPU. bge-small pipeline: about 10 min.
  Neither is better than the CPU model.

## Go portability
- **Pure static model** (Bst-base32-ft-3bag-h256): a WordPiece tokeniser (bge-base uncased vocab; about 150 lines of
  Go or an existing library), a 63k x 512 table (129 MB fp32, 65 MB fp16), three means, LayerNorm and two dense layers
  (1,580 → 1,024 → 1,024 → 54). Pure Go with no cgo; essentially as hard as a linear model.
- **Adding A's TF-IDF SVD** needs sklearn-compatible hashing (murmurhash3 word 1–2 grams) and a
  256 x 1.3M projection (1.35 GB fp32), which is heavy. It could be pruned or quantised, or the SVD replaced by A's
  sparse linear model's logits.
- **Transformer-embedding variants** need ONNX Runtime in Go (cgo) plus about 100–600 MB of models, at about 100
  items/s per CPU (C's int8 measurement): 1+ h per 400k on CPU. Not worth it when the static model is equal or better.

## Saved artefacts (for stacking)
- **Embeddings:** `tracks/B-embeddings/emb/<name>.npy`, float16 [403,202 x d], L2-normalised, rows aligned to
  `emb/items.ids.txt` (all dataset items sorted by id; a superset of items.parquet). See `emb/README.md`.
  Files: bge-small_text512 / _head128 / _chunks512 / _ctx (384); gte-mb_text512 (768); potion-retrieval-32M,
  potion-base-32M and potion-code-16M `_text` / `_ctx` (512 / 512 / 256).
- **Predictions:** `tracks/B-embeddings/preds/<run>.npz` (harness format, probabilities plus thresholds). Every
  non-blend run has all six sets (gold3, gold2 and flash, dev and test); the blends have gold3 and gold2. Best
  for stacking: Bst-base32-ft-3bag-svd (+ seed1), Bst-base32-ft-3bag-h256 (pure static) and B-gte-full-mlp2x2048.
- **Models:** `models/<run>.pt` (static models: state dict and args).
- **Scripts:** `scripts/embed.py`, `embed_static.py`, `heads.py` (frozen features → MLP), `train_static.py`,
  `blend.py`, `strata.py` (headline cells), `compare_labels.py` (per-label diff between runs).
- **Package added:** model2vec 0.10.0.

## Recommendations
1. For a Pharos-native CPU labeler, take **Bst-base32-ft-3bag-h256** (pure static, Go-trivial, 0.718/0.733) or its
   +SVD version (0.737/0.747). Use it as the cheap first stage of a cascade: send fresh/unseen-template AIUR and
   low-margin items to an LLM, or to C's fine-tuned encoder.
2. If a GPU or ONNX is acceptable, fine-tuning (track C) dominates frozen embeddings. Don't invest further in
   frozen transformer embeddings + heads.
3. Fresh AIUR needs data, not features. Label a few hundred new-template AIUR items, or retrain often. Per-cohort
   prior and threshold shifts can't be validated without fresh labels.

## Open questions
- Would a static table distilled from C's fine-tuned encoder (tokenlearn-style regression on C's embeddings) keep
  more of C's quality at static cost? Probably the best next step for the Go tier.
- Why does word-level TF-IDF SVD help while hashed WordPiece bigrams do not? Candidates: whole-word tokenisation (the TF-IDF token pattern), the separate context TF-IDF block, or SVD's regularisation. Worth isolating before porting the SVD.
- Seeds and ensembles of the static model: +0.005 from two seeds (inside the blend); not explored further.


---

# Phase 3: distillation (2026-10-29 00:45–02:15, CPU only)

**Question.** Can the Go-native static model (fine-tuned Model2Vec table + MLP, no TF-IDF SVD) close its gap to C
by distilling from C over the corpus?

**Answer: no.** Every distillation variant lands within ±0.007 J of the undistilled model on gold3_test:
0.713–0.725 raw against the 0.718 baseline, and C is at 0.782. Differences of that size are noise (SE ≈ 0.005–0.01).
The larger test sets show the same picture: gold2_test 0.727–0.737 against 0.732, and C 0.793.

What distillation does do:
- **Moves the student toward C.** Fidelity to C's corpus labels on gold3_test rises from 0.780 to 0.81. It does not
  move the student toward y3.
- **Fixes delegation.** Delegation rises from 0.631 to 0.69–0.72, the Flash-noise effect A found. Training on C's
  labels alone (no consensus targets at all) gives the most: 0.715.
- **Leaves fresh AIUR unchanged** at 0.48–0.53, against 0.671 for C.

A larger MLP (3x2048) changes nothing, so the limit is the bag-of-tokens representation (no word order, negation or
quotation scope) and the fact that train has no fresh-template AIUR. It is not label noise or head capacity.

## Setup
- **Student:** `scripts/train_static.py`, the same as Bst-base32-ft-3bag-h256. potion-base-32M table fine-tuned with
  lr 1e-2. Mean bags over the full text, the first 256 tokens and the context, plus meta features. MLP 2x1024, multi-task
  heads, 5 epochs, CPU with 8 threads. Gold3 is scored with the distilled head; thresholds are per-label F1 tuned on
  the matching dev set.
- **Teachers:**
  - C-32m: c-e32-512-all train-item probabilities from track A's `c_all_train_probs.npz`. Its y3 scorer is the
    mean of the y3 and y_hf heads.
  - C-68m: C's corpus file `C-finetune/corpus/c-e68-512-all.npz`. It provides `y3_probs` (min_hfa scorer) and
    `labels` (thresholded and closed).

  Only train-split rows are used. Both teachers are **in-sample** on those rows, because C trained on them. No
  dev/test/fresh rows were used for training.
- **Targets:**
  - (a) `cs`: the teacher's y3-scorer probabilities as soft BCE targets.
  - (b) `ch`: hard pseudo-labels. For C-32m, closure(cs ≥ 0.5); for C-68m, C's thresholded closed labels.
  - (c) `cm`: 0.5 inter_avail + 0.5 cs.

  Each is trained alongside y_hf and flash heads unless noted. Early stopping uses the y_hf BCE on gold2_dev.

## Results (harness; gold3_test unless noted; fresh AIUR = agent_in_user_role × split=fresh, n=250)

| run | teacher / target | J raw/w | split=test w | fresh r/w | fresh AIUR | delegation | LOO FA/HA/HF | gold3_dev | gold2_test | flash_test | fidelity to C-68 | CPU items/s |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| Bst-base32-ft-3bag-h256 (baseline) | consensus y_hf + flash | 0.718/0.733 | 0.764 | 0.679/0.706 | 0.518 | 0.631 | 0.709/0.716/0.713 | 0.766 | 0.732 | 0.727 | 0.780 | 18.8k |
| Bst-dist-c32-cs | (a) C-32 soft + y_hf + flash | **0.725/0.735** | 0.765 | 0.685/0.718 | 0.522 | 0.695 | 0.712/0.727/0.719 | 0.777 | 0.737 | 0.730 | 0.798 | 18.8k |
| Bst-dist-c68-cs | (a) C-68 soft + y_hf + flash | 0.717/0.729 | 0.760 | 0.677/0.706 | 0.513 | 0.646 | 0.717/0.723/0.713 | 0.769 | 0.737 | 0.729 | 0.805 | 18.8k |
| Bst-dist-c68-cs-yhf | (a) C-68 soft + y_hf (no Flash head) | 0.718/0.730 | 0.756 | 0.683/0.726 | 0.507 | 0.685 | 0.710/0.722/0.712 | 0.773 | 0.735 | 0.709 | – | 19.0k |
| Bst-dist-c68-cs-only | (a) C-68 soft only (no LLM labels at all) | 0.723/0.735 | 0.764 | 0.682/0.728 | 0.502 | **0.715** | 0.715/0.728/0.718 | 0.778 | 0.736 | 0.720 | **0.814** | 20.1k |
| Bst-dist-c32-ch | (b) C-32 hard + y_hf + flash | 0.713/0.727 | 0.754 | 0.678/0.724 | 0.505 | 0.673 | 0.700/0.718/0.707 | 0.773 | 0.728 | 0.720 | – | 18.8k |
| Bst-dist-c68-ch | (b) C-68 hard + y_hf + flash | 0.715/0.726 | 0.750 | 0.680/0.725 | 0.509 | 0.713 | 0.707/0.718/0.708 | 0.772 | 0.727 | 0.720 | – | 18.8k |
| Bst-dist-c32-cm | (c) 0.5 inter_avail + 0.5 C-32 soft | 0.719/0.732 | 0.765 | 0.681/0.696 | 0.525 | 0.621 | 0.721/0.720/0.714 | 0.768 | 0.737 | 0.729 | – | 18.9k |
| Bst-dist-c68-cm | (c) 0.5 inter_avail + 0.5 C-68 soft | **0.725/0.735** | 0.765 | 0.688/0.714 | 0.527 | 0.652 | 0.722/0.725/0.719 | 0.772 | 0.736 | 0.731 | 0.804 | 18.6k |
| Bst-dist-c32-cs-big | (a), MLP 3x2048 (capacity probe) | 0.718/0.734 | 0.765 | 0.673/0.720 | 0.481 | 0.674 | 0.712/0.723/0.712 | 0.771 | 0.735 | 0.729 | 0.789 | 16.9k |
| *+SVD* Bst-base32-ft-3bag-svd (baseline) | consensus | 0.737/0.747 | 0.778 | 0.696/0.735 | 0.548 | 0.749 | 0.716/0.737/0.731 | 0.779 | 0.744 | 0.738 | 0.802 | 16.9k + SVD feats |
| *+SVD* Bst-dist-c32-cs-svd | (a) C-32 soft + y_hf + flash | 0.735/0.745 | 0.771 | 0.701/0.740 | 0.562 | 0.725 | 0.715/0.734/0.730 | **0.790** | **0.749** | **0.740** | 0.808 | 19.4k + SVD feats |
| *ref* c-e68-512-all (C) | | 0.782/0.787 | 0.798 | 0.768/0.791 | 0.671 | 0.737 | 0.774/0.781/0.779 | 0.827 | 0.793 | 0.784 | 1 | 436 (MPS) |

Fidelity is the Jaccard of the student's closed gold3_test predictions against C-68's corpus labels (`scripts/fidelity.py`).
Training takes 7–10 min on CPU with 8 threads. Peak RSS is 15–15.7 GB for the pure-static runs; the +SVD run hit
17.3 GB, briefly over the 16 GB budget. Runs went one at a time. Inference is unchanged (17–20k items/s on CPU),
because distillation only changes the training targets.

## Findings
1. **Distillation over train items does not close the gap.** The 0.06 J gap to C stays put. Soft targets are
   neutral to slightly positive (+0.000–0.007 on gold3_test, +0.003–0.005 on gold2/flash_test). Hard pseudo-labels
   are slightly negative (−0.003–0.005 on test; they drop C's calibrated uncertainty). Mixing with consensus
   targets is neutral. The C-68 teacher is no better than C-32 for the student, even though it is the better model.
2. **Why it can't close the gap:**
   - C's probabilities on train items are in-sample, so they are close to the consensus labels the student already
     learns from. Little new information is transferred.
   - Every train item already has a Flash label, and the corpus has no unlabeled train items beyond those, so
     there is no extra data to distil on.
   - The student cannot represent what C adds: order-dependent cues such as quoted reports vs. requests (the C.report
     false positives on fresh AIUR) and negation.
   - Fidelity to C saturates around 0.80–0.81, and the bigger MLP doesn't help.
3. **Delegation improves, as in track A.** Delegation rises from 0.631 to 0.69–0.72, most with the pure-C target
   (no Flash head): the student stops copying Flash's over-labeling of delegation results as reports. Unlike A, the
   static model's **fresh AIUR doesn't get worse** with distillation (0.50–0.53 against 0.518; only the bigger MLP
   drops to 0.481), but it doesn't improve either.
4. **Training only on C's labels is as good as training on LLM consensus** (Bst-dist-c68-cs-only: 0.723/0.735,
   fidelity to C 0.814). So the Go-native student can be retrained from C's labels alone, without new LLM spend.
   That matters for the next point.
5. **The +SVD variant** with distillation ties its baseline on gold3_test (0.735/0.745 vs 0.737/0.747) and is
   slightly better on the larger sets (gold2_test 0.749 vs 0.744, flash_test 0.740 vs 0.738, gold3_dev 0.790)
   and on fresh AIUR (0.562 vs 0.548).

## Recommendations
- **Don't ship the Go-native static model expecting C-level quality.** It stays at about 0.72–0.74 J however it
  is trained. Use it as a cheap first stage: about 25 s per 400k on CPU, with confident items accepted and the rest
  sent to C, ONNX or an LLM. Or use it where about 0.73 is acceptable.
- **The decisive next experiment is transductive distillation onto new-era items.** C labels newly arriving items
  (it generalises to fresh AIUR at 0.67 where the student gets 0.52), and the student is retrained on those labels,
  which finding 4 makes cheap and label-free. This needs an approved protocol: for example, split fresh by family
  into halves A and B, distil on C's labels of half A, and score half B. Under the current rules I did not train
  on fresh.
- **If Go-native quality matters more than simplicity,** the remaining options are an ONNX encoder (C's int8
  ettin-32m: about 0.74 at about 100 items/s per CPU process) or word-order-aware cheap features (positional
  bags, e.g. separate quoted/blockquote spans, and the first sentence vs. the rest). The static bag can't do this.

New artefacts: `preds/Bst-dist-*.npz` (all six eval sets), `models/Bst-dist-*.pt`, `scripts/fidelity.py`,
`scripts/p3table.py`, and `train_static.py --cprobs/--heads cs|ch|cm|cyhf`.
