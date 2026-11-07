"""Isolated, repeatable classic/scheduled work experiments.

This deliberately does not snapshot a live library. Preparation copies a completed
backup's files before opening SQLite, including any WAL beside its snapshot.
Source folders and model files are frozen once into the master as private
inputs; every measured arm clones fresh files from the master and never rereads
a configured folder. The installed app, sources, schedules and cloud settings
are unused. Private descriptors, catalogs, logs and reports stay outside
tracked files.
"""
import argparse
import contextlib
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import resource
import shutil
import sqlite3
import subprocess
import sys
import time

MARKER = ".pharos-work-experiment.json"
VERSION = 2
LIVE = Path("/Volumes/euclid/Pharos")
BACKUP_MANIFEST = "pharos-backup.json"
# The retained binary's catalog compatibility floor.
SCHEMA_FLOOR = 23
SAFE_COMMANDS = {"build-tools", "findings", "labels", "agent-events", "health", "refine-usage"}
# A ratio over a few-second baseline is noise, not a regression.
MIN_GATED_SECONDS = 5.0
REJECT_RATIO = 1.2
MODEL_KEYS = {"labels_enabled", "labels_variant", "labels_backend", "labels_threads", "labels_workers",
              "labels_thread_budget", "labels_memory_budget_mb", "agent_events_enabled",
              "agent_events_variant", "agent_events_threads", "agent_events_days"}
# Path settings name a frozen input, never a host path.
INPUT_KEYS = {"labels_model_dir", "labels_worker", "labels_runtime", "labels_mlx_runtime", "agent_events_model_dir"}
NAME = re.compile(r"^[a-z0-9][a-z0-9._-]{0,63}$")
SHA256 = re.compile(r"^[0-9a-f]{64}$")


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


def stamp(paths):
    """Inode, size and mtime of every file under paths: a stat walk, no reads."""
    result = {}
    for label, path in sorted(paths.items()):
        if path.is_file():
            s = path.stat()
            result[label] = [s.st_ino, s.st_size, s.st_mtime_ns]
        elif path.is_dir():
            for base, dirs, files in os.walk(path, followlinks=False):
                dirs.sort()
                for name in sorted(files):
                    s = (Path(base) / name).stat()
                    result[f"{label}/{(Path(base) / name).relative_to(path)}"] = [s.st_ino, s.st_size, s.st_mtime_ns]
    return result


def digest_of(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def file_sha256(path):
    digest = hashlib.sha256()
    with path.open("rb") as file:
        for block in iter(lambda: file.read(1 << 20), b""):
            digest.update(block)
    return digest.hexdigest()


def content_identity(path):
    """Content hash of a frozen input, computed once when it is frozen."""
    digest, files, size = hashlib.sha256(), 0, 0
    entries = [(".", path)] if path.is_file() else sorted(
        (str(Path(base, name).relative_to(path)), Path(base, name))
        for base, _, names in os.walk(path, followlinks=False) for name in names)
    for relative, file in entries:
        entry = json.dumps([relative, file.stat().st_size, file_sha256(file)]).encode()
        digest.update(len(entry).to_bytes(8, "big"))
        digest.update(entry)
        files, size = files + 1, size + file.stat().st_size
    return {"sha256": digest.hexdigest(), "files": files, "bytes": size}


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


def write_private(path, data):
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(descriptor, "wb") as file:
        file.write(data)
    path.chmod(0o600)


def write_json(path, value):
    temporary = path.with_name(path.name + ".tmp")
    write_private(temporary, (json.dumps(value, indent=2, sort_keys=True) + "\n").encode())
    temporary.replace(path)


def load_marker(root, role):
    if root.is_symlink():
        raise ValueError("experiment directory must not be a symlink")
    value = json.loads((root / MARKER).read_text())
    if value.get("role") != role or value.get("version") != VERSION:
        raise ValueError("unrecognized experiment artifact")
    return value


@contextlib.contextmanager
def read_only(database):
    """A connection that can read the arm's catalog and nothing else."""
    db = sqlite3.connect(database.as_uri() + "?mode=ro", uri=True)
    try:
        db.execute("PRAGMA query_only=ON")
        allowed = {sqlite3.SQLITE_SELECT, sqlite3.SQLITE_READ, sqlite3.SQLITE_FUNCTION,
                   getattr(sqlite3, "SQLITE_RECURSIVE", 33)}
        # Denies ATTACH, PRAGMA and every write, whatever the query text.
        db.set_authorizer(lambda action, *_: sqlite3.SQLITE_OK if action in allowed else sqlite3.SQLITE_DENY)
        yield db
    finally:
        db.close()


def master_paths(master):
    return {name: master / name for name in ("catalog", "captures", "preserved", "inputs")}


def backup_evidence(backup, attestation):
    """Where the completed snapshot is, and what proves it was completed."""
    manifest = backup / BACKUP_MANIFEST
    if manifest.is_file() and not manifest.is_symlink():
        raw = manifest.read_bytes()
        value = json.loads(raw)
        catalog = value.get("catalog") or {}
        relative = PurePosixPath(str(catalog.get("path", "")))
        if value.get("version") != 1 or value.get("complete") is not True or not value.get("finished_at"):
            raise ValueError("the backup manifest does not record a completed backup")
        if relative.is_absolute() or ".." in relative.parts or not relative.parts:
            raise ValueError("the backup manifest's catalog path is not inside the backup")
        snapshot = backup / relative
        s = snapshot.stat()
        if s.st_size != catalog.get("size") or s.st_mtime_ns != catalog.get("mtime_ns"):
            raise ValueError("the catalog differs from the snapshot the completed backup recorded")
        trees = {item["name"]: backup / PurePosixPath(item["path"]) for item in value.get("items", [])
                 if item.get("name") in ("captures", "preserved")}
        for tree in trees.values():
            if not contains(backup, tree.resolve()):
                raise ValueError("a backup manifest item is outside the backup")
        evidence = {"basis": "product-manifest", "manifest_sha256": hashlib.sha256(raw).hexdigest(),
                    "finished_at": value["finished_at"], "missing": value.get("missing", []),
                    "guarantee": "the product manifest records a completed backup; the main catalog matches its recorded size/mtime and no copied file changed while copied"}
        return evidence, manifest, raw, snapshot, trees
    if attestation is None:
        raise ValueError(f"no {BACKUP_MANIFEST}: pass --attestation with an explicit completed-backup record")
    if attestation.is_symlink() or not attestation.is_file():
        raise ValueError("the attestation must be a regular file")
    raw = attestation.read_bytes()
    value = json.loads(raw)
    if not all(isinstance(value.get(key), str) and value[key] for key in ("completed_at", "method", "attested_by")):
        raise ValueError("the attestation needs completed_at, method and attested_by")
    evidence = {"basis": "operator-attested", "attestation_sha256": hashlib.sha256(raw).hexdigest(),
                "completed_at": value["completed_at"], "method": value["method"],
                "guarantee": "completeness and offline state are the operator's statement; the tool verifies only that no copied file or the attestation changed while copied"}
    return evidence, attestation, raw, backup / "catalog/catalog.sqlite3", {name: backup / name for name in ("captures", "preserved")}


def prepare(backup, destination, attestation=None):
    """Only opens the private clone, never the completed backup database."""
    if backup.is_symlink() or destination.is_symlink():
        raise ValueError("backup/destination must not be symlinks")
    backup, destination = separate(backup, destination)
    if destination.exists():
        raise ValueError("choose a new experiment directory")
    evidence, evidence_file, evidence_raw, snapshot, trees = backup_evidence(backup, attestation)
    if not snapshot.is_file():
        raise ValueError("completed backup has no catalog snapshot")
    wal = snapshot.with_name(snapshot.name + "-wal")
    watched = {"catalog": snapshot, "catalog-wal": wal, "evidence": evidence_file, **trees}
    before = stamp(watched)
    destination.mkdir(parents=True, mode=0o700)
    try:
        (destination / "catalog").mkdir()
        clone(snapshot, destination / "catalog/catalog.sqlite3")
        if wal.exists():
            clone(wal, destination / "catalog/catalog.sqlite3-wal")
        for name in ("captures", "preserved"):
            if name in trees and trees[name].exists():
                clone(trees[name], destination / name)
            else:
                (destination / name).mkdir()
        (destination / "inputs").mkdir()
        if stamp(watched) != before or evidence_file.read_bytes() != evidence_raw:
            raise ValueError("backup or its evidence changed while cloning; no experiment was admitted")
        write_private(destination / "backup-evidence.json", evidence_raw)
        if wal.exists() and evidence["basis"] == "product-manifest":
            # The product snapshot is one file; a WAL beside it was written
            # after the manifest and is outside what it proves.
            evidence["catalog_wal"] = {"bytes": wal.stat().st_size, "covered_by_manifest": False}
        # SQLite recovers committed WAL into this clone before any run starts.
        with contextlib.closing(sqlite3.connect(destination / "catalog/catalog.sqlite3")) as db:
            db.execute("PRAGMA wal_checkpoint(TRUNCATE)")
            version = db.execute("SELECT value FROM meta WHERE key='schema_version'").fetchone()
            if version is None or int(version[0]) > SCHEMA_FLOOR:
                raise ValueError(f"corpus is outside the retained binary's version-{SCHEMA_FLOOR} compatibility window")
            counts = {name: db.execute(f'SELECT count(*) FROM "{name}"').fetchone()[0]
                      for name in ("workspaces", "conversations", "messages")}
        marker = {"version": VERSION, "role": "master", "backup": str(backup), "evidence": evidence,
                  "source_stamp_sha256": digest_of(before), "counts": counts,
                  "schema_version": int(version[0]), "prepared_at": time.time(), "inputs": {}}
        marker["master_stamp_sha256"] = digest_of(stamp(master_paths(destination)))
        write_json(destination / MARKER, marker)
        return marker
    except BaseException:
        # Only remove the new directory this invocation created.
        shutil.rmtree(destination)
        raise


def checked_master(master):
    marker = load_marker(master, "master")
    if digest_of(stamp(master_paths(master))) != marker["master_stamp_sha256"]:
        raise ValueError("the experiment master changed after preparation; prepare a new one")
    return marker


def freeze(master, name, path):
    """Copy a source folder or model input into the master once, before any arm."""
    if not NAME.match(name):
        raise ValueError("input names are short lowercase identifiers")
    master = master.resolve()
    marker = checked_master(master)
    if name in marker["inputs"]:
        raise ValueError("that input is already frozen")
    path, _ = separate(path.expanduser(), master)
    target = master / "inputs" / name
    before = stamp({"input": path})
    try:
        clone(path, target)
        if stamp({"input": path}) != before:
            raise ValueError("the input changed while it was frozen; nothing was admitted")
    except BaseException:
        if target.exists():
            shutil.rmtree(target) if target.is_dir() else target.unlink()
        raise
    marker["inputs"][name] = {**content_identity(target), "kind": "directory" if target.is_dir() else "file",
                              "frozen_at": time.time()}
    marker["master_stamp_sha256"] = digest_of(stamp(master_paths(master)))
    write_json(master / MARKER, marker)
    return marker["inputs"][name]


def input_name(value):
    if not isinstance(value, dict) or set(value) != {"input"} or not isinstance(value["input"], str):
        raise ValueError('path settings and sources name a frozen input: {"input": NAME}')
    return value["input"]


def validate_descriptor(descriptor):
    """Refuse an underspecified workload before any hours of work."""
    if not isinstance(descriptor.get("name"), str) or not descriptor["name"]:
        raise ValueError("descriptor needs a name")
    commands = descriptor.get("commands")
    if not isinstance(commands, list) or not commands:
        raise ValueError("descriptor needs explicit supported CLI commands")
    for command in [descriptor.get("startup_command"), *commands]:
        if not isinstance(command, list) or not command or not all(isinstance(part, str) for part in command) or command[0] not in SAFE_COMMANDS:
            raise ValueError("descriptor needs a startup_command and explicit supported CLI commands")
        if command[0] in ("labels", "agent-events") and (len(command) < 2 or command[1] not in ("run", "status")):
            raise ValueError("experiment does not fetch assets or import external records")
    if not isinstance(descriptor.get("expected_schema_version"), int):
        raise ValueError("descriptor pins the schema version startup must reach")
    for key in ("semantic_checks", "pending_checks"):
        checks = descriptor.get(key)
        if not isinstance(checks, dict) or not checks or not all(isinstance(k, str) and isinstance(v, str) and v.strip() for k, v in checks.items()):
            raise ValueError(f"descriptor needs non-empty {key}; nothing is proven without them")
    timeout = descriptor.get("timeout_seconds", 14400)
    if not isinstance(timeout, int) or timeout <= 0 or not isinstance(descriptor.get("timezone", "UTC"), str):
        raise ValueError("descriptor timeout/timezone are malformed")
    for key, value in descriptor.get("model_settings", {}).items():
        if key in INPUT_KEYS:
            input_name(value)
        elif key not in MODEL_KEYS or not isinstance(value, (str, int, bool)):
            raise ValueError("unsupported experiment model setting")
    for source in descriptor.get("sources", []):
        if set(source) != {"name", "kind", "input"} or not all(isinstance(source[k], str) for k in ("name", "kind")):
            raise ValueError("sources name a frozen input, never a live folder")
        input_name({"input": source["input"]})


def arm(master, destination, descriptor):
    master, destination = separate(master, destination)
    marker = checked_master(master)
    if destination.exists():
        raise ValueError("each arm needs a fresh directory")
    destination.mkdir(parents=True, mode=0o700)
    for name in ("catalog", "captures", "preserved"):
        clone(master / name, destination / name)
    for name in ("staging", "home", "tmp", "inputs", "logs"):
        (destination / name).mkdir(mode=0o700)

    def frozen(name):
        if name not in marker["inputs"]:
            raise ValueError(f"input {name!r} is not frozen in this master")
        target = destination / "inputs" / name
        if not target.exists():
            clone(master / "inputs" / name, target)
        return str(target)

    # No copied host/source files, API tokens, backup destinations, app broker,
    # cloud schedules, suggestions, or startup service loops.
    config = ["library = false", 'catalog_path = "catalog/catalog.sqlite3"',
              'archive_root = "preserved"', 'capture_root = "captures"', 'staging_root = "staging"',
              "enable_reclamation = false", 'github_token = ""', 'tl1_token = ""',
              "capture_compress_after_days = 0"]
    for key, value in descriptor.get("model_settings", {}).items():
        config.append(f"{key} = {json.dumps(frozen(input_name(value)) if key in INPUT_KEYS else value)}")
    for source in descriptor.get("sources", []):
        config += ["[[sources]]", f'name = {json.dumps(source["name"])}',
                   f'kind = {json.dumps(source["kind"])}', f'path = {json.dumps(frozen(source["input"]))}',
                   'account = "experiment"', 'enabled = true']
    path = destination / "archive.toml"
    write_private(path, ("\n".join(config) + "\n").encode())
    write_json(destination / MARKER, {**marker, "role": "arm", "master": str(master)})
    return path


def digests(database, checks):
    """Stream deterministic semantic queries; never export transcript values."""
    result = {}
    with read_only(database) as db:
        for name, query in checks.items():
            digest, count = hashlib.sha256(), 0
            for row in db.execute(query):
                payload = json.dumps(row, separators=(",", ":"), default=lambda x: {"hex": x.hex()}).encode()
                digest.update(len(payload).to_bytes(8, "big"))
                digest.update(payload)
                count += 1
            result[name] = {"rows": count, "sha256": digest.hexdigest()}
    return result


def schema_version(database):
    with read_only(database) as db:
        row = db.execute("SELECT value FROM meta WHERE key='schema_version'").fetchone()
    return None if row is None else int(row[0])


def run(binary, master, destination, descriptor, role, retained_sha256=None):
    """One arm. A failed, erroring or timed-out arm still leaves a report, unsettled."""
    validate_descriptor(descriptor)
    if role not in ("baseline", "candidate"):
        raise ValueError("role is baseline or candidate")
    if retained_sha256 is not None and not SHA256.match(retained_sha256):
        raise ValueError("retained artifact hash is a lowercase SHA-256")
    binary, destination = binary.resolve(strict=True), destination.resolve()
    binary_sha256 = file_sha256(binary)
    if role == "baseline":
        # The retained binary predates engine selection, so its identity is the proof.
        if binary_sha256 != retained_sha256:
            raise ValueError("a baseline arm runs the retained artifact; pass its recorded hash")
        engine = {"value": "classic", "basis": "retained-artifact-sha256"}
    else:
        if binary_sha256 == retained_sha256:
            raise ValueError("the candidate is the retained artifact; use two baseline arms for A/A")
        engine = {"value": None, "basis": "unavailable",
                  "reason": "this CLI does not report the engine its executed path selected"}
    config = arm(master, destination, descriptor)
    database = destination / "catalog/catalog.sqlite3"
    # Allowlisted: no host HOME, tokens, PHAROS_* settings or user tool configuration.
    env = {"PATH": os.environ.get("PATH", "/usr/bin:/bin"), "HOME": str(destination / "home"),
           "TMPDIR": str(destination / "tmp"), "TZ": descriptor.get("timezone", "UTC"), "LANG": "C",
           # A request only; the report never takes the engine from here.
           "PHAROS_WORK_ENGINE": "classic" if role == "baseline" else "scheduled"}
    timeout = descriptor.get("timeout_seconds", 14400)

    def execute(label, command):
        before, at = resource.getrusage(resource.RUSAGE_CHILDREN), time.monotonic()
        entry = {"command": command[0], "exit_code": None}
        log = os.open(destination / "logs" / f"{label}.log", os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
        try:
            entry["exit_code"] = subprocess.run([str(binary), "--config", str(config), *command], env=env,
                                                stdout=log, stderr=subprocess.STDOUT, timeout=timeout).returncode
        except subprocess.TimeoutExpired:
            entry["timed_out"] = True
        finally:
            os.close(log)
            after = resource.getrusage(resource.RUSAGE_CHILDREN)
            entry.update(seconds=time.monotonic()-at,
                         cpu_seconds=after.ru_utime+after.ru_stime-before.ru_utime-before.ru_stime)
        return entry

    report = {"version": VERSION, "role": role, "engine_actual": engine, "engine_requested": env["PHAROS_WORK_ENGINE"],
              "name": descriptor["name"], "binary_sha256": binary_sha256, "retained_sha256": retained_sha256,
              "descriptor_sha256": digest_of(descriptor), "corpus": load_marker(master, "master"),
              "startup": None, "commands": [], "settled": False, "failure": None,
              "resource_basis": "waited child user+system CPU; whole-process/descendant scope, not per-task attribution",
              "memory": {"basis": "unavailable", "reason": "use resource-history export for simultaneous peak; do not sum process peaks"},
              "product_readiness": {"basis": "unavailable", "reason": "CLI-only arm; use the product-query replica probe for T_correct/T_usable"}}
    try:
        # Startup migration/initialization is timed apart from the work it precedes.
        report["startup"] = execute("startup", descriptor["startup_command"])
        report["startup"]["schema_version"] = schema_version(database)
        if report["startup"].get("timed_out") or report["startup"]["exit_code"]:
            raise RuntimeError("startup command failed or timed out")
        if report["startup"]["schema_version"] != descriptor["expected_schema_version"]:
            raise RuntimeError("startup did not bring the catalog to the pinned schema; work timings would include migration")
        for index, command in enumerate(descriptor["commands"]):
            entry = execute(f"command-{index}", command)
            report["commands"].append(entry)
            if entry.get("timed_out") or entry["exit_code"]:
                raise RuntimeError(f"experiment command {index} failed or timed out; see its private log")
        report["wall_seconds"] = sum(entry["seconds"] for entry in report["commands"])
        report["cpu_seconds"] = sum(entry["cpu_seconds"] for entry in report["commands"])
        report["semantic"] = digests(database, descriptor["semantic_checks"])
        report["pending"] = digests(database, descriptor["pending_checks"])
        if any(check["rows"] for check in report["pending"].values()):
            raise RuntimeError("required domains have not settled; a command returning is not completion")
        report["settled"] = True
    except Exception as error:
        report["failure"] = {"type": type(error).__name__, "message": str(error)}
        raise
    finally:
        write_json(destination / "report.json", report)
    return report


def ratio(candidate, baseline):
    return candidate/baseline if baseline > 0 else None


def compare(baseline, candidate, mode="ab", retained_sha256=None):
    """Paired verdict. A/B needs the retained baseline and a proven candidate engine."""
    for report in (baseline, candidate):
        if report.get("version") != VERSION:
            raise ValueError("unrecognized arm report")
        if not report.get("settled"):
            raise ValueError("unsettled arm cannot be compared")
        if not report.get("semantic") or not report.get("pending") or any(c["rows"] for c in report["pending"].values()):
            raise ValueError("an arm without semantic and empty pending checks proves nothing")
    if baseline["descriptor_sha256"] != candidate["descriptor_sha256"] or baseline["corpus"] != candidate["corpus"]:
        raise ValueError("arms did not run the same corpus, frozen inputs and workload")
    if [c["command"] for c in baseline["commands"]] != [c["command"] for c in candidate["commands"]] \
            or baseline["startup"]["schema_version"] != candidate["startup"]["schema_version"]:
        raise ValueError("arms did not run the same commands from the same schema")
    if not retained_sha256 or baseline["binary_sha256"] != retained_sha256 or baseline["role"] != "baseline":
        raise ValueError("the baseline arm must be the retained artifact")
    blockers = []
    if mode == "aa":
        if candidate["binary_sha256"] != baseline["binary_sha256"] or candidate["role"] != "baseline":
            raise ValueError("A/A runs the retained artifact in both arms")
    elif mode == "ab":
        if candidate["role"] != "candidate" or candidate["binary_sha256"] == retained_sha256:
            raise ValueError("A/B compares the retained baseline with a different candidate artifact")
        engine = candidate.get("engine_actual") or {}
        if engine.get("value") != "scheduled" or engine.get("basis") in (None, "unavailable", "environment"):
            blockers.append("candidate's executed engine is unproven")
    else:
        raise ValueError("mode is ab or aa")
    parity = baseline["semantic"] == candidate["semantic"]
    ratios = {key: ratio(candidate[key], baseline[key]) for key in ("wall_seconds", "cpu_seconds")}
    # Each command is gated on its own, so a shared fixed cost cannot dilute a step's regression.
    steps = [{"index": index, "command": b["command"], "gated": b["seconds"] >= MIN_GATED_SECONDS,
              "wall": ratio(c["seconds"], b["seconds"]), "cpu": ratio(c["cpu_seconds"], b["cpu_seconds"])}
             for index, (b, c) in enumerate(zip(baseline["commands"], candidate["commands"]))]
    gated = [ratios[key] for key in ratios if baseline[key] >= MIN_GATED_SECONDS] + \
        [step[key] for step in steps if step["gated"] for key in ("wall", "cpu")]
    regression = any(value is not None and value >= REJECT_RATIO for value in gated)
    result = {"mode": mode, "semantic_parity": parity, "ratios": ratios, "steps": steps,
              "startup_ratio": ratio(candidate["startup"]["seconds"], baseline["startup"]["seconds"])}
    if mode == "aa":
        return {**result, "noise": "exceeds-gate" if regression or not parity else "within-gate",
                "release_gate": "not-applicable"}
    rejected = not parity or regression
    gate = "rejected" if rejected else "refused" if blockers else "incomplete"
    return {**result, "rejected": rejected, "release_gate": gate, "blockers": blockers,
            "remaining_evidence": ["simultaneous memory/I/O/model-load accounting", "actual product-query readiness",
                                   "reverse-order pair if noisy", "full required rebuild and old-binary recovery"]}


def main():
    p = argparse.ArgumentParser(description=__doc__)
    sub = p.add_subparsers(dest="action", required=True)
    prep = sub.add_parser("prepare")
    prep.add_argument("--completed-backup", required=True, type=Path)
    prep.add_argument("--master", required=True, type=Path)
    prep.add_argument("--attestation", type=Path, help=f"completed-backup record when the backup has no {BACKUP_MANIFEST}")
    fr = sub.add_parser("freeze")
    fr.add_argument("--master", required=True, type=Path)
    fr.add_argument("--name", required=True)
    fr.add_argument("--path", required=True, type=Path)
    run_p = sub.add_parser("run")
    run_p.add_argument("--binary", required=True, type=Path)
    run_p.add_argument("--master", required=True, type=Path)
    run_p.add_argument("--destination", required=True, type=Path)
    run_p.add_argument("--descriptor", required=True, type=Path)
    run_p.add_argument("--role", choices=["baseline", "candidate"], required=True)
    run_p.add_argument("--retained-sha256", required=True)
    comp = sub.add_parser("compare")
    comp.add_argument("baseline", type=Path)
    comp.add_argument("candidate", type=Path)
    comp.add_argument("--mode", choices=["ab", "aa"], default="ab")
    comp.add_argument("--retained-sha256", required=True)
    args = p.parse_args()
    if args.action == "prepare":
        value = prepare(args.completed_backup, args.master, args.attestation)
    elif args.action == "freeze":
        value = freeze(args.master, args.name, args.path)
    elif args.action == "run":
        value = run(args.binary, args.master, args.destination, json.loads(args.descriptor.read_text()),
                    args.role, args.retained_sha256)
    else:
        value = compare(json.loads(args.baseline.read_text()), json.loads(args.candidate.read_text()),
                        args.mode, args.retained_sha256)
    print(json.dumps(value, indent=2))


if __name__ == "__main__":
    main()
