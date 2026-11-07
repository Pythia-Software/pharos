"""Artifact isolation and acceptance checks for the execution experiment."""
import hashlib
import json
from pathlib import Path
import sqlite3
import tempfile
import unittest
from unittest import mock

import work_experiment as work

CHECKS = {"semantic_checks": {"canonical": "SELECT count(*) FROM messages"},
          "pending_checks": {"labels": "SELECT 1 WHERE 0"}}


def descriptor(**extra):
    return {"name": "test", "startup_command": ["health"], "commands": [["health"]],
            "expected_schema_version": 23, **CHECKS, **extra}


class WorkExperimentTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name).resolve()

    def backup(self, manifest=True):
        backup = self.root / "completed-backup"
        (backup / "catalog").mkdir(parents=True)
        (backup / "captures").mkdir()
        (backup / "captures/one.jsonl").write_text("{}")
        db = sqlite3.connect(backup / "catalog/catalog.sqlite3")
        db.execute("PRAGMA journal_mode=WAL")
        db.executescript("CREATE TABLE meta(key,value); INSERT INTO meta VALUES('schema_version','23');"
                         "CREATE TABLE workspaces(id); CREATE TABLE conversations(id); CREATE TABLE messages(id,text);"
                         "INSERT INTO messages VALUES('kept','private transcript');")
        db.commit()
        self.addCleanup(db.close)
        if manifest:
            s = (backup / "catalog/catalog.sqlite3").stat()
            (backup / work.BACKUP_MANIFEST).write_text(json.dumps({
                "version": 1, "complete": True, "finished_at": "2026-11-01T00:00:00Z",
                "catalog": {"path": "catalog/catalog.sqlite3", "size": s.st_size, "mtime_ns": s.st_mtime_ns},
                "items": [{"name": "captures", "path": "captures"}]}))
        return backup, db

    def binary(self, name, status=0):
        path = self.root / name
        path.write_text(f"#!/bin/sh\n# {name}\nexit {status}\n")
        path.chmod(0o700)
        return path, hashlib.sha256(path.read_bytes()).hexdigest()

    def test_completed_backup_wal_is_cloned_before_sqlite_is_opened(self):
        backup, source = self.backup()
        master = self.root / "master"
        real_connect, opened = sqlite3.connect, []
        def connect(path, *args, **kwargs):
            opened.append(Path(path))
            self.assertTrue(Path(path).resolve().is_relative_to(master.resolve()))
            return real_connect(path, *args, **kwargs)
        with mock.patch.object(work.sqlite3, "connect", side_effect=connect):
            marker = work.prepare(backup, master)
        self.assertEqual(marker["counts"]["messages"], 1)
        self.assertEqual(marker["evidence"]["basis"], "product-manifest")
        self.assertEqual(len(opened), 1)
        self.assertTrue((master / "captures/one.jsonl").exists())
        config = work.arm(master, self.root / "arm", descriptor())
        self.assertIn("library = false", config.read_text())
        with real_connect(self.root / "arm/catalog/catalog.sqlite3") as arm:
            arm.execute("DELETE FROM messages")
            arm.commit()
        self.assertEqual(source.execute("SELECT count(*) FROM messages").fetchone()[0], 1)
        with real_connect(master / "catalog/catalog.sqlite3") as original:
            self.assertEqual(original.execute("SELECT count(*) FROM messages").fetchone()[0], 1)

    def test_backup_needs_completion_evidence_that_does_not_change(self):
        backup, _ = self.backup(manifest=False)
        with self.assertRaises(ValueError):
            work.prepare(backup, self.root / "unproven")
        attestation = self.root / "attestation.json"
        attestation.write_text(json.dumps({"completed_at": "2026-11-01", "method": "pharos backup", "attested_by": "operator"}))
        marker = work.prepare(backup, self.root / "attested", attestation)
        self.assertEqual(marker["evidence"]["basis"], "operator-attested")
        self.assertIn("operator's statement", marker["evidence"]["guarantee"])
        (backup / work.BACKUP_MANIFEST).write_text(json.dumps({"version": 1, "complete": False, "catalog": {}}))
        with self.assertRaises(ValueError):
            work.prepare(backup, self.root / "incomplete")
        s = (backup / "catalog/catalog.sqlite3").stat()
        (backup / work.BACKUP_MANIFEST).write_text(json.dumps({"version": 1, "complete": True, "finished_at": "x",
            "catalog": {"path": "catalog/catalog.sqlite3", "size": s.st_size, "mtime_ns": s.st_mtime_ns + 1}}))
        with self.assertRaises(ValueError):
            work.prepare(backup, self.root / "replaced")
        for name in ("unproven", "incomplete", "replaced"):
            self.assertFalse((self.root / name).exists())

    def test_rejects_overlap_existing_artifacts_links_and_changed_backups(self):
        backup, _ = self.backup()
        with self.assertRaises(ValueError):
            work.prepare(backup, backup / "nested")
        occupied = self.root / "occupied"
        occupied.mkdir()
        with self.assertRaises(ValueError):
            work.prepare(backup, occupied)
        link = self.root / "link"
        link.symlink_to(backup, target_is_directory=True)
        with self.assertRaises(ValueError):
            work.prepare(link, self.root / "master")
        with mock.patch.object(work, "stamp", side_effect=[{"v": 1}, {"v": 2}]):
            with self.assertRaises(ValueError):
                work.prepare(backup, self.root / "changing")
        self.assertFalse((self.root / "changing").exists())
        self.assertTrue((backup / "catalog/catalog.sqlite3").exists())

    def test_inputs_are_frozen_once_and_never_reread(self):
        backup, _ = self.backup()
        master = self.root / "master"
        work.prepare(backup, master)
        live = self.root / "live-source"
        live.mkdir()
        (live / "session.jsonl").write_text("first")
        identity = work.freeze(master, "claude", live)
        self.assertEqual(identity["files"], 1)
        (live / "session.jsonl").write_text("changed after freezing")
        source = descriptor(sources=[{"name": "claude", "kind": "claude", "input": "claude"}])
        config = work.arm(master, self.root / "arm", source).read_text()
        self.assertNotIn(str(live), config)
        self.assertEqual((self.root / "arm/inputs/claude/session.jsonl").read_text(), "first")
        with self.assertRaises(ValueError):
            work.validate_descriptor(descriptor(sources=[{"name": "claude", "kind": "claude", "snapshot": str(live)}]))
        with self.assertRaises(ValueError):
            work.validate_descriptor(descriptor(model_settings={"labels_model_dir": str(live)}))
        (master / "inputs/claude/session.jsonl").write_text("mutated master")
        with self.assertRaises(ValueError):
            work.arm(master, self.root / "after-mutation", source)

    def test_semantic_oracles_stream_hashes_and_never_export_private_values(self):
        backup, _ = self.backup()
        database = backup / "catalog/catalog.sqlite3"
        report = work.digests(database, {"canonical": "SELECT id,text FROM messages ORDER BY id"})
        self.assertEqual(report["canonical"]["rows"], 1)
        self.assertNotIn("private transcript", json.dumps(report))
        other = self.root / "other.sqlite3"
        sqlite3.connect(other).close()
        for query in ("DELETE FROM messages", f"ATTACH DATABASE '{other}' AS other", "PRAGMA query_only=OFF"):
            with self.assertRaises(sqlite3.DatabaseError):
                work.digests(database, {"illegal": query})

    def test_descriptor_without_checks_is_refused_up_front(self):
        work.validate_descriptor(descriptor())
        for broken in ({"semantic_checks": {}}, {"pending_checks": {}}, {"name": ""}, {"startup_command": None},
                       {"commands": [["labels", "fetch"]]}, {"expected_schema_version": "23"}):
            with self.assertRaises(ValueError):
                work.validate_descriptor(descriptor(**broken))

    def test_failed_arm_keeps_an_unsettled_report_and_engines_come_from_artifacts(self):
        backup, _ = self.backup()
        master = self.root / "master"
        work.prepare(backup, master)
        retained, retained_sha = self.binary("retained")
        failing, _ = self.binary("failing", 3)
        with self.assertRaises(RuntimeError):
            work.run(failing, master, self.root / "failed", descriptor(), "candidate", retained_sha)
        failed = json.loads((self.root / "failed/report.json").read_text())
        self.assertFalse(failed["settled"])
        self.assertIsNotNone(failed["failure"])
        self.assertEqual((self.root / "failed/logs/startup.log").stat().st_mode & 0o777, 0o600)
        with self.assertRaises(ValueError):
            work.run(failing, master, self.root / "not-retained", descriptor(), "baseline", retained_sha)
        with self.assertRaises(ValueError):
            work.run(retained, master, self.root / "same", descriptor(), "candidate", retained_sha)
        baseline = work.run(retained, master, self.root / "baseline", descriptor(), "baseline", retained_sha)
        self.assertEqual(baseline["engine_actual"], {"value": "classic", "basis": "retained-artifact-sha256"})
        candidate_binary, _ = self.binary("candidate")
        candidate = work.run(candidate_binary, master, self.root / "candidate-arm", descriptor(), "candidate", retained_sha)
        self.assertEqual(candidate["engine_actual"]["basis"], "unavailable")
        self.assertEqual(work.compare(baseline, candidate, "ab", retained_sha)["release_gate"], "refused")
        again = work.run(retained, master, self.root / "again", descriptor(), "baseline", retained_sha)
        self.assertEqual(work.compare(baseline, again, "aa", retained_sha)["release_gate"], "not-applicable")
        with self.assertRaises(ValueError):
            work.compare(baseline, candidate, "aa", retained_sha)
        with self.assertRaises(ValueError):
            work.compare(candidate, baseline, "ab", retained_sha)

    def report(self, role="candidate", binary="new", **extra):
        step = lambda seconds: {"command": "labels", "exit_code": 0, "seconds": seconds, "cpu_seconds": seconds}
        value = {"version": work.VERSION, "role": role, "binary_sha256": binary, "descriptor_sha256": "same",
                 "corpus": {"counts": 1}, "settled": True, "semantic": {"v": {"rows": 1, "sha256": "a"}},
                 "pending": {"p": {"rows": 0, "sha256": "b"}}, "startup": {"seconds": 1000, "schema_version": 23},
                 "commands": [step(100), step(100)], "wall_seconds": 200, "cpu_seconds": 200,
                 "engine_actual": {"value": "scheduled", "basis": "executed-path"}}
        value.update(extra)
        return value

    def test_bad_arms_cannot_be_reported_as_success_or_acceptance(self):
        baseline = self.report("baseline", "old", engine_actual={"value": "classic", "basis": "retained-artifact-sha256"})
        compare = lambda candidate, mode="ab": work.compare(baseline, candidate, mode, "old")
        self.assertTrue(compare(self.report(wall_seconds=240))["rejected"])
        report = compare(self.report(wall_seconds=190))
        self.assertFalse(report["rejected"])
        self.assertEqual(report["release_gate"], "incomplete")
        # A step regression hidden by unchanged totals still rejects.
        slower = self.report(commands=[dict(baseline["commands"][0], seconds=150), dict(baseline["commands"][1], seconds=50)])
        self.assertTrue(compare(slower)["rejected"])
        self.assertTrue(compare(self.report(semantic={"v": {"rows": 1, "sha256": "c"}}))["rejected"])
        for broken in ({"descriptor_sha256": "different"}, {"settled": False}, {"semantic": {}}, {"pending": {}},
                       {"pending": {"p": {"rows": 1, "sha256": "b"}}}, {"corpus": {"counts": 2}}, {"binary_sha256": "old"}):
            with self.assertRaises(ValueError):
                compare(self.report(**broken))
        with self.assertRaises(ValueError):
            work.compare(self.report(), baseline, "ab", "old")
        unproven = self.report(engine_actual={"value": "scheduled", "basis": "environment"})
        self.assertEqual(compare(unproven)["release_gate"], "refused")


if __name__ == "__main__":
    unittest.main()
