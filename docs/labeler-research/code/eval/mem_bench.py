# Frozen research snapshot, copied unchanged below this line from ~/pharos-labeling/research/director/mem_bench.py (2026-10-30).
# Not maintained and not run by tests; ~/pharos-labeling paths refer to the archive layout (see ../README.md).
"""Inference memory/throughput of the ONNX students at small thread counts (M2 Air proxy). Director, 2026-10-29.
usage: mem_bench.py ONNX_FILE HF_TOKENIZER THREADS [N] [TOKENS_PER_BATCH]"""
import os, sys, time, json, resource
sys.path.insert(0, os.path.expanduser("~/pharos-labeling/research/tracks/C-finetune/scripts"))
import numpy as np
from transformers import AutoTokenizer
from common import sample_items, sender_tag, Encoder, pad_batch, bucket_batches
import onnxruntime as ort
path, tokname, th = sys.argv[1], sys.argv[2], int(sys.argv[3])
n = int(sys.argv[4]) if len(sys.argv) > 4 else 600
tpb = int(sys.argv[5]) if len(sys.argv) > 5 else 8192
rss0 = resource.getrusage(resource.RUSAGE_SELF).ru_maxrss
tok = AutoTokenizer.from_pretrained(tokname)
rows = sample_items(n, seed=11)
enc = Encoder(tok, max_len=512, ctx_max=64)
seqs = enc.encode_batch([sender_tag(r[1], r[2]) for r in rows], [r[3] or "" for r in rows], [r[4] for r in rows])
rss_tok = resource.getrusage(resource.RUSAGE_SELF).ru_maxrss
so = ort.SessionOptions(); so.intra_op_num_threads = th; so.inter_op_num_threads = 1
s = ort.InferenceSession(path, so, providers=["CPUExecutionProvider"])
rss_load = resource.getrusage(resource.RUSAGE_SELF).ru_maxrss
L = np.array([len(x) for x in seqs]); bs = bucket_batches(L, tpb, max_bs=256, shuffle=False)
t = time.time()
for b in bs:
    ids, att = pad_batch([seqs[i] for i in b], tok.pad_token_id or 0)
    s.run(None, {"ids": ids, "att": att})
dt = time.time() - t
rss_peak = resource.getrusage(resource.RUSAGE_SELF).ru_maxrss
mb = lambda x: round(x / 1e6, 0)  # macOS ru_maxrss is bytes
print(json.dumps({"model": os.path.basename(path), "threads": th, "tokens_per_batch": tpb, "n": len(seqs), "mean_tokens": round(float(L.mean())),
    "items_s": round(len(seqs) / dt, 1), "hours_400k": round(400000 / (len(seqs) / dt) / 3600, 2),
    "rss_mb_baseline": mb(rss0), "rss_mb_after_tokenizer": mb(rss_tok), "rss_mb_after_load": mb(rss_load), "rss_mb_peak": mb(rss_peak)}), flush=True)
