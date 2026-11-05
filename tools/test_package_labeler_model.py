"""Tests of the labeler model packager on a tiny invented model directory."""

import hashlib
import json
from pathlib import Path
import tarfile
import tempfile
import unittest

import package_labeler_model as packager


def write_model(directory: Path, weights: bytes = b"stand-in weights") -> None:
    tokenizer = b'{"model": "invented"}'
    (directory / "model.int8.onnx").write_bytes(weights)
    (directory / "tokenizer.json").write_bytes(tokenizer)
    (directory / "notes.md").write_text("not shipped")
    manifest = {
        "name": "invented-model",
        "files": {"onnx_int8": "model.int8.onnx", "onnx_fp32": "model.fp32.onnx", "tokenizer": "tokenizer.json"},
        "sha256": {"model.int8.onnx": hashlib.sha256(weights).hexdigest()},
        "tokenizer_sha256": hashlib.sha256(tokenizer).hexdigest(),
    }
    (directory / "manifest.json").write_text(json.dumps(manifest))


class PackageTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        self.model = self.root / "model"
        self.model.mkdir()
        write_model(self.model)

    def tearDown(self):
        self.temporary.cleanup()

    def test_archive_holds_what_the_app_loads_at_its_root(self):
        path, sha256, size, name = packager.package(self.model, self.root / "out", "3", "int8")
        self.assertEqual(path.name, "invented-model-3.tar.gz")
        self.assertEqual(name, "invented-model")
        data = path.read_bytes()
        self.assertEqual((hashlib.sha256(data).hexdigest(), len(data)), (sha256, size))
        with tarfile.open(path) as archive:
            members = archive.getmembers()
        self.assertEqual([member.name for member in members], ["manifest.json", "model.int8.onnx", "tokenizer.json"])
        self.assertTrue(all(member.isfile() and member.mtime == 0 and member.uid == 0 for member in members))

    def test_packaging_again_gives_the_same_bytes(self):
        first = packager.package(self.model, self.root / "a", "1", "int8")
        second = packager.package(self.model, self.root / "b", "1", "int8")
        self.assertEqual(first[1], second[1])
        self.assertEqual(first[0].read_bytes(), second[0].read_bytes())

    def test_a_file_that_does_not_match_the_manifest_is_refused(self):
        (self.model / "model.int8.onnx").write_bytes(b"other weights")
        with self.assertRaises(SystemExit):
            packager.package(self.model, self.root / "out", "1", "int8")

    def test_fp32_archive_includes_verified_mlx_companion(self):
        manifest_path = self.model / "manifest.json"
        manifest = json.loads(manifest_path.read_text())
        for name in ("model.fp32.onnx", "model.mlx.safetensors"):
            data = name.encode()
            (self.model / name).write_bytes(data)
            manifest["sha256"][name] = hashlib.sha256(data).hexdigest()
        manifest["mlx"] = {"weights": "model.mlx.safetensors", "source_onnx_sha256": manifest["sha256"]["model.fp32.onnx"]}
        manifest_path.write_text(json.dumps(manifest))
        path, *_ = packager.package(self.model, self.root / "out", "1", "fp32")
        with tarfile.open(path) as archive:
            self.assertEqual(archive.getnames(), ["manifest.json", "model.fp32.onnx", "model.mlx.safetensors", "tokenizer.json"])
        # The default int8 asset remains a CPU artifact.
        self.assertNotIn("model.mlx.safetensors", packager.model_files(self.model, "int8"))
        (self.model / "model.mlx.safetensors").write_bytes(b"corrupt")
        with self.assertRaises(SystemExit):
            packager.model_files(self.model, "fp32")
        manifest["mlx"]["source_onnx_sha256"] = "wrong"
        manifest_path.write_text(json.dumps(manifest))
        with self.assertRaises(SystemExit):
            packager.model_files(self.model, "fp32")

    def test_pin_updates_only_the_model(self):
        descriptor = self.root / "assets.json"
        runtime = {"name": "onnxruntime-osx-arm64", "version": "1.31.1", "sha256": "ab" * 32, "size": 7}
        descriptor.write_text(json.dumps({"model": {"name": "old", "url": "https://example.invalid/{name}-{version}.tar.gz", "sha256": "PENDING", "size": 0}, "onnxruntime": runtime}))
        packager.main([str(self.model), "--out", str(self.root / "out"), "--version", "2", "--descriptor", str(descriptor)])
        document = json.loads(descriptor.read_text())
        _, sha256, size, _ = packager.package(self.model, self.root / "again", "2", "int8")
        self.assertEqual(document["model"], {"name": "invented-model", "version": "2", "url": "https://example.invalid/{name}-{version}.tar.gz", "sha256": sha256, "size": size})
        self.assertEqual(document["onnxruntime"], runtime)
        # Another model is pinned under its own key, with the releases' URL pattern.
        packager.main([str(self.model), "--out", str(self.root / "out"), "--version", "3", "--descriptor", str(descriptor), "--key", "agent_events_model"])
        document = json.loads(descriptor.read_text())
        self.assertEqual(document["agent_events_model"]["version"], "3")
        self.assertEqual(document["agent_events_model"]["url"], document["model"]["url"])
        self.assertEqual(document["model"]["version"], "2")


if __name__ == "__main__":
    unittest.main()
