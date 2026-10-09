"""Safety checks for refreshing a large, writable development catalog."""

from pathlib import Path
import sqlite3
import sys
import tempfile
import tomllib
import unittest

from dev import refresh, snapshot_database, source_configuration, validate_destination


class DevSnapshotTests(unittest.TestCase):
    @unittest.skipUnless(sys.platform == "darwin", "APFS clone integration test")
    def test_refresh_replaces_dev_edits_and_isolates_files_and_configuration(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source, destination = root / "production", root / "dev"
            (source / "catalog").mkdir(parents=True)
            (source / "captures").mkdir()
            (source / "preserved").mkdir()
            (source / "captures/message.txt").write_text("production evidence")
            (source / "hosts").mkdir()
            host_file = source / "hosts/host-air.toml"
            host_file.write_text('# MacBook Air\n[[sources]]\nname = "codex"\nkind = "codex"\npath = "~/.codex"\nenabled = true\n')
            (source / "library.toml").write_text('api_token = "production-token"\n[[sources]]\nname = "live"\npath = "/production/path"\n')
            with sqlite3.connect(source / "catalog/catalog.sqlite3") as db:
                db.execute("CREATE TABLE evidence (message TEXT)")
                db.execute("INSERT INTO evidence VALUES ('original')")
                db.commit()
            first_token = refresh(source, destination, 8798, root / "pharos")
            with (destination / "library.toml").open("rb") as file:
                config = tomllib.load(file)
            self.assertEqual(config["api_token"], first_token)
            self.assertNotEqual(first_token, "production-token")
            self.assertEqual(config["sources"][0]["name"], "live")
            self.assertEqual((destination / "hosts/host-air.toml").read_text(), host_file.read_text())
            self.assertEqual(config["capture_root"], "captures")
            (destination / "captures/message.txt").write_text("dev change")
            self.assertEqual((source / "captures/message.txt").read_text(), "production evidence")
            with sqlite3.connect(destination / "catalog/catalog.sqlite3") as db:
                db.execute("DELETE FROM evidence")
                db.commit()
            second_token = refresh(source, destination, 8798, root / "pharos")
            self.assertNotEqual(first_token, second_token)
            self.assertEqual((destination / "captures/message.txt").read_text(), "production evidence")
            self.assertEqual((destination / "hosts/host-air.toml").read_text(), host_file.read_text())
            with sqlite3.connect(destination / "catalog/catalog.sqlite3") as db:
                self.assertEqual(db.execute("SELECT message FROM evidence").fetchone()[0], "original")

    def test_library_local_source_paths_are_relocated(self):
        source = Path("/Volumes/euclid/Pharos")
        destination = Path("/Volumes/euclid/pharos-dev")
        text = '[[sources]]\nname = "exports"\npath = "/Volumes/euclid/Pharos/preserved/export"\n'
        parsed = tomllib.loads(source_configuration(text, source, destination))
        self.assertEqual(parsed["sources"][0]["path"], str(destination / "preserved/export"))
        text = '[[sources]]\npath = "~/.codex"\n'
        self.assertEqual(source_configuration(text, source, destination), text)

    def test_snapshot_includes_wal_and_changes_only_copy(self):
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory) / "production.sqlite3"
            destination = Path(directory) / "dev.sqlite3"
            with sqlite3.connect(source) as production:
                production.execute("PRAGMA journal_mode=WAL")
                production.execute("CREATE TABLE evidence (message TEXT)")
                production.execute("CREATE TABLE sync_settings (enabled INTEGER)")
                production.execute("INSERT INTO evidence VALUES ('committed in WAL')")
                production.execute("INSERT INTO sync_settings VALUES (1)")
                production.commit()
                self.assertTrue(Path(str(source) + "-wal").stat().st_size > 0)
                snapshot_database(source, destination)
                with sqlite3.connect(destination) as dev:
                    self.assertEqual(dev.execute("SELECT message FROM evidence").fetchone()[0], "committed in WAL")
                    self.assertEqual(dev.execute("SELECT enabled FROM sync_settings").fetchone()[0], 0)
                    dev.execute("DELETE FROM evidence")
                    dev.commit()
                self.assertEqual(production.execute("SELECT count(*) FROM evidence").fetchone()[0], 1)
                self.assertEqual(production.execute("SELECT enabled FROM sync_settings").fetchone()[0], 1)

    def test_rejects_production_overlap_and_unowned_destination(self):
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory) / "production"
            source.mkdir()
            for destination in (source, source / "dev", source.parent):
                with self.assertRaises(RuntimeError):
                    validate_destination(source, destination)
            destination = Path(directory) / "dev"
            destination.mkdir()
            with self.assertRaises(RuntimeError):
                validate_destination(source, destination)
            (destination / ".pharos-dev").touch()
            validate_destination(source, destination)
            link = Path(directory) / "link"
            link.symlink_to(destination, target_is_directory=True)
            with self.assertRaises(RuntimeError):
                validate_destination(source, link)


if __name__ == "__main__":
    unittest.main()
