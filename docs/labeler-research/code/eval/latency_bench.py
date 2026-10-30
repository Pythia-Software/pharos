# Frozen research snapshot, copied unchanged below this line from ~/pharos-labeling/research/director/latency_bench.py (2026-10-30).
# Not maintained and not run by tests; ~/pharos-labeling paths refer to the archive layout (see ../README.md).
"""Director: single-message latency (batch=1) of the int8 ONNX encoders on human items, 4 threads. 2026-10-29."""
import os, sys, time, statistics
sys.path.insert(0, os.path.expanduser("~/pharos-labeling/research/tracks/C-finetune/scripts"))
import numpy as np, onnxruntime as ort
from transformers import AutoTokenizer
from common import sample_items, sender_tag, Encoder, pad_batch
path, tokname = sys.argv[1], sys.argv[2]
tok = AutoTokenizer.from_pretrained(tokname)
rows = [r for r in sample_items(20000, seed=3) if r[1] == "human"][:300]
enc = Encoder(tok, max_len=512, ctx_max=64)
seqs = enc.encode_batch([sender_tag(r[1], r[2]) for r in rows], [r[3] or "" for r in rows], [r[4] for r in rows])
so = ort.SessionOptions(); so.intra_op_num_threads = 4; so.inter_op_num_threads = 1
t0 = time.time(); s = ort.InferenceSession(path, so, providers=["CPUExecutionProvider"]); load = time.time() - t0
lat = []
for q in seqs[:20]: s.run(None, dict(zip(["ids", "att"], pad_batch([q], tok.pad_token_id or 0))))  # warm-up
for q in seqs:
    ids, att = pad_batch([q], tok.pad_token_id or 0); t = time.perf_counter(); s.run(None, {"ids": ids, "att": att}); lat.append((time.perf_counter() - t) * 1000)
L = [len(q) for q in seqs]
print(f"{os.path.basename(path)}: load {load*1000:.0f} ms; human items n={len(seqs)} median tokens {int(np.median(L))}; "
      f"latency ms median {statistics.median(lat):.1f}, p90 {np.percentile(lat,90):.1f}, p99 {np.percentile(lat,99):.1f}")
