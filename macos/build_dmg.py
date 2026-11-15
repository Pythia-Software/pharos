"""Pharos's Finder disk image builder; uses only macOS tools and vendored Python.

The Finder records follow dmgbuild 1.6.7 (MIT); see vendor/dmgbuild-LICENSE.
Keep the format libraries intact; provenance and licenses live in vendor/.
"""
import argparse
from contextlib import contextmanager
import os
from pathlib import Path
import plistlib
import re
import shutil
import signal
import subprocess
import sys
import tempfile
import time

sys.path.insert(0, str(Path(__file__).resolve().parent / 'vendor'))
from ds_store import DSStore
from mac_alias import Alias


ICON_LOCATIONS = {
    'Pharos.app': (87, 180),
    'Applications': (210, 180),
    '.background.tiff': (1100, 100),
    '.DS_Store': (1100, 100),
    '.fseventsd': (1100, 100),
}


class ImageCleanupError(RuntimeError):
    """Keep the backing image available when attachment cleanup is uncertain."""


def run(*arguments, capture=False):
    return subprocess.run([str(arg) for arg in arguments], check=True,
                          stdout=subprocess.PIPE if capture else None).stdout


def preflight():
    if sys.platform != 'darwin':
        raise ValueError('DMG packaging requires macOS.')
    if sys.version_info < (3, 10):
        raise ValueError('DMG packaging requires Python 3.10 or newer on PATH.')
    for name in ('hdiutil', 'ditto', 'codesign'):
        if not Path('/usr/bin', name).is_file() and not Path('/bin', name).is_file():
            raise ValueError(f'{name} is required for DMG packaging.')


def window_settings():
    # Finder coordinates run from the bottom of the screen. The title bar
    # sits above the 760 x 440-point guide; all values match the old image.
    return {
        'ShowStatusBar': False, 'WindowBounds': '{{160, 360}, {760, 468}}',
        'ContainerShowSidebar': False, 'PreviewPaneVisibility': False,
        'SidebarWidth': 180, 'ShowTabView': False, 'ShowToolbar': False,
        'ShowPathbar': False, 'ShowSidebar': False,
    }


def icon_settings(alias):
    return {
        'viewOptionsVersion': 1, 'backgroundType': 2,
        'backgroundColorRed': 1.0, 'backgroundColorGreen': 1.0,
        'backgroundColorBlue': 1.0, 'backgroundImageAlias': alias,
        'gridOffsetX': 0.0, 'gridOffsetY': 0.0, 'gridSpacing': 100.0,
        'arrangeBy': 'none', 'showIconPreview': False, 'showItemInfo': False,
        'labelOnBottom': True, 'textSize': 12.0, 'iconSize': 72.0,
        'scrollPositionX': 0.0, 'scrollPositionY': 0.0,
    }


def write_layout(mount):
    # Generate this against the real mounted filesystem, after the background
    # has been copied. Reusing Alias bytes from another image loses identity.
    alias = Alias.for_file(str(mount / '.background.tiff')).to_bytes()
    with DSStore.open(str(mount / '.DS_Store'), 'w+') as store:
        store['.']['vSrn'] = ('long', 1)
        store['.']['bwsp'] = window_settings()
        store['.']['icvp'] = icon_settings(alias)
        store['.']['icvl'] = ('type', b'icnv')
        for name, position in ICON_LOCATIONS.items():
            store[name]['Iloc'] = position


def verify_payload(mount, version):
    app = mount / 'Pharos.app'
    run('/usr/bin/codesign', '--verify', '--strict', '--deep', app)
    with (app / 'Contents/Info.plist').open('rb') as source:
        if plistlib.load(source).get('CFBundleVersion') != version:
            raise ValueError('The app inside the DMG has the wrong release version.')
    if not (mount / 'Applications').is_symlink() or os.readlink(mount / 'Applications') != '/Applications':
        raise ValueError('The DMG Applications shortcut is invalid.')
    if not (mount / '.background.tiff').is_file():
        raise ValueError('The DMG background is missing.')
    with DSStore.open(str(mount / '.DS_Store'), 'r') as store:
        if store['.']['bwsp'] != window_settings() or store['.']['icvl'] != (b'type', b'icnv'):
            raise ValueError('The DMG Finder window settings are invalid.')
        settings = store['.']['icvp']
        alias = settings.get('backgroundImageAlias', b'')
        if settings != icon_settings(alias) or not alias:
            raise ValueError('The DMG Finder background settings are invalid.')
        Alias.from_bytes(alias)
        for name, position in ICON_LOCATIONS.items():
            if store[name]['Iloc'] != position:
                raise ValueError(f'The DMG icon position for {name} is invalid.')


def image_size(app):
    # Retain dmgbuild's headroom and per-file allocation rounding, including
    # symlink metadata without following symlinked directories in the bundle.
    size = 128 * 1024 * 1024
    for directory, _, files in os.walk(app):
        for name in files:
            size += ((os.lstat(Path(directory) / name).st_size + 4095) // 4096) * 4096
    return f'{(size + 4096 + 999) // 1000}K'


def detach(target):
    last_error = None
    for attempt in range(5):
        try:
            run('/usr/bin/hdiutil', 'detach', target, '-quiet')
            return
        except subprocess.CalledProcessError as error:
            last_error = error
            if attempt < 4:
                time.sleep(1.5 ** attempt)
    # Force is cleanup only: never publish an image that failed clean detach.
    try:
        run('/usr/bin/hdiutil', 'detach', '-force', target, '-quiet')
    except subprocess.CalledProcessError as error:
        raise ImageCleanupError(f'Could not detach {target}; eject it manually before retrying.') from error
    raise last_error


def attached_device(image):
    # Attach can finish before its caller receives a signal or a usable plist.
    # Match the image identity, never detach by a possibly reused volume name.
    try:
        info = plistlib.loads(run('/usr/bin/hdiutil', 'info', '-plist', capture=True))
        for entry in info['images']:
            if Path(entry.get('image-path', '')).resolve() == Path(image).resolve():
                device = next((e['dev-entry'] for e in entry['system-entities'] if 'dev-entry' in e), None)
                if device is None:
                    raise ValueError('Attached image has no device.')
                return device
    except (OSError, ValueError, KeyError, TypeError, subprocess.CalledProcessError) as error:
        raise ImageCleanupError(f'Could not determine whether {image} is still attached; inspect hdiutil info before retrying.') from error
    return None


@contextmanager
def mounted_image(image, mount, readonly=False):
    mount = Path(mount).resolve()
    target = None
    try:
        arguments = ['-readonly'] if readonly else ['-owners', 'off']
        output = run('/usr/bin/hdiutil', 'attach', '-nobrowse', '-noautoopen',
                     '-mountpoint', mount, *arguments, '-plist', image, capture=True)
        # Keep the device even if the returned mount point is unexpected.
        entities = plistlib.loads(output)['system-entities']
        target = next((e['dev-entry'] for e in entities if 'dev-entry' in e), None)
        if target is None or not any(e.get('mount-point') == str(mount) for e in entities):
            raise ValueError(f'hdiutil did not mount the image at {mount}.')
        yield mount
    finally:
        if target is None:
            target = attached_device(image)
        if target is not None:
            detach(target)


def build(app, output, version, background):
    app, background = Path(app).resolve(), Path(background).resolve()
    # Resolve the parent, not the final component: reject dangling symlinks too.
    output = Path(output).absolute()
    output = output.parent.resolve() / output.name
    if not re.fullmatch(r'[0-9]+\.[0-9]+\.[0-9]+', version):
        raise ValueError('The version must be major.minor.patch.')
    if not app.is_dir() or not background.is_file():
        raise ValueError('An app bundle and background TIFF are required.')
    if output.suffix != '.dmg' or os.path.lexists(output):
        raise ValueError('The output must be a new .dmg file.')
    mount = Path('/Volumes') / f'v{version} Pharos'
    if os.path.lexists(mount):
        raise ValueError(f'A volume named {mount.name} is already mounted. Eject it before building this disk image.')
    run('/usr/bin/codesign', '--verify', '--strict', '--deep', app)
    with (app / 'Contents/Info.plist').open('rb') as source:
        if plistlib.load(source).get('CFBundleVersion') != version:
            raise ValueError('The app bundle has the wrong release version.')

    # Stage on the destination filesystem so publishing is an atomic hard link
    # that refuses to overwrite any output created while we were building.
    work = Path(tempfile.mkdtemp(prefix='.pharos-dmg-', dir=output.parent))
    try:
        writable, compressed = work / 'layout.dmg', work / 'release.dmg'
        run('/usr/bin/hdiutil', 'create', '-quiet', '-volname', mount.name,
            '-fs', 'HFS+', '-fsargs', '-c c=64,a=16,e=16', '-size', image_size(app), writable)
        with mounted_image(writable, mount) as contents:
            run('/usr/bin/ditto', app, contents / 'Pharos.app')
            os.symlink('/Applications', contents / 'Applications')
            shutil.copyfile(background, contents / '.background.tiff')
            write_layout(contents)
            verify_payload(contents, version)
            shutil.rmtree(contents / '.Trashes', ignore_errors=True)
            os.sync()
        run('/usr/bin/hdiutil', 'resize', '-quiet', '-sectors', 'min', writable)
        run('/usr/bin/hdiutil', 'convert', '-quiet', writable, '-format', 'ULMO',
            '-imagekey', 'lzma-level=9', '-o', compressed)
        run('/usr/bin/hdiutil', 'verify', '-quiet', compressed)
        # The writable volume has gone away. Recheck the actual final image at
        # a different path, so copying/conversion cannot silently drop content.
        with mounted_image(compressed, work / 'verify', readonly=True) as final_mount:
            verify_payload(final_mount, version)
        os.link(compressed, output)
    finally:
        if isinstance(sys.exc_info()[1], ImageCleanupError) or os.path.ismount(mount) or os.path.ismount(work / 'verify'):
            print(f'Pharos DMG: retained {work}; inspect its image attachments before removing it.', file=sys.stderr)
        else:
            shutil.rmtree(work)


def interrupted(signum, frame):
    raise SystemExit(128 + signum)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--check', action='store_true', help='check packaging prerequisites without building')
    parser.add_argument('inputs', nargs='*', metavar='APP OUTPUT.dmg VERSION BACKGROUND.tiff')
    args = parser.parse_args()
    if (args.check and args.inputs) or (not args.check and len(args.inputs) != 4):
        parser.error('supply --check, or APP OUTPUT.dmg VERSION BACKGROUND.tiff')
    for sig in (signal.SIGHUP, signal.SIGTERM):
        signal.signal(sig, interrupted)
    try:
        preflight()
        if not args.check:
            build(*args.inputs)
    except (ValueError, OSError, RuntimeError, KeyError, TypeError, subprocess.CalledProcessError) as error:
        print(f'Pharos DMG: {error}', file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    sys.exit(main())
