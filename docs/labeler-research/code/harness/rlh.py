# Frozen research snapshot, copied unchanged below this line from ~/pharos-labeling/research/harness/rlh.py (2026-10-30).
# Not maintained and not run by tests; ~/pharos-labeling paths refer to the archive layout (see ../README.md).
"""rlh: research labeling harness (shared by every track).

    import sys; sys.path.insert(0, os.path.expanduser("~/pharos-labeling/research/harness"))
    import rlh

See ~/pharos-labeling/research/data/README.md.
"""
from __future__ import annotations

import json
import os
import time
from functools import lru_cache

import numpy as np
import pandas as pd

ROOT = os.path.expanduser("~/pharos-labeling/research")
DATA = os.path.join(ROOT, "data")
LEADERBOARD = os.path.join(ROOT, "leaderboard.jsonl")
COHORTS = ["agent_reply", "agent_in_user_role", "delegation", "human"]
TEACHERS = ["haiku", "flash", "argon"]
MACRO_MIN_SUPPORT = 20

# eval set -> target name used for scoring
EVALSET_TARGET = {
    "gold3_dev": "y3", "gold3_test": "y3",
    "gold2_dev": "y_hf", "gold2_test": "y_hf",
    "flash_dev": "flash", "flash_test": "flash",
}
# leave-one-out consensus targets scored on gold3 sets
LOO_TARGETS = {"FA": "y_fa", "HA": "y_ha", "HF": "y_hf"}
LOO_FAIR = {"haiku": "FA", "flash": "HA", "argon": "HF"}


# ----------------------------------------------------------------- loaders
@lru_cache(None)
def labels_meta() -> dict:
    with open(os.path.join(DATA, "labels.json")) as f:
        return json.load(f)


def label_list() -> list[str]:
    """Label ids in column order (taxonomy order)."""
    return list(labels_meta()["column_order"])


@lru_cache(None)
def parent_index() -> np.ndarray:
    """parent_index()[j] = column of label j's parent, or -1."""
    meta = labels_meta()
    pos = {l: i for i, l in enumerate(meta["column_order"])}
    return np.array([pos[x["parent"]] if x["parent"] else -1 for x in meta["labels"]])


@lru_cache(None)
def _ancestor_matrix() -> np.ndarray:
    """A[j, k] = 1 if k is j or an ancestor of j."""
    par = parent_index()
    L = len(par)
    A = np.eye(L, dtype=bool)
    for j in range(L):
        p = par[j]
        while p >= 0:
            A[j, p] = True
            p = par[p]
    return A


def load_items(columns=None) -> pd.DataFrame:
    """items.parquet (365k rows). Pass columns=[...] to skip text."""
    return pd.read_parquet(os.path.join(DATA, "items.parquet"), columns=columns)


def load_labels(teacher=None) -> pd.DataFrame:
    """labels.parquet: one row per (item, teacher) authoritative result."""
    df = pd.read_parquet(os.path.join(DATA, "labels.parquet"))
    return df if teacher is None else df[df.teacher == teacher].reset_index(drop=True)


@lru_cache(None)
def _matrices():
    z = np.load(os.path.join(DATA, "label_matrices.npz"))
    return {k: z[k] for k in z.files}


@lru_cache(None)
def _row_of() -> dict:
    return {i: k for k, i in enumerate(_matrices()["item_ids"])}


@lru_cache(None)
def _meta_arrays():
    it = load_items(columns=["item_id", "cohort", "split", "common_short_text"])
    m = _matrices()
    assert (it.item_id.to_numpy() == m["item_ids"]).all()
    return it.cohort.to_numpy(), it.split.to_numpy(), it.common_short_text.to_numpy()


def rows(item_ids) -> np.ndarray:
    r = _row_of()
    return np.array([r[i] for i in item_ids], dtype=np.int64)


def target_matrix(name: str, item_ids=None):
    """(Y uint8 [n, L], present bool [n]) for a target.

    name: 'y3', 'y_hf', 'y_fa', 'y_ha' (intersections of closures), 'haiku'/'flash'/'argon' (closure),
    '<teacher>_masked', '<teacher>_supported', '<teacher>_raw_closure', 'votes' (0..3 per label; present = n_teachers>0).
    Rows follow item_ids (default: items.parquet order).
    """
    m = _matrices()
    if name in TEACHERS:
        Y, P = m[f"{name}_closure"], m[f"{name}_present"]
    elif name == "votes":
        Y, P = m["votes"], m["n_teachers"] > 0
    elif name.split("_")[0] in TEACHERS:
        Y, P = m[name], m[name.split("_")[0] + "_present"]
    else:
        Y, P = m[name], m[name + "_present"]
    if item_ids is None:
        return Y, P
    r = rows(item_ids)
    return Y[r], P[r]


def n_teachers(item_ids=None):
    m = _matrices()["n_teachers"]
    return m if item_ids is None else m[rows(item_ids)]


def evalset_names() -> list[str]:
    d = os.path.join(DATA, "evalsets")
    return sorted(f[:-4] for f in os.listdir(d) if f.endswith(".txt"))


@lru_cache(None)
def _evalset(name: str) -> tuple:
    with open(os.path.join(DATA, "evalsets", name + ".txt")) as f:
        return tuple(l.strip() for l in f if l.strip())


def evalset(name: str) -> list[str]:
    """Item ids of an eval set ('gold3_dev', 'gold3_test', ..., or 'train')."""
    return list(_evalset(name))


@lru_cache(None)
def cohort_weights() -> dict:
    """Each cohort's share of all items with a usable teacher result."""
    c, _, _ = _meta_arrays()
    s = pd.Series(c).value_counts(normalize=True)
    return {k: float(s.get(k, 0.0)) for k in COHORTS}


# ------------------------------------------------------- predictions utils
def close_binary(pred: np.ndarray) -> np.ndarray:
    """Hierarchy closure: a predicted label adds all its ancestors."""
    A = _ancestor_matrix().astype(np.int32)
    return ((pred.astype(np.int32) @ A) > 0).astype(np.uint8)


def close_probs(probs: np.ndarray) -> np.ndarray:
    """p(parent) := max(p(parent), p(descendants)); optional before tuning thresholds."""
    A = _ancestor_matrix()
    out = probs.copy()
    for j in range(A.shape[0]):
        anc = np.flatnonzero(A[j])
        out[:, anc] = np.maximum(out[:, anc], probs[:, [j]])
    return out


def apply_thresholds(probs, thresholds, close=True) -> np.ndarray:
    t = np.broadcast_to(np.asarray(thresholds, dtype=float), (probs.shape[1],))
    pred = (probs >= t[None, :]).astype(np.uint8)
    return close_binary(pred) if close else pred


def _f1(tp, fp, fn):
    d = 2 * tp + fp + fn
    return np.where(d > 0, 2 * tp / np.maximum(d, 1e-12), 0.0)


def _jaccard_rows(P, Y):
    inter = (P & Y).sum(1).astype(float)
    union = (P | Y).sum(1).astype(float)
    return np.where(union > 0, inter / np.maximum(union, 1), 1.0)


def tune_thresholds(dev_probs, dev_targets, mode="per_label_f1", min_support=MACRO_MIN_SUPPORT,
                    grid=None, close=True, passes=2):
    """Thresholds tuned on dev.

    mode: 'global_f1' (one threshold maximizing micro-F1), 'global_jaccard' (one threshold maximizing mean
    Jaccard), 'per_label_f1' (each label's own F1; labels with < min_support dev positives get the best
    global_f1 threshold), 'per_label_jaccard' (coordinate ascent on mean Jaccard from the best global_jaccard
    threshold; labels with < min_support positives stay at the global value).
    Scores are computed after hierarchy closure when close=True (as evaluate() does).
    Returns np.ndarray [L].
    """
    P = np.asarray(dev_probs, dtype=np.float32)
    Y = np.asarray(dev_targets).astype(np.uint8)
    L = P.shape[1]
    grid = np.round(np.arange(0.02, 0.981, 0.02), 4) if grid is None else np.asarray(grid)
    support = Y.sum(0)

    def micro(t):
        B = apply_thresholds(P, t, close)
        tp = int((B & Y).sum()); fp = int((B & (1 - Y)).sum()); fn = int(((1 - B) & Y).sum())
        return float(_f1(tp, fp, fn))

    def jac(t):
        return float(_jaccard_rows(apply_thresholds(P, t, close), Y).mean())

    if mode in ("global_f1", "per_label_f1"):
        g = max(grid, key=micro)
        if mode == "global_f1":
            return np.full(L, g)
        th = np.full(L, g)
        for j in range(L):
            if support[j] < min_support:
                continue
            p, y = P[:, j], Y[:, j].astype(bool)
            best, bt = -1, g
            for t in grid:
                b = p >= t
                f = _f1((b & y).sum(), (b & ~y).sum(), (~b & y).sum())
                if f > best:
                    best, bt = f, t
            th[j] = bt
        return th
    if mode in ("global_jaccard", "per_label_jaccard"):
        g = max(grid, key=jac)
        th = np.full(L, g)
        if mode == "global_jaccard":
            return th
        # Coordinate ascent; changing label j's threshold only changes the closed columns of j and its
        # ancestors, so score every grid value of j at once from the other columns' fixed contribution.
        A = _ancestor_matrix()
        Yb = Y.astype(bool)
        R = P >= th[None, :]
        for _ in range(passes):
            changed = False
            for j in range(L):
                if support[j] < min_support:
                    continue
                ks = np.flatnonzero(A[j]) if close else np.array([j])
                others = np.setdiff1d(np.arange(L), ks)
                Rwo = R.copy()
                Rwo[:, j] = False
                Cwo = close_binary(Rwo).astype(bool) if close else Rwo
                inter = (Cwo[:, others] & Yb[:, others]).sum(1)[:, None].astype(np.int32)
                union = (Cwo[:, others] | Yb[:, others]).sum(1)[:, None].astype(np.int32)
                cand = P[:, j][:, None] >= grid[None, :]
                for k in ks:
                    ck = Cwo[:, k][:, None] | cand
                    yk = Yb[:, k][:, None]
                    inter = inter + (ck & yk)
                    union = union + (ck | yk)
                J = np.where(union > 0, inter / np.maximum(union, 1), 1.0).mean(0)
                cur_g = int(np.argmin(np.abs(grid - th[j])))
                g_best = int(np.argmax(J))
                if J[g_best] > J[cur_g] + 1e-9:
                    th[j] = grid[g_best]
                    R[:, j] = cand[:, g_best]
                    changed = True
            if not changed:
                break
        return th
    raise ValueError(mode)


# ----------------------------------------------------------------- metrics
def score(pred, Y, weights=None, per_label=False, min_support=MACRO_MIN_SUPPORT) -> dict:
    """Metrics of binary predictions against binary targets (both already closed).

    weights: optional per-item weights (cohort weighting). Support thresholds use unweighted counts.
    """
    P = np.asarray(pred).astype(bool)
    Y = np.asarray(Y).astype(bool)
    n = len(Y)
    if n == 0:
        return {"n": 0}
    w = np.ones(n) if weights is None else np.asarray(weights, float)
    w = w / w.sum()
    jr = _jaccard_rows(P, Y)
    exact = (P == Y).all(1)
    tp = (w[:, None] * (P & Y)).sum(0); fp = (w[:, None] * (P & ~Y)).sum(0); fn = (w[:, None] * (~P & Y)).sum(0)
    sup = Y.sum(0)
    f1 = _f1(tp, fp, fn)
    keep = sup >= min_support
    jm = float((w * jr).sum())
    out = {"n": int(n), "jaccard": jm, "jaccard_se": float(np.sqrt((w ** 2 * (jr - jm) ** 2).sum())), "exact": float((w * exact).sum()),
           "micro_p": float(tp.sum() / max(tp.sum() + fp.sum(), 1e-12)),
           "micro_r": float(tp.sum() / max(tp.sum() + fn.sum(), 1e-12)),
           "micro_f1": float(_f1(tp.sum(), fp.sum(), fn.sum())),
           "macro_f1": float(f1[keep].mean()) if keep.any() else None, "macro_labels": int(keep.sum()),
           "mean_pred_labels": float((w * P.sum(1)).sum()), "mean_target_labels": float((w * Y.sum(1)).sum()),
           "pred_empty": float((w * (P.sum(1) == 0)).sum()), "target_empty": float((w * (Y.sum(1) == 0)).sum())}
    if per_label:
        names = label_list()
        prec = np.where(tp + fp > 0, tp / np.maximum(tp + fp, 1e-12), 0.0)
        rec = np.where(tp + fn > 0, tp / np.maximum(tp + fn, 1e-12), 0.0)
        out["per_label"] = {names[j]: {"p": round(float(prec[j]), 4), "r": round(float(rec[j]), 4),
                                       "f1": round(float(f1[j]), 4), "support": int(sup[j]),
                                       "pred": int(P[:, j].sum())} for j in range(len(names))}
    return out


def _weights_for(cohorts):
    cw = cohort_weights()
    s = pd.Series(cohorts)
    counts = s.value_counts()
    present = {c: cw[c] for c in counts.index}
    tot = sum(present.values())
    return s.map(lambda c: present[c] / tot / counts[c]).to_numpy()


def _strata(item_ids):
    c, sp, cst = _meta_arrays()
    r = rows(item_ids)
    strata = {"all": np.ones(len(r), bool)}
    for k in COHORTS:
        strata[f"cohort={k}"] = c[r] == k
    strata["common_short_text"] = cst[r]
    strata["not_common_short_text"] = ~cst[r]
    for s in ("test", "fresh"):
        m = sp[r] == s
        if m.any() and not m.all():
            strata[f"split={s}"] = m
    return strata, c[r]


def score_all(pred, Y, item_ids, per_label=True) -> dict:
    """Raw and cohort-weighted metrics, overall and by stratum."""
    strata, cohorts = _strata(item_ids)
    out = {}
    for name, m in strata.items():
        if not m.any():
            continue
        res = {"raw": score(pred[m], Y[m], per_label=per_label and name == "all")}
        if not name.startswith("cohort="):
            res["weighted"] = score(pred[m], Y[m], weights=_weights_for(cohorts[m]),
                                    per_label=per_label and name == "all")
        out[name] = res
    return out


def _compact(s):
    keys = ("n", "jaccard", "jaccard_se", "exact", "micro_f1", "macro_f1", "micro_p", "micro_r", "mean_pred_labels")
    return {k: (round(s[k], 4) if isinstance(s.get(k), float) else s.get(k)) for k in keys}


# ---------------------------------------------------------------- evaluate
def _ref_loo():
    path = os.path.join(DATA, "reference_loo.json")
    if os.path.exists(path):
        with open(path) as f:
            return json.load(f)
    return {}


def _normalize(entry):
    if isinstance(entry, dict):
        return entry["item_ids"], entry.get("probs"), entry.get("thresholds"), entry.get("pred")
    if len(entry) == 2:
        ids, x = entry
        x = np.asarray(x)
        if x.dtype in (np.uint8, bool, np.int32, np.int64):
            return ids, None, None, x
        return ids, x, 0.5, None
    ids, probs, th = entry
    return ids, probs, th, None


def evaluate(run_name: str, track: str, preds: dict, meta: dict | None = None, write=True, quiet=False) -> dict:
    """Score a run on eval sets and append it to the leaderboard.

    preds: {evalset_name: (item_ids, probs[n, L], thresholds)} or {name: {"item_ids", "probs", "thresholds"}}
           or {name: {"item_ids", "pred": binary[n, L]}}. thresholds: scalar or [L] (default 0.5).
           Columns follow labels.json. Every id of the eval set must be present (extra ids are ignored).
    Predictions are hierarchy-closed before scoring. Probabilities are saved to
    tracks/<track>/preds/<run_name>.npz and full per-stratum metrics to <run_name>.metrics.json.
    """
    meta = dict(meta or {})
    L = len(label_list())
    results, saved, full = {}, {}, {}
    for es, entry in preds.items():
        if es not in EVALSET_TARGET:
            raise ValueError(f"unknown eval set {es}; known: {sorted(EVALSET_TARGET)}")
        ids, probs, th, pred = _normalize(entry)
        ids = list(ids)
        want = evalset(es)
        pos = {i: k for k, i in enumerate(ids)}
        missing = [i for i in want if i not in pos]
        if missing:
            raise ValueError(f"{es}: {len(missing)} eval-set items have no prediction (e.g. {missing[:3]})")
        sel = np.array([pos[i] for i in want])
        if probs is not None:
            probs = np.asarray(probs, dtype=np.float32)[sel]
            assert probs.shape[1] == L, probs.shape
            th = 0.5 if th is None else th
            B = apply_thresholds(probs, th, close=True)
            saved[f"{es}__probs"] = probs.astype(np.float16) if probs.size > 5e7 else probs
            saved[f"{es}__thresholds"] = np.broadcast_to(np.asarray(th, float), (L,)).copy()
        else:
            pred = np.asarray(pred)[sel]
            assert pred.shape[1] == L, pred.shape
            B = close_binary(pred)
            saved[f"{es}__pred"] = B
        saved[f"{es}__item_ids"] = np.array(want)
        tname = EVALSET_TARGET[es]
        Y, present = target_matrix(tname, want)
        assert present.all(), f"{es}: target {tname} missing for some items"
        full[es] = score_all(B, Y, want)
        res = {"target": tname, "n": len(want),
               "overall": {"raw": _compact(full[es]["all"]["raw"]), "weighted": _compact(full[es]["all"]["weighted"])},
               "strata": {k: {kk: _compact(vv) for kk, vv in v.items()} for k, v in full[es].items() if k != "all"},
               "per_label": full[es]["all"]["raw"]["per_label"]}
        if es.startswith("gold3"):
            loo = {}
            ref = _ref_loo().get(es, {})
            _, cohorts = _strata(want)
            w = _weights_for(cohorts)
            for k, t in LOO_TARGETS.items():
                Yk, _ = target_matrix(t, want)
                loo[k] = {"raw": _compact(score(B, Yk)), "weighted": _compact(score(B, Yk, weights=w)),
                          "by_cohort": {c: round(score(B[cohorts == c], Yk[cohorts == c])["jaccard"], 4)
                                        for c in COHORTS if (cohorts == c).any()}}
                if k in ref:
                    loo[k]["llm_reference"] = ref[k]
            res["loo"] = loo
        results[es] = res

    if "estimated_hours_400k" not in meta and meta.get("inference_items_per_s"):
        meta["estimated_hours_400k"] = round(400000 / float(meta["inference_items_per_s"]) / 3600, 3)
    row = {"run_name": run_name, "track": track, "time": time.strftime("%Y-%m-%dT%H:%M:%S%z"), "meta": meta,
           "results": results}
    if write:
        d = os.path.join(ROOT, "tracks", track, "preds")
        os.makedirs(d, exist_ok=True)
        np.savez_compressed(os.path.join(d, f"{run_name}.npz"), **saved)
        with open(os.path.join(d, f"{run_name}.metrics.json"), "w") as f:
            json.dump(full, f)
        with open(LEADERBOARD, "a") as f:
            f.write(json.dumps(row, default=float) + "\n")
    if not quiet:
        print(summary(row))
    return row


def summary(row) -> str:
    lines = [f"== {row['run_name']} [{row['track']}]"]
    for es, r in row["results"].items():
        o, w = r["overall"]["raw"], r["overall"]["weighted"]
        s = (f"  {es:<11} n={r['n']:<6} target={r['target']:<5} J={o['jaccard']:.3f} exact={o['exact']:.3f} "
             f"micro={o['micro_f1']:.3f} macro={(o['macro_f1'] or 0):.3f} | weighted J={w['jaccard']:.3f} "
             f"micro={w['micro_f1']:.3f} macro={(w['macro_f1'] or 0):.3f}")
        if "loo" in r:
            s += " | LOO J " + " ".join(
                f"{k}={v['raw']['jaccard']:.3f}" + (f"(llm {v['llm_reference']['who']} {v['llm_reference']['jaccard']:.3f})"
                                                    if "llm_reference" in v else "")
                for k, v in r["loo"].items())
        lines.append(s)
    return "\n".join(lines)


def read_leaderboard() -> list[dict]:
    if not os.path.exists(LEADERBOARD):
        return []
    with open(LEADERBOARD) as f:
        return [json.loads(l) for l in f if l.strip()]
