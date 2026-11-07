"""Isolated, repeatable classic/scheduled work experiments.

This deliberately does not snapshot a live library. Preparation copies a completed
backup's files before opening SQLite, including its WAL. Every measured arm has
fresh files; the installed app, sources, schedules and cloud settings are unused.
Private descriptors, catalogs and reports belong outside tracked files.
"""
import argparse
import contextlib
import hashlib
import json
import os
from pathlib import Path
import resource
import shutil
import sqlite3
import subprocess
import sys
import time

MARKER = ".pharos-work-experiment.json"
LIVE = Path("/Volumes/euclid/Pharos")
SAFE_COMMANDS = {"build-tools", "findings", "labels", "agent-events", "health", "refine-usage"}


def contains(parent, child):
    return parent == child or parent in child.parents


def separate(source, destination):
    source, destination = source.resolve(), destination.resolve()
    if contains(source, destination) or contains(destination, source):
        raise ValueError("experiment and input directories must be separate and non-nested")
    if contains(LIVE.resolve(), source) or contains(LIVE.resolve(), destination):
        raise ValueError("the work experiment never opens or writes the installed library")
    return source, destination


def no_links(path):
    if path.is_symlink():
        raise ValueError(f"experiment inputs must not contain symlinks: {path}")
    if path.is_dir():
        for base, dirs, files in os.walk(path, followlinks=False):
            for name in dirs + files:
                if (Path(base) / name).is_symlink():
                    raise ValueError("experiment inputs must not contain symlinks")


def stamp(path):
    result = {}
    for name in ("catalog.sqlite3", "catalog.sqlite3-wal"):
        file = path / name
        if file.exists():
            s = file.stat()
            result[name] = [s.st_ino, s.st_size, s.st_mtime_ns]
    return result


def clone(source, destination):
    no_links(source)
    if destination.exists():
        raise ValueError("refusing to overwrite an experiment artifact")
    if sys.platform == "darwin":
        subprocess.run(["cp", "-cR", str(source), str(destination)], check=True)
    elif source.is_dir():
        shutil.copytree(source, destination)
    else:
        shutil.copy2(source, destination)
    # Never hardlink mutable database/evidence files, even on filesystems
    # where a caller's cp configuration might behave unexpectedly.
    if source.is_file() and os.path.samestat(source.stat(), destination.stat()):
        raise ValueError("clone produced a hardlink")


def write_json(path, value):
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")
    path.chmod(0o600)


def load_marker(root, role):
    if root.is_symlink():
        raise ValueError("experiment directory must not be a symlink")
    value = json.loads((root / MARKER).read_text())
    if value.get("role") != role or value.get("version") != 1:
        raise ValueError("unrecognized experiment artifact")
    return value


def prepare(backup, destination):
    """Only opens the private clone, never the completed backup database."""
    if backup.is_symlink() or destination.is_symlink():
        raise ValueError("backup/destination must not be symlinks")
    backup, destination = separate(backup, destination)
    if destination.exists():
        raise ValueError("choose a new experiment directory")
    catalog = backup / "catalog"
    if not (catalog / "catalog.sqlite3").is_file():
        raise ValueError("completed backup has no catalog/catalog.sqlite3")
    before = stamp(catalog)
    destination.mkdir(parents=True, mode=0o700)
    try:
        for name in ("catalog", "captures", "preserved"):
            source = backup / name
            if source.exists():
                clone(source, destination / name)
            else:
                (destination / name).mkdir()
        # SHM belongs to the old process, not the snapshot. SQLite recovers
        # committed WAL into this clone before any run starts.
        (destination / "catalog/catalog.sqlite3-shm").unlink(missing_ok=True)
        if stamp(catalog) != before:
            raise ValueError("backup changed while cloning; no experiment was admitted")
        with contextlib.closing(sqlite3.connect(destination / "catalog/catalog.sqlite3")) as db:
            db.execute("PRAGMA wal_checkpoint(TRUNCATE)")
            version = db.execute("SELECT value FROM meta WHERE key='schema_version'").fetchone()
            if version is None or int(version[0]) > 23:
                raise ValueError("corpus is outside the retained binary's version-23 compatibility window")
            counts = {name: db.execute(f'SELECT count(*) FROM "{name}"').fetchone()[0]
                      for name in ("workspaces", "conversations", "messages")}
        marker = {"version": 1, "role": "master", "backup": str(backup), "source_stamp": before,
                  "counts": counts, "schema_version": int(version[0]), "prepared_at": time.time()}
        write_json(destination / MARKER, marker)
        return marker
    except BaseException:
        # Only remove the new directory this invocation created.
        shutil.rmtree(destination)
        raise


def arm(master, destination, descriptor):
    master, destination = separate(master, destination)
    marker = load_marker(master, "master")
    if destination.exists():
        raise ValueError("each arm needs a fresh directory")
    destination.mkdir(parents=True, mode=0o700)
    for name in ("catalog", "captures", "preserved"):
        clone(master / name, destination / name)
    (destination / "staging").mkdir()
    # No copied host/source files, API tokens, backup destinations, app broker,
    # cloud schedules, suggestions, or startup service loops.
    config = ["library = false", 'catalog_path = "catalog/catalog.sqlite3"',
              'archive_root = "preserved"', 'capture_root = "captures"', 'staging_root = "staging"',
              "enable_reclamation = false", 'github_token = ""', 'tl1_token = ""',
              "capture_compress_after_days = 0"]
    model_keys = {"labels_enabled", "labels_model_dir", "labels_variant", "labels_worker", "labels_runtime",
                  "labels_backend", "labels_mlx_runtime", "labels_threads", "labels_workers",
                  "labels_thread_budget", "labels_memory_budget_mb", "agent_events_enabled",
                  "agent_events_model_dir", "agent_events_variant", "agent_events_threads", "agent_events_days"}
    for key, value in descriptor.get("model_settings", {}).items():
        if key not in model_keys or not isinstance(value, (str, int, bool)):
            raise ValueError("unsupported experiment model setting")
        config.append(f"{key} = {json.dumps(value)}")
    for index, source in enumerate(descriptor.get("sources", [])):
        snapshot = Path(source["snapshot"]).expanduser().resolve()
        separate(master, snapshot)
        separate(snapshot, destination)
        target = destination / "sources" / str(index)
        target.parent.mkdir(exist_ok=True)
        clone(snapshot, target)
        config += ["[[sources]]", f'name = {json.dumps(source["name"])}',
                   f'kind = {json.dumps(source["kind"])}', f'path = {json.dumps(str(target))}',
                   'account = "experiment"', 'enabled = true']
    path = destination / "archive.toml"
    path.write_text("\n".join(config) + "\n")
    path.chmod(0o600)
    write_json(destination / MARKER, {**marker, "role": "arm", "master": str(master)})
    return path


def digests(database, checks):
    """Stream deterministic semantic queries; never export transcript values."""
    result = {}
    with contextlib.closing(sqlite3.connect(database.as_uri() + "?mode=ro", uri=True)) as db:
        db.execute("PRAGMA query_only=ON")
        for name, query in checks.items():
            digest, count = hashlib.sha256(), 0
            for row in db.execute(query):
                payload = json.dumps(row, separators=(",", ":"), default=lambda x: {"hex": x.hex()}).encode()
                digest.update(len(payload).to_bytes(8, "big"))
                digest.update(payload)
                count += 1
            result[name] = {"rows": count, "sha256": digest.hexdigest()}
    return result


def run(binary, master, destination, descriptor, engine):
    binary = binary.resolve(strict=True)
    if engine not in ("classic", "scheduled"):
        raise ValueError("unknown engine")
    commands = descriptor.get("commands", [])
    if not commands or any(not isinstance(cmd, list) or not cmd or cmd[0] not in SAFE_COMMANDS for cmd in commands):
        raise ValueError("descriptor needs explicit supported CLI commands")
    for command in commands:
        if command[0] in ("labels", "agent-events") and (len(command) < 2 or command[1] not in ("run", "status")):
            raise ValueError("experiment does not fetch assets or import external records")
    config = arm(master, destination, descriptor)
    env = {key: value for key, value in os.environ.items()
           if key not in ("PHAROS_CONFIG", "PHAROS_APP", "PHAROS_PARENT_PID", "GITHUB_TOKEN", "GH_TOKEN", "PHAROS_LABELS_MODEL_DIR", "PHAROS_LABELS_WORKER", "PHAROS_ORT_LIBRARY", "PHAROS_MLX_LIBRARY")}
    env.update(PHAROS_WORK_ENGINE=engine, TZ=descriptor.get("timezone", "UTC"))
    started, usage = time.monotonic(), resource.getrusage(resource.RUSAGE_CHILDREN)
    results = []
    for index, command in enumerate(commands):
        before, at = resource.getrusage(resource.RUSAGE_CHILDREN), time.monotonic()
        with (destination / f"command-{index}.log").open("wb") as log:
            status = subprocess.run([str(binary), "--config", str(config), *command], env=env,
                                    stdout=log, stderr=subprocess.STDOUT,
                                    timeout=descriptor.get("timeout_seconds", 14400)).returncode
        after = resource.getrusage(resource.RUSAGE_CHILDREN)
        results.append({"command": command[0], "seconds": time.monotonic()-at,
                        "cpu_seconds": after.ru_utime+after.ru_stime-before.ru_utime-before.ru_stime,
                        "exit_code": status})
        if status:
            raise RuntimeError(f"experiment command {index} failed ({status}); see its private log")
    after = resource.getrusage(resource.RUSAGE_CHILDREN)
    semantic = digests(destination / "catalog/catalog.sqlite3", descriptor.get("semantic_checks", {}))
    pending = digests(destination / "catalog/catalog.sqlite3", descriptor.get("pending_checks", {}))
    if any(check["rows"] for check in pending.values()):
        raise RuntimeError("required domains have not settled; a command returning is not completion")
    report = {"version": 1, "engine": engine, "name": descriptor["name"],
              "binary_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
              "descriptor_sha256": hashlib.sha256(json.dumps(descriptor, sort_keys=True).encode()).hexdigest(),
              "corpus": load_marker(master, "master"), "commands": results,
              "wall_seconds": time.monotonic()-started,
              "cpu_seconds": after.ru_utime+after.ru_stime-usage.ru_utime-usage.ru_stime,
              "resource_basis": "waited child user+system CPU; whole-process/descendant scope, not per-task attribution",
              "memory": {"basis": "unavailable", "reason": "use resource-history export for simultaneous peak; do not sum process peaks"},
              "semantic": semantic, "pending": pending, "settled": True,
              "product_readiness": {"basis": "unavailable", "reason": "CLI-only arm; use the product-query replica probe for T_correct/T_usable"}}
    write_json(destination / "report.json", report)
    return report


def compare(baseline, candidate):
    if baseline["descriptor_sha256"] != candidate["descriptor_sha256"] or baseline["corpus"] != candidate["corpus"]:
        raise ValueError("arms did not run the same corpus and workload")
    if not baseline["settled"] or not candidate["settled"]:
        raise ValueError("unsettled arm cannot be compared")
    parity = baseline["semantic"] == candidate["semantic"]
    ratios = {key: candidate[key]/baseline[key] if baseline[key] > 0 else None
              for key in ("wall_seconds", "cpu_seconds")}
    rejected = not parity or any(value is not None and value >= 1.2 for value in ratios.values())
    return {"semantic_parity": parity, "ratios": ratios, "rejected": rejected,
            "release_gate": "rejected" if rejected else "incomplete",
            "remaining_evidence": ["simultaneous memory/I/O/model-load accounting", "actual product-query readiness", "reverse-order pair if noisy", "full required rebuild and old-binary recovery"]}


def main():
    p = argparse.ArgumentParser(description=__doc__)
    sub = p.add_subparsers(dest="action", required=True)
    prep = sub.add_parser("prepare")
    prep.add_argument("--completed-backup", required=True, type=Path)
    prep.add_argument("--master", required=True, type=Path)
    run_p = sub.add_parser("run")
    run_p.add_argument("--binary", required=True, type=Path)
    run_p.add_argument("--master", required=True, type=Path)
    run_p.add_argument("--destination", required=True, type=Path)
    run_p.add_argument("--descriptor", required=True, type=Path)
    run_p.add_argument("--engine", choices=["classic", "scheduled"], required=True)
    comp = sub.add_parser("compare")
    comp.add_argument("baseline", type=Path)
    comp.add_argument("candidate", type=Path)
    args = p.parse_args()
    if args.action == "prepare":
        value = prepare(args.completed_backup, args.master)
    elif args.action == "run":
        value = run(args.binary, args.master, args.destination, json.loads(args.descriptor.read_text()), args.engine)
    else:
        value = compare(json.loads(args.baseline.read_text()), json.loads(args.candidate.read_text()))
    print(json.dumps(value, indent=2))


if __name__ == "__main__":
    main()
