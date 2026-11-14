#!/usr/bin/env python3
"""Write testdata/labeler/token-labels-parity.json.gz from the NUTHATCH reference.

Run with the study's interpreter, which has tokenizers and onnxruntime:

    ~/pharos-labeling/studies/NUTHATCH/.venv/bin/python tools/engagement_parity_fixture.py \
        --study ~/pharos-labeling/studies/NUTHATCH --model models/dev0/onnx

Every text here is invented for the fixture. The file records, per case, what
the Python reference (scripts/span_framing.py, the `tokenizers` library and
ONNX Runtime) produces: token ids, offsets in code points and in bytes, the
windows, the merged probabilities at or above 0.3 and the decoded spans.
Go checks its tokenizer, framing and decoding against it without the model
(internal/labeler/tokenlabels_test.go), and its worker with the model when
PHAROS_ENGAGEMENT_MODEL_DIR is set.
"""
import argparse, gzip, hashlib, json, sys
from pathlib import Path

SENTENCES = [
    "No, don't open the pull request yet.",
    "First show me the diff, because the last change broke the importer.",
    "Add a --dry-run flag to the export command and write a test for it.",
    "I checked run {n} myself and the totals looked wrong.",
    "Never edit the generated bundle; change the source in web/src instead.",
    "Why did you pick a map here rather than a sorted slice?",
    "That looks right to me, thanks, go ahead and merge it.",
    "The goal is that a new user can install this in under {n} minutes.",
    "You can run the fast tests without asking, but ask before you push.",
    "This is for my manager, so keep it short and skip the jargon.",
    "Hmm, I'm not sure that's true; can you verify it against the logs?",
    "Step {n}: rename the column, then update the docs, then tell me what changed.",
]


def long_text(count, seed):
    return " ".join(SENTENCES[(i * 5 + seed) % len(SENTENCES)].format(n=i + seed) for i in range(count))


LONG = long_text(150, 1)
LONG_CONTEXT = " ".join(f"I updated file_{i}.go and ran the fast tests; {i} passed." for i in range(1, 30))

CASES = [
    ("plain", "I ran the tests and they pass. Want me to open a pull request?",
     "No, don't open it yet. First show me the diff, because the last change broke the importer."),
    ("no-context", "", "Add a --dry-run flag to the export command and write a test for it."),
    ("spaces", "Done.", "  \n  now also update the docs   \n\nand   keep the old name\n"),
    ("unicode", "Voilà — c'est prêt.", "Great 🙂 but the café naïve test fails 👍🏽; see 日本語のログ and fix — carefully…"),
    ("markers", "Which file?", "use this one ⟦pasted⟧ and never edit ⟦harness⟧ the generated bundle ⟦pasted⟧"),
    ("marker-first", "", "⟦attachment⟧ summarize this for my manager, in two sentences"),
    ("not-nfc", "ok", "the résumé parser drops café names; Ångström too"),
    ("not-nfc-marks", "", "a\u0323\u0302b \u1100\u1161\u11a8 z q\u0307\u0323 r \u212b ok e\u0301\u0301 f"),
    ("code", "Here is the function.", "why is it\n\tif x == 1:\n        return   None\n                                # and this?"),
    ("specials", "", "the literal [SEP] token and [CLS] show up in my data, is that a problem?"),
    ("one-token", "Should I continue?", "ok"),
    ("empty", "Anything else?", ""),
    ("long", LONG_CONTEXT, LONG),
    ("long-no-context", "", long_text(110, 4)),
]

SMALL = dict(max_seq=24, max_context=6, overlap=4)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--study", required=True)
    ap.add_argument("--model", default="models/dev0/onnx")
    ap.add_argument("--out", default=str(Path(__file__).resolve().parent.parent / "testdata/labeler/token-labels-parity.json.gz"))
    ap.add_argument("--floor", type=float, default=0.3)
    a = ap.parse_args()
    study = Path(a.study).expanduser()
    sys.path.insert(0, str(study / "scripts"))
    import numpy as np
    import span_framing as F
    import span_common as C
    from tokenizers import Tokenizer

    model = study / a.model
    meta = json.loads((model / "model.json").read_text())
    tok = Tokenizer.from_file(str(model / "tokenizer.json"))
    run = C.onnx_runner(model / meta["variants"]["fp32"], threads=4)
    cls, sep, pad = meta["cls_id"], meta["sep_id"], meta["pad_id"]
    labels = meta["labels"]

    def digest(ids):
        return hashlib.sha256(",".join(map(str, ids)).encode()).hexdigest()[:16]

    def byte_at(text, chars):
        return len(text[:chars].encode("utf-8"))

    cases = []
    for name, context, text in CASES:
        te = tok.encode(text, add_special_tokens=False)
        ce = tok.encode(context, add_special_tokens=False)
        windows = F.frame_windows(ce.ids, te.ids, cls, sep, meta["max_seq_tokens"], meta["max_context_tokens"], meta["window_overlap_tokens"])
        parts = []
        for ids, pos, start, end in windows:
            i, m = C.pad_batch([ids], pad)
            parts.append((start, run(i, m)[0, pos:pos + end - start]))
        probs = F.merge_windows(len(te.ids), len(labels), [(s, rows.tolist()) for s, rows in parts])
        spans = F.decode_spans(text, te.offsets, probs, [0.5] * len(labels))
        entries = [[t, l, round(float(p), 6)] for l in range(len(labels)) for t, row in enumerate(probs) for p in [row[l]] if p >= a.floor]
        small = F.frame_windows(ce.ids, te.ids, cls, sep, **SMALL)
        cases.append({
            "name": name, "context": context, "text": text,
            "context_ids": ce.ids, "text_ids": te.ids,
            "offsets": [list(o) for o in te.offsets],
            "byte_offsets": [[byte_at(text, s), byte_at(text, e)] for s, e in te.offsets],
            "windows": [{"tokens": len(ids), "text_pos": pos, "start": s, "end": e, "ids_sha": digest(ids)} for ids, pos, s, e in windows],
            "small_windows": [{"tokens": len(ids), "text_pos": pos, "start": s, "end": e, "ids_sha": digest(ids)} for ids, pos, s, e in small],
            "entries": entries,
            "spans": [{"label": labels[l], "start": s, "end": e, "start_byte": byte_at(text, s), "end_byte": byte_at(text, e), "score": round(float(p), 6)}
                      for l, s, e, p in spans],
        })
    out = {
        "note": "Invented texts; written by tools/engagement_parity_fixture.py from the NUTHATCH reference.",
        "contract": meta["contract"], "framing": meta["framing"], "labels": labels, "floor": a.floor, "threshold": 0.5,
        "model_sha256": meta["files"][meta["variants"]["fp32"]]["sha256"], "tokenizer_sha256": meta["files"]["tokenizer.json"]["sha256"],
        "max_seq_tokens": meta["max_seq_tokens"], "max_context_tokens": meta["max_context_tokens"], "window_overlap_tokens": meta["window_overlap_tokens"],
        "cls_id": cls, "sep_id": sep, "small": SMALL, "cases": cases,
    }
    with gzip.GzipFile(a.out, "wb", mtime=0) as f:
        f.write((json.dumps(out, ensure_ascii=False, separators=(",", ":")) + "\n").encode("utf-8"))
    print(a.out, {c["name"]: (len(c["text_ids"]), len(c["windows"]), len(c["entries"]), len(c["spans"])) for c in cases})


if __name__ == "__main__":
    main()
