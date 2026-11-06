#!/usr/bin/env python3
"""Verify or publish a prepared model release bundle using only stdlib + gh.

Copy the prepared bundle to the cleanroom Mac. `verify` is offline; `publish`
requires its existing GitHub login and a clean checkout of origin/master.
`promote` verifies publication before updating app pins in a development
checkout, for a separate reviewed commit before the normal app release.
No training environment, model conversion, app signing or key setup is used.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import tarfile
import tempfile

REPO = "Pythia-Software/pharos"
TASKS = {"model": "kestrel", "agent_events_model": "jayhawk"}


def sha256_file(path):
    with path.open("rb") as source:
        return sha256_stream(source)


def sha256_stream(source):
    checksum = hashlib.sha256()
    for block in iter(lambda: source.read(1024 * 1024), b""):
        checksum.update(block)
    return checksum.hexdigest()


def local_name(name):
    if not isinstance(name, str) or not re.fullmatch(r"[a-zA-Z0-9][a-zA-Z0-9_.-]*", name):
        raise ValueError(f"invalid bundle filename: {name!r}")
    return name


def verify_model(path, pin, task):
    """Check the exact flat archive and every manifest file digest."""
    names_seen, digests, manifest = set(), {}, None
    # Streaming avoids repeated decompression when checking gigabyte-sized
    # archives on a cleanroom Mac with no ML packages installed.
    with tarfile.open(path, "r|gz") as archive:
        for member in archive:
            local_name(member.name)
            if not member.isfile() or member.name in names_seen:
                raise ValueError("model archive must contain unique regular files")
            names_seen.add(member.name)
            source = archive.extractfile(member)
            if member.name == "manifest.json":
                manifest = json.load(source)
            else:
                digests[member.name] = sha256_stream(source)
    if manifest is None:
        raise ValueError("model archive has no manifest")
    files, mlx = manifest["files"], manifest["mlx"]
    if manifest["name"] != pin["name"] or mlx["format"] != "pharos-mlx-v1" or mlx["architecture"] != "ettin-68m-v1" or mlx["task"] != task:
        raise ValueError("model architecture/task does not match release")
    names = {files["onnx_int8"], files["onnx_fp32"], files["tokenizer"], mlx["weights"]}
    if len(names) != 4 or names_seen != names | {"manifest.json"}:
        raise ValueError("release must contain int8, FP32, MLX and tokenizer files")
    for name in names:
        expected = manifest["sha256"].get(name)
        if name == files["tokenizer"]:
            expected = manifest.get("tokenizer_sha256", expected)
        if digests[name] != expected:
            raise ValueError("model digest mismatch: " + name)
    if mlx["source_onnx_sha256"] != manifest["sha256"][files["onnx_fp32"]]:
        raise ValueError("MLX companion has another FP32 source")
    return {"task": task, "source_onnx_sha256": mlx["source_onnx_sha256"],
            "weights_sha256": manifest["sha256"][mlx["weights"]]}


def verify(bundle):
    release = json.loads((bundle / "release.json").read_text())
    if release["schema"] != 1 or release["repository"] != REPO or set(r["key"] for r in release["releases"]) != set(TASKS) or len(release["releases"]) != 2:
        raise ValueError("unexpected release bundle")
    for name, checksum in release["sha256"].items():
        if sha256_file(bundle / local_name(name)) != checksum:
            raise ValueError("bundle digest mismatch: " + name)
    candidate = json.loads((bundle / "assets.json").read_text())
    for item in release["releases"]:
        pin = candidate[item["key"]]
        asset, tag = item["asset"], item["tag"]
        local_name(asset)
        expected = f"{pin['name']}-{pin['version']}.tar.gz"
        if not re.fullmatch(r"[1-9][0-9]*", pin["version"]) or asset != expected or tag != "labeler-" + expected.removesuffix(".tar.gz"):
            raise ValueError("release tag or filename does not match pins")
        url = f"https://github.com/{REPO}/releases/download/{tag}/{asset}"
        if pin["url"].replace("{name}", pin["name"]).replace("{version}", pin["version"]) != url or pin.get("default_variant") != "fp32":
            raise ValueError("release URL or default variant is wrong")
        if release["sha256"].get(asset) != pin["sha256"] or (bundle / asset).stat().st_size != pin["size"]:
            raise ValueError("archive does not match app pins")
        for name in [item["notes"], asset + ".sha256", "assets.json", "publish_labeler_models.py"]:
            if local_name(name) not in release["sha256"]:
                raise ValueError("unchecked bundle file: " + name)
        if (bundle / (asset + ".sha256")).read_text() != f"{pin['sha256']}  {asset}\n":
            raise ValueError("incorrect archive checksum file")
        identity = verify_model(bundle / asset, pin, TASKS[item["key"]])
        if identity != item["identity"]:
            raise ValueError("model provenance mismatch")
    return release, candidate


def command(args, cwd=None):
    return subprocess.run(args, cwd=cwd, check=True, text=True, capture_output=True).stdout.strip()


def remote_release(tag):
    result = subprocess.run(["gh", "api", f"repos/{REPO}/releases/tags/{tag}"], text=True, capture_output=True)
    if result.returncode:
        if "HTTP 404" in result.stderr or "(404)" in result.stderr:
            return None
        raise RuntimeError(result.stderr.strip())
    return json.loads(result.stdout)


def check_remote_file(info, name, bundle, checksum):
    found = [a for a in info["assets"] if a["name"] == name]
    if not found:
        return False
    if len(found) != 1 or found[0]["size"] != (bundle / name).stat().st_size:
        raise ValueError("published asset has another size: " + name)
    digest = found[0].get("digest")
    if digest is None:
        # Older GitHub uploads may not expose a digest. Verify downloaded bytes.
        with tempfile.TemporaryDirectory() as temporary:
            command(["gh", "release", "download", info["tag_name"], "-R", REPO, "--pattern", name, "--dir", temporary])
            digest = "sha256:" + sha256_file(Path(temporary) / name)
    if digest != "sha256:" + checksum:
        raise ValueError("published asset is immutable and has another digest: " + name)
    return True


def check_remote(release, bundle, published):
    for item in release["releases"]:
        info = remote_release(item["tag"])
        if info is None or (published and info["draft"]):
            raise ValueError("model release is not published: " + item["tag"])
        for name in [item["asset"], item["asset"] + ".sha256"]:
            if not check_remote_file(info, name, bundle, release["sha256"][name]):
                raise ValueError("release is missing " + name)


def publish(release, bundle, checkout):
    if command(["git", "status", "--porcelain"], checkout):
        raise ValueError("publish from the cleanroom's clean checkout")
    origin = command(["git", "remote", "get-url", "origin"], checkout)
    if origin not in [f"https://github.com/{REPO}", f"https://github.com/{REPO}.git", f"git@github.com:{REPO}.git", f"git@github.com:{REPO}"]:
        raise ValueError("origin must point to " + REPO)
    command(["git", "fetch", "origin", "master", "--tags"], checkout)
    target = command(["git", "rev-parse", "HEAD"], checkout)
    if target != command(["git", "rev-parse", "origin/master"], checkout):
        raise ValueError("check out origin/master before model publication")
    command(["gh", "auth", "status", "-h", "github.com"])
    # Stage and verify both releases before making either public. Interrupted
    # runs reuse matching uploads; conflicting assets are never overwritten.
    for item in release["releases"]:
        tag = item["tag"]
        info = remote_release(tag)
        if info is None:
            command(["gh", "release", "create", tag, "-R", REPO, "--draft", "--latest=false", "--target", target,
                     "--title", "Labeler model " + tag.removeprefix("labeler-"), "--notes-file", str(bundle / item["notes"])])
            info = remote_release(tag)
        for name in [item["asset"], item["asset"] + ".sha256"]:
            if check_remote_file(info, name, bundle, release["sha256"][name]):
                continue
            if not info["draft"]:
                raise ValueError("refusing to alter published release: " + tag)
            print("Uploading " + name, flush=True)
            command(["gh", "release", "upload", tag, str(bundle / name), "-R", REPO])
    check_remote(release, bundle, published=False)
    for item in release["releases"]:
        if remote_release(item["tag"])["draft"]:
            command(["gh", "release", "edit", item["tag"], "-R", REPO, "--draft=false", "--latest=false"])
    check_remote(release, bundle, published=True)


def promote(candidate, descriptor):
    document = json.loads(descriptor.read_text())
    for key in TASKS:
        old, new = document[key], candidate[key]
        if int(new["version"]) < int(old["version"]) or (new["version"] == old["version"] and new["sha256"] != old["sha256"]):
            raise ValueError("refusing an older or conflicting model version")
        document[key] = new
    temporary = descriptor.with_suffix(".partial")
    temporary.write_text(json.dumps(document, indent=2) + "\n")
    os.replace(temporary, descriptor)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=["verify", "publish", "promote"])
    parser.add_argument("bundle", type=Path)
    parser.add_argument("--checkout", type=Path, default=Path.cwd(), help="clean origin/master checkout for publication")
    parser.add_argument("--descriptor", type=Path, default=Path("internal/archive/labels/assets.json"), help="app pins to promote after publication")
    args = parser.parse_args(argv)
    bundle = args.bundle.resolve()
    release, candidate = verify(bundle)
    print("Verified both model archives, their contents, and app pins.", flush=True)
    if args.action == "publish":
        publish(release, bundle, args.checkout.resolve())
        for item in release["releases"]:
            print(f"https://github.com/{REPO}/releases/tag/{item['tag']}")
    elif args.action == "promote":
        check_remote(release, bundle, published=True)
        promote(candidate, args.descriptor)
        print(f"Updated {args.descriptor}; review and commit these pins before the normal cleanroom app release.")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, OSError, RuntimeError, subprocess.CalledProcessError) as error:
        raise SystemExit(str(error))
