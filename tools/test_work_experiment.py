"""Artifact isolation and acceptance checks for the execution experiment."""
import json
from pathlib import Path
import sqlite3
import tempfile
import unittest
from unittest import mock

import work_experiment as work


class WorkExperimentTests(unittest.TestCase):
    def backup(self, root):
        backup = root / "completed-backup"
        (backup / "catalog").mkdir(parents=True)
        db = sqlite3.connect(backup / "catalog/catalog.sqlite3")
        db.execute("PRAGMA journal_mode=WAL")
        db.executescript("CREATE TABLE meta(key,value); INSERT INTO meta VALUES('schema_version','23');"
                         "CREATE TABLE workspaces(id); CREATE TABLE conversations(id); CREATE TABLE messages(id,text);"
                         "INSERT INTO messages VALUES('kept','private transcript');")
        db.commit()
        self.addCleanup(db.close)
        return backup, db

    def test_completed_backup_wal_is_cloned_before_sqlite_is_opened(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            backup, source = self.backup(root)
            master = root / "master"
            before = work.stamp(backup / "catalog")
            real_connect, opened = sqlite3.connect, []
            def connect(path, *args, **kwargs):
                opened.append(Path(path))
                self.assertTrue(Path(path).resolve().is_relative_to(master.resolve()))
                return real_connect(path, *args, **kwargs)
            with mock.patch.object(work.sqlite3, "connect", side_effect=connect):
                report = work.prepare(backup, master)
            self.assertEqual(report["counts"]["messages"], 1)
            self.assertEqual(before, work.stamp(backup / "catalog"))
            self.assertEqual(len(opened), 1)
            config = work.arm(master, root / "arm", {"name": "test"})
            self.assertIn("library = false", config.read_text())
            with real_connect(root / "arm/catalog/catalog.sqlite3") as arm:
                arm.execute("DELETE FROM messages")
                arm.commit()
            self.assertEqual(source.execute("SELECT count(*) FROM messages").fetchone()[0], 1)
            with real_connect(master / "catalog/catalog.sqlite3") as original:
                self.assertEqual(original.execute("SELECT count(*) FROM messages").fetchone()[0], 1)

    def test_rejects_overlap_existing_artifacts_links_and_changed_backups(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            backup, _ = self.backup(root)
            with self.assertRaises(ValueError):
                work.prepare(backup, backup / "nested")
            occupied = root / "occupied"
            occupied.mkdir()
            with self.assertRaises(ValueError):
                work.prepare(backup, occupied)
            link = root / "link"
            link.symlink_to(backup, target_is_directory=True)
            with self.assertRaises(ValueError):
                work.prepare(link, root / "master")
            with mock.patch.object(work, "stamp", side_effect=[{"v": 1}, {"v": 2}]):
                with self.assertRaises(ValueError):
                    work.prepare(backup, root / "changing")
            self.assertFalse((root / "changing").exists())
            self.assertTrue((backup / "catalog/catalog.sqlite3").exists())

    def test_semantic_oracles_stream_hashes_and_never_export_private_values(self):
        with tempfile.TemporaryDirectory() as directory:
            backup, _ = self.backup(Path(directory))
            report = work.digests(backup / "catalog/catalog.sqlite3", {"canonical": "SELECT id,text FROM messages ORDER BY id"})
            self.assertEqual(report["canonical"]["rows"], 1)
            self.assertNotIn("private transcript", json.dumps(report))
            with self.assertRaises(sqlite3.OperationalError):
                work.digests(backup / "catalog/catalog.sqlite3", {"illegal": "DELETE FROM messages"})

    def test_bad_arms_cannot_be_reported_as_success_or_acceptance(self):
        baseline = {"descriptor_sha256":"same", "corpus":{"counts":1}, "settled":True,
                    "semantic":{"v":1}, "wall_seconds":100, "cpu_seconds":50}
        candidate = dict(baseline, wall_seconds=120)
        self.assertTrue(work.compare(baseline, candidate)["rejected"])
        candidate = dict(baseline, wall_seconds=90, cpu_seconds=55)
        report = work.compare(baseline,candidate)
        self.assertFalse(report["rejected"])
        self.assertEqual(report["release_gate"], "incomplete")
        with self.assertRaises(ValueError):
            work.compare(baseline,dict(candidate,descriptor_sha256="different"))
        with self.assertRaises(ValueError):
            work.compare(baseline,dict(candidate,settled=False))
        self.assertTrue(work.compare(baseline,dict(candidate,semantic={"v":2}))["rejected"])


if __name__ == "__main__":
    unittest.main()
