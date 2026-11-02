> Sanitized copy of ~/pharos-labeling/studies/GANNET/REPORT.md (archived in full on euclid at "Pharos Labeling/2026-10-29-bird-studies"). Verbatim conversation text was removed.

# GANNET: untouched holdout (round 2) — REPORT (final, 14:28 MDT)

**Question.** Give the program a holdout no model has trained or tuned on: items from families that did not exist at the round-1
snapshot, labeled by the teachers, and scored **once, at the end, by the director**, on final frozen models.

**Rule: nobody trains or tunes on GANNET labels.** They are kept apart in `~/pharos-labeling/studies/GANNET/HOLDOUT-DO-NOT-TRAIN/`
(exports and tables). EGRET's sample excludes every GANNET family. No student has been scored on GANNET here: EGRET's zero-shot
baselines skip these items on purpose.

## Numbers first
| | value |
|---|---|
| items / families | 2,351 / 697 (created 2026-10-27 13:23Z → 2026-10-29 02:23Z) |
| usable labels | Flash 2,280 (97.0%) · Argon 2,287 (97.3%) · both 2,231 |
| target (Flash∩Argon, else Flash) | 2,280 items (2,231 both + 49 Flash only); 71 without Flash (64 AIUR) |
| Flash–Argon J (closure) | **0.720** [0.707, 0.732], exact match 0.38 |
| spend (known) | **$31.46**: Flash $6.42 + repair $2.79, Argon $22.25; Haiku $0 (not run). Also ~$2.6 "possibly charged" exposure on quota-rejected attempts (zero tokens) |

## Snapshot and pool
- Same snapshot and dataset as EGRET: `EGRET/dataset-20261028`, prepared from an APFS clone of the Oct 28 9PM backup catalog
  (`/Volumes/euclid/Pharos Labeling/snapshots/2026-10-28-9PM/catalog.sqlite3`, 110.6 GB, schema 21).
- Pool: the 7,754 families with **no conversation in `full-20261027`** and no round-1 family ID. All were first seen after
  2026-10-27 07:00 MDT and all are in `fresh` (`GANNET/new_families.txt`).
- Pool by segment (non-duplicate items): AIUR 43,385 · TH 9,813 · AR 4,019 · H-typed 156 · H-template 39 · H-typed+ctx 11 · H-resent 6 ·
  DEL 85. Human text is scarce in 36 hours of new traffic. Most new volume is Antigravity automation (AIUR).

## Sample and coverage
The sample takes all human and delegation items. AIUR, AR and TH are capped: one item per target_hash, at most 20–25 per family,
provider × reason/length strata ∝ size^0.5 (seeded: `EGRET/scripts/sample.py`). Segments follow AVOCET's precedence.
| segment | items | Flash | Argon | both | mean chars | Flash–Argon J |
|---|---|---|---|---|---|---|
| H-typed | 156 | 156 | 152 | 152 | 256 | 0.766 |
| H-typed+ctx | 15 | 15 | 15 | 15 | 535 | – |
| H-template | 39 | 39 | 38 | 38 | 74 | 0.874 |
| H-resent | 2 | 2 | 2 | 2 | 106 | – |
| DEL | 85 | 85 | 84 | 84 | 1,563 | 0.795 |
| AIUR | 580 | 509 | 556 | 500 | 3,707 | **0.544** |
| AR | 800 | 800 | 781 | 781 | 1,171 | 0.736 |
| TH | 674 | 674 | 659 | 659 | 645 | 0.803 |
By provider: Claude 0.781, Codex 0.701, **Antigravity 0.491**. The teachers also disagree on new Antigravity automation prompts.
For comparison, round-1 Flash–Argon J by cohort was AR 0.783, human 0.789, DEL 0.730, AIUR 0.683, so this new traffic is harder for
the teachers, mostly in AIUR and AR.

## How to score it (director, once, at the end)
- With no Haiku, the fair comparisons are: **student vs Flash beside Argon vs Flash**, and **student vs Argon beside Flash vs Argon**
  (Flash–Argon J is symmetric: 0.720). Also report student vs `target` (Flash∩Argon, else Flash), with paired bootstrap CIs per segment.
- Tables are in `HOLDOUT-DO-NOT-TRAIN/data/`:
  - `targets.parquet`: `item_id, segment, target_kind, target, flash, argon, y_fa`.
  - `label_matrices.npz`: round-1 keys and the 54-label order. It has `flash_*`, `argon_*`, `y_fa`, `target`; the Haiku keys are empty.
  - `items.parquet`, `labels.parquet`.
  - `analysis.json`: teacher agreement.
- See `GANNET/LABELS_READY`.

## Caveats
- **No Haiku** (director 10:30, Claude quota). Job `gannet-haiku` was created but never run.
- **Flash ID-set mismatches.** In `gannet-flash`, 191 of 693 four-item requests (mixed cohorts in item-ID order, as in round 1) came back
  with a mismatched ID set: Flash dropped an item, mostly beside long AIUR items.
  - Repair job `gannet-flash-r1` used the same route, model, effort and prompt at **1 item per request** for the 344 unlabeled items.
    It labeled 273 of them; 71 still fail even alone.
  - So 12% of GANNET Flash labels come from single-item requests. Flag `target_kind`/job if packing effects matter.
- **agy quota wall.** At 10:52–14:14 agy returned `RESOURCE_EXHAUSTED (429)`. The runner recorded the rejected attempts as `tool_use`. They were
  re-queued with `reconcile --retry-tool-use` and re-labeled after the reset; no label comes from a rejected attempt.
- **Argon need_context is 18.6%** on GANNET (Flash 1.3%), driven by the short human and AR items. These rows are kept as usable, per the
  round-1 convention.
- **Small cells:** H-typed 156, H-typed+ctx 15, H-resent 2, H-feedback 0. Use CIs; do not read differences under about ±0.04 on H-typed.
- **All labels are LLM labels.** Flash∩Argon is a strict intersection.
