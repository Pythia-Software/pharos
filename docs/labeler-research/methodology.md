# Methodology

## Inputs and labels

- **Inputs.** Items come from `pharos labeling prepare` ([library labeling](../library-labeling.md)). An item is:
  - one message, or one chunk of up to 6,000 characters of a long message;
  - its model-visible text, `CURRENT_TEXT`. For human messages this is only the typed pieces, with `⟦…⟧` markers where quoted, pasted or harness material was removed;
  - the 600-character `PREVIOUS_CONTEXT`;
  - metadata: cohort, sender, provider, kind, channel, depth, chunk index and count.

  A student sees exactly these fields, the export's `inputs.jsonl`, and nothing else.
- **Labels.** Positives are accepted labels with `supported` evidence, closed upward through the taxonomy (a child implies its ancestors). Uncertain and unsupported labels are not positives.
- **Combining jobs.** Each teacher's authoritative result per item is the latest usable result across its jobs. This replicates the `labeling-export-v1` rule, and was checked item by item against the official exporter for every job.

## Targets

| Target | Definition |
|---|---|
| `y3` | Haiku ∩ Flash ∩ Argon, where all three are usable. **The user's definition of "matching".** |
| `y_hf`, `y_fa`, `y_ha` | Pairwise intersections |
| `votes` / `n_teachers` | Soft targets |
| 2-of-3 majority | Used by DOTTEREL's best model on human rows |

Training on larger, noisier targets (consensus where it exists, else Flash) beat training on `y3` alone, which has few rows.

## Splits

Families are conversations linked by lineage, forks, mirrors, delegation, launches and copy links. Each family is assigned to one split by a hash of its ID:

- `train` 80%, `dev` 10%, `test` 10%;
- `fresh`: families whose first message is within 14 days of the snapshot, a time-based holdout.

Rules:
- Train only on `train`.
- Tune thresholds, settings, ensemble weights and early stopping only on `dev`.
- Read `test` and `fresh` only through the harness.
- Held-out items whose text (40 or more characters) also occurs in train are excluded (`seen_in_train`). `common_short_text` items ("yes", "continue") are reported as their own stratum.

**GANNET** is a third holdout: families that started after the round-1 snapshot, taken from a newer backup. They were never seen by any model or decision, and were scored once at the end.

## Segments

Every item has one segment, known in advance (AVOCET `data/segments.parquet`):

| Segment | Rule |
|---|---|
| `H-typed` | Human; target pieces are only `estimated_typed`; no context piece. **The primary target.** |
| `H-typed+ctx` | Typed text plus quoted, pasted or attachment context, or mixed target kinds |
| `H-template`, `H-resent`, `H-feedback` | Human with no typed piece. Precedence: feedback > template > re-sent. |
| `AIUR` | Agent text sent in the user role: scripts, headless runs, sub-agent prompts |
| `AR` | Agent replies |
| `DEL` | Delegations and their results |
| `TH` | Thinking. Only from the schema-21 snapshot (EGRET). |

## Metrics

- **J:** mean per-item Jaccard on closed label sets; empty versus empty counts as 1.
- **Also reported:** exact match, micro-F1, and macro-F1 over labels with at least 20 positives.
- **Strata:** every metric overall, per segment, `test` vs `fresh`, and `common_short_text`.
- **Cohort-weighted:** Argon's sample was stratified by cohort, so results are also reported weighted by each cohort's population share (agent replies 81.7%, AIUR 7.8%, delegations 5.9%, human 4.6%).
- **Fair comparison (LOO):** a single LLM always has recall 1 against `y3`, so its `y3` score only measures over-labeling. Instead:
  - each LLM is scored against the other two teachers' intersection;
  - the student is scored against the same intersection, beside that LLM.
  - A student trained without Haiku and compared to Haiku against F∩A is the clean version (round 1 `c-e32-512-100k-noH`; round 2 `noH32-*`).
- **Two-teacher holdouts** (GANNET, thinking; no Haiku):
  - the comparison is the student against Argon, beside Flash against Argon;
  - it must be calibrated on in-distribution data, because a consensus-trained student trails a single teacher on it even without drift (`studies/calibrate_gannet.py`).
- **Uncertainty:**
  - paired bootstrap of the student minus the LLM, per segment;
  - family-level bootstrap where items cluster (GANNET);
  - two seeds for headline models. Seed spread is about 0.002 on `y3` and about 0.01 on LOO for n ≈ 2k.

## Harnesses

| Harness | Scope |
|---|---|
| Round 1: `research/harness/rlh.py` | Loaders, `tune_thresholds`, `evaluate`, `compare.py`, `leaderboard.jsonl` |
| Round 2: `studies/AVOCET/harness/rlh2.py` | Extends `rlh` with segments, per-segment thresholds and LOO. A prediction file is one npz holding probabilities [n, 54] for all 95,602 dev/test/fresh items, plus global or per-segment thresholds. Supports `evaluate2` and `rescore`, and writes `studies/leaderboard2.jsonl`. |

Column order everywhere is the 54 nodes of taxonomy `candidate-0.3` in file order.

## Pitfalls we hit, and how to avoid them

- **Leakage from adaptation runs.** Runs trained on half of `fresh` look great on all of `fresh`. Score adaptation only on the other half (`F2`), and name such runs so they are excluded from blends.
- **Inputs a student won't have.** Round-1 `A-tgt-*` rows used `units.sender` and `common_short_text`. Use only the export's `inputs.jsonl` fields.
- **The hidden primary cell.** Headline numbers can hide the primary target: H-typed is under 5% of items. Always report it first.
- **A wrong hypothesis.** "More context helps short human replies" was wrong. The round-1 encoder already saw about 512 of the 600 context characters. Check what a model actually receives before theorizing.
- **Label noise.** Re-running the same labeling configuration moves results by about 0.016. Compare against a same-build rerun, not a stored run.
- **Quota.** Both Claude and Antigravity quotas can run out mid-run. Write resumable checkpoints, and run long jobs under `nohup` with idempotent queues.
