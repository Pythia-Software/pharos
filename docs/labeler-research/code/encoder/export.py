# Frozen research snapshot, copied unchanged below this line from ~/pharos-labeling/studies/KESTREL/scripts/export.py (2026-10-30).
# Not maintained and not run by tests; ~/pharos-labeling paths refer to the archive layout (see ../README.md).
"""Export a trained encoder for product integration: ONNX fp32, weight-only int8 (MatMulNBits, 8 bits, block 32,
accuracy_level 4, symmetric; embeddings stay fp32, as round-1 C-finetune quant.py), tokenizer files, manifest.json.
usage: export.py RUN NAME [--thr-run RUN] [--metrics results/final_metrics.json]
  RUN: k:<kestrel run> or BOBOLINK run (dry runs). Writes export/NAME/.
ONNX graph: inputs input_ids int64 [batch, seq], attention_mask int64 [batch, seq];
outputs logits float32 [batch, 5, 54] (raw head logits), probs float32 [batch, 54] = 0.5*(sigmoid(logits[:,4]) + sigmoid(logits[:,0]))."""
import argparse, hashlib, json, os, shutil, sys, time
HERE = os.path.dirname(os.path.abspath(__file__)); sys.path.insert(0, HERE)
import numpy as np, torch, onnx, onnxruntime as ort
from onnxruntime.quantization import matmul_nbits_quantizer as mq
from transformers import AutoTokenizer, AutoModel
import kdata, kframe
sys.path.insert(0, os.path.expanduser("~/pharos-labeling/research/harness")); import rlh
ap = argparse.ArgumentParser(); ap.add_argument("run"); ap.add_argument("name"); ap.add_argument("--thr", default="")
ap.add_argument("--metrics", default=""); a = ap.parse_args()
K, ST = kdata.K, kdata.ST
if a.run.startswith("k:"):
    d = f"{K}/models/{a.run[2:]}"; meta = json.load(open(f"{d}/meta.json")); MODEL = meta["model"]
else:
    d = f"{ST}/BOBOLINK/models/{a.run}"; meta = json.load(open(f"{d}/meta.json")); MODEL = meta["args"]["model"]
heads = meta["heads"]; assert heads == kdata.HEADS
OUT = f"{K}/export/{a.name}"; os.makedirs(OUT, exist_ok=True)


class Net(torch.nn.Module):
    def __init__(self):
        super().__init__()
        self.enc = AutoModel.from_pretrained(MODEL, attn_implementation="eager"); self.drop = torch.nn.Dropout(0.1)
        self.head = torch.nn.Linear(self.enc.config.hidden_size, 54 * len(heads))

    def forward(self, input_ids, attention_mask):
        h = self.enc(input_ids=input_ids, attention_mask=attention_mask).last_hidden_state
        m = attention_mask.unsqueeze(-1).to(h.dtype)
        lo = self.head((h * m).sum(1) / m.sum(1).clamp(min=1)).view(-1, len(heads), 54)
        p = 0.5 * (torch.sigmoid(lo[:, 4]) + torch.sigmoid(lo[:, 0]))
        return lo, p


net = Net(); net.load_state_dict(torch.load(f"{d}/final.pt", map_location="cpu")); net.eval()
tok = AutoTokenizer.from_pretrained(MODEL)
fp = f"{OUT}/model.fp32.onnx"; q8 = f"{OUT}/model.int8.onnx"
ex = [kframe.encode(tok, "H-typed", "human", "user", "Previous assistant message: done.", "please fix the failing test"),
      kframe.encode(tok, "AR", "agent_reply", "assistant", "", "I updated the parser and ran the suite. " * 20)]
ids, att = kframe.pad(ex)
ids, att = torch.tensor(ids), torch.tensor(att)
if not os.path.exists(fp):
    torch.onnx.export(net, (ids, att), fp, input_names=["input_ids", "attention_mask"], output_names=["logits", "probs"],
                      dynamic_axes={"input_ids": {0: "batch", 1: "seq"}, "attention_mask": {0: "batch", 1: "seq"},
                                    "logits": {0: "batch"}, "probs": {0: "batch"}}, opset_version=17, dynamo=False)
    print("wrote", fp, round(os.path.getsize(fp) / 1e6, 1), "MB", flush=True)
if not os.path.exists(q8):
    m = onnx.load(fp)
    q = mq.MatMulNBitsQuantizer(m, bits=8, block_size=32, is_symmetric=True, accuracy_level=4)
    q.process(); q.model.save_model_to_file(q8, use_external_data_format=False)
    print("wrote", q8, round(os.path.getsize(q8) / 1e6, 1), "MB", flush=True)
# embeddings stay fp32: check no Gather on a quantized initializer
qm = onnx.load(q8); ops = {}
for n in qm.graph.node: ops[n.op_type] = ops.get(n.op_type, 0) + 1
emb_fp32 = any(i.data_type == onnx.TensorProto.FLOAT and len(i.dims) == 2 and i.dims[0] >= 50000 for i in qm.graph.initializer)
# torch vs onnx agreement on the two examples
with torch.inference_mode():
    tl, tp = net(ids, att)
so = ort.SessionOptions(); so.intra_op_num_threads = 4
agree = {}
for name, path in (("fp32", fp), ("int8", q8)):
    s = ort.InferenceSession(path, so, providers=["CPUExecutionProvider"])
    lo, p = s.run(None, {"input_ids": ids.numpy(), "attention_mask": att.numpy()})
    agree[name] = {"max_abs_logit_diff_vs_torch": float(np.abs(lo - tl.numpy()).max()), "max_abs_prob_diff_vs_torch": float(np.abs(p - tp.numpy()).max())}
print("agreement", agree, flush=True)
tok.save_pretrained(OUT)  # tokenizer.json, tokenizer_config.json, special_tokens_map.json
tsha = hashlib.sha256(open(f"{OUT}/tokenizer.json", "rb").read()).hexdigest()
# thresholds: per segment from the scoring pass (kscore.thresholds), saved as npz {default, <segment>...}
th = {}
if a.thr:
    z = np.load(a.thr); th = {k: [round(float(v), 4) for v in z[k]] for k in z.files}
labels = rlh.labels_meta()
order = rlh.label_list(); par = rlh.parent_index()
seglist = list(kframe.HUMAN) + ["AIUR", "AR", "DEL", "TH"]
man = {
    "name": a.name, "created": time.strftime("%Y-%m-%dT%H:%M:%S%z"), "base_model": MODEL, "run": a.run,
    "framing_contract": kframe.CONTRACT, "framing_doc": "FRAMING.md",
    "files": {"onnx_fp32": "model.fp32.onnx", "onnx_int8": "model.int8.onnx", "tokenizer": "tokenizer.json",
              "reference": "reference.py", "parity_fixture": "parity-fixture.json"},
    "sha256": {f: hashlib.sha256(open(f"{OUT}/{f}", "rb").read()).hexdigest() for f in ("model.fp32.onnx", "model.int8.onnx", "tokenizer.json")},
    "tokenizer_sha256": tsha,
    "int8": {"method": "onnxruntime MatMulNBitsQuantizer, weight-only", "bits": 8, "block_size": 32, "symmetric": True,
             "accuracy_level": 4, "embeddings": "fp32 (Gather not quantized)", "embedding_table_fp32": bool(emb_fp32), "op_counts": ops},
    "onnx": {"opset": 17, "inputs": {"input_ids": "int64 [batch, seq]", "attention_mask": "int64 [batch, seq]"},
             "outputs": {"logits": "float32 [batch, 5, 54], raw head logits, head order below", "probs": "float32 [batch, 54], the scorer (below)"},
             "agreement_vs_torch": agree},
    "labels": {"order": order, "parent": [order[p] if p >= 0 else None for p in par],
               "ancestors": {l["id"]: l["ancestors"] for l in labels["labels"]}, "taxonomy": labels.get("taxonomy_version", labels.get("version"))},
    "heads": heads,
    "scorer": {"definition": "p = 0.5 * (sigmoid(logits[:, heads.index('y3')]) + sigmoid(logits[:, heads.index('y_hf')]))",
               "y3_index": heads.index("y3"), "y_hf_index": heads.index("y_hf"),
               "decision": "label j is on if p[j] >= thresholds[segment][j] (segment not listed -> 'default'); then closure: "
                           "every 'on' label adds all of its ancestors (labels.ancestors)."},
    "segments": seglist, "segment_rules": "see FRAMING.md (segment is known for every item before labeling)",
    "thresholds": th,
}
if a.metrics and os.path.exists(a.metrics):
    man.update(json.load(open(a.metrics)))
json.dump(man, open(f"{OUT}/manifest.json", "w"), indent=1)
for f in ("kframe.py", "reference.py", "make_fixture.py", "FRAMING.md"):
    shutil.copy(f"{HERE}/{f}", f"{OUT}/{f}")
print("manifest written", OUT)
