#!/usr/bin/env python3
"""Fetch the pinned native MLX SDK from verified wheels; prints its directory.

The wheels distribute C++ headers/libraries/Metal resources. Python extension
modules are discarded; Python is only used at build time for extraction.
"""
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import zipfile


def main():
    root = Path(__file__).resolve().parent.parent
    descriptor = root / "macos/mlx.json"
    pins = json.loads(descriptor.read_text())
    stamp = hashlib.sha256(descriptor.read_bytes()).hexdigest()
    cache = root / "build" / ("mlx-" + pins["version"])
    if (cache / (".verified-" + stamp)).is_file():
        print(cache)
        return
    cache.parent.mkdir(exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="mlx.", dir=cache.parent) as work:
        work = Path(work)
        sdk = work / "sdk"
        sdk.mkdir()
        for pin in pins["files"]:
            wheel = work / (pin["package"] + ".whl")
            subprocess.run(["curl", "--fail", "--location", "--silent", "--show-error", "--retry", "3", pin["url"], "-o", str(wheel)], check=True)
            with wheel.open("rb") as source:
                checksum = hashlib.sha256()
                for block in iter(lambda: source.read(1024 * 1024), b""):
                    checksum.update(block)
                if checksum.hexdigest() != pin["sha256"]:
                    raise SystemExit("MLX wheel checksum mismatch: " + pin["package"])
            with zipfile.ZipFile(wheel) as archive:
                for info in archive.infolist():
                    name = info.filename
                    if info.is_dir():
                        continue
                    relative = None
                    if name.startswith("mlx/include/") or name.startswith("mlx/lib/"):
                        relative = Path(name).relative_to("mlx")
                    elif ".dist-info/" in name and ("license" in name.lower() or "notice" in name.lower()):
                        relative = Path("licenses") / pin["package"] / name.split(".dist-info/", 1)[1]
                    if relative is None:
                        continue
                    if relative.is_absolute() or ".." in relative.parts:
                        raise SystemExit("Unsafe SDK archive member")
                    target = sdk / relative
                    target.parent.mkdir(parents=True, exist_ok=True)
                    target.write_bytes(archive.read(info))
        for name in ["include/mlx/mlx.h", "lib/libmlx.dylib", "lib/libjaccl.dylib", "lib/mlx.metallib"]:
            if not (sdk / name).is_file():
                raise SystemExit("Incomplete MLX SDK: " + name)
        if not (sdk / "licenses").is_dir():
            raise SystemExit("MLX license files missing")
        (sdk / (".verified-" + stamp)).touch()
        if cache.exists():
            shutil.rmtree(cache)
        os.replace(sdk, cache)
    print(cache)


if __name__ == "__main__":
    main()
