# Frozen research snapshot, copied unchanged below this line from ~/pharos-labeling/research/director/segments.py (2026-10-30).
# Not maintained and not run by tests; ~/pharos-labeling paths refer to the archive layout (see ../README.md).
"""Per-segment scores on gold3_test (and gold2_test) by authorship origin. Director follow-up 2026-10-29."""
import json, os, sys
sys.path.insert(0, os.path.expanduser("~/pharos-labeling/research/harness"))
import numpy as np, pandas as pd, rlh
R = os.path.expanduser("~/pharos-labeling/research")
prov = {}
for job in sorted(os.listdir(f"{R}/data/exports")):
    p = f"{R}/data/exports/{job}/provenance.jsonl"
    if not os.path.exists(p): continue
    with open(p) as f:
        for l in f:
            r = json.loads(l)
            if r["item_id"] in prov: continue
            tgt = sorted({x.get("status") for x in r["pieces"] if x.get("role") == "target"})
            ctx = sorted({x.get("status") for x in r["pieces"] if x.get("role") == "context"})
            prov[r["item_id"]] = (r.get("authorship_reason") or "", tgt, ctx)
items = rlh.load_items(columns=["item_id", "cohort"]).set_index("item_id")

def seg(i):
    c = items.at[i, "cohort"]; reason, tgt, ctx = prov.get(i, ("", [], []))
    if c == "human":
        kind = "typed only" if tgt == ["estimated_typed"] else "incl. reused/template/feedback"
        return f"human: {kind}" + (" + quoted/pasted context" if ctx else "")
    if c == "agent_in_user_role":
        r = reason.lower()
        k = ("TL1 orchestration" if "tl1" in r else "launched by a command" if "launched" in r
             else "headless run (agy/codex/claude -p)" if ("headless" in r or "exec run" in r) else "sub-agent / other")
        return f"AIUR: {k}"
    return c

def J(P, Y):
    inter = (P & Y).sum(1); uni = (P | Y).sum(1)
    return np.where(uni == 0, 1.0, inter / np.maximum(uni, 1))

def run(evalset, model, target):
    z = np.load(f"{R}/tracks/C-finetune/preds/{model}.npz")
    ids = list(z[f"{evalset}__item_ids"])
    P = rlh.apply_thresholds(z[f"{evalset}__probs"], z[f"{evalset}__thresholds"]).astype(bool)
    Y, _ = rlh.target_matrix(target, ids); Y = Y.astype(bool)
    df = pd.DataFrame({"seg": [seg(i) for i in ids], "split": items.loc[ids].index.map(lambda i: i)})
    df["student"] = J(P, Y)
    if evalset.startswith("gold3"):
        H, _ = rlh.target_matrix("haiku", ids); F, _ = rlh.target_matrix("flash", ids); A, _ = rlh.target_matrix("argon", ids)
        H, F, A = H.astype(bool), F.astype(bool), A.astype(bool)
        df["student_vs_FA"] = J(P, F & A); df["haiku_vs_FA"] = J(H, F & A)
        df["student_vs_HA"] = J(P, H & A); df["flash_vs_HA"] = J(F, H & A)
    g = df.groupby("seg"); out = g.mean(numeric_only=True).round(3); out.insert(0, "n", g.size())
    return out

pd.set_option("display.width", 200)
if "--paired" not in sys.argv:
  for es, tgt in [("gold3_test", "y3"), ("gold2_test", "y_hf")]:
    for m in ["c-e68-512-all", "c-e32-512-all"]:
        print(f"\n== {es} vs {tgt}, model {m}"); print(run(es, m, tgt).to_string())

def paired(model="c-e68-512-all"):
    z = np.load(f"{R}/tracks/C-finetune/preds/{model}.npz"); ids = list(z["gold3_test__item_ids"])
    P = rlh.apply_thresholds(z["gold3_test__probs"], z["gold3_test__thresholds"]).astype(bool)
    H, _ = rlh.target_matrix("haiku", ids); F, _ = rlh.target_matrix("flash", ids); A, _ = rlh.target_matrix("argon", ids)
    H, F, A = H.astype(bool), F.astype(bool), A.astype(bool)
    d = J(P, F & A) - J(H, F & A); segs = np.array([seg(i) for i in ids]); coh = items.loc[ids, "cohort"].values
    rng = np.random.default_rng(0)
    for name, m in [("human (all)", coh == "human"), ("human typed only (± context)", np.char.startswith(segs.astype(str), "human: typed")),
                    ("agent_reply", coh == "agent_reply"), ("delegation", coh == "delegation"), ("AIUR", coh == "agent_in_user_role")]:
        x = d[m]; bs = [rng.choice(x, len(x)).mean() for _ in range(2000)]
        print(f"{model} {name:32s} n={m.sum():4d} student−Haiku vs F∩A = {x.mean():+.3f} (95% CI {np.percentile(bs,2.5):+.3f}..{np.percentile(bs,97.5):+.3f})")
if __name__ == "__main__" and "--paired" in sys.argv:
    paired(); paired("c-e32-512-all")
