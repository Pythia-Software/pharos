#!/usr/bin/env python3
"""Package a labeler model directory as its release asset.

    python3 tools/package_labeler_model.py MODEL_DIR --out DIR [--version N]
        [--variant int8] [--descriptor internal/archive/labels/assets.json]
        [--key model|agent_events_model]

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
import json
import os
from pathlib import Path
import sys
import tarfile


def model_files(model_dir: Path, variant: str) -> list[str]:
    """The files the app loads, checked against the manifest's digests."""
    manifest = json.loads((model_dir / "manifest.json").read_text())
    files = manifest.get("files", {})
    variants = ["int8", "fp32"] if variant == "all" else [variant]
    names = [files.get(f"onnx_{v}") for v in variants] + [files.get("tokenizer")]
    if not all(names):
        raise SystemExit(f"the manifest names no onnx_{variant} or tokenizer file")
    if "fp32" in variants and (mlx := manifest.get("mlx")):
        if mlx.get("source_onnx_sha256") != manifest.get("sha256", {}).get(files["onnx_fp32"]):
            raise SystemExit("MLX source identity does not match FP32 ONNX")
        names.append(mlx.get("weights"))
    for name in names:
        if not name or Path(name).name != name or name in (".", "..") or "\\" in name:
            raise SystemExit("invalid model filename")
        expected = manifest.get("sha256", {}).get(name)
        if name == files.get("tokenizer"):
            expected = manifest.get("tokenizer_sha256", expected)
        actual = file_sha256(model_dir / name)
        if not expected or actual != expected:
            raise SystemExit(f"{name}: SHA-256 {actual}, the manifest says {expected}")
    return sorted(["manifest.json", *names])


def package(model_dir: Path, out_dir: Path, version: str, variant: str) -> tuple[Path, str, int, str]:
    """Writes the archive; returns its path, SHA-256, size and model name."""
    manifest = json.loads((model_dir / "manifest.json").read_text())
    name = manifest["name"]
    if not name or Path(name).name != name or name in (".", "..") or "\\" in name:
        raise SystemExit("invalid model name")
    if not version or any(c not in "0123456789" for c in version):
        raise SystemExit("model version must be a positive integer")
    if int(version) < 1:
        raise SystemExit("model version must be a positive integer")
    names = model_files(model_dir, variant)
    out_dir.mkdir(parents=True, exist_ok=True)
    path = out_dir / f"{name}-{version}.tar.gz"
    temporary = path.with_suffix(".partial")
    # Stream large multi-runtime models rather than keeping several complete
    # ONNX/safetensors copies and the compressed archive in memory.
    with temporary.open("wb") as sink:
        with gzip.GzipFile(filename="", fileobj=sink, mode="wb", mtime=0, compresslevel=9) as zipped:
            with tarfile.open(fileobj=zipped, mode="w|", format=tarfile.USTAR_FORMAT) as archive:
                for file_name in names:
                    source = model_dir / file_name
                    info = tarfile.TarInfo(file_name)
                    info.size, info.mode, info.mtime = source.stat().st_size, 0o644, 0
                    info.uid = info.gid = 0
                    info.uname = info.gname = ""
                    with source.open("rb") as data:
                        archive.addfile(info, data)
    os.replace(temporary, path)
    return path, file_sha256(path), path.stat().st_size, name


def file_sha256(path: Path) -> str:
    checksum = hashlib.sha256()
    with path.open("rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            checksum.update(block)
    return checksum.hexdigest()


def pin(descriptor: Path, name: str, version: str, sha256: str, size: int, key: str = "model", default_variant: str | None = None) -> None:
    """Pins the asset under key (the message labeler's "model", or
    "agent_events_model") in the build's descriptor, keeping the rest. A new
    key gets the labeler releases' URL pattern."""
    document = json.loads(descriptor.read_text())
    model = document.setdefault(key, {})
    if "url" not in model and "url" in document.get("model", {}):
        model["url"] = document["model"]["url"]
    model.update({"name": name, "version": version, "sha256": sha256, "size": size})
    if default_variant is not None:
        model["default_variant"] = default_variant
    descriptor.write_text(json.dumps(document, indent=2) + "\n")


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("model_dir", type=Path)
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("--version", default="1")
    parser.add_argument("--variant", choices=["int8", "fp32", "all"], default="int8")
    parser.add_argument("--default-variant", choices=["int8", "fp32"], help="default for app-managed downloads when pinning")
    parser.add_argument("--descriptor", type=Path)
    parser.add_argument("--key", default="model", help="the descriptor's entry to pin (model, agent_events_model)")
    args = parser.parse_args(argv)
    if args.default_variant and args.variant != "all" and args.default_variant != args.variant:
        parser.error("--default-variant must be included in --variant")
    path, sha256, size, name = package(args.model_dir, args.out, args.version, args.variant)
    print(json.dumps({"path": str(path), "name": name, "version": args.version, "sha256": sha256, "size": size}, indent=2))
    if args.descriptor:
        pin(args.descriptor, name, args.version, sha256, size, args.key, args.default_variant)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
