# Frozen research snapshot, copied unchanged below this line from ~/pharos-labeling/studies/BOBOLINK/scripts/bob_train.py (2026-10-30).
# Not maintained and not run by tests; ~/pharos-labeling paths refer to the archive layout (see ../README.md).
"""BOBOLINK: segment specialists and context/framing variants (derived from track C train.py).

Writes models/<run>/{final.pt or adapter.pt, meta.json, <split>_probs.npz} and logs/<run>.log.
No harness writes here; scoring is scripts/score.py.

Framing applies to items whose segment is in --frame-segments; all other items use the round-1 encoder
("r1": tag = cohort+sender, context cut to 512 chars, >=64 ctx tokens, more when text is short).
"""
import argparse, json, math, os, sys, time, resource, random
sys.path.insert(0, os.path.dirname(__file__))
sys.path.insert(0, os.path.expanduser("~/pharos-labeling/research/harness"))
import numpy as np
import pandas as pd
import torch
import torch.nn.functional as F
from transformers import AutoTokenizer, AutoModel
from common import sender_tag, Encoder, pad_batch, bucket_batches
import rlh

S = os.path.expanduser("~/pharos-labeling/studies/BOBOLINK")
SEGFILE = os.path.expanduser("~/pharos-labeling/studies/AVOCET/data/segments.parquet")
HUMAN = ["H-typed", "H-typed+ctx", "H-template", "H-resent", "H-feedback"]
GROUPS = {"human": HUMAN, "H-rest": ["H-template", "H-resent", "H-feedback"], "all": HUMAN + ["AIUR", "AR", "DEL"]}


def segs(x):
    out = []
    for s in x.split(","):
        out += GROUPS.get(s, [s]) if s else []
    return out


ap = argparse.ArgumentParser()
ap.add_argument("--run", required=True)
ap.add_argument("--model", default="jhu-clsp/ettin-encoder-32m")
ap.add_argument("--max-len", type=int, default=512, help="max_len for framed items")
ap.add_argument("--base-max-len", type=int, default=512, help="max_len for r1-framed (other) items")
# framing
ap.add_argument("--frame-segments", default="human")
ap.add_argument("--ctx", default="r1", help="r1 | none | <N tokens> | all  (hard cap on context tokens; all = all 600 chars)")
ap.add_argument("--ctx-part", default="both", help="both | asst | user (within the 600 chars)")
ap.add_argument("--order", default="ctx_first", help="ctx_first | text_first")
ap.add_argument("--fmt", default="tag", help="tag (cohort sender :) | words | notag | segtag")
# data
ap.add_argument("--train-segments", default="all")
ap.add_argument("--oversample", default="", help="seg=k,...: repeat selected items of these segments/groups k times")
ap.add_argument("--replay", type=int, default=0, help="add N sampled items from other segments (r1 framing)")
ap.add_argument("--n-train", type=int, default=0, help="sample size from the segment pool (0 = all)")
ap.add_argument("--epochs", type=float, default=1.0)
ap.add_argument("--heads", default="y_hf,haiku,flash,argon,y3")
ap.add_argument("--targets", default="v1", help="v1 = round-1 label matrices; v2 = AVOCET v2 (when published)")
ap.add_argument("--cohort-boost", default="agent_reply=1,agent_in_user_role=2,delegation=2,human=3")
ap.add_argument("--rare-boost", type=float, default=1.0)
ap.add_argument("--lr", type=float, default=8e-5)
ap.add_argument("--head-lr", type=float, default=1e-3)
ap.add_argument("--wd", type=float, default=0.01)
ap.add_argument("--warmup", type=float, default=0.05)
ap.add_argument("--train-tokens", type=int, default=12288)
ap.add_argument("--infer-tokens", type=int, default=32768)
ap.add_argument("--ckpt-min", type=float, default=8.0)
ap.add_argument("--seed", type=int, default=0)
ap.add_argument("--init-from", default="", help="state dict (continuation / adapter base)")
ap.add_argument("--lora", type=int, default=0, help="LoRA rank on Wqkv/Wi/Wo (encoder frozen otherwise)")
ap.add_argument("--freeze-enc", action="store_true", help="train only the heads")
ap.add_argument("--new-heads", action="store_true", help="re-init heads (segment-specific heads)")
ap.add_argument("--predict", default="all", help="all | human | none: dev/test/fresh items to save probs for")
ap.add_argument("--bench", action="store_true", help="time MPS inference on 20k random corpus items")
ap.add_argument("--notes", default="")
a = ap.parse_args()

RUN = os.path.join(S, "models", a.run)
os.makedirs(RUN, exist_ok=True)
LOG = open(os.path.join(S, "logs", a.run + ".log"), "a")


def log(*x):
    s = time.strftime("%H:%M:%S ") + " ".join(str(v) for v in x)
    print(s, flush=True)
    LOG.write(s + "\n"); LOG.flush()


log("args", json.dumps(vars(a)))
torch.manual_seed(a.seed); np.random.seed(a.seed); random.seed(a.seed)
dev = torch.device("mps")
LABELS = rlh.label_list()
NL = len(LABELS)

# ------------------------------------------------------------------ data
t0 = time.time()
items = rlh.load_items(columns=["item_id", "cohort", "sender", "context", "text", "split", "is_duplicate"])
M = rlh._matrices()
assert (items.item_id.to_numpy() == M["item_ids"]).all()
sg = pd.read_parquet(SEGFILE, columns=["item_id", "segment"]).set_index("item_id").segment
segment = sg.reindex(items.item_id).to_numpy()
assert not pd.isna(segment).any()
cp = pd.read_parquet(os.path.join(S, "data", "ctx_parts.parquet")).set_index("item_id").reindex(items.item_id)
ctx_all = items.context.fillna("").to_numpy()
ctx_user, ctx_asst = cp.ctx_user.fillna("").to_numpy(), cp.ctx_asst.fillna("").to_numpy()

sys.path.insert(0, os.path.join(S, "scripts"))
from targets import load_targets  # noqa: E402
heads = [h for h in a.heads.split(",") if h]
HY, HP, HM = load_targets(a.targets, heads, M)
log(f"loaded {time.time()-t0:.1f}s; present:", {h: int(HP[h].sum()) for h in heads})

split = items.split.to_numpy()
cohort = items.cohort.to_numpy()
anyp = np.zeros(len(items), bool)
for h in heads:
    anyp |= HP[h]
train_ids = set(rlh.evalset("train"))
base = (split == "train") & ~items.is_duplicate.to_numpy().astype(bool) & anyp & np.isin(M["item_ids"], list(train_ids))
TS = segs(a.train_segments)
pool = np.flatnonzero(base & np.isin(segment, TS))
log("train pool", len(pool), pd.Series(segment[pool]).value_counts().to_dict())

rng = np.random.default_rng(a.seed)


def weights(p):
    boost = {k: float(v) for k, v in (kv.split("=") for kv in a.cohort_boost.split(","))}
    w = np.array([boost.get(c, 1.0) for c in cohort[p]])
    if a.rare_boost > 0:
        ref = HY[heads[0]][p].astype(np.float32)
        freq = ref.mean(0) + 1e-4
        rw = (ref * (1.0 / freq)[None, :] ** a.rare_boost).max(1)
        rw = np.clip(rw / np.median(rw[rw > 0]) if (rw > 0).any() else rw, 1.0, 20.0)
        w = w * rw
    return w


if a.n_train and a.n_train < len(pool):
    forced = pool[M["argon_present"][pool].astype(bool)]
    rest = pool[~M["argon_present"][pool].astype(bool)]
    w = weights(rest)
    pick = rng.choice(rest, size=max(0, a.n_train - len(forced)), replace=False, p=w / w.sum())
    sel = np.concatenate([forced, pick])
else:
    sel = pool.copy()
if a.replay:
    other = np.flatnonzero(base & ~np.isin(segment, TS))
    w = weights(other)
    sel = np.concatenate([sel, rng.choice(other, size=min(a.replay, len(other)), replace=False, p=w / w.sum())])
if a.oversample:
    for kv in a.oversample.split(","):
        g, k = kv.split("=")
        extra = sel[np.isin(segment[sel], segs(g))]
        sel = np.concatenate([sel] + [extra] * (int(k) - 1))
rng.shuffle(sel)
log("train sample", len(sel), pd.Series(segment[sel]).value_counts().to_dict())

tok = AutoTokenizer.from_pretrained(a.model)
CLS = tok.cls_token_id if tok.cls_token_id is not None else tok.bos_token_id
SEP = tok.sep_token_id if tok.sep_token_id is not None else tok.eos_token_id
PAD = tok.pad_token_id if tok.pad_token_id is not None else 0
r1enc = Encoder(tok, max_len=a.base_max_len, ctx_max=64)
FS = set(segs(a.frame_segments))
_tc = {}


def tids(s):
    v = _tc.get(s)
    if v is None:
        v = tok(s, add_special_tokens=False)["input_ids"]; _tc[s] = v
    return v


def framed(idx):
    """Encode rows idx with the configured framing."""
    if a.ctx_part == "both":
        cs = [ctx_all[i] for i in idx]  # identical to the 600-char string the teacher saw
    elif a.ctx_part == "asst":
        cs = [ctx_asst[i] for i in idx]
    else:
        cs = [ctx_user[i] for i in idx]
    if a.ctx == "none":
        cs = ["" for _ in idx]
    cap = a.max_len * 8
    txt = [items.text.iat[i] for i in idx]
    txt = [t if len(t) <= cap else t[: cap // 2] + " … " + t[-cap // 2:] for t in txt]
    T = tok(txt, add_special_tokens=False)["input_ids"]
    C = tok(cs, add_special_tokens=False)["input_ids"]
    out = []
    for k, i in enumerate(idx):
        if a.fmt == "tag":
            g1, g2 = tids(sender_tag(cohort[i], items.sender.iat[i]) + " :"), []
        elif a.fmt == "segtag":
            g1, g2 = tids(f"{segment[i]} {items.sender.iat[i] or ''} :"), []
        elif a.fmt == "notag":
            g1, g2 = [], []
        else:  # words
            g1, g2 = tids("Earlier conversation:"), tids(f"Current {cohort[i]} message:")
        c, t = C[k], T[k]
        if a.order == "text_first":
            g1, g2 = (g2, g1) if a.fmt == "words" else (g1, [])
        budget = a.max_len - 3 - len(g1) - len(g2)
        if a.ctx in ("r1",):
            c = c[-(64 * 8):]
            cb = min(len(c), max(64, budget - len(t)))
        else:
            capn = 10 ** 6 if a.ctx in ("all", "none") else int(a.ctx)
            want = min(len(c), capn)
            cb = min(want, max(min(want, 64), budget - len(t)))
        tb = budget - cb
        if len(t) > tb:
            h = int(tb * 0.7)
            t = t[:h] + t[len(t) - (tb - h):] if tb - h > 0 else t[:tb]
        c = c[len(c) - cb:] if cb > 0 else []
        if a.order == "text_first":
            out.append([CLS] + g1 + t + [SEP] + g2 + c + [SEP])
        else:
            out.append([CLS] + g1 + c + [SEP] + g2 + t + [SEP])
    return out


def encode_rows(idx):
    idx = np.asarray(idx)
    out = [None] * len(idx)
    fm = np.isin(segment[idx], list(FS))
    pos = np.flatnonzero(fm)
    for i in range(0, len(pos), 4096):
        p = pos[i:i + 4096]
        for k, v in zip(p, framed(idx[p])):
            out[k] = v
    pos = np.flatnonzero(~fm)
    tags = [sender_tag(c, s) for c, s in zip(cohort[idx], items.sender.to_numpy()[idx])]
    ctx = items.context.to_numpy()[idx]
    txt = items.text.to_numpy()[idx]
    for i in range(0, len(pos), 4096):
        p = pos[i:i + 4096]
        for k, v in zip(p, r1enc.encode_batch([tags[k] for k in p], [ctx[k] or "" for k in p], [txt[k] for k in p])):
            out[k] = v
    return out


t0 = time.time()
train_seqs = encode_rows(sel)
log(f"tokenized {len(sel)} in {time.time()-t0:.1f}s; mean len {np.mean([len(s) for s in train_seqs]):.1f}")
ex = [i for i in range(len(sel)) if segment[sel[i]] in FS][:1]
if ex:
    log("example framed:", repr(tok.decode(train_seqs[ex[0]])[:700]))


# ------------------------------------------------------------------ model
class Net(torch.nn.Module):
    def __init__(self, name, nheads):
        super().__init__()
        self.enc = AutoModel.from_pretrained(name)
        H = self.enc.config.hidden_size
        self.drop = torch.nn.Dropout(0.1)
        self.head = torch.nn.Linear(H, NL * nheads)
        self.nheads = nheads

    def forward(self, ids, att):
        h = self.enc(input_ids=ids, attention_mask=att).last_hidden_state
        m = att.unsqueeze(-1).to(h.dtype)
        pooled = (h * m).sum(1) / m.sum(1).clamp(min=1)
        return self.head(self.drop(pooled)).float().view(-1, self.nheads, NL)


net = Net(a.model, len(heads))
nparams = sum(p.numel() for p in net.parameters())
if a.init_from:
    sd = torch.load(a.init_from, map_location="cpu")
    if a.new_heads:
        sd = {k: v for k, v in sd.items() if not k.startswith("head")}
    missing = net.load_state_dict(sd, strict=not a.new_heads)
    log("initialized from", a.init_from, "new heads" if a.new_heads else "")
if not a.init_from or a.new_heads:
    with torch.no_grad():
        for k, h in enumerate(heads):
            y = HY[h][sel][HP[h][sel]].astype(np.float32)
            p = np.clip(y.mean(0), 1e-4, 1 - 1e-4)
            net.head.bias[k * NL:(k + 1) * NL] = torch.tensor(np.log(p / (1 - p)), dtype=torch.float32)
if a.lora:
    from peft import LoraConfig, get_peft_model
    net.enc = get_peft_model(net.enc, LoraConfig(r=a.lora, lora_alpha=2 * a.lora, lora_dropout=0.05,
                                                 target_modules=["Wqkv", "Wi", "Wo"]))
if a.freeze_enc:
    for p in net.enc.parameters():
        p.requires_grad = False
net = net.to(dev)
trainable = [(n, p) for n, p in net.named_parameters() if p.requires_grad]
ntrain_params = sum(p.numel() for _, p in trainable)
log(f"params {nparams/1e6:.1f}M, trainable {ntrain_params/1e6:.3f}M")

posw = {h: torch.ones(NL, device=dev) for h in heads}
lens = np.array([len(s) for s in train_seqs])
nep = max(1, math.ceil(a.epochs))
batches = []
for e in range(nep):
    bb = bucket_batches(lens, a.train_tokens, max_bs=128, shuffle=True, seed=a.seed + e)
    if e == nep - 1 and a.epochs < nep:
        bb = bb[: int(len(bb) * (a.epochs - (nep - 1)))]
    batches += bb
total = len(batches)
enc_params = [p for n, p in trainable if not n.startswith("head")]
groups = [{"params": [p for n, p in trainable if n.startswith("head")], "lr": a.head_lr}]
if enc_params:
    groups.append({"params": enc_params, "lr": a.lr if not a.lora else max(a.lr, 5e-4)})
opt = torch.optim.AdamW(groups, weight_decay=a.wd)
warm = max(1, int(total * a.warmup))
sched = torch.optim.lr_scheduler.LambdaLR(
    opt, lambda s: min(1.0, (s + 1) / warm) * max(0.05, 1 - max(0, s - warm) / max(1, total - warm)))

start_step, items_seen, train_secs = 0, 0, 0.0
ck = os.path.join(RUN, "ckpt.pt")
if os.path.exists(ck):
    st = torch.load(ck, map_location="cpu", weights_only=False)
    net.load_state_dict(st["net"]); opt.load_state_dict(st["opt"]); sched.load_state_dict(st["sched"])
    start_step, items_seen, train_secs = st["step"], st["items_seen"], st["train_secs"]
    log("resumed at step", start_step, "items", items_seen)


def save_ckpt(step):
    tmp = ck + ".tmp"
    torch.save({"net": net.state_dict(), "opt": opt.state_dict(), "sched": sched.state_dict(), "step": step,
                "items_seen": items_seen, "train_secs": train_secs}, tmp)
    os.replace(tmp, ck)


@torch.inference_mode()
def predict(idx, dtype=torch.bfloat16):
    net.eval()
    t = time.time()
    seqs = encode_rows(idx)
    L = np.array([len(s) for s in seqs])
    out = np.zeros((len(idx), len(heads), NL), np.float32)
    for b in bucket_batches(L, a.infer_tokens, max_bs=512, shuffle=False):
        ids, att = pad_batch([seqs[i] for i in b], PAD)
        with torch.autocast(device_type="mps", dtype=dtype):
            lo = net(torch.from_numpy(ids).to(dev), torch.from_numpy(att).to(dev))
        out[b] = torch.sigmoid(lo).cpu().numpy()
    torch.mps.synchronize()
    net.train()
    return out, len(idx) / (time.time() - t), float(L.mean())


# ------------------------------------------------------------------ train
net.train()
last_ck = time.time(); t_run = time.time(); ema = None
for step in range(start_step, total):
    b = batches[step]
    ids, att = pad_batch([train_seqs[i] for i in b], PAD)
    ids, att = torch.from_numpy(ids).to(dev), torch.from_numpy(att).to(dev)
    rows_ = sel[b]
    ts = time.time()
    with torch.autocast(device_type="mps", dtype=torch.bfloat16):
        lo = net(ids, att)
    loss = 0.0
    for k, h in enumerate(heads):
        pres = HP[h][rows_]
        if not pres.any():
            continue
        y = torch.from_numpy(HY[h][rows_].astype(np.float32)).to(dev)
        msk = torch.from_numpy(pres.astype(np.float32)).to(dev)[:, None].expand(-1, NL).clone()
        if HM[h] is not None:
            msk = msk * (1 - torch.from_numpy(HM[h][rows_].astype(np.float32)).to(dev))
        l = F.binary_cross_entropy_with_logits(lo[:, k], y, reduction="none")
        loss = loss + (l * msk).sum() / max(1, int(pres.sum())) / NL * 10
    loss.backward()
    torch.nn.utils.clip_grad_norm_([p for _, p in trainable], 1.0)
    opt.step(); sched.step(); opt.zero_grad(set_to_none=True)
    if step % 20 == 0:
        lv = float(loss.detach()); ema = lv if ema is None else 0.9 * ema + 0.1 * lv
    train_secs += time.time() - ts
    items_seen += len(b)
    if step % 200 == 0:
        log(f"step {step}/{total} items {items_seen} loss_ema {ema:.4f} {items_seen/max(train_secs,1e-9):.0f} items/s "
            f"mps {torch.mps.driver_allocated_memory()/1e9:.1f}GB")
    if time.time() - last_ck > a.ckpt_min * 60:
        save_ckpt(step + 1); last_ck = time.time()
log(f"training done: {items_seen} items, {train_secs/60:.2f} min train compute, wall {(time.time()-t_run)/60:.2f} min")

if a.lora:
    sd = {k: v for k, v in net.state_dict().items() if "lora_" in k or k.startswith("head")}
    torch.save(sd, os.path.join(RUN, "adapter.pt"))
    merged = net.enc.merge_and_unload(); net.enc = merged
elif a.freeze_enc:
    torch.save({k: v for k, v in net.state_dict().items() if k.startswith("head")}, os.path.join(RUN, "heads.pt"))
else:
    torch.save(net.state_dict(), os.path.join(RUN, "final.pt"))
if os.path.exists(ck):
    os.replace(ck, ck + ".done")  # keep (delete nothing); not resumed again
peak_mps = torch.mps.driver_allocated_memory() / 1e9

# ------------------------------------------------------------------ predict
pred_ips = {}
if a.predict != "none":
    for sp in ("dev", "test", "fresh"):
        m = split == sp
        if a.predict == "human":
            m &= np.isin(segment, HUMAN)
        idx = np.flatnonzero(m)
        p, ips, ml = predict(idx)
        pred_ips[sp] = (len(idx), round(ips, 1), round(ml, 1))
        np.savez_compressed(os.path.join(RUN, f"{sp}_probs.npz"), item_ids=M["item_ids"][idx],
                            probs=p.astype(np.float16), heads=np.array(heads))
    log("saved probs", pred_ips)
bench = None
if a.bench:
    bi = np.sort(np.random.default_rng(123).choice(len(items), size=20000, replace=False))
    _, ips, ml = predict(bi)
    bench = {"items_per_s": round(ips, 1), "mean_len": round(ml, 1)}
    log("bench MPS bf16 20k random corpus items", bench)
meta = {"run": a.run, "args": vars(a), "params": nparams, "trainable_params": ntrain_params,
        "n_train": int(len(sel)), "items_seen": int(items_seen), "train_min": round(train_secs / 60, 2),
        "train_segments": TS, "frame_segments": sorted(FS), "heads": heads, "pred_ips": pred_ips, "bench": bench,
        "peak_mps_gb": round(peak_mps, 2), "peak_rss_gb": round(resource.getrusage(resource.RUSAGE_SELF).ru_maxrss / 1e9, 2),
        "mean_train_len": float(lens.mean())}
with open(os.path.join(RUN, "meta.json"), "w") as f:
    json.dump(meta, f, indent=1, default=str)
log("DONE")
