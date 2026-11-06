"""Release-kit checks use invented tensor files; no training runtime or GitHub."""
import copy
import hashlib
import json
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest.mock import patch

import prepare_labeler_release as prepare
import publish_labeler_models as publish


def model(directory, name, task):
    directory.mkdir()
    data = {"model.int8.onnx": b"int8", "model.fp32.onnx": b"fp32", "model.mlx.safetensors": b"native", "tokenizer.json": b"{}"}
    sums = {name: hashlib.sha256(raw).hexdigest() for name, raw in data.items()}
    manifest = {"name": name, "files": {"onnx_int8": "model.int8.onnx", "onnx_fp32": "model.fp32.onnx", "tokenizer": "tokenizer.json"},
                "sha256": sums, "tokenizer_sha256": sums["tokenizer.json"],
                "mlx": {"format": "pharos-mlx-v1", "architecture": "ettin-68m-v1", "task": task,
                        "weights": "model.mlx.safetensors", "source_onnx_sha256": sums["model.fp32.onnx"]}}
    for filename, raw in data.items():
        (directory / filename).write_bytes(raw)
    (directory / "manifest.json").write_text(json.dumps(manifest))


class ReleaseKitTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.messages, self.events = self.root / "messages", self.root / "events"
        model(self.messages, "kestrel-example", "kestrel")
        model(self.events, "jayhawk-example", "jayhawk")
        self.descriptor = self.root / "active.json"
        self.active = {"model": {"name": "kestrel-example", "version": "1", "sha256": "old"},
                       "agent_events_model": {"name": "jayhawk-example", "version": "1", "sha256": "old"},
                       "onnxruntime": {"sha256": "unchanged", "version": "1.31.1"}}
        for key in publish.TASKS:
            self.active[key]["url"] = f"https://github.com/{publish.REPO}/releases/download/labeler-{{name}}-{{version}}/{{name}}-{{version}}.tar.gz"
        self.descriptor.write_text(json.dumps(self.active))
        self.out, self.candidate = self.root / "kit", self.root / "pending.json"

    def prepare(self):
        return prepare.prepare(self.messages, self.events, self.out, "2", self.descriptor, self.candidate)

    def test_kit_is_portable_and_leaves_live_pins_unchanged(self):
        result = self.prepare()
        self.assertEqual(json.loads(self.descriptor.read_text()), self.active)
        release, candidate = publish.verify(self.out)
        self.assertEqual(candidate, json.loads(self.candidate.read_text()))
        for key in publish.TASKS:
            self.assertEqual(candidate[key]["version"], "2")
            self.assertEqual(candidate[key]["default_variant"], "fp32")
        transfer = Path(result["bundle"])
        self.assertEqual(publish.sha256_file(transfer), result["sha256"])
        with tarfile.open(transfer) as archive:
            self.assertTrue(all(m.isfile() and m.mtime == 0 for m in archive.getmembers()))
        # Run the transferred tool in isolated Python from an empty checkout.
        # It cannot import repo-local packaging modules or ML libraries.
        cleanroom = self.root / "cleanroom"
        cleanroom.mkdir()
        completed = subprocess.run([sys.executable, "-I", str(self.out / "publish_labeler_models.py"), "verify", str(self.out)],
                                   cwd=cleanroom, text=True, capture_output=True, check=True)
        self.assertIn("Verified both model archives", completed.stdout)
        for item in release["releases"]:
            with tarfile.open(self.out / item["asset"]) as archive:
                self.assertEqual(archive.getnames(), ["manifest.json", "model.fp32.onnx", "model.int8.onnx", "model.mlx.safetensors", "tokenizer.json"])

    def test_corruption_is_refused_before_any_publication(self):
        self.prepare()
        release, _ = publish.verify(self.out)
        path = self.out / release["releases"][0]["asset"]
        path.write_bytes(path.read_bytes() + b"corruption")
        with patch.object(publish.subprocess, "run") as run:
            with self.assertRaisesRegex(ValueError, "digest mismatch"):
                publish.main(["publish", str(self.out)])
            run.assert_not_called()

    def test_validation_report_is_carried_and_failed_decisions_are_refused(self):
        report = {
            "kestrel": {"rows": 1, "worker": {"device": "gpu"}, "comparison": {"probs": {"finite": True}, "labels": {"changed_bits": 0}}},
            "jayhawk": {"rows": 1, "worker": {"device": "gpu"}, "comparison": {"event_decisions": {"same_rows": 1}, "sentences": {"finite": True, "changed_decision_bits": 0}, "families": {"finite": True, "changed_decision_bits": 0}}},
        }
        path = self.root / "validation.json"
        path.write_text(json.dumps(report))
        prepare.prepare(self.messages, self.events, self.out, "2", self.descriptor, self.candidate, path)
        self.assertEqual(json.loads((self.out / "validation.json").read_text()), report)
        release, _ = publish.verify(self.out)
        for item in release["releases"]:
            self.assertIn("1 messages and 1 event candidates", (self.out / item["notes"]).read_text())
        report["kestrel"]["comparison"]["labels"]["changed_bits"] = 1
        path.write_text(json.dumps(report))
        with self.assertRaisesRegex(ValueError, "decisions differ"):
            prepare.prepare(self.messages, self.events, self.root / "failed", "3", self.descriptor, self.root / "failed-pins.json", path)
        self.assertFalse((self.root / "failed-pins.json").exists())
        self.assertEqual(json.loads(self.descriptor.read_text()), self.active)

    def test_existing_published_assets_cannot_be_replaced(self):
        self.prepare()
        release, _ = publish.verify(self.out)
        item = release["releases"][0]
        name = item["asset"]
        info = {"tag_name": item["tag"], "draft": False, "assets": [{"name": name, "size": (self.out / name).stat().st_size, "digest": "sha256:" + "0" * 64}]}
        with self.assertRaisesRegex(ValueError, "immutable"):
            publish.check_remote_file(info, name, self.out, release["sha256"][name])
        info["assets"][0]["digest"] = "sha256:" + release["sha256"][name]
        self.assertTrue(publish.check_remote_file(info, name, self.out, release["sha256"][name]))

    def test_interrupted_upload_resumes_without_replacing_assets(self):
        self.prepare()
        release, _ = publish.verify(self.out)
        state, mutations = {}, []
        fail_once = True

        def execute(args, cwd=None):
            nonlocal fail_once
            if args[:3] == ["git", "status", "--porcelain"]:
                return ""
            if args[:3] == ["git", "remote", "get-url"]:
                return f"https://github.com/{publish.REPO}.git"
            if args[:2] == ["git", "rev-parse"]:
                return "reviewed-master-commit"
            if args[:2] == ["git", "fetch"] or args[:2] == ["gh", "auth"]:
                return ""
            action, tag = args[2:4]
            mutations.append((action, tag))
            if action == "create":
                self.assertIn("--latest=false", args)
                state[tag] = dict(tag_name=tag, draft=True, assets=[])
            elif action == "upload":
                path = Path(args[4])
                self.assertFalse(any(a["name"] == path.name for a in state[tag]["assets"]))
                state[tag]["assets"].append(dict(name=path.name, size=path.stat().st_size, digest="sha256:" + publish.sha256_file(path)))
                if fail_once:
                    fail_once = False
                    raise RuntimeError("response lost after successful upload")
            elif action == "edit":
                self.assertIn("--latest=false", args)
                state[tag]["draft"] = False
            else:
                self.fail("unexpected publication command")
            return ""

        with patch.object(publish, "command", side_effect=execute), patch.object(publish, "remote_release", side_effect=lambda tag: copy.deepcopy(state.get(tag))):
            with self.assertRaisesRegex(RuntimeError, "response lost"):
                publish.publish(release, self.out, self.root)
            publish.publish(release, self.out, self.root)
            self.assertTrue(all(not info["draft"] and len(info["assets"]) == 2 for info in state.values()))
            completed = list(mutations)
            publish.publish(release, self.out, self.root)
            self.assertEqual(mutations, completed)

    def test_promote_preserves_runtime_and_rejects_downgrades(self):
        self.prepare()
        _, candidate = publish.verify(self.out)
        publish.promote(candidate, self.descriptor)
        saved = json.loads(self.descriptor.read_text())
        self.assertEqual(saved["onnxruntime"], self.active["onnxruntime"])
        self.assertEqual(saved["model"], candidate["model"])
        self.assertEqual(saved["agent_events_model"], candidate["agent_events_model"])
        candidate["model"]["version"] = "1"
        with self.assertRaisesRegex(ValueError, "older"):
            publish.promote(candidate, self.descriptor)
        self.assertEqual(json.loads(self.descriptor.read_text()), saved)

    def test_prepare_cannot_overwrite_active_descriptor(self):
        with self.assertRaisesRegex(ValueError, "active descriptor"):
            prepare.prepare(self.messages, self.events, self.out, "2", self.descriptor, self.descriptor)
        self.assertEqual(json.loads(self.descriptor.read_text()), self.active)


if __name__ == "__main__":
    unittest.main()
