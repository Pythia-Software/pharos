# Frozen research snapshot, copied unchanged below this line from ~/pharos-labeling/research/harness/compare.py (2026-10-30).
# Not maintained and not run by tests; ~/pharos-labeling paths refer to the archive layout (see ../README.md).
"""Print the leaderboard as a table.

    ~/pharos-labeling/venv/bin/python ~/pharos-labeling/research/harness/compare.py [--set gold3_test] [--track B-embeddings] [--all] [--cohorts]

One line per run (latest row per run_name unless --all). Columns: primary-set Jaccard / micro-F1 / macro-F1,
raw and cohort-weighted (w); LOO Jaccard vs Flash∩Argon (FA), Haiku∩Argon (HA), Haiku∩Flash (HF) on the
primary gold3 set; Jaccard on gold2_test and flash_test; throughput and hours per 400k items.
LLM fair references (Haiku vs FA, Flash vs HA, Argon vs HF) are printed on the header line.
"""
import argparse
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import rlh  # noqa: E402


def f(x, w=5):
    return f"{x:.3f}".rjust(w) if isinstance(x, (int, float)) else "-".rjust(w)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--set", default="gold3_test")
    ap.add_argument("--track")
    ap.add_argument("--all", action="store_true", help="every row, not just the latest per run_name")
    ap.add_argument("--cohorts", action="store_true", help="add per-cohort Jaccard on the primary set")
    ap.add_argument("--sort", default="jaccard", choices=["jaccard", "weighted", "time"])
    a = ap.parse_args()
    rows = rlh.read_leaderboard()
    if a.track:
        rows = [r for r in rows if r["track"] == a.track]
    if not a.all:
        last = {}
        for r in rows:
            last[(r["track"], r["run_name"])] = r
        rows = list(last.values())
    gold3 = a.set if a.set.startswith("gold3") else ("gold3_dev" if a.set.endswith("dev") else "gold3_test")
    ref = rlh._ref_loo().get(gold3, {})
    if ref:
        print(f"LLM fair LOO references on {gold3} (Jaccard raw/weighted): " + "  ".join(
            f"{v['who']} vs {k}: {v['jaccard']:.3f}/{v['weighted_jaccard']:.3f}" for k, v in ref.items()))

    def key(r):
        res = r["results"].get(a.set)
        if a.sort == "time":
            return r["time"]
        if not res:
            return -1
        return res["overall"]["weighted" if a.sort == "weighted" else "raw"]["jaccard"]

    rows.sort(key=key, reverse=a.sort != "time")
    other = "gold2_dev" if a.set.endswith("dev") else "gold2_test"
    flash = "flash_dev" if a.set.endswith("dev") else "flash_test"
    hdr = (f"{'run':<34} {'track':<13} {'J':>5} {'Jw':>5} {'mic':>5} {'micw':>5} {'mac':>5} {'macw':>5} | "
           f"{'L-FA':>5} {'L-HA':>5} {'L-HF':>5} | {'g2J':>5} {'flJ':>5} | {'it/s':>8} {'h/400k':>7}")
    if a.cohorts:
        hdr += " | " + " ".join(f"{c[:6]:>6}" for c in rlh.COHORTS)
    print(f"primary set: {a.set}")
    print(hdr)
    print("-" * len(hdr))
    for r in rows:
        res = r["results"].get(a.set)
        m = r.get("meta", {})
        ips = m.get("inference_items_per_s")
        line = f"{r['run_name'][:34]:<34} {r['track'][:13]:<13} "
        if res:
            o, w = res["overall"]["raw"], res["overall"]["weighted"]
            line += " ".join(f(x) for x in (o["jaccard"], w["jaccard"], o["micro_f1"], w["micro_f1"], o["macro_f1"], w["macro_f1"]))
        else:
            line += " ".join(f(None) for _ in range(6))
        g3 = r["results"].get(gold3, {}).get("loo", {})
        line += " | " + " ".join(f(g3.get(k, {}).get("raw", {}).get("jaccard")) for k in ("FA", "HA", "HF"))
        line += " | " + " ".join(f(r["results"].get(s, {}).get("overall", {}).get("raw", {}).get("jaccard")) for s in (other, flash))
        line += f" | {(f'{ips:.0f}' if ips else '-'):>8} {(str(m.get('estimated_hours_400k')) if m.get('estimated_hours_400k') is not None else '-'):>7}"
        if a.cohorts and res:
            line += " | " + " ".join(f(res["strata"].get(f"cohort={c}", {}).get("raw", {}).get("jaccard"), 6) for c in rlh.COHORTS)
        print(line)


if __name__ == "__main__":
    main()
