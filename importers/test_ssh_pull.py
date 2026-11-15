"""Runs ssh_pull.py end to end against a fake ssh that runs the remote script here."""

from datetime import datetime, timedelta, timezone
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import sys
import tempfile
import time
import unittest

from pharos_importer import CONTRACT, RESULT_RESERVE

try:
    import jsonschema
    from referencing import Registry, Resource
except ImportError:
    jsonschema = None

HERE = Path(__file__).resolve().parent
CONTRACTS = HERE.parent / "contracts" / "plugins"
SCHEMA_BASE = "https://pharos.pythia.software/contracts/plugins/"
HOUR_AGO = int(time.time()) - 3600


class SSHPullTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name)
        self.home = self.root / "remote-home"
        self.ssh = self.root / "fake-ssh"
        # Drops the destination and runs the remote command here.
        self.ssh.write_text('#!/bin/sh\nshift\nexec "$@"\n')
        self.ssh.chmod(0o755)
        self.files = {
            "claude": {
                "-work-repo/s1.jsonl": b'{"type":"user"}\n' * 2500,
                "-work-repo/s1/subagents/agent-1.jsonl": b'{"type":"assistant"}\n' * 2000,
                "-work-repo/s2.jsonl": b'{"type":"user"}\n' * 2500,
                "-work-repo/sessions-index.json": b'{"entries":[]}',
            },
            "codex": {
                "sessions/2026/10/27/rollout-1.jsonl": b'{"type":"session_meta"}\n' * 2000,
                "session_index.jsonl": b'{"id":"1","thread_name":"x"}\n',
            },
        }
        for kind, files in self.files.items():
            for relative, content in files.items():
                self.write(kind, relative, content)
        self.write("claude", "-work-repo/notes.txt", b"not a transcript")
        self.write("codex", "log/codex-tui.jsonl", b"{}\n")
        database = self.source("codex") / "state_5.sqlite"
        with sqlite3.connect(database) as connection:
            connection.execute("PRAGMA journal_mode=WAL")
            connection.execute("CREATE TABLE threads (id TEXT, name TEXT)")
            connection.execute("INSERT INTO threads VALUES ('1', 'Fix the build')")
        connection.close()
        os.utime(database, (HOUR_AGO, HOUR_AGO))
        self.runs = 0

    def source(self, kind):
        return self.home / {"claude": ".claude/projects", "codex": ".codex"}[kind]

    def write(self, kind, relative, content, mtime=HOUR_AGO):
        path = self.source(kind) / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(content)
        os.utime(path, (mtime, mtime))

    def pull(self, cursor=None, max_bytes=1 << 30, destination="alice@lab-box"):
        self.runs += 1
        out = self.root / f"run-{self.runs}" / "out"
        out.mkdir(parents=True)
        deadline = datetime.now(timezone.utc) + timedelta(minutes=10)
        request = {"contract": CONTRACT, "direction": "import",
                   "plugin": {"id": "software.pythia.ssh-importer", "version": "0.1.0"},
                   "run_id": f"run_test{self.runs}", "output_dir": str(out),
                   "started_at": datetime.now(timezone.utc).isoformat(), "deadline": deadline.isoformat(),
                   "dry_run": False, "trigger": {"kind": "test"}, "limits": {"max_bytes": max_bytes},
                   "api": {"url": "http://127.0.0.1:1/connector/v1", "token": "t" * 32}, "cursor": cursor,
                   "params": {"destination": destination, "ssh": [str(self.ssh)]}}
        self.conforms("run-request.schema.json", request)
        request_path = out.parent / "request.json"
        request_path.write_text(json.dumps(request))
        environment = {"HOME": str(self.home), "PATH": "/usr/bin:/bin:/usr/sbin:/sbin", "LANG": "C"}
        process = subprocess.run([sys.executable, str(HERE / "ssh_pull.py"), str(request_path)],
                                 env=environment, capture_output=True, text=True)
        result = json.loads((out / "result.json").read_text())
        self.assertEqual(process.returncode != 0, result["status"] == "failed", process.stderr)
        self.conforms("run-result.schema.json", result)
        delivered = {}
        for drop in result["drops"]:
            for path in (out / drop["dir"]).rglob("*"):
                if path.is_file():
                    delivered[(drop["kind"], path.relative_to(out / drop["dir"]).as_posix())] = path
        return result, delivered

    def conforms(self, schema, instance):
        """The request this test writes and the result ssh_pull returns follow the contract."""
        if jsonschema is None:
            return
        schemas = {path.name: json.loads(path.read_text()) for path in CONTRACTS.glob("*.schema.json")}
        registry = Registry().with_resources(
            (SCHEMA_BASE + name, Resource.from_contents(value)) for name, value in schemas.items())
        validator = jsonschema.Draft202012Validator({"$ref": SCHEMA_BASE + schema}, registry=registry)
        self.assertEqual([error.message for error in validator.iter_errors(instance)], [], schema)

    def test_first_run_delivers_what_pharos_reads_with_mtimes(self):
        result, delivered = self.pull()
        self.assertEqual(result["status"], "complete", result)
        expected = {(kind, relative) for kind, files in self.files.items() for relative in files}
        self.assertEqual(set(delivered), expected | {("codex", "state_5.sqlite")})
        for (kind, relative), path in delivered.items():
            if relative != "state_5.sqlite":
                self.assertEqual(path.read_bytes(), self.files[kind][relative])
            self.assertEqual(int(path.stat().st_mtime), HOUR_AGO, relative)
        with sqlite3.connect(delivered[("codex", "state_5.sqlite")]) as connection:
            self.assertEqual(connection.execute("SELECT name FROM threads").fetchall(), [("Fix the build",)])
        self.assertEqual([machine["key"] for machine in result["machines"]], ["alice@lab-box"])
        roots = {drop["kind"]: drop["root"] for drop in result["drops"]}
        self.assertEqual(roots, {"claude": f"{self.home}/.claude/projects", "codex": f"{self.home}/.codex"})
        self.assertGreater(result["transferred_bytes"], 0)

    def test_cursor_fetches_only_changed_files(self):
        first, _ = self.pull()
        second, delivered = self.pull(first["cursor"])
        self.assertEqual((second["status"], delivered), ("complete", {}))
        self.write("claude", "-work-repo/s2.jsonl", b'{"type":"user"}\n' * 2600, mtime=int(time.time()))
        third, delivered = self.pull(second["cursor"])
        self.assertEqual((third["status"], set(delivered)), ("complete", {("claude", "-work-repo/s2.jsonl")}))

    def test_byte_budget_ends_runs_early_and_later_runs_finish(self):
        everything, _ = self.pull()
        _, all_files = self.pull()
        cursor, seen, statuses = None, set(), []
        for _ in range(10):
            result, delivered = self.pull(cursor, max_bytes=RESULT_RESERVE + 90_000)
            self.assertTrue(delivered, "every run must make progress")
            seen |= set(delivered)
            statuses.append(result["status"])
            cursor = result["cursor"]
            if result["status"] == "complete":
                break
        self.assertEqual(statuses[-1], "complete")
        self.assertIn("partial", statuses)
        self.assertEqual(seen, set(all_files))

    def test_a_file_larger_than_the_budget_fails_without_moving_the_cursor(self):
        result, _ = self.pull(max_bytes=RESULT_RESERVE + 10_000)
        self.assertEqual(result["status"], "failed")
        self.assertIn("more than max_bytes", result["message"])
        self.assertIsNone(result["cursor"])

    def test_a_new_destination_starts_over(self):
        first, _ = self.pull()
        result, delivered = self.pull(first["cursor"], destination="alice@other-box")
        self.assertEqual(len(delivered), len(self.pull()[1]))
        self.assertEqual(result["cursor"]["destination"], "alice@other-box")


if __name__ == "__main__":
    unittest.main()
