# Frozen research snapshot, copied unchanged below this line from ~/pharos-labeling/studies/calibrate_gannet.py (2026-10-30).
# Not maintained and not run by tests; ~/pharos-labeling paths refer to the archive layout (see ../README.md).
"""Director: the GANNET metric (student vs Argon beside Flash vs Argon) computed on the round-2 v2 held-out sets,
to calibrate what gap to expect with NO drift. Same thresholds convention as score_gannet.py."""
import os, sys
sys.path.insert(0, os.path.expanduser("~/pharos-labeling/research/harness"))
import numpy as np, pandas as pd, rlh
S = os.path.expanduser("~/pharos-labeling/studies")
m = np.load(f"{S}/AVOCET/data/v2/label_matrices.npz", allow_pickle=True)
ids = [str(x) for x in m["item_ids"]]; row = {k: i for i, k in enumerate(ids)}
seg = pd.read_parquet(f"{S}/AVOCET/data/segments.parquet", columns=["item_id", "segment", "split"]).set_index("item_id")
F, A = m["flash_closure"].astype(bool), m["argon_closure"].astype(bool)
both = m["flash_present"].astype(bool) & m["argon_present"].astype(bool)
items = rlh.load_items(columns=["item_id", "seen_in_train", "is_duplicate"]).set_index("item_id")
def J(P, Y):
    i = (P & Y).sum(1); u = (P | Y).sum(1); return np.where(u == 0, 1.0, i / np.maximum(u, 1))
for f in sys.argv[1:]:
    z = np.load(f, allow_pickle=True); pid = [str(x) for x in z["item_ids"]]; pr = z["probs"].astype(np.float32)
    r = np.array([row[i] for i in pid]); sg = seg.loc[pid, "segment"].values; sp = seg.loc[pid, "split"].values
    P = np.zeros((len(pid), 54), bool)
    for s in np.unique(sg):
        k = f"thresholds__{s}"; th = z[k] if k in z.files else z["thresholds"]; mk = sg == s
        P[mk] = rlh.apply_thresholds(pr[mk], th).astype(bool)
    ok = both[r] & np.isin(sp, ["test", "fresh"]) & ~items.loc[pid, "seen_in_train"].values.astype(bool) & ~items.loc[pid, "is_duplicate"].values.astype(bool)
    sA, fA, sFA = J(P, A[r]), J(F[r], A[r]), J(P, F[r] & A[r])
    print(os.path.basename(f))
    for g in ["H-typed", "H-typed+ctx", "AIUR", "AR", "DEL"]:
        x = ok & (sg == g)
        print(f"  {g:12s} n={x.sum():5d} student_vs_argon={sA[x].mean():.3f} flash_vs_argon={fA[x].mean():.3f} delta={sA[x].mean()-fA[x].mean():+.3f} student_vs_FA={sFA[x].mean():.3f}")
