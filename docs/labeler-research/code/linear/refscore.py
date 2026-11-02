# Frozen research snapshot, copied unchanged below this line from ~/pharos-labeling/studies/DOTTEREL/scripts/refscore.py (2026-10-30).
# Not maintained and not run by tests; ~/pharos-labeling paths refer to the archive layout (see ../README.md).
"""Python reference scorer for exported models: raw item fields -> probabilities, one item at a time
(the parity oracle the Go scorer must match). Also dumps parity fixtures.
  python refscore.py <model_dir> --ids <file|evalsets> --out <npz>"""
import os, sys, json, argparse, re, time
HERE = os.path.dirname(os.path.abspath(__file__)); sys.path.insert(0, HERE)
sys.path.insert(0, os.path.expanduser("~/pharos-labeling/research/tracks/A-classic"))
import numpy as np, pandas as pd
import dfeat, newblocks
import rules as R
from sklearn.utils import murmurhash3_32

def hidx(s, n):
    h = murmurhash3_32(s, seed=0)  # signed int32 (sklearn contract)
    return (2147483647 - (n - 1)) % n if h == -2147483648 else abs(h) % n

class Model:
    def __init__(self, d):
        self.m = json.load(open(f"{d}/model.json")); self.F = self.m["n_features"]
        self.W = np.fromfile(f"{d}/W.f32", dtype="<f4").reshape(self.F, self.m["n_out"]); self.b = np.array(self.m["bias"], np.float32)
        self.tab = {}
        for bl in self.m["blocks"]:
            if bl["kind"] != "hashed": continue
            nc = len(bl["copy_groups"])
            rec = np.fromfile(f"{d}/{bl['table']}", dtype=[("h", "<u4"), ("idf", "<f4"), ("col", "<i4")] + [(f"c{k}", "<i4") for k in range(nc)])
            self.tab[bl["name"]] = {int(r["h"]): (np.float32(r["idf"]), int(r["col"]), [int(r[f"c{k}"]) for k in range(nc)]) for r in rec}
        self.rx = [(re.compile(p, re.I | re.M), None if c is None else set(c)) for _, _, c, p in R.RULES]

    def strings(self, src, it, cache):
        t = it["text"]; c = it["context"] or ""
        if src == "text": return dfeat.word12(t)
        if src == "context": return dfeat.word12(c)
        if src == "text_head500": return dfeat.word12(t[:500])
        if src == "ctx_user": return dfeat.word12(dfeat.split_context(c)[0])
        if src == "ctx_asst": return dfeat.word12(dfeat.split_context(c)[1])
        if src == "ctx_tail200": return dfeat.word12(c[-200:])
        if src == "pos": return dfeat.pos_feats(cache["tt"])
        if src == "inter": return dfeat.inter_feats(cache["tt"], cache["ct"])
        if src == "pos2": return dfeat.pos2_feats(t, cache["tt"], cache["ct"])
        if src == "charwb_human": return dfeat.char_wb(t) if it["cohort"] == "human" else []
        raise ValueError(src)

    def meta2(self, it):
        t = it["text"]; c = it["context"] or ""; ch = it["chars"]; f = []
        f += [float(it["cohort"] == x) for x in ["human", "agent_reply", "agent_in_user_role", "delegation"]]
        f += [float(it["provider"] == x) for x in ["claude", "codex", "antigravity"]]
        f += [float(it["kind"] == x) for x in ["message", "delegation", "delegation_result"]]
        f.append(np.float32(min(max(it["depth"] or 0, 0), 4)) / np.float32(4))
        f.append(np.log1p(np.float32(ch)) / np.float32(9))
        f += [float(ch < b) for b in [10, 30, 100, 300, 1000, 3000]]
        cc, ci = it["chunk_count"], it["chunk_index"]
        f += [float(cc > 1), float(cc > 1 and ci == 0), float(cc > 1 and ci == cc - 1), float(cc > 1 and 0 < ci < cc - 1)]
        f += [float(len(c) == 0), float(bool(it["context_excerpt"])), float("Previous user request" in c), float(c.rstrip().endswith("?"))]
        f.append(np.float32(np.log1p(t.count("?"))) / np.float32(3)); f.append(float(t.rstrip().endswith("?")))
        f.append(np.float32(np.log1p(t.count("⟦"))) / np.float32(3)); f.append(np.float32(np.log1p(t.count("```"))) / np.float32(3))
        f.append(np.float32(np.log1p(t.count("\n"))) / np.float32(6))
        f.append(np.float32(np.log1p(len(re.findall(r"(?m)^\s*(?:[-*]|\d+\.)\s", t)))) / np.float32(4))
        f.append(float(t.lstrip()[:1] in ("{", "[")))
        return np.array(f, np.float32)

    def rules(self, it):
        t = it["text"][:3000]; co = it["cohort"]
        return np.array([1.0 if (cs is None or co in cs) and rx.search(t) else 0.0 for rx, cs in self.rx], np.float32)

    def features(self, it):
        """sparse feature vector as {col: value} (float32)."""
        x = {}
        cache = {"tt": dfeat.toks(it["text"]), "ct": dfeat.toks(it["context"] or "")}
        seg = it["segment"]
        ingroup = {g: seg in mem for g, mem in self.m["groups"].items()}
        for bl in self.m["blocks"]:
            if bl["kind"] == "hashed":
                tab = self.tab[bl["name"]]; cnt = {}
                for s in self.strings(bl["source"], it, cache):
                    k = hidx(s, bl["n_features"])
                    if k in tab: cnt[k] = cnt.get(k, 0) + 1
                vals = {k: np.float32(np.log1p(np.float32(v))) * tab[k][0] for k, v in cnt.items()}
                nrm = np.float32(np.sqrt(np.float32(sum(np.float32(v) * np.float32(v) for v in vals.values())))) if vals else np.float32(1)
                if nrm == 0: nrm = np.float32(1)
                inv = np.float32(1) / nrm
                for k, v in vals.items():
                    _, col, cc = tab[k]; val = np.float32(inv * v)
                    if col >= 0: x[col] = val
                    for g, c2 in zip(bl["copy_groups"], cc):
                        if c2 >= 0 and ingroup[g]: x[c2] = val
            else:
                n = bl["name"]
                if n == "meta2": v = self.meta2(it)
                elif n == "rules": v = self.rules(it)
                elif n == "cue": v = np.array(dfeat.cue_feats(cache["tt"], cache["ct"], it["text"]), np.float32)
                elif n == "seg": v = np.array([seg == s for s in self.m["seg_order"]], np.float32)
                for j, val in enumerate(v):
                    if val != 0: x[bl["col_offset"] + j] = np.float32(val)
        return x

    def logits(self, it):
        x = self.features(it)
        if not x: return self.b.astype(np.float64)
        cols = np.fromiter(x.keys(), int); vals = np.fromiter(x.values(), np.float64)
        return vals @ self.W[cols].astype(np.float64) + self.b

def load_items(ids):
    sys.path.insert(0, os.path.expanduser("~/pharos-labeling/research/harness")); import rlh, dscore
    it = rlh.load_items(columns=["item_id", "text", "context", "cohort", "provider", "kind", "depth", "chars", "chunk_index", "chunk_count", "context_excerpt"]).set_index("item_id").loc[ids]
    it["segment"] = dscore.segmap().reindex(it.index).fillna("?").values
    return it.reset_index()

if __name__ == "__main__":
    ap = argparse.ArgumentParser(); ap.add_argument("model"); ap.add_argument("--ids", required=True); ap.add_argument("--out", required=True)
    ap.add_argument("--limit", type=int, default=0)
    a = ap.parse_args()
    ids = [l.strip() for l in open(a.ids) if l.strip()]
    if a.limit: ids = ids[:a.limit]
    M = Model(a.model); df = load_items(ids)
    t0 = time.time(); Z = np.zeros((len(df), M.m["n_out"]))
    for i, r in enumerate(df.to_dict("records")): Z[i] = M.logits(r)
    print("scored", len(df), "%.1f items/s" % (len(df) / (time.time() - t0)))
    np.savez_compressed(a.out, item_ids=np.array(ids), logits=Z)
