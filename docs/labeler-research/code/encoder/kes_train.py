# Frozen research snapshot, copied unchanged below this line from ~/pharos-labeling/studies/KESTREL/scripts/kes_train.py (2026-10-30).
# Not maintained and not run by tests; ~/pharos-labeling paths refer to the archive layout (see ../README.md).
"""KESTREL trainer: continue a BOBOLINK v2 shared encoder (s32v-all / s68v-all) on library + TH + public data with a
source/segment-balanced sampler. Recipe = BOBOLINK (masked mean pool, 5 heads, BCE with teacher masks, AdamW, bf16 MPS,
length-bucketed batches, round-1 framing via framing.Framer). Writes models/<run>/{final.pt, meta.json, probs.npz}.
probs.npz: raw sigmoid head outputs [n, 5, 54] for every non-train row of every population (and all public rows), keys
pop, item_id, probs, heads.

usage: kes_train.py --run R --init {s32v-all|s68v-all} [--exclude-source AT] [--no-public] [--epochs 2] [--quota ...]"""
import argparse, json, math, os, sys, time, resource, random
HERE = os.path.dirname(os.path.abspath(__file__)); sys.path.insert(0, HERE)
sys.path.insert(0, os.path.expanduser("~/pharos-labeling/studies/BOBOLINK/scripts"))
sys.path.insert(0, os.path.expanduser("~/pharos-labeling/research/tracks/C-finetune/scripts"))
import numpy as np, pandas as pd, torch, torch.nn.functional as F
from transformers import AutoTokenizer, AutoModel
from framing import Framer
from common import pad_batch, bucket_batches
import kdata

K = kdata.K
B = os.path.expanduser("~/pharos-labeling/studies/BOBOLINK/models")
ap = argparse.ArgumentParser()
ap.add_argument("--run", required=True)
ap.add_argument("--init", default="s32v-all")
ap.add_argument("--exclude-source", default="", help="comma list of public sources left out entirely (LOSO)")
ap.add_argument("--no-public", action="store_true")
ap.add_argument("--public-only", action="store_true", help="train on public train rows only (library and EGRET TH never trained on)")
ap.add_argument("--epochs", type=int, default=2)
# per-epoch quotas: group=n (n<0: all rows x |n|)
ap.add_argument("--quota", default="lib:H=-1,lib:AIUR=8000,lib:DEL=6000,lib:AR=22000,th:TH=7000,pub:H=-2,pub:AR=-2,pub:DEL=-2,pub:TH=-2,pub:AIUR=-2")
ap.add_argument("--lr", type=float, default=3e-5)
ap.add_argument("--head-lr", type=float, default=1e-3)
ap.add_argument("--wd", type=float, default=0.01)
ap.add_argument("--warmup", type=float, default=0.05)
ap.add_argument("--train-tokens", type=int, default=8192)
ap.add_argument("--infer-tokens", type=int, default=24576)
ap.add_argument("--seed", type=int, default=0)
ap.add_argument("--ckpt-min", type=float, default=6.0)
a = ap.parse_args()
RUN = os.path.join(K, "models", a.run); os.makedirs(RUN, exist_ok=True)
LOG = open(os.path.join(K, "logs", a.run + ".log"), "a")


def log(*x):
    s = time.strftime("%H:%M:%S ") + " ".join(str(v) for v in x); print(s, flush=True); LOG.write(s + "\n"); LOG.flush()


log("args", json.dumps(vars(a)))
torch.manual_seed(a.seed); np.random.seed(a.seed); random.seed(a.seed)
dev = torch.device(os.environ.get("KES_DEVICE", "mps"))
SMOKE = int(os.environ.get("KES_SMOKE", "0"))  # >0: train this many steps and predict this many rows (testing only)
import contextlib
def ac():
    return torch.autocast(device_type="mps", dtype=torch.bfloat16) if dev.type == "mps" else contextlib.nullcontext()
def mps_mem():
    return torch.mps.driver_allocated_memory() / 1e9 if dev.type == "mps" else 0.0
t0 = time.time()
T, H = kdata.load_all(with_public=os.path.exists(f"{K}/data/labels_public/label_matrices.npz"))  # public rows are always predicted
excl = [s for s in a.exclude_source.split(",") if s] + (kdata.SOURCES if a.no_public else [])
pub = T["pop"].isin(kdata.SOURCES).to_numpy()
drop = T["pop"].isin(excl).to_numpy()  # never trained on
if a.public_only:
    drop = drop | ~pub
log(f"loaded {len(T)} rows in {time.time()-t0:.0f}s; pops", T["pop"].value_counts().to_dict(), "excluded sources", excl, int(drop.sum()))
seg = T.segment.to_numpy().astype(str)
sgroup = np.where(np.isin(seg, kdata.HUMAN), "H", seg)
popk = np.where(pub, "pub", T["pop"].to_numpy())
grp = np.char.add(np.char.add(popk.astype(str), ":"), sgroup.astype(str))
anyp = np.zeros(len(T), bool)
for h in kdata.HEADS:
    anyp |= H[h][1]
trainable_rows = T.train_ok.to_numpy() & anyp & ~drop
quota = {k: int(v) for k, v in (kv.split("=") for kv in a.quota.split(","))}
rng = np.random.default_rng(a.seed)
pools = {g: np.flatnonzero(trainable_rows & (grp == g)) for g in quota}
log("pools", {g: len(p) for g, p in pools.items()})


def epoch_sample(e):
    r = np.random.default_rng(a.seed * 100 + e); out = []
    for g, q in quota.items():
        p = pools[g]
        if not len(p): continue
        if q < 0: out += [p] * (-q)
        else: out.append(r.choice(p, size=min(q, len(p)), replace=False))
    s = np.concatenate(out); r.shuffle(s); return s


if a.init.startswith("hf:"):  # from the pretrained encoder (no library-trained weights); heads initialised to label priors
    MODEL = a.init[3:]; heads = list(kdata.HEADS)
else:
    meta0 = json.load(open(f"{B}/{a.init}/meta.json")); MODEL = meta0["args"]["model"]
    assert meta0["args"]["ctx"] == "r1" and meta0["args"]["fmt"] == "tag" and meta0["args"]["frame_segments"] == "human"
    heads = meta0["heads"]; assert heads == kdata.HEADS, heads
tok = AutoTokenizer.from_pretrained(MODEL); PAD = tok.pad_token_id if tok.pad_token_id is not None else 0
empty = np.array([""] * len(T), dtype=object)
fr = Framer(tok, T.cohort.to_numpy(), T.sender.to_numpy().astype(object), seg, T.context.to_numpy().astype(object), empty, empty,
            T.text.to_numpy().astype(object), kdata.HUMAN, ctx="r1")
_seq = {}


def seqs_for(rows):
    need = np.array([r for r in np.unique(rows) if r not in _seq], dtype=np.int64)
    for i in range(0, len(need), 8192):
        ch = need[i:i + 8192]
        for r, s in zip(ch, fr.encode_rows(ch)): _seq[int(r)] = s
    return [_seq[int(r)] for r in rows]


class Net(torch.nn.Module):
    def __init__(self):
        super().__init__()
        self.enc = AutoModel.from_pretrained(MODEL); self.drop = torch.nn.Dropout(0.1)
        self.head = torch.nn.Linear(self.enc.config.hidden_size, kdata.NL * len(heads))

    def forward(self, ids, att):
        h = self.enc(input_ids=ids, attention_mask=att).last_hidden_state
        m = att.unsqueeze(-1).to(h.dtype)
        return self.head(self.drop((h * m).sum(1) / m.sum(1).clamp(min=1))).float().view(-1, len(heads), kdata.NL)


net = Net()
if a.init.startswith("hf:"):
    tr_rows = np.flatnonzero(trainable_rows)
    with torch.no_grad():
        for k, h in enumerate(heads):
            Y, P, _ = H[h]; r = tr_rows[P[tr_rows]]
            pr = np.clip(Y[r].mean(0), 1e-4, 1 - 1e-4) if len(r) else np.full(kdata.NL, 0.1)
            net.head.bias[k * kdata.NL:(k + 1) * kdata.NL] = torch.tensor(np.log(pr / (1 - pr)), dtype=torch.float32)
else:
    net.load_state_dict(torch.load(f"{B}/{a.init}/final.pt", map_location="cpu"))
net.to(dev)
samples = [epoch_sample(e) for e in range(a.epochs)]
log("epoch sample", len(samples[0]), pd.Series(grp[samples[0]]).value_counts().to_dict())
batches = []
for e, s in enumerate(samples):
    L = np.array([len(x) for x in seqs_for(s)])
    batches += [s[np.asarray(b)] for b in bucket_batches(L, a.train_tokens, max_bs=128, shuffle=True, seed=a.seed + e)]
log(f"tokenized; {len(batches)} batches; prep {time.time()-t0:.0f}s")
total = len(batches) if not SMOKE else min(SMOKE, len(batches))
opt = torch.optim.AdamW([{"params": [p for n, p in net.named_parameters() if not n.startswith("head")], "lr": a.lr},
                         {"params": net.head.parameters(), "lr": a.head_lr}], weight_decay=a.wd)
warm = max(1, int(total * a.warmup))
sched = torch.optim.lr_scheduler.LambdaLR(opt, lambda s: min(1.0, (s + 1) / warm) * max(0.05, 1 - max(0, s - warm) / max(1, total - warm)))
start, seen, secs = 0, 0, 0.0
ck = os.path.join(RUN, "ckpt.pt")
if os.path.exists(ck):
    st = torch.load(ck, map_location="cpu", weights_only=False)
    net.load_state_dict(st["net"]); opt.load_state_dict(st["opt"]); sched.load_state_dict(st["sched"])
    start, seen, secs = st["step"], st["seen"], st["secs"]; log("resumed at", start)
HYt = {h: H[h] for h in heads}
net.train(); last = time.time(); ema = None
if not os.path.exists(os.path.join(RUN, "final.pt")):
    for step in range(start, total):
        rows = batches[step]
        ids, att = pad_batch(seqs_for(rows), PAD)
        ts = time.time()
        with ac():
            lo = net(torch.from_numpy(ids).to(dev), torch.from_numpy(att).to(dev))
        loss = 0.0
        for k, h in enumerate(heads):
            Y, P, M = HYt[h]; pres = P[rows]
            if not pres.any(): continue
            y = torch.from_numpy(Y[rows].astype(np.float32)).to(dev)
            msk = torch.from_numpy(pres.astype(np.float32)).to(dev)[:, None].expand(-1, kdata.NL).clone()
            if M is not None:
                msk = msk * (1 - torch.from_numpy(M[rows].astype(np.float32)).to(dev))
            l = F.binary_cross_entropy_with_logits(lo[:, k], y, reduction="none")
            loss = loss + (l * msk).sum() / max(1, int(pres.sum())) / kdata.NL * 10
        loss.backward(); torch.nn.utils.clip_grad_norm_(net.parameters(), 1.0)
        opt.step(); sched.step(); opt.zero_grad(set_to_none=True)
        if step % 20 == 0:
            lv = float(loss.detach()); ema = lv if ema is None else 0.9 * ema + 0.1 * lv
        secs += time.time() - ts; seen += len(rows)
        if step % 200 == 0:
            log(f"step {step}/{total} seen {seen} loss {ema:.4f} {seen/max(secs,1e-9):.0f} it/s mps {mps_mem():.1f}GB")
        if time.time() - last > a.ckpt_min * 60:
            tmp = ck + ".tmp"
            torch.save({"net": net.state_dict(), "opt": opt.state_dict(), "sched": sched.state_dict(), "step": step + 1, "seen": seen, "secs": secs}, tmp)
            os.replace(tmp, ck); last = time.time()
    torch.save(net.state_dict(), os.path.join(RUN, "final.pt"))
    if os.path.exists(ck): os.replace(ck, ck + ".done")
    log(f"train done {seen} items {secs/60:.1f} min")
else:
    net.load_state_dict(torch.load(os.path.join(RUN, "final.pt"), map_location="cpu")); log("final.pt exists; predicting only")
peak = mps_mem()
_seq.clear()
# ---- predict: every non-train row (all pops) + every public row (LOSO uses held-out sources' train rows too)
net.eval()
pr = np.flatnonzero((T.split.to_numpy() != "train") | pub)
if SMOKE: pr = np.random.default_rng(0).choice(pr, SMOKE * 20, replace=False)
tp = time.time(); out = np.zeros((len(pr), len(heads), kdata.NL), np.float16)
with torch.inference_mode():
    for i in range(0, len(pr), 20000):
        ch = pr[i:i + 20000]; S = fr.encode_rows(ch); L = np.array([len(s) for s in S])
        for b in bucket_batches(L, a.infer_tokens, max_bs=512, shuffle=False):
            x, m = pad_batch([S[j] for j in b], PAD)
            with ac():
                lo = net(torch.from_numpy(x).to(dev), torch.from_numpy(m).to(dev))
            out[i + np.asarray(b)] = torch.sigmoid(lo).float().cpu().numpy().astype(np.float16)
(torch.mps.synchronize() if dev.type == "mps" else None); ips = len(pr) / (time.time() - tp)
np.savez_compressed(os.path.join(RUN, "probs.npz"), pop=T["pop"].to_numpy()[pr].astype("U3"), item_id=T.item_id.to_numpy()[pr].astype("U"),
                    probs=out, heads=np.array(heads))
meta = {"run": a.run, "args": vars(a), "model": MODEL, "init": a.init, "heads": heads, "n_epoch_sample": int(len(samples[0])),
        "train_min": round(secs / 60, 2), "items_seen": int(seen), "pred_rows": int(len(pr)), "pred_items_s_mps": round(ips, 1),
        "peak_mps_gb": round(peak, 2), "peak_rss_gb": round(resource.getrusage(resource.RUSAGE_SELF).ru_maxrss / 1e9, 2),
        "pools": {g: int(len(p)) for g, p in pools.items()}, "excluded_sources": excl, "public_only": bool(a.public_only),
        "train_rows_by_pop": T.iloc[np.unique(np.concatenate(samples))]["pop"].value_counts().to_dict()}
json.dump(meta, open(os.path.join(RUN, "meta.json"), "w"), indent=1, default=str)
log("DONE", json.dumps({k: meta[k] for k in ("train_min", "pred_items_s_mps", "peak_mps_gb", "peak_rss_gb")}))
