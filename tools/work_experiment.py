"""Isolated, repeatable classic/scheduled work experiments.

This deliberately does not snapshot a live library. Preparation copies a completed
backup's files before opening SQLite, including any WAL beside its snapshot.
Source folders and model files are frozen once into the master as private
inputs; every measured arm clones fresh files from the master and never rereads
a configured folder. The installed app, sources, schedules and cloud settings
are unused. Private descriptors, catalogs, logs and reports stay outside
tracked files.

Version 3 artifacts are the only ones admitted as evidence. Version 1/2
masters, arms and reports stay diagnostic: they lack completion/derivation
evidence or resource accounting, and every command refuses them.
"""
import argparse
import contextlib
import ctypes
import datetime
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import sqlite3
import subprocess
import sys
import threading
import time

MARKER = ".pharos-work-experiment.json"
VERSION = 3
# The installed library on the development Mac, refused even if no registry names it.
LIVE = Path("/Volumes/euclid/Pharos")
BACKUP_MANIFEST = "pharos-backup.json"
# The retained baseline's commit and catalog schema come from the checked
# inventory, the one record the Go contract test also reads.
INVENTORY = Path(__file__).resolve().parent.parent / "docs/work-task-inventory.json"
_inventory = json.loads(INVENTORY.read_text())
SCHEMA_FLOOR = _inventory["catalog_compatibility_floor"]
BASELINE_COMMIT = _inventory["baseline_commit"]
SAFE_COMMANDS = {"build-tools", "findings", "labels", "agent-events", "health", "refine-usage"}
# A ratio over a few-second baseline is noise, not a regression.
MIN_GATED_SECONDS = 5.0
REJECT_RATIO = 1.2
MODEL_KEYS = {"labels_enabled", "labels_variant", "labels_backend", "labels_threads", "labels_workers",
              "labels_thread_budget", "labels_memory_budget_mb", "labels_batch_tokens", "labels_max_batch",
              "labels_idle_seconds", "agent_events_enabled",
              "agent_events_variant", "agent_events_threads", "agent_events_days"}
# Path settings name a frozen input, never a host path.
INPUT_KEYS = {"labels_model_dir", "labels_worker", "labels_runtime", "labels_mlx_runtime", "agent_events_model_dir"}
NAME = re.compile(r"^[a-z0-9][a-z0-9._-]{0,63}$")
SHA256 = re.compile(r"^[0-9a-f]{64}$")


def contains(parent, child):
    return parent == child or parent in child.parents


def support_dir():
    """Pharos's per-Mac support directory, as the service resolves it."""
    value = os.environ.get("PHAROS_SUPPORT_DIR")
    return Path(value).expanduser() if value else Path.home() / "Library/Application Support/Pharos"


def live_roots():
    """Every library Pharos on this Mac knows, from its own metadata only.

    Each application registry names its libraries' configs, which sit in the
    library directories; the support directory holds the default
    (unregistered) library's data and new local libraries. Only those JSON
    registries are read, never a catalog. An unreadable registry refuses
    rather than guessing which libraries are live."""
    support = support_dir()
    roots = {LIVE, support / "Libraries", *(support / name for name in (
        "catalog.sqlite3", "catalog.sqlite3-wal", "captures", "preserved", "staging"))}
    configs = set()
    roots.update(config_data_roots(support / "archive.toml"))
    for registry in sorted(support.glob("applications/*/registry.json")):
        try:
            libraries = json.loads(registry.read_text()).get("libraries") or []
        except (OSError, ValueError, AttributeError) as error:
            raise ValueError(f"cannot read the library registry {registry} ({error}); refusing to guess which libraries are live")
        for library in libraries:
            config = library.get("config") if isinstance(library, dict) else None
            if isinstance(config, str) and config:
                configs.add(Path(config).expanduser())
            relative = library.get("relative") if isinstance(library, dict) else None
            if isinstance(relative, str) and relative:
                relative = Path(relative)
                if relative.is_absolute() or ".." in relative.parts:
                    # Match registeredLibrary.resolve: stale non-local metadata
                    # never participates in remount resolution. Its explicit
                    # config and data paths are still guarded below.
                    continue
                # Conservatively cover every mount with that relative path.
                # No diskutil/UUID probe or catalog open is needed to cover
                # remounts such as /Volumes/euclid 1.
                mounts = Path("/Volumes")
                if mounts.is_dir():
                    configs.update(mount / relative for mount in mounts.iterdir() if mount.is_dir())
    for config in configs:
        roots.add(config.parent)
        roots.update(config_data_roots(config))
    return {root.resolve() for root in roots}


def config_data_roots(config):
    """Read only config path scalars; never initialize a library.

    Match Pharos's small config format without requiring Python 3.11 tomllib.
    Unrecognized path syntax refuses instead of guessing a live data location.
    """
    if not config.exists():
        return set()
    try:
        text = config.read_text()
        values, section, source = {}, "", False
        for line in text.splitlines():
            line = line.strip()
            if line.startswith("["):
                header = line.split("#", 1)[0].strip()
                source = header == "[[sources]]"
                section = header.strip("[]")
                continue
            if source or section in ("repositories.aliases", "repositories.separate"):
                continue
            key, sep, raw = line.partition("=")
            if not sep or key.strip() not in ("data_dir", "catalog_path", "capture_root", "archive_root", "staging_root"):
                continue
            raw = raw.strip()
            if raw.startswith('"'):
                value, end = json.JSONDecoder().raw_decode(raw)
            elif raw.startswith("'") and "'" in raw[1:]:
                end = raw.index("'", 1) + 1
                value = raw[1:end-1]
            else:
                raise ValueError("a library path must be a quoted string")
            if not isinstance(value, str) or (raw[end:].strip() and not raw[end:].strip().startswith("#")):
                raise ValueError("unsupported library path syntax")
            path = Path.home() / value[2:] if value.startswith("~/") else Path.home() if value == "~" else Path(value)
            values[key.strip()] = (config.parent / path).resolve()
        roots = set(values.values())
        if "catalog_path" in values:
            roots.add(values["catalog_path"].parent)
        return roots
    except (OSError, ValueError) as error:
        raise ValueError(f"cannot resolve library paths from {config} ({error}); refusing to guess") from error


def not_live(*paths):
    """Refuse any path inside, or containing, a library Pharos uses."""
    roots = live_roots()
    for path in paths:
        if any(contains(root, path) or contains(path, root) for root in roots):
            raise ValueError("the work experiment never opens or writes a library Pharos uses")


def separate(source, destination):
    source, destination = source.resolve(), destination.resolve()
    if contains(source, destination) or contains(destination, source):
        raise ValueError("experiment and input directories must be separate and non-nested")
    not_live(source, destination)
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
        if value.get("version") != 1 or value.get("complete") is not True or not value.get("finished_at") or value.get("missing"):
            raise ValueError("the backup manifest does not record a completed backup")
        snapshot_proof = backup / "work-snapshot.json"
        if snapshot_proof.exists():
            proof = json.loads(snapshot_proof.read_text())
            if proof.get("complete") is not True or proof.get("source_backup_history_unchanged") is not True:
                raise ValueError("the work snapshot's completion and source checks did not pass")
        if relative.is_absolute() or ".." in relative.parts or not relative.parts:
            raise ValueError("the backup manifest's catalog path is not inside the backup")
        snapshot = backup / relative
        if not contains(backup, snapshot.resolve()):
            raise ValueError("the backup manifest's catalog path is outside the backup")
        no_links(snapshot.parent)
        s = snapshot.stat()
        if s.st_size != catalog.get("size") or s.st_mtime_ns != catalog.get("mtime_ns"):
            raise ValueError("the catalog differs from the snapshot the completed backup recorded")
        trees = {item["name"]: backup / PurePosixPath(item["path"]) for item in value.get("items", [])
                 if item.get("name") in ("captures", "preserved")}
        for tree in trees.values():
            if not contains(backup, tree.resolve()):
                raise ValueError("a backup manifest item is outside the backup")
        # Config, library identity and hosts/ (per-host source declarations)
        # stay out of the master: arms write their own config and replay no
        # source, so the record names them instead of implying a full copy.
        evidence = {"basis": "product-manifest", "manifest_sha256": hashlib.sha256(raw).hexdigest(),
                    "finished_at": value["finished_at"], "missing": value.get("missing", []),
                    "excluded_items": sorted(str(item.get("name")) for item in value.get("items", [])
                                             if item.get("name") not in ("captures", "preserved")),
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
                "excluded_items": sorted(entry.name for entry in backup.iterdir()
                                         if entry.name not in ("catalog", "captures", "preserved")),
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


def invalidate(master, destination, profile, clock, recent_conversations=20):
    """Create a workload master with missing labels and dirty derived caches.

    Only the new clone is changed. Canonical records and writing classifications
    stay intact; this exercises label reconstruction rather than changing the
    quality of its upstream writing inputs to manufacture a backlog.
    """
    if profile not in ("recent", "full") or not isinstance(recent_conversations, int) or recent_conversations < 1:
        raise ValueError("invalidation is recent or full, with a positive conversation limit")
    until = rfc3339(clock)
    if until.tzinfo is None:
        raise ValueError("pin an offset-aware invalidation clock")
    master, destination = separate(master, destination)
    marker = checked_master(master)
    if destination.exists():
        raise ValueError("invalidation needs a fresh destination")
    destination.mkdir(mode=0o700)
    try:
        for name, source in master_paths(master).items():
            clone(source, destination / name)
        with contextlib.closing(sqlite3.connect(destination / "catalog/catalog.sqlite3")) as db:
            db.execute("BEGIN IMMEDIATE")
            db.execute("CREATE TEMP TABLE selected_conversations(id TEXT PRIMARY KEY)")
            if profile == "full":
                db.execute("INSERT INTO selected_conversations SELECT id FROM conversations")
            else:
                since = until - datetime.timedelta(hours=48)
                stamp_time = lambda t: t.astimezone(datetime.timezone.utc).isoformat(timespec="milliseconds").replace("+00:00", "Z")
                db.execute("""INSERT INTO selected_conversations
                    SELECT m.conversation_id FROM messages m WHERE m.created_at>=? AND m.created_at<?
                      AND EXISTS (SELECT 1 FROM message_labels l WHERE l.conversation_id=m.conversation_id)
                    GROUP BY m.conversation_id ORDER BY max(m.created_at) DESC,m.conversation_id LIMIT ?""",
                           (stamp_time(since), stamp_time(until), recent_conversations))
            ids = [row[0] for row in db.execute("SELECT id FROM selected_conversations ORDER BY id")]
            if not ids:
                raise ValueError("invalidation selected no conversations")
            removed = {}
            for table in ("message_labels", "message_labels_conversations"):
                removed[table] = db.execute(f"DELETE FROM {table} WHERE conversation_id IN (SELECT id FROM selected_conversations)").rowcount
            db.execute("INSERT OR REPLACE INTO message_labels_dirty SELECT id,hex(randomblob(8)) FROM selected_conversations")
            db.execute("INSERT OR REPLACE INTO parallelism_dirty SELECT id,hex(randomblob(8)) FROM selected_conversations")
            db.execute("INSERT OR IGNORE INTO message_label_facts_dirty SELECT id FROM selected_conversations")
            db.commit()
            db.execute("PRAGMA wal_checkpoint(TRUNCATE)")
        if checked_master(master) != marker:
            raise ValueError("input master changed during invalidation")
        event = {"event": "derived-invalidation", "profile": profile, "clock": clock,
                 "conversations": len(ids), "population_sha256": digest_of(ids), "removed_rows": removed,
                 "canonical_records": "unchanged", "writing_classifications": "unchanged"}
        derived = {**marker, "derived_from": str(master),
                   "lineage": [*marker.get("lineage", []), event], "prepared_at": time.time()}
        derived["master_stamp_sha256"] = digest_of(stamp(master_paths(destination)))
        write_json(destination / MARKER, derived)
        return derived
    except BaseException:
        shutil.rmtree(destination)
        raise


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
    """A frozen input reference: {"input": NAME} or {"input": NAME, "path": RELATIVE}."""
    if not isinstance(value, dict) or not set(value) <= {"input", "path"} or not isinstance(value.get("input"), str):
        raise ValueError('path settings and sources name a frozen input: {"input": NAME}')
    if "path" in value:
        relative = PurePosixPath(value["path"]) if isinstance(value["path"], str) else None
        if relative is None or relative.is_absolute() or ".." in relative.parts or not relative.parts:
            raise ValueError("a frozen input's path is relative and stays inside the input")
    return value["input"]


def read_only_statement(query):
    words = query.strip().rstrip(";").split()
    return bool(words) and words[0].upper() in ("SELECT", "WITH") and ";" not in query.strip().rstrip(";")


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
    semantic, pending = descriptor.get("semantic_checks"), descriptor.get("pending_checks")
    if not isinstance(semantic, dict) or not semantic or not all(
            isinstance(k, str) and isinstance(v, str) and read_only_statement(v) for k, v in semantic.items()):
        raise ValueError("descriptor needs non-empty read-only semantic_checks; nothing is proven without them")
    # The same shape as a probe workload's: each domain lists its read-only checks.
    if not isinstance(pending, dict) or not pending or not all(
            isinstance(k, str) and isinstance(v, list) and v and all(isinstance(q, str) and read_only_statement(q) for q in v)
            for k, v in pending.items()):
        raise ValueError("descriptor needs non-empty pending_checks: each domain lists read-only statements")
    timeout = descriptor.get("timeout_seconds", 14400)
    if not isinstance(timeout, int) or timeout <= 0 or not isinstance(descriptor.get("timezone", "UTC"), str):
        raise ValueError("descriptor timeout/timezone are malformed")
    for key, value in descriptor.get("model_settings", {}).items():
        if key in INPUT_KEYS:
            input_name(value)
        elif key not in MODEL_KEYS or not isinstance(value, (str, int, bool)):
            raise ValueError("unsupported experiment model setting")
    if "labels_model_dir" in descriptor.get("model_settings", {}) and descriptor["model_settings"].get("labels_variant") not in ("fp32", "int8"):
        raise ValueError("a frozen labels_model_dir needs an explicit labels_variant: custom paths change the default")
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

    def frozen(value):
        name = input_name(value)
        if name not in marker["inputs"]:
            raise ValueError(f"input {name!r} is not frozen in this master")
        target = destination / "inputs" / name
        if not target.exists():
            clone(master / "inputs" / name, target)
        if "path" in value:
            target = target / PurePosixPath(value["path"])
            if not target.exists():
                raise ValueError(f"input {name!r} has no {value['path']!r}")
        return str(target)

    # No copied host/source files, API tokens, backup destinations, app broker,
    # cloud schedules, suggestions, or startup service loops.
    config = ["library = false", 'catalog_path = "catalog/catalog.sqlite3"',
              'archive_root = "preserved"', 'capture_root = "captures"', 'staging_root = "staging"',
              "enable_reclamation = false", 'github_token = ""', 'tl1_token = ""',
              "capture_compress_after_days = 0"]
    for key, value in descriptor.get("model_settings", {}).items():
        config.append(f"{key} = {json.dumps(frozen(value) if key in INPUT_KEYS else value)}")
    if "replay" in descriptor:
        # The probe stages arrivals only from ARM/inputs/<input>.
        frozen({"input": descriptor["replay"]["input"]})
    for source in descriptor.get("sources", []):
        config += ["[[sources]]", f'name = {json.dumps(source["name"])}',
                   f'kind = {json.dumps(source["kind"])}', f'path = {json.dumps(frozen({"input": source["input"]}))}',
                   'account = "experiment"', 'enabled = true']
    path = destination / "archive.toml"
    write_private(path, ("\n".join(config) + "\n").encode())
    write_json(destination / MARKER, {**marker, "role": "arm", "master": str(master)})
    return path


# Model inference runs in helper processes; their names identify model loads.
MODEL_WORKERS = {"pharos-labeler-ort"}
# Simultaneous memory/I/O are gated only above these baselines.
MIN_GATED_BYTES = 64 << 20


class _RusageInfoV4(ctypes.Structure):
    _fields_ = [("uuid", ctypes.c_uint8 * 16)] + [(name, ctypes.c_uint64) for name in (
        "user_time", "system_time", "pkg_idle_wkups", "interrupt_wkups", "pageins", "wired_size",
        "resident_size", "phys_footprint", "proc_start_abstime", "proc_exit_abstime", "child_user_time",
        "child_system_time", "child_pkg_idle_wkups", "child_interrupt_wkups", "child_pageins",
        "child_elapsed_abstime", "diskio_bytesread", "diskio_byteswritten", "cpu_time_qos_default",
        "cpu_time_qos_maintenance", "cpu_time_qos_background", "cpu_time_qos_utility", "cpu_time_qos_legacy",
        "cpu_time_qos_user_initiated", "cpu_time_qos_user_interactive", "billed_system_time",
        "serviced_system_time", "logical_writes", "lifetime_max_phys_footprint", "instructions", "cycles",
        "billed_energy", "serviced_energy", "interval_max_phys_footprint", "runnable_time")]


def _libproc():
    if sys.platform != "darwin":
        return None
    try:
        libc = ctypes.CDLL(None)
        libc.proc_pid_rusage.argtypes = [ctypes.c_int, ctypes.c_int, ctypes.c_void_p]
        return libc
    except (OSError, AttributeError):
        return None


def process_table():
    """pid -> (ppid, pgid, %cpu, executable name). Names only, never arguments."""
    output = subprocess.run(["/bin/ps", "-A", "-o", "pid=,ppid=,pgid=,%cpu=,comm="], capture_output=True,
                            text=True, check=True).stdout
    table = {}
    for line in output.splitlines():
        parts = line.split(None, 4)
        if len(parts) == 5:
            table[int(parts[0])] = (int(parts[1]), int(parts[2]), float(parts[3]), os.path.basename(parts[4]))
    return table


class TreeSampler(threading.Thread):
    """Samples a process tree about once a second.

    Simultaneous memory is the largest observed sum of the tree's physical
    footprints, so a spike between samples can be missed. Disk bytes are each
    process's counters at its last sample: a process's final second before it
    exits is missed, so they are lower bounds. CPU is never taken from here;
    the command's wait4 rusage is the CPU total. Other processes' CPU is recorded
    as host noise, never subtracted.
    """

    def __init__(self, root_pid, interval=1.0):
        super().__init__(daemon=True)
        self.root, self.interval, self.stop_event = root_pid, interval, threading.Event()
        self.libc, self.started = _libproc(), time.monotonic()
        self.processes, self.samples, self.errors = {}, 0, []
        self.peak = {"footprint_bytes": 0, "resident_bytes": 0, "processes": 0, "at_seconds": None}
        self.noise = {"load1": [], "other_cpu_percent": []}
        # (Unix time, {process: (footprint, read, written)}, other-process CPU) per sample,
        # so a run's own phase boundaries can scope memory, disk and noise afterwards.
        self.series = []

    def tree(self, table):
        members, frontier = {self.root}, [self.root]
        children = {}
        for pid, (ppid, *_rest) in table.items():
            children.setdefault(ppid, []).append(pid)
        while frontier:
            for child in children.get(frontier.pop(), []):
                if child not in members:
                    members.add(child)
                    frontier.append(child)
        # A reparented descendant stays in the command's process group.
        members |= {pid for pid, row in table.items() if row[1] == self.root}
        return {pid for pid in members if pid in table}

    def sample(self):
        table = process_table()
        members = self.tree(table)
        now = time.monotonic() - self.started
        footprint = resident = 0
        at, readings = time.time(), {}
        for pid in members:
            info = _RusageInfoV4()
            if self.libc is None or self.libc.proc_pid_rusage(pid, 4, ctypes.byref(info)) != 0:
                continue
            key = f"{pid}:{info.proc_start_abstime}"
            entry = self.processes.setdefault(key, {"name": table[pid][3], "first_seconds": now})
            entry.update(last_seconds=now, disk_read_bytes=info.diskio_bytesread,
                         disk_written_bytes=info.diskio_byteswritten,
                         lifetime_max_footprint_bytes=info.lifetime_max_phys_footprint)
            readings[key] = (info.phys_footprint, info.diskio_bytesread, info.diskio_byteswritten)
            footprint += info.phys_footprint
            resident += info.resident_size
        self.samples += 1
        if footprint > self.peak["footprint_bytes"]:
            self.peak = {"footprint_bytes": footprint, "resident_bytes": resident, "processes": len(members),
                         "at_seconds": now}
        self.noise["load1"].append(os.getloadavg()[0])
        other = sum(row[2] for pid, row in table.items() if pid not in members)
        self.noise["other_cpu_percent"].append(other)
        self.series.append((at, readings, other))

    def run(self):
        while not self.stop_event.is_set():
            try:
                self.sample()
            except Exception as error:  # a sampling gap is reported, never fatal
                if len(self.errors) < 5:
                    self.errors.append(f"{type(error).__name__}: {error}")
            self.stop_event.wait(self.interval)

    def stop(self):
        self.stop_event.set()
        self.join()

    def window(self, start, end):
        """Simultaneous memory, disk bytes and host noise within [start, end] (Unix seconds).

        Each figure is as exact as its 1 s samples: a process's disk bytes are
        its last reading inside the window less its last reading at or before
        the start (0 if first seen inside), so each edge is within about one
        sample interval, and I/O after a process's final sample is missed."""
        if self.libc is None:
            return {"basis": "unavailable", "reason": "no libproc process sampling on this host"}
        inside = [sample for sample in self.series if start <= sample[0] <= end]
        if not inside:
            return {"basis": "unavailable", "reason": "no process-tree sample fell inside this window"}
        before, last = {}, {}
        for at, readings, _other in self.series:
            if at > start:
                break
            before.update(readings)
        for _at, readings, _other in inside:
            last.update(readings)
        disk = lambda index: sum(reading[index] - before.get(key, (0, 0, 0))[index] for key, reading in last.items())
        noise = [other for *_rest, other in inside]
        return {"basis": "1s process-tree samples inside the window; footprint peak can miss spikes; disk bytes "
                         "are within one sample interval at each edge",
                "samples": len(inside),
                "peak_footprint_bytes": max(sum(r[0] for r in readings.values()) for _at, readings, _o in inside),
                "disk_read_bytes": disk(1), "disk_written_bytes": disk(2),
                "host_noise": {"other_cpu_percent_mean": sum(noise) / len(noise), "other_cpu_percent_max": max(noise),
                               "basis": "ps %cpu of every process outside the measured tree (100 = one core)"}}

    def summary(self):
        if self.libc is None or not self.samples:
            return {"basis": "unavailable", "reason": "no libproc process sampling on this host"}
        workers = [p for p in self.processes.values() if p["name"] in MODEL_WORKERS]
        mean = lambda values: sum(values) / len(values) if values else None
        return {
            "basis": "1s process-tree samples via proc_pid_rusage; footprint peak is the largest sampled "
                     "simultaneous sum and can miss spikes; disk bytes are lower bounds (final second of each "
                     "process missed); CPU comes from the command's wait4 rusage, not samples",
            "samples": self.samples, "errors": self.errors,
            "peak_simultaneous": self.peak,
            "max_process_lifetime_footprint_bytes": max(
                (p["lifetime_max_footprint_bytes"] for p in self.processes.values()), default=0),
            "disk_read_bytes": sum(p["disk_read_bytes"] for p in self.processes.values()),
            "disk_written_bytes": sum(p["disk_written_bytes"] for p in self.processes.values()),
            "processes_observed": len(self.processes),
            "model_workers": {"started": len(workers),
                              "first_seen_seconds": min((p["first_seconds"] for p in workers), default=None),
                              "basis": "distinct labeler worker processes seen at 1s sampling; one per model load"},
            "host_noise": {"cpus": os.cpu_count(), "load1_mean": mean(self.noise["load1"]),
                           "load1_max": max(self.noise["load1"], default=None),
                           "other_cpu_percent_mean": mean(self.noise["other_cpu_percent"]),
                           "other_cpu_percent_max": max(self.noise["other_cpu_percent"], default=None),
                           "basis": "ps %cpu of every process outside the measured tree (100 = one core); "
                                    "recorded, never subtracted"},
        }


# Recorded on every measured entry and arm; arms with different CPU bases never compare.
# Basis 1 (unversioned reports) was the harness's RUSAGE_CHILDREN delta, which also
# charged the sampler's own ps processes to the command.
CPU_BASIS = "wait4-command-v2"


def _wait_command(process, timeout):
    """Wait for one command, killing its group at the timeout; returns (status, rusage, timed_out).

    The exit is observed without reaping (WNOWAIT), so the pid and its group
    cannot be reused while the timer may still signal them; then wait4 reaps
    exactly this command and returns its own rusage. Popen is told the result
    so it never waits for the pid again."""
    lock, state = threading.Lock(), {"exited": False, "timed_out": False}

    def expire():
        with lock:
            if not state["exited"]:
                state["timed_out"] = True
                with contextlib.suppress(ProcessLookupError, PermissionError):
                    os.killpg(process.pid, 9)

    timer = threading.Timer(timeout, expire)
    timer.daemon = True
    timer.start()
    try:
        os.waitid(os.P_PID, process.pid, os.WEXITED | os.WNOWAIT)
        with lock:
            state["exited"] = True
    finally:
        timer.cancel()
    _pid, status, usage = os.wait4(process.pid, 0)
    process.returncode = os.waitstatus_to_exitcode(status)
    return process.returncode, usage, state["timed_out"]


def supervised(argv, env, log_path, timeout, cwd=None, windows=None):
    """Run one measured command in its own process group with tree sampling.

    CPU is the command's own wait4 rusage: the command and every descendant
    it waited for, and nothing else this harness runs (the sampler's ps
    processes are other children of this process). Descendants still alive
    when it exits are killed and reported, because their CPU would otherwise
    be missing. windows, called once the command has exited, names the
    command's own phases as Unix-time spans; each gets the samples inside it.
    """
    at = time.monotonic()
    entry = {"command": argv[1] if len(argv) > 1 else argv[0], "exit_code": None, "cpu_basis": CPU_BASIS,
             "cpu_seconds": None}
    log = os.open(log_path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    sampler = process = None
    try:
        process = subprocess.Popen(argv, env=env, cwd=cwd, stdout=log, stderr=subprocess.STDOUT,
                                   start_new_session=True)
        sampler = TreeSampler(process.pid)
        sampler.start()
        code, usage, timed_out = _wait_command(process, timeout)
        entry["cpu_seconds"] = usage.ru_utime + usage.ru_stime
        if timed_out:
            entry["timed_out"] = True
        else:
            entry["exit_code"] = code
        leftover = [pid for pid, row in process_table().items() if row[1] == process.pid]  # same process group
        if leftover:
            entry["orphaned_descendants"] = len(leftover)
            with contextlib.suppress(ProcessLookupError):
                os.killpg(process.pid, 9)
    finally:
        if process is not None and process.returncode is None:
            # Interrupted before the command was reaped: never leave it running.
            with contextlib.suppress(ProcessLookupError, PermissionError):
                os.killpg(process.pid, 9)
            process.wait()
        os.close(log)
        if sampler is not None:
            sampler.stop()
        entry.update(seconds=time.monotonic() - at,
                     resources=sampler.summary() if sampler is not None else {"basis": "unavailable"})
        if windows is not None and sampler is not None:
            try:
                spans = windows()
            except (OSError, ValueError) as error:
                spans, entry["windows_error"] = {}, f"{type(error).__name__}: {error}"
            entry["windows"] = {name: sampler.window(*span) for name, span in spans.items()}
    return entry


def same_cpu_basis(*reports):
    """Refuse arms measured with a different, or the legacy, CPU accounting."""
    bases = {report.get("cpu_basis") for report in reports}
    if bases != {CPU_BASIS}:
        raise ValueError(f"arms must all use CPU basis {CPU_BASIS}; found {sorted(map(str, bases))}. "
                         "Legacy (unversioned) arms charged sampler CPU to the command; rerun them")


# Row digests are summed modulo 2**256: a multiset digest, so a check
# without ORDER BY cannot differ between arms only by SQLite's row order.
DIGEST_BASIS = "multiset-sha256-v1"


def flat_pending(pending):
    """Each domain's pending statements as individually digested checks."""
    return {f"{unit}#{index}": query for unit, queries in pending.items() for index, query in enumerate(queries)}


def same_digest_basis(*reports):
    """Refuse arms whose check digests were computed another way."""
    bases = {check.get("basis") for report in reports for key in ("semantic", "pending")
             for check in (report.get(key) or {}).values()}
    if bases != {DIGEST_BASIS}:
        raise ValueError(f"arms must all use digest basis {DIGEST_BASIS}; found {sorted(map(str, bases))}. "
                         "Earlier arms hashed rows in query order; rerun them")


def digests(database, checks):
    """Stream semantic queries into order-independent digests; never export transcript values."""
    result = {}
    with read_only(database) as db:
        for name, query in checks.items():
            total, count = 0, 0
            for row in db.execute(query):
                payload = json.dumps(row, separators=(",", ":"), default=lambda x: {"hex": x.hex()}).encode()
                total = (total + int.from_bytes(hashlib.sha256(payload).digest(), "big")) % (1 << 256)
                count += 1
            result[name] = {"rows": count, "sha256": hashlib.sha256(
                count.to_bytes(8, "big") + total.to_bytes(32, "big")).hexdigest(), "basis": DIGEST_BASIS}
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
        entry = supervised([str(binary), "--config", str(config), *command], env,
                           destination / "logs" / f"{label}.log", timeout)
        entry["command"] = command[0]
        return entry

    report = {"version": VERSION, "role": role, "engine_actual": engine, "engine_requested": env["PHAROS_WORK_ENGINE"],
              "name": descriptor["name"], "binary_sha256": binary_sha256, "retained_sha256": retained_sha256,
              "descriptor_sha256": digest_of(descriptor), "corpus": load_marker(master, "master"),
              "startup": None, "commands": [], "settled": False, "failure": None,
              "cpu_basis": CPU_BASIS,
              "resource_basis": "each command's wait4 user+system CPU (the command and descendants it waited for); "
                                "whole-process scope, not per-task attribution; "
                                "per-command simultaneous footprint/disk samples in each entry's resources",
              "product_readiness": {"basis": "unavailable", "reason": "CLI-only arm; use the product-query replica probe for T_correct/T_usable"}}
    try:
        # Startup migration/initialization is timed apart from the work it precedes.
        report["startup"] = execute("startup", descriptor["startup_command"])
        report["startup"]["schema_version"] = schema_version(database)
        if report["startup"].get("timed_out") or report["startup"]["exit_code"] or report["startup"].get("orphaned_descendants"):
            raise RuntimeError("startup command failed, timed out or left descendants")
        if report["startup"]["schema_version"] != descriptor["expected_schema_version"]:
            raise RuntimeError("startup did not bring the catalog to the pinned schema; work timings would include migration")
        for index, command in enumerate(descriptor["commands"]):
            entry = execute(f"command-{index}", command)
            report["commands"].append(entry)
            if entry.get("timed_out") or entry["exit_code"] or entry.get("orphaned_descendants"):
                raise RuntimeError(f"experiment command {index} failed, timed out or left processes; see its private log")
        report["wall_seconds"] = sum(entry["seconds"] for entry in report["commands"])
        report["cpu_seconds"] = sum(entry["cpu_seconds"] for entry in report["commands"])
        report["semantic"] = digests(database, descriptor["semantic_checks"])
        report["pending"] = digests(database, flat_pending(descriptor["pending_checks"]))
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


def gate(baseline, candidate, floor):
    """One metric's pair. It is gated when either arm reaches the floor, so a
    candidate cannot climb from below the floor to far above it unnoticed;
    from a zero baseline, reaching the floor is itself a regression."""
    if baseline is None or candidate is None:
        return {"baseline": baseline, "candidate": candidate, "gated": False, "ratio": None, "regression": False}
    value = ratio(candidate, baseline)
    gated = max(baseline, candidate) >= floor
    return {"baseline": baseline, "candidate": candidate, "gated": gated, "ratio": value,
            "regression": gated and (value is None or value >= REJECT_RATIO)}


def compare(baseline, candidate, mode="ab", retained_sha256=None):
    """Paired verdict. A/B needs the retained baseline and a proven candidate engine."""
    for report in (baseline, candidate):
        if report.get("version") != VERSION:
            raise ValueError("unrecognized arm report")
        if not report.get("settled"):
            raise ValueError("unsettled arm cannot be compared")
        if not report.get("semantic") or not report.get("pending") or any(c["rows"] for c in report["pending"].values()):
            raise ValueError("an arm without semantic and empty pending checks proves nothing")
    same_cpu_basis(baseline, candidate)
    same_digest_basis(baseline, candidate)
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
    totals = {key: gate(baseline[key], candidate[key], MIN_GATED_SECONDS) for key in ("wall_seconds", "cpu_seconds")}
    # Each command is gated on its own, so a shared fixed cost cannot dilute a
    # step's regression. Wall and CPU are gated separately: a mostly-waiting
    # step's long wall time does not make its tiny CPU a gate.
    steps = [{"index": index, "command": b["command"], "wall": gate(b["seconds"], c["seconds"], MIN_GATED_SECONDS),
              "cpu": gate(b["cpu_seconds"], c["cpu_seconds"], MIN_GATED_SECONDS)}
             for index, (b, c) in enumerate(zip(baseline["commands"], candidate["commands"]))]
    # Startup is timed apart from the work, and gated apart too.
    startup = {key: gate(_metric(baseline, ("startup", path)), _metric(candidate, ("startup", path)), MIN_GATED_SECONDS)
               for key, path in (("wall", "seconds"), ("cpu", "cpu_seconds"))}
    regression = any(entry["regression"] for entry in
                     [*totals.values(), *startup.values(), *(step[key] for step in steps for key in ("wall", "cpu"))])
    result = {"mode": mode, "semantic_parity": parity, "totals": totals, "steps": steps, "startup": startup}
    if mode == "aa":
        return {**result, "noise": "exceeds-gate" if regression or not parity else "within-gate",
                "release_gate": "not-applicable"}
    rejected = not parity or regression
    verdict = "rejected" if rejected else "refused" if blockers else "incomplete"
    return {**result, "rejected": rejected, "release_gate": verdict, "blockers": blockers,
            "remaining_evidence": ["simultaneous memory/I/O/model-load accounting", "actual product-query readiness",
                                   "reverse-order pair if noisy", "full required rebuild and old-binary recovery"]}


def integrity(db, mode):
    """SQLite's own consistency check on a private clone; never on a source."""
    if mode == "none":
        return {"mode": "none", "result": "not checked"}
    at = time.monotonic()
    rows = [row[0] for row in db.execute(f"PRAGMA {'quick_check' if mode == 'quick' else 'integrity_check'}")]
    if rows != ["ok"]:
        raise ValueError(f"the clone failed {mode}_check; it is not admitted")
    return {"mode": mode, "result": "ok", "seconds": time.monotonic() - at}


def catalog_facts(database, check):
    """Recover any WAL into this clone, check it, and count its corpus."""
    with contextlib.closing(sqlite3.connect(database)) as db:
        db.execute("PRAGMA wal_checkpoint(TRUNCATE)")
        checked = integrity(db, check)
        version = db.execute("SELECT value FROM meta WHERE key='schema_version'").fetchone()
        if version is None or int(version[0]) > SCHEMA_FLOOR:
            raise ValueError(f"corpus is outside the retained binary's version-{SCHEMA_FLOOR} compatibility window")
        counts = {name: db.execute(f'SELECT count(*) FROM "{name}"').fetchone()[0]
                  for name in ("workspaces", "conversations", "messages")}
    return int(version[0]), counts, checked


def tree_listing(path):
    """Relative names and sizes only: a metadata comparison, no content reads."""
    if not path.is_dir():
        return {}
    return {str(Path(base, name).relative_to(path)): (Path(base) / name).stat().st_size
            for base, _, names in os.walk(path, followlinks=False) for name in names if name != ".DS_Store"}


def observe_original(original, snapshot):
    """Stat-only observations of the backup a diagnostic master was copied from.

    These are recorded facts, not completion or offline evidence."""
    original = original.resolve()
    not_live(original)
    catalog = original / "catalog"
    files = {}
    for name in ("catalog.sqlite3", "catalog.sqlite3-wal", "catalog.sqlite3-shm"):
        if (catalog / name).is_file():
            st = (catalog / name).stat()
            files[name] = {"bytes": st.st_size, "mtime": datetime.datetime.fromtimestamp(
                st.st_mtime, datetime.timezone.utc).isoformat(), "inode": st.st_ino}
    main, copy = catalog / "catalog.sqlite3", snapshot / "catalog/catalog.sqlite3"
    captures = tree_listing(original / "captures") == tree_listing(snapshot / "captures")
    preserved = tree_listing(original / "preserved") == tree_listing(snapshot / "preserved")
    has_manifest = (original / BACKUP_MANIFEST).is_file()
    notes = []
    if "catalog.sqlite3-shm" in files and files["catalog.sqlite3-shm"]["mtime"] > files.get("catalog.sqlite3", {}).get("mtime", ""):
        notes.append("the backup's -shm is newer than its catalog: a SQLite connection opened the backup after it was written")
    return {"path": str(original), "files": files, "product_manifest": has_manifest,
            "main_catalog_bytes_match": main.is_file() and main.stat().st_size == copy.stat().st_size,
            "capture_names_and_sizes_match": captures, "preserved_names_and_sizes_match": preserved,
            "notes": notes, "basis": "stat-only observation; no SQL or content read on the original"}


def import_snapshot(source, destination, original=None, check="quick"):
    """Admit an existing (diagnostic) experiment master as a new version-3 master.

    Copies its files without opening SQLite on them, refuses any change while
    copying, then recovers and checks only the private clone. The provenance
    records exactly what was observed. Nothing here claims the original backup
    completed or that its source was offline."""
    if source.is_symlink() or destination.is_symlink():
        raise ValueError("source/destination must not be symlinks")
    source, destination = separate(source, destination)
    if destination.exists():
        raise ValueError("choose a new experiment directory")
    raw_marker = (source / MARKER).read_bytes()
    old = json.loads(raw_marker)
    if old.get("role") != "master":
        raise ValueError("import-snapshot derives only from an experiment master")
    snapshot = source / "catalog/catalog.sqlite3"
    if not snapshot.is_file():
        raise ValueError("the source master has no catalog")
    watched = {"catalog": snapshot, "catalog-wal": snapshot.with_name("catalog.sqlite3-wal"),
               "captures": source / "captures", "preserved": source / "preserved", "inputs": source / "inputs",
               "marker": source / MARKER}
    for path in watched.values():
        if path.exists():
            no_links(path)
    before = stamp(watched)
    destination.mkdir(parents=True, mode=0o700)
    try:
        (destination / "catalog").mkdir()
        clone(snapshot, destination / "catalog/catalog.sqlite3")
        if watched["catalog-wal"].exists():
            clone(watched["catalog-wal"], destination / "catalog/catalog.sqlite3-wal")
        for name in ("captures", "preserved"):
            clone(source / name, destination / name) if (source / name).exists() else (destination / name).mkdir()
        (destination / "inputs").mkdir()
        if stamp(watched) != before or (source / MARKER).read_bytes() != raw_marker:
            raise ValueError("the source master changed while it was copied; nothing was admitted")
        version, counts, checked = catalog_facts(destination / "catalog/catalog.sqlite3", check)
        evidence = {
            "basis": "derived-experiment-snapshot", "completion": "unattested", "offline": "unattested",
            "derived_from": {"path": str(source), "marker_version": old.get("version"),
                             "marker_sha256": hashlib.sha256(raw_marker).hexdigest(),
                             "recorded_backup": old.get("backup"), "recorded_counts": old.get("counts"),
                             "recorded_schema_version": old.get("schema_version"),
                             "stamp_sha256": digest_of(before)},
            "integrity": checked,
            "guarantee": "the catalog is SQLite-consistent after WAL recovery on this private clone and no "
                         "file of the derived-from master changed while copied; neither the original backup's "
                         "completion nor its source's offline state is proven",
        }
        if original is not None:
            evidence["original_observation"] = observe_original(original, source)
        if old.get("counts") not in (None, counts):
            raise ValueError("the clone's corpus differs from the counts its source master recorded")
        marker = {"version": VERSION, "role": "master", "backup": old.get("backup"), "evidence": evidence,
                  "source_stamp_sha256": digest_of(before), "counts": counts, "schema_version": version,
                  "prepared_at": time.time(), "inputs": {},
                  "lineage": [{"step": "import-snapshot", "from_marker_sha256": hashlib.sha256(raw_marker).hexdigest()}]}
        marker["master_stamp_sha256"] = digest_of(stamp(master_paths(destination)))
        write_json(destination / MARKER, marker)
        return marker
    except BaseException:
        shutil.rmtree(destination)
        raise


def normalize(master, destination, binary, retained_sha256, startup_command, expected_schema, check="quick"):
    """Run the retained binary's startup once on a clone and freeze the result.

    Every later arm then starts at the pinned schema, so no arm's startup
    includes the migration. The derived master keeps its parent's evidence
    and inputs and appends the step to its lineage."""
    if not SHA256.match(retained_sha256 or ""):
        raise ValueError("pass the retained artifact's SHA-256")
    binary = binary.resolve(strict=True)
    if file_sha256(binary) != retained_sha256:
        raise ValueError("normalization runs only the retained artifact")
    if not isinstance(startup_command, list) or not startup_command or startup_command[0] not in SAFE_COMMANDS:
        raise ValueError("normalization runs one supported local startup command")
    master, destination = separate(master, destination)
    parent = checked_master(master)
    if destination.exists():
        raise ValueError("choose a new experiment directory")
    destination.mkdir(parents=True, mode=0o700)
    try:
        for name in ("catalog", "captures", "preserved", "inputs"):
            clone(master / name, destination / name)
        work_dir = destination / "normalize"
        for name in ("home", "tmp", "logs", "staging"):
            (work_dir / name).mkdir(parents=True, mode=0o700)
        config = work_dir / "archive.toml"
        write_private(config, ("\n".join([
            "library = false", f'catalog_path = {json.dumps(str(destination / "catalog/catalog.sqlite3"))}',
            f'archive_root = {json.dumps(str(destination / "preserved"))}',
            f'capture_root = {json.dumps(str(destination / "captures"))}',
            f'staging_root = {json.dumps(str(work_dir / "staging"))}', "enable_reclamation = false",
            'github_token = ""', 'tl1_token = ""', "capture_compress_after_days = 0"]) + "\n").encode())
        env = {"PATH": "/usr/bin:/bin", "HOME": str(work_dir / "home"), "TMPDIR": str(work_dir / "tmp"),
               "TZ": "UTC", "LANG": "C"}
        entry = supervised([str(binary), "--config", str(config), *startup_command], env,
                           work_dir / "logs/startup.log", 4 * 3600)
        if entry.get("timed_out") or entry["exit_code"] or entry.get("orphaned_descendants"):
            raise RuntimeError("the retained startup failed; see the private normalize log")
        version, counts, checked = catalog_facts(destination / "catalog/catalog.sqlite3", check)
        if version != expected_schema:
            raise RuntimeError(f"startup reached schema {version}, not {expected_schema}")
        if counts != parent["counts"]:
            raise RuntimeError("startup changed the corpus counts; the result is not the same corpus")
        marker = {**parent, "schema_version": version, "prepared_at": time.time(),
                  "lineage": parent.get("lineage", []) + [{
                      "step": "normalize", "binary_sha256": retained_sha256, "command": startup_command,
                      "seconds": entry["seconds"], "cpu_seconds": entry["cpu_seconds"], "cpu_basis": entry["cpu_basis"],
                      "schema_before": parent["schema_version"], "schema_after": version,
                      "integrity": checked, "parent_master_stamp_sha256": parent["master_stamp_sha256"]}]}
        marker["master_stamp_sha256"] = digest_of(stamp(master_paths(destination)))
        write_json(destination / MARKER, marker)
        return marker
    except BaseException:
        shutil.rmtree(destination)
        raise


# The probe's scheduled kinds and the classic methods it maps each one to.
SCHEDULED_KINDS = {"analysis.authorship", "analysis.labels", "maintenance.labels.query", "maintenance.parallelism"}
# Kinds whose classic pass takes a population; the others drain whole queues.
# Restricted RunLabels forces rebuilds and omits durable dependent marking.
# Every production classic pass must therefore be demanded over the whole
# corpus. A recent phase alongside whole-history demand still measures priority.
SCOPED_KINDS = set()
# Classic-only diagnostic stages; a scheduled arm refuses them.
LEGACY_STAGES = {"timing", "tools", "identities", "authorship-estimates", "authorship-exact", "labels",
                 "agent-events", "label-facts", "findings"}
PRODUCT_ROUTE = "/api/query/message_labels/query-v3"
PROBE_TEST = "^TestWorkReplica$"


def rfc3339(value):
    if not isinstance(value, str):
        raise ValueError("times are RFC3339 strings")
    return datetime.datetime.fromisoformat(value.replace("Z", "+00:00"))


def validate_probe_descriptor(descriptor):
    """Mirror of the probe's own validation, so a bad workload fails before any copy."""
    if not isinstance(descriptor.get("name"), str) or not descriptor["name"]:
        raise ValueError("descriptor needs a name")
    rfc3339(descriptor.get("clock"))
    phases, stages = descriptor.get("phases"), descriptor.get("stages")
    if (phases is None) == (stages is None):
        raise ValueError("a probe workload has either phases (kinds) or legacy classic stages")
    units = []
    if phases is not None:
        if not isinstance(phases, list) or not phases:
            raise ValueError("phases is a non-empty list")
        for phase in phases:
            if not isinstance(phase, dict) or not set(phase) <= {"name", "action", "kinds", "from", "until"}:
                raise ValueError("a phase has name, action, kinds and optional from/until")
            if phase.get("action") not in ("recent", "backfill") or not isinstance(phase.get("name"), str):
                raise ValueError("a phase's action is recent or backfill")
            kinds = phase.get("kinds")
            if not isinstance(kinds, list) or not kinds or not set(kinds) <= SCHEDULED_KINDS:
                raise ValueError(f"phase kinds come from {sorted(SCHEDULED_KINDS)}")
            if ("from" in phase) != ("until" in phase) or ("from" in phase and phase["action"] != "backfill"):
                raise ValueError("a window is a backfill with both from and until")
            if "from" in phase and rfc3339(phase["from"]) >= rfc3339(phase["until"]):
                raise ValueError("a window's from precedes its until")
            units += kinds
        # Each kind runs over the union of the phases that demand it, in both
        # arms; a classic pass that cannot be scoped must be demanded whole.
        for kind in set(units) - SCOPED_KINDS:
            if all("from" in phase or phase["action"] == "recent" for phase in phases if kind in phase["kinds"]):
                raise ValueError(f"{kind}'s classic pass drains the whole queue; demand it in a phase without a window")
    else:
        if not isinstance(stages, list) or not stages or not set(stages) <= LEGACY_STAGES:
            raise ValueError(f"legacy stages come from {sorted(LEGACY_STAGES)}")
        units = stages
    pending = descriptor.get("pending_checks")
    for unit in set(units):
        checks = (pending or {}).get(unit)
        if not isinstance(checks, list) or not checks or not all(isinstance(q, str) and read_only_statement(q) for q in checks):
            raise ValueError(f"{unit!r} needs read-only pending_checks: returning is not settlement")
    semantic = descriptor.get("semantic_checks")
    if not isinstance(semantic, dict) or not semantic or not all(isinstance(q, str) and read_only_statement(q) for q in semantic.values()):
        raise ValueError("descriptor needs non-empty read-only semantic_checks")
    product = descriptor.get("product") or {}
    if product.get("route") != PRODUCT_ROUTE or not isinstance(product.get("query"), dict):
        raise ValueError("the product gate is the real message-label query route and its wire query")
    if not isinstance(product.get("latency_ms"), (int, float)) or product["latency_ms"] <= 0 \
            or not isinstance(product.get("consecutive"), int) or product["consecutive"] < 1:
        raise ValueError("pin product latency_ms and consecutive observations")
    if not isinstance(product.get("min_expected_rows"), int) or product["min_expected_rows"] < 0 \
            or not isinstance(product.get("expected_sha256", ""), str):
        raise ValueError("pin product.min_expected_rows (and expected_sha256 once a reference settles)")
    if product.get("expected_sha256") and not SHA256.match(product["expected_sha256"]):
        raise ValueError("product.expected_sha256 is a lowercase SHA-256")
    cpu_slots = descriptor.get("cpu_slots", 0)
    if not isinstance(cpu_slots, int) or not 0 <= cpu_slots <= (os.cpu_count() or 1):
        raise ValueError("cpu_slots is 0 (process default) or 1..CPUs")
    timeout = descriptor.get("timeout_seconds", 14400)
    if not isinstance(timeout, int) or timeout <= 0 or not isinstance(descriptor.get("timezone", "UTC"), str):
        raise ValueError("descriptor timeout/timezone are malformed")
    for key, value in descriptor.get("model_settings", {}).items():
        if key in INPUT_KEYS:
            input_name(value)
        elif key not in MODEL_KEYS or not isinstance(value, (str, int, bool)):
            raise ValueError("unsupported experiment model setting")
    if "labels_model_dir" in descriptor.get("model_settings", {}) and descriptor["model_settings"].get("labels_variant") not in ("fp32", "int8"):
        raise ValueError("a frozen labels_model_dir needs an explicit labels_variant: custom paths change the default")
    if descriptor.get("sources"):
        raise ValueError("probe workloads replay no sources yet; capture needs the source-replay probe")
    if "replay" in descriptor:
        validate_replay(descriptor["replay"])
    memory = descriptor.get("memory", {})
    if not isinstance(memory, dict) or not set(memory) <= {"interval_seconds", "idle_seconds"} \
            or not all(isinstance(v, int) and not isinstance(v, bool) for v in memory.values()) \
            or memory.get("interval_seconds", 5) < 1 or memory.get("idle_seconds", 0) < 0:
        raise ValueError("memory has integer interval_seconds (at least 1) and idle_seconds (0 or more)")


def validate_replay(replay):
    """The probe's replay shape: arrivals staged from one frozen input, in order."""
    if not isinstance(replay, dict) or not set(replay) <= {"input", "source", "identity", "count_query", "ready_checks",
                                                           "settle_timeout_seconds", "arrivals"}:
        raise ValueError("replay has input, source, count_query, arrivals and optional ready_checks/settle_timeout_seconds")
    if not NAME.match(input_name({"input": replay.get("input")})):
        raise ValueError("replay.input names a frozen input")
    source = replay.get("source")
    if not isinstance(source, dict) or set(source) != {"name", "kind", "account"} \
            or not all(isinstance(v, str) and v for v in source.values()) or not NAME.match(source["name"]):
        raise ValueError("replay.source names a fresh source with explicit name, kind and account")
    identity = replay.get("identity")
    if not isinstance(identity, dict) or set(identity) != {"provider", "native_ids", "expect"} \
            or not isinstance(identity["provider"], str) or not identity["provider"] \
            or identity["expect"] not in ("new", "extend") \
            or not isinstance(identity["native_ids"], list) or not identity["native_ids"] \
            or not all(isinstance(v, str) and v for v in identity["native_ids"]) \
            or len(identity["native_ids"]) != len(set(identity["native_ids"])):
        raise ValueError("replay.identity pins provider, distinct native_ids and new or extend")
    if not isinstance(replay.get("count_query"), str) or not read_only_statement(replay["count_query"]):
        raise ValueError("replay.count_query is one read-only SELECT pinned to the replayed messages")
    ready = replay.get("ready_checks", [])
    if not isinstance(ready, list) or not ready or not all(isinstance(q, str) and read_only_statement(q) for q in ready):
        raise ValueError("replay.ready_checks are read-only statements")
    timeout = replay.get("settle_timeout_seconds", 1800)
    if not isinstance(timeout, int) or isinstance(timeout, bool) or timeout <= 0:
        raise ValueError("replay.settle_timeout_seconds is a positive integer")
    arrivals = replay.get("arrivals")
    if not isinstance(arrivals, list) or not arrivals:
        raise ValueError("replay.arrivals is a non-empty list")
    names, previous, written = set(), 0, {}
    for arrival in arrivals:
        if not isinstance(arrival, dict) or set(arrival) != {"name", "at_seconds", "files"} \
                or not isinstance(arrival["name"], str) or not NAME.match(arrival["name"]) or arrival["name"] in names:
            raise ValueError("each arrival has a unique name, at_seconds and files")
        names.add(arrival["name"])
        at = arrival["at_seconds"]
        if not isinstance(at, (int, float)) or isinstance(at, bool) or at < previous:
            raise ValueError("arrivals run in order: at_seconds is non-negative and never decreases")
        previous = at
        if not isinstance(arrival["files"], list) or not arrival["files"]:
            raise ValueError("an arrival writes at least one file")
        for file in arrival["files"]:
            if not isinstance(file, dict) or not {"path"} <= set(file) <= {"path", "lines"}:
                raise ValueError("an arrival file has path and optional lines")
            input_name({"input": replay["input"], "path": file["path"]})
            # A whole file is every line; growth never shrinks what an earlier arrival wrote.
            lines = file.get("lines", float("inf"))
            if lines != float("inf") and (not isinstance(lines, int) or isinstance(lines, bool) or lines < 1):
                raise ValueError("an arrival file's lines is a positive integer")
            if lines < written.get(file["path"], 0):
                raise ValueError(f"{file['path']} would shrink: arrivals only extend a file")
            written[file["path"]] = lines


def scheduled_evidence(engine, descriptor):
    """Whether the probe's executed-engine record proves the scheduled path ran."""
    problems = []
    if engine.get("value") != "scheduled" or engine.get("basis") != "work-api":
        return ["the probe did not observe the scheduled work service executing"]
    kinds = {kind for phase in descriptor.get("phases", []) for kind in phase["kinds"]}
    tasks = {task.get("kind"): task for task in engine.get("tasks", [])}
    for kind in sorted(kinds):
        if kind not in tasks:
            problems.append(f"{kind} was not registered with the work service")
    requests = engine.get("requests", [])
    if len(requests) != len(descriptor.get("phases", [])):
        problems.append("not every phase was admitted as a work request")
    for request in requests:
        if request.get("state") != "succeeded" or request.get("unfinished"):
            problems.append(f"work request {request.get('id')} ended {request.get('state')}")
    return problems


def replay_identity_evidence(plan, replay):
    """Require the probe's identity proof, including when an older probe ignores it."""
    identity = plan["identity"]
    ids = sorted(identity["native_ids"])
    # Match Go encoding/json for a UTF-8 string slice, including HTML escaping.
    encoded = json.dumps(ids, ensure_ascii=False, separators=(",", ":"))
    for char, escaped in (("&", "\\u0026"), ("<", "\\u003c"), (">", "\\u003e"),
                          ("\u2028", "\\u2028"), ("\u2029", "\\u2029")):
        encoded = encoded.replace(char, escaped)
    expected = {"provider": identity["provider"], "account": plan["source"]["account"],
                "expect": identity["expect"], "native_ids": len(ids),
                "native_ids_sha256": hashlib.sha256(encoded.encode()).hexdigest(),
                "present_before": 0 if identity["expect"] == "new" else len(ids), "present_after": len(ids)}
    actual = replay.get("identity") or {}
    if any(actual.get(key) != value for key, value in expected.items()):
        raise RuntimeError("the probe did not prove the declared replay identity")
    before, after = actual.get("other_conversations_before"), actual.get("other_conversations_after")
    if not isinstance(before, int) or before != after:
        raise RuntimeError("the replay changed undeclared conversations or did not report their census")
    for arrival in replay.get("arrivals") or []:
        before, after = arrival.get("identity_message_count_before"), arrival.get("identity_message_count_after")
        if not isinstance(before, int) or not isinstance(after, int) or after <= before:
            raise RuntimeError("the replay did not grow the declared conversations in every arrival")


def probe_run(probe, probe_sha256, master, destination, descriptor, role, engine, baseline_probe_sha256):
    """One probe arm: the compiled probe test binary on a fresh clone.

    The engine is only requested here; the report's engine comes from what the
    probe observed executing. A baseline is the probe compiled from the
    retained source, identified by its hash."""
    validate_probe_descriptor(descriptor)
    master, destination = master.resolve(), destination.resolve()
    if role not in ("baseline", "candidate") or engine not in ("classic", "scheduled"):
        raise ValueError("role is baseline or candidate; engine is classic or scheduled")
    if not SHA256.match(baseline_probe_sha256 or "") or not SHA256.match(probe_sha256 or ""):
        raise ValueError("pass the probe's and the retained-source probe's SHA-256")
    probe = probe.resolve(strict=True)
    actual = file_sha256(probe)
    if actual != probe_sha256:
        raise ValueError("the probe binary is not the artifact named")
    if role == "baseline" and (actual != baseline_probe_sha256 or engine != "classic"):
        raise ValueError("a baseline arm runs the retained-source probe, classic")
    if role == "candidate" and actual == baseline_probe_sha256:
        raise ValueError("the candidate is the retained-source probe; use two baseline arms for A/A")
    if engine == "scheduled" and "phases" not in descriptor:
        raise ValueError("legacy classic stages cannot run on the scheduled engine")
    config = arm(master, destination, descriptor)
    database = destination / "catalog/catalog.sqlite3"
    descriptor_path = destination / "workload.json"
    write_json(descriptor_path, descriptor)
    env = {"PATH": "/usr/bin:/bin", "HOME": str(destination / "home"), "TMPDIR": str(destination / "tmp"),
           "TZ": descriptor.get("timezone", "UTC"), "LANG": "C",
           "PHAROS_WORK_REPLICA": str(destination), "PHAROS_WORK_DESCRIPTOR": str(descriptor_path),
           # Requests only; the probe reports the engine that executed.
           "PHAROS_WORK_ENGINE": engine}
    if descriptor.get("cpu_slots"):
        env["PHAROS_BACKGROUND_CPUS"] = str(descriptor["cpu_slots"])
    if descriptor.get("replay"):
        # Provider records retain their original cwd. The parser may ask Git
        # about it even though the finishing Git analysis is deferred. Those
        # checkouts are not frozen inputs, so suppress that optional enrichment
        # symmetrically rather than consulting the user's changing worktrees.
        tools = destination / "bin"
        tools.mkdir(mode=0o700)
        write_private(tools / "git", b"#!/bin/sh\nexit 1\n")
        (tools / "git").chmod(0o700)
        env["PATH"] = str(tools) + ":/usr/bin:/bin"
    timeout = descriptor.get("timeout_seconds", 14400)
    report = {"version": VERSION, "kind": "probe", "role": role, "engine_requested": engine,
              "engine_actual": {"value": None, "basis": "unavailable"}, "name": descriptor["name"],
              "probe_sha256": actual, "baseline_probe_sha256": baseline_probe_sha256,
              "descriptor_sha256": digest_of(descriptor), "corpus": load_marker(master, "master"),
              "cpu_basis": CPU_BASIS, "settled": False, "failure": None}
    if descriptor.get("replay"):
        report["unfrozen_git_lookup"] = "disabled; replay measures no Git enrichment"
    try:
        probe_report = destination / "probe-report.json"
        spans = lambda: _probe_spans(json.loads(probe_report.read_text())) if probe_report.is_file() else {}
        entry = supervised([str(probe), "-test.run", PROBE_TEST, "-test.timeout", f"{timeout}s", "-test.v"], env,
                           destination / "logs/probe.log", timeout + 300, cwd=destination, windows=spans)
        report["process"] = entry
        inner = json.loads(probe_report.read_text()) if probe_report.is_file() else None
        report["probe"] = inner
        if inner is None or inner.get("version") != VERSION:
            raise RuntimeError("the probe wrote no version-3 report")
        report["engine_actual"] = inner.get("engine_actual") or report["engine_actual"]
        report["cpu_policy"] = inner.get("cpu_policy")
        if descriptor.get("cpu_slots"):
            policy = report["cpu_policy"] or {}
            if not policy.get("supported") or policy.get("effective_slots") != descriptor["cpu_slots"]:
                raise RuntimeError("the probe cannot enforce the descriptor's pinned CPU allowance")
        if entry.get("timed_out") or entry["exit_code"] or entry.get("orphaned_descendants") or not inner.get("settled"):
            raise RuntimeError(f"the probe failed: {inner.get('failure') or 'see its private log'}")
        if engine == "scheduled":
            problems = scheduled_evidence(report["engine_actual"], descriptor)
            if problems:
                raise RuntimeError("; ".join(problems))
        elif report["engine_actual"].get("value") != "classic":
            raise RuntimeError("a classic request did not execute the classic path")
        if "replay" in descriptor:
            replay = inner.get("replay") or {}
            replay_identity_evidence(descriptor["replay"], replay)
            applied = [a.get("name") for a in replay.get("arrivals") or [] if isinstance(a, dict)
                       and isinstance(a.get("count_before"), int) and isinstance(a.get("count_after"), int)
                       and a["count_after"] > a["count_before"]]
            if applied != [a["name"] for a in descriptor["replay"]["arrivals"]]:
                raise RuntimeError("the probe did not report every replayed arrival as a real arrival")
            if engine == "scheduled":
                requests = replay.get("requests") or []
                if [r.get("arrival") for r in requests] != applied:
                    raise RuntimeError("the scheduled probe did not submit recent demand for every arrival")
                demanded = {kind for phase in descriptor["phases"] for kind in phase["kinds"]}
                for request in requests:
                    facts = request.get("facts") or {}
                    submitted, applied_at = request.get("submitted_seconds"), next(
                        a.get("applied_seconds") for a in replay["arrivals"] if a["name"] == request["arrival"])
                    if request.get("scope") != "recent" or set(request.get("kinds") or []) != demanded \
                            or facts.get("scope") != "recent" or not str(facts.get("key", "")).startswith("recent:") \
                            or facts.get("state") != "succeeded" or facts.get("unfinished") \
                            or request.get("request_id") != facts.get("id") or not isinstance(request.get("joined_phases"), list) \
                            or not isinstance(submitted, (int, float)) or not isinstance(applied_at, (int, float)) or submitted < applied_at:
                        raise RuntimeError("the scheduled arrival request did not prove successful recent demand")
            report["replay"] = replay
        # Recorded, never gated: the harness's process-tree sampler is the gated source.
        report["memory_retention"] = (inner.get("memory") or {}).get("retention")
        # Outside every timing: semantic oracle and an independent pending recheck.
        report["semantic"] = digests(database, descriptor["semantic_checks"])
        report["pending"] = digests(database, flat_pending(descriptor["pending_checks"]))
        if any(check["rows"] for check in report["pending"].values()):
            raise RuntimeError("a pending check is not empty after the probe settled")
        report["settled"] = True
    except Exception as error:
        report["failure"] = {"type": type(error).__name__, "message": str(error)}
        raise
    finally:
        write_json(destination / "report.json", report)
    return report


def pin_oracle(reference, descriptor, allow_empty=False):
    """Pin the product oracle from a settled retained-source classic reference arm."""
    if reference.get("version") != VERSION or reference.get("kind") != "probe" or not reference.get("settled"):
        raise ValueError("pin from a settled version-3 probe arm")
    same_cpu_basis(reference)  # the reference is a fresh run of the corrected harness, not prep
    same_digest_basis(reference)
    if reference.get("role") != "baseline" or (reference.get("engine_actual") or {}).get("value") != "classic":
        raise ValueError("the oracle comes from the retained-source classic reference")
    if reference["descriptor_sha256"] != digest_of(descriptor):
        raise ValueError("the reference ran a different workload")
    if descriptor["product"].get("expected_sha256"):
        raise ValueError("this workload already pins an oracle")
    inner = reference["probe"]
    rows = inner.get("final_product_rows")
    if not isinstance(rows, int) or (rows == 0 and not allow_empty):
        raise ValueError("the primary recent surface pins at least one row")
    pinned = json.loads(json.dumps(descriptor))
    pinned["product"].update(expected_sha256=inner["final_product_sha256"], min_expected_rows=rows)
    pinned["oracle"] = {"reference_descriptor_sha256": reference["descriptor_sha256"],
                        "reference_probe_sha256": reference["probe_sha256"],
                        "reference_semantic": reference["semantic"]}
    return pinned


def _metric(report, path):
    value = report
    for key in path:
        value = value.get(key) if isinstance(value, dict) else None
    return value if isinstance(value, (int, float)) and not isinstance(value, bool) else None


# Gated probe figures cover the execution window only: catalog open (startup,
# gated apart) and the post-settlement product checks are outside every one.
PROBE_METRICS = {  # name: (path, floor either arm must reach to be gated)
    "t_settled": (("probe", "t_settled"), MIN_GATED_SECONDS),
    "t_correct": (("probe", "t_correct"), MIN_GATED_SECONDS),
    "t_usable": (("probe", "t_usable"), MIN_GATED_SECONDS),
    "cpu_seconds": (("probe", "work"), MIN_GATED_SECONDS),
    "peak_footprint_bytes": (("process", "windows", "work", "peak_footprint_bytes"), MIN_GATED_BYTES),
    "disk_read_bytes": (("process", "windows", "work", "disk_read_bytes"), MIN_GATED_BYTES),
    "disk_written_bytes": (("process", "windows", "work", "disk_written_bytes"), MIN_GATED_BYTES),
}
# Whole probe process, startup and post-settlement checks included: recorded, never gated.
WHOLE_PROCESS = {
    "cpu_seconds": ("process", "cpu_seconds"),
    "peak_footprint_bytes": ("process", "resources", "peak_simultaneous", "footprint_bytes"),
    "disk_read_bytes": ("process", "resources", "disk_read_bytes"),
    "disk_written_bytes": ("process", "resources", "disk_written_bytes"),
}
# Host noise above this (in one-core percent, outside the measured tree) makes a pair ambiguous.
NOISE_CPU_PERCENT = 100.0


def _window_cpu(report, path):
    """A probe window's CPU, or None unless every process in its tree was read."""
    section = report
    for key in path:
        section = section.get(key) if isinstance(section, dict) else None
    if not isinstance(section, dict) or section.get("cpu_complete") is not True:
        return None
    return _metric(section, ("cpu_seconds",))


def _probe_metric(report, path):
    return _window_cpu(report, path) if path[-1] in ("work", "startup") else _metric(report, path)


def noise_problems(role, report):
    """Host noise during the arm's execution and its wait for readiness.

    A window without a reading is unavailable, never quiet; only a window
    shorter than the gate floor, where nothing gated can hide, may lack one."""
    problems = []
    spans = _probe_spans(report.get("probe") or {})
    for name in ("work", "readiness"):
        if name not in spans:
            if name == "work":
                problems.append(f"{role}: the probe reported no execution window")
            continue
        mean = _metric(report, ("process", "windows", name, "host_noise", "other_cpu_percent_mean"))
        if mean is None:
            if spans[name][1] - spans[name][0] >= MIN_GATED_SECONDS:
                problems.append(f"{role}: host noise during {name} is unavailable")
        elif mean > NOISE_CPU_PERCENT:
            problems.append(f"{role}: host noise during {name} averaged {mean:.0f}% of a core")
    return problems


def _probe_spans(inner):
    """The probe's own phase boundaries (Unix seconds), well-formed ones only."""
    spans = {}
    for name, span in (inner.get("windows") or {}).items():
        if isinstance(span, list) and len(span) == 2 and all(isinstance(v, (int, float)) for v in span) \
                and span[0] <= span[1]:
            spans[name] = span
    return spans


def replay_overlap(report):
    """Prove each arrival became pending while primary history was unfinished.

    A scheduled poll's last known-live time is a lower bound, unlike its
    observed end, which could be a full poll late. Missing bounds prove nothing.
    """
    arrivals = (report.get("replay") or {}).get("arrivals") or []
    inner, engine = report.get("probe") or {}, report.get("engine_actual") or {}
    spans = []
    if engine.get("value") == "classic":
        primary = [step for step in inner.get("steps") or []
                   if not str(step.get("name", "")).startswith("followup:")]
        for step in primary:
            start, wall = step.get("started_seconds"), step.get("wall_seconds")
            if isinstance(start, (int, float)) and isinstance(wall, (int, float)) and start >= 0 and wall >= 0:
                spans.append((start, start + wall))
        # Between primary stages the original history demand is still unfinished.
        if spans:
            spans = [(min(s[0] for s in spans), max(s[1] for s in spans))]
    elif engine.get("value") == "scheduled":
        work = (inner.get("windows") or {}).get("work") or []
        for request in engine.get("requests") or []:
            until = request.get("history_active_until_seconds")
            try:
                start = rfc3339(request.get("requested_at")).timestamp() - work[0]
            except (ValueError, TypeError, IndexError):
                continue
            if request.get("scope") == "backfill" and isinstance(until, (int, float)) and until >= start:
                spans.append((max(0, start), until))
    applied = [a.get("applied_seconds") for a in arrivals]
    pending = [a.get("ready_pending_at_apply") for a in arrivals]
    proven = bool(applied) and all(v is True for v in pending) and all(
        isinstance(at, (int, float)) and any(start <= at <= until for start, until in spans) for at in applied)
    ready = []
    for arrival in arrivals:
        start, took = arrival.get("started_seconds"), arrival.get("ready_seconds")
        at = start + took if isinstance(start, (int, float)) and isinstance(took, (int, float)) else None
        ready.append(at)
    ready_during = proven and all(isinstance(at, (int, float)) and any(
        start <= at <= until for start, until in spans) for at in ready)
    requests = (report.get("replay") or {}).get("requests") or []
    ids = [r.get("request_id") for r in requests]
    primary_ids = {q.get("id") for q in engine.get("requests") or []}
    followup_ids = {q.get("id") for q in (report.get("replay") or {}).get("followups") or []}
    fresh = all(not r.get("joined_arrival") and r.get("joined_phases") == [] for r in requests) and \
        len(set(ids)) == len(ids) and not set(ids).intersection(primary_ids | followup_ids)
    frozen_before = engine.get("value") == "classic"
    if engine.get("value") == "scheduled" and arrivals:
        try:
            started = min(a["started_seconds"] for a in arrivals)
            work = inner["windows"]["work"][0]
            frozen = [rfc3339(q.get("membership_frozen_at")).timestamp() - work for q in engine["requests"]]
            frozen_before = bool(frozen) and all(at <= started for at in frozen)
        except (KeyError, TypeError, ValueError):
            frozen_before = False
    return {"proven": proven, "ready_during_history": ready_during, "fresh_arrival_requests": fresh,
            "primary_membership_predates_arrivals": frozen_before,
            "arrival_ready_seconds": ready, "primary_history_intervals_seconds": spans,
            "arrival_applied_seconds": applied, "arrival_pending_at_apply": pending,
            "basis": "primary classic steps, or scheduled requests last observed live; follow-ups excluded"}


def compare_probe(baseline, candidate, mode, baseline_probe_sha256):
    """Paired probe verdict. Never returns acceptance: one workload is one gate."""
    for report in (baseline, candidate):
        if report.get("version") != VERSION or report.get("kind") != "probe":
            raise ValueError("unrecognized probe arm report")
        if not report.get("settled") or not report.get("semantic") or not report.get("pending") \
                or any(c["rows"] for c in report["pending"].values()):
            raise ValueError("an unsettled arm, or one without semantic and empty pending checks, proves nothing")
    same_cpu_basis(baseline, candidate)
    same_digest_basis(baseline, candidate)
    if baseline["descriptor_sha256"] != candidate["descriptor_sha256"] or baseline["corpus"] != candidate["corpus"]:
        raise ValueError("arms did not run the same corpus, frozen inputs and workload")
    if baseline["probe"].get("population") != candidate["probe"].get("population"):
        raise ValueError("arms froze different populations")
    if baseline.get("role") != "baseline" or baseline.get("probe_sha256") != baseline_probe_sha256 \
            or (baseline.get("engine_actual") or {}).get("value") != "classic":
        raise ValueError("the baseline arm is the retained-source probe on the classic path")
    expected = baseline["probe"].get("expected_sha256")
    if not expected:
        raise ValueError("unpinned product oracle: a reference run is not a measured pair")
    blockers = []
    policies = [report["probe"].get("cpu_policy") or {} for report in (baseline, candidate)]
    if any(not policy for policy in policies):
        blockers.append("effective CPU policies were not reported by both probes")
    elif policies[0].get("requested_slots") != policies[1].get("requested_slots"):
        blockers.append("arms requested different CPU allowances")
    elif policies[0].get("requested_slots") and (any(not p.get("supported") for p in policies)
            or policies[0].get("effective_slots") != policies[1].get("effective_slots")):
        blockers.append("arms cannot enforce the same pinned CPU allowance")
    if policies[0].get("gomaxprocs") != policies[1].get("gomaxprocs") or policies[0].get("logical_cpus") != policies[1].get("logical_cpus"):
        blockers.append("arms have different process CPU availability")
    if mode == "aa":
        if candidate.get("probe_sha256") != baseline["probe_sha256"] or candidate.get("role") != "baseline":
            raise ValueError("A/A runs the retained-source probe in both arms")
    elif mode == "ab":
        if candidate.get("role") != "candidate" or candidate.get("probe_sha256") == baseline_probe_sha256:
            raise ValueError("A/B compares the retained baseline with a different candidate probe")
        if candidate.get("engine_requested") != "scheduled" or (candidate.get("engine_actual") or {}).get("basis") != "work-api" \
                or candidate["engine_actual"].get("value") != "scheduled":
            blockers.append("candidate's executed engine is unproven")
    else:
        raise ValueError("mode is ab or aa")
    parity = baseline["semantic"] == candidate["semantic"] and \
        baseline["probe"].get("final_product_sha256") == candidate["probe"].get("final_product_sha256") == expected
    metrics, missing = {}, []
    for name, (path, floor) in PROBE_METRICS.items():
        entry = gate(_probe_metric(baseline, path), _probe_metric(candidate, path), floor)
        if entry["baseline"] is not None and entry["candidate"] is None and name in ("t_correct", "t_usable"):
            entry["regression"] = "candidate never reached the milestone"
        elif entry["baseline"] is None or entry["candidate"] is None:
            missing.append(name)
        metrics[name] = entry
    startup = {"wall": gate(_metric(baseline, ("probe", "startup", "wall_seconds")),
                            _metric(candidate, ("probe", "startup", "wall_seconds")), MIN_GATED_SECONDS),
               "cpu": gate(_window_cpu(baseline, ("probe", "startup")), _window_cpu(candidate, ("probe", "startup")),
                           MIN_GATED_SECONDS)}
    missing += [f"startup_{key}" for key, entry in startup.items() if None in (entry["baseline"], entry["candidate"])]
    regression = any(entry["regression"] for entry in [*metrics.values(), *startup.values()])
    noise = noise_problems("baseline", baseline) + noise_problems("candidate", candidate)
    blockers += noise + (["rerun quiet or in reverse order"] if noise else [])
    overlap = {name: replay_overlap(report) for name, report in (("baseline", baseline), ("candidate", candidate))}
    if baseline.get("replay") or candidate.get("replay"):
        blockers += [f"{name}: replay did not prove arrival while history remained active"
                     for name, evidence in overlap.items() if not evidence["proven"]]
    if mode == "ab" and candidate.get("replay"):
        if not overlap["candidate"]["fresh_arrival_requests"]:
            blockers.append("candidate: arrival demand joined an older frozen population")
        if not overlap["candidate"]["primary_membership_predates_arrivals"]:
            blockers.append("candidate: primary membership was not frozen before arrivals; priority attribution is unproven")
    arrival_readiness = {}
    baseline_arrivals = (baseline.get("replay") or {}).get("arrivals") or []
    candidate_arrivals = (candidate.get("replay") or {}).get("arrivals") or []
    if [a["name"] for a in baseline_arrivals] != [a["name"] for a in candidate_arrivals]:
        raise ValueError("arms reported different arrival sequences")
    for a, b in zip(baseline_arrivals, candidate_arrivals):
        arrival_readiness[a["name"]] = gate(a.get("ready_seconds"), b.get("ready_seconds"), MIN_GATED_SECONDS)
        if a.get("ready_seconds") is None or b.get("ready_seconds") is None:
            missing.append("arrival readiness: " + a["name"])
    regression = regression or any(v["regression"] for v in arrival_readiness.values())
    result = {"mode": mode, "semantic_parity": parity, "metrics": metrics, "startup": startup,
              "missing_metrics": missing, "blockers": blockers,
              "whole_process": {name: {"baseline": _metric(baseline, path), "candidate": _metric(candidate, path)}
                                for name, path in WHOLE_PROCESS.items()},
              # Recorded, never gated.
              "memory_retention": {role: report.get("memory_retention") for role, report in
                                   (("baseline", baseline), ("candidate", candidate))},
              "replay_followup_rounds": {role: (report.get("replay") or {}).get("followup_rounds") for role, report in
                                         (("baseline", baseline), ("candidate", candidate))},
              "replay_overlap": overlap, "arrival_readiness": arrival_readiness}
    if mode == "aa":
        # A/A measures the noise every later pair is judged against, so it is
        # refused on the same host-noise rule before any verdict.
        verdict = "refused" if blockers else "exceeds-gate" if regression or not parity \
            else "incomplete" if missing else "within-gate"
        return {**result, "noise": verdict, "release_gate": "not-applicable"}
    rejected = not parity or regression
    return {**result, "rejected": rejected,
            "release_gate": "rejected" if rejected else "refused" if blockers else "incomplete",
            "remaining_evidence": ["the full-corpus settlement workload (all labels, coarse history)",
                                   *([] if all(v["proven"] for v in overlap.values()) else
                                     ["actual recent arrival during coarse history (source replay)"]),
                                   *([] if not candidate.get("replay") or overlap["candidate"]["ready_during_history"] else
                                     ["recent arrivals ready before candidate history settles"]),
                                   "reverse-order pair", "old-binary recovery after candidate publication",
                                   *(f"missing metric: {name}" for name in missing)]}


def promote(arm_dir, destination, baseline_probe_sha256, check="quick"):
    """Freeze a settled retained-source classic arm as a new master.

    This is how derived state (for example every non-label queue drained) is
    prepared once by the retained code instead of inside every measured arm.
    The lineage records the probe, workload and report that produced it."""
    arm_dir, destination = separate(arm_dir, destination)
    marker = load_marker(arm_dir, "arm")
    raw = (arm_dir / "report.json").read_bytes()
    report = json.loads(raw)
    if report.get("version") != VERSION or report.get("kind") != "probe" or not report.get("settled"):
        raise ValueError("promote a settled version-3 probe arm")
    if report.get("role") != "baseline" or report.get("probe_sha256") != baseline_probe_sha256 \
            or (report.get("engine_actual") or {}).get("value") != "classic":
        raise ValueError("only the retained-source classic probe prepares a master")
    master = Path(marker["master"])
    parent = checked_master(master)
    if parent["master_stamp_sha256"] != marker["master_stamp_sha256"]:
        raise ValueError("the arm's master changed after the arm was made")
    if destination.exists():
        raise ValueError("choose a new experiment directory")
    destination.mkdir(parents=True, mode=0o700)
    try:
        for name in ("catalog", "captures", "preserved"):
            clone(arm_dir / name, destination / name)
        clone(master / "inputs", destination / "inputs")
        version, counts, checked = catalog_facts(destination / "catalog/catalog.sqlite3", check)
        if counts != parent["counts"]:
            raise RuntimeError("the settle arm changed the corpus counts")
        derived = {**parent, "schema_version": version, "prepared_at": time.time(),
                   "lineage": parent.get("lineage", []) + [{
                       "step": "settle", "probe_sha256": report["probe_sha256"],
                       "descriptor_sha256": report["descriptor_sha256"], "report_sha256": hashlib.sha256(raw).hexdigest(),
                       "steps": [step.get("name") for step in report["probe"].get("steps", [])],
                       "pending": report["pending"], "integrity": checked,
                       "parent_master_stamp_sha256": parent["master_stamp_sha256"]}]}
        derived["master_stamp_sha256"] = digest_of(stamp(master_paths(destination)))
        write_json(destination / MARKER, derived)
        return derived
    except BaseException:
        shutil.rmtree(destination)
        raise


def candidate_check(ready):
    """Validate the root's candidate-ready signal; returns what can run."""
    value = json.loads(ready.read_text())
    if value.get("version") != 1:
        raise ValueError("unrecognized candidate-ready signal")
    checked = {}
    for key in ("candidate_probe", "baseline_probe", "candidate_service"):
        item = value.get(key)
        if item is None:
            checked[key] = {"state": "absent"}
            continue
        path = Path(item["path"])
        if not path.is_absolute() or path.is_symlink() or not path.is_file():
            raise ValueError(f"{key} is not an absolute regular file")
        if file_sha256(path) != item.get("sha256"):
            raise ValueError(f"{key}'s hash differs from the signal")
        checked[key] = {"state": "verified", "path": str(path), "sha256": item["sha256"]}
    baseline = value.get("baseline_probe") or {}
    if baseline and baseline.get("source_commit") != BASELINE_COMMIT:
        raise ValueError("the baseline probe is compiled from the retained source")
    if checked["candidate_probe"]["state"] != "verified":
        raise ValueError("the signal names no candidate probe")
    return {"signal": value, "checked": checked}


def main():
    p = argparse.ArgumentParser(description=__doc__)
    sub = p.add_subparsers(dest="action", required=True)
    prep = sub.add_parser("prepare")
    prep.add_argument("--completed-backup", required=True, type=Path)
    prep.add_argument("--master", required=True, type=Path)
    prep.add_argument("--attestation", type=Path, help=f"completed-backup record when the backup has no {BACKUP_MANIFEST}")
    inv = sub.add_parser("invalidate", help="make a new workload master with missing labels and dirty caches")
    inv.add_argument("--master", required=True, type=Path)
    inv.add_argument("--destination", required=True, type=Path)
    inv.add_argument("--profile", required=True, choices=["recent", "full"])
    inv.add_argument("--clock", required=True)
    inv.add_argument("--recent-conversations", type=int, default=20)
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
    imp = sub.add_parser("import-snapshot", help="admit an existing experiment master as a version-3 master")
    imp.add_argument("--source-master", required=True, type=Path)
    imp.add_argument("--master", required=True, type=Path)
    imp.add_argument("--observe-original", type=Path, help="stat-only observation of the backup it came from")
    imp.add_argument("--integrity", choices=["quick", "full", "none"], default="quick")
    norm = sub.add_parser("normalize", help="run the retained startup once on a clone; freeze as a new master")
    norm.add_argument("--master", required=True, type=Path)
    norm.add_argument("--destination", required=True, type=Path)
    norm.add_argument("--binary", required=True, type=Path)
    norm.add_argument("--retained-sha256", required=True)
    norm.add_argument("--startup-command", default='["labels", "status"]', help="JSON list")
    norm.add_argument("--expected-schema-version", type=int, default=SCHEMA_FLOOR)
    norm.add_argument("--integrity", choices=["quick", "full", "none"], default="quick")
    pr = sub.add_parser("probe-run", help="one compiled-probe arm on a fresh clone")
    pr.add_argument("--probe", required=True, type=Path)
    pr.add_argument("--probe-sha256", required=True)
    pr.add_argument("--baseline-probe-sha256", required=True)
    pr.add_argument("--master", required=True, type=Path)
    pr.add_argument("--destination", required=True, type=Path)
    pr.add_argument("--descriptor", required=True, type=Path)
    pr.add_argument("--role", choices=["baseline", "candidate"], required=True)
    pr.add_argument("--engine", choices=["classic", "scheduled"], required=True)
    pc = sub.add_parser("probe-compare")
    pc.add_argument("baseline", type=Path)
    pc.add_argument("candidate", type=Path)
    pc.add_argument("--mode", choices=["ab", "aa"], default="ab")
    pc.add_argument("--baseline-probe-sha256", required=True)
    po = sub.add_parser("pin-oracle", help="write a workload pinned to a settled classic reference")
    po.add_argument("--reference", required=True, type=Path)
    po.add_argument("--descriptor", required=True, type=Path)
    po.add_argument("--output", required=True, type=Path)
    po.add_argument("--allow-empty", action="store_true")
    pm = sub.add_parser("promote", help="freeze a settled retained-source classic arm as a new master")
    pm.add_argument("--arm", required=True, type=Path)
    pm.add_argument("--master", required=True, type=Path)
    pm.add_argument("--baseline-probe-sha256", required=True)
    pm.add_argument("--integrity", choices=["quick", "full", "none"], default="quick")
    cc = sub.add_parser("candidate-check")
    cc.add_argument("--ready", required=True, type=Path)
    args = p.parse_args()
    if args.action == "prepare":
        value = prepare(args.completed_backup, args.master, args.attestation)
    elif args.action == "invalidate":
        value = invalidate(args.master, args.destination, args.profile, args.clock, args.recent_conversations)
    elif args.action == "freeze":
        value = freeze(args.master, args.name, args.path)
    elif args.action == "run":
        value = run(args.binary, args.master, args.destination, json.loads(args.descriptor.read_text()),
                    args.role, args.retained_sha256)
    elif args.action == "compare":
        value = compare(json.loads(args.baseline.read_text()), json.loads(args.candidate.read_text()),
                        args.mode, args.retained_sha256)
    elif args.action == "import-snapshot":
        value = import_snapshot(args.source_master, args.master, args.observe_original, args.integrity)
    elif args.action == "normalize":
        value = normalize(args.master, args.destination, args.binary, args.retained_sha256,
                          json.loads(args.startup_command), args.expected_schema_version, args.integrity)
    elif args.action == "probe-run":
        value = probe_run(args.probe, args.probe_sha256, args.master, args.destination,
                          json.loads(args.descriptor.read_text()), args.role, args.engine, args.baseline_probe_sha256)
    elif args.action == "probe-compare":
        value = compare_probe(json.loads(args.baseline.read_text()), json.loads(args.candidate.read_text()),
                              args.mode, args.baseline_probe_sha256)
    elif args.action == "pin-oracle":
        if args.output.exists():
            raise ValueError("refusing to overwrite a workload")
        value = pin_oracle(json.loads(args.reference.read_text()), json.loads(args.descriptor.read_text()),
                           args.allow_empty)
        write_json(args.output, value)
    elif args.action == "promote":
        value = promote(args.arm, args.master, args.baseline_probe_sha256, args.integrity)
    else:
        value = candidate_check(args.ready)
    print(json.dumps(value, indent=2))


if __name__ == "__main__":
    main()
