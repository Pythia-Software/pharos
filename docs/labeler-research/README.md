# Local labeler research (2026-10-28 to 10-30)

Can a cheap local model reproduce the message-function labels that LLMs assign
in [library labeling](../library-labeling.md)? Four research rounds answered
this, with several studies each, run by a director agent and researcher
agents. The data was this library's labeling dataset: Haiku 5.5, Gemini 3.8
Flash and Gemini 4 Argon labels, taxonomy `candidate-0.3`.

**Decision (2026-10-30): ship `kestrel-68m`.** It is a fine-tuned 68M-parameter
encoder, as int8 ONNX:

- trained on this library plus permissively licensed public data (AGPL
  excluded);
- run by Pharos in a separate, swappable worker process, ONNX Runtime first;
- labeling in batches, incrementally.

See [shipping-encoder.md](shipping-encoder.md) for the result and
[timeline.md](timeline.md) for how we got there. An earlier decision to ship
the pure-Go linear labeler was reversed after the generalization study
([generalization.md](generalization.md)) showed it holds up worse on other
people's data.

## Files

| File | What it is |
|---|---|
| [timeline.md](timeline.md) | What we did, in order, and why each decision was made |
| [shipping-encoder.md](shipping-encoder.md) | Round 4 (KESTREL): the multi-population encoder Pharos will ship, the public-only alternative, the runtime cost and the input contract |
| [findings.md](findings.md) | Results from rounds 1 and 2, per study, with the caveats |
| [methodology.md](methodology.md) | Targets, splits, segments, the fair leave-one-out comparison, the harness, and pitfalls found along the way |
| [reproduction.md](reproduction.md) | How to restore the data and code from the euclid archive and rerun each study |
| [linear-labeler-plan.md](linear-labeler-plan.md) | **Superseded.** The plan for the Go linear labeler, kept for the record; its pipeline parts (shared item builder, ledger, operation) carry over |
| [generalization.md](generalization.md) | Round 3: holding out whole groups within the library (HERON), and other people's public data (IBIS) |
| [reports/](reports/) | Sanitized copies of every study report, the Go parity contract (`r2-DOTTEREL-PARITY.md`) and the encoder framing spec (`r4-KESTREL-FRAMING.md`) |
| [code/](code/) | Frozen snapshot of the research code: harnesses, trainers, export, evaluation. Not maintained. |

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
