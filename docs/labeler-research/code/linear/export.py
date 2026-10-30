# Frozen research snapshot, copied unchanged below this line from ~/pharos-labeling/studies/DOTTEREL/scripts/export.py (2026-10-30).
# Not maintained and not run by tests; ~/pharos-labeling paths refer to the archive layout (see ../README.md).
"""Export a dexp.py --save_model linear model to the Go-loadable format (models/<name>/).
  python export.py <name> [--thresholds preds/<run>.<mode>.npz] [--segthr]
Files: model.json; <block>.tbl (little-endian records: u32 hash index, f32 idf, i32 col, i32 copycol x n_copies);
W.f32 ([F, 54] row-major float32); thresholds in model.json."""
import os, sys, json, argparse
HERE = os.path.dirname(os.path.abspath(__file__)); sys.path.insert(0, HERE)
sys.path.insert(0, os.path.expanduser("~/pharos-labeling/research/harness"))
import numpy as np, rlh, dexp, newblocks
D = os.path.expanduser("~/pharos-labeling/studies/DOTTEREL")
AF = dexp.AF; OF = dexp.OF
HASHED = {"w": ("text", 1 << 21), "x": ("context", 1 << 19), "h": ("text_head500", 1 << 19),
          "xu": ("ctx_user", 1 << 19), "xa": ("ctx_asst", 1 << 19), "xt": ("ctx_tail200", 1 << 19),
          "pos": ("pos", 1 << 19), "xi": ("inter", 1 << 20),
          "pos2": ("pos2", 1 << 19), "chH": ("charwb_human", 1 << 20)}
DENSE_N = {"meta2": 33, "rules": 34, "cue": 13, "seg": 9}

def keep_idf(b):
    d = AF if b in ("w", "x", "h") else OF
    return np.load(f"{d}/keep_{b}.npy"), np.load(f"{d}/idf_{b}.npy")

def main():
    ap = argparse.ArgumentParser(); ap.add_argument("name"); ap.add_argument("--thresholds", default="")
    a = ap.parse_args()
    z = np.load(f"{D}/models/{a.name}.npz", allow_pickle=True)
    spec = json.loads(str(z["spec"])); cols = [z[f"cols_{i}"] for i in range(len(spec))]
    W = z["W"].astype(np.float32); bvec = z["b"].astype(np.float32)
    out = f"{D}/models/{a.name}"; os.makedirs(out, exist_ok=True)
    # global column offsets in W, in spec order
    off = np.cumsum([0] + [len(c) for c in cols])
    assert off[-1] == W.shape[0], (off[-1], W.shape)
    blocks = []
    base = {s["block"]: i for i, s in enumerate(spec) if s["group"] is None}
    copies = {}
    for i, s in enumerate(spec):
        if s["group"] is not None: copies.setdefault(s["block"], []).append(i)
    for b, i in base.items():
        if b in HASHED:
            keep, idf = keep_idf(b)
            col = np.full(len(keep), -1, np.int32); col[cols[i]] = off[i] + np.arange(len(cols[i]))
            cc = []
            for ci in copies.get(b, []):
                c2 = np.full(len(keep), -1, np.int32); c2[cols[ci]] = off[ci] + np.arange(len(cols[ci])); cc.append(c2)
            rec = np.zeros(len(keep), dtype=[("h", "<u4"), ("idf", "<f4"), ("col", "<i4")] + [(f"c{k}", "<i4") for k in range(len(cc))])
            rec["h"] = keep; rec["idf"] = idf; rec["col"] = col
            for k, c2 in enumerate(cc): rec[f"c{k}"] = c2
            rec.tofile(f"{out}/{b}.tbl")
            blocks.append({"name": b, "kind": "hashed", "source": HASHED[b][0], "n_features": HASHED[b][1], "table": f"{b}.tbl",
                           "n_entries": int(len(keep)), "copy_groups": [spec[ci]["group"] for ci in copies.get(b, [])]})
        else:
            assert len(cols[i]) == DENSE_N[b], (b, len(cols[i]))
            assert not copies.get(b)
            blocks.append({"name": b, "kind": "dense", "n": DENSE_N[b], "col_offset": int(off[i])})
    W.tofile(f"{out}/W.f32")
    th = {}
    if a.thresholds:
        t = np.load(a.thresholds)
        if "thresholds" in t.files:  # harness v2 prediction file: global + optional per-segment thresholds
            th["default"] = t["thresholds"].tolist()
            th["by_segment"] = {k.split("__", 1)[1]: t[k].tolist() for k in t.files if k.startswith("thresholds__")}
        else:
            th["default"] = t["gold3_test__thresholds"].tolist()
    m = {"format": "dotterel-linear-v1", "name": a.name, "labels": rlh.label_list(), "parent": rlh.parent_index().tolist(),
         "n_features": int(W.shape[0]), "n_out": int(W.shape[1]), "bias": bvec.tolist(), "blocks": blocks,
         "groups": {g: v for g, v in dexp.GROUPS.items() if v is not None}, "seg_order": dexp.SEG_ORDER,
         "meta2_cols": json.load(open(f"{AF}/meta2_cols.json")), "thresholds": th,
         "contract": "see PARITY.md"}
    json.dump(m, open(f"{out}/model.json", "w"), indent=1)
    print("exported", out, W.shape, sum(os.path.getsize(f"{out}/{f}") for f in os.listdir(out)) / 1e6, "MB")

if __name__ == "__main__":
    main()
