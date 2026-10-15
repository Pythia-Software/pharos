"""Safety checks for refreshing a large, writable development catalog."""

import contextlib
import io
import json
from pathlib import Path
import sqlite3
import sys
import tempfile
import tomllib
import unittest
from unittest import mock

import dev

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


class DevRestartTests(unittest.TestCase):
    def test_reuse_keeps_config_and_catalog(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve()
            source, destination, binary = root / "production", root / "dev", root / "pharos"
            destination.mkdir()
            (destination / dev.MARKER).write_text(str(source))
            (destination / "catalog").mkdir()
            catalog = destination / "catalog/catalog.sqlite3"
            catalog.write_bytes(b"unchanged catalog")
            config = destination / "library.toml"
            original = f'library = true\nport = 55070\nhost = "127.0.0.1"\nexecutable = "{binary}"\napi_token = "test"\n'
            config.write_text(original)
            with contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(dev.reuse_library(source, destination, 55070, binary), "test")
            self.assertEqual(config.read_text(), original)
            self.assertEqual(catalog.read_bytes(), b"unchanged catalog")
            with self.assertRaisesRegex(RuntimeError, "different production source"):
                dev.reuse_library(root / "another", destination, 55070, binary)
            config.write_text(original + f'catalog_path = "{source}/catalog.sqlite3"\n')
            with self.assertRaisesRegex(RuntimeError, "outside its isolated directory"):
                dev.reuse_library(source, destination, 55070, binary)

    def test_projection_wait_does_not_open_ui_early(self):
        server = mock.Mock()
        server.poll.return_value = None
        statuses = [
            {"projections_ready": False, "library_pending": 10, "metrics_pending": 10, "text_search_ready": False},
            {"projections_ready": False, "library_pending": 0, "metrics_pending": 0, "text_search_ready": False},
            {"projections_ready": True},
        ]
        responses = [io.BytesIO(json.dumps(status).encode()) for status in statuses]
        with mock.patch.object(dev.urllib.request, "urlopen", side_effect=responses) as request, \
                mock.patch.object(dev.time, "sleep") as sleep, \
                contextlib.redirect_stdout(io.StringIO()):
            dev.wait_for_projections(server, 55070, "test")
        self.assertEqual(request.call_count, 3)
        self.assertEqual(sleep.call_count, 2)
        self.assertEqual(request.call_args.args[0].full_url, "http://127.0.0.1:55070/api/ready?projections=1")

    def test_projection_wait_retries_server_errors(self):
        server = mock.Mock()
        server.poll.return_value = None
        failure = dev.urllib.error.HTTPError("http://localhost", 500, "Rebuilding", {}, None)
        ready = io.BytesIO(json.dumps({"projections_ready": True}).encode())
        with mock.patch.object(dev.urllib.request, "urlopen", side_effect=[failure, ready]) as request, \
                mock.patch.object(dev.time, "sleep") as sleep, \
                contextlib.redirect_stdout(io.StringIO()):
            dev.wait_for_projections(server, 55070, "test")
        self.assertEqual(request.call_count, 2)
        sleep.assert_called_once_with(1)

    def test_projection_wait_rejects_client_errors(self):
        server = mock.Mock()
        server.poll.return_value = None
        failure = dev.urllib.error.HTTPError("http://localhost", 401, "Unauthorized", {}, None)
        with mock.patch.object(dev.urllib.request, "urlopen", side_effect=failure), \
                mock.patch.object(dev.time, "sleep") as sleep, \
                self.assertRaises(dev.urllib.error.HTTPError):
            dev.wait_for_projections(server, 55070, "test")
        sleep.assert_not_called()

    def test_projection_wait_reports_backend_exit(self):
        server = mock.Mock()
        server.poll.return_value = 2
        server.returncode = 2
        with contextlib.redirect_stdout(io.StringIO()), \
                self.assertRaisesRegex(RuntimeError, "Backend exited with status 2"):
            dev.wait_for_projections(server, 55070, "test")


if __name__ == "__main__":
    unittest.main()
