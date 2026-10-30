# Frozen research snapshot, copied unchanged below this line from ~/pharos-labeling/studies/score_gannet.py (2026-10-30).
# Not maintained and not run by tests; ~/pharos-labeling paths refer to the archive layout (see ../README.md).
"""Director: score frozen models ONCE on the GANNET untouched holdout (post-Oct-27 families).
No Haiku labels on GANNET, so the fair comparison is: student vs Argon, beside Flash vs Argon
(does the student predict Argon as well as Flash does?), and student vs Flash∩Argon.
usage: score_gannet.py PRED.npz [PRED.npz ...]"""
import os, sys, json
sys.path.insert(0, os.path.expanduser("~/pharos-labeling/research/harness"))
import numpy as np, pandas as pd, rlh
G = os.path.expanduser("~/pharos-labeling/studies/GANNET/HOLDOUT-DO-NOT-TRAIN/data")
m = np.load(f"{G}/label_matrices.npz", allow_pickle=True)
assert list(m["label_order"]) == rlh.label_list()
items = pd.read_parquet(f"{G}/items.parquet", columns=["item_id", "segment", "family_id", "seen_in_train", "is_duplicate"]).set_index("item_id")
ids = [str(x) for x in m["item_ids"]]; row = {k: i for i, k in enumerate(ids)}
F, A = m["flash_closure"].astype(bool), m["argon_closure"].astype(bool)
both = m["flash_present"].astype(bool) & m["argon_present"].astype(bool)

def J(P, Y):
    i = (P & Y).sum(1); u = (P | Y).sum(1); return np.where(u == 0, 1.0, i / np.maximum(u, 1))

def load_pred(path):
    z = np.load(path, allow_pickle=True); pid = [str(x) for x in z["item_ids"]]; probs = z["probs"].astype(np.float32)
    seg_of = items["segment"]; th_g = z["thresholds"] if "thresholds" in z.files else None
    P = np.zeros((len(ids), 54), bool); have = np.zeros(len(ids), bool)
    for k, i in enumerate(pid):
        if i not in row: continue
        s = seg_of.get(i); key = f"thresholds__{s}"
        th = z[key] if key in z.files else th_g
        P[row[i]] = rlh.apply_thresholds(probs[k:k+1], th)[0].astype(bool); have[row[i]] = True
    return P, have

def report(name, P, have):
    keep = both & have & ~items.loc[ids, "is_duplicate"].fillna(False).values.astype(bool)
    seg = items.loc[ids, "segment"].values; fam = items.loc[ids, "family_id"].values
    s_A, f_A, s_FA = J(P, A), J(F, A), J(P, F & A)
    rng = np.random.default_rng(0); out = []
    groups = [("ALL", np.ones(len(ids), bool))] + [(g, seg == g) for g in ["H-typed", "H-typed+ctx", "H-template", "AIUR", "AR", "DEL", "TH"]]
    for g, msk in groups:
        x = keep & msk
        if x.sum() < 10: continue
        d = s_A[x] - f_A[x]; fx = fam[x]; uf = np.unique(fx); idx = {f: np.where(fx == f)[0] for f in uf}
        bs = [np.mean(np.concatenate([d[idx[f]] for f in rng.choice(uf, len(uf))])) for _ in range(1000)]
        out.append(dict(model=name, group=g, n=int(x.sum()), families=len(uf), student_vs_argon=round(s_A[x].mean(), 3),
                        flash_vs_argon=round(f_A[x].mean(), 3), delta=round(d.mean(), 3),
                        ci=f"[{np.percentile(bs,2.5):+.3f},{np.percentile(bs,97.5):+.3f}]", student_vs_FA=round(s_FA[x].mean(), 3)))
    return out

rows = []
for p in sys.argv[1:]:
    P, have = load_pred(p); rows += report(os.path.basename(p).replace(".npz", ""), P, have)
df = pd.DataFrame(rows); pd.set_option("display.width", 220); print(df.to_string(index=False))
out = os.path.expanduser("~/pharos-labeling/studies/GANNET/director_scores.csv"); df.to_csv(out, index=False); print("wrote", out)
