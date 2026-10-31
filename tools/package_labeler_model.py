#!/usr/bin/env python3
"""Package a labeler model directory as its release asset.

    python3 tools/package_labeler_model.py MODEL_DIR --out DIR [--version N]
        [--variant int8] [--descriptor internal/archive/labels/assets.json]

Writes DIR/<name>-<version>.tar.gz holding what the app loads: the manifest,
the variant's ONNX file and the tokenizer, at the archive's root. The archive
is deterministic (sorted entries, fixed owners, modes and times, no gzip
timestamp), so packaging the same model again gives the same bytes and the
same SHA-256. With --descriptor it pins the archive's SHA-256 and size in
the build's descriptor (docs/local-labeler.md, "Model download").

It reads and writes local files only; attaching the archive to a GitHub
release is the release's step.
"""

import argparse
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import sys
import tarfile


def model_files(model_dir: Path, variant: str) -> list[str]:
    """The files the app loads, checked against the manifest's digests."""
    manifest = json.loads((model_dir / "manifest.json").read_text())
    files = manifest.get("files", {})
    names = [files.get(f"onnx_{variant}"), files.get("tokenizer")]
    if not all(names):
        raise SystemExit(f"the manifest names no onnx_{variant} or tokenizer file")
    for name in names:
        expected = manifest.get("sha256", {}).get(name)
        if name == files.get("tokenizer"):
            expected = manifest.get("tokenizer_sha256", expected)
        actual = hashlib.sha256((model_dir / name).read_bytes()).hexdigest()
        if expected and actual != expected:
            raise SystemExit(f"{name}: SHA-256 {actual}, the manifest says {expected}")
    return sorted(["manifest.json", *names])


def package(model_dir: Path, out_dir: Path, version: str, variant: str) -> tuple[Path, str, int, str]:
    """Writes the archive; returns its path, SHA-256, size and model name."""
    manifest = json.loads((model_dir / "manifest.json").read_text())
    name = manifest["name"]
    buffer = io.BytesIO()
    with tarfile.open(fileobj=buffer, mode="w", format=tarfile.USTAR_FORMAT) as archive:
        for file_name in model_files(model_dir, variant):
            data = (model_dir / file_name).read_bytes()
            info = tarfile.TarInfo(file_name)
            info.size, info.mode, info.mtime = len(data), 0o644, 0
            info.uid = info.gid = 0
            info.uname = info.gname = ""
            archive.addfile(info, io.BytesIO(data))
    out_dir.mkdir(parents=True, exist_ok=True)
    path = out_dir / f"{name}-{version}.tar.gz"
    compressed = io.BytesIO()
    with gzip.GzipFile(filename="", mode="wb", fileobj=compressed, mtime=0, compresslevel=9) as zipped:
        zipped.write(buffer.getvalue())
    data = compressed.getvalue()
    temporary = path.with_suffix(".partial")
    temporary.write_bytes(data)
    os.replace(temporary, path)
    return path, hashlib.sha256(data).hexdigest(), len(data), name


def pin(descriptor: Path, name: str, version: str, sha256: str, size: int) -> None:
    """Pins the model asset in the build's descriptor, keeping the rest."""
    document = json.loads(descriptor.read_text())
    model = document.setdefault("model", {})
    model.update({"name": name, "version": version, "sha256": sha256, "size": size})
    descriptor.write_text(json.dumps(document, indent=2) + "\n")


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("model_dir", type=Path)
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("--version", default="1")
    parser.add_argument("--variant", default="int8")
    parser.add_argument("--descriptor", type=Path)
    args = parser.parse_args(argv)
    path, sha256, size, name = package(args.model_dir, args.out, args.version, args.variant)
    print(json.dumps({"path": str(path), "name": name, "version": args.version, "sha256": sha256, "size": size}, indent=2))
    if args.descriptor:
        pin(args.descriptor, name, args.version, sha256, size)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
