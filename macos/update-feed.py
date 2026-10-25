#!/usr/bin/env python3
"""Build, verify and publish Pharos's signed Sparkle feed; credentials stay in Keychain.

prepare creates immutable per-release update metadata from the final stapled DMG.
publish verifies the public release before advertising it. bootstrap creates an
empty signed feed. withdraw stops offering one version without deleting artifacts.
"""
import argparse
import base64
from datetime import datetime, timezone
from email.utils import format_datetime
import hashlib
import json
import os
from pathlib import Path
import plistlib
import re
import subprocess
import sys
import tempfile
import xml.etree.ElementTree as ET

from release_support import require_release_machine
from release_keychain import check_sparkle_access

ROOT = Path(__file__).resolve().parents[1]
REPO = 'Pythia-Software/pharos'
FEED_TAG = 'update-feed'
SPARKLE = 'http://www.andymatuschak.org/xml-namespaces/sparkle'
ET.register_namespace('sparkle', SPARKLE)


def run(*args):
    return subprocess.check_output([str(arg) for arg in args], text=True).strip()


def config_and_tools():
    config = json.loads((ROOT / 'macos/sparkle.json').read_text())
    require_release_machine(config)
    tools = Path(run(ROOT / 'macos/fetch-sparkle.sh')) / 'bin'
    check_sparkle_access(tools, config['keychain_account'])
    public = run(tools / 'generate_keys', '--account', config['keychain_account'], '-p')
    if public != config['public_key']:
        raise ValueError('Release blocked: the Keychain signing key does not match macos/sparkle.json. Report this mismatch; do not create or import keys during a release task.')
    return config, tools


def version(text):
    if not re.fullmatch(r'\d+\.\d+\.\d+', text):
        raise ValueError('Version must be major.minor.patch')
    return tuple(map(int, text.split('.')))


def image_name(number):
    version(number)
    return f'v{number}-Pharos-macos-universal.dmg'


def image_url(number):
    return f'https://github.com/{REPO}/releases/download/v{number}/{image_name(number)}'


def file_sha256(path):
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def system_version(text):
    if not isinstance(text, str) or not re.fullmatch(r'[0-9]+(?:\.[0-9]+){0,2}', text):
        raise ValueError('Invalid minimum macOS version')
    parts = [str(int(part)) for part in text.split('.')]
    return '.'.join(parts + ['0'] * (3 - len(parts)))


def image_bundle_metadata(image, number):
    """Inspect the final artifact, never an adjacent/stale build directory."""
    with tempfile.TemporaryDirectory(prefix='pharos-update-mount-') as temporary:
        mount = Path(temporary)
        run('hdiutil', 'attach', '-readonly', '-nobrowse', '-mountpoint', mount, image)
        try:
            with (mount / 'Pharos.app/Contents/Info.plist').open('rb') as source:
                info = plistlib.load(source)
            if info.get('CFBundleVersion') != number:
                raise ValueError('The app inside the DMG does not match the release version')
            return system_version(info.get('LSMinimumSystemVersion'))
        finally:
            run('hdiutil', 'detach', mount)


def validate_manifest(manifest):
    number = manifest['version']
    version(number)
    if manifest['url'] != image_url(number):
        raise ValueError('Unexpected update download URL')
    if type(manifest['length']) is not int or manifest['length'] <= 0:
        raise ValueError('Invalid update size')
    if len(base64.b64decode(manifest['signature'], validate=True)) != 64:
        raise ValueError('Invalid archive signature')
    if not re.fullmatch(r'[a-f0-9]{64}', manifest['sha256']):
        raise ValueError('Invalid archive checksum')
    if system_version(manifest['minimum_system_version']) != manifest['minimum_system_version']:
        raise ValueError('Minimum macOS version must have three numeric components')
    return manifest


def add_item(channel, manifest):
    validate_manifest(manifest)
    number = manifest['version']
    existing = {item.findtext(f'{{{SPARKLE}}}version') for item in channel.findall('item')}
    if number in existing:
        raise ValueError(f'{number} is already in the feed; published versions are immutable')
    if any(version(number) <= version(old) for old in existing):
        raise ValueError('The new update must be newer than every advertised version')
    item = ET.Element('item')
    ET.SubElement(item, 'title').text = f'Pharos {number}'
    ET.SubElement(item, f'{{{SPARKLE}}}version').text = number
    ET.SubElement(item, f'{{{SPARKLE}}}shortVersionString').text = number
    ET.SubElement(item, f'{{{SPARKLE}}}minimumSystemVersion').text = manifest['minimum_system_version']
    ET.SubElement(item, 'pubDate').text = manifest['published_at']
    # Plain text is escaped as HTML, with no remote content or script execution.
    import html
    ET.SubElement(item, 'description').text = '<pre>' + html.escape(manifest['notes']) + '</pre>'
    ET.SubElement(item, 'enclosure', url=manifest['url'], length=str(manifest['length']),
                  type='application/octet-stream', **{f'{{{SPARKLE}}}edSignature': manifest['signature']})
    channel.insert(0, item)


def empty_feed():
    root = ET.Element('rss', version='2.0')
    channel = ET.SubElement(root, 'channel')
    ET.SubElement(channel, 'title').text = 'Pharos Updates'
    ET.SubElement(channel, 'link').text = f'https://github.com/{REPO}'
    ET.SubElement(channel, 'description').text = 'Signed, notarized Pharos releases for macOS.'
    return ET.ElementTree(root)


def load_feed(path):
    tree = ET.parse(path)
    if tree.getroot().tag != 'rss' or tree.find('channel') is None:
        raise ValueError('Invalid appcast')
    seen = set()
    for item in tree.findall('channel/item'):
        number = item.findtext(f'{{{SPARKLE}}}version')
        version(number)
        enclosure = item.find('enclosure')
        if number in seen or enclosure is None or enclosure.get('url') != image_url(number):
            raise ValueError('Invalid or duplicate appcast item')
        seen.add(number)
        if len(base64.b64decode(enclosure.get(f'{{{SPARKLE}}}edSignature', ''), validate=True)) != 64:
            raise ValueError('Unsigned appcast archive')
    return tree


def sign_feed(tree, path, config, tools):
    ET.indent(tree, space='  ')
    tree.write(path, encoding='utf-8', xml_declaration=True)
    run(tools / 'sign_update', '--account', config['keychain_account'], path)
    verify_signature(path, config)


def verify_signature(path, config, signature=None):
    args = [ROOT / 'macos/verify-update.sh', config['public_key'], path]
    if signature is not None:
        args.append(signature)
    run(*args)


def current_feed(directory, config, tools):
    path = directory / 'appcast.xml'
    run('gh', 'release', 'download', FEED_TAG, '-R', REPO, '--pattern', 'appcast.xml', '--dir', directory)
    verify_signature(path, config)
    return load_feed(path)


def prepare(args, config, tools):
    number = args.version
    image = args.directory / image_name(number)
    minimum = image_bundle_metadata(image, number)
    signature = run(tools / 'sign_update', '--account', config['keychain_account'], '-p', image)
    verify_signature(image, config, signature)
    release = json.loads(run('gh', 'release', 'view', f'v{number}', '-R', REPO, '--json', 'body'))
    manifest = validate_manifest(dict(version=number, url=image_url(number), length=image.stat().st_size,
        signature=signature, sha256=file_sha256(image),
        minimum_system_version=minimum, notes=release['body'], published_at=format_datetime(datetime.now(timezone.utc))))
    path = args.directory / 'update.json'
    # Never overwrite already prepared release metadata on recovery.
    with path.open('x') as output:
        json.dump(manifest, output, indent=2)
        output.write('\n')
    print(path)


def publish(args, config, tools):
    number = args.version
    release = json.loads(run('gh', 'release', 'view', f'v{number}', '-R', REPO, '--json', 'isDraft,isPrerelease'))
    if release['isDraft'] or release['isPrerelease']:
        raise ValueError('Only a published stable release may enter the production feed')
    with tempfile.TemporaryDirectory(prefix='pharos-update-') as temporary:
        directory = Path(temporary)
        run('gh', 'release', 'download', f'v{number}', '-R', REPO, '--pattern', 'update.json', '--pattern', image_name(number), '--dir', directory)
        manifest = validate_manifest(json.loads((directory / 'update.json').read_text()))
        if manifest['version'] != number:
            raise ValueError('Release metadata version does not match its tag')
        image = directory / image_name(number)
        if image.stat().st_size != manifest['length'] or file_sha256(image) != manifest['sha256']:
            raise ValueError('Published update bytes do not match their release metadata')
        verify_signature(image, config, manifest['signature'])
        signing = subprocess.run(['codesign', '-dvv', str(image)], capture_output=True, text=True, check=True)
        if 'Authority=Developer ID Application:' not in signing.stderr:
            raise ValueError('The production update DMG must be Developer ID signed')
        run('codesign', '--verify', '--verbose=2', image)
        run('xcrun', 'stapler', 'validate', image)
        if image_bundle_metadata(image, number) != manifest['minimum_system_version']:
            raise ValueError('Published compatibility metadata differs from the app inside the DMG')
        tree = current_feed(directory, config, tools)
        channel = tree.find('channel')
        for item in channel.findall('item'):
            if item.findtext(f'{{{SPARKLE}}}version') == number:
                enclosure = item.find('enclosure')
                if enclosure.get(f'{{{SPARKLE}}}edSignature') == manifest['signature'] and enclosure.get('length') == str(manifest['length']):
                    print(f'Pharos {number} is already advertised.')
                    return
                raise ValueError('This version is already advertised with different bytes')
        add_item(channel, manifest)
        path = directory / 'appcast.xml'
        sign_feed(tree, path, config, tools)
        # Retain the exact signed feed locally for recovery before advertising it.
        args.directory.mkdir(parents=True, exist_ok=True)
        (args.directory / 'appcast.xml').write_bytes(path.read_bytes())
        upload_feed(path)
    print(config['feed_url'])


def upload_feed(path):
    # GitHub has no atomic asset replacement. A brief 404 fails closed in
    # Sparkle; installed apps continue working. Always upload the whole signed
    # file and read it back, never report success on an uncertain write.
    run('gh', 'release', 'upload', FEED_TAG, path, '-R', REPO, '--clobber')
    with tempfile.TemporaryDirectory(prefix='pharos-feed-verify-') as temporary:
        run('gh', 'release', 'download', FEED_TAG, '-R', REPO, '--pattern', 'appcast.xml', '--dir', temporary)
        if (Path(temporary) / 'appcast.xml').read_bytes() != path.read_bytes():
            raise ValueError('Published appcast readback differs; inspect the update-feed release before retrying')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['bootstrap', 'prepare', 'publish', 'withdraw'])
    parser.add_argument('version', nargs='?')
    parser.add_argument('--directory', type=Path, default=ROOT / 'dist/updates')
    args = parser.parse_args()
    if args.action != 'bootstrap':
        version(args.version or '')
    from release_keychain import SESSION, with_keychain
    if not os.environ.get(SESSION):
        require_release_machine(json.loads((ROOT / 'macos/sparkle.json').read_text()))
        return with_keychain([sys.executable, str(Path(__file__).resolve()), *sys.argv[1:]])
    config, tools = config_and_tools()
    # Release operations on this Mac serialize. Publishers on other Macs must
    # coordinate; the GitHub asset API has no conditional replacement operation.
    import fcntl
    lockdir = ROOT / 'build'
    lockdir.mkdir(exist_ok=True)
    with (lockdir / 'update-feed.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        if args.action == 'prepare':
            prepare(args, config, tools)
        elif args.action == 'publish':
            publish(args, config, tools)
        else:
            with tempfile.TemporaryDirectory(prefix='pharos-feed-') as temporary:
                directory = Path(temporary)
                if args.action == 'bootstrap':
                    result = subprocess.run(['gh', 'release', 'view', FEED_TAG, '-R', REPO], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
                    if result.returncode == 0:
                        current_feed(directory, config, tools)
                        print('Existing signed feed verified; left unchanged.')
                        return
                    tree = empty_feed()
                else:
                    tree = current_feed(directory, config, tools)
                    channel = tree.find('channel')
                    matches = [item for item in channel.findall('item') if item.findtext(f'{{{SPARKLE}}}version') == args.version]
                    if not matches:
                        raise ValueError('That version is not in the update feed')
                    for item in matches:
                        channel.remove(item)
                path = directory / 'appcast.xml'
                sign_feed(tree, path, config, tools)
                if args.action == 'bootstrap':
                    run('gh', 'release', 'create', FEED_TAG, path, '-R', REPO, '--target', 'master', '--prerelease', '--latest=false',
                        '--title', 'Pharos update feed', '--notes', 'Signed Sparkle update metadata. Install Pharos from a versioned stable release. This feed is updated after release verification.')
                else:
                    upload_feed(path)
                print(config['feed_url'])


if __name__ == '__main__':
    try:
        raise SystemExit(main())
    except (ValueError, OSError, subprocess.CalledProcessError) as error:
        raise SystemExit(f'Update feed: {error}')
