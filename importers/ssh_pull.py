#!/usr/bin/env python3
"""Pull Claude Code and Codex history from another machine over SSH.

A Pharos importer (docs/plugins.md). The remote machine needs only a
POSIX shell, find, stat and tar (Linux, macOS, BSD, WSL); sqlite3 there adds
Codex thread names. Nothing is installed on it.

params:
  destination      ssh destination, e.g. "alice@lab-box" or a Host alias (required)
  sources          subset of ["claude", "claude-config", "codex"] (default all;
                   "claude-config" is Claude Code's other usual folder,
                   ~/.config/claude/projects, and is skipped quietly when absent)
  roots            {"claude": ".claude/projects", ...}; relative paths are
                   under the remote home. Name another Claude profile's
                   projects folder here.
  remote_env       true to honour CLAUDE_CONFIG_DIR and CODEX_HOME as the
                   remote shell reports them, for sources whose root is not
                   given. Off by default: nothing is read from the remote
                   environment unasked. A non-interactive ssh shell may not
                   load the profile that sets them.
  ssh              ssh command prefix (default ["ssh", "-T", "-o", "BatchMode=yes"])
  machine_key      stable machine key (default: destination)
  label            display name (default: "<hostname> (<user>)")
  overlap_seconds  how far before the run's start the next run looks (default 300)
  batch_bytes      bytes per transfer (default 64 MiB)

Cursor: {"version": 1, "destination": …,
         "sources": {name: {"since": epoch, "after": path, "databases": {relative: epoch}}}}.
Each source fetches files oldest first, resuming after the (mtime, path) it
stopped at, or from "since" when it finished. Many files can share an mtime, so
a timestamp alone could not resume inside them. A database is fetched when it or its WAL is newer than when it was last
snapshotted: reading it can touch the WAL, so "since" cannot tell.
"""

import re
import secrets
import subprocess
import tarfile

import pharos_importer

# Source name -> (kind, default root under the remote home, environment
# variable that moves it and the folder below that variable's value).
SOURCES = {
    "claude": ("claude", ".claude/projects", ("CLAUDE_CONFIG_DIR", "projects")),
    "claude-config": ("claude", ".config/claude/projects", None),
    "codex": ("codex", ".codex", ("CODEX_HOME", "")),
}
# Usual folders that many machines lack; their absence is not worth a warning.
OPTIONAL = {"claude-config"}
DATABASE = re.compile(r"state(_\d+)?\.sqlite")


def wanted(kind, relative):
    """The files Pharos's capture of this kind reads, by path under the root."""
    name = relative.rsplit("/", 1)[-1]
    if kind == "claude":
        return name.endswith(".jsonl") or name == "sessions-index.json"
    if relative == "session_index.jsonl" or DATABASE.fullmatch(relative):
        return True
    # Codex compresses a rollout idle for a week to .jsonl.zst.
    return name.endswith((".jsonl", ".jsonl.zst")) and (relative.startswith(("sessions/", "archived_sessions/"))
                                                        or name.startswith("rollout"))


def quote(value):
    """Quote for the remote POSIX sh, which reads scripts from stdin."""
    return "'" + value.replace("'", "'\\''") + "'"


class Remote:
    def __init__(self, run, destination, ssh):
        self.run, self.command = run, ssh + [destination, "sh", "-s"]

    def stream(self, script, stderr=None):
        """Run a script with the remote sh; returns the process with stdout piped."""
        process = subprocess.Popen(self.command, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=stderr)
        process.stdin.write(script.encode())
        process.stdin.close()
        return process

    def output(self, script):
        process = self.stream(script)
        data = process.stdout.read()
        self.run.transferred_bytes += len(data)
        if process.wait() != 0:
            raise RuntimeError(f"remote command failed with exit status {process.returncode}")
        return data.decode()


def root_expression(name, root, remote_env):
    """The remote sh expression for a source's root."""
    moved = SOURCES[name][2]
    if root is None and remote_env and moved:
        variable, below = moved
        default = '"$HOME"/' + quote(SOURCES[name][1])
        return f'"${{{variable}:+${variable}{"/" + below if below else ""}}}"; [ -n "$root" ] || root={default}'
    root = root or SOURCES[name][1]
    return quote(root) if root.startswith("/") else '"$HOME"/' + quote(root)


def probe_and_list(remote, roots, remote_env=False):
    """One round trip: who the machine is, its clock, and every candidate file."""
    lines = ["LC_ALL=C; export LC_ALL",
             'printf "os\\t%s\\nhost\\t%s\\nuser\\t%s\\nhome\\t%s\\nnow\\t%s\\n" '
             '"$(uname -s)" "$(uname -n)" "$(id -un)" "$HOME" "$(date +%s)"',
             'printf "machine_id\\t%s\\n" "$(cat /etc/machine-id 2>/dev/null)"',
             'command -v sqlite3 >/dev/null 2>&1 && printf "sqlite3\\tyes\\n"',
             'case "$(uname -s)" in Darwin|*BSD|DragonFly) fmt=f; spec="%m\t%z\t%N";; *) fmt=c; spec="%Y\t%s\t%n";; esac']
    for name, root in roots.items():
        lines.append(f'root={root_expression(name, root, remote_env)}; printf "root\\t{name}\\t%s\\n" "$root"')
        lines.append(f'if [ -d "$root" ]; then find "$root" -type f \\( -name "*.jsonl" -o -name "*.jsonl.zst" -o -name sessions-index.json'
                     f' -o -name "state*.sqlite" -o -name "state*.sqlite-wal" \\) -exec stat -$fmt "{name}\t$spec" {{}} +;'
                     f' else printf "missing\\t{name}\\n"; fi')
    info, roots_found, files, missing = {}, {}, {name: {} for name in roots}, set()
    for line in remote.output("\n".join(lines) + "\n").splitlines():
        fields = line.split("\t")
        if fields[0] == "root":
            roots_found[fields[1]] = fields[2]
        elif fields[0] == "missing":
            missing.add(fields[1])
        elif fields[0] in files and len(fields) == 4:
            name, mtime, size, path = fields
            files[name][path] = (int(mtime), int(size))
        elif len(fields) == 2:
            info[fields[0]] = fields[1]
    return info, roots_found, files, missing


def candidates(kind, root, listed, since, after, databases):
    """(mtime, size, relative) of files to send, oldest first."""
    chosen = []
    for path, (mtime, size) in listed.items():
        if not path.startswith(root + "/") or "\n" in path or "\\" in path:
            continue
        relative = path[len(root) + 1:]
        if relative.endswith("-wal") or not wanted(kind, relative):
            continue
        if DATABASE.fullmatch(relative):
            # A database changes through its WAL before the file itself, and
            # a snapshot holds the WAL's pages too: both count toward its size.
            # An empty WAL holds no change; reading a database can leave one.
            wal_mtime, wal_size = listed.get(path + "-wal", (0, 0))
            if wal_size:
                mtime = max(mtime, wal_mtime)
            if mtime > databases.get(relative, -1):
                chosen.append((mtime, size + wal_size, relative))
        elif since is None or mtime > since or mtime == since and (after is None or relative > after):
            chosen.append((mtime, size, relative))
    return sorted(chosen, key=lambda item: (item[0], item[2]))


def transfer_files(run, remote, root, drop_dir, batch):
    """Send a batch of plain files as one tar stream; returns those received."""
    delimiter = "PHAROS_" + secrets.token_hex(8)
    names = "\n".join("./" + relative for _, _, relative in batch)
    process = remote.stream(f"cd {quote(root)} || exit 1\ntar cf - -T - <<'{delimiter}'\n{names}\n{delimiter}\n")
    expected = {relative: mtime for mtime, _, relative in batch}
    received = set()

    class Counting:
        def read(self, size=-1):
            data = process.stdout.read(size)
            run.transferred_bytes += len(data)
            return data

    try:
        with tarfile.open(fileobj=Counting(), mode="r|") as archive:
            for member in archive:
                relative = member.name.removeprefix("./")
                if not member.isfile() or relative not in expected:
                    continue
                source = archive.extractfile(member)
                run.write_file(drop_dir, relative, iter(lambda: source.read(1 << 20), b""), member.mtime)
                received.add(relative)
    except tarfile.TarError as error:
        run.warn(f"transfer from {root} stopped: {error}")
    finally:
        process.stdout.close()
        status = process.wait()
    vanished = [relative for relative in expected if relative not in received]
    if status != 0 and vanished:
        run.warn(f"tar exited {status}; {len(vanished)} file(s) not received, e.g. {vanished[0]}")
    return received


def transfer_database(run, remote, root, drop_dir, relative, mtime):
    """Copy a consistent snapshot of a live SQLite database.

    Returns the stamp to remember: the newest of the database's and its WAL's
    mtimes just after the snapshot, which the snapshot itself may have
    advanced; an empty WAL, which reading can leave behind, holds no change. Mtimes are whole seconds, so a stamp at or after the second the
    snapshot began could hide a write the snapshot missed; it is held to the
    second before, and the next run takes the database once more.
    """
    database = quote(root + "/" + relative)
    script = (f"set -e\ntmp=$(mktemp -d)\ntrap 'rm -rf \"$tmp\"' EXIT\n"
              f"echo \"began $(date +%s)\" >&2\n"
              f"sqlite3 {database} \".backup '$tmp/db'\"\n"
              f"case \"$(uname -s)\" in Darwin|*BSD|DragonFly) fmt=f; spec=%m;; *) fmt=c; spec=%Y;; esac\n"
              f"stat -$fmt \"stamp $spec\" {database} >&2\n"
              f"if [ -s {database}-wal ]; then stat -$fmt \"stamp $spec\" {database}-wal >&2; fi\n"
              f"cat \"$tmp/db\"\n")
    process = remote.stream(script, stderr=subprocess.PIPE)

    def chunks():
        written = 0
        while data := process.stdout.read(1 << 20):
            run.transferred_bytes += len(data)
            written += len(data)
            if not run.fits(written):
                # The listed sizes are an estimate; the budget is not.
                process.kill()
                raise RuntimeError("snapshot is larger than the bytes this run has left")
            yield data
        # Failing here, before the rename, leaves no partial snapshot behind.
        if process.wait() != 0:
            said = [line for line in process.stderr.read().decode(errors="replace").splitlines()
                    if line.strip() and not line.startswith(("stamp ", "began "))]
            reason = f": {said[-1].strip()[:200]}" if said else ""
            raise RuntimeError(f"snapshot failed with exit status {process.returncode}{reason}")

    try:
        run.write_file(drop_dir, relative, chunks(), mtime)
    finally:
        process.stdout.close()
        process.wait()
    said = process.stderr.read().decode(errors="replace").splitlines()
    stamps = [int(line.split()[1]) for line in said if line.startswith("stamp ")]
    began = [int(line.split()[1]) for line in said if line.startswith("began ")]
    stamp = max(stamps, default=mtime)
    return min(stamp, began[0] - 1) if began else stamp


def pull(run):
    params = run.params
    destination = params.get("destination")
    if not destination:
        raise ValueError("params.destination is required")
    sources = params.get("sources") or list(SOURCES)
    unknown = sorted(set(sources) - set(SOURCES))
    if unknown:
        raise ValueError(f"unsupported sources {unknown}; ssh_pull reads {', '.join(SOURCES)}")
    roots = {name: (params.get("roots") or {}).get(name) for name in sources}
    overlap = int(params.get("overlap_seconds", 300))
    batch_bytes = int(params.get("batch_bytes", 64 << 20))

    cursor = run.cursor or {}
    if cursor.get("version") != 1 or cursor.get("destination") != destination:
        cursor = {"version": 1, "destination": destination, "sources": {}}
    previous = cursor["sources"]

    remote = Remote(run, destination, params.get("ssh") or ["ssh", "-T", "-o", "BatchMode=yes"])
    info, root_paths, listed, missing = probe_and_list(remote, roots, bool(params.get("remote_env")))
    now = int(info["now"])
    os_name = {"Linux": "linux", "Darwin": "darwin"}.get(info.get("os"), "other")
    machine = run.machine(params.get("machine_key") or destination,
                          params.get("label") or f"{info.get('host', destination)} ({info.get('user', '?')})",
                          os_name, info.get("machine_id") or None)

    next_sources, stopped, delivered = dict(previous), None, 0
    for name in sources:
        if name in missing:
            if name in OPTIONAL and not roots[name]:
                continue
            run.warn(f"{name}: {root_paths.get(name)} does not exist on {destination}")
            continue
        root = root_paths[name]
        state = previous.get(name) or {}
        since, after = state.get("since"), state.get("after")
        databases = dict(state.get("databases") or {})
        kind = SOURCES[name][0]
        pending = candidates(kind, root, listed[name], since, after, databases)
        if stopped:
            continue
        drop_dir = run.drop(name, machine, kind, root, source=name) if pending else None
        while pending:
            if run.out_of_time():
                stopped = "the deadline is near"
                break
            batch, size = [], 0
            for item in pending:
                if batch and (size + item[1] > batch_bytes or DATABASE.fullmatch(item[2])):
                    break
                if not run.fits(size + item[1]):
                    break
                batch.append(item)
                size += item[1]
                if DATABASE.fullmatch(item[2]):
                    break
            if not batch:
                if delivered == 0 and not run.fits(pending[0][1]):
                    raise ValueError(f"{name}/{pending[0][2]} is {pending[0][1]} bytes, more than max_bytes allows")
                stopped = "the byte budget is spent"
                break
            mtime, _, relative = batch[0]
            if DATABASE.fullmatch(relative):
                if info.get("sqlite3") == "yes":
                    try:
                        databases[relative] = transfer_database(run, remote, root, drop_dir, relative, mtime)
                    except (RuntimeError, OSError) as error:
                        # Its stamp stays as it was, so the next run tries again;
                        # one unreadable database must not hold back the rest.
                        run.warn(f"{name}: {relative} skipped: {error}")
                else:
                    run.warn(f"{name}: sqlite3 is not installed on {destination}; {relative} skipped")
                received = {relative}
            else:
                received = transfer_files(run, remote, root, drop_dir, batch)
            done = 0
            for item in batch:
                if item[2] not in received:
                    break
                done += 1
            delivered += done
            files = [item for item in batch[:done] if not DATABASE.fullmatch(item[2])]
            if files:
                since, after = files[-1][0], files[-1][2]
            pending = pending[done:]
            if done < len(batch):
                stopped = "a transfer was interrupted"
                break
        if any(not DATABASE.fullmatch(item[2]) for item in pending):
            next_sources[name] = {"since": since, "after": after, "databases": databases}
        else:
            # Finished: look again from a little before this run started.
            next_sources[name] = {"since": now - overlap, "databases": databases}

    cursor = {"version": 1, "destination": destination, "sources": next_sources}
    summary = f"{delivered} file(s), {run.dropped_bytes} bytes from {destination}"
    if stopped == "a transfer was interrupted" and delivered == 0:
        run.finish("failed", f"nothing received: {stopped}", run.cursor)
    elif stopped:
        run.finish("partial", f"{summary}; stopped because {stopped}", cursor)
    else:
        run.finish("complete", summary, cursor)


if __name__ == "__main__":
    pharos_importer.main(pull)
