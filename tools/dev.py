#!/usr/bin/env python3
"""Refresh an isolated SSD library and supervise the checkout's server/UI."""

import argparse
import contextlib
import fcntl
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import signal
import socket
import sqlite3
import subprocess
import sys
import tempfile
import time
import tomllib
import urllib.request

ROOT = Path(__file__).resolve().parent.parent
MARKER = ".pharos-dev"


def source_configuration(text, source, destination):
    """Keep source declarations, relocating library-local paths to the copy."""
    def relocate(match):
        line = match.group(0)
        value = tomllib.loads(line)["path"]
        path = Path(value).expanduser()
        if path.is_absolute():
            try:
                relative = path.resolve().relative_to(source.resolve())
            except ValueError:
                return line
            return "path = " + json.dumps(str(destination / relative))
        return line

    return re.sub(r"(?m)^\s*path\s*=.*$", relocate, text)


def copy_source_configuration(source, destination, prepared, production_text):
    """Host files are part of a library's configuration, separate from SQLite."""
    hosts = prepared / "hosts"
    hosts.mkdir()
    if (source / "hosts").is_dir():
        for file in sorted((source / "hosts").glob("*.toml")):
            target = hosts / file.name
            target.write_text(source_configuration(file.read_text(), source, destination))
            target.chmod(0o600)
    # Only source sections from library.toml; dev keeps its own root settings.
    match = re.search(r"(?m)^\[\[sources\]\]\s*$", production_text)
    if match:
        # Other tables following the sources would change dev root settings.
        sections = re.split(r"(?m)(?=^\[)", production_text[match.start():])
        return "\n" + "".join(source_configuration(section, source, destination)
                              for section in sections if section.startswith("[[sources]]"))
    return ""


def configured_path(config, base, key, default):
    path = Path(config.get(key, default)).expanduser()
    return (path if path.is_absolute() else base / path).resolve()


def validate_destination(source, destination):
    if source == destination or source in destination.parents or destination in source.parents:
        raise RuntimeError("Production and dev directories must be separate, non-nested locations")
    if destination.is_symlink():
        raise RuntimeError("Dev directory must not be a symlink")
    if destination.exists() and not (destination / MARKER).is_file():
        raise RuntimeError(f"Refusing to replace unrecognized directory: {destination}")


def snapshot_database(source, destination):
    """Include committed WAL contents without opening production for writes."""
    last_report = 0

    def progress(status, remaining, total):
        nonlocal last_report
        now = time.monotonic()
        if now - last_report >= 10 or remaining == 0:
            print(f"Database snapshot: {(total - remaining) / max(total, 1):.0%}", flush=True)
            last_report = now

    with contextlib.closing(sqlite3.connect(source.as_uri() + "?mode=ro", uri=True)) as src:
        src.execute("PRAGMA query_only=ON")
        # Pin one snapshot rather than restarting the backup as production changes.
        src.execute("BEGIN")
        src.execute("SELECT count(*) FROM sqlite_schema").fetchone()
        with contextlib.closing(sqlite3.connect(destination)) as dst:
            src.backup(dst, pages=4096, progress=progress)
            if dst.execute("SELECT 1 FROM sqlite_schema WHERE name='sync_settings'").fetchone():
                dst.execute("UPDATE sync_settings SET enabled=0")
            dst.commit()
        src.rollback()


def refresh(source, destination, port, binary):
    config_path = source / "library.toml"
    with config_path.open("rb") as file:
        config = tomllib.load(file)
    catalog = configured_path(config, source, "catalog_path", "catalog/catalog.sqlite3")
    if not catalog.is_file():
        raise RuntimeError(f"Production catalog is missing: {catalog}")
    if destination == catalog or destination in catalog.parents:
        raise RuntimeError("Production catalog is inside the dev destination")
    if shutil.disk_usage(destination.parent).free < catalog.stat().st_size + 1024**3:
        raise RuntimeError("Not enough free SSD space for a fresh catalog snapshot plus 1 GB headroom")
    token = secrets.token_urlsafe(32)
    with contextlib.ExitStack() as locks:
        captures = configured_path(config, source, "capture_root", "captures")
        # Match Pharos's shared backup locks so a capture cannot change mid-copy.
        if captures.exists():
            for host in sorted(captures.iterdir()):
                lock_path = host / ".capture.lock"
                if host.is_dir() and lock_path.is_file():
                    lock = locks.enter_context(lock_path.open("rb"))
                    fcntl.flock(lock, fcntl.LOCK_SH | fcntl.LOCK_NB)
        temporary = Path(tempfile.mkdtemp(prefix=f".{destination.name}-refresh-", dir=destination.parent))
        try:
            (temporary / "catalog").mkdir()
            print(f"Snapshotting {catalog} into {destination}", flush=True)
            snapshot_database(catalog, temporary / "catalog/catalog.sqlite3")
            for name, origin in (
                ("captures", captures),
                ("preserved", configured_path(config, source, "archive_root", "preserved")),
            ):
                if origin == destination or destination in origin.parents or origin in destination.parents:
                    raise RuntimeError(f"Production {name} overlaps the dev destination")
                if origin.exists():
                    print(f"Copying {name} with APFS clones", flush=True)
                    subprocess.run(["cp", "-cR", str(origin), str(temporary / name)], check=True)
                else:
                    (temporary / name).mkdir()
            (temporary / "staging").mkdir()
            # Retain source configuration; dev owns its token, paths, and executable.
            settings = {
                "library": True,
                "volume_id": "",
                "catalog_path": "catalog/catalog.sqlite3",
                "archive_root": "preserved",
                "capture_root": "captures",
                "staging_root": "staging",
                "api_token": token,
                "executable": str(binary),
                "host": "127.0.0.1",
                "port": port,
                "enable_reclamation": False,
                "release_hook_proven": False,
            }
            content = "# Generated by tools/dev.py; refreshed on every launch.\n"
            content += "\n".join(f"{key} = {json.dumps(value)}" for key, value in settings.items()) + "\n"
            content += copy_source_configuration(source, destination, temporary, config_path.read_text())
            (temporary / "library.toml").write_text(content)
            (temporary / "library.toml").chmod(0o600)
            (temporary / MARKER).write_text(str(source) + "\n")
            # Keep the previous copy until the entire new snapshot is ready.
            if destination.exists():
                shutil.rmtree(destination)
            temporary.rename(destination)
        finally:
            if temporary.exists():
                shutil.rmtree(temporary)
    return token


def free_port(port):
    with socket.socket() as sock:
        sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        sock.bind(("127.0.0.1", port))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", type=Path, default=Path("/Volumes/euclid/Pharos"))
    parser.add_argument("--destination", type=Path, default=Path("/Volumes/euclid/pharos-dev"))
    parser.add_argument("--port", type=int, default=8798, help="Backend port (default: 8798)")
    parser.add_argument("--ui-port", type=int, default=8799)
    parser.add_argument("--no-open", action="store_true")
    args = parser.parse_args()
    source = args.source.expanduser().resolve()
    destination = args.destination.expanduser().absolute()
    if sys.platform != "darwin":
        raise RuntimeError("This dev command requires macOS/APFS for isolated file clones")
    if not destination.parent.is_dir():
        raise RuntimeError(f"Destination parent is missing; is the SSD mounted? {destination.parent}")
    validate_destination(source, destination)
    destination = destination.resolve()
    validate_destination(source, destination)
    if args.port == args.ui_port:
        raise RuntimeError("Backend and UI need different ports")
    with (destination.parent / f".{destination.name}.lock").open("a+") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise RuntimeError("The shared dev library is already running; stop its dev command first") from None
        for port in (args.port, args.ui_port):
            free_port(port)
        children = []

        def stop(signum, frame):
            raise KeyboardInterrupt

        signal.signal(signal.SIGTERM, stop)
        binary = ROOT / ".context/dev/pharos"
        binary.parent.mkdir(parents=True, exist_ok=True)
        try:
            print("Building the checkout's server and frontend", flush=True)
            if not (ROOT / "web/node_modules").exists():
                subprocess.run(["npm", "ci"], cwd=ROOT / "web", check=True)
            subprocess.run(["npm", "run", "build"], cwd=ROOT / "web", check=True)
            subprocess.run(["go", "build", "-o", str(binary), "./cmd/pharos"], cwd=ROOT, check=True)
            token = refresh(source, destination, args.port, binary)
            env = os.environ.copy()
            for name in ("GITHUB_TOKEN", "PHAROS_TL1_TOKEN", "PHAROS_API_TOKEN", "PHAROS_PARENT_PID"):
                env.pop(name, None)
            config = str(destination / "library.toml")

            def launch(command, cwd=ROOT):
                child = subprocess.Popen(command, cwd=cwd, env=env, start_new_session=True)
                children.append(child)
                return child

            server = launch([str(binary), "--config", config, "serve"])
            request = urllib.request.Request(f"http://127.0.0.1:{args.port}/api/ready", headers={"Authorization": f"Bearer {token}"})
            print("Waiting for database initialization", flush=True)
            while True:
                if server.poll() is not None:
                    raise RuntimeError(f"Backend exited with status {server.returncode}")
                try:
                    with urllib.request.urlopen(request, timeout=2) as response:
                        if response.status == 200:
                            break
                except (OSError, urllib.error.URLError):
                    pass
                time.sleep(1)
            launch(["npm", "run", "watch"], ROOT / "web")
            command = [str(binary), "--config", config, "dev-ui", "--assets", str(ROOT / "internal/archive/assets"), "--port", str(args.ui_port)]
            if args.no_open:
                command.append("--no-open")
            launch(command)
            print(f"Dev library: {destination}\nFrontend: http://127.0.0.1:{args.ui_port}/\nCtrl-C stops all dev processes. Rerun to refresh from production.", flush=True)
            while all(child.poll() is None for child in children):
                time.sleep(1)
            raise RuntimeError("A dev process exited; stopping the remaining processes")
        finally:
            signal.signal(signal.SIGTERM, signal.SIG_IGN)
            signal.signal(signal.SIGINT, signal.SIG_IGN)
            for child in reversed(children):
                if child.poll() is None:
                    os.killpg(child.pid, signal.SIGTERM)
            for child in reversed(children):
                try:
                    child.wait(timeout=30)
                except subprocess.TimeoutExpired:
                    os.killpg(child.pid, signal.SIGKILL)
                    child.wait()


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        print("Dev environment stopped.", flush=True)
    except (RuntimeError, OSError, subprocess.CalledProcessError) as error:
        sys.exit(f"Dev environment: {error}")
