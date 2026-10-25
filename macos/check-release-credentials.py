#!/usr/bin/env python3
"""Exercise deployment credentials in one unlock without publishing a release.

Signs only disposable files. Passwords and private keys stay in Keychain.
Apple history and GitHub permissions are read-only network requests.
"""
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile

from release_keychain import KEYCHAIN, SESSION, with_keychain
from release_support import CONFIG, require_release_machine

ROOT = Path(__file__).resolve().parents[1]


def run(*args):
    return subprocess.check_output([str(arg) for arg in args], text=True).strip()


def load_feed():
    import importlib.util
    spec = importlib.util.spec_from_file_location('release_feed_check', ROOT / 'macos/update-feed.py')
    feed = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(feed)
    return feed


def exercise():
    feed = load_feed()
    print('Checking Sparkle ACLs, partitions, and public-key access…', flush=True)
    config, tools = feed.config_and_tools()
    keychain = os.environ['PHAROS_RELEASE_KEYCHAIN']
    identity = os.environ.get('PHAROS_CODESIGN_IDENTITY', 'Developer ID Application: Pythia Software LLC')
    profile = os.environ.get('PHAROS_NOTARY_PROFILE', 'pharos')
    print('Sparkle public-key lookup and dedicated-keychain authorization passed.', flush=True)
    with tempfile.TemporaryDirectory(prefix='pharos-release-proof-') as directory:
        directory = Path(directory)
        executable = directory / 'codesign-proof'
        # Copy a Mach-O solely to exercise private-key signing, never execute it.
        shutil.copyfile('/usr/bin/true', executable)
        payload = directory / 'update-proof'
        payload.write_text('Disposable Pharos deployment credential check.\n')
        for attempt in (1, 2):
            print(f'Pass {attempt}: requesting Developer ID signature…', flush=True)
            subprocess.run(['codesign', '--force', '--sign', identity, '--keychain', keychain,
                            '--options', 'runtime', '--timestamp', str(executable)], check=True,
                           stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
            subprocess.run(['codesign', '--verify', '--strict', str(executable)], check=True,
                           stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
            details = subprocess.run(['codesign', '-dvv', str(executable)], check=True,
                                     text=True, capture_output=True).stderr
            if 'Authority=Developer ID Application:' not in details or 'Timestamp=' not in details:
                raise ValueError('The disposable code signature lacks Developer ID or a secure timestamp.')
            print(f'Pass {attempt}: requesting Sparkle signature…', flush=True)
            signature = run(tools / 'sign_update', '--account', config['keychain_account'], '-p', payload)
            feed.verify_signature(payload, config, signature)
            print(f'Pass {attempt}: Developer ID and Sparkle signatures created and verified.', flush=True)
        # Explicit selection proves this does not fall back to login credentials.
        print('Authenticating to Apple notarization…', flush=True)
        history = json.loads(run('xcrun', 'notarytool', 'history', '--keychain-profile', profile,
                                 '--keychain', keychain, '--output-format', 'json'))
        if history.get('message') and 'success' not in history['message'].lower():
            raise ValueError('Apple notarization history did not report success.')
        print('Apple notarization authentication passed using only the dedicated keychain.', flush=True)
        print('Checking GitHub authentication and permissions…', flush=True)
        permissions = json.loads(run('gh', 'api', 'repos/Pythia-Software/pharos', '--jq', '.permissions'))
        if not (permissions.get('push') or permissions.get('admin')):
            raise ValueError('GitHub credentials lack repository write permission.')
        # Filter inside gh so Python never receives token-bearing account data.
        sources = json.loads(run('gh', 'auth', 'status', '--json', 'hosts', '--jq',
                                 '[.hosts["github.com"][] | select(.active) | .tokenSource]'))
        print('GitHub authentication and repository write permission passed. Credential source: '
              + ', '.join(sources or ['not reported']) + '.', flush=True)
    print('Credential operations passed; no release or update feed was modified.', flush=True)
    print('This does not count macOS dialogs. One-unlock acceptance requires no password prompts after the initial unlock.', flush=True)


def main():
    require_release_machine(json.loads(CONFIG.read_text()))
    keychain = Path(os.environ.get('PHAROS_RELEASE_KEYCHAIN', KEYCHAIN)).expanduser().resolve()
    if os.environ.get(SESSION) != str(keychain):
        return with_keychain([sys.executable, str(Path(__file__).resolve())])
    exercise()
    return 0


if __name__ == '__main__':
    try:
        sys.exit(main())
    except (ValueError, OSError, subprocess.CalledProcessError) as error:
        sys.exit(f'Credential rehearsal failed: {error}')
