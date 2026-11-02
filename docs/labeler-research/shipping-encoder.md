# The shipping encoder (round 4, KESTREL, 2026-10-29/30)

## Decision (2026-10-30)

**Pharos will ship `kestrel-68m`:**
- a fine-tuned ettin-encoder-68m, as weight-only int8 ONNX;
- trained on **this library plus permissively licensed public data**;
- run by Pharos in a **separate, swappable worker process**, ONNX Runtime first.

It replaces the earlier plan to ship the pure-Go linear labeler
([linear-labeler-plan.md](linear-labeler-plan.md), kept for the record).

Why each choice:
- **Encoder rather than linear:** the generalization research
  ([generalization.md](generalization.md)). On other people's typed text the
  linear model loses 5 points more than the encoder, and it can't learn new
  vocabulary when adapted.
- **Library plus public weights:** the user chose this over public-only after
  seeing the cost of each, below.
- **68m rather than 32m:** about +0.02 better everywhere, at 33 ms per typed
  message on four CPU threads.

## Training data

| | Items | Notes |
|---|---|---|
| Library | Round-1 dataset with AVOCET's three-teacher human labels; EGRET's thinking labels | Haiku, Flash and Argon teachers |
| Public | 18,895 labeled items from four sources | Flash on every item; Argon on 10,260 dev/test items |

- **The four public sources:**
  - **AT:** raw Claude Code and Codex sessions from Hugging Face `agent-traces` datasets, 18 uploaders;
  - **PI:** sessions from the pi coding agent;
  - **OH:** OpenHands user feedback;
  - **WC:** WildChat coding chats.

  All of it went through the real Pharos pipeline: a throwaway library,
  `ingest`, then `labeling prepare` (IBIS).
- **Licenses:** permissive only (MIT, Apache-2.0, BSD-3, CC0, CC-BY-4.0,
  ODC-BY).
  - **Excluded:** the AGPL-3.0 dataset `cfahlgren1/Fable-5-traces` (1,000
    items), plus 441 items elsewhere that copy its text.
  - The per-dataset license table is part of the model manifest.
- **Label spend:** $89 for KESTREL; IBIS $33 before it.

## Results

J against Flash∩Argon. Paired, family-bootstrap 95% CIs; "public" means test
splits of sources the final model trained on.

| Set | `kestrel-68m` | `kestrel-32m` | Library-only 68m (`s68v-all`) | Haiku |
|---|---|---|---|---|
| Library typed human text (n 1,494) | **0.780** | 0.760 | 0.781 | 0.769 |
| Public typed human text (n 3,070) | **0.716** | 0.691 | 0.617 | – |
| Thinking (n 2,000) | **0.834** | 0.823 | 0.607 | – |
| GANNET, untouched holdout (vs Argon; Flash 0.720) | **0.700** | 0.688 | – | – |

- **On unseen populations** (leave one public source out, ettin-32m),
  multi-population training lifts the encoder by +0.025 [+0.014, +0.036], with
  the library unchanged. The linear model trained the same way gains +0.002.
- **Against the library-only model:** `kestrel-68m` shows no library
  regression beyond noise on any segment. On other people's typed text it is
  +0.099 [+0.081, +0.120] better.
- **Against Haiku:** it beats Haiku overall on the library (+0.015 [+0.006,
  +0.023]).
- **Thinking** is handled inside the one shared model, with no adapter.
- **Choosing the variant:** the first 68m candidate regressed on library
  delegations. The shipped variant puts more library delegation, AIUR and
  agent-reply text in each epoch, and was picked on dev only.

## Public-only variant (the privacy alternative)

`kestrel-68m-public` trains on the public train splits only; the library is
used for nothing but dev thresholds and evaluation.

| Set | Public-only cost vs `kestrel-68m` |
|---|---|
| Other people's typed text | −0.023 [−0.033, −0.013] |
| Other people's agent replies | −0.077 |
| Other people's typed text with quoted/pasted context | −0.067 |
| Library typed text | −0.070 |
| Library agent replies | −0.113 |
| Library delegations | −0.151 |

- **Why the library costs are so large:** public data hardly covers agent text
  sent in the user role (4 training rows), delegations (129) or thinking (603).
- **Threshold option:** the manifest also carries a threshold set tuned on
  public dev only, which leaves no private data in the artifact at all.
- **The user chose library plus public weights.**

## Runtime cost (int8, 4 CPU threads, M3 Ultra; an M2 Air is about 1.3–1.5× slower)

| | `kestrel-68m` | `kestrel-32m` |
|---|---|---|
| Throughput (corpus mix) | 24.6 items/s | 63 items/s |
| Latency, typed message | 33 ms | 13 ms |
| Model file | 152 MB | 92 MB |
| RSS | 0.8 GB | 0.5 GB |

- int8 is within ±0.004 of fp32 on real held-out items.
- `coremltools` does not convert the model cleanly, so ONNX Runtime it is.

## The input contract

The runtime must rebuild the exact training input: the role tag, context
handling, head+tail truncation, special tokens, pooling, and how head outputs
become probabilities, then thresholds, then closed label sets. The spec is
[reports/r4-KESTREL-FRAMING.md](reports/r4-KESTREL-FRAMING.md).

**One bug found here affects earlier studies.** The role tag must use
`units.sender_role`:
- Several later item tables used `units.sender`, an identity field that is
  mostly empty.
- That framed inputs as `human :` instead of `human user :`.
- It barely mattered for typed human text (≤ 0.007), but understated
  delegation and thinking scores by 0.02–0.09.
- It is fixed for KESTREL, and the IBIS, EGRET and GANNET numbers carry this
  caveat.

**Export contents** (`studies/KESTREL/export/<model>/` in the archive):
- ONNX fp32 and int8;
- the tokenizer;
- `manifest.json`: labels, parents, segment thresholds, scorer, licenses,
  metrics;
- the framing spec;
- a Python reference;
- a parity fixture of 187 invented strings, 0 mismatches.

Full report: [reports/r4-KESTREL.md](reports/r4-KESTREL.md).
