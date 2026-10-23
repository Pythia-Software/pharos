#!/usr/bin/env python3
"""Explicitly create an empty, locked release keychain; never moves keys."""
import argparse
import json
from pathlib import Path
import subprocess

from release_keychain import KEYCHAIN, search_list, security
from release_support import CONFIG, require_release_machine


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--create', action='store_true', required=True)
    parser.parse_args()
    require_release_machine(json.loads(CONFIG.read_text()))
    path = Path(KEYCHAIN)
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
