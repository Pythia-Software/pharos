> Sanitized copy of ~/pharos-labeling/studies/EGRET/REPORT.md (archived in full on euclid at "Pharos Labeling/2026-10-29-bird-studies"). Verbatim conversation text was removed.

# EGRET: thinking data (round 2) — REPORT (final, 14:40 MDT)

**Question.** Can retained model thinking (`TH`) be labeled with the candidate-0.3 taxonomy? How well do the teachers agree on it?
Do the round-1 students transfer to it, and what would a TH model need?

## Numbers first
All rows are on held-out TH (test + fresh, 2,000 items with both teachers) unless noted. J = mean Jaccard on closed label sets.
The fair bar is the other LLM: Argon vs Flash = Flash vs Argon = **0.793**.
| system | vs Flash | d vs Argon (on Flash) [95% CI] | vs Argon | d vs Flash (on Argon) |
|---|---|---|---|---|
| Flash–Argon agreement, n=3,000 dev+test+fresh | | | **0.796** [0.788, 0.803] | |
| c-e32 zero-shot (y_hf head, round-1 thresholds) | 0.562 | −0.231 [−0.247, −0.216] | 0.520 | −0.273 |
| c-e68 zero-shot | 0.435 | −0.358 [−0.375, −0.342] | 0.401 | −0.392 |
| c-e32, thresholds re-tuned on TH dev (flash head) | 0.718 | −0.075 [−0.085, −0.066] | 0.732 | −0.061 |
| c-e68, re-tuned | 0.599 | −0.194 | 0.623 | −0.170 |
| **TF-IDF + logistic regression trained on 14k TH train Flash labels** (71 s CPU) | **0.768** | **−0.025** [−0.035, −0.016] | 0.769 | −0.024 [−0.034, −0.014] |
- By split, c-e32 re-tuned scores 0.747 on test and 0.689 on fresh vs Flash; the TH linear model scores 0.772 and 0.764.
- **Label spend: $69.80** (Flash $44.58 on 19,924 items, $2.24/1k; Argon $25.22 on 3,000, $8.41/1k). Haiku $0 (not run).
  About $8 more of "possibly charged" exposure sits on quota-rejected attempts that carried zero tokens.
- **GANNET** (separate report): 2,351 items, $31.46. **Round 2 total: $101.26.**

## Snapshot and dataset
- **Snapshot.** An APFS clone (`cp -c`, instant) of `/Volumes/euclid/Pharos Backup Wednesday October 28th 9PM/catalog/catalog.sqlite3` at
  `/Volumes/euclid/Pharos Labeling/snapshots/2026-10-28-9PM/catalog.sqlite3`: 110,576,578,560 B, mtime 2026-10-28 20:27:45 MDT,
  no -wal or -shm, catalog schema 21 (equal to build `0cd9422`, `/tmp/pharos-labeling-bin`).
- **Prepare.** `prepare --snapshot-copy --splits-from ~/pharos-labeling/full-20261027` built `EGRET/dataset-20261028` in 18.3 min,
  with peak RSS 2.75 GB.
  - Size: 595,938 units, 695,870 items, 44,642 conversations.
  - Splits: 34,642 conversations inherited round-1 splits, 70 joined old families, 12 were quarantined, and 7,754 families are new.
  - The manifest lists no coverage gaps. Item IDs are stable vs round 1 (402,757 of 403,202 reappear).

## Thinking coverage
- The normalized reasoning store is present (extraction generation 47079; 5 conversations still pending).
- 236,440 thinking units and 244,685 items:
  - **Claude full text:** 243,158 items, averaging 948 chars.
  - **Codex summaries:** 1,527 items, averaging 135 chars.
  - **Antigravity: none.** 431,822 reasoning records are unavailable (redacted or encrypted, never inferred). The manifest does not
    split them by provider.
- Claude by depth: 0 158k · 1 81k · 2+ 4k. By length: <200 58k · 200–600 90k · 600–1.5k 52k · 1.5–4k 28k · 4k+ 14k.
  By split: train 165k · dev 29k · test 22k · fresh 27k (of which 9.8k are in GANNET families). 25,368 units have no `created_at`.

## Sample (seeded, `scripts/sample.py`)
- 19,924 TH items from 3,542 families.
- Excluded: GANNET families, duplicates, quarantine, and held-out `seen_in_train`.
- One item per target_hash. Length strata are sampled ∝ size^0.5. A family contributes at most 2% (fresh: 60 items).
- Packed 4 TH items per request.
| split | items | Codex | <200 | 200–600 | 600–1.5k | 1.5–4k | 4k+ | Argon |
|---|---|---|---|---|---|---|---|---|
| train | 14,000 | 1,000 | 3,816 | 3,726 | 2,865 | 2,123 | 1,470 | – |
| dev | 2,000 | 0 | 416 | 587 | 457 | 333 | 207 | 1,000 |
| test | 2,000 | 0 | 415 | 536 | 420 | 326 | 303 | 1,000 |
| fresh | 1,924 | 28 | 455 | 647 | 427 | 277 | 118 | 1,000 |
- Coverage is 100%: Flash usable on 19,924 items, Argon on 3,000.
- Tables are in `EGRET/data/` (see `LABELS_READY`). Target rule: Flash∩Argon where both exist, else Flash.

## Teacher agreement on thinking
- **Overall.** Flash–Argon J is 0.796, with exact match 0.40. For comparison, round-1 Flash–Argon was AR 0.783, human 0.789, DEL 0.730,
  AIUR 0.683, so **thinking is the easiest cohort for the teachers**.
- **By split:** dev 0.802, test 0.795, fresh 0.791.
- **By length:** 0.872 (<200) → 0.828 → 0.771 → 0.721 → 0.699 (4k+). **By depth:** 0 0.789, 1 0.831.
- **Label counts.** Mean labels: Flash 4.80, Argon 5.65, Flash∩Argon 4.53. need_context: Flash 0.1%, Argon 0.4%.
- **Label mix vs round-1 agent replies.** Flash rates (TH vs AR): C.deliberation.plan 0.88 vs 0.56, hypothesis 0.22 vs 0.03,
  evidence 0.22 vs 0.02, reconsider 0.15 vs 0.01, C.report 0.59 vs 0.73, P 0.001 vs 0.04.

## Round-1 students on thinking, zero-shot
- Inputs were built exactly as in round 1: `common.Encoder`, cohort tag `thinking`, and the 600-char context.
- Inference ran on CPU fp32 with 8 threads: c-e32 74 items/s (2.35 GB), c-e68 31 items/s (2.55 GB). No GPU was used.
- The models never saw the `thinking` tag. They fire `P` on 12% of TH items (Flash 0.1%). They miss deliberation:
  c-e32 recall is 0.05 for evidence, 0.24 for reconsider, 0.39 for hypothesis and 0.65 for plan.
- 68m transfers worse than 32m.
- Re-tuning thresholds on TH dev recovers about 0.16 J for c-e32, but still leaves −0.06 to −0.08. Fresh is about 0.05 worse than test.
- Fidelity check: CPU fp32 vs round-1 MPS bf16 on 299 gold3_test items with identical inputs gives J 0.785 vs 0.793 (noise level).

## What a TH model needs
1. **In-domain TH labels; nothing else is required.** A linear model on the 14k Flash TH labels comes within 0.025 of the other teacher.
   Round 1 showed fine-tuned encoders beat linear models by 0.03–0.06, so an encoder trained with TH labels should reach teacher level
   on TH.
   - Recommendation: add the EGRET train split, with its `thinking` tag, to BOBOLINK's shared model or to a TH specialist.
   - Tune TH thresholds per segment.
   - Long items are the weak spot for teachers too: 4k+ agreement is 0.70.
2. **Targets.** Train on Flash closures; Argon exists only on held-out items, by design. Evaluate against Flash∩Argon and in the
   two-teacher LOO above.
3. **Size.** 14k labels already give −0.025 with a linear model. About $2.2 per 1k Flash labels makes more TH labels cheap if a
   learning curve says so.
4. **Codex.** Codex summaries are 1,000 train items; there is no Codex held-out, so they cannot be evaluated separately.

## Caveats
- **No Haiku** (director 10:30, Claude quota). The three Haiku jobs (`egret-haiku-train-a`, `-train-b`, `-heldout`) were created but
  never run. There is no y3; LOO is two-teacher only.
- **agy quota wall, 10:52–14:14** (`RESOURCE_EXHAUSTED 429`).
  - The runner recorded the 407 rejected attempts as `tool_use` and halted only some runners, on `usage_missing`, instead of halting
    at once on the quota error as the docs describe. **This is worth a runner fix:** treat agy `AGY_ERROR … RESOURCE_EXHAUSTED` as a
    quota halt.
  - Recovery used `reconcile --retry-tool-use`; every rejected attempt was verified to carry the 429.
- **Packing.** Requests are TH-only, while round 1 mixed cohorts. CHICKADEE found that packing shifts Haiku need_context; the effect on
  Flash and Argon is unmeasured.
- **Thresholds.** The TH linear baseline and the threshold-tuned rows use the TH dev split, which is legitimate (dev only).
  No GANNET item was predicted or used.
- **Labels.** All labels are LLM labels; Flash∩Argon is strict.
