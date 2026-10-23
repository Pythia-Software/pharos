#!/usr/bin/env python3
"""Run ON THE DEPLOYMENT MAC ONLY, once, to enable release signing.

Creates the Sparkle update key there if no public key has been committed yet.
Only the public key is written to the checkout. Apple credentials remain under
the operator's existing Developer ID / notarytool setup.
"""
import argparse
import json
import os
import subprocess
import tempfile

from release_support import CONFIG, MACHINE_FILE, ROOT, validate_public_key


def atomic_json(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    descriptor, temporary = tempfile.mkstemp(dir=path.parent)
    try:
        with os.fdopen(descriptor, 'w') as output:
            json.dump(value, output, indent=2)
            output.write('\n')
        os.replace(temporary, path)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--enable', action='store_true', required=True,
                        help='Explicitly designate THIS Mac as the deployment/signing machine')
    parser.parse_args()
    config = json.loads(CONFIG.read_text())
    distribution = subprocess.check_output([str(ROOT / 'macos/fetch-sparkle.sh')], text=True).strip()
    tool = distribution + '/bin/generate_keys'
    if config.get('public_key') is None:
        # Only this explicit deployment setup is allowed to create a real key.
        subprocess.run([tool, '--account', config['keychain_account']], check=True)
    public = subprocess.check_output([tool, '--account', config['keychain_account'], '-p'], text=True).strip()
    validate_public_key(public)
    if config.get('public_key') not in (None, public):
        raise ValueError('This Mac has a different Sparkle key. Restore the existing release key on this deployment Mac; never replace the committed public key for an installed population.')
    config['public_key'] = public
    atomic_json(CONFIG, config)
    atomic_json(MACHINE_FILE, dict(enabled=True, public_key=public))
    print('Deployment machine enabled. Back up its Sparkle key securely on the deployment side.')
    print('Commit macos/sparkle.json and transfer only that public configuration to development Macs.')


if __name__ == '__main__':
    try:
        main()
    except (ValueError, OSError, subprocess.CalledProcessError) as error:
        raise SystemExit(str(error))
