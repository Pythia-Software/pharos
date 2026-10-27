"""Feed behavior tests; no GitHub, Keychain, app builds or downloads."""
import copy
import importlib.util
import plistlib
from pathlib import Path
import tempfile
import sys
import unittest
from unittest.mock import patch

SOURCE = Path(__file__).resolve().parents[1] / 'macos/update-feed.py'
spec = importlib.util.spec_from_file_location('update_feed', SOURCE)
feed = importlib.util.module_from_spec(spec)
sys.path.insert(0, str(SOURCE.parent))
try:
    spec.loader.exec_module(feed)
finally:
    sys.path.pop(0)


def manifest(number='0.7.2'):
    return dict(version=number, url=feed.image_url(number), length=42,
                signature='A' * 86 + '==', sha256='a' * 64,
                minimum_system_version='14.0.0', notes='Fix <script> & data',
                published_at='Fri, 23 Oct 2026 12:00:00 +0000')


class UpdateFeedTests(unittest.TestCase):
    def test_missing_tool_authorization_stops_before_private_key_lookup(self):
        with patch.object(feed, 'require_release_machine'), \
             patch.object(feed, 'run', return_value='/test/Sparkle') as command, \
             patch.object(feed, 'check_sparkle_access', side_effect=ValueError('authorization required')):
            with self.assertRaisesRegex(ValueError, 'authorization required'):
                feed.config_and_tools()
            command.assert_called_once_with(feed.ROOT / 'macos/fetch-sparkle.sh')

    def test_development_machine_never_accesses_keychain(self):
        with patch.object(feed, 'require_release_machine', side_effect=ValueError('deployment only')), patch.object(feed, 'run') as command:
            with self.assertRaisesRegex(ValueError, 'deployment only'):
                feed.config_and_tools()
            command.assert_not_called()

    def test_release_machine_requires_matching_public_configuration(self):
        from release_support import require_release_machine
        import json
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / 'machine.json'
            config = {'public_key': 'A' * 43 + '='}
            with self.assertRaisesRegex(ValueError, 'disabled on this Mac'):
                require_release_machine(config, path)
            path.write_text(json.dumps(dict(enabled=True, public_key='different')))
            with self.assertRaisesRegex(ValueError, 'not configured'):
                require_release_machine(config, path)
            path.write_text(json.dumps(dict(enabled=True, public_key=config['public_key'])))
            require_release_machine(config, path)

    def test_versions_and_archives_are_validated(self):
        good = manifest()
        feed.validate_manifest(good)
        for key, value in [('version', 'latest'), ('url', 'http://example.com/app.dmg'),
                           ('url', feed.image_url('0.7.1')), ('length', 0), ('length', True),
                           ('signature', 'unsigned'), ('sha256', 'missing'),
                           ('minimum_system_version', '14'), ('minimum_system_version', 'future')]:
            with self.subTest(key=key, value=value), self.assertRaises((ValueError, TypeError)):
                feed.validate_manifest(dict(good, **{key: value}))

    def test_image_compatibility_comes_from_final_bundle_and_always_detaches(self):
        for minimum, expected in [('14', '14.0.0'), ('15.2', '15.2.0'), ('16.1.3', '16.1.3'),
                                  ('future', None), (None, None)]:
            calls = []
            def command(*args):
                calls.append(args)
                if args[1] == 'attach':
                    path = Path(args[-2]) / 'Pharos.app/Contents/Info.plist'
                    path.parent.mkdir(parents=True)
                    info = {'CFBundleVersion': '0.7.2'}
                    if minimum is not None:
                        info['LSMinimumSystemVersion'] = minimum
                    path.write_bytes(plistlib.dumps(info))
            with self.subTest(minimum=minimum), patch.object(feed, 'run', side_effect=command):
                if expected is None:
                    with self.assertRaises(ValueError):
                        feed.image_bundle_metadata(Path('final.dmg'), '0.7.2')
                else:
                    self.assertEqual(feed.image_bundle_metadata(Path('final.dmg'), '0.7.2'), expected)
                    with self.assertRaisesRegex(ValueError, 'release version'):
                        feed.image_bundle_metadata(Path('final.dmg'), '0.7.3')
                self.assertEqual(calls[-1][1], 'detach')
                self.assertIn('-readonly', calls[0])
        feed.validate_manifest(dict(manifest(), minimum_system_version='16.1.0'))

    def test_newer_releases_preserve_history_and_escape_notes(self):
        tree = feed.empty_feed()
        channel = tree.find('channel')
        feed.add_item(channel, manifest('0.7.2'))
        feed.add_item(channel, manifest('0.7.10'))
        self.assertEqual([item.findtext(f'{{{feed.SPARKLE}}}version') for item in channel.findall('item')], ['0.7.10', '0.7.2'])
        description = channel.find('item/description').text
        self.assertNotIn('<script>', description)
        self.assertIn('&lt;script&gt;', description)
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / 'appcast.xml'
            tree.write(path)
            self.assertEqual(len(feed.load_feed(path).findall('channel/item')), 2)
        for number in ('0.7.2', '0.7.9', '0.6.0'):
            with self.subTest(number=number), self.assertRaises(ValueError):
                feed.add_item(channel, manifest(number))

    def test_feed_rejects_wrong_origin_and_duplicate_versions(self):
        tree = feed.empty_feed()
        channel = tree.find('channel')
        feed.add_item(channel, manifest())
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / 'appcast.xml'
            channel.append(copy.deepcopy(channel.find('item')))
            tree.write(path)
            with self.assertRaises(ValueError):
                feed.load_feed(path)
            channel.remove(channel.findall('item')[1])
            channel.find('item/enclosure').set('url', 'https://example.com/app.dmg')
            tree.write(path)
            with self.assertRaises(ValueError):
                feed.load_feed(path)

    def test_publishing_refuses_drafts_before_downloading_or_writing(self):
        import argparse
        for field in ('isDraft', 'isPrerelease'):
            with self.subTest(field=field), tempfile.TemporaryDirectory() as temporary:
                args = argparse.Namespace(version='0.7.2', directory=Path(temporary))
                import json
                status = dict(isDraft=False, isPrerelease=False)
                status[field] = True
                with patch.object(feed, 'run', return_value=json.dumps(status)) as command:
                    with self.assertRaises(ValueError):
                        feed.publish(args, {}, Path(temporary))
                    self.assertEqual(command.call_count, 1)

    def test_publishing_checks_final_bundle_before_advertising(self):
        import argparse
        import json
        from types import SimpleNamespace
        with tempfile.TemporaryDirectory() as temporary:
            args = argparse.Namespace(version='0.7.2', directory=Path(temporary))
            def command(*arguments):
                if arguments[:3] == ('gh', 'release', 'view'):
                    return json.dumps(dict(isDraft=False, isPrerelease=False))
                if arguments[:3] == ('gh', 'release', 'download'):
                    directory = Path(arguments[-1])
                    image = directory / feed.image_name(args.version)
                    image.write_bytes(b'final stapled artifact')
                    metadata = dict(manifest(), length=image.stat().st_size,
                                    sha256=feed.file_sha256(image))
                    (directory / 'update.json').write_text(json.dumps(metadata))
            with patch.object(feed, 'run', side_effect=command), \
                 patch.object(feed, 'verify_signature'), \
                 patch.object(feed.subprocess, 'run', return_value=SimpleNamespace(stderr='Authority=Developer ID Application: Test')), \
                 patch.object(feed, 'image_bundle_metadata', return_value='15.0.0'), \
                 patch.object(feed, 'current_feed') as current:
                with self.assertRaisesRegex(ValueError, 'compatibility metadata differs'):
                    feed.publish(args, {}, Path(temporary))
                current.assert_not_called()


if __name__ == '__main__':
    unittest.main()
