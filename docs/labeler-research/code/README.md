# Research code snapshot (frozen)

This is a frozen copy of the scripts behind the labeler research, kept so the methods can be read and reproduced from
the repository. **It is not maintained, not imported by Pharos, and not run by any test.** No folder has an
`__init__.py` and no file is named `test*.py`. Nothing here is built or linted, and `python3 -m unittest discover -s tools`
only scans `tools/`.

Every file starts with a two-line comment naming its source and was otherwise copied unchanged on 2026-10-30. They
contain no corpus text, private repository names or secrets. The example strings in `encoder/export.py`,
`encoder/make_fixture.py` and `encoder/reference.py` are invented. The cue regexes in `linear/rules.py` are generic
English phrases, and they are left unchanged because the shipped linear model uses them as a feature block.

**Paths.** Absolute paths such as `~/pharos-labeling/research/...` and `~/pharos-labeling/studies/...` refer to the
working-copy layout restored from the euclid archive (see [reproduction.md](../reproduction.md) §1). The scripts expect
to run from that layout, not from this folder.

**Not included:** data, models, predictions, fixtures, logs, queue scripts and helper modules outside the list below.
The following imports are in the archive but not here:
- `common.py` (`studies/BOBOLINK/scripts/` and `research/tracks/C-finetune/scripts/`): round-1 framing `Encoder`,
  `sender_tag`, `pad_batch`, `bucket_batches`, `sample_items`;
- `score.py` (`studies/BOBOLINK/scripts/`): `Run`, which loads a BOBOLINK run's probabilities;
- `dscore.py`, `lin.py`, `newblocks.py` (`studies/DOTTEREL/scripts/`): segment map, the torch linear trainer, and the
  builders for the extra feature blocks.

`encoder/kframe.py` restates the shared framing on its own (contract
[r4-KESTREL-FRAMING.md](../reports/r4-KESTREL-FRAMING.md)). The linear featurizer's contract is
[r2-DOTTEREL-PARITY.md](../reports/r2-DOTTEREL-PARITY.md).

## Files

| file | source (under `~/pharos-labeling/`) | what it is |
|---|---|---|
| `harness/rlh.py` | `research/harness/` | Round-1 harness: label taxonomy, hierarchy closure, threshold tuning (`per_label_jaccard`), metrics, leaderboard |
| `harness/compare.py` | `research/harness/` | Prints the round-1 leaderboard |
| `harness/rlh2.py` | `studies/AVOCET/harness/` | Harness v2: per-segment eval sets, fair leave-one-teacher-out scoring with bootstrap CIs, per-segment thresholds, prediction-file convention |
| `encoder/bob_train.py` | `studies/BOBOLINK/scripts/` | Round-2 encoder trainer (ettin, masked mean pool, 5 heads, teacher-masked BCE, framing variants, LoRA) |
| `encoder/framing.py` | `studies/BOBOLINK/scripts/` | `bob_train.py`'s framing as a class (`Framer`) |
| `encoder/targets.py` | `studies/BOBOLINK/scripts/` | Loads v1 or v2 head targets |
| `encoder/to_v2.py` | `studies/BOBOLINK/scripts/` | Converts BOBOLINK runs to the v2 prediction convention, tunes thresholds and scores them |
| `encoder/kdata.py` | `studies/KESTREL/scripts/` | One table across populations: library, EGRET thinking and public sources, with `sender_role` and the license filter |
| `encoder/kes_train.py` | `studies/KESTREL/scripts/` | KESTREL trainer: source- and segment-balanced continuation, LOSO exclusions, `--public-only` |
| `encoder/kscore.py` | `studies/KESTREL/scripts/` | Mixed-dev thresholds, F∩A and dF metrics, family-cluster bootstrap, paired comparisons |
| `encoder/licenses.py` | `studies/KESTREL/scripts/` | Writes the per-dataset license and count table |
| `encoder/kframe.py` | `studies/KESTREL/scripts/` (also in `export/kestrel-68m/`) | Framing contract `kestrel-framing-v1` in pure Python |
| `encoder/export.py` | `studies/KESTREL/scripts/` | ONNX fp32 and int8 (MatMulNBits) export plus `manifest.json` |
| `encoder/reference.py` | `studies/KESTREL/export/kestrel-68m/` (same as `scripts/`) | onnxruntime reference labeler: item → tokens → probabilities → closed labels |
| `encoder/make_fixture.py` | `studies/KESTREL/export/kestrel-68m/` (same as `scripts/`) | Generates the parity fixture from invented strings: 11 framings × 17 edge cases |
| `linear/dfeat.py` | `studies/DOTTEREL/scripts/` | Canonical featurizer for the Go linear model: tokenization, n-grams, word-order, interaction, cue and char features |
| `linear/rules.py` | `studies/DOTTEREL/scripts/` | Hand-written lexical cue regexes, used as a feature block |
| `linear/dexp.py` | `studies/DOTTEREL/scripts/` | Linear experiment runner: blocks, group copies, column cap, targets (including `hybmaj`) |
| `linear/export.py` | `studies/DOTTEREL/scripts/` | Exports a linear model to the Go format `dotterel-linear-v1` |
| `linear/refscore.py` | `studies/DOTTEREL/scripts/` | Python scoring oracle that the Go scorer must match |
| `eval/score_gannet.py` | `studies/` | One-shot GANNET holdout scoring: student vs Argon beside Flash vs Argon |
| `eval/calibrate_gannet.py` | `studies/` | The same metric on v2 held-out sets, as a no-drift baseline |
| `eval/segments.py` | `research/director/` | Round-1 scores by authorship origin |
| `eval/latency_bench.py` | `research/director/` | Batch-1 latency of int8 ONNX encoders |
| `eval/mem_bench.py` | `research/director/` | Memory and throughput of the ONNX students at small thread counts |

## Mapping to reproduction.md

| reproduction.md | files here |
|---|---|
| §4 Shared data and harnesses (scoring a model) | `harness/rlh.py`, `harness/rlh2.py`, `harness/compare.py` |
| §5 DOTTEREL's Go linear labeler | `linear/dexp.py` (train), `linear/export.py` (export), `linear/dfeat.py` and `linear/refscore.py` (oracles), `linear/rules.py` |
| §6 68m encoder `c68v-hum` | `encoder/bob_train.py`, `encoder/framing.py`, `encoder/targets.py`, `encoder/to_v2.py` |
| §6 GANNET one-shot scores | `eval/score_gannet.py`, `eval/calibrate_gannet.py` |
| §6 Latency and memory benchmarks | `eval/latency_bench.py`, `eval/mem_bench.py`, `eval/segments.py` |
| §7 KESTREL (shipping encoder) | `encoder/kdata.py`, `encoder/kes_train.py`, `encoder/kscore.py`, `encoder/licenses.py`, `encoder/kframe.py`, `encoder/export.py`, `encoder/reference.py`, `encoder/make_fixture.py` |

HERON's `hexp.py` and `hb_train.py` are copies of `linear/dexp.py` and `encoder/bob_train.py`.

## Python environment

The environment is the one in [reproduction.md](../reproduction.md) §2: Python 3.12 in `~/pharos-labeling/venv`
(built with `uv`), with numpy, pandas, pyarrow, scipy, scikit-learn, torch (MPS), transformers, tokenizers, peft, onnx and
onnxruntime. The versions used were torch 2.15, transformers 5.21, scikit-learn 1.9.1 and onnxruntime 1.x. The trainers
expect an Apple-silicon GPU (`mps`). `encoder/reference.py` needs only numpy, tokenizers and onnxruntime.
