# Frozen research snapshot, copied unchanged below this line from ~/pharos-labeling/studies/DOTTEREL/scripts/dexp.py (2026-10-30).
# Not maintained and not run by tests; ~/pharos-labeling paths refer to the archive layout (see ../README.md).
"""DOTTEREL experiment runner (track A pipeline, modified).
  python dexp.py --name D-x --blocks w,x,h,meta2,rules,seg [--copies w@H,x@H] [--target inter_avail_w]
                 [--train_groups H] [--wgroup HT=3] [--maxcols 200000]
Saves probs for every non-train item to probs/<name>.npz (float16, item_ids) + meta json.
Thresholds/scoring: dthr.py."""
import os, sys, time, json, argparse, resource
HERE = os.path.dirname(os.path.abspath(__file__)); sys.path.insert(0, HERE)
sys.path.insert(0, os.path.expanduser("~/pharos-labeling/research/harness"))
import numpy as np, pandas as pd, scipy.sparse as sp, rlh
import dscore
AF = os.path.expanduser("~/pharos-labeling/research/tracks/A-classic/feats")
OF = os.path.expanduser("~/pharos-labeling/studies/DOTTEREL/data/feats")
D = os.path.expanduser("~/pharos-labeling/studies/DOTTEREL")
GROUPS = {"H": ["H-typed", "H-typed+ctx", "H-resent", "H-template", "H-feedback", "H-other"], "HT": ["H-typed"],
          "HX": ["H-typed", "H-typed+ctx"], "HO": ["H-typed+ctx", "H-resent", "H-template", "H-feedback", "H-other"],
          "M": ["AR", "AIUR", "DEL"], "AR": ["AR"], "AIUR": ["AIUR"], "DEL": ["DEL"], "ALL": None}
SEG_ORDER = ["H-typed", "H-typed+ctx", "H-resent", "H-template", "H-feedback", "H-other", "AIUR", "AR", "DEL"]

def maxrss(): return resource.getrusage(resource.RUSAGE_SELF).ru_maxrss / 1e9
_ids = None
def all_ids():
    global _ids
    if _ids is None: _ids = pd.read_parquet(f"{AF}/items.parquet", columns=["item_id"]).item_id.to_numpy()
    return _ids
def rowmap(): return pd.Series(np.arange(len(all_ids())), index=all_ids())
def seg_rows():
    return dscore.segmap().reindex(all_ids()).fillna("?").to_numpy()
def group_mask(g):
    s = seg_rows(); return np.ones(len(s), bool) if GROUPS[g] is None else np.isin(s, GROUPS[g])

def block(b):
    if b in ("w", "x", "h", "c"): return sp.load_npz(f"{AF}/X_{b}.npz").tocsr()
    if b in ("meta2",): return sp.csr_matrix(np.load(f"{AF}/X_meta2.npy"))
    if b == "rules": return sp.csr_matrix(np.load(f"{AF}/X_rules.npy"))
    if b == "cue": return sp.csr_matrix(np.load(f"{OF}/X_cue.npy"))
    if b == "seg":
        s = seg_rows(); return sp.csr_matrix(np.stack([(s == x) for x in SEG_ORDER], 1).astype(np.float32))
    if os.path.exists(f"{OF}/X_{b}.npz"): return sp.load_npz(f"{OF}/X_{b}.npz").tocsr()
    raise ValueError(b)

def build_X(blocks, copies, maxcols, scale):
    tr = rowmap().loc[rlh.evalset("train")].to_numpy()
    mats, spec = [], []
    base = []
    for b in blocks:
        M = block(b).astype(np.float32)
        if b in scale: M = M * np.float32(scale[b])
        base.append((b, M))
    if maxcols:  # top-K columns by train df over the sparse text blocks (dense blocks always kept)
        dense = {"meta2", "rules", "cue", "seg", "embH", "embA", "chH"}
        sparse_b = [(b, M) for b, M in base if b not in dense]
        dfs = np.concatenate([np.bincount(M[tr].indices, minlength=M.shape[1]) for b, M in sparse_b])
        top = np.zeros(len(dfs), bool); top[np.argsort(-dfs)[:maxcols]] = True
        off = 0; nb = []
        for b, M in base:
            if b in dense: nb.append((b, M, np.arange(M.shape[1]))); continue
            k = np.flatnonzero(top[off:off + M.shape[1]]); off += M.shape[1]
            nb.append((b, M[:, k].tocsr(), k))
    else:
        nb = [(b, M, np.arange(M.shape[1])) for b, M in base]
    bmap = {b: (M, k) for b, M, k in nb}
    for b, M, k in nb: mats.append(M); spec.append({"block": b, "cols": k, "group": None})
    for c in copies:  # group-specific copy of a block (feature augmentation): rows outside the group are zero
        b, g = c.split("@")
        M, k = bmap[b] if b in bmap else (block(b).astype(np.float32), None)
        m = group_mask(g)
        Mg = sp.diags(m.astype(np.float32)) @ M
        gtr = np.intersect1d(tr, np.flatnonzero(m))
        dfc = np.bincount(Mg[gtr].indices, minlength=Mg.shape[1])
        kk = np.flatnonzero(dfc >= 2)
        mats.append(Mg[:, kk].tocsr())
        spec.append({"block": b, "cols": (k[kk] if k is not None else kk), "group": g})
    X = sp.hstack(mats, format="csr", dtype=np.float32)
    return X, spec

C68 = os.path.expanduser("~/pharos-labeling/research/tracks/C-finetune/corpus/c-e68-512-all.npz")
def c68(ids):
    z = np.load(C68); pos = pd.Series(np.arange(len(z["item_ids"])), index=z["item_ids"])
    r = pos.loc[list(ids)].to_numpy(); heads = list(z["heads"])
    P = z["probs"][r].astype(np.float32)
    return {h: P[:, k] for k, h in enumerate(heads)} | {"y3s": z["y3_probs"][r].astype(np.float32)}

LABELS = os.environ.get("DOT_LABELS", "v1")
def _tm(name, ids):
    if LABELS == "v1": return rlh.target_matrix(name, ids)
    sys.path.insert(0, os.path.expanduser("~/pharos-labeling/studies/AVOCET/harness")); import rlh2
    return rlh2.target_matrix(name, ids, LABELS)
def _nt(ids):
    if LABELS == "v1": return rlh.n_teachers(ids)
    import rlh2
    return rlh2._matrices(LABELS)["n_teachers"][rlh.rows(ids)]

def build_target(target, ids):
    ids = np.array(ids)
    if target in ("flash", "haiku", "y3", "y_hf"):
        Y, has = _tm(target, ids); return ids[has], Y[has].astype(np.float32), None
    V, has = _tm("votes", ids)
    n = _nt(ids).astype(np.float32)
    ids, V, n = ids[has], V[has].astype(np.float32), n[has]
    ia = (V >= n[:, None]).astype(np.float32)
    w1 = np.where(n >= 2, 1.0, 0.5).astype(np.float32)
    if target == "inter_avail": return ids, ia, None
    if target == "inter_avail_w": return ids, ia, w1
    if target == "soft_nw": return ids, V / n[:, None], n
    if target == "maj":  # 3 teachers: >= 2 votes; 2 teachers: intersection; 1 teacher: its labels (x0.5)
        return ids, np.where(n[:, None] >= 3, V >= 2, V >= n[:, None]).astype(np.float32), w1
    if target == "majH":  # majority on human rows only, intersection elsewhere
        hum = group_mask("H")[rowmap().loc[ids].to_numpy()]
        Y = ia.copy(); Y[hum] = np.where(n[hum, None] >= 3, V[hum] >= 2, V[hum] >= n[hum, None]); return ids, Y.astype(np.float32), w1
    C = c68(ids)
    one = n < 2
    if target == "c68cpl":  # consensus where >=2 teachers; Flash AND closed C68-haiku head on Flash-only rows
        Y = ia.copy(); Ch = rlh.close_binary(C["haiku"] >= 0.5).astype(np.float32); Y[one] = ia[one] * Ch[one]; return ids, Y, None
    if target == "c68hf": return ids, C["y_hf"], None  # soft distillation: C68's Haiku∩Flash head
    if target == "c68mix": return ids, 0.5 * ia + 0.5 * C["y_hf"], w1
    if target == "c68mixcpl":
        Y = ia.copy(); Ch = rlh.close_binary(C["haiku"] >= 0.5).astype(np.float32); Y[one] = ia[one] * Ch[one]
        return ids, 0.5 * Y + 0.5 * C["y_hf"], None
    if target == "hyb":  # human rows: consensus (single-teacher rows x0.5); machine rows: c68cpl pseudo-labels
        Y = ia.copy(); Ch = rlh.close_binary(C["haiku"] >= 0.5).astype(np.float32); Y[one] = ia[one] * Ch[one]
        hum = group_mask("H")[rowmap().loc[ids].to_numpy()]
        Y[hum] = ia[hum]; w = np.where(hum, w1, 1.0).astype(np.float32)
        return ids, Y, w
    if target == "hybmaj":  # human rows: majority of 3 (else intersection, 1-teacher x0.5); machine rows: c68cpl
        Y = ia.copy(); Ch = rlh.close_binary(C["haiku"] >= 0.5).astype(np.float32); Y[one] = ia[one] * Ch[one]
        hum = group_mask("H")[rowmap().loc[ids].to_numpy()]
        Y[hum] = np.where(n[hum, None] >= 3, V[hum] >= 2, V[hum] >= n[hum, None]); w = np.where(hum, w1, 1.0).astype(np.float32)
        return ids, Y.astype(np.float32), w
    raise ValueError(target)

def nontrain_ids():
    it = rlh.load_items(columns=["item_id", "split"]); return it.item_id[it.split != "train"].to_numpy()

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--name", required=True); ap.add_argument("--blocks", default="w,x,h,meta2,rules")
    ap.add_argument("--copies", default=""); ap.add_argument("--target", default="inter_avail_w")
    ap.add_argument("--epochs", type=int, default=3); ap.add_argument("--lr", type=float, default=0.1)
    ap.add_argument("--batch", type=int, default=2048); ap.add_argument("--l2", type=float, default=3e-6)
    ap.add_argument("--hidden", type=int, default=0); ap.add_argument("--maxcols", type=int, default=200000)
    ap.add_argument("--train_groups", default="ALL"); ap.add_argument("--wgroup", default="")
    ap.add_argument("--scale", default=""); ap.add_argument("--threads", type=int, default=10)
    ap.add_argument("--seed", type=int, default=0); ap.add_argument("--desc", default="")
    ap.add_argument("--save_model", type=int, default=1)
    a = ap.parse_args()
    import torch, lin; torch.set_num_threads(a.threads)
    t0 = time.time()
    scale = {kv.split("=")[0]: float(kv.split("=")[1]) for kv in a.scale.split(",") if kv}
    X, spec = build_X(a.blocks.split(","), [c for c in a.copies.split(",") if c], a.maxcols, scale)
    print("features", X.shape, X.nnz, "%.0fs rss %.1f" % (time.time() - t0, maxrss()), flush=True)
    rm = rowmap()
    tr_ids = np.array(rlh.evalset("train"))
    gm = np.zeros(len(all_ids()), bool)
    for g in a.train_groups.split("+"): gm |= group_mask(g)
    tr_ids = tr_ids[gm[rm.loc[tr_ids].to_numpy()]]
    ids, Y, w = build_target(a.target, tr_ids)
    if a.wgroup:
        w = np.ones(len(ids), np.float32) if w is None else w.copy()
        r = rm.loc[ids].to_numpy()
        for kv in a.wgroup.split(","):
            g, f = kv.split("="); w[group_mask(g)[r]] *= float(f)
    Xtr = X[rm.loc[ids].to_numpy()]
    dev = rlh.evalset("gold3_dev"); Ydev, _ = rlh.target_matrix("y3", dev); Xdev = X[rm.loc[dev].to_numpy()]
    def quick(P):
        th = rlh.tune_thresholds(P, Ydev, mode="global_jaccard"); return round(float(rlh.score(rlh.apply_thresholds(P, th), Ydev)["jaccard"]), 4)
    print("train rows", len(ids), flush=True)
    t1 = time.time()
    net = lin.fit(Xtr, Y, None, w, hidden=a.hidden, epochs=a.epochs, lr=a.lr, batch=a.batch, l2=a.l2, seed=a.seed, Xdev=Xdev, dev_fn=quick)
    train_s = time.time() - t1; del Xtr
    nt = nontrain_ids()
    P = lin.predict(net, X[rm.loc[nt].to_numpy()])
    meta = {"name": a.name, "args": vars(a), "labels": LABELS, "n_train": int(len(ids)), "train_seconds": round(train_s, 1), "n_features": int(X.shape[1]),
            "peak_rss_gb": round(maxrss(), 2), "hist": net.hist}
    os.makedirs(f"{D}/probs", exist_ok=True)
    np.savez_compressed(f"{D}/probs/{a.name}.npz", item_ids=nt.astype(str), probs=P.astype(np.float16))
    json.dump(meta, open(f"{D}/probs/{a.name}.json", "w"), default=str)
    if a.save_model:
        os.makedirs(f"{D}/models", exist_ok=True)
        W = (net.W1 if a.hidden else net.W).detach().numpy(); b = (net.b1 if a.hidden else net.b).detach().numpy()
        extra = {"W2": net.out.weight.detach().numpy(), "b2": net.out.bias.detach().numpy()} if a.hidden else {}
        np.savez(f"{D}/models/{a.name}.npz", W=W, b=b, **extra,
                 spec=np.array(json.dumps([{**s, "cols": None} for s in spec])),
                 **{f"cols_{i}": s["cols"] for i, s in enumerate(spec)})
    print("done %s train %.0fs total %.0fs rss %.1f GB hist %s" % (a.name, train_s, time.time() - t0, maxrss(), net.hist), flush=True)


_block_orig = block
def block(b):
    if b in ("embH", "embA"):  # frozen potion-base-32M mean embeddings (text, context), human rows only / all rows
        E = os.path.expanduser("~/pharos-labeling/research/tracks/B-embeddings/emb")
        m = (dscore.segmap().reindex(all_ids()).fillna("?").str.startswith("H")).to_numpy() if b == "embH" else np.ones(len(all_ids()), bool)
        T = np.load(f"{E}/potion-base-32M_text.npy", mmap_mode="r"); Cx = np.load(f"{E}/potion-base-32M_ctx.npy", mmap_mode="r")
        r = np.flatnonzero(m); M = np.hstack([np.asarray(T[r], np.float32), np.asarray(Cx[r], np.float32)])
        return sp.csr_matrix((M.ravel(), np.tile(np.arange(M.shape[1]), len(r)), np.r_[0, np.cumsum(np.where(m, M.shape[1], 0))]), shape=(len(m), M.shape[1]))
    return _block_orig(b)

if __name__ == "__main__":
    main()
