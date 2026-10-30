> Sanitized copy of ~/pharos-labeling/research/tracks/D-shift/REPORT.md (archived in full on euclid at "Pharos Labeling/2026-10-28-student-research"). Verbatim conversation text was removed.

# Track D: distribution shift (dev → test/fresh) — saved by the director from the analyst's reply

## Answer
The dev→test drop is almost entirely one cell: agent_in_user_role (AIUR) in the fresh split.
- Smoke char-LR on gold3: fresh AIUR 0.342 (n=250) vs split=test AIUR 0.827 (n=63). 80% of the "0.44 AIUR on gold3_test" items are fresh.
- Fresh without AIUR scores 0.702, the same as test (0.701). Dev reweighted to test's cohort mix scores 0.716 vs 0.701: the rest is mix + noise. Fixing fresh AIUR to its test level would lift gold3_test 0.658 → ~0.717.
- Cause: fresh AIUR is a new population of automation prompts (76% codex): TL0/TL1 orchestration harness, director-style grid-review prompts [template opening omitted], candidate/review handoffs, codex prompts with image-attachment markup [markup omitted], retrieval-context prompts [template heading omitted]. Train AIUR is 60% Antigravity spreadsheet-labeling prompts. Train has zero AIUR items after 2026-10-11; only 14.5% of fresh AIUR has a near-duplicate prefix in train.
- Labels shift too: fresh AIUR y3 is 27% empty (train 63%), 4.2 labels/item (train 1.8).
- Teachers do degrade, measured fairly: Haiku vs F∩A 0.831 on test AIUR → 0.674 on fresh AIUR (its 0.83 vs y3 can't drop since y3 ⊆ Haiku). The teachers lose 0.16 on this cell; the student loses 0.49.

## AIUR composition
| split | n | families | top family share | eff. families | claude/codex/antigravity | items sharing a text hash | p200 near-dup in train | latest item |
|---|---|---|---|---|---|---|---|---|
| train | 19,817 | 4,025 | 0.458 | 4.8 | .20/.21/.60 | 0.50 | – | 10-11 |
| dev | 3,312 | 518 | 0.594 | 2.8 | .05/.09/.86 | 0.09 | 0.34 | 10-11 |
| test | 1,405 | 500 | 0.056 | 150 | .11/.26/.63 | 0.04 | 0.52 | 10-11 |
| fresh | 3,945 | 1,246 | 0.016 | 449 | .09/.76/.16 | 0.23 | 0.145 | 10-14..10-27 |

AIUR items are long (median ~5,300–5,800 chars; 75–90% are chunks). Marker shares (train→fresh): TL0/TL1 harness .055→.386; candidate/review handoffs .038→.245; director-style grid-review prompts .009→.218; codex image-attachment markup 0→.137; spreadsheet labeling .160→.001. Pharos labeling runs themselves are not a factor.
Other cohorts: agent_reply fresh is 71% codex (vs 27–29%); Flash C.deliberation rate .59→.30 and C.report.progress .23→.50 in fresh; baseline loses ~0.10 on flash_test agent_reply (0.762→0.666) but only 0.03 on gold3. Human stable. Fresh delegation: 235 items (16 in gold3) — ignore.

## Label shift (AIUR, y3): train / dev / test / fresh
empty .629/.597/.455/.272; labels/item 1.80/2.07/2.89/4.18; P.request .27/.35/.42/.52; P.frame.scope .05/.05/.16/.29; P.frame.constraint .27/.32/.44/.62; P.frame.criteria .03/.01/.03/.14; review .008/.009/.034/.172; revise .001/0/0/.116.

## Dev optimism from family concentration?
Only slightly. Dev and split=test agree per cohort within noise (AIUR .852 vs .827; agent_reply .753 vs .737; delegation .668 vs .637; human .653 vs .647). Dev cannot predict fresh because it is from the same time window as train.

## Baseline errors on fresh AIUR (gold3, n=250)
506 FP, 645 FN (2.0 / 2.6 per item vs 0.46 / 0.51 on test AIUR). ~70% of FPs are the C.report subtree (prompts quote reports/findings/handoffs). FNs are in the P subtree (scope 70, constraint 74, implement 47, review 39, request 80). Template memorization: items with a near-dup prefix in train score 0.48 vs 0.32 (gold3 fresh), 0.795 vs 0.619 (flash_dev), 0.726 vs 0.576 (flash_test test split). Not provider- or chunk-specific.

## Recommendations
1. Report per cohort × split, with fresh AIUR as a headline cell (cohort weighting shrinks it: AIUR weight 0.078).
2. Validation: family-grouped CV on train with template capping; also score dev items without a near-dup in train (flash_dev has 1,814 such AIUR items). Optionally split fresh by family into tune/eval halves (else state AIUR shift is unvalidated).
3. Training: dedup train AIUR by text hash, down-weight big families (1/sqrt(size)); train AIUR on flash/y_hf/votes (19.8k) not y3 (1.4k); retrain often.
4. Features: represent the instruction part (first ~500 chars) separately from quoted material; chunk_index, cohort, provider, kind as features; per-cohort C.report priors/thresholds; normalize ids/hashes/numbers; prefer word/semantic features.
5. Cascade: send AIUR items with an unseen prefix template to Haiku: ~280/day (~$0.25/day), ≤ ~28k items (~$25) for the 400k backfill.
6. Trustworthiness (family-bootstrap SE): split=fresh 0.608 ± 0.012 (weighted 0.688 ± 0.012); fresh AIUR 0.342 ± 0.022; fresh agent_reply 0.710 ± 0.013; fresh human 0.684 ± 0.022. For the backfill, the cohort-weighted split=test score (0.734 ± 0.011 for the baseline) is the better estimate; fresh estimates going-forward labeling for one harness era.
Open: teacher ceiling on fresh AIUR is probably ~0.67–0.70 (Haiku vs F∩A 0.674), not 0.83.

Scripts: tracks/D-shift/scripts/; logs and per-item scores in tracks/D-shift/out/.
