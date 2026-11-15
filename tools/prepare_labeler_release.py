#!/usr/bin/env python3
"""Prepare a portable release kit from two already converted model directories.

The development Mac packages and verifies the trained artifacts once. The
cleanroom Mac receives opaque archives, checksums and a stdlib publication
tool. This never changes the active app descriptor or publishes anything.
"""
import argparse
import json
from pathlib import Path
import shutil
import tarfile

import package_labeler_model as packager
import publish_labeler_models as publisher


def prepare(message_model, event_model, out, version, descriptor, candidate, validation=None):
    if descriptor.resolve() == candidate.resolve():
        raise ValueError("candidate pins must not overwrite the active descriptor before publication")
    out.mkdir(parents=True, exist_ok=False)
    shutil.copyfile(descriptor, out / "assets.json")
    shutil.copyfile(Path(__file__).with_name("publish_labeler_models.py"), out / "publish_labeler_models.py")
    validation_note = "No corpus-validation report is included in this kit."
    if validation:
        report = json.loads(validation.read_text())
        counts = {}
        for task in ("kestrel", "jayhawk"):
            row = report[task]
            comparison = row["comparison"]
            if row["rows"] <= 0 or row["worker"]["device"] != "gpu" or not all(v.get("finite", True) for v in comparison.values()):
                raise ValueError("invalid native validation report")
            if task == "kestrel":
                if comparison["labels"]["changed_bits"] != 0:
                    raise ValueError("message label decisions differ from reference")
            elif (comparison["event_decisions"]["same_rows"] != row["rows"] or
                  comparison["sentences"]["changed_decision_bits"] != 0 or comparison["families"]["changed_decision_bits"] != 0):
                raise ValueError("event/sentence decisions differ from reference")
            counts[task] = row["rows"]
        validation_note = (f"The supplied native validation report covers {counts['kestrel']} messages and {counts['jayhawk']} event candidates against FP32 ONNX CPU, "
                           "with all classification decisions matching.")
    release = {"schema": 1, "repository": publisher.REPO, "releases": [], "sha256": {}}
    for key, source in [("model", message_model), ("agent_events_model", event_model)]:
        path, checksum, size, name = packager.package(source, out, version, "all")
        packager.pin(out / "assets.json", name, version, checksum, size, key, "fp32")
        pin = json.loads((out / "assets.json").read_text())[key]
        identity = publisher.verify_model(path, pin, publisher.TASKS[key])
        tag = f"labeler-{name}-{version}"
        notes = f"{tag}.txt"
        (out / notes).write_text(f"{name} model artifact version {version}\n\n"
            "Contains the original int8 ONNX model, canonical FP32 ONNX model, native FP32 MLX companion and tokenizer. "
            "The prepared Pharos build selects FP32 automatically for managed downloads and uses MLX on supported Apple silicon Macs. "
            "CPU fallback uses the same FP32 model; explicit int8 configurations remain supported. "
            "This changes the default mathematical variant from int8 to FP32 and triggers the normal model-identity rebuild.\n\n"
            f"FP32 ONNX SHA-256: {identity['source_onnx_sha256']}\n"
            f"MLX weights SHA-256: {identity['weights_sha256']}\n\n"
            + validation_note + " MLX and int8 are not numerically identical. This model release does not update the app or Sparkle feed.\n")
        (out / (path.name + ".sha256")).write_text(f"{checksum}  {path.name}\n")
        release["releases"].append(dict(key=key, tag=tag, asset=path.name, notes=notes, identity=identity))
    if validation:
        shutil.copyfile(validation, out / "validation.json")
    (out / "README.txt").write_text("Pharos MLX model release kit\n\n"
        "Transfer this entire directory to the cleanroom Mac. No torch, numpy, training files, conversion or app-signing setup is needed.\n\n"
        "1. Verify locally (Python 3.10+):\n"
        "   python3 /path/to/kit/publish_labeler_models.py verify /path/to/kit\n\n"
        "2. From a clean origin/master checkout on the cleanroom Mac, with gh already authenticated:\n"
        "   python3 /path/to/kit/publish_labeler_models.py publish /path/to/kit\n"
        "   This stages both model releases, verifies remote digests, then publishes with --latest=false. It never signs or releases the app.\n\n"
        "3. Back in a development checkout after publication:\n"
        "   python3 /path/to/kit/publish_labeler_models.py promote /path/to/kit\n"
        "   Review, commit and merge the updated internal/archive/labels/assets.json. Runtime pins are preserved.\n\n"
        "4. Publish the app from the cleanroom's clean origin/master with the ordinary macos/release.sh workflow. "
        "Only the pins and backend source are needed there; end-user Macs download the models automatically.\n\n"
        "Current app pins are intentionally unchanged until step 3; existing users keep the published version 1 assets. "
        "Never replace a published model asset. If a bundle differs, prepare a new version.\n")
    for path in sorted(out.iterdir()):
        release["sha256"][path.name] = packager.file_sha256(path)
    (out / "release.json").write_text(json.dumps(release, indent=2) + "\n")
    publisher.verify(out)
    candidate.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(out / "assets.json", candidate)
    # Already-compressed models: a plain deterministic tar makes one portable
    # handoff without recompressing a gigabyte of model archives.
    transfer = out.with_suffix(".tar")
    with tarfile.open(transfer, "w", format=tarfile.USTAR_FORMAT) as archive:
        for path in sorted(out.iterdir()):
            info = tarfile.TarInfo(out.name + "/" + path.name)
            info.size, info.mode, info.mtime = path.stat().st_size, 0o644, 0
            info.uid = info.gid = 0
            with path.open("rb") as source:
                archive.addfile(info, source)
    checksum = packager.file_sha256(transfer)
    transfer.with_suffix(".tar.sha256").write_text(f"{checksum}  {transfer.name}\n")
    return dict(bundle=str(transfer), sha256=checksum, size=transfer.stat().st_size, candidate=str(candidate))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--message-model", type=Path, required=True)
    parser.add_argument("--event-model", type=Path, required=True)
    parser.add_argument("--out", type=Path, required=True, help="new directory for the transfer kit")
    parser.add_argument("--version", required=True)
    parser.add_argument("--descriptor", type=Path, default=Path("internal/archive/labels/assets.json"))
    parser.add_argument("--candidate", type=Path, required=True, help="pending descriptor; never the active assets.json")
    parser.add_argument("--validation-report", type=Path)
    args = parser.parse_args()
    print(json.dumps(prepare(args.message_model, args.event_model, args.out, args.version,
                             args.descriptor, args.candidate, args.validation_report), indent=2))


if __name__ == "__main__":
    main()
