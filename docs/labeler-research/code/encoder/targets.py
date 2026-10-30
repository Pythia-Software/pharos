# Frozen research snapshot, copied unchanged below this line from ~/pharos-labeling/studies/BOBOLINK/scripts/targets.py (2026-10-30).
# Not maintained and not run by tests; ~/pharos-labeling paths refer to the archive layout (see ../README.md).
"""Training targets. v1 = round-1 matrices (rlh); v2 = AVOCET data v2 label matrices (same keys/row order)."""
import os, sys
import numpy as np
sys.path.insert(0, os.path.expanduser("~/pharos-labeling/research/harness")); import rlh
V2 = os.path.expanduser("~/pharos-labeling/studies/AVOCET/data/v2/label_matrices.npz")
TEACHERS = ["haiku", "flash", "argon"]


def load_targets(version, heads, M):
    if version == "v2":
        z = np.load(V2); M = {k: z[k] for k in z.files}
        assert (M["item_ids"] == rlh._matrices()["item_ids"]).all()
    elif version != "v1":
        raise ValueError(version)
    HY, HP, HM = {}, {}, {}
    for h in heads:
        if h in TEACHERS:
            HY[h], HP[h], HM[h] = M[f"{h}_closure"], M[f"{h}_present"].astype(bool), M[f"{h}_masked"]
        else:
            HY[h], HP[h], HM[h] = M[h], M[h + "_present"].astype(bool), None
    return HY, HP, HM
