"""Artifact isolation and acceptance checks for the execution experiment."""
import hashlib
import json
import os
import subprocess
import sys
from pathlib import Path
import sqlite3
import tempfile
import unittest
from unittest import mock

import work_experiment as work

CHECKS = {"semantic_checks": {"canonical": "SELECT count(*) FROM messages"},
          "pending_checks": {"labels": ["SELECT 1 WHERE 0"]}}
FLOOR = work.SCHEMA_FLOOR


def descriptor(**extra):
    return {"name": "test", "startup_command": ["health"], "commands": [["health"]],
            "expected_schema_version": FLOOR, **CHECKS, **extra}


class Fixture(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name).resolve()
        # This Mac's own Pharos registries are never consulted by tests.
        support = self.root / "support"
        patcher = mock.patch.dict(os.environ, {"PHAROS_SUPPORT_DIR": str(support)})
        patcher.start()
        self.addCleanup(patcher.stop)

    def backup(self, manifest=True):
        backup = self.root / "completed-backup"
        (backup / "catalog").mkdir(parents=True)
        (backup / "captures").mkdir()
        (backup / "captures/one.jsonl").write_text("{}")
        db = sqlite3.connect(backup / "catalog/catalog.sqlite3")
        db.execute("PRAGMA journal_mode=WAL")
        db.executescript(f"CREATE TABLE meta(key,value); INSERT INTO meta VALUES('schema_version','{FLOOR}');"
                         "CREATE TABLE workspaces(id); CREATE TABLE conversations(id); CREATE TABLE messages(id,text);"
                         "INSERT INTO messages VALUES('kept','private transcript');")
        db.commit()
        self.addCleanup(db.close)
        if manifest:
            s = (backup / "catalog/catalog.sqlite3").stat()
            (backup / work.BACKUP_MANIFEST).write_text(json.dumps({
                "version": 1, "complete": True, "finished_at": "2026-11-01T00:00:00Z",
                "catalog": {"path": "catalog/catalog.sqlite3", "size": s.st_size, "mtime_ns": s.st_mtime_ns},
                "items": [{"name": "captures", "path": "captures"}, {"name": "hosts", "path": "hosts"}]}))
        return backup, db

    def binary(self, name, status=0):
        path = self.root / name
        path.write_text(f"#!/bin/sh\n# {name}\nexit {status}\n")
        path.chmod(0o700)
        return path, hashlib.sha256(path.read_bytes()).hexdigest()



class WorkExperimentTests(Fixture):
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
        self.assertEqual(marker["evidence"]["excluded_items"], ["hosts"])
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

    def test_registered_and_default_libraries_are_never_sources_or_destinations(self):
        backup, _ = self.backup()
        library = self.root / "Drive/Pharos"
        (library / "nested").mkdir(parents=True)
        registry = self.root / "support/applications/abc/registry.json"
        registry.parent.mkdir(parents=True)
        registry.write_text(json.dumps({"version": 1, "libraries": [{"id": "l", "config": str(library / "archive.toml")}]}))
        for source, destination in ((library / "nested", self.root / "m1"), (backup, library / "experiment"),
                                    (backup, self.root / "Drive"), (self.root / "support/captures", self.root / "m2")):
            with self.assertRaisesRegex(ValueError, "library Pharos uses"):
                work.prepare(source, destination)
        self.assertFalse((library / "experiment").exists())
        with self.assertRaisesRegex(ValueError, "library Pharos uses"):
            work.observe_original(library, self.root)
        # Model inputs in the support directory are not a library.
        master = self.root / "master"
        work.prepare(backup, master)
        (self.root / "support/labeler/model").mkdir(parents=True)
        (self.root / "support/labeler/model/weights").write_text("w")
        work.freeze(master, "model", self.root / "support/labeler/model")
        # Nonstandard data paths are protected even outside the library folder.
        external = self.root / "external-data"
        external.mkdir()
        (library / "archive.toml").write_text(f'catalog_path = {json.dumps(str(external / "catalog.sqlite3"))} # comment\ncapture_root = {json.dumps(str(external / "captures"))}\n')
        with self.assertRaisesRegex(ValueError, "library Pharos uses"):
            work.prepare(backup, external / "experiment")
        # Remounted drives are covered by registry-relative metadata alone.
        mounts = self.root / "Volumes"
        remounted = mounts / "euclid 1/Pharos"
        remounted.mkdir(parents=True)
        registry.write_text(json.dumps({"libraries": [{"config": "/Volumes/euclid/Pharos/archive.toml", "relative": "Pharos/archive.toml"}]}))
        original_path = work.Path
        with mock.patch.object(work, "Path", side_effect=lambda value: mounts if value == "/Volumes" else original_path(value)):
            with self.assertRaisesRegex(ValueError, "library Pharos uses"):
                work.prepare(backup, remounted / "experiment")
        # An unreadable registry refuses rather than guessing.
        registry.write_text("{")
        with self.assertRaisesRegex(ValueError, "registry"):
            work.prepare(backup, self.root / "m3")

    def test_config_paths_follow_go_expansion_and_sections(self):
        config = self.root / "library/archive.toml"
        config.parent.mkdir()
        home = self.root / "home"
        config.write_text('data_dir = "~/PharosData"\narchive_root = "~/kept"\nstaging_root = "~/pending"\n[other]\ncapture_root = "~/raw"\ncatalog_path = "~/catalog/catalog.sqlite3"\n')
        with mock.patch.object(work.Path, "home", return_value=home):
            roots = work.config_data_roots(config)
        for name in ("PharosData", "kept", "pending", "raw", "catalog"):
            self.assertIn(home / name, roots)
        config.write_text('[[sources]]\ncatalog_path = "ignored-source-path"\n[repositories.aliases]\ncapture_root = "ignored-alias"\n[repositories.separate]\narchive_root = "ignored-separate"\n')
        self.assertFalse(work.config_data_roots(config))
        config.write_text('data_dir = "~"\n')
        with mock.patch.object(work.Path, "home", return_value=home):
            self.assertEqual(work.config_data_roots(config), {home})

    def test_backup_catalog_symlink_is_refused_before_copy(self):
        backup, _ = self.backup()
        external = self.root / "escaped"
        (backup / "catalog").rename(external)
        (backup / "catalog").symlink_to(external, target_is_directory=True)
        with mock.patch.object(work, "clone") as clone:
            with self.assertRaises(ValueError):
                work.prepare(backup, self.root / "master")
            clone.assert_not_called()

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
        # Row order is not semantic: a check without ORDER BY cannot mismatch on it alone.
        rows = "SELECT 'a' UNION ALL SELECT 'b' UNION ALL SELECT 'b'"
        ordered = work.digests(database, {"q": rows})["q"]
        self.assertEqual(ordered, work.digests(database, {"q": f"SELECT * FROM ({rows}) ORDER BY 1 DESC"})["q"])
        self.assertNotEqual(ordered, work.digests(database, {"q": "SELECT 'a' UNION ALL SELECT 'b'"})["q"])
        other = self.root / "other.sqlite3"
        sqlite3.connect(other).close()
        for query in ("DELETE FROM messages", f"ATTACH DATABASE '{other}' AS other", "PRAGMA query_only=OFF"):
            with self.assertRaises(sqlite3.DatabaseError):
                work.digests(database, {"illegal": query})

    def test_descriptor_without_checks_is_refused_up_front(self):
        work.validate_descriptor(descriptor())
        for broken in ({"semantic_checks": {}}, {"pending_checks": {}}, {"name": ""}, {"startup_command": None},
                       {"commands": [["labels", "fetch"]]}, {"expected_schema_version": str(FLOOR)},
                       # One pending_checks shape for CLI and probe workloads: domain -> statements.
                       {"pending_checks": {"labels": "SELECT 1 WHERE 0"}},
                       {"pending_checks": {"labels": ["DELETE FROM messages"]}},
                       {"semantic_checks": {"canonical": "SELECT 1; SELECT 2"}}):
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

    def test_startup_orphans_refuse_the_arm(self):
        backup, _ = self.backup()
        master = self.root / "master"
        work.prepare(backup, master)
        retained, retained_sha = self.binary("retained")
        with mock.patch.object(work, "supervised", return_value={"exit_code": 0, "orphaned_descendants": True}):
            with self.assertRaisesRegex(RuntimeError, "descendants"):
                work.run(retained, master, self.root / "orphan", descriptor(), "baseline", retained_sha)
        report = json.loads((self.root / "orphan/report.json").read_text())
        self.assertFalse(report["settled"])

    def report(self, role="candidate", binary="new", **extra):
        step = lambda seconds: {"command": "labels", "exit_code": 0, "seconds": seconds, "cpu_seconds": seconds}
        value = {"version": work.VERSION, "role": role, "binary_sha256": binary, "descriptor_sha256": "same",
                 "corpus": {"counts": 1}, "settled": True, "semantic": {"v": {"rows": 1, "sha256": "a", "basis": work.DIGEST_BASIS}},
                 "pending": {"p": {"rows": 0, "sha256": "b", "basis": work.DIGEST_BASIS}},
                 "startup": {"seconds": 1000, "cpu_seconds": 1000, "schema_version": FLOOR},
                 "commands": [step(100), step(100)], "wall_seconds": 200, "cpu_seconds": 200,
                 "engine_actual": {"value": "scheduled", "basis": "executed-path"}, "cpu_basis": work.CPU_BASIS}
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
        self.assertTrue(compare(self.report(semantic={"v": {"rows": 1, "sha256": "c", "basis": work.DIGEST_BASIS}}))["rejected"])
        # CPU is gated on baseline CPU: a mostly-waiting step's noise cannot reject.
        def waiting(cpu):
            return [{"command": "labels", "exit_code": 0, "seconds": 100, "cpu_seconds": cpu}] * 2
        quiet = self.report("baseline", "old", commands=waiting(0.05), cpu_seconds=0.1,
                            engine_actual={"value": "classic", "basis": "retained-artifact-sha256"})
        noisy = work.compare(quiet, self.report(commands=waiting(0.08), cpu_seconds=0.16), "ab", "old")
        self.assertFalse(noisy["rejected"])
        self.assertEqual((noisy["steps"][0]["wall"]["gated"], noisy["steps"][0]["cpu"]["gated"]), (True, False))
        busy = self.report("baseline", "old", commands=waiting(50), cpu_seconds=100,
                           engine_actual={"value": "classic", "basis": "retained-artifact-sha256"})
        self.assertTrue(work.compare(busy, self.report(commands=waiting(70), cpu_seconds=140), "ab", "old")["rejected"])
        # Digests computed another way (query-ordered) never compare.
        with self.assertRaisesRegex(ValueError, "digest basis"):
            compare(self.report(semantic={"v": {"rows": 1, "sha256": "a"}}))
        for broken in ({"descriptor_sha256": "different"}, {"settled": False}, {"semantic": {}}, {"pending": {}},
                       {"pending": {"p": {"rows": 1, "sha256": "b"}}}, {"corpus": {"counts": 2}}, {"binary_sha256": "old"}):
            with self.assertRaises(ValueError):
                compare(self.report(**broken))
        with self.assertRaises(ValueError):
            work.compare(self.report(), baseline, "ab", "old")
        unproven = self.report(engine_actual={"value": "scheduled", "basis": "environment"})
        self.assertEqual(compare(unproven)["release_gate"], "refused")
        # The floor applies to either arm: a step that climbs from under it, or
        # from zero, is a regression, as is a slower startup.
        def short(seconds, cpu):
            return [{"command": "labels", "exit_code": 0, "seconds": seconds, "cpu_seconds": cpu}] * 2
        tiny = self.report("baseline", "old", commands=short(4, 0), wall_seconds=8, cpu_seconds=0,
                           engine_actual={"value": "classic", "basis": "retained-artifact-sha256"})
        climbed = work.compare(tiny, self.report(commands=short(600, 0), wall_seconds=1200, cpu_seconds=0), "ab", "old")
        self.assertTrue(climbed["rejected"])
        self.assertTrue(work.compare(tiny, self.report(commands=short(4, 10), wall_seconds=8, cpu_seconds=20), "ab", "old")["rejected"])
        self.assertFalse(work.compare(tiny, self.report(commands=short(4.5, 0.01), wall_seconds=9, cpu_seconds=0.02), "ab", "old")["rejected"])
        self.assertTrue(compare(self.report(startup={"seconds": 1300, "cpu_seconds": 1000, "schema_version": FLOOR}))["rejected"])
        self.assertEqual(compare(self.report("baseline", "old", startup={"seconds": 1300, "cpu_seconds": 1000,
                                                                          "schema_version": FLOOR}), "aa")["noise"], "exceeds-gate")


PRODUCT = {"route": work.PRODUCT_ROUTE, "query": {"version": 3}, "latency_ms": 2500, "consecutive": 3,
           "min_expected_rows": 1, "expected_sha256": ""}
KINDS = ["analysis.authorship", "analysis.labels", "maintenance.labels.query", "maintenance.parallelism"]


def probe_descriptor(**extra):
    value = {"name": "probe", "clock": "2026-11-01T22:00:00Z",
             "phases": [{"name": "recent", "action": "recent", "kinds": ["analysis.labels"]},
                        {"name": "history", "action": "backfill", "kinds": KINDS}],
             "pending_checks": {kind: ["SELECT 1 WHERE 0"] for kind in KINDS},
             "semantic_checks": {"canonical": "SELECT count(*) FROM messages"}, "product": dict(PRODUCT)}
    value.update(extra)
    return value


class ProbeArmTests(Fixture):
    def master(self):
        backup, _ = self.backup()
        master = self.root / "master"
        work.prepare(backup, master)
        return master

    def fake_probe(self, name, inner, status=0):
        """A stand-in probe binary that writes the given probe report."""
        path = self.root / name
        payload = json.dumps(inner).replace("'", "'\\''")
        path.write_text(f"#!/bin/sh\nprintf '%s' '{payload}' > \"$PHAROS_WORK_REPLICA/probe-report.json\"\n"
                        f"test \"$PHAROS_WORK_ENGINE\" != '' || exit 9\nexit {status}\n")
        path.chmod(0o700)
        return path, hashlib.sha256(path.read_bytes()).hexdigest()

    def inner(self, engine="classic", **extra):
        value = {"version": work.VERSION, "settled": True, "t_settled": 100.0, "t_correct": 10.0, "t_usable": 12.0,
                 "final_product_sha256": "a" * 64, "final_product_rows": 4, "expected_sha256": "",
                 "population": {"phases": [{"phase": "recent", "conversations": 2, "sha256": "p"}],
                                "kinds": [{"kind": "analysis.labels", "whole": False, "conversations": 2, "sha256": "p"}]},
                 "startup": {"wall_seconds": 1.0, "cpu_seconds": 0.5, "cpu_complete": True},
                 "work": {"wall_seconds": 100.0, "cpu_seconds": 100.0, "cpu_complete": True},
                 "windows": {"startup": [999.0, 1000.0], "work": [1000.0, 1100.0], "readiness": [1000.0, 1012.0]},
                 "engine_actual": {"value": engine, "basis": "probe"},
                 "cpu_policy": {"requested_slots": 0, "supported": False, "effective_slots": None,
                                "gomaxprocs": 8, "logical_cpus": 8}}
        value.update(extra)
        return value

    def test_import_snapshot_records_derivation_without_claiming_completion(self):
        master = self.master()
        real_connect = sqlite3.connect
        derived = self.root / "derived"
        def connect(path, *args, **kwargs):
            self.assertTrue(Path(path).resolve().is_relative_to(derived.resolve()))
            return real_connect(path, *args, **kwargs)
        original = self.root / "completed-backup"
        # The backup's open test connection keeps a -shm; date the catalog before it.
        os.utime(original / "catalog/catalog.sqlite3", (1, 1))
        with mock.patch.object(work.sqlite3, "connect", side_effect=connect):
            marker = work.import_snapshot(master, derived, original)
        evidence = marker["evidence"]
        self.assertEqual((evidence["basis"], evidence["completion"], evidence["offline"]),
                         ("derived-experiment-snapshot", "unattested", "unattested"))
        self.assertEqual(evidence["integrity"]["result"], "ok")
        self.assertTrue(evidence["original_observation"]["capture_names_and_sizes_match"])
        self.assertTrue(evidence["original_observation"]["notes"])
        self.assertEqual(marker["lineage"][0]["step"], "import-snapshot")
        work.checked_master(derived)
        with mock.patch.object(work, "stamp", side_effect=[{"v": 1}, {"v": 2}]):
            with self.assertRaises(ValueError):
                work.import_snapshot(master, self.root / "changing")
        self.assertFalse((self.root / "changing").exists())
        # An older diagnostic master can be a source, never an arm's master.
        old = json.loads((master / work.MARKER).read_text())
        work.write_json(master / work.MARKER, {**old, "version": 1})
        with self.assertRaises(ValueError):
            work.checked_master(master)
        self.assertEqual(work.import_snapshot(master, self.root / "from-v1")["evidence"]["derived_from"]["marker_version"], 1)

    def test_normalize_runs_only_the_retained_startup_and_pins_the_schema(self):
        master = self.master()
        with sqlite3.connect(master / "catalog/catalog.sqlite3") as db:
            db.execute("UPDATE meta SET value='21'")
        old = json.loads((master / work.MARKER).read_text())
        work.write_json(master / work.MARKER, {**old, "schema_version": 21,
                                               "master_stamp_sha256": work.digest_of(work.stamp(work.master_paths(master)))})
        script = self.root / "retained"
        script.write_text("#!/bin/sh\ncatalog=$(sed -n 's/^catalog_path = \"\\(.*\\)\"$/\\1/p' \"$2\")\n"
                          f"/usr/bin/sqlite3 \"$catalog\" \"UPDATE meta SET value='{FLOOR}'\"\n")
        script.chmod(0o700)
        sha = hashlib.sha256(script.read_bytes()).hexdigest()
        with self.assertRaises(ValueError):
            work.normalize(master, self.root / "wrong", script, "0" * 64, ["health"], FLOOR)
        marker = work.normalize(master, self.root / "normalized", script, sha, ["health"], FLOOR)
        self.assertEqual(marker["schema_version"], FLOOR)
        self.assertEqual(marker["lineage"][-1]["schema_before"], 21)
        self.assertEqual(work.schema_version(master / "catalog/catalog.sqlite3"), 21)
        work.checked_master(self.root / "normalized")
        with self.assertRaises(RuntimeError):
            work.normalize(master, self.root / "short", script, sha, ["health"], FLOOR + 1)
        self.assertFalse((self.root / "short").exists())

    def test_probe_workloads_are_refused_before_any_copy(self):
        work.validate_probe_descriptor(probe_descriptor())
        window = {"name": "history", "action": "backfill", "kinds": ["analysis.labels"],
                  "from": "2026-10-28T22:00:00Z", "until": "2026-10-30T22:00:00Z"}
        with self.assertRaisesRegex(ValueError, "whole"):
            work.validate_probe_descriptor(probe_descriptor(phases=[window]))
        for broken in ({"phases": [{**window, "until": "2026-10-01T00:00:00Z"}]},
                       {"phases": [{**window, "action": "recent"}]},
                       {"phases": [{**window, "kinds": ["maintenance.backup"]}]},
                       # Classic authorship drains its whole queue: it cannot run only a window.
                       {"phases": [{"name": "recent", "action": "recent", "kinds": ["analysis.authorship"]}]},
                       {"phases": [{**window, "kinds": ["analysis.labels", "maintenance.labels.query"]}]},
                       {"stages": ["timing"]},
                       {"pending_checks": {"analysis.labels": ["SELECT 1 WHERE 0"]}},
                       {"pending_checks": {kind: ["DELETE FROM messages"] for kind in KINDS}},
                       {"semantic_checks": {}},
                       {"product": {**PRODUCT, "route": "/api/query/other"}},
                       {"product": {**PRODUCT, "min_expected_rows": None}},
                       {"sources": [{"name": "claude", "kind": "claude", "input": "claude"}]}):
            with self.assertRaises(ValueError):
                work.validate_probe_descriptor(probe_descriptor(**broken))
        legacy = probe_descriptor(stages=["timing"], pending_checks={"timing": ["SELECT 1 WHERE 0"]})
        del legacy["phases"]
        work.validate_probe_descriptor(legacy)

    def test_probe_arm_engine_comes_from_what_the_probe_observed(self):
        master = self.master()
        baseline, baseline_sha = self.fake_probe("retained-probe", self.inner())
        with self.assertRaises(ValueError):
            work.probe_run(baseline, baseline_sha, master, self.root / "b", probe_descriptor(), "baseline", "scheduled", baseline_sha)
        report = work.probe_run(baseline, baseline_sha, master, self.root / "b", probe_descriptor(), "baseline", "classic", baseline_sha)
        self.assertTrue(report["settled"])
        self.assertIn("resources", report["process"])
        self.assertEqual((self.root / "b/report.json").stat().st_mode & 0o777, 0o600)
        # A candidate that only claims the scheduled engine is refused and left unsettled.
        claimed, claimed_sha = self.fake_probe("claimed", self.inner("scheduled"))
        with self.assertRaises(RuntimeError):
            work.probe_run(claimed, claimed_sha, master, self.root / "c", probe_descriptor(), "candidate", "scheduled", baseline_sha)
        self.assertFalse(json.loads((self.root / "c/report.json").read_text())["settled"])
        proven = self.inner(engine_actual={"value": "scheduled", "basis": "work-api", "tasks": [{"kind": k} for k in KINDS],
                                           "requests": [{"id": 1, "state": "succeeded", "unfinished": 0},
                                                        {"id": 2, "state": "succeeded", "unfinished": 0}]})
        candidate, candidate_sha = self.fake_probe("candidate", proven)
        report = work.probe_run(candidate, candidate_sha, master, self.root / "d", probe_descriptor(), "candidate", "scheduled", baseline_sha)
        self.assertEqual(report["engine_actual"]["basis"], "work-api")
        failing, failing_sha = self.fake_probe("failing", self.inner(settled=False, failure="labels: boom"), status=1)
        with self.assertRaises(RuntimeError):
            work.probe_run(failing, failing_sha, master, self.root / "e", probe_descriptor(), "candidate", "classic", baseline_sha)
        self.assertIn("boom", json.loads((self.root / "e/report.json").read_text())["failure"]["message"])

    def test_only_a_settled_retained_classic_arm_becomes_a_master(self):
        master = self.master()
        baseline, baseline_sha = self.fake_probe("retained-probe", self.inner(steps=[{"name": "timing"}]))
        work.probe_run(baseline, baseline_sha, master, self.root / "settle", probe_descriptor(), "baseline", "classic", baseline_sha)
        with self.assertRaises(ValueError):
            work.promote(self.root / "settle", self.root / "other", "0" * 64)
        derived = work.promote(self.root / "settle", self.root / "settled", baseline_sha)
        self.assertEqual(derived["lineage"][-1]["step"], "settle")
        self.assertEqual(derived["lineage"][-1]["steps"], ["timing"])
        work.checked_master(self.root / "settled")
        candidate, candidate_sha = self.fake_probe("candidate", self.inner())
        work.probe_run(candidate, candidate_sha, master, self.root / "cand", probe_descriptor(), "candidate", "classic", baseline_sha)
        with self.assertRaises(ValueError):
            work.promote(self.root / "cand", self.root / "from-candidate", baseline_sha)

    def probe_report(self, role="candidate", sha="c" * 64, engine=None, **inner):
        probe = self.inner(**{"expected_sha256": "a" * 64, **inner})
        return {"version": work.VERSION, "kind": "probe", "role": role, "probe_sha256": sha,
                "engine_requested": "scheduled" if role == "candidate" else "classic",
                "engine_actual": engine or ({"value": "scheduled", "basis": "work-api"} if role == "candidate"
                                            else {"value": "classic", "basis": "probe"}),
                "descriptor_sha256": "same", "corpus": {"counts": 1}, "settled": True, "cpu_basis": work.CPU_BASIS,
                "semantic": {"v": {"rows": 1, "sha256": "s", "basis": work.DIGEST_BASIS}},
                "pending": {"p": {"rows": 0, "sha256": "e", "basis": work.DIGEST_BASIS}},
                "process": {"cpu_seconds": 150.0, "resources": {"peak_simultaneous": {"footprint_bytes": 1 << 30},
                            "disk_read_bytes": 2 << 30, "disk_written_bytes": 2 << 30,
                            "host_noise": {"other_cpu_percent_mean": 10.0}},
                            "windows": {"work": {"peak_footprint_bytes": 1 << 30, "disk_read_bytes": 1 << 30,
                                                 "disk_written_bytes": 1 << 30,
                                                 "host_noise": {"other_cpu_percent_mean": 10.0}},
                                        "readiness": {"host_noise": {"other_cpu_percent_mean": 10.0}}}},
                "probe": probe}

    def test_probe_pairs_reject_regressions_and_never_accept(self):
        old = "b" * 64
        baseline = self.probe_report("baseline", old)
        compare = lambda candidate, mode="ab": work.compare_probe(baseline, candidate, mode, old)
        result = compare(self.probe_report())
        self.assertEqual((result["rejected"], result["release_gate"]), (False, "incomplete"))
        self.assertTrue(compare(self.probe_report(t_usable=None))["rejected"])
        self.assertTrue(compare(self.probe_report(t_settled=130.0))["rejected"])
        slow_memory = self.probe_report()
        slow_memory["process"]["windows"]["work"]["peak_footprint_bytes"] = 2 << 30
        self.assertTrue(compare(slow_memory)["rejected"])
        self.assertTrue(compare(self.probe_report(final_product_sha256="d" * 64))["rejected"])
        unproven = self.probe_report(engine={"value": "scheduled", "basis": "environment"})
        self.assertEqual(compare(unproven)["release_gate"], "refused")
        noisy = self.probe_report()
        noisy["process"]["windows"]["work"]["host_noise"]["other_cpu_percent_mean"] = 400.0
        self.assertEqual(compare(noisy)["release_gate"], "refused")
        missing = self.probe_report()
        del missing["process"]["windows"]["work"]["disk_read_bytes"]
        self.assertIn("disk_read_bytes", compare(missing)["missing_metrics"])
        for broken in ({"settled": False}, {"descriptor_sha256": "other"}, {"probe_sha256": old}):
            with self.assertRaises(ValueError):
                compare({**self.probe_report(), **broken})
        with self.assertRaises(ValueError):
            compare(self.probe_report(population={"phases": [{"phase": "recent", "conversations": 2, "sha256": "p"}],
                                                  "kinds": [{"kind": "analysis.labels", "whole": True,
                                                             "conversations": 9, "sha256": "q"}]}))
        unpinned = self.probe_report("baseline", old, expected_sha256="")
        with self.assertRaises(ValueError):
            work.compare_probe(unpinned, self.probe_report(), "ab", old)
        quiet = compare(self.probe_report("baseline", old), "aa")
        self.assertEqual((quiet["release_gate"], quiet["noise"]), ("not-applicable", "within-gate"))

    def test_probe_cpu_allowances_are_reported_and_mismatches_refused(self):
        old = "b" * 64
        baseline = self.probe_report("baseline", old)
        candidate = self.probe_report()
        candidate["probe"]["cpu_policy"].update(requested_slots=2, supported=True, effective_slots=2)
        self.assertEqual(work.compare_probe(baseline, candidate, "ab", old)["release_gate"], "refused")
        baseline["probe"]["cpu_policy"]["requested_slots"] = 2
        self.assertEqual(work.compare_probe(baseline, candidate, "ab", old)["release_gate"], "refused")
        baseline["probe"]["cpu_policy"].update(supported=True, effective_slots=2)
        self.assertFalse(work.compare_probe(baseline, candidate, "ab", old)["blockers"])
        del candidate["probe"]["cpu_policy"]
        self.assertEqual(work.compare_probe(baseline, candidate, "ab", old)["release_gate"], "refused")
        master = self.master()
        binary, sha = self.fake_probe("unsupported", self.inner())
        with self.assertRaisesRegex(RuntimeError, "CPU allowance"):
            work.probe_run(binary, sha, master, self.root / "pin", probe_descriptor(cpu_slots=1), "baseline", "classic", sha)

    def test_probe_destination_is_canonical_before_child_chdir(self):
        master = self.master()
        binary, sha = self.fake_probe("relative", self.inner())
        old_cwd = Path.cwd()
        os.chdir(self.root)
        try:
            work.probe_run(binary, sha, Path("master"), Path("relative-arm"), probe_descriptor(), "baseline", "classic", sha)
            # Also exercise a symlinked parent, like macOS /tmp -> /private/tmp.
            (self.root / "scratch").symlink_to(self.root, target_is_directory=True)
            work.probe_run(binary, sha, master, self.root / "scratch/link-arm", probe_descriptor(), "baseline", "classic", sha)
        finally:
            os.chdir(old_cwd)

    def test_probe_floors_apply_to_either_arm(self):
        old = "b" * 64
        baseline = self.probe_report("baseline", old, t_correct=4.0)
        compare = lambda candidate, mode="ab": work.compare_probe(baseline, candidate, mode, old)
        self.assertTrue(compare(self.probe_report(t_correct=600.0))["metrics"]["t_correct"]["regression"])
        self.assertTrue(compare(self.probe_report(t_correct=600.0))["rejected"])
        self.assertFalse(compare(self.probe_report(t_correct=4.5))["rejected"])
        small = self.probe_report("baseline", old)
        small["process"]["windows"]["work"]["disk_written_bytes"] = 50 << 20
        grown = self.probe_report()
        grown["process"]["windows"]["work"]["disk_written_bytes"] = 5 << 30
        self.assertTrue(work.compare_probe(small, grown, "ab", old)["rejected"])
        idle = self.probe_report("baseline", old, work={"wall_seconds": 100.0, "cpu_seconds": 0.0, "cpu_complete": True})
        busy = self.probe_report(work={"wall_seconds": 100.0, "cpu_seconds": 30.0, "cpu_complete": True})
        self.assertTrue(work.compare_probe(idle, busy, "ab", old)["rejected"])
        # Startup is gated on wall and CPU, from either arm's floor.
        slow_start = self.probe_report(startup={"wall_seconds": 30.0, "cpu_seconds": 0.5, "cpu_complete": True})
        self.assertTrue(compare(slow_start)["rejected"])
        busy_start = self.probe_report(startup={"wall_seconds": 1.0, "cpu_seconds": 20.0, "cpu_complete": True})
        self.assertTrue(compare(busy_start)["rejected"])

    def test_probe_gates_only_execution_window_resources(self):
        old = "b" * 64
        baseline = self.probe_report("baseline", old)
        compare = lambda candidate, mode="ab": work.compare_probe(baseline, candidate, mode, old)
        # Startup and post-settlement checks inflate only the whole process,
        # which is recorded but never gated.
        heavier_outside = self.probe_report()
        heavier_outside["process"].update(cpu_seconds=400.0)
        heavier_outside["process"]["resources"]["disk_written_bytes"] = 20 << 30
        result = compare(heavier_outside)
        self.assertFalse(result["rejected"])
        self.assertEqual(result["whole_process"]["cpu_seconds"], {"baseline": 150.0, "candidate": 400.0})
        # A work-window regression is not diluted by unchanged startup.
        slower = self.probe_report(work={"wall_seconds": 100.0, "cpu_seconds": 140.0, "cpu_complete": True})
        self.assertTrue(compare(slower)["rejected"])
        # A window whose process tree could not all be read is unavailable, not zero.
        partial = self.probe_report(work={"wall_seconds": 100.0, "cpu_seconds": 1.0, "cpu_complete": False})
        result = compare(partial)
        self.assertIn("cpu_seconds", result["missing_metrics"])
        self.assertEqual((result["rejected"], result["release_gate"]), (False, "incomplete"))
        no_window = self.probe_report()
        del no_window["process"]["windows"]["work"]
        self.assertEqual(compare(no_window)["release_gate"], "refused")

    def test_unavailable_or_bursty_noise_refuses_pairs_and_aa(self):
        old = "b" * 64
        baseline = self.probe_report("baseline", old)
        compare = lambda candidate, mode="ab": work.compare_probe(baseline, candidate, mode, old)
        silent = self.probe_report()
        del silent["process"]["windows"]["work"]["host_noise"]
        result = compare(silent)
        self.assertEqual(result["release_gate"], "refused")
        self.assertIn("candidate: host noise during work is unavailable", result["blockers"])
        # A burst while waiting for readiness is not averaged away by a quiet run.
        burst = self.probe_report()
        burst["process"]["windows"]["readiness"]["host_noise"]["other_cpu_percent_mean"] = 400.0
        self.assertEqual(compare(burst)["release_gate"], "refused")
        # A readiness wait under the floor may lack a sample; a longer one may not.
        brief = self.probe_report(windows={"work": [1000.0, 1100.0], "readiness": [1000.0, 1000.5]})
        del brief["process"]["windows"]["readiness"]
        self.assertEqual(compare(brief)["release_gate"], "incomplete")
        long_wait = self.probe_report()
        del long_wait["process"]["windows"]["readiness"]
        self.assertEqual(compare(long_wait)["release_gate"], "refused")
        # A/A measures the noise level, so it is refused before any verdict.
        noisy_aa = self.probe_report("baseline", old)
        noisy_aa["process"]["windows"]["work"]["host_noise"]["other_cpu_percent_mean"] = 400.0
        self.assertEqual(compare(noisy_aa, "aa")["noise"], "refused")
        silent_aa = self.probe_report("baseline", old)
        del silent_aa["process"]["windows"]["work"]["host_noise"]
        self.assertEqual(compare(silent_aa, "aa")["noise"], "refused")
        slow_start = self.probe_report("baseline", old, startup={"wall_seconds": 30.0, "cpu_seconds": 0.5, "cpu_complete": True})
        self.assertEqual(compare(slow_start, "aa")["noise"], "exceeds-gate")
        partial = self.probe_report("baseline", old, startup={"wall_seconds": 1.0, "cpu_seconds": 0.5, "cpu_complete": False})
        self.assertEqual(compare(partial, "aa")["noise"], "incomplete")

    def test_oracle_is_pinned_only_from_a_settled_classic_reference(self):
        workload = probe_descriptor()
        reference = self.probe_report("baseline", "b" * 64, expected_sha256="")
        reference["descriptor_sha256"] = work.digest_of(workload)
        pinned = work.pin_oracle(reference, workload)
        self.assertEqual(pinned["product"]["expected_sha256"], "a" * 64)
        self.assertEqual(pinned["product"]["min_expected_rows"], 4)
        work.validate_probe_descriptor(pinned)
        for broken in ({"role": "candidate"}, {"settled": False}, {"descriptor_sha256": "other"}):
            with self.assertRaises(ValueError):
                work.pin_oracle({**reference, **broken}, workload)
        empty = json.loads(json.dumps(reference))
        empty["probe"]["final_product_rows"] = 0
        with self.assertRaises(ValueError):
            work.pin_oracle(empty, workload)
        with self.assertRaises(ValueError):
            work.pin_oracle(reference, pinned)

    def test_supervised_commands_account_for_and_stop_their_descendants(self):
        script = self.root / "spawner"
        script.write_text("#!/bin/sh\n/bin/sleep 30 &\n/bin/sleep 1.5\n")
        script.chmod(0o700)
        entry = work.supervised([str(script)], {"PATH": "/usr/bin:/bin"}, self.root / "log", 20)
        self.assertEqual(entry["orphaned_descendants"], 1)
        self.assertGreaterEqual(entry["resources"].get("samples", 1), 1)

    def test_window_scopes_memory_disk_and_noise_to_its_span(self):
        sampler = work.TreeSampler(os.getpid())
        sampler.libc = object()  # the samples below stand in for proc_pid_rusage readings
        sampler.series = [(10.0, {"probe": (900, 100, 1000)}, 5.0),        # startup: before the window
                          (11.0, {"probe": (100, 150, 1100)}, 20.0),
                          (12.0, {"probe": (200, 170, 1200), "worker": (300, 40, 7)}, 40.0),
                          (13.0, {"probe": (900, 400, 9000)}, 500.0)]      # post-settlement checks
        window = sampler.window(10.5, 12.5)
        self.assertEqual((window["samples"], window["peak_footprint_bytes"]), (2, 500))
        self.assertEqual((window["disk_read_bytes"], window["disk_written_bytes"]), (70 + 40, 200 + 7))
        self.assertEqual(window["host_noise"]["other_cpu_percent_mean"], 30.0)
        self.assertEqual(sampler.window(12.6, 12.9)["basis"], "unavailable")

    @unittest.skipUnless(sys.platform == "darwin", "process-tree samples need libproc")
    def test_supervised_scopes_samples_to_the_commands_own_windows(self):
        spans = self.root / "spans.json"
        command = self.script("phased", f"s=$(date +%s); /bin/sleep 2.2; e=$(date +%s)\n"
                                        f"printf '{{\"windows\":{{\"work\":[%s,%s]}}}}' $s $e > {spans}\n")
        entry = work.supervised([str(command)], {"PATH": "/usr/bin:/bin"}, self.root / "log", 20,
                                windows=lambda: work._probe_spans(json.loads(spans.read_text())))
        self.assertGreaterEqual(entry["windows"]["work"]["samples"], 1)
        self.assertIn("other_cpu_percent_mean", entry["windows"]["work"]["host_noise"])
        broken = work.supervised([str(command)], {"PATH": "/usr/bin:/bin"}, self.root / "log", 20,
                                 windows=lambda: json.loads("not json"))
        self.assertEqual(broken["windows"], {})
        self.assertIn("windows_error", broken)

    def script(self, name, body):
        path = self.root / name
        path.write_text("#!/bin/sh\n" + body)
        path.chmod(0o700)
        return path

    def test_cpu_is_the_commands_own_and_excludes_observer_siblings(self):
        burn = f"{sys.executable} -c 'import time\nend = time.process_time() + 1.0\nwhile time.process_time() < end: pass'"
        sleeper = self.script("sleeper", "/bin/sleep 1.5\n")
        # A CPU-heavy sibling of the measured command, as the sampler's ps processes are.
        real_table = work.process_table
        def busy_table():
            subprocess.run(["/bin/sh", "-c", burn], check=True)
            return real_table()
        with mock.patch.object(work, "process_table", busy_table):
            entry = work.supervised([str(sleeper)], {"PATH": "/usr/bin:/bin"}, self.root / "log", 20)
        self.assertEqual((entry["exit_code"], entry["cpu_basis"]), (0, work.CPU_BASIS))
        self.assertLess(entry["cpu_seconds"], 0.5)
        # A descendant the command waited for is still the command's CPU.
        worker = self.script("worker", burn + "\nexit 3\n")
        entry = work.supervised([str(worker)], {"PATH": "/usr/bin:/bin"}, self.root / "log", 20)
        self.assertEqual(entry["exit_code"], 3)
        self.assertGreaterEqual(entry["cpu_seconds"], 0.9)

    def test_timed_out_command_is_killed_reaped_and_unsettled(self):
        stuck = self.script("stuck", "/bin/sleep 30\n")
        entry = work.supervised([str(stuck)], {"PATH": "/usr/bin:/bin"}, self.root / "log", 0.5)
        self.assertTrue(entry["timed_out"])
        self.assertIsNone(entry["exit_code"])
        self.assertLess(entry["seconds"], 10)
        self.assertIsInstance(entry["cpu_seconds"], float)
        self.assertNotIn("orphaned_descendants", entry)  # the whole group was killed with it

    def test_arms_with_another_cpu_basis_never_compare(self):
        old = "b" * 64
        baseline = self.probe_report("baseline", old)
        legacy = self.probe_report("baseline", old)
        del legacy["cpu_basis"]  # a prep arm from the RUSAGE_CHILDREN harness
        for first, second, mode in ((legacy, self.probe_report(), "ab"), (baseline, legacy, "aa"),
                                    (baseline, {**self.probe_report(), "cpu_basis": "other"}, "ab")):
            with self.assertRaisesRegex(ValueError, "CPU basis"):
                work.compare_probe(first, second, mode, old)
        workload = probe_descriptor()
        legacy.update(descriptor_sha256=work.digest_of(workload))
        legacy["probe"]["expected_sha256"] = ""
        with self.assertRaisesRegex(ValueError, "CPU basis"):
            work.pin_oracle(legacy, workload)

    def test_candidate_signal_hashes_are_verified(self):
        probe, sha = self.binary("candidate-probe")
        ready = self.root / "ready.json"
        ready.write_text(json.dumps({"version": 1, "candidate_probe": {"path": str(probe), "sha256": sha}}))
        self.assertEqual(work.candidate_check(ready)["checked"]["candidate_probe"]["state"], "verified")
        ready.write_text(json.dumps({"version": 1, "candidate_probe": {"path": str(probe), "sha256": "0" * 64}}))
        with self.assertRaises(ValueError):
            work.candidate_check(ready)
        ready.write_text(json.dumps({"version": 1, "candidate_probe": {"path": str(probe), "sha256": sha},
                                     "baseline_probe": {"path": str(probe), "sha256": sha, "source_commit": "abc"}}))
        with self.assertRaises(ValueError):
            work.candidate_check(ready)
        ready.write_text(json.dumps({"version": 1, "candidate_probe": {"path": str(probe), "sha256": sha},
                                     "baseline_probe": {"path": str(probe), "sha256": sha,
                                                        "source_commit": work.BASELINE_COMMIT}}))
        self.assertEqual(work.candidate_check(ready)["checked"]["baseline_probe"]["state"], "verified")

if __name__ == "__main__":
    unittest.main()
