#!/usr/bin/env python3
"""Build all Query Table packages from one retained source snapshot.

Run from any directory: python3 tools/vendor-query-table.py /path/to/query-table
This packages local files only; it does not publish to npm or edit the source tree.
"""
import base64
import gzip
import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import tempfile

ROOT = Path(__file__).resolve().parent.parent
PACKAGES = {"core": "packages/core", "react": "packages/react", "ui": "packages/ui", "codegen": "tools/schema-codegen"}


def digest(data):
    return hashlib.sha256(data).hexdigest()


def write_json(path, data):
    path.write_text(json.dumps(data, indent=2) + "\n")


def main():
    if len(sys.argv) != 2:
        raise SystemExit("Usage: python3 tools/vendor-query-table.py /path/to/query-table")
    source = Path(sys.argv[1]).expanduser().resolve()
    commit = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=source, text=True).strip()
    listed = subprocess.check_output(["git", "ls-files", "-z", "--cached", "--others", "--exclude-standard"], cwd=source).decode().split("\0")
    files = sorted({name for name in listed if name and (source / name).is_file()})
    vendor = ROOT / "web/vendor"
    vendor.mkdir(parents=True, exist_ok=True)
    context = ROOT / ".context"
    context.mkdir(exist_ok=True)
    stage = Path(tempfile.mkdtemp(prefix="query-table-vendor-", dir=context))
    snapshot = stage / "source"
    hashes = {}
    for name in files:
        data = (source / name).read_bytes()
        hashes[name] = digest(data)
        target = snapshot / name
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(data)
        target.chmod((source / name).stat().st_mode & 0o777)
    source_hash = digest(json.dumps(hashes, sort_keys=True, separators=(",", ":")).encode())
    label = "phoenix-" + source_hash[:12]
    archive_name = "query-table-source-" + label + ".tgz"
    archive = stage / archive_name
    with archive.open("wb") as stream, gzip.GzipFile(filename="", mode="wb", fileobj=stream, mtime=0) as compressed, tarfile.open(fileobj=compressed, mode="w") as tar:
        for name in files:
            path = snapshot / name
            info = tar.gettarinfo(path, arcname=name)
            info.mtime = info.uid = info.gid = 0
            info.uname = info.gname = ""
            with path.open("rb") as contents:
                tar.addfile(info, contents)
    # Build only the retained copy, never live dist folders or workspace symlinks.
    subprocess.run(["npm", "ci", "--no-audit", "--no-fund"], cwd=snapshot, check=True)
    subprocess.run(["npm", "run", "build:packages"], cwd=snapshot, check=True)
    packed = stage / "packages"
    packed.mkdir()
    package_metadata = {}
    for short, directory in PACKAGES.items():
        path = snapshot / directory
        shutil.copy2(snapshot / "LICENSE", path / "LICENSE")
        output = subprocess.check_output(["npm", "pack", "--ignore-scripts", "--json", "--pack-destination", str(packed)], cwd=path, text=True)
        name = "query-table-" + short + "-" + label + ".tgz"
        shutil.copy2(packed / json.loads(output)[0]["filename"], vendor / name)
        package_metadata[short] = (name, json.loads((path / "package.json").read_text()))
    shutil.copy2(archive, vendor / archive_name)
    manifest_name = "query-table-source-manifest.json"
    write_json(vendor / manifest_name, {"upstream_base_commit": commit, "upstream_dirty": bool(subprocess.check_output(["git", "status", "--porcelain"], cwd=source)), "sha256": source_hash, "files": hashes})
    package_path = ROOT / "web/package.json"
    lock_path = ROOT / "web/package-lock.json"
    package = json.loads(package_path.read_text())
    lock = json.loads(lock_path.read_text())
    provenance = []
    for short, (name, metadata) in package_metadata.items():
        key = metadata["name"]
        kind = "devDependencies" if short == "codegen" else "dependencies"
        ref = "file:vendor/" + name
        package[kind][key] = ref
        lock["packages"][""][kind][key] = ref
        entry = lock["packages"]["node_modules/" + key]
        data = (vendor / name).read_bytes()
        entry.update(version=metadata["version"], resolved=ref, integrity="sha512-" + base64.b64encode(hashlib.sha512(data).digest()).decode())
        for field in ["dependencies", "peerDependencies", "engines"]:
            if field in metadata:
                entry[field] = metadata[field]
            else:
                entry.pop(field, None)
        provenance.append({"package": key, "file": name, "sha256": digest(data), "upstream_base_commit": commit, "upstream_dirty": bool(subprocess.check_output(["git", "status", "--porcelain"], cwd=source)), "source_manifest_sha256": source_hash, "source_archive": archive_name, "source_archive_sha256": digest(archive.read_bytes()), "source_manifest": manifest_name})
    write_json(package_path, package)
    write_json(lock_path, lock)
    write_json(vendor / "query-table-provenance.json", provenance)
    # Ship assets built from installed archives, not the host's optional links.
    pharos = stage / "pharos"
    shutil.copytree(ROOT / "web", pharos / "web", ignore=shutil.ignore_patterns("node_modules"))
    shutil.copytree(ROOT / "schemas", pharos / "schemas")
    shutil.copytree(ROOT / "pricing", pharos / "pricing")
    assets = pharos / "internal/archive/assets"
    assets.mkdir(parents=True)
    subprocess.run(["npm", "ci", "--no-audit", "--no-fund"], cwd=pharos / "web", check=True)
    subprocess.run(["npm", "run", "build"], cwd=pharos / "web", check=True)
    for name in ["query-tables.js", "query-tables.css"]:
        shutil.copy2(assets / name, ROOT / "internal/archive/assets" / name)
    subprocess.run(["npm", "run", "check-bundle"], cwd=pharos / "web", check=True)
    # Every source/package archive now shares one baseline; remove superseded pins.
    retained = {archive_name, *(name for name, _ in package_metadata.values())}
    for path in vendor.glob("query-table-*.tgz"):
        if path.name not in retained:
            path.unlink()
    (vendor / "query-table-compact-source-manifest.json").unlink(missing_ok=True)
    print(f"Pinned four packages from {commit} + retained edits: {label}")
    print(f"Build source retained at {snapshot}")


if __name__ == "__main__":
    main()
