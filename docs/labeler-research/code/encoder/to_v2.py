# Frozen research snapshot, copied unchanged below this line from ~/pharos-labeling/studies/BOBOLINK/scripts/to_v2.py (2026-10-30).
# Not maintained and not run by tests; ~/pharos-labeling paths refer to the archive layout (see ../README.md).
"""Convert BOBOLINK runs to the harness-v2 prediction convention and score them (rlh2.evaluate2).

  to_v2.py RUN [--fill r1:c-e32-512-all] [--segs H-typed,...] [--name NAME] [--global-th] [--version v1]

A run that predicted only some items (e.g. human) is completed from --fill for the rest ("routed" composite:
the specialist serves its segments, the fill model the others). --segs restricts which segments come from RUN
(the rest from --fill) even when RUN predicted everything. Scorer: mean(y3 head, y_hf head).
Thresholds: per segment (rlh2.tune_thresholds_by_segment, y3 dev, fallback y_hf) unless --global-th.
"""
import argparse, json, os, sys
sys.path.insert(0, os.path.dirname(__file__))
sys.path.insert(0, os.path.expanduser("~/pharos-labeling/studies/AVOCET/harness"))
import numpy as np
import rlh2, rlh
from score import Run

S = os.path.expanduser("~/pharos-labeling/studies/BOBOLINK")
GROUP = {"human": rlh2.HUMAN, "H-rest": ["H-template", "H-resent", "H-feedback"], "all": rlh2.SEGMENTS}


def expand(x):
    out = []
    for s in x.split(","):
        out += GROUP.get(s, [s]) if s else []
    return out


class Blend:
    """'a+b+c': mean of the runs' y3 scorers (items all runs cover)."""
    def __init__(self, name):
        self.runs = [Run(x) for x in name.split("+")]
        self.row = {i: 0 for i in self.runs[0].ids if all(i in r.row for r in self.runs[1:])}

    def scorer(self, ids, kind):
        return np.mean([r.scorer(ids, kind) for r in self.runs], axis=0)


def composite(run, fill=None, segs=None):
    ids = list(rlh2.required_ids())
    seg = rlh2.segment_of(ids)
    r = Blend(run) if "+" in run else Run(run)
    P = np.zeros((len(ids), 54), np.float32)
    have = np.array([i in r.row for i in ids])
    use = have & (np.isin(seg, segs) if segs else True)
    src = np.where(use, run, "")
    if use.any():
        P[use] = r.scorer([i for i, u in zip(ids, use) if u], "y3")
    if (~use).any():
        assert fill, f"{run}: {int((~use).sum())} items need --fill"
        f = Run(fill)
        P[~use] = f.scorer([i for i, u in zip(ids, use) if not u], "y3")
        src[~use] = fill
    return ids, P, src


def meta_of(run):
    p = os.path.join(S, "models", run, "meta.json")
    if not os.path.exists(p):
        return {}
    m = json.load(open(p))
    return {"description": f"BOBOLINK {run}", "n_train": m.get("n_train"), "train_seconds": round(60 * m.get("train_min", 0)),
            "params": m.get("params"), "trainable_params": m.get("trainable_params"), "train_segments": m.get("train_segments"),
            "frame_segments": m.get("frame_segments"), "args": m.get("args"), "peak_mps_gb": m.get("peak_mps_gb"),
            "inference_items_per_s": (m.get("bench") or {}).get("items_per_s")}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("run")
    ap.add_argument("--fill", default="")
    ap.add_argument("--segs", default="")
    ap.add_argument("--name", default="")
    ap.add_argument("--global-th", action="store_true")
    ap.add_argument("--version", default="v1")
    ap.add_argument("--quiet", action="store_true")
    ap.add_argument("--tune-target", default="y3", help="y3 | y_fa (LOO-clean: thresholds never see Haiku)")
    a = ap.parse_args()
    segs = expand(a.segs) if a.segs else None
    ids, P, src = composite(a.run, a.fill or None, segs)
    if a.global_th:
        dev = [i for i in rlh2.evalset("ALL", "y3", "dev", a.version)]
        pos = {i: k for k, i in enumerate(ids)}
        Y = rlh2.target_matrix("y3", dev, a.version)[0]
        th = rlh.tune_thresholds(P[[pos[i] for i in dev]], Y, mode="per_label_jaccard")
    elif a.tune_target != "y3":
        pos = {i: k for k, i in enumerate(ids)}
        def tune(dev):
            Y = rlh2.target_matrix(a.tune_target, dev, a.version)[0]
            return rlh.tune_thresholds(P[[pos[i] for i in dev]], Y, mode="per_label_jaccard")
        th = {"default": tune(rlh2.evalset("ALL", "y3", "dev", a.version))}
        for s_ in rlh2.SEGMENTS:
            dev = rlh2.evalset(s_, "y3", "dev", a.version)
            if len(dev) >= 150:
                th[s_] = tune(dev)
    else:
        th = rlh2.tune_thresholds_by_segment(P, ids, level="y3", version=a.version)
    name = a.name or ("b-" + a.run + ("-g" if a.global_th else ""))
    meta = {**meta_of(a.run), "composite": {s: str(v) for s, v in zip(*np.unique(src, return_counts=True))},
            "fill": a.fill, "segs_from_run": segs or "all", "scorer": "mean(y3,y_hf)"}
    f = rlh2.save_preds(os.path.join(S, "preds", f"{name}.npz"), ids, P, th if isinstance(th, dict) else th, meta)
    rlh2.evaluate2(name, "BOBOLINK", f, version=a.version, quiet=a.quiet)


if __name__ == "__main__":
    main()
