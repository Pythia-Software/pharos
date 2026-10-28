#!/usr/bin/env python3
"""Smoke check of a Pharos labeling export (labeling-export-v1).

    python3 tools/labeling_smoke.py EXPORT_DIR [--out model.json] [--report report.json]

It checks the export manifest and file hashes and round-trips every byte
range to its text, fits a hashed character n-gram one-vs-rest logistic
regression on the train split's usable rows only, evaluates it on the test
and fresh splits, and writes the model (featurizer spec and weights) and a
report. It is a smoke check of the export and the downstream path, not a
claim about the quality a student model can reach.

Python 3 standard library only, so a training project can copy it without
Pharos. See docs/library-labeling.md.

The three steps are separate on purpose: verify_export reads every file to
check structure, train reads only inputs.jsonl, provenance.jsonl and
labels.jsonl (never a held-out file), and only evaluate reads
labels-heldout.jsonl.
"""

import argparse
import hashlib
import json
import math
import os
import random
import sys

EXPORT_VERSION = "labeling-export-v1"
MODEL_FORMAT = "pharos-labeling-smoke-model-v1"

INPUTS = "inputs.jsonl"
PROVENANCE = "provenance.jsonl"
LABELS = "labels.jsonl"
LABELS_HELDOUT = "labels-heldout.jsonl"
UNITS = "unit-labels.jsonl"
UNITS_HELDOUT = "unit-labels-heldout.jsonl"
MANIFEST = "export-manifest.json"
DATA_FILES = (INPUTS, PROVENANCE, LABELS, LABELS_HELDOUT, UNITS, UNITS_HELDOUT)
TRAINING_SPLITS = ("train", "dev")
EVALUATION_SPLITS = ("test", "fresh")

# The student-visible fields of inputs.jsonl; anything else is a leak.
INPUT_FIELDS = {
    "item_id", "dataset_id", "units_version", "unit_id", "split", "cohort", "actor_kind", "sender", "provider", "kind",
    "channel", "reasoning_format", "depth", "text", "context", "context_excerpt", "chunk_index", "chunk_count", "chars",
}

# The featurizer. Go implements the same (internal/archive/labeling_smoke_model.go);
# testdata/labeling-parity.json holds cases both must reproduce.
FEATURIZER = {
    "name": "hashed-char-ngrams-v1",
    "input": "text",
    "units": "code_points",
    "lowercase": "ascii",
    "ngram_min": 2,
    "ngram_max": 4,
    "hash": "fnv1a64-utf8",
    "buckets": 1 << 18,
    "values": "l2-normalized-counts",
}
TRAINING = {"algorithm": "one-vs-rest logistic regression, SGD", "epochs": 8, "learning_rate": 0.5, "l2": 1e-4, "seed": 20261027}
THRESHOLD = 0.5
MIN_EVAL_POSITIVES = 3
# Below these sizes the numbers say nothing; the run still checks structure.
MEANINGFUL_TRAIN_ROWS = 200
MEANINGFUL_EVAL_ROWS = 50

FNV_OFFSET = 0xCBF29CE484222325
FNV_PRIME = 0x100000001B3
MASK64 = (1 << 64) - 1


class ExportError(Exception):
    """The export does not hold what its manifest promises."""


def ascii_lower(text):
    """Lowercase A-Z only: no locale or Unicode case mapping (Turkish İ stays İ)."""
    return "".join(chr(ord(c) + 32) if "A" <= c <= "Z" else c for c in text)


def fnv1a64(data):
    h = FNV_OFFSET
    for byte in data:
        h ^= byte
        h = (h * FNV_PRIME) & MASK64
    return h


def features(text):
    """Sorted (bucket, value) pairs: code point n-grams (2-4) of the ASCII-
    lowercased text, each hashed as its UTF-8 bytes, counted, L2-normalized."""
    points = ascii_lower(text)
    counts = {}
    buckets = FEATURIZER["buckets"]
    for n in range(FEATURIZER["ngram_min"], FEATURIZER["ngram_max"] + 1):
        for start in range(0, len(points) - n + 1):
            bucket = fnv1a64(points[start:start + n].encode("utf-8")) % buckets
            counts[bucket] = counts.get(bucket, 0) + 1
    if not counts:
        return []
    total = 0.0
    for bucket in sorted(counts):
        total += float(counts[bucket]) * float(counts[bucket])
    norm = math.sqrt(total)
    return [(bucket, counts[bucket] / norm) for bucket in sorted(counts)]


def sigmoid(z):
    if z >= 0:
        return 1.0 / (1.0 + math.exp(-z))
    e = math.exp(z)
    return e / (1.0 + e)


def linear(bias, weights, feats):
    """bias + sum of weight * value, in ascending bucket order (Go sums the same way)."""
    z = bias
    for bucket, value in feats:
        w = weights.get(bucket)
        if w is not None:
            z += w * value
    return z


def score_text(model, text):
    """Each label's probability for text under a model as written by train."""
    feats = features(text)
    scores = {}
    for entry in model["labels"]:
        weights = {bucket: w for bucket, w in entry["weights"]}
        scores[entry["label"]] = sigmoid(linear(entry["bias"], weights, feats))
    return scores


# ---------------------------------------------------------------- reading


def read_jsonl(path):
    """Rows of a JSON-lines file, decoded as strict UTF-8."""
    with open(path, "rb") as handle:
        data = handle.read()
    try:
        text = data.decode("utf-8")
    except UnicodeDecodeError as error:
        raise ExportError("%s is not valid UTF-8: %s" % (os.path.basename(path), error))
    rows = []
    for number, line in enumerate(text.split("\n")[:-1] if text.endswith("\n") else text.split("\n"), 1):
        if line == "":
            continue
        try:
            rows.append(json.loads(line))
        except ValueError as error:
            raise ExportError("%s line %d: %s" % (os.path.basename(path), number, error))
    return rows


def load_manifest(export_dir):
    path = os.path.join(export_dir, MANIFEST)
    try:
        with open(path, "rb") as handle:
            manifest = json.loads(handle.read().decode("utf-8"))
    except (OSError, ValueError) as error:
        raise ExportError("cannot read %s: %s" % (MANIFEST, error))
    if manifest.get("export_version") != EXPORT_VERSION:
        raise ExportError("export version %r, this script reads %s" % (manifest.get("export_version"), EXPORT_VERSION))
    return manifest


def slice_text(data, start, end, what):
    """The text of bytes [start, end) of data, which must be whole characters."""
    if not (isinstance(start, int) and isinstance(end, int) and 0 <= start <= end <= len(data)):
        raise ExportError("%s: range %r..%r outside %d bytes" % (what, start, end, len(data)))
    try:
        return data[start:end].decode("utf-8")
    except UnicodeDecodeError:
        raise ExportError("%s: range %d..%d is not on character boundaries" % (what, start, end))


def sha256(data):
    return hashlib.sha256(data).hexdigest()


# ------------------------------------------------------------ verification


def verify_export(export_dir):
    """Checks file hashes, row counts, held-out separation and every byte
    range against its text. Returns a summary; raises ExportError."""
    manifest = load_manifest(export_dir)
    files = manifest.get("files", {})
    for name in DATA_FILES:
        if name not in files:
            raise ExportError("the manifest lists no %s" % name)
    for name, expected in sorted(files.items()):
        try:
            with open(os.path.join(export_dir, name), "rb") as handle:
                data = handle.read()
        except OSError as error:
            raise ExportError("cannot read %s: %s" % (name, error))
        if sha256(data) != expected["sha256"] or len(data) != expected["bytes"]:
            raise ExportError("%s does not match the manifest's hash" % name)
        if data.count(b"\n") != expected["rows"]:
            raise ExportError("%s has %d rows, the manifest says %d" % (name, data.count(b"\n"), expected["rows"]))

    inputs = read_jsonl(os.path.join(export_dir, INPUTS))
    texts, splits, order = {}, {}, []
    for row in inputs:
        extra = set(row) - INPUT_FIELDS
        if extra:
            raise ExportError("inputs.jsonl row %s has fields a student must not see: %s" % (row.get("item_id"), sorted(extra)))
        item = row["item_id"]
        if item in texts:
            raise ExportError("item %s is in inputs.jsonl twice" % item)
        texts[item] = row["text"].encode("utf-8")
        splits[item] = row["split"]
        order.append(item)
        if not isinstance(row["context"], str):
            raise ExportError("item %s: context is not text" % item)
    if order != sorted(order):
        raise ExportError("inputs.jsonl is not in item order")

    checked = {"items": len(order), "source_ranges": 0, "transformed_ranges": 0, "markers": 0, "evidence_occurrences": 0}
    provenance = read_jsonl(os.path.join(export_dir, PROVENANCE))
    if [row["item_id"] for row in provenance] != order:
        raise ExportError("provenance.jsonl does not hold the items of inputs.jsonl in order")
    for row in provenance:
        item = row["item_id"]
        data = texts[item]
        what = "item %s" % item
        start, end = row["visible_range"]
        if end - start != len(data):
            raise ExportError("%s: visible range %d..%d is not its text's %d bytes" % (what, start, end, len(data)))
        for source in row["source_ranges"]:
            shown = slice_text(data, source["item_start"], source["item_end"], what).encode("utf-8")
            if source.get("transform"):
                checked["transformed_ranges"] += 1
                if source.get("whole_piece") and sha256(shown) != source["piece_sha256"]:
                    raise ExportError("%s: transformed piece %d..%d does not match its hash" % (what, source["start"], source["end"]))
            else:
                checked["source_ranges"] += 1
                if sha256(shown) != source["sha256"]:
                    raise ExportError("%s: bytes %d..%d differ from the message's %d..%d" % (
                        what, source["item_start"], source["item_end"], source["start"], source["end"]))
        for marker_start, marker_end in row["markers"]:
            slice_text(data, marker_start, marker_end, what + " marker")
            checked["markers"] += 1

    seen = set()
    for name, allowed in ((LABELS, TRAINING_SPLITS), (LABELS_HELDOUT, None)):
        for row in read_jsonl(os.path.join(export_dir, name)):
            item = row["item_id"]
            if item not in texts or item in seen:
                raise ExportError("%s: item %s is unknown or repeated" % (name, item))
            seen.add(item)
            if row["split"] != splits[item]:
                raise ExportError("%s: item %s split %s, inputs say %s" % (name, item, row["split"], splits[item]))
            in_training = row["split"] in TRAINING_SPLITS
            if (allowed is not None) != in_training:
                raise ExportError("%s holds item %s of split %s" % (name, item, row["split"]))
            usable = row["status"] == "usable"
            if usable != isinstance(row["labels"], list) or (row["labels"] is None) != (row["implied_ancestors"] is None):
                raise ExportError("%s: item %s has status %s but labels %r" % (name, item, row["status"], row["labels"]))
            for evidence in row["evidence"]:
                for occurrence in evidence.get("occurrences") or []:
                    quote = slice_text(texts[item], occurrence["item_start"], occurrence["item_end"], "item %s evidence" % item)
                    if quote != evidence["quote"]:
                        raise ExportError("item %s: evidence %r is %r at its range" % (item, evidence["quote"], quote))
                    checked["evidence_occurrences"] += 1
    if seen != set(texts):
        raise ExportError("labels files miss %d items of inputs.jsonl" % len(set(texts) - seen))
    for name, training in ((UNITS, True), (UNITS_HELDOUT, False)):
        for row in read_jsonl(os.path.join(export_dir, name)):
            if (row["split"] in TRAINING_SPLITS) != training or not row.get("unvalidated_merge"):
                raise ExportError("%s: unit %s (split %s) is in the wrong file or not flagged" % (name, row["unit_id"], row["split"]))
    return {"manifest": manifest, "checked": checked}


# ---------------------------------------------------------------- training


def taxonomy_parents(manifest):
    return {node["id"]: node["parent"] for node in manifest["taxonomy"]}


def ancestors(label, parents):
    out = []
    parent = parents.get(label, "")
    while parent:
        out.append(parent)
        parent = parents.get(parent, "")
    return out


def label_target(row, label, parents):
    """1 or 0 for a usable row, None when the row says nothing about label:
    uncertain, unsupported, an implied ancestor, or a child of a returned
    broad parent (its subtype was left open)."""
    if label in row["labels"]:
        return 1
    if label in row["uncertain_labels"] or label in row["unsupported_labels"] or label in row["implied_ancestors"]:
        return None
    for ancestor in ancestors(label, parents):
        if ancestor in row["labels"]:
            return None
    return 0


def examples(export_dir, labels_file, splits, exclude_seen_in_train):
    """Usable rows of labels_file in the given splits, with their text and
    strata, delegation and fork copies dropped. Reads inputs.jsonl,
    provenance.jsonl and labels_file only."""
    provenance = {row["item_id"]: row for row in read_jsonl(os.path.join(export_dir, PROVENANCE))}
    texts = {row["item_id"]: row["text"] for row in read_jsonl(os.path.join(export_dir, INPUTS))}
    rows, skipped = [], {"not_usable": 0, "duplicate": 0, "seen_in_train": 0}
    for row in read_jsonl(os.path.join(export_dir, labels_file)):
        if row["split"] not in splits:
            continue
        origin = provenance[row["item_id"]]
        if row["status"] != "usable":
            skipped["not_usable"] += 1
        elif origin["duplicate_of"]:
            skipped["duplicate"] += 1
        elif exclude_seen_in_train and origin["seen_in_train"]:
            skipped["seen_in_train"] += 1
        else:
            rows.append(dict(row, text=texts[row["item_id"]], common_short_text=origin["common_short_text"]))
    rows.sort(key=lambda row: row["item_id"])
    return rows, skipped


def train(export_dir, manifest):
    """Fits the model on the train split's usable rows of labels.jsonl."""
    rows, skipped = examples(export_dir, LABELS, ("train",), False)
    parents = taxonomy_parents(manifest)
    feats = [features(row["text"]) for row in rows]
    model = {"format": MODEL_FORMAT, "featurizer": dict(FEATURIZER), "threshold": THRESHOLD, "training": dict(TRAINING),
             "export_id": manifest["export_id"], "train_rows": len(rows), "train_skipped": skipped, "labels": [], "untrained_labels": []}
    for node in manifest["taxonomy"]:
        label = node["id"]
        data = [(f, label_target(row, label, parents)) for f, row in zip(feats, rows)]
        data = [(f, y) for f, y in data if y is not None]
        positives = sum(y for _, y in data)
        if positives == 0 or positives == len(data):
            model["untrained_labels"].append({"label": label, "positives": positives, "rows": len(data)})
            continue
        bias, weights = 0.0, {}
        order = list(range(len(data)))
        rng = random.Random(TRAINING["seed"])
        rate, l2 = TRAINING["learning_rate"], TRAINING["l2"]
        for _ in range(TRAINING["epochs"]):
            rng.shuffle(order)
            for index in order:
                f, y = data[index]
                gradient = sigmoid(linear(bias, weights, f)) - y
                bias -= rate * gradient
                for bucket, value in f:
                    w = weights.get(bucket, 0.0)
                    weights[bucket] = w - rate * (gradient * value + l2 * w)
        model["labels"].append({"label": label, "positives": positives, "rows": len(data), "bias": bias,
                                "weights": [[bucket, weights[bucket]] for bucket in sorted(weights) if weights[bucket] != 0.0]})
    return model


# -------------------------------------------------------------- evaluation


def metrics(rows, model, parents, labels):
    scored = {}
    entries = {entry["label"]: entry for entry in model["labels"]}
    weights = {label: {bucket: w for bucket, w in entry["weights"]} for label, entry in entries.items()}
    feats = [features(row["text"]) for row in rows]
    per_label, micro = {}, [0, 0, 0]
    for label in labels:
        tp = fp = fn = support = 0
        for f, row in zip(feats, rows):
            y = label_target(row, label, parents)
            if y is None:
                continue
            p = 0.0
            if label in entries:
                p = sigmoid(linear(entries[label]["bias"], weights[label], f))
            predicted = p >= THRESHOLD
            support += y
            tp += int(predicted and y == 1)
            fp += int(predicted and y == 0)
            fn += int(not predicted and y == 1)
        if support == 0 and tp + fp == 0:
            continue
        if support < MIN_EVAL_POSITIVES:
            per_label[label] = {"support": support, "scored": False, "reason": "fewer than %d positives" % MIN_EVAL_POSITIVES}
            continue
        precision = tp / (tp + fp) if tp + fp else 0.0
        recall = tp / (tp + fn) if tp + fn else 0.0
        f1 = 2 * precision * recall / (precision + recall) if precision + recall else 0.0
        per_label[label] = {"support": support, "scored": True, "trained": label in entries, "precision": precision, "recall": recall, "f1": f1}
        scored[label] = f1
        micro[0] += tp
        micro[1] += fp
        micro[2] += fn
    tp, fp, fn = micro
    precision = tp / (tp + fp) if tp + fp else 0.0
    recall = tp / (tp + fn) if tp + fn else 0.0
    return {
        "rows": len(rows),
        "scored_labels": len(scored),
        "micro": {"precision": precision, "recall": recall, "f1": 2 * precision * recall / (precision + recall) if precision + recall else 0.0},
        "macro_f1": sum(scored.values()) / len(scored) if scored else None,
        "labels": per_label,
    }


def evaluate(export_dir, manifest, model):
    """Scores the held-out test and fresh splits, without seen_in_train rows,
    with common_short_text reported as its own stratum."""
    parents = taxonomy_parents(manifest)
    labels = [node["id"] for node in manifest["taxonomy"]]
    report = {}
    for split in EVALUATION_SPLITS:
        rows, skipped = examples(export_dir, LABELS_HELDOUT, (split,), True)
        if not rows and not any(skipped.values()):
            continue
        short = [row for row in rows if row["common_short_text"]]
        other = [row for row in rows if not row["common_short_text"]]
        report[split] = {"skipped": skipped, "all": metrics(rows, model, parents, labels),
                         "common_short_text": metrics(short, model, parents, labels), "other": metrics(other, model, parents, labels)}
    return report


# -------------------------------------------------------------------- main


def run(export_dir, out, report_path):
    verified = verify_export(export_dir)
    manifest = verified["manifest"]
    model = train(export_dir, manifest)
    evaluation = evaluate(export_dir, manifest, model)
    warnings = []
    if model["train_rows"] < MEANINGFUL_TRAIN_ROWS:
        warnings.append("only %d usable train rows: too small to train meaningfully; the numbers below say nothing about quality" % model["train_rows"])
    for split in EVALUATION_SPLITS:
        rows = evaluation.get(split, {}).get("all", {}).get("rows", 0)
        if rows < MEANINGFUL_EVAL_ROWS:
            warnings.append("only %d usable %s rows to evaluate on" % (rows, split))
    report = {
        "statement": "Smoke check of the export and the training path, not a quality claim: a hashed character n-gram baseline "
                     "trained on the train split and scored on held-out splits.",
        "export_id": manifest["export_id"],
        "checked": verified["checked"],
        "train_rows": model["train_rows"],
        "train_skipped": model["train_skipped"],
        "trained_labels": len(model["labels"]),
        "untrained_labels": len(model["untrained_labels"]),
        "evaluation": evaluation,
        "warnings": warnings,
    }
    write_json(out, model)
    write_json(report_path, report)
    return report


def write_json(path, value):
    directory = os.path.dirname(os.path.abspath(path))
    os.makedirs(directory, exist_ok=True)
    temporary = path + ".tmp"
    with open(temporary, "w", encoding="utf-8") as handle:
        json.dump(value, handle, indent=1, sort_keys=True)
        handle.write("\n")
    os.replace(temporary, path)


def summary(report):
    lines = ["Smoke check, not a quality claim.",
             "Export %s: %d items; byte ranges round-tripped: %d source, %d transformed, %d markers, %d evidence." % (
                 report["export_id"], report["checked"]["items"], report["checked"]["source_ranges"],
                 report["checked"]["transformed_ranges"], report["checked"]["markers"], report["checked"]["evidence_occurrences"]),
             "Trained %d labels on %d usable train rows (%d labels without both classes)." % (
                 report["trained_labels"], report["train_rows"], report["untrained_labels"])]
    for split, result in sorted(report["evaluation"].items()):
        overall = result["all"]
        macro = "n/a" if overall["macro_f1"] is None else "%.3f" % overall["macro_f1"]
        lines.append("%s: %d rows, %d labels scored, micro F1 %.3f, macro F1 %s (skipped %s)." % (
            split, overall["rows"], overall["scored_labels"], overall["micro"]["f1"], macro, json.dumps(result["skipped"], sort_keys=True)))
    lines.extend("Warning: " + warning for warning in report["warnings"])
    return "\n".join(lines)


def main(argv=None):
    parser = argparse.ArgumentParser(description="Smoke check of a Pharos labeling export (not a quality claim).")
    parser.add_argument("export_dir")
    parser.add_argument("--out", default="labeling-smoke-model.json", help="model JSON to write")
    parser.add_argument("--report", help="report JSON to write (default: beside the model, .report.json)")
    args = parser.parse_args(argv)
    report_path = args.report or (args.out[:-5] if args.out.endswith(".json") else args.out) + ".report.json"
    try:
        report = run(args.export_dir, args.out, report_path)
    except ExportError as error:
        print("Export check failed: %s" % error, file=sys.stderr)
        return 1
    print(summary(report))
    print("Wrote %s and %s" % (args.out, report_path))
    return 0


if __name__ == "__main__":
    sys.exit(main())
