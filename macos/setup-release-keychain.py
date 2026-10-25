#!/usr/bin/env python3
"""Explicit release Keychain setup; never creates or imports signing keys."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile

from release_keychain import KEYCHAIN, SESSION, search_list, security, sparkle_access_command, with_keychain
from release_support import CONFIG, require_release_machine


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    actions = parser.add_mutually_exclusive_group(required=True)
    actions.add_argument('--create', action='store_true')
    actions.add_argument('--authorize-sparkle', action='store_true',
                         help='Authorize only the pinned Sparkle tools for the existing update key')
    actions.add_argument('--configure-notary', action='store_true',
                         help='Interactively store Apple credentials in the release keychain, then rehearse deployment access')
    args = parser.parse_args()
    config = json.loads(CONFIG.read_text())
    require_release_machine(config)
    path = Path(os.environ.get('PHAROS_RELEASE_KEYCHAIN', KEYCHAIN)).expanduser().resolve()
    if args.configure_notary:
        if os.environ.get(SESSION) != str(path):
            result = with_keychain([sys.executable, str(Path(__file__).resolve()), '--configure-notary'])
        else:
            # Run this in the operator's Terminal. Apple's tool owns its input;
            # Python never reads credentials or places them in arguments/env/logs.
            subprocess.run(['xcrun', 'notarytool', 'store-credentials',
                            os.environ.get('PHAROS_NOTARY_PROFILE', 'pharos'),
                            '--keychain', str(path)], check=True)
            result = subprocess.call([sys.executable, str(CONFIG.parent / 'check-release-credentials.py')])
        if result:
            raise ValueError('Notarization setup or credential rehearsal failed.')
        return
    if args.authorize_sparkle:
        if not sys.stdin.isatty():
            raise ValueError('Run --authorize-sparkle in your own Terminal: security(1) needs a secure password prompt. Do not send passwords through agent chat.')
        if os.environ.get(SESSION) != str(path):
            result = with_keychain([sys.executable, str(Path(__file__).resolve()), '--authorize-sparkle'])
            if result:
                raise ValueError('Sparkle authorization failed; no release was started.')
            return
        tools = Path(subprocess.check_output(
            [str(CONFIG.parent / 'fetch-sparkle.sh')], text=True).strip()) / 'bin'
        with tempfile.TemporaryDirectory(prefix='pharos-authorize-sparkle-') as directory:
            command = sparkle_access_command(directory, 'authorize', path, tools,
                                             config['keychain_account'])
            subprocess.run(command, check=True)
            command[1] = 'partitions'
            partitions = subprocess.check_output(command, text=True).strip()
            print('Enter the release-keychain password at the security tool prompt to authorize the exact Sparkle code hashes.', flush=True)
            # Omitting -k makes Apple's tool read securely from the operator's
            # Terminal. The password never enters Python, argv or environment.
            subprocess.run(['/usr/bin/security', 'set-generic-password-partition-list',
                            '-a', config['keychain_account'], '-s', 'https://sparkle-project.org',
                            '-S', partitions, str(path)], check=True, stdout=subprocess.DEVNULL)
            command[1] = 'check'
            subprocess.run(command, check=True)
        return
    if path.exists():
        raise ValueError(f'{path} already exists; inspect it in Keychain Access. Nothing was replaced.')
    original = search_list()
    try:
        # SecurityAgent owns the password prompt. No password enters Python,
        # shell history, environment variables, command arguments or log output.
        security('create-keychain', '-P', path)
        security('set-keychain-settings', '-l', '-u', '-t', '14400', path)
    finally:
        try:
            if path.exists():
                security('lock-keychain', path)
        finally:
            security('list-keychains', '-d', 'user', '-s', *original)
    print(f'Created locked keychain: {path}')
    print('Complete the one-time key migration in docs/releases-and-updates.md before publishing.')


if __name__ == '__main__':
    try:
        main()
    except (ValueError, OSError, subprocess.CalledProcessError) as error:
        raise SystemExit(str(error))
