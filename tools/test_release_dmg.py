"""DMG metadata and failure contracts; native images are explicitly opt-in."""
from contextlib import contextmanager, nullcontext
import ctypes
import datetime
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import plistlib
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
SOURCE = ROOT / 'macos/build_dmg.py'
spec = importlib.util.spec_from_file_location('release_dmg', SOURCE)
dmg = importlib.util.module_from_spec(spec)
spec.loader.exec_module(dmg)


def synthetic_alias():
    from mac_alias import VolumeInfo, TargetInfo
    date = datetime.datetime(2026, 1, 1, tzinfo=datetime.timezone.utc)
    return dmg.Alias(volume=VolumeInfo('v0.0.1 Pharos', date, b'H+', 0, 0, b'\0\0'),
                     target=TargetInfo(0, '.background.tiff', 2, 17, date, b'\0' * 4, b'\0' * 4))


def fixture_app(root):
    app = root / 'source app/Pharos.app'
    (app / 'Contents/MacOS').mkdir(parents=True)
    (app / 'Contents/Info.plist').write_bytes(plistlib.dumps({
        'CFBundleVersion': '0.0.1', 'CFBundleIdentifier': 'test.pharos.dmg',
        'CFBundleExecutable': 'pharos', 'CFBundlePackageType': 'APPL',
        'LSMinimumSystemVersion': '14.0',
    }))
    (app / 'Contents/MacOS/pharos').write_bytes(b'test executable')
    os.symlink('pharos', app / 'Contents/MacOS/link')
    return app


class PackagingFixture:
    """Execute real copying/format code with fake image commands and mounts."""
    def __init__(self, root, fail=None):
        self.root, self.fail, self.calls, self.mounts = root, fail, [], []
        self.app = fixture_app(root)
        self.background = root / 'background.tiff'
        self.background.write_bytes(b'test TIFF placeholder')
        self.output = root / 'release image.dmg'
        self.contents = root / 'contents'
        self.contents.mkdir()

    def run(self, *args, capture=False):
        args = tuple(str(a) for a in args)
        self.calls.append(args)
        if self.fail and self.fail(args):
            raise subprocess.CalledProcessError(1, args)
        if args[0] == '/usr/bin/ditto':
            shutil.copytree(args[1], args[2], symlinks=True)
        elif args[1] == 'create':
            Path(args[-1]).write_bytes(b'writable image')
        elif args[1] == 'convert':
            Path(args[-1]).write_bytes(b'compressed image')

    @contextmanager
    def mounted(self, image, mount, readonly=False):
        self.mounts.append(readonly)
        try:
            yield self.contents
        finally:
            self.mounts.append('detached')

    def build(self):
        with patch.object(dmg, 'run', side_effect=self.run), \
             patch.object(dmg, 'mounted_image', side_effect=self.mounted), \
             patch.object(dmg.Alias, 'for_file', return_value=synthetic_alias()), \
             patch.object(dmg.os, 'sync'):
            dmg.build(self.app, self.output, '0.0.1', self.background)


class ReleaseDMGTests(unittest.TestCase):
    def test_vendored_files_match_reviewed_sources(self):
        vendor = ROOT / 'macos/vendor'
        for name, source in json.loads((vendor / 'sources.json').read_text()).items():
            base = vendor if name == 'dmgbuild' else vendor / name
            for filename, digest in source['files'].items():
                with self.subTest(package=name, file=filename):
                    self.assertEqual(hashlib.sha256((base / filename).read_bytes()).hexdigest(), digest)
        self.assertEqual(Path(sys.modules['ds_store'].__file__).parent, vendor / 'ds_store')
        self.assertEqual(Path(sys.modules['mac_alias'].__file__).parent, vendor / 'mac_alias')

    def test_finder_metadata_survives_writing_and_reopening(self):
        with tempfile.TemporaryDirectory() as directory:
            mount = Path(directory)
            alias = synthetic_alias()
            with patch.object(dmg.Alias, 'for_file', return_value=alias) as make_alias:
                dmg.write_layout(mount)
            make_alias.assert_called_once_with(str(mount / '.background.tiff'))
            with dmg.DSStore.open(str(mount / '.DS_Store'), 'r') as store:
                settings = store['.']['icvp']
                self.assertEqual(store['.']['icvl'], (b'type', b'icnv'))
                self.assertEqual(store['.']['bwsp']['WindowBounds'], '{{160, 360}, {760, 468}}')
                self.assertFalse(store['.']['bwsp']['ShowTabView'])
                self.assertEqual(settings['backgroundType'], 2)
                self.assertEqual(settings['arrangeBy'], 'none')
                self.assertEqual((settings['iconSize'], settings['textSize']), (72, 12))
                parsed = dmg.Alias.from_bytes(settings['backgroundImageAlias'])
                self.assertEqual(parsed.target.cnid, 17)
                self.assertEqual(parsed.target.filename, '.background.tiff')
                self.assertEqual(store['Pharos.app']['Iloc'], (87, 180))
                self.assertEqual(store['Applications']['Iloc'], (210, 180))

    def test_packaging_verifies_copied_and_final_app_before_publishing(self):
        with tempfile.TemporaryDirectory() as directory:
            fixture = PackagingFixture(Path(directory))
            fixture.build()
            self.assertEqual(fixture.output.read_bytes(), b'compressed image')
            self.assertEqual(fixture.mounts, [False, 'detached', True, 'detached'])
            signatures = [c[-1] for c in fixture.calls if c[0] == '/usr/bin/codesign']
            self.assertEqual(signatures, [str(fixture.app.resolve()), str(fixture.contents / 'Pharos.app'),
                                          str(fixture.contents / 'Pharos.app')])
            self.assertEqual(os.readlink(fixture.contents / 'Pharos.app/Contents/MacOS/link'), 'pharos')
            self.assertEqual(os.readlink(fixture.contents / 'Applications'), '/Applications')
            self.assertFalse(list(Path(directory).glob('.pharos-dmg-*')))

    def test_failures_never_leave_a_final_image_or_temporary_files(self):
        for command in ('create', 'copy', 'signature', 'layout', 'resize', 'convert', 'verify', 'final signature'):
            with self.subTest(command=command), tempfile.TemporaryDirectory() as directory:
                signatures = 0
                def fail(args):
                    nonlocal signatures
                    if args[0] == '/usr/bin/codesign':
                        signatures += 1
                        return (command == 'signature' and signatures == 2) or (command == 'final signature' and signatures == 3)
                    return ((args[0] == '/usr/bin/ditto' and command == 'copy') or
                            (args[0] == '/usr/bin/hdiutil' and args[1] == command))
                fixture = PackagingFixture(Path(directory), fail=fail)
                with patch.object(dmg, 'write_layout', side_effect=ValueError('bad alias')) if command == 'layout' else nullcontext():
                    with self.assertRaises((ValueError, subprocess.CalledProcessError)):
                        fixture.build()
                self.assertFalse(fixture.output.exists())
                self.assertFalse(list(Path(directory).glob('.pharos-dmg-*')))
                if False in fixture.mounts:
                    self.assertEqual(fixture.mounts[-1], 'detached')

    def test_output_is_never_overwritten_even_if_created_during_build(self):
        with tempfile.TemporaryDirectory() as directory:
            fixture = PackagingFixture(Path(directory))
            original = fixture.run
            def competing_output(*args, **kwargs):
                result = original(*args, **kwargs)
                if args[0] == '/usr/bin/hdiutil' and args[1] == 'verify':
                    fixture.output.write_bytes(b'other build')
                return result
            fixture.run = competing_output
            with self.assertRaises(FileExistsError):
                fixture.build()
            self.assertEqual(fixture.output.read_bytes(), b'other build')

    def test_invalid_inputs_stop_before_mounting(self):
        for invalid in ('version', 'existing', 'dangling', 'volume', 'app version'):
            with self.subTest(invalid=invalid), tempfile.TemporaryDirectory() as directory:
                fixture = PackagingFixture(Path(directory))
                version = 'bad/version' if invalid == 'version' else '0.0.1'
                if invalid == 'existing':
                    fixture.output.write_bytes(b'keep me')
                if invalid == 'dangling':
                    fixture.output.symlink_to('missing')
                if invalid == 'app version':
                    (fixture.app / 'Contents/Info.plist').write_bytes(plistlib.dumps({'CFBundleVersion': '0.0.2'}))
                original = os.path.lexists
                with patch.object(dmg, 'run') as run, \
                     patch.object(dmg.os.path, 'lexists', side_effect=lambda p: (str(p).startswith('/Volumes/') and invalid == 'volume') or original(p)):
                    with self.assertRaises(ValueError):
                        dmg.build(fixture.app, fixture.output, version, fixture.background)
                    self.assertFalse(any(c.args[0] == '/usr/bin/hdiutil' for c in run.call_args_list))

    def test_mount_always_detaches_on_error_or_interruption(self):
        output = plistlib.dumps({'system-entities': [
            {'dev-entry': '/dev/disk123'}, {'dev-entry': '/dev/disk123s1', 'mount-point': '/Volumes/test'},
        ]})
        for failure in (ValueError('copy failed'), KeyboardInterrupt(), SystemExit(143)):
            with self.subTest(failure=type(failure).__name__), patch.object(dmg, 'run', return_value=output), \
                 patch.object(dmg, 'detach') as detach:
                with self.assertRaises(type(failure)):
                    with dmg.mounted_image(Path('/tmp/image.dmg'), Path('/Volumes/test')):
                        raise failure
                detach.assert_called_once_with('/dev/disk123')

    def test_failed_attach_or_wrong_mount_is_cleaned_up(self):
        info = plistlib.dumps({'images': [{'image-path': str(Path('image.dmg').resolve()),
                                          'system-entities': [{'dev-entry': '/dev/disk123'}]}]})
        with patch.object(dmg, 'run', side_effect=[subprocess.CalledProcessError(1, ['attach']), info]), \
             patch.object(dmg, 'detach') as detach:
            with self.assertRaises(subprocess.CalledProcessError):
                with dmg.mounted_image('image.dmg', Path('/Volumes/test')):
                    self.fail('failed attach yielded')
            detach.assert_called_once_with('/dev/disk123')
        output = plistlib.dumps({'system-entities': [{'dev-entry': '/dev/disk123'}]})
        with patch.object(dmg, 'run', return_value=output), patch.object(dmg, 'detach') as detach:
            with self.assertRaises(ValueError):
                with dmg.mounted_image('image.dmg', Path('/Volumes/test')):
                    self.fail('unexpected mount yielded')
            detach.assert_called_once_with('/dev/disk123')

    def test_failed_attach_never_detaches_an_unrelated_image(self):
        unrelated = plistlib.dumps({'images': [{'image-path': '/other/image.dmg',
            'system-entities': [{'dev-entry': '/dev/disk456', 'mount-point': '/Volumes/test'}]}]})
        for result, failure in ((subprocess.CalledProcessError(1, ['attach']), subprocess.CalledProcessError),
                                (b'invalid plist', ValueError)):
            with self.subTest(failure=failure), patch.object(dmg, 'run', side_effect=[result, unrelated]), \
                 patch.object(dmg, 'detach') as detach:
                with self.assertRaises(failure):
                    with dmg.mounted_image('image.dmg', '/Volumes/test'):
                        self.fail('failed attach yielded')
                detach.assert_not_called()

    def test_uncertain_cleanup_retains_backing_image(self):
        for response in (plistlib.dumps({}), b'invalid plist'):
            with self.subTest(response=response), patch.object(dmg, 'run', return_value=response):
                with self.assertRaises(dmg.ImageCleanupError):
                    dmg.attached_device('image.dmg')
        with tempfile.TemporaryDirectory() as directory:
            fixture = PackagingFixture(Path(directory))
            with patch.object(fixture, 'mounted', side_effect=dmg.ImageCleanupError('still attached')):
                with self.assertRaises(dmg.ImageCleanupError):
                    fixture.build()
            self.assertFalse(fixture.output.exists())
            retained = list(Path(directory).glob('.pharos-dmg-*'))
            self.assertEqual(len(retained), 1)
            self.assertTrue((retained[0] / 'layout.dmg').exists())

    def test_unsupported_python_fails_preflight(self):
        with patch.object(dmg.sys, 'platform', 'darwin'), patch.object(dmg.sys, 'version_info', (3, 9)):
            with self.assertRaisesRegex(ValueError, 'Python 3.10'):
                dmg.preflight()

    def test_busy_detach_retries_but_forced_detach_is_a_build_failure(self):
        error = subprocess.CalledProcessError(1, ['detach'])
        for outcomes, expected in (([error, None], None), ([error] * 5 + [None], subprocess.CalledProcessError),
                                   ([error] * 6, RuntimeError)):
            with self.subTest(outcomes=len(outcomes)), patch.object(dmg, 'run', side_effect=outcomes) as run, \
                 patch.object(dmg.time, 'sleep'):
                if expected:
                    with self.assertRaises(expected):
                        dmg.detach('/dev/disk123')
                    self.assertIn('-force', run.call_args.args)
                else:
                    dmg.detach('/dev/disk123')
                    self.assertEqual(run.call_count, 2)

    @unittest.skipUnless(sys.platform == 'darwin' and os.environ.get('PHAROS_DMG_TESTS') == '1',
                         'explicit macOS disk-image integration test')
    def test_native_image_preserves_signed_app_and_resolvable_background(self):
        with tempfile.TemporaryDirectory(prefix='pharos dmg test ') as directory:
            root = Path(directory)
            app = fixture_app(root)
            shutil.copyfile('/usr/bin/true', app / 'Contents/MacOS/pharos')
            (app / 'Contents/MacOS/pharos').chmod(0o755)
            subprocess.run(['/usr/bin/codesign', '--force', '--sign', '-', '--timestamp=none', str(app)], check=True)
            image = root / 'Pharos installer.dmg'
            subprocess.run(['/bin/sh', str(ROOT / 'macos/make-release-dmg.sh'), str(app), str(image), '0.0.1'], check=True)
            # The ordinary build removes its writable image and background
            # source. Verify a fresh mount with a completely different path.
            with dmg.mounted_image(image, root / 'fresh mount', readonly=True) as mount:
                dmg.verify_payload(mount, '0.0.1')
                self.assertEqual((mount / 'Pharos.app/Contents/MacOS/pharos').read_bytes(),
                                 (app / 'Contents/MacOS/pharos').read_bytes())
                with dmg.DSStore.open(str(mount / '.DS_Store'), 'r') as store:
                    alias = store['.']['icvp']['backgroundImageAlias']
                self.assertEqual(resolve_alias(alias).resolve(), (mount / '.background.tiff').resolve())
                info = plistlib.loads(subprocess.check_output(['/usr/bin/hdiutil', 'imageinfo', '-plist', str(image)]))
                self.assertEqual(info['Format'], 'ULMO')
                representations = subprocess.check_output(['/usr/bin/tiffutil', '-info', str(mount / '.background.tiff')], stderr=subprocess.STDOUT)
                self.assertIn(b'760', representations)
                self.assertIn(b'1520', representations)


def resolve_alias(data):
    """Ask macOS itself to resolve the imported Alias, without UI or mounting."""
    cf = ctypes.CDLL('/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation')
    pointer = ctypes.c_void_p
    signatures = {
        'CFDataCreate': (pointer, [pointer, ctypes.c_char_p, ctypes.c_long]),
        'CFURLCreateBookmarkDataFromAliasRecord': (pointer, [pointer, pointer]),
        'CFURLCreateByResolvingBookmarkData': (pointer, [pointer, pointer, ctypes.c_ulong, pointer, pointer, pointer, pointer]),
        'CFURLCopyFileSystemPath': (pointer, [pointer, ctypes.c_int]),
        'CFStringGetCString': (ctypes.c_bool, [pointer, pointer, ctypes.c_long, ctypes.c_uint32]),
        'CFRelease': (None, [pointer]),
    }
    for name, (result, arguments) in signatures.items():
        function = getattr(cf, name)
        function.restype, function.argtypes = result, arguments
    objects = []
    try:
        alias = cf.CFDataCreate(None, data, len(data)); objects.append(alias)
        bookmark = cf.CFURLCreateBookmarkDataFromAliasRecord(None, alias); objects.append(bookmark)
        if not bookmark:
            raise AssertionError('macOS rejected the background Alias')
        url = cf.CFURLCreateByResolvingBookmarkData(None, bookmark, (1 << 8) | (1 << 9), None, None, None, None)
        objects.append(url)
        if not url:
            raise AssertionError('macOS could not resolve the remounted background Alias')
        path = cf.CFURLCopyFileSystemPath(url, 0); objects.append(path)
        buffer = ctypes.create_string_buffer(4096)
        if not cf.CFStringGetCString(path, buffer, len(buffer), 0x08000100):
            raise AssertionError('macOS could not decode the background path')
        return Path(buffer.value.decode('utf-8'))
    finally:
        for object_ in reversed(objects):
            if object_:
                cf.CFRelease(object_)


if __name__ == '__main__':
    unittest.main()
