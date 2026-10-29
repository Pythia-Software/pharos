# Local labeler research (2026-10-28/29)

Can a cheap local model reproduce the message-function labels that LLMs assign
in [library labeling](../library-labeling.md)? Two research rounds answered
this, with several studies each, run by a director agent and researcher
agents. The data was this library's labeling dataset: Haiku 5.5, Gemini 3.8
Flash and Gemini 4 Argon labels, taxonomy `candidate-0.3`.

**Decision (2026-10-29): ship the pure-Go linear labeler everywhere.** A
fine-tuned 68M-parameter encoder is better, by about 0.06 Jaccard on typed
human text. But the linear model:

- runs in pure Go with exact Python parity;
- scores 17–32k items/s on four cores;
- needs a 33–150 MB weight file and no native runtime.

The quality gap was judged worth the engineering simplicity. The plan is in
[linear-labeler-plan.md](linear-labeler-plan.md).

## Files

| File | What it is |
|---|---|
| [findings.md](findings.md) | Every result that matters, per round and per study, with the caveats |
| [methodology.md](methodology.md) | Targets, splits, segments, the fair leave-one-out comparison, the harness, and pitfalls found along the way |
| [reproduction.md](reproduction.md) | How to restore the data and code from the euclid archive and rerun each study |
| [linear-labeler-plan.md](linear-labeler-plan.md) | The concrete plan for wiring the Go linear labeler into Pharos |
| [reports/](reports/) | Sanitized copies of every study report and the Go parity contract (`r2-DOTTEREL-PARITY.md`) |

## What is not in the repository, and where it is

Conversation text, label datasets, trained models, predictions and scripts
stay on hardware the user controls: the library drive `euclid`, with working
copies in `~/pharos-labeling/` on the Mac Studio.

| Archive (on `/Volumes/euclid/Pharos Labeling/`) | Contents |
|---|---|
| `2026-10-27/` | The labeling dataset (`dataset.sqlite3`, about 365k items, every teacher result and ledger), attempts, pilots, run notes. See its `ARCHIVE.md`. |
| `2026-10-28-student-research/research/` | Round 1: data tables, harness `harness/rlh.py`, leaderboard, tracks A–F (scripts, models, predictions), `SUMMARY.md`, `DIRECTOR-LOG.md` |
| `2026-10-29-bird-studies/studies/` | Round 2: AVOCET–GANNET (scripts, models, predictions), harness v2 `AVOCET/harness/rlh2.py`, `SUMMARY.md`, `DIRECTOR-LOG.md`, checkpoints |
| `2026-10-29-bird-studies/full-20261027-with-avocet-jobs/` | The round-1 dataset plus AVOCET's Haiku and Argon jobs: three-teacher labels for every human item |
| `snapshots/2026-10-28-9PM/catalog.sqlite3` | APFS clone of the Oct 28 9 PM backup catalog, used by EGRET and GANNET (schema 21) |

The reports name files by their paths in `~/pharos-labeling/`. On euclid the
same tree sits under the archive folders above.
