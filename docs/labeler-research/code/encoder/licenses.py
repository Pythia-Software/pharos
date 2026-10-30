# Frozen research snapshot, copied unchanged below this line from ~/pharos-labeling/studies/KESTREL/scripts/licenses.py (2026-10-30).
# Not maintained and not run by tests; ~/pharos-labeling paths refer to the archive layout (see ../README.md).
"""data/licenses.csv: per public dataset its license, inclusion and item counts (prepared, labeled, by split); plus the private library rows."""
import os, sys
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import numpy as np, pandas as pd, kdata
K = kdata.K
it = pd.read_parquet(f"{K}/data/public_items.parquet")
z = np.load(f"{K}/data/labels_public/label_matrices.npz")
lab = pd.DataFrame({"item_id": z["item_ids"], "flash": z["flash_present"], "argon": z["argon_present"], "fa": z["y_fa_present"]})
m = it.merge(lab, on="item_id", how="left").fillna({"flash": False, "argon": False, "fa": False})
m["included"] = (m.excluded == "") & m.license.notna()
rows = []
for (src, hf), g in m.groupby(["source", "hf_id"], dropna=False):
    inc = g[g.included]; L = inc[inc.flash.astype(bool)]
    rows.append({"source": src, "dataset": hf, "license": g.license_raw.iloc[0], "status": "included" if len(inc) else "excluded",
                 "exclusion_reason": "" if len(inc) else (g.excluded.iloc[0] or "no permissive license"),
                 "items_prepared": len(g), "items_excluded": int((~g.included).sum()), "items_labeled_flash": int(len(L)),
                 "items_labeled_flash_argon": int(inc.fa.astype(bool).sum()),
                 **{f"labeled_{s}": int((L.kest_split == s).sum()) for s in ("train", "dev", "test")}})
df = pd.DataFrame(rows).sort_values(["status", "source", "items_labeled_flash"], ascending=[False, True, False])
tot = df[df.status == "included"][[c for c in df.columns if c.startswith("items") or c.startswith("labeled")]].sum()
print(df.groupby(["source", "license"]).items_labeled_flash.sum().to_string()); print(tot.to_string())
df.to_csv(f"{K}/data/licenses.csv", index=False)
