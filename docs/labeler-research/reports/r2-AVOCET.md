> Sanitized copy of ~/pharos-labeling/studies/AVOCET/REPORT.md (archived in full on euclid at "Pharos Labeling/2026-10-29-bird-studies"). Verbatim conversation text was removed.

# AVOCET report: segments, harness v2, three-teacher coverage of human items

**Question.** Build the segment column, per-segment eval sets and harness v2 (fair LOO per segment, bootstrap
CIs), then label every human item with all three LLMs under the round-1 settings, build data v2, and re-score
the round-1 baselines on it.

## Deliverables (all under `~/pharos-labeling/studies/AVOCET/`; usage in README.md)
- `data/segments.parquet`: segment for all 364,908 items, plus sub-fields (piece statuses, `ctx_reason`, `typed_byte_share`, `aiur_kind`).
  Precedence: any `estimated_typed` target piece puts an item in the typed family. It is `H-typed` only when typed is the only target
  kind and there is no context piece; otherwise it is `H-typed+ctx`. Without typed text: feedback > template > resent. README gives the reasons.
- `harness/rlh2.py` provides `evaluate2`, `rescore`, `tune_thresholds_by_segment`, `compare`, `save_preds` and `evaluate_teacher`.
  The leaderboard is `studies/leaderboard2.jsonl`. `STAGE1_READY` was written at ~08:50.
- `data/v1/`, `data/v2/`: eval sets per segment × {y3, y_hf, flash} × {dev, test, fresh} (heldout = test ∪ fresh), sizes, agreement and LLM references.
  `data/v2/` also holds labels, matrices and targets in round-1 format. `V2_READY` and `LABELING_DONE` were written at ~09:50.
- `preds/r1-*.npz`: round-1 models in the v2 prediction convention, scored on v1 and v2.

## Labeling (stage 2)
Round-1 routes, models, efforts, default prompt and packing were used throughout. The jobs' prompt-template, schema and taxonomy hashes match the round-1 jobs.

| job | route / model / effort | items | outcome | known spend | $/item |
|---|---|---|---|---|---|
| avocet-haiku-htyped | claude haiku-5-5 high, max-per-text 20 | 5,115 | 5,110 usable, 4 invalid, 1 missing | $4.17 | 0.00082 |
| avocet-haiku-human-rest | same | 3,420 | 3,420 usable | $2.52 | 0.00074 |
| avocet-argon-htyped | agy gemini-4-argon medium | 7,627 | 7,623 usable, 4 tool_use (invalid) | $61.29 | 0.0080 |
| avocet-argon-human-rest | same | 5,096 | 5,096 usable | $37.49 | 0.0074 |
| **total** | | 21,258 | 21,249 usable | **$105.47** | |

- **Coverage.** All 16,722 human items now have Haiku, Flash and Argon, except 9 H-typed items (5 Haiku, 4 Argon). No refusals.
  The refusal registry file was never created, because nothing was refused.
- **Unknown attempts.** Haiku had 76 `unknown` attempts. Every one shows Claude Code's "no stdin data received in 3s", so no prompt was sent.
  They were re-queued with `reconcile --retry-unknown` and labeled. The runner still counts $0.27 of possible exposure for them.
- **CLI-reported cost.** For Haiku the CLI reported $265, which is wrong: Claude Code 2.1.288 does not recognize the haiku-5-5 model.
  The price book's known spend is authoritative.
- **Wall clock.** About 62 min (08:41 to 09:43). Argon averaged ~$0.0078/item, above the brief's $0.0065. Human items are longer than average.

### Runner performance finding (for the Pharos repo; not patched)
- **Cause.**
  - Before every lease, and after every finished attempt, the runner calls `computeLabelingSpend` (labeling_jobs.go).
  - It runs 5 queries; 3 of them scan the whole `attempts` table, which has no index on `job`. On this dataset that is 140k round-1 attempt rows and 140k usage rows.
  - They run inside `db.Begin()` on the ledger connection, which is opened with `_txlock=immediate` and `SetMaxOpenConns(1)` (labeling_jobs.go:144).
  - So each call holds the dataset's write lock and the runner's only connection.
- **Effect.**
  - Every runner on the dataset serializes on that lock: the busy runner sits at 100% CPU and the others at 0%.
  - Requests in flight stayed at 3–17 whatever `--concurrency` was set to. Throughput was 75–110 requests/min across all runners, and fell to ~30 when the machine load reached 50–116.
  - Inside one runner, the prompt write to Claude Code's stdin waits behind the lock. Claude Code gives up after 3 s, which produced the 76 `unknown` attempts above.
  - There was one `SQLITE_BUSY` halt (`database is locked`).
- **Fix applied, with the director's approval at 09:35.** With every runner stopped, I ran `CREATE INDEX attempts_job_idx ON attempts(job)` on `full-20261027` (0.1 s), then resumed.
  - Runner CPU fell from 96% to 2%, and requests in flight rose to the configured concurrency.
  - The Argon rest job (1,147 requests) then finished in ~5 min at concurrency 28, and no further stdin unknowns appeared.
  - CHICKADEE found the same fix independently.
- **Recommendation for the repo.**
  - Create `attempts(job)` with the ledger schema.
  - Keep spend incrementally per runner, or compute it outside an immediate transaction.
  - Do not hold the single connection while a CLI waits for its stdin.

## Teacher agreement on v2 (closure sets, Jaccard, empty-empty = 1; items with all three teachers)
**This is CHICKADEE's baseline: H-typed under the standard 600-character context.** Each teacher is scored against the intersection of the other two.

| scope | n | Haiku vs F∩A | Flash vs H∩A | Argon vs H∩F | H–F | H–A | F–A | 3-way exact | any teacher need_context |
|---|---|---|---|---|---|---|---|---|---|
| H-typed, held-out (test+fresh) | 1,494 | **0.769** | **0.758** | **0.749** | 0.756 | 0.781 | 0.768 | 0.235 | 0.438 |
| H-typed, all splits | 9,991 | 0.772 | 0.760 | 0.745 | 0.759 | 0.777 | 0.767 | 0.243 | 0.449 |
| H-typed+ctx, held-out | 364 | 0.767 | 0.744 | 0.733 | 0.756 | 0.760 | 0.752 | 0.277 | 0.415 |
| H-template, held-out | 674 | 0.980 | 0.889 | 0.967 | 0.888 | 0.971 | 0.887 | 0.663 | 0.230 |
| H-feedback, held-out | 39 | 0.713 | 0.741 | 0.732 | 0.720 | 0.755 | 0.803 | 0.282 | 0.615 |
| H-resent, held-out | 20 | 0.807 | 0.808 | 0.787 | 0.794 | 0.791 | 0.874 | 0.300 | 0.250 |
| AIUR / AR / DEL, held-out (unchanged from v1) | 313 / 947 / 175 | 0.706 / 0.759 / 0.604 | 0.656 / 0.762 / 0.605 | 0.645 / 0.738 / 0.566 | | | | | |

The H-typed teacher bars did not move from v1 (n=366: 0.769 / 0.765 / 0.749); the CIs are now about half as wide.
Teachers agree least with each other on H-typed+ctx and delegations, and most on templates.
Full tables, including dev and train scopes: `data/v2/agreement.md`.

## Eval-set sizes (held-out = test + fresh; excludes `is_duplicate` and `seen_in_train`)
| segment | y3 dev v1 → v2 | y3 held-out v1 → v2 | y_hf held-out v1 → v2 | flash held-out |
|---|---|---|---|---|
| H-typed | 216 → 972 | **366 → 1,494** | 741 → 1,496 | 1,496 |
| H-typed+ctx | 53 → 251 | 86 → 364 | 171 → 364 | 364 |
| H-template | 71 → 291 | 159 → 674 | 320 → 674 | 674 |
| H-feedback | 2 → 6 | 8 → 39 | 17 → 39 | 39 |
| H-resent | 2 → 12 | 2 → 20 | 8 → 20 | 20 |
| AIUR / AR / DEL | 189 / 598 / 202 (unchanged) | 313 / 947 / 175 | 2,601 / 22,888 / 999 | 5,138 / 46,566 / 2,107 |
| ALL | 1,333 → 2,521 | 2,056 → 4,026 | 27,745 → 29,081 | 56,404 |

- **Training data.** Train rows with y3 went from 10,478 to 20,006 (H-typed 1,790 → 7,521; H-typed+ctx 510 → 2,291; H-template 654 → 2,436).
- **Expected ~1,800 held-out H-typed.** H-typed alone has 1,494 held-out items. The ~1,800 figure counted typed text with or without context, as the round-1 follow-up did; that is 1,858 here.

## Baselines through harness v2: H-typed, held-out, fair LOO
Each cell gives the student's score with the held-out LLM's score in parentheses. "Δ vs Haiku" is the paired 95% bootstrap CI.

| run | v1 vs F∩A (n=366) | v1 Δ vs Haiku | v2 vs F∩A (n=1,494) | v2 Δ vs Haiku | v2 vs H∩A (Flash 0.758) | v2 vs H∩F (Argon 0.749) | v2 H-all Δ vs Haiku (n=2,591) | v2 ALL y3 |
|---|---|---|---|---|---|---|---|---|
| r1-c-e68-512-all | 0.740 (0.769) | −0.029 [−0.056, −0.002] | 0.737 (0.769) | **−0.032 [−0.045, −0.020]** | 0.739 | 0.740 | −0.021 [−0.030, −0.013] | 0.791 |
| r1-c-e68-512-all-segth | 0.746 | −0.024 [−0.049, +0.003] | 0.748 | −0.021 [−0.033, −0.009] | 0.753 | 0.750 | −0.015 [−0.024, −0.007] | 0.797 |
| r1-c-e32-512-all | 0.722 | −0.048 [−0.076, −0.018] | 0.725 | −0.044 [−0.057, −0.031] | 0.729 | 0.725 | −0.032 [−0.041, −0.022] | 0.785 |
| r1-A-stack-multi-char | 0.697 | −0.072 | 0.688 | −0.081 [−0.095, −0.067] | 0.690 | 0.692 | −0.058 | 0.755 |
| r1-A-final-linear-200k | 0.662 | −0.107 | 0.673 | −0.095 [−0.110, −0.081] | 0.679 | 0.676 | −0.073 | 0.736 |

- **v2 confirms the round-1 finding with a much tighter CI.** The best student trails Haiku on H-typed by about 0.03 Jaccard against F∩A.
- **It ties Flash and Argon on their fair targets:** 0.739 vs 0.758 against H∩A, and 0.740 vs 0.749 against H∩F.
- **Splits.**
  - c-e68 test (n=837): −0.036 [−0.051, −0.021].
  - c-e68 fresh (n=657): −0.027 [−0.046, −0.008].
  - H-typed+ctx (n=364) is worse than H-typed: −0.044 [−0.071, −0.018].
- **Per-segment thresholds (`-segth`).** These are global thresholds plus H-typed/AIUR/AR/DEL thresholds tuned on v1 dev. They gain +0.011 on H-typed (v2 Δ −0.021) and do not hurt other segments.
  This is a free gain, but the thresholds were tuned on v1 dev, which overlaps v2 dev.
- **On v1, ALL reproduces round 1 exactly** (c-e68: 0.782; LOO 0.774 / 0.781 / 0.779).

## Cost and resources
- **Labels:** $105.47.
- **Harness:** a full `evaluate2` takes ~5 s (peak RSS 2.2 GB). `build_v2.py` takes 25 s (2.9 GB). Exports take ~45 s per job.
- **Labeling workers:** each takes about 120–160 MB of physical memory, which kept concurrency to 20–28 per route inside the 8 GB budget.
- **Packages:** none added. `harness/md.py` replaces tabulate.

## Caveats
- **Labels are LLM labels.** y3 is strict. LLM rows against y3 have recall 1 by construction, so only the LOO columns are fair.
- **Bootstrap is item-level, not family-clustered.** Correlated items within a family may make CIs somewhat optimistic.
- **Thresholds on y_hf and flash sets.** Round-1 rows use their y3-tuned thresholds there, so those columns understate a model tuned for y_hf.
- **A-classic rows cover only round-1 gold-set items.** Their dev coverage is partial (reported as `coverage`); all held-out items are covered.
- **Tiny segments.** H-resent (20) and H-feedback (39) held-out sets are indicative only.
- **Unmarked pasting.** Segments come from the piece classifier, so prose pasted without cues counts as typed.
- **need_context.** About 44% of H-typed items have a teacher saying it needed more context. This is CHICKADEE's question.
- **The index is a schema change.** `attempts_job_idx` now exists in `full-20261027`: no data changed, and every reader and exporter is unaffected.
- **Monitoring script edit.** I edited a supervisor script while it ran, which caused a harmless shell parse error after its job finished. No labeling effect.

## Recommendations / open questions
1. **Use v2 for all round-2 scoring.** Headline: H-typed held-out (n=1,494) vs F∩A, with Haiku at 0.769.
2. **Trainers should take v2's added train y3.** H-typed train y3 roughly quadruples (1,790 → 7,521).
3. **Use per-segment thresholds.** They are a free +0.01 on H-typed; retune them on v2 dev.
4. **Repo fix.** Add the `attempts(job)` index to the ledger schema, and move spend accounting out of the immediate transaction. See the runner finding above.
