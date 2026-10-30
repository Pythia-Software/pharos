# Frozen research snapshot, copied unchanged below this line from ~/pharos-labeling/studies/AVOCET/harness/rlh2.py (2026-10-30).
# Not maintained and not run by tests; ~/pharos-labeling paths refer to the archive layout (see ../README.md).
"""rlh2: research labeling harness v2 (round 2, AVOCET). Per-segment scoring with fair leave-one-out.

    import os, sys; sys.path.insert(0, os.path.expanduser("~/pharos-labeling/studies/AVOCET/harness"))
    import rlh2

Builds on round-1 `rlh` (closure, thresholds, metrics); see ~/pharos-labeling/studies/AVOCET/README.md.

Prediction file convention (one npz per run, `rlh2.save_preds`):
    item_ids        [n] str      every dev/test/fresh item (rlh2.required_ids()); extra ids are ignored
    probs           [n, 54] f16  label probabilities, columns = rlh.label_list(); not necessarily closed
    thresholds      [54]         global thresholds (fallback for segments without their own)
    thresholds__<SEG> [54]       optional per-segment thresholds (SEG as in rlh2.SEGMENTS)
    meta            str (JSON)   optional run metadata
Predictions are thresholded (by the item's segment), hierarchy-closed, then scored.
"""
from __future__ import annotations

import json
import os
import sys
import time
from functools import lru_cache

import numpy as np
import pandas as pd

sys.path.insert(0, os.path.expanduser("~/pharos-labeling/research/harness"))
import rlh  # noqa: E402  (round-1 harness: closure, thresholds, metrics)

HOME = os.path.expanduser("~")
AVOCET = os.path.join(HOME, "pharos-labeling/studies/AVOCET")
STUDIES = os.path.join(HOME, "pharos-labeling/studies")
LEADERBOARD2 = os.path.join(STUDIES, "leaderboard2.jsonl")
SEGMENTS = ["H-typed", "H-typed+ctx", "H-resent", "H-template", "H-feedback", "AIUR", "AR", "DEL"]
HUMAN = [s for s in SEGMENTS if s.startswith("H-")]
AGGREGATES = {"H-all": HUMAN, "ALL": SEGMENTS}
GROUPS = SEGMENTS + list(AGGREGATES)
LEVELS = ["y3", "y_hf", "flash"]          # target levels of eval sets
PARTS = ["dev", "test", "fresh"]           # splits of eval sets; 'heldout' = test + fresh (scored, not stored)
LOO = {"FA": ("y_fa", "haiku"), "HA": ("y_ha", "flash"), "HF": ("y_hf", "argon")}  # target, held-out LLM
VERSIONS = {"v1": {"matrices": os.path.join(rlh.DATA, "label_matrices.npz"), "evalsets": os.path.join(AVOCET, "data/v1/evalsets")},
            "v2": {"matrices": os.path.join(AVOCET, "data/v2/label_matrices.npz"), "evalsets": os.path.join(AVOCET, "data/v2/evalsets")}}
N_BOOT = 2000
L = 54


# ------------------------------------------------------------------ data
@lru_cache(None)
def segments() -> pd.DataFrame:
    """data/segments.parquet: item_id, segment, cohort, split and sub-fields (one row per item, items.parquet order)."""
    return pd.read_parquet(os.path.join(AVOCET, "data/segments.parquet"))


@lru_cache(None)
def _seg_of() -> dict:
    s = segments()
    return dict(zip(s.item_id, s.segment))


def segment_of(item_ids) -> np.ndarray:
    m = _seg_of()
    return np.array([m[i] for i in item_ids])


@lru_cache(None)
def required_ids() -> tuple:
    """Every dev, test and fresh item (prediction files must cover these)."""
    s = segments()
    return tuple(s.item_id[s.split.isin(PARTS)])


@lru_cache(None)
def _matrices(version: str) -> dict:
    z = np.load(VERSIONS[version]["matrices"])
    out = {k: z[k] for k in z.files}
    assert (out["item_ids"] == rlh._matrices()["item_ids"]).all(), "row order must equal items.parquet"
    return out


def target_matrix(name: str, item_ids, version: str = "v1"):
    """(Y uint8 [n, 54], present bool [n]) from the version's label matrices (names as rlh.target_matrix)."""
    m = _matrices(version)
    if name in rlh.TEACHERS:
        Y, P = m[f"{name}_closure"], m[f"{name}_present"]
    elif name == "votes":
        Y, P = m["votes"], m["n_teachers"] > 0
    elif name.split("_")[0] in rlh.TEACHERS:
        Y, P = m[name], m[name.split("_")[0] + "_present"]
    else:
        Y, P = m[name], m[name + "_present"]
    r = rlh.rows(item_ids)
    return Y[r], P[r]


def evalset_name(group: str, level: str, part: str) -> str:
    return f"{group}__{level}__{part}"


@lru_cache(None)
def _evalset(version: str, name: str) -> tuple:
    p = os.path.join(VERSIONS[version]["evalsets"], name + ".txt")
    if not os.path.exists(p):
        return ()
    with open(p) as f:
        return tuple(l.strip() for l in f if l.strip())


def evalset(group: str, level: str, part: str, version: str = "v1") -> list[str]:
    """Item ids of a segment eval set. group: a segment or 'H-all'/'ALL'; part: dev/test/fresh/heldout."""
    if part == "heldout":
        return evalset(group, level, "test", version) + evalset(group, level, "fresh", version)
    if group in AGGREGATES:
        return [i for g in AGGREGATES[group] for i in _evalset(version, evalset_name(g, level, part))]
    return list(_evalset(version, evalset_name(group, level, part)))


def evalset_sizes(version: str = "v1") -> pd.DataFrame:
    rows = []
    for g in GROUPS:
        for lv in LEVELS:
            r = {"group": g, "level": lv}
            for p in PARTS + ["heldout"]:
                r[p] = len(evalset(g, lv, p, version))
            rows.append(r)
    return pd.DataFrame(rows)


def build_evalsets(version: str = "v1") -> pd.DataFrame:
    """Write the version's per-segment eval sets (exclusions as round 1: is_duplicate, seen_in_train)."""
    it = rlh.load_items(columns=["item_id", "split", "is_duplicate", "seen_in_train", "common_short_text"])
    seg = segments()
    assert (it.item_id.to_numpy() == seg.item_id.to_numpy()).all()
    m = _matrices(version)
    ok = (~it.is_duplicate & ~it.seen_in_train).to_numpy()
    present = {"y3": m["y3_present"], "y_hf": m["y_hf_present"], "flash": m["flash_present"]}
    d = VERSIONS[version]["evalsets"]
    os.makedirs(d, exist_ok=True)
    sizes = {}
    for g in SEGMENTS:
        for lv in LEVELS:
            for p in PARTS:
                sel = ok & present[lv] & (it.split.to_numpy() == p) & (seg.segment.to_numpy() == g)
                ids = it.item_id.to_numpy()[sel]
                with open(os.path.join(d, evalset_name(g, lv, p) + ".txt"), "w") as f:
                    f.write("\n".join(ids) + ("\n" if len(ids) else ""))
                sizes[evalset_name(g, lv, p)] = {"n": int(sel.sum()), "common_short_text": int((sel & it.common_short_text.to_numpy()).sum())}
    excl = {"is_duplicate": int(it.is_duplicate.sum()), "seen_in_train": {p: int((it.seen_in_train & (it.split == p)).sum()) for p in PARTS}}
    with open(os.path.join(d, "sizes.json"), "w") as f:
        json.dump({"sizes": sizes, "excluded": excl}, f, indent=1)
    _evalset.cache_clear()
    return evalset_sizes(version)


# --------------------------------------------------------- predictions
def save_preds(path: str, item_ids, probs, thresholds, meta: dict | None = None, allow_partial: bool = False):
    """Write a prediction file in the v2 convention. thresholds: [54] array or {segment: [54], 'default': [54]}."""
    ids = np.asarray(list(item_ids))
    probs = np.asarray(probs)
    assert probs.shape == (len(ids), L), probs.shape
    if not allow_partial:
        missing = set(required_ids()) - set(ids.tolist())
        if missing:
            raise ValueError(f"{len(missing)} dev/test/fresh items lack predictions (e.g. {sorted(missing)[:3]})")
    out = {"item_ids": ids, "probs": probs.astype(np.float16)}
    if isinstance(thresholds, dict):
        th = strip_source(thresholds)
        default = th.pop("default", None)
        if default is None:
            default = th.pop("global", 0.5)
        out["thresholds"] = np.broadcast_to(np.asarray(default, float), (L,)).copy()
        for s, t in th.items():
            assert s in SEGMENTS, s
            out[f"thresholds__{s}"] = np.broadcast_to(np.asarray(t, float), (L,)).copy()
    else:
        out["thresholds"] = np.broadcast_to(np.asarray(thresholds, float), (L,)).copy()
    if meta:
        out["meta"] = np.array(json.dumps(meta, default=float))
    os.makedirs(os.path.dirname(os.path.abspath(path)), exist_ok=True)
    np.savez_compressed(path, **out)
    return path


def load_preds(pred_file: str) -> dict:
    z = np.load(pred_file, allow_pickle=False)
    d = {"item_ids": z["item_ids"].astype(str), "probs": z["probs"] if "probs" in z.files else None,
         "pred": z["pred"] if "pred" in z.files else None,
         "thresholds": z["thresholds"] if "thresholds" in z.files else np.full(L, 0.5),
         "seg_thresholds": {k.split("__", 1)[1]: z[k] for k in z.files if k.startswith("thresholds__")},
         "meta": json.loads(str(z["meta"])) if "meta" in z.files else {}}
    return d


def binarize(pd_: dict, idx: np.ndarray, segs: np.ndarray) -> np.ndarray:
    """Closed binary predictions for rows idx of a loaded pred file, using each item's segment thresholds."""
    if pd_["pred"] is not None:
        return rlh.close_binary(pd_["pred"][idx])
    P = pd_["probs"][idx].astype(np.float32)
    T = np.tile(np.asarray(pd_["thresholds"], float), (len(idx), 1))
    for s, t in pd_["seg_thresholds"].items():
        T[segs == s] = t
    return rlh.close_binary((P >= T).astype(np.uint8))


# --------------------------------------------------------------- scoring
def _jr(P, Y):
    return rlh._jaccard_rows(P.astype(bool), Y.astype(bool))


def _compact(s):
    keys = ("n", "jaccard", "jaccard_se", "exact", "micro_f1", "macro_f1", "macro_labels", "micro_p", "micro_r", "mean_pred_labels", "mean_target_labels")
    return {k: (round(s[k], 4) if isinstance(s.get(k), float) else s.get(k)) for k in keys}


def _boot(d: np.ndarray, rng) -> list:
    if len(d) < 2:
        return [None, None]
    idx = rng.integers(0, len(d), size=(N_BOOT, len(d)))
    bs = d[idx].mean(1)
    return [round(float(np.percentile(bs, 2.5)), 4), round(float(np.percentile(bs, 97.5)), 4)]


def score_sets(B_of, version="v1", groups=GROUPS, levels=LEVELS, parts=PARTS + ["heldout"], per_label=False, seed=0):
    """Score closed binary predictions on every (group, level, part) eval set.

    B_of(ids) -> (binary [n,54] closed, covered bool [n]). Returns {set_name: result}.
    y3 sets add LOO: predictor vs F∩A / H∩A / H∩F, the held-out LLM's score on the same items, and the paired
    bootstrap CI (item resampling, 2000 draws) of predictor − LLM.
    """
    rng = np.random.default_rng(seed)
    out = {}
    for g in groups:
        for lv in levels:
            for p in parts:
                ids = evalset(g, lv, p, version)
                if not ids:
                    continue
                B, cov = B_of(ids)
                res = {"n_set": len(ids), "coverage": round(float(cov.mean()), 4)}
                if not cov.any():
                    out[evalset_name(g, lv, p)] = res
                    continue
                ids_c = [i for i, c in zip(ids, cov) if c]
                B = B[cov]
                Y, pres = target_matrix(lv, ids_c, version)
                assert pres.all()
                s = rlh.score(B, Y, per_label=per_label)
                res.update(_compact(s))
                if per_label:
                    res["per_label"] = s["per_label"]
                if lv == "y3":
                    loo = {}
                    for k, (tname, who) in LOO.items():
                        Yk, _ = target_matrix(tname, ids_c, version)
                        Lk, _ = target_matrix(who, ids_c, version)
                        js, jl = _jr(B, Yk), _jr(Lk, Yk)
                        sk = rlh.score(B, Yk); sl = rlh.score(Lk, Yk)
                        loo[k] = {"jaccard": round(float(js.mean()), 4), "micro_f1": round(sk["micro_f1"], 4),
                                  "macro_f1": None if sk["macro_f1"] is None else round(sk["macro_f1"], 4),
                                  "llm": who, "llm_jaccard": round(float(jl.mean()), 4), "llm_micro_f1": round(sl["micro_f1"], 4),
                                  "llm_macro_f1": None if sl["macro_f1"] is None else round(sl["macro_f1"], 4),
                                  "delta": round(float((js - jl).mean()), 4), "delta_ci95": _boot(js - jl, rng)}
                    res["loo"] = loo
                out[evalset_name(g, lv, p)] = res
    return out


def _coverage_fn(pd_):
    pos = {i: k for k, i in enumerate(pd_["item_ids"])}

    def B_of(ids):
        idx = np.array([pos.get(i, -1) for i in ids])
        cov = idx >= 0
        B = np.zeros((len(ids), L), np.uint8)
        if cov.any():
            B[cov] = binarize(pd_, idx[cov], segment_of([i for i, c in zip(ids, cov) if c]))
        return B, cov
    return B_of


def evaluate2(run: str, study: str, pred_file: str, meta: dict | None = None, version: str = "v1",
              write: bool = True, quiet: bool = False, allow_partial: bool = False, per_label: bool = True) -> dict:
    """Score a v2 prediction file on every segment eval set of `version` and append a row to leaderboard2.jsonl.

    Writes the full result (per-label metrics on heldout sets) to studies/<study>/preds/<run>.<version>.metrics.json.
    allow_partial: score only covered items (reported as `coverage`); for re-scoring old runs with partial preds.
    """
    pd_ = load_preds(pred_file)
    if not allow_partial:
        missing = set(required_ids()) - set(pd_["item_ids"].tolist())
        if missing:
            raise ValueError(f"{pred_file}: {len(missing)} dev/test/fresh items lack predictions (e.g. {sorted(missing)[:3]})")
    meta = {**pd_["meta"], **(meta or {})}
    if "estimated_hours_400k" not in meta and meta.get("inference_items_per_s"):
        meta["estimated_hours_400k"] = round(400000 / float(meta["inference_items_per_s"]) / 3600, 3)
    full = score_sets(_coverage_fn(pd_), version, per_label=False)
    if per_label:  # per-label metrics only on heldout sets (keeps the row small)
        pl = score_sets(_coverage_fn(pd_), version, parts=["heldout"], per_label=True)
        for k, v in pl.items():
            if "per_label" in v:
                full[k]["per_label"] = v["per_label"]
    results = {k: {kk: vv for kk, vv in v.items() if kk != "per_label"} for k, v in full.items()}
    row = {"run_name": run, "study": study, "time": time.strftime("%Y-%m-%dT%H:%M:%S%z"), "data_version": version,
           "pred_file": os.path.abspath(pred_file),
           "thresholds": "per_segment:" + ",".join(sorted(pd_["seg_thresholds"])) if pd_["seg_thresholds"] else "global",
           "meta": meta, "results": results}
    if write:
        d = os.path.join(STUDIES, study, "preds")
        os.makedirs(d, exist_ok=True)
        with open(os.path.join(d, f"{run}.{version}.metrics.json"), "w") as f:
            json.dump(full, f, default=float)
        with open(LEADERBOARD2, "a") as f:
            f.write(json.dumps(row, default=float) + "\n")
    if not quiet:
        print(summary(row))
    return row


def rescore(pred_file: str, run: str, study: str, version: str = "v2", meta: dict | None = None, **kw) -> dict:
    """Re-score an existing prediction file on another version's eval sets (stage 2: version='v2')."""
    m = dict(meta or {})
    m.setdefault("rescored_from", os.path.abspath(pred_file))
    return evaluate2(run, study, pred_file, m, version=version, **kw)


def evaluate_teacher(name: str, study: str = "AVOCET", version: str = "v1", write: bool = True, quiet: bool = True) -> dict:
    """Reference row: a teacher's (or consensus's) closed label set as the prediction, where it exists."""
    m = _matrices(version)
    if name in rlh.TEACHERS:
        Y, P = m[f"{name}_closure"], m[f"{name}_present"]
    else:
        Y, P = m[name], m[name + "_present"]
    ids = m["item_ids"].astype(str)
    pos = {i: k for k, i in enumerate(ids)}

    def B_of(q):
        r = np.array([pos[i] for i in q])
        return Y[r], P[r]
    full = score_sets(B_of, version)
    row = {"run_name": f"ref-{name}", "study": study, "time": time.strftime("%Y-%m-%dT%H:%M:%S%z"), "data_version": version,
           "pred_file": None, "thresholds": "teacher labels",
           "meta": {"description": f"teacher reference: {name} closure as the prediction (scored where present)"}, "results": full}
    if write:
        with open(LEADERBOARD2, "a") as f:
            f.write(json.dumps(row, default=float) + "\n")
    if not quiet:
        print(summary(row))
    return row


# ---------------------------------------------------------- thresholds
def tune_thresholds_by_segment(probs, item_ids, level: str = "y3", mode: str = "per_label_jaccard", version: str = "v1",
                               min_items: int = 150, fallback_level: str | None = "y_hf", groups=None, **kw) -> dict:
    """Per-segment thresholds tuned on dev.

    probs/item_ids: predictions for (at least) the dev items. For each segment, tunes rlh.tune_thresholds on the
    segment's dev set at `level`; if that set has < min_items items, uses `fallback_level` (default y_hf) if that has
    enough, else the segment keeps the global thresholds ('default', tuned on the union of all segments' dev sets
    at `level`). Returns {'default': [54], segment: [54], ...} ready for save_preds; also '_source' {seg: how}.
    """
    pos = {i: k for k, i in enumerate(item_ids)}
    P = np.asarray(probs, dtype=np.float32)

    def tune(ids, lv):
        r = np.array([pos[i] for i in ids])
        Y, _ = target_matrix(lv, ids, version)
        return rlh.tune_thresholds(P[r], Y, mode=mode, **kw)
    all_dev = [i for i in evalset("ALL", level, "dev", version) if i in pos]
    out = {"default": tune(all_dev, level)}
    src = {"default": f"ALL dev {level} n={len(all_dev)}"}
    for s in (groups or SEGMENTS):
        ids = [i for i in evalset(s, level, "dev", version) if i in pos]
        if len(ids) >= min_items:
            out[s] = tune(ids, level); src[s] = f"{s} dev {level} n={len(ids)}"
            continue
        if fallback_level:
            ids2 = [i for i in evalset(s, fallback_level, "dev", version) if i in pos]
            if len(ids2) >= min_items:
                out[s] = tune(ids2, fallback_level); src[s] = f"{s} dev {fallback_level} n={len(ids2)}"
                continue
        src[s] = f"default (dev {level} n={len(ids)})"
    out["_source"] = src
    return out


def strip_source(th: dict) -> dict:
    return {k: v for k, v in th.items() if not k.startswith("_")}


# ---------------------------------------------------------- reporting
def summary(row, parts=("heldout",)) -> str:
    r = row["results"]
    lines = [f"== {row['run_name']} [{row['study']}] data {row['data_version']} thresholds {row.get('thresholds')}"]
    for g in GROUPS:
        cells = []
        for lv in LEVELS:
            for p in parts:
                x = r.get(evalset_name(g, lv, p))
                if not x or "jaccard" not in x:
                    continue
                c = f"{lv}/{p} n={x['n']} J={x['jaccard']:.3f}"
                if x.get("coverage", 1) < 1:
                    c += f" (cov {x['coverage']:.2f})"
                if "loo" in x:
                    c += " LOO " + " ".join(f"{k}={v['jaccard']:.3f}({v['llm']} {v['llm_jaccard']:.3f}, d {v['delta']:+.3f} "
                                            f"[{v['delta_ci95'][0]:+.3f},{v['delta_ci95'][1]:+.3f}])" if v["delta_ci95"][0] is not None else
                                            f"{k}={v['jaccard']:.3f}({v['llm']} {v['llm_jaccard']:.3f})" for k, v in x["loo"].items())
                cells.append(c)
        if cells:
            lines.append(f"  {g:<12} " + " | ".join(cells))
    return "\n".join(lines)


def read_leaderboard2() -> list[dict]:
    if not os.path.exists(LEADERBOARD2):
        return []
    with open(LEADERBOARD2) as f:
        return [json.loads(l) for l in f if l.strip()]


def compare(version: str = "v1", part: str = "heldout", study: str | None = None, runs=None, latest_only: bool = True,
            groups=("H-typed+ctx", "H-template", "AIUR", "AR", "DEL", "ALL")) -> pd.DataFrame:
    """Leaderboard table. Headline: H-typed on `part` (heldout = test+fresh): LOO vs F∩A with Haiku and Δ CI,
    LOO vs H∩A / H∩F, J vs y3 and vs y_hf (larger n). Then J vs y3 and LOO-FA for the other groups."""
    rows = [r for r in read_leaderboard2() if r["data_version"] == version and (study is None or r["study"] == study)
            and (runs is None or r["run_name"] in runs)]
    if latest_only:
        d = {}
        for r in rows:
            d[(r["study"], r["run_name"])] = r
        rows = list(d.values())
    out = []
    for r in rows:
        R = r["results"]
        h3, h2 = R.get(evalset_name("H-typed", "y3", part), {}), R.get(evalset_name("H-typed", "y_hf", part), {})
        o = {"run": r["run_name"], "study": r["study"]}
        lo = h3.get("loo", {})
        if lo:
            fa = lo["FA"]
            o["Ht n3"] = h3.get("n")
            o["Ht FA"] = fa["jaccard"]; o["Ht haiku"] = fa["llm_jaccard"]
            o["Ht d-haiku"] = fa["delta"]
            o["Ht CI"] = f"[{fa['delta_ci95'][0]:+.3f},{fa['delta_ci95'][1]:+.3f}]" if fa["delta_ci95"][0] is not None else ""
            o["Ht HA"] = lo["HA"]["jaccard"]; o["Ht flash"] = lo["HA"]["llm_jaccard"]
            o["Ht HF"] = lo["HF"]["jaccard"]; o["Ht argon"] = lo["HF"]["llm_jaccard"]
        o["Ht y3"] = h3.get("jaccard")
        o["Ht yhf"] = h2.get("jaccard"); o["Ht n2"] = h2.get("n")
        for g in groups:
            x = R.get(evalset_name(g, "y3", part), {})
            o[f"{g} y3"] = x.get("jaccard")
            if x.get("loo"):
                o[f"{g} FA"] = x["loo"]["FA"]["jaccard"]
        out.append(o)
    df = pd.DataFrame(out)
    if len(df) and "Ht FA" in df:
        df = df.sort_values("Ht FA", ascending=False, na_position="last")
    return df


if __name__ == "__main__":
    import argparse
    ap = argparse.ArgumentParser(description="rlh2 compare table")
    ap.add_argument("--version", default="v1"); ap.add_argument("--part", default="heldout")
    ap.add_argument("--study"); ap.add_argument("--all-rows", action="store_true")
    a = ap.parse_args()
    pd.set_option("display.width", 250); pd.set_option("display.max_columns", 50)
    print(compare(a.version, a.part, a.study, latest_only=not a.all_rows).to_string(index=False))
