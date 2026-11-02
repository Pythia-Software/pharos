# Frozen research snapshot, copied unchanged below this line from ~/pharos-labeling/studies/KESTREL/scripts/kdata.py (2026-10-30).
# Not maintained and not run by tests; ~/pharos-labeling paths refer to the archive layout (see ../README.md).
"""KESTREL unified data: library (round-1 items + AVOCET v2 labels), EGRET thinking (TH), public (IBIS dataset, KESTREL labels).
One table `T` (pandas) with: pop (lib|th|AT|PI|OH|WC), item_id, cohort, sender, context, text, segment, split, family,
is_dup, license; and head targets HY/HP/HM per head (y_hf, haiku, flash, argon, y3), row-aligned with T.
Targets: library = v2 matrices (y3 = three-teacher). TH and public: y3 = y_hf = F∩A where both, else Flash; flash/argon heads
= that teacher (masked labels excluded); no haiku. Public rows with an excluded license are dropped here."""
import os, sys
import numpy as np, pandas as pd
sys.path.insert(0, os.path.expanduser("~/pharos-labeling/research/harness"))
import rlh
ST = os.path.expanduser("~/pharos-labeling/studies")
K = f"{ST}/KESTREL"
HEADS = ["y_hf", "haiku", "flash", "argon", "y3"]
HUMAN = ["H-typed", "H-typed+ctx", "H-template", "H-resent", "H-feedback"]
SOURCES = ["AT", "PI", "OH", "WC"]
NL = 54


_ROLE = None


def sender_role(unit_ids):
    """units.sender_role for public/TH/GANNET units (data/sender_role.parquet). Round-1 `sender` == units.sender_role;
    the other item tables carry units.sender (identity), which must not be used for framing."""
    global _ROLE
    if _ROLE is None:
        _ROLE = pd.read_parquet(f"{K}/data/sender_role.parquet").set_index("unit_id").sender_role
    r = _ROLE.reindex(list(unit_ids))
    assert not r.isna().any(), f"{int(r.isna().sum())} units without sender_role"
    return r.to_numpy().astype(object)


def _empty(n):
    return {h: (np.zeros((n, NL), np.uint8), np.zeros(n, bool), np.zeros((n, NL), np.uint8)) for h in HEADS}


def library(need_text=True):
    cols = ["item_id", "cohort", "sender", "split", "family_id", "is_duplicate"] + (["context", "text"] if need_text else [])
    it = rlh.load_items(columns=cols)
    sg = pd.read_parquet(f"{ST}/AVOCET/data/segments.parquet", columns=["item_id", "segment"]).set_index("item_id").segment
    z = np.load(f"{ST}/AVOCET/data/v2/label_matrices.npz")
    assert (z["item_ids"] == it.item_id.to_numpy()).all()
    T = pd.DataFrame({"pop": "lib", "item_id": it.item_id, "cohort": it.cohort, "sender": it.sender.fillna(""),
                      "segment": sg.reindex(it.item_id).to_numpy(), "split": it.split, "family": it.family_id,
                      "is_dup": it.is_duplicate.astype(bool), "license": "private-library"})
    if need_text:
        T["context"] = it.context.fillna(""); T["text"] = it.text
    train_ok = it.item_id.isin(set(rlh.evalset("train"))).to_numpy()  # (np.isin on object arrays is quadratic)
    T["train_ok"] = (T.split == "train") & ~T.is_dup & train_ok
    H = {}
    for h in HEADS:
        if h in ("haiku", "flash", "argon"):
            H[h] = (z[f"{h}_closure"], z[f"{h}_present"].astype(bool), z[f"{h}_masked"])
        else:
            H[h] = (z[h], z[h + "_present"].astype(bool), None)
    return T, H


def _from_labels(it, z, pop_col, license_col):
    n = len(it)
    fp, ap = z["flash_present"].astype(bool), z["argon_present"].astype(bool)
    tgt, tp = z["target"], z["target_present"].astype(bool)
    H = {"haiku": (np.zeros((n, NL), np.uint8), np.zeros(n, bool), None),
         "flash": (z["flash_closure"], fp, z["flash_masked"]), "argon": (z["argon_closure"], ap, z["argon_masked"]),
         "y3": (tgt, tp, None), "y_hf": (tgt, tp, None)}
    return H


def thinking(need_text=True):
    E = f"{ST}/EGRET/data"
    it = pd.read_parquet(f"{E}/items.parquet")
    z = np.load(f"{E}/label_matrices.npz")
    assert (z["item_ids"] == it.item_id.to_numpy()).all()
    T = pd.DataFrame({"pop": "th", "item_id": it.item_id, "cohort": it.cohort, "sender": sender_role(it.unit_id),
                      "segment": "TH", "split": it.split, "family": it.family_id, "is_dup": it.is_duplicate.fillna(False).astype(bool),
                      "license": "private-library"})
    if need_text:
        T["context"] = it.context.fillna(""); T["text"] = it.text
    T["train_ok"] = (T.split == "train") & ~T.is_dup
    return T, _from_labels(it, z, None, None)


def public(need_text=True, labels_dir=f"{K}/data/labels_public"):
    it = pd.read_parquet(f"{labels_dir}/items.parquet")
    z = np.load(f"{labels_dir}/label_matrices.npz")
    assert (z["item_ids"] == it.item_id.to_numpy()).all()
    pi = pd.read_parquet(f"{K}/data/public_items.parquet", columns=["item_id", "source", "kest_split", "license", "excluded",
                                                                    "split_group", "segment", "hf_id"]).set_index("item_id").reindex(it.item_id)
    keep = (pi.excluded.to_numpy() == "") & pi.license.notna().to_numpy()
    T = pd.DataFrame({"pop": pi.source.to_numpy(), "item_id": it.item_id, "cohort": it.cohort, "sender": sender_role(it.unit_id),
                      "segment": pi.segment.to_numpy(), "split": pi.kest_split.to_numpy(), "family": pi.split_group.to_numpy(),
                      "is_dup": it.is_duplicate.astype(bool), "license": pi.license.to_numpy(), "hf_id": pi.hf_id.to_numpy()})
    if need_text:
        T["context"] = it.context.fillna(""); T["text"] = it.text
    T["train_ok"] = (T.split == "train") & ~T.is_dup
    H = _from_labels(it, z, None, None)
    k = np.flatnonzero(keep)
    T = T.iloc[k].reset_index(drop=True)
    H = {h: (Y[k], P[k], (M[k] if M is not None else None)) for h, (Y, P, M) in H.items()}
    return T, H


def concat(parts):
    T = pd.concat([p[0] for p in parts], ignore_index=True)
    H = {}
    for h in HEADS:
        Y = np.concatenate([p[1][h][0] for p in parts]); P = np.concatenate([p[1][h][1] for p in parts])
        Ms = [p[1][h][2] if p[1][h][2] is not None else np.zeros_like(p[1][h][0]) for p in parts]
        H[h] = (Y, P, np.concatenate(Ms))
    return T, H


def load_all(need_text=True, with_public=True):
    parts = [library(need_text), thinking(need_text)]
    if with_public:
        parts.append(public(need_text))
    return concat(parts)
