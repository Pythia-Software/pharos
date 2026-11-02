# Frozen research snapshot, copied unchanged below this line from ~/pharos-labeling/studies/KESTREL/export/kestrel-68m/reference.py (2026-10-30).
# Not maintained and not run by tests; ~/pharos-labeling paths refer to the archive layout (see ../README.md).
"""KESTREL labeler, Python reference: text item -> token ids -> probabilities -> labels, with onnxruntime.
Needs only numpy, tokenizers and onnxruntime, plus this directory's manifest.json, tokenizer.json, kframe.py and model.*.onnx.

    from reference import Labeler
    lab = Labeler(".", variant="int8")            # or "fp32"
    out = lab.label([{"segment": "H-typed", "cohort": "human", "sender": "user",
                      "context": "Previous assistant message: ...", "text": "please add a retry"}])
    out[0]["labels"]  -> closed label ids;  out[0]["probs"] -> 54 floats;  out[0]["input_ids"] -> token ids

CLI: python reference.py DIR ITEMS.jsonl [--variant int8]   (one JSON item per line; prints one JSON result per line)."""
import json, os, sys
import numpy as np
import onnxruntime as ort
from tokenizers import Tokenizer
HERE = os.path.dirname(os.path.abspath(__file__)); sys.path.insert(0, HERE)
import kframe


class Labeler:
    def __init__(self, d=HERE, variant="int8", threads=4):
        self.man = json.load(open(os.path.join(d, "manifest.json")))
        assert self.man["framing_contract"] == kframe.CONTRACT, "framing contract mismatch"
        self.tok = Tokenizer.from_file(os.path.join(d, "tokenizer.json"))
        so = ort.SessionOptions(); so.intra_op_num_threads = threads
        self.sess = ort.InferenceSession(os.path.join(d, self.man["files"][f"onnx_{variant}"]), so, providers=["CPUExecutionProvider"])
        self.order = self.man["labels"]["order"]
        anc = self.man["labels"]["ancestors"]
        self.A = np.zeros((len(self.order), len(self.order)), bool)  # A[j, k]: k is j or an ancestor of j
        for j, l in enumerate(self.order):
            self.A[j, j] = True
            for x in anc[l]:
                self.A[j, self.order.index(x)] = True
        self.th = {k: np.asarray(v, np.float32) for k, v in self.man["thresholds"].items()}

    def encode(self, it):
        return kframe.encode(self.tok, it["segment"], it["cohort"], it.get("sender", ""), it.get("context", ""), it["text"])

    def run(self, seqs):
        ids, att = kframe.pad(seqs)
        lo, p = self.sess.run(None, {"input_ids": np.asarray(ids, np.int64), "attention_mask": np.asarray(att, np.int64)})
        return lo, p, ids, att

    def decide(self, p, segment):
        on = p >= self.th.get(segment, self.th["default"])
        closed = (on[:, None] & self.A).any(0)  # an 'on' label adds itself and its ancestors
        return [l for l, c in zip(self.order, closed) if c]

    def label(self, items, batch=1):
        out = []
        for i in range(0, len(items), batch):
            chunk = items[i:i + batch]; seqs = [self.encode(x) for x in chunk]
            lo, p, ids, att = self.run(seqs)
            for k, x in enumerate(chunk):
                out.append({"input_ids": seqs[k], "probs": p[k].tolist(), "logits": lo[k].tolist(), "labels": self.decide(p[k], x["segment"])})
        return out


if __name__ == "__main__":
    d, f = sys.argv[1], sys.argv[2]; v = sys.argv[sys.argv.index("--variant") + 1] if "--variant" in sys.argv else "int8"
    lab = Labeler(d, v)
    for line in open(f):
        r = lab.label([json.loads(line)])[0]
        print(json.dumps({"labels": r["labels"], "probs": [round(x, 5) for x in r["probs"]]}))
