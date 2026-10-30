# Generalization: linear labeler vs small encoders (round 3, 2026-10-29)

**The question:** if labeler weights ship to many users, which approach holds
up better on data it wasn't trained on: the pure-Go linear model (DOTTEREL
`V4-hybmaj`) or the fine-tuned ettin encoders (32m/68m)? Two studies
answered it; no Haiku labels were used.

| Study | What it did | Cost |
|---|---|---|
| **HERON** | Held out whole groups inside this library: three repository clusters, the Codex provider, and a time cutoff. Trained each model without the group and compared, on the same items, against the same model trained with it. | $0, no labeling |
| **IBIS** | Took other people's public data from four sources and ran it through the real Pharos pipeline (throwaway library, ingest, `labeling prepare`). Labeled 3,401 items with Flash and Argon, then scored zero-shot and after adaptation. | $32.73 |

IBIS's four sources:
- raw Claude Code and Codex sessions from 30 Hugging Face datasets (18 uploaders);
- sessions from the pi coding agent;
- OpenHands feedback;
- WildChat coding chats.

Metric: fair LOO Jaccard (see [methodology.md](methodology.md)). Every
difference is paired, with CIs from a family-level bootstrap. HERON's CIs
also include training-seed noise.

## Results

### 1. Shift within one user's library (HERON)

| Segment | Linear − encoder degradation | Notes |
|---|---|---|
| Typed human text (H-typed) | repos −0.005 [−0.025, +0.015]; provider −0.003; time +0.011 | No difference. Both lose 0–0.02. |
| Agent replies, new provider | +0.033 [+0.005, +0.060] | Linear degrades more |
| Agent replies, time shift | +0.091 [+0.031, +0.151] | Linear degrades more |
| Automation prompts, new automation | +0.28 [+0.10, +0.47] | Linear −0.42, encoder −0.14 |

- **Typed human text:** the ~0.05 level gap (0.70 vs 0.75) is the same in
  and out of group. GANNET's impression that the linear model degrades more
  on new conversations, from 30 families, did not replicate in these paired
  tests.
- **Machine text with a new format:** this is where the linear model falls
  apart.
- **Local adaptation helps the linear model.** Warm-starting it on 1k–4k of the
  group's labeled items (seconds) closes 40–70% of those machine gaps.
  - With 4k items the absolute gap on agent replies nearly vanishes: Codex AR
    0.720 linear vs 0.724 encoder.
  - New-automation prompts stay apart: 0.758 vs 0.865.
- **Novelty:** lexical novelty on typed human text within the library is
  small, averaging 4% unseen n-grams, and explains almost nothing.

### 2. Other people's data (IBIS)

Typed human text, zero-shot, scored against Flash∩Argon:

| Model | In-library | Other people | Drop |
|---|---|---|---|
| Go linear `V4-hybmaj` | 0.708 | 0.540 | −0.167 |
| ettin-68m `s68v-all` | 0.772 | 0.657 | −0.115 |
| ettin-68m `c68v-hum` | 0.768 | 0.648 | −0.121 |

- **The linear model loses 0.052 [0.032, 0.073] more.** Its loss is 0.061
  more on the three coding-agent sources, and on raw Claude Code and Codex
  sessions, the most faithful source, it drops −0.165 against −0.074.
- **Every source agrees:** the linear model drops more in every source, with
  the CI excluding zero in three of four.
- **The students got worse, not the labels:** Flash–Argon agreement on these
  items is higher (0.795) than in-library (0.768).
- **Main error:** under-labeling, especially missing `implement` requests.

Adaptation within the same source: train on half of the families, score on the
other half.

| Model | Zero-shot | Adapted (~1.7k labeled items) | Gain |
|---|---|---|---|
| Go linear | 0.540 | 0.586 | +0.046, nearly all from re-tuned thresholds |
| ettin-68m `s68v-all` | 0.659 | 0.719 | +0.060, still rising with more labels |
| ettin-68m `c68v-hum` | 0.646 | 0.711 | +0.066 |

- **The linear model's vocabulary is frozen**, so new words can't be learned.
  A full retrain with a new vocabulary wasn't tested.
- **Adaptation needs labels from the same population:** adapting on other
  sources and testing on an unseen one gains only +0.00 to +0.03.
- **Agent replies:** both models lose about 0.2 across users; the linear
  model loses 0.028 [0.004, 0.053] more on the agent sources.

### 3. Robustness to perturbations (HERON, indicative)

No model is uniformly more robust:
- The linear model ignores case and whitespace, and is hurt most by stripped
  punctuation.
- ettin-32m is hurt by extra whitespace.
- ettin-68m is the most robust overall.

## Assessment

- **Two different shifts.**
  - Within one person's library, typed human text shifts little for either
    model.
  - Across people, typed human text shifts a lot for both, and the linear
    model shifts more.
- **For many users, the encoder generalizes clearly better.** On other
  people's typed text it leads by about 0.12 zero-shot (0.657 vs 0.540),
  twice the in-library gap of about 0.06. With adaptation the lead grows to
  about 0.13.
  - The direction matches the literature: pretrained transformers lose much
    less than bag-of-words models under realistic shift (Hendrycks et al.,
    ACL 2020).
- **Even the encoder loses about 0.11 on other people's typed text.** The
  biggest weakness is that all the training data is one person's library, not
  the model architecture. Both models need training data from many people
  before weights ship to everyone.
- **The linear model's one real advantage is cheap adaptation of an existing
  format**, such as a new provider's agent replies, done locally in seconds.
  That doesn't help with a new person's writing style, because its vocabulary
  is fixed when the weights ship.

## Next experiment, before choosing the shipped architecture

Train both models on several populations:
- this library;
- IBIS's prepared public sources (121k items), with about 10–20k of them
  labeled by Flash plus an Argon sample, roughly $30–60.

Then evaluate **leave-one-source-out**: train without a source, test on it.
This shows how large the linear–encoder gap is once training data is diverse,
which is the condition weights would ship under.

Licenses matter here. IBIS's sources are MIT, Apache-2.0, CC-BY-4.0, CC0,
BSD-3, ODC-BY and one AGPL-3.0 dataset. Training shipped weights on them needs
a license review, so exclude AGPL by default.

Full reports, with private repository names: `studies/HERON/REPORT.md` and
`studies/IBIS/REPORT.md` in the round-3 archive
(`Pharos Labeling/2026-10-29-bird-studies/studies/`).
