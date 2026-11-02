# Timeline: what we did and why

This program was run by a director agent and researcher agents. Each study
had its own brief, harness, report and checkpoint. The user set the goals and
made every decision below; the agents ran the experiments. The full logs are
`research/DIRECTOR-LOG.md` and `studies/DIRECTOR-LOG.md` in the euclid
archives.

## Before: the LLM labels (2026-10-27/28)

Library labeling (#281) labeled about 365k items with three LLMs:

| Teacher | Items | Spend |
|---|---|---|
| Gemini 3.8 Flash | all | $553 |
| Haiku 5.5 | 180k | $159 |
| Gemini 4 Argon | a stratified 14k | $91 |

The goal set for the research: a fast, cheap, local mechanism that
approaches those labels. "Matching" means matching the **intersection** of
the three teachers' label sets.

## Round 1: can a student match the LLMs? (2026-10-28, overnight)

**Setup:**
- **Harness:** a foundation engineer built a shared harness with family-level
  splits and an untouched time holdout ("fresh"). It defined the **fair
  leave-one-out comparison**: each LLM against the other two LLMs'
  intersection. That was needed because a single teacher always contains the
  three-way intersection.
- **Tracks:** classic models (A), frozen embeddings (B), fine-tuned encoders
  (C), distribution shift (D), ensembles and cascades (E), and a hand error
  audit (F).

**Results:**
- **Fine-tuned encoders matched or beat each LLM overall.** A 32m model
  trained without any Haiku label matched Haiku. The 68m model beat all three.
- **A dev-to-test drop was traced to one cell:** new automation templates in
  the last two weeks.
- **Weight-only int8** made CPU inference practical.

**Follow-up:**
- **The user asked about holdouts, segments, memory and cgo.** The segment
  breakdown showed the headline hid the primary target: on **typed human
  text** the student trailed Haiku (−0.03).

## Round 2: the bird studies (2026-10-29)

The user named each study after a bird, made typed human text the primary
target, and asked to test per-category models and longer labeling context.

| Study | What it did | Result |
|---|---|---|
| AVOCET | Three-teacher labels for every human item | $105 |
| BOBOLINK | Per-segment encoders | With full human labels, one shared 68m **matches Haiku on typed human text**. Per-segment models aren't needed. |
| BUFFLEHEAD | Which segments share a model | One base, plus adapters if needed |
| CHICKADEE | Longer labeling context | Doesn't help; keep 600 characters |
| DOTTEREL | Pure-Go linear model | Exact parity, about 20k items/s, about 0.06 below the encoder on typed human text |
| EGRET | Thinking labels | Thinking needs its own training data |
| GANNET | A holdout of conversations newer than the snapshot | Scored once at the end |

- **Claude quota** ran out mid-day. The user stopped Haiku labeling, and every
  study resumed from written checkpoints.
- **Decision 1:** given the speed and shipping advantages, the user chose the
  **Go linear model everywhere**, judging a 0.06 gap negligible for end users.
  The research and a plan were committed (`linear-labeler-plan.md`).

## Round 3: generalization (2026-10-29, evening)

Scoring the chosen linear model once on GANNET showed it degrading more than
the encoder on new conversations. The user then noted that the weights would
ship to **many users**, and asked which approach generalizes better.

- **HERON:** held out projects, a provider, and a time period inside the
  library.
  - On typed human text the two models degrade alike.
  - The linear model breaks on new machine-text formats.
  - GANNET's apparent extra drop did not replicate in these paired tests.
- **IBIS:** other people's public coding sessions, run through Pharos's own
  pipeline.
  - On their typed text the linear model drops 5 points more than the
    encoder.
  - Adapting the linear model barely helps, because its vocabulary is frozen.
- **Decision 2:** the user moved to the **encoder**, trained on the library
  plus public data, **excluding AGPL** data.

## Round 4: the shipping encoder (2026-10-29/30, overnight)

**KESTREL** labeled 18,895 public items ($89) and trained on the library plus
the public data.
- **Leave-one-source-out:** diverse training lifts the encoder on unseen
  people (+0.025). It doesn't lift the linear model.
- **`kestrel-68m`:** holds on the library and gains about 0.10 on other
  people's typed text. It handles thinking within one model.
- **A framing bug** in later evaluation tables (`sender` vs `sender_role`) was
  found and fixed.
- **Public-only variant:** trained for the privacy question. It costs about
  0.02 on other people's typed text and 0.07–0.15 on the library.

**Decision 3 (2026-10-30):**
- ship **`kestrel-68m`, library plus public weights**;
- run it in a **separate, swappable worker process**, ONNX Runtime first,
  designed for Linux and Windows workers later;
- support **batched, incremental** labeling.
- Before product work, commit the research, its methods and this record in
  one PR (this folder).
