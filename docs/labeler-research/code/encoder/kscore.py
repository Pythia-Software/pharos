# Frozen research snapshot, copied unchanged below this line from ~/pharos-labeling/studies/KESTREL/scripts/kscore.py (2026-10-30).
# Not maintained and not run by tests; ~/pharos-labeling paths refer to the archive layout (see ../README.md).
"""KESTREL scoring: thresholds tuned per segment on mixed dev, metrics with family-cluster bootstrap CIs, paired comparisons.

Scorer = mean(y3 head, y_hf head) for every model. Mixed dev for thresholds:
  library v2 dev y3 sets (target y3), EGRET TH dev with Argon (target F∩A), public dev with both teachers (target F∩A),
  minus any excluded (held-out) public source. Per segment: its own dev rows if >= 150, else the global thresholds.
Eval targets: F∩A everywhere (library: on the v2 y3 held-out sets, with Haiku's own F∩A score beside it; TH and public:
items with both Flash and Argon). dF = J(student, Argon) - J(Flash, Argon) (does it predict Argon as well as Flash does?).

usage:
  kscore.py table RUN [RUN ...] [--excl S]             per-group metrics -> results/table_<RUN>[__xS].csv
  kscore.py pair RUN_A RUN_B [--excl S] [--groups ...] paired family bootstrap of A - B on the same items
RUN: k:<kestrel run> | <BOBOLINK run> (library probs from BOBOLINK models, public/TH from preds_raw/)."""
import json, os, sys, functools
HERE = os.path.dirname(os.path.abspath(__file__)); sys.path.insert(0, HERE)
sys.path.insert(0, os.path.expanduser("~/pharos-labeling/studies/AVOCET/harness"))
import numpy as np, pandas as pd
import kdata, rlh, rlh2
K, ST = kdata.K, kdata.ST
LIBSEG = ["H-typed", "H-typed+ctx", "H-template", "H-resent", "H-feedback", "AIUR", "AR", "DEL"]
N_BOOT = 1000


def J(P, Y):
    return rlh._jaccard_rows(P.astype(bool), Y.astype(bool))


@functools.lru_cache(None)
def truth():
    """Per population: DataFrame(item_id, segment, split, family, source) + matrices FA, A, F, HFA(lib: haiku closure), has_fa, Ydev."""
    out = {}
    # library: v2 matrices
    T, _ = kdata.library(need_text=False)
    z = np.load(f"{ST}/AVOCET/data/v2/label_matrices.npz")
    lib = pd.DataFrame({"item_id": T.item_id, "segment": T.segment, "split": T.split, "family": T.family, "source": "lib"})
    out["lib"] = (lib, {"FA": z["flash_closure"] & z["argon_closure"], "A": z["argon_closure"], "F": z["flash_closure"],
                        "H": z["haiku_closure"], "Ydev": z["y3"], "has_fa": z["y3_present"].astype(bool)})
    Tt, _ = kdata.thinking(need_text=False)
    e = np.load(f"{ST}/EGRET/data/label_matrices.npz")
    th = pd.DataFrame({"item_id": Tt.item_id, "segment": "TH", "split": Tt.split, "family": Tt.family, "source": "th"})
    out["th"] = (th, {"FA": e["y_fa"], "A": e["argon_closure"], "F": e["flash_closure"], "Ydev": e["y_fa"],
                      "has_fa": e["y_fa_present"].astype(bool)})
    if os.path.exists(f"{K}/data/labels_public/label_matrices.npz"):
        Tp, Hp = kdata.public(need_text=False)
        z = np.load(f"{K}/data/labels_public/label_matrices.npz")
        rows = pd.Series(np.arange(len(z["item_ids"])), index=z["item_ids"]).loc[Tp.item_id].to_numpy()
        pub = pd.DataFrame({"item_id": Tp.item_id, "segment": Tp.segment, "split": Tp.split, "family": Tp.family, "source": Tp["pop"]})
        out["pub"] = (pub, {"FA": z["y_fa"][rows], "A": z["argon_closure"][rows], "F": z["flash_closure"][rows],
                            "Ydev": z["y_fa"][rows], "has_fa": z["y_fa_present"][rows].astype(bool)})
    return out


def _scorer(probs, heads=None):
    if probs.ndim == 2: return probs.astype(np.float32)  # already final probabilities (linear models)
    heads = list(heads); return 0.5 * (probs[:, heads.index("y3")].astype(np.float32) + probs[:, heads.index("y_hf")].astype(np.float32))


@functools.lru_cache(None)
def load_run(run):
    """{pop: Series item_id -> row, P [n,54]} for pops lib, th, pub. A '@pubdev' suffix only changes thresholds (public dev only)."""
    if "@" in run:
        return load_run(run.split("@")[0])
    res = {}
    if run.startswith("k:"):
        z = np.load(f"{K}/models/{run[2:]}/probs.npz"); P = _scorer(z["probs"], z["heads"]); pop = z["pop"]
        for name, m in (("lib", pop == "lib"), ("th", pop == "th"), ("pub", np.isin(pop, kdata.SOURCES))):
            res[name] = (z["item_id"][m], P[m])
        if not len(res["pub"][0]) and os.path.exists(f"{K}/preds_raw/k-{run[2:]}/public.npz"):  # trained before public labels existed
            g = np.load(f"{K}/preds_raw/k-{run[2:]}/public.npz"); res["pub"] = (g["item_id"], _scorer(g["probs"], g["heads"]))
        for extra in ("gannet",):
            f = f"{K}/preds_raw/k-{run[2:]}/{extra}.npz"
            if os.path.exists(f):
                g = np.load(f); res[extra] = (g["item_id"], _scorer(g["probs"], g["heads"]))
        return res
    if run.startswith("lin-"):  # linear models: preds_raw/<run>/{lib,public,th}.npz with item_id, probs [n,54]
        for name, f in (("lib", "lib"), ("th", "th"), ("pub", "public"), ("gannet", "gannet")):
            p = f"{K}/preds_raw/{run}/{f}.npz"
            if os.path.exists(p):
                z = np.load(p); res[name] = (z["item_id"], _scorer(z["probs"]))
        return res
    raw = f"{K}/preds_raw/{run}"
    if run.startswith("bug:"):  # baseline with the identity-sender framing bug (preds_raw/_senderbug/), for the bug's impact
        run = run[4:]; raw = f"{K}/preds_raw/_senderbug/{run}"
    ids, Ps = [], []
    for sp in ("dev", "test", "fresh"):
        z = np.load(f"{ST}/BOBOLINK/models/{run}/{sp}_probs.npz"); ids.append(z["item_ids"]); Ps.append(_scorer(z["probs"], z["heads"]))
    res["lib"] = (np.concatenate(ids), np.concatenate(Ps))
    for name, f in (("th", "th"), ("pub", "public"), ("gannet", "gannet")):
        p = f"{raw}/{f}.npz"
        if os.path.exists(p):
            z = np.load(p); res[name] = (z["item_id"], _scorer(z["probs"], z["heads"] if "heads" in z.files else None))
    return res


def aligned(run, pop, item_ids):
    ids, P = load_run(run)[pop]
    pos = pd.Series(np.arange(len(ids)), index=ids)
    return P[pos.loc[list(item_ids)].to_numpy()]


@functools.lru_cache(None)
def dev_rows(excl=()):
    """List of (pop, idx into truth table) dev rows for threshold tuning."""
    tr = truth(); out = {}
    lib, M = tr["lib"]
    ids = set(rlh2.evalset("ALL", "y3", "dev", "v2"))
    out["lib"] = np.flatnonzero(lib.item_id.isin(ids).to_numpy())
    th, M = tr["th"]; out["th"] = np.flatnonzero((th.split == "dev").to_numpy() & M["has_fa"])
    if "pub" in tr:
        pub, M = tr["pub"]
        out["pub"] = np.flatnonzero((pub.split == "dev").to_numpy() & M["has_fa"] & ~pub.source.isin(list(excl)).to_numpy())
    return out


def thresholds(run, excl=()):
    excl = tuple(sorted(excl))
    lp = f"{K}/data/labels_public/label_matrices.npz"; tag = int(os.path.getmtime(lp)) if os.path.exists(lp) else 0
    f = f"{K}/results/thr/{run.replace(':', '-').replace('@', '_')}__x{''.join(excl) or 'none'}__L{tag}.npz"
    if os.path.exists(f):
        z = np.load(f); return {k: z[k] for k in z.files}
    tr = truth(); P, Y, S = [], [], []
    for pop, idx in dev_rows(excl).items():
        if pop not in load_run(run): continue
        if run.endswith("@pubdev") and pop != "pub": continue
        t, M = tr[pop]
        P.append(aligned(run, pop, t.item_id.to_numpy()[idx])); Y.append(M["Ydev"][idx]); S.append(t.segment.to_numpy()[idx])
    P, Y, S = np.concatenate(P), np.concatenate(Y), np.concatenate(S).astype(str)
    th = {"default": rlh.tune_thresholds(P, Y, mode="per_label_jaccard")}
    for s in LIBSEG + ["TH"]:
        m = S == s
        if m.sum() >= 150:
            th[s] = rlh.tune_thresholds(P[m], Y[m], mode="per_label_jaccard")
    os.makedirs(os.path.dirname(f), exist_ok=True); np.savez(f, **th)
    return th


def binarize(P, segs, th):
    T = np.tile(np.asarray(th["default"], float), (len(P), 1))
    for s, t in th.items():
        if s != "default": T[segs == s] = t
    return rlh.close_binary((P >= T).astype(np.uint8))


def eval_sets():
    """(name, pop, idx) eval sets. Library: v2 y3 held-out sets; TH: test+fresh with both; public: per source x segment group,
    split test with both teachers ('test') and every labeled split ('all', used for LOSO)."""
    tr = truth(); out = []
    lib, _ = tr["lib"]
    for g in ["H-typed", "H-typed+ctx", "H-template", "AIUR", "AR", "DEL", "H-all", "ALL"]:
        ids = set(rlh2.evalset(g, "y3", "heldout", "v2")); out.append((f"lib/{g}", "lib", np.flatnonzero(lib.item_id.isin(ids).to_numpy())))
    th, M = tr["th"]; out.append(("th/TH", "th", np.flatnonzero(th.split.isin(["test", "fresh"]).to_numpy() & M["has_fa"])))
    if "pub" in tr:
        pub, M = tr["pub"]; seg = pub.segment.to_numpy(); src = pub.source.to_numpy(); sp = pub.split.to_numpy()
        groups = {"H-typed": ["H-typed"], "H-typed+ctx": ["H-typed+ctx"], "H-all": kdata.HUMAN, "AR": ["AR"], "DEL": ["DEL"], "TH": ["TH"]}
        for part, pm in (("test", sp == "test"), ("all", np.ones(len(sp), bool))):
            for s in kdata.SOURCES + ["ALL", "AGENT"]:
                sm = np.ones(len(src), bool) if s == "ALL" else np.isin(src, ["AT", "PI", "OH"]) if s == "AGENT" else src == s
                for g, segs in groups.items():
                    idx = np.flatnonzero(pm & sm & M["has_fa"] & np.isin(seg, segs))
                    if len(idx) >= 20: out.append((f"pub-{part}/{s}/{g}", "pub", idx))
    return out


def per_item(run, excl=()):
    """{set name: dict(sFA, dF, hFA(lib), fam)} per-item scores for one run."""
    tr = truth(); th = thresholds(run, excl); res = {}
    for name, pop, idx in eval_sets():
        if pop not in load_run(run): continue
        t, M = tr[pop]
        P = aligned(run, pop, t.item_id.to_numpy()[idx]); B = binarize(P, t.segment.to_numpy()[idx].astype(str), th)
        FA, A, F = M["FA"][idx], M["A"][idx], M["F"][idx]
        r = {"sFA": J(B, FA), "dF": J(B, A) - J(F, A), "FvA": J(F, A), "fam": t.family.to_numpy()[idx].astype(str), "n_pred": B.sum(1)}
        if pop == "lib":
            r["hFA"] = J(M["H"][idx], FA)
        res[name] = r
    return res


def fam_boot(d, fam, seed=0):
    uf, inv = np.unique(fam, return_inverse=True)
    s = np.bincount(inv, weights=d, minlength=len(uf)); c = np.bincount(inv, minlength=len(uf))
    rng = np.random.default_rng(seed); w = rng.multinomial(len(uf), np.ones(len(uf)) / len(uf), size=N_BOOT)
    bs = (w @ s) / (w @ c)
    return float(np.percentile(bs, 2.5)), float(np.percentile(bs, 97.5))


def table(run, excl=()):
    rows = []
    for name, r in per_item(run, excl).items():
        lo, hi = fam_boot(r["sFA"], r["fam"]); dlo, dhi = fam_boot(r["dF"], r["fam"])
        row = {"run": run, "excl": "".join(excl), "set": name, "n": len(r["sFA"]), "families": len(np.unique(r["fam"])),
               "J_FA": round(r["sFA"].mean(), 4), "ci": f"[{lo:.3f},{hi:.3f}]", "dF": round(r["dF"].mean(), 4),
               "dF_ci": f"[{dlo:+.3f},{dhi:+.3f}]", "FvA": round(r["FvA"].mean(), 4), "pred_labels": round(r["n_pred"].mean(), 2)}
        if "hFA" in r:
            d = r["sFA"] - r["hFA"]; a, b = fam_boot(d, r["fam"])
            row.update(haiku_FA=round(r["hFA"].mean(), 4), d_vs_haiku=round(d.mean(), 4), d_vs_haiku_ci=f"[{a:+.3f},{b:+.3f}]")
        rows.append(row)
    return pd.DataFrame(rows)


def pair(ra, rb, excl=(), excl_b=None, sets=None):
    A, Bm = per_item(ra, excl), per_item(rb, excl if excl_b is None else excl_b); rows = []
    for name in A:
        if name not in Bm or (sets and not any(s in name for s in sets)): continue
        d = A[name]["sFA"] - Bm[name]["sFA"]; lo, hi = fam_boot(d, A[name]["fam"])
        rows.append({"A": ra, "B": rb, "excl": "".join(excl), "set": name, "n": len(d), "A_FA": round(A[name]["sFA"].mean(), 4),
                     "B_FA": round(Bm[name]["sFA"].mean(), 4), "delta": round(d.mean(), 4), "ci": f"[{lo:+.3f},{hi:+.3f}]"})
    return pd.DataFrame(rows)


if __name__ == "__main__":
    import argparse
    ap = argparse.ArgumentParser(); ap.add_argument("cmd"); ap.add_argument("runs", nargs="+"); ap.add_argument("--excl", default="")
    ap.add_argument("--sets", default=""); ap.add_argument("--out", default="")
    a = ap.parse_args(); ex = tuple(s for s in a.excl.split(",") if s); pd.set_option("display.width", 250); pd.set_option("display.max_rows", 500)
    os.makedirs(f"{K}/results", exist_ok=True)
    if a.cmd == "table":
        df = pd.concat([table(r, ex) for r in a.runs])
    else:
        df = pair(a.runs[0], a.runs[1], ex, sets=[s for s in a.sets.split(",") if s] or None)
    if a.sets and a.cmd == "table":
        df = df[df.set.str.contains("|".join(a.sets.split(",")), regex=True)]
    print(df.to_string(index=False))
    if a.out: df.to_csv(a.out, index=False)
