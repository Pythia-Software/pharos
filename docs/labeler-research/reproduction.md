# Reproducing the research

Everything below runs on a Mac with the euclid drive mounted. All paths under
`~/pharos-labeling/` are working copies. Their originals are archived on
euclid (see the [README](README.md)).

## 1. Restore data and code

```sh
mkdir -p ~/pharos-labeling && cd ~/pharos-labeling
L="/Volumes/euclid/Pharos Labeling"

# Labeling dataset (round 1 teachers). The bird-study copy also holds AVOCET's jobs.
rsync -a "$L/2026-10-29-bird-studies/full-20261027-with-avocet-jobs/" full-20261027/
# The original, as archived: zstd -dc "$L/2026-10-27/full-20261027-dataset.tar.zst" | tar -xf -

# Round 1 (research/) and round 2 (studies/), with scripts, models, predictions and logs.
rsync -a "$L/2026-10-28-student-research/research/" research/
rsync -a "$L/2026-10-29-bird-studies/studies/" studies/
```

The datasets hold private conversation text. Keep them on hardware you
control, and never in git.

## 2. Python environment

```sh
cd ~/pharos-labeling
uv venv --python 3.12 venv
VIRTUAL_ENV=$PWD/venv uv pip install numpy pandas pyarrow polars scipy scikit-learn lightgbm xgboost \
  torch transformers sentence-transformers datasets accelerate tokenizers onnxruntime onnx onnx_ir \
  psutil matplotlib iterative-stratification zstandard model2vec peft
```

The versions used were torch 2.15 (MPS), transformers 5.21, scikit-learn
1.9.1 and onnxruntime 1.x. A Go toolchain is needed for the Pharos binary
and DOTTEREL's prototype; Go 1.27 was used.

## 3. Pharos labeling commands

Build from this repository (`go build -o /tmp/pharos-labeling-bin ./cmd/pharos`
after `tools/setup.sh`). Then:

| Task | How |
|---|---|
| Prepare a new dataset | Only from a **static backup copy**, never the live catalog. APFS-clone the backup's `catalog.sqlite3` into a folder without lock files (`cp -c`), then run `pharos labeling prepare --dir DIR --catalog <clone> --snapshot-copy --splits-from ~/pharos-labeling/full-20261027`. This is what EGRET did; see `studies/EGRET/REPORT.md`. |
| Label | `pharos labeling run --create …`, with the routes, models and efforts in the `jobs` table. Always pass `--max-usd` and `--refusal-registry ~/pharos-labeling/labeling-refusals.jsonl`. |
| Speed up the runner | Add `CREATE INDEX attempts_job_idx ON attempts(job)` to a large dataset first (see findings). |
| Export | `pharos labeling export --dir DIR --job JOB` |
| Context variants | CHICKADEE's `--context-variant std\|long\|lastmsg\|rich` is on local branch `gbdubs/chickadee-context` (worktree `studies/CHICKADEE/src`, uncommitted). |

## 4. Shared data and harnesses

| Step | How |
|---|---|
| Round-1 tables | `research/data/build/build.py`, `evalsets.py`, `refs.py` rebuild `research/data/` (items, label matrices, eval sets) from the dataset. `research/data/README.md` documents them. |
| Round-2 segments and v2 labels | AVOCET's scripts (`studies/AVOCET/README.md`) build `studies/AVOCET/data/segments.parquet` and `data/v2/`. |
| Score a model | Write an npz in the harness-v2 convention (`rlh2.save_preds`), then `rlh2.evaluate2(run, study, path, meta, version="v2")`. `rlh2.compare` prints the table with the H-typed headline. |

## 5. The chosen model: DOTTEREL's Go linear labeler

```sh
cd ~/pharos-labeling/studies/DOTTEREL
PY=~/pharos-labeling/venv/bin/python

# train V4-hybmaj (CPU, about 2-5 min, under 14 GB)
DOT_LABELS=v2 $PY scripts/dexp.py --name V4-hybmaj \
  --blocks w,x,h,meta2,rules,seg,xu,xa,xt,pos,xi,cue,chH --maxcols 300000 \
  --copies w@H,x@H,h@H,pos@H,xi@H,xt@H --wgroup H=3 --target hybmaj

# export for Go (dotterel-linear-v1) with per-segment thresholds tuned on v2 dev
$PY scripts/export.py V4-hybmaj --thresholds preds/V4-hybmaj-segth.npz

# Go: build, unit fixtures, parity on all 95,602 eval items, throughput
cd go && go build -o work/dlabel ./cmd/dlabel && go test ./...
tools/parity.sh models/V4-hybmaj
./work/dlabel -model ../models/V4-hybmaj -in ../data/bench_items.jsonl -bench -workers 4
```

- **Contract:** `PARITY.md` is the featurizer and scoring contract (copied to
  [reports/r2-DOTTEREL-PARITY.md](reports/r2-DOTTEREL-PARITY.md)).
- **Oracles:** the Python reference featurizer is `scripts/dfeat.py`, and the
  scoring oracle is `scripts/refscore.py`.
- **Fixtures:** `go/testdata/fixtures.jsonl` is derived from corpus items, so
  it stays out of git. `go/testdata/synth/` is a synthetic model that
  exercises every block.

## 6. Other headline results

| Result | Where |
|---|---|
| 68m encoder `c68v-hum` (round 2) | `studies/BOBOLINK/scripts/bob_train.py`, framing in `framing.py`, v2 scoring `to_v2.py`; recipes in `studies/BOBOLINK/CHECKPOINT.md` |
| Round-1 encoders, ONNX and weight-only int8 | `research/tracks/C-finetune/scripts/{train.py,quant.py,cpu_bench.py}` |
| Thinking adapter | `studies/BOBOLINK/scripts/th_train.py`, on EGRET's `studies/EGRET/data/` |
| GANNET one-shot scores | `studies/score_gannet.py`, `studies/calibrate_gannet.py`, `studies/GANNET/director-linear/run.py`; output `studies/GANNET/director_scores.{csv,txt}` |
| Latency and memory benchmarks | `research/director/{latency_bench.py,mem_bench.py,segments.py}` |

Every study directory also has `CHECKPOINT.md`, which lists exact commands,
queues and file maps.
