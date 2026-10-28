"""Tests of the labeling export smoke check on a tiny invented export."""

import builtins
import contextlib
import hashlib
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest import mock

import labeling_smoke as smoke

PARITY_FIXTURE = Path(__file__).resolve().parent.parent / "testdata" / "labeling-parity.json"

TAXONOMY = [
    {"id": "P", "parent": ""},
    {"id": "P.request", "parent": "P"},
    {"id": "P.request.work", "parent": "P.request"},
    {"id": "P.request.work.investigate", "parent": "P.request.work"},
    {"id": "P.request.work.verify", "parent": "P.request.work"},
    {"id": "C", "parent": ""},
    {"id": "C.report", "parent": "C"},
    {"id": "C.report.finding", "parent": "C.report"},
]
INVESTIGATE, FINDING, WORK = "P.request.work.investigate", "C.report.finding", "P.request.work"


def implied(labels):
    parents = {node["id"]: node["parent"] for node in TAXONOMY}
    out = set()
    for label in labels:
        parent = parents[label]
        while parent:
            if parent not in labels:
                out.add(parent)
            parent = parents[parent]
    return sorted(out)


def masked(labels, uncertain=(), unsupported=()):
    """The export's masked_labels rule, for invented rows."""
    parents = {node["id"]: node["parent"] for node in TAXONOMY}

    def descendants(label):
        return {node["id"] for node in TAXONOMY if label in lineage(node["id"])[1:]}

    def lineage(label):
        out = [label]
        while parents[out[-1]]:
            out.append(parents[out[-1]])
        return out

    out = set()
    for label in list(uncertain) + list(unsupported):
        out |= set(lineage(label)) | descendants(label)
    for label in labels:
        out |= descendants(label)
    for label in labels:
        out -= set(lineage(label))
    return sorted(out)


def item(number, split, text, labels=None, status="usable", uncertain=(), unsupported=None, seen_in_train=False,
         duplicate_of="", short=False, quote=None):
    """An invented item: its input, provenance and label rows."""
    item_id = "i_%03d" % number
    data = text.encode("utf-8")
    labels = list(labels or [])
    evidence = []
    if quote is not None:
        start = data.index(quote.encode("utf-8"))
        evidence.append({"label": labels[0] if labels else FINDING, "quote": quote, "status": "valid",
                         "occurrences": [{"item_start": start, "item_end": start + len(quote.encode("utf-8")), "source": []}]})
    usable = status in ("usable", "usable_need_context")
    return {
        "input": {"item_id": item_id, "dataset_id": "d", "units_version": "labeling-units-v1", "unit_id": "u_%03d" % number, "split": split,
                  "cohort": "human", "actor_kind": "human", "sender": "user", "provider": "claude", "kind": "message", "channel": "unspecified",
                  "reasoning_format": None, "depth": 0, "text": text, "context": "", "context_excerpt": False, "chunk_index": 0,
                  "chunk_count": 1, "chars": len(text)},
        "provenance": {"item_id": item_id, "unit_id": "u_%03d" % number, "split": split, "family_id": "f_%03d" % number,
                       "duplicate_of": duplicate_of, "length_bucket": "short", "seen_in_train": seen_in_train, "common_short_text": short, "cross_split_duplicate": False,
                       "visible_range": [0, len(data)],
                       "source_ranges": [{"start": 10, "end": 10 + len(data), "item_start": 0, "item_end": len(data),
                                          "sha256": hashlib.sha256(data).hexdigest()}],
                       "markers": []},
        "label": {"item_id": item_id, "unit_id": "u_%03d" % number, "split": split, "status": status, "outcome": "usable" if usable else status,
                  "labels": labels if usable else None, "uncertain_labels": list(uncertain) if usable else None,
                  "unsupported_labels": (unsupported or {}) if usable else None, "implied_ancestors": implied(labels) if usable else None,
                  "masked_labels": masked(labels, uncertain, unsupported or {}) if usable else None,
                  "need_context": False, "returned": None if usable else {"labels": labels, "uncertain_labels": []},
                  "evidence": evidence, "issues": [], "flags": []},
    }


def invented_items():
    investigate = ["please investigate why the build fails", "can you investigate the crash on start",
                   "investigate the slow query please", "please look into why tests time out"]
    finding = ["i found the bug in the parser", "found it: the cache key is wrong", "the root cause is a race in the queue",
               "i found that the config is never read"]
    items, number = [], 0
    for index in range(3):
        for text in investigate:
            number += 1
            items.append(item(number, "train", text + " " + "x" * index, [INVESTIGATE], quote=text.split()[0]))
        for text in finding:
            number += 1
            items.append(item(number, "train", text + " " + "y" * index, [FINDING]))
    # Rows that must never become negatives.
    for status in ("refused", "invalid", "missing", "usable_need_context", "held", "unlabeled"):
        number += 1
        items.append(item(number, "train", "please investigate the refusal " + status, [INVESTIGATE], status=status))
    number += 1
    items.append(item(number, "train", "i found the bug again, a duplicate", [FINDING], duplicate_of="m_1"))
    number += 1
    items.append(item(number, "dev", "please investigate the dev case", [INVESTIGATE]))
    for split in ("test", "fresh"):
        for text in investigate[:3]:
            number += 1
            items.append(item(number, split, text + " today", [INVESTIGATE]))
        number += 1
        items.append(item(number, split, finding[0], [FINDING], seen_in_train=True))
        number += 1
        items.append(item(number, split, "ok", [], short=True))
    return items


def write_export(directory, items, units=(), tamper=None):
    """Writes an invented export with a correct manifest; tamper may edit
    the rows before the manifest is computed."""
    directory = Path(directory)
    files = {name: [] for name in smoke.DATA_FILES}
    for entry in sorted(items, key=lambda entry: entry["input"]["item_id"]):
        files[smoke.INPUTS].append(entry["input"])
        files[smoke.PROVENANCE].append(entry["provenance"])
        heldout = entry["label"]["split"] not in smoke.TRAINING_SPLITS
        files[smoke.LABELS_HELDOUT if heldout else smoke.LABELS].append(entry["label"])
    for unit in units:
        files[smoke.UNITS_HELDOUT if unit["split"] not in smoke.TRAINING_SPLITS else smoke.UNITS].append(unit)
    if tamper:
        tamper(files)
    manifest_files = {}
    for name, rows in files.items():
        data = "".join(json.dumps(row, ensure_ascii=False) + "\n" for row in rows).encode("utf-8")
        (directory / name).write_bytes(data)
        manifest_files[name] = {"sha256": hashlib.sha256(data).hexdigest(), "rows": len(rows), "bytes": len(data)}
    heldout = json.dumps({"label_support": {}}).encode("utf-8")
    (directory / smoke.HELDOUT_MANIFEST).write_bytes(heldout)
    manifest_files[smoke.HELDOUT_MANIFEST] = {"sha256": hashlib.sha256(heldout).hexdigest(), "bytes": len(heldout)}
    manifest = {"export_version": smoke.EXPORT_VERSION, "export_id": "e_test", "files": manifest_files, "taxonomy": TAXONOMY}
    (directory / smoke.MANIFEST).write_text(json.dumps(manifest))
    return directory


class FeaturizerTests(unittest.TestCase):
    def test_fnv1a64_reference_vectors(self):
        self.assertEqual(smoke.fnv1a64(b""), 0xCBF29CE484222325)
        self.assertEqual(smoke.fnv1a64(b"a"), 0xAF63DC4C8601EC8C)
        self.assertEqual(smoke.fnv1a64(b"foobar"), 0x85944171F73967E8)

    def test_lowercases_ascii_only(self):
        self.assertEqual(smoke.ascii_lower("İI ıi ÀB"), "İi ıi Àb")

    def test_matches_the_shared_parity_fixture(self):
        fixture = json.loads(PARITY_FIXTURE.read_text(encoding="utf-8"))
        self.assertEqual(fixture, build_parity_fixture(), "testdata/labeling-parity.json differs from the featurizer")
        model = fixture["model"]
        for case in fixture["cases"]:
            got = smoke.features(case["text"])
            self.assertEqual([bucket for bucket, _ in got], [bucket for bucket, _ in case["features"]], case["name"])
            for (_, value), (_, want) in zip(got, case["features"]):
                self.assertEqual(value, want, case["name"])
            for label, score in smoke.score_text(model, case["text"]).items():
                self.assertLessEqual(abs(score - case["scores"][label]), 1e-9, case["name"])


def parity_texts():
    return [
        ("ascii", "Please investigate why the Button disappears"),
        ("empty", ""),
        ("one code point", "x"),
        ("two code points", "ab"),
        ("combining marks", "été café vs café"),
        ("astral emoji", "ship it \U0001F600\U0001F680"),
        ("zwj sequence", "\U0001F469‍\U0001F469‍\U0001F467‍\U0001F466 family"),
        ("variation selector and flag", "❤️ thanks \U0001F1F9\U0001F1F7"),
        ("hebrew rtl", "שלום עולם"),
        ("arabic with rlm", "مرحبا‏ بالعالم"),
        ("turkish dotted and dotless i", "İstanbul ıi İI iI"),
        ("crlf", "line one\r\nline two\r\n"),
        ("tabs", "col1\tcol2\t\tcol3"),
        ("nbsp", "a b  c"),
        ("non-ascii capitals", "ÀÉÎ ABC Zürich ZÜRICH"),
        ("cjk", "日本語のテキスト"),
        ("marker", "⟦excluded: quoted text, 412 chars⟧ ok"),
        ("line separators", " sep "),
    ]


def build_parity_fixture():
    """The fixture's model and expected values, computed by this module.
    PHAROS_WRITE_PARITY_FIXTURE=1 rewrites the committed file."""
    buckets = sorted({bucket for _, text in parity_texts() for bucket, _ in smoke.features(text)})
    model = {
        "format": smoke.MODEL_FORMAT, "featurizer": dict(smoke.FEATURIZER), "threshold": smoke.THRESHOLD,
        "labels": [
            {"label": INVESTIGATE, "bias": -0.25, "weights": [[b, ((b % 197) - 98) / 37.0] for b in buckets]},
            {"label": FINDING, "bias": 0.5, "weights": [[b, (((b * 7) % 101) - 50) / 23.0] for b in buckets[::2]]},
            {"label": "C.report", "bias": 3.0, "weights": []},
            {"label": "P", "bias": -40.0, "weights": [[buckets[0], 1e-3]]},
        ],
    }
    cases = []
    for name, text in parity_texts():
        cases.append({"name": name, "text": text, "features": [[b, v] for b, v in smoke.features(text)],
                      "scores": smoke.score_text(model, text)})
    return {"about": "Invented strings, a tiny invented model and the features and scores tools/labeling_smoke.py computes for them; "
                     "internal/archive/labeling_smoke_model_test.go checks Go computes the same (indices exactly, scores within 1e-9).",
            "fnv1a64": [{"utf8": "", "hash": "%016x" % smoke.fnv1a64(b"")}, {"utf8": "a", "hash": "%016x" % smoke.fnv1a64(b"a")},
                        {"utf8": "foobar", "hash": "%016x" % smoke.fnv1a64(b"foobar")},
                        {"utf8": "İ\U0001F600", "hash": "%016x" % smoke.fnv1a64("İ\U0001F600".encode("utf-8"))}],
            "model": model, "cases": cases}


if os.environ.get("PHAROS_WRITE_PARITY_FIXTURE"):
    PARITY_FIXTURE.write_text(json.dumps(build_parity_fixture(), indent=1, ensure_ascii=False) + "\n", encoding="utf-8")


class ExportCheckTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.path = Path(self.directory.name)

    def run_smoke(self, export):
        out = self.path / "model.json"
        report = smoke.run(str(export), str(out), str(self.path / "report.json"))
        return report, json.loads(out.read_text())

    def test_smoke_run_checks_trains_and_evaluates(self):
        export = write_export(self.path, invented_items())
        report, model = self.run_smoke(export)
        self.assertEqual(report["checked"]["items"], len(invented_items()))
        self.assertEqual(report["checked"]["evidence_occurrences"], 12)
        # 24 usable train rows: refused, invalid, missing, held, unlabeled,
        # the duplicate and (by default) the need-context row are not rows;
        # dev is not trained on.
        self.assertEqual(report["train_rows"], 24)
        self.assertEqual(report["train_skipped"], {"not_usable": 5, "need_context": 1, "duplicate": 1, "seen_in_train": 0})
        self.assertEqual(report["need_context_left_out"], {"by_cohort": {"human": 1}, "by_length_bucket": {"short": 1}})
        self.assertIn("usable_need_context", " ".join(report["warnings"]))
        self.assertIn("too small to train meaningfully", " ".join(report["warnings"]))
        self.assertTrue(report["statement"].startswith("Smoke check"))
        self.assertEqual(model["featurizer"], smoke.FEATURIZER)
        test = report["evaluation"]["test"]
        self.assertEqual(test["skipped"]["seen_in_train"], 1)
        self.assertEqual(test["all"]["rows"], 4)
        self.assertTrue(test["all"]["labels"][INVESTIGATE]["scored"])
        self.assertEqual(test["all"]["labels"][INVESTIGATE]["support"], 3)
        # Every taxonomy label is reported, zero-support ones unscored.
        self.assertEqual(set(test["all"]["labels"]), {node["id"] for node in TAXONOMY})
        self.assertEqual(test["all"]["labels"]["P.request.work.verify"], {"support": 0, "scored": False, "reason": "fewer than 3 positives"})
        self.assertEqual(test["common_short_text"]["rows"], 1)
        self.assertEqual(set(report["evaluation"]), {"test", "fresh"})
        with contextlib.redirect_stdout(io.StringIO()) as out:
            self.assertEqual(smoke.main([str(export), "--out", str(self.path / "m2.json")]), 0)
        self.assertIn("Smoke check, not a quality claim.", out.getvalue())
        self.assertEqual(json.loads((self.path / "m2.json").read_text()), model, "training is not deterministic")
        with contextlib.redirect_stdout(io.StringIO()):
            smoke.main([str(export), "--out", str(self.path / "m3.json"), "--include-need-context"])
        included = json.loads((self.path / "m3.report.json").read_text())
        self.assertEqual(included["train_rows"], 25)
        self.assertEqual(included["train_skipped"]["need_context"], 0)

    def test_training_never_reads_held_out_labels(self):
        export = write_export(self.path, invented_items())
        manifest = smoke.verify_export(str(export))["manifest"]
        real_open = builtins.open

        def guarded(path, *args, **kwargs):
            if os.path.basename(str(path)) in smoke.HELDOUT_FILES:
                raise AssertionError("training opened %s" % path)
            return real_open(path, *args, **kwargs)

        with mock.patch("builtins.open", guarded):
            model = smoke.train(str(export), manifest)
        # Different held-out labels give the same model.
        (export / smoke.LABELS_HELDOUT).write_text("not even json\n")
        self.assertEqual(smoke.train(str(export), manifest), model)

    def test_rows_without_a_say_are_not_negatives(self):
        row = {"labels": [WORK], "uncertain_labels": [FINDING], "unsupported_labels": {"C.report": "in_marker"}, "implied_ancestors": implied([WORK]),
               "masked_labels": masked([WORK], [FINDING], ["C.report"])}
        self.assertEqual(row["masked_labels"], ["C", "C.report", FINDING, INVESTIGATE, "P.request.work.verify"])
        self.assertEqual(smoke.label_target(row, WORK), 1)
        # Ancestors of a positive are positives for their own classifier.
        self.assertEqual(smoke.label_target(row, "P.request"), 1)
        self.assertEqual(smoke.label_target(row, "P"), 1)
        # Uncertain and unsupported labels and their relatives say nothing:
        # C is implied by the uncertain finding, so it is no negative.
        for label in (FINDING, "C.report", "C"):
            self.assertIsNone(smoke.label_target(row, label), label)
        # A broad parent is not a positive, nor a negative, for its children.
        self.assertIsNone(smoke.label_target(row, INVESTIGATE))
        # An uncertain leaf masks its parents; an unrelated label is negative.
        leaf = {"labels": [], "uncertain_labels": [INVESTIGATE], "unsupported_labels": {}, "implied_ancestors": [],
                "masked_labels": masked([], [INVESTIGATE])}
        self.assertEqual(leaf["masked_labels"], ["P", "P.request", WORK, INVESTIGATE])
        self.assertIsNone(smoke.label_target(leaf, WORK))
        self.assertEqual(smoke.label_target(leaf, FINDING), 0)

    def test_rejects_a_changed_file(self):
        export = write_export(self.path, invented_items())
        with open(export / smoke.LABELS, "ab") as handle:
            handle.write(b"\n")
        with self.assertRaisesRegex(smoke.ExportError, "hash"):
            smoke.verify_export(str(export))

    def test_rejects_ranges_that_do_not_round_trip(self):
        def shift_evidence(files):
            files[smoke.LABELS][0]["evidence"][0]["occurrences"][0]["item_start"] += 1

        with self.assertRaisesRegex(smoke.ExportError, "evidence"):
            smoke.verify_export(str(write_export(self.path, invented_items(), tamper=shift_evidence)))

        def split_character(files):
            row = files[smoke.PROVENANCE][0]
            files[smoke.INPUTS][0]["text"] = "é" + files[smoke.INPUTS][0]["text"][1:]
            row["visible_range"] = [0, len(files[smoke.INPUTS][0]["text"].encode("utf-8"))]
            row["source_ranges"][0]["item_start"] = 1

        with self.assertRaisesRegex(smoke.ExportError, "character boundaries"):
            smoke.verify_export(str(write_export(self.path, invented_items(), tamper=split_character)))

        def other_bytes(files):
            files[smoke.PROVENANCE][0]["source_ranges"][0]["sha256"] = "0" * 64

        with self.assertRaisesRegex(smoke.ExportError, "differ from the message"):
            smoke.verify_export(str(write_export(self.path, invented_items(), tamper=other_bytes)))

    def test_rejects_leaks(self):
        def held_out_in_training(files):
            files[smoke.LABELS].append(files[smoke.LABELS_HELDOUT].pop())

        with self.assertRaisesRegex(smoke.ExportError, "holds item"):
            smoke.verify_export(str(write_export(self.path, invented_items(), tamper=held_out_in_training)))

        def label_in_input(files):
            files[smoke.INPUTS][0]["labels"] = [INVESTIGATE]

        with self.assertRaisesRegex(smoke.ExportError, "must not see"):
            smoke.verify_export(str(write_export(self.path, invented_items(), tamper=label_in_input)))

        def unmasked(files):
            files[smoke.LABELS][0]["masked_labels"] = None

        with self.assertRaisesRegex(smoke.ExportError, "status usable"):
            smoke.verify_export(str(write_export(self.path, invented_items(), tamper=unmasked)))

        def refused_as_negative(files):
            row = next(row for row in files[smoke.LABELS] if row["status"] == "refused")
            row["labels"] = []

        with self.assertRaisesRegex(smoke.ExportError, "status refused"):
            smoke.verify_export(str(write_export(self.path, invented_items(), tamper=refused_as_negative)))

    def test_chunk_merges_stay_in_their_split_file(self):
        unit = {"unit_id": "u_x", "split": "test", "unvalidated_merge": True}
        smoke.verify_export(str(write_export(self.path, invented_items(), units=[unit])))

        def move(files):
            files[smoke.UNITS].append(files[smoke.UNITS_HELDOUT].pop())

        with self.assertRaisesRegex(smoke.ExportError, "wrong file"):
            smoke.verify_export(str(write_export(self.path, invented_items(), units=[unit], tamper=move)))


if __name__ == "__main__":
    unittest.main()
